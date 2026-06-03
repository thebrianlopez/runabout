package chainindex

import (
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// orphanThreshold: artifacts created before this date are excluded from the
// orphan list by default (pre-chain legacy artifacts).
const orphanThreshold = "20260421T000000Z"

// Build constructs a ChainIndex from a flat ArtifactRecord slice.
// Chain key = FDD filename stem normalized (lowercase, underscores, prefix stripped).
// Pass includeLegacy=true to include pre-orphanThreshold artifacts in the orphan list.
func Build(records []ArtifactRecord, docsRoot string, includeLegacy bool) ChainIndex {
	idx := ChainIndex{
		SchemaVersion:  "1.0",
		DocsRoot:       docsRoot,
		Artifacts:      records,
		Chains:         make(map[string]ChainEntry),
		Orphans:        []string{},
		GateRecords:    []ChainGateRecord{},
		WorkspaceLinks: []WorkspaceChainLink{},
	}
	if len(records) == 0 {
		idx.Artifacts = []ArtifactRecord{}
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

	// Pass 2: Assign TDDs, Epics, Releases, Sidecars to chains via UpstreamField.
	// UpstreamField holds the most specific upstream reference (TDD > FDD > PRD).
	assigned := map[string]bool{}
	for _, r := range records {
		if r.Type == ArtifactFDD || r.Type == ArtifactPRD || r.Type == ArtifactPOMO {
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
			case ArtifactSidecar:
				entry.Sidecars = append(entry.Sidecars, node)
			}
			idx.Chains[chainKey] = entry
			assigned[r.Path] = true
			break
		}
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
