#!/usr/bin/env bash
# Regression test for prod release preflight.
#
# A prod release must represent the exact tagged commit and a clean worktree.
# This test creates temporary git repos with untracked files; the release script
# must fail before rsync/build/deploy can observe those worktrees.
#
# Run: bash scripts/cicd/test-release-preflight.sh
set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
RELEASE_SH="$SCRIPT_DIR/release.sh"
DEPLOY_REMOTE_SH="$SCRIPT_DIR/deploy-remote.sh"
DEPLOY_SANDBOX_REMOTE_SH="$SCRIPT_DIR/deploy-sandboxd-remote.sh"
SECRET_HYGIENE_SH="$(cd "$SCRIPT_DIR/.." && pwd)/check_prod_secret_hygiene.sh"
PROD_SECRETS_ENV_SH="$(cd "$SCRIPT_DIR/.." && pwd)/check_prod_secrets_env.sh"
DOCKERIGNORE="$(cd "$SCRIPT_DIR/../.." && pwd)/.dockerignore"

die() { echo "test setup error: $1" >&2; exit 2; }

[ -f "$RELEASE_SH" ] || die "release.sh not found at $RELEASE_SH"
[ -f "$DEPLOY_REMOTE_SH" ] || die "deploy-remote.sh not found at $DEPLOY_REMOTE_SH"
[ -f "$DEPLOY_SANDBOX_REMOTE_SH" ] || die "deploy-sandboxd-remote.sh not found at $DEPLOY_SANDBOX_REMOTE_SH"
[ -f "$SECRET_HYGIENE_SH" ] || die "check_prod_secret_hygiene.sh not found at $SECRET_HYGIENE_SH"
[ -f "$PROD_SECRETS_ENV_SH" ] || die "check_prod_secrets_env.sh not found at $PROD_SECRETS_ENV_SH"
[ -f "$DOCKERIGNORE" ] || die ".dockerignore not found at $DOCKERIGNORE"

TMP="$(mktemp -d)" || die "mktemp"
SOCKET_TMP="$(mktemp -d /tmp/numind-sandbox-preflight.XXXXXX)" || die "mktemp socket tmp"
trap 'rm -rf "$TMP" "$SOCKET_TMP"' EXIT

mkdir -p "$TMP/bin" "$TMP/locks" || die "mkdir temp dirs"

cat > "$TMP/bin/rsync" <<'RSYNC' || die "write fake rsync"
#!/usr/bin/env bash
echo "RSYNC_ARGS_BEGIN"
printf '%s\n' "$@"
echo "RSYNC_ARGS_END"
exit 77
RSYNC
chmod +x "$TMP/bin/rsync" || die "chmod fake rsync"

cat > "$TMP/bin/ssh" <<'SSH' || die "write fake ssh"
#!/usr/bin/env bash
echo "SSH_ARGS_BEGIN"
printf '%s\n' "$@"
echo "SSH_ARGS_END"
exit 78
SSH
chmod +x "$TMP/bin/ssh" || die "chmod fake ssh"

make_repo() {
  local repo="$1"
  mkdir -p "$repo/scripts/cicd" || die "mkdir repo dirs"
  cp "$RELEASE_SH" "$repo/scripts/cicd/release.sh" || die "copy release.sh"
  cp "$SECRET_HYGIENE_SH" "$repo/scripts/check_prod_secret_hygiene.sh" || die "copy check_prod_secret_hygiene.sh"
  (
    cd "$repo" || exit 2
    cat > config_prod.yaml <<'YAML' || exit 2
wechat:
  notify_url: https://example.com/api/v1/payment/wechat/notify
  mch_private_key_path: /opt/numind/prod/certs/apiclient_key.pem
  wechatpay_cert_path: /opt/numind/prod/certs/wechatpay.pem
  wechatpay_public_key_id: PUB_KEY_ID_0123456789
llm:
  api_key: ${NUMIND_LLM_API_KEY}
YAML
    git init -q || exit 2
    git config user.email test@example.com || exit 2
    git config user.name "Release Preflight Test" || exit 2
    git add scripts/cicd/release.sh scripts/check_prod_secret_hygiene.sh config_prod.yaml || exit 2
    git commit -q -m "seed release script" || exit 2
  ) || die "setup temporary git repo"
}

run_release() {
  local repo="$1"
  local out="$2"
  run_release_env prod "$repo" "$out"
}

run_release_env() {
  local env="$1"
  local repo="$2"
  local out="$3"
  local mode="${4:---build-only}"
  (
    cd "$repo" || exit 2
    PATH="$TMP/bin:$PATH" TMPDIR="$TMP/locks" \
      bash scripts/cicd/release.sh "$env" server "$mode"
  ) > "$out" 2>&1
}

run_release_mode() {
  local repo="$1"
  local out="$2"
  local mode="$3"
  run_release_env prod "$repo" "$out" "$mode"
}

fail=0

assert_dirty_prod_rejected() {
  local label="$1"
  local out="$2"
  local rc="$3"
  local dirty_item="$4"

  if [ "$rc" -ne 0 ]; then
    echo "PASS: $label exits non-zero (rc=$rc)"
  else
    echo "FAIL: $label should exit non-zero"
    fail=1
  fi

  if grep -q "prod release requires a clean release-relevant worktree and exact tag" "$out"; then
    echo "PASS: $label output explains release-relevant clean worktree and exact tag requirement"
  else
    echo "FAIL: $label output missing release-relevant clean worktree/exact tag error"
    fail=1
  fi

  if grep -q "$dirty_item" "$out"; then
    echo "PASS: $label output lists the dirty item"
  else
    echo "FAIL: $label output missing dirty item $dirty_item"
    fail=1
  fi

  if grep -q "RSYNC_ARGS_BEGIN" "$out"; then
    echo "FAIL: $label reached rsync before prod preflight rejected the worktree"
    fail=1
  else
    echo "PASS: $label did not reach rsync"
  fi

  if grep -q "SSH_ARGS_BEGIN" "$out"; then
    echo "FAIL: $label reached ssh before prod preflight rejected the worktree"
    fail=1
  else
    echo "PASS: $label did not reach ssh"
  fi
}

TAGGED_REPO="$TMP/tagged-dirty-repo"
make_repo "$TAGGED_REPO"
(
  cd "$TAGGED_REPO" || exit 2
  git tag v1.2.3 || exit 2
  printf 'not in tag\n' > untracked-prod-input.txt || exit 2
) || die "setup tagged dirty repo"
run_release "$TAGGED_REPO" "$TMP/tagged.out"
tagged_rc=$?
assert_dirty_prod_rejected "dirty tagged prod release" "$TMP/tagged.out" "$tagged_rc" "untracked-prod-input.txt"

UNTAGGED_REPO="$TMP/untagged-dirty-repo"
make_repo "$UNTAGGED_REPO"
(
  cd "$UNTAGGED_REPO" || exit 2
  printf 'not tagged and not committed\n' > untracked-no-tag.txt || exit 2
) || die "setup untagged dirty repo"
run_release "$UNTAGGED_REPO" "$TMP/untagged.out"
untagged_rc=$?
assert_dirty_prod_rejected "dirty untagged prod release" "$TMP/untagged.out" "$untagged_rc" "untracked-no-tag.txt"

