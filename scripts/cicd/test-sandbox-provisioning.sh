#!/usr/bin/env bash
# Fake-root regression tests for scripts/cicd/provision-sandbox-host.sh.

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
PROVISION_SH="$SCRIPT_DIR/provision-sandbox-host.sh"

die() { echo "test setup error: $1" >&2; exit 2; }

[ -f "$PROVISION_SH" ] || die "provision-sandbox-host.sh not found"

TMP="$(mktemp -d)" || die "mktemp"
trap 'rm -rf "$TMP"' EXIT

fail=0
pass() { echo "PASS: $1"; }
fail_test() { echo "FAIL: $1"; fail=1; }

prepare_root() {
  local root="$1"
  mkdir -p "$root/sys/fs/cgroup" \
           "$root/opt/numind/prod" \
           "$root/opt/numind/config" \
           "$root/etc/ssl/certimate/youshu.asia" \
           "$root/var/lib/mysql" \
           "$root/var/lib/redis" \
           "$root/var/lib/docker" \
           "$root/var/run" || die "prepare fake root"
  printf 'memory cpu pids io\n' > "$root/sys/fs/cgroup/cgroup.controllers" || die "write cgroup controllers"
  printf 'fake docker socket\n' > "$root/var/run/docker.sock" || die "write fake docker socket"
  printf 'secret\n' > "$root/opt/numind/prod/secrets.env" || die "write fake prod secrets"
  chmod 700 "$root/opt/numind/prod" \
            "$root/opt/numind/config" \
            "$root/etc/ssl/certimate/youshu.asia" \
            "$root/var/lib/mysql" \
            "$root/var/lib/redis" \
            "$root/var/lib/docker" || die "chmod protected dirs"
  chmod 600 "$root/var/run/docker.sock" "$root/opt/numind/prod/secrets.env" || die "chmod protected files"
}

run_provision() {
  local root="$1"
  shift || true
  local seccomp_hash
  seccomp_hash="$(sha256sum "$SCRIPT_DIR/../../deploy/sandbox/seccomp.json" | awk '{print $1}')"
  NUMIND_SANDBOX_TEST_MODE=1 \
  NUMIND_SANDBOX_ROOT="$root" \
  NUMIND_SANDBOX_TEST_COMMANDS="slirp4netns newuidmap newgidmap docker dockerd dockerd-rootless-setuptool.sh rootlesskit" \
  NUMIND_SANDBOX_BROKER_INSTANCE=numind-prod-sandbox-primary \
  NUMIND_SANDBOX_API_HOST_UID=1001 \
  NUMIND_SANDBOX_IMAGE_DIGEST="ccr.ccs.tencentyun.com/youshunumind/sandbox-skill@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" \
  NUMIND_SANDBOX_SECCOMP_SHA256="sha256:${seccomp_hash}" \
  NUMIND_SANDBOX_BASELINE_BYTES=4294967296 \
  NUMIND_SANDBOX_PARENT_MEMORY_MAX_BYTES=2952790016 \
  NUMIND_SANDBOX_WORKLOAD_MEMORY_MAX_BYTES=2415919104 \
  NUMIND_SANDBOX_WORKLOAD_MEMORY_HIGH_BYTES=2147483648 \
  NUMIND_SANDBOX_WORKLOAD_MEMORY_RECOVERY_BYTES=1932735283 \
  NUMIND_SANDBOX_WORKLOAD_MEMORY_SHED_BYTES=2319282339 \
  NUMIND_SANDBOX_CONTROL_MEMORY_HIGH_BYTES=268435456 \
  NUMIND_SANDBOX_CONTROL_MEMORY_MAX_BYTES=402653184 \
  NUMIND_SANDBOX_PARENT_HEADROOM_BYTES=134217728 \
  env "$@" bash "$PROVISION_SH"
}

ROOT_OK="$TMP/root-ok"
prepare_root "$ROOT_OK"
run_provision "$ROOT_OK" > "$TMP/ok.out" 2>&1
ok_rc=$?

if [ "$ok_rc" -eq 0 ]; then
  pass "provisioning succeeds in fake root"
else
  fail_test "provisioning should succeed in fake root"
fi

