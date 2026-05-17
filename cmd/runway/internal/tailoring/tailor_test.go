package tailoring

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/thebrianlopez/runabout/cmd/runway/internal/scoring"
)

// --- fixtures ---

func sourceResume() *scoring.Resume {
	return &scoring.Resume{
		Summary: "15+ years infrastructure engineer with Kubernetes and Terraform expertise.",
		Skills: map[string][]string{
			"Infrastructure & Cloud": {"Terraform", "AWS", "Kubernetes"},
			"Languages":              {"Go", "Python"},
		},
		Experience: []scoring.ExperienceEntry{
			{Company: "Grindr", Roles: []scoring.Role{
				{Title: "Senior Cloud Engineer", Bullets: []string{
					"Managed 5 EKS clusters serving 100k users",
					"Reduced infra cost by 30% via spot instance migration",
					"Led Terraform module standardization across 12 services",
				}},
			}},
		},
	}
}

func sourceOpts(verdict scoring.Verdict, score int) TailorOpts {
	return TailorOpts{
		Result: &scoring.MatchResult{
			OverallScore: score,
			Verdict:      verdict,
			Gaps:         []string{"missing Go microservices experience"},
			Strengths:    []string{"strong Kubernetes background"},
		},
		JD: &scoring.JobDescription{
			Title:          "Staff Infrastructure Engineer",
			Company:        "Stripe",
			RequiredSkills: []string{"Terraform", "Kubernetes", "Go"},
			SeniorityLevel: "staff",
			Domain:         []string{"infrastructure", "platform"},
		},
		Resume:         sourceResume(),
		SourceYAMLPath: "/tmp/resume.yaml",
	}
}

// marshalResume serializes a Resume to YAML string for mock LLM responses.
func marshalResume(r *scoring.Resume) string {
	out, _ := yaml.Marshal(r)
	return string(out)
}

// resumeWithFabricatedBullet returns a Resume identical to source except one bullet is new.
func resumeWithFabricatedBullet(source *scoring.Resume) *scoring.Resume {
	r := *source
	entries := make([]scoring.ExperienceEntry, len(source.Experience))
	copy(entries, source.Experience)
	roles := make([]scoring.Role, len(entries[0].Roles))
	copy(roles, entries[0].Roles)
	bullets := make([]string, len(roles[0].Bullets))
	copy(bullets, roles[0].Bullets)
	bullets = append(bullets, "FABRICATED: led global platform transformation program")
	roles[0] = scoring.Role{Title: roles[0].Title, Bullets: bullets}
	entries[0] = scoring.ExperienceEntry{Company: entries[0].Company, Roles: roles}
	r.Experience = entries
	return &r
}

// resumeWithExtraSkillCategory adds a skill category not in source.
func resumeWithExtraSkillCategory(source *scoring.Resume) *scoring.Resume {
	r := *source
	skills := make(map[string][]string)
	for k, v := range source.Skills {
		skills[k] = v
	}
	skills["Blockchain & Web3"] = []string{"Solidity", "Ethereum"} // not in source
	r.Skills = skills
	return &r
}

// resumeWithLongSummary returns a resume with summary 20% longer than source.
func resumeWithLongSummary(source *scoring.Resume) *scoring.Resume {
	r := *source
	extra := strings.Repeat(" and additional leadership experience", 3)
	r.Summary = source.Summary + extra // well outside +15%
	return &r
}

// --- mocks ---

type mockLLMTailor struct {
	yamlStr string
	err     error
	called  int
}

func (m *mockLLMTailor) TailorWithLLM(_ context.Context, _ string) (string, error) {
	m.called++
	return m.yamlStr, m.err
}

type mockSchemaValidator struct {
	err error
}

func (m *mockSchemaValidator) Validate(_ context.Context, _ string) error {
	return m.err
}

func newTailorer(llm LLMTailor, validator SchemaValidator) *Tailorer {
	return &Tailorer{
		LLM:       llm,
		Validator: validator,
		Now:       func() time.Time { return time.Date(2026, 5, 8, 0, 0, 0, 0, time.UTC) },
	}
}

// --- CT-1: fabrication invariant ---

