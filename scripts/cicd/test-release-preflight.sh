#!/usr/bin/env bash
# Regression test for prod release preflight.
#
# A prod release must represent the exact tagged commit. This test creates a
# temporary git repo where HEAD is on a tag but an untracked file exists; the
# release script must fail before rsync/build/deploy can observe that worktree.
#
# Run: bash scripts/cicd/test-release-preflight.sh
set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
RELEASE_SH="$SCRIPT_DIR/release.sh"

die() { echo "test setup error: $1" >&2; exit 2; }

[ -f "$RELEASE_SH" ] || die "release.sh not found at $RELEASE_SH"

TMP="$(mktemp -d)" || die "mktemp"
trap 'rm -rf "$TMP"' EXIT

mkdir -p "$TMP/repo/scripts/cicd" "$TMP/bin" "$TMP/locks" || die "mkdir temp dirs"
cp "$RELEASE_SH" "$TMP/repo/scripts/cicd/release.sh" || die "copy release.sh"

cat > "$TMP/bin/rsync" <<'RSYNC' || die "write fake rsync"
#!/usr/bin/env bash
echo "ERROR: rsync should not run for dirty prod preflight" >&2
exit 77
RSYNC
chmod +x "$TMP/bin/rsync" || die "chmod fake rsync"

cat > "$TMP/bin/ssh" <<'SSH' || die "write fake ssh"
#!/usr/bin/env bash
echo "ERROR: ssh should not run for dirty prod preflight" >&2
exit 78
SSH
chmod +x "$TMP/bin/ssh" || die "chmod fake ssh"

(
  cd "$TMP/repo" || exit 2
  git init -q || exit 2
  git config user.email test@example.com || exit 2
  git config user.name "Release Preflight Test" || exit 2
  git add scripts/cicd/release.sh || exit 2
  git commit -q -m "seed release script" || exit 2
  git tag v1.2.3 || exit 2
  printf 'not in tag\n' > untracked-prod-input.txt || exit 2
) || die "setup temporary git repo"

(
  cd "$TMP/repo" || exit 2
  PATH="$TMP/bin:$PATH" TMPDIR="$TMP/locks" \
    bash scripts/cicd/release.sh prod server --build-only
) > "$TMP/out" 2>&1
rc=$?

fail=0
if [ "$rc" -ne 0 ]; then
  echo "PASS: dirty tagged prod release exits non-zero (rc=$rc)"
else
  echo "FAIL: dirty tagged prod release should exit non-zero"
  fail=1
fi

if grep -q "prod release requires a clean worktree and exact tag" "$TMP/out"; then
  echo "PASS: output explains clean worktree and exact tag requirement"
else
  echo "FAIL: output missing clean worktree/exact tag error"
  fail=1
fi

if grep -q "untracked-prod-input.txt" "$TMP/out"; then
  echo "PASS: output lists the dirty item"
else
  echo "FAIL: output missing untracked dirty item"
  fail=1
fi

if grep -q "rsync should not run" "$TMP/out"; then
  echo "FAIL: rsync ran before prod preflight rejected the worktree"
  fail=1
else
  echo "PASS: rsync was not reached"
fi

if grep -q "ssh should not run" "$TMP/out"; then
  echo "FAIL: ssh ran before prod preflight rejected the worktree"
  fail=1
else
  echo "PASS: ssh was not reached"
fi

echo
if [ "$fail" -ne 0 ]; then
  echo "---- release output ----"
  cat "$TMP/out"
  echo "------------------------"
  echo "release preflight test FAILED"
  exit 1
fi

echo "release preflight test PASSED"
