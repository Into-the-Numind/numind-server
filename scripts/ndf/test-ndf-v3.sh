#!/usr/bin/env bash
# test-ndf-v3.sh: end-to-end tests for the NDF v3 toolchain.
#
# Strategy:
#   - Create an isolated sandbox under /tmp/ndf-v3-test-XXXX/
#   - Inside, build a 3-repo project skeleton (numind-server / numind-web-v3 / numind-admin-web)
#     all with bare upstream + working clone with develop branch.
#   - Copy v3 scripts under sandbox/numind-server/scripts/ndf/ so cwd resolution works.
#   - Override NDF worktree base via env var (NDF_TEST_WORKTREE_BASE) — see notes below.
#
# NOTE: ndf-start.sh hardcodes /private/tmp/wt-{slug}-{repo}. For tests we override
# WT_BASE via a small "test driver": instead of running ndf-start directly, we
# run it after monkey-patching the path resolution via NDF_TEST_OVERRIDE_PATH (the
# scripts honor a single env override below if present).
#
# Each test fn:
#   - returns 0 on PASS, non-zero on FAIL
#   - prints "PASS: <name>" or "FAIL: <name> — <reason>"
# Final exit: 0 if all pass, 1 if any fail.

set -u

SANDBOX=$(mktemp -d -t ndf-v3-test-XXXXXX)
echo "Sandbox: $SANDBOX"
trap 'echo; echo "Cleaning up sandbox: $SANDBOX"; rm -rf "$SANDBOX"' EXIT

PASS=0
FAIL=0
FAIL_NAMES=()

assert_eq() {
  local actual="$1" expected="$2" msg="$3"
  if [[ "$actual" != "$expected" ]]; then
    echo "  ASSERT FAIL: $msg"
    echo "    expected: $expected"
    echo "    actual  : $actual"
    return 1
  fi
}
assert_contains() {
  local haystack="$1" needle="$2" msg="$3"
  if [[ "$haystack" != *"$needle"* ]]; then
    echo "  ASSERT FAIL: $msg"
    echo "    needle   : $needle"
    echo "    haystack : $haystack"
    return 1
  fi
}

# Build a fake 3-repo Codes/ project skeleton in the sandbox.
# Layout:
#   $SANDBOX/Codes/
#     numind-server/{.git, scripts/ndf/, .ndf/}
#     numind-web-v3/{.git}
#     numind-admin-web/{.git}
#   $SANDBOX/upstream/<repo>.git (bare)
#   $SANDBOX/wt-base/   ← override for worktree creation
setup_sandbox() {
  local SCRIPT_SRC_DIR="$1"
  mkdir -p "$SANDBOX/Codes" "$SANDBOX/upstream" "$SANDBOX/wt-base"
  for repo in numind-server numind-web-v3 numind-admin-web; do
    git init --bare -q "$SANDBOX/upstream/$repo.git"
    mkdir -p "$SANDBOX/Codes/$repo"
    (
      cd "$SANDBOX/Codes/$repo"
      git init -q -b develop
      git config user.email "t@t.t"
      git config user.name "t"
      git config commit.gpgsign false
      git config tag.gpgsign false
      mkdir -p scripts/ndf docs
      echo "# $repo" > README.md
      git add README.md
      git commit -q -m "initial"
      git remote add origin "$SANDBOX/upstream/$repo.git"
      git push -q origin develop
    )
  done

  # Copy v3 scripts under numind-server/scripts/ndf/
  cp "$SCRIPT_SRC_DIR"/ndf-start.sh "$SANDBOX/Codes/numind-server/scripts/ndf/"
  cp "$SCRIPT_SRC_DIR"/ndf-done.sh "$SANDBOX/Codes/numind-server/scripts/ndf/"
  cp "$SCRIPT_SRC_DIR"/ndf-status.sh "$SANDBOX/Codes/numind-server/scripts/ndf/"
  cp "$SCRIPT_SRC_DIR"/ndf-micro.sh "$SANDBOX/Codes/numind-server/scripts/ndf/"
  cp "$SCRIPT_SRC_DIR"/ndf-migrate-v3.sh "$SANDBOX/Codes/numind-server/scripts/ndf/"
  chmod +x "$SANDBOX/Codes/numind-server/scripts/ndf/"*.sh

  # Patch hardcoded /private/tmp paths to point at our sandbox wt-base
  # The scripts use /private/tmp/wt-{slug}-{repo} in 3 places. We sed-replace.
  for f in ndf-start.sh ndf-done.sh ndf-status.sh; do
    # macOS sed -i needs '', linux doesn't — use perl for portability
    perl -i -pe 's|/private/tmp/wt-|'"$SANDBOX/wt-base/wt-"'|g' "$SANDBOX/Codes/numind-server/scripts/ndf/$f"
    perl -i -pe 's|/private/tmp/wt-\*|'"$SANDBOX/wt-base/wt-*"'|g' "$SANDBOX/Codes/numind-server/scripts/ndf/$f"
  done

  # Make .ndf dir
  mkdir -p "$SANDBOX/Codes/numind-server/.ndf"
}

