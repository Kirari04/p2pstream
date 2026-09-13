#!/usr/bin/env bash
set -euo pipefail

# Installed as /etc/p2pstream-server-updater/compose. Keep host operations on
# the executor's exact Compose version so configuration hashes remain stable.
state_dir=/etc/p2pstream-server-updater
image_ref=$(docker compose -f "$state_dir/compose.json" config --images p2pstream-server-updater)
if [[ ! "$image_ref" =~ ^sha256:[0-9a-f]{64}$ ]]; then
  echo "The saved deployment must contain exactly one pinned updater image." >&2
  exit 1
fi
exec docker run --rm --entrypoint /usr/local/bin/docker \
  --mount type=bind,src=/var/run/docker.sock,dst=/var/run/docker.sock \
  --mount "type=bind,src=$state_dir,dst=$state_dir,readonly" \
  "$image_ref" compose --project-directory "$state_dir" -f "$state_dir/compose.json" "$@"
