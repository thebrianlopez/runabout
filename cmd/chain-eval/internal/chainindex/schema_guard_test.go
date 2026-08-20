package chainindex

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// Regression guards for F7. Each one encodes a failure this feature already
// suffered somewhere in the chain, so they are asserted by property rather than
// by example.

// rg2CleanFixtureTests are the only tests permitted to assert an empty
// violation set. Each runs against a deliberately clean fixture or a stubbed
// runner, never against the corpus.
//
// Adding a name here is a deliberate act. F1's CT-3 asserted an empty set over
// a real corpus and blinded the suite to a defect that then survived 77 days.
var rg2CleanFixtureTests = map[string]bool{
	"TestCT1_ConformantArtifactProducesNoViolation": true,
	"TestValidateArtifacts_EmptyInput":              true,
}

// RG-1: the live corpus must produce a non-empty violation set.
//
// A zero result means the validator is not running. That is the entire failure
// mode F7 exists to prevent, so it is asserted against the real docs root when
// one is present rather than only against fixtures.
func TestRG1_LiveCorpusProducesViolations(t *testing.T) {
	schemaDir := requireCUE(t)
	docsRoot := locateDocsRoot()
	if docsRoot == "" {
		t.Skip("live docs root not found - set CHAIN_DOCS_ROOT or WS_ORG_DOCS")
	}

	records, err := Scan(docsRoot, nil)
	if err != nil {
		t.Fatalf("RG-1: scan failed: %v", err)
	}
	if len(records) == 0 {
		t.Skip("live docs root has no artifacts")
	}

	violations, err := ValidateArtifacts(records, schemaDir)
	if err != nil {
		t.Fatalf("RG-1: validation failed: %v", err)
	}
	if len(violations) == 0 {
		t.Fatalf("RG-1: zero violations across %d live artifacts - the validator is not running", len(records))
	}

	// The severity split must be real in both directions, not collapsed.
	errs, warns := CountSeverities(violations)
	if errs == 0 {
		t.Errorf("RG-1: expected post-threshold errors across %d artifacts, got none", len(records))
	}
	if warns == 0 {
		t.Errorf("RG-1: expected pre-threshold legacy warnings across %d artifacts, got none", len(records))
	}
	t.Logf("RG-1: %d violations across %d artifacts (%d error, %d warning)",
		len(violations), len(records), errs, warns)
}

// RG-2: no test may assert an empty violation set except against a deliberately
// clean fixture.
func TestRG2_NoEmptySetAssertionOutsideCleanFixture(t *testing.T) {
	files, err := filepath.Glob("*_test.go")
	if err != nil {
		t.Fatal(err)
	}
	funcRe := regexp.MustCompile(`^func (Test\w+)\(`)
	// Assertions that a violation set is empty, in the forms this suite uses.
	emptyAssertRe := regexp.MustCompile(`len\(violations\)\s*!=\s*0|len\(violations\)\s*>\s*0\s*\{[^}]*t\.(Error|Fatal)`)

	for _, file := range files {
		content, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		current := ""
		for i, line := range strings.Split(string(content), "\n") {
			if m := funcRe.FindStringSubmatch(line); m != nil {
				current = m[1]
			}
			if !emptyAssertRe.MatchString(line) {
				continue
			}
			if rg2CleanFixtureTests[current] {
				continue
			}
			t.Errorf("RG-2: %s:%d in %s asserts an empty violation set outside a clean fixture; "+
				"add it to rg2CleanFixtureTests only if its input is deliberately clean",
				file, i+1, current)
		}
	}
}

// RG-3: no enum or required-field list is duplicated between artifact.cue and
// Go. A rule exists in exactly one place.
//
// The check reads the status enums out of the live artifact.cue and asserts
// that none of their members appears as a string literal in non-test Go source
// of this package. Go is allowed to classify a failure; it is not allowed to
// know which values are legal.
func TestRG3_NoRuleDuplicatedBetweenCUEAndGo(t *testing.T) {
	schemaDir := locateSchemaDir()
	if schemaDir == "" {
		t.Skip("core CUE schema dir not found")
	}
	schema, err := os.ReadFile(filepath.Join(schemaDir, "artifact.cue"))
	if err != nil {
		t.Skip("artifact.cue not readable")
	}

	enumValues := statusEnumValues(string(schema))

	// Guard the guard: if the schema's shape changes so that recovery silently
	// returns little or nothing, this check would pass vacuously - the same
	// silent-pass shape it exists to catch. Require a member of every ratified
	// enum to have been recovered.
	recovered := map[string]bool{}
	for _, v := range enumValues {
		recovered[v] = true
	}
	for _, sentinel := range []string{
		"In Development", // PRD
		"Cancelled",      // FDD
		"Withdrawn",      // TDD
		"Won't Do",       // Epic
		"Rolled Back",    // Release
		"Resolved",       // POMO
	} {
		if !recovered[sentinel] {
			t.Fatalf("RG-3: enum recovery from artifact.cue missed %q; the guard cannot see the schema", sentinel)
		}
	}

	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, file := range files {
		if strings.HasSuffix(file, "_test.go") {
			continue
		}
		content, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		for _, value := range enumValues {
			if strings.Contains(string(content), `"`+value+`"`) {
				t.Errorf("RG-3: %s restates the status value %q, which artifact.cue owns; "+
					"classify the failure instead of listing legal values", file, value)
			}
		}
	}
}

