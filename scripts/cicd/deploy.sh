#!/usr/bin/env bash
# Orchestrator: runs on build server. SCPs deploy-remote.sh and runs it.
# Invocation: bash scripts/cicd/deploy.sh <env> [target]

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd -P)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/../.." && pwd -P)"

ENV="${1:?usage: $0 <dev|qa|prod> [server|admin]}"
TARGET="${2:-server}"
GIT_SHA="${GIT_SHA:-unknown}"
GIT_TAG="${GIT_TAG:-}"

REGISTRY="ccr.ccs.tencentyun.com"
NAMESPACE="youshunumind"

case "$TARGET" in
  server) IMAGE_NAME="numind-server" ;;
  admin)  IMAGE_NAME="numind-admin"  ;;
  *) echo "ERROR: target must be server|admin" >&2; exit 1 ;;
esac

case "$ENV" in
  dev)  ROLLING_TAG="develop" ;;
  qa)   ROLLING_TAG="release" ;;
  prod) ROLLING_TAG="${GIT_TAG:-latest}" ;;
  *) echo "ERROR: env must be dev/qa/prod" >&2; exit 1 ;;
esac

SHA_TAG="${ROLLING_TAG}-${GIT_SHA}"
FULL_IMAGE="${REGISTRY}/${NAMESPACE}/${IMAGE_NAME}:${SHA_TAG}"

case "$ENV" in
  dev)  DEPLOY_HOST="49.233.219.254" ;;
  qa)   DEPLOY_HOST="49.233.219.254" ;;
  prod) DEPLOY_HOST="129.28.125.51"  ;;
esac

REMOTE_SCRIPT="/tmp/numind-deploy-remote.sh"
REMOTE_SECRETS_CHECK_SCRIPT="/tmp/numind-check-prod-secrets-env.sh"
REMOTE_PROD_CONFIG="/tmp/numind-config-prod.yaml"
REMOTE_PROD_SECRETS_EXAMPLE="/tmp/numind-prod-secrets.env.example"
REMOTE_SANDBOX_DIR="/tmp/numind-sandbox-release"
REMOTE_SANDBOX_BROKER_ENV="/tmp/numind-sandbox-broker.env"

SANDBOX_ENV_KEYS=(
  NUMIND_SANDBOX_BACKEND
  NUMIND_SANDBOX_BROKER_SOCKET
  NUMIND_SANDBOX_BROKER_OWNER_ID
  NUMIND_SANDBOX_BROKER_INSTANCE
  NUMIND_SANDBOX_API_HOST_UID
  NUMIND_SANDBOX_IMAGE_DIGEST
  NUMIND_SANDBOX_SECCOMP_SHA256
  NUMIND_SANDBOX_BASELINE_BYTES
  NUMIND_SANDBOX_PARENT_MEMORY_MAX_BYTES
  NUMIND_SANDBOX_WORKLOAD_MEMORY_MAX_BYTES
  NUMIND_SANDBOX_WORKLOAD_MEMORY_HIGH_BYTES
  NUMIND_SANDBOX_WORKLOAD_MEMORY_RECOVERY_BYTES
  NUMIND_SANDBOX_WORKLOAD_MEMORY_SHED_BYTES
  NUMIND_SANDBOX_CONTROL_MEMORY_HIGH_BYTES
  NUMIND_SANDBOX_CONTROL_MEMORY_MAX_BYTES
  NUMIND_SANDBOX_PARENT_HEADROOM_BYTES
  NUMIND_SANDBOX_USER_UID
  NUMIND_SANDBOX_GROUP_GID
  NUMIND_SANDBOX_API_GROUP_GID
  NUMIND_SANDBOX_SUBID_START
  NUMIND_SANDBOX_SUBID_COUNT
  NUMIND_SANDBOX_CPU_QUOTA_PERCENT
  NUMIND_SANDBOX_TASKS_MAX
)

quote_env_assignments() {
  local out="" key value quoted
  for key in "${SANDBOX_ENV_KEYS[@]}"; do
    if [ -n "${!key+x}" ]; then
      value="${!key}"
      printf -v quoted '%q' "$value"
      out+=" ${key}=${quoted}"
    fi
  done
  printf '%s' "$out"
}

