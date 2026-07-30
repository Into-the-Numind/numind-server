#!/usr/bin/env bash
# Idempotently provisions the same-host Sandbox isolation base for numind prod.
# It never modifies application data or config_prod.yaml. Existing host state
# that does not match this contract fails closed instead of being overwritten.

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"
UNIT_SRC_DIR="$REPO_ROOT/deploy/sandbox"

SANDBOX_USER="${NUMIND_SANDBOX_USER:-numind-sandbox}"
SANDBOX_GROUP="${NUMIND_SANDBOX_GROUP:-numind-sandbox}"
API_GROUP="${NUMIND_SANDBOX_API_GROUP:-numind-sandbox-api}"
SANDBOX_HOME="/opt/numind-sandbox"
SANDBOX_ROOT="${NUMIND_SANDBOX_ROOT:-}"
TEST_MODE="${NUMIND_SANDBOX_TEST_MODE:-0}"
DATA_IMAGE_SIZE_BYTES=8589934592
DATA_IMAGE="$SANDBOX_HOME/data-root.img"
DATA_ROOT="$SANDBOX_HOME/data-root"
RUN_DIR="/run/numind-sandbox"
CONFIG_DIR="/etc/numind-sandbox"
SYSTEMD_DIR="/etc/systemd/system"
SUBID_START="${NUMIND_SANDBOX_SUBID_START:-231072}"
SUBID_COUNT="${NUMIND_SANDBOX_SUBID_COUNT:-65536}"
DOCKER_CPU_QUOTA="${NUMIND_SANDBOX_CPU_QUOTA_PERCENT:-400%}"
DOCKER_TASKS_MAX="${NUMIND_SANDBOX_TASKS_MAX:-576}"

fail() {
  echo "ERROR: sandbox host provisioning: $1" >&2
  exit 1
}

root_path() {
  local path="$1"
  if [ -z "$SANDBOX_ROOT" ]; then
    printf '%s\n' "$path"
  else
    printf '%s%s\n' "$SANDBOX_ROOT" "$path"
  fi
}

log_action() {
  [ "$TEST_MODE" = "1" ] || return 0
  mkdir -p "$(root_path /tmp)"
  printf '%s\n' "$*" >> "$(root_path /tmp/sandbox-provision.log)"
}

require_root() {
  [ "$TEST_MODE" = "1" ] && return 0
  [ "$(id -u)" -eq 0 ] || fail "must run as root on the target host"
}

require_numeric_env() {
  local name="$1"
  local value="${!name:-}"
  [ -n "$value" ] || fail "$name is required"
  [[ "$value" =~ ^[0-9]+$ ]] || fail "$name must be numeric"
  [ "$value" -gt 0 ] || fail "$name must be greater than zero"
}

require_digest_env() {
  local name="$1"
  local value="${!name:-}"
  [ -n "$value" ] || fail "$name is required"
  if ! [[ "$value" =~ ^ccr\.ccs\.tencentyun\.com/youshunumind/sandbox-skill@sha256:[a-f0-9]{64}$ ]]; then
    fail "$name must be the pinned sandbox-skill sha256 digest"
  fi
}

require_hex64_env() {
  local name="$1"
  local value="${!name:-}"
  [ -n "$value" ] || fail "$name is required"
  [[ "$value" =~ ^[a-f0-9]{64}$ ]] || fail "$name must be 64 lowercase hex chars"
}

require_capacity_env() {
  require_numeric_env NUMIND_SANDBOX_PARENT_MEMORY_MAX_BYTES
  require_numeric_env NUMIND_SANDBOX_WORKLOAD_MEMORY_MAX_BYTES
  require_numeric_env NUMIND_SANDBOX_WORKLOAD_MEMORY_HIGH_BYTES
  require_numeric_env NUMIND_SANDBOX_WORKLOAD_MEMORY_RECOVERY_BYTES
  require_numeric_env NUMIND_SANDBOX_WORKLOAD_MEMORY_SHED_BYTES
  require_numeric_env NUMIND_SANDBOX_CONTROL_MEMORY_HIGH_BYTES
  require_numeric_env NUMIND_SANDBOX_CONTROL_MEMORY_MAX_BYTES
  require_numeric_env NUMIND_SANDBOX_PARENT_HEADROOM_BYTES
  require_numeric_env NUMIND_SANDBOX_API_HOST_UID
  require_numeric_env DOCKER_TASKS_MAX
  require_digest_env NUMIND_SANDBOX_IMAGE_DIGEST
  require_hex64_env NUMIND_SANDBOX_SECCOMP_SHA256
  [ -n "${NUMIND_SANDBOX_BROKER_INSTANCE:-}" ] || fail "NUMIND_SANDBOX_BROKER_INSTANCE is required"
  [[ "$NUMIND_SANDBOX_BROKER_INSTANCE" =~ ^[A-Za-z0-9_.-]{3,64}$ ]] || fail "NUMIND_SANDBOX_BROKER_INSTANCE is invalid"
}

