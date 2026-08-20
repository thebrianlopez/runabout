package chainindex

import (
	"fmt"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

// orphanThreshold: artifacts created before this date are excluded from the
// orphan list by default (pre-chain legacy artifacts).
const orphanThreshold = "20260421T000000Z"

// gateTimestampLayout is the compact UTC form required by #Timestamp in
// core/schemas/cue/workspace.cue (YYYYMMDDTHHMMSSZ).
const gateTimestampLayout = "20060102T150405Z"

// nowFunc is injectable so gate timestamps are deterministic in tests.
var nowFunc = func() time.Time { return time.Now().UTC() }

// BuildOption customizes a Build call.
type BuildOption func(*buildOptions)

type buildOptions struct {
	now time.Time
}

// WithNow pins the build instant so gate timestamps share a single clock read
// with the index's indexed_at field.
func WithNow(t time.Time) BuildOption {
	return func(o *buildOptions) { o.now = t.UTC() }
}

// Build constructs a ChainIndex from a flat ArtifactRecord slice.
// Chain key = FDD filename stem normalized (lowercase, underscores, prefix stripped).
// Pass includeLegacy=true to include pre-orphanThreshold artifacts in the orphan list.
func Build(records []ArtifactRecord, docsRoot string, includeLegacy bool, opts ...BuildOption) ChainIndex {
	cfg := buildOptions{now: nowFunc()}
	for _, opt := range opts {
		opt(&cfg)
	}
	idx := ChainIndex{
		SchemaVersion:    "1.0",
		DocsRoot:         docsRoot,
		Artifacts:        records,
		Chains:           make(map[string]ChainEntry),
		Orphans:          []string{},
		GateRecords:      []ChainGateRecord{},
		WorkspaceLinks:   []WorkspaceChainLink{},
		SchemaViolations: []SchemaViolation{},
	}
	if len(records) == 0 {
		idx.Artifacts = []ArtifactRecord{}
		warnGateRecordsEmpty()
		return idx
	}

	// Pass 1: Build FDD-keyed chain entries.
	chainKeyByFDDPath := map[string]string{}
	for _, r := range records {
		if r.Type != ArtifactFDD {
			continue
		}
		key := normalizeChainKey(filepath.Base(r.Path))
		if _, exists := idx.Chains[key]; exists {
			// Collision: suffix with FDD path stem to disambiguate.
			key = key + "_" + strings.ToLower(strings.ReplaceAll(filepath.Base(r.Path), ".md", ""))
		}
		node := ChainNode{Path: r.Path, Status: r.Status}
		idx.Chains[key] = ChainEntry{
			FDD:         &node,
			TDDs:        []ChainNode{},
			Epics:       []ChainNode{},
			POMOs:       []ChainNode{},
			Sidecars:    []ChainNode{},
			GateRecords: []ChainGateRecord{},
		}
		chainKeyByFDDPath[r.Path] = key
	}

	// Pass 1.5: Link each chain's root PRD via the FDD's Source PRD reference.
	prds := make([]ArtifactRecord, 0, len(records))
	for _, r := range records {
		if r.Type == ArtifactPRD {
			prds = append(prds, r)
		}
	}
	sort.Slice(prds, func(i, j int) bool { return prds[i].Path < prds[j].Path })
	for _, r := range records {
		if r.Type != ArtifactFDD || r.UpstreamField == "" {
			continue
		}
		key, ok := chainKeyByFDDPath[r.Path]
		if !ok {
			continue
		}
		for _, p := range prds {
			if !upstreamMatches(r.UpstreamField, p.Path) {
				continue
			}
			entry := idx.Chains[key]
			node := ChainNode{Path: p.Path, Status: p.Status, FeatureID: p.FeatureID}
			entry.PRD = &node
			idx.Chains[key] = entry
			break
		}
	}

	// Pass 2: Assign TDDs, Epics, Releases, POMOs and Sidecars to chains via
	// UpstreamField. UpstreamField holds the most specific upstream reference
	// (TDD > FDD > PRD).
	assigned := map[string]bool{}
	for _, r := range records {
		if r.Type == ArtifactFDD || r.Type == ArtifactPRD {
			assigned[r.Path] = true
			continue
		}
		for chainKey, entry := range idx.Chains {
			if entry.FDD == nil {
				continue
			}
			if !upstreamMatches(r.UpstreamField, entry.FDD.Path) {
				continue
			}
			node := ChainNode{Path: r.Path, Status: r.Status, FeatureID: r.FeatureID}
			switch r.Type {
			case ArtifactTDD:
				entry.TDDs = append(entry.TDDs, node)
			case ArtifactEpic:
				entry.Epics = append(entry.Epics, node)
			case ArtifactRelease:
				entry.Release = &node
			case ArtifactPOMO:
				entry.POMOs = append(entry.POMOs, node)
			case ArtifactSidecar:
				entry.Sidecars = append(entry.Sidecars, node)
			}
			idx.Chains[chainKey] = entry
			assigned[r.Path] = true
			break
		}
	}

	// Pass 2.5: Evaluate upstream_field gates over every gateable artifact and
	// attach each record to its owning chain. Runs after assignment so every
	// artifact's outcome is known, and before orphan collection.
	idx.GateRecords = EvaluateUpstreamGates(records, idx.Chains, assigned, includeLegacy, cfg.now.UTC().Format(gateTimestampLayout))
	chainKeyByArtifact := map[string]string{}
	for chainKey, entry := range idx.Chains {
		for _, group := range [][]ChainNode{entry.TDDs, entry.Epics, entry.POMOs, entry.Sidecars} {
			for _, n := range group {
				chainKeyByArtifact[n.Path] = chainKey
			}
		}
		if entry.Release != nil {
			chainKeyByArtifact[entry.Release.Path] = chainKey
		}
	}
	for _, rec := range idx.GateRecords {
		chainKey, ok := chainKeyByArtifact[rec.ArtifactPath]
		if !ok {
			continue // unsatisfied records belong to no chain
		}
		entry := idx.Chains[chainKey]
		entry.GateRecords = append(entry.GateRecords, rec)
		idx.Chains[chainKey] = entry
	}
	if len(idx.GateRecords) == 0 {
		warnGateRecordsEmpty()
	}

	// Pass 3: Collect orphans.
	for _, r := range records {
		if assigned[r.Path] {
			continue
		}
		if r.Type == ArtifactPRD || r.Type == ArtifactFDD || r.Type == ArtifactPOMO {
			continue
		}
		if !includeLegacy && r.CreatedAt < orphanThreshold {
			idx.LegacyExcludedCount++
			continue
		}
		idx.Orphans = append(idx.Orphans, r.Path)
	}
	sort.Strings(idx.Orphans)

	return idx
}

// warnGateRecordsEmpty reports the silence that hid the F6 regression for 77
// days: absence of gate records must never be indistinguishable from a clean
// result. Warning-class only - the indexer stays fail-open.
func warnGateRecordsEmpty() {
	fmt.Fprintf(gateStderr, "chain-eval index: WARN: gate_records_empty - 0 gate records produced\n")
}

// normalizeChainKey converts an FDD filename to a normalized chain key.
// "PERSONAL_20260101T000000Z_Linkari_Share_Dedup_FDD.md" → "linkari_share_dedup"
func normalizeChainKey(basename string) string {
	// Strip .md suffix.
	s := strings.TrimSuffix(basename, ".md")
	// Strip _FDD suffix.
	s = strings.TrimSuffix(s, "_FDD")
	// Strip org+timestamp prefix: [A-Z]+_\d{8}T\d{6}Z_
	s = regexp.MustCompile(`^[A-Z]+_\d{8}T\d{6}Z_`).ReplaceAllString(s, "")
	// Lowercase.
	return strings.ToLower(s)
}

// upstreamMatches checks whether the artifact's upstream field references the
// given FDD path (basename match, case-insensitive).
func upstreamMatches(upstreamField, fddPath string) bool {
	if upstreamField == "" {
		return false
	}
	fddBase := strings.ToLower(filepath.Base(fddPath))
	upBase := strings.ToLower(filepath.Base(upstreamField))
	return fddBase == upBase || strings.Contains(fddBase, strings.TrimSuffix(upBase, ".md"))
}
