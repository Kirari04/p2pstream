Pre-publisher Yamux causal overlay

Common application, configuration, qualification harness, and driver source: 5eeccaaa797254d7bde4db9854a337a02c3bc28e
Historical Yamux production source: 56336cb212ecd280c926084f43d60a4365f6b843
Overlay patch SHA256: c400c60b4b20c7a2be3c695481970ae5d43e99446de4afde2a9180b644bcf8cf

The candidate replaces exactly these three production files:
- third_party/yamux/aggregate_window.go
- third_party/yamux/session.go
- third_party/yamux/stream.go

The candidate restores the Yamux runtime immediately before paired aggregate refund publication. It therefore also restores the historical pre-shutdown-wait, pre-lazy-wake, pre-multistream-batch, pre-DATA-gather, and pre-publisher-capture behavior as one diagnostic group.

This overlay is diagnostic only. It deliberately restores the known pre-805a8ea shutdown waiter spin and must not be selected as a production candidate. It does not change application negotiation, flow-control configuration, lane selection, the public request path, or exported Yamux APIs.

Both endpoint variants are ordinary tag-off builds from fresh archives. driver-origin.test is a byte-identical copy of control-server.test.