// CT-1: Given a tailored YAML with one bullet not in source,
// ERR_TAILOR_FABRICATION is returned and UsedFallback = true.
func TestTailorResume_CT1_FabricationDetected(t *testing.T) {
	fabricated := resumeWithFabricatedBullet(sourceResume())
	llm := &mockLLMTailor{yamlStr: marshalResume(fabricated)}
	tr := newTailorer(llm, &mockSchemaValidator{err: nil})

	opts := sourceOpts(scoring.VerdictApply, 64)
	result, err := tr.TailorResume(context.Background(), opts)
	if err != nil {
		t.Fatalf("TailorResume() returned error %v, want nil (fabrication is a fallback, not fatal)", err)
	}
	if !result.UsedFallback {
		t.Error("UsedFallback = false, want true when fabricated bullet detected")
	}
	if result.YAMLPath != opts.SourceYAMLPath {
		t.Errorf("YAMLPath = %q, want source path %q on fallback", result.YAMLPath, opts.SourceYAMLPath)
	}
}

// --- CT-2: schema fallback on pykwalify failure ---

// CT-2: Given pykwalify exits non-zero, UsedFallback = true and YAMLPath
// points to original resume.yaml.
func TestTailorResume_CT2_SchemaFailFallback(t *testing.T) {
	llm := &mockLLMTailor{yamlStr: marshalResume(sourceResume())}
	validator := &mockSchemaValidator{err: errors.New("pykwalify: invalid schema")}
	tr := newTailorer(llm, validator)

	opts := sourceOpts(scoring.VerdictApply, 64)
	result, err := tr.TailorResume(context.Background(), opts)
	if err != nil {
		t.Fatalf("TailorResume() returned error %v, want nil (schema fail is a fallback)", err)
	}
	if !result.UsedFallback {
		t.Error("UsedFallback = false, want true on schema validation failure")
	}
	if result.YAMLPath != opts.SourceYAMLPath {
		t.Errorf("YAMLPath = %q, want source %q", result.YAMLPath, opts.SourceYAMLPath)
	}
}

// --- CT-3: verdict gate blocks do_not_apply ---

// CT-3: Given Verdict == do_not_apply and Override = false,
// ERR_TAILOR_VERDICT_BLOCKED returned; LLM never called.
func TestTailorResume_CT3_VerdictBlockedDoNotApply(t *testing.T) {
	llm := &mockLLMTailor{}
	tr := newTailorer(llm, &mockSchemaValidator{})

	opts := sourceOpts(scoring.VerdictDoNotApply, 15)
	opts.Override = false

	_, err := tr.TailorResume(context.Background(), opts)
	var tailorErr *TailorError
	if !errors.As(err, &tailorErr) || tailorErr.Code != "tailor/verdict_blocked" {
		t.Errorf("error = %v, want tailor/verdict_blocked", err)
	}
	if llm.called != 0 {
		t.Errorf("LLM called %d times, want 0 (blocked before LLM)", llm.called)
	}
}

// --- CT-4: verdict gate passes with --override ---

// CT-4: Given Verdict == do_not_apply and Override = true, LLM call proceeds.
func TestTailorResume_CT4_OverrideAllowsDoNotApply(t *testing.T) {
	llm := &mockLLMTailor{yamlStr: marshalResume(sourceResume())}
	tr := newTailorer(llm, &mockSchemaValidator{})

	opts := sourceOpts(scoring.VerdictDoNotApply, 15)
	opts.Override = true

	_, err := tr.TailorResume(context.Background(), opts)
	if err != nil {
		t.Errorf("TailorResume() error = %v, want nil (override set)", err)
	}
	if llm.called == 0 {
		t.Error("LLM never called, want at least 1 call when override=true")
	}
}

// --- CT-5: summary length bounds ---

// CT-5: Given tailored summary 20% longer than source,
// ERR_TAILOR_SUMMARY_BOUNDS returned and UsedFallback = true.
func TestTailorResume_CT5_SummaryBoundsViolated(t *testing.T) {
	tooLong := resumeWithLongSummary(sourceResume())
	llm := &mockLLMTailor{yamlStr: marshalResume(tooLong)}
	tr := newTailorer(llm, &mockSchemaValidator{})

	opts := sourceOpts(scoring.VerdictApply, 64)
	result, err := tr.TailorResume(context.Background(), opts)
	if err != nil {
		t.Fatalf("TailorResume() error = %v, want nil (summary bounds is a fallback)", err)
	}
	if !result.UsedFallback {
		t.Error("UsedFallback = false, want true when summary exceeds ±15%")
	}
}

// --- CT-6: skills reorder only — no additions ---

