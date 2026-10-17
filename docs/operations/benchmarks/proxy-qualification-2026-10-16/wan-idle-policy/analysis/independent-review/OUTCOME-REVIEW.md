# Independent idle-policy outcome review

**The completed 1/0/1 screen is a valid positive diagnostic under its five prospective rules. It is not a production fix or original WAN qualification pass.** The intended physical congestion-window change is visible with unchanged CUBIC and application credit policy. This materially strengthens the evidence for an interaction between long-lived TCP history and the application's burst/credit behavior. It does not identify which guarded kernel path, shared-namespace socket or historical threshold-creating event caused the regression.

This review reads saved evidence and independently calculates arithmetic. It executes no validators, contracts, builds, namespace operations, endpoints or traffic. `OUTCOME-INPUTS.json` binds 64 inputs; `OUTCOME-OBSERVATIONS.json` retains all 48 row summaries, twelve selected TCP sample summaries, all reuse outcomes, copies, namespace observations and mature failures. `OUTCOME-ARITHMETIC.json` records exact rational calculations from the recorded rates. No discrepancy was found.

## Complete outcome and exact decision

| Outcome | First policy-1 control | Policy-0 candidate | Last policy-1 control |
|---|---:|---:|---:|
| Four-download harmonic rate, MB/s | 32.709486 | 35.913604 | 24.379630 |
| Post-warmup upload harmonic rate, MB/s | 36.043333 | 35.850302 | 35.794352 |
| All-eight harmonic rate, MB/s | 34.295580 | 35.881925 | 29.004332 |
| Four-copy upstream Read, seconds | 29.067345 | 26.356720 | 39.695711 |
| Mature acceptance | false | true | false |
| Retained / replaced revisit origins | 4 / 0 | 4 / 0 | 2 / 2 |

All 48 rows have one attempted/completed transfer, one persisted event delta and the fixed 268,435,456-byte configured payload. Every recorded rate equals payload divided by its saved wall duration. All three arms remain measurement-valid. The independent exact harmonic calculation reproduces candidate/faster-control **1.097956827901965**, exceeding 1.08. Candidate/faster corresponding-copy ratios are 1.041756, 1.037306, 1.044982 and 1.076694 for 9/11/14/15; all exceed 0.95. All eight original candidate download floors pass, candidate mature performance issues are empty, and candidate aggregate Read is positive and strictly below both controls. All five criteria therefore pass without changing thresholds or removing outcomes.

The all-eight gain is 4.6255% against the faster control; the upload ratio is 0.994644. Thus the primary 9.7957% result must not be presented as an equal improvement to every transfer direction. The control download rates differ by **34.1673%**. This is one fixed-order screen, not a randomized causal estimate, confidence interval or proof of repeatable production uplift. The endpoint aggregates active transfer durations and excludes between-request gaps.

The first control fails its steady download floor on sequence 14 (28.828442 MB/s). The last control fails on 9 and 11 (27.654870 and 14.303925 MB/s); it also preserves both mature reuse failures on 13/14. Those revisits occur after 35.998/35.817 seconds and show fresh origin connections, false reused/idle flags and zero idle time on the same owned physical lane. Last-control sequence 14 is a new logical stream, so its healthy 34.098700 MB/s cannot be called a retained-stream contrast. These were prospectively included treatment-dependent outcomes. Candidate revisits retain all four origins at approximately 24 seconds.

## Physical evidence

Every selected pre/post sample reports send MSS **1,448 bytes**. Cwnd and ssthresh below are segment counts, not bytes; infinity abbreviates provider value 2147483647.

