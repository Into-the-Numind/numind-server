#!/usr/bin/env bash
# Regression test for the container-name-conflict race in deploy-remote.sh.
#
# Reproduces the 2026-06-02 dev deploy failure. deploy-remote.sh replaced a
# running `--restart always` container with `docker stop` + `docker rm` (no -f)
# then `docker run`. The restart policy can re-grab the name between stop/rm and
# run, so the un-forced `docker rm` fails on the live container (its `|| true`
# hides that) and the subsequent `docker run` aborts with:
#   exit 125 — docker: Error response from daemon: Conflict.
#   The container name "/<name>" is already in use by another container.
# `set -euo pipefail` then makes the whole deploy exit non-zero even though the
# end state would have been correct — a benign-but-misleading failure that on
# prod can trip an unnecessary rollback.
#
# The fix replaces stop+rm with remove_container(): `docker rm -f` plus a poll
# until the name is released, so `docker run` always gets a free name.
#
# This test runs the REAL deploy-remote.sh end-to-end against a fake `docker`
# (and `curl`) on PATH that models the restart-always race: a plain `docker rm`
# of an existing container fails, only `docker rm -f` removes it, and
# `docker run` returns the 125 conflict while the container still exists. With
# the old stop+rm logic the script exits 125 (RED); with remove_container it
# succeeds (GREEN).
#
# Run: bash scripts/cicd/test-deploy-replace-race.sh
set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
DEPLOY_REMOTE="$SCRIPT_DIR/deploy-remote.sh"

die() { echo "test setup error: $1" >&2; exit 2; }

[ -f "$DEPLOY_REMOTE" ] || die "deploy-remote.sh not found at $DEPLOY_REMOTE"

TMP="$(mktemp -d)" || die "mktemp"
trap 'rm -rf "$TMP"' EXIT

STATE="$TMP/state"
mkdir -p "$STATE" "$TMP/bin" || die "mkdir"

# Seed: a container of the target name already exists and is running, as if a
# previous deploy left it and `--restart always` brought it back. This is the
# precondition that drives the replace path (and the race). The name is unknown
# to the test (it differs per repo/env), so the seeded container matches
# whatever name the script asks about; once `docker run --name X` (re-)creates
# it, the fake tracks it by that exact name so the `-f name=^X$` poll filter is
# genuinely exercised.
: > "$STATE/name"

# --- Fake `docker` modelling the --restart-always name-conflict race. ---
cat > "$TMP/bin/docker" <<'DOCKER' || die "write fake docker"
#!/usr/bin/env bash
STATE="${FAKE_DOCKER_STATE:?}"
NAMEFILE="$STATE/name"
exists() { [ -f "$NAMEFILE" ]; }
cur() { cat "$NAMEFILE" 2>/dev/null; }
# Empty name file = seeded container, matches any queried name; a non-empty
# file (set by `docker run --name`) matches only that exact name.
matches() { local c; c="$(cur)"; [ -z "$c" ] || [ "$c" = "$1" ]; }
sub="${1:-}"; shift || true
case "$sub" in
  pull|network|rmi|logs|tag|image|images) exit 0 ;;   # benign; no old images to clean
  inspect)
    exists && echo "sha256:fakeimageid"          # cleanup_old_images / OLD_IMAGE probe
    exit 0 ;;
  stop)
    # `docker stop` of a --restart always container: in the race window the
    # daemon keeps/brings it back, so it still exists afterwards.
    exit 0 ;;
  rm)
    force=0; name=""
    for a in "$@"; do
      case "$a" in -f) force=1 ;; -*) : ;; *) name="$a" ;; esac
    done
    if [ "$force" = 1 ]; then
      exists && matches "$name" && rm -f "$NAMEFILE"   # force always removes
      exit 0
    fi
    if exists && matches "$name"; then           # un-forced rm races the restart
      echo "Error response from daemon: cannot remove container: container is running" >&2
      exit 1                                      # policy and fails on the live one
    fi
    exit 0 ;;
  run)
    name=""; prev=""
    for a in "$@"; do [ "$prev" = "--name" ] && name="$a"; prev="$a"; done
    if exists; then
      echo "docker: Error response from daemon: Conflict. The container name \"/$name\" is already in use by another container." >&2
      exit 125                                    # the exact failure we guard against
    fi
    printf '%s' "$name" > "$NAMEFILE"; exit 0 ;;
  ps)
    aq=0; want=""
    for a in "$@"; do
      [ "$a" = "-aq" ] && aq=1
      case "$a" in name=*) want="${a#name=}"; want="${want#^}"; want="${want%\$}" ;; esac
    done
    if [ "$aq" = 1 ]; then                        # poll: honor the -f name=^X$ filter
      exists && matches "$want" && echo "fakecontainerid"
      exit 0
    fi
    exists && echo "$(cur)  Up 1 second  0.0.0.0:80->80/tcp"   # formatted ps on success
    exit 0 ;;
  *) exit 0 ;;
esac
DOCKER
chmod +x "$TMP/bin/docker" || die "chmod fake docker"

# --- Fake `curl`: health check always healthy (we test deploy, not health). ---
printf '#!/usr/bin/env bash\nexit 0\n' > "$TMP/bin/curl" || die "write fake curl"
chmod +x "$TMP/bin/curl" || die "chmod fake curl"

# --- Fake `sudo` / `getent`: numind-server's deploy-remote.sh calls these for
#     the server target; stub them so the test never touches the real host. ---
printf '#!/usr/bin/env bash\nexit 0\n' > "$TMP/bin/sudo" || die "write fake sudo"
chmod +x "$TMP/bin/sudo" || die "chmod fake sudo"
printf '#!/usr/bin/env bash\necho "docker:x:999"\nexit 0\n' > "$TMP/bin/getent" || die "write fake getent"
chmod +x "$TMP/bin/getent" || die "chmod fake getent"

run_deploy() {
  (
    # TARGET is read only by numind-server's deploy-remote.sh (server target);
    # the frontend repos' scripts ignore it. Set here so this one test file is
    # identical across all three repos, mirroring the near-identical scripts.
    PATH="$TMP/bin:$PATH" \
    FAKE_DOCKER_STATE="$STATE" \
    ENV="dev" TARGET="server" \
    IMAGE="ccr.ccs.tencentyun.com/youshunumind/test:develop-testsha" \
      bash "$DEPLOY_REMOTE"
  ) > "$TMP/out" 2>&1
  echo $?
}

fail=0
rc=$(run_deploy)
if [ "$rc" -eq 0 ]; then
  echo "PASS: deploy succeeds despite restart-always name-conflict race (rc=0)"
else
  echo "FAIL: deploy exited $rc on the name-conflict race — expected 0"
  echo "----- deploy output -----"; cat "$TMP/out"; echo "-------------------------"
  fail=1
fi

if grep -q "Deploy success" "$TMP/out"; then
  echo "PASS: output reports 'Deploy success'"
else
  echo "FAIL: output missing 'Deploy success'"
  fail=1
fi

echo
if [ "$fail" -ne 0 ]; then
  echo "❌ deploy-replace-race test FAILED"
  exit 1
fi
echo "✅ deploy-replace-race test PASSED"
