#!/usr/bin/env bash
# XHS script MVP backend readiness check.
#
# Common usage:
#   SKIP_HTTP=1 bash scripts/check_xhs_script_runtime.sh
#   ENV=prod bash scripts/check_xhs_script_runtime.sh
#   BACKEND_BASE_URL=http://localhost:9095 CONTAINER_NAME=numind-server-prod bash scripts/check_xhs_script_runtime.sh
#
# Environment knobs:
#   ENV=dev|qa|prod                 default: prod
#   CONFIG_FILE=path/to/config.yaml default: config_${ENV}.yaml
#   BACKEND_BASE_URL=http://...     defaults: dev 9091, qa 9093, prod 9095
#   CONTAINER_NAME=name             default: numind-server-${ENV}
#   SKIP_HTTP=1                     run static + docker checks only
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/.." && pwd)"

ENV="${ENV:-prod}"
case "${ENV}" in
  dev)
    DEFAULT_BACKEND_BASE_URL="http://localhost:9091"
    ;;
  qa)
    DEFAULT_BACKEND_BASE_URL="http://localhost:9093"
    ;;
  prod)
    DEFAULT_BACKEND_BASE_URL="http://localhost:9095"
    ;;
  *)
    echo "not ok - ENV must be one of dev, qa, prod"
    exit 1
    ;;
esac

CONFIG_FILE="${CONFIG_FILE:-config_${ENV}.yaml}"
BACKEND_BASE_URL="${BACKEND_BASE_URL:-${DEFAULT_BACKEND_BASE_URL}}"
CONTAINER_NAME="${CONTAINER_NAME:-numind-server-${ENV}}"
SKIP_HTTP="${SKIP_HTTP:-0}"

CONFIG_PATH="${CONFIG_FILE}"
if [[ "${CONFIG_PATH}" != /* ]]; then
  CONFIG_PATH="${REPO_ROOT}/${CONFIG_PATH}"
fi

failures=0

ok() {
  printf 'ok - %s\n' "$*"
}

not_ok() {
  printf 'not ok - %s\n' "$*"
  failures=$((failures + 1))
}

skip() {
  printf 'skip - %s\n' "$*"
}

url_host() {
  local url="$1"
  local without_scheme host

  without_scheme="${url#*://}"
  if [[ "${without_scheme}" == "${url}" && "${url}" != *"://"* ]]; then
    without_scheme="${url}"
  fi
  host="${without_scheme%%/*}"
  host="${host##*@}"
  host="${host%%\?*}"
  printf '%s' "${host}"
}

url_path() {
  local url="$1"
  local without_scheme path

  without_scheme="${url#*://}"
  if [[ "${without_scheme}" == "${url}" && "${url}" != *"://"* ]]; then
    without_scheme="${url}"
  fi

  if [[ "${without_scheme}" == */* ]]; then
    path="/${without_scheme#*/}"
  else
    path="/"
  fi

  path="${path%%\?*}"
  path="${path%%#*}"
  printf '%s' "${path}"
}

yaml_section_value() {
  local file="$1"
  local section="$2"
  local key="$3"

  awk -v section="${section}" -v key="${key}" '
    function trim(s) {
      gsub(/^[[:space:]]+|[[:space:]]+$/, "", s)
      return s
    }
    function strip_quotes(s) {
      if (s ~ /^".*"$/ || s ~ /^\047.*\047$/) {
        return substr(s, 2, length(s) - 2)
      }
      return s
    }
    BEGIN {
      in_section = 0
      found = 0
    }
    /^[[:space:]]*#/ {
      next
    }
    $0 ~ "^[[:space:]]*" section ":[[:space:]]*($|#)" {
      in_section = 1
      next
    }
    in_section && /^[^[:space:]#][^:]*:[[:space:]]*/ {
      exit
    }
    in_section {
      line = $0
      sub(/[[:space:]]+#.*$/, "", line)
      if (line ~ "^[[:space:]]*" key ":[[:space:]]*") {
        sub("^[[:space:]]*" key ":[[:space:]]*", "", line)
        line = strip_quotes(trim(line))
        print line
        found = 1
        exit
      }
    }
    END {
      if (!found) {
        exit 1
      }
    }
  ' "${file}"
}

