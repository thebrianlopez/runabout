package event

import (
	"sync"
	"testing"
	"time"
)

func TestDeduplicator_MarkAndCheck(t *testing.T) {
	d := NewDeduplicator(time.Hour)

	if d.IsDuplicate("a") {
		t.Error("expected 'a' to not be a duplicate before marking")
	}

	d.Mark("a")

	if !d.IsDuplicate("a") {
		t.Error("expected 'a' to be a duplicate after marking")
	}

	if d.IsDuplicate("b") {
		t.Error("expected 'b' to not be a duplicate")
	}
}

func TestDeduplicator_TTLExpiry(t *testing.T) {
	d := NewDeduplicator(1 * time.Millisecond)

	d.Mark("a")
	time.Sleep(5 * time.Millisecond)

	if d.IsDuplicate("a") {
		t.Error("expected 'a' to expire after TTL")
	}
}

func TestDeduplicator_Prune(t *testing.T) {
	d := NewDeduplicator(1 * time.Millisecond)

	d.Mark("a")
	d.Mark("b")
	d.Mark("c")
	time.Sleep(5 * time.Millisecond)

	d.Prune()

	if d.Len() != 0 {
		t.Errorf("expected 0 entries after prune, got %d", d.Len())
	}
}

func TestDeduplicator_Prune_KeepsFresh(t *testing.T) {
	d := NewDeduplicator(time.Hour)

	d.Mark("a")
	d.Mark("b")

	d.Prune()

	if d.Len() != 2 {
		t.Errorf("expected 2 entries after prune (still fresh), got %d", d.Len())
	}
}

func TestDeduplicator_Seed(t *testing.T) {
	d := NewDeduplicator(time.Hour)
	d.Seed([]string{"x", "y", "z"})

	if !d.IsDuplicate("x") {
		t.Error("expected seeded 'x' to be duplicate")
	}
	if !d.IsDuplicate("y") {
		t.Error("expected seeded 'y' to be duplicate")
	}
	if d.IsDuplicate("w") {
		t.Error("expected non-seeded 'w' to not be duplicate")
	}
	if d.Len() != 3 {
		t.Errorf("expected 3 seeded entries, got %d", d.Len())
	}
}

func TestDeduplicator_SeenIDs(t *testing.T) {
	d := NewDeduplicator(time.Hour)
	d.Mark("a")
	d.Mark("b")

	ids := d.SeenIDs()
	if len(ids) != 2 {
		t.Fatalf("expected 2 IDs, got %d", len(ids))
	}
	m := map[string]bool{}
	for _, id := range ids {
		m[id] = true
	}
	if !m["a"] || !m["b"] {
		t.Errorf("expected IDs a and b, got %v", ids)
	}
}

func TestDeduplicator_Concurrency(t *testing.T) {
	d := NewDeduplicator(time.Hour)
	var wg sync.WaitGroup

	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func(id string) {
			defer wg.Done()
			d.Mark(id)
			d.IsDuplicate(id)
			d.Prune()
		}(string(rune('a' + i%26)))
	}

	wg.Wait()
	// No panic = success.
}
