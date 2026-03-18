package version

import "fmt"

// Format returns a human-readable version string for CLI --version output.
func Format(version, commit, date string) string {
	return fmt.Sprintf("%s (commit: %s, built: %s)", version, commit, date)
}
