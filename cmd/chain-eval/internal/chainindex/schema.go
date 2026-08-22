package chainindex

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// Upstream extraction states. These mirror #UpstreamState in artifact.cue.
const (
	UpstreamExtracted             = "extracted"
	UpstreamAbsent                = "absent"
	UpstreamDeclaredUnextractable = "declared_unextractable"
	UpstreamDeclaredNone          = "declared_none"
)

// Violation severities.
const (
	SeverityError   = "error"
	SeverityWarning = "warning"
)

// Error classes from TDD section 4.
const (
	RuleStatusEnum                 = "status_enum"
	RuleStatusUnparseable          = "status_unparseable"
	RuleStatusAbsent               = "status_absent"
	RuleMissingUpstreamField       = "missing_upstream_field"
	RuleUpstreamFieldUnextractable = "upstream_field_unextractable"
	RuleMissingFrontmatter         = "missing_frontmatter"
	RuleRuntimeVersionMalformed    = "runtime_version_malformed"
	RuleEpicFrontmatterMalformed   = "epic_frontmatter_malformed"
	RuleSchemaViolation            = "schema_violation"
)

// statusAbsentSentinel is ExtractStatus's output when an artifact exposes no
// status surface at all.
const statusAbsentSentinel = "Unknown"

// SchemaViolation is one field-contract breach on one artifact.
type SchemaViolation struct {
	ArtifactPath string `json:"artifact_path"`
	ArtifactType string `json:"artifact_type"`
	Rule         string `json:"rule"`
	Field        string `json:"field,omitempty"`
	Detected     string `json:"detected,omitempty"`
	Expected     string `json:"expected,omitempty"`
	Severity     string `json:"severity"`
}

// artifactPayload is the validation projection of an ArtifactRecord.
//
// Every field is emitted unconditionally - no omitempty anywhere. In the
// list-unification transport a field absent from the data silently inherits the
// schema's concrete value instead of being reported, so an omitted field is an
// unchecked field. Absence is encoded as a sentinel the schema rejects
// ("Unknown", "absent", ""). See the transport note at the top of artifact.cue.
type artifactPayload struct {
	Path           string `json:"path"`
	Type           string `json:"type"`
	Status         string `json:"status"`
	UpstreamField  string `json:"upstream_field"`
	UpstreamState  string `json:"upstream_state"`
	CreatedAt      string `json:"created_at"`
	FeatureID      string `json:"feature_id"`
	IsProtocol     bool   `json:"is_protocol"`
	RuntimeVersion string `json:"runtime_version"`
	HasFrontmatter bool   `json:"has_frontmatter"`
}

type epicAgentPayload struct {
	ID         string `json:"id"`
	CWD        string `json:"cwd"`
	Milestones any    `json:"milestones"`
}

type epicFrontmatterPayload struct {
	Path   string             `json:"path"`
	Agents []epicAgentPayload `json:"agents"`
}

// listNameByType maps an artifact type to its CUE list name and definition.
// Adding a type here and to artifact.cue is the only wiring a new type needs.
var listNameByType = map[ArtifactType]string{
	ArtifactPRD:     "prd",
	ArtifactFDD:     "fdd",
	ArtifactTDD:     "tdd",
	ArtifactEpic:    "epic",
	ArtifactRelease: "release",
	ArtifactPOMO:    "pomo",
	ArtifactSidecar: "sidecar",
}

var defNameByList = map[string]string{
	"prd":              "#PRDRecord",
	"fdd":              "#FDDRecord",
	"tdd":              "#TDDRecord",
	"epic":             "#EpicRecord",
	"release":          "#ReleaseRecord",
	"pomo":             "#POMORecord",
	"sidecar":          "#SidecarRecord",
	"epicfrontmatter":  "#EpicFrontmatter",
	"epic_frontmatter": "#EpicFrontmatter",
}

const epicFrontmatterList = "epic_frontmatter"

