# Local Yamux extension

Based on HashiCorp Yamux v0.1.2, distributed under its original MPL-2.0
license (see LICENSE). Upstream source, tests and test fixtures are retained.

The local change gives each stream an atomic receive-window limit and exposes
`MaxReceiveWindow` / `GrowReceiveWindow`. The wire protocol is unchanged.
Applications must reserve additional memory before granting credit, and hold
that reservation until the stream fully closes. Limits only grow; already
advertised receive credit must not be revoked. No other streams are changed.

p2pstream uses this to keep small/idle streams cheap while allowing sustained
transfers to grow their windows without multiplying every idle connection's
memory reservation. Upgrade this copy alongside the upstream module version
and run `go test -race ./...` in this directory when changing it.
