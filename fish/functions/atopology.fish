# atopology.fish - Topology relationship event consumer + registry
# Part of EPIC-018: Topology Relationship Events
# Captures edges between automation entities: graduation, regression, calls, bypasses, lineage
#
# Source of truth (after EPIC-031 M3/M4):
#   JSONL bus (events/*.jsonl, layer=topology) — authoritative for graduation_candidate, regression_candidate
#   topology-edges.jsonl — human-declared edges only (evolves_from, bypasses, doc_debt, etc.)
#                          also serves as a derived cache of the last agrad/aregress run snapshot
#
# topology-edges.jsonl is a DERIVED CACHE, not the source of truth for graduation/regression candidates.
# atopology status reads graduation/regression candidates exclusively from the bus.

function atopology --description "Topology relationship graph consumer"
    set -l metrics_dir ~/.automation-metrics
    if set -q AUTOMATION_METRICS_DIR
        set metrics_dir $AUTOMATION_METRICS_DIR
    end
    set -l registry "$metrics_dir/topology-edges.jsonl"

    if test (count $argv) -eq 0
        _atopology_help
        return 0
    end

    switch $argv[1]
        case status
            _atopology_status $metrics_dir $registry $argv[2..]
        case declare
            _atopology_declare $registry $argv[2..]
        case edges
            _atopology_edges $registry
        case seed
            _atopology_seed $registry
        case help --help -h
            _atopology_help
        case '*'
            echo "Unknown subcommand: $argv[1]" >&2
            return 1
    end
end

function _atopology_help
    echo "Usage: atopology <command> [options]"
    echo ""
    echo "Commands:"
    echo "  status     Reconstruct relationship graph from events + registry (last 30d)"
    echo "  declare    Declare a relationship (append to registry + emit event)"
    echo "  edges      List all registry entries"
    echo "  seed       Seed registry with known evolves_from relationships"
    echo "  help       Show this help"
    echo ""
    echo "Examples:"
    echo "  atopology seed                           # Load known lineage data"
    echo "  atopology status                         # Show relationship graph"
    echo "  atopology edges                          # List registry entries"
    echo "  atopology declare --rel-type calls \\"
    echo "    --source deploy_service --source-layer fish \\"
    echo "    --target kubectl --target-layer go_cli"
end

function _atopology_declare --argument-names registry
    set -l argv_rest $argv[2..]
    set -l rel_type ""
    set -l source ""
    set -l source_layer ""
    set -l target ""
    set -l target_layer ""
    set -l note ""

    set -l i 1
    while test $i -le (count $argv_rest)
        switch $argv_rest[$i]
            case --rel-type -r
                set i (math $i + 1)
                set rel_type $argv_rest[$i]
            case --source -s
                set i (math $i + 1)
                set source $argv_rest[$i]
            case --source-layer
                set i (math $i + 1)
                set source_layer $argv_rest[$i]
            case --target -t
                set i (math $i + 1)
                set target $argv_rest[$i]
            case --target-layer
                set i (math $i + 1)
                set target_layer $argv_rest[$i]
            case --note -n
                set i (math $i + 1)
                set note $argv_rest[$i]
        end
        set i (math $i + 1)
    end

    if test -z "$rel_type" -o -z "$source"
        echo "Error: --rel-type and --source are required" >&2
        return 1
    end

    set -l timestamp (nowutc)
    set -l note_json (echo -n "$note" | jq -Rs .)

    # Append to registry
    mkdir -p (dirname "$registry")
    set -l record (jq -cn \
        --arg rt "$rel_type" --arg s "$source" --arg sl "$source_layer" \
        --arg t "$target" --arg tl "$target_layer" --arg n "$note" \
        --arg ts "$timestamp" \
        '{rel_type:$rt, source:$s, source_layer:$sl, target:$t, target_layer:$tl, note:$n, declared_at:$ts}')
    echo "$record" >> "$registry"

    # Emit topology event
    if functions -q emit_jsonl
        set -l meta (jq -cn \
            --arg rt "$rel_type" --arg s "$source" --arg sl "$source_layer" \
            --arg t "$target" --arg tl "$target_layer" --arg n "$note" \
            '{rel_type:$rt, source:$s, source_layer:$sl, target:$t, target_layer:$tl, evidence:{declared:"manual", note:$n}}')
        emit_jsonl --layer topology --event-type relationship --command "atopology declare" \
            --metadata-json "$meta"
    end

    echo "Declared: $source ($source_layer) --[$rel_type]--> $target ($target_layer)"
    if test -n "$note"
        echo "  Note: $note"
    end
