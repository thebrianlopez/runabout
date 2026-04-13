package config

import (
	"strings"
	"testing"

	"github.com/blo-grindr/runabout/cmd/workctl/internal/models"
)

// ---------------------------------------------------------------------------
// ParseCSV
// ---------------------------------------------------------------------------

func TestParseCSV(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  []string
	}{
		{"empty", "", nil},
		{"single", "SR", []string{"SR"}},
		{"multiple", "SR,ISRE,INFRA", []string{"SR", "ISRE", "INFRA"}},
		{"trim spaces", " SR , ISRE ", []string{"SR", "ISRE"}},
		{"skip empty parts", "SR,,ISRE", []string{"SR", "ISRE"}},
		{"only commas", ",,", nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ParseCSV(tt.input)
			if len(got) != len(tt.want) {
				t.Fatalf("ParseCSV(%q) = %v (len %d), want %v (len %d)", tt.input, got, len(got), tt.want, len(tt.want))
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("ParseCSV(%q)[%d] = %q, want %q", tt.input, i, got[i], tt.want[i])
				}
			}
		})
	}
}

// ---------------------------------------------------------------------------
// ParseGitHubRepo
// ---------------------------------------------------------------------------

func TestParseGitHubRepo(t *testing.T) {
	tests := []struct {
		name      string
		input     string
		wantOwner string
		wantRepo  string
		wantErr   bool
	}{
		{"valid", "myorg/myrepo", "myorg", "myrepo", false},
		{"valid with hyphen", "my-org/my-repo", "my-org", "my-repo", false},
		{"no slash", "myorgrepo", "", "", true},
		{"empty owner", "/myrepo", "", "", true},
		{"empty repo", "myorg/", "", "", true},
		{"both empty", "/", "", "", true},
		{"empty string", "", "", "", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			owner, repo, err := ParseGitHubRepo(tt.input)
			if (err != nil) != tt.wantErr {
				t.Fatalf("ParseGitHubRepo(%q) error = %v, wantErr %v", tt.input, err, tt.wantErr)
			}
			if !tt.wantErr {
				if owner != tt.wantOwner {
					t.Errorf("owner = %q, want %q", owner, tt.wantOwner)
				}
				if repo != tt.wantRepo {
					t.Errorf("repo = %q, want %q", repo, tt.wantRepo)
				}
			}
		})
	}
}

// ---------------------------------------------------------------------------
// ValidateGitHubRepos
// ---------------------------------------------------------------------------