if grep -q "Tag: missing exact tag" "$TMP/untagged.out"; then
  echo "PASS: dirty untagged prod release explains the missing tag"
else
  echo "FAIL: dirty untagged prod release missing tag explanation"
  fail=1
fi

DATA_EXCLUDED_REPO="$TMP/data-excluded-repo"
make_repo "$DATA_EXCLUDED_REPO"
(
  cd "$DATA_EXCLUDED_REPO" || exit 2
  git tag v1.2.35 || exit 2
  mkdir -p data || exit 2
  printf 'root finder metadata\n' > .DS_Store || exit 2
  printf 'local rollback only\n' > data/revert_local.sql || exit 2
  printf 'finder metadata\n' > data/.DS_Store || exit 2
) || die "setup clean tagged repo with untracked excluded data"
run_release "$DATA_EXCLUDED_REPO" "$TMP/data-excluded.out"
data_excluded_rc=$?

if [ "$data_excluded_rc" -eq 77 ]; then
  echo "PASS: untracked excluded data does not block prod release preflight"
else
  echo "FAIL: untracked excluded data should reach fake rsync (rc=$data_excluded_rc)"
  fail=1
fi

if grep -q "data/revert_local.sql" "$TMP/data-excluded.out"; then
  echo "FAIL: untracked excluded data should not be listed as a dirty item"
  fail=1
else
  echo "PASS: untracked excluded data is not listed as a dirty item"
fi

if grep -Fqx "?? .DS_Store" "$TMP/data-excluded.out" || grep -Fqx "?? data/.DS_Store" "$TMP/data-excluded.out"; then
  echo "FAIL: untracked .DS_Store should not be listed as a dirty item"
  fail=1
else
  echo "PASS: untracked .DS_Store is not listed as a dirty item"
fi

if grep -Fqx -- "--exclude=/data/" "$TMP/data-excluded.out"; then
  echo "PASS: untracked excluded data release still excludes /data/ from rsync"
else
  echo "FAIL: untracked excluded data release missing --exclude=/data/"
  fail=1
fi

SECRET_EXCLUDE_REPO="$TMP/secret-exclude-repo"
make_repo "$SECRET_EXCLUDE_REPO"
(
  cd "$SECRET_EXCLUDE_REPO" || exit 2
  cat > .gitignore <<'GITIGNORE' || exit 2
.env
secrets.env
configs/cert/
configs/ssl/
GITIGNORE
  git add .gitignore || exit 2
  git commit -q -m "add ignored secret patterns" || exit 2
  git tag v1.2.4 || exit 2
  mkdir -p configs/cert configs/ssl || exit 2
  printf 'local secret\n' > .env || exit 2
  printf 'runtime secret\n' > secrets.env || exit 2
  printf 'private key\n' > configs/cert/apiclient_key.pem || exit 2
  printf 'tls key\n' > configs/ssl/cert.key || exit 2
) || die "setup clean tagged repo with ignored secret files"
run_release "$SECRET_EXCLUDE_REPO" "$TMP/secret-exclude.out"
secret_exclude_rc=$?

if [ "$secret_exclude_rc" -eq 77 ]; then
  echo "PASS: clean tagged prod release reaches fake rsync for exclude inspection"
else
  echo "FAIL: clean tagged prod release should reach fake rsync (rc=$secret_exclude_rc)"
  fail=1
fi

for pattern in \
  "--exclude=.env" \
  "--exclude=.env.*" \
  "--exclude=secrets.env" \
  "--exclude=*.pem" \
  "--exclude=*.key" \
  "--exclude=*.key.*" \
  "--exclude=*.crt" \
  "--exclude=*.crt.*" \
  "--exclude=configs/cert/" \
  "--exclude=configs/ssl/" \
  "--exclude=/config_dev.yaml" \
  "--exclude=/config_qa.yaml" \
  "--exclude=/config_local.yaml" \
  "--exclude=/config_*.local.yaml"
do
  if grep -Fqx -- "$pattern" "$TMP/secret-exclude.out"; then
    echo "PASS: rsync excludes $pattern"
  else
    echo "FAIL: rsync output missing $pattern"
    fail=1
  fi
done

if grep -q "SSH_ARGS_BEGIN" "$TMP/secret-exclude.out"; then
  echo "FAIL: clean tagged prod exclude inspection should stop at fake rsync before ssh"
  fail=1
else
  echo "PASS: clean tagged prod exclude inspection did not reach ssh"
fi

if grep -Fqx -- "--exclude=/data/" "$TMP/secret-exclude.out"; then
  echo "PASS: prod rsync excludes root data directory"
else
  echo "FAIL: prod rsync output missing --exclude=/data/"
  fail=1
fi

if grep -Eq '^/data/$' "$DOCKERIGNORE"; then
  echo "PASS: dockerignore excludes root data directory"
else
  echo "FAIL: dockerignore missing /data/ root directory exclude"
  fail=1
fi

HYGIENE_BUILD_ONLY_REPO="$TMP/hygiene-build-only-repo"
make_repo "$HYGIENE_BUILD_ONLY_REPO"
(
  cd "$HYGIENE_BUILD_ONLY_REPO" || exit 2
  cat > config_prod.yaml <<'YAML' || exit 2
database:
  password: build-mode-secret
YAML
  git add config_prod.yaml || exit 2
  git commit -q -m "add secret prod config" || exit 2
  git tag v1.2.5 || exit 2
) || die "setup build-only hygiene repo"
run_release_mode "$HYGIENE_BUILD_ONLY_REPO" "$TMP/hygiene-build-only.out" "--build-only"
hygiene_build_only_rc=$?

if [ "$hygiene_build_only_rc" -eq 1 ] && grep -q "prod-secret-hygiene blocked release" "$TMP/hygiene-build-only.out"; then
  echo "PASS: prod --build-only runs secret hygiene before rsync"
else
  echo "FAIL: prod --build-only should run secret hygiene before rsync"
  fail=1
fi

if grep -q "RSYNC_ARGS_BEGIN" "$TMP/hygiene-build-only.out" || grep -q "SSH_ARGS_BEGIN" "$TMP/hygiene-build-only.out"; then
  echo "FAIL: prod --build-only hygiene failure should not reach rsync or ssh"
  fail=1
else
  echo "PASS: prod --build-only hygiene failure stops before rsync and ssh"
fi

if grep -q "build-mode-secret" "$TMP/hygiene-build-only.out"; then
  echo "FAIL: prod --build-only hygiene output leaked secret value"
  fail=1
else
  echo "PASS: prod --build-only hygiene output does not leak secret value"
fi

