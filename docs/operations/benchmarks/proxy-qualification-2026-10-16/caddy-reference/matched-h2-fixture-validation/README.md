# Matched single-connection H2 fixture validation

This bounded source validation covers the opt-in connection-matched H2 mode in
`scripts/qualify-proxy-caddy.sh` and its load-driver helper. It is fixture
evidence only; no Caddy, HAProxy or p2pstream performance cohort was run.

The helper performs one validated HTTP/2 GET on the same client transport before
the ordinary warmup. It requires HTTP/2, status 200 and the exact 256-byte small
payload. For a candidate admin endpoint, it observes and flushes exactly one
persisted prewarm event before the ordinary warmup and timed recorder baseline.
The request trace retains the prewarm physical connection identity. Later
warmup and timed requests must reuse it and may not observe another connection;
the timed distinct-connection counter is independently required to equal one.
Default driver behavior is unchanged when the opt-in environment variable is
absent.

The runner's matching mode raises only HAProxy's frontend advertised concurrent
stream limit from its default 100 to 256, enabling 128 requested streams on one
connection. Every H2 row must report the validated prewarm, reuse the same
connection, observe no additional connection, and count exactly one timed
client TCP connection. Candidate rows additionally require the one prewarm
request event to be persisted before the timed baseline. H1 configuration,
workload order and performance gates are unchanged. This topology-controlled
mode does not represent HAProxy's maximum multi-connection or multi-thread
throughput.

Focused helper tests pass 20 normal repetitions and 10 race repetitions. The
installed HAProxy 3.2.9 parser accepts the exact new global setting in the saved
configuration shape. Shell syntax, embedded Python argument parsing, `gofmt`
and `git diff --check` also pass. The first normal and race commands reached the
linker but did not run tests because the host `/tmp` quota was full. The same
commands then passed with an owned workspace `GOTMPDIR`, which was removed
afterward. Both pre-test linker failures are retained rather than presented as
test failures.

`validation.txt` records commands, results and cleanup. Logs and timing files
preserve the passing checks and the initial infrastructure failures. The
HAProxy configuration is sanitized only by replacing ephemeral ports and the
generated certificate path. `SHA256SUMS` covers every committed file other
than itself.