end

function _atopology_edges --argument-names registry
    if not test -f "$registry"
        echo "No topology edges registered."
        echo "Run: atopology seed"
        return 0
    end

    echo "## Topology Edge Registry"
    echo ""
    printf "%-20s %-15s %-12s %-20s %-12s  %s\n" "Source" "Rel Type" "Src Layer" "Target" "Tgt Layer" "Note"
    printf "%-20s %-15s %-12s %-20s %-12s  %s\n" "--------------------" "---------------" "------------" "--------------------" "------------" "--------------------"

    while read -l line
        set -l src (echo "$line" | jq -r '.source')
        set -l rt (echo "$line" | jq -r '.rel_type')
        set -l sl (echo "$line" | jq -r '.source_layer // ""')
        set -l tgt (echo "$line" | jq -r '.target // ""')
        set -l tl (echo "$line" | jq -r '.target_layer // ""')
        set -l note (echo "$line" | jq -r '.note // ""')
        printf "%-20s %-15s %-12s %-20s %-12s  %s\n" "$src" "$rt" "$sl" "$tgt" "$tl" "$note"
    end < "$registry"

    echo ""
    set -l count (wc -l < "$registry" | string trim)
    echo "Total: $count registered edges"
end

function _atopology_seed --argument-names registry
    echo "Seeding topology registry with known evolves_from relationships..."

    mkdir -p (dirname "$registry")

    # 1. standups: crystallized from manual fd searches
    _atopology_declare "$registry" \
        --rel-type evolves_from --source standups --source-layer fish \
        --target "fd .* docs/standups (9x daily)" --target-layer cloud_llm \
        --note "Crystallized from 9x daily fd invocations"

    # 2. tfw: crystallized from 5-command terraform chain
    _atopology_declare "$registry" \
        --rel-type evolves_from --source tfw --source-layer fish \
        --target "5-command terraform chain" --target-layer cloud_llm \
        --note "init+fmt+validate+plan collapsed into single function"

    # 3. devdocs: crystallized from 3-step manual workflow
    _atopology_declare "$registry" \
        --rel-type evolves_from --source devdocs --source-layer fish \
        --target "cd dev-docs && git-init-from-cache && gsync" --target-layer cloud_llm \
        --note "3-step manual workflow → single command"

    # 4. envcheck: crystallized from manual env var inspection
    _atopology_declare "$registry" \
        --rel-type evolves_from --source envcheck --source-layer fish \
        --target "manual env var inspection" --target-layer cloud_llm \
        --note "Manual inspection → masked status with filters"

    # 5. mdq: graduated from fish md-tree loops
    _atopology_declare "$registry" \
        --rel-type evolves_from --source mdq --source-layer go_cli \
        --target "md-tree extract loops" --target-layer fish \
        --note "Fish loops over md-tree → structured Go CLI with typed flags"

    echo ""
    echo "Seeded 5 evolves_from relationships. Run: atopology status"
end

