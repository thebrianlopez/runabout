package ingestion

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
)

// parseGreenhouseURL extracts the board token and numeric job ID from a
// standard Greenhouse job board URL.
// Supported pattern: https://boards.greenhouse.io/{board_token}/jobs/{job_id}
func parseGreenhouseURL(rawURL string) (boardToken, jobID string, err error) {
	re := regexp.MustCompile(`boards\.greenhouse\.io/([^/]+)/jobs/(\d+)`)
	m := re.FindStringSubmatch(rawURL)
	if m == nil {
		return "", "", fmt.Errorf("not a recognized Greenhouse job URL: %s", rawURL)
	}
	return m[1], m[2], nil
}

type greenhouseBoardResponse struct {
	Name string `json:"name"`
}

type greenhouseJobResponse struct {
	ID      int64  `json:"id"`
	Title   string `json:"title"`
	Content string `json:"content"` // HTML
}

// fetchBoardName fetches the company/board display name from the Greenhouse boards API.
func (ing *Ingester) fetchBoardName(ctx context.Context, boardToken string) (string, error) {
	url := fmt.Sprintf("%s/v1/boards/%s", ing.apiBase(), boardToken)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	resp, err := ing.HTTP.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("boards API returned %d", resp.StatusCode)
	}
	var board greenhouseBoardResponse
	if err := json.NewDecoder(resp.Body).Decode(&board); err != nil {
		return "", err
	}
	return board.Name, nil
}

// fetchJob fetches job title and plain-text content from the Greenhouse jobs API.
// Returns ErrJDNotFound on 404, ErrGreenhouseRateLimit on 429.
func (ing *Ingester) fetchJob(ctx context.Context, boardToken, jobID string) (title, content string, err error) {
	url := fmt.Sprintf("%s/v1/boards/%s/jobs/%s", ing.apiBase(), boardToken, jobID)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", "", err
	}
	resp, err := ing.HTTP.Do(req)
	if err != nil {
		return "", "", err
	}
	defer resp.Body.Close()

	switch resp.StatusCode {
	case http.StatusNotFound:
		return "", "", ErrJDNotFound
	case http.StatusTooManyRequests:
		e := &IngestError{
			Code:       ErrGreenhouseRateLimit.Code,
			msg:        ErrGreenhouseRateLimit.msg,
			RetryAfter: parseRetryAfter(resp.Header.Get("Retry-After")),
		}
		return "", "", e
	}
	if resp.StatusCode != http.StatusOK {
		return "", "", fmt.Errorf("jobs API returned %d", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", "", err
	}
	var job greenhouseJobResponse
	if err := json.Unmarshal(body, &job); err != nil {
		return "", "", ErrJDParseFailed
	}
	return job.Title, stripHTML(job.Content), nil
}

// stripHTML removes HTML tags and collapses whitespace.
func stripHTML(html string) string {
	tagRE := regexp.MustCompile(`<[^>]+>`)
	text := tagRE.ReplaceAllString(html, " ")
	// Decode common HTML entities
	text = strings.NewReplacer(
		"&amp;", "&",
		"&lt;", "<",
		"&gt;", ">",
		"&quot;", `"`,
		"&#39;", "'",
		"&nbsp;", " ",
	).Replace(text)
	// Collapse whitespace
	wsRE := regexp.MustCompile(`\s+`)
	return strings.TrimSpace(wsRE.ReplaceAllString(text, " "))
}

func parseRetryAfter(s string) int {
	n, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil {
		return 0
	}
	return n
}
