#!/usr/bin/env fish
# Contract Tests: F-003 Fish Function Population Audit + Runabouts Consolidation
# TDD: PERSONAL_20260527T161230Z_Runabout_XPlatform_F3_Fish_Function_Consolidation_TDD.md
# Expected state: CT-1 through CT-6 FAILING (fish/functions/ and MANIFEST.txt don't exist);
#                 CT-7 PASS (pre-migration audit confirms no aliases wrap Population 1 functions)

set -l SCRIPT_DIR (realpath (dirname (status --current-filename)))
set -l REPO_ROOT (realpath $SCRIPT_DIR/../..)
set -l FUNCS_DIR $REPO_ROOT/fish/functions
set -l MANIFEST $FUNCS_DIR/MANIFEST.txt
set -l RELEASE_YAML $REPO_ROOT/chain.release.yaml
set -l CONF_D $REPO_ROOT/fish/conf.d/00-chain-paths.fish

# Resolve docs symlink for CT-7 alias scan
set -l DOCS_CORE_FUNCS (realpath $REPO_ROOT/docs 2>/dev/null)/core/functions

# Population 1 function names (from TDD category table)
set -l POP1_PATTERN 'emit_jsonl|_emit_agent_from_cwd|dispatch-complete|dispatch-check-stalled|dispatch-scan-validate|dispatch-skill-emit|epic-inject|epic-dispatch|epic-claim-milestone|epic-claim-discovery|epic-status|agrad|ahealth|arec|aregress|adirective|apropose|atopology|ascore|ascore-composite|_registry_resolve_cwd|_yolodispatch_resolve_cwd|yolodispatch|yolodispatch-all'

# Known personal API patterns that must not appear in Population 1 (Population 2 contamination)
set -l POP2_PATTERN 'atlassian\.net|JIRA_TOKEN|CONFLUENCE_TOKEN|SLACK_TOKEN|api\.slack\.com|GOOGLE_CLIENT'

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

# CT-1: Every .fish file in fish/functions/ has a MANIFEST.txt row (static)
echo "--- CT-1: Every fish/functions/*.fish has a corresponding MANIFEST.txt row"
if not test -d $FUNCS_DIR
    ct_fail CT-1 "fish/functions/ does not exist (migration not started)"
else if not test -f $MANIFEST
    ct_fail CT-1 "fish/functions/MANIFEST.txt does not exist"
