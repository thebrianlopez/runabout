package main

import (
	"testing"
)

func TestExtractTypes(t *testing.T) {
	src := `package example

type Server struct {
	addr string
	port int
	tls  bool
}

type Handler interface {
	ServeHTTP(w ResponseWriter, r *Request)
	Close() error
}

type Duration = int64

type StringSlice []string
`
	tree, parser := parseSource(t, src)
	defer tree.Close()
	defer parser.Close()

	root := tree.RootNode()
	types, err := extractTypes(root, []byte(src), root.Language())
	if err != nil {
		t.Fatal(err)
	}

	if len(types) != 4 {
		t.Fatalf("expected 4 types, got %d", len(types))
	}

	// Build a name→type map for order-independent assertions
	byName := make(map[string]TypeInfo)
	for _, typ := range types {
		byName[typ.Name] = typ
	}

	// Struct
	if s, ok := byName["Server"]; !ok {
		t.Error("expected Server type")
	} else {
		if s.Kind != "struct" {
			t.Errorf("expected kind 'struct', got %q", s.Kind)
		}
		if s.FieldCount != 3 {
			t.Errorf("expected 3 fields, got %d", s.FieldCount)
		}
	}

	// Interface
	if h, ok := byName["Handler"]; !ok {
		t.Error("expected Handler type")
	} else if h.Kind != "interface" {
		t.Errorf("expected kind 'interface', got %q", h.Kind)
	}

	// Type alias (type X = Y)
	if d, ok := byName["Duration"]; !ok {
		t.Error("expected Duration type")
	} else if d.Kind != "alias" {
		t.Errorf("expected kind 'alias', got %q", d.Kind)
	}

	// Defined type (type X Y)
	if ss, ok := byName["StringSlice"]; !ok {
		t.Error("expected StringSlice type")
	} else if ss.Kind != "alias" {
		t.Errorf("expected kind 'alias', got %q", ss.Kind)
	}
}

func TestGenericType(t *testing.T) {
	src := `package example

type Container[T any] struct {
	items []T
	size  int
}
`
	tree, parser := parseSource(t, src)
	defer tree.Close()
	defer parser.Close()

	root := tree.RootNode()
	types, err := extractTypes(root, []byte(src), root.Language())
	if err != nil {
		t.Fatal(err)
	}

	if len(types) != 1 {
		t.Fatalf("expected 1 type, got %d", len(types))
	}

	if types[0].Name != "Container" {
		t.Errorf("expected name 'Container', got %q", types[0].Name)
	}
	if types[0].Kind != "struct" {
		t.Errorf("expected kind 'struct', got %q", types[0].Kind)
	}
	if types[0].FieldCount != 2 {
		t.Errorf("expected 2 fields, got %d", types[0].FieldCount)
	}
}
