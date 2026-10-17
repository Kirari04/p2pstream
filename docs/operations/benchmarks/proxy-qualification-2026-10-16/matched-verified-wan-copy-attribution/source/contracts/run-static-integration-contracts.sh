#!/usr/bin/env bash
set -Eeuo pipefail

root=$(realpath "$(dirname "$0")/..")
frozen="$root/../wan-protocol-source"
approved="$root/../current-wan-prep/runner/qualify-proxy-agent-growth-owned.sh"
output=${1:?new output directory required}
[[ ! -e $output ]] || { echo "output already exists: $output" >&2; exit 2; }
mkdir -p "$output"

bash -n "$root/generated/qualify-proxy-agent-growth-wan-copy-attribution.sh" \
  "$root/run-wan-copy-attribution-arm.sh" "$root/run-wan-copy-attribution-diagnostic.sh"
python3 -m py_compile "$root/validate-copy-record.py" "$root/validate-wan-copy-attribution-arm.py"

cp "$approved" "$output/reconstructed.sh"
patch --quiet "$output/reconstructed.sh" "$frozen/wan-protocol-runner.patch"
cmp "$output/reconstructed.sh" "$frozen/generated/qualify-proxy-agent-growth-wan-protocol.sh"
printf '%s\n' "frozen-protocol-stage-verified" > "$output/frozen-stage.status"
patch --quiet "$output/reconstructed.sh" "$root/copy-attribution-runner.patch"
cmp "$output/reconstructed.sh" "$root/generated/qualify-proxy-agent-growth-wan-copy-attribution.sh"
printf '%s\n' "copy-stage-verified" > "$output/copy-stage.status"

python3 - "$root" <<'PY' > "$output/static-structure.json"
import json, pathlib, sys
root = pathlib.Path(sys.argv[1])
runner = (root / "generated/qualify-proxy-agent-growth-wan-copy-attribution.sh").read_text()
assert runner.count("P2PSTREAM_QUALIFICATION_WAN_COPY_ATTRIBUTION=1") == 2
assert runner.count("response_copy_attribution:$copy_attribution") == 1
assert runner.count("query_completed_copy_json") == 2
assert 'P2PSTREAM_QUALIFICATION_ROLE=load \\\n  P2PSTREAM_QUALIFICATION_URL=' in runner
assert 'P2PSTREAM_QUALIFICATION_ROLE=load \\\n  P2PSTREAM_QUALIFICATION_WAN_COPY_ATTRIBUTION=1' not in runner
assert runner.count('P2PSTREAM_QUALIFICATION_ROLE=proxy-external-agent') == 2
assert runner.count('P2PSTREAM_QUALIFICATION_WAN_PROTOCOL_DIAGNOSTIC=1 \\\n      P2PSTREAM_QUALIFICATION_WAN_COPY_ATTRIBUTION=1') == 2
value = {"accepted": True, "server_copy_env_occurrences": 2, "row_envelope_occurrences": 1,
         "completed_query_definition_and_call_occurrences": 2,
         "copy_env_absent_from_load_process": True, "server_role_occurrences": 2}
print(json.dumps(value, indent=2, sort_keys=True))
PY

jq -e --arg server "59eb8af1e6c767776f639b014074d542abb23d4ad019c7558182670372ea92a7" \
  --arg agent "9e635d6e1e8fbd8651260eb081fb5fc7847438bc51c653a5f7785cbecde4a137" \
  --arg server_ref "8ac4c8df8237e37777ba2fac0da6f949da17bdea" \
  --arg agent_ref "8ac4c8df8237e37777ba2fac0da6f949da17bdea" '
    .base_source_ref == $server_ref and .aggregate_agent_source_ref == $agent_ref and
    .sha256.server_test_binary == $server and .sha256.aggregate_agent_test_binary == $agent and
    .sha256.frozen_wan_base_patch == "b2c90d2ee6bf9ade304163795022071b4d368b61f0e9d5bc73d0e52d2569538b" and
    .sha256.frozen_wan_test_helper == "b0b6d037d51892e89d39e6f55107f73c30647ad9a20283a026007fd69b3be42d" and
    .sha256.copy_attribution_overlay_patch == "4da0889228f80407806ac111187366ab06abd0ffd5d130d08d41407a8a02ff5f" and
    .sha256.design == "a52431ddf9439a7424a9da6e81e26b2121ba0590e32ae8603b4ad20478aa1baf" and
    .reconstruction_status == 0 and .build_status == 0 and
    .focused_tests.normal_status == 0 and .focused_tests.race_status == 0 and .traffic_run == false
  ' "$root/provenance/copy-attribution-build-manifest.json" >/dev/null
