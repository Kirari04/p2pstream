#!/usr/bin/env bash
# Run the predeclared aggregate-v1 lowat-32k tunnel-lowat causal diagnostic.
set -Eeuo pipefail

root_dir=$(cd "$(dirname "$0")/.." && pwd)
runner="$root_dir/scripts/qualify-proxy-agent-tunnel-lowat-causal.sh"
driver_binary=""
control_server_binary=""
lowat_server_binary=""
control_agent_binary=""
lowat_agent_binary=""
base_source_ref=""
control_source_ref=""
lowat_source_ref=""
common_scaffold=""
lowat_32k_overlay=""
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
    --lowat-server-binary) lowat_server_binary=$2; shift 2 ;;
    --control-agent-binary) control_agent_binary=$2; shift 2 ;;
    --lowat-agent-binary) lowat_agent_binary=$2; shift 2 ;;
    --base-source-ref) base_source_ref=$2; shift 2 ;;
    --control-source-ref) control_source_ref=$2; shift 2 ;;
    --lowat-source-ref) lowat_source_ref=$2; shift 2 ;;
    --common-scaffold) common_scaffold=$2; shift 2 ;;
    --lowat-32k-overlay) lowat_32k_overlay=$2; shift 2 ;;
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
expected_lowat_source_ref=5eeccaaa797254d7bde4db9854a337a02c3bc28e
expected_common_scaffold_sha256=1f5406709d4347975f6110830fbaf24ef06f96e6a22280071ac8ab76eaba24d8
expected_lowat_32k_overlay_sha256=076c8dfc22212296e30b9430c9654625dd312b03cfaa19a55b1faa4c1fb69379
expected_overlay_readme_sha256=f5cbb3db08768afa55f6e4108c8739bf2e6162d60b9529a0b6c8f712d8f34096
expected_source_hashes_sha256=e8775e1fccb30889697c000a48597e17acc14354e79138cc7ca5790a178659b9
expected_build_provenance_sha256=1de229556fb15aa5accd165b8b153ab09df37a104223b8092633c79589c9a72f
expected_build_manifest_sha256=1c3cc0f51ca0cbabc9d45b3b6d52cdba5334ef8170d870112b02dfcd83e34d06

[[ -f $runner && -f $common_scaffold && -f $lowat_32k_overlay && -f $overlay_readme && -f $source_hashes && -f $build_provenance && -f $build_manifest ]] || {
  echo "runner, common scaffold, lowat-32k overlay, overlay README, source hashes, build provenance and build manifest must be readable files" >&2
  exit 2
}
[[ -x $driver_binary && -x $control_server_binary && -x $lowat_server_binary &&
    -x $control_agent_binary && -x $lowat_agent_binary ]] || {
  echo "driver and both endpoint server/agent binary pairs must be executable" >&2
  exit 2
}
[[ $base_source_ref == "$expected_base_ref" ]] || {
  echo "application and harness source must be the frozen 5eeccaa base" >&2
  exit 2
}
[[ $control_source_ref == "$expected_control_source_ref" ]] || {
  echo "default-LOWAT control identity must use the frozen 5eeccaa base" >&2
  exit 2
}
[[ $lowat_source_ref == "$expected_lowat_source_ref" ]] || {
  echo "LOWAT-32K identity must use the frozen 5eeccaa base" >&2
  exit 2
}
for tool in setsid sha256sum python3 realpath find grep jq; do
  command -v "$tool" >/dev/null || { echo "$tool is required" >&2; exit 2; }
done

