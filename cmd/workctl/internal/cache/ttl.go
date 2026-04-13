package cache

import "time"

// Source identifies the origin of cached data.
const (
	SourceJira          = "jira"
	SourceConfluence    = "confluence"
	SourceGitHubEvents  = "github_events"
	SourceGitHubSearch  = "github_search"
	SourceGitHubGraphQL = "github_graphql"
)

// Default TTLs per source. These can be overridden via config.
var DefaultTTLs = map[string]time.Duration{
	SourceJira:          1 * time.Hour,
	SourceConfluence:    1 * time.Hour,
	SourceGitHubEvents:  15 * time.Minute,
	SourceGitHubSearch:  24 * time.Hour,
	SourceGitHubGraphQL: 24 * time.Hour,
}

// TTLFor returns the TTL for a source, falling back to the default.
// If overrides is nil or the source is not found, the package default is used.
// If neither is found, returns 1 hour.
func TTLFor(source string, overrides map[string]time.Duration) time.Duration {
	if overrides != nil {
		if ttl, ok := overrides[source]; ok {
			return ttl
		}
	}
	if ttl, ok := DefaultTTLs[source]; ok {
		return ttl
	}
	return 1 * time.Hour
}
