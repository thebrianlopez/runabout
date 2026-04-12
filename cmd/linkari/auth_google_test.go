package main

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math/big"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// testKeyID is the kid used in test JWTs.
const testKeyID = "test-key-1"

// testClientID is the OAuth client ID used in tests.
const testClientID = "test-client-id.apps.googleusercontent.com"

// testJWKSServer starts an httptest server serving a JWKS endpoint with the given RSA public key.
func testJWKSServer(t *testing.T, pub *rsa.PublicKey) *httptest.Server {
	t.Helper()
	nB64 := base64.RawURLEncoding.EncodeToString(pub.N.Bytes())
	eBytes := big.NewInt(int64(pub.E)).Bytes()
	eB64 := base64.RawURLEncoding.EncodeToString(eBytes)

	jwks := jwkSet{
		Keys: []jwk{
			{
				Kid: testKeyID,
				Kty: "RSA",
				Alg: "RS256",
				Use: "sig",
				N:   nB64,
				E:   eB64,
			},
		},
	}

	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(jwks)
	}))
}

// signTestJWT creates a signed JWT with the given claims and private key.
func signTestJWT(t *testing.T, claims GoogleClaims, priv *rsa.PrivateKey) string {
	t.Helper()

	header := map[string]string{
		"alg": "RS256",
		"kid": testKeyID,
		"typ": "JWT",
	}
	headerJSON, _ := json.Marshal(header)
	claimsJSON, _ := json.Marshal(claims)

	headerB64 := base64.RawURLEncoding.EncodeToString(headerJSON)
	claimsB64 := base64.RawURLEncoding.EncodeToString(claimsJSON)

	signed := []byte(headerB64 + "." + claimsB64)
	hashed := sha256.Sum256(signed)
	sig, err := rsa.SignPKCS1v15(rand.Reader, priv, crypto.SHA256, hashed[:])
	if err != nil {
		t.Fatalf("sign JWT: %v", err)
	}
	sigB64 := base64.RawURLEncoding.EncodeToString(sig)

	return fmt.Sprintf("%s.%s.%s", headerB64, claimsB64, sigB64)
}

func TestVerifyValidToken(t *testing.T) {
	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}

	srv := testJWKSServer(t, &priv.PublicKey)
	defer srv.Close()

	v := NewGoogleTokenVerifier(testClientID)
	v.jwksURL = srv.URL

	claims := GoogleClaims{
		Sub:           "google-sub-123",
		Email:         "test@example.com",
		EmailVerified: true,
		Name:          "Test User",
		Iss:           "accounts.google.com",
		Aud:           testClientID,
		Exp:           time.Now().Add(time.Hour).Unix(),
		Iat:           time.Now().Unix(),
	}

	token := signTestJWT(t, claims, priv)
	got, err := v.Verify(token)
	if err != nil {
		t.Fatalf("Verify() error: %v", err)
	}

	if got.Sub != "google-sub-123" {
		t.Errorf("Sub = %q, want %q", got.Sub, "google-sub-123")
	}
	if got.Email != "test@example.com" {
		t.Errorf("Email = %q, want %q", got.Email, "test@example.com")
	}
}

func TestVerifyExpiredToken(t *testing.T) {
	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}

	srv := testJWKSServer(t, &priv.PublicKey)
	defer srv.Close()

	v := NewGoogleTokenVerifier(testClientID)
	v.jwksURL = srv.URL

	claims := GoogleClaims{
		Sub: "google-sub-123",
		Iss: "accounts.google.com",
		Aud: testClientID,
		Exp: time.Now().Add(-time.Hour).Unix(), // expired
		Iat: time.Now().Add(-2 * time.Hour).Unix(),
	}

	token := signTestJWT(t, claims, priv)
	_, err = v.Verify(token)
	if err == nil {
		t.Fatal("Verify() should fail for expired token")
	}
}

func TestVerifyWrongAudience(t *testing.T) {
	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}

	srv := testJWKSServer(t, &priv.PublicKey)
	defer srv.Close()

	v := NewGoogleTokenVerifier(testClientID)
	v.jwksURL = srv.URL

	claims := GoogleClaims{
		Sub: "google-sub-123",
		Iss: "accounts.google.com",
		Aud: "wrong-client-id",
		Exp: time.Now().Add(time.Hour).Unix(),
	}

	token := signTestJWT(t, claims, priv)
	_, err = v.Verify(token)
	if err == nil {
		t.Fatal("Verify() should fail for wrong audience")
	}
}

func TestVerifyBadSignature(t *testing.T) {
	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}

	// Sign with a different key than what JWKS serves.
	otherPriv, _ := rsa.GenerateKey(rand.Reader, 2048)

	srv := testJWKSServer(t, &priv.PublicKey)
	defer srv.Close()

	v := NewGoogleTokenVerifier(testClientID)
	v.jwksURL = srv.URL

	claims := GoogleClaims{
		Sub: "google-sub-123",
		Iss: "accounts.google.com",
		Aud: testClientID,
		Exp: time.Now().Add(time.Hour).Unix(),
	}

	token := signTestJWT(t, claims, otherPriv)
	_, err = v.Verify(token)
	if err == nil {
		t.Fatal("Verify() should fail for bad signature")
	}
}

func TestVerifyMalformedToken(t *testing.T) {
	v := NewGoogleTokenVerifier(testClientID)
	_, err := v.Verify("not.a.valid.jwt.at.all")
	if err == nil {
		t.Fatal("Verify() should fail for malformed token")
	}

	_, err = v.Verify("only-one-part")
	if err == nil {
		t.Fatal("Verify() should fail for single-part token")
	}
}
