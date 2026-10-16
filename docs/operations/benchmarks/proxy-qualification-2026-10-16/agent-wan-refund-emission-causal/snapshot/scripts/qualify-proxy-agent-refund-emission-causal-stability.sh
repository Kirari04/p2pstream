#!/usr/bin/env bash
# Run the predeclared aggregate-v1 gathered-versus-split refund-emission causal diagnostic.
set -Eeuo pipefail

root_dir=$(cd "$(dirname "$0")/.." && pwd)
runner="$root_dir/scripts/qualify-proxy-agent-refund-emission-causal.sh"
driver_binary=""
current_server_binary=""
split_server_binary=""
current_agent_binary=""
split_agent_binary=""
base_source_ref=""
current_source_ref=""
split_source_ref=""
common_scaffold=""
split_overlay=""
overlay_readme=""
source_hashes=""
build_provenance=""
build_manifest=""
output_dir=""
preflight_only=0

while (($#)); do
  case "$1" in
    --runner) runner=$2; shift 2 ;;
    --driver-binary) driver_binary=$2; shift 2 ;;
    --current-server-binary) current_server_binary=$2; shift 2 ;;
    --split-server-binary) split_server_binary=$2; shift 2 ;;
    --current-agent-binary) current_agent_binary=$2; shift 2 ;;
    --split-agent-binary) split_agent_binary=$2; shift 2 ;;
    --base-source-ref) base_source_ref=$2; shift 2 ;;
    --current-source-ref) current_source_ref=$2; shift 2 ;;
    --split-source-ref) split_source_ref=$2; shift 2 ;;
    --common-scaffold) common_scaffold=$2; shift 2 ;;
    --split-overlay) split_overlay=$2; shift 2 ;;
    --overlay-readme) overlay_readme=$2; shift 2 ;;
    --source-hashes) source_hashes=$2; shift 2 ;;
    --build-provenance) build_provenance=$2; shift 2 ;;
    --build-manifest) build_manifest=$2; shift 2 ;;
    --output-dir) output_dir=$2; shift 2 ;;
    --preflight-only) preflight_only=1; shift ;;
    *) echo "unknown argument: $1" >&2; exit 2 ;;
  esac
done

expected_base_ref=5eeccaaa797254d7bde4db9854a337a02c3bc28e
expected_current_source_ref=5eeccaaa797254d7bde4db9854a337a02c3bc28e
expected_split_source_ref=5eeccaaa797254d7bde4db9854a337a02c3bc28e
expected_common_scaffold_sha256=e78b384e8cf2fa28339ca2794dd32aaf57053a2b07aeca873fe4f2430a40c6f7
expected_split_overlay_sha256=28f4c3d5ac08d32c0e146dde3b1502a2cd8250be200da6c4b7e002eeb02386ed
expected_overlay_readme_sha256=b9ff483187880bf4c395cccfc77de6604cf1b58c10053ba967472a5cad1b30e3
expected_source_hashes_sha256=87ff187214faa13add9a54bd46a21c3a216fb1158f6d57c9f1cd6982609dea4a

[[ -f $runner && -f $common_scaffold && -f $split_overlay && -f $overlay_readme && -f $source_hashes && -f $build_provenance && -f $build_manifest ]] || {
  echo "runner, common scaffold, split overlay, overlay README, source hashes, build provenance and build manifest must be readable files" >&2
  exit 2
}
[[ -x $driver_binary && -x $current_server_binary && -x $split_server_binary &&
    -x $current_agent_binary && -x $split_agent_binary ]] || {
  echo "driver and both endpoint server/agent binary pairs must be executable" >&2
  exit 2
}
[[ $base_source_ref == "$expected_base_ref" ]] || {
  echo "application and harness source must be the frozen 5eeccaa base" >&2
  exit 2
}
[[ $current_source_ref == "$expected_current_source_ref" ]] || {
  echo "gathered-control identity must use the frozen 5eeccaa base" >&2
  exit 2
}
[[ $split_source_ref == "$expected_split_source_ref" ]] || {
  echo "split-control identity must use the frozen 5eeccaa base" >&2
  exit 2
}
for tool in setsid sha256sum python3 realpath find go grep jq; do
  command -v "$tool" >/dev/null || { echo "$tool is required" >&2; exit 2; }
