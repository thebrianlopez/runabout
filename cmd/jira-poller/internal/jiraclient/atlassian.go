package jiraclient

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	jira "github.com/ctreminiom/go-atlassian/jira/v3"
	"github.com/ctreminiom/go-atlassian/pkg/infra/models"
)

const (
	maxRetries        = 3
	defaultRetryAfter = 60 * time.Second
	defaultTimeout    = 30 * time.Second
	defaultMaxResults = 100
)

// userAgentTransport wraps an http.RoundTripper to inject a User-Agent header.
type userAgentTransport struct {
	ua   string
	base http.RoundTripper
}

func (u *userAgentTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	r := req.Clone(req.Context())
	r.Header.Set("User-Agent", u.ua)
	return u.base.RoundTrip(r)
}

// atlassianClient implements Client using go-atlassian v3.
type atlassianClient struct {
	jc      *jira.Client
	sleepFn func(time.Duration) // injectable for tests; defaults to time.Sleep
}

// NewAtlassianClient constructs a production Client.
//   - baseURL:    Jira Cloud base URL, e.g. "https://your-org.atlassian.net"
//   - email:      Atlassian account email
//   - token:      Atlassian API token
//   - version:    service version string for User-Agent ("dev" during tests)
func NewAtlassianClient(baseURL, email, token, version string) (Client, error) {
	return newAtlassianClientInternal(baseURL, email, token, version, time.Sleep)
}

// newAtlassianClientWithSleep is the internal constructor that accepts a
// sleepFn for test injection. Tests pass a no-op to avoid real delays.
func newAtlassianClientInternal(baseURL, email, token, version string, sleepFn func(time.Duration)) (Client, error) {
	transport := &userAgentTransport{
		ua:   fmt.Sprintf("jira-transition-poller/%s", version),
		base: http.DefaultTransport,
	}
	httpClient := &http.Client{
		Timeout:   defaultTimeout,
		Transport: transport,
	}

	jc, err := jira.New(httpClient, baseURL)
	if err != nil {
		return nil, fmt.Errorf("jiraclient: init: %w", err)
	}
	jc.Auth.SetBasicAuth(email, token)

	return &atlassianClient{jc: jc, sleepFn: sleepFn}, nil
}

// NewAtlassianClientForTest constructs a client for httptest-based tests.
// The caller controls the http.Client (transport, timeout) and the sleepFn.
// Exported for use in _test packages only.
func NewAtlassianClientForTest(hc *http.Client, baseURL, version string, sleepFn func(time.Duration)) (Client, error) {
	// Wrap the test client's transport with the User-Agent injector.
	base := hc.Transport
	if base == nil {
		base = http.DefaultTransport
	}
	hc.Transport = &userAgentTransport{
		ua:   fmt.Sprintf("jira-transition-poller/%s", version),
		base: base,
	}

	jc, err := jira.New(hc, baseURL)
	if err != nil {
		return nil, fmt.Errorf("jiraclient: init: %w", err)
	}
	jc.Auth.SetBasicAuth("test@example.com", "test-token")

	return &atlassianClient{jc: jc, sleepFn: sleepFn}, nil
}

// SearchTransitions implements Client.
func (a *atlassianClient) SearchTransitions(ctx context.Context, req SearchRequest) (*SearchResult, error) {
	maxResults := req.MaxResults
	if maxResults == 0 {
		maxResults = defaultMaxResults
	}

	startAt, err := parseNextToken(req.NextToken)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrPagination, err)
	}

	jql := buildJQL(req.Projects, req.LookbackMinutes)
	fields := []string{"summary", "issuetype", "assignee", "labels", "changelog"}
	expands := []string{"changelog"}

	var (
		result *models.IssueSearchScheme
		resp   *models.ResponseScheme
	)

	for attempt := 0; attempt < maxRetries; attempt++ {
		result, resp, err = a.jc.Issue.Search.Post(ctx, jql, fields, expands, startAt, maxResults, "")
		if err == nil {
			break
		}

		// Classify the error from the response code.
		if resp == nil {
			// Network / timeout error.
			if isTimeoutErr(err) {
				return nil, fmt.Errorf("%w: %w", ErrTimeout, err)
			}
			return nil, fmt.Errorf("%w: %w", ErrUpstream, err)
		}

		code := resp.Code
		switch {
		case code == http.StatusUnauthorized || code == http.StatusForbidden:
			return nil, fmt.Errorf("%w: status %d", ErrAuthFailure, code)

		case code == http.StatusNotFound:
			return nil, fmt.Errorf("%w: status %d", ErrNotFound, code)

		case code == http.StatusTooManyRequests:
			if attempt >= maxRetries-1 {
				return nil, fmt.Errorf("%w: status %d after %d attempts", ErrRateLimited, code, attempt+1)
			}
			delay := retryAfterDelay(resp)
			a.sleepFn(delay)
			continue

		case code >= 500:
			if attempt >= maxRetries-1 {
				return nil, fmt.Errorf("%w: status %d after %d attempts", ErrUpstream, code, attempt+1)
			}
			delay := backoffDelay(attempt)
			a.sleepFn(delay)
			continue

		default:
			return nil, fmt.Errorf("%w: unexpected status %d", ErrUpstream, code)
		}
	}

	issues, err := mapIssues(result)
	if err != nil {
		return nil, err
	}

	nextToken := ""
	if result != nil && len(result.Issues) == maxResults {
		nextToken = strconv.Itoa(startAt + maxResults)
	}

	return &SearchResult{
		Issues:    issues,
		NextToken: nextToken,
	}, nil
}

