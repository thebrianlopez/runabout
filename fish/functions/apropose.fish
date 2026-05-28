# apropose.fish — Interactive directive proposal reviewer
# Part of EPIC-028 M3: Approval Event Schema + Rejection Memory
# Usage: apropose review [--all | --fingerprint <fp>]
#        apropose list
#        apropose stats

function apropose --description 'Review and decide on pending directive proposals'
    if test (count $argv) -eq 0
        _apropose_help
        return 0
    end

    switch $argv[1]
        case review
            _apropose_review $argv[2..]
        case list
            _apropose_list $argv[2..]
        case stats status
            _apropose_stats $argv[2..]
        case help --help -h
            _apropose_help
        case '*'
            echo "Unknown subcommand: $argv[1]" >&2
            echo "Run: apropose help" >&2
            return 1
    end
end

function _apropose_help
    echo "Usage: apropose <command> [options]"
    echo ""
    echo "Commands:"
    echo "  review   Interactively approve or reject pending proposals"
    echo "  list     List all proposals with status"
    echo "  stats    Show acceptance rate and decision summary"
    echo "  help     Show this help"
    echo ""
    echo "Options for review:"
    echo "  --all                  Review all pending proposals (default)"
    echo "  --fingerprint <fp>     Review a single proposal by fingerprint"
    echo ""
    echo "Examples:"
    echo "  apropose list"
    echo "  apropose review"
    echo "  apropose review --fingerprint 3cfe40276122cc521c3c6ea575b8839ec875a978"
    echo "  apropose stats"
end