done

common_scaffold_sha256=$(sha256sum "$common_scaffold" | awk '{print $1}')
[[ $common_scaffold_sha256 == "$expected_common_scaffold_sha256" ]] || {
  echo "common scaffold hash $common_scaffold_sha256 does not match the reviewed scaffold" >&2
  exit 2
}
split_overlay_sha256=$(sha256sum "$split_overlay" | awk '{print $1}')
[[ $split_overlay_sha256 == "$expected_split_overlay_sha256" ]] || {
  echo "split toggle hash $split_overlay_sha256 does not match the reviewed overlay" >&2
  exit 2
}
actual_overlay_readme_sha256=$(sha256sum "$overlay_readme" | awk '{print $1}')
[[ $actual_overlay_readme_sha256 == "$expected_overlay_readme_sha256" ]] || {
  echo "overlay README hash $actual_overlay_readme_sha256 does not match the reviewed design" >&2
  exit 2
}
actual_source_hashes_sha256=$(sha256sum "$source_hashes" | awk '{print $1}')
[[ $actual_source_hashes_sha256 == "$expected_source_hashes_sha256" ]] || {
  echo "source-hash manifest $actual_source_hashes_sha256 does not match the reviewed identity" >&2
  exit 2
}

if [[ -z $output_dir ]]; then
  mkdir -p "$root_dir/tmp/proxy-agent-refund-emission-causal"
  output_dir=$(mktemp -d -p "$root_dir/tmp/proxy-agent-refund-emission-causal" run.XXXXXXXX)
else
  output_dir=$(realpath -m "$output_dir")
  if [[ -e $output_dir ]]; then
    [[ -d $output_dir && -z $(find "$output_dir" -mindepth 1 -maxdepth 1 -print -quit) ]] || {
      echo "--output-dir must not exist or must be empty" >&2
      exit 2
    }
  else
    mkdir -p "$output_dir"
  fi
fi

snapshot_root="$output_dir/snapshot"
mkdir -p "$snapshot_root/scripts" "$snapshot_root/overlay"
cp "$runner" "$snapshot_root/scripts/qualify-proxy-agent-refund-emission-causal.sh"
cp "$0" "$snapshot_root/scripts/qualify-proxy-agent-refund-emission-causal-stability.sh"
cp "$common_scaffold" "$snapshot_root/overlay/common-scaffold.patch"
cp "$split_overlay" "$snapshot_root/overlay/split-arm-toggle.patch"
cp "$overlay_readme" "$snapshot_root/overlay/README.md"
cp "$source_hashes" "$snapshot_root/overlay/source-hashes.json"
cp "$build_provenance" "$snapshot_root/build-provenance.txt"
cp "$build_manifest" "$snapshot_root/build-manifest.json"
chmod +x "$snapshot_root/scripts/qualify-proxy-agent-refund-emission-causal.sh"

driver_binary=$(realpath "$driver_binary")
current_server_binary=$(realpath "$current_server_binary")
split_server_binary=$(realpath "$split_server_binary")
current_agent_binary=$(realpath "$current_agent_binary")
split_agent_binary=$(realpath "$split_agent_binary")
results="$output_dir/results.jsonl"
summary="${results%.jsonl}.summary.json"

