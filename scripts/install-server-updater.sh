#!/usr/bin/env bash
set -euo pipefail
script_dir=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
exec python3 -I "$script_dir/server-updater-host.py" install --bundle "$script_dir" "$@"
