package poller

import (
	"testing"
	"time"
)

func TestPRPoller_Name(t *testing.T) {
	p := &PRPoller{knownPRs: make(map[int]prState)}
	if p.Name() != "pr" {
		t.Errorf("Name: got %q, want %q", p.Name(), "pr")
	}
}

func TestActionForNewPR(t *testing.T) {
	tests := []struct {
		name   string
		state  string
		merged bool
		want   string
	}{
		{"open", "open", false, "opened"},
		{"merged", "closed", true, "merged"},
		{"closed not merged", "closed", false, "closed"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := actionForNewPR(tt.state, tt.merged)
			if got != tt.want {
				t.Errorf("actionForNewPR(%q, %v) = %q, want %q", tt.state, tt.merged, got, tt.want)
			}
		})
	}
}

func TestDetectAction(t *testing.T) {
	now := time.Now()
	tests := []struct {
		name     string
		prev     prState
		newState string
		merged   bool
		want     string
	}{
		{
			"open to merged",
			prState{State: "open", UpdatedAt: now, Merged: false},
			"closed", true, "merged",
		},
		{
			"open to closed",
			prState{State: "open", UpdatedAt: now, Merged: false},
			"closed", false, "closed",
		},
		{
			"closed to reopened",
			prState{State: "closed", UpdatedAt: now, Merged: false},
			"open", false, "reopened",
		},
		{
			"same state",
			prState{State: "open", UpdatedAt: now, Merged: false},
			"open", false, "updated",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := detectAction(tt.prev, tt.newState, tt.merged)
			if got != tt.want {
				t.Errorf("detectAction = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestStateChanged(t *testing.T) {
	now := time.Now()
	tests := []struct {
		name     string
		prev     prState
		newState string
		merged   bool
		updated  time.Time
		want     bool
	}{
		{
			"state change",
			prState{State: "open", UpdatedAt: now},
			"closed", false, now, true,
		},
		{
			"merged change",
			prState{State: "closed", UpdatedAt: now, Merged: false},
			"closed", true, now, true,
		},
		{
			"no change",
			prState{State: "open", UpdatedAt: now, Merged: false},
			"open", false, now, false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := stateChanged(tt.prev, tt.newState, tt.merged, tt.updated)
			if got != tt.want {
				t.Errorf("stateChanged = %v, want %v", got, tt.want)
			}
		})
	}
}
