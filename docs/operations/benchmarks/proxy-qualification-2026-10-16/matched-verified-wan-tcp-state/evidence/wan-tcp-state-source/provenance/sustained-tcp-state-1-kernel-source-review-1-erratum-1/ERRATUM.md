# Attribution erratum for the threshold-history proposal

This append-only erratum corrects two design statements in the frozen
`sustained-tcp-state-1-kernel-source-review-1/REPORT.md`. The original report
remains unchanged.

1. Sampling only DATA raw writes is insufficient. The accepted raw connection
   wrapper binds after TCP connect and before TLS, and TLS handshake, alert,
   control, and DATA writes all share that wrapper. The first finite
   `snd_ssthresh` can therefore appear before the first DATA envelope. A valid
   first-transition monitor must begin at raw-owner bind and synchronously
   bracket every existing raw `Write` until the first sampled threshold change.
   DATA envelope, stream, and body attribution is retained only when it exists;
   it is not required for TLS/control writes.

2. Namespace `TCPHystart*` counters cannot attribute an event to one owned
   socket, even when the public transfer schedule is serialized. Other sockets
   in the private namespace, including origin-facing loopback connections, can
   change the same counters. Those counters are therefore excluded from the
   minimal per-socket discriminator. They could only be corroborating
   namespace evidence, never an owner-level join.

The corrected minimal design uses only the already owned socket and existing
TCP_INFO provider. It retains a constant-size baseline, last-equal sample, and
first-different bracket, stops after the first sampled change, and treats its
brackets as discrete observations rather than an atomic assignment trace.
