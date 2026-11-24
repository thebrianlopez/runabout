package scoringcli

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeStub(t *testing.T, script string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "pi-stub.sh")
	if err := os.WriteFile(p, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestCompleteJSON_EnvelopeParse(t *testing.T) {
	stub := writeStub(t, "#!/bin/sh\necho 'prefix {\"score\":1,\"ok\":true} trailer'\n")
	raw, err := CompleteJSON(context.Background(), JSONOpts{
		BinaryPath:   stub,
		SystemPrompt: "sys",
		Content:      "user",
		Schema:       `{"type":"object"}`,
	})
	if err != nil {
		t.Fatalf("CompleteJSON: %v", err)
	}
	got, err := ParseEnvelopeJSON(raw)
	if err != nil {
		t.Fatalf("ParseEnvelopeJSON: %v", err)
	}
	if !strings.Contains(string(got), `"score":1`) {
		t.Fatalf("envelope %s", got)
	}
}

func TestCompleteJSON_StderrOnFailure(t *testing.T) {
	stub := writeStub(t, "#!/bin/sh\necho boom-detail >&2\nexit 1\n")
	_, err := CompleteJSON(context.Background(), JSONOpts{BinaryPath: stub, Content: "x"})
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "boom-detail") {
		t.Fatalf("stderr not in error: %v", err)
	}
	if err.Error() == "exit status 1" {
		t.Fatal("bare exit status 1")
	}
}

func TestCompleteJSON_MissingBinary(t *testing.T) {
	_, err := CompleteJSON(context.Background(), JSONOpts{
		BinaryPath: filepath.Join(t.TempDir(), "no-such-pi"),
		Content:    "x",
	})
	if err == nil {
		t.Fatal("expected missing binary error")
	}
	if !MissingBinaryError(err) {
		t.Fatalf("want missing-binary classification, got %v", err)
	}
}

func TestCompleteJSON_SchemaInPrompt(t *testing.T) {
	stub := writeStub(t, "#!/bin/sh\nprintf '%s\\n' \"$*\"\n")
	raw, err := CompleteJSON(context.Background(), JSONOpts{
		BinaryPath:   stub,
		SystemPrompt: "base",
		Content:      "c",
		Schema:       `{"required":["score"]}`,
	})
	if err != nil {
		t.Fatal(err)
	}
	s := string(raw)
	if !strings.Contains(s, `{"required":["score"]}`) {
		t.Fatalf("schema not in argv: %s", s)
	}
	if !strings.Contains(s, "--no-extensions") {
		t.Fatalf("missing hermetic flags: %s", s)
	}
}

func TestParseEnvelopeJSON_Malformed(t *testing.T) {
	_, err := ParseEnvelopeJSON([]byte("not json at all"))
	if err == nil {
		t.Fatal("expected parse error")
	}
}

func TestValidateBinary_Missing(t *testing.T) {
	err := ValidateBinary(filepath.Join(t.TempDir(), "missing-pi"))
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "OAuth") {
		t.Fatalf("actionable architecture error missing: %v", err)
	}
}
