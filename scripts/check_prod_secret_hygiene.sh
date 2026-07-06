#!/usr/bin/env bash
# Prod config secret hygiene gate.
#
# This is intentionally conservative for prod builds: config_prod.yaml is copied
# into the Docker image, so real secret-like material must live in runtime
# env-files instead (for example /opt/numind/prod/secrets.env).
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/.." && pwd)"

ENV="${ENV:-prod}"
case "${ENV}" in
  dev|qa|prod) ;;
  *) echo "ERROR: ENV must be one of dev, qa, prod" >&2; exit 1 ;;
esac

CONFIG_FILE="${CONFIG_FILE:-config_${ENV}.yaml}"

display_path() {
  local path="$1"
  if [[ "${path}" == "${REPO_ROOT}/"* ]]; then
    printf '%s' "${path#"${REPO_ROOT}/"}"
  else
    printf '%s' "${path}"
  fi
}

if [[ "${ENV}" != "prod" ]]; then
  echo "prod-secret-hygiene: ENV=${ENV}; strict prod config scan skipped (${CONFIG_FILE})"
  exit 0
fi

scan_file() {
  local path="$1"
  local rel_path

  if [[ "${path}" != /* ]]; then
    path="${REPO_ROOT}/${path}"
  fi
  rel_path="$(display_path "${path}")"

  if [[ ! -f "${path}" ]]; then
    echo "ERROR: prod-secret-hygiene: config file not found: ${rel_path}" >&2
    return 1
  fi

  awk -v file="${rel_path}" '
    function ltrim(s) { sub(/^[[:space:]]+/, "", s); return s }
    function rtrim(s) { sub(/[[:space:]]+$/, "", s); return s }
    function trim(s) { return rtrim(ltrim(s)) }
    function lower(s) { return tolower(s) }
    function indent_of(s,    copy) {
      copy = s
      sub(/[^[:space:]].*$/, "", copy)
      return length(copy)
    }
    function strip_quotes(s) {
      s = trim(s)
      if ((s ~ /^".*"$/) || (s ~ /^\047.*\047$/)) {
        return substr(s, 2, length(s) - 2)
      }
      return s
    }
    function strip_inline_comment(s,    out, i, c, quote, prev) {
      out = ""
      quote = ""
      prev = ""
      for (i = 1; i <= length(s); i++) {
        c = substr(s, i, 1)
        if (quote == "" && (c == "\"" || c == "\047")) {
          quote = c
        } else if (quote != "" && c == quote && prev != "\\") {
          quote = ""
        }
        if (quote == "" && c == "#" && (i == 1 || substr(s, i - 1, 1) ~ /[[:space:]]/)) {
          break
        }
        out = out c
        prev = c
      }
      return out
    }
    function key_label(key) {
      if (key == "") {
        return "(line)"
      }
      return key
    }
    function emit(line_no, rule, key) {
      printf "%s:%d: %s key=%s\n", file, line_no, rule, key_label(key)
      findings++
    }
    function is_placeholder(value, v) {
      v = lower(strip_quotes(trim(value)))
      if (v == "" || v == "|" || v == ">" || v == "|-" || v == ">-") return 1
      if (v ~ /^\$\{[A-Za-z_][A-Za-z0-9_]*(:-[^}]*)?\}$/) return 1
      if (v ~ /^\$[A-Za-z_][A-Za-z0-9_]*$/) return 1
      if (v ~ /^<[^>]+>$/) return 1
      if (v ~ /^(todo|tbd|changeme|change-me|replace-me|placeholder|null|nil|none|unset|dummy|example|your_.+|xxx+)$/) return 1
      if (v ~ /^(填写|待填写|占位|替换|请替换)$/) return 1
      return 0
    }
    function is_low_risk_key_value(key, value, k, v) {
      k = lower(key)
      v = strip_quotes(trim(value))
      if (k == "notify_url") return 1
      if (k == "mch_private_key_path") return 1
      if (k == "wechatpay_cert_path") return 1
      if (k == "wechatpay_public_key_id") return 1
      if (k ~ /public_key_id$/) return 1
      if ((k ~ /(cert|certificate|key)_path$/ || k ~ /_path$/ || k == "path") &&
          (v ~ /^\// || v ~ /^\.\.?\// || v ~ /\// || v ~ /\.(pem|key|crt|cert|cer|p12|pfx)$/)) return 1
      return 0
    }
    function looks_base64_secret(value, v) {
      v = strip_quotes(trim(value))
      gsub(/[[:space:]]/, "", v)
      if (length(v) < 80) return 0
      if (v ~ /^[A-Za-z0-9+\/=]{80,}$/ && v !~ /^https?:\/\//) return 1
      return 0
    }
    function scan_value(line_no, key, value, k, v) {
      k = lower(key)
      v = strip_quotes(trim(value))
      if (v ~ /BEGIN[[:space:]][A-Z0-9 ]*PRIVATE KEY/) {
        emit(line_no, "pem-private-key", key)
        return
      }
      if (v ~ /(^|[^A-Za-z0-9])sk-[A-Za-z0-9_-]{20,}/) {
        emit(line_no, "openai-sk", key)
      }
      if (v ~ /(^|[^A-Za-z0-9])AKID[A-Za-z0-9]{12,}/) {
        emit(line_no, "akid", key)
      }
      if (looks_base64_secret(v) && k !~ /(url|uri|path|public|cert)/) {
        emit(line_no, "long-base64-like-secret", key)
      }
    }
    BEGIN { findings = 0 }
    {
      raw = $0
      sub(/\r$/, "", raw)
      line = trim(raw)
      indent = indent_of(raw)

      if (in_sensitive_block) {
        if (line == "" || line ~ /^#/) next
        if (indent > block_indent) {
          value = strip_inline_comment(raw)
          if (!is_placeholder(value)) {
            emit(NR, "multiline-secret-key-value", block_key)
            scan_value(NR, block_key, value)
          }
          next
        }
        in_sensitive_block = 0
      }

      if (line ~ /^#/) next

      if (raw ~ /BEGIN[[:space:]][A-Z0-9 ]*PRIVATE KEY/) {
        emit(NR, "pem-private-key", current_key)
      }

      candidate = raw
      sub(/^[[:space:]-]*/, "", candidate)
      if (candidate ~ /^[A-Za-z0-9_.-]+[[:space:]]*:/) {
        key = candidate
        sub(/[[:space:]]*:.*/, "", key)
        value = candidate
        sub(/^[^:]*:[[:space:]]*/, "", value)
        value = strip_inline_comment(value)
        current_key = key

        if (lower(key) ~ /(secret|private_key|api_key|access_key|token|password)/) {
          if (trim(value) == "|" || trim(value) == ">" || trim(value) == "|-" || trim(value) == ">-") {
            in_sensitive_block = 1
            block_key = key
            block_indent = indent
            next
          }
          if (is_placeholder(value)) next
          if (is_low_risk_key_value(key, value)) next
          emit(NR, "secret-key-value", key)
          scan_value(NR, key, value)
          next
        }

        scan_value(NR, current_key, value)
        next
      }

      scan_value(NR, current_key, raw)
    }
    END { exit findings ? 1 : 0 }
  ' "${path}"
}

