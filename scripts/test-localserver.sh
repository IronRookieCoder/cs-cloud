#!/usr/bin/env bash
# Test runner for the cs-cloud localserver image.
#
# Usage:
#   scripts/test-localserver.sh                          # use defaults
#   MODEL_API_KEY=sk-xxx scripts/test-localserver.sh     # override via env
#   scripts/test-localserver.sh --clean                  # stop & remove container, no start
#
# Configurable env vars (with defaults):
#   IMAGE              default: cs-cloud-localserver:amd64
#   CONTAINER_NAME     default: cs-cloud-localserver-test
#   HOST_PORT          default: 8080
#   CONTAINER_PORT     default: 8080
#   WORKSPACE_DIR      default: ./workspace (resolved to absolute path)
#   MODEL_PROVIDER     default: anthropic
#   MODEL_BASE_URL     default: https://your-gateway.example.com
#   MODEL_API_KEY      default: sk-your-gateway-key   ← 替换成你的真实值
#   EXTRA_ENV          default: (空)                   ← 额外 -e 透传，例如 OPENAI_DEFAULT_SONNET_MODEL
#   RETAIN             default: (空)                   ← 设为 1 跑完保留容器，否则自动清理
#
# Examples:
#   # OpenAI 流
#   MODEL_PROVIDER=openai \
#     MODEL_BASE_URL=https://api.openai.com/v1 \
#     MODEL_API_KEY=sk-xxx \
#     EXTRA_ENV='-e OPENAI_DEFAULT_SONNET_MODEL=gpt-4o -e OPENAI_DEFAULT_HAIKU_MODEL=gpt-4o-mini' \
#     scripts/test-localserver.sh
#
#   # 直接覆盖整包（绕过 entrypoint 的 L1 翻译）
#   EXTRA_ENV='-e CS_BRIDGE_AGENT_ENV={"OPENAI_BASE_URL":"https://api.openai.com/v1","OPENAI_API_KEY":"sk-xxx","OPENAI_DEFAULT_SONNET_MODEL":"gpt-4o"}' \
#     scripts/test-localserver.sh
set -euo pipefail

# ---------------------------------------------------------------------------
# Config
# ---------------------------------------------------------------------------
IMAGE="${IMAGE:-cs-cloud-localserver:amd64}"
CONTAINER_NAME="${CONTAINER_NAME:-cs-cloud-localserver-test}"
HOST_PORT="${HOST_PORT:-8080}"
CONTAINER_PORT="${CONTAINER_PORT:-8080}"
WORKSPACE_DIR="${WORKSPACE_DIR:-$(pwd)/workspace}"

MODEL_PROVIDER="${MODEL_PROVIDER:-anthropic}"
MODEL_BASE_URL="${MODEL_BASE_URL:-https://your-gateway.example.com}"
MODEL_API_KEY="${MODEL_API_KEY:-sk-your-gateway-key}"

EXTRA_ENV="${EXTRA_ENV:-}"
RETAIN="${RETAIN:-}"

READY_TIMEOUT="${READY_TIMEOUT:-60}"   # seconds
PROBE_INTERVAL="${PROBE_INTERVAL:-2}"  # seconds

# Pretty output
GREEN=$'\033[32m'; YELLOW=$'\033[33m'; RED=$'\033[31m'; DIM=$'\033[2m'; RESET=$'\033[0m'
log()   { echo "${GREEN}[test]${RESET} $*"; }
warn()  { echo "${YELLOW}[warn]${RESET} $*"; }
err()   { echo "${RED}[err]${RESET} $*" >&2; }
header(){ echo; echo "${GREEN}=== $* ===${RESET}"; }

# ---------------------------------------------------------------------------
# Helpers
# ---------------------------------------------------------------------------
cleanup() {
  if [ -n "${RETAIN}" ]; then
    log "RETAIN=1, container left running: ${CONTAINER_NAME}"
    log "Inspect: docker logs ${CONTAINER_NAME} | docker exec ${CONTAINER_NAME} cat /root/.costrict/cs-cloud/app.log"
    return
  fi
  if docker ps -a --format '{{.Names}}' | grep -q "^${CONTAINER_NAME}$"; then
    warn "cleaning up container ${CONTAINER_NAME}"
    docker rm -f "${CONTAINER_NAME}" >/dev/null 2>&1 || true
  fi
}

stop_only() {
  cleanup
  exit 0
}

# `--clean` short-circuits
if [ "${1:-}" = "--clean" ] || [ "${1:-}" = "clean" ]; then
  stop_only
fi

trap cleanup EXIT INT TERM

# Sanity: docker available
if ! command -v docker >/dev/null 2>&1; then
  err "docker not found in PATH"
  exit 1
fi

# Sanity: image exists locally
if ! docker image inspect "${IMAGE}" >/dev/null 2>&1; then
  err "image not found: ${IMAGE}"
  err "build it first: docker build -f Dockerfile.localserver -t ${IMAGE} ."
  exit 1
fi

# Sanity: workspace dir
mkdir -p "${WORKSPACE_DIR}"
WORKSPACE_ABS="$(cd "${WORKSPACE_DIR}" && pwd)"