# Run a test case.
run_test() {
  local name="$1"; shift
  echo
  echo "─── Test: $name ───"
  if ( set -e; "$@" ); then
    echo "PASS: $name"
    PASS=$((PASS + 1))
  else
    echo "FAIL: $name"
    FAIL=$((FAIL + 1))
    FAIL_NAMES+=("$name")
  fi
}

###############################################################################
# TEST 1: basic — ndf-start micro foo writes .ndf-active and creates worktree
###############################################################################
test_basic_start() {
  cd "$SANDBOX/Codes/numind-server"
  output=$(./scripts/ndf/ndf-start.sh micro test-foo --repos numind-server 2>&1)
  echo "$output"
  WT="$SANDBOX/wt-base/wt-test-foo-numind-server"
  [[ -d "$WT" ]] || { echo "FAIL: worktree dir missing"; return 1; }
  [[ -f "$WT/.ndf-active" ]] || { echo "FAIL: .ndf-active missing"; return 1; }
  id=$(jq -r '.id' "$WT/.ndf-active")
  track=$(jq -r '.track' "$WT/.ndf-active")
  stage=$(jq -r '.stage' "$WT/.ndf-active")
  branch=$(jq -r '.branches["numind-server"]' "$WT/.ndf-active")
  version=$(jq -r '.version' "$WT/.ndf-active")
  assert_eq "$id" "test-foo" "id" || return 1
  assert_eq "$track" "micro" "track" || return 1
  assert_eq "$stage" "M1" "stage" || return 1
  assert_eq "$branch" "micro/test-foo" "branch" || return 1
  assert_eq "$version" "ndf-v3" "version" || return 1
  # Branch should exist
  git -C "$WT" rev-parse --verify "refs/heads/$branch" >/dev/null || { echo "branch not in repo"; return 1; }
}

###############################################################################
# TEST 2: concurrent — start a and b at the same time, both succeed
###############################################################################
test_concurrent_starts() {
  cd "$SANDBOX/Codes/numind-server"
  ( ./scripts/ndf/ndf-start.sh micro test-a --repos numind-server >/dev/null 2>&1 ) &
  PA=$!
  ( ./scripts/ndf/ndf-start.sh micro test-b --repos numind-server >/dev/null 2>&1 ) &
  PB=$!
  wait $PA; ra=$?
  wait $PB; rb=$?
  [[ $ra -eq 0 ]] || { echo "ndf-start test-a failed: $ra"; return 1; }
  [[ $rb -eq 0 ]] || { echo "ndf-start test-b failed: $rb"; return 1; }
  [[ -f "$SANDBOX/wt-base/wt-test-a-numind-server/.ndf-active" ]] || { echo "test-a .ndf-active missing"; return 1; }
  [[ -f "$SANDBOX/wt-base/wt-test-b-numind-server/.ndf-active" ]] || { echo "test-b .ndf-active missing"; return 1; }
  ida=$(jq -r '.id' "$SANDBOX/wt-base/wt-test-a-numind-server/.ndf-active")
  idb=$(jq -r '.id' "$SANDBOX/wt-base/wt-test-b-numind-server/.ndf-active")
  assert_eq "$ida" "test-a" "id a" || return 1
  assert_eq "$idb" "test-b" "id b" || return 1
}

