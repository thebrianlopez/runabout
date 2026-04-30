package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"golang.org/x/oauth2"
)

var (
	ErrGoogleAuth           = errors.New("google_auth_error")
	ErrGoogleNotFound       = errors.New("google_not_found")
	ErrGoogleQuotaExceeded  = errors.New("google_quota_exceeded")
	ErrUnsupportedGoogleURL = errors.New("google_unsupported_url")
	ErrGoogleTokenInvalid   = errors.New("google_token_invalid")
	ErrGoogleUnexpected     = errors.New("google_unexpected")
)

// compile-time assertion
var _ DomainClient = (*GoogleAPIsClient)(nil)

// GoogleService identifies the Google product targeted by a URL.
type GoogleService int

const (
	GoogleDrive GoogleService = iota
)

// GoogleAPIsClient fetches Google Drive files via the Drive v3 API.
type GoogleAPIsClient struct {
	ts      oauth2.TokenSource
	client  *http.Client // injected for tests; nil = build from ts
	apiBase string       // injectable base URL; "" = "https://www.googleapis.com"
	onEvent func(Event)
}

// NewGoogleAPIsClient creates a client authenticated via a serialized oauth2.Token JSON.
func NewGoogleAPIsClient(clientID, clientSecret, tokenJSON string) (*GoogleAPIsClient, error) {
	var token oauth2.Token
	if err := json.Unmarshal([]byte(tokenJSON), &token); err != nil || token.AccessToken == "" {
		return nil, ErrGoogleTokenInvalid
	}
	cfg := &oauth2.Config{
		ClientID:     clientID,
		ClientSecret: clientSecret,
		Scopes:       []string{"https://www.googleapis.com/auth/drive.readonly"},
	}
	ts := cfg.TokenSource(context.Background(), &token)
	return &GoogleAPIsClient{ts: ts}, nil
}

// EmitVia wires a production EventLogger into the client's event pipeline.
func (c *GoogleAPIsClient) EmitVia(logger *EventLogger) *GoogleAPIsClient {
	if logger == nil {
		return c
	}
	c.onEvent = func(e Event) {
		_ = logger.Emit(e.EventType, e.Metadata)
	}
	return c
}

func (c *GoogleAPIsClient) base() string {
	if c.apiBase != "" {
		return c.apiBase
	}
	return "https://www.googleapis.com"
}

func (c *GoogleAPIsClient) httpClient(ctx context.Context) *http.Client {
	if c.client != nil {
		return c.client
	}
	return oauth2.NewClient(ctx, c.ts)
}

// ParseGoogleURL extracts the Drive resource ID from Google Drive / Docs URLs.
func ParseGoogleURL(u *url.URL) (GoogleService, string, error) {
	host := u.Hostname()
	path := u.Path

	extractID := func(prefix string) (string, bool) {
		rest, ok := strings.CutPrefix(path, prefix)
		if !ok {
			return "", false
		}
		id := strings.SplitN(rest, "/", 2)[0]
		return id, id != ""
	}

	switch host {
	case "drive.google.com":
		if id, ok := extractID("/file/d/"); ok {
			return GoogleDrive, id, nil
		}
	case "docs.google.com":
		for _, prefix := range []string{"/document/d/", "/spreadsheets/d/", "/presentation/d/"} {
			if id, ok := extractID(prefix); ok {
				return GoogleDrive, id, nil
			}
		}
	}
	return 0, "", ErrUnsupportedGoogleURL
}

// Fetch implements DomainClient.
func (c *GoogleAPIsClient) Fetch(ctx context.Context, u *url.URL) (string, ContentType, error) {
	_, fileID, err := ParseGoogleURL(u)
	if err != nil {
		return "", ContentTypePlain, err
	}
	content, err := c.FetchDriveFile(ctx, fileID)
	return content, ContentTypePlain, err
}

type driveFileMeta struct {
	MimeType string `json:"mimeType"`
}

// FetchDriveFile retrieves a Drive file's text content.
func (c *GoogleAPIsClient) FetchDriveFile(ctx context.Context, fileID string) (string, error) {
	hc := c.httpClient(ctx)

	// Step 1: get metadata to determine mimeType.
	metaURL := fmt.Sprintf("%s/drive/v3/files/%s?fields=id,name,mimeType", c.base(), fileID)
	req, err := http.NewRequestWithContext(ctx, "GET", metaURL, nil)
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrGoogleUnexpected, err)
	}
	resp, err := hc.Do(req)
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrGoogleUnexpected, err)
	}
	defer resp.Body.Close()

	if err := checkGoogleStatus(resp.StatusCode); err != nil {
		return "", err
	}

	var meta driveFileMeta
	if err := json.NewDecoder(resp.Body).Decode(&meta); err != nil {
		return "", fmt.Errorf("%w: decode meta: %v", ErrGoogleUnexpected, err)
	}

	// Step 2: fetch content via export (Google Workspace) or alt=media (binary).
	var contentURL string
	if strings.HasPrefix(meta.MimeType, "application/vnd.google-apps.") {
		contentURL = fmt.Sprintf("%s/drive/v3/files/%s/export?mimeType=text/plain", c.base(), fileID)
	} else {
		contentURL = fmt.Sprintf("%s/drive/v3/files/%s?alt=media", c.base(), fileID)
	}

	req2, err := http.NewRequestWithContext(ctx, "GET", contentURL, nil)
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrGoogleUnexpected, err)
	}
	resp2, err := hc.Do(req2)
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrGoogleUnexpected, err)
	}
	defer resp2.Body.Close()

	if err := checkGoogleStatus(resp2.StatusCode); err != nil {
		return "", err
	}

	const maxBytes = 32000
	body, err := io.ReadAll(io.LimitReader(resp2.Body, maxBytes))
	if err != nil {
		return "", fmt.Errorf("%w: read body: %v", ErrGoogleUnexpected, err)
	}
	return string(body), nil
}

func checkGoogleStatus(code int) error {
	switch code {
	case http.StatusOK:
		return nil
	case http.StatusUnauthorized, http.StatusForbidden:
		return ErrGoogleAuth
	case http.StatusNotFound:
		return ErrGoogleNotFound
	case http.StatusTooManyRequests:
		return ErrGoogleQuotaExceeded
	default:
		return fmt.Errorf("%w: status %d", ErrGoogleUnexpected, code)
	}
}
