package insights

import (
	"fmt"
	"io"
	"math"
	"sort"
	"strings"
)

// TrackResult holds the output of scoring a SignalSet against a career track.
type TrackResult struct {
	Track       string           `json:"track"`
	Description string           `json:"description"`
	Dimensions  []DimensionScore `json:"dimensions"`
	Overall     float64          `json:"overall"` // weighted average, 0.0-1.0
}

// DimensionScore holds one dimension's contribution to a track score.
type DimensionScore struct {
	Name       string  `json:"name"`
	Raw        float64 `json:"raw"`
	Normalized float64 `json:"normalized"` // capped at 1.0
	Weight     float64 `json:"weight"`
	Weighted   float64 `json:"weighted"` // normalized * weight
}

// defaultCeilings maps each dimension to its normalization ceiling.
var defaultCeilings = map[string]float64{
	"cross_team_impact":   1.0,
	"pr_review_ratio":     1.0,
	"multi_project_span":  5.0,
	"infra_theme_ratio":   1.0,
	"change_velocity":     20.0,
	"incident_reduction":  1.0,
	"pr_comment_ratio":    1.0,
	"collaborator_span":   8.0,
	"operational_cadence": 0.5,  // 50% infra commands in shell history → full score
	"automation_maturity": 0.75, // 75% agent events in audit log → full score
}

// builtinTrack defines a track's description and dimension weights (sum to 1.0).
type builtinTrack struct {
	description string
	weights     map[string]float64
}

var builtinTracks = map[string]builtinTrack{
	"staff": {
		description: "Staff Engineer — cross-team impact, velocity, breadth",
		weights: map[string]float64{
			"cross_team_impact":   0.20,
			"pr_review_ratio":     0.10,
			"multi_project_span":  0.15,
			"infra_theme_ratio":   0.05,
			"change_velocity":     0.10, // was 0.15; -0.05 to fund automation_maturity
			"incident_reduction":  0.05,
			"pr_comment_ratio":    0.10,
			"collaborator_span":   0.15,
			"operational_cadence": 0.05, // shell infra command density
			"automation_maturity": 0.05, // AI-assisted work intensity
		},
	},
	"platform": {
		description: "Platform Engineer — infrastructure depth, reliability, operational cadence",
		weights: map[string]float64{
			"cross_team_impact":   0.10,
			"pr_review_ratio":     0.05, // was 0.10; -0.05 to fund automation_maturity
			"multi_project_span":  0.00,
			"infra_theme_ratio":   0.25,
			"change_velocity":     0.05,
			"incident_reduction":  0.20,
			"pr_comment_ratio":    0.05,
			"collaborator_span":   0.10,
			"operational_cadence": 0.10, // was 0.15; -0.05 to fund automation_maturity
			"automation_maturity": 0.10, // AI tooling adoption for platform work
		},
	},
	"manager": {
		description: "Engineering Manager — collaboration, cross-team, mentorship",
		weights: map[string]float64{
			"cross_team_impact":   0.20,
			"pr_review_ratio":     0.20,
			"multi_project_span":  0.10,
			"infra_theme_ratio":   0.00,
			"change_velocity":     0.05,
			"incident_reduction":  0.05,
			"pr_comment_ratio":    0.20,
			"collaborator_span":   0.20,
			"operational_cadence": 0.00,
			"automation_maturity": 0.00, // not relevant for management track
		},
	},
}

