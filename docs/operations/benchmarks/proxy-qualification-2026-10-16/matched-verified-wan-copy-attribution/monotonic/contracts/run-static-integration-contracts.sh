#!/usr/bin/env bash
set -Eeuo pipefail

root=$(cd "$(dirname "$0")/.." && pwd)
old="$root/../wan-copy-attribution-source"
protocol="$root/../wan-protocol-source"
owned="$root/../current-wan-prep/runner/qualify-proxy-agent-growth-owned.sh"
output=$1
[[ ! -e $output ]] || { echo "output already exists" >&2; exit 2; }
mkdir -p "$output"

bash -n "$root/run-wan-copy-monotonic-diagnostic.sh"
bash -n "$root/run-wan-copy-monotonic-arm.sh"
bash -n "$root/generated/qualify-proxy-agent-growth-wan-copy-monotonic.sh"
python3 -m py_compile "$root/validate-copy-record.py" "$root/validate-wan-copy-monotonic-arm.py"

cp "$owned" "$output/rebuilt.sh"
patch --quiet "$output/rebuilt.sh" "$protocol/wan-protocol-runner.patch"
cmp "$output/rebuilt.sh" "$protocol/generated/qualify-proxy-agent-growth-wan-protocol.sh"
patch --quiet "$output/rebuilt.sh" "$old/copy-attribution-runner.patch"
cmp "$output/rebuilt.sh" "$old/generated/qualify-proxy-agent-growth-wan-copy-attribution.sh"
patch --quiet "$output/rebuilt.sh" "$root/monotonic-runner.patch"
cmp "$output/rebuilt.sh" "$root/generated/qualify-proxy-agent-growth-wan-copy-monotonic.sh"

server="$root/build/bin/current-server-wan-copy-monotonic-audited.test"
agent="$root/../current-wan-prep/bin/current-agent.test"
server_sha=$(sha256sum "$server" | awk '{print $1}')
agent_sha=$(sha256sum "$agent" | awk '{print $1}')
[[ $server_sha == 0530cdc308bf06964f61d55ca730309e125cabd3c296ed3c22f0cfe218552013 ]]
[[ $agent_sha == 9e635d6e1e8fbd8651260eb081fb5fc7847438bc51c653a5f7785cbecde4a137 ]]
[[ $(sha256sum "$root/provenance/monotonic-build-manifest.json" | awk '{print $1}') == f505bcd62f1bd050f21f4ddc6d407b377ebb5039029ec66980439f050023ada3 ]]
[[ $(sha256sum "$root/provenance/audited-server-build-manifest.json" | awk '{print $1}') == 0ba246408bdabdb31bad837d0163b9957c93cbe8888eef2ed3fd4c29d2fed38b ]]
[[ $(sha256sum "$root/provenance/final-source-binary-toolchain-attestation.json" | awk '{print $1}') == 18a25434183eeecf08d3214eb6748c528a89fa2f74bc7c98854075eafb3ed37a ]]

jq -e --arg server "$server_sha" --arg agent "$agent_sha" '
  .source_verification.reconstruction_status == 0 and
  .source_verification.source_pre_post_test_equal == true and
  .source_verification.changed_paths_contract.accepted == true and
  .focused_tests.normal.status == 0 and .focused_tests.race.status == 0 and
  .focused_tests.copy_record_contracts.status == 0 and
  .build.status == 0 and .build.source_compare_status == 0 and
  .sha256.server_test_binary == $server and .sha256.aggregate_agent_test_binary == $agent and
  .traffic_run == false
' "$root/provenance/monotonic-build-manifest.json" >/dev/null
jq -e --arg server "$server_sha" --arg gomod "$root/build/source/go.mod" '
  .source_pre_post_equal == true and .source_compare_status == 0 and .build_status == 0 and
  .toolchain.sha256 == "29e6e0b8be61beb1489ceae62b304343566de8a1dc700af74bde7aeb9c80ad45" and
  .metadata_inspection.go_env_captured_from_compile_working_directory == true and
  .metadata_inspection.gomod == $gomod and .output.sha256 == $server and
  .test_rerun == false and .traffic_run == false
' "$root/provenance/audited-server-build-manifest.json" >/dev/null

grep -F 'copy_record.get("handler_elapsed_ns")' "$root/validate-wan-copy-monotonic-arm.py" >/dev/null
grep -F 'monotonic handler elapsed time' "$root/validate-wan-copy-monotonic-arm.py" >/dev/null
grep -F 'monotonic handler elapsed time' "$root/generated/qualify-proxy-agent-growth-wan-copy-monotonic.sh" >/dev/null
! grep -F 'returned_ns - started_ns' "$root/validate-wan-copy-monotonic-arm.py" >/dev/null
! grep -F 'handler wall time' "$root/validate-wan-copy-monotonic-arm.py" >/dev/null

python3 - "$output/result.json" "$root" "$server_sha" "$agent_sha" <<'PY'
import hashlib, json, pathlib, sys
destination, root, server, agent = sys.argv[1:]
root = pathlib.Path(root)
def sha(path): return hashlib.sha256(pathlib.Path(path).read_bytes()).hexdigest()
value = {
    "accepted": True,
    "agent_sha256": agent,
    "generated_runner_sha256": sha(root / "generated/qualify-proxy-agent-growth-wan-copy-monotonic.sh"),
    "runner_reconstruction_sha256": sha(pathlib.Path(destination).with_name("rebuilt.sh")),
    "schema_version": 1,
    "server_sha256": server,
    "traffic_run": False,
}
pathlib.Path(destination).write_text(json.dumps(value, indent=2, sort_keys=True) + "\n")
PY
printf '%s\n' complete > "$output/FINAL"
