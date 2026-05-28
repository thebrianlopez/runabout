function dispatch-scan-validate --description 'Scan a .claude-dispatch directory and validate trigger files'
    # Usage: dispatch-scan-validate <dispatch_dir>
    #
    # Output contract (one line per event):
    #   "trigger: <basename>"         valid trigger found
    #   "warning: <msg>"              schema issue — trigger skipped
    #   "WARNING: <msg>"              zero-match on non-empty dir (pattern mismatch)
    #   "No pending dispatches"       directory exists and is empty

    if test (count $argv) -lt 1
        echo "Usage: dispatch-scan-validate <dispatch_dir>" >&2
        return 1
    end

    set -l dir $argv[1]

    if not test -d "$dir"
        echo "dispatch-scan-validate: directory not found: $dir" >&2
        return 1
    end

    set -l md_files
    set -l json_files
    set -l other_files

    for f in $dir/*
        test -f "$f" || continue
        # Skip .claimed sentinels — they end in .claimed regardless of base extension
        string match -q -- "*.claimed" $f; and continue
        if string match -q -- "*.md" $f
            set -a md_files $f
        else if string match -q -- "*.json" $f
            set -a json_files $f
        else
            set -a other_files $f
        end
    end

    set -l trigger_count (math (count $md_files) + (count $json_files))
    set -l total_non_sentinel (math $trigger_count + (count $other_files))

    # Guard: directory has files but none are recognised trigger formats
    if test $trigger_count -eq 0; and test $total_non_sentinel -gt 0
        echo "WARNING: $dir contains files but no .md or .json triggers found — pattern mismatch likely"
    end

    # Validate .md triggers — schema_version is required
    for f in $md_files
        set -l fname (basename $f)
        if not grep -q '^schema_version:' $f 2>/dev/null
            echo "warning: $fname missing schema_version — skipping"
            continue
        end
        echo "trigger: $fname"
    end

    # Legacy .json triggers are accepted without schema validation
    for f in $json_files
        echo "trigger: "(basename $f)
    end

    if test $trigger_count -eq 0; and test $total_non_sentinel -eq 0
        echo "No pending dispatches"
    end
end
