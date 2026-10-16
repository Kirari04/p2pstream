# Aggregate agent cold-control pressure baseline

This is a deliberately failing, finite test-only baseline for the cold-control admission gap found after aggregate session-pool growth. It uses candidate `c3a9fa47d72595a592fb7a1c200704b5ed7cd234`, one server and one real external agent in distinct 4 GiB cgroup-v2 scopes with swap disabled, and the same 80 ms / 1 Gbit/s namespace path as the WAN growth qualification.

This is a **smoke-sized pool-growth cohort**. Four 4 MiB downloads followed by four 4 MiB uploads exercise every receiving bulk lane, then retained warmups preserve the sessions. It proves the pressure fixture, admission failure, cold-control discrepancy, release, and recovery behavior. It does not replace the full 256 MiB all-lane workload, whose earlier receiving pools reached roughly 90–172 MiB per lane.

## Result

The runner exited 1 as required because `control_progress_during_pressure=false`. All other acceptance fields passed.

- The agent reached `critical/memory` at 3,933,089,792 of 4,294,967,296 bytes (91.57%) with 361,877,504 bytes (345.11 MiB) remaining below the hard cgroup limit. The test stopped at the first sampled step at or above 90%.
- Agent and server `memory.events` exposed both `oom` and `oom_kill`; both remained zero. Swap was zero. The server's sampled cgroup peak was 80,719,872 bytes.
- A cold public request returned 5xx with zero useful completions. Its event was persisted, the queue drained, no event was dropped, and the agent pressure-rejection counter increased from 0 to 1.
- A real one-shot health request on control lane 0 then failed 503 with `agent_resource_pressure`; the agent pressure-rejection counter increased again from 1 to 2. Server control and total in-use counters were zero before and after the rejected call, so connected lanes alone did not mask progress failure.
- After releasing the 3,712 MiB test ballast and forcing memory return, the agent cgroup fell to 24,477,696 bytes and the controller returned healthy. All five physical sessions remained connected. Every bulk lane was free, had no buffered or pending-refund units, and had zero logical streams. The retained agent session reservation was 75,827,712 bytes.
- The post-release health request succeeded. Exact-hash 16 MiB download and upload recovery requests both completed, with one persisted event apiece and no event loss.

The pressure fixture records server and agent cgroups independently and never sums separately sampled peaks. The cold-control failure is a real failed gate, while the successful release and functional recovery remain valid supporting evidence.

## Replay

Build from a clean archive of the candidate ref, then run the snapshotted harness. The legacy binary argument is mandatory at the command surface but unused in pressure mode.

```bash
git archive c3a9fa47d72595a592fb7a1c200704b5ed7cd234 | tar -x -C "$BUILD_ROOT"
(cd "$BUILD_ROOT" && GOMAXPROCS=4 go test -c -o bin/server.test ./internal/server)
(cd "$BUILD_ROOT" && GOMAXPROCS=4 go test -c -o bin/agent.test ./internal/agent)

bash qualify-proxy-agent-growth.sh.txt \
  --server-binary "$BUILD_ROOT/bin/server.test" \
  --aggregate-agent-binary "$BUILD_ROOT/bin/agent.test" \
  --legacy-agent-binary "$BUILD_ROOT/bin/agent.test" \
  --output results.jsonl \
  --server-source-ref c3a9fa47d72595a592fb7a1c200704b5ed7cd234 \
  --aggregate-agent-source-ref c3a9fa47d72595a592fb7a1c200704b5ed7cd234 \
  --legacy-agent-source-ref unused-pressure-mode \
  --smoke --pressure
```

The expected exit for this baseline is 1 after `summary.json` is written. A corrected candidate must retain public pressure rejection while making the cold control roundtrip succeed, then pass full-size grown-pool pressure qualification.
