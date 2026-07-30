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
        local_hour = int(((timestamp + 8 * 60 * 60) % (24 * 60 * 60)) / (60 * 60))
        business = (local_hour >= 8 && local_hour < 23) ? 1 : 0
        print timestamp "," value "," business
      }
    }
  ' >"$output"
}

SEVEN_DAYS=$((7 * 24 * 60 * 60))
SEVENTY_TWO_HOURS=$((72 * 60 * 60))
EIGHT_GIB=$((8 * 1024 * 1024 * 1024))
THREE_GIB=$((3 * 1024 * 1024 * 1024))
NOW="$(date +%s)"
HISTORY_START=$((NOW - SEVEN_DAYS))
FRESH_START=$((NOW - SEVENTY_TWO_HOURS))

make_samples "$TEMP_DIR/history.csv" "$HISTORY_START" "$NOW" "$EIGHT_GIB"
"$CALCULATOR" \
  --mode historical \
  --samples "$TEMP_DIR/history.csv" >"$TEMP_DIR/history.json"

python3 -I - "$TEMP_DIR/history.json" <<'PY'
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

make_samples "$TEMP_DIR/fresh.csv" "$FRESH_START" "$NOW" "$EIGHT_GIB" 73
"$CALCULATOR" \
  --mode fresh \
  --samples "$TEMP_DIR/fresh.csv" >"$TEMP_DIR/fresh.json"

if "$CALCULATOR" \
  --mode historical \
  --samples "$TEMP_DIR/fresh.csv" >"$TEMP_DIR/short.json" 2>"$TEMP_DIR/short.err"; then
  echo "historical evidence shorter than seven days was accepted" >&2
  exit 1
fi
python3 -I - "$TEMP_DIR/short.json" <<'PY'
import json
import sys

with open(sys.argv[1], encoding="utf-8") as source:
    result = json.load(source)
assert result["status"] == "blocked"
assert result["reason"] == "insufficient_evidence"
PY

make_samples "$TEMP_DIR/low.csv" "$HISTORY_START" "$NOW" "$THREE_GIB"
if "$CALCULATOR" \
  --mode historical \
  --samples "$TEMP_DIR/low.csv" >"$TEMP_DIR/low.json" 2>"$TEMP_DIR/low.err"; then
  echo "unsafe low-memory capacity was accepted" >&2
  exit 1
fi
python3 -I - "$TEMP_DIR/low.json" <<'PY'
import json
import sys

with open(sys.argv[1], encoding="utf-8") as source:
    result = json.load(source)
assert result["status"] == "blocked"
assert result["reason"] == "insufficient_capacity"
assert result["parent_memory_max_bytes"] < 2 * 1024**3
assert "systemd" not in result
PY

awk -F, -v OFS=, '
  NR == 1 { print; next }
  {
    $3 = (NR == 2 || NR == 170) ? 1 : 0
    print
  }
' "$TEMP_DIR/history.csv" >"$TEMP_DIR/cherry-picked.csv"
if "$CALCULATOR" \
  --mode historical \
  --samples "$TEMP_DIR/cherry-picked.csv" >"$TEMP_DIR/cherry-picked.json" 2>/dev/null; then
  echo "two cherry-picked business-window samples were accepted" >&2
  exit 1
fi
python3 -I - "$TEMP_DIR/cherry-picked.json" <<'PY'
import json
import sys

with open(sys.argv[1], encoding="utf-8") as source:
    result = json.load(source)
assert result["status"] == "blocked"
assert result["reason"] == "invalid_evidence"
assert "systemd" not in result
PY

make_samples \
  "$TEMP_DIR/stale.csv" \
  "$((HISTORY_START - 7200))" \
  "$((NOW - 7200))" \
  "$EIGHT_GIB"
if "$CALCULATOR" \
  --mode historical \
  --samples "$TEMP_DIR/stale.csv" >"$TEMP_DIR/stale.json" 2>/dev/null; then
  echo "stale capacity evidence was accepted" >&2
  exit 1
fi

mkdir "$TEMP_DIR/poison"
printf '%s\n' 'raise RuntimeError("poison csv module loaded")' >"$TEMP_DIR/poison/csv.py"
(
  cd "$TEMP_DIR/poison"
  PYTHONPATH="$TEMP_DIR/poison" "$CALCULATOR" \
    --mode historical \
    --samples "$TEMP_DIR/history.csv" >"$TEMP_DIR/isolated.json"
)
python3 -I - "$TEMP_DIR/isolated.json" <<'PY'
import json
import sys

with open(sys.argv[1], encoding="utf-8") as source:
    result = json.load(source)
assert result["status"] == "ready"
PY

cp "$TEMP_DIR/history.csv" "$TEMP_DIR/malformed.csv"
printf '%s\n' '"unterminated,123,1' >>"$TEMP_DIR/malformed.csv"
if "$CALCULATOR" \
  --mode historical \
  --samples "$TEMP_DIR/malformed.csv" >"$TEMP_DIR/malformed.json" 2>/dev/null; then
  echo "malformed sample input was accepted" >&2
  exit 1
fi

ln -s "$TEMP_DIR/history.csv" "$TEMP_DIR/evidence-link.csv"
if "$CALCULATOR" \
  --mode historical \
  --samples "$TEMP_DIR/evidence-link.csv" >/dev/null 2>&1; then
  echo "symlinked capacity evidence was accepted" >&2
  exit 1
fi

echo "sandbox capacity tests passed"
