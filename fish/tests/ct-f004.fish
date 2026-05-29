#!/usr/bin/env fish
# Contract Tests: F-004 nFPM Debian/Ubuntu Packaging
# TDD: PERSONAL_20260529T033651Z_Runabout_XPlatform_F4_nFPM_Debian_Packaging_TDD.md
# Expected state: CT-1 through CT-5 FAILING (nfpm.yaml and .goreleaser.yaml nfpms: section don't exist);
#                 CT-6 SKIP on macOS (Ubuntu 22.04 Docker CI only)

set -l SCRIPT_DIR (realpath (dirname (status --current-filename)))
set -l REPO_ROOT (realpath $SCRIPT_DIR/../..)
set -l NFPM_YAML $REPO_ROOT/nfpm.yaml
set -l GORELEASER $REPO_ROOT/.goreleaser.yaml

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

# CT-1: nfpm.yaml exists at repo root
echo "--- CT-1: nfpm.yaml exists at repo root"
if test -f $NFPM_YAML
    ct_pass CT-1 "nfpm.yaml exists"
else
    ct_fail CT-1 "nfpm.yaml does not exist at $NFPM_YAML"
end

# CT-2: nfpm.yaml has name: chaintools
echo "--- CT-2: nfpm.yaml has 'name: chaintools'"
if not test -f $NFPM_YAML
    ct_fail CT-2 "nfpm.yaml does not exist"
else
    if grep -qF 'name: chaintools' $NFPM_YAML
        ct_pass CT-2 "nfpm.yaml has name: chaintools"
    else
        ct_fail CT-2 "nfpm.yaml missing 'name: chaintools'"
    end
end

# CT-3: nfpm.yaml contents includes fish/functions/ directory entry
echo "--- CT-3: nfpm.yaml contents includes fish/functions/ dir entry"
if not test -f $NFPM_YAML
    ct_fail CT-3 "nfpm.yaml does not exist"
else
    if grep -q 'fish/functions' $NFPM_YAML
        ct_pass CT-3 "nfpm.yaml contents includes fish/functions/ entry"
    else
        ct_fail CT-3 "nfpm.yaml missing fish/functions/ contents entry"
    end
end

# CT-4: nfpm.yaml scripts.postinstall references postinstall.fish
echo "--- CT-4: nfpm.yaml scripts.postinstall references postinstall.fish"
if not test -f $NFPM_YAML
    ct_fail CT-4 "nfpm.yaml does not exist"
else
    if grep -q 'postinstall' $NFPM_YAML; and grep -q 'postinstall.fish' $NFPM_YAML
        ct_pass CT-4 "nfpm.yaml scripts.postinstall references postinstall.fish"
    else
        ct_fail CT-4 "nfpm.yaml missing scripts.postinstall: postinstall.fish"
    end
end

# CT-5: .goreleaser.yaml has nfpms: section
echo "--- CT-5: .goreleaser.yaml has nfpms: section"
if not test -f $GORELEASER
    ct_fail CT-5 ".goreleaser.yaml does not exist"
else
    if grep -q '^nfpms:' $GORELEASER
        ct_pass CT-5 ".goreleaser.yaml has nfpms: section"
    else
        ct_fail CT-5 ".goreleaser.yaml missing nfpms: section"
    end
end

# CT-6: dpkg install smoke test (Ubuntu 22.04 Docker CI only)
echo "--- CT-6: dpkg -i dist/*.deb installs chaintools and fish -c 'type emit_jsonl' succeeds"
if test (uname) = Darwin
    ct_skip CT-6 "Ubuntu 22.04 Docker CI only"
else if not test -d $REPO_ROOT/dist
    ct_skip CT-6 "dist/ not found - run goreleaser release first"
else
    set -l deb_files (string match -r '.*\.deb' $REPO_ROOT/dist/*.deb 2>/dev/null)
    if test -z "$deb_files"
        ct_fail CT-6 "no .deb files found in dist/"
    else
        dpkg -i $deb_files 2>/dev/null
        set -l install_status $status
        set -l type_result (fish -c 'type emit_jsonl' 2>&1)
        if test $install_status -eq 0; and string match -q '*function*' -- $type_result
            ct_pass CT-6 "dpkg install succeeded and emit_jsonl autoloads"
        else
            ct_fail CT-6 "dpkg install_status=$install_status type_result='$type_result'"
        end
    end
end

# Summary
echo ""
echo "F-004 Contract Tests: $pass_count passed, $fail_count failed, $skip_count skipped"
test $fail_count -eq 0
