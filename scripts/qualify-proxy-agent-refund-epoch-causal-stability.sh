#!/usr/bin/env bash
# Run the predeclared aggregate-v1 deferred-global refund-epoch causal diagnostic.
set -Eeuo pipefail

root_dir=$(cd "$(dirname "$0")/.." && pwd)
runner="$root_dir/scripts/qualify-proxy-agent-refund-epoch-causal.sh"
driver_binary=""
control_server_binary=""
deferred_server_binary=""
control_agent_binary=""
deferred_agent_binary=""
base_source_ref=""
control_source_ref=""
deferred_source_ref=""
common_scaffold=""
deferred_global_overlay=""
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
    --control-server-binary) control_server_binary=$2; shift 2 ;;
    --deferred-server-binary) deferred_server_binary=$2; shift 2 ;;
    --control-agent-binary) control_agent_binary=$2; shift 2 ;;
    --deferred-agent-binary) deferred_agent_binary=$2; shift 2 ;;
    --base-source-ref) base_source_ref=$2; shift 2 ;;
    --control-source-ref) control_source_ref=$2; shift 2 ;;
    --deferred-source-ref) deferred_source_ref=$2; shift 2 ;;
    --common-scaffold) common_scaffold=$2; shift 2 ;;
    --deferred-global-overlay) deferred_global_overlay=$2; shift 2 ;;
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
expected_control_source_ref=5eeccaaa797254d7bde4db9854a337a02c3bc28e
expected_deferred_source_ref=5eeccaaa797254d7bde4db9854a337a02c3bc28e
expected_common_scaffold_sha256=3fa10c967b69d4d371c64dd6bb2ca175ffbd9f98d80193d3d41b2722809bfe59
expected_deferred_global_overlay_sha256=d07cac9d6f5dac0311416282c0c0e19ece2d3369e1d372abc43b406b3e33b81d
expected_overlay_readme_sha256=56b792868181965c2e82b75f61380ba238f541401cf35be78e46fc38486c3e5f
expected_source_hashes_sha256=24d37dbe261ab15f7f949595879117bb7afac8cd1b40597a0062eb3fd4d6dfba
expected_build_provenance_sha256=bfa306ba5affaa265b4a87fc2b3faccddb454b5c1883194c7f343210ce5aea48
expected_build_manifest_sha256=dd606c6e0478a6e6c46e4d765122803b7ab20b3dfc23fb4d14dd0ceb54da7f19