HYGIENE_FULL_REPO="$TMP/hygiene-full-repo"
make_repo "$HYGIENE_FULL_REPO"
(
  cd "$HYGIENE_FULL_REPO" || exit 2
  cat > config_prod.yaml <<'YAML' || exit 2
service:
  api_key: full-mode-secret
YAML
  git add config_prod.yaml || exit 2
  git commit -q -m "add secret prod config" || exit 2
  git tag v1.2.6 || exit 2
) || die "setup full hygiene repo"
run_release_mode "$HYGIENE_FULL_REPO" "$TMP/hygiene-full.out" "full"
hygiene_full_rc=$?

if [ "$hygiene_full_rc" -eq 1 ] && grep -q "prod-secret-hygiene blocked release" "$TMP/hygiene-full.out"; then
  echo "PASS: prod full release runs secret hygiene before rsync"
else
  echo "FAIL: prod full release should run secret hygiene before rsync"
  fail=1
fi

if grep -q "RSYNC_ARGS_BEGIN" "$TMP/hygiene-full.out" || grep -q "SSH_ARGS_BEGIN" "$TMP/hygiene-full.out"; then
  echo "FAIL: prod full hygiene failure should not reach rsync or ssh"
  fail=1
else
  echo "PASS: prod full hygiene failure stops before rsync and ssh"
fi

if grep -q "full-mode-secret" "$TMP/hygiene-full.out"; then
  echo "FAIL: prod full hygiene output leaked secret value"
  fail=1
else
  echo "PASS: prod full hygiene output does not leak secret value"
fi

DEPLOY_ONLY_REPO="$TMP/deploy-only-repo"
make_repo "$DEPLOY_ONLY_REPO"
(
  cd "$DEPLOY_ONLY_REPO" || exit 2
  cat > config_prod.yaml <<'YAML' || exit 2
database:
  password: deploy-only-secret
YAML
  git add config_prod.yaml || exit 2
  git commit -q -m "add secret prod config" || exit 2
  git tag v1.2.7 || exit 2
) || die "setup deploy-only repo"
run_release_mode "$DEPLOY_ONLY_REPO" "$TMP/deploy-only.out" "--deploy-only"
deploy_only_rc=$?

if [ "$deploy_only_rc" -eq 78 ] && grep -q "SSH_ARGS_BEGIN" "$TMP/deploy-only.out"; then
  echo "PASS: prod --deploy-only skips local config hygiene and reaches deploy ssh"
else
  echo "FAIL: prod --deploy-only should skip local config hygiene and reach deploy ssh"
  fail=1
fi

if grep -q "prod-secret-hygiene blocked release" "$TMP/deploy-only.out"; then
  echo "FAIL: prod --deploy-only should not run secret hygiene"
  fail=1
else
  echo "PASS: prod --deploy-only does not run secret hygiene"
fi

if grep -q "deploy-only-secret" "$TMP/deploy-only.out"; then
  echo "FAIL: prod --deploy-only output leaked secret value"
  fail=1
else
  echo "PASS: prod --deploy-only output does not leak secret value"
fi

QA_COMPAT_REPO="$TMP/qa-compat-repo"
make_repo "$QA_COMPAT_REPO"
(
  cd "$QA_COMPAT_REPO" || exit 2
  printf 'qa local env\n' > .env || exit 2
) || die "setup qa compatibility repo"
run_release_env qa "$QA_COMPAT_REPO" "$TMP/qa-compat.out"
qa_compat_rc=$?

if [ "$qa_compat_rc" -eq 77 ]; then
  echo "PASS: qa release still reaches fake rsync for compatibility inspection"
else
  echo "FAIL: qa release should reach fake rsync (rc=$qa_compat_rc)"
  fail=1
fi

if grep -Fqx -- "--exclude=/data/" "$TMP/qa-compat.out"; then
  echo "PASS: qa rsync excludes root data directory"
else
  echo "FAIL: qa rsync output missing --exclude=/data/"
  fail=1
fi

for pattern in \
  "--exclude=.env" \
  "--exclude=.env.*" \
  "--exclude=secrets.env" \
  "--exclude=*.pem" \
  "--exclude=*.key" \
  "--exclude=*.key.*" \
  "--exclude=*.crt" \
  "--exclude=*.crt.*" \
  "--exclude=configs/cert/" \
  "--exclude=configs/ssl/" \
  "--exclude=/config_dev.yaml" \
  "--exclude=/config_qa.yaml" \
  "--exclude=/config_local.yaml" \
  "--exclude=/config_*.local.yaml"
do
  if grep -Fqx -- "$pattern" "$TMP/qa-compat.out"; then
    echo "FAIL: qa release should not include prod-only secret exclude $pattern"
    fail=1
  else
    echo "PASS: qa release omits prod-only secret exclude $pattern"
  fi
done

DEPLOY_REMOTE_BIN="$TMP/deploy-remote-bin"
mkdir -p "$DEPLOY_REMOTE_BIN" || die "mkdir deploy-remote bin"
cat > "$DEPLOY_REMOTE_BIN/docker" <<'DOCKER' || die "write fake deploy-remote docker"
#!/usr/bin/env bash
printf '%s\n' "$*" >> "${FAKE_DOCKER_RUN_LOG:?}"
case "${1:-}" in
  inspect) exit 1 ;;
  pull|network|rm|image|images|rmi|ps|logs) exit 0 ;;
  run) exit 0 ;;
  *) exit 0 ;;
esac
DOCKER
chmod +x "$DEPLOY_REMOTE_BIN/docker" || die "chmod fake deploy-remote docker"
printf '#!/usr/bin/env bash\nexit 0\n' > "$DEPLOY_REMOTE_BIN/curl" || die "write fake deploy-remote curl"
chmod +x "$DEPLOY_REMOTE_BIN/curl" || die "chmod fake deploy-remote curl"
cat > "$DEPLOY_REMOTE_BIN/sudo" <<'SUDO' || die "write fake deploy-remote sudo"
#!/usr/bin/env bash
case "${1:-}" in
  mkdir|chown|chmod)
    exit 0
    ;;
  *)
    exec "$@"
    ;;
esac
SUDO
chmod +x "$DEPLOY_REMOTE_BIN/sudo" || die "chmod fake deploy-remote sudo"

make_unix_socket() {
  local socket_path="$1"
  python3 - "$socket_path" <<'PY'
import socket
import sys
s = socket.socket(socket.AF_UNIX)
s.bind(sys.argv[1])
s.close()
PY
}

ADMIN_SECRETS_FILE="$TMP/admin-prod-secrets.env"
printf 'NUMIND_FAKE_SECRET=\n' > "$ADMIN_SECRETS_FILE" || die "write fake admin secrets file"
ADMIN_DOCKER_RUN_LOG="$TMP/admin-docker-run.log"
(
  PATH="$DEPLOY_REMOTE_BIN:$PATH" \
  FAKE_DOCKER_RUN_LOG="$ADMIN_DOCKER_RUN_LOG" \
  ENV=prod TARGET=admin IMAGE="example.invalid/numind-admin:prod-test" \
  SECRETS_FILE="$ADMIN_SECRETS_FILE" \
  REQUIRE_PROD_SECRETS_ENV=0 \
  NUMIND_SANDBOX_BACKEND=broker \
  NUMIND_SANDBOX_BROKER_SOCKET=/var/run/docker.sock \
  NUMIND_SANDBOX_BROKER_OWNER_ID=admin-should-not-use \
  NUMIND_SANDBOX_BROKER_GID=1999 \
    bash "$DEPLOY_REMOTE_SH"
) > "$TMP/admin-deploy-remote.out" 2>&1
admin_deploy_remote_rc=$?

