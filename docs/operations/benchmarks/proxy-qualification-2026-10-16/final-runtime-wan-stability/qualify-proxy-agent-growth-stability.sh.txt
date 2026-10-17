#!/usr/bin/env bash
# Run three complete, alternating large-transfer pairs and require every pair
# to retain the aggregate-versus-compatible-legacy throughput gate.
set -Eeuo pipefail

root_dir=$(cd "$(dirname "$0")/.." && pwd)
runner="$root_dir/scripts/qualify-proxy-agent-growth.sh"
server_binary=""
aggregate_agent_binary=""
legacy_agent_binary=""
server_source_ref=""
aggregate_agent_source_ref=""
legacy_agent_source_ref=""
output_dir=""

while (($#)); do
  case "$1" in
    --runner) runner=$2; shift 2 ;;
    --server-binary) server_binary=$2; shift 2 ;;
    --aggregate-agent-binary) aggregate_agent_binary=$2; shift 2 ;;
    --legacy-agent-binary) legacy_agent_binary=$2; shift 2 ;;
    --server-source-ref) server_source_ref=$2; shift 2 ;;
    --aggregate-agent-source-ref) aggregate_agent_source_ref=$2; shift 2 ;;
    --legacy-agent-source-ref) legacy_agent_source_ref=$2; shift 2 ;;
    --output-dir) output_dir=$2; shift 2 ;;
    *) echo "unknown argument: $1" >&2; exit 2 ;;
  esac
done

[[ -f $runner ]] || { echo "--runner must name a file" >&2; exit 2; }
[[ -x $server_binary && -x $aggregate_agent_binary && -x $legacy_agent_binary ]] || {
  echo "all three --*-binary arguments must name executables" >&2
  exit 2
}
[[ -n $server_source_ref && -n $aggregate_agent_source_ref && -n $legacy_agent_source_ref ]] || {
  echo "all three --*-source-ref arguments are required" >&2
  exit 2
}
for tool in setsid sha256sum python3 realpath find; do
  command -v "$tool" >/dev/null || { echo "$tool is required" >&2; exit 2; }
done

if [[ -z $output_dir ]]; then
  mkdir -p "$root_dir/tmp/proxy-agent-growth-stability"
  output_dir=$(mktemp -d -p "$root_dir/tmp/proxy-agent-growth-stability" run.XXXXXXXX)
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
mkdir -p "$snapshot_root/scripts"
cp "$runner" "$snapshot_root/scripts/qualify-proxy-agent-growth.sh"
cp "$0" "$snapshot_root/scripts/qualify-proxy-agent-growth-stability.sh"
chmod +x "$snapshot_root/scripts/qualify-proxy-agent-growth.sh"
results="$output_dir/results.jsonl"
summary="$output_dir/summary.json"

server_binary=$(realpath "$server_binary")
aggregate_agent_binary=$(realpath "$aggregate_agent_binary")
legacy_agent_binary=$(realpath "$legacy_agent_binary")
runner_sha256=$(sha256sum "$snapshot_root/scripts/qualify-proxy-agent-growth.sh" | awk '{print $1}')
wrapper_sha256=$(sha256sum "$snapshot_root/scripts/qualify-proxy-agent-growth-stability.sh" | awk '{print $1}')
server_binary_sha256=$(sha256sum "$server_binary" | awk '{print $1}')
aggregate_agent_binary_sha256=$(sha256sum "$aggregate_agent_binary" | awk '{print $1}')
legacy_agent_binary_sha256=$(sha256sum "$legacy_agent_binary" | awk '{print $1}')

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

set +e
setsid "$snapshot_root/scripts/qualify-proxy-agent-growth.sh" \
  --server-binary "$server_binary" \
  --aggregate-agent-binary "$aggregate_agent_binary" \
  --legacy-agent-binary "$legacy_agent_binary" \
  --server-source-ref "$server_source_ref" \
  --aggregate-agent-source-ref "$aggregate_agent_source_ref" \
  --legacy-agent-source-ref "$legacy_agent_source_ref" \
  --output "$results" \
  --runs 3 \
  --warmup-mib 256 \
  --steady-mib 256 \
  --transfer-repetitions 3 \
  --required-pool-mib 8 \
  --rtt-ms 80 &
child_pid=$!
wait "$child_pid"
runner_status=$?
child_pid=""
set -e

set +e
python3 - "$results" "$summary" "$runner_status" <<'PY'
import json
import statistics
import sys
from pathlib import Path

source, destination, runner_status = Path(sys.argv[1]), Path(sys.argv[2]), int(sys.argv[3])
rows = []
if source.exists():
    with source.open(encoding="utf-8") as input_file:
        rows = [json.loads(line) for line in input_file if line.strip()]

summary = {
    "acceptance": {},
    "complete": len(rows) == 96,
    "gates": {"minimum_every_pair_direction_ratio": 0.90},
    "mode_order": ["legacy-then-aggregate", "aggregate-then-legacy", "legacy-then-aggregate"],
    "runner_exit_status": runner_status,
    "row_count": len(rows),
    "pairs": {},
}
failures = []
if len(rows) != 96:
    failures.append(f"row count {len(rows)}, want 96")

