# Voiceprint microservice (CAM++ speaker embedding) — CPU only.
# See docs/meeting/DIARIZATION_SPEC.md §5 D1 / §7 T1.
#
# China machine: pip uses the Tsinghua mirror; apt uses the Tencent Cloud mirror.
# ffmpeg is installed for T2's /diarize (decodes webm/opus full recordings to PCM);
# T1 itself does not invoke ffmpeg but the container is shared with T2.

FROM python:3.10-slim

# --- apt: switch to Tencent Cloud mirror, install ffmpeg ---
# Debian 12 (bookworm) uses the deb822 sources format under /etc/apt/sources.list.d/.
RUN set -eux; \
    if [ -f /etc/apt/sources.list.d/debian.sources ]; then \
        sed -i 's@deb.debian.org@mirrors.tencent.com@g' /etc/apt/sources.list.d/debian.sources; \
    fi; \
    if [ -f /etc/apt/sources.list ]; then \
        sed -i 's@deb.debian.org@mirrors.tencent.com@g' /etc/apt/sources.list; \
    fi; \
    apt-get update; \
    apt-get install -y --no-install-recommends ffmpeg; \
    rm -rf /var/lib/apt/lists/*

WORKDIR /app

# --- pip: Tsinghua mirror; torch CPU wheels from the Tsinghua PyTorch mirror ---
ENV PIP_INDEX_URL=https://pypi.tuna.tsinghua.edu.cn/simple \
    PIP_NO_CACHE_DIR=1

COPY requirements.txt .
# torch/torchaudio CPU wheels live in a dedicated index; --extra-index-url keeps
# the Tsinghua PyPI mirror as the primary source for everything else.
RUN pip install --no-cache-dir \
        --extra-index-url https://mirrors.tuna.tsinghua.edu.cn/pytorch-wheels/cpu \
        -r requirements.txt

COPY app.py .
COPY entrypoint.sh .
RUN chmod +x entrypoint.sh

# CAM++ ONNX model is mounted at runtime (-v) at VOICEPRINT_ONNX_PATH.
ENV VOICEPRINT_ONNX_PATH=/models/campplus.onnx

EXPOSE 11236

ENTRYPOINT ["./entrypoint.sh"]
