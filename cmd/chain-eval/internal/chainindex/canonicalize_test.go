package chainindex

import "testing"

// CT-1: Frontmatter status wins over body table; drift=true when they disagree.
func TestExtractStatus_FrontmatterWins(t *testing.T) {
	content := `---
status: Complete
---

# Doc

## Status and Metadata

| Field | Value |
|-------|-------|
| **Status** | Draft |
`
	got := ExtractStatus(content)
	if got.Canonical != "Complete" {
		t.Errorf("CT-1: canonical should be 'Complete' (frontmatter wins), got %q", got.Canonical)
	}
	if !got.SurfaceDrift {
		t.Error("CT-1: expected SurfaceDrift=true when frontmatter and body disagree")
	}
	if got.Surfaces.Frontmatter != "Complete" {
		t.Errorf("CT-1: Surfaces.Frontmatter should be 'Complete', got %q", got.Surfaces.Frontmatter)
	}
	if got.Surfaces.Body != "Draft" {
		t.Errorf("CT-1: Surfaces.Body should be 'Draft', got %q", got.Surfaces.Body)
	}
	if len(got.Surfaces.Divergent) == 0 || got.Surfaces.Divergent[0] != "Draft" {
		t.Errorf("CT-1: Divergent should be [Draft], got %v", got.Surfaces.Divergent)
	}
}

// CT-2: All surfaces agree - drift=false, Divergent nil.
func TestExtractStatus_AllAgree(t *testing.T) {
	content := `---
status: Approved
---

## Status and Metadata

| Field | Value |
|-------|-------|
| **Status** | Approved |
`
	got := ExtractStatus(content)
	if got.Canonical != "Approved" {
		t.Errorf("CT-2: canonical should be 'Approved', got %q", got.Canonical)
	}
	if got.SurfaceDrift {
		t.Error("CT-2: expected SurfaceDrift=false when all surfaces agree")
	}
	if len(got.Surfaces.Divergent) != 0 {
		t.Errorf("CT-2: Divergent should be nil/empty, got %v", got.Surfaces.Divergent)
	}
}

// CT-3: Missing frontmatter - body table status is canonical; no drift if single surface.
func TestExtractStatus_NoFrontmatter(t *testing.T) {
	content := `# Doc

## Status and Metadata

| Field | Value |
|-------|-------|
| **Status** | Approved |
`
	got := ExtractStatus(content)
	if got.Canonical != "Approved" {
		t.Errorf("CT-3: canonical should be 'Approved' (body table), got %q", got.Canonical)
	}
	if got.SurfaceDrift {
		t.Error("CT-3: expected SurfaceDrift=false for single surface")
	}
}

// CT-4: Missing frontmatter + two disagreeing body surfaces - drift=true.
func TestExtractStatus_TwoBodySurfacesDisagree(t *testing.T) {
	content := `# Doc

## Status and Metadata

| Field | Value |
|-------|-------|
| **Status** | Approved |

Status: Draft
`
	got := ExtractStatus(content)
	if got.SurfaceDrift != true {
		t.Error("CT-4: expected SurfaceDrift=true for disagreeing body surfaces")
	}
}

// CT-5: No status found - canonical="Unknown", drift=false.
func TestExtractStatus_NotFound(t *testing.T) {
	content := "# A document with no status anywhere\n\nSome text.\n"
	got := ExtractStatus(content)
	if got.Canonical != "Unknown" {
		t.Errorf("CT-5: expected 'Unknown', got %q", got.Canonical)
	}
	if got.SurfaceDrift {
		t.Error("CT-5: expected SurfaceDrift=false when no status found")
	}
}

// CT-6: Drift=true records carry canonical and all divergent values.
func TestExtractStatus_DriftPayload(t *testing.T) {
	content := `---
status: Complete
---

## Status and Metadata

| Field | Value |
|-------|-------|
| **Status** | Draft |
`
	got := ExtractStatus(content)
	if got.Canonical != "Complete" {
		t.Errorf("CT-6: expected canonical 'Complete', got %q", got.Canonical)
	}
	if len(got.Surfaces.Divergent) == 0 {
		t.Error("CT-6: Divergent should contain the body value")
	}
}

// BT-3: Case-insensitive comparison ("approved" == "Approved") produces no drift.
func TestExtractStatus_CaseInsensitive(t *testing.T) {
	content := `---
status: approved
---

## Status and Metadata

| Field | Value |
|-------|-------|
| **Status** | Approved |
`
	got := ExtractStatus(content)
	if got.SurfaceDrift {
		t.Error("BT-3: expected no drift for case-insensitive match 'approved' == 'Approved'")
	}
}