check_file_contains() {
  local file="$1"
  local pattern="$2"
  local success_message="$3"
  local failure_message="$4"

  if [[ ! -f "${file}" ]]; then
    not_ok "${failure_message}: file missing"
    return
  fi

  if grep -Eq "${pattern}" "${file}"; then
    ok "${success_message}"
  else
    not_ok "${failure_message}"
  fi
}

read_wechat_field() {
  local field="$1"
  local var_name="$2"
  local value

  if value="$(yaml_section_value "${CONFIG_PATH}" "wechat" "${field}")" && [[ -n "${value}" ]]; then
    ok "wechat.${field} is present"
    printf -v "${var_name}" '%s' "${value}"
    return 0
  fi

  not_ok "wechat.${field} is missing or empty"
  printf -v "${var_name}" ''
  return 1
}

http_code() {
  local method="$1"
  local path="$2"
  local url="${BACKEND_BASE_URL%/}${path}"
  local code

  if [[ "${method}" == "POST" ]]; then
    if code="$(curl -sS -o /dev/null -w '%{http_code}' \
      --connect-timeout 2 --max-time 10 \
      -X POST -H 'Content-Type: application/json' \
      --data '{}' "${url}" 2>/dev/null)"; then
      printf '%s' "${code}"
      return 0
    fi
  else
    if code="$(curl -sS -o /dev/null -w '%{http_code}' \
      --connect-timeout 2 --max-time 10 \
      -X GET "${url}" 2>/dev/null)"; then
      printf '%s' "${code}"
      return 0
    fi
  fi

  printf '000'
  return 1
}

check_http_healthz() {
  local code
  code="$(http_code "GET" "/healthz")" || true

  if [[ "${code}" =~ ^2[0-9][0-9]$ ]]; then
    ok "GET /healthz returned ${code} at host $(url_host "${BACKEND_BASE_URL}")"
  else
    not_ok "GET /healthz returned ${code} at host $(url_host "${BACKEND_BASE_URL}"); start backend or run SKIP_HTTP=1 for static-only checks"
  fi
}

check_http_route_not_missing() {
  local method="$1"
  local path="$2"
  local code

  code="$(http_code "${method}" "${path}")" || true

  case "${code}" in
    000)
      not_ok "${method} ${path} could not reach host $(url_host "${BACKEND_BASE_URL}"); start backend or run SKIP_HTTP=1 for static-only checks"
      ;;
    404|405)
      not_ok "${method} ${path} returned ${code}; route is missing or method is not mounted"
      ;;
    *)
      ok "${method} ${path} returned ${code}; route is mounted"
      ;;
  esac
}

docker_available() {
  command -v docker >/dev/null 2>&1 && docker info >/dev/null 2>&1
}

container_exists() {
  docker inspect "${CONTAINER_NAME}" >/dev/null 2>&1
}

container_running() {
  [[ "$(docker inspect -f '{{.State.Running}}' "${CONTAINER_NAME}" 2>/dev/null || true)" == "true" ]]
}

check_container_file() {
  local path="$1"
  local label="$2"

  if docker exec "${CONTAINER_NAME}" sh -c 'test -f "$1"' sh "${path}" >/dev/null 2>&1; then
    ok "container ${label} file exists at ${path}"
  else
    not_ok "container ${label} file is missing at ${path}"
  fi
}

echo "XHS script runtime readiness check"
echo "env=${ENV}"
echo "config_file=${CONFIG_FILE}"
echo "backend_host=$(url_host "${BACKEND_BASE_URL}")"

echo
echo "Static checks"
if [[ -f "${CONFIG_PATH}" ]]; then
  ok "config file exists: ${CONFIG_FILE}"
else
  not_ok "config file missing: ${CONFIG_FILE}"
fi

wechat_app_id=""
wechat_mch_id=""
wechat_mch_cert_serial_no=""
wechat_mch_api_v3_key=""
wechat_mch_private_key_path=""
wechat_wechatpay_cert_path=""
wechat_wechatpay_public_key_id=""
wechat_notify_url=""

