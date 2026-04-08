// Package secrets resolves values from a small set of URI schemes:
//
//	secretsmanager://<secret-id>[#<json-key>]   → AWS Secrets Manager
//	file://<absolute-path>                       → file contents (trimmed)
//	<literal>                                    → returned as-is
//
// Resolve is the only public entrypoint. Callers construct a Resolver once at
// startup (which wires the AWS SDK lazily on first use) and call Resolve per
// field. Unknown or malformed URIs return an error so that misconfiguration
// surfaces at startup rather than at request time.
//
// Provenance: Resolve returns both the resolved value and a Source describing
// where it came from, so callers can emit a startup log line with a SHA-256
// fingerprint (never the value itself).
package secrets

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
)

// Source describes where a resolved value came from. Safe to log.
type Source struct {
	Scheme string // "secretsmanager", "file", "literal"
	ID     string // secret ID, file path, or "<literal>"
	Key    string // JSON key for secretsmanager://...#key, else ""
}

// String returns a log-safe representation of the source.
func (s Source) String() string {
	switch s.Scheme {
	case "secretsmanager":
		if s.Key != "" {
			return fmt.Sprintf("secretsmanager://%s#%s", s.ID, s.Key)
		}
		return "secretsmanager://" + s.ID
	case "file":
		return "file://" + s.ID
	case "literal":
		return "<literal>"
	default:
		return s.Scheme + "://" + s.ID
	}
}

// Fingerprint returns the first 4 hex chars of SHA-256(value). Safe to log.
func Fingerprint(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])[:8]
}

// SecretsManagerAPI is the minimal surface used by Resolver. The real
// implementation is *secretsmanager.Client from aws-sdk-go-v2; tests supply
// an in-memory fake.
type SecretsManagerAPI interface {
	GetSecretValue(ctx context.Context, id string) (string, error)
}

// Resolver dispatches on URI scheme. Zero value is NOT usable; call New.
type Resolver struct {
	sm     SecretsManagerAPI
	smOnce sync.Once
	smErr  error
	// smFactory lazily constructs the SM client on first secretsmanager://
	// reference. Injected so tests can avoid AWS config discovery entirely.
	smFactory func(ctx context.Context) (SecretsManagerAPI, error)
}

// New constructs a Resolver. smFactory is invoked at most once, lazily, on
// the first secretsmanager:// URI. Pass nil to use the default AWS SDK wiring
// (not implemented in this file — see sm_aws.go for the production factory).
func New(smFactory func(ctx context.Context) (SecretsManagerAPI, error)) *Resolver {
	return &Resolver{smFactory: smFactory}
}

// Resolve dispatches on URI scheme and returns the resolved value plus its
// source. An empty value string returns ("", Source{Scheme:"literal"}, nil).
func (r *Resolver) Resolve(ctx context.Context, value string) (string, Source, error) {
	switch {
	case strings.HasPrefix(value, "secretsmanager://"):
		return r.resolveSM(ctx, value)
	case strings.HasPrefix(value, "file://"):
		return r.resolveFile(value)
	default:
		return value, Source{Scheme: "literal", ID: "<literal>"}, nil
	}
}

func (r *Resolver) resolveSM(ctx context.Context, value string) (string, Source, error) {
	raw := strings.TrimPrefix(value, "secretsmanager://")
	if raw == "" {
		return "", Source{}, errors.New("secrets: empty secretsmanager:// URI")
	}
	id, key, _ := strings.Cut(raw, "#")
	if id == "" {
		return "", Source{}, errors.New("secrets: secretsmanager:// URI missing secret id")
	}
	src := Source{Scheme: "secretsmanager", ID: id, Key: key}

	client, err := r.smClient(ctx)
	if err != nil {
		return "", src, fmt.Errorf("secrets: sm client init: %w", err)
	}
	payload, err := client.GetSecretValue(ctx, id)
	if err != nil {
		return "", src, fmt.Errorf("secrets: get %s: %w", id, err)
	}
	if key == "" {
		return payload, src, nil
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(payload), &m); err != nil {
		return "", src, fmt.Errorf("secrets: %s#%s: payload not JSON: %w", id, key, err)
	}
	raw2, ok := m[key]
	if !ok {
		return "", src, fmt.Errorf("secrets: %s: json key %q not found", id, key)
	}
	s, ok := raw2.(string)
	if !ok {
		return "", src, fmt.Errorf("secrets: %s#%s: value is not a string", id, key)
	}
	return s, src, nil
}

func (r *Resolver) resolveFile(value string) (string, Source, error) {
	path := strings.TrimPrefix(value, "file://")
	if path == "" {
		return "", Source{}, errors.New("secrets: empty file:// URI")
	}
	src := Source{Scheme: "file", ID: path}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", src, fmt.Errorf("secrets: read %s: %w", path, err)
	}
	return strings.TrimRight(string(data), "\n"), src, nil
}

func (r *Resolver) smClient(ctx context.Context) (SecretsManagerAPI, error) {
	r.smOnce.Do(func() {
		if r.smFactory == nil {
			r.smErr = errors.New("secrets: no SM factory configured")
			return
		}
		r.sm, r.smErr = r.smFactory(ctx)
	})
	return r.sm, r.smErr
}
