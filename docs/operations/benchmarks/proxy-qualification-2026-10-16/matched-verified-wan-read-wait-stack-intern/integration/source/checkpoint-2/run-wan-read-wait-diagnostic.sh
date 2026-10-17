#!/usr/bin/env bash
set -Eeuo pipefail

integration_dir=$(cd "$(dirname "$0")" && pwd)
capacity_root=$(realpath "$integration_dir/../../wan-read-wait-capacity-source")
compact_root=$(realpath "$integration_dir/..")
script_dir=$capacity_root
supervisor="$integration_dir/run-wan-read-wait-arm.sh"
validator="$integration_dir/validate-wan-read-wait-arm.py"
copy_record_validator="$script_dir/validate-copy-record.py"
runner="$script_dir/generated/qualify-proxy-agent-growth-wan-read-wait.sh"
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
approved_runner="$script_dir/../current-wan-prep/runner/qualify-proxy-agent-growth-owned.sh"
go_patch="$script_dir/../wan-protocol-source/proxy-qualification-base.patch"
go_helper="$script_dir/../wan-protocol-source/proxy_qualification_wan_protocol_test.go"
copy_overlay="$script_dir/../wan-copy-attribution-source/copy-attribution-overlay.patch"
monotonic_observer_patch="$monotonic_root/monotonic-observer.patch"
monotonic_validator_patch="$monotonic_root/monotonic-validator-contract.patch"
monotonic_arm_validator_patch="$monotonic_root/monotonic-arm-validator.patch"
copy_design="$monotonic_root/DESIGN.md"
monotonic_build_manifest="$monotonic_root/provenance/monotonic-build-manifest.json"
monotonic_audited_build_manifest="$monotonic_root/provenance/audited-server-build-manifest.json"
audited_build_manifest="$predecessor_read_wait_root/provenance/audited-server-build-manifest.json"
final_source_attestation="$monotonic_root/provenance/final-source-binary-toolchain-attestation.json"
runner_reconstruction_attestation="$monotonic_root/provenance/runner-reconstruction-attestation.json"
integration_notes="$monotonic_root/INTEGRATION-NOTES.md"
decision_map="$script_dir/../current-wan-prep/COPY-TIMING-DECISION-MAP.md"
observer_patch="$predecessor_read_wait_root/observer-source.patch"
observer_checkpoint="$predecessor_read_wait_root/OBSERVER-SOURCE-CHECKPOINT.json"
focused_checkpoint="$predecessor_read_wait_root/provenance/focused-tests/FOCUSED-TEST-CHECKPOINT.json"
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
server_binary=""
aggregate_agent_binary=""
server_source_ref=""
aggregate_agent_source_ref=""
output_dir=""
smoke=0

while (($#)); do
  case "$1" in
    --server-binary) server_binary=$2; shift 2 ;;
    --aggregate-agent-binary) aggregate_agent_binary=$2; shift 2 ;;
    --server-source-ref) server_source_ref=$2; shift 2 ;;
    --aggregate-agent-source-ref) aggregate_agent_source_ref=$2; shift 2 ;;
    --output-dir) output_dir=$2; shift 2 ;;
    --smoke) smoke=1; shift ;;
    *) echo "unknown argument: $1" >&2; exit 2 ;;
  esac
done

[[ -x $server_binary && -x $aggregate_agent_binary && -x $supervisor && -x $validator && -x $copy_record_validator && -x $runner &&
   -x $scope_entrypoint && -x $scope_sentinel && -x $scope_validator && -x $scope_resource_validator ]] || {
  echo "executable binaries, runner, supervisors, validators, and scope helpers are required" >&2
  exit 2
}
[[ -n $server_source_ref && -n $aggregate_agent_source_ref && -n $output_dir ]] || {
  echo "source refs and output directory are required" >&2
  exit 2
}
server_binary=$(realpath "$server_binary")
aggregate_agent_binary=$(realpath "$aggregate_agent_binary")
for tool in cmp jq openssl patch python3 sha256sum; do
  command -v "$tool" >/dev/null || { echo "$tool is required" >&2; exit 2; }
