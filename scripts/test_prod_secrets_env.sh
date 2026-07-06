#!/usr/bin/env bash
# Regression tests for scripts/check_prod_secrets_env.sh.
#
# Run: bash scripts/test_prod_secrets_env.sh
set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
CHECK_SH="${SCRIPT_DIR}/check_prod_secrets_env.sh"

die() { echo "test setup error: $1" >&2; exit 2; }

[ -f "${CHECK_SH}" ] || die "check_prod_secrets_env.sh not found at ${CHECK_SH}"

TMP="$(mktemp -d)" || die "mktemp"
trap 'rm -rf "${TMP}"' EXIT

fail=0

write_file() {
  local file="$1"
  shift
  printf '%s\n' "$@" > "${file}" || die "write ${file}"
}

write_config() {
  local file="$1"
  write_file "${file}" \
    "jwt:" \
    "  secret: \${NUMIND_JWT_SECRET}" \
    "database:" \
    "  password: \${NUMIND_DB_PASSWORD}" \
    "wechat:" \
    "  mch_api_v3_key: \${NUMIND_WECHAT_MCH_API_V3_KEY}"
}

write_example() {
  local file="$1"
  write_file "${file}" \
    "# example" \
    "NUMIND_JWT_SECRET=" \
    "NUMIND_DB_PASSWORD=" \
    "NUMIND_WECHAT_MCH_API_V3_KEY="
}

write_good_secrets() {
  local file="$1"
  write_file "${file}" \
    "NUMIND_JWT_SECRET=super-secret-jwt-value" \
    "NUMIND_DB_PASSWORD=super-secret-db-value" \
    "NUMIND_WECHAT_MCH_API_V3_KEY=super-secret-wechat-v3-value"
  chmod 600 "${file}" || die "chmod ${file}"
}

run_check() {
  local config="$1"
  local example="$2"
  local secrets="$3"
  local out="$4"
  (
    cd "${TMP}" || exit 2
    ENV=prod CONFIG_FILE="${config}" SECRETS_EXAMPLE="${example}" SECRETS_FILE="${secrets}" bash "${CHECK_SH}"
  ) > "${out}" 2>&1
}

assert_pass() {
  local label="$1"
  local rc="$2"
  local out="$3"

  if [ "${rc}" -eq 0 ]; then
    echo "PASS: ${label}"
  else
    echo "FAIL: ${label} should pass (rc=${rc})"
    cat "${out}"
    fail=1
  fi
}

assert_fail() {
  local label="$1"
  local rc="$2"
  local out="$3"
  local expected="$4"

  if [ "${rc}" -ne 0 ]; then
    echo "PASS: ${label} exits non-zero"
  else
    echo "FAIL: ${label} should fail"
    fail=1
  fi

  if grep -q "${expected}" "${out}"; then
    echo "PASS: ${label} reports ${expected}"
  else
    echo "FAIL: ${label} output missing ${expected}"
    cat "${out}"
    fail=1
  fi
}

assert_no_secret_leak() {
  local label="$1"
  local out="$2"

  if grep -Eq 'super-secret|quoted-secret|duplicate-secret|malformed-secret|placeholder-secret' "${out}"; then
    echo "FAIL: ${label} leaked fixture secret material"
    cat "${out}"
    fail=1
  else
    echo "PASS: ${label} does not print secret values"
  fi
}

CONFIG="${TMP}/config_prod.yaml"
EXAMPLE="${TMP}/prod-secrets.env.example"
GOOD="${TMP}/secrets.env"
write_config "${CONFIG}"
write_example "${EXAMPLE}"
write_good_secrets "${GOOD}"

run_check "${CONFIG}" "${EXAMPLE}" "${GOOD}" "${TMP}/good.out"
assert_pass "complete secrets env-file passes" "$?" "${TMP}/good.out"
assert_no_secret_leak "complete secrets env-file" "${TMP}/good.out"

run_check "${CONFIG}" "${EXAMPLE}" "${TMP}/missing.env" "${TMP}/missing-file.out"
missing_file_rc=$?
assert_fail "missing secrets env-file" "${missing_file_rc}" "${TMP}/missing-file.out" "secrets env-file not found"

MISSING_KEY="${TMP}/missing-key.env"
write_file "${MISSING_KEY}" \
  "NUMIND_JWT_SECRET=super-secret-jwt-value" \
  "NUMIND_DB_PASSWORD=super-secret-db-value"
