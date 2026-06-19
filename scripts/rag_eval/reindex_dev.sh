#!/usr/bin/env bash
# reindex_dev.sh — Re-index (re-chunk + re-embed) one or more documents via the
# chunker admin endpoint POST /v1/admin/chunker/reindex.
#
# Setup / tunnel note:
#   The chunker endpoints live on the USER service (port 9091 dev / 19091 local).
#   Admin login is on the ADMIN service (port 9099 dev / 19099 local).
#   So the typical local workflow is:
#
#     1. Obtain an admin token from the admin service:
#          TOKEN=$(curl -s -X POST http://localhost:19099/v1/admin/login \
#            -H 'Content-Type: application/json' \
#            -d '{"username":"admin","password":"..."}' | jq -r .data.token)
#
#     2. Call the chunker endpoint on the USER service:
#          BASE_URL=http://localhost:19091 TOKEN=$TOKEN USER_ID=25 \
#            ./scripts/rag_eval/reindex_dev.sh 127 128 129
#
# Environment variables:
#   BASE_URL  — base URL of the USER service (default: http://localhost:19091)
#   TOKEN     — admin Bearer token (required)
#   USER_ID   — owner user_id of the documents; 0 = admin bypass (default: 0)
#
# Arguments: one or more document IDs (integers)

set -euo pipefail

BASE_URL="${BASE_URL:-http://localhost:19091}"
TOKEN="${TOKEN:-}"
USER_ID="${USER_ID:-0}"

if [[ -z "$TOKEN" ]]; then
  echo "ERROR: TOKEN env var is required (obtain via admin login on port 19099)." >&2
  exit 1
fi

if [[ $# -eq 0 ]]; then
  echo "Usage: $0 <doc_id> [doc_id ...]" >&2
  exit 1
fi

# Build JSON array of document IDs.
IDS_JSON="["
first=1
for id in "$@"; do
  if [[ "$first" -eq 1 ]]; then
    IDS_JSON+="$id"
    first=0
  else
    IDS_JSON+=",$id"
  fi
done
IDS_JSON+="]"

PAYLOAD=$(printf '{"document_ids":%s,"user_id":%s}' "$IDS_JSON" "$USER_ID")

echo "POST ${BASE_URL}/v1/admin/chunker/reindex"
echo "Payload: $PAYLOAD"
echo ""

curl -s -w "\nHTTP %{http_code}\n" \
  -X POST "${BASE_URL}/v1/admin/chunker/reindex" \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer ${TOKEN}" \
  -d "$PAYLOAD" | jq .