if [ "$admin_deploy_remote_rc" -eq 0 ]; then
  echo "PASS: prod admin deploy-remote succeeds with fake docker"
else
  echo "FAIL: prod admin deploy-remote should succeed with fake docker (rc=$admin_deploy_remote_rc)"
  fail=1
fi

if grep -Fq -- "--env-file $ADMIN_SECRETS_FILE" "$ADMIN_DOCKER_RUN_LOG"; then
  echo "PASS: prod admin docker run includes --env-file"
else
  echo "FAIL: prod admin docker run missing --env-file"
  fail=1
fi

if grep -Fq "Secrets: $ADMIN_SECRETS_FILE (loaded)" "$TMP/admin-deploy-remote.out"; then
  echo "PASS: prod admin deploy output reports loaded secrets env-file"
else
  echo "FAIL: prod admin deploy output missing loaded secrets env-file"
  fail=1
fi

if grep -Fq -- "-e NUMIND_SANDBOX_BACKEND=disabled" "$ADMIN_DOCKER_RUN_LOG"; then
  echo "PASS: prod admin docker run forces Sandbox disabled"
else
  echo "FAIL: prod admin docker run should force Sandbox disabled"
  fail=1
fi

if grep -Eq -- 'docker\.sock|sandboxd\.sock|--group-add' "$ADMIN_DOCKER_RUN_LOG"; then
  echo "FAIL: prod admin docker run must not mount broker/Docker socket or add Sandbox group"
  fail=1
else
  echo "PASS: prod admin docker run has no broker/Docker socket or Sandbox group"
fi

SERVER_BROKER_SOCKET="$SOCKET_TMP/sandboxd.sock"
make_unix_socket "$SERVER_BROKER_SOCKET" || die "create fake broker socket"
SERVER_BROKER_SECRETS_FILE="$TMP/server-broker-prod-secrets.env"
printf 'NUMIND_FAKE_SECRET=\n' > "$SERVER_BROKER_SECRETS_FILE" || die "write fake server broker secrets file"
SERVER_BROKER_DOCKER_RUN_LOG="$TMP/server-broker-docker-run.log"
(
  PATH="$DEPLOY_REMOTE_BIN:$PATH" \
  FAKE_DOCKER_RUN_LOG="$SERVER_BROKER_DOCKER_RUN_LOG" \
  ENV=prod TARGET=server IMAGE="example.invalid/numind-server:broker-test" \
  SECRETS_FILE="$SERVER_BROKER_SECRETS_FILE" \
  REQUIRE_PROD_SECRETS_ENV=0 \
  NUMIND_SANDBOX_BACKEND=broker \
  NUMIND_SANDBOX_BROKER_SOCKET="$SERVER_BROKER_SOCKET" \
  NUMIND_SANDBOX_BROKER_OWNER_ID=numind-user-api-primary \
  NUMIND_SANDBOX_BROKER_GID=1999 \
    bash "$DEPLOY_REMOTE_SH"
) > "$TMP/server-broker-deploy-remote.out" 2>&1
server_broker_deploy_rc=$?

if [ "$server_broker_deploy_rc" -eq 0 ]; then
  echo "PASS: prod server broker deploy-remote succeeds with fake docker"
else
  echo "FAIL: prod server broker deploy-remote should succeed with fake docker (rc=$server_broker_deploy_rc)"
  fail=1
fi

if grep -Fq -- "-e NUMIND_SANDBOX_BACKEND=broker" "$SERVER_BROKER_DOCKER_RUN_LOG" &&
   grep -Fq -- "-e NUMIND_SANDBOX_BROKER_SOCKET=$SERVER_BROKER_SOCKET" "$SERVER_BROKER_DOCKER_RUN_LOG" &&
   grep -Fq -- "-e NUMIND_SANDBOX_BROKER_OWNER_ID=numind-user-api-primary" "$SERVER_BROKER_DOCKER_RUN_LOG"; then
  echo "PASS: prod server broker docker run injects Sandbox broker env"
else
  echo "FAIL: prod server broker docker run missing broker env"
  fail=1
fi

if grep -Fq -- "-v $SERVER_BROKER_SOCKET:$SERVER_BROKER_SOCKET" "$SERVER_BROKER_DOCKER_RUN_LOG" &&
   grep -Fq -- "--group-add 1999" "$SERVER_BROKER_DOCKER_RUN_LOG"; then
  echo "PASS: prod server broker docker run mounts only broker socket with dedicated group"
else
  echo "FAIL: prod server broker docker run missing broker socket mount or dedicated group"
  fail=1
fi

if grep -Fq -- "docker.sock" "$SERVER_BROKER_DOCKER_RUN_LOG"; then
  echo "FAIL: prod server broker docker run must not mount a Docker socket"
  fail=1
else
  echo "PASS: prod server broker docker run has no Docker socket"
fi

SERVER_DISABLED_DOCKER_RUN_LOG="$TMP/server-disabled-docker-run.log"
(
  PATH="$DEPLOY_REMOTE_BIN:$PATH" \
  FAKE_DOCKER_RUN_LOG="$SERVER_DISABLED_DOCKER_RUN_LOG" \
  ENV=prod TARGET=server IMAGE="example.invalid/numind-server:disabled-test" \
  SECRETS_FILE="$SERVER_BROKER_SECRETS_FILE" \
  REQUIRE_PROD_SECRETS_ENV=0 \
  NUMIND_SANDBOX_BACKEND=disabled \
    bash "$DEPLOY_REMOTE_SH"
) > "$TMP/server-disabled-deploy-remote.out" 2>&1
server_disabled_deploy_rc=$?

if [ "$server_disabled_deploy_rc" -eq 0 ]; then
  echo "PASS: prod server disabled deploy-remote succeeds without broker socket"
else
  echo "FAIL: prod server disabled deploy-remote should succeed without broker socket (rc=$server_disabled_deploy_rc)"
  fail=1
fi

if grep -Fq -- "-e NUMIND_SANDBOX_BACKEND=disabled" "$SERVER_DISABLED_DOCKER_RUN_LOG" &&
   ! grep -Eq -- 'docker\.sock|sandboxd\.sock|--group-add' "$SERVER_DISABLED_DOCKER_RUN_LOG"; then
  echo "PASS: prod server disabled docker run has no broker/Docker socket or Sandbox group"
else
  echo "FAIL: prod server disabled docker run should be Sandbox-disabled and socket-free"
  fail=1
fi

