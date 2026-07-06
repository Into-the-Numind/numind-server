#!/usr/bin/env bash
# Runs on the deploy server (dev/qa/prod). Uploaded by deploy.sh.
# Env vars expected:
#   ENV    - dev | qa | prod
#   TARGET - server | admin
#   IMAGE  - full TCR image with tag

set -euo pipefail

: "${ENV:?ENV must be set}"
: "${TARGET:?TARGET must be set}"
: "${IMAGE:?IMAGE must be set}"

EXTRA_RUN_FLAGS=""

case "$TARGET" in
  server)
    case "$ENV" in
      dev)  CONTAINER="numind-server-dev";  PORTS="-p 9091:9091 -p 9092:9092"; HEALTH_PORT=9091 ;;
      qa)   CONTAINER="numind-server-qa";   PORTS="-p 9093:9091 -p 9094:9092"; HEALTH_PORT=9093 ;;
      prod) CONTAINER="numind-server-prod"; PORTS="-p 9095:9091 -p 9096:9092"; HEALTH_PORT=9095 ;;
    esac
    VOLUMES="-v /opt/numind/${ENV}:/opt/numind/${ENV} \
             -v /opt/numind/config/cert:/opt/numind/config/cert:ro \
             -v /etc/ssl/certimate/youshu.asia:/etc/ssl/certimate/youshu.asia:ro \
             -v /opt/numind/model/model_cache:/app/model_cache"
    # agent-mode-sandbox-integration #4: dev mounts host /var/run/docker.sock so
    # the server container can drive the host docker daemon (DooD). prod does NOT
    # mount it — sandbox.backend stays disabled in prod and no docker access is
    # available to the binary.
    EXTRA_RUN_FLAGS=""
    if [ "$ENV" = "dev" ]; then
      VOLUMES="$VOLUMES -v /var/run/docker.sock:/var/run/docker.sock"
      # The container runs as uid 1001(numind); /var/run/docker.sock is root:docker
      # on the host. Resolve the host docker group gid and pass it via --group-add
      # so the container user can read/write the socket. Without this the binary
      # logs `permission denied while trying to connect to the Docker daemon` and
      # sandbox.Pool spawns fail.
      DOCKER_GID=$(getent group docker 2>/dev/null | cut -d: -f3)
      [ -n "$DOCKER_GID" ] && EXTRA_RUN_FLAGS="--group-add $DOCKER_GID"
    fi
    HEALTH_PATH="/healthz"
    LOG_MAX_SIZE="10m"; LOG_MAX_FILE="3"
    [ "$ENV" = "prod" ] && { LOG_MAX_SIZE="20m"; LOG_MAX_FILE="5"; }
    ;;
  admin)
    case "$ENV" in
      dev)  CONTAINER="numind-admin-server-dev";  PORTS="-p 9099:9099"; HEALTH_PORT=9099 ;;
      qa)   CONTAINER="numind-admin-server-qa";   PORTS="-p 9103:9099"; HEALTH_PORT=9103 ;;
      prod) CONTAINER="numind-admin-server-prod"; PORTS="-p 9104:9099"; HEALTH_PORT=9104 ;;
    esac
    VOLUMES=""
    HEALTH_PATH="/healthz"
    LOG_MAX_SIZE="10m"; LOG_MAX_FILE="3"
    ;;
  *)
    echo "ERROR: TARGET must be 'server' or 'admin', got '$TARGET'" >&2
    exit 1 ;;
esac

HEALTH_URL="http://localhost:${HEALTH_PORT}${HEALTH_PATH}"

