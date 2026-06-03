#!/usr/bin/env bash
# Smoke test for the numind-ml-base image (regression guard for the
# docker-ml-base-image hotfix). Asserts the heavy ML / runtime Python deps we
# baked into the base are importable, and that torch is the CPU build. If a
# future base rebuild drops or breaks a dep, this fails loudly BEFORE the
# business image is built on top of it.
#
# Usage (on a host with docker + the base image available locally):
#   bash scripts/cicd/verify-ml-base.sh [image-ref]
#     image-ref: full image; default ccr.ccs.tencentyun.com/youshunumind/numind-ml-base:<today>

set -euo pipefail

REGISTRY="ccr.ccs.tencentyun.com"
NAMESPACE="youshunumind"
IMAGE_NAME="numind-ml-base"
IMG="${1:-${REGISTRY}/${NAMESPACE}/${IMAGE_NAME}:$(date +%Y%m%d)}"

echo "Verifying ML base image: $IMG"

docker run --rm "$IMG" python3 - <<'PY'
# 1) every baked dep must import (import name in comment where it differs)
import torch
import sentence_transformers
import fitz                 # pymupdf
import docx                 # python-docx
import numpy
import fastapi
import uvicorn
import multipart            # python-multipart
import markitdown

# 2) torch must be the CPU build (we install from the cpu index to keep size down)
assert not torch.cuda.is_available(), "torch reports CUDA available — expected CPU-only build"
assert "+cu" not in torch.__version__, f"torch {torch.__version__} looks like a CUDA build"

# 3) system runtime deps the stack needs (Python imports alone won't catch these)
import shutil, ctypes
assert shutil.which("antiword"), "antiword binary not on PATH (.doc parsing)"
ctypes.CDLL("libgomp.so.1")  # OpenMP runtime for torch / sentence-transformers

print("OK: torch", torch.__version__,
      "| sentence-transformers", sentence_transformers.__version__,
      "| numpy", numpy.__version__)
PY

echo "✅ ML base smoke test passed"
