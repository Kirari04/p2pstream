#!/usr/bin/env bash
set -euo pipefail
# Read-only operations on the saved model use the executor's pinned tools.
# Mutations must pass the host controller's lock, layout checks and rollback.
case "${1:-}" in
  ps|logs|config) ;;
  *) echo "Use sudo /etc/p2pstream-server-updater/manage {apply|repair|rollback|remove} for deployment changes." >&2; exit 1 ;;
esac
exec python3 -I /etc/p2pstream-server-updater/server-updater-host.py inspect "$@"
