#!/usr/bin/env bash
# Entrypoint for the bare cs-cloud localserver image.
#
# - Forces --mode local (no cloud registration, no OAuth, no WS tunnel).
# - Runs cs-cloud's _daemon subcommand directly in the foreground (PID 1 = tini
#   -> this script -> exec cs-cloud _daemon), so the container stays alive
#   as long as the daemon runs.
# - Translates MODEL_PROVIDER / MODEL_BASE_URL / MODEL_API_KEY / MODEL_NAME
#   into the per-provider env vars csc actually reads, then packs them into
#   CS_CLOUD_AGENT_ENV so cs-cloud's agent runtime injects them into the
#   csc subprocess.
set -euo pipefail

: "${CS_CLOUD_DATA_DIR:=/root/.costrict}"
: "${CS_CLOUD_PORT:=8080}"
: "${CS_CLOUD_HOST:=0.0.0.0}"
: "${CS_CLOUD_AGENT_PATH:=/usr/local/bin/csc}"
: "${MODEL_PROVIDER:=anthropic}"
# BYPASS_PERMISSIONS=1 (default) makes csc skip all permission prompts.
# Set BYPASS_PERMISSIONS=0 to restore interactive permission prompts.
: "${BYPASS_PERMISSIONS:=1}"

# cs-cloud's _daemon reads its mode from <data-dir>/cs-cloud/mode (defaults to
# "cloud" when absent, which would force a device-register path). Pin it to
# "local" so the daemon skips cloud/tunnel/heartbeat entirely.
APP_DIR="${CS_CLOUD_DATA_DIR}/cs-cloud"
mkdir -p "${APP_DIR}"
printf 'local' > "${APP_DIR}/mode"

# Pre-seed csc user settings so:
# - `permissions.defaultMode=bypassPermissions` takes effect (BYPASS_PERMISSIONS=1).
#   ACP_PERMISSION_MODE=bypassPermissions only makes bypass *available*; the session
#   still defaults to 'default' mode unless the settings file (or _meta.permissionMode
#   on session create) opts in.
# - `modelType` is set for known MODEL_PROVIDER values. csc's isNotLoggedIn()
#   (csc/src/utils/logoV2Utils.ts:245) treats an undefined modelType as "not logged
#   in" and blocks /model with "You need to login first" — even when the operator
#   has already injected OPENAI_API_KEY/OPENAI_BASE_URL via CS_CLOUD_AGENT_ENV.
#   Writing modelType here unblocks the /model picker for env-only providers.
# - `env` mirrors the same provider env vars we hand to CS_CLOUD_AGENT_ENV. csc's
#   settings-driven paths (readUserProviderConfig in csc/src/utils/model/providerConfig.ts,
#   the /api/v1/provider-config endpoint, the /model picker) read settings.env, NOT
#   process.env — so without this mirror they report the provider as "not configured"
#   even though the subprocess env is correctly injected and actual API calls work.
# csc reads <costrict-config-home>/settings.json (default ~/.costrict —
# COSTRICT_CONFIG_DIR overrides), see csc/src/utils/settings/userSettingsFilePaths.ts.
# We only write if the file is absent; mount your own to override.
CSC_CONFIG_HOME="${COSTRICT_CONFIG_DIR:-${CS_CLOUD_DATA_DIR}}"
mkdir -p "${CSC_CONFIG_HOME}"
SETTINGS_FILE="${CSC_CONFIG_HOME}/settings.json"
if [ ! -f "${SETTINGS_FILE}" ]; then
  settings_body=""
  settings_sep=""
  case "${MODEL_PROVIDER}" in
    anthropic) settings_body="${settings_body}${settings_sep}\"modelType\":\"anthropic\""; settings_sep="," ;;
    openai)    settings_body="${settings_body}${settings_sep}\"modelType\":\"openai\""; settings_sep="," ;;
  esac
  if [ "${BYPASS_PERMISSIONS}" = "1" ] || [ "${BYPASS_PERMISSIONS}" = "true" ]; then
    settings_body="${settings_body}${settings_sep}\"permissions\":{\"defaultMode\":\"bypassPermissions\"}"
    settings_sep=","
  fi
  # Mirror the L3 provider env vars (built by build_agent_env below) into settings.env
  # so csc's settings-driven code paths see the same values the subprocess gets via
  # CS_CLOUD_AGENT_ENV. We share the same translation by computing it once here.
  settings_env_body=""
  case "${MODEL_PROVIDER}" in
    anthropic)
      [ -n "${MODEL_BASE_URL:-}" ] && settings_env_body="${settings_env_body}\"ANTHROPIC_BASE_URL\":\"${MODEL_BASE_URL}\","
      [ -n "${MODEL_API_KEY:-}"  ] && settings_env_body="${settings_env_body}\"ANTHROPIC_AUTH_TOKEN\":\"${MODEL_API_KEY}\","
      [ -n "${MODEL_NAME:-}"     ] && settings_env_body="${settings_env_body}\"ANTHROPIC_MODEL\":\"${MODEL_NAME}\","
      ;;
    openai)
      [ -n "${MODEL_BASE_URL:-}" ] && settings_env_body="${settings_env_body}\"OPENAI_BASE_URL\":\"${MODEL_BASE_URL}\","
      [ -n "${MODEL_API_KEY:-}"  ] && settings_env_body="${settings_env_body}\"OPENAI_API_KEY\":\"${MODEL_API_KEY}\","
      if [ -n "${MODEL_NAME:-}" ]; then
        settings_env_body="${settings_env_body}\"OPENAI_DEFAULT_HAIKU_MODEL\":\"${MODEL_NAME}\",\"OPENAI_DEFAULT_SONNET_MODEL\":\"${MODEL_NAME}\",\"OPENAI_DEFAULT_OPUS_MODEL\":\"${MODEL_NAME}\","
      fi
      ;;
  esac
  settings_env_body="${settings_env_body%,}"
  if [ -n "${settings_env_body}" ]; then
    settings_body="${settings_body}${settings_sep}\"env\":{${settings_env_body}}"
    settings_sep=","
  fi
  if [ -n "${settings_body}" ]; then
    printf '{%s}' "${settings_body}" > "${SETTINGS_FILE}"
  fi
