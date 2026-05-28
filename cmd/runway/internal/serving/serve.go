// Package serving implements F4 Variant Serving.
// See: docs/design/personal_20260508T115312Z_JobSearch_F4-VariantServing_TDD.md
package serving

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"regexp"
	"time"
)

const (
	defaultBucket    = "cdn.blo.la"
	defaultBaseURL   = "https://brianlopez.us"
	defaultSSMPrefix = "/runway/resume-variants"
	variantTTL       = 7 * 24 * time.Hour
	maxUploadRetries = 3
)

var slugRE = regexp.MustCompile(`^[a-z0-9-]+$`)

// variant describes one output format.
type variant struct {
	name string // e.g. "html"
	ext  string // S3 key suffix; empty for HTML (no extension)
}

var allVariants = []variant{
	{"html", ""},
	{"pdf", ".pdf"},
	{"json", ".json"},
	{"md", ".md"},
	{"txt", ".txt"},
	{"yaml", ".yaml"},
}

// ServeResult is returned by ServeVariant on success.
type ServeResult struct {
	Slug       string
	URL        string
	ExpiresAt  time.Time
	Formats    []string
	SSMWritten bool
}

// ServeError codes match TDD §4 taxonomy.
type ServeError struct {
	Code string
	msg  string
}

func (e *ServeError) Error() string { return e.msg }

var (
	ErrSlugInvalid    = &ServeError{Code: "serve/slug_invalid", msg: "slug contains invalid characters (must match [a-z0-9-])"}
	ErrRenderFailed   = &ServeError{Code: "serve/render_failed", msg: "resume render failed; run runway render --debug for logs"}
	ErrS3UploadFailed = &ServeError{Code: "serve/s3_upload_failed", msg: "upload to S3 failed after 3 attempts; retry with runway serve --slug"}
	ErrSSMWriteFailed = &ServeError{Code: "serve/ssm_write_failed", msg: "slug registration failed; resume uploaded but URL may not resolve until SSM is fixed"}
)

// RenderPipeline produces all 6 format files in outputDir from yamlPath.
// Output files are named resume-{slug}[.ext].
type RenderPipeline interface {
	Render(ctx context.Context, slug, yamlPath, outputDir string) error
}

// S3Uploader uploads a single object. PutObject is idempotent.
type S3Uploader interface {
	PutObject(ctx context.Context, bucket, key string, body io.Reader) error
}

// SSMWriter writes a single SSM String parameter with Overwrite=true.
type SSMWriter interface {
	PutParameter(ctx context.Context, name, value string) error
}

// ssmRecord is the JSON value stored in SSM for each slug.
type ssmRecord struct {
	CreatedAt string `json:"created_at"`
	ExpiresAt string `json:"expires_at"`
	S3Prefix  string `json:"s3_prefix"`
}

// Storer orchestrates F4: slug validation → render → S3 upload × 6 → SSM write.
type Storer struct {
	Render      RenderPipeline
	S3          S3Uploader
	SSM         SSMWriter
	Bucket      string           // default: cdn.blo.la
	BaseURL     string           // default: https://brianlopez.us
	SSMPrefix   string           // default: /runway/resume-variants
	Logger      *slog.Logger     // nil disables logging
	Now         func() time.Time // injectable for tests
	RetryDelays []time.Duration  // injectable for tests; default [1s,2s,4s]
}

func (st *Storer) bucket() string {
	if st.Bucket != "" {
		return st.Bucket
	}
	return defaultBucket
}

func (st *Storer) baseURL() string {
	if st.BaseURL != "" {
		return st.BaseURL
	}
	return defaultBaseURL
}

func (st *Storer) ssmPrefix() string {
	if st.SSMPrefix != "" {
		return st.SSMPrefix
	}
	return defaultSSMPrefix
}

func (st *Storer) now() time.Time {
	if st.Now != nil {
		return st.Now()
	}
	return time.Now().UTC()
}

func (st *Storer) retryDelays() []time.Duration {
	if len(st.RetryDelays) > 0 {
		return st.RetryDelays
	}
	return []time.Duration{time.Second, 2 * time.Second, 4 * time.Second}
}

func (st *Storer) log(ctx context.Context, level slog.Level, msg string, attrs ...slog.Attr) {
	if st.Logger != nil {
		st.Logger.LogAttrs(ctx, level, msg, attrs...)
	}
}