else
    set -l missing_count 0
    for f in $FUNCS_DIR/*.fish
        set -l fname (basename $f .fish)
        if not grep -qF "$fname" $MANIFEST
            echo "  missing from MANIFEST.txt: $fname"
            set missing_count (math $missing_count + 1)
        end
    end
    if test $missing_count -eq 0
        ct_pass CT-1 "All fish/functions/*.fish files have MANIFEST.txt rows"
    else
        ct_fail CT-1 "$missing_count function(s) missing from MANIFEST.txt"
    end
end

# CT-2: No $ORG_PATH references in fish/functions/ after migration (static)
echo "--- CT-2: No \$ORG_PATH references in fish/functions/ (static)"
if not test -d $FUNCS_DIR
    ct_fail CT-2 "fish/functions/ does not exist (migration not started)"
else
    set -l matches (rg -l 'ORG_PATH' $FUNCS_DIR 2>/dev/null)
    if test -z "$matches"
        ct_pass CT-2 "No \$ORG_PATH references in fish/functions/"
    else
        ct_fail CT-2 "\$ORG_PATH still present in: $matches"
    end
end

# CT-3: Population 1 functions autoload from CHAIN_INSTALL_PREFIX
echo "--- CT-3: Population 1 functions autoload when fish_function_path includes CHAIN_INSTALL_PREFIX"
if not test -d $FUNCS_DIR
    ct_fail CT-3 "fish/functions/ does not exist (migration not started)"
else if not test -f $FUNCS_DIR/emit_jsonl.fish
    ct_fail CT-3 "fish/functions/emit_jsonl.fish not present (migration incomplete)"
else
    set -l tmpdir (mktemp -d)
    # Symlink functions dir to simulate CHAIN_INSTALL_PREFIX/fish/functions
    ln -s $FUNCS_DIR $tmpdir/fish
    set -l result (env CHAIN_INSTALL_PREFIX=$tmpdir fish --no-config -c "
        set -p fish_function_path $tmpdir/fish
        type emit_jsonl
        echo exit:\$status
    " 2>&1)
    rm -rf $tmpdir
    if string match -q '*exit:0*' -- $result
        ct_pass CT-3 "emit_jsonl autoloads from CHAIN_INSTALL_PREFIX"
    else
        ct_fail CT-3 "type emit_jsonl failed: '$result'"
    end
end

# CT-4: No Population 2 (personal API) patterns in fish/functions/ (static)
echo "--- CT-4: No personal API patterns in fish/functions/ (no Population 2 contamination)"
if not test -d $FUNCS_DIR
    ct_fail CT-4 "fish/functions/ does not exist (migration not started)"
else
    set -l matches (rg -l $POP2_PATTERN $FUNCS_DIR 2>/dev/null)
    if test -z "$matches"
        ct_pass CT-4 "No personal API patterns found in fish/functions/"
    else
        ct_fail CT-4 "Population 2 contamination detected in: $matches"
    end
end

# CT-5: MANIFEST.txt version matches chain.release.yaml (static)
echo "--- CT-5: MANIFEST.txt version matches chain.release.yaml"
if not test -f $MANIFEST
    ct_fail CT-5 "fish/functions/MANIFEST.txt does not exist"
else if not test -f $RELEASE_YAML
    ct_fail CT-5 "chain.release.yaml does not exist"
else
    set -l manifest_version (grep '^# version:' $MANIFEST | string replace '# version:' '' | string trim)
    set -l release_version (yq '.fish_functions.manifest_version' $RELEASE_YAML 2>/dev/null)
    if test -z "$manifest_version"
        ct_fail CT-5 "MANIFEST.txt has no '# version:' comment"
    else if test -z "$release_version"; or test "$release_version" = null
        ct_fail CT-5 "chain.release.yaml has no .fish_functions.manifest_version field"
    else if test "$manifest_version" = "$release_version"
        ct_pass CT-5 "MANIFEST.txt version '$manifest_version' matches chain.release.yaml"
    else
        ct_fail CT-5 "version mismatch: MANIFEST.txt='$manifest_version' chain.release.yaml='$release_version'"
    end
end

# CT-6: conf.d loads only CHAIN_INSTALL_PREFIX path, not $ORG_PATH
echo "--- CT-6: conf.d/00-chain-paths.fish does not add \$ORG_PATH to fish_function_path"
if not test -f $CONF_D
    ct_fail CT-6 "fish/conf.d/00-chain-paths.fish does not exist"
else
    set -l result (fish --no-config -c "
        set -gx CHAIN_INSTALL_PREFIX /tmp/chain-test
        set -gx ORG_PATH /tmp/org-test
        source $CONF_D
        if contains /tmp/org-test/docs/core/functions \$fish_function_path; or contains \$ORG_PATH/docs/core/functions \$fish_function_path
            echo org-path-found
        else
            echo ok
        end
    " 2>&1)
    if test "$result" = ok
        ct_pass CT-6 "conf.d does not add \$ORG_PATH to fish_function_path"
    else
        ct_fail CT-6 "conf.d added \$ORG_PATH to fish_function_path (got: '$result')"
    end
end

# CT-7: Alias inventory - no aliases in docs/core/functions/ wrap Population 1 functions
# Pre-migration audit result: PASS (TDD confirms no aliases found before migration)
echo "--- CT-7: No aliases in docs/core/functions/ wrap Population 1 function names"
if not test -d "$DOCS_CORE_FUNCS"
    ct_skip CT-7 "docs/core/functions/ not found at '$DOCS_CORE_FUNCS' (symlink must resolve)"
else
    set -l alias_matches (rg -L --no-filename '^alias' $DOCS_CORE_FUNCS 2>/dev/null | grep -E "($POP1_PATTERN)" 2>/dev/null)
    if test -z "$alias_matches"
        ct_pass CT-7 "No aliases wrap Population 1 function names"
    else
        ct_fail CT-7 "Found aliases wrapping Population 1 functions (each must be in MANIFEST.txt):\n$alias_matches"
    end
end

# Summary
echo ""
echo "F-003 Contract Tests: $pass_count passed, $fail_count failed, $skip_count skipped"
test $fail_count -eq 0