# Runtime secrets file at /opt/numind/<env>/secrets.env for server/admin.
# Format: KEY=value per line, no quotes. Used to inject runtime secrets that
# must NOT live in config_*.yaml or the image (e.g. NUMIND_WEB_SEARCH_TAVILY_API_KEY).
# File is owned by deploy server admin; deploy script reads it only — never writes.
# In prod, deploy.sh copies a checker/config/template to /tmp and this script
# validates the real env-file before docker pull/run.
ENV_FILE_FLAG=""
SECRETS_FILE="${SECRETS_FILE:-/opt/numind/${ENV}/secrets.env}"
PROD_SECRETS_CHECK_SCRIPT="${PROD_SECRETS_CHECK_SCRIPT:-/tmp/numind-check-prod-secrets-env.sh}"
PROD_SECRETS_CONFIG_FILE="${PROD_SECRETS_CONFIG_FILE:-/tmp/numind-config-prod.yaml}"
PROD_SECRETS_EXAMPLE="${PROD_SECRETS_EXAMPLE:-/tmp/numind-prod-secrets.env.example}"
if [ -z "${REQUIRE_PROD_SECRETS_ENV:-}" ]; then
  if [ "$ENV" = "prod" ]; then
    REQUIRE_PROD_SECRETS_ENV=1
  else
    REQUIRE_PROD_SECRETS_ENV=0
  fi
fi
if [ -f "$SECRETS_FILE" ]; then
  ENV_FILE_FLAG="--env-file $SECRETS_FILE"
  SECRETS_INFO="$SECRETS_FILE (loaded)"
else
  SECRETS_INFO="$SECRETS_FILE (not present, skipping)"
fi

secure_secrets_file() {
  [ -f "$SECRETS_FILE" ] || return 0
  if [ "$(id -u)" -eq 0 ] && command -v sudo >/dev/null 2>&1; then
    sudo chmod 600 "$SECRETS_FILE" || chmod 600 "$SECRETS_FILE"
  else
    chmod 600 "$SECRETS_FILE"
  fi
}

validate_prod_secrets_file() {
  [ "$ENV" = "prod" ] || return 0

  if [ "$REQUIRE_PROD_SECRETS_ENV" != "1" ]; then
    echo "WARNING: prod secrets env-file validation disabled by REQUIRE_PROD_SECRETS_ENV=$REQUIRE_PROD_SECRETS_ENV"
    return 0
  fi

  if [ ! -f "$PROD_SECRETS_CHECK_SCRIPT" ]; then
    echo "ERROR: prod secrets checker missing: $PROD_SECRETS_CHECK_SCRIPT" >&2
    exit 1
  fi

  echo "Validating prod secrets env-file before deploy..."
  ENV="$ENV" \
    CONFIG_FILE="$PROD_SECRETS_CONFIG_FILE" \
    SECRETS_EXAMPLE="$PROD_SECRETS_EXAMPLE" \
    SECRETS_FILE="$SECRETS_FILE" \
    bash "$PROD_SECRETS_CHECK_SCRIPT"
}

echo "==============================================="
echo "Deploy: $CONTAINER"
echo "  Image  : $IMAGE"
echo "  Env    : $ENV"
echo "  Health : $HEALTH_URL"
echo "  Secrets: $SECRETS_INFO"
echo "==============================================="

validate_prod_secrets_file

OLD_IMAGE=""
if [ "$ENV" = "prod" ]; then
  OLD_IMAGE=$(docker inspect --format='{{.Config.Image}}' "$CONTAINER" 2>/dev/null || echo "")
  [ -n "$OLD_IMAGE" ] && echo "Previous image (for rollback): $OLD_IMAGE"
fi

echo "Pulling image..."
docker pull "$IMAGE" \
  || { echo "Pull retry 1/2..."; sleep 10; docker pull "$IMAGE"; } \
  || { echo "Pull retry 2/2..."; sleep 20; docker pull "$IMAGE"; }
docker image prune -f >/dev/null 2>&1 || true

docker network create numind-network 2>/dev/null || true
if [ "$TARGET" = "server" ]; then
  sudo mkdir -p "/opt/numind/${ENV}/image/upload/avatars" \
                "/opt/numind/${ENV}/image/upload/card" \
                "/opt/numind/${ENV}/image/upload/book"
  sudo mkdir -p /opt/numind/config/cert
  sudo chown -R 1001:1001 "/opt/numind/${ENV}" || true
  sudo chown -R 1001:1001 /opt/numind/config || true
  sudo chmod -R 775 "/opt/numind/${ENV}"
  sudo chmod -R 755 /opt/numind/config
  # Re-secure secrets file after the recursive chmod above. Mode 600 is
  # owner-only read/write; docker daemon runs as root and bypasses ACLs, so
  # --env-file still works regardless of ownership.
  secure_secrets_file
