# Sustained diagnostic result and threshold chronology

H1 passes the fixed absolute29.7MB/s download floor. H2 fails that floor at steady sequences11 and14, while all32 transfers complete correctly with healthy reuse conditioning, exact payload/protocol checks and natural-zero endpoint/helper exits. H2 wrapper/runner/validation status1 and its performance rejection are preserved. Both arms pass the unchanged reviewed106 raw validator and prospective closure classifier without any exception. This is not aggregate-versus-legacy qualification: the original relative90% gate requires three alternating32-transfer pairs,96total, on final production source and remains unresolved.

| Selected download | H1 MB/s | H2 MB/s | H1 pre→post snd_ssthresh | H2 pre→post snd_ssthresh |
|---|---:|---:|---|---|
|9, lane1|33.897135|33.883497|2147483647→2147483647|2147483647→2147483647|
|11, lane3|33.837608|27.480695|2147483647→2147483647|243→243|
|14, lane2|30.707864|27.495906|456→1810|235→235|
|15, lane3|34.201609|34.927638|2147483647→2147483647|2325→2325|

All selected pre samples have cwnd10 and the recorded tcp_slow_start_after_idle setting1. H2 selected intervals have zero cumulative retransmission deltas. H1 sequence14 has a delta of9 and still passes its floor. H2 sequences11 and15 use the same owner lane3/session/stream6 and healthy reused connection, yet one fails and the later one passes with a different retained threshold. These are associations and counterexamples to a uniform H2 ceiling or retransmission-only account, not proof that a threshold causes the slowdown. CUBIC is the recorded algorithm; the captured host kernel is7.0.0-38-generic.

Every data owner starts at captured snd_ssthresh2147483647. Every data owner's bounded first-change monitor exhausts4096 raw-write attempts before recording a change. All report failure_reason=attempt_limit, transition_observed=false, first_changed_threshold=0 and explanation_status=inconclusive; provider capture errors are0. The first-change field0 is not an observed threshold. Last equal captures occur inside warmup download1/2/3/4 for lanes1/2/3/4 respectively, after about41.9–47.2MB sent. The limit is therefore consumed early within each first256MiB response, not by a failure to perform TCP_INFO.

| Arm/lane | Last equal capture, seconds after owner bind | First available finite retained boundary |
|---|---:|---|
|H1/1|2.039|sequence14 pre:461|
|H1/2|10.059|sequence9 pre:456|
|H1/3|17.967|none through sequence15 post|
|H1/4|26.151|sequence9 pre:474|
|H2/1|2.135|sequence14 pre:244|
|H2/2|10.093|sequence11 pre:235|
|H2/3|18.074|sequence9 pre:243|
|H2/4|26.219|sequence9 pre:244|

These finite boundary observations narrow some intervals: H1 lane1 and H2 lane1 remain at2147483647 through sequence11 post but are finite by sequence14 pre; H2 lane2 remains initial through sequence9 post but is235 by sequence11 pre; H2 lane3 is243 through sequence11 post and2325 by sequence14 pre. They do not identify the first kernel transition or the event between those retained boundaries. The4,096-attempt history cannot answer the intended first-change question on this sustained workload. Preserve that inconclusiveness; no automatic cap increase, new observer or retry follows.

THRESHOLD-CHRONOLOGY.json binds all source artifacts by path/hash, records each exact owner tuple, capture times, ssthresh/cwnd/retrans values, last-equal warmup placement, and all eight retained boundaries per lane. Actual pre/post TCP captures both come from sequence-N-post-sender.json after interval arming; the earlier pre-arm response contains uncaptured zero fields. Every used capture explicitly succeeded, has the expected sequence/owner, and each pre capture equals its persistent history copy. Missing/zero unobserved fields are never treated as samples.

The smallest threshold-focused next investigation is the existing-record/source comparison of controller/idle-restart state, with no production fix presently established. The independently proposed one-variable CUBIC→Reno→CUBIC H2 experiment is consistent with this evidence as a test of broad controller dependence. Both CUBIC brackets would have to reproduce the predeclared failures and the middle Reno arm clear them, with every original correctness/conditioning/resource requirement preserved, or the result stays rejecting/inconclusive. Even that pattern would not prove HyStart specifically, justify a production controller switch, or clear the original aggregate-versus-legacy gate. Root must review source feasibility and exact preconditions before any implementation or traffic. No such experiment was run here.

Runtime was released after the audit:933 source/hash checks unchanged, no owned endpoint/group/namespace remnants, all10 endpoint/helper exits natural0, complete five-owner intent per arm, zero raw errors/shorts and zero attributed exceptions. Total task artifacts were143,281,052bytes before this compact analysis, below the prospective256MiB storage allowance. Historical failed runs, blocked checkpoints, all prior manifests and the H2 performance failure remain preserved.
