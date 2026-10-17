# Independent incomplete early-share outcome review

Decision: retain INCOMPLETE, with no complete treatment-effect estimate or qualification clearance. The screen correctly stopped after the candidate; the second control was not run. Do not relabel the current arm, salvage the third arm, or repeat this candidate merely to obtain a cleaner teardown. This reviewer read saved artifacts and source only; no validator, test or traffic was executed.

## Exact rejection

The candidate completed sixteen 256 MiB transfers with zero recorded payload errors. Its five endpoint/helper exits were natural zero, and both early-share sidecars report eight completed attempts, zero active/dropped attempts and four joined event sessions. These facts do not waive the separate mature raw-write closure gate.

The failing owner is agent lane 3, generation 2, session `0x27c5cfa58008`, `10.212.0.2:39268` to `10.211.0.1:46589`. Its sole terminal raw error attempted 34 TLS bytes, wrote zero and matched `net.ErrClosed`. The captured application plaintext is a 12-byte Yamux FIN: type 1, flags 4, stream 8, value zero. The live stack joins TLS application Write through the observed tunnel body to Yamux sendLoop/send. It is not an early allocation update or positive byte-credit grant.

The observer's same-owner ordinal order is:

`stop 309980 < plaintext begin 309981 < physical close begin 309982 < raw begin 309983 < raw return 309984 < raw after 309985 < plaintext end 309986 < physical close end 309987 < joined 309988`.

The producer reports `problems=20`: `terminalPostCloseWrite` (4) plus `terminalOtherLegacyAmbiguity` (16). Final activity is zero and the session is joined. The existing classifier requires zero problem bits, a raw begin before physical close, and, for the accepted cancellation exception, `plaintext begin < raw begin < stop < close begin`. The observed record violates both the pre-close raw-start and pre-stop overlap boundaries. The validator and producer therefore agree; this is not a schema incompatibility or an already accepted class accidentally missed.

Existing Yamux Session.Close closes the physical transport before joining its send loop, so pending control transmission can race that close. The evidence identifies a teardown FIN interleaving, not payload corruption, a leaked worker or unique early-share causation. A broader terminal cancellation interpretation would require a new prospective, source-backed contract and controlled shutdown tests. It must preserve the original verdict and reject DATA, unknown plaintext, partial writes, ownership changes and genuine post-close new operations. It is not justified as a way to rescue this performance candidate. Serializing Close behind an arbitrary blocked Write would also risk defeating cancellation and is not a supported fix.

## Candidate usefulness

Both roles show four initial-pool no-ops and four actual later grants from 524,288 to 4,194,304 bytes. Thus the intervention was exercised in each receive direction. Candidate download rates for sequences 9, 11, 14 and 15 are respectively **27.6096, 27.1750, 15.0946 and 34.1201 MB/s**. The first three miss the immutable 29.7 MB/s floor; the saved mature attestation independently records retained-warmup and steady download-floor failures. Their upstream Read totals are 8.7383, 8.9791, 16.8590 and 6.9477 seconds.

Those observed candidate floor failures alone prevent the preregistered positive conjunction from passing, regardless of an unrun second control. This does not establish a complete negative treatment effect: teardown integrity failed and the fixed three-arm comparison is incomplete. It does make another WAN run of the same trigger/cap a poor next action. Do not increase the cap, add warmups or tune timing adaptively from this outcome.

## Smallest next action

Proceed with source-only feasibility review of public-stream lane selection under ordinary CUBIC and idle policy 1. Determine whether a sequential request can unnecessarily select a cold physical sender while another already warm lane has usable capacity, and whether a bounded preference can preserve all four public lanes, control isolation, negotiated/operator limits and concurrent capacity. The earlier idle-policy result motivates this hypothesis; successful early credit grants with persistent slow downloads make another initial-credit-only trial less informative.

This is not permission to reduce the pool to one lane, keep TCP artificially busy, change OS policy or run a new benchmark. Both modes' public-lane layouts and lane identities must first be mapped accurately. A later experiment would need a separately preregistered routing-policy estimand, since selected lane and origin reuse would become treatment outcomes rather than immutable same-lane conditioning. Address the terminal contract prospectively only if that next experiment needs it; preserve this incomplete run and all strict failures unchanged.

Exact evidence bindings are in `INCOMPLETE-OUTCOME-INPUTS.json` (SHA256 `0c45b03f21a49db3d25e9c62c0bad147a91ac9152062db833956d5e2b7b3b804`).
