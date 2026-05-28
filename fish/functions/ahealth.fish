# ahealth.fish — Composite health score: adoption, anti-pattern reduction, layer coverage
# EPIC-009: composite = mean of non-null sub-scores; null propagation; go_lib always L0
#
# Sub-scores:
#   adoption_rate:          fish user_intent events / window_days (null = W501 if no events)
#   anti_pattern_reduction: % of recs where latest measurement < baseline (null = W502)
#   layer_coverage:         L2+ layers / declared_layers (go_lib always in denom, never numer)
#
# Declared layers (8 total):
#   cloud_llm fish go_cli go_lib topology interactive_shell orchestration claude_code

function ahealth --description "Composite health score across adoption, anti-pattern, and layer coverage"
    set -l window 30
    set -l format table
    set -l fixture_file ""
    set -l as_of_date ""

    set -l i 1
    while test $i -le (count $argv)
        switch $argv[$i]
            case --window
                set i (math $i + 1)
                set window $argv[$i]
            case --format
                set i (math $i + 1)
                set format $argv[$i]
            case --fixture
                set i (math $i + 1)
                set fixture_file $argv[$i]
            case --as-of-date
                set i (math $i + 1)
                set as_of_date $argv[$i]
        end
        set i (math $i + 1)
    end

    if test -z "$as_of_date"
        set as_of_date (date -u +%Y%m%dT%H%M%SZ)
    end
    set -l as_of_epoch (date -j -f "%Y%m%dT%H%M%SZ" "$as_of_date" +%s 2>/dev/null)
    set -l cutoff_epoch (math "$as_of_epoch - $window * 86400")
    set -l cutoff_ts (date -j -f "%s" "$cutoff_epoch" +"%Y%m%dT%H%M%SZ" 2>/dev/null)

    # Resolve event files
    set -l event_files
    if test -n "$fixture_file"
        if test -f "$fixture_file"
            set event_files $fixture_file
        else
            echo "ahealth: fixture not found: $fixture_file" >&2
            return 1
        end
    else
        set -l events_dir ~/.automation-metrics/events
        if set -q AUTOMATION_METRICS_DIR
            set events_dir $AUTOMATION_METRICS_DIR/events
        end
        for d in (seq 0 (math $window - 1))
            set -l check_date (date -j -v-{$d}d "+%Y-%m-%d")
            if test -f "$events_dir/$check_date.jsonl"
                set -a event_files "$events_dir/$check_date.jsonl"
            end
        end
    end

    # Merge + pre-filter malformed JSON
    set -l clean_input (mktemp /tmp/ahealth-events-XXXXXX.jsonl)
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
        except: pass
