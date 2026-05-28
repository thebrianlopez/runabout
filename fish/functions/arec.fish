# arec.fish - Recommendation tracking closed loop
# Part of EPIC-006: Recommendation Tracking Closed Loop
# Tracks crystallization recommendations with measurable before/after metrics

function arec --description "Track automation recommendations with measurable outcomes"
    set -l metrics_dir ~/.automation-metrics
    if set -q AUTOMATION_METRICS_DIR
        set metrics_dir $AUTOMATION_METRICS_DIR
    end
    set -l registry "$metrics_dir/recommendations.jsonl"

    if test (count $argv) -eq 0
        _arec_help
        return 0
    end

    switch $argv[1]
        case create
            _arec_create $metrics_dir $registry $argv[2..]
        case status
            _arec_status $registry $argv[2..]
        case measure
            _arec_measure $registry $argv[2..]
        case close
            _arec_close $registry $argv[2..]
        case stats
            _arec_stats $metrics_dir $argv[2..]
        case help --help -h
            _arec_help
        case '*'
            echo "Unknown subcommand: $argv[1]" >&2
            echo "Run: arec help" >&2
            return 1
    end
end

function _arec_help
    echo "Usage: arec <command> [options]"
    echo ""
    echo "Commands:"
    echo "  create   Register a new recommendation with baseline metric"
    echo "  status   Show all recommendations with lifecycle state"
    echo "  measure  Re-run stored queries and update measurements"
    echo "  close    Close a recommendation with final measurement"
    echo "  stats    Show proposal acceptance rate and governance summary"
    echo "  help     Show this help"
    echo ""
    echo "Examples:"
    echo "  arec create --title 'Reduce find usage' --source terminal-history-insights \\"
    echo "    --metric 'find command count' --baseline 60 --target 10 \\"
    echo "    --query 'jq -rs \"[.[] | select(.event_type==\\\"tool_use\\\")] | map(.command | split(\\\" \\\") | .[0]) | map(select(.==\\\"find\\\")) | length\"'"
    echo ""
    echo "  arec status"
    echo "  arec measure --id rec-001"
    echo "  arec measure --all"
    echo "  arec close --id rec-001"
end

function _arec_create --argument-names metrics_dir registry
    set -l argv_rest $argv[3..]
    set -l title ""
    set -l source ""
    set -l metric ""
    set -l baseline ""
    set -l target ""
    set -l query_cmd ""
    set -l category ""

    set -l i 1
    while test $i -le (count $argv_rest)
        switch $argv_rest[$i]
            case --title -t
                set i (math $i + 1)
                set title $argv_rest[$i]
            case --source -s
                set i (math $i + 1)
                set source $argv_rest[$i]
            case --metric -m
                set i (math $i + 1)
                set metric $argv_rest[$i]
            case --baseline -b
                set i (math $i + 1)
                set baseline $argv_rest[$i]
            case --target
                set i (math $i + 1)
                set target $argv_rest[$i]
            case --query -q
                set i (math $i + 1)
                set query_cmd $argv_rest[$i]
            case --category -c
                set i (math $i + 1)
                set category $argv_rest[$i]
        end
        set i (math $i + 1)
    end

    # Validate required fields
    if test -z "$title"
        echo "Error: --title is required" >&2
        return 1
    end
    if test -z "$metric"
        echo "Error: --metric is required" >&2
        return 1
    end
    if test -z "$baseline"
        echo "Error: --baseline is required" >&2
        return 1
    end

    # Generate recommendation ID
    set -l existing_count 0
    if test -f "$registry"
        set existing_count (wc -l < "$registry" | string trim)
    end
    set -l rec_id "rec-"(printf "%03d" (math $existing_count + 1))

    # Set defaults
    if test -z "$source"
        set source manual
    end
    if test -z "$target"
        set target 0
    end
    if test -z "$category"
        set category general
    end

    set -l timestamp (nowutc)

    # Build JSON record
    set -l json_query ""
    if test -n "$query_cmd"
        # Escape the query for JSON
        set json_query (echo -n "$query_cmd" | jq -Rs .)
    else
        set json_query "null"
    end

    set -l record (printf '{"id":"%s","title":"%s","source":"%s","metric":"%s","baseline":%s,"target":%s,"status":"proposed","created_at":"%s","category":"%s","query":%s,"measurements":[{"timestamp":"%s","value":%s,"type":"baseline"}]}' \
        "$rec_id" "$title" "$source" "$metric" "$baseline" "$target" "$timestamp" "$category" "$json_query" "$timestamp" "$baseline")

    # Validate JSON
    if not echo "$record" | jq . >/dev/null 2>&1
        echo "Error: failed to construct valid JSON" >&2
        return 1
    end

    # Append to registry
    mkdir -p (dirname "$registry")
    echo "$record" >> "$registry"

    # Emit telemetry
    if functions -q emit_jsonl
        emit_jsonl --layer orchestration --event-type function_call --command "arec create" \
            --metadata-json '{"rec_id": "'$rec_id'", "metric": "'$metric'", "baseline": '$baseline'}'
    end

    echo "Created recommendation $rec_id: $title"
    echo "  Metric: $metric (baseline: $baseline, target: $target)"
    echo "  Source: $source | Category: $category"
    if test -n "$query_cmd"
        echo "  Query: $query_cmd"
    end
