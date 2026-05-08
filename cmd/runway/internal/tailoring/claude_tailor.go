package tailoring

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"time"
)

const (
	defaultTailorModel   = "claude-sonnet-4-6"
	defaultTailorTimeout = 30 * time.Second
	tailorModelEnvVar    = "RUNWAY_TAILOR_MODEL"
)

// ClaudeLLMTailor implements LLMTailor by calling the claude CLI.
// Model is configurable via RUNWAY_TAILOR_MODEL env var or the Model field.
// No retry on failure — cost protection per TDD §4.
type ClaudeLLMTailor struct {
	Model string // explicit override; falls back to env var, then default
}

func (c *ClaudeLLMTailor) model() string {
	if c.Model != "" {
		return c.Model
	}
	if env := os.Getenv(tailorModelEnvVar); env != "" {
		return env
	}
	return defaultTailorModel
}

// TailorWithLLM calls the claude CLI with the tailoring prompt and returns
// the raw YAML text response. The 30s deadline is enforced by the caller
// (Tailorer.TailorResume sets context timeout before calling).
func (c *ClaudeLLMTailor) TailorWithLLM(ctx context.Context, prompt string) (string, error) {
	cmd := exec.CommandContext(ctx, "claude", "-p", prompt, "--model", c.model())
	out, err := cmd.Output()
	if err != nil {
		return "", ErrTailorLLMTimeout
	}
	return strings.TrimSpace(string(out)), nil
}
