package main

import (
	"context"
	"fmt"
	"testing"
)

func TestJaccardSimilarity(t *testing.T) {
	tests := []struct {
		a, b []string
		want float64
	}{
		{[]string{"go", "testing"}, []string{"go", "testing"}, 1.0},
		{[]string{"go", "testing"}, []string{"go", "ci"}, 1.0 / 3.0},
		{[]string{"go"}, []string{"rust"}, 0.0},
		{nil, nil, 0.0},
		{[]string{"a", "b", "c"}, []string{"b", "c", "d"}, 2.0 / 4.0},
	}
	for _, tt := range tests {
		got := jaccardSimilarity(tt.a, tt.b)
		if got < tt.want-0.01 || got > tt.want+0.01 {
			t.Errorf("jaccard(%v, %v) = %.3f, want %.3f", tt.a, tt.b, got, tt.want)
		}
	}
}

func TestParseTags(t *testing.T) {
	tests := []struct {
		input string
		want  int
	}{
		{`["go","testing","ci"]`, 3},
		{``, 0},
		{`go,testing,ci`, 3}, // legacy comma-separated fallback
	}
	for _, tt := range tests {
		got := parseTags(tt.input)
		if len(got) != tt.want {
			t.Errorf("parseTags(%q) = %d tags, want %d", tt.input, len(got), tt.want)
		}
	}
}

func TestBuildClusters(t *testing.T) {
	// 4 items: 3 with overlapping tags (should cluster), 1 different.
	items := []QueueItem{
		{ID: 1, TopicTags: `["go","testing","ci"]`},
		{ID: 2, TopicTags: `["go","testing","devops"]`},
		{ID: 3, TopicTags: `["go","ci","automation"]`},
		{ID: 4, TopicTags: `["cooking","recipes","italian"]`},
	}

	clusters := buildClusters(items, 0.3, 3)
	if len(clusters) != 1 {
		t.Fatalf("expected 1 cluster, got %d", len(clusters))
	}
	if len(clusters[0]) != 3 {
		t.Errorf("cluster should have 3 items, got %d", len(clusters[0]))
	}
}

func TestBuildClustersNoCluster(t *testing.T) {
	// All items different — no cluster should form.
	items := []QueueItem{
		{ID: 1, TopicTags: `["go"]`},
		{ID: 2, TopicTags: `["rust"]`},
		{ID: 3, TopicTags: `["python"]`},
	}
	clusters := buildClusters(items, 0.4, 3)
	if len(clusters) != 0 {
		t.Errorf("expected 0 clusters, got %d", len(clusters))
	}
}

func TestBuildClustersBelowMinItems(t *testing.T) {
	items := []QueueItem{
		{ID: 1, TopicTags: `["go","testing"]`},
		{ID: 2, TopicTags: `["go","testing"]`},
	}
	clusters := buildClusters(items, 0.4, 3)
	if len(clusters) != 0 {
		t.Errorf("expected 0 clusters (below minItems), got %d", len(clusters))
	}
}

func TestClusterTheme(t *testing.T) {
	items := []QueueItem{
		{ID: 1, TopicTags: `["go","testing","ci"]`},
		{ID: 2, TopicTags: `["go","testing","devops"]`},
		{ID: 3, TopicTags: `["go","testing","automation"]`},
	}
	theme := clusterTheme(items)
	if theme != "go, testing" {
		t.Errorf("theme = %q, want %q", theme, "go, testing")
	}
}

func TestDetectClustersEndToEnd(t *testing.T) {
	q := newTestQueue(t)

	// Create 4 items with overlapping tags.
	for i, tags := range []string{
		`["kubernetes","devops","cloud"]`,
		`["kubernetes","devops","infrastructure"]`,
		`["kubernetes","cloud","monitoring"]`,
		`["cooking","recipes"]`, // unrelated
	} {
		id, _ := q.Enqueue(&ShareRequest{Type: "url", URL: fmt.Sprintf("https://cluster-%d.test", i), Profile: "eng"})
		q.UpdateScore(id, 70+i*5, "go", "verdict", fmt.Sprintf("cluster-%d", i), "", "")
		q.SetTopicTags(id, parseTags(tags))
	}

	detectClusters(context.Background(), q, "eng", 0.3, 3)

	clusters, err := q.ListClusters("eng")
	if err != nil {
		t.Fatalf("ListClusters: %v", err)
	}
	if len(clusters) != 1 {
		t.Fatalf("expected 1 cluster, got %d", len(clusters))
	}
	if clusters[0].ItemCount != 3 {
		t.Errorf("cluster item_count = %d, want 3", clusters[0].ItemCount)
	}

	// Verify cluster_id is set on member items.
	items, err := q.GetClusterItems(clusters[0].ID)
	if err != nil {
		t.Fatalf("GetClusterItems: %v", err)
	}
	if len(items) != 3 {
		t.Errorf("expected 3 cluster items, got %d", len(items))
	}
}

func TestTagIntersection(t *testing.T) {
	a := []string{"go", "testing", "ci"}
	b := []string{"go", "ci", "automation"}
	got := tagIntersection(a, b)
	if len(got) != 2 || got[0] != "ci" || got[1] != "go" {
		t.Errorf("tagIntersection = %v, want [ci go]", got)
	}
}
