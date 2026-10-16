# Standalone sustained H2 complement — source/invocation plan only

Status: **not executed and not authorized by this checkpoint**.

This plan calls the existing frozen single-arm supervisor directly. It does not modify or regenerate the supervisor, timed runner, endpoint binary, certificate, schedule, copy observer, trace observer, resource scope, or timeout. Omitting `--smoke` selects the inherited fixed sustained schedule: one run, 256 MiB warmup and steady bodies, three transfer repetitions, 8 MiB required pool, 80 ms RTT, 30 s idle policy, and trace sequences 9/11/14/15.

The arm uses protocol `h2`, fresh endpoint/topology state, the exact H1 sustained certificate/key and DER fingerprint, server/agent source `8ac4c8df8237e37777ba2fac0da6f949da17bdea`, server `57d77d…`, agent `9e635d…`, frozen arm `60573b…`, and original analyzer `ebb8e1…`. The output and all adjacent launch sidecars must be absent before launch.

The original arm result remains authoritative and fail-closed. If the original analyzer rejects exact generation-status snapshots, the single arm remains rejected/nonzero even if endpoint lifecycle, resource, parser, and canonicalizer gates are sound. A separately reviewed corrected reducer may later consume the same closed canonical inputs into a new output directory; it cannot rewrite the arm, complete the withheld original pair, repair the H1 floor failure, or make this a qualification result. Unknown stack attribution remains unresolved.

Before any corrected offline reduction, independently require: all 16 expected H2 rows and event joins; protocol/TLS/certificate/payload/copy/pool/reuse checks; exact natural endpoint and outer joins with no forced cleanup; unchanged 4 GiB/no-swap leaf resource evidence and zero pressure/OOM events; parser/canonicalizer direct statuses 0 and naturally joined child groups for sequences 9/11/14/15; immutable pre/post bindings. Preserve every original analyzer/status/reduction/index artifact byte-for-byte.
