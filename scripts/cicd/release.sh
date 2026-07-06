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

# Per-repo deploy lock. 3 个仓库的部署都 rsync 到构建机各自的 ~/repos/<repo>/ 共享目录，
# 再在该目录跑 docker build。rsync 用 --delete。根因(2026-06-16 deploy-rsync-lock)：
# 同一仓库的两个部署并发时，一个的 rsync --delete 会在另一个 docker build 读上下文期间
# 删掉 skills/ 等目录 → "COPY skills: /skills not found" 这类晦涩失败(skills/ 明明在源里)。
# 用 per-repo mkdir 原子锁把【同仓库】部署串行化(不同仓库各自独立锁，仍可并行)。
RELEASE_LOCK="${TMPDIR:-/tmp}/numind-release-$(basename "$BUILD_REPO_PATH").lock"

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
RSYNC_SECRET_EXCLUDES=()
if [ "$ENV" = "prod" ]; then
  GIT_TAG=$(git describe --tags --exact-match HEAD 2>/dev/null || true)
  PROD_WORKTREE_STATUS="$(git status --porcelain --untracked-files=all)"
  if [ -z "$GIT_TAG" ] || [ -n "$PROD_WORKTREE_STATUS" ]; then
    echo "ERROR: prod release requires a clean worktree and exact tag." >&2
    if [ -z "$GIT_TAG" ]; then
      echo "Tag: missing exact tag (branch=$GIT_BRANCH, sha=$GIT_SHA)" >&2
    fi
    if [ -n "$PROD_WORKTREE_STATUS" ]; then
      echo "Dirty items:" >&2
      echo "$PROD_WORKTREE_STATUS" >&2
    fi
    exit 1
  fi
  RSYNC_SECRET_EXCLUDES=(
    --exclude='.env'
    --exclude='.env.*'
    --exclude='secrets.env'
    --exclude='*.pem'
    --exclude='*.key'
    --exclude='*.key.*'
    --exclude='*.crt'
    --exclude='*.crt.*'
    --exclude='configs/cert/'
    --exclude='configs/ssl/'
    --exclude='/config_dev.yaml'
    --exclude='/config_qa.yaml'
    --exclude='/config_local.yaml'
    --exclude='/config_*.local.yaml'
  )

  case "$MODE" in
    full|--build-only) ENV="$ENV" "${REPO_ROOT}/scripts/check_prod_secret_hygiene.sh" ;;
  esac
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
    ${RSYNC_SECRET_EXCLUDES[@]+"${RSYNC_SECRET_EXCLUDES[@]}"} \
    --exclude='.idea/' \
    --exclude='.vscode/' \
    --exclude='.cursor/' \
    --exclude='coverage.out' \
    --exclude='coverage.html' \
    --exclude='/data/' \
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
  verify_build_context
}

do_build() {
  echo
  echo "--- [2/3] build + push image on build server ---"
  ssh $SSH_OPTS "$SSH_TARGET" \
    "cd $BUILD_REPO_PATH && GIT_SHA='${EFFECTIVE_SHA}' GIT_TAG='${GIT_TAG}' bash scripts/cicd/build-and-push.sh '$ENV' '$TARGET'"
}

# Keep last 72h of build cache and unused images. Build server is shared across 3 repos
# (numind-server / numind-web-v3 / numind-admin-web); without this, cache + unused images
# accumulate until disk fills.
do_cleanup_build_cache() {
  echo
  echo "--- [2.5/3] cleanup build cache on build server (keep last 72h) ---"
  local start=$(date +%s)
  if ! ssh $SSH_OPTS "$SSH_TARGET" '
    set -e
    echo "Before cleanup:"
    docker system df
    echo
    echo "Pruning builder cache (--filter until=72h)..."
    docker builder prune -af --filter "until=72h"
    echo
    echo "Pruning unused images (--filter until=72h)..."
    docker image prune -af --filter "until=72h"
    echo
    echo "After cleanup:"
    docker system df
  '; then
    echo "⚠️  cleanup failed (non-fatal, continuing deploy)"
  fi
  echo "cleanup took $(($(date +%s) - start))s"
}

