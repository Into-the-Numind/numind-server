#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
CALCULATOR="$SCRIPT_DIR/calculate-sandbox-capacity.sh"
TEMP_DIR="$(mktemp -d)"
trap 'rm -rf "$TEMP_DIR"' EXIT

make_samples() {
  local output="$1"
  local start="$2"
  local end="$3"
  local value="$4"
  local count="${5:-169}"
  awk -v start="$start" -v end="$end" -v value="$value" -v count="$count" '
    BEGIN {
      print "observed_at_epoch,mem_available_bytes,business_window"
      for (i = 0; i < count; i++) {
        timestamp = int(start + ((end - start) * i / (count - 1)))
        print timestamp "," value ",1"
      }
    }
  ' >"$output"
}

START=1784505600
SEVEN_DAYS=$((7 * 24 * 60 * 60))
SEVENTY_TWO_HOURS=$((72 * 60 * 60))
EIGHT_GIB=$((8 * 1024 * 1024 * 1024))
THREE_GIB=$((3 * 1024 * 1024 * 1024))

make_samples "$TEMP_DIR/history.csv" "$START" "$((START + SEVEN_DAYS))" "$EIGHT_GIB"
"$CALCULATOR" \
  --mode historical \
  --samples "$TEMP_DIR/history.csv" >"$TEMP_DIR/history.json"

python3 - "$TEMP_DIR/history.json" <<'PY'
import json
import sys

with open(sys.argv[1], encoding="utf-8") as source:
    result = json.load(source)
assert result["status"] == "ready"
assert result["evidence_mode"] == "historical"
assert result["parent_memory_max_bytes"] == 11 * 1024**3 // 4
assert result["workload_memory_max_bytes"] == 9 * 1024**3 // 4
assert result["workload_memory_high_bytes"] == 2 * 1024**3
assert result["systemd"]["NUMIND_SANDBOX_PARENT_MEMORY_MAX_BYTES"] == result["parent_memory_max_bytes"]
PY

make_samples "$TEMP_DIR/fresh.csv" "$START" "$((START + SEVENTY_TWO_HOURS))" "$EIGHT_GIB" 73
"$CALCULATOR" \
  --mode fresh \
  --samples "$TEMP_DIR/fresh.csv" >"$TEMP_DIR/fresh.json"

if "$CALCULATOR" \
  --mode historical \
  --samples "$TEMP_DIR/fresh.csv" >"$TEMP_DIR/short.json" 2>"$TEMP_DIR/short.err"; then
  echo "historical evidence shorter than seven days was accepted" >&2
  exit 1
fi
python3 - "$TEMP_DIR/short.json" <<'PY'
import json
import sys

with open(sys.argv[1], encoding="utf-8") as source:
    result = json.load(source)
assert result["status"] == "blocked"
assert result["reason"] == "insufficient_evidence"
PY

make_samples "$TEMP_DIR/low.csv" "$START" "$((START + SEVEN_DAYS))" "$THREE_GIB"
if "$CALCULATOR" \
  --mode historical \
  --samples "$TEMP_DIR/low.csv" >"$TEMP_DIR/low.json" 2>"$TEMP_DIR/low.err"; then
  echo "unsafe low-memory capacity was accepted" >&2
  exit 1
fi
python3 - "$TEMP_DIR/low.json" <<'PY'
import json
import sys

with open(sys.argv[1], encoding="utf-8") as source:
    result = json.load(source)
assert result["status"] == "blocked"
assert result["reason"] == "insufficient_capacity"
assert result["parent_memory_max_bytes"] < 2 * 1024**3
PY

cp "$TEMP_DIR/history.csv" "$TEMP_DIR/malformed.csv"
printf '%s\n' 'not-an-epoch,123,1' >>"$TEMP_DIR/malformed.csv"
if "$CALCULATOR" \
  --mode historical \
  --samples "$TEMP_DIR/malformed.csv" >"$TEMP_DIR/malformed.json" 2>/dev/null; then
  echo "malformed sample input was accepted" >&2
  exit 1
fi

echo "sandbox capacity tests passed"