common_scaffold_sha256=$(sha256sum "$common_scaffold" | awk '{print $1}')
[[ $common_scaffold_sha256 == "$expected_common_scaffold_sha256" ]] || {
  echo "common scaffold hash $common_scaffold_sha256 does not match the reviewed scaffold" >&2
  exit 2
}
lowat_32k_overlay_sha256=$(sha256sum "$lowat_32k_overlay" | awk '{print $1}')
[[ $lowat_32k_overlay_sha256 == "$expected_lowat_32k_overlay_sha256" ]] || {
  echo "lowat-32k toggle hash $lowat_32k_overlay_sha256 does not match the reviewed overlay" >&2
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
bundle_root=$(dirname "$(realpath "$build_manifest")")
for binding in \
  "$common_scaffold:tunnel-lowat-common.patch" \
  "$lowat_32k_overlay:tunnel-lowat-candidate.patch" \
  "$overlay_readme:README.md" \
  "$source_hashes:source-hashes.json" \
  "$build_provenance:build-provenance.txt" \
  "$build_manifest:build-manifest.json"; do
  path=${binding%%:*}
  name=${binding#*:}
  [[ $(realpath "$path") == "$bundle_root/$name" ]] || {
    echo "$name must come from the exact immutable LOWAT bundle" >&2
    exit 2
  }
done
[[ -f $bundle_root/SHA256SUMS ]] || { echo "bundle SHA256SUMS is required" >&2; exit 2; }
(
  cd "$bundle_root"
  sha256sum -c SHA256SUMS >/dev/null
) || { echo "LOWAT bundle checksum validation failed" >&2; exit 2; }

if [[ -z $output_dir ]]; then
  mkdir -p "$root_dir/tmp/proxy-agent-tunnel-lowat-causal"
  output_dir=$(mktemp -d -p "$root_dir/tmp/proxy-agent-tunnel-lowat-causal" run.XXXXXXXX)
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
mkdir -p "$snapshot_root/scripts" "$snapshot_root/overlay" "$snapshot_root/bundle-metadata"
cp "$runner" "$snapshot_root/scripts/qualify-proxy-agent-tunnel-lowat-causal.sh"
cp "$0" "$snapshot_root/scripts/qualify-proxy-agent-tunnel-lowat-causal-stability.sh"
cp "$common_scaffold" "$snapshot_root/overlay/tunnel-lowat-common.patch"
cp "$lowat_32k_overlay" "$snapshot_root/overlay/tunnel-lowat-candidate.patch"
cp "$overlay_readme" "$snapshot_root/overlay/README.md"
cp "$source_hashes" "$snapshot_root/overlay/source-hashes.json"
cp "$build_provenance" "$snapshot_root/build-provenance.txt"
cp "$build_manifest" "$snapshot_root/build-manifest.json"
cp "$bundle_root"/{SHA256SUMS,binary-buildids.txt,binary-version-m.txt,toolchain.txt} "$snapshot_root/bundle-metadata/"
cp -R "$bundle_root/validation" "$snapshot_root/bundle-metadata/validation"
chmod +x "$snapshot_root/scripts/qualify-proxy-agent-tunnel-lowat-causal.sh"

driver_binary=$(realpath "$driver_binary")
control_server_binary=$(realpath "$control_server_binary")
lowat_server_binary=$(realpath "$lowat_server_binary")
control_agent_binary=$(realpath "$control_agent_binary")
lowat_agent_binary=$(realpath "$lowat_agent_binary")
results="$output_dir/results.jsonl"
summary="${results%.jsonl}.summary.json"

runner_sha256=$(sha256sum "$snapshot_root/scripts/qualify-proxy-agent-tunnel-lowat-causal.sh" | awk '{print $1}')
wrapper_sha256=$(sha256sum "$snapshot_root/scripts/qualify-proxy-agent-tunnel-lowat-causal-stability.sh" | awk '{print $1}')
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
lowat_server_binary_sha256=$(sha256sum "$lowat_server_binary" | awk '{print $1}')
control_agent_binary_sha256=$(sha256sum "$control_agent_binary" | awk '{print $1}')
lowat_agent_binary_sha256=$(sha256sum "$lowat_agent_binary" | awk '{print $1}')
go_binary=$(jq -r '.build.go_binary' "$snapshot_root/build-manifest.json")
[[ $go_binary == /home/user/go/pkg/mod/golang.org/toolchain@v0.0.1-go1.26.6.linux-amd64/bin/go && -x $go_binary ]] || {
  echo "build manifest must pin the installed Go 1.26.6 toolchain" >&2
  exit 2
}
[[ $($go_binary version) == go\ version\ go1.26.6\ linux/amd64 ]] || {
  echo "pinned Go toolchain version mismatch" >&2
  exit 2
}
driver_build_id=$($go_binary tool buildid "$driver_binary")
control_server_build_id=$($go_binary tool buildid "$control_server_binary")
lowat_server_build_id=$($go_binary tool buildid "$lowat_server_binary")
control_agent_build_id=$($go_binary tool buildid "$control_agent_binary")
lowat_agent_build_id=$($go_binary tool buildid "$lowat_agent_binary")
for build_id in "$driver_build_id" "$control_server_build_id" "$lowat_server_build_id" \
  "$control_agent_build_id" "$lowat_agent_build_id"; do
  [[ -n $build_id ]] || { echo "all binaries must carry readable Go build IDs" >&2; exit 2; }
done
[[ $control_server_binary_sha256 != "$lowat_server_binary_sha256" ]] || {
  echo "default-LOWAT and lowat-32k endpoint server binaries unexpectedly have the same hash" >&2
  exit 2
}
[[ $control_agent_binary_sha256 != "$lowat_agent_binary_sha256" ]] || {
  echo "default-LOWAT and lowat-32k endpoint agent binaries unexpectedly have the same hash" >&2
  exit 2
}

for expected in "$base_source_ref" "$control_source_ref" "$lowat_source_ref" \
  "$common_scaffold_sha256" "$lowat_32k_overlay_sha256" "$overlay_readme_sha256"; do
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
  --arg toggle "$lowat_32k_overlay_sha256" --arg readme "$overlay_readme_sha256" \
  --arg source_hashes "$actual_source_hashes_sha256" --arg provenance "$build_provenance_sha256" \
  --arg driver_sha "$driver_binary_sha256" --arg control_server_sha "$control_server_binary_sha256" \
  --arg lowat_server_sha "$lowat_server_binary_sha256" \
  --arg control_agent_sha "$control_agent_binary_sha256" --arg lowat_agent_sha "$lowat_agent_binary_sha256" \
  --arg driver_id "$driver_build_id" --arg control_server_id "$control_server_build_id" \
  --arg lowat_server_id "$lowat_server_build_id" \
  --arg control_agent_id "$control_agent_build_id" --arg lowat_agent_id "$lowat_agent_build_id" '
    .kind == "p2pstream-tunnel-lowat-causal-bundle-v1" and
    .base_source.ref == $base and .build.go_version == "go1.26.6" and .build.tags == [] and
    .overlays.common_scaffold.path == "tunnel-lowat-common.patch" and
    .overlays.common_scaffold.sha256 == $common and
    .overlays.lowat_32k_overlay.path == "tunnel-lowat-candidate.patch" and
    .overlays.lowat_32k_overlay.sha256 == $toggle and
    .evidence.readme.path == "README.md" and
    .evidence.readme.sha256 == $readme and
    .evidence.source_hashes.path == "source-hashes.json" and
    .evidence.source_hashes.sha256 == $source_hashes and
    .evidence.build_provenance.path == "build-provenance.txt" and
    .evidence.build_provenance.sha256 == $provenance and
    .driver_origin_identical_to == "tunnel-lowat-default-control.server" and
    .driver_origin.path == "driver-origin.test" and
    .driver_origin.sha256 == $driver_sha and .driver_origin.build_id == $driver_id and
    .variants["tunnel-lowat-default-control"].apply_tunnel_notsent_lowat == false and
    .variants["tunnel-lowat-32k"].apply_tunnel_notsent_lowat == true and
    .variants["tunnel-lowat-default-control"].source_identity == ($base + "+tunnel-lowat-default-control@sha256:" + $common) and
    .variants["tunnel-lowat-32k"].source_identity == ($base + "+tunnel-lowat-32k@sha256:" + $common + "+sha256:" + $toggle) and
    .variants["tunnel-lowat-default-control"].binaries.server.path == "tunnel-lowat-default-control.server.test" and
    .variants["tunnel-lowat-32k"].binaries.server.path == "tunnel-lowat-32k.server.test" and
    .variants["tunnel-lowat-default-control"].binaries.agent.path == "tunnel-lowat-default-control.agent.test" and
    .variants["tunnel-lowat-32k"].binaries.agent.path == "tunnel-lowat-32k.agent.test" and
    .variants["tunnel-lowat-default-control"].binaries.server.sha256 == $control_server_sha and
    .variants["tunnel-lowat-32k"].binaries.server.sha256 == $lowat_server_sha and
    .variants["tunnel-lowat-default-control"].binaries.agent.sha256 == $control_agent_sha and
    .variants["tunnel-lowat-32k"].binaries.agent.sha256 == $lowat_agent_sha and
    .variants["tunnel-lowat-default-control"].binaries.server.build_id == $control_server_id and
    .variants["tunnel-lowat-32k"].binaries.server.build_id == $lowat_server_id and
    .variants["tunnel-lowat-default-control"].binaries.agent.build_id == $control_agent_id and
    .variants["tunnel-lowat-32k"].binaries.agent.build_id == $lowat_agent_id and
    .validation.patch_replay.forward_exact == true and
    .validation.patch_replay.reverse_exact == true and
    .validation.new_focused_normal.count == 10 and
    .validation.new_focused_normal.variants == ["tunnel-lowat-default-control", "tunnel-lowat-32k"] and
    .validation.new_focused_race.count == 5 and
    .validation.new_focused_race.variants == ["tunnel-lowat-default-control", "tunnel-lowat-32k"] and
    .validation.server_existing_normal.count == 1 and
    .validation.server_existing_normal.variants == ["tunnel-lowat-default-control", "tunnel-lowat-32k"] and
    .validation.server_existing_race.count == 1 and
    .validation.server_existing_race.variants == ["tunnel-lowat-default-control", "tunnel-lowat-32k"] and
    .validation.full_tunnel_agent_normal.count == 1 and
    .validation.full_tunnel_agent_normal.variants == ["tunnel-lowat-default-control", "tunnel-lowat-32k"] and
    .validation.endpoint_compile.agent == true and
    .validation.endpoint_compile.server == true and
    .validation.endpoint_compile.ordinary_tag_off == true and
    all([
      .driver_origin,
      .variants["tunnel-lowat-default-control"].binaries.server,
      .variants["tunnel-lowat-32k"].binaries.server,
      .variants["tunnel-lowat-default-control"].binaries.agent,
      .variants["tunnel-lowat-32k"].binaries.agent
    ][]; (.sha256 | type == "string" and test("^[0-9a-f]{64}$")) and
      (.build_id | type == "string" and length > 0))
  ' "$snapshot_root/build-manifest.json" >/dev/null || {
  echo "build manifest does not match the exact source, overlay, tag-off and binary identities" >&2
  exit 2
}

cat > "$output_dir/predeclared-gates.json" <<'JSON'
{
  "diagnostic_only": true,
  "every_pair_download_lowat_to_control_ratio_minimum": 1.10,
  "every_pair_upload_lowat_to_control_ratio_minimum": 0.90,
  "secondary_transition_screen": {
    "lowat_all_24_download_samples_minimum_bytes_per_second": 29700000,
    "control_pairs_with_subthreshold_download_sample_minimum": 2,
    "all_pair_upload_gates_must_pass": true,
    "exact_correctness_and_cleanup": true,
    "does_not_override_main_pair_gates": true
  },
  "physical_tunnel_socket_gate": {
    "snapshots": ["before-load", "after-load"],
    "observations_per_endpoint": 5,
    "server_lanes": [0, 1, 2, 3, 4],
    "reverse_tuple_match": "exact",
    "observation_set_after_load": "identical",
    "dropped_observations": 0,
    "candidate_notsent_lowat_after": 32768,
    "effective_baseline_minimum_exclusive": 32768,
    "live_polling": false
  },
  "exact_payload_and_event_conservation": true,
  "pairs": 3,
  "pair_order": ["control-then-lowat", "lowat-then-control", "control-then-lowat"],
  "selective_retries": false,
  "total_transfers": 96
}
JSON

if ((preflight_only)); then
  python3 - "$output_dir/preflight-manifest.json" \
    "$base_source_ref" "$control_source_ref" "$lowat_source_ref" \
    "$common_scaffold_sha256" "$lowat_32k_overlay_sha256" "$overlay_readme_sha256" "$actual_source_hashes_sha256" \
    "$runner_sha256" "$wrapper_sha256" \
    "$build_provenance_sha256" "$build_manifest_sha256" "$driver_binary_sha256" "$control_server_binary_sha256" \
    "$lowat_server_binary_sha256" "$control_agent_binary_sha256" "$lowat_agent_binary_sha256" \
    "$driver_build_id" "$control_server_build_id" "$lowat_server_build_id" \
    "$control_agent_build_id" "$lowat_agent_build_id" <<'PY'
import json
import sys
from pathlib import Path

(
    destination,
    base_source_ref,
    control_source_ref,
    lowat_source_ref,
    common_scaffold_sha256,
    lowat_32k_overlay_sha256,
    overlay_readme_sha256,
    source_hashes_sha256,
    runner_sha256,
    wrapper_sha256,
    build_provenance_sha256,
    build_manifest_sha256,
    driver_sha256,
    control_server_sha256,
    lowat_server_sha256,
    control_agent_sha256,
    lowat_agent_sha256,
    driver_build_id,
    control_server_build_id,
    lowat_server_build_id,
    control_agent_build_id,
    lowat_agent_build_id,
) = sys.argv[1:]
manifest = {
    "schema_version": 1,
    "scope": "fixture_preflight_only",
    "workload_started": False,
    "acceptance_result": None,
    "source_refs": {
        "application_and_harness": base_source_ref,
        "default_lowat_control_base": control_source_ref,
        "lowat_32k_base": lowat_source_ref,
    },
    "common_scaffold_sha256": common_scaffold_sha256,
    "lowat_32k_overlay_sha256": lowat_32k_overlay_sha256,
    "overlay_readme_sha256": overlay_readme_sha256,
    "source_hashes_sha256": source_hashes_sha256,
    "runner_sha256": runner_sha256,
    "wrapper_sha256": wrapper_sha256,
    "build_provenance_sha256": build_provenance_sha256,
    "build_manifest_sha256": build_manifest_sha256,
    "binary_sha256": {
        "driver_and_origin": driver_sha256,
        "control_server": control_server_sha256,
        "lowat_server": lowat_server_sha256,
        "control_agent": control_agent_sha256,
        "lowat_agent": lowat_agent_sha256,
    },
    "binary_build_id": {
        "driver_and_origin": driver_build_id,
        "control_server": control_server_build_id,
        "lowat_server": lowat_server_build_id,
        "control_agent": control_agent_build_id,
        "lowat_agent": lowat_agent_build_id,
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
  setsid "$snapshot_root/scripts/qualify-proxy-agent-tunnel-lowat-causal.sh"
  --driver-binary "$driver_binary"
  --control-server-binary "$control_server_binary"
  --lowat-server-binary "$lowat_server_binary"
  --control-agent-binary "$control_agent_binary"
  --lowat-agent-binary "$lowat_agent_binary"
  --base-source-ref "$base_source_ref"
  --control-source-ref "$control_source_ref"
  --lowat-source-ref "$lowat_source_ref"
  --common-scaffold-sha256 "$common_scaffold_sha256"
  --lowat-32k-overlay-sha256 "$lowat_32k_overlay_sha256"
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
    "implementation_difference": "identical common 5ee application, Yamux, accounting, buffers and shutdown behavior; the candidate changes only a private bool initializer so both authenticated physical tunnel endpoints request TCP_NOTSENT_LOWAT=32768, while the control observes the option without setting it",
    "source_refs": {
        "application_and_harness": "$base_source_ref",
        "default_lowat_control_base": "$control_source_ref",
        "lowat_32k_base": "$lowat_source_ref",
    },
    "common_scaffold_sha256": "$common_scaffold_sha256",
    "lowat_32k_overlay_sha256": "$lowat_32k_overlay_sha256",
    "overlay_readme_sha256": "$overlay_readme_sha256",
    "source_hashes_sha256": "$actual_source_hashes_sha256",
    "binary_sha256": {
        "driver_and_origin": "$driver_binary_sha256",
        "control_server": "$control_server_binary_sha256",
        "lowat_server": "$lowat_server_binary_sha256",
        "control_agent": "$control_agent_binary_sha256",
        "lowat_agent": "$lowat_agent_binary_sha256",
    },
    "binary_build_id": {
        "driver_and_origin": "$driver_build_id",
        "control_server": "$control_server_build_id",
        "lowat_server": "$lowat_server_build_id",
        "control_agent": "$control_agent_build_id",
        "lowat_agent": "$lowat_agent_build_id",
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
        "order": ["control-then-lowat", "lowat-then-control", "control-then-lowat"],
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
    [[ ! -f results.tunnel-lowat-observations.jsonl ]] || printf '%s\0' results.tunnel-lowat-observations.jsonl
    find snapshot -type f -print0
  } | sort -z | xargs -0 sha256sum > SHA256SUMS
  sha256sum -c SHA256SUMS >/dev/null
)
printf 'results=%s\nsummary=%s\nmanifest=%s\n' "$results" "$summary" "$output_dir/manifest.json"
exit "$runner_status"