###############################################################################
# TEST 3: path conflict protection — second start with same slug must error
###############################################################################
test_path_conflict() {
  cd "$SANDBOX/Codes/numind-server"
  ./scripts/ndf/ndf-start.sh micro conflict-foo --repos numind-server >/dev/null 2>&1
  WT="$SANDBOX/wt-base/wt-conflict-foo-numind-server"
  [[ -d "$WT" ]] || { echo "1st start did not create worktree"; return 1; }
  # Add an uncommitted change to ensure it would be lost if --force was honored
  echo "this is precious uncommitted work" > "$WT/PRECIOUS.txt"
  # Second start MUST fail
  out=$(./scripts/ndf/ndf-start.sh micro conflict-foo --repos numind-server 2>&1) && rc=0 || rc=$?
  [[ $rc -ne 0 ]] || { echo "2nd start succeeded but should've failed"; return 1; }
  # PRECIOUS file MUST still be there
  [[ -f "$WT/PRECIOUS.txt" ]] || { echo "PRECIOUS.txt was deleted! v2 bug regression"; return 1; }
  # --force must be explicitly rejected
  out=$(./scripts/ndf/ndf-start.sh micro conflict-foo --repos numind-server --force 2>&1) && rc=0 || rc=$?
  [[ $rc -ne 0 ]] || { echo "--force did not fail"; return 1; }
  assert_contains "$out" "--force" "error mentions removed flag" || return 1
}

###############################################################################
# TEST 4: ndf-done — successfully merges to develop, cleans up
###############################################################################
test_done_basic() {
  cd "$SANDBOX/Codes/numind-server"
  ./scripts/ndf/ndf-start.sh hotfix test-done --repos numind-server >/dev/null
  WT="$SANDBOX/wt-base/wt-test-done-numind-server"
  # Make a real commit
  echo "new content" > "$WT/HELLO.md"
  ( cd "$WT" && git add HELLO.md && git commit -q -m "feat: hello" )
  # Run ndf-done from inside the worktree
  ( cd "$WT" && bash "$SANDBOX/Codes/numind-server/scripts/ndf/ndf-done.sh" ) || { echo "ndf-done failed"; return 1; }
  # Worktree should be gone
  [[ ! -d "$WT" ]] || { echo "worktree not removed: $WT"; return 1; }
  # Branch should be gone
  ! git -C "$SANDBOX/Codes/numind-server" rev-parse --verify refs/heads/fix/test-done >/dev/null 2>&1 \
    || { echo "branch fix/test-done not deleted"; return 1; }
  # develop should have the commit
  git -C "$SANDBOX/Codes/numind-server" log --oneline develop | grep -q "feat: hello" \
    || { echo "develop missing the merged commit"; return 1; }
  # origin/develop should also have it
  git -C "$SANDBOX/Codes/numind-server" log --oneline origin/develop | grep -q "feat: hello" \
    || { echo "origin/develop missing merged commit"; return 1; }
}

###############################################################################
# TEST 5: ndf-done from outside any worktree must fail
###############################################################################
test_done_outside_worktree() {
  cd "$SANDBOX/Codes/numind-server"
  ./scripts/ndf/ndf-start.sh micro test-out --repos numind-server >/dev/null
  # Run ndf-done from a directory that's NOT in any worktree
  out=$(cd "$SANDBOX" && bash "$SANDBOX/Codes/numind-server/scripts/ndf/ndf-done.sh" 2>&1) && rc=0 || rc=$?
  [[ $rc -ne 0 ]] || { echo "ndf-done from outside should fail"; return 1; }
  assert_contains "$out" "cwd 不在" "error mentions cwd" || return 1
  # Cleanup the unfinished one
  git -C "$SANDBOX/Codes/numind-server" worktree remove --force "$SANDBOX/wt-base/wt-test-out-numind-server" 2>/dev/null
  git -C "$SANDBOX/Codes/numind-server" branch -D micro/test-out 2>/dev/null || true
}