SERVER_DOCKER_SOCKET_DOCKER_RUN_LOG="$TMP/server-docker-socket-docker-run.log"
(
  PATH="$DEPLOY_REMOTE_BIN:$PATH" \
  FAKE_DOCKER_RUN_LOG="$SERVER_DOCKER_SOCKET_DOCKER_RUN_LOG" \
  ENV=prod TARGET=server IMAGE="example.invalid/numind-server:dangerous-socket-test" \
  SECRETS_FILE="$SERVER_BROKER_SECRETS_FILE" \
  REQUIRE_PROD_SECRETS_ENV=0 \
  NUMIND_SANDBOX_BACKEND=broker \
  NUMIND_SANDBOX_BROKER_SOCKET=/var/run/docker.sock \
  NUMIND_SANDBOX_BROKER_OWNER_ID=numind-user-api-primary \
  NUMIND_SANDBOX_BROKER_GID=1999 \
    bash "$DEPLOY_REMOTE_SH"
) > "$TMP/server-docker-socket-deploy-remote.out" 2>&1
server_docker_socket_deploy_rc=$?

if [ "$server_docker_socket_deploy_rc" -ne 0 ] &&
   grep -Fq "never a Docker socket" "$TMP/server-docker-socket-deploy-remote.out"; then
  echo "PASS: prod server broker deploy rejects Docker socket path"
else
  echo "FAIL: prod server broker deploy should reject Docker socket path"
  fail=1
fi

if grep -Eq '^pull( |$)|^run( |$)' "$SERVER_DOCKER_SOCKET_DOCKER_RUN_LOG" 2>/dev/null; then
  echo "FAIL: prod server Docker-socket rejection should happen before docker pull/run"
  fail=1
else
  echo "PASS: prod server Docker-socket rejection happens before docker pull/run"
fi

SANDBOXD_DEPLOY_BIN="$TMP/sandboxd-deploy-bin"
mkdir -p "$SANDBOXD_DEPLOY_BIN" || die "mkdir sandboxd deploy bin"
cat > "$SANDBOXD_DEPLOY_BIN/docker" <<'DOCKER' || die "write fake sandboxd docker"
#!/usr/bin/env bash
printf '%s\n' "$*" >> "${FAKE_DOCKER_RUN_LOG:?}"
hash_file() {
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum "$1" | awk '{print $1}'
  else
    shasum -a 256 "$1" | awk '{print $1}'
  fi
}
case "${1:-}" in
  pull)
    exit 0
    ;;
  create)
    echo "fake-sandboxd-container"
    exit 0
    ;;
  cp)
    src="${2:-}"
    dest="${3:-}"
    mkdir -p "$(dirname "$dest")"
    case "$src" in
      *:/app/numind-sandboxd)
        printf '#!/usr/bin/env sh\necho sandboxd-new "$@"\n' > "$dest"
        chmod +x "$dest"
        ;;
      *:/app/numind-sandbox-reconcile)
        printf '#!/usr/bin/env sh\necho reconcile-new "$@"\n' > "$dest"
        chmod +x "$dest"
        ;;
      *:/app/sandbox-artifacts.sha256)
        dir="$(dirname "$dest")"
        printf '%s  /out/numind-sandboxd\n' "$(hash_file "$dir/numind-sandboxd")" > "$dest"
        printf '%s  /out/numind-sandbox-reconcile\n' "$(hash_file "$dir/numind-sandbox-reconcile")" >> "$dest"
        ;;
      *)
        echo "unexpected docker cp source: $src" >&2
        exit 1
        ;;
    esac
    exit 0
    ;;
  rm|image)
    exit 0
    ;;
  *)
    exit 0
    ;;
esac
DOCKER
chmod +x "$SANDBOXD_DEPLOY_BIN/docker" || die "chmod fake sandboxd docker"

prepare_sandboxd_root() {
  local root="$1"
  mkdir -p "$root/sys/fs/cgroup" \
           "$root/opt/numind/prod" \
           "$root/opt/numind/config" \
           "$root/etc/ssl/certimate/youshu.asia" \
           "$root/var/lib/mysql" \
           "$root/var/lib/redis" \
           "$root/var/lib/docker" \
           "$root/var/run" \
           "$root/opt/numind-sandbox/bin" || die "prepare sandboxd fake root"
  printf 'memory cpu pids io\n' > "$root/sys/fs/cgroup/cgroup.controllers" || die "sandboxd cgroup"
  printf 'fake docker socket\n' > "$root/var/run/docker.sock" || die "sandboxd fake docker socket"
  printf 'secret\n' > "$root/opt/numind/prod/secrets.env" || die "sandboxd fake secret"
  chmod 700 "$root/opt/numind/prod" \
            "$root/opt/numind/config" \
            "$root/etc/ssl/certimate/youshu.asia" \
            "$root/var/lib/mysql" \
            "$root/var/lib/redis" \
            "$root/var/lib/docker" || die "sandboxd chmod protected dirs"
  chmod 600 "$root/var/run/docker.sock" "$root/opt/numind/prod/secrets.env" || die "sandboxd chmod protected files"
  printf '#!/usr/bin/env sh\necho sandboxd-old "$@"\n' > "$root/opt/numind-sandbox/bin/numind-sandboxd" || die "old sandboxd"
  printf '#!/usr/bin/env sh\necho reconcile-old "$@"\n' > "$root/opt/numind-sandbox/bin/numind-sandbox-reconcile" || die "old reconcile"
  chmod +x "$root/opt/numind-sandbox/bin/numind-sandboxd" "$root/opt/numind-sandbox/bin/numind-sandbox-reconcile" || die "chmod old binaries"
}

run_sandboxd_deploy() {
  local root="$1"
  local out="$2"
  local docker_log="$3"
  shift 3 || true
  local seccomp_hash
  seccomp_hash="$(sha256sum "$SCRIPT_DIR/../../deploy/sandbox/seccomp.json" | awk '{print $1}')"
  PATH="$SANDBOXD_DEPLOY_BIN:$PATH" \
  FAKE_DOCKER_RUN_LOG="$docker_log" \
  NUMIND_SANDBOX_TEST_MODE=1 \
  NUMIND_SANDBOX_DEPLOY_ROOT="$root" \
  NUMIND_SANDBOX_BROKER_ENV_FILE="$root/tmp/broker.env" \
  NUMIND_SANDBOX_READY_TRIES=1 \
  NUMIND_SANDBOX_READY_SLEEP_SECONDS=0 \
  NUMIND_SANDBOX_TEST_COMMANDS="slirp4netns newuidmap newgidmap dockerd rootlesskit" \
  NUMIND_SANDBOX_BACKEND=broker \
  NUMIND_SANDBOX_BROKER_INSTANCE=numind-prod-sandbox-primary \
  NUMIND_SANDBOX_API_HOST_UID=1001 \
  NUMIND_SANDBOX_IMAGE_DIGEST="ccr.ccs.tencentyun.com/youshunumind/sandbox-skill@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" \
  NUMIND_SANDBOX_SECCOMP_SHA256="sha256:${seccomp_hash}" \
  NUMIND_SANDBOX_PARENT_MEMORY_MAX_BYTES=2952790016 \
  NUMIND_SANDBOX_WORKLOAD_MEMORY_MAX_BYTES=2415919104 \
  NUMIND_SANDBOX_WORKLOAD_MEMORY_HIGH_BYTES=2147483648 \
  NUMIND_SANDBOX_WORKLOAD_MEMORY_RECOVERY_BYTES=1932735283 \
  NUMIND_SANDBOX_WORKLOAD_MEMORY_SHED_BYTES=2319282339 \
  NUMIND_SANDBOX_CONTROL_MEMORY_HIGH_BYTES=268435456 \
  NUMIND_SANDBOX_CONTROL_MEMORY_MAX_BYTES=402653184 \
  NUMIND_SANDBOX_PARENT_HEADROOM_BYTES=134217728 \
  env "$@" ENV=prod IMAGE="example.invalid/numind-server:sandboxd-test" bash "$DEPLOY_SANDBOX_REMOTE_SH" > "$out" 2>&1
}