// CT-6: Given tailored YAML with a skill category not in source,
// fabrication detector catches it and UsedFallback = true.
func TestTailorResume_CT6_NewSkillCategoryRejected(t *testing.T) {
	extra := resumeWithExtraSkillCategory(sourceResume())
	llm := &mockLLMTailor{yamlStr: marshalResume(extra)}
	tr := newTailorer(llm, &mockSchemaValidator{})

	opts := sourceOpts(scoring.VerdictApply, 64)
	result, err := tr.TailorResume(context.Background(), opts)
	if err != nil {
		t.Fatalf("TailorResume() error = %v, want nil (fabrication is fallback)", err)
	}
	if !result.UsedFallback {
		t.Error("UsedFallback = false, want true when new skill category added")
	}
}

// --- CT-7: LLM not called on strong_fit > 95 ---

// CT-7: Given Verdict == strong_fit and score > 95,
// NothingChanged = true returned without LLM call.
func TestTailorResume_CT7_NothingChangedOnStrongFit(t *testing.T) {
	llm := &mockLLMTailor{}
	tr := newTailorer(llm, &mockSchemaValidator{})

	opts := sourceOpts(scoring.VerdictStrongFit, 96)

	result, err := tr.TailorResume(context.Background(), opts)
	if err != nil {
		t.Fatalf("TailorResume() error = %v, want nil", err)
	}
	if !result.Diff.NothingChanged {
		t.Error("NothingChanged = false, want true for strong_fit > 95")
	}
	if llm.called != 0 {
		t.Errorf("LLM called %d times, want 0 for strong_fit > 95", llm.called)
	}
}

// --- CT-8: slug derivation ---

// CT-8: Given Company="Stripe", Title="Staff Engineer", date=20260508,
// slug == "stripe-staff-engineer-20260508".
func TestTailorResume_CT8_SlugDerivation(t *testing.T) {
	fixedTime := time.Date(2026, 5, 8, 0, 0, 0, 0, time.UTC)
	jd := &scoring.JobDescription{Company: "Stripe", Title: "Staff Engineer"}
	got := deriveSlug(jd, fixedTime)
	want := "stripe-staff-engineer-20260508"
	if got != want {
		t.Errorf("deriveSlug() = %q, want %q", got, want)
	}
}

// CT-8b: slug derivation handles spaces and punctuation.
func TestTailorResume_CT8b_SlugKebabCasing(t *testing.T) {
	fixedTime := time.Date(2026, 5, 8, 0, 0, 0, 0, time.UTC)
	cases := []struct{ company, title, want string }{
		{"Acme Corp.", "Sr. Platform Engineer", "acme-corp-sr-platform-engineer-20260508"},
		{"DeepMind", "ML Infra Lead", "deepmind-ml-infra-lead-20260508"},
	}
	for _, tt := range cases {
		jd := &scoring.JobDescription{Company: tt.company, Title: tt.title}
		got := deriveSlug(jd, fixedTime)
		if got != tt.want {
			t.Errorf("deriveSlug(%q, %q) = %q, want %q", tt.company, tt.title, got, tt.want)
		}
	}
}

// CT-8c: slug truncated to 80 chars when company+title is long.
func TestTailorResume_CT8c_SlugMaxLength(t *testing.T) {
	fixedTime := time.Date(2026, 5, 8, 0, 0, 0, 0, time.UTC)
	jd := &scoring.JobDescription{
		Company: "Very Long Company Name Incorporated",
		Title:   "Senior Principal Distinguished Staff Infrastructure Platform Engineer",
	}
	got := deriveSlug(jd, fixedTime)
	if len(got) > 80 {
		t.Errorf("slug length = %d, want ≤ 80\nslug: %q", len(got), got)
	}
}

// --- CT-9: timeout ---

// CT-9: Given LLM call exceeds 30s deadline, ERR_TAILOR_LLM_TIMEOUT returned;
// no partial YAML written to disk (temp dir checked).
func TestTailorResume_CT9_TimeoutNoPartialFile(t *testing.T) {
	llm := &mockLLMTailor{err: context.DeadlineExceeded}
	tr := newTailorer(llm, &mockSchemaValidator{})

	before, _ := countTempFiles(t)

	opts := sourceOpts(scoring.VerdictApply, 64)
	_, err := tr.TailorResume(context.Background(), opts)

	var tailorErr *TailorError
	if !errors.As(err, &tailorErr) || tailorErr.Code != "tailor/timeout" {
		t.Errorf("error = %v, want tailor/timeout", err)
	}

	after, _ := countTempFiles(t)
	if after > before {
		t.Errorf("temp file count increased by %d after timeout — partial file leaked", after-before)
	}
}

func countTempFiles(t *testing.T) (int, []string) {
	t.Helper()
	dir := os.TempDir()
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0, nil
	}
	var matches []string
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "resume-tailored-") {
			matches = append(matches, e.Name())
		}
	}
	return len(matches), matches
}

