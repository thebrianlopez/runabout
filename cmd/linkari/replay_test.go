package main

import "testing"

func TestReplayShareRequestCarriesQueueRowID(t *testing.T) {
	item := QueueItem{
		ID:      27630,
		Type:    "url",
		Action:  "uinit_auto",
		URL:     "https://www.youtube.com/watch?v=sTCGJH_utDk",
		Profile: "eng",
		Intent:  "score",
	}

	req := replayShareRequest(item)

	if req.QueueRowID != item.ID {
		t.Fatalf("QueueRowID = %d, want %d", req.QueueRowID, item.ID)
	}
	if req.URL != item.URL || req.Action != item.Action || req.Profile != item.Profile || req.Intent != item.Intent {
		t.Fatalf("replay request lost persisted routing fields: got url=%q action=%q profile=%q intent=%q", req.URL, req.Action, req.Profile, req.Intent)
	}
	if !req.Enter {
		t.Fatal("Enter = false, want true for replay routing")
	}
}
