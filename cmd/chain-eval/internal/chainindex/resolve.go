package chainindex

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// V2 (EPIC-268 chain-root sentinel epic): the referent-resolves-to-nothing
// resolver. Consumes the already-classified upstream_state that
// classifyUpstreamState (scanner.go) and its declared_none/NO-UPSTREAM
// recognition (M2b) produce - this file does not re-derive sentinel-ness or
// any other classification from raw cell text. That split is deliberate
// (RG-3 discipline carried forward from the scoping document): extraction and
// classification live in scanner.go, resolution lives here.

// Resolution outcomes. Distinct from UpstreamState (schema.go): those
// classify the cell's *declaration*; these classify what the declared
// referent resolves to on disk.
const (
	ResolutionResolved   = "resolved"
	ResolutionUnresolved = "unresolved"
	ResolutionSevered    = "severed"
)

// upstreamReferentTypes is V2's candidate population.
//
// CORRECTED (orchestrator review, post-implementation sanity check against
// the real corpus): the first cut of this set was FDD/TDD only, on the
// (correct, but incomplete) reasoning that epics must stay excluded per Q3.
// That produced 0 unresolved against the real corpus where the epic's own
// arithmetic requires 3 - the three fabricated referents section 5.3.2 of
// research/PERSONAL_20260822T130928Z_..._Gate_Specification.md names
// explicitly all live in `releases/`, a type FDD/TDD-only silently dropped
// from the population instead of resolving and failing. Confirmed by
// widening the filter and re-running: exactly the 3 named artifacts
// (releases/PERSONAL_20260609T154000Z_..._Release.md and its two siblings)
// come back unresolved, with no other candidate's outcome changing.
//
// The corrected set mirrors gates.go's gateableTypes (the codebase's own
// prior-art boundary for "which artifact types carry an upstream
// obligation"), plus FDD - gates.go excludes FDD only because its PRD link
// is tracked as a *separate* gate type there, not because FDD has no
// obligation; V2 resolves both FDD's and TDD's Source PRD/FDD cells through
// the same code path, so both belong here.
//
// Excluded, each for a reason already established elsewhere in this package,
// not a new one invented for V2:
//   - Epic: Q3 (declared_none/#EpicRecord not widened by M2a) plus a
//     structurally different upstream_field (extractEpicFDD, not the
//     Source PRD/FDD/TDD table-cell path this file consumes).
//   - PRD: chain root by definition (gates.go's own comment); empirically
//     confirmed to carry no non-empty upstream_field in the corpus.
//   - Sidecar: "advisory-only and never gates a chain" (gates.go's own
//     comment on gateableTypes) - the same rationale extends to V2's report.
var upstreamReferentTypes = map[ArtifactType]bool{
	ArtifactFDD:     true,
	ArtifactTDD:     true,
	ArtifactRelease: true,
	ArtifactPOMO:    true,
}

// UpstreamResolution is one V2 resolution result for one candidate record.
type UpstreamResolution struct {
	ArtifactPath  string `json:"artifact_path"`
	ArtifactType  string `json:"artifact_type"`
	UpstreamField string `json:"upstream_field"`
	Normalized    string `json:"normalized_referent"`
	Outcome       string `json:"outcome"` // resolved | unresolved | severed
	ResolvedPath  string `json:"resolved_path,omitempty"`
}

// ResolutionReport summarizes one V2 run over a record set. Every count is
// carried explicitly (task requirement 4 / scoping doc section 3 item 4):
// an undocumented denominator shift is exactly the kind of silent predicate
// change the epic's own precedent (805/28/52/27/12 reconciliation) spent
// multiple rounds recovering from.
type ResolutionReport struct {
	// DeclaredNoneExcluded counts FDD/TDD records with a non-empty
	// upstream_field whose upstream_state is declared_none (the NO-UPSTREAM
	// sentinel, M2b). These never enter resolution - filtered before, not
	// resolved-and-suppressed.
	DeclaredNoneExcluded int `json:"declared_none_excluded"`
	// Resolved counts referents that resolved to a real, non-archived file.
	Resolved int `json:"resolved"`
	// Unresolved counts referents that resolved to nothing under either root.
	Unresolved int `json:"unresolved"`
	// Severed counts referents that resolved, but under an archive path - a
	// distinct outcome from Unresolved, per the epic's archive-awareness
	// requirement. Not collapsed into either Resolved or Unresolved.
	Severed int `json:"severed"`
}

