#!/usr/bin/env bash
# Regression tests for scripts/check_prod_secret_hygiene.sh.
#
# Run: bash scripts/test_prod_secret_hygiene.sh
set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
CHECK_SH="${SCRIPT_DIR}/check_prod_secret_hygiene.sh"

die() { echo "test setup error: $1" >&2; exit 2; }

TMP="$(mktemp -d)" || die "mktemp"
trap 'rm -rf "$TMP"' EXIT

fail=0

write_fixture() {
  local file="$1"
  shift
  printf '%s\n' "$@" > "$file" || die "write fixture $file"
}

run_check() {
  local config="$1"
  local out="$2"
  shift 2
  (
    cd "$TMP" || exit 2
    env "$@" CONFIG_FILE="$config" bash "$CHECK_SH"
  ) > "$out" 2>&1
}

assert_pass() {
  local label="$1"
  local rc="$2"
  local out="$3"

  if [ "$rc" -eq 0 ]; then
    echo "PASS: $label"
  else
    echo "FAIL: $label should pass (rc=$rc)"
    cat "$out"
    fail=1
  fi
}

assert_fail() {
  local label="$1"
  local rc="$2"
  local out="$3"
  local expected="$4"

  if [ "$rc" -ne 0 ]; then
    echo "PASS: $label exits non-zero"
  else
    echo "FAIL: $label should fail"
    fail=1
  fi

  if grep -q "$expected" "$out"; then
    echo "PASS: $label reports $expected"
  else
    echo "FAIL: $label output missing $expected"
    cat "$out"
    fail=1
  fi
}

assert_no_fixture_secret_leak() {
  local label="$1"
  local out="$2"

  if grep -Eq 'super-secret|sk-test|AKIDEXAMPLE|PRIVATE KEY MATERIAL|extra-secret|plain-secret|slash-secret|real-secret-token|access-token-secret' "$out"; then
    echo "FAIL: $label leaked fixture secret material"
    cat "$out"
    fail=1
  else
    echo "PASS: $label does not print secret values"
  fi
}

BENIGN="$TMP/benign.yaml"
write_fixture "$BENIGN" \
  "wechat:" \
  "  notify_url: https://example.com/api/v1/payment/wechat/notify" \
  "  mch_private_key_path: /opt/numind/prod/certs/apiclient_key.pem" \
  "  wechatpay_cert_path: /opt/numind/prod/certs/wechatpay.pem" \
  "  wechatpay_public_key_id: PUB_KEY_ID_0123456789" \
  "tls:" \
  "  cert_path: /opt/numind/prod/tls/server.crt" \
  "  key_path: /opt/numind/prod/tls/server.key"
run_check "$BENIGN" "$TMP/benign.out" ENV=prod
assert_pass "benign paths, public ids, and urls are allowed" "$?" "$TMP/benign.out"

PLACEHOLDER="$TMP/placeholder.yaml"
write_fixture "$PLACEHOLDER" \
  "service:" \
  "  password: \"\"" \
  "  api_key: \${NUMIND_API_KEY}" \
  "  token: changeme" \
  "  access_key: <ACCESS_KEY>" \
  "  private_key: TODO"
run_check "$PLACEHOLDER" "$TMP/placeholder.out" ENV=prod
assert_pass "empty values, env references, and placeholders are allowed" "$?" "$TMP/placeholder.out"

TOKEN_COUNTS="$TMP/token-counts.yaml"
write_fixture "$TOKEN_COUNTS" \
  "llm:" \
  "  tokens: 4000" \
  "  max_tokens: 8192" \
  "  prompt_tokens: 123" \
  "  completion_tokens: 456" \
  "  total_tokens: 579" \
  "  cached_tokens: 42"
run_check "$TOKEN_COUNTS" "$TMP/token-counts.out" ENV=prod
assert_pass "numeric token-count fields are allowed" "$?" "$TMP/token-counts.out"

TOKEN_SECRET="$TMP/token-secret.yaml"
write_fixture "$TOKEN_SECRET" \
  "auth:" \
  "  token: real-secret-token"
run_check "$TOKEN_SECRET" "$TMP/token-secret.out" ENV=prod
token_secret_rc=$?
assert_fail "token string value is rejected" "$token_secret_rc" "$TMP/token-secret.out" "secret-key-value"
assert_no_fixture_secret_leak "token secret rejection" "$TMP/token-secret.out"

ACCESS_TOKEN_SECRET="$TMP/auth-token-values.yaml"
write_fixture "$ACCESS_TOKEN_SECRET" \
  "auth:" \
  "  access_token: access-token-secret-value" \
  "  refresh_token: access-token-secret-refresh" \
  "  api_token: access-token-secret-api"
run_check "$ACCESS_TOKEN_SECRET" "$TMP/auth-token-values.out" ENV=prod
access_token_secret_rc=$?
assert_fail "access/refresh/api token string values are rejected" "$access_token_secret_rc" "$TMP/auth-token-values.out" "secret-key-value"
assert_no_fixture_secret_leak "access token secret rejection" "$TMP/auth-token-values.out"