declare -a SCAN_FILES=("${CONFIG_FILE}")
if [[ -n "${EXTRA_CONFIG_FILES:-}" ]]; then
  while IFS= read -r extra_config_file; do
    [[ -n "${extra_config_file}" ]] || continue
    SCAN_FILES+=("${extra_config_file}")
  done < <(printf '%s\n' "${EXTRA_CONFIG_FILES}" | tr ':,' '\n')
fi

scan_rc=0
scan_output=""
for scan_path in "${SCAN_FILES[@]}"; do
  if file_output="$(scan_file "${scan_path}")"; then
    :
  else
    scan_rc=1
  fi
  if [[ -n "${file_output:-}" ]]; then
    if [[ -n "${scan_output}" ]]; then
      scan_output="${scan_output}"$'\n'"${file_output}"
    else
      scan_output="${file_output}"
    fi
  fi
  unset file_output
done

if [[ "${scan_rc}" -ne 0 ]]; then
  if [[ "${ALLOW_PROD_CONFIG_SECRETS:-0}" == "1" ]]; then
    echo "WARNING: prod-secret-hygiene found secret-like config material but ALLOW_PROD_CONFIG_SECRETS=1 is set; continuing."
    printf '%s\n' "${scan_output}"
    exit 0
  fi

  echo "ERROR: prod-secret-hygiene blocked release; move secrets to runtime env-file and keep config_prod.yaml placeholders only." >&2
  printf '%s\n' "${scan_output}" >&2
  exit 1
fi

echo "prod-secret-hygiene: scanned ${#SCAN_FILES[@]} config file(s); passed strict prod scan"
