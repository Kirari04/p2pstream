# Amendment: observed H2 expiry overrides the intended warmth schedule

This is a separate amendment to `STREAM-WARMTH-SCHEDULE-MAP.md`, preserved at
SHA-256 `646e93342b77e15339c42cff1310dfba227a552f921ee79e6097182962b91593`.
The original source map describes the warmth opportunities implied by the
fixed row order. The final transport trace below is authoritative for which
opportunities actually occurred.

## Final observed trace

H2 sequence 14 was an exact same-key, same-origin reuse of sequence 10 after
23.982017568 seconds. It reproduced the retained slowdown at 14.550217 MB/s:
the handler took 18.305310 seconds, with 17.333642 seconds inside upstream
`Read` and 0.802516 seconds inside downstream `Write`.

That long handler changed the state available to later rows. H2 sequences 15
and 16 did not reuse their intended sequence-11 and sequence-12 connections:

| Sequence | Intended prior row | Age since prior `PutIdleConn` | Observed result |
| ---: | ---: | ---: | --- |
| 15 | 11 | 34.587811824 s | `Reused=false`, `WasIdle=false`, new origin connection ID |
| 16 | 12 | 35.000628464 s | `Reused=false`, `WasIdle=false`, new origin connection ID |

Both ages exceed the unchanged `http.Transport.IdleConnTimeout` of 30 seconds
(`internal/server/agent_transport_pool.go:475-491`). Their lane-specific route
keys were revisited, but the prior connections and their logical Yamux stream
state had expired. H2 sequence 15 was therefore a fresh stream, not the
same-direction-warm reuse described as an intended opportunity in the original
map. Its fast result cannot establish retained same-stream H2 recovery. The H2
arm's final classification remains `inconclusive-upstream-reuse`.

H1 did realize the intended schedule. Its four post-close revisits occurred
after 25.153-25.420 seconds and reused the exact physical keys and origin
identities. H1 sequence 15 is valid same-direction-warm evidence; H2 sequence
15 is not its matched retained-stream counterpart.

## Interpretation correction

The source conclusions in the original map remain valid: successful
keepalive reuse preserves the logical Yamux stream and its per-direction
`GrowingConn` state, while a replacement stream starts cold even on a grown
physical session. The final observations narrow their application:

- sequence 14 is the decisive H2 same-key/same-origin direction-cold reuse and
  localizes its excess primarily inside upstream `Read`;
- sequences 15 and 16 are expiry-confounded fresh connections caused by the
  preceding long sequence-14 handler;
- the fixed phase order defines reuse opportunities, not guaranteed reuse;
  observed `GotConn` and origin identity must override schedule inference; and
- no conclusion that H2 becomes fast on same-direction retained reuse is
  supported by this cohort.

This expiry is consistent with the configured 30-second idle lifetime and is
not evidence of premature transport retirement. It also creates no basis to
repeat the rejected allocation, growth, refund, DATA, epoch, or TCP-low-water
experiments. The immutable cohort and its inconclusive classification remain
the evidence.

Sources: `tmp/continuation-20261016/wan-copy-monotonic-source/runs/
matched-copy-monotonic-sustained-1/OFFLINE-ANALYSIS.md` and its
`offline-audit.json` (`98178534cae530d1cef826ee95a8eb725ce5613792cf214b8076e783cdb9bbb0`).
