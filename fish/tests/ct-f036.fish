#!/usr/bin/env fish
# Contract Tests: F-036 Termux Native Package Repository Support
# TDD: PERSONAL_20260714T212000Z_Runabout_Termux_F1_Package_Prefix_TDD.md
#      PERSONAL_20260714T212100Z_Runabout_Termux_F2_Repo_Hosting_Metadata_TDD.md
# Expected state: package contract files present and Termux prefix contract enforced.

set -l SCRIPT_DIR (realpath (dirname (status --current-filename)))
set -l REPO_ROOT (realpath $SCRIPT_DIR/../..)
set -l TERMUX_NFPM $REPO_ROOT/nfpm.termux.yaml
set -l TERMUX_SMOKE_SCRIPT $REPO_ROOT/scripts/termux-smoke.sh
set -l RELEASE_WORKFLOW $REPO_ROOT/.github/workflows/release.yml

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

# CT-5: arch is aarch64 for the Termux package lane
if test -f $TERMUX_NFPM; and grep -q '^arch: aarch64$' $TERMUX_NFPM
    ct_pass CT-5 "Termux package arch is aarch64"
else if test -f $TERMUX_NFPM
    ct_fail CT-5 "Termux package missing arch: aarch64"
end

# CT-6: package includes the published runabout CLI suite binaries (spot check)
# EPIC-257: bmux, protonexport, plaid-service and wasend are unpublished and must
# NOT appear here - see CT-6b. Keep this list in sync with Makefile PUBLISHED and
# the .goreleaser.yaml build ids.
set -l required_bins mdq perfgate shellprof hookval effiscore castex chain-eval linkari linkari-labeler workctl ghwatch jira-poller runway
set -l missing_bins
for bin in $required_bins
    if not grep -qF "dist/bin/$bin" $TERMUX_NFPM
        set missing_bins $missing_bins $bin
    end
end
if test (count $missing_bins) -eq 0
    ct_pass CT-6 "Termux package references the published CLI suite"
else
    ct_fail CT-6 "Missing binaries in Termux package: $missing_bins"
end

# CT-6b (EPIC-257): unpublished tools must not be referenced by the Termux
# manifest. goreleaser no longer builds them, so dist/bin/<tool> never exists and
# nfpm fails the termux-repo job at release time. This guards the third surface -
# Makefile and .goreleaser.yaml are the other two.
set -l unpublished_bins bmux protonexport plaid-service wasend
set -l leaked_bins
for bin in $unpublished_bins
    if grep -qF "dist/bin/$bin" $TERMUX_NFPM
        set leaked_bins $leaked_bins $bin
    end
end
if test (count $leaked_bins) -eq 0
    ct_pass CT-6b "Termux package excludes unpublished tools"
else
    ct_fail CT-6b "Unpublished tools present in Termux package: $leaked_bins"
end

# CT-7: smoke harness exists and exercises repo add/update/install/bin execution
if test -f $TERMUX_SMOKE_SCRIPT; and grep -qF 'apt-get "${apt_opts[@]}" update' $TERMUX_SMOKE_SCRIPT; and grep -qF 'apt-get "${apt_opts[@]}" -y install "$pkg_name"' $TERMUX_SMOKE_SCRIPT; and grep -qF 'DPkg::Options::="--force-architecture"' $TERMUX_SMOKE_SCRIPT; and grep -qF '"$installed_bin" --version' $TERMUX_SMOKE_SCRIPT
    ct_pass CT-7 "Termux smoke harness performs add/update/install/binary checks"
else if test -f $TERMUX_SMOKE_SCRIPT
    ct_fail CT-7 "Termux smoke harness missing repo/install/binary execution checks"
end

# CT-8: release workflow publishes and smokes the Termux lane without masking failures
if test -f $RELEASE_WORKFLOW; and grep -qF 'termux-repo:' $RELEASE_WORKFLOW; and grep -qF 'concurrency:' $RELEASE_WORKFLOW; and grep -qF 'timeout-minutes: 30' $RELEASE_WORKFLOW; and grep -qF 'termux-smoke-test:' $RELEASE_WORKFLOW; and grep -qF 'runs-on: ubuntu-24.04-arm' $RELEASE_WORKFLOW; and grep -qF 'PKG="$(ls -t /tmp/termux-smoke/*.deb | head -1)"' $RELEASE_WORKFLOW; and grep -qF 'sudo timeout 10m bash scripts/termux-smoke.sh /tmp/termux-smoke "$PKG" /tmp/termux-root /tmp/termux-smoke/key.gpg' $RELEASE_WORKFLOW; and not grep -qF 'continue-on-error: true' $RELEASE_WORKFLOW
    ct_pass CT-8 "Release workflow includes hardened Termux publish + smoke jobs"
else if test -f $RELEASE_WORKFLOW
    ct_fail CT-8 "Release workflow is missing hardened Termux publish/smoke coverage"
end

# Summary
echo ""
echo "F-036 Contract Tests: $pass_count passed, $fail_count failed, $skip_count skipped"
test $fail_count -eq 0