# ---------------------------------------------------------------------------
# Print plan
# ---------------------------------------------------------------------------
header "Configuration"
log "image          : ${IMAGE}"
log "container name : ${CONTAINER_NAME}"
log "port mapping   : ${HOST_PORT}:${CONTAINER_PORT}"
log "workspace      : ${WORKSPACE_ABS}"
log "MODEL_PROVIDER : ${MODEL_PROVIDER}"
log "MODEL_BASE_URL : ${MODEL_BASE_URL}"
log "MODEL_API_KEY  : ${MODEL_API_KEY:0:8}*** (masked)"
[ -n "${EXTRA_ENV}" ] && log "EXTRA_ENV      : ${EXTRA_ENV}" || true
[ -n "${RETAIN}" ]    && log "RETAIN         : ${RETAIN}"     || true

# ---------------------------------------------------------------------------
# (Re)create container
# ---------------------------------------------------------------------------
header "Starting container"

# Remove any stale container first
if docker ps -a --format '{{.Names}}' | grep -q "^${CONTAINER_NAME}$"; then
  warn "removing stale container"
  docker rm -f "${CONTAINER_NAME}" >/dev/null
fi

# shellcheck disable=SC2086
docker run -d \
  --name "${CONTAINER_NAME}" \
  -p "${HOST_PORT}:${CONTAINER_PORT}" \
  -e MODEL_PROVIDER="${MODEL_PROVIDER}" \
  -e MODEL_BASE_URL="${MODEL_BASE_URL}" \
  -e MODEL_API_KEY="${MODEL_API_KEY}" \
  ${EXTRA_ENV} \
  -v "${WORKSPACE_ABS}:/workspace" \
  "${IMAGE}" >/dev/null

log "container started (id=$(docker inspect -f '{{.Id}}' "${CONTAINER_NAME}" | cut -c1-12))"

# ---------------------------------------------------------------------------
# Wait for daemon ready (state file == running)
# ---------------------------------------------------------------------------
header "Waiting for daemon ready (timeout ${READY_TIMEOUT}s)"

deadline=$(( $(date +%s) + READY_TIMEOUT ))
last_state=""
while [ "$(date +%s)" -lt "${deadline}" ]; do
  # Container still alive?
  if ! docker ps --format '{{.Names}}' | grep -q "^${CONTAINER_NAME}$"; then
    err "container exited unexpectedly"
    err "----- docker logs -----"
    docker logs "${CONTAINER_NAME}" 2>&1 | tail -30 >&2
    exit 1
  fi

  state="$(docker exec "${CONTAINER_NAME}" cat /root/.costrict/cs-cloud/state 2>/dev/null | tr -d '[:space:]' || true)"
  if [ "${state}" = "running" ]; then
    log "daemon state: running"
    break
  fi
  if [ "${state}" != "${last_state}" ]; then
    echo "${DIM}    state=${state:-<init>}${RESET}"
    last_state="${state}"
  fi
  sleep "${PROBE_INTERVAL}"
done

if [ "${state:-}" != "running" ]; then
  err "daemon did not reach 'running' state within ${READY_TIMEOUT}s"
  err "----- docker logs -----"
  docker logs "${CONTAINER_NAME}" 2>&1 | tail -50 >&2
  exit 1
fi

# ---------------------------------------------------------------------------
# Smoke tests
# ---------------------------------------------------------------------------
header "HTTP smoke tests"

base="http://localhost:${HOST_PORT}"

probe() {
  local path="$1" expect="$2"
  local code body
  code="$(curl -s -o /tmp/_body.$$ -w '%{http_code}' --max-time 5 "${base}${path}" || true)"
  body="$(head -c 200 /tmp/_body.$$ 2>/dev/null)"
  rm -f /tmp/_body.$$
  if [[ "${code}" =~ ^${expect} ]]; then
    log "GET ${path} -> ${code} ${DIM}(ok)${RESET}"
    [ -n "${body}" ] && echo "${DIM}      body: ${body}${RESET}"
    return 0
  else
    warn "GET ${path} -> ${code} (expected ${expect})"
    [ -n "${body}" ] && echo "${DIM}      body: ${body}${RESET}"
    return 1
  fi
}

probe "/api/v1/agents" "200"
probe "/api/v1/docs"    "200"
probe "/health"         "200|404"   # 路由不存在也算通过（不是每个版本都有 /health）

# ---------------------------------------------------------------------------
# Daemon log inspection
# ---------------------------------------------------------------------------
header "Recent daemon log"
docker exec "${CONTAINER_NAME}" tail -20 /root/.costrict/cs-cloud/app.log 2>/dev/null \
  | sed 's/^/    /'

# ---------------------------------------------------------------------------
# Manual verification hint
# ---------------------------------------------------------------------------
header "Manual verification"
cat <<EOF
Container is up. Useful follow-ups:

  # Tail logs
  docker logs -f ${CONTAINER_NAME}

  # Read full daemon app.log
  docker exec ${CONTAINER_NAME} cat /root/.costrict/cs-cloud/app.log

  # Check what env vars csc actually received
  docker exec ${CONTAINER_NAME} sh -c 'env | grep -E "ANTHROPIC_|OPENAI_|MODEL_" | sort'

  # Swagger UI (open in browser)
  ${base}/api/v1/docs

  # Send a real model request through the csc adapter (find the right path in swagger)
  curl -s ${base}/api/v1/messages -X POST -H 'Content-Type: application/json' -d '{...}'
EOF

if [ -z "${RETAIN}" ]; then
  echo
  warn "container will be auto-removed on script exit (set RETAIN=1 to keep)"
else
  echo
  log "RETAIN=1, container stays running after exit"
fi