fi

start_container() {
  local img="$1"
  docker run -d \
    --name "$CONTAINER" \
    --network numind-network \
    $PORTS \
    -e "APP_ENV=${ENV}" \
    $ENV_FILE_FLAG \
    $VOLUMES \
    $EXTRA_RUN_FLAGS \
    --log-driver json-file \
    --log-opt "max-size=${LOG_MAX_SIZE}" \
    --log-opt "max-file=${LOG_MAX_FILE}" \
    --restart always \
    "$img"
}

# Force-remove any existing container of this name and wait until the name is
# released before re-creating it. A plain `docker stop` + `docker rm` (no -f)
# races the `--restart always` policy: the daemon can restart the container
# between stop and rm, the un-forced rm then fails on the running container
# (its `|| true` hides that), and the next `docker run` aborts with exit 125
# "Conflict. The container name is already in use". `docker rm -f` kills and
# removes in one step so the restart policy can't intervene; the poll guards
# against the daemon releasing the name slightly after rm returns.
remove_container() {
  docker rm -f "$CONTAINER" >/dev/null 2>&1 || true
  local tries=0
  while [ -n "$(docker ps -aq -f "name=^${CONTAINER}\$" 2>/dev/null)" ]; do
    if [ "$tries" -ge 10 ]; then
      echo "ERROR: container '$CONTAINER' still present after force-remove; aborting" >&2
      return 1
    fi
    docker rm -f "$CONTAINER" >/dev/null 2>&1 || true
    tries=$((tries + 1))
    sleep 1
  done
}

# Keep only the image of the currently-running container; remove all older
# images of the same repository. Safe to call after a successful deploy or
# successful rollback (it inspects the live container's image id).
cleanup_old_images() {
  local repo="${IMAGE%:*}"
  local running_id
  running_id=$(docker inspect "$CONTAINER" --format='{{.Image}}' 2>/dev/null) || return 0
  echo "Cleaning up old images for ${repo} (keep only currently-running)..."
  docker images "$repo" --format '{{.ID}} {{.Repository}}:{{.Tag}}' | while read -r short_id tag; do
    if [[ "$running_id" != *"$short_id"* ]]; then
      docker rmi -f "$tag" >/dev/null 2>&1 || true
    fi
  done
  docker image prune -f >/dev/null 2>&1 || true
}

remove_container
start_container "$IMAGE"

MAX_TRIES=72; SLEEP_INT=5
# admin server needs ~60s startup for DB seeding/config sync — give it 3 min
[ "$TARGET" = "admin" ] && { MAX_TRIES=36; SLEEP_INT=5; }

echo "Waiting for health check (up to $((MAX_TRIES * SLEEP_INT))s)..."
READY=false
for i in $(seq 1 "$MAX_TRIES"); do
  if curl -sf "$HEALTH_URL" >/dev/null 2>&1; then
    READY=true; break
  fi
  sleep "$SLEEP_INT"
done

if [ "$READY" = true ]; then
  echo "✅ Deploy success: $CONTAINER is healthy"
  docker ps -f "name=^${CONTAINER}\$" --format "table {{.Names}}\t{{.Status}}\t{{.Ports}}"
  cleanup_old_images || true
  exit 0
fi

echo "❌ Health check timeout for $CONTAINER" >&2
docker logs --tail 50 "$CONTAINER" || true

if [ "$ENV" = "prod" ] && [ -n "$OLD_IMAGE" ]; then
  echo "🔄 Rolling back to $OLD_IMAGE..."
  remove_container || { echo "❌ Rollback aborted: could not release container name '$CONTAINER'" >&2; exit 1; }
  start_container "$OLD_IMAGE"
  for i in $(seq 1 "$MAX_TRIES"); do
    if curl -sf "$HEALTH_URL" >/dev/null 2>&1; then
      echo "⚠️  Rollback success: $OLD_IMAGE restored"
      cleanup_old_images || true
      exit 1
    fi
    sleep "$SLEEP_INT"
  done
  echo "❌ Rollback also failed" >&2
fi
exit 1