// extractDimensions derives raw dimension values from a SignalSet.
func extractDimensions(s *SignalSet) map[string]float64 {
	d := make(map[string]float64, 10)

	// cross_team_impact: fraction of issues from non-primary projects
	if s.TotalIssues > 0 {
		d["cross_team_impact"] = float64(s.Collaboration.CrossTeamIssues) / float64(s.TotalIssues)
	}

	// pr_review_ratio: PR reviews / total activities
	if s.TotalActivities > 0 {
		d["pr_review_ratio"] = float64(s.Collaboration.PRReviews) / float64(s.TotalActivities)
	}

	// multi_project_span: number of distinct projects
	d["multi_project_span"] = float64(len(s.ProjectFocus))

	// infra_theme_ratio: infrastructure issues / total issues
	if s.TotalIssues > 0 {
		d["infra_theme_ratio"] = float64(s.ThemeCounts[ThemeInfrastructure]) / float64(s.TotalIssues)
	}

	// change_velocity: average monthly closed issues
	if len(s.Velocity) > 0 {
		totalClosed := 0
		for _, v := range s.Velocity {
			totalClosed += v.Closed
		}
		d["change_velocity"] = float64(totalClosed) / float64(len(s.Velocity))
	}

	// incident_reduction: 1 - incident ratio (higher is better)
	d["incident_reduction"] = 1.0 - s.Ownership.IncidentRatio

	// pr_comment_ratio: issue comments / total activities
	if s.TotalActivities > 0 {
		d["pr_comment_ratio"] = float64(s.Collaboration.IssueComments) / float64(s.TotalActivities)
	}

	// collaborator_span: unique repos contributed to
	d["collaborator_span"] = float64(s.Collaboration.UniqueRepos)

	// operational_cadence: fraction of shell commands that are infrastructure operations.
	// Derived from local shell history (EPIC-015); remains 0 when --shell=false or no data.
	if s.ShellActivity != nil && s.ShellActivity.TotalCommands > 0 {
		d["operational_cadence"] = float64(s.ShellActivity.InfraCommands) / float64(s.ShellActivity.TotalCommands)
	}

	// automation_maturity: fraction of audit log events from agent sources.
	// Measures AI-assisted work intensity (EPIC-021). Remains 0 when no AI data.
	if s.AIActivity != nil {
		totalCmds := s.AIActivity.HumanCommands + s.AIActivity.AgentCommands
		if totalCmds > 0 {
			d["automation_maturity"] = float64(s.AIActivity.AgentCommands) / float64(totalCmds)
		}
	}

	return d
}

// CustomTrack defines a user-provided career track (mirrors config.TrackConfig).
type CustomTrack struct {
	Description string
	Inherit     string // parent track name (builtin or custom); empty = no inheritance
	Weights     map[string]float64
}

// knownDimensions is the set of recognized dimension names.
var knownDimensions = map[string]bool{
	"cross_team_impact":   true,
	"pr_review_ratio":     true,
	"multi_project_span":  true,
	"infra_theme_ratio":   true,
	"change_velocity":     true,
	"incident_reduction":  true,
	"pr_comment_ratio":    true,
	"collaborator_span":   true,
	"operational_cadence": true, // fraction of shell commands that are infra operations (EPIC-015)
	"automation_maturity": true, // fraction of audit events from agent sources (EPIC-021)
}

// ValidateTrackWeights checks that weights sum to ~1.0 and all dimension names are recognized.
func ValidateTrackWeights(weights map[string]float64) error {
	var sum float64
	for name, w := range weights {
		if !knownDimensions[name] {
			known := make([]string, 0, len(knownDimensions))
			for k := range knownDimensions {
				known = append(known, k)
			}
			sort.Strings(known)
			return fmt.Errorf("unknown dimension %q (known: %s)", name, strings.Join(known, ", "))
		}
		sum += w
	}
	if math.Abs(sum-1.0) > 0.001 {
		return fmt.Errorf("weights sum to %.4f, want 1.0", sum)
	}
	return nil
}

// ScoreTrack scores a SignalSet against a named career track.
func ScoreTrack(track string, signals *SignalSet, ceilings map[string]float64, custom map[string]CustomTrack) (*TrackResult, error) {
	weights, desc, err := ResolveTrack(track, custom)
	if err != nil {
		return nil, err
	}

	merged := ResolveCeilings(ceilings)
	rawDims := extractDimensions(signals)

	dims := make([]DimensionScore, 0, len(weights))
	var overall float64

	// Sort dimension names for deterministic output
	names := make([]string, 0, len(weights))
	for name := range weights {
		names = append(names, name)
	}
	sort.Strings(names)

	for _, name := range names {
		w := weights[name]
		raw := rawDims[name]
		ceil := merged[name]
		if ceil <= 0 {
			ceil = 1.0
		}

		normalized := raw / ceil
		if normalized > 1.0 {
			normalized = 1.0
		}
		normalized = math.Round(normalized*1000) / 1000

		weighted := normalized * w

		dims = append(dims, DimensionScore{
			Name:       name,
			Raw:        raw,
			Normalized: normalized,
			Weight:     w,
			Weighted:   weighted,
		})
		overall += weighted
	}

	return &TrackResult{
		Track:       track,
		Description: desc,
		Dimensions:  dims,
		Overall:     math.Round(overall*1000) / 1000,
	}, nil
}

// ResolveTrack returns the weights and description for a track name.
// Custom tracks take precedence over builtins. If a custom track has an
// Inherit field, the parent's weights are used as the base and the child's
// weights are overlaid. Cycle detection prevents infinite recursion.
func ResolveTrack(name string, custom map[string]CustomTrack) (weights map[string]float64, description string, err error) {
	return resolveTrackInherited(name, custom, make(map[string]bool))
}

