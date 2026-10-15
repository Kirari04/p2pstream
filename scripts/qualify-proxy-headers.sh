#!/usr/bin/env bash
# Finite header-burst staircase using a clean production archive. Only the
# explicit test harness is overlaid; concurrent implementation edits stay out.
set -Eeuo pipefail
cd "$(dirname "$0")/.."
umask 077
for tool in go git docker python3 taskset sha256sum; do
  command -v "$tool" >/dev/null || { echo "$tool is required" >&2; exit 1; }
done
docker info >/dev/null
source_commit=$(git rev-parse "${1:-HEAD}^{commit}")
mkdir -p tmp/header-burst
run_dir=$(realpath "$(mktemp -d -p tmp/header-burst run.XXXXXX)")
build_dir="$run_dir/source"
mkdir -p "$build_dir"
git archive "$source_commit" | tar -x -C "$build_dir"
install -m 0644 internal/server/public_header_burst_qualification_test.go \
  "$build_dir/internal/server/public_header_burst_qualification_test.go"
binary="$run_dir/server.test"
(cd "$build_dir" && go test -c -o "$binary" ./internal/server)
mapfile -t available_cpus < <(python3 - <<'PY'
import os
for cpu in sorted(os.sched_getaffinity(0)):
    print(cpu)
PY
)
proxy_count=${#available_cpus[@]}
((proxy_count > 4)) && proxy_count=4
proxy_cpu_set=$(IFS=,; echo "${available_cpus[*]:0:proxy_count}")
load_cpu_set=$proxy_cpu_set
if ((${#available_cpus[@]} > proxy_count)); then
  load_cpu_set=$(IFS=,; echo "${available_cpus[*]:proxy_count}")
fi
binary_sha=$(sha256sum "$binary" | cut -d ' ' -f 1)
harness_sha=$(sha256sum "$build_dir/internal/server/public_header_burst_qualification_test.go" | cut -d ' ' -f 1)
echo "Header-burst source $source_commit; evidence $run_dir"
P2PSTREAM_HEADER_BURST=driver \
P2PSTREAM_HEADER_BURST_DIR="$run_dir" \
P2PSTREAM_HEADER_BURST_SOURCE="$source_commit" \
P2PSTREAM_HEADER_BURST_BINARY_SHA256="$binary_sha" \
P2PSTREAM_HEADER_BURST_HARNESS_SHA256="$harness_sha" \
P2PSTREAM_HEADER_BURST_PROXY_CPUS="$proxy_cpu_set" \
P2PSTREAM_HEADER_BURST_DRIVER_CPUS="$load_cpu_set" \
  taskset -c "$load_cpu_set" "$binary" \
    -test.run '^TestPublicHeaderBurstQualification$' -test.v -test.timeout 8m \
    2>&1 | tee "$run_dir/driver.log"
