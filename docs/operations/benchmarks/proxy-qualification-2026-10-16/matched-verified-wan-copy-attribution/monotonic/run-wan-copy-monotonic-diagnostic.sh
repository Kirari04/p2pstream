#!/usr/bin/env bash
set -Eeuo pipefail

script_dir=$(cd "$(dirname "$0")" && pwd)
supervisor="$script_dir/run-wan-copy-monotonic-arm.sh"
validator="$script_dir/validate-wan-copy-monotonic-arm.py"
copy_record_validator="$script_dir/validate-copy-record.py"
runner="$script_dir/generated/qualify-proxy-agent-growth-wan-copy-monotonic.sh"
frozen_protocol_runner="$script_dir/../wan-protocol-source/generated/qualify-proxy-agent-growth-wan-protocol.sh"
frozen_copy_runner="$script_dir/../wan-copy-attribution-source/generated/qualify-proxy-agent-growth-wan-copy-attribution.sh"
wan_protocol_runner_patch="$script_dir/../wan-protocol-source/wan-protocol-runner.patch"
copy_runner_patch="$script_dir/../wan-copy-attribution-source/copy-attribution-runner.patch"
monotonic_runner_patch="$script_dir/monotonic-runner.patch"
approved_runner="$script_dir/../current-wan-prep/runner/qualify-proxy-agent-growth-owned.sh"
go_patch="$script_dir/../wan-protocol-source/proxy-qualification-base.patch"
go_helper="$script_dir/../wan-protocol-source/proxy_qualification_wan_protocol_test.go"
copy_overlay="$script_dir/../wan-copy-attribution-source/copy-attribution-overlay.patch"
monotonic_observer_patch="$script_dir/monotonic-observer.patch"
monotonic_validator_patch="$script_dir/monotonic-validator-contract.patch"
monotonic_arm_validator_patch="$script_dir/monotonic-arm-validator.patch"
copy_design="$script_dir/DESIGN.md"
build_manifest="$script_dir/provenance/monotonic-build-manifest.json"
audited_build_manifest="$script_dir/provenance/audited-server-build-manifest.json"
final_source_attestation="$script_dir/provenance/final-source-binary-toolchain-attestation.json"
runner_reconstruction_attestation="$script_dir/provenance/runner-reconstruction-attestation.json"
integration_notes="$script_dir/INTEGRATION-NOTES.md"
decision_map="$script_dir/../current-wan-prep/COPY-TIMING-DECISION-MAP.md"
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

[[ -x $server_binary && -x $aggregate_agent_binary && -x $supervisor && -x $validator && -x $copy_record_validator && -x $runner ]] || {
  echo "executable server, agent, runner, supervisor, arm validator, and copy validator are required" >&2
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
output_dir=$(realpath -m "$output_dir")
allowed_root=$(realpath -m "$script_dir/runs")
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
    "$wan_protocol_runner_patch" "$copy_runner_patch" "$monotonic_runner_patch" "$approved_runner" "$go_patch" "$go_helper" "$copy_overlay" \
    "$monotonic_observer_patch" "$monotonic_validator_patch" "$monotonic_arm_validator_patch" "$copy_design" \
    "$build_manifest" "$audited_build_manifest" "$final_source_attestation" "$runner_reconstruction_attestation" "$integration_notes" "$decision_map" \
    "$server_binary" "$aggregate_agent_binary" "$cert" "$key" \
    "$server_source_ref" "$aggregate_agent_source_ref" "$expected_leaf_sha256" <<'PY'
import hashlib, json, pathlib, sys
(destination, wrapper, supervisor, validator, copy_record_validator, runner, frozen_protocol_runner, frozen_copy_runner,
 wan_protocol_runner_patch, copy_runner_patch, monotonic_runner_patch, approved, go_patch, go_helper, copy_overlay,
 monotonic_observer_patch, monotonic_validator_patch, monotonic_arm_validator_patch, copy_design, build_manifest, audited_build_manifest,
 final_source_attestation, runner_reconstruction_attestation, integration_notes, decision_map,
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
        "approved_lifecycle_runner": sha(approved), "go_base_patch": sha(go_patch), "go_helper": sha(go_helper),
        "copy_attribution_overlay_patch": sha(copy_overlay), "copy_attribution_design": sha(copy_design),
        "monotonic_observer_patch": sha(monotonic_observer_patch), "monotonic_validator_patch": sha(monotonic_validator_patch),
        "monotonic_arm_validator_patch": sha(monotonic_arm_validator_patch),
        "diagnostic_build_manifest": sha(build_manifest), "audited_build_manifest": sha(audited_build_manifest),
        "final_source_attestation": sha(final_source_attestation),
        "runner_reconstruction_attestation": sha(runner_reconstruction_attestation),
        "integration_notes": sha(integration_notes), "copy_timing_decision_map": sha(decision_map), "server_binary": sha(server),
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
  jq -e '.group_joined == true and .forced == false and .invalidates_success == false' "$output_dir/h1/supervisor-lifecycle.json" >/dev/null && \
  jq -e '.all_private == true and .shell_pid_is_one == true and .python_parent_is_one == true' "$output_dir/h1/outer-namespaces.json" >/dev/null; then
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
