# Fresh moderate-load HTTPS latency at `248bad0`

All 36 rows delivered their fixed schedules with 9,374,040 exact successful
requests and zero errors. All four candidate/Caddy p95 and p99 gates passed;
all four candidate/HAProxy gates failed. No cell was capacity-confounded.
The workload and wrapper exited one solely for the predeclared latency
rejection. All processes completed naturally, without timeout, interruption,
forced group cleanup or forced container cleanup.

| Public protocol / concurrency | Offered requests/s | Candidate p95 / p99 ms | Caddy p95 / p99 ms | HAProxy p95 / p99 ms |
| --- | ---: | ---: | ---: | ---: |
| H1 / 16 | 14,643 | 0.440303 / 1.538012 | 1.015156 / 2.244211 | 0.238388 / 0.322924 |
| H1 / 128 | 13,914 | 0.499027 / 2.619124 | 0.851518 / 3.023083 | 0.255985 / 0.433092 |
| H2 / 16 | 10,964 | 0.518867 / 1.017191 | 1.071730 / 2.184457 | 0.273849 / 0.403659 |
| H2 / 128 | 12,557 | 0.642162 / 2.179775 | 1.541018 / 3.803764 | 0.284035 / 0.401535 |

| Cell | Candidate / Caddy p95 / p99 | Candidate / HAProxy p95 / p99 |
| --- | ---: | ---: |
| H1 / 16 | 0.43373 / 0.68532 | 1.84700 / 4.76277 |
| H1 / 128 | 0.58604 / 0.86638 | 1.94944 / 6.04750 |
| H2 / 16 | 0.48414 / 0.46565 | 1.89472 / 2.51993 |
| H2 / 128 | 0.41671 / 0.57306 | 2.26086 / 5.42861 |

Values are three-repeat medians at equal offered rates within each cell.
The preregistered rate is the integer floor of half the lower warmed median
candidate/HAProxy throughput from the [fresh matched reference](../final-runtime-reference/README.md).
Each row schedules `20 * offered_rps` requests. Latency includes delay from
the scheduled dispatch time; actual schedule, dispatch and completion times
are retained. Useful throughput must reach 99% of the offered rate and elapsed
time must stay within 101% of the planned duration. Correctness, payload,
request events and protocol checks are prerequisites. Both p95 and p99 ratios
must be at most 1.25. No offered rates or gates changed after seeing results.

The original 24 candidate/HAProxy cases keep their three alternating pair
orders. The preregistered extension adds 12 Caddy cases at exactly those same
rates. The saved plan files are original pre-run documents, so their pending
fields describe registration time; `manifest.json` and raw outcomes record
actual execution. The complete run took 726.00 seconds.

The candidate/driver binary, pinned Caddy and HAProxy binaries, HTTPS public
listeners, HTTP/1 origin, CPU sets, Go runtime, container constraints and H2
single-connection contract are identical to the calibration reference. All 18
H2 rows use one client connection; H1 includes initial connection/TLS setup.
Independent review in `root-review.json` verifies every result and hash,
delivery, payload/events, protocol topology and natural lifecycle.

The original H1 driver sends no explicit SNI for its IP URL. These rows do not
pin observed peer-certificate fingerprints; the calibration README records the
later discovered fallback-certificate limitation. This matters particularly to
claims about initial TLS establishment. The later explicit-SNI native control
is a separate attribution experiment, without a direct causal comparison to
these cells.

These results support Caddy-class moderate-load latency for the tested direct
tiny-GET workloads. They do not qualify latency near saturation or broad proxy
parity. The substantial HAProxy advantage, its failed gates and the original
agent WAN regression remain open.

`SHA256SUMS` covers files actually exported here. Raw result/configuration/log
bytes are unchanged. Executables, databases, ready/stop files and generated TLS
credentials are omitted. Saved paths and upstream manifests may refer to
omitted files. `export-map.json` records copied source paths and identities.