func resolveTrackInherited(name string, custom map[string]CustomTrack, visited map[string]bool) (map[string]float64, string, error) {
	if visited[name] {
		return nil, "", fmt.Errorf("track inheritance cycle detected at %q", name)
	}
	visited[name] = true

	if ct, ok := custom[name]; ok {
		if ct.Inherit != "" {
			parentWeights, _, err := resolveTrackInherited(ct.Inherit, custom, visited)
			if err != nil {
				return nil, "", fmt.Errorf("resolving parent %q for track %q: %w", ct.Inherit, name, err)
			}
			merged := make(map[string]float64, len(parentWeights))
			for k, v := range parentWeights {
				merged[k] = v
			}
			for k, v := range ct.Weights {
				merged[k] = v
			}
			return merged, ct.Description, nil
		}
		return ct.Weights, ct.Description, nil
	}

	bt, ok := builtinTracks[name]
	if !ok {
		known := ListTracks(custom)
		return nil, "", fmt.Errorf("unknown track %q (available: %s)", name, strings.Join(known, ", "))
	}
	return bt.weights, bt.description, nil
}

// ScoreAllTracks scores a SignalSet against every available track (builtin + custom)
// and returns the results keyed by track name.
func ScoreAllTracks(signals *SignalSet, ceilings map[string]float64, custom map[string]CustomTrack) ([]*TrackResult, error) {
	names := ListTracks(custom)
	results := make([]*TrackResult, 0, len(names))
	for _, name := range names {
		result, err := ScoreTrack(name, signals, ceilings, custom)
		if err != nil {
			return nil, fmt.Errorf("scoring track %q: %w", name, err)
		}
		results = append(results, result)
	}
	return results, nil
}

// ResolveCeilings merges overrides onto default ceilings.
func ResolveCeilings(overrides map[string]float64) map[string]float64 {
	merged := make(map[string]float64, len(defaultCeilings))
	for k, v := range defaultCeilings {
		merged[k] = v
	}
	for k, v := range overrides {
		merged[k] = v
	}
	return merged
}

// ListTracks returns sorted, deduplicated names of all builtin and custom tracks.
func ListTracks(custom map[string]CustomTrack) []string {
	seen := make(map[string]bool, len(builtinTracks)+len(custom))
	for name := range builtinTracks {
		seen[name] = true
	}
	for name := range custom {
		seen[name] = true
	}
	names := make([]string, 0, len(seen))
	for name := range seen {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// RenderCareer writes a markdown career track report.
func RenderCareer(w io.Writer, result *TrackResult, period string) {
	fmt.Fprintf(w, "# Career Track Analysis: %s\n\n", result.Track)
	fmt.Fprintf(w, "**Track:** %s  \n", result.Description)
	fmt.Fprintf(w, "**Period:** %s  \n", period)
	fmt.Fprintf(w, "**Overall Score:** %.1f%%\n\n", result.Overall*100)
	fmt.Fprintf(w, "---\n\n")

	fmt.Fprintf(w, "## Dimension Scores\n\n")
	fmt.Fprintf(w, "| Dimension | Raw | Normalized | Weight | Weighted |\n")
	fmt.Fprintf(w, "|-----------|----:|-----------:|-------:|---------:|\n")
	for _, d := range result.Dimensions {
		fmt.Fprintf(w, "| %s | %.2f | %.3f | %.2f | %.3f |\n",
			d.Name, d.Raw, d.Normalized, d.Weight, d.Weighted)
	}
	fmt.Fprintln(w)

	fmt.Fprintf(w, "## Interpretation\n\n")
	// Identify top 3 dimensions by weighted score
	sorted := make([]DimensionScore, len(result.Dimensions))
	copy(sorted, result.Dimensions)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Weighted > sorted[j].Weighted })

	fmt.Fprintf(w, "**Strengths:**\n")
	for i := 0; i < 3 && i < len(sorted); i++ {
		d := sorted[i]
		if d.Weighted > 0 {
			fmt.Fprintf(w, "- %s (%.1f%% contribution)\n", d.Name, d.Weighted/result.Overall*100)
		}
	}
	fmt.Fprintln(w)

	// Identify weakest dimensions (lowest normalized with non-zero weight)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Normalized < sorted[j].Normalized })
	fmt.Fprintf(w, "**Growth Areas:**\n")
	for i := 0; i < 3 && i < len(sorted); i++ {
		d := sorted[i]
		if d.Weight > 0 {
			fmt.Fprintf(w, "- %s (%.0f%% of ceiling)\n", d.Name, d.Normalized*100)
		}
	}
	fmt.Fprintln(w)
}
