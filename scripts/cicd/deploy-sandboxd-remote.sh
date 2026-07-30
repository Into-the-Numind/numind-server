#!/usr/bin/env bash
# Runs on the prod deploy host. It provisions the local Rootless Sandbox base,
# extracts sandboxd/reconcile artifacts from the already-built server image,
# atomically replaces sandboxd, verifies broker readiness, and restores the old
# binary if the broker does not come back healthy.

set -euo pipefail

: "${ENV:?ENV must be set}"
: "${IMAGE:?IMAGE must be set}"

if [ "$ENV" != "prod" ]; then
  echo "Sandbox broker deploy is prod-only; skipping for ENV=$ENV"
  exit 0
fi

if [ "${NUMIND_SANDBOX_BACKEND:-disabled}" != "broker" ]; then
  echo "Sandbox broker backend is not broker; skipping sandboxd deploy"
  exit 0
fi

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
PROVISION_SH="${NUMIND_SANDBOX_PROVISION_SH:-$SCRIPT_DIR/provision-sandbox-host.sh}"
DEPLOY_ROOT="${NUMIND_SANDBOX_DEPLOY_ROOT:-}"
BROKER_ENV_FILE="${NUMIND_SANDBOX_BROKER_ENV_FILE:-/tmp/numind-sandbox-broker.env}"
SOCKET_PATH="${NUMIND_SANDBOX_BROKER_SOCKET:-/run/numind-sandbox/sandboxd.sock}"
BIN_DIR="/opt/numind-sandbox/bin"
BACKUP_DIR="/opt/numind-sandbox/backups"
TMP_PARENT="/opt/numind-sandbox/tmp"
RECONCILE_CONFIG="${NUMIND_SANDBOX_RECONCILE_CONFIG:-/opt/numind/prod/config_prod.yaml}"
READY_TRIES="${NUMIND_SANDBOX_READY_TRIES:-60}"
READY_SLEEP_SECONDS="${NUMIND_SANDBOX_READY_SLEEP_SECONDS:-5}"
TEST_MODE="${NUMIND_SANDBOX_TEST_MODE:-0}"

fail() {
  echo "ERROR: sandboxd deploy: $1" >&2
  exit 1
}

root_path() {
  local path="$1"
  if [ -z "$DEPLOY_ROOT" ]; then
    printf '%s\n' "$path"
  else
    printf '%s%s\n' "$DEPLOY_ROOT" "$path"
  fi
}

hash_file() {
  local file="$1"
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum "$file" | awk '{print $1}'
  else
    shasum -a 256 "$file" | awk '{print $1}'
  fi
}

expected_hash() {
  local checksum_file="$1"
  local artifact="$2"
  awk -v artifact="$artifact" '
    {
      n = split($2, parts, "/")
      if (parts[n] == artifact) {
        print $1
        found = 1
      }
    }
    END { if (!found) exit 1 }
  ' "$checksum_file"
}

validate_artifact_hash() {
  local checksum_file="$1"
  local artifact_path="$2"
  local artifact_name
  artifact_name="$(basename "$artifact_path")"
  local expected actual
  expected="$(expected_hash "$checksum_file" "$artifact_name")" || fail "checksum missing for $artifact_name"
  actual="$(hash_file "$artifact_path")"
  [ "$actual" = "$expected" ] || fail "checksum mismatch for $artifact_name"
}

systemctl_cmd() {
  if [ "$TEST_MODE" = "1" ]; then
    printf 'systemctl %s\n' "$*" >> "$(root_path /tmp/sandboxd-deploy.log)"
    if [ "${NUMIND_SANDBOX_TEST_FAIL_RESTART:-0}" = "1" ] && [ "${1:-}" = "restart" ]; then
      return 1
    fi
    return 0
  fi
  systemctl "$@"
}

broker_http_ok() {
  local path="$1"
  if [ "$TEST_MODE" = "1" ]; then
    printf 'curl %s\n' "$path" >> "$(root_path /tmp/sandboxd-deploy.log)"
    [ "${NUMIND_SANDBOX_TEST_FAIL_READY:-0}" != "1" ] || return 1
    return 0
  fi
  curl --unix-socket "$SOCKET_PATH" -sf "http://sandboxd${path}" >/dev/null
}

wait_broker_ready() {
  local i
  for i in $(seq 1 "$READY_TRIES"); do
    if broker_http_ok /healthz && broker_http_ok /readyz; then
      return 0
    fi
    sleep "$READY_SLEEP_SECONDS"
  done
  return 1
}

service_is_active() {
  if [ "$TEST_MODE" = "1" ]; then
    return 0
  fi
  systemctl is-active --quiet numind-sandboxd
}

run_reconcile_dry_run() {
  local reconcile_bin
  reconcile_bin="$(root_path "$BIN_DIR/numind-sandbox-reconcile")"
  [ -x "$reconcile_bin" ] || return 0
  if [ "$TEST_MODE" = "1" ]; then
    printf 'reconcile dry-run\n' >> "$(root_path /tmp/sandboxd-deploy.log)"
    return 0
  fi
  "$reconcile_bin" -broker-socket "$SOCKET_PATH" -config "$RECONCILE_CONFIG" -limit 100 || true
}

