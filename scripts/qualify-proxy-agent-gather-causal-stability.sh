#!/usr/bin/env bash
# Run the predeclared aggregate-v1 gather-versus-split causal diagnostic.
set -Eeuo pipefail

root_dir=$(cd "$(dirname "$0")/.." && pwd)
runner="$root_dir/scripts/qualify-proxy-agent-gather-causal.sh"
server_binary=""
gather_agent_binary=""
split_agent_binary=""
server_source_ref=""
agent_base_source_ref=""
split_overlay=""
overlay_readme=""
build_provenance=""
output_dir=""

while (($#)); do
  case "$1" in
    --runner) runner=$2; shift 2 ;;
    --server-binary) server_binary=$2; shift 2 ;;
    --gather-agent-binary) gather_agent_binary=$2; shift 2 ;;
    --split-agent-binary) split_agent_binary=$2; shift 2 ;;
    --server-source-ref) server_source_ref=$2; shift 2 ;;
    --agent-base-source-ref) agent_base_source_ref=$2; shift 2 ;;
    --split-overlay) split_overlay=$2; shift 2 ;;
    --overlay-readme) overlay_readme=$2; shift 2 ;;
    --build-provenance) build_provenance=$2; shift 2 ;;
    --output-dir) output_dir=$2; shift 2 ;;
    *) echo "unknown argument: $1" >&2; exit 2 ;;
  esac
done

expected_server_ref=dc738e9000e60cd7ce3a0279127486f3bc05067a
expected_agent_ref=5eeccaaa797254d7bde4db9854a337a02c3bc28e
expected_overlay_sha256=ca680a7062735c3d1f236fc5d9d53c66b92724fd5b0dd1d82eef53f299b60d4f
expected_overlay_readme_sha256=8da94bdb538cd98954c9847d84497df395ec5755c62f81353a4998d4a3761db0
expected_runtime_hunk_sha256=e84c4de1351c78bcefecaac8701cae48b886e306c74118426872bfff402d942d

[[ -f $runner && -f $split_overlay && -f $overlay_readme && -f $build_provenance ]] || {
  echo "runner, split overlay, overlay README and build provenance must be readable files" >&2
  exit 2
}
[[ -x $server_binary && -x $gather_agent_binary && -x $split_agent_binary ]] || {
  echo "server, gather-agent and split-agent binaries must be executable" >&2
  exit 2
}
[[ $server_source_ref == "$expected_server_ref" ]] || {
  echo "server source must be the frozen dc738e9 runtime" >&2
  exit 2
}
[[ $agent_base_source_ref == "$expected_agent_ref" ]] || {
  echo "both agent variants must use the frozen 5eeccaa base" >&2
  exit 2
}
for tool in setsid sha256sum python3 realpath find go grep; do
  command -v "$tool" >/dev/null || { echo "$tool is required" >&2; exit 2; }
done

overlay_sha256=$(sha256sum "$split_overlay" | awk '{print $1}')
[[ $overlay_sha256 == "$expected_overlay_sha256" ]] || {
  echo "split overlay hash $overlay_sha256 does not match the reviewed overlay" >&2
  exit 2
}
actual_overlay_readme_sha256=$(sha256sum "$overlay_readme" | awk '{print $1}')
[[ $actual_overlay_readme_sha256 == "$expected_overlay_readme_sha256" ]] || {
  echo "overlay README hash $actual_overlay_readme_sha256 does not match the reviewed design" >&2
  exit 2
}

if [[ -z $output_dir ]]; then
  mkdir -p "$root_dir/tmp/proxy-agent-gather-causal"
  output_dir=$(mktemp -d -p "$root_dir/tmp/proxy-agent-gather-causal" run.XXXXXXXX)
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
cp "$runner" "$snapshot_root/scripts/qualify-proxy-agent-gather-causal.sh"
cp "$0" "$snapshot_root/scripts/qualify-proxy-agent-gather-causal-stability.sh"
cp "$split_overlay" "$snapshot_root/overlay/agent-data-gather-revert.patch"
cp "$overlay_readme" "$snapshot_root/overlay/README.md"
cp "$build_provenance" "$snapshot_root/build-provenance.txt"
chmod +x "$snapshot_root/scripts/qualify-proxy-agent-gather-causal.sh"

server_binary=$(realpath "$server_binary")
gather_agent_binary=$(realpath "$gather_agent_binary")
split_agent_binary=$(realpath "$split_agent_binary")
results="$output_dir/results.jsonl"
summary="$output_dir/summary.json"

runner_sha256=$(sha256sum "$snapshot_root/scripts/qualify-proxy-agent-gather-causal.sh" | awk '{print $1}')
wrapper_sha256=$(sha256sum "$snapshot_root/scripts/qualify-proxy-agent-gather-causal-stability.sh" | awk '{print $1}')
overlay_readme_sha256=$(sha256sum "$snapshot_root/overlay/README.md" | awk '{print $1}')
build_provenance_sha256=$(sha256sum "$snapshot_root/build-provenance.txt" | awk '{print $1}')
server_binary_sha256=$(sha256sum "$server_binary" | awk '{print $1}')
gather_agent_binary_sha256=$(sha256sum "$gather_agent_binary" | awk '{print $1}')
split_agent_binary_sha256=$(sha256sum "$split_agent_binary" | awk '{print $1}')
server_build_id=$(go tool buildid "$server_binary")
gather_agent_build_id=$(go tool buildid "$gather_agent_binary")
split_agent_build_id=$(go tool buildid "$split_agent_binary")
[[ -n $server_build_id && -n $gather_agent_build_id && -n $split_agent_build_id ]] || {
  echo "all binaries must carry readable Go build IDs" >&2
  exit 2
}
[[ $gather_agent_binary_sha256 != "$split_agent_binary_sha256" ]] || {
  echo "gather and split binaries unexpectedly have the same hash" >&2
  exit 2
}

