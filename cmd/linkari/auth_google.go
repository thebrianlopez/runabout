package main

import (
	"context"
	"crypto"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math/big"
	"net/http"
	"strings"
	"sync"
	"time"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
)

// defaultGoogleJWKSURL is the public endpoint for Google's OAuth2 signing keys.
const defaultGoogleJWKSURL = "https://www.googleapis.com/oauth2/v3/certs"

// GoogleClaims holds the verified claims from a Google ID token.
type GoogleClaims struct {
	Sub           string `json:"sub"`
	Email         string `json:"email"`
	EmailVerified bool   `json:"email_verified"`
	Name          string `json:"name"`
	Iss           string `json:"iss"`
	Aud           string `json:"aud"`
	Exp           int64  `json:"exp"`
	Iat           int64  `json:"iat"`
}

// GoogleTokenVerifier verifies Google ID tokens using the public JWKS endpoint.
// Keys are cached for 1 hour to avoid hitting the endpoint on every request.
type GoogleTokenVerifier struct {
	clientID string
	jwksURL  string // overridable for tests

	mu      sync.RWMutex
	keys    map[string]*rsa.PublicKey
	fetched time.Time
}

// NewGoogleTokenVerifier creates a verifier for the given Google OAuth client ID.
func NewGoogleTokenVerifier(clientID string) *GoogleTokenVerifier {
	return &GoogleTokenVerifier{
		clientID: clientID,
		jwksURL:  defaultGoogleJWKSURL,
	}
}

// jwkSet is the JSON structure returned by Google's JWKS endpoint.
type jwkSet struct {
	Keys []jwk `json:"keys"`
}

type jwk struct {
	Kid string `json:"kid"`
	Kty string `json:"kty"`
	Alg string `json:"alg"`
	Use string `json:"use"`
	N   string `json:"n"`
	E   string `json:"e"`
}

const jwksCacheDuration = 1 * time.Hour

// fetchKeys retrieves and caches the JWKS keyset. Returns cached keys if fresh.
func (v *GoogleTokenVerifier) fetchKeys() (map[string]*rsa.PublicKey, error) {
	v.mu.RLock()
	if v.keys != nil && time.Since(v.fetched) < jwksCacheDuration {
		keys := v.keys
		v.mu.RUnlock()
		return keys, nil
	}
	v.mu.RUnlock()

	v.mu.Lock()
	defer v.mu.Unlock()

	// Double-check after acquiring write lock.
	if v.keys != nil && time.Since(v.fetched) < jwksCacheDuration {
		return v.keys, nil
	}

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Get(v.jwksURL)
	if err != nil {
		return nil, fmt.Errorf("fetch JWKS: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("fetch JWKS: status %d", resp.StatusCode)
	}

	var set jwkSet
	if err := json.NewDecoder(resp.Body).Decode(&set); err != nil {
		return nil, fmt.Errorf("decode JWKS: %w", err)
	}

	keys := make(map[string]*rsa.PublicKey, len(set.Keys))
	for _, k := range set.Keys {
		if k.Kty != "RSA" {
			continue
		}
		pub, err := parseRSAPublicKey(k.N, k.E)
		if err != nil {
			continue // skip malformed keys
		}
		keys[k.Kid] = pub
	}

	v.keys = keys
	v.fetched = time.Now()
	return keys, nil
}

// parseRSAPublicKey decodes base64url-encoded n and e into an RSA public key.
func parseRSAPublicKey(nStr, eStr string) (*rsa.PublicKey, error) {
	nBytes, err := base64.RawURLEncoding.DecodeString(nStr)
	if err != nil {
		return nil, fmt.Errorf("decode n: %w", err)
	}
	eBytes, err := base64.RawURLEncoding.DecodeString(eStr)
	if err != nil {
		return nil, fmt.Errorf("decode e: %w", err)
	}

	n := new(big.Int).SetBytes(nBytes)
	e := new(big.Int).SetBytes(eBytes)
	if !e.IsInt64() {
		return nil, errors.New("exponent too large")
	}

	return &rsa.PublicKey{
		N: n,
		E: int(e.Int64()),
	}, nil
}

// jwtHeader is the decoded JWT header used to select the signing key.
type jwtHeader struct {
	Alg string `json:"alg"`
	Kid string `json:"kid"`
	Typ string `json:"typ"`
}

// Verify validates a Google ID token and returns the claims if valid.
// It checks: signature (RS256), issuer, audience, and expiration.
func (v *GoogleTokenVerifier) Verify(idToken string) (*GoogleClaims, error) {
	parts := strings.SplitN(idToken, ".", 3)
	if len(parts) != 3 {
		return nil, errors.New("invalid JWT: expected 3 parts")
	}

	// Decode header.
	headerJSON, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return nil, fmt.Errorf("decode header: %w", err)
	}
	var header jwtHeader
	if err := json.Unmarshal(headerJSON, &header); err != nil {
		return nil, fmt.Errorf("parse header: %w", err)
	}
	if header.Alg != "RS256" {
		return nil, fmt.Errorf("unsupported algorithm: %s", header.Alg)
	}

	// Fetch signing keys.
	keys, err := v.fetchKeys()
	if err != nil {
		return nil, err
	}

	key, ok := keys[header.Kid]
	if !ok {
		return nil, fmt.Errorf("unknown key id: %s", header.Kid)
	}

	// Verify RS256 signature.
	sigBytes, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return nil, fmt.Errorf("decode signature: %w", err)
	}

	signed := []byte(parts[0] + "." + parts[1])
	hashed := sha256.Sum256(signed)
	if err := rsa.VerifyPKCS1v15(key, crypto.SHA256, hashed[:], sigBytes); err != nil {
		return nil, fmt.Errorf("signature verification failed: %w", err)
	}

	// Decode claims.
	claimsJSON, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil, fmt.Errorf("decode claims: %w", err)
	}
	var claims GoogleClaims
	if err := json.Unmarshal(claimsJSON, &claims); err != nil {
		return nil, fmt.Errorf("parse claims: %w", err)
	}

	// Validate issuer.
	if claims.Iss != "accounts.google.com" && claims.Iss != "https://accounts.google.com" {
		return nil, fmt.Errorf("invalid issuer: %s", claims.Iss)
	}

	// Validate audience.
	if claims.Aud != v.clientID {
		return nil, fmt.Errorf("invalid audience: %s", claims.Aud)
	}

	// Validate expiration.
	if time.Now().Unix() > claims.Exp {
		return nil, errors.New("token expired")
	}

	return &claims, nil
}