runner_sha256=$(sha256sum "$snapshot_root/scripts/qualify-proxy-agent-refund-emission-causal.sh" | awk '{print $1}')
wrapper_sha256=$(sha256sum "$snapshot_root/scripts/qualify-proxy-agent-refund-emission-causal-stability.sh" | awk '{print $1}')
overlay_readme_sha256=$(sha256sum "$snapshot_root/overlay/README.md" | awk '{print $1}')
build_provenance_sha256=$(sha256sum "$snapshot_root/build-provenance.txt" | awk '{print $1}')
build_manifest_sha256=$(sha256sum "$snapshot_root/build-manifest.json" | awk '{print $1}')
driver_binary_sha256=$(sha256sum "$driver_binary" | awk '{print $1}')
current_server_binary_sha256=$(sha256sum "$current_server_binary" | awk '{print $1}')
split_server_binary_sha256=$(sha256sum "$split_server_binary" | awk '{print $1}')
current_agent_binary_sha256=$(sha256sum "$current_agent_binary" | awk '{print $1}')
split_agent_binary_sha256=$(sha256sum "$split_agent_binary" | awk '{print $1}')
driver_build_id=$(go tool buildid "$driver_binary")
current_server_build_id=$(go tool buildid "$current_server_binary")
split_server_build_id=$(go tool buildid "$split_server_binary")
current_agent_build_id=$(go tool buildid "$current_agent_binary")
split_agent_build_id=$(go tool buildid "$split_agent_binary")
for build_id in "$driver_build_id" "$current_server_build_id" "$split_server_build_id" \
  "$current_agent_build_id" "$split_agent_build_id"; do
  [[ -n $build_id ]] || { echo "all binaries must carry readable Go build IDs" >&2; exit 2; }
done
[[ $current_server_binary_sha256 != "$split_server_binary_sha256" ]] || {
  echo "current and split endpoint server binaries unexpectedly have the same hash" >&2
  exit 2
}
[[ $current_agent_binary_sha256 != "$split_agent_binary_sha256" ]] || {
  echo "current and split endpoint agent binaries unexpectedly have the same hash" >&2
  exit 2
}

for expected in "$base_source_ref" "$current_source_ref" "$split_source_ref" \
  "$common_scaffold_sha256" "$split_overlay_sha256" "$overlay_readme_sha256"; do
  grep -F "$expected" "$snapshot_root/build-provenance.txt" >/dev/null || {
    echo "build provenance does not contain source/overlay identity $expected" >&2
    exit 2
  }
done
grep -F 'go test -c' "$snapshot_root/build-provenance.txt" >/dev/null || {
  echo "build provenance does not record ordinary go test -c commands" >&2
  exit 2
}
if grep -E -- '(^|[[:space:]])-tags([=[:space:]]|$)' "$snapshot_root/build-provenance.txt" >/dev/null; then
  echo "all five binaries must be ordinary tag-off builds" >&2
  exit 2
