# Prospective terminal-attribution source review checkpoint

Status: source proposal only, not approved for execution. No builds, tests, containers, or traffic have run for this proposal. Tracked production files are unchanged. This checkpoint exposes a provenance blocker found during source review; the proposed positive classification must not be used until that blocker is resolved.

The original TCP threshold question remains unanswered. This is a diagnostic fixture proposal, not a throughput fix. Historical rejected runs remain rejected. The standalone local proof under ../wan-next-candidate-20261017 showed two forced real TCP/TLS GoAway schedules only; it does not validate this larger integration or other controls.

## Source and evidence map

- INPUTS.json pins the three immutable observer/fixture inputs and two immutable runner inputs by path and SHA256. observer-integration.diff and runner-wiring.diff are proposed patches against those inputs. No copied build tree is needed for review.
- source/internal/agent/qualification_terminal_attribution_qualification.go adds bounded terminal metadata, exact above-TLS header parsing, all-five-owner local intent, and a separate sidecar. The tag-only Yamux getter returns the already installed connection.
- validate-terminal-attribution.py defines the new verdict. LEGACY-VALIDATOR.json pins the reviewed 106-case validators directly; these were not copied. The proposed runner still needs coherent wiring of those same 106-case inputs, updated pins, and a reviewed build receipt before execution. Existing pins deliberately do not approve the new source.
- source/internal/agent/qualification_terminal_attribution_test.go and contracts/test-terminal-attribution.py are unexecuted contract sources. They do not establish transport causality. The Python minimal fixtures do not replace full legacy validation.
- Prior real TCP result: ../wan-next-candidate-20261017/RESULT.json SHA256 08e9a347b32c5c56b3027c801edf19a7de10cd7a0ae9180762883d9dc9840241. Prior review: ../wan-next-candidate-20261017/REVIEW.md SHA256 d6558e414a65c47fb7648130224464977f232efcc51a70c4e8f024baca61890f.

## Contract preserved by the proposed adapter

The old sidecar, raw error totals, ambiguity/invalid flags, old validator result, saved old final record, and old rejection strings are retained verbatim. The separate attribution sidecar hashes the old raw bytes. The new validator recomputes the old verdict using pinned sources and compares the saved result and artifact hashes. It requires the old record to report actual natural endpoint exit 0; it cannot reinterpret an old endpoint exit 1.

The fixture replacement handles only the exact persisted legacy-diagnostic-incomplete sentinel. File, sync, intent, agent shutdown, and arbitrary endpoint errors still fail. A sentinel alone never makes an arm acceptable: the independent prospective predicate and all original endpoint, resource, transfer, sender, process-group and workload checks still run. The old verdict remains exported even when the new one would differ.

The new metadata is bounded to five owner identities, 16 retained raw records per owner, and 17 parsed headers per plaintext write. All errors are counted over the owner's lifetime. Only matching owner/session/generation with complete five-owner intent and joined retirement can qualify. DATA, unknown/mixed frames, errors with bytes written, nil-error short writes, overflow, newly started post-close writes, post-retirement activity, missing owners, and independent ambiguity remain rejecting. A snapshot drain is only a prerequisite for the public DATA boundary, never evidence that producers stopped. A failed TCP after-capture remains unavailable/inconclusive.

## Blocking provenance findings

1. The raw Return sequence is an observation after Conn.Write and after acquiring owner.mu. Scheduler delay can move it across Close. The corrected source comment explicitly disclaims an atomic return timestamp and causality from that order. Attribution needs a uniquely owned real TCP socket whose only physical close path is the observed wrapper, plus typed errors.Is(err, net.ErrClosed). The existing bindRaw accepts syscall.Conn rather than requiring a concrete *net.TCPConn, exposes QualificationUnderlyingConn, and the proposed close metadata records first close but not duplicate attempts. Those ownership assumptions need to be tightened or proved against all exact build inputs; they are not proven by the current JSON predicate.

2. Above-TLS frame identity is exact, but association of that plaintext to a concurrent raw call is not yet exact. Local Go source /usr/local/go/src/crypto/tls/conn.go (VERSION go1.26.3) increments activeCall in Write before taking out.Lock (1202–1220). handleKeyUpdate can hold out.Lock and emit a handshake response (1334–1360) while the observed Yamux Write waits. The current active-plaintext copy could then mislabel that TLS protocol raw call as Yamux control. TLS Close skips close_notify when activeCall is nonzero (1423–1460), which narrows one race but does not remove the key-update/alert counterexample. The existing metadata does not distinguish these paths. Thus current positive synthetic classifier tests do not prove actual exact raw/frame provenance.

Do not execute this positive classifier on traffic as an accepted diagnostic. A bounded call-site provenance discriminator inside the raw wrapper might close the second gap, but has not been designed, approved, or implemented here. Do not add another instrumentation layer without a coordinator decision. If exact provenance cannot be shown within the approved bounds, keep the result rejecting and stop this candidate.

## Control parser scope and cost

The parser permits only complete 12-byte header-only controls emitted by current Yamux source: session.go GoAway/Ping (383,407,892), stream.go WINDOW_UPDATE/FIN/RST (490,558,674,692), aggregate_window.go allocation/global refunds (721,725) and growth (795,844). The maximum refund batch is 16 stream headers plus one global header (aggregate_window.go:22). DATA including zero-length DATA rejects. Unexecuted local parser controls cover these cases and malformed/mixed/overflow variants. The earlier transport proof covers normal GoAway only, not Ping, credit updates, or batches.

This observer parses every plaintext Write, allocates a header slice for each accepted control, acquires owner.mu around plaintext begin/end and raw return, and retains terminal records. That overhead can change TLS lock timing, control cadence, CPU allocation, and TCP behavior. Instrumented throughput is not production-equivalent. Neither provider capture duration nor the new sequences measure total observer overhead. Any future TCP trace is diagnostic evidence under this observer, never a production performance comparison or causal proof by itself.
