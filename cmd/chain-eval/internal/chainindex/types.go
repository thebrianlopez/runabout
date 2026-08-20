package chainindex

// ArtifactType identifies the kind of pipeline artifact.
type ArtifactType string

const (
	ArtifactPRD     ArtifactType = "prd"
	ArtifactFDD     ArtifactType = "fdd"
	ArtifactTDD     ArtifactType = "tdd"
	ArtifactEpic    ArtifactType = "epic"
	ArtifactRelease ArtifactType = "release"
	ArtifactPOMO    ArtifactType = "pomo"
	ArtifactSidecar ArtifactType = "sidecar"
)

// ArtifactRecord is one scanned pipeline document.
//
// The trailing conformance fields (F7) are carried in memory only, marked
// `json:"-"`. They are validation input, not index output: serializing them
// would widen the .chain-index.json contract with command_chain.md for no
// consumer. Schema breaches surface in ChainIndex.SchemaViolations instead.
type ArtifactRecord struct {
	Path               string          `json:"path"` // relative to docs_root
	Type               ArtifactType    `json:"type"`
	Status             string          `json:"status"`
	UpstreamField      string          `json:"upstream_field,omitempty"`
	FeatureID          string          `json:"feature_id,omitempty"`
	CreatedAt          string          `json:"created_at,omitempty"`
	IsProtocol         bool            `json:"is_protocol"`
	StatusSurfaceDrift bool            `json:"status_surface_drift"`      // F4
	StatusSurfaces     *StatusSurfaces `json:"status_surfaces,omitempty"` // F4

	// UpstreamState classifies upstream extraction: "extracted", "absent" or
	// "declared_unextractable" (F7).
	UpstreamState string `json:"-"`
	// RuntimeVersion is the declared Chain Runtime Version, "" when absent (F7).
	RuntimeVersion string `json:"-"`
	// HasFrontmatter reports whether a YAML frontmatter block was parsed (F7).
	HasFrontmatter bool `json:"-"`
	// EpicAgents holds the epic frontmatter `agents:` block that epic-dispatch
	// consumes. Nil for non-epics and for epics without a frontmatter block (F7).
	EpicAgents []EpicAgentAssignment `json:"-"`
}

// EpicAgentAssignment is one entry of an epic's frontmatter `agents:` block.
//
// Milestones is deliberately `any` rather than []string: a malformed shape
// (a bare scalar where a list belongs) must survive parsing and reach the
// validator, which is the breach CT-12 exercises. Typing it would silently
// discard exactly the defect being looked for.
type EpicAgentAssignment struct {
	ID         string `yaml:"id"`
	CWD        string `yaml:"cwd"`
	Milestones any    `yaml:"milestones"`
}

// StatusSurfaces captures per-surface status values for drift detection (F4).
type StatusSurfaces struct {
	Canonical   string   `json:"canonical"`
	Frontmatter string   `json:"frontmatter,omitempty"`
	Body        string   `json:"body,omitempty"`
	Divergent   []string `json:"divergent,omitempty"`
}

// ChainNode is a lightweight reference to one artifact within a chain.
type ChainNode struct {
	Path      string `json:"path"`
	Status    string `json:"status"`
	FeatureID string `json:"feature_id,omitempty"`
}

// ChainEntry holds all artifacts belonging to one chain (keyed by FDD stem).
type ChainEntry struct {
	PRD         *ChainNode        `json:"prd,omitempty"`
	FDD         *ChainNode        `json:"fdd,omitempty"`
	TDDs        []ChainNode       `json:"tdds"`
	Epics       []ChainNode       `json:"epics"`
	Release     *ChainNode        `json:"release,omitempty"`
	POMOs       []ChainNode       `json:"pomos"`
	Sidecars    []ChainNode       `json:"sidecars"`
	GateRecords []ChainGateRecord `json:"gate_records"`
}

// ChainGateRecord encodes one gate evaluation result for CUE validation (F2).
type ChainGateRecord struct {
	GateID           string `json:"gate_id"`
	GateType         string `json:"gate_type"` // "upstream_field" | "pomo_resolution" | ...
	ArtifactPath     string `json:"artifact_path"`
	UpstreamArtifact string `json:"upstream_artifact,omitempty"`
	Status           string `json:"status"` // "satisfied" | "failed" | "advisory"
	SatisfiedAt      string `json:"satisfied_at,omitempty"`
}

// WorkspaceChainLink records a cross-workspace chain association.
type WorkspaceChainLink struct {
	WorkspaceName string `json:"workspace_name"`
	ChainKey      string `json:"chain_key"`
	LinkedAt      string `json:"linked_at"`
}

// ChainIndex is the top-level structure written to .chain-index.json.
type ChainIndex struct {
	SchemaVersion       string                `json:"schema_version"` // "1.0"
	IndexedAt           string                `json:"indexed_at"`     // UTC RFC3339
	ContentHash         string                `json:"content_hash"`   // "sha256:<hex>"
	DocsRoot            string                `json:"docs_root"`      // absolute path
	Artifacts           []ArtifactRecord      `json:"artifacts"`
	Chains              map[string]ChainEntry `json:"chains"`
	Orphans             []string              `json:"orphans"`
	LegacyExcludedCount int                   `json:"legacy_excluded_count"`
	GateRecords         []ChainGateRecord     `json:"gate_records"`
	WorkspaceLinks      []WorkspaceChainLink  `json:"workspace_links"`
	SchemaViolations    []SchemaViolation     `json:"schema_violations"` // F7
}

// StatusExtractionResult holds the output of ExtractStatus (F4).
type StatusExtractionResult struct {
	Canonical    string
	SurfaceDrift bool
	Surfaces     StatusSurfaces
}
