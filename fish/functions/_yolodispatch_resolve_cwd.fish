# org.yaml primary → write-through cache → JSONL fallback → miss event
# EPIC-089 F4: migrates from JSONL-first to org.yaml-first resolution
# Test overrides: _DISPATCH_REGISTRY_FILE (JSONL path), AUTOMATION_METRICS_DIR (events dir)
function _yolodispatch_resolve_cwd --description 'Resolve agent CWD from org.yaml (primary) with JSONL cache/fallback'
    set -l target $argv[1]
    test -z "$target"; and return 1

    # Try exact name and with -agent suffix appended
    set -l candidates $target
    string match -q '*-agent' $target; or set -a candidates "$target-agent"

    set -q _DISPATCH_REGISTRY_FILE
        and set -l registry_file $_DISPATCH_REGISTRY_FILE
        or set -l registry_file $HOME/.automation-metrics/dispatch-registry.jsonl

    # Primary path: org.yaml resolution via _registry_resolve_cwd (EPIC-088 F3)
    if functions -q _registry_resolve_cwd
        for candidate in $candidates
            set -l resolved_cwd (_registry_resolve_cwd $candidate)
            set -l rc $status
            if test $rc -eq 0; and test -n "$resolved_cwd"
                # Write-through cache: keep JSONL warm for fallback
                set -l new_entry (printf '{"agent":"%s","cwd":"%s","dispatch_dir":".claude-dispatch","dispatch_fn":"yolodispatch","last_dispatched":null}' \
                    $candidate $resolved_cwd)
                if test -f "$registry_file"
                    set -l tmpfile "$registry_file.tmp.$fish_pid"
                    jq -c --arg a "$candidate" 'select(.agent != $a)' "$registry_file" > $tmpfile 2>/dev/null
                    echo $new_entry >> $tmpfile
                    command mv "$tmpfile" "$registry_file"
                else
                    mkdir -p (dirname "$registry_file") 2>/dev/null
                    echo $new_entry >> $registry_file
                end
                echo $resolved_cwd
                return 0
            end
        end
    end

    # Fallback: dispatch-registry.jsonl (org.yaml unreadable, agent missing, or idle)
    if test -f "$registry_file"
        for candidate in $candidates
            set -l cwd (jq -r --arg a "$candidate" 'select(.agent == $a) | .cwd' "$registry_file" 2>/dev/null | tail -1)
            if test -n "$cwd"
                echo $cwd
                return 0
            end
        end
    end

    # Total miss — emit event
    functions -q emit_jsonl; and emit_jsonl \
        --layer fish \
        --event-type dispatch_registry_miss \
        --command "_yolodispatch_resolve_cwd $target" \
        --agent fish-config-agent \
        --metadata-json (printf '{"agent_id":"%s"}' $target)

    return 1
end
