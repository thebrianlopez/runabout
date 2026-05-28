# agrad.fish — Fish→Go CLI graduation candidate detection
# EPIC-007: Confidence floor 0.7, full evidence bundle, no auto-promotion
# bypass_rate = bypass_count / usage_30d (how often bash bypasses the fish function)

function agrad --description "Detect fish→go_cli graduation candidates with confidence scoring"
    set -l window 30
    set -l confidence_floor 0.7
    set -l dry_run false
    set -l fixture_file ""
    set -l as_of_date ""

    set -l i 1
    while test $i -le (count $argv)
        switch $argv[$i]
            case --window
                set i (math $i + 1)
                set window $argv[$i]
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
            echo "agrad: fixture not found: $fixture_file" >&2
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
        echo "agrad: no event files found for window ($window days)"
        return 0
    end

    # Pre-filter malformed JSON lines into merged temp file
    set -l clean_input (mktemp /tmp/agrad-events-XXXXXX.jsonl)
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

    # jq: per-function usage_30d and bypass_count (user_intent events only)
    set -l jq_filter_file ~/.automation-metrics/scripts/agrad-candidates.jq

    set -l candidates (jq -rn --rawfile content $clean_input --arg cutoff $cutoff_ts \
        -f $jq_filter_file 2>/dev/null | jq -rc '.')
    rm -f $clean_input

    set -l evaluated 0
    set -l emitted 0

    for cand in $candidates
        set -l func (echo $cand | jq -r '.func')
        set -l usage (echo $cand | jq -r '.usage_30d')
        set -l bypass_count (echo $cand | jq -r '.bypass_count')

        # Compute complexity from function body (line count proxy)
        set -l complexity 30
        if functions -q $func 2>/dev/null
            set complexity (functions $func 2>/dev/null | wc -l | string trim)
        end

        # bypass_rate = bypass_count / usage_30d (how often bash bypasses the function)
        set -l bypass_rate 0
        if test $usage -gt 0
            set bypass_rate (math "round($bypass_count / $usage * 1000) / 1000")
        end

        # Confidence = usage_factor*0.5 + complexity_factor*0.3 + (1-bypass_rate)*0.2
        set -l usage_factor (math "min(1.0, $usage / 60.0)")
        set -l complexity_factor (math "min(1.0, $complexity / 60.0)")
        set -l bypass_factor (math "1.0 - $bypass_rate")
        set -l confidence (math "round(($usage_factor * 0.5 + $complexity_factor * 0.3 + $bypass_factor * 0.2) * 1000) / 1000")

        set evaluated (math $evaluated + 1)

        # Confidence floor enforcement (CT-1, RG-2)
        if test (math "floor($confidence * 1000)") -lt (math "floor($confidence_floor * 1000)")
            echo "agrad: $func — below confidence floor ($confidence < $confidence_floor) (I201)"
            continue
        end

        # W201: high bypass rate (BT-1)
        if test (math "floor($bypass_rate * 1000)") -gt 300
            echo "agrad: $func — bypass_rate=$bypass_rate > 0.3 — manual review needed (W201)" >&2
        end

        set -l layer_fitness_score (math "round($confidence * 100)")

        set -l evidence (jq -cn \
            --argjson usage $usage \
            --argjson bypass $bypass_rate \
            --argjson complexity $complexity \
            '{usage_30d:$usage,bypass_rate:$bypass,complexity:$complexity,scope:"single_machine"}')

        set -l metadata (jq -cn \
            --arg source $func \
            --argjson confidence $confidence \
            --argjson fitness $layer_fitness_score \
            --argjson ev $evidence \
            '{source:$source,source_layer:"fish",target_layer:"go_cli",confidence:$confidence,layer_fitness_score:$fitness,cynefin_domain:"complicated",evidence:$ev}')

        if test $dry_run = false
            if functions -q emit_jsonl
                emit_jsonl --layer topology --event-type graduation_candidate --command agrad \
                    --metadata-json $metadata
            end
        else
            echo "[dry-run] graduation_candidate: $func (confidence: $confidence, fitness: $layer_fitness_score)"
        end

        set emitted (math $emitted + 1)
        echo "  ↑ $func: usage=$usage bypass_rate=$bypass_rate complexity=$complexity confidence=$confidence"
    end

    echo ""
    echo "agrad: evaluated $evaluated candidates, emitted $emitted graduation_candidate event(s)"
end
