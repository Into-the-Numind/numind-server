#!/usr/bin/env python3
"""
Voiceprint Microservice (CAM++ speaker embedding)
==================================================

Self-contained FastAPI service for the 会议副驾 (Meeting Copilot) speaker
diarization feature. See docs/meeting/DIARIZATION_SPEC.md §5 (D1 engine) / §7 (T1).

Engine: 3D-Speaker CAM++ `iic/speech_campplus_sv_zh-cn_16k-common` exported to ONNX
(192-d embedding, Chinese 16k). Loaded via onnxruntime CPUExecutionProvider — no
torch/modelscope/funasr framework at inference time (torch is used ONLY for the
Kaldi fbank front-end; see the kaldi-native-fbank optimization note below).

Endpoints
---------
- POST /embed   : s16le 16k mono PCM (base64) -> {embedding:[192], dim, duration_ms, valid}
- GET  /healthz : {ok:true, dim:192}
- POST /diarize : 501 NotImplemented placeholder (owned by T2)

This is T1: ONLY the /embed + /healthz skeleton is functional. /diarize is a
deliberate 501 stub.
"""

import base64
import binascii
import os
import sys
from contextlib import asynccontextmanager
from typing import List, Optional

import numpy as np
from fastapi import FastAPI
from fastapi.responses import JSONResponse
from pydantic import BaseModel, Field

import onnxruntime as ort

# torch + torchaudio are used ONLY for the Kaldi-compatible fbank front-end.
# This MUST match the feature extraction CAM++ was trained with (see _FBANK_* and
# compute_fbank below). Future optimization: replace torchaudio.compliance.kaldi.fbank
# with kaldi-native-fbank to drop the heavy torch dependency from this CPU service.
import torch
import torchaudio.compliance.kaldi as kaldi


# ---------------------------------------------------------------------------
# Constants (kept in sync with DIARIZATION_SPEC §5 D9 where applicable)
# ---------------------------------------------------------------------------

EMBED_DIM = 192                     # CAM++ embedding dimension (D9: dim 192)
SAMPLE_RATE = 16000                 # CAM++ trained at 16k; input is s16le 16k mono
MIN_DURATION_MS = 700               # valid=false below this (segment too short).
#                                     D9 初值 (MIN_DUR_MS 700); dev 验收用真实会议校准。
MIN_RMS_S16LE = 184.0               # low-energy gate on int16 RMS. 184 ≈ -45 dBFS
#                                     (20*log10(184/32768)), aligns with D9 MIN_RMS -45dBFS;
#                                     filters silence/near-silence frames. D9 初值, dev 验收校准。

# Kaldi fbank front-end — MUST match CAM++ training config.
_FBANK_NUM_MEL_BINS = 80            # 80-mel
_FBANK_SAMPLE_FREQUENCY = 16000     # sample_frequency=16000
_FBANK_DITHER = 0.0                 # dither=0 (deterministic)

ONNX_PATH = os.environ.get("VOICEPRINT_ONNX_PATH", "/models/campplus.onnx")


# ---------------------------------------------------------------------------
# ONNX session (loaded once at module import / startup)
# ---------------------------------------------------------------------------

_session: Optional[ort.InferenceSession] = None
_input_name: Optional[str] = None
_output_name: Optional[str] = None


