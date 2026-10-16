#!/usr/bin/env bash
set -Eeuo pipefail

integration_dir=$(cd "$(dirname "$0")" && pwd)
capacity_root=$(realpath "$integration_dir/../../wan-read-wait-capacity-source")
compact_root=$(realpath "$integration_dir/..")
script_dir=$capacity_root
runner="$script_dir/generated/qualify-proxy-agent-growth-wan-read-wait.sh"
validator="$integration_dir/validate-wan-read-wait-arm.py"
copy_record_validator="$script_dir/validate-copy-record.py"
approved_runner="$script_dir/../current-wan-prep/runner/qualify-proxy-agent-growth-owned.sh"
frozen_protocol_runner="$script_dir/../wan-protocol-source/generated/qualify-proxy-agent-growth-wan-protocol.sh"
frozen_copy_runner="$script_dir/../wan-copy-attribution-source/generated/qualify-proxy-agent-growth-wan-copy-attribution.sh"
wan_protocol_runner_patch="$script_dir/../wan-protocol-source/wan-protocol-runner.patch"
copy_runner_patch="$script_dir/../wan-copy-attribution-source/copy-attribution-runner.patch"
monotonic_root=$(realpath "$script_dir/../wan-copy-monotonic-source")
predecessor_read_wait_root=$(realpath "$script_dir/../wan-read-wait-source")
monotonic_runner_patch="$monotonic_root/monotonic-runner.patch"
read_wait_runner_patch="$script_dir/read-wait-runner.patch"
server_capacity_patch="$script_dir/server-capacity-snapshot.patch"
cleanup_status_patch="$script_dir/cleanup-status.patch"
go_patch="$script_dir/../wan-protocol-source/proxy-qualification-base.patch"
go_helper="$script_dir/../wan-protocol-source/proxy_qualification_wan_protocol_test.go"
copy_overlay="$script_dir/../wan-copy-attribution-source/copy-attribution-overlay.patch"
monotonic_observer_patch="$monotonic_root/monotonic-observer.patch"
monotonic_validator_patch="$monotonic_root/monotonic-validator-contract.patch"
monotonic_arm_validator_patch="$monotonic_root/monotonic-arm-validator.patch"
copy_design="$monotonic_root/DESIGN.md"
monotonic_build_manifest="$monotonic_root/provenance/monotonic-build-manifest.json"
monotonic_audited_build_manifest="$monotonic_root/provenance/audited-server-build-manifest.json"
final_source_attestation="$monotonic_root/provenance/final-source-binary-toolchain-attestation.json"
runner_reconstruction_attestation="$monotonic_root/provenance/runner-reconstruction-attestation.json"
integration_notes="$monotonic_root/INTEGRATION-NOTES.md"
decision_map="$script_dir/../current-wan-prep/COPY-TIMING-DECISION-MAP.md"
observer_patch="$predecessor_read_wait_root/observer-source.patch"
observer_checkpoint="$predecessor_read_wait_root/OBSERVER-SOURCE-CHECKPOINT.json"
focused_checkpoint="$predecessor_read_wait_root/provenance/focused-tests/FOCUSED-TEST-CHECKPOINT.json"
audited_build_manifest="$predecessor_read_wait_root/provenance/audited-server-build-manifest.json"
capacity_source_checkpoint="$script_dir/review/capacity-go-source-checkpoint-2/CHECKPOINT.json"
capacity_source_checkpoint_manifest="$script_dir/review/capacity-go-source-checkpoint-2/SHA256SUMS.txt"
capacity_focused_checkpoint="$script_dir/provenance/CAPACITY-FOCUSED-TEST-CHECKPOINT.json"
capacity_audited_build_manifest="$script_dir/provenance/capacity-audited-server-build-manifest.json"
scope_entrypoint="$script_dir/server-cgroup-entrypoint.sh"
scope_sentinel="$script_dir/server-scope-sentinel.sh"
scope_validator="$script_dir/validate-server-scope.py"
scope_resource_validator="$script_dir/validate-server-resource.py"
scope_probe_attestation="$script_dir/scope-probe/attempt-3/attestation.json"
trace_analyzer="$compact_root/analyze_interned_trace.py"
trace_analyzer_hashes="$compact_root/review/source-checkpoint-8/SHA256SUMS.txt"
trace_analyzer_schema="$compact_root/OUTPUT-SCHEMA.json"
trace_analyzer_checkpoint="$integration_dir/COMPACT-CHECKPOINT.json"
trace_parser_wrapper="$compact_root/run_interned_trace_parser.py"
trace_canonicalizer="$compact_root/intern_parsed_trace.py"
trace_format="$compact_root/stack_intern_format.py"
trace_focused_manifest="$compact_root/runs/stack-intern-focused-5/MANIFEST.sha256"
trace_focused_summary="$compact_root/runs/stack-intern-focused-5/work/summary.json"
trace_lifecycle_manifest="$compact_root/adapter-contracts-source/FINAL-SHA256SUMS.txt"
trace_lifecycle_summary="$compact_root/adapter-contracts-source/runs/adapter-contracts-1/summary.json"
runner_integration_design="$integration_dir/INTEGRATION-DESIGN.md"
static_contract="$script_dir/contracts/run-static-integration-contracts.sh"
resource_contract="$script_dir/contracts/run-server-resource-contracts.py"
traced_transfer_contract="$script_dir/contracts/run-traced-transfer-contracts.py"
arm_validator_contract="$script_dir/contracts/run-arm-validator-contracts.py"
cleanup_status_contract="$script_dir/contracts/run-cleanup-status-contract.sh"
cleanup_status_body="$script_dir/contracts/PROPOSED-CLEANUP.sh"
cleanup_status_holder="$script_dir/contracts/owned-group-holder.py"
all_contracts="$script_dir/contracts/run-all-no-traffic-contracts.sh"
pinned_go=/home/user/go/pkg/mod/golang.org/toolchain@v0.0.1-go1.26.6.linux-amd64/bin/go
server_binary=""
aggregate_agent_binary=""
server_source_ref=""
aggregate_agent_source_ref=""
protocol=""
cert=""
key=""
expected_leaf_sha256=""
output_dir=""
smoke=0

while (($#)); do
  case "$1" in
    --server-binary) server_binary=$2; shift 2 ;;
    --aggregate-agent-binary) aggregate_agent_binary=$2; shift 2 ;;
    --server-source-ref) server_source_ref=$2; shift 2 ;;
    --aggregate-agent-source-ref) aggregate_agent_source_ref=$2; shift 2 ;;
    --protocol) protocol=$2; shift 2 ;;
    --cert) cert=$2; shift 2 ;;
    --key) key=$2; shift 2 ;;
    --expected-leaf-sha256) expected_leaf_sha256=$2; shift 2 ;;
    --output-dir) output_dir=$2; shift 2 ;;
    --smoke) smoke=1; shift ;;
    *) echo "unknown argument: $1" >&2; exit 2 ;;
  esac
done

[[ -x $server_binary && -x $aggregate_agent_binary && -x $runner && -x $validator && -x $copy_record_validator ]] || {
  echo "server, agent, runner, arm validator, and copy validator must be executable" >&2
  exit 2
}
[[ -n $server_source_ref && -n $aggregate_agent_source_ref && -n $output_dir ]] || {
  echo "source refs and output directory are required" >&2
  exit 2
}
[[ $protocol == h1 || $protocol == h2 ]] || { echo "protocol must be h1 or h2" >&2; exit 2; }
[[ -f $cert && -f $key && $expected_leaf_sha256 =~ ^[0-9a-f]{64}$ ]] || {
  echo "certificate, key, and lowercase DER fingerprint are required" >&2
  exit 2
}
server_binary=$(realpath "$server_binary")
aggregate_agent_binary=$(realpath "$aggregate_agent_binary")
approved_server_binary=$(realpath "$script_dir/build/bin/current-server-wan-read-wait-capacity-audited.test")
cert=$(realpath "$cert")
key=$(realpath "$key")
for tool in cmp jq lscpu openssl patch python3 readlink setsid sha256sum systemctl systemd-run taskset timeout unshare; do
  command -v "$tool" >/dev/null || { echo "$tool is required" >&2; exit 2; }
done
[[ $(sha256sum "$approved_runner" | awk '{print $1}') == b29242b6891105fd6231fecd7739ae48e6909049d0583a184765839ad2827c5f ]] || {
  echo "approved lifecycle runner hash changed" >&2
  exit 2
}
[[ $(sha256sum "$frozen_protocol_runner" | awk '{print $1}') == 309d90622ad36f8f082bfd010ec85a7f87d13d80d9d8ce9a8b97ec883a12bad7 ]] || {
  echo "frozen WAN protocol runner hash changed" >&2
  exit 2
}
[[ $(sha256sum "$frozen_copy_runner" | awk '{print $1}') == 4c76336266bceaab9d2639a5d81e3ce272b4dc924d15154bb6ccbdea4b58f852 ]] || {
  echo "frozen copy-attribution runner hash changed" >&2
  exit 2
}
for name in P2PSTREAM_QUALIFICATION_PROFILE_DIR P2PSTREAM_QUALIFICATION_PROFILE_MODE; do
  [[ -z ${!name+x} ]] || { echo "$name must be unset" >&2; exit 2; }
