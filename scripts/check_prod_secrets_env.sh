#!/usr/bin/env bash
# Validate the runtime prod secrets env-file used by docker --env-file.
#
# The checker compares config_prod.yaml ${NUMIND_*} placeholders with the
# committed prod-secrets.env.example and a real, uncommitted secrets.env file.
# It reports variable names and structural issues only; secret values are never
# printed.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/.." && pwd)"

ENV="${ENV:-prod}"
CONFIG_FILE="${CONFIG_FILE:-config_${ENV}.yaml}"
SECRETS_EXAMPLE="${SECRETS_EXAMPLE:-scripts/cicd/prod-secrets.env.example}"
SECRETS_FILE="${SECRETS_FILE:-/opt/numind/${ENV}/secrets.env}"

case "${ENV}" in
  dev|qa|prod) ;;
  *) echo "ERROR: ENV must be one of dev, qa, prod" >&2; exit 2 ;;
esac

if [[ "${ENV}" != "prod" ]]; then
  echo "prod-secrets-env: ENV=${ENV}; runtime prod secrets check skipped"
  exit 0
fi

resolve_path() {
  local path="$1"
  if [[ "${path}" == /* ]]; then
    printf '%s' "${path}"
  else
    printf '%s/%s' "${REPO_ROOT}" "${path}"
  fi
}

display_path() {
  local path="$1"
  if [[ "${path}" == "${REPO_ROOT}/"* ]]; then
    printf '%s' "${path#"${REPO_ROOT}/"}"
  else
    printf '%s' "${path}"
  fi
}

file_mode() {
  local path="$1"
  stat -f '%Lp' "${path}" 2>/dev/null || stat -c '%a' "${path}" 2>/dev/null || true
}

CONFIG_PATH="$(resolve_path "${CONFIG_FILE}")"
EXAMPLE_PATH="$(resolve_path "${SECRETS_EXAMPLE}")"
SECRETS_PATH="$(resolve_path "${SECRETS_FILE}")"

issues=0

issue() {
  printf 'ERROR: prod-secrets-env: %s\n' "$*" >&2
  issues=$((issues + 1))
}

require_file() {
  local path="$1"
  local label="$2"

  if [[ -f "${path}" ]]; then
    return 0
  fi

  issue "${label} not found: $(display_path "${path}")"
  return 1
}

TMP_DIR="$(mktemp -d)"
trap 'rm -rf "${TMP_DIR}"' EXIT

required_vars="${TMP_DIR}/required.vars"
example_vars="${TMP_DIR}/example.vars"

if require_file "${CONFIG_PATH}" "config file"; then
  grep -Eoh '\$\{NUMIND_[A-Za-z0-9_]+(:-[^}]*)?\}|\$NUMIND_[A-Za-z0-9_]+' "${CONFIG_PATH}" \
    | sed -E 's/^\$\{//; s/\}$//; s/:.*$//; s/^\$//' \
    | sort -u > "${required_vars}"
else
  : > "${required_vars}"
fi

if require_file "${EXAMPLE_PATH}" "secrets example"; then
  awk '
    /^[[:space:]]*($|#)/ { next }
    /^[A-Za-z_][A-Za-z0-9_]*=/ {
      key = $0
      sub(/=.*/, "", key)
      if (key ~ /^NUMIND_/) print key
      next
    }
  ' "${EXAMPLE_PATH}" | sort -u > "${example_vars}"
else
  : > "${example_vars}"
fi

required_count="$(wc -l < "${required_vars}" | tr -d '[:space:]')"
if [[ "${required_count}" == "0" ]]; then
  issue "no NUMIND_* placeholders found in $(display_path "${CONFIG_PATH}")"
fi

if [[ -s "${required_vars}" && -s "${example_vars}" ]]; then
  while IFS= read -r missing; do
    [[ -n "${missing}" ]] || continue
    issue "secrets example is missing required variable ${missing}"
  done < <(comm -23 "${required_vars}" "${example_vars}")
fi

if require_file "${SECRETS_PATH}" "secrets env-file"; then
  mode="$(file_mode "${SECRETS_PATH}")"
  if [[ -n "${mode}" && "${mode}" =~ ^[0-7]+$ ]]; then
    mode3="${mode}"
    while [[ "${#mode3}" -lt 3 ]]; do
      mode3="0${mode3}"
    done
    mode3="${mode3:${#mode3}-3}"
    group_digit="${mode3:1:1}"
    other_digit="${mode3:2:1}"
    if (( group_digit != 0 || other_digit != 0 )); then
      issue "secrets env-file must not be group/world readable; run: chmod 600 $(display_path "${SECRETS_PATH}")"
    fi
  fi

  awk -v required_file="${required_vars}" -v example_file="${example_vars}" '
    BEGIN {
      while ((getline key < required_file) > 0) {
        if (key != "") required[key] = 1
      }
      close(required_file)
      while ((getline key < example_file) > 0) {
        if (key != "") example[key] = 1
      }
      close(example_file)
      issues = 0
    }
    function trim(s) {
      gsub(/^[[:space:]]+|[[:space:]]+$/, "", s)
      return s
    }
    function lower(s) { return tolower(s) }
    function emit(line_no, rule, key) {
      if (key == "") key = "(line)"
      printf "ERROR: prod-secrets-env: line %d: %s key=%s\n", line_no, rule, key > "/dev/stderr"
      issues++
    }
    function is_placeholder(value, v) {
      v = lower(trim(value))
      if (v == "") return 1
      if (v ~ /^\$\{?NUMIND_[A-Za-z0-9_]+/) return 1
      if (v ~ /^(todo|tbd|changeme|change-me|replace-me|placeholder|null|nil|none|unset|dummy|example|your_.+|xxx+)$/) return 1
      if (v ~ /^(填写|待填写|占位|替换|请替换)$/) return 1
      return 0
    }
    /^[[:space:]]*($|#)/ { next }
    {
      raw = $0
      sub(/\r$/, "", raw)
      if (raw ~ /^[[:space:]]/ || raw ~ /[[:space:]]$/) {
        emit(NR, "leading-or-trailing-whitespace", "")
        next
      }
      if (raw ~ /^export[[:space:]]+/) {
        emit(NR, "export-prefix-not-supported", "")
        next
      }
      if (raw !~ /^[A-Za-z_][A-Za-z0-9_]*=/) {
        emit(NR, "malformed-env-line", "")
        next
      }

      key = raw
      sub(/=.*/, "", key)
      value = raw
      sub(/^[^=]*=/, "", value)

      if (seen[key]) {
        emit(NR, "duplicate-key", key)
      }
      seen[key] = 1

      if (value ~ /^["\047]/ || value ~ /["\047]$/) {
        emit(NR, "quoted-value-not-supported-by-docker-env-file", key)
      }

      if (is_placeholder(value)) {
        emit(NR, "empty-or-placeholder-value", key)
      }

      if (key in required) {
        present[key] = 1
      } else if (key ~ /^NUMIND_/ && !(key in example)) {
        printf "WARN: prod-secrets-env: line %d: extra NUMIND variable key=%s\n", NR, key > "/dev/stderr"
      }
    }
    END {
      for (key in required) {
        if (!(key in present)) {
          printf "ERROR: prod-secrets-env: missing required variable %s\n", key > "/dev/stderr"
          issues++
        }
      }
      exit issues ? 1 : 0
    }
  ' "${SECRETS_PATH}" || issues=$((issues + 1))
fi

if ((issues > 0)); then
  echo "prod-secrets-env: failed with ${issues} issue group(s)" >&2
  exit 1
fi

echo "prod-secrets-env: checked $(display_path "${SECRETS_PATH}") against ${required_count} required NUMIND_* variable(s); passed"
