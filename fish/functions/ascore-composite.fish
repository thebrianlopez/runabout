# ascore-composite — Composite Fitness Score Producer
# EPIC-002 M6: Reads session_summary events from bus, computes 5-family
# composite score per session, emits score_run events.
#
# Families (7 bus-available sub-metrics):
#   Outcome:   task_completion, cost_efficiency, execution_reliability
#   Taxonomy:  task_classification (coverage rate)
#   Archetype: workflow_reuse (tool distribution similarity)
#   Heuristic: search_efficiency, uncertainty_calibration
#
# Usage: ascore-composite [--days N] [--dry-run] [--session <id>]

function ascore-composite --description 'Compute composite fitness scores from session_summary events'
    set -l window 7
    set -l dry_run false
    set -l filter_session ""

    set -l i 1
    while test $i -le (count $argv)
        switch $argv[$i]
            case --days -d
                set i (math $i + 1)
                set window $argv[$i]
            case --dry-run -n
                set dry_run true
            case --session -s
                set i (math $i + 1)
                set filter_session $argv[$i]
            case --help -h
                echo "Usage: ascore-composite [--days N] [--dry-run] [--session <id>]"
                echo ""
                echo "Options:"
                echo "  --days N, -d N      Analysis window (default: 7)"
                echo "  --dry-run, -n       Print scores without emitting events"
                echo "  --session <id>      Score a single session"
                echo "  --help, -h          Show this help"
                return 0
        end
        set i (math $i + 1)
    end

    set -l events_dir ~/.automation-metrics/events
    if set -q AUTOMATION_METRICS_DIR
        set events_dir $AUTOMATION_METRICS_DIR/events
    end

    # Collect event files for the window
    set -l event_files
    set -l check_date (daysago $window)
    set -l end_date (date +%Y-%m-%d)
    while test "$check_date" != "$end_date"
        set -l f "$events_dir/$check_date.jsonl"
        if test -f "$f"
            set -a event_files $f
        end
        set check_date (date -j -v+1d -f "%Y-%m-%d" "$check_date" "+%Y-%m-%d")
    end
    if test -f "$events_dir/$end_date.jsonl"
        set -a event_files "$events_dir/$end_date.jsonl"
    end

    if test (count $event_files) -eq 0
        echo "No events found in $window-day window."
        return 0
    end

    # Extract session_summary events and compute scores per session via jq
    set -l session_filter ""
    if test -n "$filter_session"
        set session_filter " and .session_id == \"$filter_session\""
    end

    set -l scored_sessions (jq -rs --arg filter "$session_filter" '
        [.[] | select(.event_type == "session_summary"'"$session_filter"')] |
        map({
            session_id: .session_id,
            timestamp: .timestamp,
            cwd: .cwd,
            m: .metadata
        }) |
        # Deduplicate: keep last summary per session (sessions can emit multiple summaries)
        group_by(.session_id) | map(sort_by(.timestamp) | last) |
        # Score each session
        map({
            session_id: .session_id,
            timestamp: .timestamp,
            cwd: .cwd,

            # --- Outcome family ---
            # task_completion: proxy via lines_added > 0 or git_commits > 0 (binary: 0 or 100)
            task_completion: (if (.m.lines_added // 0) > 0 or (.m.git_commits // 0) > 0 then 100 else 0 end),

            # cost_efficiency: cache_hit_pct directly (0-100)
            cost_efficiency: ((.m.cache_hit_pct // 0) | . * 100 | round / 100),

            # execution_reliability: 100 - (tool_errors / max(tool_events, 1) * 100), clamped 0-100
            execution_reliability: ([100 - (((.m.tool_errors // 0) / ([(.m.tool_events // 1), 1] | max)) * 100), 0] | max | [., 100] | min | . * 100 | round / 100),

            # --- Taxonomy family ---
            # task_classification: presence of task_category in inference events (proxy: >0 prompt_count or user_message_count)
            # For now: 100 if session has user messages (classified), 0 if automated-only
            task_classification: (if (.m.user_message_count // 0) > 0 then 100 else 50 end),

            # --- Archetype family ---
            # workflow_reuse: tool distribution diversity (lower unique tools / total = more focused = higher reuse)
            # Score: 100 - (unique_tools / max(total_tool_events, 1) * 100), clamped to 0-100
            workflow_reuse: (
                ((.m.tool_distribution // {}) | keys | length) as $unique |
                ([(.m.tool_events // 1), 1] | max) as $total |
                if $unique <= 1 then 100
                elif $total <= 0 then 50
                else ([100 - ($unique / $total * 100), 0] | max | . * 100 | round / 100)
                end
            ),

            # --- Heuristic family ---
            # search_efficiency: inverse of tool_events per turn (fewer tools per turn = more efficient)
            # Score: max(0, 100 - (tool_events / max(turns, 1) - 1) * 20)
            search_efficiency: (
                ([(.m.turns // 1), 1] | max) as $turns |
                ((.m.tool_events // 0) / $turns) as $ratio |
                [100 - (($ratio - 1) * 20), 0] | max | [., 100] | min | . * 100 | round / 100
            ),

            # uncertainty_calibration: approval_rate (0-100, null defaults to 50)
            uncertainty_calibration: ((.m.approval_rate // 50) | . * 100 | round / 100)
        }) |
        # Compute composite (equal weight across 7 metrics)
        map(. + {
            composite: ((.task_completion + .cost_efficiency + .execution_reliability + .task_classification + .workflow_reuse + .search_efficiency + .uncertainty_calibration) / 7 | . * 100 | round / 100)
        }) |
        sort_by(-.composite) |
        .[] |
        @json
    ' $event_files 2>/dev/null)

    if test -z "$scored_sessions"
        echo "No session_summary events found in window."
        return 0
    end

    # Output header
    printf "%-38s %5s %5s %5s %5s %5s %5s %5s %7s\n" \
        "SESSION" "COMPL" "COST" "RELI" "CLASS" "REUSE" "SRCH" "CALIB" "SCORE"
    printf "%-38s %5s %5s %5s %5s %5s %5s %5s %7s\n" \
        "--------------------------------------" "-----" "-----" "-----" "-----" "-----" "-----" "-----" "-------"

    set -l total_composite 0
    set -l n_sessions 0

    for session_json in $scored_sessions
        set -l sid (echo $session_json | jq -r '.session_id')
        set -l tc (echo $session_json | jq -r '.task_completion')
        set -l ce (echo $session_json | jq -r '.cost_efficiency')
        set -l er (echo $session_json | jq -r '.execution_reliability')
        set -l tcl (echo $session_json | jq -r '.task_classification')
        set -l wr (echo $session_json | jq -r '.workflow_reuse')
        set -l se (echo $session_json | jq -r '.search_efficiency')
        set -l uc (echo $session_json | jq -r '.uncertainty_calibration')
        set -l composite (echo $session_json | jq -r '.composite')

        printf "%-38s %5.1f %5.1f %5.1f %5.1f %5.1f %5.1f %5.1f %7.1f\n" \
            $sid $tc $ce $er $tcl $wr $se $uc $composite

        set total_composite (math "$total_composite + $composite")
        set n_sessions (math $n_sessions + 1)

        # Emit score_run event
        if not $dry_run; and functions -q emit_jsonl
            emit_jsonl --layer topology --event-type score_run --command "ascore-composite" \
                --session-id "$sid" \
                --metadata-json (echo $session_json | jq -c '{
                    task_completion: .task_completion,
                    cost_efficiency: .cost_efficiency,
                    execution_reliability: .execution_reliability,
                    task_classification: .task_classification,
                    workflow_reuse: .workflow_reuse,
                    search_efficiency: .search_efficiency,
                    uncertainty_calibration: .uncertainty_calibration,
                    composite: .composite,
                    layer_fitness_score: (.composite | round)
                }')
        end
    end

    echo ""
    if test $n_sessions -gt 0
        set -l avg (math "$total_composite / $n_sessions" | xargs printf "%.1f")
        printf "Sessions scored: %d | Average composite: %s\n" $n_sessions $avg
    end

    if $dry_run
        echo "(dry-run — no events emitted)"
    end
end