require_cgroup_v2() {
  local controllers_file
  controllers_file="$(root_path /sys/fs/cgroup/cgroup.controllers)"
  [ -f "$controllers_file" ] || fail "cgroup v2 controllers file is missing"
  local controllers
  controllers="$(cat "$controllers_file")"
  for controller in memory cpu pids io; do
    [[ " $controllers " == *" $controller "* ]] || fail "cgroup v2 controller missing: $controller"
  done
}

test_group_file() { root_path "/tmp/fake-groups/$1"; }
test_user_file() { root_path "/tmp/fake-users/$1"; }

group_exists() {
  local group="$1"
  if [ "$TEST_MODE" = "1" ]; then
    [ -f "$(test_group_file "$group")" ]
  else
    getent group "$group" >/dev/null
  fi
}

group_gid() {
  local group="$1"
  if [ "$TEST_MODE" = "1" ]; then
    awk -F: '{print $2}' "$(test_group_file "$group")"
  else
    getent group "$group" | cut -d: -f3
  fi
}

ensure_group() {
  local group="$1"
  local expected_gid="${2:-}"
  if group_exists "$group"; then
    local actual_gid
    actual_gid="$(group_gid "$group")"
    if [ -n "$expected_gid" ] && [ "$actual_gid" != "$expected_gid" ]; then
      fail "existing group $group has gid=$actual_gid, expected $expected_gid"
    fi
    return 0
  fi
  if [ "$TEST_MODE" = "1" ]; then
    local default_gid="1998"
    [ "$group" = "$API_GROUP" ] && default_gid="1999"
    mkdir -p "$(dirname "$(test_group_file "$group")")"
    printf '%s:%s\n' "$group" "${expected_gid:-$default_gid}" > "$(test_group_file "$group")"
    log_action "groupadd $group ${expected_gid:-auto}"
  else
    if [ -n "$expected_gid" ]; then
      groupadd --system --gid "$expected_gid" "$group"
    else
      groupadd --system "$group"
    fi
  fi
}

user_exists() {
  local user="$1"
  if [ "$TEST_MODE" = "1" ]; then
    [ -f "$(test_user_file "$user")" ]
  else
    id -u "$user" >/dev/null 2>&1
  fi
}

user_field() {
  local user="$1"
  local field="$2"
  if [ "$TEST_MODE" = "1" ]; then
    awk -F: -v field="$field" '
      field == "uid" {print $2}
      field == "group" {print $3}
      field == "home" {print $4}
      field == "shell" {print $5}
    ' "$(test_user_file "$user")"
  else
    case "$field" in
      uid) id -u "$user" ;;
      group) id -gn "$user" ;;
      home) getent passwd "$user" | cut -d: -f6 ;;
      shell) getent passwd "$user" | cut -d: -f7 ;;
    esac
  fi
}

ensure_user() {
  local user="$1"
  local group="$2"
  local expected_uid="${3:-}"
  if user_exists "$user"; then
    [ "$(user_field "$user" group)" = "$group" ] || fail "existing user $user has unexpected primary group"
    [ "$(user_field "$user" home)" = "$SANDBOX_HOME" ] || fail "existing user $user has unexpected home"
    case "$(user_field "$user" shell)" in
      /usr/sbin/nologin|/sbin/nologin|/bin/false) ;;
      *) fail "existing user $user must be non-login" ;;
    esac
    if [ -n "$expected_uid" ] && [ "$(user_field "$user" uid)" != "$expected_uid" ]; then
      fail "existing user $user has unexpected uid"
    fi
    return 0
  fi
  if [ "$TEST_MODE" = "1" ]; then
    mkdir -p "$(dirname "$(test_user_file "$user")")"
    printf '%s:%s:%s:%s:%s\n' "$user" "${expected_uid:-1998}" "$group" "$SANDBOX_HOME" "/usr/sbin/nologin" > "$(test_user_file "$user")"
    log_action "useradd $user ${expected_uid:-auto}"
  else
    local args=(--system --home-dir "$SANDBOX_HOME" --shell /usr/sbin/nologin --gid "$group" --create-home)
    [ -z "$expected_uid" ] || args+=(--uid "$expected_uid")
    useradd "${args[@]}" "$user"
  fi
}