// ValidateArtifacts vets every record against artifact.cue and, for epics
// carrying frontmatter, against #EpicFrontmatter. It returns one violation per
// breach, deterministically ordered.
//
// Returns ErrCUENotFound when cue or the schema is unavailable (warning-class,
// per F2): the caller writes the index unvalidated rather than failing.
//
// No enum or required-field list is restated here. This function decides which
// records to send, classifies the field paths cue reports back into the section
// 4 taxonomy, and assigns severity by artifact age. The field contract itself
// lives only in artifact.cue (RG-3).
func ValidateArtifacts(records []ArtifactRecord, schemaDir string) ([]SchemaViolation, error) {
	if len(records) == 0 {
		return []SchemaViolation{}, nil
	}
	schemaPath := filepath.Join(schemaDir, "artifact.cue")
	if _, err := os.Stat(schemaPath); os.IsNotExist(err) {
		return nil, ErrCUENotFound
	}

	// Stable order in, stable indices back out of cue.
	sorted := make([]ArtifactRecord, len(records))
	copy(sorted, records)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].Path < sorted[j].Path })

	lists := map[string][]artifactPayload{}
	byListIndex := map[string][]ArtifactRecord{}
	frontmatter := []epicFrontmatterPayload{}
	frontmatterRecords := []ArtifactRecord{}

	for _, r := range sorted {
		list, ok := listNameByType[r.Type]
		if !ok {
			continue
		}
		lists[list] = append(lists[list], payloadFor(r))
		byListIndex[list] = append(byListIndex[list], r)

		if r.Type == ArtifactEpic && r.HasFrontmatter {
			agents := make([]epicAgentPayload, 0, len(r.EpicAgents))
			for _, a := range r.EpicAgents {
				agents = append(agents, epicAgentPayload{
					ID:         a.ID,
					CWD:        a.CWD,
					Milestones: normalizeMilestones(a.Milestones),
				})
			}
			frontmatter = append(frontmatter, epicFrontmatterPayload{Path: r.Path, Agents: agents})
			frontmatterRecords = append(frontmatterRecords, r)
		}
	}
	byListIndex[epicFrontmatterList] = frontmatterRecords

	data := map[string]any{}
	for list, payloads := range lists {
		data[list] = payloads
	}
	data[epicFrontmatterList] = frontmatter

	out, code, err := runArtifactVet(schemaPath, data, lists)
	if err != nil {
		return nil, err
	}
	if code == 127 || (code != 0 && isCUENotFound(string(out))) {
		return nil, ErrCUENotFound
	}
	if code == 0 {
		return []SchemaViolation{}, nil
	}

	violations := violationsFromCUE(string(out), byListIndex)
	sort.SliceStable(violations, func(i, j int) bool {
		if violations[i].ArtifactPath != violations[j].ArtifactPath {
			return violations[i].ArtifactPath < violations[j].ArtifactPath
		}
		if violations[i].Rule != violations[j].Rule {
			return violations[i].Rule < violations[j].Rule
		}
		return violations[i].Field < violations[j].Field
	})
	return violations, nil
}

func payloadFor(r ArtifactRecord) artifactPayload {
	upstream := r.UpstreamState
	if upstream == "" {
		upstream = UpstreamAbsent
	}
	return artifactPayload{
		Path:           r.Path,
		Type:           string(r.Type),
		Status:         r.Status,
		UpstreamField:  r.UpstreamField,
		UpstreamState:  upstream,
		CreatedAt:      r.CreatedAt,
		FeatureID:      r.FeatureID,
		IsProtocol:     r.IsProtocol,
		RuntimeVersion: r.RuntimeVersion,
		HasFrontmatter: r.HasFrontmatter,
	}
}

// normalizeMilestones preserves a malformed milestones value verbatim so the
// schema can reject it, while converting a well-formed list to []string.
func normalizeMilestones(v any) any {
	if v == nil {
		return []string{}
	}
	items, ok := v.([]any)
	if !ok {
		return v
	}
	out := make([]any, 0, len(items))
	for _, it := range items {
		if s, ok := it.(string); ok {
			out = append(out, s)
			continue
		}
		out = append(out, it)
	}
	return out
}