done
server_binary_sha256=$(sha256sum "$server_binary" | awk '{print $1}')
agent_binary_sha256=$(sha256sum "$aggregate_agent_binary" | awk '{print $1}')
runner_sha256=$(sha256sum "$runner" | awk '{print $1}')
[[ $server_binary == "$approved_server_binary" ]] || {
  echo "server binary path is not the fresh audited diagnostic build" >&2
  exit 2
}
[[ $server_binary_sha256 == 57d77ddc5ee8ec6ab49cab0b7d0dd29641a70fa8598f0c7ba3eac73f46632ff5 && \
   $agent_binary_sha256 == 9e635d6e1e8fbd8651260eb081fb5fc7847438bc51c653a5f7785cbecde4a137 ]] || {
  echo "server or aggregate-agent binary is not the approved diagnostic build" >&2
  exit 2
}
[[ $(sha256sum "$monotonic_build_manifest" | awk '{print $1}') == f505bcd62f1bd050f21f4ddc6d407b377ebb5039029ec66980439f050023ada3 ]] || {
  echo "monotonic build manifest hash changed" >&2
  exit 2
}
[[ $(sha256sum "$final_source_attestation" | awk '{print $1}') == 18a25434183eeecf08d3214eb6748c528a89fa2f74bc7c98854075eafb3ed37a ]] || {
  echo "final monotonic source attestation hash changed" >&2
  exit 2
}
[[ $(sha256sum "$monotonic_observer_patch" | awk '{print $1}') == a8cc867dc9e12681e74b6a51396941d0a03c5145450eabff2e0bae0d8e79965a &&
   $(sha256sum "$monotonic_validator_patch" | awk '{print $1}') == f1dafaabc985842bc7cbc18a199e79abdc709e9021f4c5d7695ad17eb5e90f63 &&
   $(sha256sum "$monotonic_runner_patch" | awk '{print $1}') == 23cb24cd59481005ae83e580a8d20dedc0c4e1167df01ffd3ec8c854a807263a &&
   $(sha256sum "$monotonic_arm_validator_patch" | awk '{print $1}') == fcae97fd6653cb438e5b85295844b394aa1108e0f66d064cdee5942b412d04f3 ]] || {
  echo "monotonic correction artifact hash changed" >&2
  exit 2
}
jq -e --arg server "0530cdc308bf06964f61d55ca730309e125cabd3c296ed3c22f0cfe218552013" --arg agent "$agent_binary_sha256" \
  --arg server_ref "$server_source_ref" --arg agent_ref "$aggregate_agent_source_ref" '
    .base_source_ref == $server_ref and .aggregate_agent_source_ref == $agent_ref and
    .sha256.server_test_binary == $server and .sha256.aggregate_agent_test_binary == $agent and
    .frozen_input.copy_attribution_overlay_sha256 == "4da0889228f80407806ac111187366ab06abd0ffd5d130d08d41407a8a02ff5f" and
    .sha256.monotonic_observer_patch == "a8cc867dc9e12681e74b6a51396941d0a03c5145450eabff2e0bae0d8e79965a" and
    .sha256.monotonic_validator_contract_patch == "f1dafaabc985842bc7cbc18a199e79abdc709e9021f4c5d7695ad17eb5e90f63" and
    .source_verification.reconstruction_status == 0 and .source_verification.source_pre_post_test_equal == true and
    .focused_tests.normal.status == 0 and .focused_tests.race.status == 0 and
    .focused_tests.copy_record_contracts.status == 0 and .build.status == 0 and .build.source_compare_status == 0 and
    .traffic_run == false
  ' "$monotonic_build_manifest" >/dev/null || { echo "diagnostic build manifest does not bind the requested artifacts" >&2; exit 2; }
jq -e --arg server "0530cdc308bf06964f61d55ca730309e125cabd3c296ed3c22f0cfe218552013" --arg agent "$agent_binary_sha256" '
    .source_verification.frozen_copy_attribution_overlay_sha256 == "4da0889228f80407806ac111187366ab06abd0ffd5d130d08d41407a8a02ff5f" and
    .source_verification.monotonic_observer_patch_sha256 == "a8cc867dc9e12681e74b6a51396941d0a03c5145450eabff2e0bae0d8e79965a" and
    .source_verification.reconstruction_status == 0 and .toolchain.version == "go version go1.26.6 linux/amd64" and
    .binaries.server.sha256 == $server and .binaries.agent.sha256 == $agent and .diagnostic_only == true and .traffic_run == false
  ' "$final_source_attestation" >/dev/null || { echo "final source/binary/toolchain attestation does not bind the requested artifacts" >&2; exit 2; }
[[ $(sha256sum "$monotonic_audited_build_manifest" | awk '{print $1}') == 0ba246408bdabdb31bad837d0163b9957c93cbe8888eef2ed3fd4c29d2fed38b ]] || {
  echo "audited server build manifest hash changed" >&2
  exit 2
}
jq -e --arg server "0530cdc308bf06964f61d55ca730309e125cabd3c296ed3c22f0cfe218552013" --arg path "build/bin/current-server-wan-copy-monotonic-audited.test" \
  --arg gomod "$monotonic_root/build/source/go.mod" '
    .base_source_ref == "8ac4c8df8237e37777ba2fac0da6f949da17bdea" and
    .frozen_copy_attribution_overlay_sha256 == "4da0889228f80407806ac111187366ab06abd0ffd5d130d08d41407a8a02ff5f" and
    .monotonic_observer_patch_sha256 == "a8cc867dc9e12681e74b6a51396941d0a03c5145450eabff2e0bae0d8e79965a" and
    .source_pre_post_equal == true and .build_status == 0 and .source_compare_status == 0 and
    .toolchain.path == "/home/user/go/pkg/mod/golang.org/toolchain@v0.0.1-go1.26.6.linux-amd64/bin/go" and
    .toolchain.sha256 == "29e6e0b8be61beb1489ceae62b304343566de8a1dc700af74bde7aeb9c80ad45" and
    .toolchain.version == "go version go1.26.6 linux/amd64" and .toolchain.pre_post_equal == true and
    .output.path == $path and .output.sha256 == $server and
    .metadata_inspection.go_env_captured_from_compile_working_directory == true and
    .metadata_inspection.gomod == $gomod and
    .test_rerun == false and .traffic_run == false
  ' "$monotonic_audited_build_manifest" >/dev/null || { echo "audited monotonic server build manifest changed" >&2; exit 2; }
[[ $(sha256sum "$observer_checkpoint" | awk '{print $1}') == 327a70507ddb94af99e1b2bc2ab7b9037a18b93daebfaef9acb51a7c0bd80faf &&
   $(sha256sum "$observer_patch" | awk '{print $1}') == 6ced006fc0aa8284898d7b696bb24b983a6162994513c520e3626c74e64d0c57 &&
   $(sha256sum "$focused_checkpoint" | awk '{print $1}') == accb27f10a34d7d3319e1d11bd03600d000de62e33ae5f1cb6ae2328a5a50958 &&
   $(sha256sum "$audited_build_manifest" | awk '{print $1}') == 42f3d7660883425391dbf01f83975957873701a65ea8daa19041a4afada15b3c ]] || {
  echo "read-wait observer source, test, or audited-build checkpoint changed" >&2
  exit 2
}
jq -e --arg server "47981bcd767fb7eb3e1456ae4d99c6d80d9fffc62eecb6be0fb67f9245f8b2aa" --arg cwd "$predecessor_read_wait_root/build/source" '
    .observer_source_checkpoint_sha256 == "327a70507ddb94af99e1b2bc2ab7b9037a18b93daebfaef9acb51a7c0bd80faf" and
    .observer_source_patch_sha256 == "6ced006fc0aa8284898d7b696bb24b983a6162994513c520e3626c74e64d0c57" and
    .build.status == 0 and .build.source_compare_status == 0 and .build.module_compare_status == 0 and
    .build.toolchain_compare_status == 0 and .toolchain.sha256 == "29e6e0b8be61beb1489ceae62b304343566de8a1dc700af74bde7aeb9c80ad45" and
    .metadata.working_directory == $cwd and .output.sha256 == $server and
    .endpoint_execution_run == false
  ' "$audited_build_manifest" >/dev/null || { echo "read-wait audited build manifest does not bind the requested artifact" >&2; exit 2; }