chmod 600 "${MISSING_KEY}" || die "chmod missing-key"
run_check "${CONFIG}" "${EXAMPLE}" "${MISSING_KEY}" "${TMP}/missing-key.out"
missing_key_rc=$?
assert_fail "missing required variable" "${missing_key_rc}" "${TMP}/missing-key.out" "NUMIND_WECHAT_MCH_API_V3_KEY"
assert_no_secret_leak "missing required variable" "${TMP}/missing-key.out"

EMPTY_VALUE="${TMP}/empty-value.env"
write_file "${EMPTY_VALUE}" \
  "NUMIND_JWT_SECRET=super-secret-jwt-value" \
  "NUMIND_DB_PASSWORD=" \
  "NUMIND_WECHAT_MCH_API_V3_KEY=super-secret-wechat-v3-value"
chmod 600 "${EMPTY_VALUE}" || die "chmod empty-value"
run_check "${CONFIG}" "${EXAMPLE}" "${EMPTY_VALUE}" "${TMP}/empty-value.out"
empty_value_rc=$?
assert_fail "empty required value" "${empty_value_rc}" "${TMP}/empty-value.out" "empty-or-placeholder-value"
assert_no_secret_leak "empty required value" "${TMP}/empty-value.out"

PLACEHOLDER_VALUE="${TMP}/placeholder-value.env"
write_file "${PLACEHOLDER_VALUE}" \
  "NUMIND_JWT_SECRET=placeholder-secret-jwt-value" \
  "NUMIND_DB_PASSWORD=changeme" \
  "NUMIND_WECHAT_MCH_API_V3_KEY=\${NUMIND_WECHAT_MCH_API_V3_KEY}"
chmod 600 "${PLACEHOLDER_VALUE}" || die "chmod placeholder-value"
run_check "${CONFIG}" "${EXAMPLE}" "${PLACEHOLDER_VALUE}" "${TMP}/placeholder-value.out"
placeholder_value_rc=$?
assert_fail "placeholder required value" "${placeholder_value_rc}" "${TMP}/placeholder-value.out" "empty-or-placeholder-value"
assert_no_secret_leak "placeholder required value" "${TMP}/placeholder-value.out"

QUOTED_VALUE="${TMP}/quoted-value.env"
write_file "${QUOTED_VALUE}" \
  "NUMIND_JWT_SECRET=\"quoted-secret-jwt-value\"" \
  "NUMIND_DB_PASSWORD=super-secret-db-value" \
  "NUMIND_WECHAT_MCH_API_V3_KEY=super-secret-wechat-v3-value"
chmod 600 "${QUOTED_VALUE}" || die "chmod quoted-value"
run_check "${CONFIG}" "${EXAMPLE}" "${QUOTED_VALUE}" "${TMP}/quoted-value.out"
quoted_value_rc=$?
assert_fail "quoted required value" "${quoted_value_rc}" "${TMP}/quoted-value.out" "quoted-value-not-supported"
assert_no_secret_leak "quoted required value" "${TMP}/quoted-value.out"

QUOTED_EXTRA_VALUE="${TMP}/quoted-extra-value.env"
write_file "${QUOTED_EXTRA_VALUE}" \
  "NUMIND_JWT_SECRET=super-secret-jwt-value" \
  "NUMIND_DB_PASSWORD=super-secret-db-value" \
  "NUMIND_WECHAT_MCH_API_V3_KEY=super-secret-wechat-v3-value" \
  "OPTIONAL_SECRET=\"quoted-secret-extra-value\""
chmod 600 "${QUOTED_EXTRA_VALUE}" || die "chmod quoted-extra-value"
run_check "${CONFIG}" "${EXAMPLE}" "${QUOTED_EXTRA_VALUE}" "${TMP}/quoted-extra-value.out"
quoted_extra_value_rc=$?
assert_fail "quoted extra value" "${quoted_extra_value_rc}" "${TMP}/quoted-extra-value.out" "quoted-value-not-supported"
assert_no_secret_leak "quoted extra value" "${TMP}/quoted-extra-value.out"