docker_cleanup_unreferenced() {
  docker image prune -f >/dev/null 2>&1 || true
}

restore_old_binary() {
  local old_sandboxd="$1"
  local old_reconcile="$2"
  if [ -n "$old_sandboxd" ] && [ -f "$old_sandboxd" ]; then
    cp -p "$old_sandboxd" "$(root_path "$BIN_DIR/numind-sandboxd")"
  fi
  if [ -n "$old_reconcile" ] && [ -f "$old_reconcile" ]; then
    cp -p "$old_reconcile" "$(root_path "$BIN_DIR/numind-sandbox-reconcile")"
  fi
  systemctl_cmd restart numind-sandboxd || true
}

[ -x "$PROVISION_SH" ] || fail "provision script missing: $PROVISION_SH"

echo "Provisioning Sandbox host base..."
PROVISION_OUT="$(
  NUMIND_SANDBOX_ROOT="$DEPLOY_ROOT" \
  NUMIND_SANDBOX_TEST_MODE="$TEST_MODE" \
  bash "$PROVISION_SH"
)"
printf '%s\n' "$PROVISION_OUT"
BROKER_GID="$(printf '%s\n' "$PROVISION_OUT" | awk -F= '/^NUMIND_SANDBOX_BROKER_GID=/{print $2; exit}')"
[[ "$BROKER_GID" =~ ^[0-9]+$ ]] || fail "provisioning did not report NUMIND_SANDBOX_BROKER_GID"
printf 'NUMIND_SANDBOX_BROKER_GID=%s\n' "$BROKER_GID" > "$BROKER_ENV_FILE"
chmod 0600 "$BROKER_ENV_FILE"

mkdir -p "$(root_path "$BIN_DIR")" "$(root_path "$BACKUP_DIR")" "$(root_path "$TMP_PARENT")"

echo "Pulling release image for Sandbox artifacts..."
docker pull "$IMAGE"
CID="$(docker create "$IMAGE")"
TMP_DIR="$(mktemp -d "$(root_path "$TMP_PARENT/deploy.XXXXXX")")"
cleanup() {
  docker rm -f "$CID" >/dev/null 2>&1 || true
  rm -rf "$TMP_DIR"
}
trap cleanup EXIT

docker cp "$CID:/app/numind-sandboxd" "$TMP_DIR/numind-sandboxd"
docker cp "$CID:/app/numind-sandbox-reconcile" "$TMP_DIR/numind-sandbox-reconcile"
docker cp "$CID:/app/sandbox-artifacts.sha256" "$TMP_DIR/sandbox-artifacts.sha256"
chmod 0755 "$TMP_DIR/numind-sandboxd" "$TMP_DIR/numind-sandbox-reconcile"
validate_artifact_hash "$TMP_DIR/sandbox-artifacts.sha256" "$TMP_DIR/numind-sandboxd"
validate_artifact_hash "$TMP_DIR/sandbox-artifacts.sha256" "$TMP_DIR/numind-sandbox-reconcile"

TIMESTAMP="$(date +%Y%m%d%H%M%S)"
OLD_SANDBOXD=""
OLD_RECONCILE=""
if [ -f "$(root_path "$BIN_DIR/numind-sandboxd")" ]; then
  OLD_SANDBOXD="$(root_path "$BACKUP_DIR/numind-sandboxd.$TIMESTAMP")"
  cp -p "$(root_path "$BIN_DIR/numind-sandboxd")" "$OLD_SANDBOXD"
fi
if [ -f "$(root_path "$BIN_DIR/numind-sandbox-reconcile")" ]; then
  OLD_RECONCILE="$(root_path "$BACKUP_DIR/numind-sandbox-reconcile.$TIMESTAMP")"
  cp -p "$(root_path "$BIN_DIR/numind-sandbox-reconcile")" "$OLD_RECONCILE"
fi

if service_is_active; then
  echo "Draining old sandboxd (systemd TimeoutStopSec handles the 300s window)..."
  systemctl_cmd stop numind-sandboxd
fi

install -m 0755 "$TMP_DIR/numind-sandboxd" "$(root_path "$BIN_DIR/.numind-sandboxd.new")"
install -m 0755 "$TMP_DIR/numind-sandbox-reconcile" "$(root_path "$BIN_DIR/.numind-sandbox-reconcile.new")"
mv -f "$(root_path "$BIN_DIR/.numind-sandboxd.new")" "$(root_path "$BIN_DIR/numind-sandboxd")"
mv -f "$(root_path "$BIN_DIR/.numind-sandbox-reconcile.new")" "$(root_path "$BIN_DIR/numind-sandbox-reconcile")"

systemctl_cmd daemon-reload
if ! systemctl_cmd restart numind-sandboxd || ! wait_broker_ready; then
  echo "Broker did not become ready; restoring previous sandboxd binary" >&2
  restore_old_binary "$OLD_SANDBOXD" "$OLD_RECONCILE"
  run_reconcile_dry_run
  fail "broker readiness failed; user API deploy must not proceed"
fi

run_reconcile_dry_run
docker_cleanup_unreferenced

echo "✅ sandboxd deploy success; broker ready"
