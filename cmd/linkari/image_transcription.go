package main

// EPIC-122: Image transcription — F1, F2, F3 core functions.
//
// F1: extractImageText — Claude Haiku vision pre-pass to transcribe visible text.
// F2: saveImageTranscript — persists transcript file + buildImageParts for parts enrichment.
// F3: shouldSuppressShortCircuit — pure predicate for personal-photo gate bypass.

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// imageTextExtractionSem is a semaphore enforcing max_concurrency=1 for the
// F1 vision pre-pass subprocess. This prevents concurrent claude calls from
// the transcription path from overwhelming the claude CLI.
var imageTextExtractionSem = make(chan struct{}, 1)

// imageShortCircuitBypassMinChars is the minimum extracted-text length (chars)
// required to suppress the personal-photo short-circuit instruction.
// Set from ServerConfig.ImageShortCircuitBypassMinChars at startup; defaults to 20.
var imageShortCircuitBypassMinChars = 20

// imageShortCircuitBypassMinCharsOnce guards the "missing config → WARNING" log.
var imageShortCircuitBypassMinCharsOnce sync.Once

// imageTextExtractionEnabled gates F1/F2/F3. When false, extractImageText is
// never called and scoreAsync falls through to the current metadata-only path.
// Set from ServerConfig.ImageTextExtractionEnabled at startup; defaults to false
// for safe rollout (feature must be explicitly enabled in config.toml).
var imageTextExtractionEnabled = false

// imageTextResultSchema is the --json-schema argument for the F1 pre-pass call.
const imageTextResultSchema = `{"type":"object","properties":{"text":{"type":"string","description":"All visible text transcribed exactly as it appears in the image. Empty string if no text visible."}},"required":["text"]}`

// imageTextResult is the structured JSON output from the F1 pre-pass claude call.
type imageTextResult struct {
	Text string `json:"text"`
}

// TranscriptMetadata holds provenance fields from the ShareRequest that are
// written into the transcript file YAML header and appended to the parts slice.
type TranscriptMetadata struct {
	CallingPackage string
	IsScreenshot   bool
	RelativePath   string
	URL            string
	MimeType       string
	Timestamp      time.Time
}