SANDBOXD_SUCCESS_ROOT="$TMP/sandboxd-success-root"
prepare_sandboxd_root "$SANDBOXD_SUCCESS_ROOT"
SANDBOXD_SUCCESS_DOCKER_LOG="$TMP/sandboxd-success-docker.log"
run_sandboxd_deploy "$SANDBOXD_SUCCESS_ROOT" "$TMP/sandboxd-success.out" "$SANDBOXD_SUCCESS_DOCKER_LOG"
sandboxd_success_rc=$?

if [ "$sandboxd_success_rc" -eq 0 ] && grep -Fq "sandboxd deploy success" "$TMP/sandboxd-success.out"; then
  echo "PASS: sandboxd deploy succeeds after broker readiness"
else
  echo "FAIL: sandboxd deploy should succeed after broker readiness"
  fail=1
fi

if grep -Fq "sandboxd-new" "$SANDBOXD_SUCCESS_ROOT/opt/numind-sandbox/bin/numind-sandboxd" &&
   grep -Fq "NUMIND_SANDBOX_BROKER_GID=1999" "$SANDBOXD_SUCCESS_ROOT/tmp/broker.env" &&
   [ -f "$SANDBOXD_SUCCESS_ROOT/opt/numind-sandbox/seccomp/seccomp.json" ]; then
  echo "PASS: sandboxd deploy installs new binary and writes broker gid env"
else
  echo "FAIL: sandboxd deploy should install new binary, seccomp profile, and write broker gid env"
  fail=1
fi

if grep -Fq "systemctl stop numind-sandboxd" "$SANDBOXD_SUCCESS_ROOT/tmp/sandboxd-deploy.log" &&
   grep -Fq "systemctl restart numind-sandboxd" "$SANDBOXD_SUCCESS_ROOT/tmp/sandboxd-deploy.log" &&
   grep -Fq "curl /readyz" "$SANDBOXD_SUCCESS_ROOT/tmp/sandboxd-deploy.log" &&
   grep -Fq "image prune" "$SANDBOXD_SUCCESS_DOCKER_LOG"; then
  echo "PASS: sandboxd deploy drains, restarts, checks readiness, and only prunes unreferenced images"
else
  echo "FAIL: sandboxd deploy missing drain/restart/ready/prune evidence"
  fail=1
fi

SANDBOXD_FAIL_ROOT="$TMP/sandboxd-fail-root"
prepare_sandboxd_root "$SANDBOXD_FAIL_ROOT"
SANDBOXD_FAIL_DOCKER_LOG="$TMP/sandboxd-fail-docker.log"
set +e
run_sandboxd_deploy "$SANDBOXD_FAIL_ROOT" "$TMP/sandboxd-fail.out" "$SANDBOXD_FAIL_DOCKER_LOG" NUMIND_SANDBOX_TEST_FAIL_READY=1
sandboxd_fail_rc=$?
set +e

if [ "$sandboxd_fail_rc" -ne 0 ] && grep -Fq "user API deploy must not proceed" "$TMP/sandboxd-fail.out"; then
  echo "PASS: sandboxd deploy blocks user API when broker readiness fails"
else
  echo "FAIL: sandboxd deploy should block user API when broker readiness fails"
  fail=1
fi

if grep -Fq "sandboxd-old" "$SANDBOXD_FAIL_ROOT/opt/numind-sandbox/bin/numind-sandboxd" &&
   grep -Fq "reconcile dry-run" "$SANDBOXD_FAIL_ROOT/tmp/sandboxd-deploy.log"; then
  echo "PASS: sandboxd deploy restores old binary and runs reconcile dry-run on readiness failure"
else
  echo "FAIL: sandboxd deploy should restore old binary and run reconcile dry-run"
  fail=1
fi

PROD_SECRETS_CONFIG="$TMP/prod-secrets-config.yaml"
PROD_SECRETS_EXAMPLE="$TMP/prod-secrets.env.example"
PROD_SECRETS_VALID="$TMP/prod-secrets-valid.env"
cat > "$PROD_SECRETS_CONFIG" <<'YAML' || die "write prod secrets config"
jwt:
  secret: ${NUMIND_JWT_SECRET}
database:
  password: ${NUMIND_DB_PASSWORD}
YAML
cat > "$PROD_SECRETS_EXAMPLE" <<'ENVEXAMPLE' || die "write prod secrets example"
NUMIND_JWT_SECRET=
NUMIND_DB_PASSWORD=
ENVEXAMPLE
cat > "$PROD_SECRETS_VALID" <<'ENVFILE' || die "write prod secrets valid env"
NUMIND_JWT_SECRET=fixture-jwt-secret
NUMIND_DB_PASSWORD=fixture-db-secret
ENVFILE
chmod 600 "$PROD_SECRETS_VALID" || die "chmod prod secrets valid env"

PROD_SECRETS_DOCKER_RUN_LOG="$TMP/prod-secrets-docker-run.log"
(
  PATH="$DEPLOY_REMOTE_BIN:$PATH" \
  FAKE_DOCKER_RUN_LOG="$PROD_SECRETS_DOCKER_RUN_LOG" \
  ENV=prod TARGET=admin IMAGE="example.invalid/numind-admin:prod-secrets-test" \
  SECRETS_FILE="$PROD_SECRETS_VALID" \
  PROD_SECRETS_CHECK_SCRIPT="$PROD_SECRETS_ENV_SH" \
  PROD_SECRETS_CONFIG_FILE="$PROD_SECRETS_CONFIG" \
  PROD_SECRETS_EXAMPLE="$PROD_SECRETS_EXAMPLE" \
    bash "$DEPLOY_REMOTE_SH"
) > "$TMP/prod-secrets-deploy-remote.out" 2>&1
prod_secrets_deploy_rc=$?

if [ "$prod_secrets_deploy_rc" -eq 0 ]; then
  echo "PASS: prod deploy-remote validates complete secrets env-file"
else
  echo "FAIL: prod deploy-remote should accept complete secrets env-file (rc=$prod_secrets_deploy_rc)"
  fail=1