ensure_subid() {
  local file="$1"
  local user="$2"
  local start="$3"
  local count="$4"
  local path
  path="$(root_path "$file")"
  mkdir -p "$(dirname "$path")"
  touch "$path"
  local existing
  existing="$(grep -E "^${user}:" "$path" || true)"
  if [ -n "$existing" ]; then
    [ "$existing" = "${user}:${start}:${count}" ] || fail "existing $file entry for $user conflicts"
    return 0
  fi
  printf '%s:%s:%s\n' "$user" "$start" "$count" >> "$path"
  log_action "subid $file $user $start $count"
}

require_prerequisite_commands() {
  if [ "$TEST_MODE" = "1" ]; then
    local available=" ${NUMIND_SANDBOX_TEST_COMMANDS:-} "
    for cmd in slirp4netns newuidmap newgidmap dockerd rootlesskit; do
      [[ "$available" == *" $cmd "* ]] || fail "required rootless prerequisite missing: $cmd"
    done
    return 0
  fi
  local missing=()
  for cmd in slirp4netns newuidmap newgidmap dockerd rootlesskit; do
    command -v "$cmd" >/dev/null 2>&1 || missing+=("$cmd")
  done
  if [ "${#missing[@]}" -ne 0 ]; then
    fail "required rootless prerequisite(s) missing: ${missing[*]}"
  fi
}

ensure_directories() {
  for dir in "$SANDBOX_HOME" "$DATA_ROOT" "$SANDBOX_HOME/bin" "$SANDBOX_HOME/journal" "$SANDBOX_HOME/seccomp" "$SANDBOX_HOME/docker-config" "$CONFIG_DIR" "$RUN_DIR"; do
    mkdir -p "$(root_path "$dir")"
  done
  chmod 0750 "$(root_path "$SANDBOX_HOME")"
  chmod 02770 "$(root_path "$RUN_DIR")"
  if [ "$TEST_MODE" != "1" ]; then
    chown -R "$SANDBOX_USER:$SANDBOX_GROUP" "$SANDBOX_HOME"
    chown "root:$API_GROUP" "$RUN_DIR"
  fi
}

ensure_data_image() {
  local image_path
  image_path="$(root_path "$DATA_IMAGE")"
  if [ -L "$image_path" ]; then
    fail "data-root image must not be a symlink"
  fi
  if [ -e "$image_path" ]; then
    [ -f "$image_path" ] || fail "data-root image exists but is not a regular file"
    local size
    size="$(wc -c < "$image_path" | tr -d ' ')"
    [ "$size" -eq "$DATA_IMAGE_SIZE_BYTES" ] || fail "existing data-root image has unexpected size"
  else
    truncate -s "$DATA_IMAGE_SIZE_BYTES" "$image_path"
    if [ "$TEST_MODE" = "1" ]; then
      printf '%s\n' "11111111-2222-3333-4444-555555555555" > "${image_path}.uuid"
      log_action "mkfs.ext4 $DATA_IMAGE"
    else
      mkfs.ext4 -F "$image_path"
    fi
  fi
}

data_root_uuid() {
  local image_path
  image_path="$(root_path "$DATA_IMAGE")"
  if [ "$TEST_MODE" = "1" ]; then
    cat "${image_path}.uuid"
  else
    blkid -p -s UUID -o value "$image_path"
  fi
}

write_file() {
  local path="$1"
  local mode="$2"
  mkdir -p "$(dirname "$(root_path "$path")")"
  cat > "$(root_path "$path")"
  chmod "$mode" "$(root_path "$path")"
}

