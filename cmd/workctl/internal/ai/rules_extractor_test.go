package ai

import (
	"bytes"
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/blo-grindr/runabout/cmd/workctl/internal/models"
	"github.com/blo-grindr/runabout/cmd/workctl/internal/pipeline"
)

func newTestExtractor(buf *bytes.Buffer) *RulesExtractor {
	return NewRulesExtractor(pipeline.NewWriterMetrics(buf))
}

func TestRulesExtractor_EmptyEvents(t *testing.T) {
	var buf bytes.Buffer
	e := newTestExtractor(&buf)

	s, err := e.Extract(context.Background(), nil)

	require.NoError(t, err)
	require.NotNil(t, s)
	require.NotNil(t, s.ShellActivity)
	require.NotNil(t, s.AIActivity)
	require.Equal(t, 0, s.ShellActivity.TotalCommands)
	require.Equal(t, 0, s.AIActivity.TotalSessions)
}

func TestRulesExtractor_ShellCmdEvents(t *testing.T) {
	var buf bytes.Buffer
	e := newTestExtractor(&buf)
	now := time.Now()

	events := []pipeline.Event{
		{Source: "fish_history", Timestamp: now, Kind: "shell_cmd",
			Payload: models.ShellCommand{Binary: "kubectl", IsInfra: true}},
		{Source: "fish_history", Timestamp: now, Kind: "shell_cmd",
			Payload: models.ShellCommand{Binary: "git", IsInfra: false}},
	}

	s, err := e.Extract(context.Background(), events)

	require.NoError(t, err)
	require.NotNil(t, s.ShellActivity)
	require.Equal(t, 2, s.ShellActivity.TotalCommands)
	require.Equal(t, 1, s.ShellActivity.InfraCommands)
}

func TestRulesExtractor_AIActivityEvents(t *testing.T) {
	var buf bytes.Buffer
	e := newTestExtractor(&buf)
	now := time.Now()

	events := []pipeline.Event{
		{Source: "claude_stats", Timestamp: now, Kind: "ai_activity",
			Payload: models.AIActivity{SessionCount: 3, MessageCount: 12, TokensUsed: 5000}},
	}

	s, err := e.Extract(context.Background(), events)

	require.NoError(t, err)
	require.NotNil(t, s.AIActivity)
	require.Equal(t, 3, s.AIActivity.TotalSessions)
	require.Equal(t, 12, s.AIActivity.TotalMessages)
	require.Equal(t, 5000, s.AIActivity.TotalTokens)
}

func TestRulesExtractor_AuditEventSetsAgentCommands(t *testing.T) {
	var buf bytes.Buffer
	e := newTestExtractor(&buf)
	now := time.Now()

	events := []pipeline.Event{
		{Source: "audit_log", Timestamp: now, Kind: "audit_event",
			Payload: models.AuditEvent{Source: "claude_code", Cwd: "/code/myproj"}},
		{Source: "audit_log", Timestamp: now, Kind: "audit_event",
			Payload: models.AuditEvent{Source: "interactive_shell", Cwd: "/code/myproj"}},
	}

	s, err := e.Extract(context.Background(), events)

	require.NoError(t, err)
	require.NotNil(t, s.AIActivity)
	require.Equal(t, 1, s.AIActivity.AgentCommands)
	require.Equal(t, 1, s.AIActivity.HumanCommands)
}

func TestRulesExtractor_MetricsEmitted(t *testing.T) {
	var buf bytes.Buffer
	e := newTestExtractor(&buf)

	events := []pipeline.Event{
		{Kind: "shell_cmd", Payload: models.ShellCommand{Binary: "kubectl", IsInfra: true}},
		{Kind: "shell_cmd", Payload: models.ShellCommand{Binary: "git"}},
	}

	_, err := e.Extract(context.Background(), events)

	require.NoError(t, err)
	line := buf.String()
	require.Contains(t, line, "workctl_extract")
	require.Contains(t, line, "1.00") // confidence_avg = 1.0
	require.Contains(t, line, "weekly_signals")
	require.Contains(t, line, "rules")
}

func TestRulesExtractor_IgnoresUnknownKinds(t *testing.T) {
	var buf bytes.Buffer
	e := newTestExtractor(&buf)

	events := []pipeline.Event{
		{Kind: "issue", Payload: "some jira issue text"},
		{Kind: "pr", Payload: map[string]any{"url": "https://github.com/x/y/pull/1"}},
	}

	s, err := e.Extract(context.Background(), events)

	require.NoError(t, err)
	require.NotNil(t, s)
	require.Equal(t, 0, s.ShellActivity.TotalCommands)
	require.Equal(t, 0, s.AIActivity.TotalSessions)
}

func TestRulesExtractor_MixedEvents(t *testing.T) {
	var buf bytes.Buffer
	e := newTestExtractor(&buf)
	now := time.Now()

	events := []pipeline.Event{
		{Kind: "shell_cmd", Timestamp: now, Payload: models.ShellCommand{Binary: "kubectl", IsInfra: true}},
		{Kind: "audit_event", Timestamp: now, Payload: models.AuditEvent{Source: "claude_code", Cwd: "/code/myproj"}},
		{Kind: "ai_activity", Timestamp: now, Payload: models.AIActivity{SessionCount: 2, MessageCount: 8}},
		{Kind: "issue", Payload: "ignored"},
	}

	s, err := e.Extract(context.Background(), events)

	require.NoError(t, err)
	require.Equal(t, 1, s.ShellActivity.TotalCommands)
	require.Equal(t, 1, s.ShellActivity.InfraCommands)
	require.Equal(t, 2, s.AIActivity.TotalSessions)
	require.Equal(t, 1, s.AIActivity.AgentCommands) // audit_event source=claude_code
}

func TestRulesExtractor_SignalCountInMetrics(t *testing.T) {
	var buf bytes.Buffer
	e := newTestExtractor(&buf)
	now := time.Now()

	// 3 shell cmds + 2 sessions → signalCount = 5; metrics line should contain "|5|"
	events := []pipeline.Event{
		{Kind: "shell_cmd", Timestamp: now, Payload: models.ShellCommand{Binary: "kubectl"}},
		{Kind: "shell_cmd", Timestamp: now, Payload: models.ShellCommand{Binary: "git"}},
		{Kind: "shell_cmd", Timestamp: now, Payload: models.ShellCommand{Binary: "terraform"}},
		{Kind: "ai_activity", Timestamp: now, Payload: models.AIActivity{SessionCount: 2}},
	}

	_, err := e.Extract(context.Background(), events)

	require.NoError(t, err)
	require.Contains(t, buf.String(), "|5|") // signalCount = 3 cmds + 2 sessions
}

func TestRulesExtractor_Tier(t *testing.T) {
	var buf bytes.Buffer
	e := newTestExtractor(&buf)
	require.Equal(t, pipeline.TierRules, e.Tier())
}
