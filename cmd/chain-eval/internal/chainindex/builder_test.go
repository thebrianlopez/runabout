package chainindex

import (
	"testing"
)

// CT-3: Empty docs root produces a valid, non-panicking empty index.
func TestBuild_EmptyCorpus(t *testing.T) {
	idx := Build(nil, t.TempDir(), false)
	if idx.Artifacts != nil && len(idx.Artifacts) != 0 {
		t.Errorf("CT-3: expected empty artifacts, got %d", len(idx.Artifacts))
	}
	if len(idx.Chains) != 0 {
		t.Errorf("CT-3: expected empty chains, got %d", len(idx.Chains))
	}
	if len(idx.Orphans) != 0 {
		t.Errorf("CT-3: expected empty orphans, got %d", len(idx.Orphans))
	}
	if len(idx.GateRecords) != 0 {
		t.Errorf("CT-3: expected empty gate records, got %d", len(idx.GateRecords))
	}
}

// CT-1: Two Build calls with identical input produce the same content_hash.
// (indexed_at may differ; content_hash is based on paths + stat metadata.)
func TestBuild_Deterministic(t *testing.T) {
	records := []ArtifactRecord{
		{Path: "prds/PRD_A.md", Type: ArtifactPRD, Status: "Active", CreatedAt: "20260601T000000Z"},
		{Path: "design/FDD_A.md", Type: ArtifactFDD, Status: "Approved", CreatedAt: "20260601T000000Z"},
	}
	root := t.TempDir()
	idx1 := Build(records, root, false)
	idx2 := Build(records, root, false)
	if idx1.ContentHash != idx2.ContentHash {
		t.Errorf("CT-1: content_hash not deterministic: %q vs %q", idx1.ContentHash, idx2.ContentHash)
	}
}

// CT-7: Chain key is derived from the FDD filename stem, not the PRD.
// Given FDD named PERSONAL_20260101T000000Z_Linkari_Share_Dedup_FDD.md,
// chain key should be "linkari_share_dedup" (prefix+timestamp stripped, lowercase).
func TestBuild_ChainKeyFromFDDStem(t *testing.T) {
	records := []ArtifactRecord{
		{
			Path:      "design/PERSONAL_20260101T000000Z_Linkari_Share_Dedup_FDD.md",
			Type:      ArtifactFDD,
			Status:    "Approved",
			CreatedAt: "20260601T000000Z",
		},
	}
	idx := Build(records, t.TempDir(), false)
	if _, ok := idx.Chains["linkari_share_dedup"]; !ok {
		t.Errorf("CT-7: expected chain key 'linkari_share_dedup', got keys: %v", chainKeys(idx))
	}
}

// CT-8: Pre-2026-04-21 orphans are excluded from the orphan list by default.
func TestBuild_LegacyOrphansExcluded(t *testing.T) {
	// Epic with no parent FDD, created before the threshold.
	records := []ArtifactRecord{
		{
			Path:      "epics/OLD_EPIC_001.md",
			Type:      ArtifactEpic,
			Status:    "Complete",
			CreatedAt: "20260101T000000Z", // before 2026-04-21
		},
	}
	idx := Build(records, t.TempDir(), false /* includeLegacy=false */)
	if len(idx.Orphans) != 0 {
		t.Errorf("CT-8: expected 0 orphans (legacy excluded), got %d: %v", len(idx.Orphans), idx.Orphans)
	}
}

// CT-8b: With includeLegacy=true, pre-threshold orphans appear in the list.
func TestBuild_LegacyOrphansIncluded(t *testing.T) {
	records := []ArtifactRecord{
		{
			Path:      "epics/OLD_EPIC_001.md",
			Type:      ArtifactEpic,
			Status:    "Complete",
			CreatedAt: "20260101T000000Z",
		},
	}
	idx := Build(records, t.TempDir(), true /* includeLegacy=true */)
	if len(idx.Orphans) == 0 {
		t.Error("CT-8b: expected at least one legacy orphan when includeLegacy=true")
	}
}

func chainKeys(idx ChainIndex) []string {
	keys := make([]string, 0, len(idx.Chains))
	for k := range idx.Chains {
		keys = append(keys, k)
	}
	return keys
}