if [[ -f "${CONFIG_PATH}" ]]; then
  read_wechat_field "app_id" "wechat_app_id" || true
  read_wechat_field "mch_id" "wechat_mch_id" || true
  read_wechat_field "mch_cert_serial_no" "wechat_mch_cert_serial_no" || true
  read_wechat_field "mch_api_v3_key" "wechat_mch_api_v3_key" || true
  read_wechat_field "mch_private_key_path" "wechat_mch_private_key_path" || true
  read_wechat_field "wechatpay_cert_path" "wechat_wechatpay_cert_path" || true
  read_wechat_field "wechatpay_public_key_id" "wechat_wechatpay_public_key_id" || true
  read_wechat_field "notify_url" "wechat_notify_url" || true

  if [[ -n "${wechat_notify_url}" ]]; then
    if [[ "$(url_path "${wechat_notify_url}")" == *"/api/v1/payment/wechat/notify"* ]]; then
      ok "wechat.notify_url host=$(url_host "${wechat_notify_url}") path=$(url_path "${wechat_notify_url}") contains /api/v1/payment/wechat/notify"
    else
      not_ok "wechat.notify_url host=$(url_host "${wechat_notify_url}") path=$(url_path "${wechat_notify_url}") does not contain /api/v1/payment/wechat/notify"
    fi
  fi
else
  skip "wechat field checks skipped because config file is missing"
fi

check_file_contains \
  "${REPO_ROOT}/Dockerfile" \
  'apt-get install.*ffmpeg|apk add.*ffmpeg|yum install.*ffmpeg|dnf install.*ffmpeg' \
  "Dockerfile installs ffmpeg" \
  "Dockerfile does not appear to install ffmpeg"

check_file_contains \
  "${REPO_ROOT}/internal/numind/router.go" \
  'v1Group\.POST\("[^"]*/?payment/wechat/notify"' \
  "router registers /v1 payment WeChat notify route" \
  "router is missing /v1 payment WeChat notify registration"

if [[ -f "${REPO_ROOT}/internal/numind/router.go" ]] && \
  grep -Eq 'g\.Group\("/api/v1"\)' "${REPO_ROOT}/internal/numind/router.go" && \
  grep -Eq 'apiV1Group\.POST\("[^"]*/?payment/wechat/notify"' "${REPO_ROOT}/internal/numind/router.go"; then
  ok "router registers /api/v1 payment WeChat notify compatibility route"
else
  not_ok "router is missing /api/v1 payment WeChat notify compatibility registration"
fi

check_file_contains \
  "${REPO_ROOT}/scripts/cicd/deploy-remote.sh" \
  'prod\).*9095:9091' \
  "deploy-remote.sh maps prod host 9095 to container 9091" \
  "deploy-remote.sh is missing prod 9095:9091 mapping"

echo
echo "HTTP runtime checks"
if [[ "${SKIP_HTTP}" == "1" ]]; then
  skip "HTTP checks skipped because SKIP_HTTP=1"
elif ! command -v curl >/dev/null 2>&1; then
  not_ok "curl is not available; install curl or run SKIP_HTTP=1 for static-only checks"
else
  check_http_healthz
  check_http_route_not_missing "GET" "/v1/xhs-script/me"
  check_http_route_not_missing "POST" "/api/v1/payment/wechat/notify"
fi

echo
echo "Docker runtime checks"
if ! docker_available; then
  skip "docker checks skipped because docker CLI or daemon is unavailable"
elif ! container_exists; then
  skip "docker checks skipped because container ${CONTAINER_NAME} does not exist"
elif ! container_running; then
  skip "docker checks skipped because container ${CONTAINER_NAME} is not running"
else
  if docker exec "${CONTAINER_NAME}" sh -c 'command -v ffmpeg >/dev/null 2>&1 && ffmpeg -version >/dev/null 2>&1'; then
    ok "container ${CONTAINER_NAME} has runnable ffmpeg"
  else
    not_ok "container ${CONTAINER_NAME} cannot run ffmpeg"
  fi

  if [[ -n "${wechat_mch_private_key_path}" ]]; then
    check_container_file "${wechat_mch_private_key_path}" "wechat.mch_private_key_path"
  else
    skip "container private key file check skipped because wechat.mch_private_key_path is missing"
  fi

  if [[ -n "${wechat_wechatpay_cert_path}" ]]; then
    check_container_file "${wechat_wechatpay_cert_path}" "wechat.wechatpay_cert_path"
  else
    skip "container WeChat Pay cert file check skipped because wechat.wechatpay_cert_path is missing"
  fi
fi

echo
if [[ "${failures}" -eq 0 ]]; then
  ok "all required XHS script runtime readiness checks passed"
else
  printf 'not ok - %s required check(s) failed\n' "${failures}"
fi

exit "${failures}"