// ServeVariant renders all 6 format variants of the tailored resume and registers
// the slug in S3 + SSM. SSM write failure is a warning — S3 upload is durable.
//
// slug must match [a-z0-9-]+. yamlPath must point to a valid resume YAML file.
// usedFallback is informational (logged).
func (st *Storer) ServeVariant(ctx context.Context, slug, yamlPath string, usedFallback bool) (*ServeResult, error) {
	// CT-8: slug validation before any I/O
	if !slugRE.MatchString(slug) {
		return nil, &ServeError{Code: ErrSlugInvalid.Code, msg: fmt.Sprintf("invalid slug %q: must match [a-z0-9-]", slug)}
	}

	st.log(
		ctx, slog.LevelInfo, "serve.started",
		slog.String("slug", slug),
		slog.Bool("used_fallback", usedFallback),
	)

	outputDir, err := os.MkdirTemp("", "runway-serve-*")
	if err != nil {
		return nil, &ServeError{Code: ErrRenderFailed.Code, msg: fmt.Sprintf("failed to create render dir: %v", err)}
	}
	defer os.RemoveAll(outputDir)

	// CT-7: render is fatal — no S3 upload if it fails (RG-1)
	st.log(ctx, slog.LevelInfo, "serve.render_started", slog.String("slug", slug))
	if err := st.Render.Render(ctx, slug, yamlPath, outputDir); err != nil {
		st.log(
			ctx, slog.LevelError, "serve.failed",
			slog.String("slug", slug),
			slog.String("error_class", ErrRenderFailed.Code),
			slog.String("step", "render"),
		)
		return nil, &ServeError{Code: ErrRenderFailed.Code, msg: fmt.Sprintf("render failed: %v", err)}
	}
	st.log(ctx, slog.LevelInfo, "serve.render_completed", slog.String("slug", slug))

	s3Prefix := fmt.Sprintf("lambda/resume-%s", slug)

	// CT-1, CT-4, CT-5: upload 6 variants with retry
	if err := st.uploadAll(ctx, slug, s3Prefix, outputDir); err != nil {
		st.log(
			ctx, slog.LevelError, "serve.failed",
			slog.String("slug", slug),
			slog.String("error_class", ErrS3UploadFailed.Code),
			slog.String("step", "upload"),
		)
		return nil, err
	}

	// CT-3, CT-6: SSM write is warning-only — S3 state is durable (RG-2: SSM after uploads)
	now := st.now()
	expiresAt := now.Add(variantTTL)
	ssmWritten := true
	if err := st.writeSSM(ctx, slug, s3Prefix, now, expiresAt); err != nil {
		ssmWritten = false
		st.log(
			ctx, slog.LevelWarn, "serve.ssm_failed",
			slog.String("slug", slug),
			slog.String("error_class", ErrSSMWriteFailed.Code),
			slog.String("error", err.Error()),
		)
	}

	url := fmt.Sprintf("%s/resume/%s", st.baseURL(), slug)
	formatNames := make([]string, len(allVariants))
	for i, v := range allVariants {
		formatNames[i] = v.name
	}

	result := &ServeResult{
		Slug:       slug,
		URL:        url,
		ExpiresAt:  expiresAt,
		Formats:    formatNames,
		SSMWritten: ssmWritten,
	}
	st.log(
		ctx, slog.LevelInfo, "serve.completed",
		slog.String("slug", slug),
		slog.String("url", url),
		slog.Bool("ssm_written", ssmWritten),
	)
	return result, nil
}

// uploadAll uploads all 6 format variants sequentially. Returns on first fatal failure.
func (st *Storer) uploadAll(ctx context.Context, slug, s3Prefix, outputDir string) error {
	for _, v := range allVariants {
		localFile := fmt.Sprintf("%s/resume-%s%s", outputDir, slug, v.ext)
		s3Key := s3Prefix + v.ext
		if err := st.uploadWithRetry(ctx, slug, v.name, s3Key, localFile); err != nil {
			return err
		}
	}
	return nil
}

func (st *Storer) uploadWithRetry(ctx context.Context, slug, format, s3Key, localFile string) error {
	delays := st.retryDelays()
	var lastErr error

	for attempt := 1; attempt <= maxUploadRetries; attempt++ {
		f, err := os.Open(localFile)
		if err != nil {
			return &ServeError{Code: ErrRenderFailed.Code, msg: fmt.Sprintf("rendered file missing %s: %v", localFile, err)}
		}

		st.log(
			ctx, slog.LevelInfo, "serve.upload_started",
			slog.String("slug", slug),
			slog.String("format", format),
			slog.String("s3_key", s3Key),
		)

		putErr := st.S3.PutObject(ctx, st.bucket(), s3Key, f)
		f.Close()

		if putErr == nil {
			st.log(
				ctx, slog.LevelInfo, "serve.upload_completed",
				slog.String("slug", slug),
				slog.String("format", format),
				slog.Int("attempt", attempt),
			)
			return nil
		}

		lastErr = putErr
		st.log(
			ctx, slog.LevelWarn, "serve.upload_retry",
			slog.String("slug", slug),
			slog.String("format", format),
			slog.Int("attempt", attempt),
			slog.String("error", putErr.Error()),
		)

		if attempt < maxUploadRetries {
			delay := delays[attempt-1]
			if delay > 0 {
				select {
				case <-time.After(delay):
				case <-ctx.Done():
					return &ServeError{Code: ErrS3UploadFailed.Code, msg: "upload cancelled by context"}
				}
			}
		}
	}

	return &ServeError{Code: ErrS3UploadFailed.Code, msg: fmt.Sprintf("upload failed after %d attempts: %v", maxUploadRetries, lastErr)}
}

// writeSSM writes the slug registration record to SSM. Called after all S3 uploads succeed.
func (st *Storer) writeSSM(ctx context.Context, slug, s3Prefix string, createdAt, expiresAt time.Time) error {
	record := ssmRecord{
		CreatedAt: createdAt.Format(time.RFC3339),
		ExpiresAt: expiresAt.Format(time.RFC3339),
		S3Prefix:  s3Prefix,
	}
	val, err := json.Marshal(record)
	if err != nil {
		return err
	}
	paramName := fmt.Sprintf("%s/%s", st.ssmPrefix(), slug)
	return st.SSM.PutParameter(ctx, paramName, string(val))
}
