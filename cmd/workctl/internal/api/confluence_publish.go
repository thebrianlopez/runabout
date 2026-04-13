package api

import (
	"context"
	"fmt"

	model "github.com/ctreminiom/go-atlassian/v2/pkg/infra/models"
)

// PublishPage creates a new Confluence page under ancestorID in the given space.
// ancestorID may be a folder ID or an existing page ID (Confluence Cloud supports
// folder ancestors via the v1 Content API).
//
// Returns (pageID, pageURL, error). Uses withRateLimitAndRetry for resilience.
func (a *AtlassianClients) PublishPage(ctx context.Context, spaceKey, ancestorID, title, htmlBody string) (string, string, error) {
	payload := &model.ContentScheme{
		Type:  "page",
		Title: title,
		Space: &model.SpaceScheme{Key: spaceKey},
		Ancestors: []*model.ContentScheme{
			{ID: ancestorID},
		},
		Body: &model.BodyScheme{
			Storage: &model.BodyNodeScheme{
				Value:          htmlBody,
				Representation: "storage",
			},
		},
	}

	var result *model.ContentScheme

	if err := a.withRateLimitAndRetry(func() error {
		var err error
		result, _, err = a.Confluence.Content.Create(ctx, payload)
		return err
	}); err != nil {
		return "", "", fmt.Errorf("publishing page %q: %w", title, err)
	}

	if result == nil {
		return "", "", fmt.Errorf("publishing page %q: nil response from Confluence API", title)
	}

	pageURL := fmt.Sprintf("https://%s/wiki/spaces/%s/pages/%s", a.domain, spaceKey, result.ID)
	return result.ID, pageURL, nil
}
