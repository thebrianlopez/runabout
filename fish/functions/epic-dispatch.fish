function epic-dispatch --description 'Write dispatch triggers for assigned agents in an epic'
    # Parse flags
    argparse 'yolo' 'dry-run' 'discovery' 'model=' 'all-ready' -- $argv
    or return 1

    set -l yolo_mode (test -n "$_flag_yolo"; and echo 1; or echo 0)
    set -l dry_run (test -n "$_flag_dry_run"; and echo 1; or echo 0)
    set -l discovery_mode (test -n "$_flag_discovery"; and echo 1; or echo 0)
    set -l all_ready (test -n "$_flag_all_ready"; and echo 1; or echo 0)
    set -l model_value (test -n "$_flag_model"; and echo $_flag_model; or echo sonnet)
    set -l epic_file $argv[1]
    set -l _org_base (set -q CHAIN_ORG; and echo $CHAIN_ORG; or echo "$HOME/code/personal")

    if test -z "$epic_file" -a $all_ready -eq 0
        echo "Usage: epic-dispatch [--yolo] [--dry-run] [--discovery] [--model <alias>] <epic-filename.md>"
        echo "       epic-dispatch --all-ready [--dry-run] [--model <alias>]"
        echo "  --yolo       Auto-launch agents in tmux windows after dispatching"
        echo "  --dry-run    Preview dispatch plan without writing triggers or launching"
        echo "  --discovery  Write Type 2 discovery dispatches (agents investigate, write back to epic)"
        echo "  --all-ready  Dispatch all epics with status: Draft in docs/epics/"
        echo "  --model      Model alias for launched agents (default: sonnet)"
        echo "Example: epic-dispatch --yolo DEVTOOLS_20260318T192042Z_ClaudeCode_EPIC-006_Foo.md"
        echo "Example: epic-dispatch --all-ready --dry-run"
        return 1
    end

    # --all-ready: scan docs/epics/ for Draft epics and dispatch each
    if test $all_ready -eq 1
        set -l epics_dir $_org_base/docs/epics
        set -l org_yaml $_org_base/docs/org.yaml

        if not test -d $epics_dir
            echo "No epics directory: $epics_dir"
            return 1
        end

        # Build dispatch plan: scan Draft epics, resolve CWDs from org.yaml registry
        set -l dispatch_plan (echo '
import sys, os, yaml, glob
epics_dir, org_yaml = sys.argv[1], sys.argv[2]
try:
    with open(org_yaml) as f:
        org = yaml.safe_load(f)
    registry = {a["id"]: (a.get("cwd") or "") for a in (org or {}).get("agents", [])}
except Exception:
    registry = {}
for epic_path in sorted(glob.glob(os.path.join(epics_dir, "*.md"))):
    try:
        with open(epic_path) as f:
            content = f.read()
        parts = content.split("---", 2)
        if len(parts) < 3:
            continue
        fm = yaml.safe_load(parts[1])
        if not fm or fm.get("status") != "Draft":
            continue
        agents = fm.get("agents")
        if not agents:
            print("SKIP_NO_AGENTS:" + epic_path)
            continue
        for a in agents:
            aid = a.get("id", "")
            ms = a.get("milestones", [])
            if not ms:
                continue
            if aid not in registry:
                print("SKIP_UNKNOWN:" + epic_path + ":" + aid)
                continue
            cwd = registry[aid]
            if not cwd:
                print("SKIP_NULL_CWD:" + epic_path + ":" + aid)
                continue
            print("DISPATCH:" + epic_path + ":" + aid + ":" + cwd + ":" + ",".join(str(m) for m in ms))
    except Exception:
        continue
' | python3 - $epics_dir $org_yaml)

        if test -z "$dispatch_plan"
            echo "No epics with status: Draft found. Nothing to dispatch."
            return 0
        end

        set -l found_draft 0
        for line in $dispatch_plan
            if string match -rq '^(DISPATCH|SKIP_)' $line
                set found_draft 1
                break
            end
        end
        if test $found_draft -eq 0
            echo "No epics with status: Draft found. Nothing to dispatch."
            return 0
        end

        if test $dry_run -eq 1
            echo ""
            echo "Dispatching Draft epics... (dry run)"
        else
            echo ""
            echo "Dispatching Draft epics..."
        end
        echo ""

        set -l total_dispatched 0
        set -l total_skipped 0

        for line in $dispatch_plan
            if string match -rq '^SKIP_NO_AGENTS:' $line
                set -l epic_name (basename (string replace 'SKIP_NO_AGENTS:' '' $line))
                echo "  ⚠️  $epic_name - no agents: block - skipped" >&2
                continue
            end
            if string match -rq '^SKIP_UNKNOWN:' $line
                set -l sp (string split ':' (string replace 'SKIP_UNKNOWN:' '' $line))
                echo "  ⚠️  "(basename $sp[1])": agent '$sp[2]' not in registry - skipped" >&2
                continue
            end
            if string match -rq '^SKIP_NULL_CWD:' $line
                set -l sp (string split ':' (string replace 'SKIP_NULL_CWD:' '' $line))
                echo "  ⚠️  "(basename $sp[1])": agent '$sp[2]' has no CWD - skipped" >&2
                continue
            end
            if not string match -rq '^DISPATCH:' $line
                continue
            end

            # DISPATCH:<epic_path>:<agent_id>:<cwd>:<milestones>
            set -l sp (string split ':' (string replace 'DISPATCH:' '' $line))
            set -l d_epic_path $sp[1]
            set -l d_agent_id $sp[2]
            set -l d_agent_cwd (string replace '~' $HOME -- $sp[3] | string trim --right --chars='/')
            set -l d_milestones $sp[4]
            set -l d_epic_name (basename $d_epic_path)
            set -l d_basename_noext (string replace -r '\.md$' '' $d_epic_name)
            set -l d_trigger_dir $d_agent_cwd/.claude-dispatch
            set -l d_trigger_file $d_trigger_dir/$d_basename_noext.md

            if test $dry_run -eq 1
                echo "  -> $d_epic_name  ->  $d_agent_id  ($d_trigger_dir/)"
                continue
            end

            # Idempotency: skip if a pending trigger already exists
            if test -f $d_trigger_file
                set -l existing_status (python3 -c "
import sys, yaml
with open(sys.argv[1]) as f:
    c = f.read()
parts = c.split('---', 2)
if len(parts) >= 3:
    try:
        print(yaml.safe_load(parts[1]).get('status', ''))
    except: pass
" $d_trigger_file 2>/dev/null)
                if test "$existing_status" = pending
                    echo "  -> $d_epic_name  ->  $d_agent_id  (already pending - skipped)"
                    set total_skipped (math $total_skipped + 1)
                    continue
                end
            end

            if not mkdir -p $d_trigger_dir
                echo "  ⛔ Failed to dispatch $d_epic_name to $d_agent_id: mkdir $d_trigger_dir failed" >&2
                return 1
            end

            set -l ts (date -u +%Y%m%dT%H%M%SZ)
            printf '%s\n' \
                '---' \
                "schema_version: 1" \
                "task: $d_basename_noext" \
                "agent: $d_agent_id" \
                "epic_path: $d_epic_path" \
                "dispatched_at: $ts" \
                "status: pending" \
                "claimed_at: null" \
                "completed_at: null" \
                "milestones: [$d_milestones]" \
                "capabilities: []" \
                "producer: epic-dispatch --all-ready" \
                "model: $model_value" \
                '---' \
                '' \
                "# Task: $d_epic_name" \
                '' \
                "Execute milestones [$d_milestones] for this epic." \
                '' \
                '## Response' \
                '' \
                > $d_trigger_file
            or begin
                echo "  ⛔ Failed to write $d_trigger_file" >&2
                return 1
            end

            echo "  -> $d_epic_name  ->  $d_agent_id  ✅"
            set total_dispatched (math $total_dispatched + 1)
        end

        echo ""
        if test $dry_run -eq 1
            echo "Dry run complete. Would dispatch Draft epics."
        else
            echo "Done. $total_dispatched dispatch(es) written, $total_skipped skipped (already pending)."
        end
        echo ""
        return 0
    end

    # Accept bare filename or full path
    if not string match -rq '^/' $epic_file
        set epic_file $_org_base/docs/epics/$epic_file
    end

    if not test -f $epic_file
        echo "Epic not found: $epic_file"
        return 1
    end

    echo ""
    if test $dry_run -eq 1
        echo "🔍 DRY RUN  -  Dispatching: "(basename $epic_file)
    else
        echo "📡 Dispatching: "(basename $epic_file)
    end
    echo ""

    # Parse agents with non-empty milestones from YAML frontmatter
    set -l agents (echo '
import sys, yaml
epic_path = sys.argv[1]
with open(epic_path) as f:
    content = f.read()
parts = content.split("---", 2)
if len(parts) < 3:
    sys.exit(1)
fm = yaml.safe_load(parts[1])
agents = fm.get("agents", [])
if isinstance(agents, dict):
    print("ERROR:agents: must be a list of objects (- id: ... cwd: ... milestones: [...]), not a mapping", file=sys.stderr)
    sys.exit(2)
if not isinstance(agents, list):
    print("ERROR:agents: must be a list, got " + type(agents).__name__, file=sys.stderr)
    sys.exit(2)
for a in agents:
    ms = a.get("milestones", [])
    if ms:
        print(a.get("id", "") + "|" + a.get("cwd", "") + "|" + ",".join(str(m) for m in ms))
' | python3 - $epic_file)

    set -l parse_status $status
    if test $parse_status -eq 2
        echo "❌ Invalid agents: schema in epic frontmatter."
        echo "   agents: must be a list of objects, not a mapping."
        echo "   Correct:  agents:"
        echo "               - id: fish-config-agent"
        echo "                 cwd: ~/.config/fish/"
        echo "                 milestones: [M1, M3]"
        return 1
    else if test $parse_status -ne 0
        echo "❌ Failed to parse epic frontmatter (requires python3 + pyyaml)"
        return 1
    end

    if test -z "$agents"
        echo "⚠️  No agents with assigned milestones found."
        echo "    Edit agents[] frontmatter to assign milestones, then re-run."
        return 1
    end

    set -l dispatched 0
    for line in $agents
        set -l parts (string split '|' $line)
        set -l agent_id $parts[1]
        set -l agent_cwd (string replace '~' $HOME -- $parts[2])
        set -l milestones $parts[3]

        if not test -d $agent_cwd
            echo "  ⚠️  $agent_id  -  CWD not found: $agent_cwd (skipping)"
            continue
        end

        set -l trigger_dir $agent_cwd/.claude-dispatch
        set -l basename_noext (string replace -r '\.md$' '' (basename $epic_file))
        # EPIC-073 M4: emit markdown+frontmatter instead of JSON
        set -l trigger_file $trigger_dir/$basename_noext.md
        set -l ts (date -u +%Y%m%dT%H%M%SZ)

        if test $dry_run -eq 1
            echo "  ○ $agent_id  →  [$milestones]"
            echo "    would write: $trigger_file"
        else
            mkdir -p $trigger_dir

            if test $discovery_mode -eq 1
                # Type 2 discovery dispatch  -  agents investigate and write back to epic
                set -l epic_basename_noext (string replace -r '\.md$' '' (basename $epic_file))
                set -l task_id (string lower (string replace -r '^.*EPIC-' 'epic-' $epic_basename_noext | string replace -r '_.*' ''))-discovery

                printf '%s\n' \
                    '---' \
                    "schema_version: 1" \
                    "task: $task_id" \
                    "agent: $agent_id" \
                    "epic_path: $epic_file" \
                    "dispatched_at: $ts" \
                    "status: pending" \
                    "claimed_at: null" \
                    "completed_at: null" \
                    "milestones: []" \
                    "capabilities: []" \
                    "producer: epic-dispatch" \
                    "model: $model_value" \
                    '---' \
                    '' \
                    "# Task: Discovery for $epic_basename_noext" \
                    '' \
                    "Investigate feasibility of [$milestones]. Append findings to $epic_file under ## Notes → ### Discovery Report: $agent_id ($milestones)." \
                    '' \
                    '## Response' \
                    '' \
                    > $trigger_file
            else
                # Type 1 milestone dispatch  -  EPIC-073 M4 frontmatter schema
                printf '%s\n' \
                    '---' \
                    "schema_version: 1" \
                    "task: $basename_noext" \
                    "agent: $agent_id" \
                    "epic_path: $epic_file" \
                    "dispatched_at: $ts" \
                    "status: pending" \
                    "claimed_at: null" \
                    "completed_at: null" \
                    "milestones: [$milestones]" \
                    "capabilities: []" \
                    "producer: epic-dispatch" \
                    "model: $model_value" \
                    '---' \
                    '' \
                    "# Task: $(basename $epic_file)" \
                    '' \
                    "Execute milestones [$milestones] for this epic." \
                    '' \
                    '## Response' \
                    '' \
                    > $trigger_file
            end

            echo "  ✓ $agent_id  →  [$milestones]"
            echo "    $trigger_file"
        end
        set dispatched (math $dispatched + 1)
    end

    echo ""
    if test $dispatched -gt 0
        if test $dry_run -eq 1
            echo "🔍 $dispatched agent(s) would be dispatched (dry run  -  no changes made)"
            if test $yolo_mode -eq 1
                echo "   Would also launch agents in tmux windows"
            end
            echo ""
            return 0
        end

        echo "✅ $dispatched agent(s) dispatched"
        echo "   Check status: epic-status "(basename $epic_file)

        # --yolo: auto-launch agents in tmux windows
        if test $yolo_mode -eq 1
            # Determine tmux target session
            set -l tmux_target
            if set -q TMUX
                # Inside tmux  -  use current session
                set tmux_target (tmux display-message -p '#S')
            else
                # Not in tmux  -  create a new session
                set tmux_target "dispatch"
                if not tmux has-session -t $tmux_target 2>/dev/null
                    tmux new-session -d -s $tmux_target
                end
            end

            echo ""
            echo "🚀 Launching $dispatched agent(s) in tmux session '$tmux_target'..."
            echo ""

            for line in $agents
                set -l parts (string split '|' $line)
                set -l agent_id $parts[1]
                set -l agent_cwd (string replace '~' $HOME -- $parts[2])
                set -l milestones $parts[3]

                if not test -d $agent_cwd
                    continue
                end

                # Window name: agent id without -agent suffix for brevity
                set -l win_name (string replace -r -- '-agent$' '' $agent_id)

                tmux new-window -t $tmux_target -n $win_name -c $agent_cwd \
                    "fish -c 'claude -p --dangerously-skip-permissions --model $model_value --max-budget-usd 5.00 \"pickup on our dispatches\"'"

                echo "  🚀 $agent_id → tmux:$tmux_target:$win_name"

                # Telemetry: dispatch_yolo_launch (EPIC-060 M4)
                if functions -q emit_jsonl
                    set -l epic_basename (basename $epic_file)
                    emit_jsonl \
                        --layer orchestration \
                        --event-type dispatch_yolo_launch \
                        --command "epic-dispatch --yolo $epic_basename" \
                        --agent $agent_id \
                        --epic $epic_basename \
                        --metadata-json (printf '{"epic":"%s","agent_id":"%s","tmux_session":"%s","tmux_window":"%s","milestones":"%s","agent_cwd":"%s","agents_total":%d}' \
                            $epic_basename $agent_id $tmux_target $win_name $milestones $agent_cwd $dispatched)
                end
            end
            echo ""
        else
            echo "   Tip: use --yolo to auto-launch agents"
        end
    else
        echo "⚠️  No agents dispatched (check agent CWDs exist and milestones are assigned)"
    end
    echo ""
end
