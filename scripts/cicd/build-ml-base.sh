#!/usr/bin/env bash
# Build and push the numind-ml-base image to TCR. Runs on the build server.
#
# WHY: numind-server's runtime image needs torch CPU + sentence-transformers +
# pymupdf / markitdown etc. These are several GB, change almost never, and were
# being re-downloaded cross-border (pytorch.org @ ~8 KB/s) on every deploy because
# the build machine's BuildKit cache GC evicts the large layers. Baking them into
# a prebuilt base image that lives in TCR means the business build just pulls
# prebuilt layers over the national backbone instead of rebuilding them.
# See manifest feature docker-ml-base-image.
#
# WHEN TO RUN: only when the ML deps in Dockerfile.ml-base change. After running,
# bump the `ARG ML_BASE_TAG` default in Dockerfile to the new tag, then redeploy.
#
# Usage (on the build server, from repo root):
#   bash scripts/cicd/build-ml-base.sh [tag]
#     tag: image tag, default = today's date (YYYYMMDD)

set -euo pipefail

REGISTRY="ccr.ccs.tencentyun.com"
NAMESPACE="youshunumind"
IMAGE_NAME="numind-ml-base"
DOCKERFILE="Dockerfile.ml-base"

TAG="${1:-$(date +%Y%m%d)}"
IMG="${REGISTRY}/${NAMESPACE}/${IMAGE_NAME}:${TAG}"

echo "============================================="
echo "Building ML base image"
echo "  Dockerfile : $DOCKERFILE"
echo "  Image      : $IMG"
echo "============================================="

# Verify we are in the repo root (Dockerfile.ml-base present)
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
docker build --tag "$IMG" -f "$DOCKERFILE" .
echo "Build took $(( $(date +%s) - START ))s"

# Push to TCR. CRITICAL: TCR may DENY a push (e.g. repo at its tag limit) while
# `docker push` still returns exit 0 — so a bare push slips past `set -euo
# pipefail`. Check BOTH the real exit code AND the output for denial patterns,
# same guard as build-and-push.sh (manifest tcr-push-silent-success).
out=""; rc=0
if out="$(docker push "$IMG" 2>&1)"; then rc=0; else rc=$?; fi
printf '%s\n' "$out"
if [ "$rc" -ne 0 ] || printf '%s\n' "$out" | grep -Eqi 'denied|reached its limit|too many (requests|tags|images)|toomanyrequests|quota|unauthorized|forbidden'; then
  echo >&2
  echo "ERROR: docker push FAILED for $IMG (exit=$rc)" >&2
  if printf '%s\n' "$out" | grep -Eqi 'reached its limit|limit\(100\)|too many tags'; then
    cat >&2 <<EOF
>>> TCR tag-limit reached: ${NAMESPACE}/${IMAGE_NAME} is at its 100-tag cap, so
>>> the base image was NOT pushed (TCR returns "denied" but docker push exits 0).
>>> Clear old tags in the Tencent TCR console
>>> (${REGISTRY} -> ${NAMESPACE}/${IMAGE_NAME} -> 版本管理), then re-run.
EOF
  fi
  exit 1
fi

echo
echo "Pushed: $IMG"
echo
echo ">>> NEXT: set 'ARG ML_BASE_TAG=$TAG' default in Dockerfile, then redeploy server."