for file in \
  "$ROOT_OK/etc/systemd/system/numind-sandbox-control.slice" \
  "$ROOT_OK/etc/systemd/system/numind-sandbox-workload.slice" \
  "$ROOT_OK/etc/systemd/system/opt-numind\\x2dsandbox-data\\x2droot.mount" \
  "$ROOT_OK/etc/systemd/system/numind-sandboxd.service" \
  "$ROOT_OK/etc/systemd/system/numind-sandbox.slice.d/10-capacity.conf" \
  "$ROOT_OK/etc/systemd/system/numind-sandbox-workload.slice.d/10-capacity.conf" \
  "$ROOT_OK/etc/numind-sandbox/sandboxd.yaml" \
  "$ROOT_OK/etc/numind-sandbox/sandboxd.env" \
  "$ROOT_OK/opt/numind-sandbox/seccomp/seccomp.json" \
  "$ROOT_OK/opt/numind-sandbox/.config/docker/daemon.json" \
  "$ROOT_OK/opt/numind-sandbox/docker-config/daemon.json" \
  "$ROOT_OK/opt/numind-sandbox/docker-config/config.json"
do
  if [ -f "$file" ]; then
    pass "created ${file#$ROOT_OK/}"
  else
    fail_test "missing ${file#$ROOT_OK/}"
  fi
done

if [ "$(wc -c < "$ROOT_OK/opt/numind-sandbox/data-root.img" | tr -d ' ')" -eq 8589934592 ]; then
  pass "created 8GiB data-root image"
else
  fail_test "data-root image should be exactly 8GiB"
fi

if grep -Fq "data_root_uuid: 11111111-2222-3333-4444-555555555555" "$ROOT_OK/etc/numind-sandbox/sandboxd.yaml" &&
   grep -Fq "baseline_bytes: 4294967296" "$ROOT_OK/etc/numind-sandbox/sandboxd.yaml" &&
   grep -Fq "MemoryMax=2952790016" "$ROOT_OK/etc/systemd/system/numind-sandbox.slice.d/10-capacity.conf" &&
   grep -Fq "MemoryMax=2415919104" "$ROOT_OK/etc/systemd/system/numind-sandbox-workload.slice.d/10-capacity.conf" &&
   grep -Fq "seccomp_path: /opt/numind-sandbox/seccomp/seccomp.json" "$ROOT_OK/etc/numind-sandbox/sandboxd.yaml"; then
  pass "rendered UUID and capacity values"
else
  fail_test "rendered config should include UUID and capacity values"
fi

if cmp -s "$SCRIPT_DIR/../../deploy/sandbox/seccomp.json" "$ROOT_OK/opt/numind-sandbox/seccomp/seccomp.json"; then
  pass "installed seccomp profile for sandboxd"
else
  fail_test "seccomp profile should be installed from release bundle"
fi

if grep -Fq "chown root:numind-sandbox /etc/numind-sandbox/sandboxd.yaml" "$ROOT_OK/tmp/sandbox-provision.log" &&
   grep -Fq "chown root:numind-sandbox /etc/numind-sandbox/sandboxd.env" "$ROOT_OK/tmp/sandbox-provision.log" &&
   grep -Fq "chown root:numind-sandbox /opt/numind-sandbox/seccomp/seccomp.json" "$ROOT_OK/tmp/sandbox-provision.log"; then
  pass "sandboxd config and seccomp ownership allow service user reads"
else
  fail_test "sandboxd config/seccomp ownership should be set for service user"
fi

if grep -Fq "NUMIND_SANDBOX_BROKER_GID=1999" "$TMP/ok.out"; then
  pass "prints broker gid for prod server deploy"
else
  fail_test "provisioning should print broker gid"
fi

if [ "$(grep -c '^groupadd numind-sandbox ' "$ROOT_OK/tmp/sandbox-provision.log")" -eq 1 ] &&
   [ "$(grep -c '^useradd numind-sandbox ' "$ROOT_OK/tmp/sandbox-provision.log")" -eq 1 ]; then
  pass "first run creates sandbox user and groups once"
else
  fail_test "first run should create sandbox user and groups once"
fi

run_provision "$ROOT_OK" > "$TMP/ok-second.out" 2>&1
second_rc=$?
if [ "$second_rc" -eq 0 ] &&
   [ "$(grep -c '^groupadd numind-sandbox ' "$ROOT_OK/tmp/sandbox-provision.log")" -eq 1 ] &&
   [ "$(grep -c '^useradd numind-sandbox ' "$ROOT_OK/tmp/sandbox-provision.log")" -eq 1 ]; then
  pass "second run is idempotent"
else
  fail_test "second run should be idempotent"
fi

run_dir_mode="$(stat -f %Lp "$ROOT_OK/run/numind-sandbox" 2>/dev/null || stat -c %a "$ROOT_OK/run/numind-sandbox")"
if [ "$run_dir_mode" = "2770" ] || [ "$run_dir_mode" = "770" ]; then
  pass "broker socket directory uses 02770 mode"
else
  fail_test "broker socket directory should use 02770 mode"
fi