// candidateCount is the total number of records V2 actually attempted to
// resolve (upstream_field != "" AND upstream_state == extracted, FDD/TDD
// only). Resolved + Unresolved + Severed always sums to this.
func (r ResolutionReport) candidateCount() int {
	return r.Resolved + r.Unresolved + r.Severed
}

var (
	// annotationSuffixRe strips one or more trailing parenthetical
	// annotations, e.g. "PRD.md (F-018)" -> "PRD.md". Repeated application
	// (via ReplaceAllString + a loop in normalizeReferent) handles the rare
	// doubled-annotation case; a single pass covers the corpus's only known
	// instance (LinkariAndroid_UI_Polish_FDD.md, epic Q3 exclusion list).
	annotationSuffixRe = regexp.MustCompile(`\s*\([^()]*\)\s*$`)
	// bareStemRe matches a referent with no path separator and no extension -
	// a bare document stem that the corpus convention appends .md to.
	bareStemRe = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)
	// docsPrefixRe strips a leading docs-root-style prefix so a referent
	// authored as "docs/design/x_FDD.md" resolves the same as "design/x_FDD.md".
	docsPrefixRe = regexp.MustCompile(`^(\.{0,2}/)?docs/`)
)

// normalizeReferent applies path normalisation to raw `Source PRD`/`Source
// FDD`/`Source TDD` cell prose, in the order the scoping document specifies:
// strip a docs/-style prefix, strip a trailing annotation suffix, strip an
// #anchor fragment, then append .md to a bare stem. Order matters - an
// anchor fragment or annotation could otherwise survive into the appended
// extension (".md#anchor" is not a bare stem).
//
// Returns "" when nothing resolvable remains (defensive; V2's candidate
// filter already guarantees a non-empty upstream_field for every call site).
func normalizeReferent(raw string) string {
	s := strings.TrimSpace(raw)
	s = strings.Trim(s, "`")
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}

	s = docsPrefixRe.ReplaceAllString(s, "")

	// Strip a trailing annotation suffix. Bounded loop, not unbounded: one
	// pass handles the corpus's known case; a second guards a doubled
	// annotation without risking runaway stripping of legitimate parenthetical
	// content that is not a trailing suffix.
	for i := 0; i < 2; i++ {
		stripped := annotationSuffixRe.ReplaceAllString(s, "")
		if stripped == s {
			break
		}
		s = strings.TrimSpace(stripped)
	}

	// Strip an #anchor fragment.
	if idx := strings.Index(s, "#"); idx >= 0 {
		s = strings.TrimSpace(s[:idx])
	}

	if s == "" {
		return ""
	}

	if bareStemRe.MatchString(s) && !strings.HasSuffix(strings.ToLower(s), ".md") {
		s += ".md"
	}

	return s
}

// isArchivePath reports whether rel (a path relative to a resolution root)
// passes through an "archive" directory segment at any depth. Case-sensitive
// on the segment name to match the corpus convention (prds/archive/).
func isArchivePath(rel string) bool {
	for _, seg := range strings.Split(filepath.ToSlash(rel), "/") {
		if seg == "archive" {
			return true
		}
	}
	return false
}

// resolutionRoots pairs a resolution root with a precomputed basename index
// for that root, used by the mandatory clause-3 (basename-anywhere) fallback.
type resolutionRoots struct {
	roots     []string
	basenames map[string][]string // basename -> absolute paths, root order then alpha
}

// buildResolutionRoots walks each root once and indexes every regular file by
// basename. Empty/nonexistent roots are skipped rather than erroring - V2 is a
// report-only, best-effort resolver (ADVISORY-ONLY ruling), and a missing
// second root (e.g. WS_ORG_CORE unset in a test) should degrade resolution
// coverage, not fail the run.
func buildResolutionRoots(roots ...string) resolutionRoots {
	rr := resolutionRoots{basenames: map[string][]string{}}
	for _, root := range roots {
		if root == "" {
			continue
		}
		if _, err := os.Stat(root); err != nil {
			continue
		}
		rr.roots = append(rr.roots, root)
		_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return nil //nolint:nilerr
			}
			base := d.Name()
			rr.basenames[base] = append(rr.basenames[base], path)
			return nil
		})
	}
	for base := range rr.basenames {
		sort.Strings(rr.basenames[base])
	}
	return rr
}

