#!/usr/bin/env bash
set -Eeuo pipefail
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
aggregate_agent_binary=/home/user/.codex/worktrees/astra-proxy-performance/p2pstream/tmp/wan-terminal-attribution-20261017/wiring-window/arm-outcome-fixture/bound-aggregate_agent_binary
all_contracts=/home/user/.codex/worktrees/astra-proxy-performance/p2pstream/tmp/wan-terminal-attribution-20261017/wiring-window/arm-outcome-fixture/bound-all_contracts
approved_runner=/home/user/.codex/worktrees/astra-proxy-performance/p2pstream/tmp/wan-terminal-attribution-20261017/wiring-window/arm-outcome-fixture/bound-approved_runner
arm_outcome_validator=/home/user/.codex/worktrees/astra-proxy-performance/p2pstream/tmp/wan-terminal-attribution-20261017/wiring-window/arm-outcome-fixture/bound-arm_outcome_validator
arm_validator_contract=/home/user/.codex/worktrees/astra-proxy-performance/p2pstream/tmp/wan-terminal-attribution-20261017/wiring-window/arm-outcome-fixture/bound-arm_validator_contract
audited_build_manifest=/home/user/.codex/worktrees/astra-proxy-performance/p2pstream/tmp/wan-terminal-attribution-20261017/wiring-window/arm-outcome-fixture/bound-audited_build_manifest
base_cp7_checkpoint=/home/user/.codex/worktrees/astra-proxy-performance/p2pstream/tmp/wan-terminal-attribution-20261017/wiring-window/arm-outcome-fixture/bound-base_cp7_checkpoint
base_cp7_reviewed_manifest=/home/user/.codex/worktrees/astra-proxy-performance/p2pstream/tmp/wan-terminal-attribution-20261017/wiring-window/arm-outcome-fixture/bound-base_cp7_reviewed_manifest
base_cp7_runner=/home/user/.codex/worktrees/astra-proxy-performance/p2pstream/tmp/wan-terminal-attribution-20261017/wiring-window/arm-outcome-fixture/bound-base_cp7_runner
byte_credit_adapter_checkpoint=/home/user/.codex/worktrees/astra-proxy-performance/p2pstream/tmp/wan-terminal-attribution-20261017/wiring-window/arm-outcome-fixture/bound-byte_credit_adapter_checkpoint
byte_credit_adapter_contract_ledger=/home/user/.codex/worktrees/astra-proxy-performance/p2pstream/tmp/wan-terminal-attribution-20261017/wiring-window/arm-outcome-fixture/bound-byte_credit_adapter_contract_ledger
byte_credit_ordinary_proof_receipt=/home/user/.codex/worktrees/astra-proxy-performance/p2pstream/tmp/wan-terminal-attribution-20261017/wiring-window/arm-outcome-fixture/bound-byte_credit_ordinary_proof_receipt
byte_credit_protocol_checkpoint=/home/user/.codex/worktrees/astra-proxy-performance/p2pstream/tmp/wan-terminal-attribution-20261017/wiring-window/arm-outcome-fixture/bound-byte_credit_protocol_checkpoint
byte_credit_protocol_focused_ledger=/home/user/.codex/worktrees/astra-proxy-performance/p2pstream/tmp/wan-terminal-attribution-20261017/wiring-window/arm-outcome-fixture/bound-byte_credit_protocol_focused_ledger
byte_credit_protocol_focused_outer=/home/user/.codex/worktrees/astra-proxy-performance/p2pstream/tmp/wan-terminal-attribution-20261017/wiring-window/arm-outcome-fixture/bound-byte_credit_protocol_focused_outer
byte_credit_runner_contract=/home/user/.codex/worktrees/astra-proxy-performance/p2pstream/tmp/wan-terminal-attribution-20261017/wiring-window/arm-outcome-fixture/bound-byte_credit_runner_contract
byte_credit_validator=/home/user/.codex/worktrees/astra-proxy-performance/p2pstream/tmp/wan-terminal-attribution-20261017/wiring-window/arm-outcome-fixture/bound-byte_credit_validator
capacity_audited_build_manifest=/home/user/.codex/worktrees/astra-proxy-performance/p2pstream/tmp/wan-terminal-attribution-20261017/wiring-window/arm-outcome-fixture/bound-capacity_audited_build_manifest
capacity_focused_checkpoint=/home/user/.codex/worktrees/astra-proxy-performance/p2pstream/tmp/wan-terminal-attribution-20261017/wiring-window/arm-outcome-fixture/bound-capacity_focused_checkpoint
capacity_source_checkpoint=/home/user/.codex/worktrees/astra-proxy-performance/p2pstream/tmp/wan-terminal-attribution-20261017/wiring-window/arm-outcome-fixture/bound-capacity_source_checkpoint
capacity_source_checkpoint_manifest=/home/user/.codex/worktrees/astra-proxy-performance/p2pstream/tmp/wan-terminal-attribution-20261017/wiring-window/arm-outcome-fixture/bound-capacity_source_checkpoint_manifest
cert=/home/user/.codex/worktrees/astra-proxy-performance/p2pstream/tmp/wan-terminal-attribution-20261017/wiring-window/arm-outcome-fixture/bound-cert
cleanup_status_body=/home/user/.codex/worktrees/astra-proxy-performance/p2pstream/tmp/wan-terminal-attribution-20261017/wiring-window/arm-outcome-fixture/bound-cleanup_status_body
cleanup_status_contract=/home/user/.codex/worktrees/astra-proxy-performance/p2pstream/tmp/wan-terminal-attribution-20261017/wiring-window/arm-outcome-fixture/bound-cleanup_status_contract
cleanup_status_holder=/home/user/.codex/worktrees/astra-proxy-performance/p2pstream/tmp/wan-terminal-attribution-20261017/wiring-window/arm-outcome-fixture/bound-cleanup_status_holder
cleanup_status_patch=/home/user/.codex/worktrees/astra-proxy-performance/p2pstream/tmp/wan-terminal-attribution-20261017/wiring-window/arm-outcome-fixture/bound-cleanup_status_patch
combined_build_receipt=/home/user/.codex/worktrees/astra-proxy-performance/p2pstream/tmp/wan-terminal-attribution-20261017/wiring-window/arm-outcome-fixture/bound-combined_build_receipt
combined_checkpoint_manifest=/home/user/.codex/worktrees/astra-proxy-performance/p2pstream/tmp/wan-terminal-attribution-20261017/wiring-window/arm-outcome-fixture/bound-combined_checkpoint_manifest
combined_contract_ledger=/home/user/.codex/worktrees/astra-proxy-performance/p2pstream/tmp/wan-terminal-attribution-20261017/wiring-window/arm-outcome-fixture/bound-combined_contract_ledger
copy_design=/home/user/.codex/worktrees/astra-proxy-performance/p2pstream/tmp/wan-terminal-attribution-20261017/wiring-window/arm-outcome-fixture/bound-copy_design
copy_overlay=/home/user/.codex/worktrees/astra-proxy-performance/p2pstream/tmp/wan-terminal-attribution-20261017/wiring-window/arm-outcome-fixture/bound-copy_overlay
copy_record_validator=/home/user/.codex/worktrees/astra-proxy-performance/p2pstream/tmp/wan-terminal-attribution-20261017/wiring-window/arm-outcome-fixture/bound-copy_record_validator
copy_runner_patch=/home/user/.codex/worktrees/astra-proxy-performance/p2pstream/tmp/wan-terminal-attribution-20261017/wiring-window/arm-outcome-fixture/bound-copy_runner_patch
decision_map=/home/user/.codex/worktrees/astra-proxy-performance/p2pstream/tmp/wan-terminal-attribution-20261017/wiring-window/arm-outcome-fixture/bound-decision_map
final_source_attestation=/home/user/.codex/worktrees/astra-proxy-performance/p2pstream/tmp/wan-terminal-attribution-20261017/wiring-window/arm-outcome-fixture/bound-final_source_attestation
focused_checkpoint=/home/user/.codex/worktrees/astra-proxy-performance/p2pstream/tmp/wan-terminal-attribution-20261017/wiring-window/arm-outcome-fixture/bound-focused_checkpoint
frozen_copy_runner=/home/user/.codex/worktrees/astra-proxy-performance/p2pstream/tmp/wan-terminal-attribution-20261017/wiring-window/arm-outcome-fixture/bound-frozen_copy_runner
frozen_protocol_runner=/home/user/.codex/worktrees/astra-proxy-performance/p2pstream/tmp/wan-terminal-attribution-20261017/wiring-window/arm-outcome-fixture/bound-frozen_protocol_runner
go_helper=/home/user/.codex/worktrees/astra-proxy-performance/p2pstream/tmp/wan-terminal-attribution-20261017/wiring-window/arm-outcome-fixture/bound-go_helper
go_patch=/home/user/.codex/worktrees/astra-proxy-performance/p2pstream/tmp/wan-terminal-attribution-20261017/wiring-window/arm-outcome-fixture/bound-go_patch
history_build_outer_ledger=/home/user/.codex/worktrees/astra-proxy-performance/p2pstream/tmp/wan-terminal-attribution-20261017/wiring-window/arm-outcome-fixture/bound-history_build_outer_ledger
history_build_records_ledger=/home/user/.codex/worktrees/astra-proxy-performance/p2pstream/tmp/wan-terminal-attribution-20261017/wiring-window/arm-outcome-fixture/bound-history_build_records_ledger
history_producer_checkpoint=/home/user/.codex/worktrees/astra-proxy-performance/p2pstream/tmp/wan-terminal-attribution-20261017/wiring-window/arm-outcome-fixture/bound-history_producer_checkpoint
history_source_map=/home/user/.codex/worktrees/astra-proxy-performance/p2pstream/tmp/wan-terminal-attribution-20261017/wiring-window/arm-outcome-fixture/bound-history_source_map
history_validator_checkpoint=/home/user/.codex/worktrees/astra-proxy-performance/p2pstream/tmp/wan-terminal-attribution-20261017/wiring-window/arm-outcome-fixture/bound-history_validator_checkpoint
history_validator_contract_ledger=/home/user/.codex/worktrees/astra-proxy-performance/p2pstream/tmp/wan-terminal-attribution-20261017/wiring-window/arm-outcome-fixture/bound-history_validator_contract_ledger
history_validator_contract_outer=/home/user/.codex/worktrees/astra-proxy-performance/p2pstream/tmp/wan-terminal-attribution-20261017/wiring-window/arm-outcome-fixture/bound-history_validator_contract_outer
integration_notes=/home/user/.codex/worktrees/astra-proxy-performance/p2pstream/tmp/wan-terminal-attribution-20261017/wiring-window/arm-outcome-fixture/bound-integration_notes
key=/home/user/.codex/worktrees/astra-proxy-performance/p2pstream/tmp/wan-terminal-attribution-20261017/wiring-window/arm-outcome-fixture/bound-key
monotonic_arm_validator_patch=/home/user/.codex/worktrees/astra-proxy-performance/p2pstream/tmp/wan-terminal-attribution-20261017/wiring-window/arm-outcome-fixture/bound-monotonic_arm_validator_patch
monotonic_audited_build_manifest=/home/user/.codex/worktrees/astra-proxy-performance/p2pstream/tmp/wan-terminal-attribution-20261017/wiring-window/arm-outcome-fixture/bound-monotonic_audited_build_manifest
monotonic_build_manifest=/home/user/.codex/worktrees/astra-proxy-performance/p2pstream/tmp/wan-terminal-attribution-20261017/wiring-window/arm-outcome-fixture/bound-monotonic_build_manifest
monotonic_observer_patch=/home/user/.codex/worktrees/astra-proxy-performance/p2pstream/tmp/wan-terminal-attribution-20261017/wiring-window/arm-outcome-fixture/bound-monotonic_observer_patch
monotonic_runner_patch=/home/user/.codex/worktrees/astra-proxy-performance/p2pstream/tmp/wan-terminal-attribution-20261017/wiring-window/arm-outcome-fixture/bound-monotonic_runner_patch
monotonic_validator_patch=/home/user/.codex/worktrees/astra-proxy-performance/p2pstream/tmp/wan-terminal-attribution-20261017/wiring-window/arm-outcome-fixture/bound-monotonic_validator_patch
observer_checkpoint=/home/user/.codex/worktrees/astra-proxy-performance/p2pstream/tmp/wan-terminal-attribution-20261017/wiring-window/arm-outcome-fixture/bound-observer_checkpoint
observer_patch=/home/user/.codex/worktrees/astra-proxy-performance/p2pstream/tmp/wan-terminal-attribution-20261017/wiring-window/arm-outcome-fixture/bound-observer_patch
raw_closure_validator=/home/user/.codex/worktrees/astra-proxy-performance/p2pstream/tmp/wan-terminal-attribution-20261017/wiring-window/arm-outcome-fixture/bound-raw_closure_validator
raw_ordinary_proof_receipt=/home/user/.codex/worktrees/astra-proxy-performance/p2pstream/tmp/wan-terminal-attribution-20261017/wiring-window/arm-outcome-fixture/bound-raw_ordinary_proof_receipt
raw_write_validator=/home/user/.codex/worktrees/astra-proxy-performance/p2pstream/tmp/wan-terminal-attribution-20261017/wiring-window/arm-outcome-fixture/bound-raw_write_validator
read_wait_runner_patch=/home/user/.codex/worktrees/astra-proxy-performance/p2pstream/tmp/wan-terminal-attribution-20261017/wiring-window/arm-outcome-fixture/bound-read_wait_runner_patch
resource_contract=/home/user/.codex/worktrees/astra-proxy-performance/p2pstream/tmp/wan-terminal-attribution-20261017/wiring-window/arm-outcome-fixture/bound-resource_contract
reviewed_integration_manifest=/home/user/.codex/worktrees/astra-proxy-performance/p2pstream/tmp/wan-terminal-attribution-20261017/wiring-window/arm-outcome-fixture/bound-reviewed_integration_manifest
reviewed_integration_verifier=/home/user/.codex/worktrees/astra-proxy-performance/p2pstream/tmp/wan-terminal-attribution-20261017/wiring-window/arm-outcome-fixture/bound-reviewed_integration_verifier
runner=/home/user/.codex/worktrees/astra-proxy-performance/p2pstream/tmp/wan-terminal-attribution-20261017/wiring-window/arm-outcome-fixture/bound-runner
runner_integration_design=/home/user/.codex/worktrees/astra-proxy-performance/p2pstream/tmp/wan-terminal-attribution-20261017/wiring-window/arm-outcome-fixture/bound-runner_integration_design
runner_reconstruction_attestation=/home/user/.codex/worktrees/astra-proxy-performance/p2pstream/tmp/wan-terminal-attribution-20261017/wiring-window/arm-outcome-fixture/bound-runner_reconstruction_attestation
scope_entrypoint=/home/user/.codex/worktrees/astra-proxy-performance/p2pstream/tmp/wan-terminal-attribution-20261017/wiring-window/arm-outcome-fixture/bound-scope_entrypoint
scope_probe_attestation=/home/user/.codex/worktrees/astra-proxy-performance/p2pstream/tmp/wan-terminal-attribution-20261017/wiring-window/arm-outcome-fixture/bound-scope_probe_attestation
scope_resource_validator=/home/user/.codex/worktrees/astra-proxy-performance/p2pstream/tmp/wan-terminal-attribution-20261017/wiring-window/arm-outcome-fixture/bound-scope_resource_validator
scope_sentinel=/home/user/.codex/worktrees/astra-proxy-performance/p2pstream/tmp/wan-terminal-attribution-20261017/wiring-window/arm-outcome-fixture/bound-scope_sentinel
scope_validator=/home/user/.codex/worktrees/astra-proxy-performance/p2pstream/tmp/wan-terminal-attribution-20261017/wiring-window/arm-outcome-fixture/bound-scope_validator
sender_build_manifest=/home/user/.codex/worktrees/astra-proxy-performance/p2pstream/tmp/wan-terminal-attribution-20261017/wiring-window/arm-outcome-fixture/bound-sender_build_manifest
sender_control_patch=/home/user/.codex/worktrees/astra-proxy-performance/p2pstream/tmp/wan-terminal-attribution-20261017/wiring-window/arm-outcome-fixture/bound-sender_control_patch
sender_harness_receipt=/home/user/.codex/worktrees/astra-proxy-performance/p2pstream/tmp/wan-terminal-attribution-20261017/wiring-window/arm-outcome-fixture/bound-sender_harness_receipt
sender_integration_checkpoint=/home/user/.codex/worktrees/astra-proxy-performance/p2pstream/tmp/wan-terminal-attribution-20261017/wiring-window/arm-outcome-fixture/bound-sender_integration_checkpoint
sender_observer_checkpoint=/home/user/.codex/worktrees/astra-proxy-performance/p2pstream/tmp/wan-terminal-attribution-20261017/wiring-window/arm-outcome-fixture/bound-sender_observer_checkpoint
sender_observer_patch=/home/user/.codex/worktrees/astra-proxy-performance/p2pstream/tmp/wan-terminal-attribution-20261017/wiring-window/arm-outcome-fixture/bound-sender_observer_patch
sender_runner_patch=/home/user/.codex/worktrees/astra-proxy-performance/p2pstream/tmp/wan-terminal-attribution-20261017/wiring-window/arm-outcome-fixture/bound-sender_runner_patch
sender_snapshot_validator=/home/user/.codex/worktrees/astra-proxy-performance/p2pstream/tmp/wan-terminal-attribution-20261017/wiring-window/arm-outcome-fixture/bound-sender_snapshot_validator
sender_wrapper_contract=/home/user/.codex/worktrees/astra-proxy-performance/p2pstream/tmp/wan-terminal-attribution-20261017/wiring-window/arm-outcome-fixture/bound-sender_wrapper_contract
server_binary=/home/user/.codex/worktrees/astra-proxy-performance/p2pstream/tmp/wan-terminal-attribution-20261017/wiring-window/arm-outcome-fixture/bound-server_binary
server_capacity_patch=/home/user/.codex/worktrees/astra-proxy-performance/p2pstream/tmp/wan-terminal-attribution-20261017/wiring-window/arm-outcome-fixture/bound-server_capacity_patch
server_growth_checkpoint=/home/user/.codex/worktrees/astra-proxy-performance/p2pstream/tmp/wan-terminal-attribution-20261017/wiring-window/arm-outcome-fixture/bound-server_growth_checkpoint
server_growth_focused_receipt=/home/user/.codex/worktrees/astra-proxy-performance/p2pstream/tmp/wan-terminal-attribution-20261017/wiring-window/arm-outcome-fixture/bound-server_growth_focused_receipt
server_growth_overlay_patch=/home/user/.codex/worktrees/astra-proxy-performance/p2pstream/tmp/wan-terminal-attribution-20261017/wiring-window/arm-outcome-fixture/bound-server_growth_overlay_patch
server_growth_runner_patch=/home/user/.codex/worktrees/astra-proxy-performance/p2pstream/tmp/wan-terminal-attribution-20261017/wiring-window/arm-outcome-fixture/bound-server_growth_runner_patch
server_growth_validator=/home/user/.codex/worktrees/astra-proxy-performance/p2pstream/tmp/wan-terminal-attribution-20261017/wiring-window/arm-outcome-fixture/bound-server_growth_validator
static_contract=/home/user/.codex/worktrees/astra-proxy-performance/p2pstream/tmp/wan-terminal-attribution-20261017/wiring-window/arm-outcome-fixture/bound-static_contract
tcp_build_outer_ledger=/home/user/.codex/worktrees/astra-proxy-performance/p2pstream/tmp/wan-terminal-attribution-20261017/wiring-window/arm-outcome-fixture/bound-tcp_build_outer_ledger
tcp_build_records_ledger=/home/user/.codex/worktrees/astra-proxy-performance/p2pstream/tmp/wan-terminal-attribution-20261017/wiring-window/arm-outcome-fixture/bound-tcp_build_records_ledger
tcp_producer_checkpoint=/home/user/.codex/worktrees/astra-proxy-performance/p2pstream/tmp/wan-terminal-attribution-20261017/wiring-window/arm-outcome-fixture/bound-tcp_producer_checkpoint
tcp_source_map=/home/user/.codex/worktrees/astra-proxy-performance/p2pstream/tmp/wan-terminal-attribution-20261017/wiring-window/arm-outcome-fixture/bound-tcp_source_map
tcp_state_validator=/home/user/.codex/worktrees/astra-proxy-performance/p2pstream/tmp/wan-terminal-attribution-20261017/wiring-window/arm-outcome-fixture/bound-tcp_state_validator
tcp_validator_contract_ledger=/home/user/.codex/worktrees/astra-proxy-performance/p2pstream/tmp/wan-terminal-attribution-20261017/wiring-window/arm-outcome-fixture/bound-tcp_validator_contract_ledger
tcp_validator_contract_outer=/home/user/.codex/worktrees/astra-proxy-performance/p2pstream/tmp/wan-terminal-attribution-20261017/wiring-window/arm-outcome-fixture/bound-tcp_validator_contract_outer
terminal_attribution_validator=/home/user/.codex/worktrees/astra-proxy-performance/p2pstream/tmp/wan-terminal-attribution-20261017/wiring-window/arm-outcome-fixture/bound-terminal_attribution_validator
terminal_build_plan=/home/user/.codex/worktrees/astra-proxy-performance/p2pstream/tmp/wan-terminal-attribution-20261017/wiring-window/arm-outcome-fixture/bound-terminal_build_plan
terminal_build_receipt=/home/user/.codex/worktrees/astra-proxy-performance/p2pstream/tmp/wan-terminal-attribution-20261017/wiring-window/arm-outcome-fixture/bound-terminal_build_receipt
terminal_build_source_inventory=/home/user/.codex/worktrees/astra-proxy-performance/p2pstream/tmp/wan-terminal-attribution-20261017/wiring-window/arm-outcome-fixture/bound-terminal_build_source_inventory
terminal_build_verifier=/home/user/.codex/worktrees/astra-proxy-performance/p2pstream/tmp/wan-terminal-attribution-20261017/wiring-window/arm-outcome-fixture/bound-terminal_build_verifier
terminal_legacy_references=/home/user/.codex/worktrees/astra-proxy-performance/p2pstream/tmp/wan-terminal-attribution-20261017/wiring-window/arm-outcome-fixture/bound-terminal_legacy_references
validator=/home/user/.codex/worktrees/astra-proxy-performance/p2pstream/tmp/wan-terminal-attribution-20261017/wiring-window/arm-outcome-fixture/bound-validator
wan_protocol_runner_patch=/home/user/.codex/worktrees/astra-proxy-performance/p2pstream/tmp/wan-terminal-attribution-20261017/wiring-window/arm-outcome-fixture/bound-wan_protocol_runner_patch
server_source_ref=fixture-server-source
aggregate_agent_source_ref=fixture-agent-source
expected_leaf_sha256=aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa
size_mode=small
write_hashes /home/user/.codex/worktrees/astra-proxy-performance/p2pstream/tmp/wan-terminal-attribution-20261017/wiring-window/arm-outcome-fixture/pre-hashes.json
