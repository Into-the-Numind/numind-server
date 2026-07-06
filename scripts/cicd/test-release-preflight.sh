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

die() { echo "test setup error: $1" >&2; exit 2; }

[ -f "$RELEASE_SH" ] || die "release.sh not found at $RELEASE_SH"

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
  (
    cd "$repo" || exit 2
    git init -q || exit 2
    git config user.email test@example.com || exit 2
    git config user.name "Release Preflight Test" || exit 2
    git add scripts/cicd/release.sh || exit 2
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
  (
    cd "$repo" || exit 2
    PATH="$TMP/bin:$PATH" TMPDIR="$TMP/locks" \
      bash scripts/cicd/release.sh "$env" server --build-only
  ) > "$out" 2>&1
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
  "--exclude=configs/ssl/"
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
  "--exclude=configs/ssl/"
do
  if grep -Fqx -- "$pattern" "$TMP/qa-compat.out"; then
    echo "FAIL: qa release should not include prod-only secret exclude $pattern"
    fail=1
  else
    echo "PASS: qa release omits prod-only secret exclude $pattern"
  fi
done

echo
if [ "$fail" -ne 0 ]; then
  echo "---- tagged dirty release output ----"
  cat "$TMP/tagged.out" 2>/dev/null || true
  echo "---- untagged dirty release output ----"
  cat "$TMP/untagged.out" 2>/dev/null || true
  echo "---- clean tagged secret exclude output ----"
  cat "$TMP/secret-exclude.out" 2>/dev/null || true
  echo "---- qa compatibility output ----"
  cat "$TMP/qa-compat.out" 2>/dev/null || true
  echo "--------------------------------------"
  echo "release preflight test FAILED"
  exit 1
fi

echo "release preflight test PASSED"