fi

if grep -Fq "prod-secrets-env: checked" "$TMP/prod-secrets-deploy-remote.out"; then
  echo "PASS: prod deploy-remote runs prod secrets env-file checker"
else
  echo "FAIL: prod deploy-remote output missing prod secrets checker evidence"
  fail=1
fi

if grep -Eq 'fixture-jwt-secret|fixture-db-secret' "$TMP/prod-secrets-deploy-remote.out"; then
  echo "FAIL: prod deploy-remote leaked fixture secret values"
  fail=1
else
  echo "PASS: prod deploy-remote does not leak fixture secret values"
fi

MISSING_PROD_SECRETS_DOCKER_RUN_LOG="$TMP/missing-prod-secrets-docker-run.log"
(
  PATH="$DEPLOY_REMOTE_BIN:$PATH" \
  FAKE_DOCKER_RUN_LOG="$MISSING_PROD_SECRETS_DOCKER_RUN_LOG" \
  ENV=prod TARGET=admin IMAGE="example.invalid/numind-admin:missing-secrets-test" \
  SECRETS_FILE="$TMP/missing-prod-secrets.env" \
  PROD_SECRETS_CHECK_SCRIPT="$PROD_SECRETS_ENV_SH" \
  PROD_SECRETS_CONFIG_FILE="$PROD_SECRETS_CONFIG" \
  PROD_SECRETS_EXAMPLE="$PROD_SECRETS_EXAMPLE" \
    bash "$DEPLOY_REMOTE_SH"
) > "$TMP/missing-prod-secrets-deploy-remote.out" 2>&1
missing_prod_secrets_deploy_rc=$?

if [ "$missing_prod_secrets_deploy_rc" -ne 0 ]; then
  echo "PASS: prod deploy-remote rejects missing secrets env-file"
else
  echo "FAIL: prod deploy-remote should reject missing secrets env-file"
  fail=1
fi

if grep -Fq "secrets env-file not found" "$TMP/missing-prod-secrets-deploy-remote.out"; then
  echo "PASS: prod deploy-remote explains missing secrets env-file"
else
  echo "FAIL: prod deploy-remote output missing missing-secrets explanation"
  fail=1
fi

if grep -Eq '^pull( |$)' "$MISSING_PROD_SECRETS_DOCKER_RUN_LOG" 2>/dev/null; then
  echo "FAIL: prod deploy-remote should fail before docker pull when secrets are missing"
  fail=1
else
  echo "PASS: prod deploy-remote fails before docker pull when secrets are missing"
fi

if grep -Eq '^run( |$)' "$MISSING_PROD_SECRETS_DOCKER_RUN_LOG" 2>/dev/null; then
  echo "FAIL: prod deploy-remote should fail before docker run when secrets are missing"
  fail=1
else
  echo "PASS: prod deploy-remote fails before docker run when secrets are missing"
fi

PROD_SECRETS_BAD_MODE="$TMP/prod-secrets-bad-mode.env"
cp "$PROD_SECRETS_VALID" "$PROD_SECRETS_BAD_MODE" || die "copy prod secrets bad mode"
chmod 644 "$PROD_SECRETS_BAD_MODE" || die "chmod prod secrets bad mode"
BAD_MODE_PROD_SECRETS_DOCKER_RUN_LOG="$TMP/bad-mode-prod-secrets-docker-run.log"
(
  PATH="$DEPLOY_REMOTE_BIN:$PATH" \
  FAKE_DOCKER_RUN_LOG="$BAD_MODE_PROD_SECRETS_DOCKER_RUN_LOG" \
  ENV=prod TARGET=admin IMAGE="example.invalid/numind-admin:bad-mode-secrets-test" \
  SECRETS_FILE="$PROD_SECRETS_BAD_MODE" \
  PROD_SECRETS_CHECK_SCRIPT="$PROD_SECRETS_ENV_SH" \
  PROD_SECRETS_CONFIG_FILE="$PROD_SECRETS_CONFIG" \
  PROD_SECRETS_EXAMPLE="$PROD_SECRETS_EXAMPLE" \
    bash "$DEPLOY_REMOTE_SH"
) > "$TMP/bad-mode-prod-secrets-deploy-remote.out" 2>&1
bad_mode_prod_secrets_deploy_rc=$?

if [ "$bad_mode_prod_secrets_deploy_rc" -ne 0 ]; then
  echo "PASS: prod deploy-remote rejects group/world readable secrets env-file"
else
  echo "FAIL: prod deploy-remote should reject group/world readable secrets env-file"
  fail=1
fi

if grep -Fq "chmod 600" "$TMP/bad-mode-prod-secrets-deploy-remote.out"; then
  echo "PASS: prod deploy-remote explains bad secrets env-file mode"
else
  echo "FAIL: prod deploy-remote output missing bad-mode explanation"
  fail=1
fi

if grep -Eq '^pull( |$)' "$BAD_MODE_PROD_SECRETS_DOCKER_RUN_LOG" 2>/dev/null; then
  echo "FAIL: prod deploy-remote should fail before docker pull when secrets mode is unsafe"
  fail=1
else
  echo "PASS: prod deploy-remote fails before docker pull when secrets mode is unsafe"
fi

if grep -Eq '^run( |$)' "$BAD_MODE_PROD_SECRETS_DOCKER_RUN_LOG" 2>/dev/null; then
  echo "FAIL: prod deploy-remote should fail before docker run when secrets mode is unsafe"
  fail=1
else
  echo "PASS: prod deploy-remote fails before docker run when secrets mode is unsafe"
fi

SERVER_POST_CHMOD_BIN="$TMP/server-post-chmod-bin"
mkdir -p "$SERVER_POST_CHMOD_BIN" || die "mkdir server post-chmod bin"
cp "$DEPLOY_REMOTE_BIN/docker" "$SERVER_POST_CHMOD_BIN/docker" || die "copy fake docker"
cp "$DEPLOY_REMOTE_BIN/curl" "$SERVER_POST_CHMOD_BIN/curl" || die "copy fake curl"
cat > "$SERVER_POST_CHMOD_BIN/chmod" <<'CHMOD' || die "write fake chmod"
#!/usr/bin/env bash
if [ "${1:-}" = "600" ] && [ "${2:-}" = "${FAIL_CHMOD_PATH:-}" ]; then
  echo "chmod: fixture failure for $2" >&2
  exit 1
fi
exec /bin/chmod "$@"
CHMOD
chmod +x "$SERVER_POST_CHMOD_BIN/chmod" || die "chmod fake chmod"
cat > "$SERVER_POST_CHMOD_BIN/sudo" <<'SUDO' || die "write fake sudo"
#!/usr/bin/env bash
case "${1:-}" in
  mkdir)
    exit 0
    ;;
  chown)
    exit 0
    ;;
  chmod)
    if [ "${2:-}" = "600" ] && [ "${3:-}" = "${FAIL_CHMOD_PATH:-}" ]; then
      echo "sudo chmod: fixture failure for $3" >&2
      exit 1
    fi
    exit 0
    ;;
  *)
    exec "$@"
    ;;
