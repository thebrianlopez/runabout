# emit_jsonl.fish - Unified JSONL telemetry emitter (schema v2)
# Part of EPIC-002: Unified JSONL Telemetry Schema
# Created: 2026-02-28
# Updated: 20260319 (EPIC-022 M3b)  -  added --agent, --epic, --milestone flags
# Updated: 20260324 (EPIC-030 M2)   -  auto-populate agent= from CWD via _emit_agent_from_cwd
# Updated: 20260522 (PRD open items Q2)  -  added source_machine field (hostname) for multi-machine dedup pre-work
# Updated: 20260528 (EPIC-128 M3)  -  BSD/GNU stat compat; AUTOMATION_METRICS_SINK routing
#
# Usage:
#   emit_jsonl --layer <layer> --event-type <type> --command <cmd> \
#     [--session-id <id>] [--duration-ms <n>] [--exit-code <n>] \
#     [--metadata-json <json>] [--agent <name>] [--epic <id>] [--milestone <id>]
#
# Sink routing (AUTOMATION_METRICS_SINK):
#   unset   -> write to $AUTOMATION_METRICS_DIR/events/YYYY-MM-DD.jsonl (default)
#   stdout  -> write one JSON line to stdout; no file I/O
#   <other> -> stderr warning; fall back to file sink
#
# Agent attribution:
#   If --agent is not supplied, agent= is resolved automatically by looking up $PWD
#   against AGENT_CWD_MAP (conf.d/agent-cwd-map.fish). If no entry matches, agent=null.
#   Explicit --agent always wins; CWD lookup is a fallback only.
#
# Layers: cloud_llm, interactive_shell, fish, go_cli, go_lib, claude_code
# Output: ~/.automation-metrics/events/{YYYY-MM-DD}.jsonl
#
# Availability guard pattern for callers (hooks):
#   functions -q emit_jsonl || exit 0

