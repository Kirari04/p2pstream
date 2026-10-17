# Saved policy-screen growth observations — 2026-10-17

Read-only extraction from the three completed arms under `tmp/wan-controller-screen-20261017/runner-source/integration/runs/controller-crc-policy-once`. Root/Astra owns the complete negative diagnostic decision; this note supplies source-boundary evidence and does not revise the screen's gates. No test, build, validator or runtime was invoked. No earlier evidence file was changed.

`POLICY-GROWTH-EXTRACTION.json` retains all twelve selected rows, exact contributing paths, server credit/growth state, copy durations and sender pre/post TCP fields. Growth times below are completed-successful proposals' **sample-start offsets from the same server's handler start**; they are not publication-completion times or cross-endpoint delays. `∞` denotes the provider's initial threshold `2147483647`. Credit wait includes byte and allocation decision-path waits, which cannot be added to overlapping copy time.

| Arm/sequence | MB/s | 4 MiB s | 8 MiB s | 16 MiB s | Pre→post threshold | Retrans delta | Credit wait s |
|---|---:|---:|---:|---:|---|---:|---:|
| C0/9 | 33.793 | .975 | 1.144 | 1.401 | ∞→∞ | 3 | .870 |
| C0/11 | 33.852 | .970 | 1.142 | 1.405 | ∞→∞ | 0 | .865 |
| C0/14 | 27.686 | 1.853 | 2.740 | 3.948 | 243→243 | 0 | 1.823 |
| C0/15 | 34.902 | — | — | — | ∞→∞ | 0 | 0 |
| R/9 | 34.198 | .971 | 1.140 | 1.402 | ∞→∞ | 0 | .869 |
| R/11 | 33.852 | .969 | 1.132 | 1.560 | ∞→∞ | 0 | .868 |
| R/14 | 34.173 | .890 | 1.062 | 1.325 | ∞→∞ | 0 | .869 |
| R/15 | 34.608 | — | — | — | ∞→∞ | 0 | 0 |
| C2/9 | 14.580 | 2.091 | 11.824 | 16.422 | 244→292 | 4 | 3.672 |
| C2/11 | 27.264 | 2.015 | 2.821 | 4.112 | 231→231 | 0 | 1.903 |
| C2/14 | 28.686 | 1.206 | 2.412 | 3.623 | 238→238 | 0 | 1.268 |
| C2/15 | 34.803 | — | — | — | 2328→2328 | 0 | 0 |

Reno/14 reaches the growth milestones earlier than both corresponding CUBIC rows, with an initially unlimited slow-start threshold instead of 243/238. Its upstream Read total is 6.993 s versus 8.632/8.448 s; byte-zero wait is .790 s versus 1.664/1.188 s. No session-allocation-zero wait is recorded in any selected row. The excess in those two controls is principally byte-zero, rather than extra allocation-only waiting. All selected pre-cwnd values are 10. C0 and Reno/11 have essentially identical healthy throughput and similar ramp timings, whereas C2/11 begins with threshold 231 and ramps later. These facts make credit eligibility and transport progress co-vary while application policy stays fixed; they do not show an independent application-credit defect.

The severe C2/9 has 17.259 s inside upstream Read and .806 s in downstream Write. It takes 11.824/16.422 s to reach 8/16 MiB, but its 4 MiB crossing is already at 2.091 s. Four retransmissions and threshold movement occur somewhere inside the interval; their exact order relative to the long plateau is not isolated here. The pre-existing 244 threshold also precedes this selected payload interval. Retransmissions alone are insufficient to explain the other slow CUBIC rows, which have zero delta, and healthy C0/9 has three retransmissions.

Same-direction sequence 15 remains healthy in all arms without new growth or credit waits. C2/15 is particularly limiting evidence: its threshold is finite (2328), post-cwnd 3073, and it nevertheless delivers 34.803 MB/s with retained 23 MiB byte/allocation limits. Thus a finite threshold or post-cwnd alone does not determine total throughput. Application window warmth and physical transport history interact, and both differ between a fresh/direction-cold stream and its later reuse.

Earlier demand eligibility under Reno/14 is consistent with faster DATA arrival allowing the same measured-demand estimator to grow sooner. Conversely, larger retained credit can help avoid credit-induced gaps and change physical writer cadence. These captures do not independently vary those two directions of feedback or identify the creator of the low CUBIC thresholds. They therefore neither justify eager initial credit nor establish Reno as a production fix. The earlier bounded-pool-share candidate remains an optimization hypothesis, and the complete screen's negative acceptance decision remains authoritative.
