#!/usr/bin/env bash
# =============================================================================
# build.sh — Three-layer skill-image build script
#
# Usage:
#   ./build.sh [--push] [--target base|libs|skills] [--tag-suffix <suffix>]
#
# Flags:
#   --push           Push images to TCR after building (default: local only)
#   --target         Build up to and including this layer (default: all three)
#   --tag-suffix     Override version suffix (default: "v1.5.0")
#
# Examples:
#   ./build.sh                          # Build all three layers locally
#   ./build.sh --push                   # Build all + push to TCR
#   ./build.sh --target libs --push     # Rebuild libs layer only + push
#
# Registry: ccr.ccs.tencentyun.com/youshunumind/sandbox-skill
# =============================================================================

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REGISTRY="ccr.ccs.tencentyun.com/youshunumind/sandbox-skill"
TAG_SUFFIX="${TAG_SUFFIX:-v1.5.0}"
PUSH=false
TARGET="skills" # default: build all three layers

# ─── Parse flags ─────────────────────────────────────────────────────────────
while [[ $# -gt 0 ]]; do
  case "$1" in
    --push)       PUSH=true; shift ;;
    --target)     TARGET="$2"; shift 2 ;;
    --tag-suffix) TAG_SUFFIX="$2"; shift 2 ;;
    *) echo "Unknown flag: $1"; exit 1 ;;
  esac
done

BASE_TAG="${REGISTRY}:base-${TAG_SUFFIX}"
LIBS_TAG="${REGISTRY}:libs-${TAG_SUFFIX}"
SKILLS_TAG="${REGISTRY}:skills-${TAG_SUFFIX}"

echo "=== Numind Skill Image Build ==="
echo "    Registry : ${REGISTRY}"
echo "    Suffix   : ${TAG_SUFFIX}"
echo "    Target   : ${TARGET}"
echo "    Push     : ${PUSH}"
echo ""

# ─── Layer 1: base ───────────────────────────────────────────────────────────
build_base() {
  echo "[1/3] Building base layer → ${BASE_TAG}"
  docker build \
    -f "${SCRIPT_DIR}/Dockerfile.base" \
    -t "${BASE_TAG}" \
    "${SCRIPT_DIR}"
  echo "      ✓ base built"
}

# ─── Layer 2: libs ───────────────────────────────────────────────────────────
build_libs() {
  echo "[2/3] Building libs layer → ${LIBS_TAG}"
  docker build \
    -f "${SCRIPT_DIR}/Dockerfile.libs" \
    --build-arg "BASE_TAG=base-${TAG_SUFFIX}" \
    -t "${LIBS_TAG}" \
    "${SCRIPT_DIR}"
  echo "      ✓ libs built"
}

# ─── Layer 3: skills ─────────────────────────────────────────────────────────
build_skills() {
  echo "[3/3] Building skills layer → ${SKILLS_TAG}"
  docker build \
    -f "${SCRIPT_DIR}/Dockerfile.skills" \
    --build-arg "LIBS_TAG=libs-${TAG_SUFFIX}" \
    -t "${SKILLS_TAG}" \
    "${SCRIPT_DIR}"
  echo "      ✓ skills built"
}

# ─── Push to TCR ─────────────────────────────────────────────────────────────
push_image() {
  local tag="$1"
  echo "    Pushing ${tag} ..."
  docker push "${tag}"
  echo "    ✓ pushed"
}

# ─── Execute ──────────────────────────────────────────────────────────────────
case "${TARGET}" in
  base)
    build_base
    ${PUSH} && push_image "${BASE_TAG}"
    ;;
  libs)
    build_base
    build_libs
    ${PUSH} && { push_image "${BASE_TAG}"; push_image "${LIBS_TAG}"; }
    ;;
  skills)
    build_base
    build_libs
    build_skills
    ${PUSH} && {
      push_image "${BASE_TAG}"
      push_image "${LIBS_TAG}"
      push_image "${SKILLS_TAG}"
    }
    ;;
  *)
    echo "Unknown target: ${TARGET}. Must be one of: base, libs, skills"
    exit 1
    ;;
esac

echo ""
echo "=== Build complete ==="
echo "    Runtime image to set in config: sandbox.image_tag: ${SKILLS_TAG}"
