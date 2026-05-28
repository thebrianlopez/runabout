# adirective — Directive Proposal Consumer
#
# Reads recommendations.jsonl, applies taxonomy tier filtering from
# directive-taxonomy.md, and emits directive_proposal events + writes
# proposal files to proposals/.
#
# Tier thresholds (from directive-taxonomy.md frontmatter):
#   T1 (wording_reinforcement): auto_propose=true, threshold=0.75, window=14d
#   T2 (new_rule):              auto_propose=true, threshold=0.85, window=30d
#   T3 (model_routing):         auto_propose=false — E401, human drafts only
#   T4 (observability):         auto_propose=false — E401, human drafts only
#
# Confidence: read from rec's .confidence field if present; else computed from
# measurements as: tier_threshold + min(0.10, max(0, (baseline - current) / baseline))
# (improvement = current < baseline for anti-pattern recs)
#
# Dedup: proposals/{fingerprint}.jsonl per rec — SHA1(signal_id:target_file:current_val)
# Snooze: decisions/{fingerprint}.json with snooze_until blocks re-proposal

function adirective --description 'Read JSONL bus signals, apply taxonomy filtering, emit directive_proposal events'
    set -l dry_run false
    set -l filter_signal ""
    set -l force false
    set -l fixture_file ""
    set -l now_ts ""
    set -l proposals_dir_override ""
    set -l decisions_dir_override ""

    set -l i 1
    while test $i -le (count $argv)
        switch $argv[$i]
            case --dry-run -n
                set dry_run true
            case --force
                set force true
            case '--signal=*'
                set filter_signal (string replace --regex '^--signal=' '' $argv[$i])
            case --fixture
                set i (math $i + 1)
                set fixture_file $argv[$i]
            case --now
                set i (math $i + 1)
                set now_ts $argv[$i]
            case --proposals-dir
                set i (math $i + 1)
                set proposals_dir_override $argv[$i]
            case --decisions-dir
                set i (math $i + 1)
                set decisions_dir_override $argv[$i]
            case --help -h
                echo "Usage: adirective [--dry-run] [--signal=<id>] [--force]"
                echo "       adirective [--fixture <path>] [--now <ts>]"
                echo "       adirective [--proposals-dir <dir>] [--decisions-dir <dir>]"
                return 0
        end
        set i (math $i + 1)
    end

    set -l taxonomy ~/.claude/rules/directive-taxonomy.md
    set -l rules_dir ~/.claude/rules
    set -l recs_file ~/.automation-metrics/recommendations.jsonl
    if test -n "$fixture_file"
        set recs_file $fixture_file
    end
    set -l proposals_dir ~/.automation-metrics/proposals
    if test -n "$proposals_dir_override"
        set proposals_dir $proposals_dir_override
    end
    set -l decisions_dir ~/.automation-metrics/decisions
    if test -n "$decisions_dir_override"
        set decisions_dir $decisions_dir_override
    end
    set -l events_dir ~/.automation-metrics/events

    if test -z "$now_ts"
        set now_ts (date -u +%Y%m%dT%H%M%SZ)
    end
    set -l today (string sub -l 10 (string replace -a T - (string replace -a Z '' $now_ts)) | string replace -r '^([0-9]{4})-([0-9]{2})-([0-9]{2}).*' '$1-$2-$3')
    set -l today (date -j -f "%Y%m%dT%H%M%SZ" "$now_ts" +%Y-%m-%d 2>/dev/null)

    if not test -f $taxonomy
        echo "adirective: taxonomy not found: $taxonomy" >&2
        return 1
    end

    if not test -f $recs_file
        echo "adirective: recommendations not found: $recs_file" >&2
        return 1
    end

    mkdir -p $proposals_dir
    mkdir -p $decisions_dir

    # Read taxonomy thresholds and T4 blocklist via yq
    set -l t1_threshold (yq --front-matter=extract '.tiers.T1.confidence_threshold' $taxonomy 2>/dev/null)
    set -l t2_threshold (yq --front-matter=extract '.tiers.T2.confidence_threshold' $taxonomy 2>/dev/null)
    set -l t4_paths (yq --front-matter=extract '.t4_targets[]' $taxonomy 2>/dev/null)

    if test -z "$t1_threshold" -o "$t1_threshold" = "null"
        set t1_threshold 0.75
    end
    if test -z "$t2_threshold" -o "$t2_threshold" = "null"
        set t2_threshold 0.85
    end

    set -l now_epoch (date -j -f "%Y%m%dT%H%M%SZ" "$now_ts" +%s 2>/dev/null)

    set -l n_proposed 0
    set -l n_skipped 0
    set -l n_blocked 0

    while read -l rec
        if test -z "$rec"
            continue
        end

        set -l rec_id (echo $rec | jq -r '.id')
        set -l rec_status (echo $rec | jq -r '.status')
        set -l rec_title (echo $rec | jq -r '.title')
        set -l rec_category (echo $rec | jq -r '.category')
        set -l rec_created (echo $rec | jq -r '.created_at')

        if test "$rec_status" = "closed"
            continue
        end

        if test -n "$filter_signal"; and test "$rec_id" != "$filter_signal"
            continue
        end

        # Resolve target_file: from rec if present, else category→file map
        set -l target_file (echo $rec | jq -r '.target_file // empty')
        if test -z "$target_file"
            set target_file (_adirective_target_file $rec_category $rules_dir)
        end
        if test -z "$target_file"
            echo "adirective: $rec_id — unknown category '$rec_category', no target file mapping (I401)"
            set n_skipped (math $n_skipped + 1)
            continue
        end

        if not test -f $target_file
            echo "adirective: $rec_id — target file missing: $target_file (I401)"
            set n_skipped (math $n_skipped + 1)
            continue
        end

        # Read tier metadata from target file frontmatter
        set -l tier (yq --front-matter=extract '.directive_tier' $target_file 2>/dev/null)
        set -l auto_propose (yq --front-matter=extract '.auto_propose' $target_file 2>/dev/null)
        set -l directive_class (yq --front-matter=extract '.directive_class' $target_file 2>/dev/null)
        set -l evidence_window (yq --front-matter=extract '.evidence_window_days' $target_file 2>/dev/null)

        # T3/T4: auto_propose=false — structural block (E401, visible error)
        if test "$auto_propose" = "false"
            echo "adirective: $rec_id — $target_file is $tier (blocked from auto-proposal) (E401)" >&2
            set n_blocked (math $n_blocked + 1)
            continue
        end

        # T4 blocklist path check (belt-and-suspenders vs frontmatter)
        for t4 in $t4_paths
            set -l t4_exp (string replace '~' $HOME $t4)
            set -l t4_prefix (string replace --regex '/\*\*?$' '' $t4_exp)
            if string match -q "$t4_prefix*" $target_file
                echo "adirective: $rec_id — $target_file matches T4 blocklist (E401)" >&2
                set n_blocked (math $n_blocked + 1)
                continue 2
            end
        end

        # Resolve tier threshold
        set -l tier_threshold
        switch $tier
            case T1
                set tier_threshold $t1_threshold
            case T2
                set tier_threshold $t2_threshold
            case '*'
                echo "adirective: $rec_id — unrecognized tier '$tier' (I401)"
                set n_skipped (math $n_skipped + 1)
                continue
        end

        # Confidence: use rec's .confidence field if present; else compute
        set -l confidence (echo $rec | jq -r '.confidence // empty')
        if test -z "$confidence"
            set -l baseline (echo $rec | jq -r '[.measurements[] | select(.type=="baseline")] | last | .value // 0')
            set -l current_val (echo $rec | jq -r '[.measurements[] | select(.type=="measurement")] | last | .value // 0')
            set -l improvement_bonus 0
            if test "$baseline" -gt 0 2>/dev/null
                # improvement = baseline decreased (anti-pattern reduction)
                set improvement_bonus (math "min(0.10, max(0, ($baseline - $current_val) / $baseline))")
            end
            set confidence (math "round(($tier_threshold + $improvement_bonus) * 1000) / 1000")
        end

        # Confidence floor check (I401)
        if test (math "floor($confidence * 1000)") -lt (math "floor($tier_threshold * 1000)")
            echo "adirective: $rec_id — confidence $confidence below $tier threshold $tier_threshold, skip (I401)"
            set n_skipped (math $n_skipped + 1)
            continue
        end

        # Evidence window check (soft warning, not blocking)
        if test -n "$rec_created" -a -n "$now_epoch" -a -n "$evidence_window" -a "$evidence_window" != "null"
            set -l created_epoch (date -j -f "%Y%m%dT%H%M%SZ" $rec_created +%s 2>/dev/null)
            if test -n "$created_epoch"
                set -l age_days (math "floor(($now_epoch - $created_epoch) / 86400)")
                if test $age_days -lt $evidence_window
                    echo "adirective: $rec_id — insufficient window ($age_days d < $evidence_window d required) (I404)"
                    set n_skipped (math $n_skipped + 1)
                    continue
                end
            end
        end

        # Current value for fingerprint
        set -l current_val (echo $rec | jq -r '[.measurements[] | select(.type=="measurement")] | last | .value // 0')
        set -l baseline (echo $rec | jq -r '[.measurements[] | select(.type=="baseline")] | last | .value // 0')

        # Fingerprint: SHA1(signal_id:target_file:current_val)
        set -l fingerprint (printf '%s:%s:%s' $rec_id $target_file $current_val | shasum -a 1 | cut -d' ' -f1)
        set -l proposal_file "$proposals_dir/$fingerprint.jsonl"

        # Snooze check (I403): rejected decision with future snooze_until
        if not $force
            set -l decision_file "$decisions_dir/$fingerprint.json"
            if test -f $decision_file
                set -l prev_decision (cat $decision_file)
                set -l prev_result (echo $prev_decision | jq -r '.decision')
                set -l snooze_until (echo $prev_decision | jq -r '.snooze_until // empty')
                if test "$prev_result" = "rejected" -a -n "$snooze_until"
                    set -l snooze_epoch (date -j -f "%Y%m%dT%H%M%SZ" $snooze_until +%s 2>/dev/null)
                    if test -n "$snooze_epoch" -a -n "$now_epoch" -a $now_epoch -lt $snooze_epoch
                        echo "adirective: $rec_id — snoozed until $snooze_until (I403)"
                        set n_skipped (math $n_skipped + 1)
                        continue
                    end
                end
            end
        end

        # Fingerprint dedup check (I402): pending proposal already exists
        if not $force; and test -f $proposal_file
            set -l existing_status (jq -r '.status' $proposal_file 2>/dev/null)
            if test "$existing_status" = "pending"
                echo "adirective: $rec_id — suppressed (pending fingerprint $(string sub -l 8 $fingerprint)) (I402)"
                set n_skipped (math $n_skipped + 1)
                continue
            end
        end

        # Build proposal payload
        set -l confidence_fmt (printf '%.3f' $confidence)
        set -l proposal (jq -cn \
            --arg timestamp $now_ts \
            --arg event_type "directive_proposal" \
            --arg signal_id $rec_id \
            --arg target_file $target_file \
            --arg tier $tier \
            --arg directive_class $directive_class \
            --argjson confidence $confidence_fmt \
            --arg title $rec_title \
            --arg category $rec_category \
            --argjson baseline $baseline \
            --argjson current_value $current_val \
            --arg fingerprint $fingerprint \
            --arg status "pending" \
            '{timestamp:$timestamp,event_type:$event_type,signal_id:$signal_id,
              target_file:$target_file,tier:$tier,directive_class:$directive_class,
              confidence:$confidence,title:$title,category:$category,
              baseline:$baseline,current_value:$current_value,
              fingerprint:$fingerprint,status:$status}')

        if $dry_run
            printf '[dry-run] %s → %s | confidence: %s | target: %s\n' \
                $rec_id $tier $confidence_fmt (string replace $HOME '~' $target_file)
        else
            if not echo $proposal > $proposal_file
                echo "adirective: failed to write proposal $fingerprint (E402)" >&2
                return 1
            end
            if functions -q emit_jsonl
                emit_jsonl --layer orchestration --event-type directive_proposal --command adirective \
                    --metadata-json (echo $proposal | jq -c '{signal_id,target_file,tier,directive_class,confidence,title,category,baseline,current_value,fingerprint}')
            end
            printf 'adirective: %s → %s proposal (confidence: %s) fingerprint: %s\n' \
                $rec_id $tier $confidence_fmt $fingerprint
        end

        set n_proposed (math $n_proposed + 1)

    end < $recs_file

    echo ""
    printf 'adirective: %d proposed | %d skipped | %d blocked (T3/T4)\n' \
        $n_proposed $n_skipped $n_blocked
end

function _adirective_target_file --argument-names category rules_dir
    switch $category
        case tool-antipattern
            echo "$rules_dir/tool-selection.md"
        case hook-context
            echo "$rules_dir/hook-context.md"
        case model-routing
            echo "$rules_dir/model-selection.md"
        case git-workflow
            echo "$rules_dir/git-workflow.md"
        case documentation
            echo "$rules_dir/documentation.md"
        case performance
            echo "$rules_dir/performance.md"
        case '*'
            echo ""
    end
end
