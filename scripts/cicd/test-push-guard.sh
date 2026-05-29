#!/usr/bin/env bash
# Regression test for the TCR push-guard in build-and-push.sh.
#
# Reproduces the 2026-05-29 silent no-op deploy: `docker push` returns exit 0
# while TCR denies the new tag (repo at its 100-tag limit:
# "denied: ...tag has reached its limit(100)..."). Because the command exits 0,
# `set -euo pipefail` never fires, build-and-push.sh prints "Pushed:" and
# release.sh prints "✅ Release complete" — having shipped nothing.
#
# This test stubs `docker` and asserts build-and-push.sh:
#   silent-deny -> exits NON-ZERO, does NOT print "Pushed:"   (the bug)
#   hard-fail   -> exits NON-ZERO                             (sanity)
#   ok          -> exits ZERO, prints "Pushed:"               (happy path)
#
# Run: bash scripts/cicd/test-push-guard.sh
set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
BUILD_AND_PUSH="$SCRIPT_DIR/build-and-push.sh"

TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

# --- Fake `docker` on PATH. Distinguishes build vs push; push behaviour is
#     driven by FAKE_PUSH_MODE so each case exercises one registry response. ---
mkdir -p "$TMP/bin"
cat > "$TMP/bin/docker" <<'DOCKER'
#!/usr/bin/env bash
case "$1" in
  build) exit 0 ;;
  push)
    case "${FAKE_PUSH_MODE:-ok}" in
      silent-deny)
        # The dangerous case: registry denies but `docker push` exits 0.
        echo "The push refers to repository [$2]"
        echo "denied: requested access to the resource is denied: tag has reached its limit(100)" >&2
        exit 0 ;;
      hard-fail)
        echo "denied: tag has reached its limit(100)" >&2
        exit 1 ;;
      *)
        echo "develop: digest: sha256:deadbeefcafe size: 4242"
        exit 0 ;;
    esac ;;
  *) exit 0 ;;
esac
DOCKER
chmod +x "$TMP/bin/docker"

# --- Fake HOME with a TCR-logged-in docker config (build-and-push.sh greps it). ---
mkdir -p "$TMP/home/.docker"
echo '{ "auths": { "ccr.ccs.tencentyun.com": {} } }' > "$TMP/home/.docker/config.json"

# --- Fake build context (both Dockerfile names so any target works). ---
mkdir -p "$TMP/ctx"
echo "FROM scratch" > "$TMP/ctx/Dockerfile"
echo "FROM scratch" > "$TMP/ctx/Dockerfile.admin"

run_case() {
  local mode="$1"
  (
    cd "$TMP/ctx" \
      && PATH="$TMP/bin:$PATH" HOME="$TMP/home" \
         GIT_SHA="testsha" FAKE_PUSH_MODE="$mode" \
         bash "$BUILD_AND_PUSH" dev
  ) > "$TMP/out.$mode" 2>&1
  echo $?
}

fail=0
note_pass() { echo "PASS: $1"; }
note_fail() { echo "FAIL: $1"; fail=1; }

# --- silent-deny: the regression under test. ---
rc=$(run_case silent-deny)
if [ "$rc" -ne 0 ]; then note_pass "silent TCR denial -> non-zero exit (rc=$rc)"
else note_fail "silent TCR denial -> expected non-zero exit, got 0"; fi
if grep -q "Pushed:" "$TMP/out.silent-deny"; then
  note_fail "silent TCR denial must NOT print 'Pushed:'"
else
  note_pass "silent TCR denial did not print 'Pushed:'"
fi

# --- hard-fail: push exits non-zero (set -e already handles this; assert it). ---
rc=$(run_case hard-fail)
if [ "$rc" -ne 0 ]; then note_pass "hard push failure -> non-zero exit (rc=$rc)"
else note_fail "hard push failure -> expected non-zero exit, got 0"; fi

# --- ok: happy path must still succeed. ---
rc=$(run_case ok)
if [ "$rc" -eq 0 ]; then note_pass "successful push -> zero exit"
else note_fail "successful push -> expected zero exit, got $rc"; fi
if grep -q "Pushed:" "$TMP/out.ok"; then
  note_pass "successful push printed 'Pushed:'"
else
  note_fail "successful push should print 'Pushed:'"
fi

echo
if [ "$fail" -ne 0 ]; then
  echo "❌ push-guard test FAILED"
  exit 1
fi
echo "✅ push-guard test PASSED"