func TestValidateGitHubRepos(t *testing.T) {
	tests := []struct {
		name    string
		repos   []string
		wantErr bool
	}{
		{"nil slice", nil, false},
		{"empty slice", []string{}, false},
		{"valid repos", []string{"org/repo1", "org/repo2"}, false},
		{"first invalid", []string{"badrepo", "org/repo2"}, true},
		{"second invalid", []string{"org/repo1", "badrepo"}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateGitHubRepos(tt.repos)
			if (err != nil) != tt.wantErr {
				t.Errorf("ValidateGitHubRepos(%v) error = %v, wantErr %v", tt.repos, err, tt.wantErr)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// IsValidProjectKey
// ---------------------------------------------------------------------------

func TestIsValidProjectKey(t *testing.T) {
	tests := []struct {
		name string
		key  string
		want bool
	}{
		{"2 chars valid", "SR", true},
		{"10 chars valid", "ABCDEFGHIJ", true},
		{"5 chars valid", "ISRE1", false}, // digits not allowed
		{"1 char too short", "S", false},
		{"11 chars too long", "ABCDEFGHIJK", false},
		{"lowercase invalid", "isre", false},
		{"mixed case invalid", "Isre", false},
		{"digits invalid", "12", false},
		{"empty invalid", "", false},
		{"uppercase letters only 3", "ENG", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := IsValidProjectKey(tt.key)
			if got != tt.want {
				t.Errorf("IsValidProjectKey(%q) = %v, want %v", tt.key, got, tt.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// IsValidSpaceKey
// ---------------------------------------------------------------------------

func TestIsValidSpaceKey(t *testing.T) {
	tests := []struct {
		name string
		key  string
		want bool
	}{
		{"empty invalid", "", false},
		{"single char valid", "X", true},
		{"normal key valid", "ENG", true},
		{"lowercase valid", "myspace", true},
		{"hyphen valid", "my-space", true},
		{"underscore valid", "SPACE_1", true},
		{"mixed valid", "My-Space_2", true},
		{"255 chars valid", strings.Repeat("A", 255), true},
		{"256 chars invalid", strings.Repeat("A", 256), false},
		// CQL injection attempts
		{"CQL injection paren", `KEY) OR text = "secret`, false},
		{"CQL injection quote", `KEY"injected`, false},
		{"space character", "MY SPACE", false},
		{"semicolon", "KEY;DROP", false},
		{"backslash", `KEY\x`, false},
		{"equals sign", "KEY=val", false},
		{"angle bracket", "KEY<script>", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := IsValidSpaceKey(tt.key)
			if got != tt.want {
				t.Errorf("IsValidSpaceKey(%q) = %v, want %v", tt.key, got, tt.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// ValidateQueryConfig
// ---------------------------------------------------------------------------

func TestValidateQueryConfig(t *testing.T) {
	tests := []struct {
		name    string
		cfg     *models.QueryConfig
		wantErr bool
	}{
		{
			name:    "no keys valid",
			cfg:     &models.QueryConfig{},
			wantErr: false,
		},
		{
			name: "valid project keys",
			cfg: &models.QueryConfig{
				ProjectKeys: []string{"SR", "ISRE"},
			},
			wantErr: false,
		},
		{
			name: "invalid project key lowercase",
			cfg: &models.QueryConfig{
				ProjectKeys: []string{"isre"},
			},
			wantErr: true,
		},
		{
			name: "invalid project key too short",
			cfg: &models.QueryConfig{
				ProjectKeys: []string{"S"},
			},
			wantErr: true,
		},
		{
			name: "valid space keys",
			cfg: &models.QueryConfig{
				SpaceKeys: []string{"ENG", "INFRA"},
			},
			wantErr: false,
		},
		{
			name: "invalid space key empty",
			cfg: &models.QueryConfig{
				SpaceKeys: []string{""},
			},
			wantErr: true,
		},
		{
			name: "CQL injection via space key",
			cfg: &models.QueryConfig{
				SpaceKeys: []string{`KEY) OR text = "secret`},
			},
			wantErr: true,
		},
		{
			name: "valid space keys with hyphens and underscores",
			cfg: &models.QueryConfig{
				SpaceKeys: []string{"my-space", "SPACE_1"},
			},
			wantErr: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateQueryConfig(tt.cfg)
			if (err != nil) != tt.wantErr {
				t.Errorf("ValidateQueryConfig() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// escapeJQLValue
// ---------------------------------------------------------------------------

func TestEscapeJQLValue(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"no special chars", "Done", "Done"},
		{"embedded double quote", `Done" OR project = "SECRET`, `Done\" OR project = \"SECRET`},
		{"single quotes unchanged", "It's done", "It's done"},
		{"multiple double quotes", `a"b"c`, `a\"b\"c`},
		{"only double quote", `"`, `\"`},
		{"empty string", "", ""},
		{"backslash preserved", `back\slash`, `back\slash`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := escapeJQLValue(tt.input)
			if got != tt.want {
				t.Errorf("escapeJQLValue(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// BuildJQLFilters
// ---------------------------------------------------------------------------

func TestBuildJQLFilters(t *testing.T) {
	tests := []struct {
		name       string
		cfg        *models.QueryConfig
		wantEmpty  bool
		wantSubstr string
	}{
		{
			name:      "empty config returns empty string",
			cfg:       &models.QueryConfig{},
			wantEmpty: true,
		},
		{
			name:       "single status filter",
			cfg:        &models.QueryConfig{JiraStatus: []string{"Done"}},
			wantSubstr: `status in ("Done")`,
		},
		{
			name:       "multiple statuses",
			cfg:        &models.QueryConfig{JiraStatus: []string{"Done", "In Progress"}},
			wantSubstr: `status in ("Done", "In Progress")`,
		},
		{
			name:       "single type filter",
			cfg:        &models.QueryConfig{JiraType: []string{"Story"}},
			wantSubstr: `type in ("Story")`,
		},
		{
			name:       "single priority filter",
			cfg:        &models.QueryConfig{JiraPriority: []string{"High"}},
			wantSubstr: `priority in ("High")`,
		},
		{
			name: "all filters combined",
			cfg: &models.QueryConfig{
				JiraStatus:   []string{"Done"},
				JiraType:     []string{"Story"},
				JiraPriority: []string{"High"},
			},
			wantSubstr: " AND ",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := BuildJQLFilters(tt.cfg)
			if tt.wantEmpty {
				if got != "" {
					t.Errorf("BuildJQLFilters() = %q, want empty", got)
				}
				return
			}
			if !strings.Contains(got, tt.wantSubstr) {
				t.Errorf("BuildJQLFilters() = %q, want to contain %q", got, tt.wantSubstr)
			}
		})
	}
}

func TestBuildJQLFilters_Injection(t *testing.T) {
	tests := []struct {
		name           string
		cfg            *models.QueryConfig
		wantSubstr     string
		mustNotContain string
	}{
		{
			name:           "status injection escaped",
			cfg:            &models.QueryConfig{JiraStatus: []string{`Done" OR project = "SECRET`}},
			wantSubstr:     `"Done\" OR project = \"SECRET"`,
			mustNotContain: `"Done" OR project`,
		},
		{
			name:           "type injection escaped",
			cfg:            &models.QueryConfig{JiraType: []string{`Story" OR assignee = "admin`}},
			wantSubstr:     `"Story\" OR assignee = \"admin"`,
			mustNotContain: `"Story" OR assignee`,
		},
		{
			name:           "priority injection escaped",
			cfg:            &models.QueryConfig{JiraPriority: []string{`High" OR 1=1 --`}},
			wantSubstr:     `"High\" OR 1=1 --"`,
			mustNotContain: `"High" OR 1=1`,
		},
		{
			name:       "single quotes pass through safely",
			cfg:        &models.QueryConfig{JiraStatus: []string{"It's done"}},
			wantSubstr: `"It's done"`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := BuildJQLFilters(tt.cfg)
			if !strings.Contains(got, tt.wantSubstr) {
				t.Errorf("BuildJQLFilters() = %q, want to contain %q", got, tt.wantSubstr)
			}
			if tt.mustNotContain != "" && strings.Contains(got, tt.mustNotContain) {
				t.Errorf("BuildJQLFilters() = %q, must NOT contain %q (injection not escaped)", got, tt.mustNotContain)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// BuildCQLFilters
// ---------------------------------------------------------------------------

func TestBuildCQLFilters(t *testing.T) {
	tests := []struct {
		name      string
		cfg       *models.QueryConfig
		wantEmpty bool
		wantStr   string
	}{
		{
			name:      "empty type returns empty",
			cfg:       &models.QueryConfig{},
			wantEmpty: true,
		},
		{
			name:      "page type is no-op",
			cfg:       &models.QueryConfig{ConfluenceType: "page"},
			wantEmpty: true,
		},
		{
			name:    "blogpost adds filter",
			cfg:     &models.QueryConfig{ConfluenceType: "blogpost"},
			wantStr: `type = "blogpost"`,
		},
		{
			name:    "CQL injection escaped",
			cfg:     &models.QueryConfig{ConfluenceType: `blogpost" OR space = "SECRET`},
			wantStr: `type = "blogpost\" OR space = \"SECRET"`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := BuildCQLFilters(tt.cfg)
			if tt.wantEmpty {
				if got != "" {
					t.Errorf("BuildCQLFilters() = %q, want empty", got)
				}
				return
			}
			if !strings.Contains(got, tt.wantStr) {
				t.Errorf("BuildCQLFilters() = %q, want to contain %q", got, tt.wantStr)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// ValidateInputs
// ---------------------------------------------------------------------------

func TestValidateInputs(t *testing.T) {
	tests := []struct {
		name      string
		startDate string
		endDate   string
		timeZone  string
		wantErr   bool
	}{
		{
			name:      "valid inputs",
			startDate: "2025-01-01",
			endDate:   "2025-12-31",
			timeZone:  "America/Chicago",
			wantErr:   false,
		},
		{
			name:      "invalid timezone",
			startDate: "2025-01-01",
			endDate:   "2025-12-31",
			timeZone:  "Not/ATimezone",
			wantErr:   true,
		},
		{
			name:      "invalid start date",
			startDate: "not-a-date",
			endDate:   "2025-12-31",
			timeZone:  "UTC",
			wantErr:   true,
		},
		{
			name:      "invalid end date",
			startDate: "2025-01-01",
			endDate:   "not-a-date",
			timeZone:  "UTC",
			wantErr:   true,
		},
		{
			name:      "end before start",
			startDate: "2025-12-31",
			endDate:   "2025-01-01",
			timeZone:  "UTC",
			wantErr:   true,
		},
		{
			name:      "same day valid",
			startDate: "2025-06-01",
			endDate:   "2025-06-01",
			timeZone:  "UTC",
			wantErr:   false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateInputs(tt.startDate, tt.endDate, tt.timeZone)
			if (err != nil) != tt.wantErr {
				t.Errorf("ValidateInputs(%q, %q, %q) error = %v, wantErr %v",
					tt.startDate, tt.endDate, tt.timeZone, err, tt.wantErr)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// RedactEmail
// ---------------------------------------------------------------------------

func TestRedactEmail(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"user@example.com", "u***@example.com"},
		{"a@b.com", "a@b.com"},      // single-char local stays as-is
		{"ab@b.com", "a*@b.com"},    // two-char local
		{"", "[email]"},             // empty → sentinel
		{"notanemail", "[email]"},   // no @ → sentinel
		{"@example.com", "[email]"}, // empty local → sentinel
	}
	for _, tt := range tests {
		got := RedactEmail(tt.input)
		if got != tt.want {
			t.Errorf("RedactEmail(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

// ---------------------------------------------------------------------------
// RedactName
// ---------------------------------------------------------------------------

func TestRedactName(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"Brian Lopez", "[name]"},
		{"Alice", "[name]"},
		{"", ""},
	}
	for _, tt := range tests {
		got := RedactName(tt.input)
		if got != tt.want {
			t.Errorf("RedactName(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

// ---------------------------------------------------------------------------
// RedactOthersInIssues
// ---------------------------------------------------------------------------

func TestRedactOthersInIssues(t *testing.T) {
	self := "me@example.com"

	issues := []models.Issue{
		// Self — email matches: leave name and email untouched
		{Assignee: "Me Myself", AssigneeEmail: "me@example.com", AssigneeAccountID: "acc-001"},
		// Self — case-insensitive match
		{Assignee: "Me Myself", AssigneeEmail: "ME@EXAMPLE.COM", AssigneeAccountID: "acc-001"},
		// Other — email present: redact name and email
		{Assignee: "Alice Smith", AssigneeEmail: "alice@example.com", AssigneeAccountID: "acc-002"},
		// Other — empty email: cannot verify, redact both
		{Assignee: "Unknown Person", AssigneeEmail: "", AssigneeAccountID: "acc-003"},
		// Unassigned: no-op (empty fields stay empty)
		{Assignee: "", AssigneeEmail: "", AssigneeAccountID: ""},
	}

	got := RedactOthersInIssues(issues, self)

	// Self (exact match) — untouched
	if got[0].Assignee != "Me Myself" || got[0].AssigneeEmail != "me@example.com" {
		t.Errorf("[0] self exact: got Assignee=%q Email=%q, want unchanged", got[0].Assignee, got[0].AssigneeEmail)
	}
	if got[0].AssigneeAccountID != "acc-001" {
		t.Errorf("[0] AccountID must not be redacted, got %q", got[0].AssigneeAccountID)
	}

	// Self (case-insensitive) — untouched
	if got[1].Assignee != "Me Myself" || got[1].AssigneeEmail != "ME@EXAMPLE.COM" {
		t.Errorf("[1] self case-insensitive: got Assignee=%q Email=%q, want unchanged", got[1].Assignee, got[1].AssigneeEmail)
	}

	// Other — redacted
	if got[2].Assignee != "[name]" {
		t.Errorf("[2] other Assignee = %q, want [name]", got[2].Assignee)
	}
	if !strings.Contains(got[2].AssigneeEmail, "***") {
		t.Errorf("[2] other AssigneeEmail = %q, want masked", got[2].AssigneeEmail)
	}
	if got[2].AssigneeAccountID != "acc-002" {
		t.Errorf("[2] AccountID must not be redacted, got %q", got[2].AssigneeAccountID)
	}

	// Empty email — name redacted
	if got[3].Assignee != "[name]" {
		t.Errorf("[3] empty-email Assignee = %q, want [name]", got[3].Assignee)
	}

	// Fully empty — no-op
	if got[4].Assignee != "" || got[4].AssigneeEmail != "" {
		t.Errorf("[4] empty issue should remain empty, got Assignee=%q Email=%q", got[4].Assignee, got[4].AssigneeEmail)
	}
}

func TestRedactOthersInIssues_MultiSelf(t *testing.T) {
	// Two self emails (e.g. rc.Email + rc.AtlassianEmail may differ)
	got := RedactOthersInIssues(
		[]models.Issue{{Assignee: "Me", AssigneeEmail: "alt@example.com"}},
		"primary@example.com", "alt@example.com",
	)
	if got[0].Assignee != "Me" {
		t.Errorf("alt self should not be redacted, got %q", got[0].Assignee)
	}
}

func TestRedactOthersInIssues_OriginalUnchanged(t *testing.T) {
	// Verify the function does not mutate the input slice.
	orig := []models.Issue{{Assignee: "Alice", AssigneeEmail: "alice@example.com"}}
	_ = RedactOthersInIssues(orig, "me@example.com")
	if orig[0].Assignee != "Alice" {
		t.Errorf("original slice must not be mutated, got %q", orig[0].Assignee)
	}
}

// ---------------------------------------------------------------------------
// RedactOthersInArticles
// ---------------------------------------------------------------------------

func TestRedactOthersInArticles(t *testing.T) {
	self := "me@example.com"

	articles := []models.ConfluenceArticle{
		// Self creator — untouched; LastEditor always redacted
		{Creator: "Me Myself", CreatorEmail: "me@example.com", CreatorAccountID: "acc-me", LastEditor: "Bob Jones"},
		// Other creator — redacted; LastEditor always redacted
		{Creator: "Alice Smith", CreatorEmail: "alice@example.com", CreatorAccountID: "acc-alice", LastEditor: "Carol Lee"},
		// Creator — empty email → redact; no LastEditor → no-op
		{Creator: "Unknown", CreatorEmail: "", CreatorAccountID: "acc-u", LastEditor: ""},
	}

	got := RedactOthersInArticles(articles, self)

	// Self creator: name and email preserved
	if got[0].Creator != "Me Myself" || got[0].CreatorEmail != "me@example.com" {
		t.Errorf("[0] self creator: got Creator=%q Email=%q, want unchanged", got[0].Creator, got[0].CreatorEmail)
	}
	if got[0].CreatorAccountID != "acc-me" {
		t.Errorf("[0] CreatorAccountID must not be redacted, got %q", got[0].CreatorAccountID)
	}
	// LastEditor always redacted
	if got[0].LastEditor != "[name]" {
		t.Errorf("[0] LastEditor = %q, want [name]", got[0].LastEditor)
	}

	// Other creator: redacted
	if got[1].Creator != "[name]" {
		t.Errorf("[1] other Creator = %q, want [name]", got[1].Creator)
	}
	if !strings.Contains(got[1].CreatorEmail, "***") {
		t.Errorf("[1] other CreatorEmail = %q, want masked", got[1].CreatorEmail)
	}
	if got[1].CreatorAccountID != "acc-alice" {
		t.Errorf("[1] CreatorAccountID must not be redacted, got %q", got[1].CreatorAccountID)
	}
	if got[1].LastEditor != "[name]" {
		t.Errorf("[1] LastEditor = %q, want [name]", got[1].LastEditor)
	}

	// Empty email: creator redacted
	if got[2].Creator != "[name]" {
		t.Errorf("[2] empty-email Creator = %q, want [name]", got[2].Creator)
	}
	// Empty LastEditor: no-op
	if got[2].LastEditor != "" {
		t.Errorf("[2] empty LastEditor should remain empty, got %q", got[2].LastEditor)
	}
}

func TestRedactOthersInArticles_OriginalUnchanged(t *testing.T) {
	orig := []models.ConfluenceArticle{{Creator: "Alice", CreatorEmail: "alice@example.com"}}
	_ = RedactOthersInArticles(orig, "me@example.com")
	if orig[0].Creator != "Alice" {
		t.Errorf("original slice must not be mutated, got %q", orig[0].Creator)
	}
}
