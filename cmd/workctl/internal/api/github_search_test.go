package api

import (
	"testing"
)

// TestExtractRepoNameFromURL tests repository name extraction from GitHub API URLs
func TestExtractRepoNameFromURL(t *testing.T) {
	tests := []struct {
		name string
		url  string
		want string
	}{
		{
			name: "standard GitHub API URL",
			url:  "https://api.github.com/repos/example-org/infra-terraform",
			want: "example-org/infra-terraform",
		},
		{
			name: "URL with trailing slash",
			url:  "https://api.github.com/repos/octocat/Hello-World/",
			want: "octocat/Hello-World/",
		},
		{
			name: "URL with nested path",
			url:  "https://api.github.com/repos/kubernetes/kubernetes",
			want: "kubernetes/kubernetes",
		},
		{
			name: "malformed URL (no /repos/)",
			url:  "https://github.com/example-org/infra-terraform",
			want: "https://github.com/example-org/infra-terraform", // Fallback: return original
		},
		{
			name: "empty URL",
			url:  "",
			want: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractRepoNameFromURL(tt.url)
			if got != tt.want {
				t.Errorf("extractRepoNameFromURL() = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestConvertSearchResultToActivity tests conversion of search results to activities
func TestConvertSearchResultToActivity(t *testing.T) {
	// Note: This would require mocking github.Issue objects
	// For now, we test the helper function above which is pure logic
	// Integration tests will validate the full conversion pipeline
	t.Skip("Requires mocking github.Issue - covered by integration tests")
}

// BenchmarkExtractRepoNameFromURL benchmarks the repo name extraction
func BenchmarkExtractRepoNameFromURL(b *testing.B) {
	url := "https://api.github.com/repos/example-org/infra-terraform"
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		extractRepoNameFromURL(url)
	}
}