fi
jq -e \
  --arg base "$base_source_ref" --arg common "$common_scaffold_sha256" \
  --arg toggle "$split_overlay_sha256" --arg readme "$overlay_readme_sha256" \
  --arg source_hashes "$actual_source_hashes_sha256" \
  --arg driver_sha "$driver_binary_sha256" --arg current_server_sha "$current_server_binary_sha256" \
  --arg split_server_sha "$split_server_binary_sha256" \
  --arg current_agent_sha "$current_agent_binary_sha256" --arg split_agent_sha "$split_agent_binary_sha256" \
  --arg driver_id "$driver_build_id" --arg current_server_id "$current_server_build_id" \
  --arg split_server_id "$split_server_build_id" \
  --arg current_agent_id "$current_agent_build_id" --arg split_agent_id "$split_agent_build_id" '
    .schema_version == 1 and .base_ref == $base and .ordinary_tags == "off" and
    .kind == "aggregate-refund-physical-emission-causal-diagnostic" and
    .overlays.common_scaffold.path == "meta/common-scaffold.patch" and
    .overlays.common_scaffold.sha256 == $common and
    .overlays.split_arm_toggle.path == "meta/split-arm-toggle.patch" and
    .overlays.split_arm_toggle.sha256 == $toggle and
    .overlays.readme.path == "meta/overlay-README.txt" and
    .overlays.readme.sha256 == $readme and
    .overlays.source_hashes.path == "meta/source-hashes.json" and
    .overlays.source_hashes.sha256 == $source_hashes and
    .driver_origin_identical_to == "refund-gathered-control.server" and
    .driver_origin.path == "bin/driver-origin.test" and
    .driver_origin.sha256 == $driver_sha and .driver_origin.build_id == $driver_id and
    .variants["refund-gathered-control"].source_dir == "control" and
    .variants["refund-split-control"].source_dir == "candidate" and
    .variants["refund-gathered-control"].diagnostic_split_aggregate_refund_writes == false and
    .variants["refund-split-control"].diagnostic_split_aggregate_refund_writes == true and
    .variants["refund-gathered-control"].server.path == "bin/refund-gathered-control-server.test" and
    .variants["refund-split-control"].server.path == "bin/refund-split-control-server.test" and
    .variants["refund-gathered-control"].agent.path == "bin/refund-gathered-control-agent.test" and
    .variants["refund-split-control"].agent.path == "bin/refund-split-control-agent.test" and
    .variants["refund-gathered-control"].server.sha256 == $current_server_sha and
    .variants["refund-split-control"].server.sha256 == $split_server_sha and
    .variants["refund-gathered-control"].agent.sha256 == $current_agent_sha and
    .variants["refund-split-control"].agent.sha256 == $split_agent_sha and
    .variants["refund-gathered-control"].server.build_id == $current_server_id and
    .variants["refund-split-control"].server.build_id == $split_server_id and
    .variants["refund-gathered-control"].agent.build_id == $current_agent_id and
    .variants["refund-split-control"].agent.build_id == $split_agent_id and
    .validation.focused_normal.control == "PASS" and
    .validation.focused_normal.split == "PASS" and
    .validation.focused_normal.count == 10 and
    .validation.focused_race.control == "PASS" and
    .validation.focused_race.split == "PASS" and
    .validation.focused_race.count == 5 and
    .validation.full_yamux_normal.control == "PASS" and
    .validation.full_yamux_normal.split == "PASS" and
    .validation.full_yamux_normal.count == 1 and
    all([
      .driver_origin,
      .variants["refund-gathered-control"].server,
      .variants["refund-split-control"].server,
      .variants["refund-gathered-control"].agent,
      .variants["refund-split-control"].agent
    ][]; (.sha256 | type == "string" and test("^[0-9a-f]{64}$")) and
      (.build_id | type == "string" and length > 0))
  ' "$snapshot_root/build-manifest.json" >/dev/null || {
  echo "build manifest does not match the exact source, overlay, tag-off and binary identities" >&2
  exit 2
}

cat > "$output_dir/predeclared-gates.json" <<'JSON'
{
  "diagnostic_only": true,
  "every_pair_download_split_to_current_ratio_minimum": 1.10,
  "every_pair_upload_split_to_current_ratio_minimum": 0.90,
  "secondary_transition_screen": {
    "split_all_24_download_samples_minimum_bytes_per_second": 29700000,
    "current_pairs_with_subthreshold_download_sample_minimum": 2,
    "all_pair_upload_gates_must_pass": true,
    "exact_correctness_and_cleanup": true,
    "does_not_override_main_pair_gates": true
  },
  "exact_payload_and_event_conservation": true,
  "pairs": 3,
  "pair_order": ["gathered-then-split", "split-then-gathered", "gathered-then-split"],
  "selective_retries": false,
  "total_transfers": 96
}
JSON

if ((preflight_only)); then
  python3 - "$output_dir/preflight-manifest.json" \
    "$base_source_ref" "$current_source_ref" "$split_source_ref" \
    "$common_scaffold_sha256" "$split_overlay_sha256" "$overlay_readme_sha256" "$actual_source_hashes_sha256" \
    "$runner_sha256" "$wrapper_sha256" \
    "$build_provenance_sha256" "$build_manifest_sha256" "$driver_binary_sha256" "$current_server_binary_sha256" \
    "$split_server_binary_sha256" "$current_agent_binary_sha256" "$split_agent_binary_sha256" \
    "$driver_build_id" "$current_server_build_id" "$split_server_build_id" \
    "$current_agent_build_id" "$split_agent_build_id" <<'PY'
import json
import sys
from pathlib import Path

