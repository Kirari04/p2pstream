set -eu
root=tmp/continuation-20261016/wan-copy-monotonic-source
chmod +x "$root/provenance/run-audited-server-build.sh"
bash -n "$root/provenance/run-audited-server-build.sh"
printf '0\n' > "$root/logs/audited-build-static.status"
set +e
"$root/provenance/run-audited-server-build.sh" > "$root/logs/audited-build-wrapper.stdout.log" 2> "$root/logs/audited-build-wrapper.stderr.log"
status=$?
set -e
printf '%s\n' "$status" > "$root/logs/audited-build-wrapper.status"
cat "$root/logs/audited-build-wrapper.stdout.log"
cat "$root/logs/audited-build-wrapper.stderr.log"
exit "$status"
