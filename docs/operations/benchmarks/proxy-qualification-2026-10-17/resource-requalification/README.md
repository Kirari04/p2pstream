# Resource requalification at `8fa1637` — passed

The exact immutable checkpoint `8fa16378af4ac0117276726fe6e51d8bc4c74c85` passed a fresh original absolute candidate capacity cohort and a separate original grown-pool pressure cohort using ordinary Go 1.26.6 binaries. This packet does not requalify the later CPU identity change at `1fa81d839f35ae575c41779a309d356c23c1f956`, claim performance parity, or turn old cold-boundary observations into current results.

## Held capacity and recovery

| Topology | Held H1 useful / events / origin peak | Public connection / request owner peaks | H2 recovery useful / events |
| --- | --- | --- | --- |
| Combined server + colocated agent, 4 GiB | 1536 / 1536 / 1536 | 1536 / 1536 | 512 / 512 |
| Separate one-agent 4 GiB cgroups | 1536 / 1536 / 1536 | 1536 / 1536 | 512 / 512 |
| Separate four-agent 4 GiB cgroups | 1536 / 1536 / 1536 | 1536 / 1536 | 512 / 512 |

The original 1 GiB touched server ballast, 3 s held barrier and 200 ms post-release hold remain. All statuses, payloads, exact request events, ownership peaks and readable zero-OOM checks pass. The current-agent / old-server one-lane and four-lane compatibility bursts each conserve 128 held + 512 recovery events with zero errors; their observed legacy session envelopes are 181,586,560 / 726,346,240 B, exactly 1:4. All 22 natural process exits have status zero, and every container exit is verified. Outer elapsed runtime was 21.8997 s with no forced cleanup, adopted descendants or surviving owned processes or containers. Detailed per-topology rows, summary and receipts are retained under `capacity/`.

## Grown-pool pressure

The independent pressure cohort preserved the original host-PID / user-net topology, separate 4 GiB no-swap systemd user scopes, four bulk lanes plus one control lane, 80 ms / 1 Gbit shaping, four 256 MiB warmups in each direction, retained 256 MiB transfers, a canceled 32 MiB upload and 16 MiB recovery in each direction. All 13 literal-true original gates, 12 original transfer rows and 14 conserved event rows pass.

All receiving pools exceed 80 MiB before pressure and remain identical after recovery: server [85.75, 88.25, 90, 90.5] MiB; agent [82.5, 91, 91.5, 92.75] MiB. Actual agent memory reaches 3,885,223,936 / 4,294,967,296 B with 3648 MiB ballast, leaving 409,743,360 B below the hard limit. Both cgroups retain zero OOM and OOM-kill counters. One public 503 changes pressure rejection from 0 to 1; a cold reserved-control request returns 200 while ordinary admission remains zero, leaves pressure rejection at 1 and returns control ownership to 0. Cancellation reaches the origin (independent observation: 9,502,720 B), preserves all five session identities and returns lane allocation and refunds to a quiescent state. Both 16 MiB recovery transfers complete with status 200 and exact events.

The cohort completes in 131.44 s with six natural owner exits, each with status zero, full process-group joins, empty owned endpoint scopes and no emergency or forced cleanup. Detailed results, lifecycle, summary and independent attestation are under `pressure/`.

## Source, build and preserved preparation failures

Both phases reuse the exact ordinary route-locality server `5d7552f7a51f907dcbbd4c7b3e73590a3a887cbf1333cc7da058a8e23ee27577` and agent `c8f541da6c4be5ea3ed4ae3cc1df9bfb188da210d78ffc00962602354ff41537`. The coordinator verified all 430 compile inputs before and after each phase. The independent saved-evidence audit subsequently compared all 430 inputs against the archived `8fa1637` sources, all 10,375 current-source files and 784 legacy-source files against their exact Git archives (plus the canonical legacy fixture), all ten pressure manifest inputs, executable hashes and the unchanged original pressure runner and validator. Source archives, binaries and credentials are omitted; their exact hashes, refs and full-source audit result are retained.

The original capacity preparation stopped before any endpoints because its file-set scan counted two ignored runtime certificate filenames. Its failure and original source map remain. A distinct continuation explicitly excludes only those known noncompile runtime paths; no certificate or key content was read or exported, and unknown additional compile inputs remain rejected. The successful endpoint cohort is the continuation.

The first offline pressure contract run preserved exit status 1: nine of ten methods passed; one errored because the copied inert scope fixture was missing. The fixture was restored byte-for-byte from the accepted owner-aware source (SHA256 `b0c0429056a3c87b49688dace1d22f9701c00fd773492766ae331898e4379d18`). Only that missing method was subsequently executed once and passed; validators and assertions stayed unchanged. Both the original failure and narrow continuation logs and statuses are retained under `offline-contracts/`.

This packet qualifies the original held-capacity and pressure gates at `8fa1637` only. Standing 16 / 32, mixed-route regression additions, later CPU changes and historical cold H2 / direct boundaries remain separately scoped. Preparation files describe the original prospective plan and are not additional measured gates.
