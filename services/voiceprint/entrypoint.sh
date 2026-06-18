#!/bin/sh
# Voiceprint microservice entrypoint.
# Single uvicorn worker — onnxruntime already parallelizes internally
# (intra_op_num_threads = cpu//2). Multiple uvicorn workers would multiply CPU
# contention on the shared 4-core build machine (see DIARIZATION_SPEC §4 P1-资源争用).
set -e

PORT="${VOICEPRINT_PORT:-11236}"

echo "[voiceprint] starting uvicorn on 0.0.0.0:${PORT} (ONNX=${VOICEPRINT_ONNX_PATH:-/models/campplus.onnx})"
exec uvicorn app:app --host 0.0.0.0 --port "${PORT}"