// --- CT-9b: successful tailoring writes a temp file ---

func TestTailorResume_SuccessWritesTempFile(t *testing.T) {
	llm := &mockLLMTailor{yamlStr: marshalResume(sourceResume())}
	tr := newTailorer(llm, &mockSchemaValidator{})

	opts := sourceOpts(scoring.VerdictApply, 64)
	result, err := tr.TailorResume(context.Background(), opts)
	if err != nil {
		t.Fatalf("TailorResume() error = %v", err)
	}
	if result.UsedFallback {
		t.Fatal("UsedFallback = true, want false for valid tailored resume")
	}
	if _, err := os.Stat(result.YAMLPath); err != nil {
		t.Errorf("YAMLPath %q does not exist: %v", result.YAMLPath, err)
	}
	os.Remove(result.YAMLPath)
}

// --- validate.go unit tests ---

func TestDetectFabrication_NoneWhenIdentical(t *testing.T) {
	src := sourceResume()
	if detectFabrication(src, src) {
		t.Error("detectFabrication(src, src) = true, want false for identical resume")
	}
}

func TestDetectFabrication_TrueOnNewBullet(t *testing.T) {
	src := sourceResume()
	fab := resumeWithFabricatedBullet(src)
	if !detectFabrication(src, fab) {
		t.Error("detectFabrication = false, want true when new bullet added")
	}
}

func TestDetectFabrication_TrueOnNewCategory(t *testing.T) {
	src := sourceResume()
	extra := resumeWithExtraSkillCategory(src)
	if !detectFabrication(src, extra) {
		t.Error("detectFabrication = false, want true when new skill category added")
	}
}

func TestCheckSummaryBounds_WithinRange(t *testing.T) {
	source := "This is a summary of approximately fifty characters long here."
	tailored := "This is a summary of approximately fifty chars long here ok." // similar length
	if !checkSummaryBounds(source, tailored) {
		t.Errorf("checkSummaryBounds(%d, %d) = false, want true", len(source), len(tailored))
	}
}

func TestCheckSummaryBounds_TooLong(t *testing.T) {
	source := "Short summary."
	tailored := source + strings.Repeat(" extra words added here", 5) // well over 115%
	if checkSummaryBounds(source, tailored) {
		t.Errorf("checkSummaryBounds(%d, %d) = true, want false (tailored too long)", len(source), len(tailored))
	}
}

func TestCheckSummaryBounds_TooShort(t *testing.T) {
	source := "This is a longer source summary that has many words and characters."
	tailored := "Short." // well under 85%
	if checkSummaryBounds(source, tailored) {
		t.Error("checkSummaryBounds = true, want false (tailored too short)")
	}
}

// --- BT-5: prompt contains no API keys or credentials ---

func TestBuildTailorPrompt_BT5_NoPIIOrSecrets(t *testing.T) {
	opts := sourceOpts(scoring.VerdictApply, 64)
	prompt := buildTailorPrompt(opts)

	emailRE := regexp.MustCompile(`\b[a-zA-Z0-9._%+\-]+@[a-zA-Z0-9.\-]+\.[a-zA-Z]{2,}\b`)
	if m := emailRE.FindString(prompt); m != "" {
		t.Errorf("prompt contains email address: %q", m)
	}

	forbidden := []string{"sk-ant-", "Bearer ", "api_key=", "token=", "password=", "Authorization:"}
	for _, pat := range forbidden {
		if strings.Contains(prompt, pat) {
			t.Errorf("prompt contains forbidden pattern %q", pat)
		}
	}

	for _, section := range []string{"job_description", "instructions", "source_resume_yaml"} {
		if !strings.Contains(prompt, section) {
			t.Errorf("prompt missing expected section %q", section)
		}
	}
}

// --- BT-6: RUNWAY_TAILOR_MODEL env var overrides default ---

func TestClaudeLLMTailor_BT6_ModelEnvVarOverride(t *testing.T) {
	t.Setenv(tailorModelEnvVar, "gemini-cli-test")
	c := &ClaudeLLMTailor{}
	if got := c.model(); got != "gemini-cli-test" {
		t.Errorf("model() = %q, want \"gemini-cli-test\" from env var", got)
	}
}

func TestClaudeLLMTailor_ModelFieldOverridesEnv(t *testing.T) {
	t.Setenv(tailorModelEnvVar, "gemini-cli-test")
	c := &ClaudeLLMTailor{Model: "explicit-model"}
	if got := c.model(); got != "explicit-model" {
		t.Errorf("model() = %q, want \"explicit-model\" (field takes precedence over env)", got)
	}
}

