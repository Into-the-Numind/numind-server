#!/usr/bin/env bash
# Audit script for aihubmix-protocol-audit Task 7b (Q10=A / S3 P2-E)
#
# Run BEFORE migrations/20260421_000002_audit_user_model_preference.sql to capture
# existing user_model_preference rows affected by the thinking_only flag correction.
# Output is tee'd to a log file committed alongside this script for audit trail.
#
# Usage (from SSH box with DB access):
#   bash migrations/audit/20260421_preference_audit.sh > migrations/audit/20260421_preference_audit_output.log
#
# Requires env: MYSQL_HOST, MYSQL_USER, MYSQL_PASS, MYSQL_DB
# (or edit the mysql command inline for dev box — use dev creds from CLAUDE.md §7)

set -euo pipefail

MYSQL_HOST="${MYSQL_HOST:-numind-mysql-dev:3306}"
MYSQL_USER="${MYSQL_USER:-root}"
MYSQL_PASS="${MYSQL_PASS:-Numind2025}"
MYSQL_DB="${MYSQL_DB:-numind-dev}"

echo "=== user_model_preference audit 20260421 ==="
echo "Pre-migration snapshot — capture rows that may experience behavior change after 20260421_000001 flag fix"
echo ""

mysql -h"${MYSQL_HOST%:*}" -P"${MYSQL_HOST##*:}" -u"${MYSQL_USER}" -p"${MYSQL_PASS}" -D"${MYSQL_DB}" -e "
SELECT p.user_id, p.feature, p.model_key, p.thinking,
       s.supports_thinking AS current_supports, s.thinking_only AS current_only
FROM user_model_preference p
JOIN ai_service s ON s.model_key = p.model_key
WHERE s.model_key IN (
    'claude-sonnet-4-6-thinking',
    'deepseek-v3.2',
    'gpt-5.4',
    'gemini-3.1-pro-preview-thinking',
    'deepseek-v3.2-thinking',
    'gpt-5.4-thinking'
)
ORDER BY p.model_key, p.user_id;
"

echo ""
echo "=== Counts per model_key ==="
mysql -h"${MYSQL_HOST%:*}" -P"${MYSQL_HOST##*:}" -u"${MYSQL_USER}" -p"${MYSQL_PASS}" -D"${MYSQL_DB}" -e "
SELECT model_key, thinking, COUNT(*) AS row_count
FROM user_model_preference
WHERE model_key IN (
    'claude-sonnet-4-6-thinking',
    'deepseek-v3.2',
    'gpt-5.4',
    'gemini-3.1-pro-preview-thinking',
    'deepseek-v3.2-thinking',
    'gpt-5.4-thinking'
)
GROUP BY model_key, thinking
ORDER BY model_key, thinking;
"

echo ""
echo "=== DONE. Commit the output log to migrations/audit/ for audit trail. ==="
