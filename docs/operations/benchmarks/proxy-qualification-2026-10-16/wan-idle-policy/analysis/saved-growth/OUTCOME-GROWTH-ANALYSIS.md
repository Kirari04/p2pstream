# Idle-policy outcome: saved growth, TCP and copy evidence

The completed private-namespace 1/0/1 screen passes all five prospective criteria. Its four-download harmonic rates are **32.709486 / 35.913604 / 24.379630 MB/s**; the value-0 arm is 9.7957% above the faster control. The two controls differ by 34.1673%, so this is one positive fixed-order diagnostic with substantial control variation. It does not clear the original aggregate/legacy WAN qualification or justify production tuning. Root/Astra owns the final causal assessment.

`OUTCOME-GROWTH-EXTRACTION.json` preserves all twelve selected rows, explicit input paths, actual provider MSS/cwnd/ssthresh/retransmission fields, same-server growth crossings, copy totals, byte/allocation wait partitions, policy readbacks and all origin outcomes. No validator, test, build, probe or traffic was run for this extraction. Existing runtime/source inputs are unchanged.

## Intended manipulation is visible

Every selected pre/post provider sample has **send MSS 1,448 bytes**. Both value-1 controls start all four selected transfers at cwnd 10 segments. Value 0 starts at 5,875–5,893 segments and retains those values through each selected transfer. Multiplying cwnd by the current send MSS gives 14,480 bytes for the controls versus approximately 8.507–8.533 MB for value 0. That product is a window-capacity proxy, not measured bytes in flight. It prevents mistaking segment counts for byte limits.

All controllers remain CUBIC. Fresh target readbacks and actual agent/origin endpoint pre/post observations are 1/0/1 by arm; independent namespace-file readbacks and existing agent setup/terminal reads agree. All five cached owner values agree with their intended arm. Host, runner and actual server endpoint values remain 1. These are point-in-time observations, not continuous sysctl monitoring. The intervention covers the whole shared agent/origin namespace, including control and origin-facing TCP, and disables both guarded cwnd-validation paths.

| Arm/sequence | MB/s | cwnd pre→post, segments | ssthresh pre→post, segments | Retrans delta | 16 MiB proposal s | Upstream Read s | Credit wait s |
|---|---:|---|---|---:|---:|---:|---:|
| 1a/9 | 33.821 | 10→5884 | ∞→∞ | 0 | 1.391 | 7.002 | .871 |
| 1a/11 | 34.064 | 10→5902 | ∞→∞ | 0 | 1.407 | 6.846 | .870 |
| 1a/14 | 28.828 | 10→3100 | 238→238 | 0 | 3.544 | 8.368 | 1.382 |
| 1a/15 | 34.871 | 10→5885 | ∞→∞ | 0 | — | 6.852 | 0 |
| 0/9 | 35.234 | 5875→5875 | 5875→5875 | 0 | 1.080 | 6.694 | .623 |
| 0/11 | 35.335 | 5891→5891 | 5891→5891 | 0 | 1.068 | 6.656 | .623 |
| 0/14 | 35.633 | 5893→5893 | 5893→5893 | 0 | .995 | 6.686 | .478 |
| 0/15 | 37.550 | 5891→5891 | 5891→5891 | 0 | — | 6.320 | 0 |
| 1b/9 | 27.655 | 10→3074 | 241→241 | 0 | 3.949 | 8.633 | 1.826 |
| 1b/11 | 14.304 | 10→2561 | 248→248 | 3 | 16.024 | 17.483 | 7.950 |
| 1b/14 | 34.099 | 10→5895 | ∞→∞ | 0 | 1.404 | 6.797 | .866 |
| 1b/15 | 34.875 | 10→3003 | 1920→1920 | 0 | — | 6.783 | 0 |

Here ∞ is the provider value 2147483647. All selected RTO-count deltas are zero. Growth times are successful proposals' **sample-start offsets from the same server's handler start**, not publication completion or cross-endpoint delay. Sender credit waits overlap upstream-copy waiting and must not be added to it. A dash means no new crossing during that interval because the stream already retained larger credit.

## Faster eligibility coexists with the unchanged logical ramp

Value 0 reaches 1 MiB at .325/.323/.241 seconds on 9/11/14, compared with .645/.730/.642 for control 1a. Its 4/8 MiB crossings are .729/.810, .730/.811 and .484/.739 seconds. The application credit policy and starting logical limits are unchanged. Keeping physical cwnd large therefore coincides with earlier useful consumption and earlier eligibility under the existing demand estimator; this improvement occurred without a new initial-credit policy.

Credit waiting does not disappear: value 0 retains approximately .623/.623/.478 seconds on the three direction-cold selections. Those totals comprise .544/.544/.398 seconds in the byte-zero decision path and approximately .079/.080/.079 seconds in allocation-only waits. No selected session-allocation-zero wait occurs. Same-direction sequence 15 has no new growth and zero credit wait in every arm. Final server byte/allocation limits remain similar, about 21.6–23.1 MiB, rather than a new enlarged application allowance.

Aggregate upstream Read totals fall to 26.356720 seconds from 29.067345 and 39.695711 across the four selected copies. Value-0 downstream Write totals remain .562–.607 seconds per copy; its handler totals are 7.442/7.431/7.355/6.969 seconds. The severe control-1b/11 spends 17.483 seconds in Read and .934 seconds in Write within an 18.590-second handler. This retains the established upstream boundary while showing a measured physical-window manipulation. It does not identify which guarded kernel path or shared-namespace socket produced each saved gap.

## Origin outcomes and historical context

Control 1a and value 0 each retain all four intended origins. Value-0 revisits occur after 24.159–24.744 seconds. Control 1b loses origin reuse on sequences 13 and 14 after 35.998 and 35.817 seconds, preserving both mature conditioning failures. Its sequence 14 is a **new logical stream 8**, although the same owned physical tunnel session remains. Its healthy 34.099 MB/s result must not be described as a same-retained-stream comparison. Sequences 15/16 retain their origins. The screen prospectively includes these complete origin outcomes and preserves every mature failure.

The earlier C/R/C comparison is context, never a control in this screen: it did not pass all five criteria, and its faster Reno samples still began at cwnd 10. This screen's observed value-0 retention of approximately 5,900 segments is a distinct manipulation with unchanged CUBIC. Both histories continue to show healthy same-direction copies despite finite thresholds; threshold size alone does not determine transfer performance. These new observations support the broader idle/application-limited policy interaction while leaving the finite-threshold creator and exact feedback path unresolved. No production sysctl, controller or application-credit change follows from this extraction alone.
