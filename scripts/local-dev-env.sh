# Sourced by Make recipes; keep the credential out of command arguments and logs.
if [ -z "${AGENT_TOKEN:-}" ]; then
  python3 scripts/local-dev-token.py || exit 1
  IFS= read -r AGENT_TOKEN < "${CONFIG_DIR:-p2pstream-data}/.local-dev/agent-token" || exit 1
fi
if [ "${#AGENT_TOKEN}" -lt 32 ]; then
  echo "AGENT_TOKEN must contain at least 32 characters" >&2
  exit 1
fi
export AGENT_TOKEN
# An explicit environment/.env bind remains an intentional operator override.
MANAGEMENT_BIND_ADDRESS="${MANAGEMENT_BIND_ADDRESS:-127.0.0.1}"
export MANAGEMENT_BIND_ADDRESS