" $ef >> $clean_input
    end

    # Declared layers for layer_coverage (go_lib always L0)
    set -l declared_layers cloud_llm fish go_cli go_lib topology interactive_shell orchestration claude_code
    set -l declared_count (count $declared_layers)

    # --- Sub-score 1: adoption_rate ---
    # Count unique days with fish user_intent events; null if no events at all
    set -l fish_day_count (jq -rn --rawfile content $clean_input --arg cutoff $cutoff_ts '
        ($content | rtrimstr("\n") | split("\n") | map(select(. != "") | try fromjson catch null) | map(select(. != null))) as $events |
        ($events | map(select(
            .layer == "fish" and
            (.event_class == null or .event_class == "user_intent") and
            .timestamp >= $cutoff
        )) | map(.timestamp | .[0:10]) | unique | length)
    ' 2>/dev/null)

    set -l total_fish (jq -rn --rawfile content $clean_input --arg cutoff $cutoff_ts '
        ($content | rtrimstr("\n") | split("\n") | map(select(. != "") | try fromjson catch null) | map(select(. != null))) as $events |
        ($events | map(select(
            .layer == "fish" and
            (.event_class == null or .event_class == "user_intent") and
            .timestamp >= $cutoff
        )) | length)
    ' 2>/dev/null)

    set -l adoption_rate "null"
    if test -n "$total_fish" -a "$total_fish" != "0"
        set adoption_rate (math "round($fish_day_count / $window * 1000) / 1000")
        if test (math "floor($adoption_rate * 1000)") -gt 1000
            set adoption_rate 1.0
        end
    else
        echo "ahealth: adoption_rate undefined — no adoption events in $window d (W501)" >&2
    end

    # --- Sub-score 2: anti_pattern_reduction ---
    # From config_metric events (arec measurements): % of recs where latest value < baseline
    set -l recs_file ~/.automation-metrics/recommendations.jsonl
    if set -q AUTOMATION_METRICS_DIR
        set recs_file $AUTOMATION_METRICS_DIR/recommendations.jsonl
    end

    set -l anti_reduction "null"
    if test -f $recs_file
        set -l measured_recs 0
        set -l improving_recs 0
        while read -l rec
            if test -z "$rec"
                continue
            end
            set -l rec_status (echo $rec | jq -r '.status')
            if test "$rec_status" = "closed"
                continue
            end
            set -l baseline (echo $rec | jq -r '[.measurements[] | select(.type=="baseline")] | last | .value // empty')
            set -l latest (echo $rec | jq -r '[.measurements[] | select(.type=="measurement")] | last | .value // empty')
            if test -z "$baseline" -o -z "$latest"
                continue
            end
            set measured_recs (math $measured_recs + 1)
            if test (math "floor($latest * 1000)") -lt (math "floor($baseline * 1000)")
                set improving_recs (math $improving_recs + 1)
            end
        end < $recs_file

        if test $measured_recs -gt 0
            set anti_reduction (math "round($improving_recs / $measured_recs * 1000) / 1000")
        else
            echo "ahealth: anti_pattern_reduction undefined — run arec first (W502)" >&2
        end
    else
        echo "ahealth: anti_pattern_reduction undefined — run arec first (W502)" >&2
    end

    # --- Sub-score 3: layer_coverage ---
    # Count declared layers with events in window (go_lib always excluded from numerator)
    set -l active_layers 0
    for layer in $declared_layers
        if test "$layer" = "go_lib"
            continue
        end
        set -l layer_count (jq -rn --rawfile content $clean_input --arg layer $layer --arg cutoff $cutoff_ts '
            ($content | rtrimstr("\n") | split("\n") | map(select(. != "") | try fromjson catch null) | map(select(. != null))) as $events |
            ($events | map(select(.layer == $layer and .timestamp >= $cutoff)) | length)
        ' 2>/dev/null)
        if test -n "$layer_count" -a "$layer_count" != "0"
            set active_layers (math $active_layers + 1)
        end
    end
    set -l layer_coverage (math "round($active_layers / $declared_count * 1000) / 1000")

    rm -f $clean_input

    # --- Composite: mean of non-null sub-scores ---
    set -l scores_sum 0
    set -l scores_count 0
    if test "$adoption_rate" != "null"
        set scores_sum (math "$scores_sum + $adoption_rate")
        set scores_count (math $scores_count + 1)
    end
    if test "$anti_reduction" != "null"
        set scores_sum (math "$scores_sum + $anti_reduction")
        set scores_count (math $scores_count + 1)
    end
    set scores_sum (math "$scores_sum + $layer_coverage")
    set scores_count (math $scores_count + 1)

    set -l composite
    if test $scores_count -gt 0
        set composite (math "round($scores_sum / $scores_count * 1000) / 1000")
    else
        set composite 0.0
    end

    # --- Output ---
    set -l adoption_display $adoption_rate
    set -l anti_display $anti_reduction
    if test "$adoption_rate" = "null"
        set adoption_display "(undefined)"
    end
    if test "$anti_reduction" = "null"
        set anti_display "(undefined)"
    end

    if test "$format" = json
        jq -cn \
            --argjson adoption (test "$adoption_rate" = "null" && echo null || echo $adoption_rate) \
            --argjson anti_pattern (test "$anti_reduction" = "null" && echo null || echo $anti_reduction) \
            --argjson layer_coverage $layer_coverage \
            --argjson composite $composite \
            --argjson window $window \
            --argjson go_lib_level 0 \
            '{adoption_rate:$adoption,anti_pattern_reduction:$anti_pattern,layer_coverage:$layer_coverage,composite_score:$composite,window_days:$window,go_lib_level:$go_lib_level}'
    else
        echo "## ahealth — $as_of_date (window: $window d)"
        echo ""
        printf "  %-30s %s\n" "adoption_rate" $adoption_display
        printf "  %-30s %s\n" "anti_pattern_reduction" $anti_display
        printf "  %-30s $layer_coverage  ($active_layers / $declared_count declared; go_lib=L0)\n" "layer_coverage"
        echo ""
        printf "  %-30s %s\n" "composite_score" $composite
    end

    # --- Emit config_metric events ---
    if functions -q emit_jsonl
        for pair in \
            "adoption_rate:$adoption_rate" \
            "anti_pattern_reduction:$anti_reduction" \
            "layer_coverage:$layer_coverage" \
            "composite_health_score:$composite"
            set -l name (string split ':' $pair)[1]
            set -l val (string split ':' $pair)[2]
            if test "$val" != "null"
                emit_jsonl --layer orchestration --event-type config_metric --command ahealth \
                    --metadata-json (jq -cn --arg mn $name --argjson v $val \
                        '{metric_name:$mn,value:$v,baseline:null,target:null}')
            end
        end
    end

    echo ""
end