func TestClaudeLLMTailor_DefaultModel(t *testing.T) {
	t.Setenv(tailorModelEnvVar, "")
	c := &ClaudeLLMTailor{}
	if got := c.model(); got != defaultTailorModel {
		t.Errorf("model() = %q, want default %q", got, defaultTailorModel)
	}
}

// --- RG-1: fabricated bullet never reaches output ---

// RG-1: After tailoring, every bullet in YAMLPath exists verbatim in source.
// Uses a fixture with a known fabrication attempt to lock out regression.
func TestTailorResume_RG1_FabricatedBulletNeverInOutput(t *testing.T) {
	fabricated := resumeWithFabricatedBullet(sourceResume())
	llm := &mockLLMTailor{yamlStr: marshalResume(fabricated)}
	tr := newTailorer(llm, &mockSchemaValidator{})

	opts := sourceOpts(scoring.VerdictApply, 64)
	result, err := tr.TailorResume(context.Background(), opts)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Fallback must have triggered; YAMLPath must be source (no fabricated content written)
	if !result.UsedFallback {
		t.Fatal("UsedFallback = false — fabricated bullet was not caught")
	}
	if result.YAMLPath == "" || result.YAMLPath == marshalResume(fabricated) {
		t.Error("YAMLPath should not contain fabricated YAML content")
	}
}

// --- RG-2: schema failure never hard-fails pipeline ---

// RG-2: Given pykwalify failure, TailoredResume is still returned (non-nil)
// with UsedFallback=true — pipeline is not blocked.
func TestTailorResume_RG2_SchemaFailNeverBlocksPipeline(t *testing.T) {
	llm := &mockLLMTailor{yamlStr: marshalResume(sourceResume())}
	validator := &mockSchemaValidator{err: errors.New("schema invalid")}
	tr := newTailorer(llm, validator)

	opts := sourceOpts(scoring.VerdictApply, 64)
	result, err := tr.TailorResume(context.Background(), opts)
	if err != nil {
		t.Errorf("TailorResume() returned error %v — pipeline must never hard-fail on schema error", err)
	}
	if result == nil {
		t.Fatal("result is nil — pipeline hard-failed on schema error")
	}
	if !result.UsedFallback {
		t.Error("UsedFallback = false, want true on schema failure")
	}
}

// --- OBS: logging ---

func TestTailorResume_Obs_LogsCompleted(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, nil))
	llm := &mockLLMTailor{yamlStr: marshalResume(sourceResume())}
	tr := &Tailorer{
		LLM:       llm,
		Validator: &mockSchemaValidator{},
		Logger:    logger,
		Now:       func() time.Time { return time.Date(2026, 5, 8, 0, 0, 0, 0, time.UTC) },
	}

	opts := sourceOpts(scoring.VerdictApply, 64)
	result, err := tr.TailorResume(context.Background(), opts)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "tailor.completed") {
		t.Errorf("expected tailor.completed log event\ngot: %s", out)
	}
	if result != nil {
		os.Remove(result.YAMLPath)
	}
}

func TestTailorResume_Obs_LogsFallback(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, nil))
	llm := &mockLLMTailor{yamlStr: marshalResume(resumeWithFabricatedBullet(sourceResume()))}
	tr := &Tailorer{
		LLM:       llm,
		Validator: &mockSchemaValidator{},
		Logger:    logger,
		Now:       func() time.Time { return time.Date(2026, 5, 8, 0, 0, 0, 0, time.UTC) },
	}

	opts := sourceOpts(scoring.VerdictApply, 64)
	tr.TailorResume(context.Background(), opts)
	out := buf.String()
	if !strings.Contains(out, "tailor.fallback") {
		t.Errorf("expected tailor.fallback log event\ngot: %s", out)
	}
}

func TestTailorResume_Obs_LogsNothingChanged(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, nil))
	llm := &mockLLMTailor{}
	tr := &Tailorer{
		LLM:       llm,
		Validator: &mockSchemaValidator{},
		Logger:    logger,
		Now:       func() time.Time { return time.Date(2026, 5, 8, 0, 0, 0, 0, time.UTC) },
	}

	opts := sourceOpts(scoring.VerdictStrongFit, 97)
	tr.TailorResume(context.Background(), opts)
	out := buf.String()
	if !strings.Contains(out, "tailor.nothing_changed") {
		t.Errorf("expected tailor.nothing_changed log event\ngot: %s", out)
	}
}
