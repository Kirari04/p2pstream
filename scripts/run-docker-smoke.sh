#!/usr/bin/env bash
# One credential boundary for the server, agent and smoke client. Never source .env.
set -Eeuo pipefail

repo=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)
cd -- "$repo"
case "${1:-run}" in
  run|clean) mode=${1:-run} ;;
  *) echo "Usage: $0 [run|clean]" >&2; exit 2 ;;
esac

random_credential() {
  LC_ALL=C od -An -N32 -tx1 /dev/urandom | tr -d ' \n'
}
SMOKE_SETUP_TOKEN=$(random_credential)
SMOKE_AGENT_TOKEN=$(random_credential)
SMOKE_ADMIN_PASSWORD=$(random_credential)
export SMOKE_SETUP_TOKEN SMOKE_AGENT_TOKEN SMOKE_ADMIN_PASSWORD

# Stable per checkout so the clean target can recover an interrupted run without
# touching another checkout's stack. --env-file suppresses Compose's .env loading.
project="p2pstream-smoke-$(printf '%s' "$repo" | cksum | cut -d ' ' -f1)"
compose() {
  docker compose --env-file /dev/null --project-name "$project" \
    -f "$repo/docker-compose.test.yml" "$@"
}

if [[ "$mode" == clean ]]; then
  compose down -v --remove-orphans --timeout 10
  exit
fi

cleanup() {
  status=$?
  trap - EXIT INT TERM
  compose down -v --remove-orphans --timeout 10 || {
    echo "Smoke cleanup failed; run make docker-smoke-clean." >&2
    if (( status == 0 )); then status=1; fi
  }
  exit "$status"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

echo "Running disposable Docker smoke tests; host ports 18080/18081/18088/18089/18443 bind to 127.0.0.1."
compose down -v --remove-orphans --timeout 10
compose up --build --abort-on-container-exit --exit-code-from smoke
