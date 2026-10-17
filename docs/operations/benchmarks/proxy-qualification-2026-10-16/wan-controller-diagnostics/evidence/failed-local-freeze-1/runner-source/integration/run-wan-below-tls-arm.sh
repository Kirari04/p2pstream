#!/usr/bin/env bash
set -Eeuo pipefail
# SOURCE-ONLY: reviewed derivative binary/build tuple does not exist yet.
expected_terminal_build_receipt_sha256=$(cat "$(dirname "$0")/../../REVIEWED-RECEIPT-SHA256") # BLOCKED_PENDING_REVIEWED_CONTROLLER_BUILD_RECEIPT
[[ $expected_terminal_build_receipt_sha256 =~ ^[0-9a-f]{64}$ ]] || {
  echo "prospective terminal build/source receipt is not reviewed; endpoint execution blocked" >&2
  exit 125
}

integration_dir=$(cd "$(dirname "$0")" && pwd)
combined_root=/home/user/.codex/worktrees/astra-proxy-performance/p2pstream/tmp/continuation-20261016/wan-tcp-threshold-history-source/integration-source-2
continuation_root=/home/user/.codex/worktrees/astra-proxy-performance/p2pstream/tmp/continuation-20261016
byte_credit_root=$(realpath "$continuation_root/wan-byte-credit-integration-source")
tcp_state_root=$(realpath "$continuation_root/wan-tcp-state-source")
history_root=$(realpath "$continuation_root/wan-tcp-threshold-history-source")
raw_source_root=$(realpath "$continuation_root/wan-below-tls-raw-write-source/implementation-source")
capacity_root=$(realpath "$continuation_root/wan-read-wait-capacity-source")
mature_sender_root=$(realpath "$continuation_root/wan-sender-wait-integration-source")
script_dir=$capacity_root
runner="$integration_dir/generated/qualify-proxy-agent-growth-wan-below-tls.sh"
validator="$integration_dir/validate-wan-below-tls-arm.py"
arm_outcome_validator="$integration_dir/validate-arm-outcome.py"
sender_snapshot_validator="$integration_dir/validate-sender-wait-snapshot.py"
raw_write_validator="$integration_dir/validate-raw-write-activity.py"
server_growth_validator="$integration_dir/validate-server-receive-growth.py"
raw_closure_validator="$integration_dir/validate-post-join-raw-closure.py"
byte_credit_validator="$integration_dir/validate-byte-credit-recycle.py"
byte_credit_adapter_checkpoint="$byte_credit_root/implementation-source/review/source-checkpoint-11/SHA256SUMS.txt"
byte_credit_adapter_contract_ledger="$byte_credit_root/runs/focused-contract-attempt-2/SHA256SUMS.txt"
byte_credit_protocol_checkpoint="$raw_source_root/byte-credit-recycle/review/source-checkpoint-5/SHA256SUMS.txt"
byte_credit_protocol_focused_ledger="$raw_source_root/byte-credit-recycle/runs/focused-attempt-2/records/SHA256SUMS.txt"
byte_credit_protocol_focused_outer="$raw_source_root/byte-credit-recycle/provenance/focused-attempt-2-worker-tool-envelope/SHA256SUMS.txt"
byte_credit_runner_contract="$combined_root/contracts/test-byte-credit-runner-source.py"
tcp_state_validator="$integration_dir/validate-tcp-state.py"
tcp_producer_checkpoint="$tcp_state_root/implementation-source/integration/review/source-checkpoint-6/SHA256SUMS.txt"
tcp_source_map="$tcp_state_root/implementation-source/integration/review/source-checkpoint-6/SOURCE-MAP.json"
tcp_build_records_ledger="$tcp_state_root/build-source-1/runs/audited-build-attempt-2/records/SHA256SUMS.txt"
tcp_build_outer_ledger="$tcp_state_root/build-source-1/provenance/audited-build-attempt-2-tool-envelope/SHA256SUMS.txt"
tcp_validator_contract_ledger="$tcp_state_root/runner-source-1/runs/tcp-validator-contract-attempt-1/records/SHA256SUMS.txt"
tcp_validator_contract_outer="$tcp_state_root/runner-source-1/provenance/tcp-validator-contract-attempt-1-outer/SHA256SUMS.txt"
history_producer_checkpoint="$history_root/implementation-source/review/source-checkpoint-8/SHA256SUMS.txt"
history_source_map="$history_root/implementation-source/review/source-checkpoint-8/SOURCE-MAP.json"
history_build_records_ledger="$history_root/build-source-1/runs/audited-build-attempt-3/records/SHA256SUMS.txt"
history_build_outer_ledger="$history_root/build-source-1/provenance/audited-build-attempt-3-native-envelope/SHA256SUMS.txt"
history_validator_checkpoint="$history_root/implementation-source/validator/review/source-checkpoint-2/SHA256SUMS.txt"
history_validator_contract_ledger="$history_root/runner-source-1/runs/history-validator-contract-attempt-1/records/SHA256SUMS.txt"
history_validator_contract_outer="$history_root/runner-source-1/provenance/history-validator-contract-attempt-1-outer/SHA256SUMS.txt"
sender_observer_checkpoint="$script_dir/../wan-sender-wait-source/review/source-checkpoint-9/SHA256SUMS.txt"
sender_observer_patch="$script_dir/../wan-sender-wait-source/review/source-checkpoint-9/SOURCE-PATCH.diff"
sender_integration_checkpoint="$mature_sender_root/review/implementation-source-checkpoint-10/SHA256SUMS.txt"
reviewed_integration_manifest="$mature_sender_root/provenance/reviewed-integration-source-manifest-cp10.json"
reviewed_integration_verifier="$integration_dir/verify-reviewed-integration-manifest.py"
sender_build_manifest="$mature_sender_root/provenance/audited-server-build-manifest.json"
sender_harness_receipt="$mature_sender_root/provenance/sender-validator-manifest-correction-receipt.json"
base_cp7_checkpoint="$mature_sender_root/review/implementation-source-checkpoint-7/SHA256SUMS.txt"
base_cp7_reviewed_manifest="$mature_sender_root/provenance/reviewed-integration-source-manifest.json"
base_cp7_runner="$mature_sender_root/review/implementation-source-checkpoint-7/integration-source/generated/qualify-proxy-agent-growth-wan-sender-wait.sh"
historical_cp10_runner="$mature_sender_root/review/implementation-source-checkpoint-10/integration-source/generated/qualify-proxy-agent-growth-wan-sender-wait.sh"
sender_runner_patch="$mature_sender_root/SENDER-WAIT-RUNNER.patch"
sender_control_patch="$mature_sender_root/GO-CONTROL-PATCH.diff"
copy_record_validator="$integration_dir/validate-copy-record.py"
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
runner_integration_design="$mature_sender_root/DESIGN-PROPOSAL.md"
static_contract="$script_dir/contracts/run-static-integration-contracts.sh"
resource_contract="$script_dir/contracts/run-server-resource-contracts.py"
sender_wrapper_contract="$integration_dir/contracts/test-query-dispatch.sh"
arm_validator_contract="$integration_dir/contracts/test-sender-arm-bindings.py"
cleanup_status_contract="$script_dir/contracts/run-cleanup-status-contract.sh"
cleanup_status_body="$script_dir/contracts/PROPOSED-CLEANUP.sh"
cleanup_status_holder="$script_dir/contracts/owned-group-holder.py"
all_contracts="$script_dir/contracts/run-all-no-traffic-contracts.sh"
combined_checkpoint_manifest="$raw_source_root/server-growth/review/combined-integration-source-checkpoint-5/SHA256SUMS.txt"
combined_contract_ledger="$raw_source_root/server-growth/runs/combined-contract-attempt-1/records/SHA256SUMS.txt"
server_growth_checkpoint="$raw_source_root/server-growth/review/source-checkpoint-3/SHA256SUMS.txt"
server_growth_overlay_patch="$raw_source_root/server-growth/review/source-checkpoint-3/SERVER-GROWTH-GO.patch"
server_growth_runner_patch="$raw_source_root/server-growth/review/source-checkpoint-3/SERVER-GROWTH-RUNNER.patch"
server_growth_focused_receipt="$continuation_root/wan-below-tls-raw-write-source/runs/server-growth-focused-attempt-1/records/SHA256SUMS.txt"
expected_raw_ordinary_proof_receipt_sha256=b9ff8977b934f5a993fa7c29ab0d5b432a9a99d81c6c588c356cb476071a638d
expected_byte_credit_ordinary_proof_receipt_sha256=6562b975ba8f25d1b7a1a83c323c4c289f62f97b5508421e9e0f2e244c940460
expected_combined_build_receipt_sha256=dae7e3e7e58cd829804b07aea9b063aa6d0233552ae0068d9c38725bed427f13
raw_ordinary_proof_receipt="$combined_root/provenance/raw-ordinary-proof-receipt.json"
byte_credit_ordinary_proof_receipt="$combined_root/provenance/byte-credit-ordinary-proof-receipt.json"
combined_build_receipt="$combined_root/provenance/audited-combined-build-receipt.json"
server_binary=""
aggregate_agent_binary=""
server_source_ref=""
aggregate_agent_source_ref=""
protocol=""
cert=""
key=""
expected_leaf_sha256=""
output_dir=""
size_mode=sustained

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
    --small-diagnostic) size_mode=small; shift ;;
    *) echo "unknown argument: $1" >&2; exit 2 ;;
  esac