###############################################################################
# TEST 6: ndf-status — lists all active features
###############################################################################
test_status_lists() {
  cd "$SANDBOX/Codes/numind-server"
  ./scripts/ndf/ndf-start.sh micro status-x --repos numind-server >/dev/null
  ./scripts/ndf/ndf-start.sh hotfix status-y --repos numind-server >/dev/null

  # Patch the status script to scan our sandbox wt-base. We already perl-replaced
  # /private/tmp paths. But the script also adds repo-local .claude/worktrees + git wt list.
  # The git wt list should include both new worktrees → no need to override anything else.
  json=$(NDF_EXTRA_WORKTREE_DIRS="$SANDBOX/wt-base" ./scripts/ndf/ndf-status.sh --json)
  echo "status json: $json"
  cnt=$(jq '.features | length' <<<"$json")
  # At least 2 (status-x and status-y). May be more depending on prior tests + git wt list.
  [[ $cnt -ge 2 ]] || { echo "expected ≥2 features, got $cnt"; return 1; }
  has_x=$(jq -r '.features | map(select(.id == "status-x")) | length' <<<"$json")
  has_y=$(jq -r '.features | map(select(.id == "status-y")) | length' <<<"$json")
  [[ $has_x -eq 1 ]] || { echo "missing status-x"; return 1; }
  [[ $has_y -eq 1 ]] || { echo "missing status-y"; return 1; }

  # Cleanup
  git -C "$SANDBOX/Codes/numind-server" worktree remove --force "$SANDBOX/wt-base/wt-status-x-numind-server" 2>/dev/null
  git -C "$SANDBOX/Codes/numind-server" worktree remove --force "$SANDBOX/wt-base/wt-status-y-numind-server" 2>/dev/null
  git -C "$SANDBOX/Codes/numind-server" branch -D micro/status-x fix/status-y 2>/dev/null || true
}

###############################################################################
# TEST 7: cross-repo feature — 3 worktrees, ndf-done handles each
###############################################################################
test_cross_repo() {
  cd "$SANDBOX/Codes/numind-server"
  ./scripts/ndf/ndf-start.sh standard multi-foo --repos numind-server,numind-web-v3,numind-admin-web >/dev/null
  for r in numind-server numind-web-v3 numind-admin-web; do
    [[ -d "$SANDBOX/wt-base/wt-multi-foo-$r" ]] || { echo "missing $r worktree"; return 1; }
    [[ -f "$SANDBOX/wt-base/wt-multi-foo-$r/.ndf-active" ]] || { echo "missing $r .ndf-active"; return 1; }
    repos_in_active=$(jq -r '.repos | join(",")' "$SANDBOX/wt-base/wt-multi-foo-$r/.ndf-active")
    assert_eq "$repos_in_active" "numind-server,numind-web-v3,numind-admin-web" "repos in $r .ndf-active" || return 1
  done
  # Make a commit in each
  for r in numind-server numind-web-v3 numind-admin-web; do
    wt="$SANDBOX/wt-base/wt-multi-foo-$r"
    echo "multi" > "$wt/MULTI_$r.md"
    ( cd "$wt" && git add . && git commit -q -m "feat: multi $r" )
  done
  # ndf-done from inside any one of them
  ( cd "$SANDBOX/wt-base/wt-multi-foo-numind-server" && \
    bash "$SANDBOX/Codes/numind-server/scripts/ndf/ndf-done.sh" ) || { echo "ndf-done cross-repo failed"; return 1; }
  # All 3 worktrees should be gone
  for r in numind-server numind-web-v3 numind-admin-web; do
    [[ ! -d "$SANDBOX/wt-base/wt-multi-foo-$r" ]] || { echo "worktree $r not removed"; return 1; }
    # branch gone
    ! git -C "$SANDBOX/Codes/$r" rev-parse --verify refs/heads/feature/multi-foo >/dev/null 2>&1 \
      || { echo "branch in $r not deleted"; return 1; }
    # develop has the merge
    git -C "$SANDBOX/Codes/$r" log --oneline develop | grep -q "feat: multi $r" \
      || { echo "develop in $r missing merge"; return 1; }
  done
}