function emit_jsonl --description "Unified JSONL telemetry emitter (schema v2)"
    argparse 'layer=' 'event-type=' 'command=' 'session-id=' 'duration-ms=' 'exit-code=' 'metadata-json=' 'agent=' 'epic=' 'milestone=' 'event-class=' -- $argv
    or return 0

    # Required fields
    if not set -q _flag_layer; or not set -q _flag_event_type; or not set -q _flag_command
        echo "emit_jsonl: --layer, --event-type, and --command are required" >&2
        return 0
    end

    # Resolve defaults
    set -l sid
    if set -q _flag_session_id
        set sid $_flag_session_id
    else if set -q __fish_session_id
        set sid $__fish_session_id
    else
        set sid unknown
    end

    set -l dur null
    if set -q _flag_duration_ms
        set dur $_flag_duration_ms
    end

    set -l ec null
    if set -q _flag_exit_code
        set ec $_flag_exit_code
    end

    set -l meta '{}'
    if set -q _flag_metadata_json
        set meta $_flag_metadata_json
    end

    # Orchestration fields (top-level for queryability without parsing metadata)
    # Agent: explicit flag wins; fall back to CWD lookup; null if unmanaged CWD.
    set -l agent_val null
    if set -q _flag_agent
        set agent_val "\"$_flag_agent\""
    else if functions -q _emit_agent_from_cwd
        set -l cwd_agent (_emit_agent_from_cwd $PWD)
        if test -n "$cwd_agent"
            set agent_val "\"$cwd_agent\""
        end
    end
    set -l epic_val null
    if set -q _flag_epic
        set epic_val "\"$_flag_epic\""
    end
    set -l milestone_val null
    if set -q _flag_milestone
        set milestone_val "\"$_flag_milestone\""
    end

    set -l event_class user_intent
    if set -q _flag_event_class
        set event_class $_flag_event_class
    end

    # Timestamp (UTC for the record, local date for file partitioning)
    # Use date directly  -  nowutc is a config.fish convenience not available in --no-config envs
    set -l ts (date -u +%Y-%m-%dT%H:%M:%SZ)
    set -l date_str (date +%Y-%m-%d)

    # Output path with fallback
    set -l metrics_dir ~/.automation-metrics
    if set -q AUTOMATION_METRICS_DIR
        set metrics_dir $AUTOMATION_METRICS_DIR
    end
    set -l events_dir $metrics_dir/events
    set -l output_file $events_dir/$date_str.jsonl

    # Directory bootstrap (cached via test -d)
    if not test -d $events_dir
        mkdir -p $events_dir 2>/dev/null
        or begin
            echo "emit_jsonl: cannot create $events_dir" >&2
            return 0
        end
    end

    # Log rotation guard (50MB = 52428800 bytes)
    # BSD stat: -f%z; GNU stat: -c%s; fallback: echo 0 (skip rotation)
    if test -f $output_file
        set -l fsize (command stat -f%z $output_file 2>/dev/null
            or command stat -c%s $output_file 2>/dev/null
            or echo 0)
        if test "$fsize" -gt 52428800
            set -l n 1
            while test -f $output_file.$n
                set n (math $n + 1)
            end
            command mv $output_file $output_file.$n 2>/dev/null
        end
    end

    # JSON-escape command (order matters: backslash first, then quotes, then control chars)
    set -l esc_cmd (string replace -a '\\' '\\\\' -- $_flag_command)
    set esc_cmd (string replace -a '"' '\\"' -- $esc_cmd)
    set esc_cmd (string join "\\n" -- $esc_cmd)
    set esc_cmd (string replace -a \t "\\t" -- $esc_cmd)
    set esc_cmd (string replace -a \r "\\r" -- $esc_cmd)

    # JSON-escape cwd
    set -l esc_cwd (string replace -a '\\' '\\\\' -- $PWD)
    set esc_cwd (string replace -a '"' '\\"' -- $esc_cwd)

    # Construct JSON record (single printf, all builtins, no subprocess for JSON)
    set -l source_machine (hostname 2>/dev/null; or echo unknown)
    set -l json (printf '{"schema_version":"2","timestamp":"%s","layer":"%s","event_type":"%s","event_class":"%s","command":"%s","session_id":"%s","user":"%s","cwd":"%s","source_machine":"%s","duration_ms":%s,"exit_code":%s,"agent":%s,"epic":%s,"milestone":%s,"metadata":%s}' \
        $ts $_flag_layer $_flag_event_type "$event_class" "$esc_cmd" $sid $USER "$esc_cwd" "$source_machine" $dur $ec $agent_val $epic_val $milestone_val $meta)

    # Sink routing: AUTOMATION_METRICS_SINK controls output destination
    if set -q AUTOMATION_METRICS_SINK
        switch "$AUTOMATION_METRICS_SINK"
            case stdout
                echo $json 2>/dev/null
                or true
                return 0
            case '*'
                echo "emit_jsonl: unknown AUTOMATION_METRICS_SINK='$AUTOMATION_METRICS_SINK'; falling back to file sink" >&2
        end
    end

    # Atomic write (EPIC-001 M1 fix for race condition)
    # When multiple events fire at the same millisecond, concurrent `echo >> file` appends
    # can interleave mid-line, producing malformed JSON. Solution: write to temp file,
    # then atomically append via single `cat` process (no interleaving).
    #
    # Each emitter gets its own temp file (PID + random), so no temp file collisions.
    # The `cat >> main` operation is fast (<1ms) and single-process, preventing races.
    set -l tmpfile "$output_file.tmp.$fish_pid.(random)"
    echo $json > $tmpfile 2>/dev/null
    or begin
        return 0
    end

    # Atomic append: single cat process cannot interleave with other callers
    cat $tmpfile >> $output_file 2>/dev/null
    command rm -f $tmpfile 2>/dev/null
    return 0
end
