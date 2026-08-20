package chainindex

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// GateTypeUpstreamField is the only gate type produced by the indexer (F6).
// The remaining types declared in chain_gate.cue (subprocess_contract,
// config_authority, design_contract, cross_chain_prereq) are deferred.
const GateTypeUpstreamField = "upstream_field"

// Gate record status values. "waived" is intentionally absent: waivers are an
// operator act requiring waive_reason, and the deterministic indexer has no
// waiver input.
const (
	GateStatusSatisfied   = "satisfied"
	GateStatusUnsatisfied = "unsatisfied"
)

// maxGateIDCollisions bounds disambiguation suffixing for one normalized stem.
const maxGateIDCollisions = 99

// gateStderr receives gate warnings; overridden in tests.
var gateStderr io.Writer = os.Stderr

var (
	// gateStemDisallowedRe matches any run of characters outside the schema charset.
	gateStemDisallowedRe = regexp.MustCompile(`[^a-z0-9_-]+`)
	// gateStemDashRunRe collapses repeated dashes.
	gateStemDashRunRe = regexp.MustCompile(`-{2,}`)
	// gateTypeRe mirrors the gate-type segment of chain_gate.cue's gate_id regex.
	gateTypeRe = regexp.MustCompile(`^[a-z_]+$`)
)

// gateableTypes are the artifact types that carry an upstream obligation.
// PRD is a chain root by definition, FDD's PRD link is a separate gate type,
// and Sidecars are advisory-only and never gate a chain.
var gateableTypes = map[ArtifactType]bool{
	ArtifactTDD:     true,
	ArtifactEpic:    true,
	ArtifactRelease: true,
	ArtifactPOMO:    true,
}

// GateID builds a schema-conformant gate identifier of the form
// "{normalized-artifact-stem}:{gate_type}". It returns an error when the
// artifact stem cannot be normalized to the required charset or the gate type
// violates the schema's [a-z_]+ segment.
func GateID(artifactPath string, gateType string) (string, error) {
	if !gateTypeRe.MatchString(gateType) {
		return "", fmt.Errorf("gate_id_unnormalizable: invalid gate_type %q", gateType)
	}
	stem := normalizeGateStem(artifactPath)
	if stem == "" {
		return "", fmt.Errorf("gate_id_unnormalizable: %q normalizes to empty stem", artifactPath)
	}
	return stem + ":" + gateType, nil
}

// normalizeGateStem lowercases the artifact basename, strips the .md extension,
// replaces every character outside [a-z0-9_-] with "-", collapses dash runs and
// trims leading/trailing dashes.
func normalizeGateStem(artifactPath string) string {
	s := strings.ToLower(filepath.Base(artifactPath))
	s = strings.TrimSuffix(s, ".md")
	s = gateStemDisallowedRe.ReplaceAllString(s, "-")
	s = gateStemDashRunRe.ReplaceAllString(s, "-")
	return strings.Trim(s, "-")
}

// EvaluateUpstreamGates emits one ChainGateRecord per gateable artifact.
//
// Pure: no I/O beyond warning output, no clock read - nowUTC is injected so
// builds are reproducible in tests.
//
// An artifact assigned to a chain in Pass 2 produces a "satisfied" record
// carrying the resolved upstream path; an unassigned post-threshold artifact
// produces an "unsatisfied" record. Legacy (pre-orphanThreshold) artifacts are
// excluded unless includeLegacy is set, mirroring orphan detection: artifacts
// predating the chain workflow cannot be judged against it.
func EvaluateUpstreamGates(
	records []ArtifactRecord,
	chains map[string]ChainEntry,
	assigned map[string]bool,
	includeLegacy bool,
	nowUTC string,
) []ChainGateRecord {
	upstreamByPath := upstreamPathIndex(chains)

	sorted := make([]ArtifactRecord, 0, len(records))
	for _, r := range records {
		if !gateableTypes[r.Type] {
			continue
		}
		if !includeLegacy && r.CreatedAt < orphanThreshold {
			continue
		}
		sorted = append(sorted, r)
	}
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Path < sorted[j].Path })

	out := []ChainGateRecord{}
	used := map[string]bool{}
	for _, r := range sorted {
		gateID, err := GateID(r.Path, GateTypeUpstreamField)
		if err != nil {
			fmt.Fprintf(gateStderr, "chain-eval index: WARN: %v - gate record skipped\n", err)
			continue
		}
		gateID, err = disambiguateGateID(gateID, used)
		if err != nil {
			fmt.Fprintf(gateStderr, "chain-eval index: WARN: %v - gate record skipped\n", err)
			continue
		}
		used[gateID] = true

		rec := ChainGateRecord{
			GateID:       gateID,
			GateType:     GateTypeUpstreamField,
			ArtifactPath: r.Path,
			Status:       GateStatusUnsatisfied,
		}
		if assigned[r.Path] {
			rec.Status = GateStatusSatisfied
			rec.UpstreamArtifact = upstreamByPath[r.Path]
			rec.SatisfiedAt = nowUTC
		}
		out = append(out, rec)
	}
	return out
}

// disambiguateGateID appends -2, -3, ... to the stem segment (never the
// gate-type segment, which the schema restricts to [a-z_]+) until the id is
// unique within one build.
func disambiguateGateID(gateID string, used map[string]bool) (string, error) {
	if !used[gateID] {
		return gateID, nil
	}
	sep := strings.LastIndex(gateID, ":")
	stem, gateType := gateID[:sep], gateID[sep+1:]
	for n := 2; n <= maxGateIDCollisions+1; n++ {
		candidate := fmt.Sprintf("%s-%d:%s", stem, n, gateType)
		if !used[candidate] {
			return candidate, nil
		}
	}
	return "", fmt.Errorf("gate_id_collision_exhausted: %q", gateID)
}

// upstreamPathIndex maps an assigned artifact path to the chain-root FDD path
// that satisfied its upstream reference.
func upstreamPathIndex(chains map[string]ChainEntry) map[string]string {
	idx := map[string]string{}
	for _, entry := range chains {
		if entry.FDD == nil {
			continue
		}
		fddPath := entry.FDD.Path
		for _, group := range [][]ChainNode{entry.TDDs, entry.Epics, entry.POMOs, entry.Sidecars} {
			for _, n := range group {
				idx[n.Path] = fddPath
			}
		}
		if entry.Release != nil {
			idx[entry.Release.Path] = fddPath
		}
	}
	return idx
}
