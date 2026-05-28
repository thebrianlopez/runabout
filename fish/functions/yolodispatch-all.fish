function yolodispatch-all --description 'Scan all agent CWDs for pending dispatches and launch in tmux'
    argparse 's/split' 'l/local' 'g/global' 'w/windows' -- $argv
    or return 1

    # Defaults: --local --split (use --global and/or --windows to override)
    if not set -q _flag_global
        set _flag_local 1
    end
    if not set -q _flag_windows
        set _flag_split 1
    end

    set -l pending_agents
    set -l pending_cwds
    set -l pending_triggers
    set -l _org_base (set -q CHAIN_ORG; and echo $CHAIN_ORG; or echo "$HOME/code/personal")

    if set -q _flag_local
        # Local mode: scan CWD and immediate subdirs for .claude-dispatch/ triggers
        # Works for any workspace — no global registration required
        set -l scan_dirs (pwd -P)
        for subdir in (pwd -P)/*/
            set -l resolved (string replace -r '/$' '' $subdir)
            # Skip symlinks — they point to directories owned by other agents
            test -L $resolved; and continue
            set -a scan_dirs $resolved
        end

        for dir in $scan_dirs
            set -l dispatch_dir $dir/.claude-dispatch
            if not test -d $dispatch_dir
                continue
            end

            for trigger in $dispatch_dir/*.json
                if test -f $trigger
                    # Derive agent name from directory basename
                    set -l agent_id (basename $dir)-agent
                    set -a pending_agents $agent_id
                    set -a pending_cwds $dir
                    set -a pending_triggers $trigger
                end
            end
        end
    else
        # Global mode: read agent CWDs from dispatch registry, fall back to org.yaml
        set -l registry_file $HOME/.automation-metrics/dispatch-registry.jsonl
        set -l agent_dirs

        if test -f "$registry_file"
            for line in (jq -r '"\(.agent)|\(.cwd)"' "$registry_file" 2>/dev/null)
                set -a agent_dirs $line
            end
        end

        if test (count $agent_dirs) -eq 0
            # org.yaml fallback when registry is absent or empty
            set -l org_yaml "$_org_base/docs/org.yaml"
            if test -f "$org_yaml"
                for line in (yq '.agents[] | .id + "|" + .cwd' "$org_yaml" 2>/dev/null)
                    set -l parts (string split '|' $line)
                    set -l cwd (string replace '~' $HOME $parts[2])
                    set -a agent_dirs "$parts[1]|$cwd"
                end
            end
        end

        for entry in $agent_dirs
            set -l parts (string split '|' $entry)
            set -l agent_id $parts[1]
            set -l agent_cwd $parts[2]
            set -l dispatch_dir $agent_cwd/.claude-dispatch

            if not test -d $dispatch_dir
                continue
            end

            # Dual-glob: both .json (legacy) and .md (new substrate) — EPIC-073 M3
            for trigger in $dispatch_dir/*.json $dispatch_dir/*.md
                if test -f $trigger
                    set -a pending_agents $agent_id
                    set -a pending_cwds $agent_cwd
                    set -a pending_triggers $trigger
                end
            end
        end
    end

    if test (count $pending_agents) -eq 0
        if set -q _flag_local
            echo "✅ No pending dispatches in "(pwd -P)" or subdirectories."
        else
            echo "✅ No pending dispatches found across all agents."
        end
        return 0
    end

    # Deduplicate by agent (one slot per agent, even with multiple triggers)
    set -l unique_agents
    set -l unique_cwds
    for i in (seq (count $pending_agents))
        set -l agent_id $pending_agents[$i]
        set -l agent_cwd $pending_cwds[$i]
        if not contains $agent_id $unique_agents
            set -a unique_agents $agent_id
            set -a unique_cwds $agent_cwd
        end
    end

    echo ""
    echo "📡 Found "(count $pending_triggers)" trigger(s) across "(count $unique_agents)" agent(s):"
    echo ""
    for i in (seq (count $pending_agents))
        echo "  • $pending_agents[$i]  ←  "(basename $pending_triggers[$i])
    end
    echo ""

    # Determine tmux target session
    set -l tmux_target
    if set -q TMUX
        set tmux_target (tmux display-message -p '#S')
    else
        set tmux_target "dispatch"
        if not tmux has-session -t $tmux_target 2>/dev/null
            tmux new-session -d -s $tmux_target
        end
    end

    if set -q _flag_split
        # Split mode: left pane = shell, right pane = agents stacked vertically
        #
        # ┌──────────┬──────────┐
        # │          │ agent 1  │
        # │  shell   ├──────────┤
        # │          │ agent 2  │
        # │          ├──────────┤
        # │          │ agent 3  │
        # └──────────┴──────────┘

        set -l left_pane (tmux display-message -p '#{pane_id}')
        set -l last_pane

        echo "🚀 Launching "(count $unique_agents)" agent(s) as split panes..."
        echo ""

        for i in (seq (count $unique_agents))
            set -l agent_id $unique_agents[$i]
            set -l agent_cwd $unique_cwds[$i]

            if test $i -eq 1
                # First agent: vertical split → creates right pane
                set last_pane (tmux split-window -h -c $agent_cwd -P -F '#{pane_id}' \
                    "fish -c 'yolo --opus \"pickup on our dispatches\"'")
            else
                # Subsequent: horizontal split within right side
                set last_pane (tmux split-window -v -t $last_pane -c $agent_cwd -P -F '#{pane_id}' \
                    "fish -c 'yolo --opus \"pickup on our dispatches\"'")
            end

            echo "  🚀 $agent_id → pane"
        end

        # Even out right-side panes and return focus to shell
        tmux select-layout main-vertical
        tmux select-pane -t $left_pane

        echo ""
        echo "✅ All agents launched in split layout."
        echo ""
    else
        # Window mode: one tmux window per agent
        echo "🚀 Launching "(count $unique_agents)" agent(s) in tmux session '$tmux_target'..."
        echo ""

        for i in (seq (count $unique_agents))
            set -l agent_id $unique_agents[$i]
            set -l agent_cwd $unique_cwds[$i]
            set -l win_name (string replace -r -- '-agent$' '' $agent_id)

            tmux new-window -a -t $tmux_target -n $win_name -c $agent_cwd \
                "fish -c 'yolo --opus \"pickup on our dispatches\"'"

            echo "  🚀 $agent_id → tmux:$tmux_target:$win_name"
        end

        echo ""
        echo "✅ All agents launched. Monitor with: tmux list-windows -t $tmux_target"
        echo ""
    end
end