// runArtifactVet writes the payload plus a generated list constraint and
// invokes cue vet once for the whole corpus.
func runArtifactVet(schemaPath string, data map[string]any, lists map[string][]artifactPayload) ([]byte, int, error) {
	tmpDir, err := os.MkdirTemp("", "chain-artifact-validate-*")
	if err != nil {
		return nil, 0, fmt.Errorf("create temp dir: %w", err)
	}
	defer os.RemoveAll(tmpDir) //nolint:errcheck

	encoded, err := json.Marshal(data)
	if err != nil {
		return nil, 0, fmt.Errorf("marshal: %w", err)
	}
	dataPath := filepath.Join(tmpDir, "artifacts.json")
	if err := os.WriteFile(dataPath, encoded, 0o600); err != nil {
		return nil, 0, fmt.Errorf("write temp: %w", err)
	}

	// The constraint file binds each list to its definition. Without it cue
	// unifies the data against the package's top level and checks nothing -
	// the F6 silent-pass shape.
	var b strings.Builder
	fmt.Fprintf(&b, "package %s\n\n", schemaPackage(schemaPath))
	names := make([]string, 0, len(lists)+1)
	for list := range lists {
		names = append(names, list)
	}
	names = append(names, epicFrontmatterList)
	sort.Strings(names)
	for _, list := range names {
		fmt.Fprintf(&b, "%s: [...%s]\n", list, defNameByList[list])
	}
	constraintPath := filepath.Join(tmpDir, "artifacts_constraint.cue")
	if err := os.WriteFile(constraintPath, []byte(b.String()), 0o600); err != nil {
		return nil, 0, fmt.Errorf("write constraint: %w", err)
	}

	args := append([]string{"vet", "--all-errors"}, schemaPackageFiles(schemaPath)...)
	args = append(args, constraintPath, dataPath)
	out, code := cueRunner(args...)
	return out, code, nil
}

var (
	// cueErrLineRe matches "list.3.status: message" and nested field paths
	// such as "epic_frontmatter.0.agents.1.id: message".
	cueErrLineRe = regexp.MustCompile(`^([a-z_]+)\.(\d+)\.([A-Za-z0-9_.]+): (.*)$`)
	// cueConflictRe pulls the schema-side alternative out of a conflict message.
	cueConflictRe = regexp.MustCompile(`^conflicting values (.+?) and (.+?):?$`)
	// cueBoundRe pulls the constraint out of an out-of-bound message.
	cueBoundRe = regexp.MustCompile(`\(out of bound (.+)\)`)
	// statusDecorationRe matches any character that cannot belong to a bare
	// status word: digits, punctuation, emoji, brackets.
	statusDecorationRe = regexp.MustCompile(`[^\p{L}\s'-]`)
)

type violationKey struct {
	list  string
	index int
	field string
}

// violationsFromCUE parses cue vet output into one violation per breached
// field, reconstructing the expected value from cue's own messages rather than
// restating it in Go.
func violationsFromCUE(out string, byListIndex map[string][]ArtifactRecord) []SchemaViolation {
	order := []violationKey{}
	expected := map[violationKey][]string{}
	seen := map[violationKey]bool{}

	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(line, " ") || strings.HasPrefix(line, "\t") {
			continue // indented source-location continuation
		}
		m := cueErrLineRe.FindStringSubmatch(strings.TrimRight(line, "\r"))
		if m == nil {
			continue
		}
		idx, err := strconv.Atoi(m[2])
		if err != nil {
			continue
		}
		key := violationKey{list: m[1], index: idx, field: m[3]}
		if !seen[key] {
			seen[key] = true
			order = append(order, key)
		}
		if alt := expectedFromMessage(m[4]); alt != "" {
			expected[key] = appendUnique(expected[key], alt)
		}
	}

	violations := make([]SchemaViolation, 0, len(order))
	for _, key := range order {
		recs, ok := byListIndex[key.list]
		if !ok || key.index >= len(recs) {
			continue
		}
		r := recs[key.index]
		detected := detectedFor(r, key.field)
		exp := expected[key]
		sort.Strings(exp)
		violations = append(violations, SchemaViolation{
			ArtifactPath: r.Path,
			ArtifactType: string(r.Type),
			Rule:         ruleFor(key.list, key.field, r),
			Field:        fieldNameFor(key.list, key.field),
			Detected:     detected,
			Expected:     strings.Join(exp, " | "),
			Severity:     severityFor(r.CreatedAt),
		})
	}
	return violations
}

