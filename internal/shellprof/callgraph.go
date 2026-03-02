package shellprof

// CallGraph represents the call relationships between functions.
type CallGraph struct {
	Root  *Node
	Nodes map[string]*Node
	Edges []Edge
}

// Node represents a function in the call graph.
type Node struct {
	Name      string
	Calls     int
	TotalTime float64
	SelfTime  float64
}

// Edge represents a caller-to-callee relationship.
type Edge struct {
	Caller string
	Callee string
	Count  int
}

// Build constructs a call graph from profiling records.
func Build(records []CallRecord) *CallGraph {
	return &CallGraph{
		Nodes: make(map[string]*Node),
	}
}
