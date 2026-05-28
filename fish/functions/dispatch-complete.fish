# dispatch-complete.fish — EPIC-030 M3 + EPIC-073 M5: Dispatch lifecycle completion
# Emits dispatch_complete event then removes the trigger file.
# EPIC-073 M5: Supports both JSON (legacy) and markdown+frontmatter (new substrate).
# For .md triggers: flips status → complete and sets completed_at before removal.
#
# Usage:
#   dispatch-complete <trigger-file-path>
#
# Example:
#   dispatch-complete ~/.config/fish/.claude-dispatch/EPIC-030_fish-config-agent_M2_M3.json
#   dispatch-complete ~/.claude/.claude-dispatch/epic-073-discovery.md
#
# Emits: dispatch_complete event via emit_jsonl with layer=claude_code
# Also removes: <trigger>.claimed sentinel if present

function dispatch-complete --description "Emit dispatch_complete and remove trigger file"
    if test (count $argv) -eq 0
        echo "dispatch-complete: trigger file path required" >&2
        return 1
    end

    set -l trigger $argv[1]

    if not test -f "$trigger"
        echo "dispatch-complete: trigger not found: $trigger" >&2
        return 1
    end

    set -l trigger_basename (basename "$trigger")
    set -l ext (string match -r '\.[^.]+$' "$trigger_basename")

    # Emit dispatch_complete before deletion
    functions -q emit_jsonl && emit_jsonl \
        --layer claude_code \
        --event-type dispatch_complete \
        --command "dispatch-complete: $trigger_basename" \
        --metadata-json "{\"trigger\":\"$trigger_basename\",\"cwd\":\"$PWD\",\"format\":\"$ext\"}" \
        2>/dev/null

    # Signal tmux orchestrator (sticky — survives until consumed by wait-for -w)
    if set -q TMUX
        set -l signal_name "dispatch:"(string replace -r '\.[^.]+$' '' -- "$trigger_basename")
        tmux wait-for -S "$signal_name" 2>/dev/null

        # EPIC-001 M1: notify orchestrator window
        set -l orch_wid (tmux show-option -gqv @orchestrator_window_id 2>/dev/null)
        if test -n "$orch_wid"
            tmux display-message -t "$orch_wid" -d 5000 "dispatch complete: $trigger_basename" 2>/dev/null
        end

        # EPIC-001 M2: rename completed window via sidecar mapping
        set -l trigger_stem (string replace -r '\.[^.]+$' '' -- "$trigger_basename")
        for sidecar in /tmp/dispatch-window-$USER-*.trigger
            test -f "$sidecar"; or continue
            set -l sidecar_content (cat "$sidecar" 2>/dev/null | string trim)
            if test "$sidecar_content" = "$trigger_stem"
                # Extract window_id from filename: /tmp/dispatch-window-USER-@N.trigger → @N
                set -l wid (string match -r '@\d+' -- (basename "$sidecar"))
                if test -n "$wid"
                    tmux rename-window -t "$wid" "✅ $trigger_stem" 2>/dev/null
                    # EPIC-001 M1: clear per-window @agent_waiting
                    functions -q tmux-stop-notify && tmux-stop-notify --window-id "$wid"
                end
                rm "$sidecar" 2>/dev/null
                break
            end
        end
    end

    # Remove claimed sentinel if present
    if test -f "$trigger.claimed"
        rm "$trigger.claimed" 2>/dev/null
    end

    # Read epic_path from trigger before deletion (format-aware)
    set -l epic_path ""
    if test "$ext" = ".json"
        # Legacy JSON format
        set epic_path (python3 -c '
import sys, json
d = json.load(open(sys.argv[1]))
print(d.get("epic_path", ""))
' "$trigger" 2>/dev/null)
    else if test "$ext" = ".md"
        # New markdown+frontmatter format
        # Flip status → complete and set completed_at before removal (audit trail in event bus)
        set -l now_utc (date -u +%Y%m%dT%H%M%SZ)
        set epic_path (python3 -c '
import sys, yaml

path, ts = sys.argv[1], sys.argv[2]
content = open(path).read()

# Parse frontmatter
parts = content.split("---", 2)
if len(parts) < 3:
    print("")
    sys.exit(0)

try:
    fm = yaml.safe_load(parts[1]) or {}
except:
    print("")
    sys.exit(0)

# Update status and completed_at in frontmatter
fm["status"] = "complete"
fm["completed_at"] = ts

# Reconstruct file with updated frontmatter
new_fm = yaml.dump(fm, default_flow_style=False, sort_keys=False).rstrip()
new_content = "---\n" + new_fm + "\n---" + parts[2]
open(path, "w").write(new_content)

print(fm.get("epic_path", ""))
' "$trigger" "$now_utc" 2>/dev/null)
    end

    # Remove the trigger file
    rm "$trigger"

    # Promote epic Discovery → Ready if this was the last trigger (EPIC-072 M3)
    # EPIC-073 M5: check both .json and .md triggers across agent CWDs
    if test -n "$epic_path" -a -f "$epic_path"
        python3 -c '
import sys, json, yaml, os, glob as g

epic_path = sys.argv[1]

# Parse frontmatter for status and agents
lines = open(epic_path).readlines()
in_front = False
status = ""
agents = []
current_agent = {}
for line in lines:
    stripped = line.strip()
    if stripped == "---":
        if in_front:
            break
        in_front = True
        continue
    if not in_front:
        continue
    if stripped.startswith("status:"):
        status = stripped.split(":", 1)[1].strip()
    if stripped.startswith("- id:"):
        if current_agent:
            agents.append(current_agent)
        current_agent = {"id": stripped.split(":", 1)[1].strip(), "cwd": ""}
    elif stripped.startswith("cwd:") and current_agent:
        current_agent["cwd"] = stripped.split(":", 1)[1].strip()
if current_agent:
    agents.append(current_agent)

# Only promote Discovery → Ready
if status != "Discovery":
    sys.exit(0)

# Check if any triggers remain for this epic across all agent CWDs
epic_basename = os.path.basename(epic_path)
epic_stem = os.path.splitext(epic_basename)[0]
for agent in agents:
    cwd = os.path.expanduser(agent["cwd"])
    dispatch_dir = os.path.join(cwd, ".claude-dispatch")
    if not os.path.isdir(dispatch_dir):
        continue
    # Check both JSON (legacy) and MD (new substrate) triggers
    for pattern in ["*.json", "*.md"]:
        for tf in g.glob(os.path.join(dispatch_dir, pattern)):
            # Skip sentinel files
            if tf.endswith(".claimed"):
                continue
            try:
                if tf.endswith(".json"):
                    d = json.load(open(tf))
                else:
                    content = open(tf).read()
                    parts = content.split("---", 2)
                    d = yaml.safe_load(parts[1]) if len(parts) >= 3 else {}
                    d = d or {}
                ep = d.get("epic_path", "")
                task = d.get("task", "")
                if ep == epic_path or epic_stem in task or epic_stem in os.path.basename(tf):
                    sys.exit(0)
            except Exception:
                continue

# No triggers remain — promote Discovery → Ready
content = open(epic_path).read()
content = content.replace("status: Discovery", "status: Ready")
open(epic_path, "w").write(content)
print("promoted")
' "$epic_path" 2>/dev/null
        if test $status -eq 0
            if python3 -c "print('Ready' if 'status: Ready' in open(sys.argv[1]).read() else '')" "$epic_path" 2>/dev/null | string match -q 'Ready'
                echo "📋 Epic promoted: Discovery → Ready"
            end
        end
    end
end