jq -e '
  .source_verification.build_source_equals_independent_reconstruction == true and
  .toolchain.go_version == "go version go1.26.6 linux/amd64" and
  .binaries.server.sha256 == "59eb8af1e6c767776f639b014074d542abb23d4ad019c7558182670372ea92a7" and
  .traffic_run == false
' "$root/provenance/final-source-binary-toolchain-attestation.json" >/dev/null
jq -e --arg server "59eb8af1e6c767776f639b014074d542abb23d4ad019c7558182670372ea92a7" \
  --arg path "build/bin/current-server-wan-copy-attribution-audited.test" --arg gomod "$root/build/source/go.mod" '
    .base_source_ref == "8ac4c8df8237e37777ba2fac0da6f949da17bdea" and
    .copy_attribution_overlay_sha256 == "4da0889228f80407806ac111187366ab06abd0ffd5d130d08d41407a8a02ff5f" and
    .source_pre_post_equal == true and .build_status == 0 and .source_compare_status == 0 and
    .toolchain.path == "/home/user/go/pkg/mod/golang.org/toolchain@v0.0.1-go1.26.6.linux-amd64/bin/go" and
    .toolchain.sha256 == "29e6e0b8be61beb1489ceae62b304343566de8a1dc700af74bde7aeb9c80ad45" and
    .toolchain.version == "go version go1.26.6 linux/amd64" and .toolchain.pre_post_equal == true and
    .output.path == $path and .output.sha256 == $server and
    .metadata_inspection.original_prebuild_go_env.captured_before_compile_cd == true and
    .metadata_inspection.postbuild_go_env_in_compile_working_directory.gomod == $gomod and
    .metadata_inspection.postbuild_go_env_in_compile_working_directory.inspection_only_no_compile_or_test == true and
    .test_rerun == false and .traffic_run == false
  ' "$root/provenance/audited-server-build-manifest.json" >/dev/null
[[ $(sha256sum "$root/provenance/audited-server-build-manifest.json" | awk '{print $1}') == 70be2d865f415653f1344ec29d230bae39a0de9148afa37830c05da2dc8f81bb ]]

server=$(realpath "$root/build/bin/current-server-wan-copy-attribution-audited.test")
agent=$(realpath "$root/../current-wan-prep/bin/current-agent.test")
cert=$(realpath "$root/../wan-protocol-source/runs/sustained-matched-1/tls/public.test.crt.pem")
key=$(realpath "$root/../wan-protocol-source/runs/sustained-matched-1/tls/public.test.key.pem")
[[ $server == /* && $agent == /* && $cert == /* && $key == /* && -x $server && -x $agent && -f $cert && -f $key ]]
[[ $(sha256sum "$server" | awk '{print $1}') == 59eb8af1e6c767776f639b014074d542abb23d4ad019c7558182670372ea92a7 ]]
[[ $(sha256sum "$agent" | awk '{print $1}') == 9e635d6e1e8fbd8651260eb081fb5fc7847438bc51c653a5f7785cbecde4a137 ]]
[[ $(sha256sum "$approved" | awk '{print $1}') == b29242b6891105fd6231fecd7739ae48e6909049d0583a184765839ad2827c5f ]]
[[ $(sha256sum "$root/../current-wan-prep/COPY-TIMING-DECISION-MAP.md" | awk '{print $1}') == f6dc409926ca8178ba0da8ec262960a6b8e238d2a9d94dbe0afc9077f629c6f7 ]]
printf 'server=%s\nagent=%s\ncert=%s\nkey=%s\n' "$server" "$agent" "$cert" "$key" > "$output/canonical-paths.txt"

sha256sum "$root/generated/qualify-proxy-agent-growth-wan-copy-attribution.sh" "$root/copy-attribution-runner.patch" \
  "$root/validate-copy-record.py" "$root/validate-wan-copy-attribution-arm.py" \
  "$root/run-wan-copy-attribution-arm.sh" "$root/run-wan-copy-attribution-diagnostic.sh" > "$output/source-hashes.txt"
printf '%s\n' "static integration contracts passed"