install_systemd_units() {
  mkdir -p "$(root_path "$SYSTEMD_DIR")"
  for unit in \
    numind-sandbox-control.slice \
    numind-sandbox-workload.slice \
    numind-sandbox-data-root.mount \
    numind-sandboxd.service
  do
    cp "$UNIT_SRC_DIR/$unit" "$(root_path "$SYSTEMD_DIR/$unit")"
    chmod 0644 "$(root_path "$SYSTEMD_DIR/$unit")"
  done
}

render_capacity_dropins() {
  write_file "$SYSTEMD_DIR/numind-sandbox.slice.d/10-capacity.conf" 0644 <<EOF
[Slice]
CPUAccounting=yes
MemoryAccounting=yes
TasksAccounting=yes
IOAccounting=yes
MemoryMax=${NUMIND_SANDBOX_PARENT_MEMORY_MAX_BYTES}
CPUQuota=${DOCKER_CPU_QUOTA}
TasksMax=${DOCKER_TASKS_MAX}
EOF
  write_file "$SYSTEMD_DIR/numind-sandbox-control.slice.d/10-capacity.conf" 0644 <<EOF
[Slice]
MemoryHigh=${NUMIND_SANDBOX_CONTROL_MEMORY_HIGH_BYTES}
MemoryMax=${NUMIND_SANDBOX_CONTROL_MEMORY_MAX_BYTES}
CPUQuota=${DOCKER_CPU_QUOTA}
TasksMax=${DOCKER_TASKS_MAX}
EOF
  write_file "$SYSTEMD_DIR/numind-sandbox-workload.slice.d/10-capacity.conf" 0644 <<EOF
[Slice]
MemoryHigh=${NUMIND_SANDBOX_WORKLOAD_MEMORY_HIGH_BYTES}
MemoryMax=${NUMIND_SANDBOX_WORKLOAD_MEMORY_MAX_BYTES}
CPUQuota=${DOCKER_CPU_QUOTA}
TasksMax=${DOCKER_TASKS_MAX}
EOF
}

render_docker_config() {
  write_file "$SANDBOX_HOME/docker-config/daemon.json" 0640 <<EOF
{
  "data-root": "${DATA_ROOT}/docker",
  "bridge": "none",
  "iptables": false,
  "ip-forward": false,
  "userland-proxy": false,
  "live-restore": false
}
EOF
}

render_sandboxd_config() {
  local uuid
  uuid="$(data_root_uuid)"
  local sandbox_uid api_gid
  sandbox_uid="$(user_field "$SANDBOX_USER" uid)"
  api_gid="$(group_gid "$API_GROUP")"
  write_file "$CONFIG_DIR/sandboxd.yaml" 0640 <<EOF
sandboxd:
  journal_path: ${SANDBOX_HOME}/journal/leases.sqlite3
  broker_instance: ${NUMIND_SANDBOX_BROKER_INSTANCE}
  docker_host: unix:///run/user/${sandbox_uid}/docker.sock
  docker_config_dir: ${SANDBOX_HOME}/docker-config
  socket:
    path: ${RUN_DIR}/sandboxd.sock
    uid: ${sandbox_uid}
    gid: ${api_gid}
    dir_uid: 0
    dir_gid: ${api_gid}
  allowed_api_uids: [${NUMIND_SANDBOX_API_HOST_UID}]
  runtime:
    image_digest: ${NUMIND_SANDBOX_IMAGE_DIGEST}
    seccomp_path: ${SANDBOX_HOME}/seccomp/seccomp.json
    seccomp_sha256: ${NUMIND_SANDBOX_SECCOMP_SHA256}
    allowed_skills: [pptx-author, docx-author, xlsx-author, pdf-from-html]
    allowed_tool_env_keys: [NUMIND_OUTPUT_FORMAT]
  capacity:
    evidence_mode: fresh
    baseline_bytes: ${NUMIND_SANDBOX_PARENT_MEMORY_MAX_BYTES}
    parent_max_bytes: ${NUMIND_SANDBOX_PARENT_MEMORY_MAX_BYTES}
    workload_max_bytes: ${NUMIND_SANDBOX_WORKLOAD_MEMORY_MAX_BYTES}
    workload_high_bytes: ${NUMIND_SANDBOX_WORKLOAD_MEMORY_HIGH_BYTES}
    workload_recovery_bytes: ${NUMIND_SANDBOX_WORKLOAD_MEMORY_RECOVERY_BYTES}
    workload_shed_bytes: ${NUMIND_SANDBOX_WORKLOAD_MEMORY_SHED_BYTES}
    control_high_bytes: ${NUMIND_SANDBOX_CONTROL_MEMORY_HIGH_BYTES}
    control_max_bytes: ${NUMIND_SANDBOX_CONTROL_MEMORY_MAX_BYTES}
    parent_headroom_bytes: ${NUMIND_SANDBOX_PARENT_HEADROOM_BYTES}
  readiness:
    parent_cgroup_path: /sys/fs/cgroup/numind-sandbox.slice
    workload_cgroup_path: /sys/fs/cgroup/numind-sandbox.slice/numind-sandbox-workload.slice
    data_root_path: ${DATA_ROOT}
    data_root_uuid: ${uuid}
EOF
  write_file "$CONFIG_DIR/sandboxd.env" 0640 <<EOF
NUMIND_SANDBOX_BROKER_INSTANCE=${NUMIND_SANDBOX_BROKER_INSTANCE}
NUMIND_SANDBOX_API_HOST_UID=${NUMIND_SANDBOX_API_HOST_UID}
NUMIND_SANDBOX_IMAGE_DIGEST=${NUMIND_SANDBOX_IMAGE_DIGEST}
NUMIND_SANDBOX_SECCOMP_SHA256=${NUMIND_SANDBOX_SECCOMP_SHA256}
NUMIND_SANDBOX_DATA_ROOT_UUID=${uuid}
EOF
  echo "NUMIND_SANDBOX_BROKER_GID=${api_gid}"
}