[[ -f $runner && -f $common_scaffold && -f $deferred_global_overlay && -f $overlay_readme && -f $source_hashes && -f $build_provenance && -f $build_manifest ]] || {
  echo "runner, common scaffold, deferred-global overlay, overlay README, source hashes, build provenance and build manifest must be readable files" >&2
  exit 2
}
[[ -x $driver_binary && -x $control_server_binary && -x $deferred_server_binary &&
    -x $control_agent_binary && -x $deferred_agent_binary ]] || {
  echo "driver and both endpoint server/agent binary pairs must be executable" >&2
  exit 2
}
[[ $base_source_ref == "$expected_base_ref" ]] || {
  echo "application and harness source must be the frozen 5eeccaa base" >&2
  exit 2
}
[[ $control_source_ref == "$expected_control_source_ref" ]] || {
  echo "current-batch control identity must use the frozen 5eeccaa base" >&2
  exit 2
}
[[ $deferred_source_ref == "$expected_deferred_source_ref" ]] || {
  echo "streams-before-global identity must use the frozen 5eeccaa base" >&2
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
deferred_global_overlay_sha256=$(sha256sum "$deferred_global_overlay" | awk '{print $1}')
[[ $deferred_global_overlay_sha256 == "$expected_deferred_global_overlay_sha256" ]] || {
  echo "deferred-global toggle hash $deferred_global_overlay_sha256 does not match the reviewed overlay" >&2
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
  mkdir -p "$root_dir/tmp/proxy-agent-refund-epoch-causal"
  output_dir=$(mktemp -d -p "$root_dir/tmp/proxy-agent-refund-epoch-causal" run.XXXXXXXX)
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
cp "$runner" "$snapshot_root/scripts/qualify-proxy-agent-refund-epoch-causal.sh"
cp "$0" "$snapshot_root/scripts/qualify-proxy-agent-refund-epoch-causal-stability.sh"
cp "$common_scaffold" "$snapshot_root/overlay/common-scaffold.patch"
cp "$deferred_global_overlay" "$snapshot_root/overlay/deferred-global-toggle.patch"
cp "$overlay_readme" "$snapshot_root/overlay/README.md"
cp "$source_hashes" "$snapshot_root/overlay/source-hashes.json"
cp "$build_provenance" "$snapshot_root/build-provenance.txt"
cp "$build_manifest" "$snapshot_root/build-manifest.json"
chmod +x "$snapshot_root/scripts/qualify-proxy-agent-refund-epoch-causal.sh"

driver_binary=$(realpath "$driver_binary")
control_server_binary=$(realpath "$control_server_binary")
deferred_server_binary=$(realpath "$deferred_server_binary")
control_agent_binary=$(realpath "$control_agent_binary")
deferred_agent_binary=$(realpath "$deferred_agent_binary")
results="$output_dir/results.jsonl"
summary="${results%.jsonl}.summary.json"

runner_sha256=$(sha256sum "$snapshot_root/scripts/qualify-proxy-agent-refund-epoch-causal.sh" | awk '{print $1}')
wrapper_sha256=$(sha256sum "$snapshot_root/scripts/qualify-proxy-agent-refund-epoch-causal-stability.sh" | awk '{print $1}')
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
control_server_binary_sha256=$(sha256sum "$control_server_binary" | awk '{print $1}')
deferred_server_binary_sha256=$(sha256sum "$deferred_server_binary" | awk '{print $1}')
control_agent_binary_sha256=$(sha256sum "$control_agent_binary" | awk '{print $1}')
deferred_agent_binary_sha256=$(sha256sum "$deferred_agent_binary" | awk '{print $1}')
driver_build_id=$(go tool buildid "$driver_binary")
control_server_build_id=$(go tool buildid "$control_server_binary")
deferred_server_build_id=$(go tool buildid "$deferred_server_binary")
control_agent_build_id=$(go tool buildid "$control_agent_binary")
deferred_agent_build_id=$(go tool buildid "$deferred_agent_binary")
for build_id in "$driver_build_id" "$control_server_build_id" "$deferred_server_build_id" \
  "$control_agent_build_id" "$deferred_agent_build_id"; do
  [[ -n $build_id ]] || { echo "all binaries must carry readable Go build IDs" >&2; exit 2; }
done
[[ $control_server_binary_sha256 != "$deferred_server_binary_sha256" ]] || {
  echo "current-batch and deferred-global endpoint server binaries unexpectedly have the same hash" >&2
  exit 2
}
[[ $control_agent_binary_sha256 != "$deferred_agent_binary_sha256" ]] || {
  echo "current-batch and deferred-global endpoint agent binaries unexpectedly have the same hash" >&2
  exit 2
}

for expected in "$base_source_ref" "$control_source_ref" "$deferred_source_ref" \
  "$common_scaffold_sha256" "$deferred_global_overlay_sha256" "$overlay_readme_sha256"; do
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
  --arg toggle "$deferred_global_overlay_sha256" --arg readme "$overlay_readme_sha256" \
  --arg source_hashes "$actual_source_hashes_sha256" --arg provenance "$build_provenance_sha256" \
  --arg driver_sha "$driver_binary_sha256" --arg control_server_sha "$control_server_binary_sha256" \
  --arg deferred_server_sha "$deferred_server_binary_sha256" \
  --arg control_agent_sha "$control_agent_binary_sha256" --arg deferred_agent_sha "$deferred_agent_binary_sha256" \
  --arg driver_id "$driver_build_id" --arg control_server_id "$control_server_build_id" \
  --arg deferred_server_id "$deferred_server_build_id" \
  --arg control_agent_id "$control_agent_build_id" --arg deferred_agent_id "$deferred_agent_build_id" '
    .kind == "p2pstream-refund-deferred-global-epoch-causal-bundle-v1" and
    .base_source.ref == $base and .build.tags == [] and
    .overlays.common_scaffold.path == "common-scaffold.patch" and
    .overlays.common_scaffold.sha256 == $common and
    .overlays.deferred_global_overlay.path == "deferred-global-toggle.patch" and
    .overlays.deferred_global_overlay.sha256 == $toggle and
    .evidence.readme.path == "README.md" and
    .evidence.readme.sha256 == $readme and
    .evidence.source_hashes.path == "source-hashes.json" and
    .evidence.source_hashes.sha256 == $source_hashes and
    .evidence.build_provenance.path == "build-provenance.txt" and
    .evidence.build_provenance.sha256 == $provenance and
    .driver_origin_identical_to == "refund-current-batch-control.server" and
    .driver_origin.path == "driver-origin.test" and
    .driver_origin.sha256 == $driver_sha and .driver_origin.build_id == $driver_id and
    .variants["refund-current-batch-control"].deferred_global_epoch == false and
    .variants["refund-streams-before-global-epoch"].deferred_global_epoch == true and
    .variants["refund-current-batch-control"].source_identity == ($base + "+refund-current-batch-control@sha256:" + $common) and
    .variants["refund-streams-before-global-epoch"].source_identity == ($base + "+refund-streams-before-global-epoch@sha256:" + $common + "+sha256:" + $toggle) and
    .variants["refund-current-batch-control"].binaries.server.path == "refund-current-batch-control.server.test" and
    .variants["refund-streams-before-global-epoch"].binaries.server.path == "refund-streams-before-global-epoch.server.test" and
    .variants["refund-current-batch-control"].binaries.agent.path == "refund-current-batch-control.agent.test" and
    .variants["refund-streams-before-global-epoch"].binaries.agent.path == "refund-streams-before-global-epoch.agent.test" and
    .variants["refund-current-batch-control"].binaries.server.sha256 == $control_server_sha and
    .variants["refund-streams-before-global-epoch"].binaries.server.sha256 == $deferred_server_sha and
    .variants["refund-current-batch-control"].binaries.agent.sha256 == $control_agent_sha and
    .variants["refund-streams-before-global-epoch"].binaries.agent.sha256 == $deferred_agent_sha and
    .variants["refund-current-batch-control"].binaries.server.build_id == $control_server_id and
    .variants["refund-streams-before-global-epoch"].binaries.server.build_id == $deferred_server_id and
    .variants["refund-current-batch-control"].binaries.agent.build_id == $control_agent_id and
    .variants["refund-streams-before-global-epoch"].binaries.agent.build_id == $deferred_agent_id and
    .validation.patch_replay.forward_exact == true and
    .validation.patch_replay.reverse_exact == true and
    .validation.control_focused_normal.count == 10 and
    .validation.control_focused_race.count == 5 and
    .validation.deferred_focused_normal.count == 10 and
    .validation.deferred_focused_race.count == 5 and
    .validation.deferred_conservation_normal.count == 3 and
    .validation.deferred_conservation_race.count == 3 and
    .validation.full_yamux_normal.count == 1 and
    .validation.endpoint_compile.agent == true and
    .validation.endpoint_compile.server == true and
    .validation.endpoint_compile.ordinary_tag_off == true and
    all([
      .driver_origin,
      .variants["refund-current-batch-control"].binaries.server,
      .variants["refund-streams-before-global-epoch"].binaries.server,
      .variants["refund-current-batch-control"].binaries.agent,
      .variants["refund-streams-before-global-epoch"].binaries.agent
    ][]; (.sha256 | type == "string" and test("^[0-9a-f]{64}$")) and
      (.build_id | type == "string" and length > 0))
  ' "$snapshot_root/build-manifest.json" >/dev/null || {
  echo "build manifest does not match the exact source, overlay, tag-off and binary identities" >&2
  exit 2
}

cat > "$output_dir/predeclared-gates.json" <<'JSON'
{
  "diagnostic_only": true,
  "every_pair_download_deferred_to_control_ratio_minimum": 1.10,
  "every_pair_upload_deferred_to_control_ratio_minimum": 0.90,
  "secondary_transition_screen": {
    "deferred_all_24_download_samples_minimum_bytes_per_second": 29700000,
    "control_pairs_with_subthreshold_download_sample_minimum": 2,
    "all_pair_upload_gates_must_pass": true,
    "exact_correctness_and_cleanup": true,
    "does_not_override_main_pair_gates": true
  },
  "exact_payload_and_event_conservation": true,
  "pairs": 3,
  "pair_order": ["control-then-deferred", "deferred-then-control", "control-then-deferred"],
  "selective_retries": false,
  "total_transfers": 96
}
JSON

if ((preflight_only)); then
  python3 - "$output_dir/preflight-manifest.json" \
    "$base_source_ref" "$control_source_ref" "$deferred_source_ref" \
    "$common_scaffold_sha256" "$deferred_global_overlay_sha256" "$overlay_readme_sha256" "$actual_source_hashes_sha256" \
    "$runner_sha256" "$wrapper_sha256" \
    "$build_provenance_sha256" "$build_manifest_sha256" "$driver_binary_sha256" "$control_server_binary_sha256" \
    "$deferred_server_binary_sha256" "$control_agent_binary_sha256" "$deferred_agent_binary_sha256" \
    "$driver_build_id" "$control_server_build_id" "$deferred_server_build_id" \
    "$control_agent_build_id" "$deferred_agent_build_id" <<'PY'
import json
import sys
from pathlib import Path

(
    destination,
    base_source_ref,
    control_source_ref,
    deferred_source_ref,
    common_scaffold_sha256,
    deferred_global_overlay_sha256,
    overlay_readme_sha256,
    source_hashes_sha256,
    runner_sha256,
    wrapper_sha256,
    build_provenance_sha256,
    build_manifest_sha256,
    driver_sha256,
    control_server_sha256,
    deferred_server_sha256,
    control_agent_sha256,
    deferred_agent_sha256,
    driver_build_id,
    control_server_build_id,
    deferred_server_build_id,
    control_agent_build_id,
    deferred_agent_build_id,
) = sys.argv[1:]
manifest = {
    "schema_version": 1,
    "scope": "fixture_preflight_only",
    "workload_started": False,
    "acceptance_result": None,
    "source_refs": {
        "application_and_harness": base_source_ref,
        "current_batch_control_base": control_source_ref,
        "deferred_global_epoch_base": deferred_source_ref,
    },
    "common_scaffold_sha256": common_scaffold_sha256,
    "deferred_global_overlay_sha256": deferred_global_overlay_sha256,
    "overlay_readme_sha256": overlay_readme_sha256,
    "source_hashes_sha256": source_hashes_sha256,
    "runner_sha256": runner_sha256,
    "wrapper_sha256": wrapper_sha256,
    "build_provenance_sha256": build_provenance_sha256,
    "build_manifest_sha256": build_manifest_sha256,
    "binary_sha256": {
        "driver_and_origin": driver_sha256,
        "control_server": control_server_sha256,
        "deferred_server": deferred_server_sha256,
        "control_agent": control_agent_sha256,
        "deferred_agent": deferred_agent_sha256,
    },
    "binary_build_id": {
        "driver_and_origin": driver_build_id,
        "control_server": control_server_build_id,
        "deferred_server": deferred_server_build_id,
        "control_agent": control_agent_build_id,
        "deferred_agent": deferred_agent_build_id,
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
  setsid "$snapshot_root/scripts/qualify-proxy-agent-refund-epoch-causal.sh"
  --driver-binary "$driver_binary"
  --control-server-binary "$control_server_binary"
  --deferred-server-binary "$deferred_server_binary"
  --control-agent-binary "$control_agent_binary"
  --deferred-agent-binary "$deferred_agent_binary"
  --base-source-ref "$base_source_ref"
  --control-source-ref "$control_source_ref"
  --deferred-source-ref "$deferred_source_ref"
  --common-scaffold-sha256 "$common_scaffold_sha256"
  --deferred-global-overlay-sha256 "$deferred_global_overlay_sha256"
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
    "implementation_difference": "identical common 5ee application/Yamux scaffold and current sendReady layout; the deferred-global arm changes only a private bool initializer so each epoch selects at most 16 serial stream refunds, including requeues and terminal refunds, then flushes their accumulated matching global refund even if the stream queue remains nonempty; if selection empties that queue, the same locked observation may also include bounded global-only credit; one epoch I/O deadline and current ownership, DATA, and shutdown behavior remain",
    "source_refs": {
        "application_and_harness": "$base_source_ref",
        "current_batch_control_base": "$control_source_ref",
        "deferred_global_epoch_base": "$deferred_source_ref",
    },
    "common_scaffold_sha256": "$common_scaffold_sha256",
    "deferred_global_overlay_sha256": "$deferred_global_overlay_sha256",
    "overlay_readme_sha256": "$overlay_readme_sha256",
    "source_hashes_sha256": "$actual_source_hashes_sha256",
    "binary_sha256": {
        "driver_and_origin": "$driver_binary_sha256",
        "control_server": "$control_server_binary_sha256",
        "deferred_server": "$deferred_server_binary_sha256",
        "control_agent": "$control_agent_binary_sha256",
        "deferred_agent": "$deferred_agent_binary_sha256",
    },
    "binary_build_id": {
        "driver_and_origin": "$driver_build_id",
        "control_server": "$control_server_build_id",
        "deferred_server": "$deferred_server_build_id",
        "control_agent": "$control_agent_build_id",
        "deferred_agent": "$deferred_agent_build_id",
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
        "order": ["control-then-deferred", "deferred-then-control", "control-then-deferred"],
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
