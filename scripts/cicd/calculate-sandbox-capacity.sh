#!/usr/bin/env bash
set -euo pipefail

usage() {
  echo "usage: $0 --mode historical|fresh --samples <csv-file>" >&2
  exit 64
}

MODE=""
SAMPLES=""
while (($# > 0)); do
  case "$1" in
    --mode)
      (($# >= 2)) || usage
      MODE="$2"
      shift 2
      ;;
    --samples)
      (($# >= 2)) || usage
      SAMPLES="$2"
      shift 2
      ;;
    *)
      usage
      ;;
  esac
done

[[ "$MODE" == "historical" || "$MODE" == "fresh" ]] || usage
[[ -n "$SAMPLES" && -f "$SAMPLES" && ! -L "$SAMPLES" ]] || usage

exec python3 -I - "$MODE" "$SAMPLES" <<'PY'
import csv
import io
import json
import os
import stat
import sys
import time

MIB = 1024**2
GIB = 1024**3
PARENT_POC_MAX = 11 * GIB // 4
CORE_RESERVE = 5 * GIB // 4
CONTROL_RESERVE = 384 * MIB
CONTROL_HIGH = 256 * MIB
PARENT_HEADROOM = 128 * MIB
PARENT_MINIMUM = 2 * GIB
WORKLOAD_MINIMUM = 3 * GIB // 2
WORKLOAD_HIGH_MAX = 2 * GIB
FLOOR_QUANTUM = 64 * MIB
SAMPLE_MAXIMUM = 1 << 60
MAX_SAMPLES = 1_000_000
MAX_SAMPLE_GAP_SECONDS = 60 * 60
MAX_BUSINESS_GAP_SECONDS = 24 * 60 * 60
MAX_EVIDENCE_AGE_SECONDS = 60 * 60
MAX_FUTURE_SKEW_SECONDS = 5 * 60
BUSINESS_DIVISOR = 6
BUSINESS_START_HOUR = 8
BUSINESS_END_HOUR = 23
BUSINESS_UTC_OFFSET_SECONDS = 8 * 60 * 60
MINIMUM_SECONDS = {
    "historical": 7 * 24 * 60 * 60,
    "fresh": 72 * 60 * 60,
}
HEADER = [
    "observed_at_epoch",
    "mem_available_bytes",
    "business_window",
]


def emit(payload, code):
    json.dump(payload, sys.stdout, sort_keys=True, separators=(",", ":"))
    sys.stdout.write("\n")
    raise SystemExit(code)


def blocked(reason, summary, **values):
    emit(
        {
            "status": "blocked",
            "reason": reason,
            "summary": summary,
            **values,
        },
        2 if reason != "insufficient_capacity" else 3,
    )


mode = sys.argv[1]
sample_path = sys.argv[2]
samples = []
source = None
try:
    if not hasattr(os, "O_NOFOLLOW"):
        raise ValueError("O_NOFOLLOW is required")
    flags = os.O_RDONLY | os.O_CLOEXEC | os.O_NOFOLLOW
    descriptor = os.open(sample_path, flags)
    file_stat = os.fstat(descriptor)
    if not stat.S_ISREG(file_stat.st_mode) or file_stat.st_nlink != 1:
        os.close(descriptor)
        raise ValueError("evidence is not one regular file")
    source = io.TextIOWrapper(
        os.fdopen(descriptor, "rb", closefd=True),
        encoding="utf-8",
        newline="",
    )
    with source:
        reader = csv.reader(source, strict=True)
        if next(reader, None) != HEADER:
            blocked(
                "invalid_evidence",
                "Capacity evidence has an invalid CSV header.",
            )
        for row in reader:
            if len(row) != 3 or len(samples) >= MAX_SAMPLES:
                blocked(
                    "invalid_evidence",
                    "Capacity evidence has an invalid row count or shape.",
                )
            observed = int(row[0], 10)
            available = int(row[1], 10)
            if row[2] not in ("0", "1"):
                raise ValueError("invalid business-window flag")
            if observed <= 0 or available <= 0 or available > SAMPLE_MAXIMUM:
                raise ValueError("sample outside fixed bounds")
            local_hour = (
                (observed + BUSINESS_UTC_OFFSET_SECONDS)
                % (24 * 60 * 60)
                // (60 * 60)
            )
            expected_business = (
                BUSINESS_START_HOUR
                <= local_hour
                < BUSINESS_END_HOUR
            )
            if (row[2] == "1") != expected_business:
                raise ValueError("business-window flag disagrees with timestamp")
            samples.append((observed, available, expected_business))
        after_stat = os.fstat(descriptor)
        stable_fields = (
            "st_dev",
            "st_ino",
            "st_nlink",
            "st_size",
            "st_mtime_ns",
            "st_ctime_ns",
        )
        if any(
            getattr(file_stat, field) != getattr(after_stat, field)
            for field in stable_fields
        ):
            raise ValueError("evidence changed while being read")
except (OSError, UnicodeError, ValueError, csv.Error):
    blocked(
        "invalid_evidence",
        "Capacity evidence could not be parsed within fixed bounds.",
    )

if len(samples) < 2:
    blocked(
        "invalid_evidence",
        "Capacity evidence must contain at least two samples.",
    )
samples.sort(key=lambda sample: sample[0])
evaluated_at = int(time.time())
if any(
    current[0] <= previous[0]
    for previous, current in zip(samples, samples[1:])
):
    blocked(
        "invalid_evidence",
        "Capacity evidence contains duplicate timestamps.",
    )
if any(
    current[0] - previous[0] > MAX_SAMPLE_GAP_SECONDS
    for previous, current in zip(samples, samples[1:])
):
    blocked(
        "insufficient_evidence",
        "Release blocked: capacity evidence contains a sampling gap longer than one hour.",
        evidence_mode=mode,
        total_samples=len(samples),
    )
if samples[-1][0] > evaluated_at + MAX_FUTURE_SKEW_SECONDS:
    blocked(
        "invalid_evidence",
        "Capacity evidence contains a sample too far in the future.",
    )
if evaluated_at - samples[-1][0] > MAX_EVIDENCE_AGE_SECONDS:
    blocked(
        "insufficient_evidence",
        "Release blocked: capacity evidence is stale.",
        evidence_mode=mode,
        evidence_end_epoch=samples[-1][0],
        evaluated_at_epoch=evaluated_at,
    )

duration = samples[-1][0] - samples[0][0]
business_samples = [sample for sample in samples if sample[2]]
business_values = sorted(sample[1] for sample in business_samples)
business_covers_window = (
    len(business_samples) >= 2
    and business_samples[0][0] - samples[0][0] <= MAX_BUSINESS_GAP_SECONDS
    and samples[-1][0] - business_samples[-1][0] <= MAX_BUSINESS_GAP_SECONDS
    and all(
        current[0] - previous[0] <= MAX_BUSINESS_GAP_SECONDS
        for previous, current in zip(
            business_samples,
            business_samples[1:],
        )
    )
)
if (
    duration < MINIMUM_SECONDS[mode]
    or len(business_values) * BUSINESS_DIVISOR < len(samples)
    or not business_covers_window
):
    blocked(
        "insufficient_evidence",
        "Release blocked: the required evidence window or business-period samples are incomplete.",
        evidence_mode=mode,
        evidence_duration_seconds=duration,
        total_samples=len(samples),
        business_samples=len(business_values),
    )

rank = (len(business_values) + 99) // 100
baseline = business_values[rank - 1]
parent_candidate = min(PARENT_POC_MAX, max(0, baseline - CORE_RESERVE))
parent_max = parent_candidate // FLOOR_QUANTUM * FLOOR_QUANTUM
workload_max = max(
    0,
    parent_max - CONTROL_RESERVE - PARENT_HEADROOM,
)
workload_high = min(
    WORKLOAD_HIGH_MAX,
    workload_max * 90 // 100,
)
result = {
    "evidence_mode": mode,
    "evidence_start_epoch": samples[0][0],
    "evidence_end_epoch": samples[-1][0],
    "evidence_duration_seconds": duration,
    "total_samples": len(samples),
    "business_samples": len(business_values),
    "baseline_mem_available_bytes": baseline,
    "parent_memory_max_bytes": parent_max,
    "workload_memory_max_bytes": workload_max,
    "workload_memory_high_bytes": workload_high,
    "workload_memory_recovery_bytes": workload_max * 80 // 100,
    "workload_memory_shed_bytes": workload_max * 96 // 100,
    "control_memory_high_bytes": CONTROL_HIGH,
    "control_memory_max_bytes": CONTROL_RESERVE,
    "parent_headroom_bytes": PARENT_HEADROOM,
}
if parent_max < PARENT_MINIMUM or workload_max < WORKLOAD_MINIMUM:
    blocked(
        "insufficient_capacity",
        "Release blocked: this host cannot preserve the required core-service memory reserve.",
        **result,
    )

result["systemd"] = {
    "NUMIND_SANDBOX_PARENT_MEMORY_MAX_BYTES": result["parent_memory_max_bytes"],
    "NUMIND_SANDBOX_WORKLOAD_MEMORY_MAX_BYTES": result["workload_memory_max_bytes"],
    "NUMIND_SANDBOX_WORKLOAD_MEMORY_HIGH_BYTES": result["workload_memory_high_bytes"],
    "NUMIND_SANDBOX_WORKLOAD_MEMORY_RECOVERY_BYTES": result["workload_memory_recovery_bytes"],
    "NUMIND_SANDBOX_WORKLOAD_MEMORY_SHED_BYTES": result["workload_memory_shed_bytes"],
    "NUMIND_SANDBOX_CONTROL_MEMORY_HIGH_BYTES": result["control_memory_high_bytes"],
    "NUMIND_SANDBOX_CONTROL_MEMORY_MAX_BYTES": result["control_memory_max_bytes"],
    "NUMIND_SANDBOX_PARENT_HEADROOM_BYTES": result["parent_headroom_bytes"],
}
emit(
    {
        "status": "ready",
        "reason": "",
        "summary": "Capacity evidence is sufficient; derived systemd ceilings are ready for release preflight.",
        **result,
    },
    0,
)
PY
