# EPIC-088 F3: Resolve agent CWD from org.yaml + workspace scan.
# Resolution order: workspace scan → static cwd → idle/miss
# Returns: CWD on stdout (exit 0), empty string for idle (exit 0), exit 1 on miss.
# Test overrides: _REGISTRY_ORG_YAML (path), _REGISTRY_WS_DIR (workspace base dir)
function _registry_resolve_cwd --argument-names agent_id
    test -z "$agent_id"; and return 1

    set -l _org_base (set -q CHAIN_ORG; and echo $CHAIN_ORG; or echo "$HOME/code/personal")

    set -q _REGISTRY_ORG_YAML
        and set -l org_yaml $_REGISTRY_ORG_YAML
        or set -l org_yaml "$_org_base/docs/org.yaml"
    test -f "$org_yaml"; or return 1

    # Check agent exists in registry; capture its static cwd (may be literal "null")
    set -l raw_cwd (yq ".agents[] | select(.id == \"$agent_id\") | .cwd" "$org_yaml" 2>/dev/null)
    test -z "$raw_cwd"; and return 1

    # Workspace scan takes priority over static CWD (F3 CT-5)
    set -q _REGISTRY_WS_DIR
        and set -l ws_dir $_REGISTRY_WS_DIR
        or set -l ws_dir "$_org_base/jira_work"

    set -l best_match ""
    set -l best_mtime 0
    set -l match_count 0

    for ws_file in $ws_dir/*/workspace.yaml
        test -f "$ws_file"; or continue

        # Skip closed/archived workspaces (malformed yq → empty → skip)
        set -l ws_status (yq '.status // ""' "$ws_file" 2>/dev/null)
        if test "$ws_status" = "closed" -o "$ws_status" = "archived"
            continue
        end

        # Find matching repo; silently skip malformed files (yq exits non-zero)
        set -l repo_path (yq ".repos[] | select(.agent == \"$agent_id\") | .path" "$ws_file" 2>/dev/null)
        if test -z "$repo_path" -o "$repo_path" = "null"
            continue
        end

        # Resolve relative paths against the workspace directory
        if not string match -q '/*' "$repo_path"
            set repo_path (dirname "$ws_file")/$repo_path
        end

        set match_count (math $match_count + 1)
        set -l mtime (command stat -f '%m' "$ws_file" 2>/dev/null)
        test -n "$mtime"; or set mtime 0
        if test $mtime -gt $best_mtime
            set best_mtime $mtime
            set best_match $repo_path
        end
    end

    if test $match_count -gt 1
        # W102: multiple matches  -  most recent already selected above
        echo "W102: agent '$agent_id' in $match_count workspaces  -  using most recent" >&2
    end

    if test -n "$best_match"
        echo $best_match
        return 0
    end

    # Static CWD fallback (non-null cwd in org.yaml)
    if test "$raw_cwd" != "null"
        echo $raw_cwd | string replace '~' $HOME
        return 0
    end

    # cwd=null + no active workspace → idle (empty stdout, exit 0)
    return 0
end