done

for pending in "$expected_raw_ordinary_proof_receipt_sha256" "$expected_byte_credit_ordinary_proof_receipt_sha256" "$expected_combined_build_receipt_sha256"; do
  [[ $pending =~ ^[0-9a-f]{64}$ ]] || {
    echo "final proof/build receipt hashes are not approved; endpoint execution is blocked" >&2
    exit 2
  }
done

[[ -x $server_binary && -x $aggregate_agent_binary && -x $runner && -x $validator && -x $arm_outcome_validator && -x $copy_record_validator &&
   -x $sender_snapshot_validator && -x $raw_write_validator && -x $server_growth_validator && -x $raw_closure_validator && -x $tcp_state_validator ]] || {
  echo "server, agent, runner, arm validator, and copy validator must be executable" >&2
  exit 2
}
[[ -x $byte_credit_validator && -x $byte_credit_runner_contract &&
   $(sha256sum "$byte_credit_adapter_checkpoint" | awk '{print $1}') == 4b91d78895462b0280ac0915bbf9580e49c864b747a6f231c3065d227f994062 &&
   $(sha256sum "$byte_credit_adapter_contract_ledger" | awk '{print $1}') == b4253d5b10bbbec2ea1d94b679ded5dea805081c62b2d31c36b3798ea726125a &&
   $(sha256sum "$byte_credit_protocol_checkpoint" | awk '{print $1}') == d315f2e4f49c3b00d714346d5ff38a797c52e7b1874e7d21e7d89679a46d4287 &&
   $(sha256sum "$byte_credit_protocol_focused_ledger" | awk '{print $1}') == 2ebacd83ac4102b008185ed173910c467bc271ca87ada6c4c831f1357fb0ea2e &&
   $(sha256sum "$byte_credit_protocol_focused_outer" | awk '{print $1}') == ffcd04d875bcc1d533bd93d2045e113f68a2e9f072643f1eaf96f5270db8126d ]] || {
  echo "byte-credit source or focused-contract binding failed" >&2
  exit 2
}
[[ -x $tcp_state_validator &&
   $(sha256sum "$tcp_producer_checkpoint" | awk '{print $1}') == 809f30484ea276a20586fec84517fdec9ef84ff58b2c96c9da3ab65b5122a3a0 &&
   $(sha256sum "$tcp_source_map" | awk '{print $1}') == 1211123ced422276abb4156ebd108fb3bec056273b3ddc384448fba283002d33 &&
   $(sha256sum "$tcp_build_records_ledger" | awk '{print $1}') == 3419e3da3b395db795e544aea1c1c59262033ad1827674ecf0dbce1d7200bbd8 &&
   $(sha256sum "$tcp_build_outer_ledger" | awk '{print $1}') == 442ffeb22586ad5179fd18646cbf48097848d0a3d2d8f5e7bc89a7a6bf0af8b0 &&
   $(sha256sum "$tcp_validator_contract_ledger" | awk '{print $1}') == 7cfe27fc04b1f64facac7c323b611a4b04cf608b87b07c20c41e82ac9e8fba67 &&
   $(sha256sum "$tcp_validator_contract_outer" | awk '{print $1}') == c0a705bb95d39dbeb05a4291b74b449dd65eb4fae6adb44fef2efe5e227e4c24 ]] || {
  echo "TCP-state source, build, or validator-contract binding failed" >&2
  exit 2
}
[[ $(sha256sum "$history_producer_checkpoint" | awk '{print $1}') == 3f1e9e54d979ba52680b5b16e761df188f9846d3add75a4d7a2a7f1617ccf0b9 &&
   $(sha256sum "$history_source_map" | awk '{print $1}') == 4d74567dcc24618dd841265e8dcbcdb378e63b9e59d25bfcb77e16740af49bdd &&
   $(sha256sum "$history_build_records_ledger" | awk '{print $1}') == 229a585ca3365ff06223043afcad608fc831fd9958f5f721386285a6c9ee7b1d &&
   $(sha256sum "$history_build_outer_ledger" | awk '{print $1}') == 5d115157591391a7ff29fef0795d698de91a7e349fd5690155d15fdf44d3efcb &&
   $(sha256sum "$history_validator_checkpoint" | awk '{print $1}') == 37c184eeffed66424f13dee384d0d0e31856c40892220c59d060308c46da7dc2 &&
   $(sha256sum "$history_validator_contract_ledger" | awk '{print $1}') == 058b0c7b912fe506867262f09f87f4d015ea2049acd3c51ede3d3e7dd8c23dac &&
   $(sha256sum "$history_validator_contract_outer" | awk '{print $1}') == fff73fb6e841714e3677c5b694afdfee2f7df7ebc9c64aff1ecb0abc1255040b ]] || {
  echo "threshold-history source, build, or validator-contract binding failed" >&2
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
cert=$(realpath "$cert")
key=$(realpath "$key")
for tool in cmp jq lscpu openssl patch python3 readlink setsid sha256sum systemctl systemd-run taskset timeout unshare; do
  command -v "$tool" >/dev/null || { echo "$tool is required" >&2; exit 2; }
done
[[ -f $raw_ordinary_proof_receipt && -f $byte_credit_ordinary_proof_receipt && -f $combined_build_receipt &&
   $(sha256sum "$raw_ordinary_proof_receipt" | awk '{print $1}') == "$expected_raw_ordinary_proof_receipt_sha256" &&
   $(sha256sum "$byte_credit_ordinary_proof_receipt" | awk '{print $1}') == "$expected_byte_credit_ordinary_proof_receipt_sha256" &&
   $(sha256sum "$combined_build_receipt" | awk '{print $1}') == "$expected_combined_build_receipt_sha256" ]] || {
  echo "final proof/build receipt binding failed" >&2
  exit 2
}
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
jq -e '.schema_version == 1 and .diagnostic == "wan-below-tls-raw-write" and
       .accepted == true and .ordinary_untagged_code_and_referents_equivalent == true and
       .strict_generated_symbol_names_equal == false and .strict_proof_accepted == false and
       .supplemental_reference_equivalence_accepted == true and
       .endpoint_execution == false and .endpoint_or_traffic == false and .wan_traffic == false' \
       "$raw_ordinary_proof_receipt" >/dev/null || {
  echo "raw ordinary proof receipt was not accepted" >&2; exit 2;
}
jq -e '.schema_version == 1 and .diagnostic == "wan-byte-credit-recycle" and
       .accepted == true and .ordinary_untagged_code_and_referents_equivalent == true and
       .strict_generated_symbol_names_equal == false and .strict_proof_accepted == false and
       .supplemental_reference_equivalence_accepted == true and
       .endpoint_execution == false and .endpoint_or_traffic == false and .wan_traffic == false and
       .source_checkpoint_sha256 == "d315f2e4f49c3b00d714346d5ff38a797c52e7b1874e7d21e7d89679a46d4287" and
       .focused_records_ledger_sha256 == "2ebacd83ac4102b008185ed173910c467bc271ca87ada6c4c831f1357fb0ea2e" and
       .reused_failed_records_ledger_sha256 == "9366180252ad6af8f4ae14f8212aa629e62581e5c0e90ad25b7a08a8f1da6927" and
       .strict_attempt_records_ledger_sha256 == "b44462efcda19a1a8cd4357effe7fdb590b2c565a034c29ddf747264ea44b13b" and
       .strict_report_sha256 == "e6a8aaa14b0682470cd944445498a0a1b9fc9250d79d7f99dd20e3b14440a581" and
       .strict_proof_checkpoint_sha256 == "6ce6b7a39f74be0e04bf653cc533b7bc5258d628f88bb776a8d869485585bace" and
       .base_source_inventory_sha256 == "2bf3c0201892b5da5af984874f93bbdfa8c59dd92c04886fbeaf545f4437816c" and
       .supplement_checkpoint_sha256 == "a29ff8ee73ac071e849aaf86624ebba7293489ca463de4f4e14d0b2c7f0b9ddf" and
       (.supplement_result_sha256 | type == "string" and test("^[0-9a-f]{64}$")) and
       (.supplement_contract_result_sha256 | type == "string" and test("^[0-9a-f]{64}$")) and
       (.final_sha256 | type == "string" and test("^[0-9a-f]{64}$")) and
       (.records_ledger_sha256 | type == "string" and test("^[0-9a-f]{64}$"))' \
       "$byte_credit_ordinary_proof_receipt" >/dev/null || {
  echo "byte-credit ordinary proof receipt was not accepted" >&2; exit 2;
}
# Keep the pinned historical receipt bound to its own historical tuple.
# The prospective requested tuple is checked independently below.
jq -e --arg server "67e7eed1317e944be1a54609ed3bcc911f855f7a39e21402695b6d7b6e5a55bb" --arg agent "3d6ee99bb47091f8a5986dd0d99d90a27e84c2e78fbd34eac536794e8fa5cd54" \
  --arg server_ref "8ac4c8df8237e37777ba2fac0da6f949da17bdea+tcp-threshold-history-corpus-970b41a3d3bb4ec7f3142e27875b9ba6d951e251b6c0bf6771059fb5ea801dbb" --arg agent_ref "8ac4c8df8237e37777ba2fac0da6f949da17bdea+tcp-threshold-history-corpus-970b41a3d3bb4ec7f3142e27875b9ba6d951e251b6c0bf6771059fb5ea801dbb" \
  --arg proof "$expected_raw_ordinary_proof_receipt_sha256" \
  --arg byte_proof "$expected_byte_credit_ordinary_proof_receipt_sha256" '
  .schema_version == 1 and .diagnostic == "wan-tcp-threshold-history-qualification" and
  .accepted == true and .diagnostic_only == true and .endpoint_execution == false and
  .wan_traffic == false and .traffic_run == false and .test_rerun == true and
  .server_binary_sha256 == $server and .agent_binary_sha256 == $agent and
  .server_source_ref == $server_ref and .agent_source_ref == $agent_ref and
  .raw_ordinary_proof_receipt_sha256 == $proof and
  .byte_credit_ordinary_proof_receipt_sha256 == $byte_proof and
  .base_build_receipt_sha256 == "bcca6079b5de32635701b4a1d5195cd9b9f9e8976d76e48956cea7d396cb7b97" and
  .base_build_records_ledger_sha256 == "3419e3da3b395db795e544aea1c1c59262033ad1827674ecf0dbce1d7200bbd8" and
  .base_source_inventory_sha256 == "0595c8e5fa4b19bc6612b409cfe69b7ddd0d4f7fa190a6dc26e27fd610648cd4" and
  .base_source_ref == "8ac4c8df8237e37777ba2fac0da6f949da17bdea" and
  .history_checkpoint_manifest_sha256 == "3f1e9e54d979ba52680b5b16e761df188f9846d3add75a4d7a2a7f1617ccf0b9" and
  .history_overlay_manifest_sha256 == "4d74567dcc24618dd841265e8dcbcdb378e63b9e59d25bfcb77e16740af49bdd" and
  .history_focused_test_plan_sha256 == "f6f6c04169c03b8e13210b0a6932199f4b064d2f0c6cca6c828dbfb0e56d0057" and
  .history_runtime_schema_carry_sha256 == "4df2fdb1a885fa15ca1e7ca1b0d61ebd3aa76414fcf8e1dd05a8d9811ea37d4a" and
  .history_validator_checkpoint_manifest_sha256 == "37c184eeffed66424f13dee384d0d0e31856c40892220c59d060308c46da7dc2" and
  .accepted_history_validator_contract_records_ledger_sha256 == "058b0c7b912fe506867262f09f87f4d015ea2049acd3c51ede3d3e7dd8c23dac" and
  .accepted_history_validator_contract_outer_ledger_sha256 == "fff73fb6e841714e3677c5b694afdfee2f7df7ebc9c64aff1ecb0abc1255040b" and
  .build_checkpoint_manifest_sha256 == "029354a26cbff494d8a17b690ecca231956517aea1aa9a3cff3cbdb426ac3825" and
  .build_driver_sha256 == "8306665ce7c2cadbef26919cd08e471b05e2c8940df500aead7156f132fa7fcf" and
  .records_ledger_sha256 == "229a585ca3365ff06223043afcad608fc831fd9958f5f721386285a6c9ee7b1d" and
  .final_source_inventory_sha256 == "970b41a3d3bb4ec7f3142e27875b9ba6d951e251b6c0bf6771059fb5ea801dbb"