ROOT_BAD_USER="$TMP/root-bad-user"
prepare_root "$ROOT_BAD_USER"
mkdir -p "$ROOT_BAD_USER/tmp/fake-groups" "$ROOT_BAD_USER/tmp/fake-users" || die "bad user fake dirs"
printf 'numind-sandbox:1999\n' > "$ROOT_BAD_USER/tmp/fake-groups/numind-sandbox" || die "bad group"
printf 'numind-sandbox-api:1999\n' > "$ROOT_BAD_USER/tmp/fake-groups/numind-sandbox-api" || die "bad api group"
printf 'numind-sandbox:1998:numind-sandbox:/wrong/home:/usr/sbin/nologin\n' > "$ROOT_BAD_USER/tmp/fake-users/numind-sandbox" || die "bad user"
set +e
run_provision "$ROOT_BAD_USER" > "$TMP/bad-user.out" 2>&1
bad_user_rc=$?
set -e
if [ "$bad_user_rc" -ne 0 ] && grep -Fq "unexpected home" "$TMP/bad-user.out"; then
  pass "existing conflicting user fails closed"
else
  fail_test "conflicting existing user should fail closed"
fi

ROOT_BAD_CGROUP="$TMP/root-bad-cgroup"
prepare_root "$ROOT_BAD_CGROUP"
printf 'memory cpu pids\n' > "$ROOT_BAD_CGROUP/sys/fs/cgroup/cgroup.controllers" || die "bad cgroup"
set +e
run_provision "$ROOT_BAD_CGROUP" > "$TMP/bad-cgroup.out" 2>&1
bad_cgroup_rc=$?
set -e
if [ "$bad_cgroup_rc" -ne 0 ] && grep -Fq "controller missing: io" "$TMP/bad-cgroup.out"; then
  pass "missing cgroup controller fails closed"
else
  fail_test "missing cgroup controller should fail closed"
fi

ROOT_BAD_PERM="$TMP/root-bad-perm"
prepare_root "$ROOT_BAD_PERM"
chmod 755 "$ROOT_BAD_PERM/opt/numind/prod" || die "bad chmod prod"
set +e
run_provision "$ROOT_BAD_PERM" > "$TMP/bad-perm.out" 2>&1
bad_perm_rc=$?
set -e
if [ "$bad_perm_rc" -ne 0 ] && grep -Fq "world access to /opt/numind/prod" "$TMP/bad-perm.out"; then
  pass "readable prod directory fails closed"
else
  fail_test "readable prod directory should fail closed"
fi

ROOT_BAD_DIGEST="$TMP/root-bad-digest"
prepare_root "$ROOT_BAD_DIGEST"
set +e
run_provision "$ROOT_BAD_DIGEST" NUMIND_SANDBOX_IMAGE_DIGEST="ccr.ccs.tencentyun.com/youshunumind/sandbox-skill:skills-v1.5.3" > "$TMP/bad-digest.out" 2>&1
bad_digest_rc=$?
set -e
if [ "$bad_digest_rc" -ne 0 ] && grep -Fq "pinned sandbox-skill sha256 digest" "$TMP/bad-digest.out"; then
  pass "floating sandbox image tag fails closed"
else
  fail_test "floating sandbox image tag should fail closed"
fi

ROOT_BAD_SECCOMP="$TMP/root-bad-seccomp"
prepare_root "$ROOT_BAD_SECCOMP"
set +e
run_provision "$ROOT_BAD_SECCOMP" NUMIND_SANDBOX_SECCOMP_SHA256="sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb" > "$TMP/bad-seccomp.out" 2>&1
bad_seccomp_rc=$?
set -e
if [ "$bad_seccomp_rc" -ne 0 ] && grep -Fq "seccomp profile checksum mismatch" "$TMP/bad-seccomp.out"; then
  pass "seccomp checksum mismatch fails closed"
else
  fail_test "seccomp checksum mismatch should fail closed"
fi

echo
if [ "$fail" -ne 0 ]; then
  echo "---- ok output ----"
  cat "$TMP/ok.out" 2>/dev/null || true
  echo "---- second output ----"
  cat "$TMP/ok-second.out" 2>/dev/null || true
  echo "---- bad user output ----"
  cat "$TMP/bad-user.out" 2>/dev/null || true
  echo "---- bad cgroup output ----"
  cat "$TMP/bad-cgroup.out" 2>/dev/null || true
  echo "---- bad perm output ----"
  cat "$TMP/bad-perm.out" 2>/dev/null || true
  echo "---- bad digest output ----"
  cat "$TMP/bad-digest.out" 2>/dev/null || true
  echo "---- bad seccomp output ----"
  cat "$TMP/bad-seccomp.out" 2>/dev/null || true
  echo "sandbox provisioning tests FAILED"
  exit 1
fi

echo "sandbox provisioning tests PASSED"
