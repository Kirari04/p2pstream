#!/usr/bin/env bash
set -Eeuo pipefail

root=$(cd "$(dirname "$0")/.." && pwd)
source_dir="$root/build/source"
go_binary=/home/user/go/pkg/mod/golang.org/toolchain@v0.0.1-go1.26.6.linux-amd64/bin/go
output="$root/build/bin/current-server-wan-copy-attribution-audited.test"
record="$root/provenance/audited-server-build"
tmpdir="$root/build/audited-tmp"
gocache="$root/build/audited-cache"

[[ ! -e $output && ! -e $record ]] || { echo "audited output or record already exists" >&2; exit 2; }
[[ $(sha256sum "$go_binary" | awk '{print $1}') == 29e6e0b8be61beb1489ceae62b304343566de8a1dc700af74bde7aeb9c80ad45 ]] || {
  echo "canonical pinned Go binary hash changed" >&2
  exit 2
}
[[ $($go_binary version) == "go version go1.26.6 linux/amd64" ]] || { echo "canonical pinned Go version changed" >&2; exit 2; }
mkdir -p "$record" "$tmpdir" "$gocache"

source_manifest() {
  local destination=$1
  python3 - "$source_dir" "$destination" <<'PY'
import hashlib, json, pathlib, sys
root, destination = map(pathlib.Path, sys.argv[1:])
files = {}
for path in sorted(root.rglob("*")):
    if path.is_file():
        digest = hashlib.sha256()
        with path.open("rb") as source:
            for block in iter(lambda: source.read(1024 * 1024), b""):
                digest.update(block)
        files[str(path.relative_to(root))] = digest.hexdigest()
destination.write_text(json.dumps({"schema_version": 1, "root": str(root), "files": files}, indent=2, sort_keys=True) + "\n")
PY
}

source_manifest "$record/source-pre.json"
printf '%s\n' "$source_dir" > "$record/working-directory.txt"
sha256sum "$go_binary" > "$record/toolchain-pre.sha256"
"$go_binary" version > "$record/go-version.txt"
env -i HOME=/home/user PATH=/usr/bin:/bin TMPDIR="$tmpdir" GOCACHE="$gocache" GOENV=off GOTOOLCHAIN=local GOWORK=off GOFLAGS= CGO_ENABLED=1 \
  "$go_binary" env -json > "$record/go-env.json"
printf '%q ' env -i HOME=/home/user PATH=/usr/bin:/bin TMPDIR="$tmpdir" GOCACHE="$gocache" GOENV=off GOTOOLCHAIN=local GOWORK=off GOFLAGS= CGO_ENABLED=1 \
  "$go_binary" test -c -o "$output" ./internal/server > "$record/invocation.txt"
printf '\n' >> "$record/invocation.txt"

set +e
(
  cd "$source_dir"
  env -i HOME=/home/user PATH=/usr/bin:/bin TMPDIR="$tmpdir" GOCACHE="$gocache" GOENV=off GOTOOLCHAIN=local GOWORK=off GOFLAGS= CGO_ENABLED=1 \
    "$go_binary" test -c -o "$output" ./internal/server
) > "$record/stdout.log" 2> "$record/stderr.log"
status=$?
set -e
printf '%s\n' "$status" > "$record/status"
source_manifest "$record/source-post.json"
sha256sum "$go_binary" > "$record/toolchain-post.sha256"
cmp "$record/source-pre.json" "$record/source-post.json" > "$record/source-pre-post.stdout.log" 2> "$record/source-pre-post.stderr.log"
printf '%s\n' "$?" > "$record/source-pre-post.status"
if ((status != 0)); then
  exit "$status"
fi
sha256sum "$output" > "$record/output.sha256"
"$go_binary" version -m "$output" > "$record/output.go-version-m.txt"
printf '%s\n' complete > "$record/FINAL"
