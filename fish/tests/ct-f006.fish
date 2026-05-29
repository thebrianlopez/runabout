#!/usr/bin/env fish
# Contract Tests: F-006 CI Verification Matrix
# TDD: PERSONAL_20260529T033651Z_Runabout_XPlatform_F6_CI_Verification_Matrix_TDD.md
# Expected state: CT-1 through CT-5 FAILING (.github/workflows/verify.yml does not exist)

set -l SCRIPT_DIR (realpath (dirname (status --current-filename)))
set -l REPO_ROOT (realpath $SCRIPT_DIR/../..)
set -l VERIFY_YML $REPO_ROOT/.github/workflows/verify.yml

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

# CT-1: .github/workflows/verify.yml exists
echo "--- CT-1: .github/workflows/verify.yml exists"
if test -f $VERIFY_YML
    ct_pass CT-1 "verify.yml exists"
else
    ct_fail CT-1 "verify.yml does not exist at $VERIFY_YML"
end

# CT-2: verify.yml has strategy.matrix.include with 4 entries
echo "--- CT-2: verify.yml has 4-entry strategy.matrix.include"
if not test -f $VERIFY_YML
    ct_fail CT-2 "verify.yml does not exist"
else
    set -l include_count (grep -c '^\s*- name:' $VERIFY_YML 2>/dev/null)
    if test "$include_count" -ge 4
        ct_pass CT-2 "verify.yml has $include_count matrix include entries (>= 4)"
    else
        ct_fail CT-2 "expected >= 4 matrix include entries, found $include_count"
    end
end

# CT-3: verify.yml includes a macos-latest target
echo "--- CT-3: verify.yml includes macos-latest target"
if not test -f $VERIFY_YML
    ct_fail CT-3 "verify.yml does not exist"
else
    if grep -q 'macos-latest' $VERIFY_YML
        ct_pass CT-3 "verify.yml includes macos-latest target"
    else
        ct_fail CT-3 "verify.yml missing macos-latest target"
    end
end

# CT-4: verify.yml includes ubuntu dpkg target
echo "--- CT-4: verify.yml includes ubuntu dpkg target"
if not test -f $VERIFY_YML
    ct_fail CT-4 "verify.yml does not exist"
else
    if grep -q 'dpkg' $VERIFY_YML
        ct_pass CT-4 "verify.yml includes dpkg install target"
    else
        ct_fail CT-4 "verify.yml missing dpkg install target"
    end
end

# CT-5: verify.yml includes container target with emit_jsonl check
echo "--- CT-5: verify.yml includes container target with emit_jsonl check"
if not test -f $VERIFY_YML
    ct_fail CT-5 "verify.yml does not exist"
else
    if grep -q 'emit_jsonl' $VERIFY_YML; and grep -q 'container' $VERIFY_YML
        ct_pass CT-5 "verify.yml includes container target with emit_jsonl check"
    else
        ct_fail CT-5 "verify.yml missing container target or emit_jsonl check"
    end
end

# Summary
echo ""
echo "F-006 Contract Tests: $pass_count passed, $fail_count failed, $skip_count skipped"
test $fail_count -eq 0
