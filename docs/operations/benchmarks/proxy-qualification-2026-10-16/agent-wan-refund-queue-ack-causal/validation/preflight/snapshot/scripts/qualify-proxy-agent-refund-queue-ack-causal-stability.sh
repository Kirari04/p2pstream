#!/usr/bin/env bash
# Run the predeclared aggregate-v1 batched-item-versus-separate-items refund-queue-ack causal diagnostic.
set -Eeuo pipefail

root_dir=$(cd "$(dirname "$0")/.." && pwd)
runner="$root_dir/scripts/qualify-proxy-agent-refund-queue-ack-causal.sh"
driver_binary=""
batched_server_binary=""
separate_server_binary=""
batched_agent_binary=""
separate_agent_binary=""
base_source_ref=""
batched_source_ref=""
separate_source_ref=""
common_scaffold=""
separate_items_overlay=""
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
    --batched-server-binary) batched_server_binary=$2; shift 2 ;;
    --separate-server-binary) separate_server_binary=$2; shift 2 ;;
    --batched-agent-binary) batched_agent_binary=$2; shift 2 ;;
    --separate-agent-binary) separate_agent_binary=$2; shift 2 ;;
    --base-source-ref) base_source_ref=$2; shift 2 ;;
    --batched-source-ref) batched_source_ref=$2; shift 2 ;;
    --separate-source-ref) separate_source_ref=$2; shift 2 ;;
    --common-scaffold) common_scaffold=$2; shift 2 ;;
    --separate-items-overlay) separate_items_overlay=$2; shift 2 ;;
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
expected_batched_source_ref=5eeccaaa797254d7bde4db9854a337a02c3bc28e
expected_separate_source_ref=5eeccaaa797254d7bde4db9854a337a02c3bc28e
expected_common_scaffold_sha256=dd1b8a0ef22a7e29fc112bdd201bb758031eaafbaff8e912f90b0e8e50fef53c
expected_separate_items_overlay_sha256=b50b28cbafdb4b7609a547f781154446c484efe381b623dfac4524d5e01a4431
expected_overlay_readme_sha256=6acb3e2114bb3cdf469bfee4dfe5b12055eb238a550853af2ce0db0a818c0f6c
expected_source_hashes_sha256=357dcfc0fbbfcb0f09a09d632a5bdac33f26c98c613fbd024b45c8229eb17bc1
expected_build_provenance_sha256=f3811a802d27e4e34b62d403ad96f336517bb4d7f46107b672092d27858cad7d
expected_build_manifest_sha256=48d157ded31ec26e812fffd2b233d0d1ad132053bb7e8e12d12a872a88488dbe

