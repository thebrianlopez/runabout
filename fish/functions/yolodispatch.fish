function yolodispatch -d "yolo into /dispatch  -  default: interactive TUI, --headless/-H for fire-and-forget"
    # Strip --headless/-H before positional parsing
    set -l headless 0
    set -l argv_clean
    for arg in $argv
        if test "$arg" = --headless -o "$arg" = -H
            set headless 1
        else
            set -a argv_clean $arg
        end
    end

    set -l target $argv_clean[1]
    set -l rest $argv_clean[2..-1]
    set -l cwd
    set -l _org_base (set -q CHAIN_ORG; and echo $CHAIN_ORG; or echo "$HOME/code/personal")

    # Guard: detect epic file passed as agent name
    if string match -q '*.md' "$target"; or string match -q '*EPIC*' "$target"
        echo "yolodispatch: '$target' looks like an epic file" >&2
        echo "  Did you mean: epic-dispatch $target" >&2
        return 1
    end

    # Handle empty/local target first
    switch "$target"
        case '' .
            if test $headless -eq 1
                _yolodispatch_local --headless $rest
            else
                _yolodispatch_local $rest
            end
            return $status
    end

    # Resolve agent CWD from org registry, with alias support
    set cwd (_yolodispatch_resolve_cwd "$target")
    if test -z "$cwd"
        echo "yolodispatch: unknown agent '$target'" >&2
        echo "  Tip: agents are resolved from \$CHAIN_ORG/docs/org.yaml" >&2
        # List known agents
        set -l org_yaml "$_org_base/docs/org.yaml"
        if test -f "$org_yaml"
            echo "  known:" (yq '.agents[].id' "$org_yaml" | string replace -r '-agent$' '' | string join ' | ') "| . (cwd)" >&2
        end
        return 1
    end

    # Named agent: single-agent launch
    if not test -d "$cwd"
        echo "yolodispatch: $cwd does not exist" >&2
        return 1
    end

    # Dual-glob: both .json (legacy) and .md (new substrate)  -  EPIC-073 M3
    set -l triggers
    for f in $cwd/.claude-dispatch/*.json $cwd/.claude-dispatch/*.md
        test -f "$f"; and set -a triggers $f
    end

    if test (count $triggers) -eq 0
        echo "yolodispatch: no pending dispatches in $cwd" >&2
        return 0
    end

    set -l prompt "dispatch"
    if test (count $rest) -gt 0
        set prompt "$prompt $rest"
    end

    # Parse model from first trigger's frontmatter (default: sonnet)
    set -l dispatch_model (yq --front-matter=extract '.model // "sonnet"' $triggers[1])

    echo ""
    echo "Found "(count $triggers)" trigger(s) for $target (model: $dispatch_model):"
    echo ""
    for t in $triggers
        echo "  $target  <-  "(basename $t)
    end
    echo ""

    # Interactive TUI (default): Claude opens in a new tmux window with its full UI. The
    # session-start hook fires automatically and surfaces dispatches - switch to the window to
    # observe and optionally interact. Pass --headless/-H for fire-and-forget: `claude -p` runs
    # non-interactively, the calling shell returns immediately, `exec fish` keeps the window open
    # for post-completion audit. Budget cap only applies in headless mode; user supervises interactively.
    set -l win_name "$target:"(basename $triggers[1] | string replace -r '\.(md|json)$' '')
    set -l escaped_prompt (string replace -a "'" "\\'" -- $prompt)
    set -l trigger_stem (basename $triggers[1] | string replace -r '\.(md|json)$' '')

    # Capture window ID for dispatch lifecycle tracking (EPIC-001 M2)
    set -l window_id
    if test $headless -eq 1
        set window_id (tmux new-window -P -F '#{window_id}' -n "$win_name" -c $cwd \
            "fish -c 'claude -p --dangerously-skip-permissions --model $dispatch_model --max-budget-usd 5.00 \"$escaped_prompt\"; echo \"✅ Dispatch complete  -  audit ready.\"; exec fish'")
    else
        set window_id (tmux new-window -P -F '#{window_id}' -n "$win_name" -c $cwd \
            "claude --dangerously-skip-permissions --model $dispatch_model")
    end

    # Write sidecar + set per-window @agent_waiting (EPIC-001 M1/M2)
    if test -n "$window_id"
        echo "$trigger_stem" > /tmp/dispatch-window-$USER-$window_id.trigger
        tmux set-option -t "$window_id" @agent_waiting 1 2>/dev/null
    end

    set -l mode_label (test $headless -eq 1; and echo audit; or echo interactive)
    echo "$target -> window '$win_name' ($window_id, $mode_label)"
    echo ""
end

function _yolodispatch_resolve_cwd -d "Resolve agent alias to CWD from org registry"
    set -l target $argv[1]
    set -l _org_base (set -q CHAIN_ORG; and echo $CHAIN_ORG; or echo "$HOME/code/personal")
    set -l org_yaml "$_org_base/docs/org.yaml"

    if not test -f "$org_yaml"
        # Fallback: try common aliases without registry
        switch "$target"
            case claude
                echo ~/.claude
            case fish
                echo ~/.config/fish
            case global
                echo ~/.config
        end
        return
    end

    # Try exact agent ID match in both static and workspace agents (EPIC-073 P2)
    # Note: yq expressions use set -l to avoid fish $var[] array-slice parsing
    for list_key in agents workspace_agents
        set -l yq_expr '.'$list_key'[] | select(.id == "'$target'") | .cwd'
        set -l exact_cwd (yq "$yq_expr" "$org_yaml" 2>/dev/null)
        if test -n "$exact_cwd" -a "$exact_cwd" != "null"
            echo (string replace '~' $HOME -- $exact_cwd)
            return
        end
    end

    # Try suffix match: target + "-agent" (e.g., "claude" -> "claude-config-agent")
    # Search both agents[] and workspace_agents[] (EPIC-073 P2)
    for list_key in agents workspace_agents
        set -l yq_ids '.'$list_key'[].id'
        set -l agent_ids (yq "$yq_ids" "$org_yaml" 2>/dev/null)
        for aid in $agent_ids
            test -n "$aid" -a "$aid" != "null"; or continue
            set -l stem (string replace -r -- '-agent$' '' $aid)
            if test "$stem" = "$target"
                or string match -q -- "*$target*" $stem
                set -l yq_cwd '.'$list_key'[] | select(.id == "'$aid'") | .cwd'
                set -l matched_cwd (yq "$yq_cwd" "$org_yaml" 2>/dev/null)
                if test -n "$matched_cwd" -a "$matched_cwd" != "null"
                    echo (string replace '~' $HOME -- $matched_cwd)
                    return
                end
            end
        end
    end
end

function _yolodispatch_local -d "Scan CWD + subdirs for dispatches, split-stack in tmux"
    set -l headless 0
    set -l extra_prompt
    for arg in $argv
        if test "$arg" = --headless -o "$arg" = -H
            set headless 1
        else
            set -a extra_prompt $arg
        end
    end

    # Scan CWD and immediate subdirs
    set -l scan_dirs (pwd -P)
    for subdir in (pwd -P)/*/
        set -a scan_dirs (string replace -r '/$' '' $subdir)
    end

    set -l pending_agents
    set -l pending_cwds
    set -l pending_triggers

    for dir in $scan_dirs
        set -l dispatch_dir $dir/.claude-dispatch
        if not test -d $dispatch_dir
            continue
        end

        # Dual-glob: both .json (legacy) and .md (new substrate)  -  EPIC-073 M3
        for trigger in $dispatch_dir/*.json $dispatch_dir/*.md
            if test -f $trigger
                set -l agent_id (basename $dir)-agent
                set -a pending_agents $agent_id
                set -a pending_cwds $dir
                set -a pending_triggers $trigger
            end
        end
    end

    if test (count $pending_agents) -eq 0
        echo "yolodispatch: no pending dispatches in "(pwd -P)" or subdirectories." >&2
        return 0
    end

    # Deduplicate by agent
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
    echo "Found "(count $pending_triggers)" trigger(s) across "(count $unique_agents)" agent(s):"
    echo ""
    for i in (seq (count $pending_agents))
        echo "  $pending_agents[$i]  <-  "(basename $pending_triggers[$i])
    end
    echo ""

    set -l prompt "dispatch"
    if test (count $extra_prompt) -gt 0
        set prompt "$prompt $extra_prompt"
    end

    # New window per agent, named after dispatch metadata
    echo "Launching "(count $unique_agents)" agent(s) as new windows..."
    echo ""

    for i in (seq (count $unique_agents))
        set -l agent_id $unique_agents[$i]
        set -l agent_cwd $unique_cwds[$i]

        # Find first trigger for this agent to derive window name and model
        set -l trigger_name ""
        set -l trigger_path ""
        for j in (seq (count $pending_agents))
            if test "$pending_agents[$j]" = "$agent_id"
                set trigger_name (basename $pending_triggers[$j] | string replace -r '\.(md|json)$' '')
                set trigger_path $pending_triggers[$j]
                break
            end
        end

        # Parse model from trigger frontmatter (default: sonnet)
        set -l dispatch_model sonnet
        if test -n "$trigger_path"; and string match -q '*.md' "$trigger_path"
            set dispatch_model (yq --front-matter=extract '.model // "sonnet"' $trigger_path)
        end

        set -l win_name "$agent_id:"(test -n "$trigger_name"; and echo $trigger_name; or echo dispatch)
        set -l escaped_prompt (string replace -a "'" "\\'" -- $prompt)

        # Capture window ID for dispatch lifecycle tracking (EPIC-001 M2)
        set -l window_id
        if test $headless -eq 1
            set window_id (tmux new-window -P -F '#{window_id}' -n "$win_name" -c $agent_cwd \
                "fish -c 'claude -p --dangerously-skip-permissions --model $dispatch_model --max-budget-usd 5.00 \"$escaped_prompt\"; echo \"✅ Dispatch complete  -  audit ready.\"; exec fish'")
        else
            set window_id (tmux new-window -P -F '#{window_id}' -n "$win_name" -c $agent_cwd \
                "claude --dangerously-skip-permissions --model $dispatch_model")
        end

        # Write sidecar + set per-window @agent_waiting (EPIC-001 M1/M2)
        if test -n "$window_id"
            set -l sidecar_stem (test -n "$trigger_name"; and echo $trigger_name; or echo dispatch)
            echo "$sidecar_stem" > /tmp/dispatch-window-$USER-$window_id.trigger
            tmux set-option -t "$window_id" @agent_waiting 1 2>/dev/null
        end

        set -l mode_label (test $headless -eq 1; and echo audit; or echo interactive)
        echo "  $agent_id -> window '$win_name' ($window_id, model: $dispatch_model, $mode_label)"
    end

    echo ""
    echo "All agents launched in new windows."
    echo ""
end
