package tailoring

import (
	"context"
	"fmt"
	"os/exec"

	"github.com/thebrianlopez/runabout/cmd/runway/internal/scoring"
)

// detectFabrication returns true if the tailored resume contains content
// absent from the source: either a skill category not in source, or a bullet
// not present verbatim in source experience.
func detectFabrication(source, tailored *scoring.Resume) bool {
	// Skill categories: none may be added
	sourceCategories := make(map[string]struct{}, len(source.Skills))
	for cat := range source.Skills {
		sourceCategories[cat] = struct{}{}
	}
	for cat := range tailored.Skills {
		if _, ok := sourceCategories[cat]; !ok {
			return true
		}
	}

	// Experience bullets: every tailored bullet must exist verbatim in source
	sourceBullets := make(map[string]struct{})
	for _, entry := range source.Experience {
		for _, role := range entry.Roles {
			for _, bullet := range role.Bullets {
				sourceBullets[bullet] = struct{}{}
			}
		}
	}
	for _, entry := range tailored.Experience {
		for _, role := range entry.Roles {
			for _, bullet := range role.Bullets {
				if _, ok := sourceBullets[bullet]; !ok {
					return true
				}
			}
		}
	}
	return false
}

// checkSummaryBounds returns true if the tailored summary length is within
// ±15% of the source summary length (character count).
// Empty source or tailored summary always returns false (bounds violated).
func checkSummaryBounds(source, tailored string) bool {
	if len(source) == 0 || len(tailored) == 0 {
		return false
	}
	ratio := float64(len(tailored)) / float64(len(source))
	return ratio >= 0.85 && ratio <= 1.15
}

// PyKwalifyValidator implements SchemaValidator using the pykwalify subprocess.
type PyKwalifyValidator struct {
	SchemaPath string // path to resume-schema.yaml
}

// Validate runs pykwalify against the given YAML file.
// Returns nil on exit 0, error on non-zero exit (stderr captured in error message).
func (v *PyKwalifyValidator) Validate(ctx context.Context, yamlPath string) error {
	cmd := exec.CommandContext(ctx, "pykwalify", "-d", yamlPath, "-s", v.SchemaPath)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("pykwalify validation failed: %w\n%s", err, string(out))
	}
	return nil
}