// youtubeTokenExchanger is a seam for testing token refresh without real HTTP.
type youtubeTokenExchanger func(ctx context.Context, refreshToken string) (*oauth2.Token, error)

// mockInvalidGrantExchanger simulates an invalid_grant response from Google.
func mockInvalidGrantExchanger(_ context.Context, _ string) (*oauth2.Token, error) {
	return nil, errors.New("invalid_grant")
}

// youtubeTokenSource returns a self-refreshing oauth2.TokenSource for YouTube API access.
// Returns youtube_auth_missing when no refresh token is stored.
// Returns youtube_token_revoked when the stored token gets invalid_grant from Google.
func youtubeTokenSource(ctx context.Context, profile string, q *Queue, clientID, clientSecret string) (oauth2.TokenSource, error) {
	return youtubeTokenSourceWithExchanger(ctx, profile, q, clientID, clientSecret, nil)
}

func youtubeTokenSourceWithExchanger(ctx context.Context, profile string, q *Queue, clientID, clientSecret string, exchanger youtubeTokenExchanger) (oauth2.TokenSource, error) {
	refreshToken, expiresAt, err := q.GetYouTubeRefreshToken(profile)
	if err != nil {
		return nil, fmt.Errorf("youtube_auth_missing: %w", err)
	}
	if refreshToken == "" {
		slog.Warn("youtube auth missing", "event_type", "youtube_auth_missing", "profile", profile, "error_class", "youtube_auth_missing")
		return nil, errors.New("youtube_auth_missing")
	}

	tok := &oauth2.Token{
		RefreshToken: refreshToken,
		Expiry:       time.Unix(expiresAt, 0),
	}

	cfg := &oauth2.Config{
		ClientID:     clientID,
		ClientSecret: clientSecret,
		Scopes:       []string{"https://www.googleapis.com/auth/youtube.readonly", "https://www.googleapis.com/auth/youtube"},
		Endpoint:     google.Endpoint,
	}
	ts := cfg.TokenSource(ctx, tok)

	if exchanger != nil {
		newTok, err := exchanger(ctx, refreshToken)
		if err != nil {
			if strings.Contains(err.Error(), "invalid_grant") {
				slog.Warn("youtube token revoked", "event_type", "youtube_token_revoked", "profile", profile, "error_class", "youtube_token_revoked")
				return nil, errors.New("youtube_token_revoked")
			}
			return nil, err
		}
		_ = newTok
	}

	return ts, nil
}

// youtubeTokenSourceForSlot returns an oauth2.TokenSource for the given OAuth slot.
// Returns sql.ErrNoRows when the slot has no stored credential (not yet authed).
// Propagates other DB errors as-is.
func youtubeTokenSourceForSlot(ctx context.Context, slot string, userID int64, q *Queue, clientID, clientSecret string) (oauth2.TokenSource, error) {
	refreshToken, expiresAt, err := q.GetYouTubeSlotToken(userID, slot)
	if err != nil {
		return nil, err // sql.ErrNoRows propagated for slot_no_token handling
	}
	tok := &oauth2.Token{
		RefreshToken: refreshToken,
		Expiry:       time.Unix(expiresAt, 0),
	}
	cfg := &oauth2.Config{
		ClientID:     clientID,
		ClientSecret: clientSecret,
		Scopes:       []string{"https://www.googleapis.com/auth/youtube.readonly", "https://www.googleapis.com/auth/youtube"},
		Endpoint:     google.Endpoint,
	}
	return cfg.TokenSource(ctx, tok), nil
}

// storeYouTubeToken persists a YouTube refresh token and emits a youtube_token_stored event.
func storeYouTubeToken(q *Queue, profile, refreshToken string, expiresAt int64) error {
	if err := q.SetYouTubeRefreshToken(profile, refreshToken, expiresAt); err != nil {
		return err
	}
	slog.Info("youtube token stored", "event_type", "youtube_token_stored", "profile", profile, "expires_at", expiresAt)
	return nil
}

func classifyYouTubeAPIError(err error) (eventClass, remediation string) {
	if err == nil {
		return "", ""
	}
	if strings.Contains(err.Error(), "invalid_grant") {
		return "oauth_invalid_grant", "run `linkari auth youtube` to re-authorize Google/YouTube access"
	}
	if isQuotaExhausted(err) {
		return "quota_exhausted", "wait for YouTube API quota reset or reduce polling"
	}
	return "api_error", ""
}
