# Matched verified WAN sender-wait integration evidence

This package preserves the full bounded source, contract, build, publication,
small-smoke, offline-correction and sustained-run history for the test-only
sender wait observer. The sustained result **fails qualification**:

| Arm | Warmup minimum | Retained-warmup minimum | Steady minimum | Result |
| --- | ---: | ---: | ---: | --- |
| H1 | 33.820845 MB/s | 29.835158 MB/s | **15.558586 MB/s** | failed steady floor; sequences 13/14 expired before the intended retained revisit |
| H2 | 33.799582 MB/s | **27.496095 MB/s** | **27.370167 MB/s** | failed retained-warmup and steady floors; expected reuse remained valid |

The independent floor is 29.7 MB/s. Both arms retain `accepted:false`, runner
status 1, and the native outer tool-envelope exit 1. There was no retry.

All 32 transfer rows completed with status 200, exact payload, verified public
TLS and requested protocol. All selected sender intervals pass their schema,
identity, conservation, response-copy and terminal-closure checks. Each arm's
five owned endpoint/namespace processes exits naturally with status 0 and
`forced=false`; the outer groups and systemd unit also join naturally.

H1 remains `inconclusive-upstream-reuse`: its intended post-close retained
visits at sequences 13 and 14 occur after the 30-second idle policy expires,
so they open fresh origins and cannot serve as retained controls. H2 preserves
the same HTTP connection, origin, lane and Yamux stream for each exact retained
revisit. A reused physical session alone could instead carry a new stream, and
numeric stream IDs are scoped to their lane. Cold versus retained
interpretation therefore uses the bound session, lane, stream, tuple and origin
evidence together.

## What the observer measures

The selected intervals cover sustained download sequences 9, 11, 14 and 15.
The wrapper drains and validates all four sender lanes before the unchanged
timed request, executes that request once, and drains immediately afterward.
The timed `run_transfer` function is exactly 7,350 bytes with SHA-256
`b86593361c76c3d2903d722b1b0292fa1bc833210d5efb2438dea1aac08ad798`.
The fixed sixteen-transfer schedule is unchanged.

The evidence keeps four timing domains separate:

- sender credit-decision waits, partitioned by cause and wake outcome;
- the whole Yamux DATA call interval;
- its enqueue-select and caller completion-select intervals; and
- the observed physical `conn.Write` interval.

The 256 MiB HTTP body, Yamux DATA payload and Yamux-framed plaintext bytes
submitted to `conn.Write` are separate domains. DATA includes tunneled HTTP
response headers in addition to the body. The recorded `conn.Write` elapsed
time includes TLS serialization, output-lock work and lower-I/O blocking. It
does not measure ciphertext bytes, delivery, TCP completion or NIC completion.
The observer has no direct public-request-to-stream marker;
its attribution is descriptive interval/exclusivity evidence and does not
establish a cause.

## Read-only stream-lifetime follow-up

A separately mapped read-only analysis is bound by `SHA256SUMS.txt`
`5c0b53c1333ccc26664bbab35fffbd5b58b14dade6e8069e945247915d7cd84d`.
It preserves the failed second analysis attempt and the successful third and
fourth attempts. H2 sequence 15 reuses the same lane, stream, tuple and origin
after 26.67 seconds and records zero sender credit wait. Direction-cold H2
sequences 9, 11 and 14 record positive waits, but sequence 14's retained HTTP
connection crosses direction: the preceding request grew the agent receive
side, while the selected download sends the response from the server side.

The available ten-event growth history is agent receive-side evidence and does
not provide a time-aligned server receiver credit snapshot. The follow-up is
consistent with retained same-direction stream growth, but it does not explain
the throughput spread or establish a cause. The remaining observation gap is a
time-aligned server-side per-stream growth and credit record.

## Source, toolchain and binary binding

The integration is derived from current source commit
`8ac4c8df8237e37777ba2fac0da6f949da17bdea`. Final selected integration source
checkpoint 10 is bound by manifest
`b532337700b7e3b17e1debc3a90ef8f786317d58bfb54449bf577616bd1514f3`.
The reviewed source-corpus manifest is
`78820279349001e1af0ba24cce843030b795f5e52548bd39548071204cc41ea3`,
and the validator-manifest correction receipt is
`e9453df0e13a2c342e8e802712206d991c478f0a36d6f291b10053229de2abc6`.
The later checkpoint 11 attempt is preserved as partial and unused.

The audited build uses pinned Go 1.26.6. Its manifest is
`ba1a31b8f54e7cbb4df8dace0990a4aba87fd4462803a31a6e3082810e9e1b2b`;
the agent binary hash is
`43eadb7664d70f9585887ac824cc7e6dcb55d6fa40731e3888b6fa5a1cc8d74f`
and the server binary hash is
`f2e6cf390fdcd727b8c7120a6d877dc3a53afe9d8b970aa7b0d7f6cc892a7bdc`.
Binary bytes are excluded, while build commands, source pre/post inventories,
Go build metadata, test records, publication receipts and binary hashes remain.

Checkpoint 10's scoped static verification has direct status 0. Its saved
outer tool envelope remains 1, and the package preserves that discrepancy and
the independent direct-status-0 artifact-closure review without normalizing
the original result.

## Preserved history

Every failed or superseded attempt remains available. This includes proposal
overwrite evidence; integration checkpoints 1 through 10; rejected checkpoint
8; partial unused checkpoint 11; all static and behavioral contract attempts;
build and publication corrections; smoke 1's pre-endpoint failure; smoke 2's
launcher, preparation, audit and original validator failures; the separate
offline validator; and the final sustained failure. `ATTEMPT-HISTORY.md`
summarizes the sequence without replacing the original records.

## Package structure

`PACKAGE-MAP.tsv` maps 1,315 selected artifacts to one destination each. The
83 short summaries, ledgers, statuses and reports remain readable as loose
files. The other 1,232 artifacts are stored once in
`ancillary-sender-wait-integration-evidence.tar.gz`. The archive normalizes
only member order and tar metadata; content bytes and selected modes are
unchanged.

`ALLOWLIST.tsv` is the explicit selection. `EXCLUDED-RUN-ARTIFACTS.tsv` lists
all 182 omitted smoke/sustained artifacts with their original SHA-256, size,
reason and full-run ledger. `ANCILLARY-BUNDLE.sha256` binds the archive,
`SHA256SUMS` binds every package file, and `verify-evidence-package.py` verifies
every archive and loose entry without extraction.

The ignored source corpus is not reconstructable from an ordinary checkout.
The builder therefore requires an explicit source root. It is retained as
historical build documentation rather than a promise that absent tmp evidence
can be recreated later.

## Security and qualification limits

The package excludes TLS private keys, readiness files, generated management
tokens, endpoint fixture logs/state, runtime caches, compiled binaries and full
reconstructed source trees. Separate nonprinting scans of the 168-file smoke
and 168-file sustained run selections each found zero exact or heuristic hit
artifacts. The builder verifies both frozen scan reports, their scanner-source hashes and
their exact candidate bindings before packaging. The process-local trace
bearers were not persisted, so exact comparison against those values is
unavailable.

This evidence does not establish a runtime cause or fix. It does not clear the
original current-production WAN failure, the 90% throughput floor, HAProxy
parity, pressure, capacity, broader integration, release or merge gates.
