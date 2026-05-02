package ws

import "sync"

// ClientRegistry tracks connected clients and their pane subscriptions.
type ClientRegistry struct {
	mu      sync.Mutex
	clients map[string]*clientEntry // clientID → entry
}

type clientEntry struct {
	// cancelSubs holds the cancel functions for each active pane subscription.
	// key: paneID, value: cancel func returned by ControlModeBridge.Subscribe
	cancelSubs map[string]func()
}

func newClientRegistry() *ClientRegistry {
	return &ClientRegistry{clients: make(map[string]*clientEntry)}
}

// Add registers a new client. Returns false if max is exceeded.
func (r *ClientRegistry) Add(id string, max int) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.clients) >= max {
		return false
	}
	r.clients[id] = &clientEntry{cancelSubs: make(map[string]func())}
	return true
}

// Remove deregisters a client and cancels all its subscriptions.
func (r *ClientRegistry) Remove(id string) {
	r.mu.Lock()
	entry, ok := r.clients[id]
	if ok {
		delete(r.clients, id)
	}
	r.mu.Unlock()
	if ok {
		for _, cancel := range entry.cancelSubs {
			cancel()
		}
	}
}

// AddSub records a pane subscription cancel func for a client.
func (r *ClientRegistry) AddSub(clientID, paneID string, cancel func()) {
	r.mu.Lock()
	defer r.mu.Unlock()
	entry, ok := r.clients[clientID]
	if !ok {
		// Client already removed — immediately cancel.
		cancel()
		return
	}
	// If re-attaching, cancel existing subscription for this pane first.
	if existing, ok := entry.cancelSubs[paneID]; ok {
		existing()
	}
	entry.cancelSubs[paneID] = cancel
}

// RemoveSub cancels and removes the subscription for paneID from a client.
func (r *ClientRegistry) RemoveSub(clientID, paneID string) {
	r.mu.Lock()
	entry, ok := r.clients[clientID]
	if !ok {
		r.mu.Unlock()
		return
	}
	cancel, ok := entry.cancelSubs[paneID]
	if ok {
		delete(entry.cancelSubs, paneID)
	}
	r.mu.Unlock()
	if ok {
		cancel()
	}
}

// Count returns the current number of registered clients.
func (r *ClientRegistry) Count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.clients)
}
