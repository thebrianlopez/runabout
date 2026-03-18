package shellprof

import "testing"

func TestBuildEmpty(t *testing.T) {
	g := Build(nil)
	if g == nil {
		t.Fatal("expected non-nil graph")
	}
	if len(g.Nodes) != 0 {
		t.Errorf("expected empty nodes, got %d", len(g.Nodes))
	}
	if len(g.Edges) != 0 {
		t.Errorf("expected empty edges, got %d", len(g.Edges))
	}
}

func TestBuildEmptySlice(t *testing.T) {
	g := Build([]CallRecord{})
	if g == nil {
		t.Fatal("expected non-nil graph")
	}
	if len(g.Nodes) != 0 {
		t.Errorf("expected empty nodes, got %d", len(g.Nodes))
	}
}
