#!/usr/bin/env bash
# Regression test for the TCR push-guard in build-and-push.sh.
#
# Reproduces the 2026-05-29 silent no-op deploy: `docker push` returns exit 0
# while TCR denies the new tag (repo at its 100-tag limit:
# "denied: ...tag has reached its limit(100)..."). Because the command exits 0,
# `set -euo pipefail` never fires, build-and-push.sh prints "Pushed:" and
# release.sh prints "✅ Release complete" — having shipped nothing.
#
# Cases (each stubs `docker push` differently via FAKE_PUSH_MODE):
#   silent-deny  -> denial on stderr, exit 0  -> MUST exit non-zero, no "Pushed:"  (the bug)
#   stdout-deny  -> denial on stdout, exit 0  -> MUST exit non-zero, no "Pushed:"
#   hard-fail    -> denial, exit 1            -> MUST exit non-zero                 (sanity)
#   transient-ok -> benign "too many open connections" notice + success, exit 0
#                                             -> MUST exit 0, prints "Pushed:"      (no false abort)
#   ok           -> clean success, exit 0     -> MUST exit 0, prints "Pushed:"      (happy path)
#
# Run: bash scripts/cicd/test-push-guard.sh
set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
BUILD_AND_PUSH="$SCRIPT_DIR/build-and-push.sh"

die() { echo "test setup error: $1" >&2; exit 2; }

TMP="$(mktemp -d)" || die "mktemp"
trap 'rm -rf "$TMP"' EXIT

# --- Fake `docker` on PATH. Distinguishes build vs push; push behaviour is
#     driven by FAKE_PUSH_MODE so each case exercises one registry response. ---
mkdir -p "$TMP/bin" || die "mkdir bin"
cat > "$TMP/bin/docker" <<'DOCKER' || die "write fake docker"
#!/usr/bin/env bash
case "$1" in
  build) exit 0 ;;
  push)
    case "${FAKE_PUSH_MODE:-ok}" in
      silent-deny)
        # The dangerous case: registry denies on stderr but `docker push` exits 0.
        echo "The push refers to repository [$2]"
        echo "denied: requested access to the resource is denied: tag has reached its limit(100)" >&2
        exit 0 ;;
      stdout-deny)
        # Same denial, but emitted on stdout (some clients/registries do this).
        echo "denied: tag has reached its limit(100)"
        exit 0 ;;
      hard-fail)
        echo "denied: tag has reached its limit(100)" >&2
        exit 1 ;;
      transient-ok)
        # Benign retry notice that contains the substring "too many" but is NOT
        # a registry denial; the push then succeeds. Must not trigger a false abort.
        echo "retrying in 1s: too many open connections" >&2
        echo "develop: digest: sha256:deadbeefcafe size: 4242"
        exit 0 ;;
      *)
        echo "develop: digest: sha256:deadbeefcafe size: 4242"
        exit 0 ;;
    esac ;;
  *) exit 0 ;;
esac
DOCKER
chmod +x "$TMP/bin/docker" || die "chmod fake docker"

# --- Fake HOME with a TCR-logged-in docker config (build-and-push.sh greps it). ---
mkdir -p "$TMP/home/.docker" || die "mkdir home"
echo '{ "auths": { "ccr.ccs.tencentyun.com": {} } }' > "$TMP/home/.docker/config.json" || die "write docker config"

# --- Fake build context (both Dockerfile names so any target works). ---
mkdir -p "$TMP/ctx" || die "mkdir ctx"
echo "FROM scratch" > "$TMP/ctx/Dockerfile" || die "write Dockerfile"
echo "FROM scratch" > "$TMP/ctx/Dockerfile.admin" || die "write Dockerfile.admin"

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

assert_nonzero() { # mode, desc
  local rc; rc=$(run_case "$1")
  if [ "$rc" -ne 0 ]; then note_pass "$2 (rc=$rc)"; else note_fail "$2 — expected non-zero, got 0"; fi
}
assert_zero() { # mode, desc
  local rc; rc=$(run_case "$1")
  if [ "$rc" -eq 0 ]; then note_pass "$2"; else note_fail "$2 — expected 0, got $rc"; fi
}
assert_no_pushed() { # mode, desc  (run_case must have run first)
  if grep -q "Pushed:" "$TMP/out.$1"; then note_fail "$2 — must NOT print 'Pushed:'"; else note_pass "$2"; fi
}
assert_pushed() { # mode, desc
  if grep -q "Pushed:" "$TMP/out.$1"; then note_pass "$2"; else note_fail "$2 — should print 'Pushed:'"; fi
}

# --- denial cases: must abort, must not claim success ---
assert_nonzero  silent-deny "silent TCR denial (stderr, exit 0) -> non-zero exit"
assert_no_pushed silent-deny "silent TCR denial -> no 'Pushed:'"
assert_nonzero  stdout-deny "TCR denial on stdout (exit 0) -> non-zero exit"
assert_no_pushed stdout-deny "stdout TCR denial -> no 'Pushed:'"
assert_nonzero  hard-fail   "hard push failure (exit 1) -> non-zero exit"

# --- non-denial cases: must NOT false-abort ---
assert_zero   transient-ok "benign 'too many open connections' notice -> zero exit"
assert_pushed transient-ok "benign retry notice -> still prints 'Pushed:'"
assert_zero   ok           "successful push -> zero exit"
assert_pushed ok           "successful push -> prints 'Pushed:'"

echo
if [ "$fail" -ne 0 ]; then
  echo "❌ push-guard test FAILED"
  exit 1
fi
echo "✅ push-guard test PASSED"
