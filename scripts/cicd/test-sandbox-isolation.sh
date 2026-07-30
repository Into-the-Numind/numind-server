#!/usr/bin/env bash
# Final static/runtime gate for the Prod same-host Sandbox isolation feature.
# This script intentionally reads evidence and runs contract tests; it does not
# touch Prod, databases, secrets, or deployment hosts.

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"

fail=0
pass() { echo "PASS: $1"; }
fail_test() { echo "FAIL: $1"; fail=1; }

assert_grep() {
  local pattern="$1"
  local file="$2"
  local label="$3"
  if grep -Eq -- "$pattern" "$file"; then
    pass "$label"
  else
    fail_test "$label"
  fi
}

assert_no_grep() {
  local pattern="$1"
  local file="$2"
  local label="$3"
  if grep -Eq -- "$pattern" "$file"; then
    fail_test "$label"
  else
    pass "$label"
  fi
}

cd "$REPO_ROOT"

go test ./internal/numind/sandboxbroker -run 'IntegrationSecurityContract' -count=1
pass "Go integration security contract passes"

assert_grep 'fail_sandbox_contract "server may mount sandboxd\.sock only, never a Docker socket"' \
  scripts/cicd/deploy-remote.sh \
  "prod server deploy rejects Docker socket mounts"
assert_grep 'TARGET" = "admin".*|NUMIND_SANDBOX_BACKEND=disabled' \
  scripts/cicd/deploy-remote.sh \
  "prod admin deploy forces Sandbox disabled"
assert_grep 'deploy_sandboxd_if_needed' scripts/cicd/deploy.sh \
  "deploy.sh runs broker deploy before user API when enabled"
assert_grep "\\. '\\$\\{REMOTE_SANDBOX_BROKER_ENV\\}'" scripts/cicd/deploy.sh \
  "deploy.sh sources broker gid before user API deploy"
assert_grep 'NUMIND_SANDBOX_BACKEND' scripts/cicd/release.sh \
  "release.sh forwards Sandbox env values"
assert_grep 'WITH_DOCKER_CLI=false' scripts/cicd/build-and-push.sh \
  "prod API build keeps Docker CLI off by default"
assert_grep 'command -v docker' scripts/cicd/test-sandbox-artifacts.sh \
  "artifact test verifies prod runtime has no Docker CLI when Docker is available"
assert_grep 'runuser -u "\$SANDBOX_USER" -- test -r "\$path"' scripts/cicd/provision-sandbox-host.sh \
  "provisioning verifies Sandbox user cannot read protected Prod paths"
assert_grep 'sandbox-skill@sha256' \
  scripts/cicd/provision-sandbox-host.sh \
  "provisioning requires pinned Sandbox image digest"
assert_grep 'install_seccomp_profile' \
  scripts/cicd/provision-sandbox-host.sh \
  "provisioning installs checked seccomp profile"
assert_grep 'seccomp profile checksum mismatch' \
  scripts/cicd/test-sandbox-provisioning.sh \
  "provisioning test covers missing or wrong seccomp profile"
assert_grep 'ALPINE_MIRROR=https://mirrors\.aliyun\.com/alpine' \
  Dockerfile \
  "sandbox artifact build uses domestic Alpine mirror by default"
assert_grep 'DEBIAN_MIRROR=https://mirrors\.aliyun\.com/debian' \
  Dockerfile \
  "server builder uses domestic Debian mirror by default"
assert_grep 'UBUNTU_MIRROR=https://mirrors\.aliyun\.com/ubuntu' \
  Dockerfile \
  "server runtime uses domestic Ubuntu mirror by default"
assert_grep 'sandboxd must not read config_prod\.yaml' cmd/numind-sandboxd/main.go \
  "sandboxd explicitly rejects prod business config"

if git diff -- config_prod.yaml | grep -q .; then
  fail_test "config_prod.yaml must remain unchanged"
else
  pass "config_prod.yaml unchanged"
fi

if [ "$fail" -ne 0 ]; then
  echo "sandbox isolation final gate FAILED"
  exit 1
fi

echo "sandbox isolation final gate PASSED"