esac
SUDO
chmod +x "$SERVER_POST_CHMOD_BIN/sudo" || die "chmod fake sudo"

SERVER_POST_CHMOD_SECRETS="$TMP/server-post-chmod-secrets.env"
cp "$PROD_SECRETS_VALID" "$SERVER_POST_CHMOD_SECRETS" || die "copy server post-chmod secrets"
chmod 600 "$SERVER_POST_CHMOD_SECRETS" || die "chmod server post-chmod secrets"
SERVER_POST_CHMOD_DOCKER_RUN_LOG="$TMP/server-post-chmod-docker-run.log"
(
  PATH="$SERVER_POST_CHMOD_BIN:$PATH" \
  FAKE_DOCKER_RUN_LOG="$SERVER_POST_CHMOD_DOCKER_RUN_LOG" \
  FAIL_CHMOD_PATH="$SERVER_POST_CHMOD_SECRETS" \
  ENV=prod TARGET=server IMAGE="example.invalid/numind-server:post-chmod-test" \
  SECRETS_FILE="$SERVER_POST_CHMOD_SECRETS" \
  PROD_SECRETS_CHECK_SCRIPT="$PROD_SECRETS_ENV_SH" \
  PROD_SECRETS_CONFIG_FILE="$PROD_SECRETS_CONFIG" \
  PROD_SECRETS_EXAMPLE="$PROD_SECRETS_EXAMPLE" \
    bash "$DEPLOY_REMOTE_SH"
) > "$TMP/server-post-chmod-deploy-remote.out" 2>&1
server_post_chmod_rc=$?

if [ "$server_post_chmod_rc" -ne 0 ]; then
  echo "PASS: prod server deploy-remote fails when post-validation secrets chmod fails"
else
  echo "FAIL: prod server deploy-remote should fail when post-validation secrets chmod fails"
  fail=1
fi

if grep -Fq "fixture failure" "$TMP/server-post-chmod-deploy-remote.out"; then
  echo "PASS: prod server deploy-remote reports post-validation chmod failure"
else
  echo "FAIL: prod server deploy-remote output missing post-validation chmod failure"
  fail=1
fi

if grep -Eq '^pull( |$)' "$SERVER_POST_CHMOD_DOCKER_RUN_LOG" 2>/dev/null; then
  echo "PASS: prod server post-validation chmod check occurs after docker pull"
else
  echo "FAIL: prod server post-validation chmod regression did not reach docker pull"
  fail=1
fi

if grep -Eq '^run( |$)' "$SERVER_POST_CHMOD_DOCKER_RUN_LOG" 2>/dev/null; then
  echo "FAIL: prod server deploy-remote should fail before docker run when post-validation chmod fails"
  fail=1
else
  echo "PASS: prod server deploy-remote fails before docker run when post-validation chmod fails"
fi

echo
if [ "$fail" -ne 0 ]; then
  echo "---- tagged dirty release output ----"
  cat "$TMP/tagged.out" 2>/dev/null || true
  echo "---- untagged dirty release output ----"
  cat "$TMP/untagged.out" 2>/dev/null || true
  echo "---- clean tagged secret exclude output ----"
  cat "$TMP/secret-exclude.out" 2>/dev/null || true
  echo "---- hygiene build-only output ----"
  cat "$TMP/hygiene-build-only.out" 2>/dev/null || true
  echo "---- hygiene full output ----"
  cat "$TMP/hygiene-full.out" 2>/dev/null || true
  echo "---- deploy-only output ----"
  cat "$TMP/deploy-only.out" 2>/dev/null || true
  echo "---- qa compatibility output ----"
  cat "$TMP/qa-compat.out" 2>/dev/null || true
  echo "---- admin deploy-remote output ----"
  cat "$TMP/admin-deploy-remote.out" 2>/dev/null || true
  echo "---- admin docker run log ----"
  cat "$ADMIN_DOCKER_RUN_LOG" 2>/dev/null || true
  echo "---- server broker deploy-remote output ----"
  cat "$TMP/server-broker-deploy-remote.out" 2>/dev/null || true
  echo "---- server broker docker run log ----"
  cat "$SERVER_BROKER_DOCKER_RUN_LOG" 2>/dev/null || true
  echo "---- server disabled deploy-remote output ----"
  cat "$TMP/server-disabled-deploy-remote.out" 2>/dev/null || true
  echo "---- server disabled docker run log ----"
  cat "$SERVER_DISABLED_DOCKER_RUN_LOG" 2>/dev/null || true
  echo "---- server docker socket deploy-remote output ----"
  cat "$TMP/server-docker-socket-deploy-remote.out" 2>/dev/null || true
  echo "---- server docker socket docker run log ----"
  cat "$SERVER_DOCKER_SOCKET_DOCKER_RUN_LOG" 2>/dev/null || true
  echo "---- sandboxd success output ----"
  cat "$TMP/sandboxd-success.out" 2>/dev/null || true
  echo "---- sandboxd success deploy log ----"
  cat "$SANDBOXD_SUCCESS_ROOT/tmp/sandboxd-deploy.log" 2>/dev/null || true
  echo "---- sandboxd success docker log ----"
  cat "$SANDBOXD_SUCCESS_DOCKER_LOG" 2>/dev/null || true
  echo "---- sandboxd fail output ----"
  cat "$TMP/sandboxd-fail.out" 2>/dev/null || true
  echo "---- sandboxd fail deploy log ----"
  cat "$SANDBOXD_FAIL_ROOT/tmp/sandboxd-deploy.log" 2>/dev/null || true
  echo "---- sandboxd fail docker log ----"
  cat "$SANDBOXD_FAIL_DOCKER_LOG" 2>/dev/null || true
  echo "---- prod secrets deploy-remote output ----"
  cat "$TMP/prod-secrets-deploy-remote.out" 2>/dev/null || true
  echo "---- missing prod secrets deploy-remote output ----"
  cat "$TMP/missing-prod-secrets-deploy-remote.out" 2>/dev/null || true
  echo "---- missing prod secrets docker run log ----"
  cat "$MISSING_PROD_SECRETS_DOCKER_RUN_LOG" 2>/dev/null || true
  echo "---- bad mode prod secrets deploy-remote output ----"
  cat "$TMP/bad-mode-prod-secrets-deploy-remote.out" 2>/dev/null || true
  echo "---- bad mode prod secrets docker run log ----"
  cat "$BAD_MODE_PROD_SECRETS_DOCKER_RUN_LOG" 2>/dev/null || true
  echo "---- server post-chmod deploy-remote output ----"
  cat "$TMP/server-post-chmod-deploy-remote.out" 2>/dev/null || true
  echo "---- server post-chmod docker run log ----"
  cat "$SERVER_POST_CHMOD_DOCKER_RUN_LOG" 2>/dev/null || true
  echo "--------------------------------------"
  echo "release preflight test FAILED"
  exit 1
fi

echo "release preflight test PASSED"
