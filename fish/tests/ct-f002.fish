#!/usr/bin/env fish
# Contract Tests: F-002 emit_jsonl Platform Portability
# TDD: PERSONAL_20260527T161230Z_Runabout_XPlatform_F2_EmitJsonl_Portability_TDD.md
# Expected state: FAILING - no implementation yet (fish/functions/emit_jsonl.fish does not exist)

set -l SCRIPT_DIR (realpath (dirname (status --current-filename)))
set -l REPO_ROOT (realpath $SCRIPT_DIR/../..)
set -l FUNCS_DIR $REPO_ROOT/fish/functions
set -l EMIT_JSONL $FUNCS_DIR/emit_jsonl.fish

set -g pass_count 0
set -g fail_count 0
set -g skip_count 0

function ct_pass -a id msg
    set -g pass_count (math $pass_count + 1)
    echo "PASS $id: $msg"
end

function ct_fail -a id msg
    set -g fail_count (math $fail_count + 1)
    echo "FAIL $id: $msg"
end

function ct_skip -a id msg
    set -g skip_count (math $skip_count + 1)
    echo "SKIP $id: $msg"
end

# CT-1: BSD stat fallback on Linux (Docker Ubuntu CI only)
echo "--- CT-1: BSD stat fallback - emit_jsonl computes file size via stat -c%s on Linux"
if test (uname) = Darwin
    ct_skip CT-1 "GNU coreutils required - Docker Ubuntu 22.04 CI only"
else if not test -f $EMIT_JSONL
    ct_fail CT-1 "fish/functions/emit_jsonl.fish does not exist"