// extractImageText calls the claude CLI with --json-schema to transcribe all
// visible text from an image. It uses a 1-slot semaphore to enforce
// max_concurrency=1 per the F1 subprocess contract.
//
// Returns:
//   - (text, nil)   when text is extracted successfully
//   - ("", nil)     when the image contains no text (claude returns empty string)
//   - ("", err)     on CLI failure, timeout, or JSON parse failure
//
// Callers MUST check err before using text. A non-nil err signals a pipeline
// degradation — the caller should log the failure and fall through to the
// current metadata-only scoring path without retrying.
func extractImageText(ctx context.Context, imagePath string, visionModel string) (string, error) {
	// Acquire semaphore (max_concurrency=1).
	select {
	case imageTextExtractionSem <- struct{}{}:
		defer func() { <-imageTextExtractionSem }()
	case <-ctx.Done():
		return "", fmt.Errorf("extractImageText: context cancelled waiting for semaphore: %w", ctx.Err())
	}

	// Write the system prompt to a temp file (mirrors runClaudeHaikuJSON pattern).
	systemPrompt := `Transcribe all visible text from this image exactly as it appears. Return empty string if no text is visible.`
	spFile, _, err := writeSystemPromptFile(systemPrompt)
	if err != nil {
		return "", fmt.Errorf("extractImageText: write system prompt: %w", err)
	}
	defer os.Remove(spFile)

	// Build prompt that instructs claude to read the image file via the Read tool.
	// Format mirrors runClaudeHaikuVision so the tool-use turn cycle is identical.
	prompt := fmt.Sprintf("Read the image file at %s and transcribe all visible text.", imagePath)

	// Enforce 30s timeout per subprocess contract (the outer ctx may have a longer deadline).
	callCtx, callCancel := context.WithTimeout(ctx, 30*time.Second)
	defer callCancel()

	model := visionModel
	if model == "" {
		model = visionModelName
	}

	cmd := exec.CommandContext(callCtx, claudeBinaryPath, buildClaudeArgs(claudeExecOpts{
		Model:        model,
		MaxTurns:     "3", // Read tool needs 3 turns: invoke + result + final output
		AllowedTools: "Read",
		OutputFormat: "json",
		JSONSchema:   imageTextResultSchema,
		SystemPrompt: spFile,
	})...)
	cmd.Stdin = strings.NewReader(prompt)
	cmd.Dir = os.TempDir()
	cmd.Env = haikuEnv()

	var stdout, stderr strings.Builder
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if runErr := cmd.Run(); runErr != nil {
		return "", fmt.Errorf("extractImageText: claude exec: %w (stderr=%s)", runErr, strings.TrimSpace(stderr.String()))
	}

	out := strings.TrimSpace(stdout.String())
	if out == "" {
		return "", fmt.Errorf("extractImageText: claude returned empty output")
	}

	// Unwrap the --output-format json envelope: {"type":"result","result":"<json>","is_error":false,...}
	var envelope struct {
		Result  interface{} `json:"result"`
		IsError bool        `json:"is_error"`
	}
	if parseErr := json.Unmarshal([]byte(out), &envelope); parseErr != nil {
		return "", fmt.Errorf("extractImageText: envelope parse: %w", parseErr)
	}
	if envelope.IsError {
		return "", fmt.Errorf("extractImageText: claude returned is_error=true")
	}

	// result may be a JSON-encoded string or an already-decoded object.
	var resultBytes []byte
	switch v := envelope.Result.(type) {
	case string:
		resultBytes = []byte(v)
	default:
		// Re-marshal the decoded object back to JSON for uniform parsing.
		b, marshalErr := json.Marshal(v)
		if marshalErr != nil {
			return "", fmt.Errorf("extractImageText: re-marshal result: %w", marshalErr)
		}
		resultBytes = b
	}

	var res imageTextResult
	if parseErr := json.Unmarshal(resultBytes, &res); parseErr != nil {
		return "", fmt.Errorf("extractImageText: result parse: %w (raw=%s)", parseErr, string(resultBytes))
	}

	return strings.TrimSpace(res.Text), nil
}

// saveImageTranscript writes the extracted text to a markdown file with a YAML
// frontmatter header containing the transcript metadata.
//
// File path: transcriptsDir/YYYYMMDD_<rowID>_IMG_<slug>.md
// where slug is a URL-safe version of the filename basename.
//
// Returns ("", err) when:
//   - text is empty (caller contract violation)
//   - directory cannot be created
//   - file cannot be written
//
// On success returns the absolute path to the written file.
func saveImageTranscript(transcriptsDir string, rowID int64, filename string, text string, meta TranscriptMetadata) (string, error) {
	if len(text) == 0 {
		return "", fmt.Errorf("saveImageTranscript: text must not be empty")
	}

	if err := os.MkdirAll(transcriptsDir, 0o755); err != nil {
		return "", fmt.Errorf("saveImageTranscript: create dir: %w", err)
	}

	now := time.Now().UTC()
	if !meta.Timestamp.IsZero() {
		now = meta.Timestamp.UTC()
	}
	date := now.Format("20060102")

	slug := sanitizeTranscriptFilename(filepath.Base(filename))
	if slug == "" {
		slug = "untitled"
	}
	slug = strings.TrimSuffix(slug, filepath.Ext(slug))
	fname := fmt.Sprintf("%s_%d_IMG_%s.md", date, rowID, slug)
	path := filepath.Join(transcriptsDir, fname)

	var buf strings.Builder
	ts := now.Format("20060102T150405Z")
	fmt.Fprintf(&buf, "---\n")
	fmt.Fprintf(&buf, "timestamp: %q\n", ts)
	fmt.Fprintf(&buf, "row_id: %d\n", rowID)
	fmt.Fprintf(&buf, "source: %q\n", "img")
	if filename != "" {
		fmt.Fprintf(&buf, "original_filename: %q\n", filename)
	}
	if meta.MimeType != "" {
		fmt.Fprintf(&buf, "mime_type: %q\n", meta.MimeType)
	}
	if meta.CallingPackage != "" {
		fmt.Fprintf(&buf, "calling_package: %q\n", meta.CallingPackage)
	}
	fmt.Fprintf(&buf, "is_screenshot: %v\n", meta.IsScreenshot)
	if meta.RelativePath != "" {
		fmt.Fprintf(&buf, "relative_path: %q\n", meta.RelativePath)
	}
	if meta.URL != "" {
		fmt.Fprintf(&buf, "url: %q\n", meta.URL)
	}
	fmt.Fprintf(&buf, "---\n\n")
	buf.WriteString(text)
	buf.WriteString("\n")

	if err := os.WriteFile(path, []byte(buf.String()), 0o644); err != nil {
		return "", fmt.Errorf("saveImageTranscript: write file: %w", err)
	}
	return path, nil
}

