package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/thebrianlopez/runabout/internal/consensus"
)

func TestExtractJSON_PassNoiseMalformed(t *testing.T) {
	pass, err := consensus.ExtractJSON(`ok {"next_action":"go","score":1}`)
	if err != nil {
		t.Fatalf("pass: %v", err)
	}
	if !strings.Contains(string(pass), `"score":1`) {
		t.Fatalf("pass payload %s", pass)
	}

	noise, err := consensus.ExtractJSON("Skip: radio now-playing announcement")
	if err == nil {
		t.Fatalf("noise should fail parse, got %s", noise)
	}

	_, err = consensus.ExtractJSON("{not-json")
	if err == nil {
		t.Fatal("malformed should fail")
	}
}

func TestChainEvalBinaryHasNoAPIKeyLiteral(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "chain-eval")
	cmd := exec.Command("go", "build", "-o", bin, ".")
	cmd.Dir = "."
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	data, err := os.ReadFile(bin)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "ANTHROPIC_API_KEY") {
		t.Fatal("chain-eval binary contains ANTHROPIC_API_KEY")
	}
}