do_deploy() {
  echo
  echo "--- [3/3] deploy to $ENV ---"
  ssh $SSH_OPTS "$SSH_TARGET" \
    "cd $BUILD_REPO_PATH && GIT_SHA='${EFFECTIVE_SHA}' GIT_TAG='${GIT_TAG}' bash scripts/cicd/deploy.sh '$ENV' '$TARGET'"
}

# 获取 per-repo 部署锁，把同仓库的并发部署串行化（防共享构建目录被并发 rsync --delete 破坏）。
# mkdir 是原子操作，可移植（macOS 无 flock）。陈旧锁(持有进程已死)自动清理。
acquire_release_lock() {
  local waited=0 max=900 holder
  while ! mkdir "$RELEASE_LOCK" 2>/dev/null; do
    holder="$(cat "$RELEASE_LOCK/pid" 2>/dev/null || true)"
    # 持有者进程已不存在 → 陈旧锁(上次部署崩溃残留)，清理后重试
    if [ -n "$holder" ] && ! kill -0 "$holder" 2>/dev/null; then
      echo "⚠ 清理陈旧部署锁 ($RELEASE_LOCK，持有者 PID $holder 已退出)"
      rm -rf "$RELEASE_LOCK"
      # 计入等待并小睡，避免极端"进程快速创建后崩溃"的 flapping 导致紧自旋且永不超时
      sleep 1
      waited=$((waited + 1))
      continue
    fi
    if [ "$waited" -ge "$max" ]; then
      echo "ERROR: 等待同仓库并发部署锁超时 ${max}s (持有者 PID ${holder:-?})。" >&2
      echo "  → 若确认无其他部署在跑，手动删 $RELEASE_LOCK 后重试。" >&2
      exit 1
    fi
    [ "$waited" -eq 0 ] && echo "⏳ 另一个 $(basename "$BUILD_REPO_PATH") 部署正在进行，等待其完成以避免共享构建目录被并发 rsync --delete 破坏..."
    sleep 3
    waited=$((waited + 3))
  done
  echo "$$" > "$RELEASE_LOCK/pid"
  # shellcheck disable=SC2064
  trap "rm -rf '$RELEASE_LOCK'" EXIT
}

# rsync 后兜底断言：docker build 要 COPY 的关键上下文目录已就位(skills/ 是历史踩坑点)。
# 锁已防并发破坏，此处 fail-fast 把"源/上下文缺 skills/"早报为清晰错误，
# 而非 docker build 半途的晦涩 "COPY skills: /skills not found"。
verify_build_context() {
  # 只有 server 的 Dockerfile 有 COPY skills；admin 的 Dockerfile.admin 不 COPY skills，无需校验。
  [ "$TARGET" = "server" ] || return 0
  if ! ssh $SSH_OPTS "$SSH_TARGET" "cd '$BUILD_REPO_PATH' && [ -d skills ] && [ -n \"\$(ls -A skills 2>/dev/null)\" ]"; then
    echo "ERROR: 构建上下文缺 skills/ (构建机 $BUILD_REPO_PATH/skills 不存在或为空)" >&2
    echo "  → Dockerfile 有 COPY skills /app/skills，缺它会导致构建失败。" >&2
    echo "  → 多为并发/陈旧部署的 rsync --delete 所致；确认无并发部署后重跑本命令。" >&2
    exit 1
  fi
}

# rsync+build 共用构建目录的模式才需要锁；--deploy-only 仅拉已构建镜像，无需锁。
case "$MODE" in
  full|--build-only) acquire_release_lock ;;
esac

case "$MODE" in
  full)         do_rsync; do_build; do_cleanup_build_cache; do_deploy ;;
  --build-only) do_rsync; do_build; do_cleanup_build_cache ;;
  --deploy-only) do_deploy ;;
  *) echo "ERROR: unknown mode $MODE" >&2; exit 1 ;;
esac

echo
echo "==============================================="
echo "  ✅ Release complete: $TARGET -> $ENV ($EFFECTIVE_SHA)"
echo "==============================================="
