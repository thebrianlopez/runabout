function epic-inject --description 'Create a new multi-agent epic in docs/epics/'
    argparse 'title=' 'component=' 'org=' 'status=' -- $argv
    or return 1

    if test -z "$_flag_title" -o -z "$_flag_component" -o -z "$_flag_org"
        echo "Usage: epic-inject --org DEVTOOLS --component ClaudeCode --title 'my epic title'"
        echo ""
        echo "  --org         Uppercase org slug (DEVTOOLS, GRINDR, etc.)"
        echo "  --component   PascalCase component name"
        echo "  --title       Human-readable title (spaces OK)"
        echo "  --status      Initial status (default: Draft). Valid: Draft, Discovery, Ready, In Progress, Complete"
        return 1
    end

    # Default status to Draft per Epic Status Lifecycle (EPIC-072)
    set -l epic_status "Draft"
    if test -n "$_flag_status"
        set epic_status $_flag_status
    end

    set -l timestamp (date -u +%Y%m%dT%H%M%SZ)
    set -l org (string upper -- $_flag_org)
    set -l component $_flag_component
    set -l title_raw $_flag_title
    # Sanitize title for filename: spaces→underscores, strip non-ASCII and punctuation that break shell/git
    set -l title_slug (string replace --all ' ' '_' -- $title_raw)
    set title_slug (echo $title_slug | LC_ALL=C sed 's/[^A-Za-z0-9_.-]//g')
    # Collapse repeated underscores left by stripped chars (e.g. "winit_—_Dynamic" → "winit_Dynamic")
    set title_slug (string replace --all --regex '_+' '_' -- $title_slug)
    set title_slug (string trim --chars='_' -- $title_slug)
    set -l _org_base (set -q CHAIN_ORG; and echo $CHAIN_ORG; or echo "$HOME/code/personal")
    set -l docs_epics $_org_base/docs/epics

    if not test -d $docs_epics
        echo "Error: epics dir not found: $docs_epics"
        return 1
    end

    # Find highest existing EPIC number for this component via fish glob
    set -l last_num 0
    for f in $docs_epics/*$component*EPIC-*.md
        test -f $f; or continue
        set -l m (string match -r 'EPIC-(\d+)' $f)
        if set -q m[2]; and test $m[2] -gt $last_num 2>/dev/null
            set last_num $m[2]
        end
    end

    set -l next_num (math $last_num + 1)
    set -l epic_num (printf 'EPIC-%03d' $next_num)
    set -l filename "$org"_"$timestamp"_"$component"_"$epic_num"_"$title_slug".md
    set -l filepath $docs_epics/$filename

    begin
        echo "---"
        echo "template_id: epic"
        echo 'version: "1.0"'
        echo "last_updated: \"$timestamp\""
        echo "output_dir: docs/epics/"
        echo "type: epic"
        echo "# Valid status values: Draft | Discovery | Ready | In Progress | Complete"
        echo "status: $epic_status"
        echo "agents:"
        set -l org_yaml $_org_base/docs/org.yaml
        if test -f $org_yaml
            for agent_line in (yq -r '.agents[] | "  - id: " + .id + "\n    cwd: " + .cwd + "\n    milestones: []"' $org_yaml 2>/dev/null)
                echo $agent_line
            end
        else
            echo "  # WARNING: $org_yaml not found — add agents manually"
        end
        echo "---"
        echo ""
        echo "# EPIC: $title_raw"
        echo ""
        echo "## Status and Metadata"
        echo ""
        echo "| Field | Value |"
        echo "|-------|-------|"
        echo "| **Org** | $org |"
        echo "| **Component** | $component |"
        echo "| **Created** | \`$timestamp\` |"
        echo "| **Owner** | \`brian\` |"
        echo "| **Status** | $epic_status |"
        echo ""
        echo "---"
        echo ""
        echo "## Summary"
        echo ""
        echo "<!-- What is this epic about and why does it matter? -->"
        echo ""
        echo "---"
        echo ""
        echo "## Goals"
        echo ""
        echo "- <!-- measurable outcome -->"
        echo ""
        echo "---"
        echo ""
        echo "## Non-Goals"
        echo ""
        echo "- <!-- explicitly out of scope -->"
        echo ""
        echo "---"
        echo ""
        echo "## Acceptance Criteria"
        echo ""
        echo "- [ ] <!-- clear done marker -->"
        echo ""
        echo "---"
        echo ""
        echo "## Milestones"
        echo ""
        echo "| Milestone | Description | Agent | ETA | Status |"
        echo "|-----------|-------------|-------|-----|--------|"
        echo "| M1 | TBD | TBD | TBD | Pending |"
        echo ""
        echo "### M1 Deliverables"
        echo "- [ ] TBD"
        echo ""
        echo "---"
        echo ""
        echo "## Notes"
        echo ""
        echo "- Created via \`epic-inject\` at $timestamp"
        echo "- Edit agents[] milestones in frontmatter and milestone table"
        echo "- Then run: \`epic-dispatch $filename\`"
    end > $filepath

    echo ""
    echo "✅ Epic created: $filepath"
    echo ""
    echo "Next steps:"
    echo "  1. Edit the epic — fill Summary, Goals, assign milestones to agents"
    echo "  2. Run: epic-dispatch $filename"
    echo ""
end
