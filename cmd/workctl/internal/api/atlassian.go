package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	cboff "github.com/cenkalti/backoff/v4"
	confluence "github.com/ctreminiom/go-atlassian/v2/confluence"
	jira "github.com/ctreminiom/go-atlassian/v2/jira/v3"
	"github.com/ctreminiom/go-atlassian/v2/pkg/infra/models"
	"golang.org/x/time/rate"

	"github.com/blo-grindr/runabout/cmd/workctl/internal/config"
)

const (
	maxRetries        = 5
	apiTimeoutSeconds = 60
)

// EscapeCQLString escapes double quotes in a string for safe interpolation
// into CQL query values enclosed in double quotes.
func EscapeCQLString(s string) string {
	return strings.ReplaceAll(s, `"`, `\"`)
}

// AtlassianClients wraps both Jira and Confluence clients with rate limiting
type AtlassianClients struct {
	Jira        *jira.Client
	Confluence  *confluence.Client
	domain      string // Store domain for URL construction
	rateLimiter *rate.Limiter
}

// NewAtlassianClients creates Jira and Confluence clients with authentication
func NewAtlassianClients(domain, email, token string) (*AtlassianClients, error) {
	siteURL := fmt.Sprintf("https://%s", domain)

	// Create custom HTTP client with timeout
	httpClient := &http.Client{
		Timeout: apiTimeoutSeconds * time.Second,
	}

	// Initialize Jira v3 client
	jiraClient, err := jira.New(httpClient, siteURL)
	if err != nil {
		return nil, fmt.Errorf("failed to create Jira client: %w", err)
	}
	jiraClient.Auth.SetBasicAuth(email, token)

	// Initialize Confluence v1 client (for CQL search support)
	confluenceClient, err := confluence.New(httpClient, siteURL)
	if err != nil {
		return nil, fmt.Errorf("failed to create Confluence client: %w", err)
	}
	confluenceClient.Auth.SetBasicAuth(email, token)

	return &AtlassianClients{
		Jira:        jiraClient,
		Confluence:  confluenceClient,
		domain:      domain,
		rateLimiter: rate.NewLimiter(rate.Every(time.Second), 1),
	}, nil
}

// withRateLimitAndRetry wraps API calls with rate limiting and exponential backoff retry.
func (a *AtlassianClients) withRateLimitAndRetry(fn func() error) error {
	if err := a.rateLimiter.Wait(context.Background()); err != nil {
		return fmt.Errorf("rate limiter cancelled: %w", err)
	}

	bo := cboff.WithMaxRetries(cboff.NewExponentialBackOff(), maxRetries-1)
	notify := func(err error, d time.Duration) {
		config.LogDebug("Request failed: %v, retrying in %v", err, d)
	}
	if err := cboff.RetryNotify(fn, bo, notify); err != nil {
		return fmt.Errorf("failed after %d retries: %w", maxRetries, err)
	}
	return nil
}

// GetJiraUserAccountID searches for a Jira user by email and returns their account ID
func (a *AtlassianClients) GetJiraUserAccountID(email string) (string, error) {
	if config.Debug {
		config.LogDebug("Searching for Jira user with email: %s", config.RedactEmail(email))
	}

	var users []*models.UserScheme
	var response *models.ResponseScheme

	err := a.withRateLimitAndRetry(func() error {
		ctx := context.Background()
		var err error
		users, response, err = a.Jira.User.Search.Do(ctx, "", email, 0, 50)
		return err
	})

	if err != nil {
		if response != nil && config.Debug {
			config.LogDebug("API returned status %d: %s", response.Code, response.Endpoint)
		}
		return "", fmt.Errorf("failed to fetch Jira user: %w", err)
	}

	if len(users) == 0 {
		return "", errors.New("no Jira user found with the given email")
	}

	user := users[0]
	if config.Debug {
		config.LogDebug("Found Jira user details:")
		config.LogDebug("  - Account ID: %s", user.AccountID)
		config.LogDebug("  - Name: %s", config.RedactName(user.DisplayName))
		config.LogDebug("  - Email: %s", config.RedactEmail(user.EmailAddress))
	}

	return user.AccountID, nil
}

// GetConfluenceUserAccountID searches for a Confluence user by email and returns their account ID
func (a *AtlassianClients) GetConfluenceUserAccountID(email string) (string, error) {
	// Extract name from email (assuming format: firstname.lastname@domain.com)
	name := strings.Split(email, "@")[0]
	name = strings.ReplaceAll(name, ".", " ")

	// Build CQL query to search by full name.
	// Escape double quotes to prevent CQL injection via crafted email local parts.
	cql := fmt.Sprintf("user.fullname ~ \"%s\"", EscapeCQLString(name))

	if config.Debug {
		config.LogDebug("Searching for Confluence user with name: %s", config.RedactName(name))
		config.LogDebug("Using CQL query: %s", cql)
	}

	var result *models.SearchPageScheme
	var response *models.ResponseScheme

	err := a.withRateLimitAndRetry(func() error {
		ctx := context.Background()
		var err error
		result, response, err = a.Confluence.Search.Users(ctx, cql, 0, 10, []string{})
		return err
	})

	if err != nil {
		if response != nil && config.Debug {
			config.LogDebug("API returned status %d: %s", response.Code, response.Endpoint)
		}
		return "", fmt.Errorf("failed to fetch Confluence user: %w", err)
	}

	if config.Debug {
		config.LogDebug("Found %d Confluence users for email: %s", result.Size, config.RedactEmail(email))
	}

	if result.Size == 0 {
		return "", errors.New("no Confluence user found with the given email")
	}

	// Extract account ID from the first result
	if len(result.Results) > 0 && result.Results[0].User != nil {
		accountID := result.Results[0].User.AccountID
		if config.Debug {
			config.LogDebug("Found Confluence user details:")
			config.LogDebug("  - Account ID: %s", accountID)
			config.LogDebug("  - Name: %s", config.RedactName(result.Results[0].User.DisplayName))
		}
		return accountID, nil
	}

	return "", errors.New("unable to extract account ID from Confluence user search results")
}