// resolve applies the three-clause fallback (root-relative, citer-relative,
// basename-anywhere) to one normalized referent, returning the absolute path
// of the first match and the root it was found under, or ok=false.
//
// Clause order and the mandatory (non-optional) basename-anywhere clause
// mirror the pinned M2/M3 citation predicate (scoping doc section 2): removing
// clause 3 there took the resolved count from thousands to a handful, and
// V2's referent prose is authored even more variably than that predicate's
// bare-token citations.
func (rr resolutionRoots) resolve(normalized, citerAbsPath string) (absPath, root string, ok bool) {
	if normalized == "" {
		return "", "", false
	}

	// Clause 1: root-relative.
	for _, root := range rr.roots {
		candidate := filepath.Join(root, normalized)
		if fileExists(candidate) {
			return candidate, root, true
		}
	}

	// Clause 2: citer-relative.
	if citerAbsPath != "" {
		citerDir := filepath.Dir(citerAbsPath)
		candidate := filepath.Join(citerDir, normalized)
		if fileExists(candidate) {
			for _, root := range rr.roots {
				if rel, err := filepath.Rel(root, candidate); err == nil && !strings.HasPrefix(rel, "..") {
					return candidate, root, true
				}
			}
			// Resolved outside every known root (rare - a citer under one root
			// pointing relatively past it). Report it under its own directory's
			// nearest containing root if any, else unattributed.
			return candidate, citerDir, true
		}
	}

	// Clause 3: basename-anywhere. Mandatory, per the predicate.
	base := filepath.Base(normalized)
	if matches := rr.basenames[base]; len(matches) > 0 {
		match := matches[0]
		for _, root := range rr.roots {
			if rel, err := filepath.Rel(root, match); err == nil && !strings.HasPrefix(rel, "..") {
				return match, root, true
			}
		}
		return match, "", true
	}

	return "", "", false
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

// ResolveUpstreamReferents is V2: for every FDD/TDD record whose
// upstream_field is non-empty and whose upstream_state is "extracted" (the
// declared_none/NO-UPSTREAM sentinel is excluded upstream of this function,
// by construction - see DeclaredNoneExcluded), normalizes the referent and
// resolves it against disk under docsRoot and, when non-empty, coreRoot.
//
// docsRoot is also the base every record's Path is relative to (Scan's
// contract). coreRoot may be "" - resolution then runs against docsRoot alone,
// with reduced coverage rather than an error (see buildResolutionRoots).
func ResolveUpstreamReferents(records []ArtifactRecord, docsRoot, coreRoot string) ([]UpstreamResolution, ResolutionReport) {
	rr := buildResolutionRoots(docsRoot, coreRoot)

	var results []UpstreamResolution
	var report ResolutionReport

	sorted := make([]ArtifactRecord, len(records))
	copy(sorted, records)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].Path < sorted[j].Path })

	for _, r := range sorted {
		if !upstreamReferentTypes[r.Type] {
			continue
		}
		if r.UpstreamField == "" {
			continue // missing_upstream_field population - not V2's target
		}
		if r.UpstreamState == UpstreamDeclaredNone {
			report.DeclaredNoneExcluded++
			continue
		}
		if r.UpstreamState != UpstreamExtracted {
			// Defensive: a non-empty upstream_field only ever classifies as
			// extracted or declared_none (declared_unextractable requires an
			// empty extracted field, see classifyUpstreamState). Skip rather
			// than guess if that invariant is ever broken upstream.
			continue
		}

		normalized := normalizeReferent(r.UpstreamField)
		citerAbsPath := filepath.Join(docsRoot, r.Path)

		res := UpstreamResolution{
			ArtifactPath:  r.Path,
			ArtifactType:  string(r.Type),
			UpstreamField: r.UpstreamField,
			Normalized:    normalized,
		}

		absPath, root, ok := rr.resolve(normalized, citerAbsPath)
		if !ok {
			res.Outcome = ResolutionUnresolved
			report.Unresolved++
			results = append(results, res)
			continue
		}

		rel := absPath
		if root != "" {
			if r2, err := filepath.Rel(root, absPath); err == nil {
				rel = r2
			}
		}
		res.ResolvedPath = absPath

		if isArchivePath(rel) {
			res.Outcome = ResolutionSevered
			report.Severed++
		} else {
			res.Outcome = ResolutionResolved
			report.Resolved++
		}
		results = append(results, res)
	}

	return results, report
}
