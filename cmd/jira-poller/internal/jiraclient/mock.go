package jiraclient

import "context"

// MockClient is a test double for Client. F4 unit tests use it directly.
type MockClient struct {
	// Results are returned in order per call; the last entry is repeated if
	// the slice is exhausted before calls stop.
	Results []SearchResult

	// Err is returned on every call when non-nil.
	Err error

	// Calls records every SearchRequest received, in order.
	Calls []SearchRequest

	idx int
}

// SearchTransitions implements Client.
func (m *MockClient) SearchTransitions(_ context.Context, req SearchRequest) (*SearchResult, error) {
	m.Calls = append(m.Calls, req)
	if m.Err != nil {
		return nil, m.Err
	}
	if len(m.Results) == 0 {
		return &SearchResult{}, nil
	}
	r := m.Results[m.idx]
	if m.idx < len(m.Results)-1 {
		m.idx++
	}
	return &r, nil
}