done
[[ $(sha256sum "$approved_runner" | awk '{print $1}') == b29242b6891105fd6231fecd7739ae48e6909049d0583a184765839ad2827c5f ]] || {
  echo "approved owned lifecycle runner hash changed" >&2
  exit 2
}
[[ $(sha256sum "$read_wait_runner_patch" | awk '{print $1}') == 267a6421bea2ceec7634b5cf13b700e11c3a9da5a2d3c1aa14f6cc1fe0210e5c &&
   $(sha256sum "$server_capacity_patch" | awk '{print $1}') == 0392a5055b5c1282627d959216bed7bd898e6f48da46a0c79c46d457e94284de ]] || {
  echo "read-wait runner patch changed" >&2
  exit 2
}
[[ $(sha256sum "$server_binary" | awk '{print $1}') == 57d77ddc5ee8ec6ab49cab0b7d0dd29641a70fa8598f0c7ba3eac73f46632ff5 &&
   $(sha256sum "$aggregate_agent_binary" | awk '{print $1}') == 9e635d6e1e8fbd8651260eb081fb5fc7847438bc51c653a5f7785cbecde4a137 ]] || {
  echo "server or agent is not the approved diagnostic binary" >&2
  exit 2
}
[[ -f $trace_analyzer && -f $trace_parser_wrapper &&
   $(sha256sum "$trace_analyzer" | awk '{print $1}') == ebb8e1a48b10f966df026a475d6217c83f117ac70e7a9195d66aadc7e1bdf895 &&
   $(sha256sum "$trace_parser_wrapper" | awk '{print $1}') == 719eac5dac37a0dc686283025e7005aba3cbf5408436b6257e6fde8811a50df8 &&
   $(sha256sum "$trace_canonicalizer" | awk '{print $1}') == 4d1bbcdd2140d148741775842e1ba9d2d74f2541b15933a6232b18cf3473e793 &&
   $(sha256sum "$trace_format" | awk '{print $1}') == f370243b753a94a122157e9b0b13eb535065fe88746269bd70de5475d82e4dbf &&
   $(sha256sum "$trace_analyzer_schema" | awk '{print $1}') == fc81d1afe93b8f805f98cae922f21032948b15a3985470f238545bb8663e4df7 &&
   $(sha256sum "$trace_analyzer_checkpoint" | awk '{print $1}') == 7a851aca80494e1b12d4e9dd4c94406f02445cf067e3b0c497dc679ab0fd2cbc &&
   $(sha256sum "$trace_focused_manifest" | awk '{print $1}') == e5c1832565bb77818a4c40e5e00faab66ac19ea0a556885cabccd3f8be1803d2 &&
   $(sha256sum "$trace_focused_summary" | awk '{print $1}') == 669fdeb61e44fb35bcda0c9b3f1e42d3097f4d42a36596bd6defd94f2d0b332f &&
   $(sha256sum "$trace_lifecycle_manifest" | awk '{print $1}') == 22a87032adbc4096fde3dc97db2de80a2cad6ae86994cbacb37aec6366915897 &&
   $(sha256sum "$trace_lifecycle_summary" | awk '{print $1}') == 37159578894bc8a4706d9bc191e7c4986a259b01973c527f174b56736d5f7cd6 ]] || {
  echo "pinned compact trace source or checkpoint changed" >&2
  exit 2
}
jq -e '.diagnostic_only == true and .traffic_run == false and .source_checkpoint == 8 and
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
output_dir=$(realpath -m "$output_dir")
allowed_root=$(realpath -m "$integration_dir/runs")
[[ $output_dir == "$allowed_root"/* ]] || { echo "output must remain below $allowed_root" >&2; exit 2; }
[[ ! -e $output_dir ]] || { echo "output directory already exists: $output_dir" >&2; exit 2; }
mkdir -p "$output_dir/tls" "$output_dir/preflight"

rebuilt="$output_dir/preflight/runner-rebuilt.sh"
cp "$approved_runner" "$rebuilt"
patch --quiet "$rebuilt" "$wan_protocol_runner_patch"
cmp "$rebuilt" "$frozen_protocol_runner"
patch --quiet "$rebuilt" "$copy_runner_patch"
cmp "$rebuilt" "$frozen_copy_runner"
patch --quiet "$rebuilt" "$monotonic_runner_patch"
cmp "$rebuilt" "$monotonic_root/generated/qualify-proxy-agent-growth-wan-copy-monotonic.sh"
patch --quiet "$rebuilt" "$read_wait_runner_patch"
cmp "$rebuilt" "$runner"
printf '%s\n' verified > "$output_dir/preflight/generated-runner-consistency.status"

cert="$output_dir/tls/public.test.crt.pem"
key="$output_dir/tls/public.test.key.pem"
openssl req -x509 -newkey ec -pkeyopt ec_paramgen_curve:P-256 -nodes \
  -subj /CN=public.test -addext subjectAltName=DNS:public.test -days 1 \
  -keyout "$key" -out "$cert" >/dev/null 2>&1
cert=$(realpath "$cert")
key=$(realpath "$key")
openssl x509 -in "$cert" -noout -checkhost public.test >/dev/null
expected_leaf_sha256=$(openssl x509 -in "$cert" -outform DER | sha256sum | awk '{print $1}')
[[ $expected_leaf_sha256 =~ ^[0-9a-f]{64}$ ]]

write_pair_hashes() {
  local destination=$1
  python3 - "$destination" "$0" "$supervisor" "$validator" "$copy_record_validator" "$runner" "$frozen_protocol_runner" "$frozen_copy_runner" \
    "$wan_protocol_runner_patch" "$copy_runner_patch" "$monotonic_runner_patch" "$read_wait_runner_patch" "$server_capacity_patch" "$cleanup_status_patch" "$approved_runner" "$go_patch" "$go_helper" "$copy_overlay" \
    "$monotonic_observer_patch" "$monotonic_validator_patch" "$monotonic_arm_validator_patch" "$copy_design" \
    "$monotonic_build_manifest" "$monotonic_audited_build_manifest" "$audited_build_manifest" "$final_source_attestation" \
    "$runner_reconstruction_attestation" "$integration_notes" "$decision_map" "$observer_patch" "$observer_checkpoint" "$focused_checkpoint" \
    "$capacity_source_checkpoint" "$capacity_source_checkpoint_manifest" "$capacity_focused_checkpoint" "$capacity_audited_build_manifest" \
    "$scope_entrypoint" "$scope_sentinel" "$scope_validator" "$scope_resource_validator" "$scope_probe_attestation" "$trace_analyzer" "$trace_analyzer_schema" \
    "$trace_analyzer_checkpoint" "$trace_parser_wrapper" "$trace_canonicalizer" "$trace_format" \
    "$trace_focused_manifest" "$trace_focused_summary" "$trace_lifecycle_manifest" "$trace_lifecycle_summary" \
    "$runner_integration_design" "$static_contract" "$resource_contract" "$traced_transfer_contract" "$arm_validator_contract" \
    "$cleanup_status_contract" "$cleanup_status_body" "$cleanup_status_holder" "$all_contracts" \
    "$server_binary" "$aggregate_agent_binary" "$cert" "$key" \
    "$server_source_ref" "$aggregate_agent_source_ref" "$expected_leaf_sha256" <<'PY'
import hashlib, json, pathlib, sys
(destination, wrapper, supervisor, validator, copy_record_validator, runner, frozen_protocol_runner, frozen_copy_runner,
 wan_protocol_runner_patch, copy_runner_patch, monotonic_runner_patch, read_wait_runner_patch, server_capacity_patch, cleanup_status_patch, approved, go_patch, go_helper, copy_overlay,
 monotonic_observer_patch, monotonic_validator_patch, monotonic_arm_validator_patch, copy_design,
 monotonic_build_manifest, monotonic_audited_build_manifest, audited_build_manifest,
 final_source_attestation, runner_reconstruction_attestation, integration_notes, decision_map, observer_patch, observer_checkpoint,
 focused_checkpoint, capacity_source_checkpoint, capacity_source_checkpoint_manifest, capacity_focused_checkpoint, capacity_audited_build_manifest,
 scope_entrypoint, scope_sentinel, scope_validator, scope_resource_validator, scope_probe_attestation, trace_analyzer, trace_analyzer_schema,
 trace_analyzer_checkpoint, trace_parser_wrapper, trace_canonicalizer, trace_format,
 trace_focused_manifest, trace_focused_summary, trace_lifecycle_manifest, trace_lifecycle_summary,
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
        "wrapper": sha(wrapper), "supervisor": sha(supervisor), "validator": sha(validator),
        "copy_record_validator": sha(copy_record_validator), "runner": sha(runner), "frozen_protocol_runner": sha(frozen_protocol_runner),
        "frozen_copy_runner": sha(frozen_copy_runner), "wan_protocol_runner_patch": sha(wan_protocol_runner_patch),
        "copy_runner_patch": sha(copy_runner_patch), "monotonic_runner_patch": sha(monotonic_runner_patch),
        "read_wait_runner_patch": sha(read_wait_runner_patch), "server_capacity_snapshot_patch": sha(server_capacity_patch),
        "cleanup_status_patch": sha(cleanup_status_patch),
        "approved_lifecycle_runner": sha(approved), "go_base_patch": sha(go_patch), "go_helper": sha(go_helper),
        "copy_attribution_overlay_patch": sha(copy_overlay), "copy_attribution_design": sha(copy_design),
        "monotonic_observer_patch": sha(monotonic_observer_patch), "monotonic_validator_patch": sha(monotonic_validator_patch),
        "monotonic_arm_validator_patch": sha(monotonic_arm_validator_patch),
        "monotonic_build_manifest": sha(monotonic_build_manifest),
        "monotonic_audited_build_manifest": sha(monotonic_audited_build_manifest),
        "read_wait_audited_build_manifest": sha(audited_build_manifest),
        "final_source_attestation": sha(final_source_attestation),
        "runner_reconstruction_attestation": sha(runner_reconstruction_attestation),
        "integration_notes": sha(integration_notes), "copy_timing_decision_map": sha(decision_map),
        "read_wait_observer_patch": sha(observer_patch), "read_wait_observer_checkpoint": sha(observer_checkpoint),
        "read_wait_focused_checkpoint": sha(focused_checkpoint), "server_scope_entrypoint": sha(scope_entrypoint),
        "capacity_source_checkpoint": sha(capacity_source_checkpoint), "capacity_source_checkpoint_manifest": sha(capacity_source_checkpoint_manifest),
        "capacity_focused_checkpoint": sha(capacity_focused_checkpoint), "capacity_audited_build_manifest": sha(capacity_audited_build_manifest),
        "server_scope_sentinel": sha(scope_sentinel), "server_scope_validator": sha(scope_validator),
        "server_scope_resource_validator": sha(scope_resource_validator),
        "delegated_scope_probe_attestation": sha(scope_probe_attestation), "trace_analyzer": sha(trace_analyzer),
        "trace_analyzer_schema": sha(trace_analyzer_schema), "trace_compact_checkpoint": sha(trace_analyzer_checkpoint),
        "trace_parser_adapter": sha(trace_parser_wrapper), "trace_canonicalizer": sha(trace_canonicalizer),
        "trace_format": sha(trace_format), "trace_focused_manifest": sha(trace_focused_manifest),
        "trace_focused_summary": sha(trace_focused_summary), "trace_lifecycle_manifest": sha(trace_lifecycle_manifest),
        "trace_lifecycle_summary": sha(trace_lifecycle_summary), "server_binary": sha(server),
        "runner_integration_design": sha(runner_integration_design), "static_contract": sha(static_contract),
        "resource_contract": sha(resource_contract), "traced_transfer_contract": sha(traced_transfer_contract),
        "arm_validator_contract": sha(arm_validator_contract), "cleanup_status_contract": sha(cleanup_status_contract),
        "cleanup_status_body": sha(cleanup_status_body), "cleanup_status_holder": sha(cleanup_status_holder),
        "all_contracts": sha(all_contracts),
        "aggregate_agent_binary": sha(agent), "certificate_pem": sha(cert), "private_key_pem": sha(key),
    },
}
pathlib.Path(destination).write_text(json.dumps(value, indent=2, sort_keys=True) + "\n")
PY
}
write_pair_hashes "$output_dir/pre-hashes.json"

common=(
  --server-binary "$server_binary"
  --aggregate-agent-binary "$aggregate_agent_binary"
  --server-source-ref "$server_source_ref"
  --aggregate-agent-source-ref "$aggregate_agent_source_ref"
  --cert "$cert"
  --key "$key"
  --expected-leaf-sha256 "$expected_leaf_sha256"
)
((smoke == 0)) || common+=(--smoke)

set +e
"$supervisor" "${common[@]}" --protocol h1 --output-dir "$output_dir/h1" \
  > "$output_dir/h1-supervisor.stdout.log" 2> "$output_dir/h1-supervisor.stderr.log"
h1_status=$?
set -e
h1_safe=0
if [[ -f $output_dir/h1/supervisor-lifecycle.json && -f $output_dir/h1/outer-namespaces.json ]] && \
  jq -e '.group_joined == true and .forced == false and
         .server_scope.systemd_run_status == 0 and .server_scope.resource_status == 0 and .server_scope.unit_joined == true and
         .server_scope.unit_query_status == 0 and .server_scope.unit_active_state != "failed" and
         .trace_reduction.adapter_status == 0 and .trace_reduction.parser_status == 0 and
         .trace_reduction.canonicalizer_status == 0 and .trace_reduction.analyzer_status == 0' "$output_dir/h1/supervisor-lifecycle.json" >/dev/null && \
  jq -e '.all_private == true and .shell_pid_is_one == true and .python_parent_is_one == true and
         .cgroup_namespace_unchanged == true and .owned.cgroup == .parent.cgroup' "$output_dir/h1/outer-namespaces.json" >/dev/null; then
  h1_safe=1
fi
printf '%s\n' "$h1_status" > "$output_dir/h1-arm.status"
printf '%s\n' "$h1_safe" > "$output_dir/h1-safe-to-continue.status"

h2_started=0
h2_status=125
if ((h1_safe)); then
  h2_started=1
  set +e
  "$supervisor" "${common[@]}" --protocol h2 --output-dir "$output_dir/h2" \
    > "$output_dir/h2-supervisor.stdout.log" 2> "$output_dir/h2-supervisor.stderr.log"
  h2_status=$?
  set -e
else
  printf '%s\n' "H2 not started because H1 outer namespace/process-group cleanup was not proved" > "$output_dir/h2-not-started.txt"
fi
printf '%s\n' "$h2_started" > "$output_dir/h2-started.status"
printf '%s\n' "$h2_status" > "$output_dir/h2-arm.status"
write_pair_hashes "$output_dir/post-hashes.json"

pair_hashes_equal=0
cmp "$output_dir/pre-hashes.json" "$output_dir/post-hashes.json" && pair_hashes_equal=1
python3 - "$output_dir/final-status.json" "$h1_status" "$h1_safe" "$h2_started" "$h2_status" \
  "$pair_hashes_equal" "$smoke" "$output_dir" <<'PY'
import json, pathlib, sys, time
destination, h1, h1_safe, h2_started, h2, hashes_equal, smoke, root = sys.argv[1:]
root = pathlib.Path(root)
def accepted(arm):
    path = root / arm / "attestation.json"
    return path.is_file() and json.loads(path.read_text()).get("accepted") is True
value = {
    "schema_version": 1,
    "diagnostic_only": True,
    "order": ["h1", "h2"],
    "same_binaries_both_arms": True,
    "same_verified_certificate_both_arms": True,
    "fresh_outer_topology_per_started_arm": True,
    "smoke": smoke == "1",
    "h1": {"status": int(h1), "safe_to_continue": h1_safe == "1", "accepted": accepted("h1")},
    "h2": {"started": h2_started == "1", "status": int(h2), "accepted": accepted("h2")},
    "pre_post_hashes_equal": hashes_equal == "1",
}
value["accepted"] = (value["h1"]["status"] == 0 and value["h1"]["safe_to_continue"] and
                     value["h1"]["accepted"] and value["h2"]["started"] and
                     value["h2"]["status"] == 0 and value["h2"]["accepted"] and
                     value["pre_post_hashes_equal"])
value["finished_unix_nano"] = time.time_ns()
pathlib.Path(destination).write_text(json.dumps(value, indent=2, sort_keys=True) + "\n")
PY
printf '%s\n' complete > "$output_dir/FINAL"
((h1_status == 0 && h1_safe == 1 && h2_started == 1 && h2_status == 0 && pair_hashes_equal == 1))