expected_orders = [
    ("legacy", "aggregate-v1"),
    ("aggregate-v1", "legacy"),
    ("legacy", "aggregate-v1"),
]
for pair_index, expected_order in enumerate(expected_orders, 1):
    pair_rows = rows[(pair_index - 1) * 32 : pair_index * 32]
    pair = {}
    observed_order = []
    for row in pair_rows:
        mode = row.get("transport_mode")
        if mode and (not observed_order or observed_order[-1] != mode):
            observed_order.append(mode)
    if tuple(observed_order) != expected_order:
        failures.append(
            f"pair {pair_index} mode order {observed_order!r}, want {list(expected_order)!r}"
        )
    for direction in ("download", "upload"):
        values = {}
        for mode in ("legacy", "aggregate-v1"):
            selected = [
                row["useful_transfer_bytes_per_second"]
                for row in pair_rows
                if row.get("measurement_stage") == "steady"
                and row.get("transport_mode") == mode
                and row.get("transfer_direction") == direction
            ]
            if len(selected) != 3:
                failures.append(
                    f"pair {pair_index} {mode} {direction} has {len(selected)} timed rows, want 3"
                )
                values[mode] = None
            else:
                values[mode] = selected
        if None in values.values():
            ratio = None
            passed = False
            aggregate_median = None
            legacy_median = None
        else:
            aggregate_median = statistics.median(values["aggregate-v1"])
            legacy_median = statistics.median(values["legacy"])
            ratio = aggregate_median / legacy_median
            passed = ratio >= 0.90
        pair[direction] = {
            "aggregate_median_useful_bytes_per_second": aggregate_median,
            "aggregate_samples": values["aggregate-v1"],
            "legacy_median_useful_bytes_per_second": legacy_median,
            "legacy_samples": values["legacy"],
            "ratio": ratio,
            "passed": passed,
        }
        if not passed:
            failures.append(f"pair {pair_index} {direction} ratio {ratio!r} is below 0.90")
    pair["mode_order"] = observed_order
    summary["pairs"][str(pair_index)] = pair

steady = [row for row in rows if row.get("measurement_stage") == "steady"]
for direction in ("download", "upload"):
    baseline = [
        row["useful_transfer_bytes_per_second"]
        for row in steady
        if row.get("transport_mode") == "legacy" and row.get("transfer_direction") == direction
    ]
    candidate = [
        row["useful_transfer_bytes_per_second"]
        for row in steady
        if row.get("transport_mode") == "aggregate-v1" and row.get("transfer_direction") == direction
    ]
    if baseline and candidate:
        summary.setdefault("pooled_diagnostic", {})[direction] = {
            "aggregate_median_useful_bytes_per_second": statistics.median(candidate),
            "legacy_median_useful_bytes_per_second": statistics.median(baseline),
            "ratio_of_medians": statistics.median(candidate) / statistics.median(baseline),
            "aggregate_samples": candidate,
            "legacy_samples": baseline,
        }

for index, row in enumerate(rows, 1):
    row_ok = (
        row.get("attempted") == 1
        and row.get("useful_completed") == 1
        and row.get("errors") == 0
        and row.get("persisted_event_delta") == 1
        and row.get("dropped_event_delta", 0) == 0
        and row.get("event_queue_after_drain", 0) == 0
        and not row.get("event_drain_error")
    )
    if not row_ok:
        failures.append(f"row {index} failed payload/request/event conservation")

summary["acceptance"]["runner_passed"] = runner_status == 0
summary["acceptance"]["all_rows_conserved"] = not any("conservation" in failure for failure in failures)
summary["acceptance"]["every_pair_direction_passed"] = all(
    summary["pairs"].get(str(pair), {}).get(direction, {}).get("passed", False)
    for pair in (1, 2, 3)
    for direction in ("download", "upload")
)
summary["acceptance"]["overall"] = not failures and runner_status == 0
summary["failures"] = failures
with destination.open("w", encoding="utf-8") as output_file:
    json.dump(summary, output_file, indent=2, sort_keys=True)
    output_file.write("\n")
raise SystemExit(0 if summary["acceptance"]["overall"] else 1)
PY
summary_status=$?
set -e

python3 - "$output_dir/manifest.json" <<PY
import json
from pathlib import Path

destination = Path("$output_dir/manifest.json")
manifest = {
    "source_refs": {
        "server": "$server_source_ref",
        "aggregate_agent": "$aggregate_agent_source_ref",
        "legacy_agent": "$legacy_agent_source_ref",
    },
    "binary_sha256": {
        "server": "$server_binary_sha256",
        "aggregate_agent": "$aggregate_agent_binary_sha256",
        "legacy_agent": "$legacy_agent_binary_sha256",
    },
    "runner_sha256": "$runner_sha256",
    "wrapper_sha256": "$wrapper_sha256",
    "runner_exit_status": $runner_status,
    "summary_exit_status": $summary_status,
    "fixed_workload": {
        "runs": 3,
        "mode_order": ["legacy-then-aggregate", "aggregate-then-legacy", "legacy-then-aggregate"],
        "warmup_mib": 256,
        "steady_mib": 256,
        "transfer_repetitions": 3,
        "required_pool_mib": 8,
        "configured_rtt_ms": 80,
    },
}
destination.write_text(json.dumps(manifest, indent=2, sort_keys=True) + "\n", encoding="utf-8")
PY

(
  cd "$output_dir"
  sha256sum results.jsonl summary.json manifest.json \
    snapshot/scripts/qualify-proxy-agent-growth.sh \
    snapshot/scripts/qualify-proxy-agent-growth-stability.sh > SHA256SUMS
)
printf 'results=%s\nsummary=%s\nmanifest=%s\n' "$results" "$summary" "$output_dir/manifest.json"
((runner_status == 0 && summary_status == 0))