end

function _arec_status --argument-names registry
    set -l argv_rest $argv[2..]
    set -l filter_status ""

    set -l i 1
    while test $i -le (count $argv_rest)
        switch $argv_rest[$i]
            case --filter
                set i (math $i + 1)
                set filter_status $argv_rest[$i]
        end
        set i (math $i + 1)
    end

    if not test -f "$registry"
        echo "No recommendations registered yet."
        echo "Run: arec create --title '...' --metric '...' --baseline N"
        return 0
    end

    # Use jq to format the status table
    set -l status_jq (mktemp /tmp/arec-status-XXXXXX.jq)
    printf '%s\n' '[inputs] |
    if $filter != "" then map(select(.status == $filter)) else . end |
    if length == 0 then "No recommendations found.\n" else
    "## Recommendation Tracker\n\n" +
    "| ID | Status | Title | Metric | Baseline | Target | Latest | Delta | Source |\n" +
    "|-----|--------|-------|--------|----------|--------|--------|-------|--------|\n" +
    (map(
      (.measurements | sort_by(.timestamp) | last) as $latest |
      (if ($latest.value != null and .baseline != null and .baseline != 0) then
        ((($latest.value - .baseline) / .baseline * 100) | round | tostring) + "%"
      else "—" end) as $delta |
      "| `\(.id)` | \(.status) | \(.title) | \(.metric) | \(.baseline) | \(.target) | \($latest.value // "—") | \($delta) | \(.source) |"
    ) | join("\n")) + "\n\n" +
    "Total: \(length) recommendations (" +
    ([group_by(.status) | .[] | "\(.[0].status): \(length)"] | join(", ")) + ")\n"
    end' > "$status_jq"

    jq -n -r --arg filter "$filter_status" -f "$status_jq" "$registry"
    rm -f "$status_jq"
end

function _arec_measure --argument-names registry
    set -l argv_rest $argv[2..]
    set -l rec_id ""
    set -l measure_all false
    set -l fixture_file ""
    set -l window_days 14

    set -l i 1
    while test $i -le (count $argv_rest)
        switch $argv_rest[$i]
            case --id
                set i (math $i + 1)
                set rec_id $argv_rest[$i]
            case --all -a
                set measure_all true
            case --fixture
                set i (math $i + 1)
                set fixture_file $argv_rest[$i]
            case --window
                set i (math $i + 1)
                set window_days $argv_rest[$i]
        end
        set i (math $i + 1)
    end

    if test -z "$rec_id" -a "$measure_all" = false
        echo "Error: specify --id <rec-id> or --all" >&2
        return 1
    end

    if not test -f "$registry"
        echo "No recommendations to measure." >&2
        return 1
    end

    set -l timestamp (nowutc)
    set -l updated_file (mktemp /tmp/arec-updated-XXXXXX.jsonl)
    set -l measured_count 0

    while read -l line
        set -l id (echo "$line" | jq -r '.id')
        set -l line_status (echo "$line" | jq -r '.status')
        set -l query (echo "$line" | jq -r '.query // empty')

        # Skip if not matching filter
        if test "$measure_all" = false -a "$id" != "$rec_id"
            echo "$line" >> "$updated_file"
            continue
        end

        # Skip closed recommendations (BT-3)
        if test "$line_status" = closed
            echo "$line" >> "$updated_file"
            continue
        end

        # Skip if no query stored
        if test -z "$query"
            echo "$line" >> "$updated_file"
            echo "  Skipping $id (no measurement query stored)"
            continue
        end

        # Resolve events input: fixture file or real events dir
        set -l event_files
        if test -n "$fixture_file"
            if test -f "$fixture_file"
                set event_files $fixture_file
            else
                echo "arec: $id: fixture not found: $fixture_file" >&2
                echo "$line" >> "$updated_file"
                continue
            end
        else
            set -l events_dir ~/.automation-metrics/events
            if set -q AUTOMATION_METRICS_DIR
                set events_dir $AUTOMATION_METRICS_DIR/events
            end
            for d in (seq 0 (math $window_days - 1))
                set -l check_date (date -j -v-{$d}d "+%Y-%m-%d")
                if test -f "$events_dir/$check_date.jsonl"
                    set -a event_files "$events_dir/$check_date.jsonl"
                end
            end
        end

        if test (count $event_files) -eq 0
            echo "arec: $id: no events in {$window_days}d window (E101)" >&2
            echo "$line" >> "$updated_file"
            continue
        end

        echo "Measuring $id..."

        # Pre-filter: strip malformed JSON lines
        set -l clean_input (mktemp /tmp/arec-events-XXXXXX.jsonl)
        for ef in $event_files
            python3 -c "