PLACEHOLDER_EXTRA_VALUE="${TMP}/placeholder-extra-value.env"
write_file "${PLACEHOLDER_EXTRA_VALUE}" \
  "NUMIND_JWT_SECRET=super-secret-jwt-value" \
  "NUMIND_DB_PASSWORD=super-secret-db-value" \
  "NUMIND_WECHAT_MCH_API_V3_KEY=super-secret-wechat-v3-value" \
  "OPTIONAL_SECRET=changeme" \
  "NUMIND_EXTRA=\${NUMIND_EXTRA}"
chmod 600 "${PLACEHOLDER_EXTRA_VALUE}" || die "chmod placeholder-extra-value"
run_check "${CONFIG}" "${EXAMPLE}" "${PLACEHOLDER_EXTRA_VALUE}" "${TMP}/placeholder-extra-value.out"
placeholder_extra_value_rc=$?
assert_fail "placeholder extra value" "${placeholder_extra_value_rc}" "${TMP}/placeholder-extra-value.out" "empty-or-placeholder-value"
assert_no_secret_leak "placeholder extra value" "${TMP}/placeholder-extra-value.out"

MALFORMED="${TMP}/malformed.env"
write_file "${MALFORMED}" \
  "export NUMIND_JWT_SECRET=malformed-secret-jwt-value" \
  "NUMIND_DB_PASSWORD=super-secret-db-value" \
  "NUMIND_WECHAT_MCH_API_V3_KEY=super-secret-wechat-v3-value"
chmod 600 "${MALFORMED}" || die "chmod malformed"
run_check "${CONFIG}" "${EXAMPLE}" "${MALFORMED}" "${TMP}/malformed.out"
malformed_rc=$?
assert_fail "export prefix is rejected" "${malformed_rc}" "${TMP}/malformed.out" "export-prefix-not-supported"
assert_no_secret_leak "export prefix is rejected" "${TMP}/malformed.out"

DUPLICATE="${TMP}/duplicate.env"
write_file "${DUPLICATE}" \
  "NUMIND_JWT_SECRET=super-secret-jwt-value" \
  "NUMIND_DB_PASSWORD=super-secret-db-value" \
  "NUMIND_DB_PASSWORD=duplicate-secret-db-value" \
  "NUMIND_WECHAT_MCH_API_V3_KEY=super-secret-wechat-v3-value"
chmod 600 "${DUPLICATE}" || die "chmod duplicate"
run_check "${CONFIG}" "${EXAMPLE}" "${DUPLICATE}" "${TMP}/duplicate.out"
duplicate_rc=$?
assert_fail "duplicate keys are rejected" "${duplicate_rc}" "${TMP}/duplicate.out" "duplicate-key"
assert_no_secret_leak "duplicate keys are rejected" "${TMP}/duplicate.out"

BAD_MODE="${TMP}/bad-mode.env"
write_good_secrets "${BAD_MODE}"
chmod 644 "${BAD_MODE}" || die "chmod bad-mode"
run_check "${CONFIG}" "${EXAMPLE}" "${BAD_MODE}" "${TMP}/bad-mode.out"
bad_mode_rc=$?
assert_fail "group/world readable file is rejected" "${bad_mode_rc}" "${TMP}/bad-mode.out" "chmod 600"
assert_no_secret_leak "group/world readable file" "${TMP}/bad-mode.out"

MISSING_EXAMPLE="${TMP}/missing-example.env"
write_file "${MISSING_EXAMPLE}" \
  "NUMIND_JWT_SECRET=" \
  "NUMIND_DB_PASSWORD="
run_check "${CONFIG}" "${MISSING_EXAMPLE}" "${GOOD}" "${TMP}/missing-example.out"
missing_example_rc=$?
assert_fail "example missing required variable" "${missing_example_rc}" "${TMP}/missing-example.out" "secrets example is missing required variable"

(
  cd "${TMP}" || exit 2
  ENV=qa CONFIG_FILE="${CONFIG}" SECRETS_EXAMPLE="${EXAMPLE}" SECRETS_FILE="${TMP}/missing.env" bash "${CHECK_SH}"
) > "${TMP}/qa-skip.out" 2>&1
assert_pass "non-prod skips runtime prod secrets check" "$?" "${TMP}/qa-skip.out"

echo
if [ "${fail}" -ne 0 ]; then
  echo "prod secrets env test FAILED"
  exit 1
fi

echo "prod secrets env test PASSED"
