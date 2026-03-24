package effiscore

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"
)

// Client queries the Datadog Metrics API.
type Client struct {
	apiKey     string
	appKey     string
	httpClient *http.Client
	BaseURL    string
}

// NewClient returns a Client configured with DD credentials.
func NewClient(apiKey, appKey string) *Client {
	return &Client{
		apiKey:     apiKey,
		appKey:     appKey,
		httpClient: &http.Client{Timeout: 15 * time.Second},
		BaseURL:    "https://api.datadoghq.com",
	}
}

type ddQueryResponse struct {
	Series []ddSeries `json:"series"`
}

type ddSeries struct {
	Pointlist [][2]json.Number `json:"pointlist"`
}

// FetchMetric queries DD for a single metric and returns the summed scalar.
func (c *Client) FetchMetric(query string, from, to int64) (float64, error) {
	u := fmt.Sprintf("%s/api/v1/query?from=%d&to=%d&query=%s",
		c.BaseURL, from, to, url.QueryEscape(query))

	req, err := http.NewRequest("GET", u, nil)
	if err != nil {
		return 0, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("DD-API-KEY", c.apiKey)
	req.Header.Set("DD-APPLICATION-KEY", c.appKey)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return 0, fmt.Errorf("DD API request: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return 0, fmt.Errorf("read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("DD API returned %d: %s", resp.StatusCode, string(body))
	}

	var result ddQueryResponse
	if err := json.Unmarshal(body, &result); err != nil {
		return 0, fmt.Errorf("parse DD response: %w", err)
	}

	return sumPoints(result.Series), nil
}

// sumPoints sums all point values across all series.
func sumPoints(series []ddSeries) float64 {
	var total float64
	for _, s := range series {
		for _, pt := range s.Pointlist {
			if v, err := pt[1].Float64(); err == nil {
				total += v
			}
		}
	}
	return total
}

// FetchAll queries all 6 metrics for the given user and window.
// Returns RawMetrics, HealthStats for topology emission, and any error.
func (c *Client) FetchAll(user string, windowDays int) (RawMetrics, HealthStats, error) {
	now := time.Now().Unix()
	from := now - int64(windowDays)*86400

	type metricDef struct {
		query string
		dest  *float64
	}

	tag := fmt.Sprintf("user_name:%s", user)

	var r RawMetrics
	defs := []metricDef{
		{
			query: fmt.Sprintf("sum:anthropic.messages.cache_read_input_tokens{%s}.as_count()", tag),
			dest:  &r.CacheReadTokens,
		},
		{
			query: fmt.Sprintf("sum:anthropic.messages.input_tokens{%s}.as_count()", tag),
			dest:  &r.InputTokens,
		},
		{
			query: fmt.Sprintf("sum:anthropic.messages.output_tokens{%s}.as_count()", tag),
			dest:  &r.OutputTokens,
		},
		{
			query: fmt.Sprintf("sum:anthropic.messages.cache_creation.ephemeral_5m_input_tokens{%s}.as_count()", tag),
			dest:  &r.CacheWrite5mTokens,
		},
		{
			query: fmt.Sprintf("sum:anthropic.messages.cache_creation.ephemeral_1h_input_tokens{%s}.as_count()", tag),
			dest:  &r.CacheWrite1hTokens,
		},
		{
			query: fmt.Sprintf("sum:anthropic.messages.input_tokens{%s,model:*haiku*}.as_count()", tag),
			dest:  &r.HaikuInputTokens,
		},
	}

	var health HealthStats
	for _, d := range defs {
		v, err := c.FetchMetric(d.query, from, now)
		if err != nil {
			return RawMetrics{}, health, fmt.Errorf("query %q: %w", d.query, err)
		}
		health.MetricsReturned++
		if v == 0 {
			health.Nulls++
		}
		*d.dest = v
	}

	return r, health, nil
}
