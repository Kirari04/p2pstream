# Public HTTP parser ownership

Public connections retain an allowance for parsing and connection state before
request-level admission runs. Releasing that allowance merely because a TLS
handshake completed would leave native HTTP parsing without its resource owner.
Low measured RSS alone does not establish a safe parser bound.

The non-socket promise comprises 256 KiB for retained TLS records, HTTP buffers,
connection state and allocator slack, plus twice the configured wire header
limit (128 KiB by default). The separate temporary 256 KiB handshake promise
returns on activation. The socket baseline remains separately owned through
physical close.

The native Go HTTP/1 server parses a request before invoking the handler or
reporting the active state. After a response, its idle state precedes the next
parse. Bytes already buffered by HTTP or TLS can supply that parse without
another read from the wrapped socket. A connection-state callback cannot return
an error to stop parsing.

Consequently, a socket read gate or close-on-denial callback alone cannot safely
transfer all parser ownership to an admitted request. A future reduction needs
an atomic transfer to decoded-request ownership, a proven permanent connection
working bound, and enforcement before every subsequent parse allocates.
HTTP/2 needs separate treatment because connection state describes aggregate
activity rather than individual requests.

Twice the wire header limit is an allowance, not a decoded-map bound. Many
distinct short header keys can allocate more than their wire representation.
Parser changes therefore need coverage for TLS read-ahead, buffered pipelines,
large and many-key headers on initial and reused connections, concurrent hostile
header bursts under real memory limits, rejection, cancellation and races.
Held-request success alone does not establish parser safety.

See [Agent proxy performance](./agent-proxy-performance) for the other
connection, stream and request owners.