[[ $(sha256sum "$capacity_source_checkpoint" | awk '{print $1}') == 04da91fe74d9a7d13a8c8ae6e5c982ac2275709d950280c3c7776fc06fb22a7f &&
   $(sha256sum "$capacity_source_checkpoint_manifest" | awk '{print $1}') == a3c4f89addc191cc27c51994589c8bef9ec92ab898274b9d3cb151f91e5e30c3 &&
   $(sha256sum "$capacity_focused_checkpoint" | awk '{print $1}') == 95da60a769b1efd32abb8e799b8e94fedea1c197965fbe239ded10939f8771ff &&
   $(sha256sum "$capacity_audited_build_manifest" | awk '{print $1}') == 2bf50c5e7c7aaa4a1d16c0ab09ae516b92abf714404af7a0869234e80a64e075 ]] || {
  echo "capacity source, focused-test, or audited-build checkpoint changed" >&2
  exit 2
}
jq -e '
    .all_direct_and_saved_statuses_passed == true and
    (.attempts.normal | .inner_status == 0 and .saved_outer_status == 0 and .source_pre_post_status == 0 and .toolchain_pre_post_status == 0 and .tool_envelope_status == 1) and
    (.attempts.race | .inner_status == 0 and .saved_outer_status == 0 and .source_pre_post_status == 0 and .toolchain_pre_post_status == 0 and .tool_envelope_status == 1) and
    .endpoint_execution_run == false and .traffic_run == false
  ' "$capacity_focused_checkpoint" >/dev/null || { echo "capacity focused-test checkpoint changed" >&2; exit 2; }
jq -e --arg server "$server_binary_sha256" --arg cwd "$script_dir/build/source" '
    .capacity_source_checkpoint_sha256 == "04da91fe74d9a7d13a8c8ae6e5c982ac2275709d950280c3c7776fc06fb22a7f" and
    .server_capacity_snapshot_patch_sha256 == "0392a5055b5c1282627d959216bed7bd898e6f48da46a0c79c46d457e94284de" and
    .source.pre_post_status == 0 and .module.pre_post_status == 0 and .toolchain.pre_post_status == 0 and
    .toolchain.path == "/home/user/go/pkg/mod/golang.org/toolchain@v0.0.1-go1.26.6.linux-amd64/bin/go" and
    .toolchain.sha256 == "29e6e0b8be61beb1489ceae62b304343566de8a1dc700af74bde7aeb9c80ad45" and
    .toolchain.version == "go version go1.26.6 linux/amd64" and .metadata.working_directory == $cwd and .metadata.gomod == ($cwd + "/go.mod") and
    .build.status == 0 and .build.saved_outer_status == 0 and .build.tool_envelope_status == 1 and
    .output.path == "build/bin/current-server-wan-read-wait-capacity-audited.test" and .output.sha256 == $server and
    .endpoint_execution_run == false and .traffic_run == false
  ' "$capacity_audited_build_manifest" >/dev/null || { echo "capacity audited build manifest changed" >&2; exit 2; }
[[ -x $scope_entrypoint && -x $scope_sentinel && -x $scope_validator && -x $scope_resource_validator ]] || {
  echo "scope helpers and validators must be executable" >&2
  exit 2
}
[[ $(sha256sum "$scope_probe_attestation" | awk '{print $1}') == c6822a22fb26467a28374addb9f222f3a021f47c133911659f95a4135677e35e ]] || {
  echo "accepted delegated-scope probe attestation changed" >&2
  exit 2
}
jq -e '.accepted == true and .forced_cleanup == false and .statuses == {dummy:0,outer:0,systemd_run:0} and .unit_joined == true' "$scope_probe_attestation" >/dev/null || {
  echo "delegated-scope prerequisite was not accepted" >&2
  exit 2
}
[[ -f $trace_analyzer && -f $trace_parser_wrapper && -x $pinned_go &&
   $(sha256sum "$trace_analyzer" | awk '{print $1}') == ebb8e1a48b10f966df026a475d6217c83f117ac70e7a9195d66aadc7e1bdf895 &&
   $(sha256sum "$trace_parser_wrapper" | awk '{print $1}') == 719eac5dac37a0dc686283025e7005aba3cbf5408436b6257e6fde8811a50df8 &&
   $(sha256sum "$trace_canonicalizer" | awk '{print $1}') == 4d1bbcdd2140d148741775842e1ba9d2d74f2541b15933a6232b18cf3473e793 &&
   $(sha256sum "$trace_format" | awk '{print $1}') == f370243b753a94a122157e9b0b13eb535065fe88746269bd70de5475d82e4dbf &&
   $(sha256sum "$trace_analyzer_hashes" | awk '{print $1}') == 59a25b09de27905c9a558cae9f64c370517e283b014edb2b562142eeb3a21563 &&
   $(sha256sum "$trace_analyzer_schema" | awk '{print $1}') == fc81d1afe93b8f805f98cae922f21032948b15a3985470f238545bb8663e4df7 &&
   $(sha256sum "$trace_analyzer_checkpoint" | awk '{print $1}') == 7a851aca80494e1b12d4e9dd4c94406f02445cf067e3b0c497dc679ab0fd2cbc &&
   $(sha256sum "$trace_focused_manifest" | awk '{print $1}') == e5c1832565bb77818a4c40e5e00faab66ac19ea0a556885cabccd3f8be1803d2 &&
   $(sha256sum "$trace_focused_summary" | awk '{print $1}') == 669fdeb61e44fb35bcda0c9b3f1e42d3097f4d42a36596bd6defd94f2d0b332f &&
   $(sha256sum "$trace_lifecycle_manifest" | awk '{print $1}') == 22a87032adbc4096fde3dc97db2de80a2cad6ae86994cbacb37aec6366915897 &&
   $(sha256sum "$trace_lifecycle_summary" | awk '{print $1}') == 37159578894bc8a4706d9bc191e7c4986a259b01973c527f174b56736d5f7cd6 &&
   $(sha256sum "$pinned_go" | awk '{print $1}') == 29e6e0b8be61beb1489ceae62b304343566de8a1dc700af74bde7aeb9c80ad45 ]] || {
  echo "pinned compact trace source, checkpoint, or parser toolchain changed" >&2
  exit 2
}
jq -e '.diagnostic_only == true and .traffic_run == false and .source_checkpoint == 8 and
  .fixed_bounds == {"canonical_bytes":536870912,"definition_bytes":67108864,"event_bytes":4194304,"events":5000000,"expanded_parsed_bytes":4294967296,"line_bytes":1048576,"raw_trace_bytes":67108864,"reducer_output_bytes":8388608,"stack_definitions":4096} and
  .sha256.analyzer == "ebb8e1a48b10f966df026a475d6217c83f117ac70e7a9195d66aadc7e1bdf895" and
  .sha256.adapter == "719eac5dac37a0dc686283025e7005aba3cbf5408436b6257e6fde8811a50df8" and
  .sha256.canonicalizer == "4d1bbcdd2140d148741775842e1ba9d2d74f2541b15933a6232b18cf3473e793" and
  .sha256.format == "f370243b753a94a122157e9b0b13eb535065fe88746269bd70de5475d82e4dbf" and
  .sha256.focused_summary == "669fdeb61e44fb35bcda0c9b3f1e42d3097f4d42a36596bd6defd94f2d0b332f" and
  .sha256.adapter_lifecycle_summary == "37159578894bc8a4706d9bc191e7c4986a259b01973c527f174b56736d5f7cd6"' \
  "$trace_analyzer_checkpoint" >/dev/null || { echo "compact checkpoint contract changed" >&2; exit 2; }
jq -e '.accepted == true and .case_count == 6 and .contract_count == 17 and
  ([.six[].reduction_equal] | all) and ([.six[].reused_verified_run] | all)' \
  "$trace_focused_summary" >/dev/null || { echo "compact focused verification was not accepted" >&2; exit 2; }
jq -e '.accepted == true and .case_count == 14' "$trace_lifecycle_summary" >/dev/null || {
  echo "compact adapter lifecycle verification was not accepted" >&2
  exit 2
}
[[ $(sha256sum "$read_wait_runner_patch" | awk '{print $1}') == 267a6421bea2ceec7634b5cf13b700e11c3a9da5a2d3c1aa14f6cc1fe0210e5c &&
   $(sha256sum "$server_capacity_patch" | awk '{print $1}') == 0392a5055b5c1282627d959216bed7bd898e6f48da46a0c79c46d457e94284de ]] || {
  echo "read-wait runner patch changed" >&2
  exit 2
}
[[ $(sha256sum "$decision_map" | awk '{print $1}') == f6dc409926ca8178ba0da8ec262960a6b8e238d2a9d94dbe0afc9077f629c6f7 ]] || {
  echo "copy timing decision map hash changed" >&2
  exit 2
}
actual_leaf_sha256=$(openssl x509 -in "$cert" -outform DER | sha256sum | awk '{print $1}')
[[ $actual_leaf_sha256 == "$expected_leaf_sha256" ]] || { echo "certificate DER fingerprint mismatch" >&2; exit 2; }

