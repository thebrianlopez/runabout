package version

import (
	"encoding/json"
	"fmt"
	"runtime"
	"runtime/debug"
)

// Overridden via -ldflags at build time.
var (
	Version   = "dev"
	Commit    = "unknown"
	BuildDate = "unknown"
)

// Info holds version metadata.
type Info struct {
	Version   string `json:"version"`
	Commit    string `json:"commit"`
	BuildDate string `json:"build_date"`
	GoVersion string `json:"go_version"`
	Platform  string `json:"platform"`
}

// Get returns the current build's version information.
// If no ldflags were injected at build time, it falls back to
// runtime/debug VCS info (populated by go install / go run from a git repo).
func Get() Info {
	info := Info{
		Version:   Version,
		Commit:    Commit,
		BuildDate: BuildDate,
		GoVersion: runtime.Version(),
		Platform:  runtime.GOOS + "/" + runtime.GOARCH,
	}

	// Fallback: if no ldflags were set, try runtime/debug VCS info.
	if info.Version == "dev" {
		if bi, ok := debug.ReadBuildInfo(); ok {
			for _, s := range bi.Settings {
				switch s.Key {
				case "vcs.revision":
					if len(s.Value) >= 7 {
						info.Commit = s.Value[:7]
					}
				case "vcs.time":
					info.BuildDate = s.Value
				case "vcs.modified":
					if s.Value == "true" {
						info.Commit += "-dirty"
					}
				}
			}
		}
	}

	return info
}

// String returns a human-readable version string.
func (i Info) String() string {
	return fmt.Sprintf("workctl %s (commit: %s, built: %s, %s, %s)",
		i.Version, i.Commit, i.BuildDate, i.GoVersion, i.Platform)
}

// JSON returns the version info as an indented JSON string.
func (i Info) JSON() string {
	b, _ := json.MarshalIndent(i, "", "  ")
	return string(b)
}
