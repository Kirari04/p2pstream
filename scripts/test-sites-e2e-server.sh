#!/usr/bin/env bash
# Isolated, test-runner-owned fixture. Serves the built UI without a dev server.
set -euo pipefail

# Credentials are generated once by the runner and inherited by browser workers.
if [[ ! "${PLAYWRIGHT_SETUP_TOKEN:-}" =~ ^[0-9a-f]{64}$ ]]; then
  echo "PLAYWRIGHT_SETUP_TOKEN must be supplied by the Playwright runner config" >&2
  exit 1
fi

repo_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
management_port="${PLAYWRIGHT_MANAGEMENT_PORT:-19481}"
if [[ ! "$management_port" =~ ^[0-9]+$ ]] || (( management_port < 1024 || management_port > 65535 )); then
  echo "PLAYWRIGHT_MANAGEMENT_PORT must be an unprivileged TCP port" >&2
  exit 1
fi
if [[ ! -f "$repo_dir/web/management/dist/index.html" ]]; then
  echo "Build the management UI before running Site browser tests: cd web/management && bun run build" >&2
  exit 1
fi

fixture_dir="$(mktemp -d "${TMPDIR:-/tmp}/p2pstream-sites-e2e.XXXXXX")"
server_pid=""
cleanup() {
  trap - EXIT INT TERM
  if [[ -n "$server_pid" ]]; then
    kill -TERM "$server_pid" 2>/dev/null || true
    wait "$server_pid" 2>/dev/null || true
  fi
  rm -rf -- "$fixture_dir"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

cd "$repo_dir"
go build -o "$fixture_dir/p2pstream" .
go run ./scripts/browser-fixture-db "$fixture_dir/config/p2pstream.db"
# A private cwd prevents config.Load from reading the developer's repository .env.
cd "$fixture_dir"
# Read the token from stdin after clearing the environment, keeping it out of
# env(1)'s argument list as well as the logged Playwright command.
env -i \
  PATH="$PATH" \
  HOME="$HOME" \
  CONFIG_DIR="$fixture_dir/config" \
  DATABASE_URL="file:$fixture_dir/config/p2pstream.db?_busy_timeout=10000&_fk=1&_journal_mode=WAL&_synchronous=NORMAL&cache=private&mode=rwc" \
  PUBLIC_CACHE_DIR="$fixture_dir/cache" \
  MANAGEMENT_BIND_ADDRESS=127.0.0.1 \
  MANAGEMENT_PORT="$management_port" \
  MANAGEMENT_UI_DIST_DIR="$repo_dir/web/management/dist" \
  MANAGEMENT_UI_DEV_PROXY= \
  AGENT_UPDATES_ENABLED=false \
  ENV=development \
  bash -c 'IFS= read -r MANAGEMENT_SETUP_TOKEN; export MANAGEMENT_SETUP_TOKEN; exec "$@"' \
  -- "$fixture_dir/p2pstream" server <<< "$PLAYWRIGHT_SETUP_TOKEN" &
server_pid=$!
wait "$server_pid"
