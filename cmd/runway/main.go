package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/config"
	awss3 "github.com/aws/aws-sdk-go-v2/service/s3"
	awsssm "github.com/aws/aws-sdk-go-v2/service/ssm"
	"gopkg.in/yaml.v3"

	"github.com/blo-grindr/runabout/cmd/runway/internal/ingestion"
	"github.com/blo-grindr/runabout/cmd/runway/internal/scoring"
	"github.com/blo-grindr/runabout/cmd/runway/internal/serving"
	"github.com/blo-grindr/runabout/cmd/runway/internal/tailoring"
)

const usage = `Usage: runway match <jd-url>
       runway match --text <file>

Score a job description against resume.yaml and generate a tailored resume.

Flags:
  --text <file>      Read JD from file instead of Greenhouse URL (use "-" for stdin)
  --resume <file>    Path to resume.yaml (default: ~/.config/runway/resume.yaml)
  --schema <file>    Path to resume-schema.yaml for pykwalify validation
  --render-py <file> Path to render.py for variant serving (default: render.py)
  --override         Tailor even when verdict is do_not_apply
  --no-serve         Score and tailor but do not upload to S3
  --json             Output machine-readable JSON
  --model <model>    Override tailoring model (default: claude-sonnet-4-6)
`

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "runway: "+err.Error())
		os.Exit(1)
	}
}

func run(args []string) error {
	fs := flag.NewFlagSet("runway", flag.ContinueOnError)
	fs.Usage = func() { fmt.Fprint(os.Stderr, usage) }

	textFile := fs.String("text", "", "read JD from file (use - for stdin)")
	resumePath := fs.String("resume", defaultResumePath(), "path to resume.yaml")
	schemaPath := fs.String("schema", "", "path to resume-schema.yaml")
	renderPy := fs.String("render-py", "render.py", "path to render.py")
	override := fs.Bool("override", false, "tailor even on do_not_apply verdict")
	noServe := fs.Bool("no-serve", false, "skip S3 upload and variant serving")
	jsonOut := fs.Bool("json", false, "machine-readable JSON output")
	model := fs.String("model", "", "tailoring model override")

	if err := fs.Parse(args); err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))

	// --- F1: Ingest JD ---
	source, err := buildIngestSource(fs.Args(), *textFile)
	if err != nil {
		return err
	}

	ing := &ingestion.Ingester{
		HTTP: http.DefaultClient,
		LLM:  &ingestion.ClaudeLLMExtractor{},
	}
	jd, err := ing.IngestJD(ctx, source)
	if err != nil {
		return fmt.Errorf("ingest: %w", err)
	}
	if !*jsonOut {
		fmt.Fprintf(os.Stderr, "→ JD: %s at %s\n", jd.Title, jd.Company)
	}

	// --- Load resume.yaml ---
	resume, err := loadResume(*resumePath)
	if err != nil {
		return fmt.Errorf("load resume: %w", err)
	}

	// --- F2: Score ---
	scorer := &scoring.Scorer{
		LLM:    &scoring.ClaudeLLMScorer{},
		Logger: logger,
	}
	result, err := scorer.ScoreMatch(ctx, jd, resume)
	if err != nil {
		return fmt.Errorf("score: %w", err)
	}

	printScore(result, *jsonOut)

	if result.Verdict == scoring.VerdictDoNotApply && !*override {
		fmt.Fprintln(os.Stderr, "\n✗ Verdict: do_not_apply — skipping tailoring. Use --override to proceed anyway.")
		return nil
	}

	// --- F3: Tailor ---
	var validator tailoring.SchemaValidator = &noopValidator{}
	if *schemaPath != "" {
		validator = &tailoring.PyKwalifyValidator{SchemaPath: *schemaPath}
	}

	t := &tailoring.Tailorer{
		LLM:       &tailoring.ClaudeLLMTailor{Model: *model},
		Validator: validator,
	}
	tailored, err := t.TailorResume(ctx, tailoring.TailorOpts{
		Result:         result,
		JD:             jd,
		Resume:         resume,
		SourceYAMLPath: *resumePath,
		Override:       *override,
		Model:          *model,
	})
	if err != nil {
		return fmt.Errorf("tailor: %w", err)
	}
	if tailored.UsedFallback && !*jsonOut {
		fmt.Fprintln(os.Stderr, "⚠  Tailoring validation failed — using original resume")
	}
	if tailored.Diff.NothingChanged && !*jsonOut {
		fmt.Fprintln(os.Stderr, "✓  Strong fit — resume unchanged")
	}

	if *noServe {
		if *jsonOut {
			printJSON(map[string]any{"score": result.OverallScore, "verdict": result.Verdict, "yaml_path": tailored.YAMLPath})
		} else {
			fmt.Printf("tailored YAML: %s\n", tailored.YAMLPath)
		}
		return nil
	}

	// --- F4: Serve ---
	s3Client, ssmClient, awsErr := buildAWSClients(ctx)
	if awsErr != nil {
		if !*jsonOut {
			fmt.Fprintf(os.Stderr, "⚠  AWS not configured (%v) — skipping S3 upload\n", awsErr)
			fmt.Printf("tailored YAML: %s\n", tailored.YAMLPath)
		}
		return nil
	}

	storer := &serving.Storer{
		Render: &serving.ExecRenderPipeline{RenderPyPath: *renderPy},
		S3:     &serving.AWSS3Uploader{Client: s3Client},
		SSM:    &serving.AWSSSMWriter{Client: ssmClient},
		Logger: logger,
	}
	serveResult, err := storer.ServeVariant(ctx, tailored.Slug, tailored.YAMLPath, tailored.UsedFallback)
	if err != nil {
		return fmt.Errorf("serve: %w", err)
	}

	if *jsonOut {
		printJSON(map[string]any{
			"score":       result.OverallScore,
			"verdict":     result.Verdict,
			"slug":        serveResult.Slug,
			"url":         serveResult.URL,
			"expires_at":  serveResult.ExpiresAt.Format(time.RFC3339),
			"ssm_written": serveResult.SSMWritten,
		})
	} else {
		fmt.Printf("\n✓  Tailored resume: %s\n", serveResult.URL)
		fmt.Printf("   Expires: %s\n", serveResult.ExpiresAt.Format("2006-01-02"))
		if !serveResult.SSMWritten {
			fmt.Fprintln(os.Stderr, "⚠  SSM write failed — URL may not resolve. Run: runway register --slug "+serveResult.Slug)
		}
	}
	return nil
}

