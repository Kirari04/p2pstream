# Completed controller-policy outcome: independent review

**The fresh diagnostic completed all three arms and is negative under its preregistered decision. This is not a structural stop.** All 48 exact 256 MiB transfers remain in the analysis. Independently recomputing the five criteria from canonical rates and copy durations reproduces the saved verdict: only the 108% pooled-rate criterion fails. No production fix, production Reno recommendation or original WAN qualification clearance follows.

## Complete result

| Metric | CUBIC 0 | Reno | CUBIC 2 |
|---|---:|---:|---:|
| Four-download aggregate, decimal MB/s | 32.283160 | 34.205885 | 23.688142 |
| Four-upload aggregate, decimal MB/s | 36.142804 | 35.472328 | 35.724686 |
| All-eight active-transfer aggregate, decimal MB/s | 34.104128 | 34.827597 | 28.487162 |
| Four-download upstream Read, seconds | 29.337419174 | 27.727152809 | 41.551392224 |
| Mature accepted | false | true | false |
| Fully evidenced origin replacements | 0 | 0 | 0 |

Reno/faster-CUBIC is **1.0595581514735009**, below 1.08. Reno/slower-CUBIC is 1.444008766600861, but the preregistration correctly uses the faster whole control. The two CUBIC pools differ by a factor of **1.3628405053490593**. Reno passes every original download floor, mature performance, the four corresponding-download 95% guards, and positive/lower aggregate Read versus both controls. Its smallest corresponding-download ratio is sequence 15 at 0.9915945138439012. All five criteria were evaluated after all three arms.

The all-eight active-transfer improvement over CUBIC 0 is only 2.12%; post-warmup upload rate is 1.86% below the faster upload control. These are reported secondary outcomes, not new gates. Warmup uploads also have severe slow rows in both CUBIC 0 and Reno, so this run cannot be described as Reno eliminating all slow behavior. The complete rate vector follows; displayed rounding does not enter exact-Fraction decisions.

| Sequence | Stage/direction | CUBIC 0 MB/s | Reno MB/s | CUBIC 2 MB/s |
|---|---|---:|---:|---:|
| 1 | warmup/download | 34.574786 | 34.583498 | 34.853457 |
| 2 | warmup/download | 34.035229 | 34.371075 | 34.077596 |
| 3 | warmup/download | 34.331554 | 33.579013 | 34.357666 |
| 4 | warmup/download | 34.372065 | 34.657915 | 34.287456 |
| 5 | warmup/upload | 29.483103 | 29.543417 | 35.388494 |
| 6 | warmup/upload | 29.527167 | 15.674513 | 29.499035 |
| 7 | warmup/upload | 28.608837 | 35.647882 | 29.025178 |
| 8 | warmup/upload | 14.648129 | 28.793423 | 35.402195 |
| 9 | retained-warmup/download | 33.793465 | 34.197969 | 14.579564 |
| 10 | retained-warmup/upload | 35.698972 | 35.186151 | 35.393864 |
| 11 | steady/download | 33.851622 | 33.852469 | 27.264049 |
| 12 | steady/upload | 35.677559 | 33.778540 | 35.124004 |
| 13 | steady/upload | 36.392725 | 36.329188 | 36.066628 |
| 14 | steady/download | 27.685824 | 34.173099 | 28.686160 |
| 15 | steady/download | 34.901774 | 34.608407 | 34.802769 |
| 16 | steady/upload | 36.828009 | 36.747127 | 36.341319 |

The saved and independently regenerated attestation objects are identical in all arms. Each has empty integrity and conditioning issue lists, natural group join and no forced cleanup. CUBIC 0 fails the steady download floor (sequence 14); CUBIC 2 fails retained and steady download floors (9,11,14). Reno's mature acceptance is true. All twelve revisits retained their origins; the new treatment-policy design allowed churn prospectively but none occurred here. Therefore this cohort supplies no measured reconnection-effect contrast.

`screen-final.json` records `comparison_incomplete=false`, `stopped=false`, three measurement-valid arms, `diagnostic_positive=false`, and `original_qualification_cleared=false`. Its artifact count is 214,627,949 bytes against the unchanged 268,435,456-byte rejection threshold. Exit 1 means a complete failed diagnostic criterion, not invalid measurement or abandoned remaining arms. The individual canonical rows confirm one useful completion, exact configured payload, HTTP 200 and one persisted event each.

## What the saved clocks and TCP states add

I independently extracted existing selected server growth samples relative only to their own server handler start, and agent pre/post TCP snapshots on the validated selected lane. No cross-endpoint clock subtraction or new capture was used. Growth crossing times describe the sample start of a subsequently successful `grown` proposal, not publication completion.