(
    destination,
    base_source_ref,
    current_source_ref,
    split_source_ref,
    common_scaffold_sha256,
    split_overlay_sha256,
    overlay_readme_sha256,
    source_hashes_sha256,
    runner_sha256,
    wrapper_sha256,
    build_provenance_sha256,
    build_manifest_sha256,
    driver_sha256,
    current_server_sha256,
    split_server_sha256,
    current_agent_sha256,
    split_agent_sha256,
    driver_build_id,
    current_server_build_id,
    split_server_build_id,
    current_agent_build_id,
    split_agent_build_id,
) = sys.argv[1:]
manifest = {
    "schema_version": 1,
    "scope": "fixture_preflight_only",
    "workload_started": False,
    "acceptance_result": None,
    "source_refs": {
        "application_and_harness": base_source_ref,
        "gathered_control_base": current_source_ref,
        "split_control_base": split_source_ref,
    },
    "common_scaffold_sha256": common_scaffold_sha256,
    "split_overlay_sha256": split_overlay_sha256,
    "overlay_readme_sha256": overlay_readme_sha256,
    "source_hashes_sha256": source_hashes_sha256,
    "runner_sha256": runner_sha256,
    "wrapper_sha256": wrapper_sha256,
    "build_provenance_sha256": build_provenance_sha256,
    "build_manifest_sha256": build_manifest_sha256,
    "binary_sha256": {
        "driver_and_origin": driver_sha256,
        "current_server": current_server_sha256,
        "split_server": split_server_sha256,
        "current_agent": current_agent_sha256,
        "split_agent": split_agent_sha256,
    },
    "binary_build_id": {
        "driver_and_origin": driver_build_id,
        "current_server": current_server_build_id,
        "split_server": split_server_build_id,
        "current_agent": current_agent_build_id,
        "split_agent": split_agent_build_id,
    },
}
Path(destination).write_text(json.dumps(manifest, indent=2, sort_keys=True) + "\n", encoding="utf-8")
PY
  (
    cd "$output_dir"
    {
      printf '%s\0' predeclared-gates.json preflight-manifest.json
      find snapshot -type f -print0
    } | sort -z | xargs -0 sha256sum > SHA256SUMS
    sha256sum -c SHA256SUMS >/dev/null
  )
  printf 'preflight_manifest=%s\n' "$output_dir/preflight-manifest.json"
  exit 0
fi

child_pid=""
stop_child() {
  local signal=$1
  [[ -n $child_pid ]] || return 0
  kill -"$signal" -- "-$child_pid" 2>/dev/null || true
  for ((attempt=0; attempt<150; attempt++)); do
    kill -0 -- "-$child_pid" 2>/dev/null || { wait "$child_pid" 2>/dev/null || true; return 0; }
    sleep .1
  done
  kill -KILL -- "-$child_pid" 2>/dev/null || true
  wait "$child_pid" 2>/dev/null || true
}
on_interrupt() { stop_child TERM; exit 130; }
on_terminate() { stop_child TERM; exit 143; }
trap on_interrupt INT
trap on_terminate TERM

started_utc=$(date -u +%Y-%m-%dT%H:%M:%SZ)
run_command=(
  setsid "$snapshot_root/scripts/qualify-proxy-agent-refund-emission-causal.sh"
  --driver-binary "$driver_binary"
  --current-server-binary "$current_server_binary"
  --split-server-binary "$split_server_binary"
  --current-agent-binary "$current_agent_binary"
  --split-agent-binary "$split_agent_binary"
  --base-source-ref "$base_source_ref"
  --current-source-ref "$current_source_ref"
  --split-source-ref "$split_source_ref"
  --common-scaffold-sha256 "$common_scaffold_sha256"
  --split-overlay-sha256 "$split_overlay_sha256"
  --output "$results"
  --runs 3
  --warmup-mib 256
  --steady-mib 256
  --transfer-repetitions 3
  --required-pool-mib 8
  --rtt-ms 80
)
printf '%q ' "${run_command[@]}" > "$output_dir/run-command.txt"
printf '\n' >> "$output_dir/run-command.txt"
set +e
"${run_command[@]}" > "$output_dir/run.log" 2>&1 &
child_pid=$!
wait "$child_pid"
runner_status=$?
child_pid=""
set -e
completed_utc=$(date -u +%Y-%m-%dT%H:%M:%SZ)