// buildJQL constructs the JQL string for a status-change poll.
func buildJQL(projects []string, lookbackMinutes int) string {
	quoted := make([]string, len(projects))
	for i, p := range projects {
		quoted[i] = fmt.Sprintf("%q", p)
	}
	return fmt.Sprintf(
		"project IN (%s) AND status CHANGED AFTER \"-%dm\"",
		strings.Join(quoted, ", "),
		lookbackMinutes,
	)
}

// parseNextToken decodes the token. Empty → 0. Otherwise must be a non-negative int.
func parseNextToken(token string) (int, error) {
	if token == "" {
		return 0, nil
	}
	n, err := strconv.Atoi(token)
	if err != nil || n < 0 {
		return 0, fmt.Errorf("invalid nextToken %q", token)
	}
	return n, nil
}

// retryAfterDelay reads the Retry-After header, falling back to defaultRetryAfter.
// The value is capped at defaultRetryAfter (60s).
func retryAfterDelay(resp *models.ResponseScheme) time.Duration {
	if resp.Response != nil {
		if h := resp.Response.Header.Get("Retry-After"); h != "" {
			if secs, err := strconv.Atoi(h); err == nil && secs > 0 {
				d := time.Duration(secs) * time.Second
				if d > defaultRetryAfter {
					d = defaultRetryAfter
				}
				return d
			}
		}
	}
	return defaultRetryAfter
}

// backoffDelay returns an exponential back-off for attempt n (0-based).
// attempt=0 → 1s, attempt=1 → 2s, attempt=2 → 4s (capped at 10s).
func backoffDelay(attempt int) time.Duration {
	d := time.Duration(1<<uint(attempt)) * time.Second
	if d > 10*time.Second {
		d = 10 * time.Second
	}
	return d
}

// isTimeoutErr checks whether err looks like a net timeout.
func isTimeoutErr(err error) bool {
	type interface_ interface{ Timeout() bool }
	if t, ok := err.(interface_); ok {
		return t.Timeout()
	}
	return strings.Contains(err.Error(), "context deadline exceeded") ||
		strings.Contains(err.Error(), "timeout")
}

// mapIssues converts go-atlassian IssueScheme slices to our domain types.
func mapIssues(result *models.IssueSearchScheme) ([]Issue, error) {
	if result == nil {
		return []Issue{}, nil
	}
	out := make([]Issue, 0, len(result.Issues))
	for _, raw := range result.Issues {
		if raw == nil {
			continue
		}
		issue := Issue{
			Key:  raw.Key,
			Self: raw.Self,
		}
		if raw.Fields != nil {
			issue.Summary = raw.Fields.Summary
			if raw.Fields.IssueType != nil {
				issue.IssueType = raw.Fields.IssueType.Name
			}
			if raw.Fields.Assignee != nil {
				issue.Assignee = &User{
					AccountID:   raw.Fields.Assignee.AccountID,
					DisplayName: raw.Fields.Assignee.DisplayName,
					Email:       raw.Fields.Assignee.EmailAddress,
				}
			}
			if raw.Fields.Labels != nil {
				issue.Labels = raw.Fields.Labels
			} else {
				issue.Labels = []string{}
			}
		} else {
			issue.Labels = []string{}
		}
		if raw.Changelog != nil {
			issue.Changelog = mapChangelog(raw.Changelog.Histories)
		}
		out = append(out, issue)
	}
	return out, nil
}

// mapChangelog filters to status-field history items only.
func mapChangelog(histories []*models.IssueChangelogHistoryScheme) []ChangelogEntry {
	var entries []ChangelogEntry
	for _, h := range histories {
		if h == nil {
			continue
		}
		for _, item := range h.Items {
			if item == nil || item.Field != "status" {
				continue
			}
			t, _ := time.Parse(time.RFC3339, h.Created)
			author := User{}
			if h.Author != nil {
				author = User{
					AccountID:   h.Author.AccountID,
					DisplayName: h.Author.DisplayName,
					Email:       h.Author.EmailAddress,
				}
			}
			entries = append(entries, ChangelogEntry{
				HistoryID:  h.ID,
				Created:    t,
				Author:     author,
				FromStatus: item.FromString, // "" when null (initial creation)
				ToStatus:   item.ToString,
			})
		}
	}
	return entries
}