| Arm/sequence | MB/s | Pre ssthresh | Retransmission delta | 4 MiB sample s | 8 MiB sample s | 16 MiB sample s |
|---|---:|---:|---:|---:|---:|---:|
| CUBIC 0 / 11 | 33.851622 | 2147483647 | 0 | .970 | 1.142 | 1.405 |
| Reno / 11 | 33.852469 | 2147483647 | 0 | .969 | 1.132 | 1.560 |
| CUBIC 2 / 11 | 27.264049 | 231 | 0 | 2.015 | 2.821 | 4.112 |
| CUBIC 0 / 14 | 27.685824 | 243 | 0 | 1.853 | 2.740 | 3.948 |
| Reno / 14 | 34.173099 | 2147483647 | 0 | .890 | 1.062 | 1.325 |
| CUBIC 2 / 14 | 28.686160 | 238 | 0 | 1.206 | 2.412 | 3.623 |
| CUBIC 2 / 9 | 14.579564 | 244 | 4 | 2.091 | 11.824 | 16.422 |

All twelve selected transfers have pre-cwnd 10. All selected threshold-history caps were already exhausted. None has a pre/post RTO increment. The slow zero-retransmission CUBIC cases retain finite ssthresh through their selected transfers. CUBIC 2/9 changes 244 to 292 and has four retransmissions; timing does not establish retransmission causation. CUBIC 2/15 is healthy at 34.802769 MB/s despite finite pre-ssthresh 2328: it also retains about 23 MiB application credit, so finite threshold alone is not sufficient to predict slowness. Maximum downstream Write in the slow 9/11/14 examples stays below one millisecond; most elapsed time remains upstream Read.

The same application growth policy can be healthy (CUBIC 0/11) or slow (CUBIC 2/11), with different pre-existing physical state. Reno's unchanged application policy can reach healthy growth quickly. Thus delayed application growth is at least compatible with downstream feedback from TCP progress; it is not isolated as the initiating defect. The severe CUBIC 2/9 already reaches about 4 MiB at 2.091 seconds but does not reach 8 MiB until 11.824 seconds. A larger initial logical window cannot be presumed to remove its retained low-threshold/cwnd behavior.

## Interpretation and next choice

The negative screen rejects its specific 8%-plus-all-guards success rule; it does not prove absence of any controller effect. The observed roughly 6% faster-control contrast and successful Reno floor outcome are leads. Fixed order, one Reno arm and 36% divergence between CUBIC controls preclude a strong causal or reproducibility claim. Do not change the threshold, prefer the slow control, select sequence 14 alone, or append retries to this experiment.

**Recommend a source plan for one fresh fixed-CUBIC 1/0/1 private-agent-network-namespace `tcp_slow_start_after_idle` screen before eager stream-credit implementation.** This directly intervenes on the policy family implicated by cwnd restart/retention while keeping application credit policy and accepted binaries unchanged. It is a diagnostic intervention, not a deployable sysctl recommendation or proof that aggregate differs from legacy for this reason. The separate `IDLE-POLICY-PLAN.md` defines the scope and mandatory preflight/proof conditions.

The exact retained v7.0 kernel source, with the documented Ubuntu 7.0.0-38.38 alignment and its erratum, supplies the mechanism: `tcp.h:1659–1670` reads `sock_net(sk)->ipv4.sysctl_tcp_slow_start_after_idle` before `tcp_cwnd_restart`; `tcp_output.c:166–181` can reduce cwnd toward the initial restart window while setting ssthresh through `tcp_current_ssthresh`; `tcp_output.c:2165–2168` uses the **same switch** to enable application-limited cwnd reduction. Therefore setting zero disables both idle restart and application-limited validation. It does not selectively isolate only inter-request idle restart, reset ssthresh, identify its creator, or disable loss recovery. Source proof must be re-bound to the kernel actually used for any later run. The prior report's assertion that no policy experiment is justified before observing the creator was a historical recommendation, not a source theorem: a prospective intervention can test the downstream mechanism without identifying every upstream cause.

The switch is namespace-scoped by `sock_net(sk)`, not socket-scoped. All relevant sockets in the selected namespace—including data/control lanes and agent-origin sockets—are exposed; any origin endpoint sharing that namespace is exposed too. The new plan must record that scope and show that separate server/runner namespaces retain their values. Installing zero before the arm's first relevant socket also changes conditioning history; a positive result supports whole-workload idle/application-limited policy dependence, not a within-socket counterfactual that holds its entire warmup state fixed.

## Why defer the backed-share candidate

The source opportunity is real and the bounded prior-candidate search found no exact earlier experiment. `AggregateReceiveWindowGrowth.Grow` can use existing backing when rounded target times four fits the current session pool, then publish allocation before byte credit. That is not proof of a performance defect. In particular, an already-grown pool does not itself clear finite TCP history; the saved severe example remains slow after several MiB are available.

A future backed-share implementation would additionally change demand policy and fairness: several unproven or malicious readers can occupy much more of already reserved global storage. Global memory boundedness does not guarantee fair progress. Admission boundaries, both directional receive lifetimes, exact quarter-share/operator caps, no new reservation, error propagation and ambiguous-publication fencing require deterministic proof. A constructor that silently publishes without an error-return path is not adequate. Public headers, request paths and untrusted demand must not select an unchecked target. These are real implementation costs avoided by testing the more directly implicated transport policy family first.

No further production change is supported by this review. Original aggregate/legacy WAN gates remain unresolved; any retained application fix will still require the prescribed original qualification and protected dev-first PR workflow.