[[ -f $runner && -f $common_scaffold && -f $separate_items_overlay && -f $overlay_readme && -f $source_hashes && -f $build_provenance && -f $build_manifest ]] || {
  echo "runner, common scaffold, separate overlay, overlay README, source hashes, build provenance and build manifest must be readable files" >&2
  exit 2
}
[[ -x $driver_binary && -x $batched_server_binary && -x $separate_server_binary &&
    -x $batched_agent_binary && -x $separate_agent_binary ]] || {
  echo "driver and both endpoint server/agent binary pairs must be executable" >&2
  exit 2
}
[[ $base_source_ref == "$expected_base_ref" ]] || {
  echo "application and harness source must be the frozen 5eeccaa base" >&2
  exit 2
}
[[ $batched_source_ref == "$expected_batched_source_ref" ]] || {
  echo "batched-item-control identity must use the frozen 5eeccaa base" >&2
  exit 2
}
[[ $separate_source_ref == "$expected_separate_source_ref" ]] || {
  echo "separate-items-ack identity must use the frozen 5eeccaa base" >&2
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
separate_items_overlay_sha256=$(sha256sum "$separate_items_overlay" | awk '{print $1}')
[[ $separate_items_overlay_sha256 == "$expected_separate_items_overlay_sha256" ]] || {
  echo "separate toggle hash $separate_items_overlay_sha256 does not match the reviewed overlay" >&2
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
  mkdir -p "$root_dir/tmp/proxy-agent-refund-queue-ack-causal"
  output_dir=$(mktemp -d -p "$root_dir/tmp/proxy-agent-refund-queue-ack-causal" run.XXXXXXXX)
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
cp "$runner" "$snapshot_root/scripts/qualify-proxy-agent-refund-queue-ack-causal.sh"
cp "$0" "$snapshot_root/scripts/qualify-proxy-agent-refund-queue-ack-causal-stability.sh"
cp "$common_scaffold" "$snapshot_root/overlay/common-scaffold.patch"
cp "$separate_items_overlay" "$snapshot_root/overlay/separate-items-toggle.patch"
cp "$overlay_readme" "$snapshot_root/overlay/README.md"
cp "$source_hashes" "$snapshot_root/overlay/source-hashes.json"
cp "$build_provenance" "$snapshot_root/build-provenance.txt"
cp "$build_manifest" "$snapshot_root/build-manifest.json"
chmod +x "$snapshot_root/scripts/qualify-proxy-agent-refund-queue-ack-causal.sh"

driver_binary=$(realpath "$driver_binary")
batched_server_binary=$(realpath "$batched_server_binary")
separate_server_binary=$(realpath "$separate_server_binary")
batched_agent_binary=$(realpath "$batched_agent_binary")
separate_agent_binary=$(realpath "$separate_agent_binary")
results="$output_dir/results.jsonl"
summary="${results%.jsonl}.summary.json"

runner_sha256=$(sha256sum "$snapshot_root/scripts/qualify-proxy-agent-refund-queue-ack-causal.sh" | awk '{print $1}')
wrapper_sha256=$(sha256sum "$snapshot_root/scripts/qualify-proxy-agent-refund-queue-ack-causal-stability.sh" | awk '{print $1}')
overlay_readme_sha256=$(sha256sum "$snapshot_root/overlay/README.md" | awk '{print $1}')
build_provenance_sha256=$(sha256sum "$snapshot_root/build-provenance.txt" | awk '{print $1}')
build_manifest_sha256=$(sha256sum "$snapshot_root/build-manifest.json" | awk '{print $1}')
[[ $build_provenance_sha256 == "$expected_build_provenance_sha256" ]] || {
  echo "build provenance hash $build_provenance_sha256 does not match the reviewed bundle" >&2
  exit 2
}
[[ $build_manifest_sha256 == "$expected_build_manifest_sha256" ]] || {
  echo "build manifest hash $build_manifest_sha256 does not match the reviewed bundle" >&2
  exit 2
}
driver_binary_sha256=$(sha256sum "$driver_binary" | awk '{print $1}')
batched_server_binary_sha256=$(sha256sum "$batched_server_binary" | awk '{print $1}')
separate_server_binary_sha256=$(sha256sum "$separate_server_binary" | awk '{print $1}')
batched_agent_binary_sha256=$(sha256sum "$batched_agent_binary" | awk '{print $1}')
separate_agent_binary_sha256=$(sha256sum "$separate_agent_binary" | awk '{print $1}')
driver_build_id=$(go tool buildid "$driver_binary")
batched_server_build_id=$(go tool buildid "$batched_server_binary")
separate_server_build_id=$(go tool buildid "$separate_server_binary")
batched_agent_build_id=$(go tool buildid "$batched_agent_binary")
separate_agent_build_id=$(go tool buildid "$separate_agent_binary")
for build_id in "$driver_build_id" "$batched_server_build_id" "$separate_server_build_id" \
  "$batched_agent_build_id" "$separate_agent_build_id"; do
  [[ -n $build_id ]] || { echo "all binaries must carry readable Go build IDs" >&2; exit 2; }
done
[[ $batched_server_binary_sha256 != "$separate_server_binary_sha256" ]] || {
  echo "batched and separate-items endpoint server binaries unexpectedly have the same hash" >&2
  exit 2
}
[[ $batched_agent_binary_sha256 != "$separate_agent_binary_sha256" ]] || {
  echo "batched and separate-items endpoint agent binaries unexpectedly have the same hash" >&2
  exit 2
}

for expected in "$base_source_ref" "$batched_source_ref" "$separate_source_ref" \
  "$common_scaffold_sha256" "$separate_items_overlay_sha256" "$overlay_readme_sha256"; do
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
  --arg toggle "$separate_items_overlay_sha256" --arg readme "$overlay_readme_sha256" \
  --arg source_hashes "$actual_source_hashes_sha256" --arg provenance "$build_provenance_sha256" \
  --arg driver_sha "$driver_binary_sha256" --arg batched_server_sha "$batched_server_binary_sha256" \
  --arg separate_server_sha "$separate_server_binary_sha256" \
  --arg batched_agent_sha "$batched_agent_binary_sha256" --arg separate_agent_sha "$separate_agent_binary_sha256" \
  --arg driver_id "$driver_build_id" --arg batched_server_id "$batched_server_build_id" \
  --arg separate_server_id "$separate_server_build_id" \
  --arg batched_agent_id "$batched_agent_build_id" --arg separate_agent_id "$separate_agent_build_id" '
    .kind == "p2pstream-refund-queue-ack-causal-bundle-v1" and
    .base_source.ref == $base and .build.tags == [] and
    .overlays.common_scaffold.path == "common-scaffold.patch" and
    .overlays.common_scaffold.sha256 == $common and
    .overlays.separate_items_overlay.path == "separate-items-toggle.patch" and
    .overlays.separate_items_overlay.sha256 == $toggle and
    .evidence.readme.path == "README.md" and
    .evidence.readme.sha256 == $readme and
    .evidence.source_hashes.path == "source-hashes.json" and
    .evidence.source_hashes.sha256 == $source_hashes and
    .evidence.build_provenance.path == "build-provenance.txt" and
    .evidence.build_provenance.sha256 == $provenance and
    .driver_origin_identical_to == "refund-batched-item-control.server" and
    .driver_origin.path == "driver-origin.test" and
    .driver_origin.sha256 == $driver_sha and .driver_origin.build_id == $driver_id and
    .variants["refund-batched-item-control"].separate_items_ack == false and
    .variants["refund-separate-items-ack"].separate_items_ack == true and
    .variants["refund-batched-item-control"].source_identity == ($base + "+refund-batched-item-control@sha256:" + $common) and
    .variants["refund-separate-items-ack"].source_identity == ($base + "+refund-separate-items-ack@sha256:" + $common + "+sha256:" + $toggle) and
    .variants["refund-batched-item-control"].binaries.server.path == "refund-batched-item-control.server.test" and
    .variants["refund-separate-items-ack"].binaries.server.path == "refund-separate-items-ack.server.test" and
    .variants["refund-batched-item-control"].binaries.agent.path == "refund-batched-item-control.agent.test" and
    .variants["refund-separate-items-ack"].binaries.agent.path == "refund-separate-items-ack.agent.test" and
    .variants["refund-batched-item-control"].binaries.server.sha256 == $batched_server_sha and
    .variants["refund-separate-items-ack"].binaries.server.sha256 == $separate_server_sha and
    .variants["refund-batched-item-control"].binaries.agent.sha256 == $batched_agent_sha and
    .variants["refund-separate-items-ack"].binaries.agent.sha256 == $separate_agent_sha and
    .variants["refund-batched-item-control"].binaries.server.build_id == $batched_server_id and
    .variants["refund-separate-items-ack"].binaries.server.build_id == $separate_server_id and
    .variants["refund-batched-item-control"].binaries.agent.build_id == $batched_agent_id and
    .variants["refund-separate-items-ack"].binaries.agent.build_id == $separate_agent_id and
    .validation.patch_replay.forward_exact == true and
    .validation.patch_replay.reverse_exact == true and
    .validation.focused_normal.count == 10 and
    .validation.focused_race.count == 5 and
    .validation.full_yamux_normal.count == 1 and
    .validation.endpoint_compile.agent == true and
    .validation.endpoint_compile.server == true and
    .validation.endpoint_compile.ordinary_tag_off == true and
    all([
      .driver_origin,
      .variants["refund-batched-item-control"].binaries.server,
      .variants["refund-separate-items-ack"].binaries.server,
      .variants["refund-batched-item-control"].binaries.agent,
      .variants["refund-separate-items-ack"].binaries.agent
    ][]; (.sha256 | type == "string" and test("^[0-9a-f]{64}$")) and
      (.build_id | type == "string" and length > 0))
  ' "$snapshot_root/build-manifest.json" >/dev/null || {
  echo "build manifest does not match the exact source, overlay, tag-off and binary identities" >&2
  exit 2
}

cat > "$output_dir/predeclared-gates.json" <<'JSON'
{
  "diagnostic_only": true,
  "every_pair_download_separate_to_batched_ratio_minimum": 1.10,
  "every_pair_upload_separate_to_batched_ratio_minimum": 0.90,
  "secondary_transition_screen": {
    "separate_all_24_download_samples_minimum_bytes_per_second": 29700000,
    "batched_pairs_with_subthreshold_download_sample_minimum": 2,
    "all_pair_upload_gates_must_pass": true,
    "exact_correctness_and_cleanup": true,
    "does_not_override_main_pair_gates": true
  },
  "exact_payload_and_event_conservation": true,
  "pairs": 3,
  "pair_order": ["batched-then-separate", "separate-then-batched", "batched-then-separate"],
  "selective_retries": false,
  "total_transfers": 96
}
JSON

if ((preflight_only)); then
  python3 - "$output_dir/preflight-manifest.json" \
    "$base_source_ref" "$batched_source_ref" "$separate_source_ref" \
    "$common_scaffold_sha256" "$separate_items_overlay_sha256" "$overlay_readme_sha256" "$actual_source_hashes_sha256" \
    "$runner_sha256" "$wrapper_sha256" \
    "$build_provenance_sha256" "$build_manifest_sha256" "$driver_binary_sha256" "$batched_server_binary_sha256" \
    "$separate_server_binary_sha256" "$batched_agent_binary_sha256" "$separate_agent_binary_sha256" \
    "$driver_build_id" "$batched_server_build_id" "$separate_server_build_id" \
    "$batched_agent_build_id" "$separate_agent_build_id" <<'PY'
import json
import sys
from pathlib import Path

(
    destination,
    base_source_ref,
    batched_source_ref,
    separate_source_ref,
    common_scaffold_sha256,
    separate_items_overlay_sha256,
    overlay_readme_sha256,
    source_hashes_sha256,
    runner_sha256,
    wrapper_sha256,
    build_provenance_sha256,
    build_manifest_sha256,
    driver_sha256,
    batched_server_sha256,
    separate_server_sha256,
    batched_agent_sha256,
    separate_agent_sha256,
    driver_build_id,
    batched_server_build_id,
    separate_server_build_id,
    batched_agent_build_id,
    separate_agent_build_id,
) = sys.argv[1:]
manifest = {
    "schema_version": 1,
    "scope": "fixture_preflight_only",
    "workload_started": False,
    "acceptance_result": None,
    "source_refs": {
        "application_and_harness": base_source_ref,
        "batched_item_control_base": batched_source_ref,
        "separate_items_ack_base": separate_source_ref,
    },
    "common_scaffold_sha256": common_scaffold_sha256,
    "separate_items_overlay_sha256": separate_items_overlay_sha256,
    "overlay_readme_sha256": overlay_readme_sha256,
    "source_hashes_sha256": source_hashes_sha256,
    "runner_sha256": runner_sha256,
    "wrapper_sha256": wrapper_sha256,
    "build_provenance_sha256": build_provenance_sha256,
    "build_manifest_sha256": build_manifest_sha256,
    "binary_sha256": {
        "driver_and_origin": driver_sha256,
        "batched_server": batched_server_sha256,
        "separate_server": separate_server_sha256,
        "batched_agent": batched_agent_sha256,
        "separate_agent": separate_agent_sha256,
    },
    "binary_build_id": {
        "driver_and_origin": driver_build_id,
        "batched_server": batched_server_build_id,
        "separate_server": separate_server_build_id,
        "batched_agent": batched_agent_build_id,
        "separate_agent": separate_agent_build_id,
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
  setsid "$snapshot_root/scripts/qualify-proxy-agent-refund-queue-ack-causal.sh"
  --driver-binary "$driver_binary"
  --batched-server-binary "$batched_server_binary"
  --separate-server-binary "$separate_server_binary"
  --batched-agent-binary "$batched_agent_binary"
  --separate-agent-binary "$separate_agent_binary"
  --base-source-ref "$base_source_ref"
  --batched-source-ref "$batched_source_ref"
  --separate-source-ref "$separate_source_ref"
  --common-scaffold-sha256 "$common_scaffold_sha256"
  --separate-items-overlay-sha256 "$separate_items_overlay_sha256"
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
    "implementation_difference": "identical common 5ee application/Yamux scaffold and sendReady layout; the separate-items arm changes only a private bool initializer so validated aggregate refund batches with 2..17 ordinary 12-byte headers use sequential priority items with acknowledgement between, one shared deadline, and joint accounting commit only on the first item; DATA, unrelated frames, and singleton controls are unchanged",
    "source_refs": {
        "application_and_harness": "$base_source_ref",
        "batched_item_control_base": "$batched_source_ref",
        "separate_items_ack_base": "$separate_source_ref",
    },
    "common_scaffold_sha256": "$common_scaffold_sha256",
    "separate_items_overlay_sha256": "$separate_items_overlay_sha256",
    "overlay_readme_sha256": "$overlay_readme_sha256",
    "source_hashes_sha256": "$actual_source_hashes_sha256",
    "binary_sha256": {
        "driver_and_origin": "$driver_binary_sha256",
        "batched_server": "$batched_server_binary_sha256",
        "separate_server": "$separate_server_binary_sha256",
        "batched_agent": "$batched_agent_binary_sha256",
        "separate_agent": "$separate_agent_binary_sha256",
    },
    "binary_build_id": {
        "driver_and_origin": "$driver_build_id",
        "batched_server": "$batched_server_build_id",
        "separate_server": "$separate_server_build_id",
        "batched_agent": "$batched_agent_build_id",
        "separate_agent": "$separate_agent_build_id",
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
        "order": ["batched-then-separate", "separate-then-batched", "batched-then-separate"],
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
