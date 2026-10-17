#!/bin/bash
set +e
mkdir -m 0700 /home/user/.codex/worktrees/astra-proxy-performance/p2pstream/tmp/continuation-20261016/wan-read-wait-stack-intern-source/independent-canonical-partition-source/runs/sustained-h1-1
mkdir_status=$?
if ((mkdir_status != 0)); then
  printf '%s\n' "$mkdir_status" > /home/user/.codex/worktrees/astra-proxy-performance/p2pstream/tmp/continuation-20261016/wan-read-wait-stack-intern-source/independent-canonical-partition-source/runs/sustained-h1-1.status
  printf 'INDEPENDENT_CANONICAL_H1_STATUS=%s\n' "$mkdir_status"
  exit "$mkdir_status"
fi
/usr/bin/python3.14 /home/user/.codex/worktrees/astra-proxy-performance/p2pstream/tmp/continuation-20261016/wan-read-wait-stack-intern-source/independent-canonical-partition-source/audit_canonical_handler_partitions.py --index /home/user/.codex/worktrees/astra-proxy-performance/p2pstream/tmp/continuation-20261016/wan-read-wait-stack-intern-source/integration-source/runs/sustained-matched-protocol-1/h1/read-wait-reductions/index.json --format-module /home/user/.codex/worktrees/astra-proxy-performance/p2pstream/tmp/continuation-20261016/wan-read-wait-stack-intern-source/stack_intern_format.py --output /home/user/.codex/worktrees/astra-proxy-performance/p2pstream/tmp/continuation-20261016/wan-read-wait-stack-intern-source/independent-canonical-partition-source/runs/sustained-h1-1/audit.json > /home/user/.codex/worktrees/astra-proxy-performance/p2pstream/tmp/continuation-20261016/wan-read-wait-stack-intern-source/independent-canonical-partition-source/runs/sustained-h1-1.stdout.log 2> /home/user/.codex/worktrees/astra-proxy-performance/p2pstream/tmp/continuation-20261016/wan-read-wait-stack-intern-source/independent-canonical-partition-source/runs/sustained-h1-1.stderr.log
status=$?
printf '%s\n' "$status" > /home/user/.codex/worktrees/astra-proxy-performance/p2pstream/tmp/continuation-20261016/wan-read-wait-stack-intern-source/independent-canonical-partition-source/runs/sustained-h1-1.status
printf 'INDEPENDENT_CANONICAL_H1_STATUS=%s\n' "$status"
exit "$status"