PASSWORD="$TMP/password.yaml"
write_fixture "$PASSWORD" \
  "database:" \
  "  password: super-secret-password"
run_check "$PASSWORD" "$TMP/password.out" ENV=prod
password_rc=$?
assert_fail "secret-like password value is rejected" "$password_rc" "$TMP/password.out" "secret-key-value"
assert_no_fixture_secret_leak "password rejection" "$TMP/password.out"

MULTILINE="$TMP/multiline.yaml"
write_fixture "$MULTILINE" \
  "database:" \
  "  password: |" \
  "    super-secret-password" \
  "llm:" \
  "  api_key: >-" \
  "    plain-secret-key-value"
run_check "$MULTILINE" "$TMP/multiline.out" ENV=prod
multiline_rc=$?
assert_fail "multiline sensitive key values are rejected" "$multiline_rc" "$TMP/multiline.out" "multiline-secret-key-value"
assert_no_fixture_secret_leak "multiline rejection" "$TMP/multiline.out"

SLASH_KEYS="$TMP/slash-keys.yaml"
write_fixture "$SLASH_KEYS" \
  "llm:" \
  "  api_key: sk-proj/slash-secret-value-abcdefghijklmnopqrstuvwxyz" \
  "cloud:" \
  "  access_key: ABCD/EFGH/slash-secret-value-1234567890"
run_check "$SLASH_KEYS" "$TMP/slash-keys.out" ENV=prod
slash_keys_rc=$?
assert_fail "slash-containing api/access key values are rejected" "$slash_keys_rc" "$TMP/slash-keys.out" "secret-key-value"
assert_no_fixture_secret_leak "slash-containing key rejection" "$TMP/slash-keys.out"

TARGET_OK="$TMP/target-ok.yaml"
EXTRA_BAD="$TMP/extra-bad.yaml"
write_fixture "$TARGET_OK" \
  "service:" \
  "  api_key: \${NUMIND_API_KEY}"
write_fixture "$EXTRA_BAD" \
  "legacy:" \
  "  password: extra-secret-password"
run_check "$TARGET_OK" "$TMP/extra-config.out" ENV=prod EXTRA_CONFIG_FILES="$EXTRA_BAD"
extra_config_rc=$?
assert_fail "EXTRA_CONFIG_FILES secret is rejected" "$extra_config_rc" "$TMP/extra-config.out" "extra-bad.yaml"
assert_no_fixture_secret_leak "EXTRA_CONFIG_FILES rejection" "$TMP/extra-config.out"

API_KEY="$TMP/api-key.yaml"
write_fixture "$API_KEY" \
  "llm:" \
  "  api_key: sk-test-abcdefghijklmnopqrstuvwxyz1234567890"
run_check "$API_KEY" "$TMP/api-key.out" ENV=prod
api_key_rc=$?
assert_fail "OpenAI-style api key is rejected" "$api_key_rc" "$TMP/api-key.out" "openai-sk"
assert_no_fixture_secret_leak "api key rejection" "$TMP/api-key.out"

PRIVATE_KEY="$TMP/private-key.yaml"
write_fixture "$PRIVATE_KEY" \
  "signing:" \
  "  private_key: |" \
  "    -----BEGIN PRIVATE KEY-----" \
  "    PRIVATE KEY MATERIAL" \
  "    -----END PRIVATE KEY-----"
run_check "$PRIVATE_KEY" "$TMP/private-key.out" ENV=prod
private_key_rc=$?
assert_fail "inline private key material is rejected" "$private_key_rc" "$TMP/private-key.out" "pem-private-key"
assert_no_fixture_secret_leak "private key rejection" "$TMP/private-key.out"

AKID="$TMP/akid.yaml"
write_fixture "$AKID" \
  "cloud:" \
  "  access_key: AKIDEXAMPLE1234567890ABCDEF"
run_check "$AKID" "$TMP/akid.out" ENV=prod
akid_rc=$?
assert_fail "AKID access key is rejected" "$akid_rc" "$TMP/akid.out" "akid"
assert_no_fixture_secret_leak "AKID rejection" "$TMP/akid.out"

run_check "$PASSWORD" "$TMP/override.out" ENV=prod ALLOW_PROD_CONFIG_SECRETS=1
override_rc=$?
assert_pass "emergency override allows release gate to pass" "$override_rc" "$TMP/override.out"
if grep -q "WARNING" "$TMP/override.out"; then
  echo "PASS: override prints warning"
else
  echo "FAIL: override output missing warning"
  cat "$TMP/override.out"
  fail=1
fi
assert_no_fixture_secret_leak "override warning" "$TMP/override.out"

run_check "$PASSWORD" "$TMP/qa.out" ENV=qa
assert_pass "non-prod skips strict secret scan" "$?" "$TMP/qa.out"

echo
if [ "$fail" -ne 0 ]; then
  echo "prod secret hygiene test FAILED"
  exit 1
fi

echo "prod secret hygiene test PASSED"
