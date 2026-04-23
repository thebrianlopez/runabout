package ssh

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// CT-9: ControlModeParser emits PaneOutput for %output lines.
func TestControlModeParser_PaneOutput(t *testing.T) {
	// "hello" in base64 is "aGVsbG8="
	input := "%output %1 aGVsbG8=\n%exit\n"
	ch := ControlModeParser("testhost", strings.NewReader(input))

	var events []PaneEvent
	for ev := range ch {
		events = append(events, ev)
	}

	require.Len(t, events, 2, "expected output + exit events")

	out := events[0]
	assert.Equal(t, PaneOutput, out.Type)
	assert.Equal(t, "testhost", out.Host)
	assert.Equal(t, "1", out.PaneID)
	assert.Equal(t, []byte("hello"), out.Data)
}

// %exit causes a PaneClosed event and closes the channel.
func TestControlModeParser_Exit(t *testing.T) {
	input := "%exit\n"
	ch := ControlModeParser("host", strings.NewReader(input))

	events := collect(ch)
	require.Len(t, events, 1)
	assert.Equal(t, PaneClosed, events[0].Type)
}

// %window-add emits PaneCreated.
func TestControlModeParser_WindowAdd(t *testing.T) {
	input := "%window-add @1\n%exit\n"
	ch := ControlModeParser("host", strings.NewReader(input))

	events := collect(ch)
	require.GreaterOrEqual(t, len(events), 1)
	assert.Equal(t, PaneCreated, events[0].Type)
	assert.Equal(t, "1", events[0].PaneID)
}

// Informational lines (%begin, %end, %session-changed) are silently skipped.
func TestControlModeParser_SkipsInformationalLines(t *testing.T) {
	input := "%begin 123 0 0\n%end 123 0 0\n%session-changed $1 main\n%exit\n"
	ch := ControlModeParser("host", strings.NewReader(input))

	events := collect(ch)
	// Only the %exit event should be emitted.
	require.Len(t, events, 1)
	assert.Equal(t, PaneClosed, events[0].Type)
}

// Unknown lines are silently skipped — parser continues running.
func TestControlModeParser_SkipsUnknownLines(t *testing.T) {
	input := "%unknown-future-event foo bar\n%output %2 dGVzdA==\n%exit\n"
	ch := ControlModeParser("host", strings.NewReader(input))

	events := collect(ch)
	require.Len(t, events, 2)
	assert.Equal(t, PaneOutput, events[0].Type)
	assert.Equal(t, []byte("test"), events[0].Data)
}

// Multiple %output lines are all emitted.
func TestControlModeParser_MultipleOutputs(t *testing.T) {
	// "foo" = Zm9v, "bar" = YmFy
	input := "%output %0 Zm9v\n%output %0 YmFy\n%exit\n"
	ch := ControlModeParser("host", strings.NewReader(input))

	events := collect(ch)
	require.Len(t, events, 3)
	assert.Equal(t, []byte("foo"), events[0].Data)
	assert.Equal(t, []byte("bar"), events[1].Data)
}

func collect(ch <-chan PaneEvent) []PaneEvent {
	var out []PaneEvent
	for ev := range ch {
		out = append(out, ev)
	}
	return out
}