output_dir=$(realpath -m "$output_dir")
allowed_root=$(realpath -m "$integration_dir/runs")
[[ $output_dir == "$allowed_root"/* ]] || { echo "output must remain below $allowed_root" >&2; exit 2; }
[[ ! -e $output_dir ]] || { echo "output directory already exists: $output_dir" >&2; exit 2; }
mkdir -p "$output_dir/tmp"
export TMPDIR="$output_dir/tmp"
results="$output_dir/results.jsonl"
lifecycle="$output_dir/results.lifecycle.jsonl"
summary="$output_dir/results.summary.json"
outer="$output_dir/outer-namespaces.json"
supervisor="$output_dir/supervisor-lifecycle.json"
pre_hashes="$output_dir/pre-hashes.json"
post_hashes="$output_dir/post-hashes.json"
attestation="$output_dir/attestation.json"
scope_dir="$output_dir/server-scope"
mkdir -m 0700 "$scope_dir"
scope_nonce=$(cat /proc/sys/kernel/random/uuid)
scope_unit="p2p-readwait-${protocol}-${BASHPID}-${scope_nonce:0:8}.scope"
scope_sentinel_state="$scope_dir/sentinel.json"
scope_sentinel_release="$scope_dir/sentinel.release"
scope_preexec_state="$scope_dir/server-preexec.json"
scope_preexec_release="$scope_dir/server-preexec.release"
scope_app_ready="$scope_dir/server-app-ready.json"
scope_app_release="$scope_dir/server-app.release"

write_hashes() {
  local destination=$1
  python3 - "$destination" "$0" "$runner" "$validator" "$copy_record_validator" "$approved_runner" "$frozen_protocol_runner" "$frozen_copy_runner" \
    "$wan_protocol_runner_patch" "$copy_runner_patch" "$monotonic_runner_patch" "$read_wait_runner_patch" "$server_capacity_patch" "$cleanup_status_patch" "$go_patch" "$go_helper" "$copy_overlay" \
    "$monotonic_observer_patch" "$monotonic_validator_patch" "$monotonic_arm_validator_patch" "$copy_design" \
    "$monotonic_build_manifest" "$monotonic_audited_build_manifest" "$final_source_attestation" \
    "$runner_reconstruction_attestation" "$integration_notes" "$decision_map" "$observer_patch" "$observer_checkpoint" \
    "$focused_checkpoint" "$audited_build_manifest" "$capacity_source_checkpoint" "$capacity_source_checkpoint_manifest" \
    "$capacity_focused_checkpoint" "$capacity_audited_build_manifest" "$scope_entrypoint" "$scope_sentinel" "$scope_validator" "$scope_resource_validator" "$scope_probe_attestation" \
    "$trace_analyzer" "$trace_analyzer_hashes" "$trace_analyzer_schema" "$trace_analyzer_checkpoint" "$trace_parser_wrapper" "$trace_canonicalizer" "$trace_format" \
    "$trace_focused_manifest" "$trace_focused_summary" "$trace_lifecycle_manifest" "$trace_lifecycle_summary" "$pinned_go" \
    "$runner_integration_design" "$static_contract" "$resource_contract" "$traced_transfer_contract" "$arm_validator_contract" \
    "$cleanup_status_contract" "$cleanup_status_body" "$cleanup_status_holder" "$all_contracts" \
    "$server_binary" "$aggregate_agent_binary" "$cert" "$key" "$server_source_ref" \
    "$aggregate_agent_source_ref" "$expected_leaf_sha256" <<'PY'
import hashlib, json, pathlib, sys
(destination, supervisor, runner, validator, copy_record_validator, approved, frozen_protocol_runner, frozen_copy_runner,
 wan_protocol_runner_patch, copy_runner_patch, monotonic_runner_patch, read_wait_runner_patch, server_capacity_patch, cleanup_status_patch, go_patch, go_helper, copy_overlay,
 monotonic_observer_patch, monotonic_validator_patch, monotonic_arm_validator_patch, copy_design,
 monotonic_build_manifest, monotonic_audited_build_manifest, final_source_attestation,
 runner_reconstruction_attestation, integration_notes, decision_map, observer_patch, observer_checkpoint,
 focused_checkpoint, audited_build_manifest, capacity_source_checkpoint, capacity_source_checkpoint_manifest,
 capacity_focused_checkpoint, capacity_audited_build_manifest, scope_entrypoint, scope_sentinel, scope_validator, scope_resource_validator, scope_probe_attestation,
 trace_analyzer, trace_analyzer_hashes, trace_analyzer_schema, trace_analyzer_checkpoint, trace_parser_wrapper, trace_canonicalizer, trace_format,
 trace_focused_manifest, trace_focused_summary, trace_lifecycle_manifest, trace_lifecycle_summary, pinned_go,
 runner_integration_design, static_contract, resource_contract, traced_transfer_contract, arm_validator_contract,
 cleanup_status_contract, cleanup_status_body, cleanup_status_holder, all_contracts,
 server, agent, cert, key, server_ref, agent_ref, fingerprint) = sys.argv[1:]
def sha(path):
    digest = hashlib.sha256()
    with open(path, "rb") as source:
        for block in iter(lambda: source.read(1024 * 1024), b""):
            digest.update(block)
    return digest.hexdigest()
value = {
    "schema_version": 1,
    "server_source_ref": server_ref,
    "aggregate_agent_source_ref": agent_ref,
    "expected_leaf_der_sha256": fingerprint,
    "sha256": {
        "supervisor": sha(supervisor), "runner": sha(runner), "validator": sha(validator),
        "copy_record_validator": sha(copy_record_validator), "approved_lifecycle_runner": sha(approved),
        "frozen_protocol_runner": sha(frozen_protocol_runner), "frozen_copy_runner": sha(frozen_copy_runner),
        "wan_protocol_runner_patch": sha(wan_protocol_runner_patch), "copy_runner_patch": sha(copy_runner_patch),
        "monotonic_runner_patch": sha(monotonic_runner_patch), "read_wait_runner_patch": sha(read_wait_runner_patch),
        "server_capacity_snapshot_patch": sha(server_capacity_patch),
        "cleanup_status_patch": sha(cleanup_status_patch),
        "go_base_patch": sha(go_patch), "go_helper": sha(go_helper),
        "copy_attribution_overlay_patch": sha(copy_overlay), "copy_attribution_design": sha(copy_design),
        "monotonic_observer_patch": sha(monotonic_observer_patch), "monotonic_validator_patch": sha(monotonic_validator_patch),
        "monotonic_arm_validator_patch": sha(monotonic_arm_validator_patch),
        "monotonic_build_manifest": sha(monotonic_build_manifest),
        "monotonic_audited_build_manifest": sha(monotonic_audited_build_manifest),
        "final_source_attestation": sha(final_source_attestation), "integration_notes": sha(integration_notes),
        "runner_reconstruction_attestation": sha(runner_reconstruction_attestation),
        "copy_timing_decision_map": sha(decision_map),
        "read_wait_observer_patch": sha(observer_patch), "read_wait_observer_checkpoint": sha(observer_checkpoint),
        "read_wait_focused_checkpoint": sha(focused_checkpoint), "read_wait_audited_build_manifest": sha(audited_build_manifest),
        "capacity_source_checkpoint": sha(capacity_source_checkpoint), "capacity_source_checkpoint_manifest": sha(capacity_source_checkpoint_manifest),
        "capacity_focused_checkpoint": sha(capacity_focused_checkpoint), "capacity_audited_build_manifest": sha(capacity_audited_build_manifest),
        "server_scope_entrypoint": sha(scope_entrypoint), "server_scope_sentinel": sha(scope_sentinel),
        "server_scope_validator": sha(scope_validator), "server_scope_resource_validator": sha(scope_resource_validator),
        "delegated_scope_probe_attestation": sha(scope_probe_attestation),
        "trace_analyzer": sha(trace_analyzer), "trace_source_checkpoint_manifest": sha(trace_analyzer_hashes),
        "trace_analyzer_schema": sha(trace_analyzer_schema), "trace_compact_checkpoint": sha(trace_analyzer_checkpoint),
        "trace_parser_adapter": sha(trace_parser_wrapper), "trace_canonicalizer": sha(trace_canonicalizer),
        "trace_format": sha(trace_format), "trace_focused_manifest": sha(trace_focused_manifest),
        "trace_focused_summary": sha(trace_focused_summary), "trace_lifecycle_manifest": sha(trace_lifecycle_manifest),
        "trace_lifecycle_summary": sha(trace_lifecycle_summary), "pinned_go": sha(pinned_go),
        "runner_integration_design": sha(runner_integration_design), "static_contract": sha(static_contract),
        "resource_contract": sha(resource_contract), "traced_transfer_contract": sha(traced_transfer_contract),
        "arm_validator_contract": sha(arm_validator_contract), "cleanup_status_contract": sha(cleanup_status_contract),
        "cleanup_status_body": sha(cleanup_status_body), "cleanup_status_holder": sha(cleanup_status_holder),
        "all_contracts": sha(all_contracts),
        "server_binary": sha(server), "aggregate_agent_binary": sha(agent),
        "certificate_pem": sha(cert), "private_key_pem": sha(key),
    },
}
pathlib.Path(destination).write_text(json.dumps(value, indent=2, sort_keys=True) + "\n")
PY
}
write_hashes "$pre_hashes"

{
  uname -a
  taskset -pc $$
  lscpu
  stat -f -c 'tmpdir_filesystem=%T' "$TMPDIR"
  printf 'TMPDIR=%s\n' "$TMPDIR"
  env | LC_ALL=C sort | grep -E '^(GOFLAGS|P2PSTREAM_.*PROFILE|TMPDIR)=' || true
  printf 'self_cgroup='; tr '\n' ';' < /proc/self/cgroup; printf '\n'
  for name in cpu.max cpuset.cpus.effective memory.current memory.high memory.max memory.swap.max pids.current pids.max; do
    path=/sys/fs/cgroup/$name
    [[ -r $path ]] && printf '%s=%s\n' "$name" "$(<"$path")"
  done
  for path in /sys/class/thermal/thermal_zone*/temp /sys/class/hwmon/hwmon*/temp*_input; do
    [[ -r $path ]] && printf '%s=%s\n' "$path" "$(<"$path")"
  done
} > "$output_dir/host-settings.txt"

command=(
  "$runner"
  --server-binary "$server_binary"
  --aggregate-agent-binary "$aggregate_agent_binary"
  --output "$results"
  --runs 1
  --warmup-mib 256
  --steady-mib 256
  --transfer-repetitions 3
  --required-pool-mib 8
  --rtt-ms 80
  --server-source-ref "$server_source_ref"
  --aggregate-agent-source-ref "$aggregate_agent_source_ref"
  --protocol "$protocol"
  --cert "$cert"
  --key "$key"
  --expected-leaf-sha256 "$expected_leaf_sha256"
)
timeout_seconds=4800
if ((smoke)); then
  command+=(--smoke)
  timeout_seconds=1200
fi
printf '%q ' "${command[@]}" > "$output_dir/invocation.txt"
printf '\n' >> "$output_dir/invocation.txt"

runner_pid=""
scope_launcher_pid=""
scope_started=0
forced=0
cleanup() {
  local status=$? attempt runner_group_joined=false scope_state="" scope_query_status="" scope_stop_status="" \
    runner_wait_status="" runner_term_status="" runner_kill_status="" launcher_wait_status="" sentinel_release_status=0
  trap - EXIT INT TERM
  # Releasing either server barrier during failure cleanup could permit work to
  # begin. Release only the non-workload sentinel; terminate/join the owned
  # runner topology and then stop any surviving transient scope.
  set +e
  touch "$scope_sentinel_release" 2>/dev/null
  sentinel_release_status=$?
  set -e
  if [[ -n $runner_pid ]] && kill -0 -- "-$runner_pid" 2>/dev/null; then
    forced=1
    set +e
    kill -TERM -- "-$runner_pid" 2>/dev/null
    runner_term_status=$?
    set -e
    for ((attempt=0; attempt<100; attempt++)); do
      kill -0 -- "-$runner_pid" 2>/dev/null || { runner_group_joined=true; break; }
      sleep .05
    done
    if [[ $runner_group_joined != true ]]; then
      set +e
      kill -KILL -- "-$runner_pid" 2>/dev/null
      runner_kill_status=$?
      set -e
    fi
  elif [[ -n $runner_pid ]]; then
    runner_group_joined=true
  fi
  if [[ -n $runner_pid ]]; then
    set +e
    wait "$runner_pid" 2>/dev/null
    runner_wait_status=$?
    set -e
  fi
  if [[ -n $scope_launcher_pid ]]; then
    set +e
    wait "$scope_launcher_pid" 2>/dev/null
    launcher_wait_status=$?
    set -e
    [[ $launcher_wait_status == 0 ]] || forced=1
  fi
  if ((scope_started)); then
    set +e
    scope_state=$(systemctl --user show "$scope_unit" -p LoadState -p ActiveState -p SubState 2>&1)
    scope_query_status=$?
    set -e
    printf 'status=%s\n%s\n' "$scope_query_status" "$scope_state" > "$scope_dir/unit-cleanup-query.txt" 2>/dev/null || true
    if ((scope_query_status != 0)) \
       || [[ $(grep -c '^LoadState=' <<<"$scope_state") != 1 ]] \
       || [[ $(grep -c '^ActiveState=' <<<"$scope_state") != 1 ]] \
       || [[ $(grep -c '^SubState=' <<<"$scope_state") != 1 ]] \
       || [[ $(grep -Ec '^(LoadState|ActiveState|SubState)=' <<<"$scope_state") != 3 ]]; then
      forced=1
    fi
    if grep -q '^ActiveState=failed$' <<<"$scope_state"; then
      forced=1
    fi
    if grep -Eq '^ActiveState=(active|activating|deactivating)$' <<<"$scope_state"; then
      forced=1
      set +e
      systemctl --user stop "$scope_unit" >/dev/null 2>&1
      scope_stop_status=$?
      set -e
    fi
  fi
  python3 - "$output_dir/supervisor-cleanup.json" "$status" "$forced" "$sentinel_release_status" \
    "$runner_pid" "$runner_wait_status" "$runner_group_joined" "$runner_term_status" "$runner_kill_status" \
    "$scope_launcher_pid" "$launcher_wait_status" "$scope_query_status" "$scope_stop_status" "$scope_state" <<'PY'
import json, pathlib, sys
(out, entry, forced, sentinel_release, runner_pid, runner_wait, runner_joined, runner_term,
 runner_kill, launcher_pid, launcher_wait, unit_query, unit_stop, unit_show) = sys.argv[1:]
def number(value):
    return int(value) if value not in ("", "null") else None
value = {
    "schema_version": 1, "entry_status": int(entry), "forced_supervisor_cleanup": forced == "1",
    "invalidates_success": forced == "1" or int(entry) != 0, "sentinel_release_status": int(sentinel_release),
    "runner": {"pid": number(runner_pid), "wait_status": number(runner_wait),
               "group_joined": runner_joined == "true", "term_status": number(runner_term),
               "kill_status": number(runner_kill)},
    "scope_launcher": {"pid": number(launcher_pid), "wait_status": number(launcher_wait)},
    "unit": {"show_status": number(unit_query), "stop_status": number(unit_stop), "show": unit_show},
}
pathlib.Path(out).write_text(json.dumps(value, indent=2, sort_keys=True) + "\n")
PY
  if ((forced)); then ((status != 0)) || status=1; fi
  exit "$status"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

setsid systemd-run --user --scope --quiet --collect --unit="$scope_unit" \
  -p MemoryMax=4294967296 -p MemorySwapMax=0 -p Delegate=yes -- \
  "$scope_sentinel" "$scope_sentinel_state" "$scope_sentinel_release" \
  >"$scope_dir/systemd-run.stdout.log" 2>"$scope_dir/systemd-run.stderr.log" &
scope_launcher_pid=$!
scope_started=1
python3 - "$scope_dir/scope-launcher.json" "$scope_launcher_pid" <<'PY'
import json, os, pathlib, sys, time
out, pid_text = sys.argv[1:]
pid = int(pid_text)
deadline = time.monotonic() + 20
initial = None
while time.monotonic() < deadline:
    try:
        stat = pathlib.Path(f"/proc/{pid}/stat").read_text().split()
        if initial is None:
            initial = {"start_ticks": int(stat[21]), "initial_pgid": os.getpgid(pid), "initial_sid": os.getsid(pid)}
        elif int(stat[21]) != initial["start_ticks"]:
            raise SystemExit("scope launcher PID identity changed before setsid stabilized")
        pgid, sid = os.getpgid(pid), os.getsid(pid)
        if pgid == pid and sid == pid:
            value = {"pid": pid, "pgid": pgid, "sid": sid, **initial,
                     "setsid_identity_stable": True}
            break
    except (FileNotFoundError, ProcessLookupError):
        pass
    time.sleep(0.01)
else:
    raise SystemExit("scope launcher did not establish its own process group and session")
pathlib.Path(out).write_text(json.dumps(value, indent=2, sort_keys=True) + "\n")
PY
for _ in $(seq 1 400); do
  [[ -s $scope_sentinel_state ]] && break
  kill -0 "$scope_launcher_pid" 2>/dev/null || break
  sleep .05
done
[[ -s $scope_sentinel_state ]] || { echo "delegated server scope did not become ready" >&2; exit 1; }
set +e
systemctl --user show "$scope_unit" \
  -p Id -p LoadState -p ActiveState -p SubState -p ControlGroup -p Delegate \
  -p MemoryCurrent -p MemoryPeak -p MemoryHigh -p MemoryMax -p EffectiveMemoryMax -p MemorySwapMax \
  > "$scope_dir/unit-with-sentinel.txt"
unit_with_sentinel_status=$?
scope_cgroup=$(systemctl --user show "$scope_unit" -p ControlGroup --value)
unit_control_group_status=$?
set -e
printf '%s\n' "$unit_with_sentinel_status" > "$scope_dir/unit-with-sentinel.status"
printf '%s\n' "$unit_control_group_status" > "$scope_dir/unit-control-group.status"
((unit_with_sentinel_status == 0 && unit_control_group_status == 0)) || {
  echo "delegated server scope property query failed" >&2
  exit 1
}
python3 - "$scope_dir/unit-with-sentinel.txt" "$scope_dir/unit-policy.json" <<'PY'
import json, pathlib, sys
source, out = map(pathlib.Path, sys.argv[1:])
properties = dict(line.split("=", 1) for line in source.read_text().splitlines() if "=" in line)
checks = {
    "load_state_loaded": properties.get("LoadState") == "loaded",
    "active_state_active": properties.get("ActiveState") == "active",
    "delegate_enabled": properties.get("Delegate") == "yes",
    "memory_max_4gib": properties.get("MemoryMax") == "4294967296",
    "effective_memory_max_4gib": properties.get("EffectiveMemoryMax") == "4294967296",
    "memory_swap_max_zero": properties.get("MemorySwapMax") == "0",
}
value = {"schema_version": 1, "accepted": all(checks.values()), "checks": checks, "properties": properties}
out.write_text(json.dumps(value, indent=2, sort_keys=True) + "\n")
raise SystemExit(0 if value["accepted"] else 1)
PY
[[ $scope_cgroup == /user.slice/user-*.slice/user@*.service/app.slice/*.scope ]] || {
  echo "delegated server scope path is outside the owned user service" >&2
  exit 1
}
scope_cgroup_dir=/sys/fs/cgroup$scope_cgroup
scope_cgroup_procs=$scope_cgroup_dir/cgroup.procs
[[ -d $scope_cgroup_dir && -w $scope_cgroup_procs ]] || { echo "delegated server scope is not writable" >&2; exit 1; }
python3 - "$scope_dir/cgroup-delegation-paths.json" "$scope_cgroup_dir" "$scope_cgroup_procs" <<'PY'
import json, os, pathlib, stat, sys
out, destination_text, procs_text = sys.argv[1:]
destination = pathlib.Path(destination_text).resolve()
procs = pathlib.Path(procs_text).resolve()
own_rel = next(line.split("::", 1)[1] for line in pathlib.Path("/proc/self/cgroup").read_text().splitlines() if "::" in line)
source = pathlib.Path("/sys/fs/cgroup" + own_rel).resolve()
common = pathlib.Path(os.path.commonpath((str(source), str(destination))))
def metadata(path):
    value = path.stat()
    return {"path": str(path), "uid": value.st_uid, "gid": value.st_gid,
            "mode": stat.S_IMODE(value.st_mode), "writable": os.access(path, os.W_OK)}
value = {"schema_version": 1, "source_cgroup": str(source), "destination_cgroup": str(destination),
         "nearest_common_ancestor": str(common), "source": metadata(source), "destination": metadata(destination),
         "destination_cgroup_procs": metadata(procs), "common_ancestor": metadata(common),
         "common_ancestor_cgroup_procs": metadata(common / "cgroup.procs")}
pathlib.Path(out).write_text(json.dumps(value, indent=2, sort_keys=True) + "\n")
PY
"$scope_validator" snapshot --cgroup-dir "$scope_cgroup_dir" --output "$scope_dir/cgroup-with-sentinel.json"
expected_server_cpus=$(python3 - <<'PY'
import os
print(",".join(str(cpu) for cpu in sorted(os.sched_getaffinity(0))[:4]))
PY
)
cat > "$output_dir/server-scope-invocation.txt" <<EOF
unit=$scope_unit
memory_max_bytes=4294967296
memory_swap_max_bytes=0
delegate=yes
scope_cgroup=$scope_cgroup
scope_nonce=$scope_nonce
expected_server_cpus=$expected_server_cpus
EOF

parent_user_ns=$(readlink /proc/self/ns/user)
parent_pid_ns=$(readlink /proc/self/ns/pid)
parent_mount_ns=$(readlink /proc/self/ns/mnt)
parent_net_ns=$(readlink /proc/self/ns/net)
parent_cgroup_ns=$(readlink /proc/self/ns/cgroup)
P2PSTREAM_GROWTH_SERVER_SCOPE_PROCS="$scope_cgroup_procs" \
P2PSTREAM_GROWTH_SERVER_SCOPE_CGROUP="$scope_cgroup" \
P2PSTREAM_GROWTH_SERVER_SCOPE_CONTROL_DIR="$scope_dir" \
P2PSTREAM_GROWTH_SERVER_SCOPE_NONCE="$scope_nonce" \
P2PSTREAM_GROWTH_SERVER_SCOPE_ENTRYPOINT="$scope_entrypoint" \
setsid timeout --signal=TERM --kill-after=10s "${timeout_seconds}s" \
  unshare --user --map-root-user --pid --fork --kill-child=TERM --mount --mount-proc --net \
  bash -c '
    out=$1 parent_user=$2 parent_pid=$3 parent_mount=$4 parent_net=$5 parent_cgroup=$6
    shift 6
    python3 - "$out" "$parent_user" "$parent_pid" "$parent_mount" "$parent_net" "$parent_cgroup" "$$" <<'"'"'PY'"'"'
import json, os, pathlib, sys
out, parent_user, parent_pid, parent_mount, parent_net, parent_cgroup, shell_pid = sys.argv[1:]
current = {name: os.readlink(f"/proc/self/ns/{name}") for name in ("user", "pid", "mnt", "net", "cgroup")}
nspid = next(line.split()[1:] for line in pathlib.Path("/proc/self/status").read_text().splitlines() if line.startswith("NSpid:"))
payload = {
    "schema_version": 1,
    "boot_id": pathlib.Path("/proc/sys/kernel/random/boot_id").read_text().strip(),
    "parent": {"user": parent_user, "pid": parent_pid, "mnt": parent_mount, "net": parent_net, "cgroup": parent_cgroup},
    "owned": current,
    "shell_pid": int(shell_pid),
    "shell_pid_is_one": int(shell_pid) == 1,
    "python_parent_pid": os.getppid(),
    "python_parent_is_one": os.getppid() == 1,
    "nspid": [int(value) for value in nspid],
    "all_private": all(current[name] != value for name, value in (("user", parent_user), ("pid", parent_pid), ("mnt", parent_mount), ("net", parent_net))),
    "cgroup_namespace_unchanged": current["cgroup"] == parent_cgroup,
}
pathlib.Path(out).write_text(json.dumps(payload, indent=2, sort_keys=True) + "\n")
PY
    exec "$@"
  ' bash "$outer" "$parent_user_ns" "$parent_pid_ns" "$parent_mount_ns" "$parent_net_ns" "$parent_cgroup_ns" "${command[@]}" \
  > "$output_dir/runner.stdout.log" 2> "$output_dir/runner.stderr.log" &
runner_pid=$!

for _ in $(seq 1 800); do
  [[ -s $scope_preexec_state && -s $scope_dir/runner-owned-namespaces.json && -s $outer ]] && break
  kill -0 "$runner_pid" 2>/dev/null || break
  sleep .05
done
[[ -s $scope_preexec_state && -s $scope_dir/runner-owned-namespaces.json && -s $outer ]] || {
  echo "server did not reach the delegated-scope pre-exec barrier" >&2
  exit 1
}
touch "$scope_sentinel_release"
set +e
wait "$scope_launcher_pid"
scope_launcher_status=$?
set -e
printf '%s\n' "$scope_launcher_status" > "$scope_dir/systemd-run.status"
scope_launcher_pid=""
[[ $scope_launcher_status == 0 && -e $scope_sentinel_state.natural-exit ]] || {
  echo "server-scope sentinel or systemd launcher did not exit naturally" >&2
  exit 1
}
python3 - "$scope_dir/scope-process-join.json" "$scope_dir/scope-launcher.json" "$scope_sentinel_state" <<'PY'
import json, os, pathlib, sys
out, launcher_path, sentinel_path = sys.argv[1:]
launcher, sentinel = json.load(open(launcher_path)), json.load(open(sentinel_path))
def alive(identity):
    path = pathlib.Path(f"/proc/{identity['pid']}/stat")
    try:
        return path.exists() and int(path.read_text().split()[21]) == identity["start_ticks"]
    except (FileNotFoundError, ProcessLookupError):
        return False
pgids = {int(value) for value in os.popen("ps -e -o pgid=").read().split()}
value = {
    "launcher_alive_same_identity": alive(launcher), "sentinel_alive_same_identity": alive(sentinel),
    "launcher_group_joined": launcher["pgid"] not in pgids, "sentinel_group_joined": sentinel["pgid"] not in pgids,
}
pathlib.Path(out).write_text(json.dumps(value, indent=2, sort_keys=True) + "\n")
raise SystemExit(0 if not value["launcher_alive_same_identity"] and not value["sentinel_alive_same_identity"]
                 and value["launcher_group_joined"] and value["sentinel_group_joined"] else 1)
PY
systemctl --user show "$scope_unit" -p LoadState -p ActiveState -p SubState -p ControlGroup -p Delegate \
  > "$scope_dir/unit-server-preexec.txt"
"$scope_validator" bind --phase preexec --cgroup-dir "$scope_cgroup_dir" --state "$scope_preexec_state" \
  --runner-namespaces "$scope_dir/runner-owned-namespaces.json" --outer-namespaces "$outer" \
  --preexec-state "$scope_preexec_state" --nonce "$scope_nonce" --expected-cgroup "$scope_cgroup" \
  --server-binary "$server_binary" --runner "$runner" --entrypoint "$scope_entrypoint" \
  --expected-cpus "$expected_server_cpus" --output "$scope_dir/server-preexec-binding.json"
touch "$scope_preexec_release"

for _ in $(seq 1 800); do
  [[ -s $scope_app_ready ]] && break
  kill -0 "$runner_pid" 2>/dev/null || break
  sleep .05
done
[[ -s $scope_app_ready ]] || { echo "server did not reach the pre-traffic application barrier" >&2; exit 1; }
"$scope_validator" bind --phase application --cgroup-dir "$scope_cgroup_dir" --state "$scope_app_ready" \
  --runner-namespaces "$scope_dir/runner-owned-namespaces.json" --outer-namespaces "$outer" \
  --preexec-state "$scope_preexec_state" --nonce "$scope_nonce" --expected-cgroup "$scope_cgroup" \
  --server-binary "$server_binary" --runner "$runner" --entrypoint "$scope_entrypoint" \
  --expected-cpus "$expected_server_cpus" --output "$scope_dir/server-application-binding.json"
"$scope_validator" snapshot --cgroup-dir "$scope_cgroup_dir" --output "$scope_dir/server-cgroup-pre-traffic.json"
touch "$scope_app_release"

set +e
wait "$runner_pid"
runner_status=$?
set -e
printf '%s\n' "$runner_status" > "$output_dir/runner.status"

trace_reduction_dir="$output_dir/read-wait-reductions"
mkdir -m 0700 "$trace_reduction_dir"
if ((smoke)); then trace_sequences=(9 11 13); else trace_sequences=(9 11 14 15); fi
trace_adapter_all_status=0
trace_parser_all_status=0
trace_canonicalizer_all_status=0
trace_analyzer_all_status=0
for trace_sequence in "${trace_sequences[@]}"; do
  raw_trace="${results%.jsonl}.read-wait-traces/runtime-trace-sequence-${trace_sequence}.out"
  canonical_trace="$trace_reduction_dir/sequence-${trace_sequence}.intern"
  ledger="$trace_reduction_dir/sequence-${trace_sequence}.ledger.json"
  parser_status_file="$trace_reduction_dir/sequence-${trace_sequence}.parser.status"
  canonicalizer_status_file="$trace_reduction_dir/sequence-${trace_sequence}.canonicalizer.status"
  adapter_status_file="$trace_reduction_dir/sequence-${trace_sequence}.adapter.status"
  process_record="$trace_reduction_dir/sequence-${trace_sequence}.process.json"
  reduction="$trace_reduction_dir/sequence-${trace_sequence}.reduction.json"
  printf '%q ' /usr/bin/python3.14 "$trace_parser_wrapper" \
    --raw-trace "$raw_trace" --working-directory "$trace_reduction_dir" \
    --canonical-output "$canonical_trace" --ledger "$ledger" \
    --parser-status "$parser_status_file" --canonicalizer-status "$canonicalizer_status_file" \
    --parser-stderr "$trace_reduction_dir/sequence-${trace_sequence}.parser.stderr.log" \
    --canonicalizer-stderr "$trace_reduction_dir/sequence-${trace_sequence}.canonicalizer.stderr.log" \
    --process-record "$process_record" > "$trace_reduction_dir/sequence-${trace_sequence}.adapter.invocation.txt"
  printf '\n' >> "$trace_reduction_dir/sequence-${trace_sequence}.adapter.invocation.txt"
  set +e
  /usr/bin/python3.14 "$trace_parser_wrapper" \
    --raw-trace "$raw_trace" --working-directory "$trace_reduction_dir" \
    --canonical-output "$canonical_trace" --ledger "$ledger" \
    --parser-status "$parser_status_file" --canonicalizer-status "$canonicalizer_status_file" \
    --parser-stderr "$trace_reduction_dir/sequence-${trace_sequence}.parser.stderr.log" \
    --canonicalizer-stderr "$trace_reduction_dir/sequence-${trace_sequence}.canonicalizer.stderr.log" \
    --process-record "$process_record" \
    > "$trace_reduction_dir/sequence-${trace_sequence}.adapter.stdout.log" \
    2> "$trace_reduction_dir/sequence-${trace_sequence}.adapter.stderr.log"
  adapter_status=$?
  set -e
  printf '%s\n' "$adapter_status" > "$adapter_status_file"
  parser_status=127
  canonicalizer_status=127
  [[ -f $parser_status_file ]] && parser_status=$(<"$parser_status_file")
  [[ -f $canonicalizer_status_file ]] && canonicalizer_status=$(<"$canonicalizer_status_file")
  ((adapter_status == 0)) || trace_adapter_all_status=1
  [[ $parser_status == 0 ]] || trace_parser_all_status=1
  [[ $canonicalizer_status == 0 ]] || trace_canonicalizer_all_status=1
  printf '%q ' /usr/bin/python3.14 "$trace_analyzer" --input "$canonical_trace" \
    --tool-status "$parser_status_file" --canonicalizer-status "$canonicalizer_status_file" \
    --ledger "$ledger" --output "$reduction" \
    > "$trace_reduction_dir/sequence-${trace_sequence}.analyzer.invocation.txt"
  printf '\n' >> "$trace_reduction_dir/sequence-${trace_sequence}.analyzer.invocation.txt"
  set +e
  /usr/bin/python3.14 "$trace_analyzer" --input "$canonical_trace" \
    --tool-status "$parser_status_file" --canonicalizer-status "$canonicalizer_status_file" \
    --ledger "$ledger" --output "$reduction" \
    > "$trace_reduction_dir/sequence-${trace_sequence}.analyzer.stdout.log" \
    2> "$trace_reduction_dir/sequence-${trace_sequence}.analyzer.stderr.log"
  analyzer_status=$?
  set -e
  printf '%s\n' "$analyzer_status" > "$trace_reduction_dir/sequence-${trace_sequence}.analyzer.status"
  ((analyzer_status == 0)) || trace_analyzer_all_status=1
done
printf '%s\n' "$trace_adapter_all_status" > "$trace_reduction_dir/adapter-all.status"
printf '%s\n' "$trace_parser_all_status" > "$trace_reduction_dir/parser-all.status"
printf '%s\n' "$trace_canonicalizer_all_status" > "$trace_reduction_dir/canonicalizer-all.status"
printf '%s\n' "$trace_analyzer_all_status" > "$trace_reduction_dir/analyzer-all.status"
python3 - "$trace_reduction_dir/index.json" "$results" "$protocol" "$server_source_ref" \
  "$server_binary_sha256" "$runner_sha256" "$expected_leaf_sha256" "${trace_sequences[@]}" <<'PY'
import hashlib, json, pathlib, sys
out, results_path, protocol, source_ref, server_sha, runner_sha, cert_sha, *sequences = sys.argv[1:]
root = pathlib.Path(out).parent
rows = [json.loads(line) for line in pathlib.Path(results_path).read_text().splitlines() if line.strip()]
entries = []
for text in sequences:
    sequence = int(text)
    row = rows[sequence - 1] if len(rows) >= sequence else {}
    raw = pathlib.Path(str(results_path).removesuffix(".jsonl") + f".read-wait-traces/runtime-trace-sequence-{sequence}.out")
    canonical = root / f"sequence-{sequence}.intern"
    ledger = root / f"sequence-{sequence}.ledger.json"
    parser_status = root / f"sequence-{sequence}.parser.status"
    canonicalizer_status = root / f"sequence-{sequence}.canonicalizer.status"
    adapter_status = root / f"sequence-{sequence}.adapter.status"
    analyzer_status = root / f"sequence-{sequence}.analyzer.status"
    process_record = root / f"sequence-{sequence}.process.json"
    reduction = root / f"sequence-{sequence}.reduction.json"
    def digest(path):
        if not path.is_file(): return None
        value = hashlib.sha256()
        with path.open("rb") as source:
            for block in iter(lambda: source.read(1024 * 1024), b""): value.update(block)
        return value.hexdigest()
    tls = row.get("wan_response_tls", [])
    origin = tls[0].get("origin_connection_id") if len(tls) == 1 and isinstance(tls[0], dict) else None
    entries.append({
        "sequence": sequence, "raw_path": str(raw), "raw_sha256": digest(raw),
        "canonical_path": str(canonical), "canonical_sha256": digest(canonical),
        "ledger_path": str(ledger), "ledger_sha256": digest(ledger),
        "parser_status_path": str(parser_status), "parser_status_sha256": digest(parser_status),
        "canonicalizer_status_path": str(canonicalizer_status), "canonicalizer_status_sha256": digest(canonicalizer_status),
        "adapter_status_path": str(adapter_status), "adapter_status_sha256": digest(adapter_status),
        "analyzer_status_path": str(analyzer_status), "analyzer_status_sha256": digest(analyzer_status),
        "process_record_path": str(process_record), "process_record_sha256": digest(process_record),
        "reduction_path": str(reduction), "reduction_sha256": digest(reduction),
        "server_binary_sha256": server_sha, "server_source_ref": source_ref,
        "runner_sha256": runner_sha, "public_protocol": protocol,
        "certificate_der_sha256": cert_sha, "origin_connection_id": origin,
    })
pathlib.Path(out).write_text(json.dumps({"schema_version": 2, "diagnostic_only": True,
    "input_representation": "p2pstream-parsed-stack-intern-v1",
    "duration_relation_to_copy_observer": "descriptive-only", "entries": entries}, indent=2, sort_keys=True) + "\n")
PY

scope_resource_status=0
if [[ -s $scope_dir/server-cgroup-pre-traffic.json && -s $scope_dir/server-cgroup-post-workload.json ]]; then
  set +e
  "$scope_resource_validator" --pre "$scope_dir/server-cgroup-pre-traffic.json" \
    --post "$scope_dir/server-cgroup-post-workload.json" --expected-absolute-cgroup "$scope_cgroup_dir" \
    --expected-unified-cgroup "$scope_cgroup" --output "$scope_dir/server-resource-attestation.json"
  scope_resource_status=$?
  set -e
else
  scope_resource_status=1
  printf '{"schema_version":1,"accepted":false,"error":"missing pre-traffic or post-workload cgroup snapshot"}\n' \
    > "$scope_dir/server-resource-attestation.json"
fi
printf '%s\n' "$scope_resource_status" > "$scope_dir/server-resource.status"

scope_unit_joined=0
scope_unit_final_query_status=1
scope_unit_final_load_state=""
scope_unit_final_active_state=""
scope_unit_final_sub_state=""
scope_unit_final_show_status=1
scope_unit_final_confirmation=""
: > "$scope_dir/unit-final-polls.txt"
for _ in $(seq 1 200); do
  set +e
  unit_show=$(systemctl --user show "$scope_unit" -p LoadState -p ActiveState -p SubState 2>&1)
  unit_show_status=$?
  set -e
  printf 'status=%s %s\n' "$unit_show_status" "${unit_show//$'\n'/ }" >> "$scope_dir/unit-final-polls.txt"
  scope_unit_final_show_status=$unit_show_status
  if ((unit_show_status == 0)) && [[ $(grep -c '^LoadState=' <<<"$unit_show") == 1 ]] \
     && [[ $(grep -c '^ActiveState=' <<<"$unit_show") == 1 ]] \
     && [[ $(grep -c '^SubState=' <<<"$unit_show") == 1 ]] \
     && [[ $(grep -Ec '^(LoadState|ActiveState|SubState)=' <<<"$unit_show") == 3 ]]; then
    scope_unit_final_query_status=0
    scope_unit_final_load_state=$(sed -n 's/^LoadState=//p' <<<"$unit_show")
    scope_unit_final_active_state=$(sed -n 's/^ActiveState=//p' <<<"$unit_show")
    scope_unit_final_sub_state=$(sed -n 's/^SubState=//p' <<<"$unit_show")
    if [[ $scope_unit_final_load_state == not-found && $scope_unit_final_active_state == inactive && $scope_unit_final_sub_state == dead ]] || \
       [[ $scope_unit_final_load_state == loaded && $scope_unit_final_active_state == inactive && $scope_unit_final_sub_state == dead ]]; then
      scope_unit_joined=1
      scope_unit_final_confirmation=show
      break
    fi
  fi
  sleep .05
done
printf '%s\n' "$scope_unit_joined" > "$scope_dir/unit.joined"
python3 - "$scope_dir/unit-final.json" "$scope_unit_final_query_status" "$scope_unit_joined" \
  "$scope_unit_final_load_state" "$scope_unit_final_active_state" "$scope_unit_final_sub_state" \
  "$scope_unit_final_show_status" "$scope_unit_final_confirmation" <<'PY'
import json, pathlib, sys
out, query, joined, load, active, sub, show_status, confirmation = sys.argv[1:]
value = {"schema_version": 1, "query_status": int(query), "joined": joined == "1",
         "load_state": load, "active_state": active, "sub_state": sub,
         "show_status": int(show_status), "confirmation": confirmation,
         "failed_state_rejected": active != "failed"}
pathlib.Path(out).write_text(json.dumps(value, indent=2, sort_keys=True) + "\n")
PY

group_joined=false
for ((attempt=0; attempt<100; attempt++)); do
  kill -0 -- "-$runner_pid" 2>/dev/null || { group_joined=true; break; }
  sleep .05
done
if [[ $group_joined == false ]]; then
  forced=1
  kill -TERM -- "-$runner_pid" 2>/dev/null || true
  sleep .1
  kill -KILL -- "-$runner_pid" 2>/dev/null || true
fi
invalidates_success=false
if ((runner_status != 0 || forced != 0 || scope_launcher_status != 0 || scope_resource_status != 0 || \
     scope_unit_joined != 1 || scope_unit_final_query_status != 0 || trace_adapter_all_status != 0 || \
     trace_parser_all_status != 0 || trace_canonicalizer_all_status != 0 || trace_analyzer_all_status != 0)); then
  invalidates_success=true
fi
printf '{"runner_status":%s,"group_joined":%s,"forced":%s,"invalidates_success":%s,"server_scope":{"systemd_run_status":%s,"resource_status":%s,"unit_joined":%s,"unit_query_status":%s,"unit_load_state":"%s","unit_active_state":"%s","unit_sub_state":"%s"},"trace_reduction":{"adapter_status":%s,"parser_status":%s,"canonicalizer_status":%s,"analyzer_status":%s}}\n' \
  "$runner_status" "$group_joined" "$([[ $forced == 0 ]] && printf false || printf true)" \
  "$invalidates_success" "$scope_launcher_status" "$scope_resource_status" \
  "$([[ $scope_unit_joined == 1 ]] && printf true || printf false)" "$scope_unit_final_query_status" \
  "$scope_unit_final_load_state" "$scope_unit_final_active_state" "$scope_unit_final_sub_state" \
  "$trace_adapter_all_status" "$trace_parser_all_status" "$trace_canonicalizer_all_status" "$trace_analyzer_all_status" > "$supervisor"
runner_pid=""
write_hashes "$post_hashes"

set +e
"$validator" "$results" "$lifecycle" "$summary" "$outer" "$supervisor" "$pre_hashes" "$post_hashes" \
  "$attestation" "$protocol" "$smoke" > "$output_dir/validation.stdout.log" 2> "$output_dir/validation.stderr.log"
validation_status=$?
set -e
printf '%s\n' "$validation_status" > "$output_dir/validation.status"
((runner_status == 0 && forced == 0 && scope_launcher_status == 0 && scope_resource_status == 0 && scope_unit_joined == 1 && scope_unit_final_query_status == 0 && trace_adapter_all_status == 0 && trace_parser_all_status == 0 && trace_canonicalizer_all_status == 0 && trace_analyzer_all_status == 0 && validation_status == 0))