import json, sys
with open(sys.argv[1]) as f:
    for line in f:
        line = line.strip()
        if not line: continue
        try:
            json.loads(line)
            print(line)
        except json.JSONDecodeError:
            pass
" $ef >> $clean_input
        end

        # Dedup: multiple Stop events per session emit cumulative snapshots — keep max(turns) per session_id
        set -l uses_session_summary (string match -r "session_summary" "$query")
        if test -n "$uses_session_summary"
            set -l deduped_input (mktemp /tmp/arec-dedup-XXXXXX.jsonl)
            python3 -c "
import json, sys
events, other = {}, []
with open(sys.argv[1]) as f:
    for line in f:
        line = line.strip()
        if not line: continue
        try:
            obj = json.loads(line)
        except: continue
        if obj.get('event_type') == 'session_summary':
            sid = obj.get('session_id', '')
            turns = (obj.get('metadata') or {}).get('turns', 0) or 0
            prev = events.get(sid)
            if prev is None or turns > ((prev.get('metadata') or {}).get('turns', 0) or 0):
                events[sid] = obj
        else:
            other.append(line)
for line in other:
    print(line)
for obj in events.values():
    print(json.dumps(obj))
" "$clean_input" > "$deduped_input"
            mv "$deduped_input" "$clean_input"
        end

        # W101/E102 detection for session_summary recs
        if test -n "$uses_session_summary"
            set -l diag (jq -rn --rawfile content $clean_input '
                ($content | rtrimstr("\n") | split("\n") | map(select(. != "") | try fromjson catch null) | map(select(. != null))) as $events |
                ($events | map(select(.event_type == "session_summary"))) as $sessions |
                {
                  w101: ($sessions | map(select(.metadata.cache_write_tokens == null and .metadata.cache_read_tokens == null)) | length),
                  e102: ($sessions | map(
                    ((.metadata.input_tokens // 0) + (.metadata.cache_write_tokens // 0) + (.metadata.cache_read_tokens // 0)) as $ei |
                    select($ei == 0)
                  ) | length)
                } | "\(.w101) \(.e102)"
            ' 2>/dev/null)
            set -l w101 (echo $diag | cut -d' ' -f1)
            set -l e102 (echo $diag | cut -d' ' -f2)
            if test "$w101" -gt 0 2>/dev/null
                echo "arec: $id: $w101 pre-v2.7 session(s) — using input_tokens fallback (W101)" >&2
            end
            if test "$e102" -gt 0 2>/dev/null
                echo "arec: $id: $e102 session(s) skipped — zero effective_input (E102)" >&2
            end
        end

        set -l measured_value (eval "jq -rs '$query' $clean_input" 2>&1)
        set -l query_status $status
        rm -f $clean_input

        if test $query_status -ne 0
            echo "  Query failed for $id: $measured_value" >&2
            echo "$line" >> "$updated_file"
            continue
        end

        # Validate numeric result (null means no qualifying sessions)
        if test "$measured_value" = null -o "$measured_value" = ""
            echo "arec: $id: no qualifying sessions in window (E101)" >&2
            echo "$line" >> "$updated_file"
            continue
        end
        if not string match -qr '^\-?[0-9]+\.?[0-9]*$' "$measured_value"
            echo "  Non-numeric result for $id: $measured_value" >&2
            echo "$line" >> "$updated_file"
            continue
        end

        # Add measurement and update status
        set -l new_measurement "{\"timestamp\":\"$timestamp\",\"value\":$measured_value,\"type\":\"measurement\"}"
        set -l updated_line (echo "$line" | jq -c ".measurements += [$new_measurement] | .status = \"measured\"")
        echo "$updated_line" >> "$updated_file"

        # Calculate delta
        set -l baseline (echo "$line" | jq -r '.baseline')
        set -l target_val (echo "$line" | jq -r '.target')
        if test "$baseline" != "0" -a "$baseline" != "null"
            set -l delta_pct (math "round(($measured_value - $baseline) / $baseline * 100)")
            echo "  $id: $baseline -> $measured_value ($delta_pct% vs baseline, target: $target_val)"
        else
            echo "  $id: baseline=$baseline -> current=$measured_value (target: $target_val)"
        end

        set measured_count (math $measured_count + 1)

        # Emit config_metric event (CT-5)
        _arec_emit_config_metric $id $measured_value $baseline $target_val

        # Emit telemetry
        if functions -q emit_jsonl
            emit_jsonl --layer orchestration --event-type function_call --command "arec measure" \
                --metadata-json '{"rec_id": "'$id'", "measured_value": '$measured_value'}'
        end
    end < "$registry"

    # Replace registry with updated version
    if test -f "$updated_file"
        mv "$updated_file" "$registry"
    end

    echo ""
    echo "Measured $measured_count recommendation(s)."
end

function _arec_emit_config_metric --argument-names rec_id value baseline target
    set -l ts (date -u +%Y%m%dT%H%M%SZ)
    set -l today_log "$HOME/.automation-metrics/events/"(date +%Y-%m-%d)".jsonl"
    set -l baseline_json (test -n "$baseline" && echo $baseline || echo null)
    set -l target_json (test -n "$target" && echo $target || echo null)
    printf '{"timestamp":"%s","layer":"orchestration","event_type":"config_metric","command":"arec measure","metadata":{"metric_name":"rec_measurement","recommendation_id":"%s","value":%s,"baseline":%s,"target":%s}}\n' \
        "$ts" "$rec_id" "$value" "$baseline_json" "$target_json" >> "$today_log" 2>/dev/null
end

function _arec_close --argument-names registry
    set -l argv_rest $argv[2..]
    set -l rec_id ""
    set -l note ""

    set -l i 1
    while test $i -le (count $argv_rest)
        switch $argv_rest[$i]
            case --id
                set i (math $i + 1)
                set rec_id $argv_rest[$i]
            case --note -n
                set i (math $i + 1)
                set note $argv_rest[$i]
        end
        set i (math $i + 1)
    end

    if test -z "$rec_id"
        echo "Error: --id is required" >&2
        return 1
    end

    if not test -f "$registry"
        echo "No recommendations to close." >&2
        return 1
    end

    set -l timestamp (nowutc)
    set -l updated_file (mktemp /tmp/arec-close-XXXXXX.jsonl)
    set -l found false

    while read -l line
        set -l id (echo "$line" | jq -r '.id')
        if test "$id" = "$rec_id"
            set found true
            set -l note_json "null"
            if test -n "$note"
                set note_json (echo -n "$note" | jq -Rs .)
            end
            set -l final_measurement "{\"timestamp\":\"$timestamp\",\"value\":null,\"type\":\"close\",\"note\":$note_json}"
            set -l updated (echo "$line" | jq -c ".status = \"closed\" | .closed_at = \"$timestamp\" | .measurements += [$final_measurement]")
            echo "$updated" >> "$updated_file"

            set -l baseline (echo "$line" | jq -r '.baseline')
            set -l latest (echo "$line" | jq -r '[.measurements[] | select(.type != "close") | .value] | last')
            echo "Closed $rec_id (baseline: $baseline, last measurement: $latest)"
        else
            echo "$line" >> "$updated_file"
        end
    end < "$registry"

    if test "$found" = false
        echo "Recommendation $rec_id not found." >&2
        rm -f "$updated_file"
        return 1
    end

    mv "$updated_file" "$registry"

    # Emit telemetry
    if functions -q emit_jsonl
        emit_jsonl --layer orchestration --event-type function_call --command "arec close" \
            --metadata-json '{"rec_id": "'$rec_id'"}'
    end
end

# _arec_stats — proposal acceptance rate and governance summary (EPIC-028 M3)
function _arec_stats --argument-names metrics_dir
    set -l proposals_dir "$metrics_dir/proposals"
    set -l decisions_dir "$metrics_dir/decisions"
    set -l registry "$metrics_dir/recommendations.jsonl"

    # Recommendation summary from registry
    set -l total_recs 0
    set -l open_recs 0
    set -l closed_recs 0
    if test -f $registry
        set total_recs (wc -l < $registry | string trim)
        set open_recs (grep -c '"status":"proposed"\|"status":"measured"' $registry 2>/dev/null || echo 0)
        set closed_recs (grep -c '"status":"closed"' $registry 2>/dev/null || echo 0)
    end

    # Proposal counts from proposals dir
    set -l total_proposals 0
    set -l pending_proposals 0
    set -l approved_proposals 0
    set -l rejected_proposals 0

    if test -d $proposals_dir
        set -l pfiles $proposals_dir/*.jsonl
        if test -e $pfiles[1]
            for f in $pfiles
                set -l p (cat $f | head -1)
                set -l s (echo $p | jq -r '.status')
                set total_proposals (math $total_proposals + 1)
                switch $s
                    case pending
                        set pending_proposals (math $pending_proposals + 1)
                    case approved
                        set approved_proposals (math $approved_proposals + 1)
                    case rejected
                        set rejected_proposals (math $rejected_proposals + 1)
                end
            end
        end
    end

    # Rolling 30d acceptance rate from decision files
    set -l decided_30d 0
    set -l approved_30d 0
    set -l active_snoozes 0
    set -l now_epoch (date -u +%s)
    set -l cutoff_epoch (math "$now_epoch - 2592000") # 30 days in seconds

    if test -d $decisions_dir
        for df in $decisions_dir/*.json
            if not test -e $df
                continue
            end
            set -l d (cat $df)
            set -l dec (echo $d | jq -r '.decision')
            set -l ts (echo $d | jq -r '.timestamp')
            set -l snooze (echo $d | jq -r '.snooze_until // empty')

            # Parse decision timestamp for 30d window
            set -l dec_epoch (date -j -f "%Y%m%dT%H%M%SZ" $ts +%s 2>/dev/null)
            if test -n "$dec_epoch" -a $dec_epoch -ge $cutoff_epoch
                set decided_30d (math $decided_30d + 1)
                if test "$dec" = "approved"
                    set approved_30d (math $approved_30d + 1)
                end
            end

            # Check active snoozes
            if test "$dec" = "rejected" -a -n "$snooze"
                set -l snooze_epoch (date -j -f "%Y%m%dT%H%M%SZ" $snooze +%s 2>/dev/null)
                if test -n "$snooze_epoch" -a $now_epoch -lt $snooze_epoch
                    set active_snoozes (math $active_snoozes + 1)
                end
            end
        end
    end

    set -l acceptance_rate "n/a (no decisions yet)"
    if test $decided_30d -gt 0
        set acceptance_rate (math "round($approved_30d / $decided_30d * 100)")"%  ($approved_30d approved / $decided_30d decided)"
    end

    echo "## arec Governance Stats"
    echo ""
    echo "### Recommendations"
    printf "  Total:              %d\n" $total_recs
    printf "  Open:               %d\n" $open_recs
    printf "  Closed:             %d\n" $closed_recs
    echo ""
    echo "### Directive Proposals (all time)"
    printf "  Total:              %d\n" $total_proposals
    printf "  Pending review:     %d\n" $pending_proposals
    printf "  Approved:           %d\n" $approved_proposals
    printf "  Rejected:           %d\n" $rejected_proposals
    printf "  Active snoozes:     %d\n" $active_snoozes
    echo ""
    echo "### Acceptance Rate (rolling 30d)"
    printf "  proposal_acceptance_rate: %s\n" $acceptance_rate
    echo ""
    if test $pending_proposals -gt 0
        echo "  ▶ Run 'apropose review' to process $pending_proposals pending proposal(s)."
    end
end
