#!/usr/bin/env bash
# Build the bare localserver image.
#
# Steps:
#   1. If CSC_SRC_DIR/dist is absent, run `bun install && bun run build` there.
#   2. Sync CSC_SRC_DIR/dist into ./csc-dist/ (build context).
#   3. Stage bun for the target arch (./bun-linux-<TARGET_ARCH>):
#        - reuse host bun when arch matches
#        - otherwise download from github (Linux x64 / arm64)
#   4. Rebuild cs-cloud Go binary inside Docker (handled by Dockerfile).
#
# Required env:
#   CSC_SRC_DIR   — path to the csc checkout (default: ../csc relative to repo root)
#
# Optional:
#   TARGET_ARCH   — amd64 (default) | arm64
#   IMAGE_TAG     — image name:tag (default: cs-cloud-localserver:<TARGET_ARCH>)
#   SKIP_CSC_BUILD — set to 1 to skip the bun build (assumes dist already built)
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_DIR="$(cd "${SCRIPT_DIR}/.." && pwd)"

: "${TARGET_ARCH:=amd64}"
: "${CSC_SRC_DIR:=${REPO_DIR}/../csc}"
case "${TARGET_ARCH}" in
  amd64|arm64) ;;
  *) echo "error: TARGET_ARCH must be amd64 or arm64 (got: ${TARGET_ARCH})" >&2; exit 1 ;;
esac
: "${IMAGE_TAG:=cs-cloud-localserver:${TARGET_ARCH}}"

CSC_SRC_DIR="$(cd "${CSC_SRC_DIR}" && pwd)"
echo "target arch : ${TARGET_ARCH}"
echo "image tag   : ${IMAGE_TAG}"
echo "csc source  : ${CSC_SRC_DIR}"

# Locate bun (host) — needed for building csc dist, regardless of target arch.
BUN_BIN="${BUN_BIN:-}"
if [ -z "${BUN_BIN}" ]; then
  if command -v bun >/dev/null 2>&1; then
    BUN_BIN="$(command -v bun)"
  elif [ -x "${HOME}/.bun/bin/bun" ]; then
    BUN_BIN="${HOME}/.bun/bin/bun"
  else
    echo "error: bun not found. Install bun or set BUN_BIN." >&2
    exit 1
  fi
fi
echo "host bun    : ${BUN_BIN} ($("${BUN_BIN}" --version))"

# Detect host arch (amd64 / arm64) so we know whether to reuse host bun.
case "$(uname -m)" in
  x86_64|amd64)   HOST_ARCH=amd64 ;;
  aarch64|arm64)  HOST_ARCH=arm64 ;;
  *)              HOST_ARCH="" ;;
esac

# Build csc dist if needed
#
# COSTRICT_ENABLED_API is a BUILD-TIME macro read by csc's scripts/defines.ts
# and baked into dist as MACRO.ENABLED_API. The localserver image is meant to
# drive third-party model providers (OpenAI / Anthropic-compatible gateways /
# Bedrock / Vertex / etc.), so we pin it to "1" — otherwise csc falls back to
# fetching /costrict-static/cli-api-control/config.json at runtime, which is
# unreachable in an air-gapped container and would disable every non-CoStrict
# provider. Override via CSC_ENABLED_API if a different policy is ever needed.
: "${CSC_ENABLED_API:=1}"
if [ "${SKIP_CSC_BUILD:-0}" != "1" ]; then
  if [ ! -d "${CSC_SRC_DIR}/node_modules" ]; then
    echo ">> installing csc deps"
    (cd "${CSC_SRC_DIR}" && "${BUN_BIN}" install)
  fi
  echo ">> building csc dist (COSTRICT_ENABLED_API=${CSC_ENABLED_API})"
  (cd "${CSC_SRC_DIR}" && SKIP_REVIEW_BUILTIN=1 COSTRICT_ENABLED_API="${CSC_ENABLED_API}" "${BUN_BIN}" run build)
else
  echo ">> SKIP_CSC_BUILD=1, skipping bun build"
fi

if [ ! -f "${CSC_SRC_DIR}/dist/cli.js" ]; then
  echo "error: ${CSC_SRC_DIR}/dist/cli.js missing after build" >&2
  exit 1
