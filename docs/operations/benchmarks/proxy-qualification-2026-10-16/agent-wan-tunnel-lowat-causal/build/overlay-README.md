# TCP_NOTSENT_LOWAT tunnel causal diagnostic bundle

This bundle is a temporary, ordinary-build causal experiment based on application and harness ref `5eeccaaa797254d7bde4db9854a337a02c3bc28e`. It is not a production candidate and does not authorize retaining either overlay.

Both variants apply the identical common diagnostic scaffold. The only arm difference is the private `apply_tunnel_notsent_lowat` boolean: `false` for `tunnel-lowat-default-control` and `true` for `tunnel-lowat-32k`. The candidate requests `TCP_NOTSENT_LOWAT=32768` only on authenticated physical tunnel sockets at both endpoints. It does not call `tcpsocket.Configure`, alter `SO_SNDBUF` or `SO_RCVBUF`, change origin/public socket policy, or change Yamux framing, credit, allocation, shutdown, or DATA behavior.

The scaffold records a bounded maximum of 64 startup observations per process. The causal cohort is conclusive only with exactly five initial physical tunnel sockets at each endpoint, the same five after the workload, no reconnects, no dropped observations, exact reverse tuple matching, and lanes 0 through 4. The control arm records raw LOWAT without setting it; a raw value of zero is interpreted only with the recorded namespace `tcp_notsent_lowat`. Send/receive buffer values are observational because kernel autotuning may change them naturally.

The strict close/failure behavior for unsupported sockets, read/set errors, or candidate mismatch exists solely to prevent a silently mixed diagnostic arm. It is not proposed production policy. Agent custom `DialTLS` or `DialTLSContext` hooks are deliberately rejected by this dedicated diagnostic transport because they bypass the wrapped raw-TCP `DialContext`.

The patches are portable and were proven by forward apply, reverse dry-run, and exact control/candidate tree comparison. All final tests and endpoint builds used the absolute Go 1.26.6 toolchain recorded in `toolchain.txt`, with no build tags. `driver-origin.test` is a byte-for-byte copy of the control server binary.