' "$combined_build_receipt" >/dev/null || {
  echo "historical threshold-history build receipt no longer binds its historical tuple" >&2; exit 2;
}
[[ $(sha256sum "$integration_dir/verify-terminal-build.py" | awk '{print $1}') == a7d04c74ede96b84d241373117a17885175ef25bab1e4cd5e31b0fe3c00c49d2 ]] || { echo "prospective validator/verifier source changed" >&2; exit 2; }
[[ $(sha256sum "$integration_dir/validate-terminal-attribution.py" | awk '{print $1}') == 1be48e2f76dd566630c6427d9cf0a19dc6100fdf71532fb21616da209691d510 ]] || { echo "prospective validator/verifier source changed" >&2; exit 2; }
[[ $(sha256sum "$integration_dir/LEGACY-VALIDATOR.json" | awk '{print $1}') == b77ed3613b5d088efeacb1d6d4d763b3b255aa2fc35b5dc674771a39cdc73c38 ]] || { echo "prospective validator/verifier source changed" >&2; exit 2; }
PYTHONDONTWRITEBYTECODE=1 python3 -I -B "$integration_dir/verify-terminal-build.py" \
  --receipt "$integration_dir/../../build/receipt.json" \
  --expected-receipt-sha256 "$expected_terminal_build_receipt_sha256" \
  --server "$server_binary" --agent "$aggregate_agent_binary" \
  --server-ref "$server_source_ref" --agent-ref "$aggregate_agent_source_ref"
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
jq -e --arg server "0530cdc308bf06964f61d55ca730309e125cabd3c296ed3c22f0cfe218552013" --arg agent "9e635d6e1e8fbd8651260eb081fb5fc7847438bc51c653a5f7785cbecde4a137" \
  --arg server_ref "8ac4c8df8237e37777ba2fac0da6f949da17bdea" --arg agent_ref "8ac4c8df8237e37777ba2fac0da6f949da17bdea" '
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
jq -e --arg server "0530cdc308bf06964f61d55ca730309e125cabd3c296ed3c22f0cfe218552013" --arg agent "9e635d6e1e8fbd8651260eb081fb5fc7847438bc51c653a5f7785cbecde4a137" '
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
jq -e --arg server "57d77ddc5ee8ec6ab49cab0b7d0dd29641a70fa8598f0c7ba3eac73f46632ff5" --arg cwd "$script_dir/build/source" '
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
[[ $(sha256sum "$sender_observer_checkpoint" | awk '{print $1}') == 9f96e06bfd571fdf3550d095e6818b949bff0e3cdd733394e7ce93299e965e30 &&
   $(sha256sum "$sender_observer_patch" | awk '{print $1}') == 02f5ab266a7c6225dcf242dacb70edcf322cc790420f62e0d9ec8ae1ec11d130 &&
   $(sha256sum "$runner" | awk '{print $1}') == 308ab9ef7e9cb2b3c03014707203824ed19bf74486a8404bc1f2a1c336286416 &&
   $(sha256sum "$validator" | awk '{print $1}') == dd526df214e7adcbcfd4c790d2581348f8b1048d13fd5ca0d832306f57860235 &&
   $(sha256sum "$arm_outcome_validator" | awk '{print $1}') == 4c4f1c8119aac278b732fadabb72fa08f9249ee9ed003d0b881c5220b74f1eb1 &&
   $(sha256sum "$copy_record_validator" | awk '{print $1}') == f1c70a2f347a77975a00330d4d37929700b1a38bac5fa195c3dff02b3f773c26 &&
   $(sha256sum "$sender_snapshot_validator" | awk '{print $1}') == 25a934047a84c82d41852871047486138fa10fa4e8a1d3db3e867f38ad50e260 &&
   $(sha256sum "$raw_write_validator" | awk '{print $1}') == 66ff26fcdbfe71143d49b816e448f28ff7de789daba11e02c8d618d11c361bff &&
   $(sha256sum "$server_growth_validator" | awk '{print $1}') == bab8a8def06fc4347b5cfd8e7547c40ca85013fe28892da43c0e365c32dd7477 &&
   $(sha256sum "$byte_credit_validator" | awk '{print $1}') == e53f911e0362eeae728a55386cce5ebb0eb60a52163df14f8a95d0135aa0c3eb &&
   $(sha256sum "$tcp_state_validator" | awk '{print $1}') == 4a7addc3981eb892f364e85932b82856afcc1283139105b6d4d228ead2c4fa2c &&
   $(sha256sum "$raw_closure_validator" | awk '{print $1}') == 7b5b7962f603ebd7c160fabcdb9d4a78b7699e9866b4ed4172480be25b847eae ]] || {
  echo "sender observer or integration source checkpoint changed" >&2
  exit 2
}
[[ $(sha256sum "$combined_checkpoint_manifest" | awk '{print $1}') == 7a5aa90f45d588fc05e58f1968772b7a0accbad295bfecc7470bcb980022aab5 &&
   $(sha256sum "$combined_contract_ledger" | awk '{print $1}') == d63e8212ba90000f5067c20546a7f9084ae65f0073aa3cb9690c3c0c8615ef37 &&
   $(sha256sum "$server_growth_checkpoint" | awk '{print $1}') == 29e33466c5f79928b4d3805fcf957fb0d49b1eeb1f0c8fba720ff001c7193856 &&
   $(sha256sum "$server_growth_overlay_patch" | awk '{print $1}') == da47ac7179f6113a6c3a918a19db6ea0a8fd116feff7c893c2123b5fa9779644 &&
   $(sha256sum "$server_growth_runner_patch" | awk '{print $1}') == f08461837990894b693157a45510b8694749f882ccae8e0b01ce8c818e2e69a3 &&
   $(sha256sum "$server_growth_focused_receipt" | awk '{print $1}') == 2109895c1d9d0d4824f49b9823b034a44693dfa1d8ca05c5340491024fa726ff ]] || {
  echo "combined integration checkpoint or accepted no-endpoint receipt changed" >&2
  exit 2
}
[[ -f $sender_build_manifest && -f $sender_harness_receipt && -f $sender_integration_checkpoint &&
   -f $reviewed_integration_manifest && -f $reviewed_integration_verifier &&
   -f $base_cp7_checkpoint && -f $base_cp7_reviewed_manifest && -x $base_cp7_runner ]] || {
  echo "audited sender-duration build and cp10 validator receipt are unavailable" >&2; exit 2;
}
# The immutable build manifest proves the unchanged Go source and binaries. Its
# cp7 harness fields remain historical and are checked exactly; the separate
# externally reviewed cp8 receipt binds the shell-only correction below.
jq -e --arg server "f2e6cf390fdcd727b8c7120a6d877dc3a53afe9d8b970aa7b0d7f6cc892a7bdc" --arg agent "43eadb7664d70f9585887ac824cc7e6dcb55d6fa40731e3888b6fa5a1cc8d74f" \
  --arg base_runner "$(sha256sum "$base_cp7_runner" | awk '{print $1}')" \
  --arg base_checkpoint "$(sha256sum "$base_cp7_checkpoint" | awk '{print $1}')" \
  --arg base_reviewed "$(sha256sum "$base_cp7_reviewed_manifest" | awk '{print $1}')" \
  --arg source_ref "8ac4c8df8237e37777ba2fac0da6f949da17bdea" --arg agent_ref "8ac4c8df8237e37777ba2fac0da6f949da17bdea" '
    .diagnostic == "wan-sender-wait" and .diagnostic_only == true and .traffic_run == false and
    .base_source_ref == $source_ref and .aggregate_agent_source_ref == $agent_ref and
    .sender_observer_checkpoint_sha256 == "9f96e06bfd571fdf3550d095e6818b949bff0e3cdd733394e7ce93299e965e30" and
    .sender_observer_patch_sha256 == "02f5ab266a7c6225dcf242dacb70edcf322cc790420f62e0d9ec8ae1ec11d130" and
    .sender_integration_checkpoint_sha256 == $base_checkpoint and
    .reviewed_integration_source_manifest_sha256 == $base_reviewed and
    .source.pre_post_status == 0 and .module.pre_post_status == 0 and .toolchain.pre_post_status == 0 and
    .tests.normal_status == 0 and .tests.race_status == 0 and .ordinary_proof_status == 0 and
    .build.status == 0 and .build.source_compare_status == 0 and
    .runner_sha256 == $base_runner and
    .sender_snapshot_validator_sha256 == "de77c640a72236f4df2d482702ba21cdc225731bb0a0fef3dac14e915ed32da9" and
    .sender_runner_patch_sha256 == "7ea2f679c170e0580832c653afcb64e80fa27da9c8ea613f84092b9994ff6e27" and
    .sender_control_patch_sha256 == "dc60a099541ad9c67d35a5af46d8af2a94e2f277725f53f8e658598dd5515c9f" and
    .output.server_sha256 == $server and .output.agent_sha256 == $agent
  ' "$sender_build_manifest" >/dev/null || { echo "audited sender-duration build manifest changed" >&2; exit 2; }
