# Caddy moderate-load extension: plan and bounded validation

This extension was preregistered at 09:28:03 UTC before any real reference
calibration or moderate-load latency run. It adds 12 Caddy rows to the existing
24 candidate/HAProxy rows. Offered rates still come only from half the lower
candidate/HAProxy warmed throughput median in each H1/H2, c16/c128 cell; Caddy
never lowers or changes that rate. The original candidate/HAProxy pair orders
and all delivery and latency gates remain intact.

The three proxy orders are candidate/Caddy/HAProxy, HAProxy/Caddy/candidate,
and candidate/HAProxy/Caddy. Both reference comparisons separately require
candidate median p95 and p99 ratios no greater than 1.25. Every proxy must
complete exact payload/status/protocol checks and deliver at least 99% of the
offered rate within 101% of the scheduled duration. Candidate event counts
must be exact. Scheduled arrival time includes queue delay. H1 includes initial
client/TLS establishment; H2 uses one separately accounted prewarm connection.
This is moderate-load qualification, without a near-saturation claim.

A Caddy row with valid measurements and payloads but missed delivery limits is
preserved, with a separate failure ledger, and does not stop the remaining
unchanged schedules. Its cell's Caddy tail comparison is marked
`capacity_confounded` and cannot pass; HAProxy results remain separate. This
means the offered load was not delivered, without attributing the cause to
Caddy alone. Correctness, protocol or instrumentation failures still stop the
run. A hard timeout or missing raw row leaves an incomplete, inconclusive run.
Rates are never lowered, and failed cells are never silently omitted.

Bounded validation passed 28 synthetic calibration/schedule/summary cases,
shell syntax, embedded Python compilation and CLI help. These include the
original 19 contracts plus unchanged rates/orders, original 24-row results,
independent Caddy tail failure, capacity-confounded Caddy cells, incomplete or
duplicate cells, delivery versus payload error classification, and invalid
extension CLI usage. All fixtures and binary identities are explicitly fake;
no Go compilation, proxy, Docker container or network load ran.

The finite wrapper also passed Python syntax/help and four fake-process cleanup
cases: natural exit, timeout, SIGTERM and parent exit before its child. Docker
discovery was mocked empty. Both preregistered JSON plans are read from the
frozen calibration source archive and compared byte-for-byte with its exact
Git commit before copying and hashing. Candidate/driver, toolchain, harness,
runner, source and calibration hashes remain pinned; no rebuild or overlay is
introduced between calibration and latency measurement.

`validation-source.py.txt` extracts the actual runner helpers with Python AST,
then generates synthetic inputs. Its `repo` constant names the original
worktree and can be changed for replay. `cleanup-tests.py.txt` executes the
wrapper's exact cleanup block with shortened test-only wait bounds; update its
`WRAPPER` path to replay. The full tested runner/wrapper, test sources and
results are preserved. No real 36-row moderate-load result is claimed here.
