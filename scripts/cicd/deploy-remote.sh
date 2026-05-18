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

echo "==============================================="
echo "Deploy: $CONTAINER"
echo "  Image  : $IMAGE"
echo "  Env    : $ENV"
echo "  Health : $HEALTH_URL"
echo "==============================================="

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
fi

start_container() {
  local img="$1"
  docker run -d \
    --name "$CONTAINER" \
    --network numind-network \
    $PORTS \
    -e "APP_ENV=${ENV}" \
    $VOLUMES \
    --log-driver json-file \
    --log-opt "max-size=${LOG_MAX_SIZE}" \
    --log-opt "max-file=${LOG_MAX_FILE}" \
    --restart always \
    "$img"
}

docker stop "$CONTAINER" 2>/dev/null || true
docker rm "$CONTAINER" 2>/dev/null || true
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
  exit 0
fi

echo "❌ Health check timeout for $CONTAINER" >&2
docker logs --tail 50 "$CONTAINER" || true

if [ "$ENV" = "prod" ] && [ -n "$OLD_IMAGE" ]; then
  echo "🔄 Rolling back to $OLD_IMAGE..."
  docker stop "$CONTAINER" 2>/dev/null || true
  docker rm "$CONTAINER" 2>/dev/null || true
  start_container "$OLD_IMAGE"
  for i in $(seq 1 "$MAX_TRIES"); do
    if curl -sf "$HEALTH_URL" >/dev/null 2>&1; then
      echo "⚠️  Rollback success: $OLD_IMAGE restored"
      exit 1
    fi
    sleep "$SLEEP_INT"
  done
  echo "❌ Rollback also failed" >&2
fi
exit 1