jq -e --arg build_manifest "$(sha256sum "$sender_build_manifest" | awk '{print $1}')" \
  --arg integration_checkpoint "$(sha256sum "$sender_integration_checkpoint" | awk '{print $1}')" \
  --arg reviewed_manifest "$(sha256sum "$reviewed_integration_manifest" | awk '{print $1}')" \
  --arg runner "$(sha256sum "$historical_cp10_runner" | awk '{print $1}')" --arg helper "$(sha256sum "$script_dir/validate-copy-record.py" | awk '{print $1}')" \
  --arg server "f2e6cf390fdcd727b8c7120a6d877dc3a53afe9d8b970aa7b0d7f6cc892a7bdc" --arg agent "43eadb7664d70f9585887ac824cc7e6dcb55d6fa40731e3888b6fa5a1cc8d74f" '
    .schema_version == 1 and .action == "sender-validator-manifest-correction" and
    .diagnostic == "wan-sender-wait" and .accepted == true and
    .base_build_manifest_sha256 == $build_manifest and
    .base_integration_checkpoint_sha256 == "fa0f2237aef78d5253882eef1fc45942446f0d96627efa8e0234161858eedb34" and
    .base_reviewed_manifest_sha256 == "2eee698fdfa8322b42491f3b090ac7f3535c3046d3f3f827460cc142d8c4aca1" and
    .cp10_integration_checkpoint_sha256 == $integration_checkpoint and
    .cp10_reviewed_manifest_sha256 == $reviewed_manifest and
    .cp10_runner_sha256 == $runner and .copy_record_validator_sha256 == $helper and
    .base_source_ref == "8ac4c8df8237e37777ba2fac0da6f949da17bdea" and
    .aggregate_agent_source_ref == "8ac4c8df8237e37777ba2fac0da6f949da17bdea" and
    .sender_observer_checkpoint_sha256 == "9f96e06bfd571fdf3550d095e6818b949bff0e3cdd733394e7ce93299e965e30" and
    .sender_observer_patch_sha256 == "02f5ab266a7c6225dcf242dacb70edcf322cc790420f62e0d9ec8ae1ec11d130" and
    .base_runner_sha256 == "e9e453206cd479e1d29b78c05cf4d0eaf0cbaefe3c6ff13f0ba9594235c3bea0" and
    .sender_control_patch_sha256 == "dc60a099541ad9c67d35a5af46d8af2a94e2f277725f53f8e658598dd5515c9f" and
    .sender_snapshot_validator_sha256 == "de77c640a72236f4df2d482702ba21cdc225731bb0a0fef3dac14e915ed32da9" and
    .server_binary_sha256 == $server and .agent_binary_sha256 == $agent and
    .cp9_contracts.direct_status == 0 and .cp9_contracts.source_pre_post_equal == true and
    .offline_validator_contract.direct_status == 0 and .offline_validator_contract.input_compare_status == 0 and
    .offline_validator_contract.accepted == true and .offline_validator_contract.outer_tool_status == 1 and
    .offline_verification.h1_status == 0 and .offline_verification.h2_status == 0 and
    .offline_verification.input_compare_status == 0 and .offline_verification.audit_status == 0 and
    .offline_verification.accepted == true and .offline_verification.outer_tool_status == 1 and
    .cp9_integration_checkpoint_sha256 == "601ffd71333a0476e9a6aef92eb3947604fd91a594d6d4c906d9f1e8a2de3fc0" and
    .cp9_reviewed_manifest_sha256 == "fdf2e441f130aac840d2b08cd0b82a7196a71d4dd1519a1c49b6dfcdebfc3fab" and
    .cp9_harness_receipt_sha256 == "11b3f6cfbd1de9a8ff612ef483b127b87e16d4099debc369d5f5db542fc9d53e" and
    .validator_delta_sha256 == "df717131968ffefabbb7619983c60d57a130c381ad54e93aae44429dd6d781e2" and
    .offline_validator_sha256 == "9ae7fa74c6be09706d0b77759b52b696dbc302ffd14b3ba8c6a44b23547a2860" and
    .unchanged_go_source_and_binaries == true and .endpoint_execution_run == false and .traffic_run == false
  ' "$sender_harness_receipt" >/dev/null || { echo "cp10 validator correction receipt changed" >&2; exit 2; }
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
python3 "$reviewed_integration_verifier" "$reviewed_integration_manifest" "$mature_sender_root" \
  "$output_dir/historical-cp10-source-verification.json"
