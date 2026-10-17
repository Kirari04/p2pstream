# Materialized local-test source checkpoint

Status: ready for source review only. No compiler, test, runtime, container, smoke, or WAN invocation has run. The exact proposed commands are PREDECLARED-COMMANDS.json. The coordinator has not granted a runtime window.

## Inputs and ordinary-source mapping

`../test-source` contains 459 files, 8,164,026 bytes. It was independently copied from prior audited history build attempt 3's `internal`, `gen`, `third_party`, go.mod and go.sum; docs (85 MiB), app entrypoints, and web assets were excluded because these agent-package tests do not import them. MATERIALIZATION.json records all 450 original input paths/hashes. No source hardlinks were used; inode checks and hashes confirm historical inputs remain unchanged. CHECKPOINT-1-SHA256SUMS and the prior successor/SHA256SUMS still verify unchanged.

The original reviewed observer input hashes match attempt 3, including the external fixture previously referenced from attempt 2. The blocked predecessor diffs plus successor diffs were applied only in the isolated tree. Formatting is the only executed Go tool; no type checker/compiler/test was invoked. The new real transport fixture exists only in this tree.

Production reference is 27c8e31f3826ab5326d941b20644c1f56159d99d. The exact ordinary 0895fdd→27c8e31 health-admission change in public_routing.go was applied while preserving the historical observer's response attribution hook. See ordinary-health-mapping.diff. The broader ordinary-source additions are inherited from the prior audited qualification build, not newly claimed production changes: agent binding hooks; public response/copy attribution; Yamux raw/TCP/sender diagnostics; trace hooks. MATERIALIZED-CHANGES.json lists all 13 files changed/added against the audited source and all existing Go files differing from the current production worktree. Old test-source server tests are retained as compile inputs; this narrow run does not claim to repeat current production health tests or the root's integration race validation.

TEST-SOURCE-SHA256SUMS pins every materialized file. MATERIALIZED-FOCUS.diff shows the real transport fixture, stack helper and new terminal observer in one focused review artifact. No new archived source tree or WAN runner was created.

## Existing consumer and fake-test inventory

Source search of all materialized Go inputs finds exactly one production bindRaw caller (the private cloned default HTTP transport dial wrapper), two existing test bindRaw calls (both use qualificationRawWriteTestTCPPair's concrete *net.TCPConn), and no call to QualificationUnderlyingConn except the newly added rejecting ownership test. The prior getter had no source consumer. Normal request binding wraps a newly dialed TCP connection once and returns only the observed wrapper. TLS receives that wrapper; its underlying TCP reference remains owner-held. TCP_INFO providers use RawConn.Control and SYS_GETSOCKOPT only; they do not close or shut down the FD. These are exact source-closure assumptions, not a claim about arbitrary replacement transports or external malicious Go code.

Existing fake-connection tests do not call bindRaw. They directly construct qualificationObservedRawConn or invoke beginRawWrite/finishRawWrite to test legacy duration/error/short counters, straddling/boundary behavior, overflow, generation, concurrent protocol temporal classification, and shutdown behavior. They remain unchanged and are included by the predeclared selector. Those metadata-only tests do not gain prospective stack/TCP attribution. No fake input was converted into a real binding or removed to satisfy the stricter predicate.

Old terminal recorder tests constructing five synthetic completed records still test original schema/order/fresh-output behavior. They may emit a separate invalid prospective sidecar, which does not alter their original verdict. The new five-owner intent tests separately cover all-five owner membership, identity, closed/unbound/active-DATA state and duplicate intent.

## Real controls and finite schedules

`qualification_terminal_transport_test.go` uses real loopback *net.TCPConn, the actual bindRaw wrapper, verified TLS 1.3, the actual observed above-TLS body, and real aggregate Yamux send/Close/retirement. It asserts the actual stack grammar and exact normal GoAway plaintext. It reuses the existing per-owner threshold-history capture function seam; there is no added production hook or observer.

- First schedule: the real pre-write TCP_INFO succeeds, then a test barrier holds its return. Local intent and actual owner physical Close occur; releasing the barrier makes the real TCP Write return zero bytes with net.ErrClosed. The raw wrapper remains in its real TLS/Yamux caller stack.
- Second schedule: the real raw write completes and the peer verifies the exact normal GoAway. The test pauses the existing after-capture provider before its real TCP_INFO call. Intent and actual Close occur; release produces the real unavailable capture, followed by retained finalization with the original application stack still live.
- Neither is a kernel-stalled write or a performance measurement. Intent here belongs to one real owner; five-owner coverage has separate owner-contract tests. Neither schedule alone establishes a valid integrated five-lane arm.
- Real handshake and close_notify writes run with a deliberately unrelated active plaintext-control token. Their exact known protocol ancestry must be present; the application stack predicate must reject and clear the inherited plaintext token. They cannot explain a legacy failure.
- New real ownership controls reject a fake non-TCP bind, an actual underlying getter escape, and a second actual Close returning net.ErrClosed. Existing Python contract mutations additionally reject missing TCP proof, duplicate/non-nil-error close, escape, wrong Go, protocol path and truncated stack.

The repository certificate expired in 2025. These local controls verify trust, server name and signature at cert.NotBefore+1h; all real transport deadlines/order use the real clock. This does not claim current certificate validity.

Each socket gets a 10-second real deadline; every fixture join has a five-second deadline. The test runner has its own 60/90-second limit and outer 240-second compile/run limit. Cleanup releases barriers, closes fixture sockets and joins all owned workers. Timeout/forced termination is rejecting, not evidence of a normal join. Natural zero exits, no remaining owned process-group members, and unchanged input hashes are acceptance prerequisites.

## Proposed bounded execution, after review only

Run Python contracts once and Go selector `^TestQualification(Terminal|RawWrite|TCP)` once normally and once with race, pinned Go 1.26.6, CPU 8–11, GOMAXPROCS=2, -p=2, module network downloads disabled. The Go commands compile in their ordinary temporary build location; no extra retained binaries are needed. Preserve bounded output/status and exact command records under this task directory, within the 100 MiB total artifact budget. The command manifest contains all argv and limits; it launches nothing itself.

On the first source/build/test/provenance failure, stop for root review. Do not broaden the stack grammar, retry until pass, or infer TLS provenance from ciphertext size. The source-pinned optional forwarding frames and compiler inlining behavior must be established by actual default/race loopback controls. This checkpoint makes no such runtime claim.

The proposed 4 MiB H1/H2 smoke remains a separate future decision after these contracts and runner/build wiring pass review. No WAN command appears in this plan. Original transfer, resource and performance gates and historical rejected verdicts remain unchanged.