python3 - "$output_dir/manifest.json" "$summary" <<PY
import json
from pathlib import Path

summary_path = Path("$summary")
summary = json.loads(summary_path.read_text(encoding="utf-8")) if summary_path.exists() else None
manifest = {
    "scope": "diagnostic_only",
    "implementation_difference": "identical common 5ee application/Yamux scaffold and sendReady layout; split changes only a private bool initializer so validated aggregate refund batches with 2..17 ordinary 12-byte headers use one physical Write per header inside the same bounded priority item and joint pre-wire commit; DATA, unrelated frames, and singleton controls are unchanged",
    "source_refs": {
        "application_and_harness": "$base_source_ref",
        "gathered_control_base": "$current_source_ref",
        "split_control_base": "$split_source_ref",
    },
    "common_scaffold_sha256": "$common_scaffold_sha256",
    "split_overlay_sha256": "$split_overlay_sha256",
    "overlay_readme_sha256": "$overlay_readme_sha256",
    "source_hashes_sha256": "$actual_source_hashes_sha256",
    "binary_sha256": {
        "driver_and_origin": "$driver_binary_sha256",
        "current_server": "$current_server_binary_sha256",
        "split_server": "$split_server_binary_sha256",
        "current_agent": "$current_agent_binary_sha256",
        "split_agent": "$split_agent_binary_sha256",
    },
    "binary_build_id": {
        "driver_and_origin": "$driver_build_id",
        "current_server": "$current_server_build_id",
        "split_server": "$split_server_build_id",
        "current_agent": "$current_agent_build_id",
        "split_agent": "$split_agent_build_id",
    },
    "runner_sha256": "$runner_sha256",
    "wrapper_sha256": "$wrapper_sha256",
    "build_provenance_sha256": "$build_provenance_sha256",
    "build_manifest_sha256": "$build_manifest_sha256",
    "started_utc": "$started_utc",
    "completed_utc": "$completed_utc",
    "runner_exit_status": $runner_status,
    "fixed_workload": {
        "pairs": 3,
        "order": ["gathered-then-split", "split-then-gathered", "gathered-then-split"],
        "transfers_per_variant": 16,
        "total_transfers": 96,
        "warmup_mib": 256,
        "steady_mib": 256,
        "timed_transfers_per_direction": 3,
        "required_pool_mib": 8,
        "configured_rtt_ms": 80,
        "transport_mode_both_variants": "aggregate-v1",
    },
    "result_classification": (
        "complete_main_gate_pass" if summary and summary.get("acceptance", {}).get("overall")
        else "complete_timing_rejection" if summary and summary.get("acceptance", {}).get("complete") and summary.get("acceptance", {}).get("all_rows_exact") and summary.get("acceptance", {}).get("no_non_timing_failures")
        else "correctness_safety_or_fixture_failure"
    ),
    "secondary_transition_screen": summary.get("transition_screen") if summary else None,
    "raw_fixture_artifacts": "local only; excluded from SHA256SUMS and evidence because they contain ephemeral fixture credentials",
}
Path("$output_dir/manifest.json").write_text(json.dumps(manifest, indent=2, sort_keys=True) + "\n", encoding="utf-8")
PY

(
  cd "$output_dir"
  {
    printf '%s\0' manifest.json predeclared-gates.json results.jsonl run-command.txt run.log
    [[ ! -f results.summary.json ]] || printf '%s\0' results.summary.json
    find snapshot -type f -print0
  } | sort -z | xargs -0 sha256sum > SHA256SUMS
  sha256sum -c SHA256SUMS >/dev/null
)
printf 'results=%s\nsummary=%s\nmanifest=%s\n' "$results" "$summary" "$output_dir/manifest.json"
exit "$runner_status"
