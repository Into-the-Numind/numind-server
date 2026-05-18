#!/usr/bin/env bash
# Mac-side orchestrator. rsync code -> build on build server -> deploy.
# Invocation:
#   bash scripts/cicd/release.sh <env> [target] [--build-only|--deploy-only]
# Run from numind-server repo root.

set -euo pipefail

ENV="${1:?usage: $0 <dev|qa|prod> [server|admin] [--build-only|--deploy-only]}"
TARGET="${2:-server}"
MODE="${3:-full}"

case "$ENV" in dev|qa|prod) ;; *) echo "ERROR: env must be dev/qa/prod" >&2; exit 1 ;; esac
case "$TARGET" in server|admin) ;; *) echo "ERROR: target must be server/admin" >&2; exit 1 ;; esac

BUILD_HOST="${BUILD_SSH_HOST:-139.155.129.13}"
BUILD_USER="${BUILD_SSH_USER:-ubuntu}"
BUILD_SSH_KEY="${BUILD_SSH_KEY:-$HOME/.ssh/numind_build_server}"
BUILD_REPO_PATH="repos/numind-server"

SSH_OPTS="-i $BUILD_SSH_KEY -o StrictHostKeyChecking=no"
SSH_TARGET="${BUILD_USER}@${BUILD_HOST}"

REPO_ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
cd "$REPO_ROOT"
if ! GIT_SHA=$(git rev-parse --short HEAD 2>/dev/null); then
  echo "ERROR: not in a git repo, or git not available" >&2
  exit 1
fi
GIT_BRANCH=$(git rev-parse --abbrev-ref HEAD)
GIT_DIRTY=""
if ! git diff --quiet 2>/dev/null || ! git diff --cached --quiet 2>/dev/null; then
  GIT_DIRTY="-dirty"
fi
EFFECTIVE_SHA="${GIT_SHA}${GIT_DIRTY}"

GIT_TAG=""
if [ "$ENV" = "prod" ]; then
  GIT_TAG=$(git describe --tags --exact-match HEAD 2>/dev/null || true)
  if [ -z "$GIT_TAG" ]; then
    echo "ERROR: prod release requires HEAD to be on a git tag (got branch=$GIT_BRANCH, sha=$GIT_SHA)" >&2
    exit 1
  fi
fi

echo "==============================================="
echo "  Release $TARGET -> $ENV"
echo "  Branch  : $GIT_BRANCH"
echo "  Commit  : $EFFECTIVE_SHA"
[ -n "$GIT_TAG" ] && echo "  Tag     : $GIT_TAG"
echo "  Mode    : $MODE"
echo "  Build   : $SSH_TARGET:$BUILD_REPO_PATH"
echo "==============================================="

do_rsync() {
  echo
  echo "--- [1/3] rsync code to build server ---"
  local start=$(date +%s)
  rsync -az --delete \
    --exclude='.git/' \
    --exclude='.claude/' \
    --exclude='node_modules/' \
    --exclude='tmp/' \
    --exclude='dist/' \
    --exclude='build/' \
    --exclude='logs/' \
    --exclude='*.log' \
    --exclude='*.tar.gz' \
    --exclude='.DS_Store' \
    --exclude='.idea/' \
    --exclude='.vscode/' \
    --exclude='.cursor/' \
    --exclude='coverage.out' \
    --exclude='coverage.html' \
    --exclude='__pycache__/' \
    --exclude='*.pyc' \
    --exclude='vendor/' \
    --exclude='/numind' \
    --exclude='/numind-admin' \
    --exclude='/numind-server' \
    --exclude='/main' \
    --exclude='/migrate-vectors' \
    --exclude='/scripts/build_test_files/' \
    -e "ssh $SSH_OPTS" \
    "$REPO_ROOT/" \
    "${SSH_TARGET}:${BUILD_REPO_PATH}/"
  echo "rsync took $(($(date +%s) - start))s"
}

do_build() {
  echo
  echo "--- [2/3] build + push image on build server ---"
  ssh $SSH_OPTS "$SSH_TARGET" \
    "cd $BUILD_REPO_PATH && GIT_SHA='${EFFECTIVE_SHA}' GIT_TAG='${GIT_TAG}' bash scripts/cicd/build-and-push.sh '$ENV' '$TARGET'"
}

do_deploy() {
  echo
  echo "--- [3/3] deploy to $ENV ---"
  ssh $SSH_OPTS "$SSH_TARGET" \
    "cd $BUILD_REPO_PATH && GIT_SHA='${EFFECTIVE_SHA}' GIT_TAG='${GIT_TAG}' bash scripts/cicd/deploy.sh '$ENV' '$TARGET'"
}

case "$MODE" in
  full)         do_rsync; do_build; do_deploy ;;
  --build-only) do_rsync; do_build ;;
  --deploy-only) do_deploy ;;
  *) echo "ERROR: unknown mode $MODE" >&2; exit 1 ;;
esac

echo
echo "==============================================="
echo "  ✅ Release complete: $TARGET -> $ENV ($EFFECTIVE_SHA)"
echo "==============================================="