function _atopology_status --argument-names metrics_dir registry
    # Parse optional --format and --registry-file from remaining argv
    set -l format table
    set -l registry_override ""
    set -l i 3
    while test $i -le (count $argv)
        switch $argv[$i]
            case --format
                set i (math $i + 1)
                set format $argv[$i]
            case --registry-file
                set i (math $i + 1)
                set registry_override $argv[$i]
        end
        set i (math $i + 1)
    end
    if test -n "$registry_override"
        set registry $registry_override
    end

    set -l events_dir $metrics_dir/events

    echo "## Topology Relationship Graph"
    echo ""

    # Collect edges from two sources:
    # 1. Registry (human-declared, always included)
    # 2. Events stream (auto-detected, last 30d, layer="topology")

    set -l tmp_edges (mktemp /tmp/atopology-edges-XXXXXX.jsonl)

    # Source 1: Registry edges — human-curated, always take precedence on conflict.
    # graduation_candidate and regression_candidate are excluded; bus is authoritative for those.
    if test -f "$registry"
        while read -l line
            set -l rt (echo "$line" | jq -r '.rel_type')
            if test "$rt" = "graduation_candidate" -o "$rt" = "regression_candidate"
                continue
            end
            set -l src (echo "$line" | jq -r '.source')
            set -l sl (echo "$line" | jq -r '.source_layer // ""')
            set -l tgt (echo "$line" | jq -r '.target // ""')
            set -l tl (echo "$line" | jq -r '.target_layer // ""')
            set -l ts (echo "$line" | jq -r '.declared_at // ""')
            echo (jq -cn --arg rt "$rt" --arg s "$src" --arg sl "$sl" \
                --arg t "$tgt" --arg tl "$tl" --arg ts "$ts" --arg origin "human_curated" \
                '{rel_type:$rt, source:$s, source_layer:$sl, target:$t, target_layer:$tl, timestamp:$ts, origin:$origin}') >> $tmp_edges
        end < "$registry"
    end

    # Source 2: Events stream (last 30d, topology layer) — bus_event origin
    if test -d "$events_dir"
        set -l cutoff (daysago 30)
        set -l files $events_dir/*.jsonl
        if test (count $files) -gt 0; and test -f "$files[1]"
            jq -r --arg cutoff "$cutoff" '
                select(.layer == "topology" and .event_type == "relationship" and .timestamp >= $cutoff) |
                {rel_type: .metadata.rel_type, source: .metadata.source, source_layer: .metadata.source_layer,
                 target: (.metadata.target // ""), target_layer: (.metadata.target_layer // ""),
                 timestamp: .timestamp, origin: "bus_event"}
            ' $files 2>/dev/null >> $tmp_edges
        end
    end

    if not test -s "$tmp_edges"
        echo "(No topology edges found. Run: atopology seed)"
        rm -f $tmp_edges
        return 0
    end

    # Deduplicate: group by (rel_type, source, target).
    # Human-curated edges always win over bus_event on conflict.
    set -l deduped (jq -rs '
        group_by([.rel_type, .source, .target]) |
        map(
            if any(.origin == "human_curated") then
                map(select(.origin == "human_curated")) | sort_by(.timestamp) | last
            else
                sort_by(.timestamp) | last
            end
        ) |
        sort_by(.rel_type) |
        .[] |
        "\(.rel_type)\t\(.source)\t\(.source_layer)\t\(.target)\t\(.target_layer)\t\(.timestamp)\t\(.origin)"
    ' $tmp_edges 2>/dev/null)

    # Group by rel_type for display
    set -l current_type ""
    set -l edge_count 0

    for line in $deduped
        set -l parts (string split \t -- $line)
        if test (count $parts) -lt 7
            continue
        end
        set -l rt $parts[1]
        set -l src $parts[2]
        set -l sl $parts[3]
        set -l tgt $parts[4]
        set -l tl $parts[5]
        set -l ts $parts[6]
        set -l origin $parts[7]

        # Section header when rel_type changes
        if test "$rt" != "$current_type"
            if test -n "$current_type"
                echo ""
            end
            echo "### $rt"
            echo ""
            printf "  %-20s %-12s %-25s %-12s  %-8s %s\n" "Source" "Src Layer" "Target" "Tgt Layer" "Origin" "Timestamp"
            printf "  %-20s %-12s %-25s %-12s  %-8s %s\n" "--------------------" "------------" "-------------------------" "------------" "--------" "--------------------"
            set current_type $rt
        end

        printf "  %-20s %-12s %-25s %-12s  %-8s %s\n" "$src" "$sl" "$tgt" "$tl" "$origin" "$ts"
        set edge_count (math $edge_count + 1)
    end

    echo ""
    echo "Total edges: $edge_count (deduplicated by rel_type + source + target, latest timestamp wins)"

    rm -f $tmp_edges
end
