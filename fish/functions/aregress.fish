# aregress.fish — Go CLI dormancy→retire regression candidate detection
# EPIC-007: 14-day dormancy threshold, confidence floor 0.7, full evidence bundle
# targets go_cli layer events (not fish functions — see aregress-enhanced for fish pruning)
# Suppression: CLIs with a 'retired' edge in topology-edges.jsonl are skipped (no re-surfacing)

function aregress --description "Detect dormant go_cli→retire regression candidates"
    set -l dormancy_days 14
    set -l confidence_floor 0.7
    set -l dry_run false
    set -l fixture_file ""
    set -l as_of_date ""
    set -l window 60

    set -l i 1
    while test $i -le (count $argv)
        switch $argv[$i]
            case --dormancy-days
                set i (math $i + 1)
                set dormancy_days $argv[$i]
            case --confidence-floor
                set i (math $i + 1)
                set confidence_floor $argv[$i]
            case --dry-run
                set dry_run true
            case --fixture
                set i (math $i + 1)
                set fixture_file $argv[$i]
            case --as-of-date
                set i (math $i + 1)
                set as_of_date $argv[$i]
            case --window
                set i (math $i + 1)
                set window $argv[$i]
        end
        set i (math $i + 1)
    end

    if test -z "$as_of_date"
        set as_of_date (date -u +%Y%m%dT%H%M%SZ)
    end
    set -l as_of_epoch (date -j -f "%Y%m%dT%H%M%SZ" "$as_of_date" +%s 2>/dev/null)
    set -l window_cutoff_epoch (math "$as_of_epoch - $window * 86400")
    set -l window_cutoff_ts (date -j -f "%s" "$window_cutoff_epoch" +"%Y%m%dT%H%M%SZ" 2>/dev/null)

    # Resolve event files
    set -l event_files
    if test -n "$fixture_file"
        if test -f "$fixture_file"
            set event_files $fixture_file
        else
            echo "aregress: fixture not found: $fixture_file" >&2
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

    if test (count $event_files) -eq 0
        echo "aregress: no event files found for window ($window days)"
        return 0
    end

    # Pre-filter malformed JSON lines
    set -l clean_input (mktemp /tmp/aregress-events-XXXXXX.jsonl)
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

    # jq: per-CLI last_used, usage counts (user_intent go_cli events only)
    set -l jq_filter_file ~/.automation-metrics/scripts/aregress-candidates.jq

    set -l cli_stats (jq -rn --rawfile content $clean_input --arg cutoff $window_cutoff_ts \
        -f $jq_filter_file 2>/dev/null | jq -rc '.')
    rm -f $clean_input

    # Build suppression set from topology-edges.jsonl (rel_type=retired)
    set -l suppress_set
    set -l edges_file ~/.automation-metrics/topology-edges.jsonl
    if test -f $edges_file
        set suppress_set (jq -r 'select(.rel_type=="retired") | .source' $edges_file 2>/dev/null)
    end

    set -l evaluated 0
    set -l emitted 0

    for stat in $cli_stats
        set -l cli (echo $stat | jq -r '.cli')

        # Skip CLIs with a confirmed 'retired' topology edge
        set -l cli_base (string split ' ' $cli)[1]
        if contains -- $cli_base $suppress_set
            echo "aregress: $cli — suppressed (retired topology edge present) (I202)"
            continue
        end

        set -l last_used_ts (echo $stat | jq -r '.last_used')
        set -l usage_first (echo $stat | jq -r '.usage_first_half')
        set -l usage_second (echo $stat | jq -r '.usage_second_half')

        # Compute dormancy_days = as_of_epoch - last_used_epoch
        set -l last_used_epoch (date -j -f "%Y%m%dT%H%M%SZ" "$last_used_ts" +%s 2>/dev/null)
        set -l dormancy_secs (math "$as_of_epoch - $last_used_epoch")
        set -l dormancy_d (math "floor($dormancy_secs / 86400)")

        # decay_rate = (first_half_usage - second_half_usage) / (window / 2) events/day drop
        set -l half_window (math "max(1, $window / 2)")
        set -l decay_rate (math "round(($usage_first - $usage_second) / $half_window * 1000) / 1000")

        # Confidence = 0.7 + (dormancy_days - threshold) * 0.02, capped at 1.0
        set -l confidence_raw (math "0.7 + ($dormancy_d - $dormancy_days) * 0.02")
        set -l confidence (math "min(1.0, max(0.0, round($confidence_raw * 1000) / 1000))")

        set evaluated (math $evaluated + 1)

        # CT-7: below dormancy threshold → confidence < 0.7 → no event
        if test (math "floor($confidence * 1000)") -lt (math "floor($confidence_floor * 1000)")
            echo "aregress: $cli — dormancy $dormancy_d day(s) < $dormancy_days day threshold, confidence=$confidence (I201)"
            continue
        end

        set -l evidence (jq -cn \
            --argjson dormancy $dormancy_d \
            --arg last_used $last_used_ts \
            --argjson decay $decay_rate \
            '{dormancy_days:$dormancy,last_used:$last_used,decay_rate:$decay,scope:"single_machine"}')

        set -l metadata (jq -cn \
            --arg source $cli \
            --argjson confidence $confidence \
            --argjson ev $evidence \
            '{source:$source,source_layer:"go_cli",target_layer:"fish",confidence:$confidence,cynefin_domain:"clear",evidence:$ev}')

        if test $dry_run = false
            if functions -q emit_jsonl
                emit_jsonl --layer topology --event-type regression_candidate --command aregress \
                    --metadata-json $metadata
            end
        else
            echo "[dry-run] regression_candidate: $cli (dormancy: $dormancy_d days, confidence: $confidence)"
        end

        set emitted (math $emitted + 1)
        echo "  ↓ $cli: dormancy=$dormancy_d days, last_used=$last_used_ts, decay_rate=$decay_rate, confidence=$confidence"
    end

    echo ""
    echo "aregress: evaluated $evaluated CLI(s), emitted $emitted regression_candidate event(s)"
end