deploy_sandboxd_if_needed() {
  [ "$ENV" = "prod" ] || return 0
  [ "$TARGET" = "server" ] || return 0
  [ "${NUMIND_SANDBOX_BACKEND:-disabled}" = "broker" ] || return 0

  local sandbox_env
  sandbox_env="$(quote_env_assignments)"
  echo "Deploying sandboxd broker before user API..."
  ssh -o StrictHostKeyChecking=no "root@${DEPLOY_HOST}" \
      "rm -rf '${REMOTE_SANDBOX_DIR}' && mkdir -p '${REMOTE_SANDBOX_DIR}/scripts/cicd' '${REMOTE_SANDBOX_DIR}/deploy'"
  scp -o StrictHostKeyChecking=no -q \
      "${SCRIPT_DIR}/deploy-sandboxd-remote.sh" \
      "${SCRIPT_DIR}/provision-sandbox-host.sh" \
      "root@${DEPLOY_HOST}:${REMOTE_SANDBOX_DIR}/scripts/cicd/"
  scp -o StrictHostKeyChecking=no -q -r \
      "${REPO_ROOT}/deploy/sandbox" \
      "root@${DEPLOY_HOST}:${REMOTE_SANDBOX_DIR}/deploy/"
  ssh -o StrictHostKeyChecking=no "root@${DEPLOY_HOST}" \
      "ENV='${ENV}' IMAGE='${FULL_IMAGE}' NUMIND_SANDBOX_BROKER_ENV_FILE='${REMOTE_SANDBOX_BROKER_ENV}' ${sandbox_env} bash '${REMOTE_SANDBOX_DIR}/scripts/cicd/deploy-sandboxd-remote.sh'"
}

echo "==============================================="
echo "Deploying $IMAGE_NAME -> $ENV ($DEPLOY_HOST)"
echo "Image: $FULL_IMAGE"
echo "==============================================="

scp -o StrictHostKeyChecking=no -q \
    "${SCRIPT_DIR}/deploy-remote.sh" \
    "root@${DEPLOY_HOST}:${REMOTE_SCRIPT}"

REMOTE_SECRETS_ENV=""
if [ "$ENV" = "prod" ]; then
  scp -o StrictHostKeyChecking=no -q \
      "${REPO_ROOT}/scripts/check_prod_secrets_env.sh" \
      "root@${DEPLOY_HOST}:${REMOTE_SECRETS_CHECK_SCRIPT}"
  scp -o StrictHostKeyChecking=no -q \
      "${REPO_ROOT}/config_prod.yaml" \
      "root@${DEPLOY_HOST}:${REMOTE_PROD_CONFIG}"
  scp -o StrictHostKeyChecking=no -q \
      "${SCRIPT_DIR}/prod-secrets.env.example" \
      "root@${DEPLOY_HOST}:${REMOTE_PROD_SECRETS_EXAMPLE}"

  REMOTE_SECRETS_ENV="REQUIRE_PROD_SECRETS_ENV='${REQUIRE_PROD_SECRETS_ENV:-1}' PROD_SECRETS_CHECK_SCRIPT='${REMOTE_SECRETS_CHECK_SCRIPT}' PROD_SECRETS_CONFIG_FILE='${REMOTE_PROD_CONFIG}' PROD_SECRETS_EXAMPLE='${REMOTE_PROD_SECRETS_EXAMPLE}'"
fi

deploy_sandboxd_if_needed

REMOTE_SANDBOX_ENV="$(quote_env_assignments)"
ssh -o StrictHostKeyChecking=no "root@${DEPLOY_HOST}" \
    "set -a; [ ! -f '${REMOTE_SANDBOX_BROKER_ENV}' ] || . '${REMOTE_SANDBOX_BROKER_ENV}'; set +a; ENV='${ENV}' TARGET='${TARGET}' IMAGE='${FULL_IMAGE}' ${REMOTE_SECRETS_ENV} ${REMOTE_SANDBOX_ENV} bash ${REMOTE_SCRIPT}"