else
    set -l tmpdir (mktemp -d)
    set -l testfile $tmpdir/test.jsonl
    printf '{"test": "line"}\n' > $testfile
    set -l result (fish --no-config -c "
        set -p fish_function_path $FUNCS_DIR
        set -gx AUTOMATION_METRICS_DIR $tmpdir
        set -e AUTOMATION_METRICS_SINK
        emit_jsonl --layer fish --event-type ct1-stat-test --command test
        echo exit:\$status
    " 2>&1)
    set -l event_count (count $tmpdir/events/*.jsonl 2>/dev/null)
    rm -rf $tmpdir
    if string match -q '*exit:0*' -- $result; and test "$event_count" -gt 0
        ct_pass CT-1 "emit_jsonl writes event using stat -c%s on Linux"
    else
        ct_fail CT-1 "expected exit:0 + event file, got: '$result' (event_count=$event_count)"
    end
end

# CT-2: AUTOMATION_METRICS_SINK=stdout writes one valid JSON object to stdout
echo "--- CT-2: AUTOMATION_METRICS_SINK=stdout writes one valid JSON line to stdout"
if not test -f $EMIT_JSONL
    ct_fail CT-2 "fish/functions/emit_jsonl.fish does not exist"
else
    set -l tmpdir (mktemp -d)
    set -l stdout_output (fish --no-config -c "
        set -p fish_function_path $FUNCS_DIR
        set -gx AUTOMATION_METRICS_SINK stdout
        set -gx AUTOMATION_METRICS_DIR $tmpdir
        emit_jsonl --layer fish --event-type ct2-test --command test
    " 2>/dev/null)
    rm -rf $tmpdir
    if echo $stdout_output | python3 -m json.tool > /dev/null 2>&1
        ct_pass CT-2 "AUTOMATION_METRICS_SINK=stdout writes valid JSON to stdout"
    else
        ct_fail CT-2 "expected valid JSON on stdout, got: '$stdout_output'"
    end
end

# CT-3: AUTOMATION_METRICS_SINK=stdout creates no file in AUTOMATION_METRICS_DIR
echo "--- CT-3: AUTOMATION_METRICS_SINK=stdout creates no file in AUTOMATION_METRICS_DIR"
if not test -f $EMIT_JSONL
    ct_fail CT-3 "fish/functions/emit_jsonl.fish does not exist"
else
    set -l tmpdir (mktemp -d)
    fish --no-config -c "
        set -p fish_function_path $FUNCS_DIR
        set -gx AUTOMATION_METRICS_SINK stdout
        set -gx AUTOMATION_METRICS_DIR $tmpdir
        emit_jsonl --layer fish --event-type ct3-test --command test
    " > /dev/null 2>&1
    set -l file_count (count $tmpdir/events/*.jsonl 2>/dev/null)
    rm -rf $tmpdir
    if test "$file_count" -eq 0
        ct_pass CT-3 "AUTOMATION_METRICS_SINK=stdout creates no event file"
    else
        ct_fail CT-3 "expected 0 event files, found $file_count"
    end
end

# CT-4: AUTOMATION_METRICS_SINK unset - events write to file as before
echo "--- CT-4: AUTOMATION_METRICS_SINK unset - events write to AUTOMATION_METRICS_DIR/events/"
if not test -f $EMIT_JSONL
    ct_fail CT-4 "fish/functions/emit_jsonl.fish does not exist"
else
    set -l tmpdir (mktemp -d)
    fish --no-config -c "
        set -p fish_function_path $FUNCS_DIR
        set -e AUTOMATION_METRICS_SINK
        set -gx AUTOMATION_METRICS_DIR $tmpdir
        emit_jsonl --layer fish --event-type ct4-test --command test
    " > /dev/null 2>&1
    set -l file_count (count $tmpdir/events/*.jsonl 2>/dev/null)
    rm -rf $tmpdir
    if test "$file_count" -gt 0
        ct_pass CT-4 "AUTOMATION_METRICS_SINK unset writes event to file"
    else
        ct_fail CT-4 "expected event file in AUTOMATION_METRICS_DIR/events/, found none"
    end
end

# CT-5: emit_jsonl always returns 0 even when stdout is broken
# /dev/full is a Linux-only always-ENOSPC block device
echo "--- CT-5: emit_jsonl returns exit 0 when stdout write fails (ENOSPC)"
if not test -e /dev/full
    ct_skip CT-5 "/dev/full not available - Linux only"
else if not test -f $EMIT_JSONL
    ct_fail CT-5 "fish/functions/emit_jsonl.fish does not exist"
else
    # Redirect ONLY emit_jsonl's stdout to /dev/full, and record its status to a
    # file. The previous form redirected the whole harness - including its own
    # trailing `echo $status` - so that echo hit ENOSPC, the outer fish exited 1,
    # and the assertion measured the harness rather than emit_jsonl. It passed
    # locally only because macOS has no /dev/full and the case was skipped.
    set -l tmpdir (mktemp -d)
    set -l statusfile (mktemp)
    fish --no-config -c "
        set -p fish_function_path $FUNCS_DIR
        set -gx AUTOMATION_METRICS_SINK stdout
        set -gx AUTOMATION_METRICS_DIR $tmpdir
        emit_jsonl --layer fish --event-type ct5-test --command test > /dev/full 2>/dev/null
        echo \$status > $statusfile
    " 2>/dev/null
    set -l exit_code (cat $statusfile 2>/dev/null; or echo missing)
    rm -rf $tmpdir $statusfile
    if test "$exit_code" = 0
        ct_pass CT-5 "emit_jsonl returns 0 when stdout write fails"
    else
        ct_fail CT-5 "expected exit 0, got: $exit_code"
    end
end

# CT-6: invalid AUTOMATION_METRICS_SINK value falls back to file sink with stderr warning
echo "--- CT-6: invalid AUTOMATION_METRICS_SINK falls back to file with stderr warning"
if not test -f $EMIT_JSONL
    ct_fail CT-6 "fish/functions/emit_jsonl.fish does not exist"
else
    set -l tmpdir (mktemp -d)
    set -l stderr_out (fish --no-config -c "
        set -p fish_function_path $FUNCS_DIR
        set -gx AUTOMATION_METRICS_SINK invalid
        set -gx AUTOMATION_METRICS_DIR $tmpdir
        emit_jsonl --layer fish --event-type ct6-test --command test
    " 2>&1 >/dev/null)
    set -l file_count (count $tmpdir/events/*.jsonl 2>/dev/null)
    rm -rf $tmpdir
    if string match -q '*AUTOMATION_METRICS_SINK*' -- $stderr_out; and test "$file_count" -gt 0
        ct_pass CT-6 "invalid sink emits warning to stderr and falls back to file"
    else
        ct_fail CT-6 "expected stderr warning + event file; stderr='$stderr_out' file_count=$file_count"
    end
end

# CT-7: rotation guard disabled gracefully when stat is absent
# Simulates missing stat by prepending a fake stat (always returns 1) to PATH
echo "--- CT-7: emit_jsonl writes event without error when stat binary is absent"
if not test -f $EMIT_JSONL
    ct_fail CT-7 "fish/functions/emit_jsonl.fish does not exist"
else
    set -l tmppath (mktemp -d)
    printf '#!/bin/sh\nexit 1\n' > $tmppath/stat
    chmod +x $tmppath/stat
    set -l path_str (string join : $PATH)
    set -l tmpdir (mktemp -d)
    set -l result (env PATH="$tmppath:$path_str" fish --no-config -c "
        set -p fish_function_path $FUNCS_DIR
        set -e AUTOMATION_METRICS_SINK
        set -gx AUTOMATION_METRICS_DIR $tmpdir
        emit_jsonl --layer fish --event-type ct7-test --command test
        echo exit:\$status
    " 2>&1)
    set -l file_count (count $tmpdir/events/*.jsonl 2>/dev/null)
    rm -rf $tmppath $tmpdir
    if string match -q '*exit:0*' -- $result; and test "$file_count" -gt 0
        ct_pass CT-7 "emit_jsonl writes event and exits 0 with stat absent (rotation guard skipped)"
    else
        ct_fail CT-7 "expected exit:0 + event file; got: '$result' file_count=$file_count"
    end
end

# Summary
echo ""
echo "F-002 Contract Tests: $pass_count passed, $fail_count failed, $skip_count skipped"
test $fail_count -eq 0
