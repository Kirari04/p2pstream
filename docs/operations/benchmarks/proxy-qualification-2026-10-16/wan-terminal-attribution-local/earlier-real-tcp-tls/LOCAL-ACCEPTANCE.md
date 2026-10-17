# Predeclared local acceptance, source-only

Scope: this standalone module imports the current local production Yamux module. It does not alter the WAN runner/observer/validator, production source, historical verdicts, or any gate. No WAN experiment follows automatically.

Two channel-forced schedules use real loopback TCP, certificate-verified TLS 1.3, and production Yamux Session.GoAway/Session.Close. Raw Write always delegates to the real TCPConn; RawConn.Control is real and its callback invocation is recorded. Certificate and private key are existing public test fixtures and are not copied or printed. The only eligible plaintext is the exact 12-byte GoAway(normal) header emitted by that real Yamux call. No inference depends on ciphertext length.

Source preflight found that the repository test certificate expired in August 2025. TLS verification therefore uses its NotBefore + one hour as a fixed certificate-validation time; trust, name and signature checks remain enabled. All networking, timeouts and event ordering use real clocks. This is local fixture verification, not current-date certificate acceptance or a WAN trust change.

1. Pause the raw wrapper before TCPConn.Write, mark local stop intent, call Session.Close, await the actual TCP close, then release the real Write and actual RawConn.Control. Accept only zero bytes with errors.Is(net.ErrClosed), verified plaintext, correct owner-local event order, closed-provider Control error with no callback, and naturally joined Session.Close/caller/fixture workers.
2. Permit the real TCP Write and exact peer plaintext read; pause before actual Control, close locally, resume Control. Require full successful Write, real unavailable-after capture, and natural joins. It must not classify as an errored cancellation.
3. Twelve predicate negative controls mutate the retained real positive evidence: DATA/unknown, absent/late intent, non-local error, partial/nil-short write, changed generation, missing join, post-retirement activity, overlap, active write, and invalid capture ordering. All reject. The predicate explicitly requires raw Write return < after-capture completion < plaintext Write return. The exact GoAway allowlist is established by source and the real scheduled GoAway; no separate test merely restates bytes.Equal.

Limitations predeclared: no saturated/kernel-blocked write; the barrier pauses before the syscall. No exact tiny2 interleaving or frame identity claim. RawConn.Control is exercised directly, without a synthetic result or a TCP_INFO syscall: after close, the real Control callback must never run. Negative reset/short/data cases are predicate controls, not claims to reproduce those socket failures. This proves a narrow typed-control/local-close interpretation, not a complete WAN schema or broad benign-error exception. The no-error successful path in the original observer is not modified.

Proposed one-shot invocations, only after coordinator source review and the granted CPU window:

```
taskset -c 8-11 env GOMAXPROCS=2 timeout --signal=TERM --kill-after=5s 90s go test -count=1 -timeout=30s -v .
taskset -c 8-11 env GOMAXPROCS=2 timeout --signal=TERM --kill-after=5s 120s go test -race -count=1 -timeout=30s -v .
```

The module reuses the system Go cache and contains no copied repository tree, artifacts archive, generated build corpus, or new dependencies beyond the existing local Yamux. Capture status and stdout/stderr for each mode once. Record pre/post source hashes, exact tests, natural exit, and no remaining owned process. A failed attempt is retained and reviewed before any revised invocation. Five-second per-operation fixture deadlines and the process timeout are failure bounds, not timing assertions. Total new retained artifacts remain below 100 MiB.
