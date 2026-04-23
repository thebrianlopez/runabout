package ssh

import (
	"context"
	"fmt"
	"sync"

	"github.com/blo-grindr/bmux/internal/config"
)

// manager is the concrete SSHManager implementation.
type manager struct {
	mu       sync.Mutex
	sessions map[string]*sshSession
	events   chan PaneEvent
	dialer   sshDialer
}

// NewManager creates an SSHManager. The events channel has a 256-event buffer.
func NewManager() SSHManager {
	return &manager{
		sessions: make(map[string]*sshSession),
		events:   make(chan PaneEvent, 256),
		dialer:   &defaultDialer{},
	}
}

// newManagerWithDialer is the testable constructor.
func newManagerWithDialer(dialer sshDialer) *manager {
	return &manager{
		sessions: make(map[string]*sshSession),
		events:   make(chan PaneEvent, 256),
		dialer:   dialer,
	}
}

func (m *manager) Connect(ctx context.Context, host config.HostConfig) (Session, error) {
	m.mu.Lock()
	if _, exists := m.sessions[host.Name]; exists {
		m.mu.Unlock()
		return nil, fmt.Errorf("session %q already exists", host.Name)
	}
	m.mu.Unlock()

	sess, err := connect(ctx, host, m.dialer, m.events)
	if err != nil {
		return nil, err
	}

	m.mu.Lock()
	m.sessions[host.Name] = sess
	m.mu.Unlock()

	// Remove from map on disconnect.
	go func() {
		<-sess.Disconnected()
		m.mu.Lock()
		delete(m.sessions, host.Name)
		m.mu.Unlock()
	}()

	return sess, nil
}

func (m *manager) Disconnect(name string) error {
	m.mu.Lock()
	sess, ok := m.sessions[name]
	m.mu.Unlock()
	if !ok {
		return fmt.Errorf("no session named %q", name)
	}
	return sess.Close()
}

func (m *manager) Sessions() []Session {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]Session, 0, len(m.sessions))
	for _, s := range m.sessions {
		out = append(out, s)
	}
	return out
}

func (m *manager) Events() <-chan PaneEvent { return m.events }
