// Package ingestion implements F1 JD Ingestion: accepts a Greenhouse URL or
// raw text and returns a normalized JobDescription for scoring.
// See: docs/design/personal_20260508T114032Z_JobSearch_JD-Match-Intelligence_FDD.md § F1
package ingestion

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/thebrianlopez/runabout/cmd/runway/internal/scoring"
)

const maxWords = 5000

// IngestSource specifies the input for ingestion. URL and Text are mutually exclusive.
type IngestSource struct {
	URL  string // Greenhouse job board URL
	Text string // raw job description text (or file contents)
}

// IngestError is returned by Ingester on all failure paths.
type IngestError struct {
	Code       string // matches FDD error taxonomy
	URL        string // set for URL-based errors
	RetryAfter int    // seconds; set for rate-limit errors
	msg        string
}

func (e *IngestError) Error() string { return e.msg }

// Sentinel errors. Use errors.As to inspect Code and fields.
var (
	ErrJDNotFound          = &IngestError{Code: "ingest/not_found", msg: "job posting not found; it may have been removed"}
	ErrJDParseFailed       = &IngestError{Code: "ingest/parse_failed", msg: "could not parse job description; try --text"}
	ErrJDTooLong           = &IngestError{Code: "ingest/too_long", msg: fmt.Sprintf("job description too long (max %d words)", maxWords)}
	ErrGreenhouseRateLimit = &IngestError{Code: "ingest/rate_limit", msg: "greenhouse rate limit hit; retry later"}
)

// JDExtractor extracts structured JobDescription fields from raw text.
// The nil value must not be used — always inject a concrete implementation.
type JDExtractor interface {
	ExtractJD(ctx context.Context, text string) (*scoring.JobDescription, error)
}

// HTTPDoer is a minimal interface over *http.Client for testability.
type HTTPDoer interface {
	Do(req *http.Request) (*http.Response, error)
}

// Ingester orchestrates JD ingestion. Inject HTTP and LLM for testing.
type Ingester struct {
	HTTP              HTTPDoer
	LLM               JDExtractor
	GreenhouseAPIBase string // defaults to https://boards-api.greenhouse.io
}

func (ing *Ingester) apiBase() string {
	if ing.GreenhouseAPIBase != "" {
		return ing.GreenhouseAPIBase
	}
	return "https://boards-api.greenhouse.io"
}

// IngestJD routes to URL-based or text-based ingestion based on the source.
func (ing *Ingester) IngestJD(ctx context.Context, source IngestSource) (*scoring.JobDescription, error) {
	if source.URL != "" {
		return ing.ingestURL(ctx, source.URL)
	}
	return ing.ingestText(ctx, source.Text)
}

func (ing *Ingester) ingestURL(ctx context.Context, rawURL string) (*scoring.JobDescription, error) {
	boardToken, jobID, err := parseGreenhouseURL(rawURL)
	if err != nil {
		return nil, ErrJDParseFailed
	}

	company, err := ing.fetchBoardName(ctx, boardToken)
	if err != nil {
		company = boardToken // non-fatal; fall back to token as display name
	}

	title, content, err := ing.fetchJob(ctx, boardToken, jobID)
	if err != nil {
		return nil, err
	}

	if wordCount(content) > maxWords {
		return nil, ErrJDTooLong
	}

	jd, err := ing.LLM.ExtractJD(ctx, content)
	if err != nil {
		return nil, err
	}
	jd.Title = title
	jd.Company = company
	jd.SourceURL = rawURL
	jd.RawText = content
	return jd, nil
}

func (ing *Ingester) ingestText(ctx context.Context, text string) (*scoring.JobDescription, error) {
	if wordCount(text) > maxWords {
		return nil, ErrJDTooLong
	}
	jd, err := ing.LLM.ExtractJD(ctx, text)
	if err != nil {
		return nil, err
	}
	jd.RawText = text
	return jd, nil
}

func wordCount(s string) int {
	return len(strings.Fields(s))
}