// expectedFromMessage recovers the schema-side expectation from a cue message.
// Deriving it from cue output rather than writing it in Go is what keeps the
// enum in exactly one place (RG-3).
func expectedFromMessage(msg string) string {
	if m := cueBoundRe.FindStringSubmatch(msg); m != nil {
		return strings.TrimSpace(m[1])
	}
	if m := cueConflictRe.FindStringSubmatch(msg); m != nil {
		return strings.TrimSpace(m[1])
	}
	return ""
}

func appendUnique(list []string, v string) []string {
	for _, existing := range list {
		if existing == v {
			return list
		}
	}
	return append(list, v)
}

// ruleFor maps a breached field path to a section 4 error class.
//
// This is classification of a failure, not restatement of a rule: it never asks
// which values are legal, only which field cue rejected and - for status, whose
// three classes share one field - what shape the rejected value has.
func ruleFor(list, fieldPath string, r ArtifactRecord) string {
	if list == epicFrontmatterList {
		return RuleEpicFrontmatterMalformed
	}
	switch rootField(fieldPath) {
	case "status":
		switch {
		case r.Status == statusAbsentSentinel:
			return RuleStatusAbsent
		case statusDecorationRe.MatchString(r.Status):
			return RuleStatusUnparseable
		default:
			return RuleStatusEnum
		}
	case "upstream_state":
		if r.UpstreamState == UpstreamDeclaredUnextractable {
			return RuleUpstreamFieldUnextractable
		}
		return RuleMissingUpstreamField
	case "runtime_version":
		return RuleRuntimeVersionMalformed
	case "has_frontmatter":
		return RuleMissingFrontmatter
	default:
		return RuleSchemaViolation
	}
}

func fieldNameFor(list, fieldPath string) string {
	if list == epicFrontmatterList {
		return fieldPath
	}
	return rootField(fieldPath)
}

func rootField(fieldPath string) string {
	if i := strings.Index(fieldPath, "."); i >= 0 {
		return fieldPath[:i]
	}
	return fieldPath
}

func detectedFor(r ArtifactRecord, fieldPath string) string {
	switch rootField(fieldPath) {
	case "status":
		return r.Status
	case "upstream_state":
		return r.UpstreamState
	case "runtime_version":
		return r.RuntimeVersion
	case "has_frontmatter":
		return strconv.FormatBool(r.HasFrontmatter)
	default:
		return ""
	}
}

// severityFor implements the Q7 threshold rule. Artifacts created on or after
// 2026-04-21 are errors and block a chain; earlier ones are legacy and warn.
//
// Q7 reversed the original judgment after measurement: the intuitive
// alternative, exempting artifacts that lack frontmatter as pre-convention,
// turned out to exempt 135 post-threshold epics, 88 of them in live chains.
// The same constant already governs orphan detection and gate evaluation, so
// the corpus has one boundary rather than three.
func severityFor(createdAt string) string {
	if createdAt >= orphanThreshold {
		return SeverityError
	}
	return SeverityWarning
}

// CountSeverities summarizes a violation set for the build report.
func CountSeverities(violations []SchemaViolation) (errors, warnings int) {
	for _, v := range violations {
		if v.Severity == SeverityError {
			errors++
			continue
		}
		warnings++
	}
	return errors, warnings
}

// WarnSchemaViolationsEmpty reports the silence that hid the F6 regression:
// absence of violations over a non-empty corpus must never be
// indistinguishable from a clean result.
func WarnSchemaViolationsEmpty(artifactCount int) {
	fmt.Fprintf(gateStderr,
		"chain-eval index: WARN: schema_violations_empty - 0 violations across %d artifacts; verify the validator ran\n",
		artifactCount)
}