enable_linger_and_units() {
  if [ "$TEST_MODE" = "1" ]; then
    log_action "loginctl enable-linger $SANDBOX_USER"
    log_action "systemctl daemon-reload"
    log_action "systemctl enable numind-sandbox-data-root.mount numind-sandboxd.service"
    return 0
  fi
  loginctl enable-linger "$SANDBOX_USER"
  systemctl daemon-reload
  systemctl enable numind-sandbox-data-root.mount numind-sandboxd.service
}

stat_mode() {
  local path="$1"
  if stat -c %a "$path" >/dev/null 2>&1; then
    stat -c %a "$path"
  else
    stat -f %Lp "$path"
  fi
}

check_unreadable_by_sandbox() {
  local logical="$1"
  local path
  path="$(root_path "$logical")"
  [ -e "$path" ] || return 0
  if [ "$TEST_MODE" = "1" ]; then
    local mode
    mode="$(stat_mode "$path")"
    local other="${mode: -1}"
    [ "$other" = "0" ] || fail "Sandbox user would have world access to $logical"
    return 0
  fi
  if runuser -u "$SANDBOX_USER" -- test -r "$path"; then
    fail "$SANDBOX_USER can read protected path $logical"
  fi
}

verify_negative_permissions() {
  for path in \
    /opt/numind/prod \
    /opt/numind/config \
    /etc/ssl/certimate/youshu.asia \
    /var/lib/mysql \
    /var/lib/redis \
    /var/lib/docker \
    /var/run/docker.sock \
    /opt/numind/prod/secrets.env
  do
    check_unreadable_by_sandbox "$path"
  done
}

main() {
  require_root
  require_capacity_env
  require_cgroup_v2
  require_prerequisite_commands
  ensure_group "$SANDBOX_GROUP" "${NUMIND_SANDBOX_GROUP_GID:-}"
  ensure_group "$API_GROUP" "${NUMIND_SANDBOX_API_GROUP_GID:-}"
  ensure_user "$SANDBOX_USER" "$SANDBOX_GROUP" "${NUMIND_SANDBOX_USER_UID:-}"
  ensure_subid /etc/subuid "$SANDBOX_USER" "$SUBID_START" "$SUBID_COUNT"
  ensure_subid /etc/subgid "$SANDBOX_USER" "$SUBID_START" "$SUBID_COUNT"
  ensure_directories
  ensure_data_image
  install_systemd_units
  render_capacity_dropins
  render_docker_config
  render_sandboxd_config
  verify_negative_permissions
  enable_linger_and_units
  echo "sandbox host provisioning complete"
}

main "$@"
