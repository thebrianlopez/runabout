package ctlbridge

import (
	"log/slog"
	"sync"
)

// SubscriptionRouter maintains per-pane subscriber channels and fans out data
// to all subscribers of a given pane. Fan-out is non-blocking: a full channel
// causes a frame drop with a WARN log; it never blocks the router goroutine.
type SubscriptionRouter struct {
	mu   sync.RWMutex
	subs map[string][]chan<- []byte // keyed by paneID
}

// NewSubscriptionRouter creates an empty SubscriptionRouter.
func NewSubscriptionRouter() *SubscriptionRouter {
	return &SubscriptionRouter{
		subs: make(map[string][]chan<- []byte),
	}
}

// Subscribe registers ch to receive output for paneID.
// Returns an unsubscribe func that removes ch from the routing table.
func (r *SubscriptionRouter) Subscribe(paneID string, ch chan<- []byte) (unsubscribe func()) {
	r.mu.Lock()
	r.subs[paneID] = append(r.subs[paneID], ch)
	r.mu.Unlock()

	return func() {
		r.mu.Lock()
		defer r.mu.Unlock()
		chans := r.subs[paneID]
		filtered := chans[:0:len(chans)]
		filtered = filtered[:0]
		for _, c := range chans {
			if c != ch {
				filtered = append(filtered, c)
			}
		}
		if len(filtered) == 0 {
			delete(r.subs, paneID)
		} else {
			r.subs[paneID] = filtered
		}
	}
}

// Deliver fans out data to all subscribers of paneID.
// Non-blocking: full channels are dropped with a WARN log.
func (r *SubscriptionRouter) Deliver(paneID string, data []byte) {
	r.mu.RLock()
	chans := make([]chan<- []byte, len(r.subs[paneID]))
	copy(chans, r.subs[paneID])
	r.mu.RUnlock()

	for _, ch := range chans {
		select {
		case ch <- data:
		default:
			slog.Warn("subscriber_frame_dropped",
				"pane_id", paneID,
				"dropped_bytes", len(data))
		}
	}
}
