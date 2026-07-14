#!/usr/bin/env fish
# Contract Tests: F-036 Termux Native Package Repository Support
# TDD: PERSONAL_20260714T212000Z_Runabout_Termux_F1_Package_Prefix_TDD.md
#      PERSONAL_20260714T212100Z_Runabout_Termux_F2_Repo_Hosting_Metadata_TDD.md
# Expected state: package contract files present and Termux prefix contract enforced.

set -l SCRIPT_DIR (realpath (dirname (status --current-filename)))
set -l REPO_ROOT (realpath $SCRIPT_DIR/../..)
set -l TERMUX_NFPM $REPO_ROOT/nfpm.termux.yaml

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

# CT-1: nfpm.termux.yaml exists
if test -f $TERMUX_NFPM
    ct_pass CT-1 "nfpm.termux.yaml exists"
else
    ct_fail CT-1 "nfpm.termux.yaml does not exist at $TERMUX_NFPM"
end

# CT-2: package name is runabout
if test -f $TERMUX_NFPM; and grep -q '^name: runabout$' $TERMUX_NFPM
    ct_pass CT-2 "Termux package name is runabout"
else if test -f $TERMUX_NFPM
    ct_fail CT-2 "Termux package missing name: runabout"
end

# CT-3: prefix contract targets the Termux prefix, not /usr
if test -f $TERMUX_NFPM; and grep -qF '/data/data/com.termux/files/usr/bin/' $TERMUX_NFPM
    ct_pass CT-3 "Termux package targets the Termux prefix"
else if test -f $TERMUX_NFPM
    ct_fail CT-3 "Termux package missing /data/data/com.termux/files/usr/bin paths"
end

# CT-4: no /usr/local payload paths remain in the Termux package config
if test -f $TERMUX_NFPM; and not grep -q '/usr/local/' $TERMUX_NFPM
    ct_pass CT-4 "Termux package avoids /usr/local payload paths"
else if test -f $TERMUX_NFPM
    ct_fail CT-4 "Termux package still contains /usr/local paths"
end

# CT-5: arch is arm64 for the Termux package lane
if test -f $TERMUX_NFPM; and grep -q '^arch: arm64$' $TERMUX_NFPM
    ct_pass CT-5 "Termux package arch is arm64"
else if test -f $TERMUX_NFPM
    ct_fail CT-5 "Termux package missing arch: arm64"
end

# CT-6: package includes the runabout CLI suite binaries (spot check)
set -l required_bins mdq perfgate shellprof hookval effiscore castex chain-eval bmux protonexport linkari linkari-labeler plaid-service wasend workctl ghwatch jira-poller runway
set -l missing_bins
for bin in $required_bins
    if not grep -qF "dist/bin/$bin" $TERMUX_NFPM
        set missing_bins $missing_bins $bin
    end
end
if test (count $missing_bins) -eq 0
    ct_pass CT-6 "Termux package references the full CLI suite"
else
    ct_fail CT-6 "Missing binaries in Termux package: $missing_bins"
end

# Summary
echo ""
echo "F-036 Contract Tests: $pass_count passed, $fail_count failed, $skip_count skipped"
test $fail_count -eq 0
