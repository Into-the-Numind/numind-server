#!/usr/bin/env bash
# Build and push numind-server image to TCR. Runs on build server.
# Invocation: bash scripts/cicd/build-and-push.sh <env> [target]
#   env:    dev | qa | prod
#   target: server | admin  (default: server)
# Env vars (set by caller from Mac):
#   GIT_SHA  - short git sha for tagging (required for traceability)
#   GIT_TAG  - version tag for prod (e.g. v1.2.3), optional for dev/qa

set -euo pipefail

ENV="${1:?usage: $0 <dev|qa|prod> [server|admin]}"
TARGET="${2:-server}"
GIT_SHA="${GIT_SHA:-unknown}"
GIT_TAG="${GIT_TAG:-}"

REGISTRY="ccr.ccs.tencentyun.com"
NAMESPACE="youshunumind"

case "$TARGET" in
  server)
    IMAGE_NAME="numind-server"
    DOCKERFILE="Dockerfile"
    ;;
  admin)
    IMAGE_NAME="numind-admin"
    DOCKERFILE="Dockerfile.admin"
    ;;
  *)
    echo "ERROR: target must be 'server' or 'admin', got '$TARGET'" >&2
    exit 1
    ;;
esac

case "$ENV" in
  dev)  ROLLING_TAG="develop" ;;
  qa)   ROLLING_TAG="release" ;;
  prod) ROLLING_TAG="${GIT_TAG:-latest}" ;;
  *) echo "ERROR: env must be dev/qa/prod, got '$ENV'" >&2; exit 1 ;;
esac

SHA_TAG="${ROLLING_TAG}-${GIT_SHA}"
IMG_ROLLING="${REGISTRY}/${NAMESPACE}/${IMAGE_NAME}:${ROLLING_TAG}"
IMG_SHA="${REGISTRY}/${NAMESPACE}/${IMAGE_NAME}:${SHA_TAG}"

echo "============================================="
echo "Building $IMAGE_NAME for ENV=$ENV"
echo "  Dockerfile : $DOCKERFILE"
echo "  Git SHA    : $GIT_SHA"
echo "  Tags       : $IMG_ROLLING"
echo "               $IMG_SHA"
echo "============================================="

# Verify we are in the repo root (Dockerfile present)
if [ ! -f "$DOCKERFILE" ]; then
  echo "ERROR: $DOCKERFILE not found. Run from repo root." >&2
  exit 1
fi

# Verify docker is logged in to TCR
if ! grep -q "ccr.ccs.tencentyun.com" ~/.docker/config.json 2>/dev/null; then
  echo "ERROR: not logged in to TCR. Run: docker login ccr.ccs.tencentyun.com" >&2
  exit 1
fi

START=$(date +%s)

# agent-mode-sandbox-integration #4: only dev image installs docker CLI
# (DooD pattern for sandbox). prod stays minimal.
WITH_DOCKER_CLI=false
if [ "$ENV" = "dev" ]; then
  WITH_DOCKER_CLI=true
fi

docker build \
  --tag "$IMG_ROLLING" \
  --tag "$IMG_SHA" \
  --build-arg "ENV=$ENV" \
  --build-arg "WITH_DOCKER_CLI=$WITH_DOCKER_CLI" \
  --label "git.commit=$GIT_SHA" \
  --label "build.env=$ENV" \
  --label "build.at=$(date -Iseconds)" \
  -f "$DOCKERFILE" \
  .

BUILD_DONE=$(date +%s)
echo "Build took $((BUILD_DONE - START))s"

# Push to TCR. CRITICAL: TCR may DENY a push (e.g. repo at its 100-tag limit,
# "denied: ...tag has reached its limit(100)...") while `docker push` still
# returns exit 0 — so a bare push slips past `set -euo pipefail` and the deploy
# reports success having shipped nothing. push_image() checks BOTH the real exit
# code AND the output for denial patterns, and aborts loudly on either.
push_image() {
  local img="$1" out rc
  if out="$(docker push "$img" 2>&1)"; then rc=0; else rc=$?; fi
  printf '%s\n' "$out"
  # Abort on a real failure (non-zero) OR a registry denial that `docker push`
  # reported with exit 0 (TCR does this at the 100-tag limit). The patterns are
  # words that never appear in a successful push; `too many` is anchored to
  # registry quota signals so a benign "too many open connections" retry notice
  # can't trip a false abort of a good deploy.
  if [ "$rc" -ne 0 ] || printf '%s\n' "$out" | grep -Eqi 'denied|reached its limit|too many (requests|tags|images)|toomanyrequests|quota|unauthorized|forbidden'; then
    echo >&2
    echo "ERROR: docker push FAILED for $img (exit=$rc)" >&2
    if printf '%s\n' "$out" | grep -Eqi 'reached its limit|limit\(100\)|too many tags'; then
      cat >&2 <<EOF
>>> TCR tag-limit reached: ${NAMESPACE}/${IMAGE_NAME} is at its 100-tag cap, so
>>> the image was NOT pushed (TCR returns "denied" but docker push exits 0).
>>> Clear old '${ROLLING_TAG}-<sha>' tags in the Tencent TCR console
>>> (${REGISTRY} -> ${NAMESPACE}/${IMAGE_NAME} -> 版本管理), then redeploy.
EOF
    fi
    # exit (not return): a denied push must abort the entire deploy.
    exit 1
  fi
}

push_image "$IMG_ROLLING"
push_image "$IMG_SHA"

PUSH_DONE=$(date +%s)
echo "Push took $((PUSH_DONE - BUILD_DONE))s"
echo
echo "Pushed:"
echo "  $IMG_ROLLING"
echo "  $IMG_SHA"
