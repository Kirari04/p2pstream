# Aggregate refund queue/ack causal diagnostic bundle

This diagnostic compares two ordinary, tag-off builds from the same application source ref `5eeccaaa797254d7bde4db9854a337a02c3bc28e`.

Both variants apply `common-scaffold.patch`. It retains the current selected refund ledger, sorted locks, shutdown behavior, DATA gather/copy ownership, and one joint accounting commit before the first physical byte. It validates a bounded aggregate refund batch of 1 through 17 12-byte headers and uses one connection-write deadline for the whole operation. A nonblocking shutdown/deadline precheck runs before every enqueue and after every successful acknowledgment.

- `refund-batched-item-control`: queues the entire validated batch as one priority item and waits for one acknowledgment.
- `refund-separate-items-ack`: differs only by the private diagnostic boolean in `separate-items-toggle.patch`. It queues each 12-byte header as a separate priority item and waits for its acknowledgment before advancing. Only the first item carries the joint accounting commit.

The separate-item arm intrinsically changes queue-item allocation and scheduling as well as physical write scheduling. This is a causal diagnostic, not a pure TCP-write experiment and not a production candidate. Any timeout, shutdown, short write, or write error permanently aborts the batch; remaining headers are never retried. Ambiguously committed credit remains session-owned until full session cleanup.

## Portable replay

From a clean archive of the base source ref:

```sh
patch -p1 < common-scaffold.patch
# Apply only for refund-separate-items-ack:
patch -p1 < separate-items-toggle.patch
```

Both patches use canonical `a/third_party/...` and `b/third_party/...` paths. Forward application reproduced the recorded arm source hashes. Actual reverse application restored the clean base hashes and removed the diagnostic test file.

## Validation

Both arms passed:

- focused normal, count 10;
- focused race, count 5;
- full Yamux normal, count 1;
- ordinary tag-off agent and server test-binary compilation.

The focused set covers exact 1/2/17-header wire order, exactly one commit, malformed batches, short/error boundaries without retry, timeout before/after selection, later queued timeout/Close, one shared deadline, ready deadline/shutdown fairness, reader-control progress, terminal/global handling, selected-ref cleanup, DATA gather ownership, and partial DATA writes.

`driver-origin.test` is a byte-for-byte copy of `refund-batched-item-control.server.test`. `SHA256SUMS` is generated after all other bundle contents and deliberately does not contain its own digest.
