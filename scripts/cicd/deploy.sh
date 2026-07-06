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

ssh -o StrictHostKeyChecking=no "root@${DEPLOY_HOST}" \
    "ENV='${ENV}' TARGET='${TARGET}' IMAGE='${FULL_IMAGE}' ${REMOTE_SECRETS_ENV} bash ${REMOTE_SCRIPT}"
