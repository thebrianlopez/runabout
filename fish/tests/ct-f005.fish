#!/usr/bin/env fish
# Contract Tests: F-005 OCI Image for Kubernetes
# TDD: PERSONAL_20260529T033651Z_Runabout_XPlatform_F5_OCI_Image_Kubernetes_TDD.md
# Expected state: CT-1 through CT-5 FAILING (Dockerfile.release and .goreleaser.yaml dockers: don't exist);
#                 CT-6 and CT-7 SKIP locally (Docker CI only)

set -l SCRIPT_DIR (realpath (dirname (status --current-filename)))
set -l REPO_ROOT (realpath $SCRIPT_DIR/../..)
set -l DOCKERFILE $REPO_ROOT/Dockerfile.release
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

# CT-1: Dockerfile.release exists at repo root
echo "--- CT-1: Dockerfile.release exists at repo root"
if test -f $DOCKERFILE
    ct_pass CT-1 "Dockerfile.release exists"
else
    ct_fail CT-1 "Dockerfile.release does not exist at $DOCKERFILE"
end

# CT-2: Dockerfile.release has FROM ubuntu:22.04
echo "--- CT-2: Dockerfile.release has 'FROM ubuntu:22.04'"
if not test -f $DOCKERFILE
    ct_fail CT-2 "Dockerfile.release does not exist"
else
    if grep -qF 'FROM ubuntu:22.04' $DOCKERFILE
        ct_pass CT-2 "Dockerfile.release has FROM ubuntu:22.04"
    else
        ct_fail CT-2 "Dockerfile.release missing 'FROM ubuntu:22.04'"
    end
end

# CT-3: Dockerfile.release has ENV AUTOMATION_METRICS_SINK=stdout
echo "--- CT-3: Dockerfile.release has 'ENV AUTOMATION_METRICS_SINK=stdout'"
if not test -f $DOCKERFILE
    ct_fail CT-3 "Dockerfile.release does not exist"
else
    if grep -qF 'AUTOMATION_METRICS_SINK=stdout' $DOCKERFILE
        ct_pass CT-3 "Dockerfile.release has ENV AUTOMATION_METRICS_SINK=stdout"
    else
        ct_fail CT-3 "Dockerfile.release missing 'ENV AUTOMATION_METRICS_SINK=stdout'"
    end
end

# CT-4: Dockerfile.release has ENV CHAIN_INSTALL_PREFIX=/usr/local/share/chaintools
echo "--- CT-4: Dockerfile.release has 'ENV CHAIN_INSTALL_PREFIX=/usr/local/share/chaintools'"
if not test -f $DOCKERFILE
    ct_fail CT-4 "Dockerfile.release does not exist"
else
    if grep -qF 'CHAIN_INSTALL_PREFIX=/usr/local/share/chaintools' $DOCKERFILE
        ct_pass CT-4 "Dockerfile.release has ENV CHAIN_INSTALL_PREFIX=/usr/local/share/chaintools"
    else
        ct_fail CT-4 "Dockerfile.release missing 'ENV CHAIN_INSTALL_PREFIX=/usr/local/share/chaintools'"
    end
end

# CT-5: .goreleaser.yaml has dockers: section
echo "--- CT-5: .goreleaser.yaml has dockers: section"
if not test -f $GORELEASER
    ct_fail CT-5 ".goreleaser.yaml does not exist"
else
    if grep -q '^dockers:' $GORELEASER
        ct_pass CT-5 ".goreleaser.yaml has dockers: section"
    else
        ct_fail CT-5 ".goreleaser.yaml missing dockers: section"
    end
end

# CT-6: docker run smoke test - emit_jsonl autoloads (Docker CI only)
echo "--- CT-6: docker run chaintools:latest fish -c 'type emit_jsonl' exits 0"
if not type -q docker
    ct_skip CT-6 "docker not available - Docker CI only"
else if not docker image inspect chaintools:latest > /dev/null 2>&1
    ct_skip CT-6 "chaintools:latest image not built - run goreleaser release first"
else
    set -l result (docker run --rm chaintools:latest fish -c 'type emit_jsonl' 2>&1)
    if test $status -eq 0; and string match -q '*function*' -- $result
        ct_pass CT-6 "emit_jsonl autoloads in chaintools:latest container"
    else
        ct_fail CT-6 "type emit_jsonl failed in container: '$result'"
    end
end

# CT-7: docker run - emit_jsonl emits valid JSON to stdout (Docker CI only)
echo "--- CT-7: docker run chaintools:latest emit_jsonl emits valid JSON to stdout"
if not type -q docker
    ct_skip CT-7 "docker not available - Docker CI only"
else if not docker image inspect chaintools:latest > /dev/null 2>&1
    ct_skip CT-7 "chaintools:latest image not built - run goreleaser release first"
else
    set -l output (docker run --rm -e AUTOMATION_METRICS_SINK=stdout chaintools:latest \
        fish -c 'emit_jsonl --layer fish --event-type ci_check --command verify' 2>/dev/null)
    if echo $output | python3 -m json.tool > /dev/null 2>&1
        ct_pass CT-7 "emit_jsonl emits valid JSON to stdout from container"
    else
        ct_fail CT-7 "expected valid JSON on stdout, got: '$output'"
    end
end

# Summary
echo ""
echo "F-005 Contract Tests: $pass_count passed, $fail_count failed, $skip_count skipped"
test $fail_count -eq 0
