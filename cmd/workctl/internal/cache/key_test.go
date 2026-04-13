package cache

import "testing"

func TestKeyDeterminism(t *testing.T) {
	// Same inputs must produce the same key every time.
	k1 := JiraUserKey("alice@example.com")
	k2 := JiraUserKey("alice@example.com")
	if k1 != k2 {
		t.Errorf("JiraUserKey not deterministic: %s != %s", k1, k2)
	}
}

func TestKeyUniqueness(t *testing.T) {
	k1 := JiraUserKey("alice@example.com")
	k2 := JiraUserKey("bob@example.com")
	if k1 == k2 {
		t.Error("different emails produced the same key")
	}
}

func TestSortedKeysProduceSameResult(t *testing.T) {
	k1 := JiraProjectIssuesKey([]string{"SR", "ISRE"}, "2026-01-01", "2026-02-15", nil, nil, nil)
	k2 := JiraProjectIssuesKey([]string{"ISRE", "SR"}, "2026-01-01", "2026-02-15", nil, nil, nil)
	if k1 != k2 {
		t.Errorf("reordered project keys produced different cache keys: %s != %s", k1, k2)
	}
}

func TestJiraAssignedIssuesKey(t *testing.T) {
	k1 := JiraAssignedIssuesKey("acct123", "2026-01-01", "2026-02-15", []string{"Done"}, nil, nil)
	k2 := JiraAssignedIssuesKey("acct123", "2026-01-01", "2026-02-15", nil, nil, nil)
	if k1 == k2 {
		t.Error("different status filters produced the same key")
	}
}

func TestConfluenceUserKey(t *testing.T) {
	k1 := ConfluenceUserKey("alice@example.com")
	// Should differ from Jira user key for the same email.
	k2 := JiraUserKey("alice@example.com")
	if k1 == k2 {
		t.Error("Confluence and Jira user keys should differ for same email")
	}
}

func TestConfluenceArticlesKey(t *testing.T) {
	k1 := ConfluenceArticlesKey("acct1", "2026-01-01", "2026-02-15", "page", false)
	k2 := ConfluenceArticlesKey("acct1", "2026-01-01", "2026-02-15", "page", true)
	if k1 == k2 {
		t.Error("different hydrate flags should produce different keys")
	}
}

func TestConfluenceSpacePagesKey(t *testing.T) {
	k1 := ConfluenceSpacePagesKey([]string{"ENG", "INFRA"}, "2026-01-01", "2026-02-15", "page", false)
	k2 := ConfluenceSpacePagesKey([]string{"INFRA", "ENG"}, "2026-01-01", "2026-02-15", "page", false)
	if k1 != k2 {
		t.Error("reordered space keys should produce the same cache key")
	}
}

func TestGitHubActivityKey(t *testing.T) {
	key1, src1 := GitHubActivityKey("user1", "events", "2026-01-01", "2026-02-15")
	key2, src2 := GitHubActivityKey("user1", "search", "2026-01-01", "2026-02-15")
	if key1 == key2 {
		t.Error("different strategies should produce different keys")
	}
	if src1 != SourceGitHubEvents {
		t.Errorf("events strategy source = %s, want %s", src1, SourceGitHubEvents)
	}
	if src2 != SourceGitHubSearch {
		t.Errorf("search strategy source = %s, want %s", src2, SourceGitHubSearch)
	}

	_, src3 := GitHubActivityKey("user1", "graphql", "2026-01-01", "2026-02-15")
	if src3 != SourceGitHubGraphQL {
		t.Errorf("graphql strategy source = %s, want %s", src3, SourceGitHubGraphQL)
	}
}

func TestKeyLength(t *testing.T) {
	// SHA-256 hex digest should be 64 characters.
	k := JiraUserKey("test@example.com")
	if len(k) != 64 {
		t.Errorf("key length = %d, want 64 (SHA-256 hex)", len(k))
	}
}
