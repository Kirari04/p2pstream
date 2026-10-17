# Independent saved latest-source capacity audit — passed

Candidate: `8fa16378af4ac0117276726fe6e51d8bc4c74c85`. All430 current compile-source hashes and exact ordinary server/agent binaries match the accepted build. No workload was rerun by this audit.

| Topology | Held useful/events/origin/public connection/public request peaks | Recovery useful/events | Sampled server peak | Sampled agent peaks |
|---|---|---|---:|---|
| combined-4g-server-plus-colocated-agent | 1536 /1536 /1536 /1536 /1536 | 512 /512 | 1394958336 B | colocated |
| external-four-agents-separate-4g-cgroups | 1536 /1536 /1536 /1536 /1536 | 512 /512 | 1320128512 B | 24743936 B, 23904256 B, 25157632 B, 24072192 B |
| external-one-agent-separate-4g-cgroups | 1536 /1536 /1536 /1536 /1536 | 512 /512 | 1314807808 B | 68665344 B |

All three topologies conserve1536 held +512 recovery events with zero response errors, dropped events, queued events, drain errors and readable OOM/OOM-kill deltas. Original3s barrier/200ms post-release hold,1GiB ballast and4GiB envelopes are retained. Separate process peaks are sampled and are not summed into a simultaneous deployment peak.

Both current-agent/old-server one/four-lane compatibility bursts complete128 held requests/events and512 recovery requests/events with zero errors. Their legacy session envelopes are181,586,560/726,346,240B, exactly1:4.

Exactly22 process receipts (3+4+7+4+4) rehash correctly and explicitly prove natural_exit_verified=true, forced_cleanup=false and integer exit0. Every constrained container was stopped with ExitCode0 and OOMKilled=false. Current read-only audit finds no recorded shell PIDs or qualification containers remaining. Outer supervisor status0, forced=false, accepted_cleanup=true, no adopted descendants or surviving owned identities. Elapsed runtime21.8997s; observed new-proof bytes3,571,811 inside512MiB bound.

The original source-closure preflight failure remains preserved. Its separate continuation is the successful endpoint cohort. This audit establishes exact latest-source absolute candidate capacity and mixed-version compatibility only; it does not imply performance parity, standing/pressure acceptance, mixed-route fairness or a universal capacity maximum. Detailed rows, receipts and hashes are in saved-capacity-audit.json.