// buildImageParts builds the content parts slice for an image share.
// It extends the 5-field base (ExtraSubject, ExtraText, Filename, MimeType,
// FileSize) with up to 5 provenance fields when they are non-empty.
//
// Parts added when non-empty:
//   - CallingPackage (conditional)
//   - RelativePath (conditional)
//   - IsScreenshot (always — boolean, always appended as "IsScreenshot: true/false")
//   - URL (conditional)
//   - TranscribedText (conditional — only when extractedText non-empty)
//
// Returns the parts slice. The caller joins with "\n" to build the scoring content.
func buildImageParts(req *ShareRequest, extractedText string) []string {
	var parts []string

	// 5 base fields (same as current scoreAsync metadata synthesis).
	if req.ExtraSubject != "" {
		parts = append(parts, "Subject: "+req.ExtraSubject)
	}
	if req.ExtraText != "" {
		parts = append(parts, "Text: "+req.ExtraText)
	}
	if req.Filename != "" {
		parts = append(parts, "Filename: "+req.Filename)
	}
	if req.MimeType != "" {
		parts = append(parts, "Type: "+req.MimeType)
	}
	if req.FileSize > 0 {
		parts = append(parts, fmt.Sprintf("FileSize: %d bytes", req.FileSize))
	}

	// Provenance fields — added only when extractedText is non-empty.
	if extractedText != "" {
		if req.CallingPackage != "" {
			parts = append(parts, "CallingPackage: "+req.CallingPackage)
		}
		if req.RelativePath != "" {
			parts = append(parts, "RelativePath: "+req.RelativePath)
		}
		// IsScreenshot always added (boolean, may be false).
		parts = append(parts, fmt.Sprintf("IsScreenshot: %v", req.IsScreenshot))
		if req.URL != "" {
			parts = append(parts, "URL: "+req.URL)
		}
		parts = append(parts, "TranscribedText: "+extractedText)
	}

	return parts
}

// shouldSuppressShortCircuit returns true when the extracted text length exceeds
// the threshold, indicating that the personal-photo short-circuit instruction
// should be omitted from the vision scoring system prompt.
//
// Pure function — no side effects.
func shouldSuppressShortCircuit(extractedText string, threshold int) bool {
	return len(extractedText) > threshold
}

// initImageTranscriptionConfig reads image transcription config from ServerConfig
// and sets the package-level vars. Called from initClaudeConfig.
func initImageTranscriptionConfig(cfg *ServerConfig) {
	if cfg == nil {
		return
	}
	imageTextExtractionEnabled = cfg.ImageTextExtractionEnabled
	if cfg.ImageShortCircuitBypassMinChars > 0 {
		imageShortCircuitBypassMinChars = cfg.ImageShortCircuitBypassMinChars
	} else if cfg.ImageShortCircuitBypassMinChars == 0 {
		// Absent/zero config key — log WARNING once per process lifetime.
		imageShortCircuitBypassMinCharsOnce.Do(func() {
			slog.Warn(
				"image_short_circuit_bypass_min_chars not configured — using default 20",
				"event_type", "image_short_circuit_bypass_default",
				"default", 20,
			)
		})
	}
}