| Arm / sequence | MB/s | Cwnd pre → post | Ssthresh pre → post | Retransmission delta |
|---|---:|---:|---:|---:|
| 1a / 9 | 33.821 | 10 → 5884 | infinity → infinity | 0 |
| 1a / 11 | 34.064 | 10 → 5902 | infinity → infinity | 0 |
| 1a / 14 | 28.828 | 10 → 3100 | 238 → 238 | 0 |
| 1a / 15 | 34.871 | 10 → 5885 | infinity → infinity | 0 |
| 0 / 9 | 35.234 | 5875 → 5875 | 5875 → 5875 | 0 |
| 0 / 11 | 35.335 | 5891 → 5891 | 5891 → 5891 | 0 |
| 0 / 14 | 35.633 | 5893 → 5893 | 5893 → 5893 | 0 |
| 0 / 15 | 37.550 | 5891 → 5891 | 5891 → 5891 | 0 |
| 1b / 9 | 27.655 | 10 → 3074 | 241 → 241 | 0 |
| 1b / 11 | 14.304 | 10 → 2561 | 248 → 248 | 3 |
| 1b / 14 | 34.099 | 10 → 5895 | infinity → infinity | 0 |
| 1b / 15 | 34.875 | 10 → 3003 | 1920 → 1920 | 0 |

All selected RTO deltas are zero. Multiplying pre-cwnd by current MSS gives a capacity proxy of **14,480 bytes** in the controls versus **8.507–8.533 MB** in the candidate; this is not observed flight occupancy. Healthy controls with infinite or larger finite thresholds demonstrate that cwnd 10 or a finite threshold alone is not sufficient to predict a slow transfer. Zero-retransmission slow controls show loss is not required.

At late selected raw-write samples, slow 1a/14 and 1b/9 already have about 3.00 MB and 2.74 MB queued but not sent, with congestion-window-sized flight. Their remaining delay cannot simply be assigned to absence of application bytes. The unchanged application policy reaches a successful 16 MiB growth proposal on candidate 9/11/14 after 1.080/1.068/.995 seconds, versus 1.391/1.407/3.544 in the first control and 3.949/16.024/1.404 in the last. These are same-server proposal sample-start offsets, not publication completion. Candidate credit waiting remains about .623/.623/.478 seconds, and sequence 15 has no new growth or credit wait in all arms. Faster physical progress can therefore accelerate the existing demand estimator; the observed improvement required no initial-credit change. Credit and upstream-copy waits overlap and must not be summed.

## Scope and integrity

Fresh target/setter/independent readbacks and actual agent/origin endpoint observations match 1/0/1. Host, runner, router and actual server observations remain 1. Existing agent setup/terminal reads and cached owner values match independent intent. The target namespaces are distinct from protected namespaces, and actual endpoints join the owned helpers/readiness/lifecycle records. These are point-in-time observations. The intervention includes lane 0, origin-facing sockets and both ends of origin TCP; it is not confined to tunnel DATA sockets.

The frozen kernel guards show that this setting controls both idle restart and application-limited cwnd reduction. Existing threshold history still exhausts before the creator, so the result does not identify HyStart or a unique threshold transition. The middle arm also changes conditioning history, not just post-warmup sends.

Root's saved final audit reports accepted cleanup, all three arms/48 rows, no input changes across 40 derivative and 540 original bindings, no owned process/namespace remnants and closed scopes. Arm outer statuses 1/0/1 preserve mature outcomes; validators and final comparison exit zero. The saved screen uses 76,302,095 bytes against 268,435,456, with comparison complete, stopped false, diagnostic positive true and original qualification false. The audit's inaccessible unrelated `/proc` entries do not constitute a global host-process absence claim.

## Consequence for a default-OS application fix

No production change is established. The smallest remaining application-only experiment is a bounded larger initial logical receive share drawn from an already reserved grown pool, under ordinary policy 1 and CUBIC. This is a new falsification experiment: it tests whether reducing early credit limitation changes recovery, not whether it restores the retained pre-cwnd observed here. The existing `Grow` path can avoid a new session reservation below the quarter-pool bound, but irrevocable credit, fairness, hostile readers, admission and publication failure still require explicit protection. A separate finite source plan defines that work; this outcome grants no implementation or runtime.

The reviewed Linux TCP option interface exposes no direct per-socket equivalent of this namespace switch. TCP congestion-controller selection is a broader intervention; the earlier Reno screen was negative, and another controller cannot be inferred to work here. Frequent tiny application pings do not disable application-limited reduction, while privileged TCP repair or inflated RTO settings change unrelated transport guarantees. None is a supported production shortcut. Warm-lane selection or safe idle physical-session renewal may address physical history more directly, but they change lane/ownership/reuse semantics and require a separate design, not silent relaxation of the current five-owner experiment.
