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
SECRET_HYGIENE_SH="$(cd "$SCRIPT_DIR/.." && pwd)/check_prod_secret_hygiene.sh"

die() { echo "test setup error: $1" >&2; exit 2; }

[ -f "$RELEASE_SH" ] || die "release.sh not found at $RELEASE_SH"
[ -f "$DEPLOY_REMOTE_SH" ] || die "deploy-remote.sh not found at $DEPLOY_REMOTE_SH"
[ -f "$SECRET_HYGIENE_SH" ] || die "check_prod_secret_hygiene.sh not found at $SECRET_HYGIENE_SH"

TMP="$(mktemp -d)" || die "mktemp"
trap 'rm -rf "$TMP"' EXIT

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

  if grep -q "prod release requires a clean worktree and exact tag" "$out"; then
    echo "PASS: $label output explains clean worktree and exact tag requirement"
  else
    echo "FAIL: $label output missing clean worktree/exact tag error"
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

ADMIN_SECRETS_FILE="$TMP/admin-prod-secrets.env"
printf 'NUMIND_FAKE_SECRET=\n' > "$ADMIN_SECRETS_FILE" || die "write fake admin secrets file"
ADMIN_DOCKER_RUN_LOG="$TMP/admin-docker-run.log"
(
  PATH="$DEPLOY_REMOTE_BIN:$PATH" \
  FAKE_DOCKER_RUN_LOG="$ADMIN_DOCKER_RUN_LOG" \
  ENV=prod TARGET=admin IMAGE="example.invalid/numind-admin:prod-test" \
  SECRETS_FILE="$ADMIN_SECRETS_FILE" \
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
  echo "--------------------------------------"
  echo "release preflight test FAILED"
  exit 1
fi

echo "release preflight test PASSED"
