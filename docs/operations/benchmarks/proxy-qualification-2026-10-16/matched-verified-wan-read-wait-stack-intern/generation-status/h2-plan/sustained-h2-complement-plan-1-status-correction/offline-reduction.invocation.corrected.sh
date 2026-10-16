#!/usr/bin/env bash
# SOURCE ONLY — NOT EXECUTED. This is a separate offline step after the arm is closed
# and raw lifecycle/resource/parser/canonicalizer gates are independently accepted.
set -Eeuo pipefail
analyzer='/home/user/.codex/worktrees/astra-proxy-performance/p2pstream/tmp/continuation-20261016/wan-read-wait-stack-intern-generation-status-source/analyze_interned_trace.py'
arm_output='/home/user/.codex/worktrees/astra-proxy-performance/p2pstream/tmp/continuation-20261016/wan-read-wait-stack-intern-source/integration-source/runs/sustained-h2-complement-1'
inputs="$arm_output/read-wait-reductions"
out='/home/user/.codex/worktrees/astra-proxy-performance/p2pstream/tmp/continuation-20261016/wan-read-wait-stack-intern-generation-status-source/runs/sustained-h2-complement-corrected-reductions-1'
[[ ! -e $out ]] || { echo "fresh corrected reduction output exists: $out" >&2; exit 2; }
[[ $(sha256sum "$analyzer" | awk '{print $1}') == 05b3b0fa9d95a35e71ef43f6fa7f101e62238eb3cfb69f4750bc77ce30c00569 ]]
# Before this loop, require the original arm to be closed and preserve its nonzero
# old-analyzer/validator result. Require parser/canonicalizer direct status 0 per sequence.
mkdir -m 700 "$out"
for seq in 9 11 14 15; do
  set +e
  /usr/bin/python3 "$analyzer"     --input "$inputs/sequence-$seq.intern"     --tool-status "$inputs/sequence-$seq.parser.status"     --canonicalizer-status "$inputs/sequence-$seq.canonicalizer.status"     --ledger "$inputs/sequence-$seq.ledger.json"     --output "$out/sequence-$seq.reduction.json"     >"$out/sequence-$seq.stdout" 2>"$out/sequence-$seq.stderr"
  analyzer_status=$?
  set -e
  printf '%s\n' "$analyzer_status" >"$out/sequence-$seq.status"
  if ((analyzer_status != 0)); then
    exit "$analyzer_status"
  fi
done