fi

# Translate MODEL_* into provider-specific env vars, packed as JSON for
# CS_CLOUD_AGENT_ENV. Skip if the operator already supplied CS_CLOUD_AGENT_ENV
# (we honour their full payload verbatim).
build_agent_env() {
  local provider="$1"
  local base_url="${MODEL_BASE_URL:-}"
  local api_key="${MODEL_API_KEY:-}"
  local model_name="${MODEL_NAME:-}"
  local out=""

  case "${provider}" in
    anthropic)
      [ -n "${base_url}"  ] && out="${out}\"ANTHROPIC_BASE_URL\":\"${base_url}\","
      [ -n "${api_key}"   ] && out="${out}\"ANTHROPIC_AUTH_TOKEN\":\"${api_key}\","
      [ -n "${model_name}" ] && out="${out}\"ANTHROPIC_MODEL\":\"${model_name}\","
      ;;
    openai)
      [ -n "${base_url}" ] && out="${out}\"OPENAI_BASE_URL\":\"${base_url}\","
      [ -n "${api_key}"  ] && out="${out}\"OPENAI_API_KEY\":\"${api_key}\","
      # OpenAI 流把模型拆成 HAIKU/SONNET/OPUS 三个档位槽位。多数场景只想
      # 用一个模型，MODEL_NAME 在这里同时灌进三个槽位；想分档配置请直接
      # 覆盖 CS_CLOUD_AGENT_ENV，此处不会再生效（因为 entrypoint 跳过翻译）。
      [ -n "${model_name}" ] && out="${out}\"OPENAI_DEFAULT_HAIKU_MODEL\":\"${model_name}\",\"OPENAI_DEFAULT_SONNET_MODEL\":\"${model_name}\",\"OPENAI_DEFAULT_OPUS_MODEL\":\"${model_name}\","
      ;;
    *)
      echo "Warning: unknown MODEL_PROVIDER='${provider}', skipping model env injection" >&2
      ;;
  esac

  # Auto-bypass csc permission prompts. Requires IS_SANDBOX=1 because csc
  # refuses bypass mode when running as root (our default), see
  # csc/src/services/acp/agent.ts:941-945. IS_SANDBOX satisfies the guard.
  if [ "${BYPASS_PERMISSIONS}" = "1" ] || [ "${BYPASS_PERMISSIONS}" = "true" ]; then
    out="${out}\"ACP_PERMISSION_MODE\":\"bypassPermissions\",\"IS_SANDBOX\":\"1\","
  fi

  # strip trailing comma, wrap in braces
  out="${out%,}"
  if [ -n "${out}" ]; then
    printf '{%s}' "${out}"
  else
    printf ''
  fi
}

if [ -z "${CS_CLOUD_AGENT_ENV:-}" ]; then
  agent_env="$(build_agent_env "${MODEL_PROVIDER}")"
  if [ -n "${agent_env}" ]; then
    export CS_CLOUD_AGENT_ENV="${agent_env}"
  fi
fi

echo "[entrypoint] cs-cloud localserver starting"
echo "[entrypoint]   data-dir : ${CS_CLOUD_DATA_DIR}"
echo "[entrypoint]   bind     : ${CS_CLOUD_HOST}:${CS_CLOUD_PORT}"
echo "[entrypoint]   agent    : ${CS_CLOUD_AGENT_PATH}"
echo "[entrypoint]   agent cmd: ${CS_CLOUD_AGENT_COMMAND:-${CS_CLOUD_AGENT_PATH} serve (default)}"
echo "[entrypoint]   provider: ${MODEL_PROVIDER}"
[ -n "${MODEL_NAME:-}" ] && echo "[entrypoint]   model    : ${MODEL_NAME}"
if [ "${BYPASS_PERMISSIONS}" = "1" ] || [ "${BYPASS_PERMISSIONS}" = "true" ]; then
  echo "[entrypoint]   perms    : bypass (ACP_PERMISSION_MODE=bypassPermissions, IS_SANDBOX=1)"
else
  echo "[entrypoint]   perms    : interactive (BYPASS_PERMISSIONS=0)"
fi
if [ -n "${CS_CLOUD_AGENT_ENV:-}" ]; then
  echo "[entrypoint]   agent-env: ${CS_CLOUD_AGENT_ENV}"
fi

exec cs-cloud _daemon \
  --data-dir "${CS_CLOUD_DATA_DIR}" \
  --agent-path "${CS_CLOUD_AGENT_PATH}" \
  --host "${CS_CLOUD_HOST}" \
  --port "${CS_CLOUD_PORT}" \
  --no-auto-upgrade