func buildIngestSource(positional []string, textFile string) (ingestion.IngestSource, error) {
	if textFile != "" {
		var data []byte
		var err error
		if textFile == "-" {
			data, err = os.ReadFile("/dev/stdin")
		} else {
			data, err = os.ReadFile(textFile)
		}
		if err != nil {
			return ingestion.IngestSource{}, fmt.Errorf("read %s: %w", textFile, err)
		}
		return ingestion.IngestSource{Text: strings.TrimSpace(string(data))}, nil
	}
	if len(positional) < 2 || positional[0] != "match" {
		return ingestion.IngestSource{}, fmt.Errorf("usage: runway match <jd-url> or runway match --text <file>")
	}
	return ingestion.IngestSource{URL: positional[1]}, nil
}

func loadResume(path string) (*scoring.Resume, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var r scoring.Resume
	if err := yaml.Unmarshal(data, &r); err != nil {
		return nil, err
	}
	if r.Summary == "" {
		return nil, fmt.Errorf("resume.yaml missing required field: summary")
	}
	if r.Skills == nil {
		return nil, fmt.Errorf("resume.yaml missing required field: skills")
	}
	return &r, nil
}

func printScore(result *scoring.MatchResult, jsonMode bool) {
	if jsonMode {
		return // printed at end with full result
	}
	verdict := strings.ToUpper(strings.ReplaceAll(string(result.Verdict), "_", " "))
	fmt.Printf("\nScore: %d/100  [%s]\n", result.OverallScore, verdict)
	fmt.Printf("  Skills: %d  Seniority: %d  Domain: %d  Title: %d\n",
		result.Dimensions.Skills, result.Dimensions.Seniority,
		result.Dimensions.Domain, result.Dimensions.Title)
	if len(result.Gaps) > 0 {
		n := 3
		if len(result.Gaps) < n {
			n = len(result.Gaps)
		}
		fmt.Printf("  Top gaps:      %s\n", strings.Join(result.Gaps[:n], " | "))
	}
	if len(result.Strengths) > 0 {
		n := 3
		if len(result.Strengths) < n {
			n = len(result.Strengths)
		}
		fmt.Printf("  Top strengths: %s\n", strings.Join(result.Strengths[:n], " | "))
	}
	fmt.Printf("  %s\n", result.VerdictReason)
}

func printJSON(v any) {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	_ = enc.Encode(v)
}

func defaultResumePath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return "resume.yaml"
	}
	return filepath.Join(home, ".config", "runway", "resume.yaml")
}

func buildAWSClients(ctx context.Context) (*awss3.Client, *awsssm.Client, error) {
	cfg, err := config.LoadDefaultConfig(ctx)
	if err != nil {
		return nil, nil, err
	}
	return awss3.NewFromConfig(cfg), awsssm.NewFromConfig(cfg), nil
}

// noopValidator passes all YAML files — used when --schema is not provided.
type noopValidator struct{}

func (n *noopValidator) Validate(_ context.Context, _ string) error { return nil }