def _build_session_options() -> ort.SessionOptions:
    """CPU-tuned session options.

    intra_op_num_threads = cpu_count // 2 (spike used //2 = 2 on the 4-core build
    machine; see SPEC §4 P1-资源争用 — the build machine is shared with crawl4ai and
    is a docker build target). inter_op_num_threads = 1.
    """
    so = ort.SessionOptions()
    cpu = os.cpu_count() or 2
    intra = max(1, cpu // 2)
    so.intra_op_num_threads = intra
    so.inter_op_num_threads = 1
    so.graph_optimization_level = ort.GraphOptimizationLevel.ORT_ENABLE_ALL
    return so


def load_model() -> None:
    """Load the CAM++ ONNX model with CPUExecutionProvider.

    Input/output tensor names are read dynamically from get_inputs()/get_outputs()
    because the exported CosyVoice/3D-Speaker campplus.onnx does not use fixed names.
    """
    global _session, _input_name, _output_name
    if _session is not None:
        return
    if not os.path.exists(ONNX_PATH):
        # Defer the hard failure: /healthz can still report dim, and an /embed call
        # will surface a clear error. This keeps container startup observable.
        print(
            f"[voiceprint] WARNING: ONNX model not found at {ONNX_PATH}; "
            "embed calls will fail until the model is mounted.",
            file=sys.stderr,
        )
        return
    so = _build_session_options()
    try:
        _session = ort.InferenceSession(
            ONNX_PATH,
            sess_options=so,
            providers=["CPUExecutionProvider"],
        )
        _input_name = _session.get_inputs()[0].name
        _output_name = _session.get_outputs()[0].name
    except Exception as exc:  # noqa: BLE001 — a corrupt/incompatible .onnx must not crash startup
        # Keep _session=None so the container stays up and /embed soft-degrades
        # (valid=false). /healthz can still report dim; the model can be remounted.
        _session = None
        _input_name = None
        _output_name = None
        print(
            f"[voiceprint] ERROR: failed to load ONNX model at {ONNX_PATH}: {exc}; "
            "embed calls will return valid=false until a working model is mounted.",
            file=sys.stderr,
        )
        return
    print(
        f"[voiceprint] loaded {ONNX_PATH} "
        f"(input={_input_name}, output={_output_name}, "
        f"intra={so.intra_op_num_threads}, inter={so.inter_op_num_threads})",
        file=sys.stderr,
    )


# ---------------------------------------------------------------------------
# Feature extraction — MUST match CAM++ (mirror of the spike pipeline)
# ---------------------------------------------------------------------------

def _decode_pcm_s16le(audio_b64: str) -> np.ndarray:
    """Decode base64 s16le PCM into a float32 mono waveform.

    Returns waveform scaled to int16 range ([-32768, 32767] as float), matching
    torchaudio.compliance.kaldi.fbank's expectation (it internally treats the input
    as 16-bit-scaled samples). We do NOT divide by 32768 here — kaldi fbank wants
    the raw int16-scaled magnitude.
    """
    raw = base64.b64decode(audio_b64)
    pcm = np.frombuffer(raw, dtype="<i2")  # little-endian int16
    return pcm.astype(np.float32)


def _rms_int16(wave_i16scaled: np.ndarray) -> float:
    if wave_i16scaled.size == 0:
        return 0.0
    return float(np.sqrt(np.mean(np.square(wave_i16scaled, dtype=np.float64))))


def compute_fbank(wave_i16scaled: np.ndarray) -> np.ndarray:
    """80-mel Kaldi fbank + per-utterance mean normalization (CMN).

    Matches CAM++ / the spike (torchaudio.compliance.kaldi.fbank), specifically:
      - num_mel_bins=80, sample_frequency=16000, dither=0
      - per-utterance mean subtraction (mean_nor=True): subtract the mean over time,
        do NOT divide by variance.

    Returns a float32 array of shape [num_frames, 80].
    """
    # kaldi.fbank expects a 2-D tensor [channels, samples].
    wav = torch.from_numpy(wave_i16scaled).unsqueeze(0)
    feat = kaldi.fbank(
        wav,
        num_mel_bins=_FBANK_NUM_MEL_BINS,
        sample_frequency=_FBANK_SAMPLE_FREQUENCY,
        dither=_FBANK_DITHER,
    )  # [num_frames, 80]
    # Per-utterance mean normalization (subtract mean over time; no variance scaling).
    feat = feat - feat.mean(dim=0, keepdim=True)
    return feat.numpy().astype(np.float32)


def _l2_normalize(vec: np.ndarray) -> np.ndarray:
    norm = float(np.linalg.norm(vec))
    if norm < 1e-12:
        return vec
    return vec / norm


def embed_pcm(wave_i16scaled: np.ndarray) -> np.ndarray:
    """Run the CAM++ ONNX model on a waveform -> L2-normalized 192-d embedding."""
    if _session is None:
        raise RuntimeError(
            f"voiceprint model not loaded (expected at {ONNX_PATH})"
        )
    feat = compute_fbank(wave_i16scaled)            # [T, 80]
    # CAM++ ONNX expects a batched fbank: [batch, T, 80].
    model_input = feat[np.newaxis, :, :]            # [1, T, 80]
    outputs = _session.run([_output_name], {_input_name: model_input})
    emb = np.asarray(outputs[0]).reshape(-1).astype(np.float32)
    return _l2_normalize(emb)


# ---------------------------------------------------------------------------
# FastAPI app + schemas
# ---------------------------------------------------------------------------

@asynccontextmanager
async def lifespan(app: FastAPI):
    """Load the ONNX model once at startup (replaces the deprecated
    @app.on_event("startup") hook). load_model() never raises — a missing/corrupt
    model leaves _session=None and /embed soft-degrades to valid=false."""
    load_model()
    yield


app = FastAPI(title="Voiceprint Service (CAM++)", version="1.0.0", lifespan=lifespan)


class EmbedRequest(BaseModel):
    audio_b64: str = Field(..., description="base64 of s16le 16k mono PCM")
    sample_rate: int = Field(SAMPLE_RATE, description="must be 16000")
    session_id: Optional[str] = Field(None, description="meeting session id (observability)")
    segment_id: Optional[int] = Field(None, description="segment id (observability)")


class EmbedResponse(BaseModel):
    embedding: List[float]
    dim: int
    duration_ms: int
    valid: bool


@app.get("/healthz")
async def healthz():
    return {"ok": True, "dim": EMBED_DIM}


@app.post("/embed", response_model=EmbedResponse)
async def embed(req: EmbedRequest):
    # Model not loaded (missing/corrupt .onnx) → soft-degrade rather than 500.
    # The Go client treats valid=false as a non-error skip (the meeting continues).
    if _session is None:
        return EmbedResponse(embedding=[], dim=EMBED_DIM, duration_ms=0, valid=False)

    try:
        wave = _decode_pcm_s16le(req.audio_b64)

        # duration based on declared sample_rate (fall back to 16k if 0/missing).
        sr = req.sample_rate if req.sample_rate and req.sample_rate > 0 else SAMPLE_RATE
        duration_ms = int(round(wave.size / float(sr) * 1000.0))

        # valid=false on too-short or low-energy audio (don't even run the model).
        rms = _rms_int16(wave)
        if duration_ms < MIN_DURATION_MS or rms < MIN_RMS_S16LE:
            return EmbedResponse(
                embedding=[],
                dim=EMBED_DIM,
                duration_ms=duration_ms,
                valid=False,
            )

        emb = embed_pcm(wave)
        return EmbedResponse(
            embedding=emb.tolist(),
            dim=EMBED_DIM,
            duration_ms=duration_ms,
            valid=True,
        )
    except (binascii.Error, ValueError, RuntimeError) as exc:
        # Malformed base64 (binascii.Error), odd-byte / unparseable PCM (ValueError),
        # or model-not-loaded (RuntimeError from embed_pcm) → soft-degrade with a
        # 200 valid=false response (matches the EmbedResponse model) instead of a
        # bare 500 traceback. The Go client branches on valid, never on HTTP status.
        print(f"[voiceprint] /embed soft-degraded: {type(exc).__name__}: {exc}", file=sys.stderr)
        return EmbedResponse(embedding=[], dim=EMBED_DIM, duration_ms=0, valid=False)


@app.post("/diarize")
async def diarize():
    """Placeholder — owned by T2 (server-side VAD + sliding-window embedding +
    global AHC, ffmpeg-decoded webm/opus). See DIARIZATION_SPEC §7 T2."""
    return JSONResponse(
        status_code=501,
        content={
            "ok": False,
            "error": "not_implemented",
            "detail": "/diarize is implemented in T2 (offline diarization)",
        },
    )


if __name__ == "__main__":
    import uvicorn

    uvicorn.run(app, host="0.0.0.0", port=11236)