scope_dir="$output_dir/server-scope"
mkdir -m 0700 "$scope_dir"
scope_nonce=$(cat /proc/sys/kernel/random/uuid)
scope_unit="p2p-senderwait-${protocol}-${BASHPID}-${scope_nonce:0:8}.scope"
scope_sentinel_state="$scope_dir/sentinel.json"
scope_sentinel_release="$scope_dir/sentinel.release"
scope_preexec_state="$scope_dir/server-preexec.json"
scope_preexec_release="$scope_dir/server-preexec.release"
scope_app_ready="$scope_dir/server-app-ready.json"
scope_app_release="$scope_dir/server-app.release"

terminal_attribution_validator="$integration_dir/validate-terminal-attribution.py"
terminal_legacy_references="$integration_dir/LEGACY-VALIDATOR.json"
terminal_build_receipt="$integration_dir/../../build/receipt.json"
terminal_build_plan="$integration_dir/../../BUILD-TEST-PLAN.json"
terminal_build_verifier="$integration_dir/verify-terminal-build.py"
terminal_build_source_inventory="$integration_dir/../../SOURCE-SHA256SUMS"

write_hashes() {
  local destination=$1
  python3 - "$destination" "$0" "$runner" "$validator" "$arm_outcome_validator" "$copy_record_validator" "$sender_snapshot_validator" "$raw_write_validator" "$server_growth_validator" "$raw_closure_validator" "$byte_credit_validator" "$approved_runner" "$frozen_protocol_runner" "$frozen_copy_runner" \
    "$wan_protocol_runner_patch" "$copy_runner_patch" "$monotonic_runner_patch" "$read_wait_runner_patch" "$server_capacity_patch" "$cleanup_status_patch" "$go_patch" "$go_helper" "$copy_overlay" \
    "$monotonic_observer_patch" "$monotonic_validator_patch" "$monotonic_arm_validator_patch" "$copy_design" \
    "$monotonic_build_manifest" "$monotonic_audited_build_manifest" "$final_source_attestation" \
    "$runner_reconstruction_attestation" "$integration_notes" "$decision_map" "$observer_patch" "$observer_checkpoint" \
    "$focused_checkpoint" "$audited_build_manifest" "$capacity_source_checkpoint" "$capacity_source_checkpoint_manifest" \
    "$capacity_focused_checkpoint" "$capacity_audited_build_manifest" "$scope_entrypoint" "$scope_sentinel" "$scope_validator" "$scope_resource_validator" "$scope_probe_attestation" \
    "$sender_observer_checkpoint" "$sender_observer_patch" "$sender_integration_checkpoint" "$reviewed_integration_manifest" "$reviewed_integration_verifier" "$sender_snapshot_validator" \
    "$sender_build_manifest" "$sender_harness_receipt" "$base_cp7_checkpoint" "$base_cp7_reviewed_manifest" "$base_cp7_runner" "$sender_runner_patch" "$sender_control_patch" \
    "$runner_integration_design" "$static_contract" "$resource_contract" "$sender_wrapper_contract" "$arm_validator_contract" \
    "$cleanup_status_contract" "$cleanup_status_body" "$cleanup_status_holder" "$all_contracts" \
    "$server_growth_checkpoint" "$server_growth_overlay_patch" "$server_growth_runner_patch" "$server_growth_focused_receipt" \
    "$combined_checkpoint_manifest" "$combined_contract_ledger" "$raw_ordinary_proof_receipt" "$byte_credit_ordinary_proof_receipt" "$combined_build_receipt" \
    "$byte_credit_adapter_checkpoint" "$byte_credit_adapter_contract_ledger" "$byte_credit_protocol_checkpoint" "$byte_credit_protocol_focused_ledger" "$byte_credit_protocol_focused_outer" "$byte_credit_runner_contract" \
    "$tcp_state_validator" "$tcp_producer_checkpoint" "$tcp_source_map" "$tcp_build_records_ledger" "$tcp_build_outer_ledger" "$tcp_validator_contract_ledger" "$tcp_validator_contract_outer" \
    "$history_producer_checkpoint" "$history_source_map" "$history_build_records_ledger" "$history_build_outer_ledger" "$history_validator_checkpoint" "$history_validator_contract_ledger" "$history_validator_contract_outer" \
    "$terminal_attribution_validator" "$terminal_legacy_references" "$terminal_build_receipt" "$terminal_build_plan" "$terminal_build_verifier" "$terminal_build_source_inventory" \
    "$server_binary" "$aggregate_agent_binary" "$cert" "$key" "$server_source_ref" \
    "$aggregate_agent_source_ref" "$expected_leaf_sha256" "$size_mode" <<'PY'
import hashlib, json, pathlib, sys
(destination, supervisor, runner, validator, arm_outcome_validator, copy_record_validator, sender_snapshot_validator_current, raw_write_validator, server_growth_validator, raw_closure_validator, byte_credit_validator, approved, frozen_protocol_runner, frozen_copy_runner,
 wan_protocol_runner_patch, copy_runner_patch, monotonic_runner_patch, read_wait_runner_patch, server_capacity_patch, cleanup_status_patch, go_patch, go_helper, copy_overlay,
 monotonic_observer_patch, monotonic_validator_patch, monotonic_arm_validator_patch, copy_design,
 monotonic_build_manifest, monotonic_audited_build_manifest, final_source_attestation,
 runner_reconstruction_attestation, integration_notes, decision_map, observer_patch, observer_checkpoint,
 focused_checkpoint, audited_build_manifest, capacity_source_checkpoint, capacity_source_checkpoint_manifest,
 capacity_focused_checkpoint, capacity_audited_build_manifest, scope_entrypoint, scope_sentinel, scope_validator, scope_resource_validator, scope_probe_attestation,
 sender_observer_checkpoint, sender_observer_patch, sender_integration_checkpoint, reviewed_integration_manifest, reviewed_integration_verifier, sender_snapshot_validator,
 sender_build_manifest, sender_harness_receipt, base_cp7_checkpoint, base_cp7_reviewed_manifest, base_cp7_runner, sender_runner_patch, sender_control_patch,
 runner_integration_design, static_contract, resource_contract, sender_wrapper_contract, arm_validator_contract,
 cleanup_status_contract, cleanup_status_body, cleanup_status_holder, all_contracts,
 server_growth_checkpoint, server_growth_overlay_patch, server_growth_runner_patch, server_growth_focused_receipt,
 combined_checkpoint_manifest, combined_contract_ledger, raw_ordinary_proof_receipt, byte_credit_ordinary_proof_receipt, combined_build_receipt,
 byte_credit_adapter_checkpoint, byte_credit_adapter_contract_ledger, byte_credit_protocol_checkpoint, byte_credit_protocol_focused_ledger, byte_credit_protocol_focused_outer, byte_credit_runner_contract,
 tcp_state_validator, tcp_producer_checkpoint, tcp_source_map, tcp_build_records_ledger, tcp_build_outer_ledger, tcp_validator_contract_ledger, tcp_validator_contract_outer,
 history_producer_checkpoint, history_source_map, history_build_records_ledger, history_build_outer_ledger, history_validator_checkpoint, history_validator_contract_ledger, history_validator_contract_outer,
 terminal_attribution_validator, terminal_legacy_references, terminal_build_receipt, terminal_build_plan, terminal_build_verifier, terminal_build_source_inventory,
 server, agent, cert, key, server_ref, agent_ref, fingerprint, size_mode) = sys.argv[1:]
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
    "size_mode": size_mode,
    "sha256": {
        "supervisor": sha(supervisor), "runner": sha(runner), "validator": sha(validator),
        "arm_outcome_validator": sha(arm_outcome_validator),
        "terminal_attribution_validator": sha(terminal_attribution_validator),
        "terminal_legacy_references": sha(terminal_legacy_references),
        "terminal_build_receipt": sha(terminal_build_receipt),
        "terminal_build_plan": sha(terminal_build_plan),
        "terminal_build_verifier": sha(terminal_build_verifier),
        "terminal_build_source_inventory": sha(terminal_build_source_inventory),
        "copy_record_validator": sha(copy_record_validator), "approved_lifecycle_runner": sha(approved),
        "sender_snapshot_validator_current": sha(sender_snapshot_validator_current),
        "raw_write_validator": sha(raw_write_validator), "server_growth_validator": sha(server_growth_validator),
        "raw_closure_validator": sha(raw_closure_validator),
        "byte_credit_validator": sha(byte_credit_validator),
        "frozen_protocol_runner": sha(frozen_protocol_runner), "frozen_copy_runner": sha(frozen_copy_runner),
        "wan_protocol_runner_patch": sha(wan_protocol_runner_patch), "copy_runner_patch": sha(copy_runner_patch),
        "monotonic_runner_patch": sha(monotonic_runner_patch), "read_wait_runner_patch": sha(read_wait_runner_patch),
        "server_capacity_snapshot_patch": sha(server_capacity_patch), "cleanup_status_patch": sha(cleanup_status_patch),
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
        "sender_observer_checkpoint": sha(sender_observer_checkpoint), "sender_observer_patch": sha(sender_observer_patch),
        "sender_integration_checkpoint": sha(sender_integration_checkpoint),
        "reviewed_integration_manifest": sha(reviewed_integration_manifest),
        "reviewed_integration_verifier": sha(reviewed_integration_verifier),
        "sender_snapshot_validator": sha(sender_snapshot_validator),
        "sender_build_manifest": sha(sender_build_manifest), "sender_harness_receipt": sha(sender_harness_receipt),
        "base_cp7_checkpoint": sha(base_cp7_checkpoint), "base_cp7_reviewed_manifest": sha(base_cp7_reviewed_manifest),
        "base_cp7_runner": sha(base_cp7_runner),
        "sender_runner_patch": sha(sender_runner_patch),
        "sender_control_patch": sha(sender_control_patch),
        "runner_integration_design": sha(runner_integration_design), "static_contract": sha(static_contract),
        "resource_contract": sha(resource_contract), "sender_wrapper_contract": sha(sender_wrapper_contract),
        "arm_validator_contract": sha(arm_validator_contract), "cleanup_status_contract": sha(cleanup_status_contract),
        "cleanup_status_body": sha(cleanup_status_body), "cleanup_status_holder": sha(cleanup_status_holder),
        "all_contracts": sha(all_contracts),
        "server_growth_checkpoint": sha(server_growth_checkpoint),
        "server_growth_overlay_patch": sha(server_growth_overlay_patch),
        "server_growth_runner_patch": sha(server_growth_runner_patch),
        "server_growth_focused_receipt": sha(server_growth_focused_receipt),
        "combined_checkpoint_manifest": sha(combined_checkpoint_manifest),
        "combined_contract_ledger": sha(combined_contract_ledger),
        "raw_ordinary_proof_receipt": sha(raw_ordinary_proof_receipt),
        "byte_credit_ordinary_proof_receipt": sha(byte_credit_ordinary_proof_receipt),
        "combined_build_receipt": sha(combined_build_receipt),
        "byte_credit_adapter_checkpoint": sha(byte_credit_adapter_checkpoint),
        "byte_credit_adapter_contract_ledger": sha(byte_credit_adapter_contract_ledger),
        "byte_credit_protocol_checkpoint": sha(byte_credit_protocol_checkpoint),
        "byte_credit_protocol_focused_ledger": sha(byte_credit_protocol_focused_ledger),
        "byte_credit_protocol_focused_outer": sha(byte_credit_protocol_focused_outer),
        "byte_credit_runner_contract": sha(byte_credit_runner_contract),
        "tcp_state_validator": sha(tcp_state_validator),
        "tcp_producer_checkpoint": sha(tcp_producer_checkpoint),
        "tcp_source_map": sha(tcp_source_map),
        "tcp_build_records_ledger": sha(tcp_build_records_ledger),
        "tcp_build_outer_ledger": sha(tcp_build_outer_ledger),
        "tcp_validator_contract_ledger": sha(tcp_validator_contract_ledger),
        "tcp_validator_contract_outer": sha(tcp_validator_contract_outer),
        "history_producer_checkpoint": sha(history_producer_checkpoint),
        "history_source_map": sha(history_source_map),
        "history_build_records_ledger": sha(history_build_records_ledger),
        "history_build_outer_ledger": sha(history_build_outer_ledger),
        "history_validator_checkpoint": sha(history_validator_checkpoint),
        "history_validator_contract_ledger": sha(history_validator_contract_ledger),
        "history_validator_contract_outer": sha(history_validator_contract_outer),
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

warmup_mib=256
steady_mib=256
required_pool_mib=8
if [[ $size_mode == small ]]; then
  warmup_mib=4
  steady_mib=4
  required_pool_mib=2
fi
command=(
  "$runner"
  --server-binary "$server_binary"
  --aggregate-agent-binary "$aggregate_agent_binary"
  --output "$results"
  --runs 1
  --warmup-mib "$warmup_mib"
  --steady-mib "$steady_mib"
  --transfer-repetitions 3
  --required-pool-mib "$required_pool_mib"
  --rtt-ms 80
  --server-source-ref "$server_source_ref"
  --aggregate-agent-source-ref "$aggregate_agent_source_ref"
  --protocol "$protocol"
  --cert "$cert"
  --key "$key"
  --expected-leaf-sha256 "$expected_leaf_sha256"
  --copy-record-validator "$copy_record_validator"
  --copy-record-validator-sha256 "$(sha256sum "$copy_record_validator" | awk '{print $1}')"
  --sender-wait-validator "$sender_snapshot_validator"
  --sender-wait-validator-sha256 "$(sha256sum "$sender_snapshot_validator" | awk '{print $1}')"
  --raw-write-validator "$raw_write_validator"
  --raw-write-validator-sha256 "$(sha256sum "$raw_write_validator" | awk '{print $1}')"
)
[[ $size_mode == sustained ]] || command+=(--sender-wait-small)
timeout_seconds=4800
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

sender_terminal_status=1
sender_terminal_validation_status=1
sender_terminal_valid=false
if [[ -s ${results%.jsonl}.sender-wait-records.jsonl ]]; then
  set +e
  terminal_values=$(jq -er -s '
    [.[] | select(.action == "terminal")] as $rows |
    if ($rows|length) == 1 then
      ($rows[0].status|tostring) + " " + ($rows[0].validation_status|tostring) + " " + ($rows[0].valid|tostring)
    else error("terminal sender record cardinality changed") end
  ' "${results%.jsonl}.sender-wait-records.jsonl")
  terminal_extract_status=$?
  set -e
  if ((terminal_extract_status == 0)); then
    read -r sender_terminal_status sender_terminal_validation_status sender_terminal_valid <<<"$terminal_values"
  fi
fi

raw_finalization_status=1
raw_final_closure_valid=false
raw_supervisor_validation="$output_dir/raw-post-join-supervisor-validation.json"
set +e
PYTHONDONTWRITEBYTECODE=1 PYTHONOPTIMIZE= PYTHONPATH= python3 -I -B - \
  "$raw_closure_validator" "$sender_snapshot_validator" \
  "${results%.jsonl}.sender-wait-controls" "$raw_supervisor_validation" <<'PY'
import importlib.util, json, pathlib, sys
closure_path, sender_path, control_dir, output = map(pathlib.Path, sys.argv[1:])
def load(name, path):
    spec = importlib.util.spec_from_file_location(name, path)
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module
closure = load("post_join_raw_closure", closure_path)
sender = load("sender_wait_snapshot", sender_path)
value = closure.validate_post_join_raw_closure(control_dir, sender)
with output.open("x", encoding="utf-8") as stream:
    json.dump(value, stream, indent=2, sort_keys=True)
    stream.write("\n")
raise SystemExit(0 if value.get("accepted") is True else 1)
PY
raw_finalization_status=$?
set -e
if ((raw_finalization_status == 0)) && jq -e '.accepted == true' "$raw_supervisor_validation" >/dev/null; then
  raw_final_closure_valid=true
fi

# Prospective terminal contract: preserve old raw verdict, require a new
# independently checked attribution result and actual natural endpoint exits.
raw_attribution_status=1
raw_attribution_valid=false
raw_attribution_validation="$output_dir/prospective-terminal-attribution.json"
set +e
PYTHONDONTWRITEBYTECODE=1 PYTHONOPTIMIZE= PYTHONPATH= python3 -I -B \
  "$integration_dir/validate-terminal-attribution.py" \
  --raw "${results%.jsonl}.sender-wait-controls/final-raw-write-terminal.json" \
  --attribution "${results%.jsonl}.sender-wait-controls/final-raw-write-terminal.json.attribution.json" \
  --control-directory "${results%.jsonl}.sender-wait-controls" --output "$raw_attribution_validation"
raw_attribution_status=$?
set -e
if ((raw_attribution_status == 0)) && jq -e '.attribution_accepted == true' "$raw_attribution_validation" >/dev/null; then
  raw_attribution_valid=true
fi

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
     scope_unit_joined != 1 || scope_unit_final_query_status != 0 || sender_terminal_status != 0 || \
     sender_terminal_validation_status != 0 || raw_attribution_status != 0)) || \
     [[ $sender_terminal_valid != true || $raw_attribution_valid != true ]]; then
  invalidates_success=true
fi
printf '{"runner_status":%s,"group_joined":%s,"forced":%s,"invalidates_success":%s,"server_scope":{"systemd_run_status":%s,"resource_status":%s,"unit_joined":%s,"unit_query_status":%s,"unit_load_state":"%s","unit_active_state":"%s","unit_sub_state":"%s"},"sender_evidence":{"terminal_status":%s,"terminal_validation_status":%s,"terminal_valid":%s},"raw_write_evidence":{"finalization_status":%s,"final_closure_valid":%s},"prospective_terminal_attribution":{"validation_status":%s,"accepted":%s}}\n' \
  "$runner_status" "$group_joined" "$([[ $forced == 0 ]] && printf false || printf true)" \
  "$invalidates_success" "$scope_launcher_status" "$scope_resource_status" \
  "$([[ $scope_unit_joined == 1 ]] && printf true || printf false)" "$scope_unit_final_query_status" \
  "$scope_unit_final_load_state" "$scope_unit_final_active_state" "$scope_unit_final_sub_state" \
  "$sender_terminal_status" "$sender_terminal_validation_status" "$sender_terminal_valid" \
  "$raw_finalization_status" "$raw_final_closure_valid" "$raw_attribution_status" "$raw_attribution_valid" > "$supervisor"
runner_pid=""
write_hashes "$post_hashes"

set +e
"$validator" "$results" "$lifecycle" "$summary" "$outer" "$supervisor" "$pre_hashes" "$post_hashes" \
  "$attestation" "$protocol" "$size_mode" > "$output_dir/validation.stdout.log" 2> "$output_dir/validation.stderr.log"
validation_status=$?
set -e
printf '%s\n' "$validation_status" > "$output_dir/validation.status"
((runner_status == 0 && forced == 0 && scope_launcher_status == 0 && scope_resource_status == 0 && scope_unit_joined == 1 && scope_unit_final_query_status == 0 && sender_terminal_status == 0 && sender_terminal_validation_status == 0 && raw_attribution_status == 0 && validation_status == 0)) && [[ $sender_terminal_valid == true && $raw_attribution_valid == true ]]
