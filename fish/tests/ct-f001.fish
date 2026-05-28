#!/usr/bin/env fish
# Contract Tests: F-001 OrgPath Abstraction (CHAIN_INSTALL_PREFIX)
# TDD: PERSONAL_20260527T161230Z_Runabout_XPlatform_F1_OrgPath_Abstraction_TDD.md
# Expected state: FAILING - no implementation yet (fish/conf.d/ and fish/scripts/ don't exist)

set -l SCRIPT_DIR (realpath (dirname (status --current-filename)))
set -l REPO_ROOT (realpath $SCRIPT_DIR/../..)
set -l CONF_D $REPO_ROOT/fish/conf.d/00-chain-paths.fish
set -l POSTINSTALL $REPO_ROOT/fish/scripts/postinstall.fish
set -l FUNCS_DIR $REPO_ROOT/fish/functions

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

# CT-1: CHAIN_INSTALL_PREFIX default path
echo "--- CT-1: conf.d sources without error; CHAIN_INSTALL_PREFIX defaults to /usr/local/share/chaintools"
if not test -f $CONF_D
    ct_fail CT-1 "fish/conf.d/00-chain-paths.fish does not exist"
else
    set -l result (fish --no-config -c "set -e CHAIN_INSTALL_PREFIX; source $CONF_D; echo \$CHAIN_INSTALL_PREFIX" 2>&1)
    if test "$result" = /usr/local/share/chaintools
        ct_pass CT-1 "CHAIN_INSTALL_PREFIX defaults to /usr/local/share/chaintools"
    else
        ct_fail CT-1 "expected /usr/local/share/chaintools, got: '$result'"
    end
end

# CT-2: custom CHAIN_INSTALL_PREFIX prepended to fish_function_path
echo "--- CT-2: custom CHAIN_INSTALL_PREFIX prepended to fish_function_path"
if not test -f $CONF_D
    ct_fail CT-2 "fish/conf.d/00-chain-paths.fish does not exist"
else
    set -l result (fish --no-config -c "set -gx CHAIN_INSTALL_PREFIX /custom/path; source $CONF_D; contains /custom/path/fish/functions \$fish_function_path; and echo ok; or echo fail" 2>&1)
    if test "$result" = ok
        ct_pass CT-2 "/custom/path/fish/functions prepended to fish_function_path"
    else
        ct_fail CT-2 "expected ok, got: '$result'"
    end
end

# CT-3: postinstall.fish sets CHAIN_INSTALL_PREFIX as universal variable
echo "--- CT-3: postinstall.fish sets CHAIN_INSTALL_PREFIX universal variable"
if not test -f $POSTINSTALL
    ct_fail CT-3 "fish/scripts/postinstall.fish does not exist"
else
    set -l tmpxdg (mktemp -d)
    set -l result (env XDG_CONFIG_HOME=$tmpxdg XDG_DATA_HOME=$tmpxdg fish --no-config -c "source $POSTINSTALL; echo \$CHAIN_INSTALL_PREFIX" 2>&1)
    rm -rf $tmpxdg
    if test "$result" = /usr/local/share/chaintools
        ct_pass CT-3 "postinstall sets CHAIN_INSTALL_PREFIX to /usr/local/share/chaintools"
    else
        ct_fail CT-3 "expected /usr/local/share/chaintools, got: '$result'"
    end
end

# CT-4: No $ORG_PATH references in fish/functions/ (static)
echo "--- CT-4: No \$ORG_PATH references in fish/functions/ (static)"
if not test -d $FUNCS_DIR
    ct_fail CT-4 "fish/functions/ does not exist (migration not started)"
else
    set -l matches (rg -l 'ORG_PATH' $FUNCS_DIR 2>/dev/null)
    if test -z "$matches"
        ct_pass CT-4 "No \$ORG_PATH references in fish/functions/"
    else
        ct_fail CT-4 "\$ORG_PATH found in: $matches"
    end
end

# CT-5: CHAIN_DOCS_ROOT absent emits CP-002 error from epic-inject
echo "--- CT-5: epic-inject exits 1 with CP-002 message when CHAIN_DOCS_ROOT unset"
if not test -f $FUNCS_DIR/epic-inject.fish
    ct_fail CT-5 "fish/functions/epic-inject.fish does not exist"
else
    set -l output (fish --no-config -c "set -p fish_function_path $FUNCS_DIR; set -e CHAIN_DOCS_ROOT; epic-inject 2>&1; echo exit:\$status" 2>&1)
    if string match -q '*CHAIN_DOCS_ROOT not set*' -- $output; and string match -q '*exit:1*' -- $output
        ct_pass CT-5 "epic-inject exits 1 with CP-002 message"
    else
        ct_fail CT-5 "expected CP-002 message + exit:1, got: '$output'"
    end
end

# CT-6: conf.d autoload from /etc/fish/conf.d/ (requires root - Docker Ubuntu CI only)
echo "--- CT-6: conf.d triggers autoload from /etc/fish/conf.d/"
ct_skip CT-6 "requires root access to /etc/fish/conf.d/ - Docker Ubuntu CI only"

# CT-7 (CT-X): conf.d fallback safe in read-only container
# Verifies conf.d uses set -gx (not set -U) in the default-path branch, so it works
# when XDG_CONFIG_HOME is non-writable (e.g., read-only OCI layer)
echo "--- CT-7: conf.d sets CHAIN_INSTALL_PREFIX via set -gx when XDG_CONFIG_HOME is unwritable"
if not test -f $CONF_D
    ct_fail CT-7 "fish/conf.d/00-chain-paths.fish does not exist"
else
    set -l stdout_val (env XDG_CONFIG_HOME=/nonexistent fish --no-config -c "set -e CHAIN_INSTALL_PREFIX; source $CONF_D; echo \$CHAIN_INSTALL_PREFIX" 2>/dev/null)
    set -l stderr_val (env XDG_CONFIG_HOME=/nonexistent fish --no-config -c "set -e CHAIN_INSTALL_PREFIX; source $CONF_D" 2>&1 >/dev/null)
    if test "$stdout_val" = /usr/local/share/chaintools; and test -z "$stderr_val"
        ct_pass CT-7 "conf.d sets CHAIN_INSTALL_PREFIX via set -gx without error"
    else
        set -l reason "value='$stdout_val'"
        test -n "$stderr_val"; and set reason "$reason stderr='$stderr_val'"
        ct_fail CT-7 "$reason"
    end
end

# Summary
echo ""
echo "F-001 Contract Tests: $pass_count passed, $fail_count failed, $skip_count skipped"
test $fail_count -eq 0