for expected in "$server_binary_sha256" "$gather_agent_binary_sha256" "$split_agent_binary_sha256"; do
  grep -F "$expected" "$snapshot_root/build-provenance.txt" >/dev/null || {
    echo "build provenance does not contain binary hash $expected" >&2
    exit 2
  }
done
for expected in "$server_source_ref" "$agent_base_source_ref" "$overlay_sha256" "$expected_runtime_hunk_sha256"; do
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
  echo "agent variants must be ordinary tag-off builds" >&2
  exit 2
fi

cat > "$output_dir/predeclared-gates.json" <<'JSON'
{
  "diagnostic_only": true,
  "every_pair_download_split_to_gather_ratio_minimum": 1.10,
  "every_pair_upload_split_to_gather_ratio_minimum": 0.90,
  "exact_payload_and_event_conservation": true,
  "pairs": 3,
  "pair_order": ["gather-then-split", "split-then-gather", "gather-then-split"],
  "selective_retries": false,
  "total_transfers": 96
}
JSON

child_pid=""
stop_child() {
  local signal=$1
  [[ -n $child_pid ]] || return 0
  kill -"$signal" -- "-$child_pid" 2>/dev/null || true
  for ((attempt=0; attempt<150; attempt++)); do
    kill -0 "$child_pid" 2>/dev/null || return 0
    sleep .1
  done
  kill -KILL -- "-$child_pid" 2>/dev/null || true
}
on_interrupt() { stop_child TERM; exit 130; }
on_terminate() { stop_child TERM; exit 143; }
trap on_interrupt INT
trap on_terminate TERM

started_utc=$(date -u +%Y-%m-%dT%H:%M:%SZ)
run_command=(
  setsid "$snapshot_root/scripts/qualify-proxy-agent-gather-causal.sh"
  --server-binary "$server_binary"
  --gather-agent-binary "$gather_agent_binary"
  --split-agent-binary "$split_agent_binary"
  --server-source-ref "$server_source_ref"
  --gather-agent-source-ref "$agent_base_source_ref"
  --split-agent-source-ref "$agent_base_source_ref"
  --split-overlay-sha256 "$overlay_sha256"
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
    "implementation_difference": "split variant preserves owned payload/capacity/commit and writes DATA header then body instead of one gathered physical write",
    "source_refs": {
        "server": "$server_source_ref",
        "agent_base_both_variants": "$agent_base_source_ref",
    },
    "split_overlay_sha256": "$overlay_sha256",
    "split_overlay_runtime_hunk_sha256": "$expected_runtime_hunk_sha256",
    "overlay_readme_sha256": "$overlay_readme_sha256",
    "binary_sha256": {
        "server": "$server_binary_sha256",
        "gather_agent": "$gather_agent_binary_sha256",
        "split_agent": "$split_agent_binary_sha256",
    },
    "binary_build_id": {
        "server": "$server_build_id",
        "gather_agent": "$gather_agent_build_id",
        "split_agent": "$split_agent_build_id",
    },
    "runner_sha256": "$runner_sha256",
    "wrapper_sha256": "$wrapper_sha256",
    "build_provenance_sha256": "$build_provenance_sha256",
    "started_utc": "$started_utc",
    "completed_utc": "$completed_utc",
    "runner_exit_status": $runner_status,
    "fixed_workload": {
        "pairs": 3,
        "order": ["gather-then-split", "split-then-gather", "gather-then-split"],
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
        "complete_pass" if summary and summary.get("acceptance", {}).get("overall")
        else "complete_timing_rejection" if summary and summary.get("acceptance", {}).get("complete") and summary.get("acceptance", {}).get("all_rows_exact")
        else "correctness_safety_or_fixture_failure"
    ),
    "raw_fixture_artifacts": "local only; excluded from SHA256SUMS and evidence because they contain ephemeral fixture credentials",
}
Path("$output_dir/manifest.json").write_text(json.dumps(manifest, indent=2, sort_keys=True) + "\n", encoding="utf-8")
PY

(
  cd "$output_dir"
  {
    printf '%s\0' manifest.json predeclared-gates.json results.jsonl run-command.txt run.log
    [[ ! -f summary.json ]] || printf '%s\0' summary.json
    find snapshot -type f -print0
  } | sort -z | xargs -0 sha256sum > SHA256SUMS
  sha256sum -c SHA256SUMS >/dev/null
)
printf 'results=%s\nsummary=%s\nmanifest=%s\n' "$results" "$summary" "$output_dir/manifest.json"
exit "$runner_status"
