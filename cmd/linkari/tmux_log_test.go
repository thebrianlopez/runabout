package main

import (
	"bytes"
	"log/slog"
	"os/exec"
	"strings"
	"testing"
)

// captureSlog swaps the default slog logger for a text handler writing
// to a buffer. Returns the buffer and a restore func.
func captureSlog(t *testing.T) (*bytes.Buffer, func()) {
	t.Helper()
	buf := &bytes.Buffer{}
	prev := slog.Default()
	h := slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug})
	slog.SetDefault(slog.New(h))
	return buf, func() { slog.SetDefault(prev) }
}

func TestPosixQuote(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"", "''"},
		{"simple", "simple"},
		{"has-dash", "has-dash"},
		{"=linkari", "=linkari"},
		{"path/to/file.go", "path/to/file.go"},
		{"has space", "'has space'"},
		{"has;semi", "'has;semi'"},
		{"has$dollar", "'has$dollar'"},
		{"has`tick`", "'has`tick`'"},
		{`has"dquote"`, `'has"dquote"'`},
		{"has'squote", `'has'\''squote'`},
		{"eng: articles-foo", "'eng: articles-foo'"},
		{"uinit --auto-resume https://example.com/$foo; exec fish", "'uinit --auto-resume https://example.com/$foo; exec fish'"},
	}
	for _, c := range cases {
		got := posixQuote(c.in)
		if got != c.want {
			t.Errorf("posixQuote(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestLogTmuxExec_StructuredFields(t *testing.T) {
	buf, restore := captureSlog(t)
	defer restore()

	cmd := exec.Command("tmux", "has-session", "-t", "=linkari")
	logTmuxExec(cmd)

	out := buf.String()
	if !strings.Contains(out, `msg="tmux exec"`) {
		t.Errorf("missing msg: %s", out)
	}
	// slog text handler renders []string as [a b c] and quotes values with spaces
	if !strings.Contains(out, `argv="[tmux has-session -t =linkari]"`) {
		t.Errorf("missing argv field: %s", out)
	}
	// repro field uses POSIX quoting; safe tokens pass through unquoted,
	// but slog wraps the whole value in double-quotes because it has spaces
	if !strings.Contains(out, `repro="tmux has-session -t =linkari"`) {
		t.Errorf("missing repro field: %s", out)
	}
}

func TestLogTmuxExec_SpacePreservation(t *testing.T) {
	buf, restore := captureSlog(t)
	defer restore()

	// The exact bug class from the 2026-04-09 log: window name with a space.
	cmd := exec.Command("tmux", "new-window", "-n", "eng: articles-foo", "fish", "-c", "uinit; exec fish")
	logTmuxExec(cmd)

	out := buf.String()
	// The space-bearing token MUST appear quoted as a single unit
	if !strings.Contains(out, `'eng: articles-foo'`) {
		t.Errorf("space-bearing token not quoted as unit: %s", out)
	}
	if !strings.Contains(out, `'uinit; exec fish'`) {
		t.Errorf("semicolon-bearing token not quoted as unit: %s", out)
	}
}

func TestLogTmuxExec_EmptyExtraArgs(t *testing.T) {
	buf, restore := captureSlog(t)
	defer restore()

	cmd := exec.Command("tmux")
	logTmuxExec(cmd)

	out := buf.String()
	if !strings.Contains(out, "repro=tmux") {
		t.Errorf("single-arg repro missing: %s", out)
	}
}

// TestPosixJoin_RoundTrip is the core M3 validation: feed the repro
// string to /bin/sh and verify the shell tokenizes it back into the
// original argv. This is the "paste into terminal works" guarantee.
func TestPosixJoin_RoundTrip(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("sh not found")
	}

	cases := [][]string{
		{"tmux", "has-session", "-t", "=linkari"},
		{"tmux", "new-window", "-n", "eng: articles-foo"},
		{"tmux", "new-window", "-a", "-t", "=linkari", "-n", "eng: articles-ai-agent-transport-layer", "fish", "-c", "uinit --auto-resume https://www.infoq.com/articles/ai-agent-transport-layer/; exec fish"},
		{"tmux", "send-keys", "-t", "=linkari:0", "-l", "echo $PATH"},
		{"tmux", "send-keys", "-t", "=linkari:0", "-l", "echo `date`"},
		{"tmux", "send-keys", "-t", "=linkari:0", "-l", `echo "quoted"`},
		{"tmux", "send-keys", "-t", "=linkari:0", "-l", "has'squote"},
		{"tmux", "set-option", "-p", "-t", "=linkari:{end}", "remain-on-exit", "failed"},
		// EPIC-057 M4: hostile Jira summary with shell metachars, backticks, $, emoji, newline.
		{"tmux", "send-keys", "-t", "=linkari:0", "-l", "ginit PROJ-42 # title: `rm -rf` $HOME \xf0\x9f\x94\xa5\ninjected"},
	}

	for _, argv := range cases {
		repro := posixJoin(argv)
		// Pass `set -- <repro>` directly as sh source — exec.Command hands
		// the script to sh as a single argv, so sh parses repro's single
		// quotes before any expansion happens. Using `eval "set -- …"` here
		// would be WRONG: the outer double-quotes perform $var expansion on
		// repro's contents before eval runs.
		script := "set -- " + repro + `; for a do printf '%s\0' "$a"; done`
		out, err := exec.Command("sh", "-c", script).Output()
		if err != nil {
			t.Errorf("sh eval failed for %v (repro=%q): %v", argv, repro, err)
			continue
		}
		trimmed := strings.TrimSuffix(string(out), "\x00")
		got := strings.Split(trimmed, "\x00")
		if len(got) != len(argv) {
			t.Errorf("round-trip length mismatch\nargv=%v\nrepro=%s\ngot=%v", argv, repro, got)
			continue
		}
		for i := range argv {
			if got[i] != argv[i] {
				t.Errorf("round-trip token mismatch at %d\nargv[%d]=%q\ngot[%d]=%q\nrepro=%s", i, i, argv[i], i, got[i], repro)
			}
		}
	}
}