// statusEnumValues pulls the quoted members of every #...Status definition out
// of artifact.cue.
func statusEnumValues(schema string) []string {
	defRe := regexp.MustCompile(`(?s)#\w*Status:\s*(.*?)\n\n`)
	litRe := regexp.MustCompile(`"([^"]+)"`)
	seen := map[string]bool{}
	out := []string{}
	for _, block := range defRe.FindAllStringSubmatch(schema, -1) {
		for _, lit := range litRe.FindAllStringSubmatch(block[1], -1) {
			if seen[lit[1]] {
				continue
			}
			seen[lit[1]] = true
			out = append(out, lit[1])
		}
	}
	return out
}

// RG-4: the registry's artifact entry resolves to a real file and declares a
// non-null instances pointer.
//
// Sweep finding #10 recorded existing entries pointing at core/schemas/<name>.cue
// while the files live at core/schemas/cue/<name>.cue. F7 must not add another.
func TestRG4_RegistryArtifactEntryResolves(t *testing.T) {
	coreRoot := locateCoreRoot()
	if coreRoot == "" {
		t.Skip("core repo not found - set WS_ORG_CORE")
	}
	registryPath := filepath.Join(coreRoot, "registry.yaml")
	content, err := os.ReadFile(registryPath)
	if err != nil {
		t.Skipf("registry.yaml not readable: %v", err)
	}

	var registry struct {
		Schemas map[string]struct {
			Version   string `yaml:"version"`
			Spec      string `yaml:"spec"`
			Instances any    `yaml:"instances"`
		} `yaml:"schemas"`
	}
	if err := yaml.Unmarshal(content, &registry); err != nil {
		t.Fatalf("RG-4: parse registry.yaml: %v", err)
	}

	entry, ok := registry.Schemas["artifact"]
	if !ok {
		t.Fatal("RG-4: registry.yaml has no schemas.artifact entry")
	}
	if entry.Spec == "" {
		t.Fatal("RG-4: schemas.artifact declares no spec path")
	}
	if entry.Instances == nil {
		t.Error("RG-4: schemas.artifact must declare a non-null instances pointer")
	}

	// spec paths are recorded relative to the parent of the core repo.
	resolved := filepath.Join(filepath.Dir(coreRoot), entry.Spec)
	if _, err := os.Stat(resolved); err != nil {
		t.Errorf("RG-4: schemas.artifact spec %q does not resolve (%s): %v", entry.Spec, resolved, err)
	}
}

// The five sibling entries flagged by sweep finding #10 must stay resolvable
// too, so the class does not silently return.
func TestRG4_AllRegistrySchemaSpecsResolve(t *testing.T) {
	coreRoot := locateCoreRoot()
	if coreRoot == "" {
		t.Skip("core repo not found - set WS_ORG_CORE")
	}
	content, err := os.ReadFile(filepath.Join(coreRoot, "registry.yaml"))
	if err != nil {
		t.Skipf("registry.yaml not readable: %v", err)
	}
	var registry struct {
		Schemas map[string]struct {
			Spec string `yaml:"spec"`
		} `yaml:"schemas"`
	}
	if err := yaml.Unmarshal(content, &registry); err != nil {
		t.Fatalf("parse registry.yaml: %v", err)
	}
	for name, entry := range registry.Schemas {
		if entry.Spec == "" {
			continue
		}
		resolved := filepath.Join(filepath.Dir(coreRoot), entry.Spec)
		if _, err := os.Stat(resolved); err != nil {
			t.Errorf("registry schema %q: spec %q does not resolve (%s)", name, entry.Spec, resolved)
		}
	}
}

func locateCoreRoot() string {
	candidates := []string{os.Getenv("WS_ORG_CORE")}
	if home, err := os.UserHomeDir(); err == nil {
		candidates = append(candidates, filepath.Join(home, "core"))
		candidates = append(candidates, filepath.Join(home, "code", "personal", "core"))
	}
	for _, c := range candidates {
		if c == "" {
			continue
		}
		if _, err := os.Stat(filepath.Join(c, "registry.yaml")); err == nil {
			return c
		}
	}
	return ""
}

func locateDocsRoot() string {
	candidates := []string{os.Getenv("CHAIN_DOCS_ROOT"), os.Getenv("WS_ORG_DOCS")}
	if home, err := os.UserHomeDir(); err == nil {
		candidates = append(candidates, filepath.Join(home, "docs"))
		candidates = append(candidates, filepath.Join(home, "code", "personal", "docs"))
	}
	for _, c := range candidates {
		if c == "" {
			continue
		}
		if _, err := os.Stat(filepath.Join(c, "epics")); err == nil {
			return c
		}
	}
	return ""
}
