#!/usr/bin/env bash
set -euo pipefail
repo=${1:?repository path required}
out=${2:?empty output directory required}
bundle=$(cd -- "$(dirname -- "$0")/.." && pwd)
mkdir -p "$out/accepted" "$out/http"
git -C "$repo" archive ccb9c9cb683b555044033d68ff59112aa4bf21d9 | tar -xf - -C "$out/accepted"
patch -s -p1 -d "$out/accepted" < "$bundle/overlay/accepted-transport-ceiling.patch"
cp -a "$out/accepted/." "$out/http/"
git -C "$out/http" init -q
git -C "$out/http" apply --check "$bundle/overlay/http-stage.patch"
git -C "$out/http" apply "$bundle/overlay/http-stage.patch"
git -C "$out/http" add -A
http_tree=$(git -C "$out/http" write-tree)
[[ $http_tree == 364e7744603841f5d306f4c242f3acd2b04183bf ]]
git -C "$out/http" apply -R --check "$bundle/overlay/http-stage.patch"
git -C "$out/http" apply -R "$bundle/overlay/http-stage.patch"
git -C "$out/http" add -A
accepted_tree=$(git -C "$out/http" write-tree)
[[ $accepted_tree == 40e1046c2f68ee3d545dfd582cd424c2f3604b2a ]]
printf 'accepted_tree=%s\nhttp_tree=%s\nreverse_tree=%s\n' 40e1046c2f68ee3d545dfd582cd424c2f3604b2a 364e7744603841f5d306f4c242f3acd2b04183bf "$accepted_tree"