fi

# Sync dist into build context
echo ">> syncing ${CSC_SRC_DIR}/dist -> ${REPO_DIR}/csc-dist"
rm -rf "${REPO_DIR}/csc-dist"
mkdir -p "${REPO_DIR}/csc-dist"
cp -r "${CSC_SRC_DIR}/dist/." "${REPO_DIR}/csc-dist/"

# Stage bun for target arch at ./bun-linux-<TARGET_ARCH>.
# Reuse a pre-staged binary if present (SKIP_BUN_FETCH=1 or file exists) —
# useful when the host has no direct github access and the binary was
# downloaded separately.
STAGED_BUN="${REPO_DIR}/bun-linux-${TARGET_ARCH}"
if [ "${SKIP_BUN_FETCH:-0}" = "1" ] && [ -s "${STAGED_BUN}" ]; then
  echo ">> SKIP_BUN_FETCH=1, reusing pre-staged ${STAGED_BUN}"
  chmod +x "${STAGED_BUN}"
elif [ "${HOST_ARCH}" = "${TARGET_ARCH}" ]; then
  HOST_BUN_PATH="$("${BUN_BIN}" --bun --print 'process.execPath' 2>/dev/null || echo "${BUN_BIN}")"
  [ -x "${HOST_BUN_PATH}" ] || HOST_BUN_PATH="${BUN_BIN}"
  echo ">> staging host bun (${HOST_ARCH}) -> ${STAGED_BUN}"
  cp -f "${HOST_BUN_PATH}" "${STAGED_BUN}"
else
  # Map docker arch to bun release arch naming (amd64 -> x64, arm64 -> aarch64).
  case "${TARGET_ARCH}" in
    amd64) BUN_ARCH="x64" ;;
    arm64) BUN_ARCH="aarch64" ;;
  esac
  : "${BUN_VERSION:=$("${BUN_BIN}" --version)}"
  URL="https://github.com/oven-sh/bun/releases/download/bun-v${BUN_VERSION}/bun-linux-${BUN_ARCH}.zip"
  echo ">> downloading cross-arch bun: ${URL}"
  TMP="$(mktemp -d)"; trap 'rm -rf "${TMP}"' EXIT
  curl -fsSL "${URL}" -o "${TMP}/bun.zip"
  unzip -q "${TMP}/bun.zip" -d "${TMP}"
  cp -f "${TMP}/bun-linux-${BUN_ARCH}/bun" "${STAGED_BUN}"
  chmod +x "${STAGED_BUN}"
fi

# Clean up old-arch staged bun (e.g. previous bun-linux-x64) so stale files
# don't linger in the build context.
find "${REPO_DIR}" -maxdepth 1 -name 'bun-linux-*' -type f ! -name "bun-linux-${TARGET_ARCH}" -delete

echo ">> docker buildx build (--platform linux/${TARGET_ARCH})"
# Explicitly pass TARGETARCH because Dockerfile.localserver declares
# `ARG TARGETARCH=amd64` in the runtime stage; the default sticks when the
# legacy builder is used, so we override it to be safe across builders.
#
# BuildKit's registry resolver reads proxy config from process env, NOT from
# dockerd's Environment= (which only affects the daemon's own image pulls).
# Without HTTPS_PROXY exported here, `docker buildx build` direct-connects to
# auth.docker.io even when `docker pull` works fine. Forward any operator-set
# proxy vars so builds behave the same as pulls.
env ${HTTP_PROXY:+HTTP_PROXY="${HTTP_PROXY}"} \
    ${HTTPS_PROXY:+HTTPS_PROXY="${HTTPS_PROXY}"} \
    ${NO_PROXY:+NO_PROXY="${NO_PROXY}"} \
docker buildx build \
  --platform "linux/${TARGET_ARCH}" \
  --build-arg "TARGETARCH=${TARGET_ARCH}" \
  --build-arg "TARGETOS=linux" \
  -f "${REPO_DIR}/Dockerfile.localserver" \
  -t "${IMAGE_TAG}" \
  --load \
  "${REPO_DIR}"

echo
echo "Done: ${IMAGE_TAG} (${TARGET_ARCH})"
