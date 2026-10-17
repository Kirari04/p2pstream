from pathlib import Path
import sys
runner=Path(sys.argv[1]).read_text(); wrapper=Path(sys.argv[2]).read_text(); out=Path(sys.argv[3])
block_start=runner.index('declare -A owned_groups=()')
block_end=runner.index('\nmapfile -t cpus',block_start)
block=runner[block_start:block_end]
prelude='''#!/usr/bin/env bash
set -Eeuo pipefail
[[ ${P2PSTREAM_TRANSPORT_CEILING_INNER_FIXTURE:-} == 1 && $$ -eq 1 ]] || { echo "owned inner PID/network namespace required" >&2; exit 2; }
mapfile -t initial_interfaces < <(ip -o link show | awk -F': ' '{sub(/@.*/,"",$2); print $2}')
[[ ${#initial_interfaces[@]} -eq 1 && ${initial_interfaces[0]} == lo && -z $(ip route show) && -z $(ip -6 route show) ]] || { echo "unclean initial network namespace" >&2; exit 2; }
overall_timeout_seconds=120
configured_rtt_ms=80
one_way_netem_ms=40
run_dir=$1
mkdir -p "$run_dir"
topologies_output=$run_dir/topologies.jsonl
: > "$topologies_output"
'''
epilogue='''
mapfile -t cpus < <(python3 - <<'PYCPU'
import os
for cpu in sorted(os.sched_getaffinity(0)): print(cpu)
PYCPU
)
((${#cpus[@]} >= 9))
server_cpu_set=$(IFS=,; echo "${cpus[*]:0:4}")
agent_cpu_set=$(IFS=,; echo "${cpus[*]:4:4}")
load_cpu_set=$(IFS=,; echo "${cpus[*]:8}")
for generation in 1 2 3 4; do
  case $generation in
    1) pair=1; order_position=1; variant=aggregate-yamux ;;
    2) pair=1; order_position=2; variant=raw-tls ;;
    3) pair=2; order_position=1; variant=raw-tls ;;
    4) pair=2; order_position=2; variant=aggregate-yamux ;;
  esac
  start_topology
  destination=$run_dir/generation-$generation-topology.json
  capture_topology "$pair" "$order_position" "$variant" "$destination"
  stop_topology "$destination"
done
((last_joined_generation == 4 && topology_generation == 4 && ${#owned_groups[@]} == 0))
trap - EXIT INT TERM
'''
(out/'topology-harness.sh').write_text(prelude+block+epilogue)
(out/'topology-harness.sh').chmod(0o755)
# Exact current wrapper shim source.
marker="cat > \"$snapshot/scripts/launch-owned-pidns.py\" <<'PY'"
start=wrapper.index(marker); start=wrapper.index('\n',start)+1; end=wrapper.index('\nPY\n',start)
(out/'launch-owned-pidns.py').write_text(wrapper[start:end]+'\n')
# Exact final analyzer body.
marker='python3 - "$output" "$snapshots_output" "$topologies_output"'
start=runner.index(marker); start=runner.index("<<'PY'",start)+len("<<'PY'"); start=runner.index('\n',start)+1; end=runner.index('\nPY\n',start)
(out/'final-analyzer.py').write_text(runner[start:end]+'\n')
