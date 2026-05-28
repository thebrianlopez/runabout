# dispatch-check-stalled.fish  -  EPIC-001 M5: Detect stuck dispatches
# Scans all agent CWDs for claimed triggers older than TTL.
# Emits dispatch_stalled event to JSONL bus for each stalled trigger.
#
# Usage:
#   dispatch-check-stalled [--ttl SECONDS]
#
# Default TTL: 600 (10 minutes)

function dispatch-check-stalled --description "Detect and report stalled dispatches"
    argparse 'ttl=' -- $argv
    or return 1

    set -l ttl_seconds 600
    if set -q _flag_ttl
        set ttl_seconds $_flag_ttl
    end

    # Resolve agent CWDs from org registry
    set -l _org_base (set -q CHAIN_ORG; and echo $CHAIN_ORG; or echo "$HOME/code/personal")
    set -l org_yaml "$_org_base/docs/org.yaml"
    set -l agent_cwds

    if test -f "$org_yaml"
        for list_key in agents workspace_agents
            set -l yq_expr '.'$list_key'[].cwd'
            for cwd in (yq "$yq_expr" "$org_yaml" 2>/dev/null)
                test -n "$cwd" -a "$cwd" != "null"; or continue
                set -a agent_cwds (string replace '~' $HOME -- $cwd)
            end
        end
    else
        # Fallback: common agent locations
        set agent_cwds ~/.claude ~/.config/fish ~/.config ~/.automation-metrics
    end

    set -l stalled_count 0

    for agent_cwd in $agent_cwds
        set -l dispatch_dir $agent_cwd/.claude-dispatch
        test -d "$dispatch_dir"; or continue

        for trigger in $dispatch_dir/*.json $dispatch_dir/*.md
            test -f "$trigger"; or continue
            # Skip sentinel files
            string match -q '*.claimed' "$trigger"; and continue

            # Determine if this trigger is claimed and when
            set -l is_claimed false
            set -l claimed_ts ""

            # Check .claimed sentinel file
            if test -f "$trigger.claimed"
                set is_claimed true
            end

            # For .md triggers, check frontmatter claimed_at
            if string match -q '*.md' "$trigger"
                set -l fm_claimed (python3 -c '
import sys, yaml
path = sys.argv[1]
content = open(path).read()
parts = content.split("---", 2)
if len(parts) >= 3:
    fm = yaml.safe_load(parts[1]) or {}
    status = fm.get("status", "")
    claimed_at = fm.get("claimed_at")
    if status == "claimed" or claimed_at not in (None, "null", ""):
        print(claimed_at if claimed_at not in (None, "null", "") else "USE_MTIME")
    else:
        print("")
else:
    print("")
' "$trigger" 2>/dev/null)

                if test -n "$fm_claimed"
                    set is_claimed true
                    if test "$fm_claimed" != "USE_MTIME"
                        set claimed_ts "$fm_claimed"
                    end
                end
            end

            # Not claimed → not stalled (still pending)
            $is_claimed; or continue

            # Calculate elapsed time
            set -l elapsed (python3 -c '
import sys, os
from datetime import datetime, timezone

trigger = sys.argv[1]
claimed_ts = sys.argv[2] if len(sys.argv) > 2 and sys.argv[2] else ""

if claimed_ts and claimed_ts != "USE_MTIME":
    try:
        ts = datetime.strptime(claimed_ts, "%Y%m%dT%H%M%SZ").replace(tzinfo=timezone.utc)
    except ValueError:
        ts = datetime.fromtimestamp(os.path.getmtime(trigger + ".claimed"), tz=timezone.utc) if os.path.exists(trigger + ".claimed") else datetime.fromtimestamp(os.path.getmtime(trigger), tz=timezone.utc)
elif os.path.exists(trigger + ".claimed"):
    ts = datetime.fromtimestamp(os.path.getmtime(trigger + ".claimed"), tz=timezone.utc)
else:
    ts = datetime.fromtimestamp(os.path.getmtime(trigger), tz=timezone.utc)

elapsed = (datetime.now(timezone.utc) - ts).total_seconds()
print(int(elapsed))
' "$trigger" "$claimed_ts" 2>/dev/null)

            test -n "$elapsed"; or continue

            if test "$elapsed" -ge "$ttl_seconds"
                set stalled_count (math $stalled_count + 1)
                set -l trigger_basename (basename "$trigger")
                set -l agent_id (basename "$agent_cwd")"-agent"

                # Emit dispatch_stalled event
                functions -q emit_jsonl && emit_jsonl \
                    --layer claude_code \
                    --event-type dispatch_stalled \
                    --command "dispatch-check-stalled: $trigger_basename" \
                    --metadata-json "{\"trigger\":\"$trigger_basename\",\"agent\":\"$agent_id\",\"claimed_at\":\"$claimed_ts\",\"elapsed_seconds\":$elapsed,\"ttl_seconds\":$ttl_seconds}" \
                    2>/dev/null

                echo "⚠️  Stalled: $trigger_basename ($agent_id)  -  "(math $elapsed / 60)"min elapsed (TTL: "(math $ttl_seconds / 60)"min)"
            end
        end
    end

    if test $stalled_count -eq 0
        return 0
    end

    echo ""
    echo "$stalled_count stalled dispatch(es) detected."
    return 1
end
