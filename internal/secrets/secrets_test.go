package secrets

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeSM is an in-memory SecretsManagerAPI for tests.
type fakeSM struct {
	data map[string]string
	err  error
}

func (f *fakeSM) GetSecretValue(_ context.Context, id string) (string, error) {
	if f.err != nil {
		return "", f.err
	}
	v, ok := f.data[id]
	if !ok {
		return "", os.ErrNotExist
	}
	return v, nil
}

func newTestResolver(sm SecretsManagerAPI) *Resolver {
	return New(func(_ context.Context) (SecretsManagerAPI, error) { return sm, nil })
}

func TestResolve_Literal(t *testing.T) {
	r := newTestResolver(nil)
	got, src, err := r.Resolve(context.Background(), "hello-world")
	if err != nil {
		t.Fatal(err)
	}
	if got != "hello-world" {
		t.Errorf("value = %q, want hello-world", got)
	}
	if src.Scheme != "literal" {
		t.Errorf("scheme = %q, want literal", src.Scheme)
	}
}

func TestResolve_SecretsManager_Plain(t *testing.T) {
	sm := &fakeSM{data: map[string]string{"linkari/bearer-token": "tok-abc123"}}
	r := newTestResolver(sm)
	got, src, err := r.Resolve(context.Background(), "secretsmanager://linkari/bearer-token")
	if err != nil {
		t.Fatal(err)
	}
	if got != "tok-abc123" {
		t.Errorf("value = %q", got)
	}
	if src.Scheme != "secretsmanager" || src.ID != "linkari/bearer-token" || src.Key != "" {
		t.Errorf("source = %+v", src)
	}
	if !strings.HasPrefix(src.String(), "secretsmanager://linkari/") {
		t.Errorf("source.String = %q", src.String())
	}
}

func TestResolve_SecretsManager_JSONKey(t *testing.T) {
	sm := &fakeSM{data: map[string]string{
		"linkari/firebase-sa": `{"type":"service_account","project_id":"linkari-dev"}`,
	}}
	r := newTestResolver(sm)
	got, src, err := r.Resolve(context.Background(), "secretsmanager://linkari/firebase-sa#project_id")
	if err != nil {
		t.Fatal(err)
	}
	if got != "linkari-dev" {
		t.Errorf("value = %q", got)
	}
	if src.Key != "project_id" {
		t.Errorf("key = %q", src.Key)
	}
}

func TestResolve_SecretsManager_Errors(t *testing.T) {
	sm := &fakeSM{data: map[string]string{"ok": `{"k":"v"}`, "notjson": "plain"}}
	r := newTestResolver(sm)
	cases := []struct {
		name  string
		uri   string
		errIn string
	}{
		{"empty uri", "secretsmanager://", "empty secretsmanager"},
		{"missing id with key", "secretsmanager://#k", "missing secret id"},
		{"not found", "secretsmanager://missing", "get missing"},
		{"key missing in json", "secretsmanager://ok#nope", `json key "nope" not found`},
		{"payload not json", "secretsmanager://notjson#k", "payload not JSON"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, _, err := r.Resolve(context.Background(), tc.uri)
			if err == nil {
				t.Fatalf("%s: want error, got nil", tc.name)
			}
			if !strings.Contains(err.Error(), tc.errIn) {
				t.Errorf("%s: err = %v, want contains %q", tc.name, err, tc.errIn)
			}
		})
	}
}

func TestResolve_File(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "tok")
	if err := os.WriteFile(path, []byte("file-value\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	r := newTestResolver(nil)
	got, src, err := r.Resolve(context.Background(), "file://"+path)
	if err != nil {
		t.Fatal(err)
	}
	if got != "file-value" {
		t.Errorf("value = %q (trailing newline should be trimmed)", got)
	}
	if src.Scheme != "file" || src.ID != path {
		t.Errorf("source = %+v", src)
	}
}

func TestResolve_File_Missing(t *testing.T) {
	r := newTestResolver(nil)
	_, _, err := r.Resolve(context.Background(), "file:///nonexistent/path/xyz")
	if err == nil {
		t.Fatal("want error for missing file")
	}
}

func TestResolve_SMFactoryNotConfigured(t *testing.T) {
	r := New(nil)
	_, _, err := r.Resolve(context.Background(), "secretsmanager://linkari/token")
	if err == nil || !strings.Contains(err.Error(), "no SM factory") {
		t.Fatalf("err = %v, want 'no SM factory'", err)
	}
}

func TestFingerprint_Deterministic(t *testing.T) {
	a := Fingerprint("secret-value")
	b := Fingerprint("secret-value")
	c := Fingerprint("different")
	if a != b {
		t.Errorf("non-deterministic: %s vs %s", a, b)
	}
	if a == c {
		t.Errorf("collision: %s", a)
	}
	if len(a) != 8 {
		t.Errorf("fingerprint len = %d, want 8", len(a))
	}
}