function _apropose_list
    set -l proposals_dir ~/.automation-metrics/proposals
    if not test -d $proposals_dir
        echo "No proposals directory found: $proposals_dir"
        return 0
    end

    set -l proposal_files $proposals_dir/*.jsonl
    if test (count $proposal_files) -eq 0; or not test -e $proposal_files[1]
        echo "No proposals found."
        return 0
    end

    printf "%-10s %-10s %-8s %-6s %-8s %s\n" "FINGERPRINT" "SIGNAL" "TIER" "CONF" "STATUS" "TITLE"
    printf "%-10s %-10s %-8s %-6s %-8s %s\n" "----------" "------" "----" "----" "------" "-----"

    for f in $proposal_files
        set -l p (cat $f | head -1)
        set -l fp (echo $p | jq -r '.fingerprint' | cut -c1-10)
        set -l sig (echo $p | jq -r '.signal_id')
        set -l tier (echo $p | jq -r '.tier')
        set -l conf (echo $p | jq -r '.confidence' | xargs printf "%.2f")
        set -l pstatus (echo $p | jq -r '.status')
        set -l title (echo $p | jq -r '.title')
        printf "%-10s %-10s %-8s %-6s %-8s %s\n" $fp $sig $tier $conf $pstatus $title
    end
end

function _apropose_review
    set -l proposals_dir ~/.automation-metrics/proposals
    set -l events_dir ~/.automation-metrics/events
    set -l decisions_dir ~/.automation-metrics/decisions
    set -l filter_fp ""
    set -l now_ts ""
    set -l reviewer (whoami)

    set -l i 1
    while test $i -le (count $argv)
        switch $argv[$i]
            case --fingerprint -f
                set i (math $i + 1)
                set filter_fp $argv[$i]
            case --now
                set i (math $i + 1)
                set now_ts $argv[$i]
            case --proposals-dir
                set i (math $i + 1)
                set proposals_dir $argv[$i]
            case --decisions-dir
                set i (math $i + 1)
                set decisions_dir $argv[$i]
        end
        set i (math $i + 1)
    end

    if test -z "$now_ts"
        set now_ts (date -u +%Y%m%dT%H%M%SZ)
    end
    set -l today (date -j -f "%Y%m%dT%H%M%SZ" "$now_ts" +%Y-%m-%d 2>/dev/null)

    mkdir -p $decisions_dir

    if not test -d $proposals_dir
        echo "No proposals directory found."
        return 0
    end

    set -l proposal_files $proposals_dir/*.jsonl
    if test (count $proposal_files) -eq 0; or not test -e $proposal_files[1]
        echo "No proposals to review."
        return 0
    end

    set -l reviewed 0
    set -l skipped 0

    for f in $proposal_files
        set -l proposal (cat $f | head -1)
        set -l fp (echo $proposal | jq -r '.fingerprint')
        set -l pstatus (echo $proposal | jq -r '.status')

        # Filter by fingerprint if specified
        if test -n "$filter_fp"; and test "$fp" != "$filter_fp"
            continue
        end

        # Only review pending proposals
        if test "$pstatus" != "pending"
            set skipped (math $skipped + 1)
            continue
        end

        # Check rejection memory: is there an active snooze for this fingerprint?
        set -l snooze_active false
        set -l decision_file "$decisions_dir/$fp.json"
        if test -f $decision_file
            set -l prev_decision (cat $decision_file)
            set -l prev_result (echo $prev_decision | jq -r '.decision')
            set -l snooze_until (echo $prev_decision | jq -r '.snooze_until // empty')
            if test "$prev_result" = "rejected" -a -n "$snooze_until"
                set -l snooze_epoch (date -j -f "%Y%m%dT%H%M%SZ" $snooze_until +%s 2>/dev/null)
                set -l now_epoch (date -u +%s)
                if test -n "$snooze_epoch" -a $now_epoch -lt $snooze_epoch
                    set snooze_active true
                end
            end
        end

        if $snooze_active
            echo "⏭  $fp ($(echo $proposal | jq -r '.signal_id')): snoozed until $snooze_until — skipping"
            set skipped (math $skipped + 1)
            continue
        end

        # Display proposal for review
        set -l sig_id (echo $proposal | jq -r '.signal_id')
        set -l tier (echo $proposal | jq -r '.tier')
        set -l directive_class (echo $proposal | jq -r '.directive_class')
        set -l confidence (echo $proposal | jq -r '.confidence')
        set -l title (echo $proposal | jq -r '.title')
        set -l category (echo $proposal | jq -r '.category')
        set -l baseline (echo $proposal | jq -r '.baseline')
        set -l current_val (echo $proposal | jq -r '.current_value')
        set -l target_file (echo $proposal | jq -r '.target_file')
        set -l proposed_at (echo $proposal | jq -r '.proposed_at')

        echo ""
        echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
        printf "📋 Proposal: %s | %s (%s)\n" $sig_id $tier $directive_class
        printf "   Title:    %s\n" $title
        printf "   Category: %s\n" $category
        printf "   Target:   %s\n" (string replace $HOME '~' $target_file)
        printf "   Evidence: baseline=%s → current=%s (confidence: %.3f)\n" $baseline $current_val $confidence
        printf "   Proposed: %s\n" $proposed_at
        echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"

        # Show relevant section of the target rule file
        if test -f $target_file
            echo ""
            echo "📄 Current rule file excerpt (tool anti-pattern section):"
            grep -A 5 -B 1 "anti.pattern\|ls.*used\|grep.*used\|find.*used\|cat.*used" $target_file 2>/dev/null | head -20
            echo ""
        end

        # Prompt for decision
        echo "Decision: [a]pprove / [r]eject / [s]kip / [q]uit"
        read -l -P "→ " decision_input

        switch $decision_input
            case a approve
                # Approve: update proposal status, write decision event
                set -l updated (echo $proposal | jq -c '.status = "approved"')
                echo $updated > $f

                set -l decision_json (jq -cn \
                    --arg schema_version "2" \
                    --arg timestamp $now_ts \
                    --arg event_type "directive_decision" \
                    --arg layer "orchestration" \
                    --arg command "apropose review" \
                    --arg fingerprint $fp \
                    --arg signal_id $sig_id \
                    --arg target_file $target_file \
                    --arg decision "approved" \
                    --arg decided_by $reviewer \
                    --argjson snooze_until null \
                    '{schema_version: $schema_version, timestamp: $timestamp,
                      event_type: $event_type, layer: $layer, command: $command,
                      fingerprint: $fingerprint, signal_id: $signal_id,
                      target_file: $target_file, decision: $decision,
                      decided_by: $decided_by, snooze_until: $snooze_until}')

                echo $decision_json > $decisions_dir/$fp.json
                # EPIC-002 M5: use emit_jsonl for directive_decision events
                if functions -q emit_jsonl
                    emit_jsonl --layer orchestration --event-type directive_decision --command "apropose review" \
                        --metadata-json (jq -cn --arg fp $fp --arg sig $sig_id --arg tf $target_file --arg dec "approved" --arg by $reviewer \
                            '{fingerprint:$fp,signal_id:$sig,target_file:$tf,decision:$dec,decided_by:$by}')
                else
                    echo $decision_json >> $events_dir/$today.jsonl
                end

                echo "✅ Approved: $sig_id ($fp)"
                set reviewed (math $reviewed + 1)

            case r reject
                # Collect optional rejection note
                read -l -P "Rejection note (optional, Enter to skip): " rejection_note

                # Compute snooze_until: now_ts + 14 days
                set -l now_epoch_r (date -j -f "%Y%m%dT%H%M%SZ" "$now_ts" +%s 2>/dev/null)
                set -l snooze_epoch_r (math "$now_epoch_r + 14 * 86400")
                set -l snooze_until (date -j -f "%s" "$snooze_epoch_r" +%Y%m%dT%H%M%SZ 2>/dev/null)

                # Update proposal status
                set -l updated (echo $proposal | jq -c '.status = "rejected"')
                echo $updated > $f

                set -l note_json "null"
                if test -n "$rejection_note"
                    set note_json (echo -n "$rejection_note" | jq -Rs .)
                end

                set -l decision_json (jq -cn \
                    --arg schema_version "2" \
                    --arg timestamp $now_ts \
                    --arg event_type "directive_decision" \
                    --arg layer "orchestration" \
                    --arg command "apropose review" \
                    --arg fingerprint $fp \
                    --arg signal_id $sig_id \
                    --arg target_file $target_file \
                    --arg decision "rejected" \
                    --arg decided_by $reviewer \
                    --arg snooze_until $snooze_until \
                    --argjson note $note_json \
                    '{schema_version: $schema_version, timestamp: $timestamp,
                      event_type: $event_type, layer: $layer, command: $command,
                      fingerprint: $fingerprint, signal_id: $signal_id,
                      target_file: $target_file, decision: $decision,
                      decided_by: $decided_by, snooze_until: $snooze_until,
                      note: $note}')

                echo $decision_json > $decisions_dir/$fp.json
                # EPIC-002 M5: use emit_jsonl for directive_decision events
                if functions -q emit_jsonl
                    emit_jsonl --layer orchestration --event-type directive_decision --command "apropose review" \
                        --metadata-json (jq -cn --arg fp $fp --arg sig $sig_id --arg tf $target_file --arg dec "rejected" --arg by $reviewer --arg snooze $snooze_until --argjson n $note_json \
                            '{fingerprint:$fp,signal_id:$sig,target_file:$tf,decision:$dec,decided_by:$by,snooze_until:$snooze,note:$n}')
                else
                    echo $decision_json >> $events_dir/$today.jsonl
                end

                echo "🚫 Rejected: $sig_id ($fp) — snoozed until $snooze_until"
                set reviewed (math $reviewed + 1)

            case s skip ''
                echo "⏭  Skipped: $sig_id"
                set skipped (math $skipped + 1)

            case q quit
                echo "Exiting review."
                break

            case '*'
                echo "Unknown input '$decision_input' — skipping"
                set skipped (math $skipped + 1)
        end
    end

    echo ""
    printf "apropose review: %d decided | %d skipped\n" $reviewed $skipped
end

function _apropose_stats
    set -l proposals_dir ~/.automation-metrics/proposals
    set -l decisions_dir ~/.automation-metrics/decisions

    if not test -d $proposals_dir
        echo "No proposals found."
        return 0
    end

    set -l total 0
    set -l pending 0
    set -l approved 0
    set -l rejected 0

    set -l proposal_files $proposals_dir/*.jsonl
    if test -e $proposal_files[1]
        for f in $proposal_files
            set -l p (cat $f | head -1)
            set -l ps (echo $p | jq -r '.status')
            set total (math $total + 1)
            switch $ps
                case pending
                    set pending (math $pending + 1)
                case approved
                    set approved (math $approved + 1)
                case rejected
                    set rejected (math $rejected + 1)
            end
        end
    end

    set -l decided (math $approved + $rejected)
    set -l acceptance_rate "n/a"
    if test $decided -gt 0
        set acceptance_rate (math "round($approved / $decided * 100)")"%"
    end

    echo "## Directive Proposal Stats"
    echo ""
    printf "  Total proposals:    %d\n" $total
    printf "  Pending:            %d\n" $pending
    printf "  Approved:           %d\n" $approved
    printf "  Rejected:           %d\n" $rejected
    printf "  Acceptance rate:    %s\n" $acceptance_rate
    echo ""

    # Show active snoozes
    if test -d $decisions_dir
        set -l now_epoch (date -u +%s)
        set -l active_snoozes 0
        for df in $decisions_dir/*.json
            if not test -e $df
                continue
            end
            set -l d (cat $df)
            set -l dec (echo $d | jq -r '.decision')
            set -l snooze (echo $d | jq -r '.snooze_until // empty')
            if test "$dec" = "rejected" -a -n "$snooze"
                set -l snooze_epoch (date -j -f "%Y%m%dT%H%M%SZ" $snooze +%s 2>/dev/null)
                if test -n "$snooze_epoch" -a $now_epoch -lt $snooze_epoch
                    set active_snoozes (math $active_snoozes + 1)
                end
            end
        end
        printf "  Active snoozes:     %d\n" $active_snoozes
    end
end
