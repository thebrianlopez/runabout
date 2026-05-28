function epic-status --description 'Show milestone status for an epic, or list all pending dispatch triggers'
    set -l epic_file $argv[1]
    set -l _org_base (set -q CHAIN_ORG; and echo $CHAIN_ORG; or echo "$HOME/code/personal")

    if test -z "$epic_file"
        # No arg: list all pending dispatch triggers  -  read agent CWDs from registry
        set -l found 0
        set -l org_yaml "$_org_base/docs/org.yaml"

        # Build dispatch dirs from registry
        set -l dispatch_dirs
        if test -f "$org_yaml"
            for agent_cwd in (yq '.agents[].cwd' "$org_yaml" 2>/dev/null)
                set -l expanded (string replace '~' $HOME -- $agent_cwd)
                set -a dispatch_dirs $expanded.claude-dispatch
            end
        end

        for dispatch_dir in $dispatch_dirs
            # Dual-glob: both .json (legacy) and .md (new substrate)
            for f in $dispatch_dir/*.json $dispatch_dir/*.md
                test -f $f; or continue
                # Skip sentinel files
                string match -q '*.claimed' $f; and continue
                set found (math $found + 1)
                set -l info (_epic_status_parse_trigger $f)
                set -l parts (string split '|' $info)
                echo "  -> [$parts[1]] $parts[2]  milestones: $parts[3]"
            end
        end

        if test $found -eq 0
            echo "No pending dispatch triggers."
        else
            echo ""
            echo "$found pending trigger(s)"
        end
        return 0
    end

    # With arg: show milestone table from the epic file
    if not string match -rq '^/' $epic_file
        set epic_file $_org_base/docs/epics/$epic_file
    end

    if not test -f $epic_file
        echo "Epic not found: $epic_file"
        return 1
    end

    echo ""
    echo "$(basename $epic_file)"
    echo ""
    md-tree extract $epic_file "Milestones"

    # Cross-reference dispatch triggers from agents: frontmatter
    set -l dispatch_script '
import sys, json, yaml, os, glob as g
from datetime import datetime

epic_path = sys.argv[1]
epic_basename = os.path.basename(epic_path)
epic_stem = os.path.splitext(epic_basename)[0]

lines = open(epic_path).readlines()
in_front = False
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
    if stripped.startswith("- id:"):
        if current_agent:
            agents.append(current_agent)
        current_agent = {"id": stripped.split(":", 1)[1].strip(), "cwd": ""}
    elif stripped.startswith("cwd:") and current_agent:
        current_agent["cwd"] = stripped.split(":", 1)[1].strip()
if current_agent:
    agents.append(current_agent)

if not agents:
    sys.exit(0)

print("  dispatch-status:")
for agent in agents:
    aid = agent["id"]
    cwd = os.path.expanduser(agent["cwd"])
    dispatch_dir = os.path.join(cwd, ".claude-dispatch")
    found_trigger = False
    if os.path.isdir(dispatch_dir):
        for pattern in ["*.json", "*.md"]:
            for tf in g.glob(os.path.join(dispatch_dir, pattern)):
                if tf.endswith(".claimed"):
                    continue
                try:
                    d = {}
                    if tf.endswith(".json"):
                        d = json.load(open(tf))
                    else:
                        content = open(tf).read()
                        parts = content.split("---", 2)
                        if len(parts) >= 3:
                            d = yaml.safe_load(parts[1]) or {}
                    ep = d.get("epic_path", "")
                    task = d.get("task", "")
                    if ep == epic_path or epic_stem in task or epic_stem in os.path.basename(tf):
                        found_trigger = True
                        claimed = os.path.exists(tf + ".claimed")
                        status_val = d.get("status", "")
                        age_str = ""
                        dispatched = d.get("dispatched_at", "")
                        label = "[" + (status_val or ("claimed" if claimed else "dispatched")) + "]"
                        if dispatched:
                            try:
                                dt = datetime.strptime(str(dispatched), "%Y%m%dT%H%M%SZ")
                                delta = datetime.utcnow() - dt
                                mins = int(delta.total_seconds() / 60)
                                if mins < 60:
                                    age_str = " (" + str(mins) + "m ago)"
                                elif mins < 1440:
                                    age_str = " (" + str(mins // 60) + "h ago)"
                                else:
                                    age_str = " (" + str(mins // 1440) + "d ago)"
                            except Exception:
                                pass
                        print("    " + aid + ": " + label + age_str)
                        break
                except Exception:
                    continue
            if found_trigger:
                break
    if not found_trigger:
        print("    " + aid + ": no trigger (complete or not dispatched)")
'
    set -l dispatch_info (python3 -c $dispatch_script $epic_file 2>/dev/null)

    if test -n "$dispatch_info"
        echo ""
        printf '%s\n' $dispatch_info
    end

    echo ""
end

function _epic_status_parse_trigger -d "Parse a trigger file (JSON or MD) and return agent|label|milestones"
    set -l f $argv[1]
    if string match -q '*.json' $f
        python3 -c '
import sys, json
d = json.load(open(sys.argv[1]))
agent = d.get("agent", d.get("agent_id", "?"))
label = d.get("task", d.get("epic", d.get("epic_path", "?")))
ms = d.get("milestones", [])
ms_str = ",".join(ms) if isinstance(ms, list) else str(ms)
print(agent + "|" + str(label) + "|" + ms_str)
' "$f" 2>/dev/null
    else
        python3 -c '
import sys, yaml
content = open(sys.argv[1]).read()
parts = content.split("---", 2)
if len(parts) < 3:
    print("?|?|")
    sys.exit(0)
fm = yaml.safe_load(parts[1]) or {}
agent = fm.get("agent", "?")
label = fm.get("task", "?")
ms = fm.get("milestones", [])
ms_str = ",".join(str(m) for m in ms) if isinstance(ms, list) else str(ms)
print(agent + "|" + str(label) + "|" + ms_str)
' "$f" 2>/dev/null
    end
end
