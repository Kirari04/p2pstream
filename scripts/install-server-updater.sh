#!/usr/bin/env bash
set -euo pipefail

# Run on the deployment host, from its Compose directory. Additional arguments
# are Docker Compose global options, e.g. -f compose.yaml -f production.yaml.
if [[ ${EUID} -ne 0 ]]; then
  echo "Run this installer with sudo on the Docker deployment host." >&2
  exit 1
fi
state_dir=/etc/p2pstream-server-updater
script_dir=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
if [[ -f "$state_dir/config.json" ]]; then
  echo "Server updater is already enrolled. Inspect it with: sudo $state_dir/compose ps"
  echo "If initial activation failed, fix the reported problem and run: sudo $state_dir/compose up -d --no-deps --no-build --pull never p2pstream p2pstream-server-updater"
  exit 0
fi
command -v docker >/dev/null
docker compose version >/dev/null
container_id=$(docker compose "$@" ps --quiet p2pstream)
if [[ -z "$container_id" || "$container_id" == *$'\n'* ]]; then
  echo "Expected one running p2pstream Compose service." >&2
  exit 1
fi
image_id=$(docker inspect --format '{{.Image}}' "$container_id")
image_ref=$(docker image inspect --format '{{index .RepoDigests 0}}' "$image_id")
if [[ ! "$image_ref" =~ ^ghcr\.io/[a-z0-9_.-]+/[a-z0-9_.-]+@sha256:[0-9a-f]{64}$ ]]; then
  echo "Install a published, digest-addressable p2pstream release first." >&2
  exit 1
fi
# Reject releases predating the updater API before making any host changes.
docker run --rm --entrypoint /app/p2pstream "$image_ref" server-updater --help >/dev/null
install -d -m 0700 "$state_dir"
install -m 0700 "$script_dir/server-updater-compose.sh" "$state_dir/compose"
snapshot=$(mktemp "$state_dir/.compose-input-XXXXXX.json")
trap 'rm -f -- "$snapshot"' EXIT
docker compose "$@" config --format json >"$snapshot"
# Adopt the running deployment, not unapplied edits to its Compose file or
# .env. Re-read the resolved snapshot with the same host Compose version that
# identified the container, before enrollment can recreate it or change data.
running_hash=$(docker inspect --format '{{ index .Config.Labels "com.docker.compose.config-hash" }}' "$container_id")
snapshot_hash=$(docker compose --project-directory "$PWD" -f "$snapshot" config --hash p2pstream)
if [[ ! "$snapshot_hash" =~ ^p2pstream[[:space:]]+([0-9a-f]{64})$ ]] || [[ "${BASH_REMATCH[1]}" != "$running_hash" ]]; then
  echo "The resolved Compose configuration differs from the running server. Apply or revert pending Compose/.env changes using your existing deployment procedure before enrolling." >&2
  exit 1
fi
docker build --quiet --build-arg "P2PSTREAM_IMAGE=$image_ref" \
  -t p2pstream-server-updater:installed - <"$script_dir/../Dockerfile.updater" >/dev/null
updater_image=$(docker image inspect --format '{{.Id}}' p2pstream-server-updater:installed)
docker run --rm --user 0:0 --entrypoint /app/p2pstream \
  --mount "type=bind,src=$state_dir,dst=$state_dir" "$image_ref" server-updater enroll \
  --directory "$state_dir" --compose "$snapshot" --image "$image_ref" --updater-image "$updater_image"
echo "Enabling the updater restarts p2pstream once; public traffic and agent tunnels will reconnect."
"$state_dir/compose" up -d --no-deps --no-build --pull never p2pstream p2pstream-server-updater
echo "Server updates are enabled. Select this environment in System → Server Updates."
echo "Use this saved deployment for future host operations: sudo $state_dir/compose"