###############################################################################
# TEST 8: migration — mock v2 state.json + run migrate
###############################################################################
test_migration() {
  # Reset .ndf dir
  rm -rf "$SANDBOX/Codes/numind-server/.ndf"
  mkdir -p "$SANDBOX/Codes/numind-server/.ndf"
  # Create a fake hotfix worktree to migrate
  cd "$SANDBOX/Codes/numind-server"
  ./scripts/ndf/ndf-start.sh hotfix legacy-foo --repos numind-server >/dev/null
  WT="$SANDBOX/wt-base/wt-legacy-foo-numind-server"
  # Remove the .ndf-active that ndf-start put there, to simulate v2 worktree
  rm -f "$WT/.ndf-active"
  # Write a v2-style state.json mimicking this worktree
  cat > "$SANDBOX/Codes/numind-server/.ndf/state.json" <<EOF
{
  "version": "ndf-v2",
  "active_feature": "legacy-foo",
  "active": {
    "id": "legacy-foo",
    "track": "hotfix",
    "stage": "H1",
    "created_at": "2026-05-21T00:00:00+0800",
    "repos": ["numind-server"],
    "worktrees": {"numind-server": "$WT"},
    "branches": {"numind-server": "fix/legacy-foo"},
    "review_policy": "single",
    "blockers": []
  }
}
EOF
  # Run migration
  bash "$SANDBOX/Codes/numind-server/scripts/ndf/ndf-migrate-v3.sh" || { echo "migrate failed"; return 1; }
  # .ndf-active should now exist in the worktree
  [[ -f "$WT/.ndf-active" ]] || { echo "post-migration .ndf-active missing"; return 1; }
  v3id=$(jq -r '.id' "$WT/.ndf-active")
  v3version=$(jq -r '.version' "$WT/.ndf-active")
  assert_eq "$v3id" "legacy-foo" "id" || return 1
  assert_eq "$v3version" "ndf-v3" "version" || return 1
  # state.json should be archived
  [[ ! -f "$SANDBOX/Codes/numind-server/.ndf/state.json" ]] || { echo "state.json not removed"; return 1; }
  ls "$SANDBOX/Codes/numind-server/.ndf/" | grep -q "v2-archive" || { echo "archive file missing"; return 1; }

  # Cleanup
  git -C "$SANDBOX/Codes/numind-server" worktree remove --force "$WT" 2>/dev/null
  git -C "$SANDBOX/Codes/numind-server" branch -D fix/legacy-foo 2>/dev/null || true
}

###############################################################################
# RUN
###############################################################################
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
setup_sandbox "$SCRIPT_DIR"

run_test "basic_start"             test_basic_start
run_test "concurrent_starts"       test_concurrent_starts
run_test "path_conflict_protect"   test_path_conflict
run_test "done_basic"              test_done_basic
run_test "done_outside_worktree"   test_done_outside_worktree
run_test "status_lists"            test_status_lists
run_test "cross_repo_feature"      test_cross_repo
run_test "migration"               test_migration

echo
echo "═══════════════════════════════════════════"
echo "Summary: $PASS passed, $FAIL failed"
echo "═══════════════════════════════════════════"
if [[ $FAIL -gt 0 ]]; then
  echo "FAILED tests:"
  for n in "${FAIL_NAMES[@]}"; do echo "  - $n"; done
  exit 1
fi
exit 0
