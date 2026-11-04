# Public HTTP parser ownership decision

Retain the existing public connection allowance in this change. The proposed
384 KiB reduction cannot be implemented as a safe parsing-phase transfer using
Go's current public server hooks. No parser, socket or header constant changes
resulted from this investigation.

Admission can exhaust conservative reservations before measured memory reaches
its limit. Lowering a charge without controlling the allocation it represents
would conceal that limitation; RSS alone does not establish a safe parser bound.

The current non-socket promise comprises 256 KiB for retained TLS records, HTTP
buffers, connection state and allocator slack, plus twice the configured wire
header limit (128 KiB by default). The 256 KiB component is not solely parser
storage. The separate temporary 256 KiB handshake promise already returns on
activation; the socket baseline and Linux autotuning remain separately owned.

The original source review used Go 1.26.6; this ordering was rechecked against
Go 1.27.3. Its native H1 server calls `readRequestLimit` before `StateActive`
and before invoking the handler. After the response, `StateIdle` precedes a
buffered peek and the next parse.
[Native server source](https://github.com/golang/go/blob/go1.27.3/src/net/http/server.go).

Go 1.27.3 also caps HTTP/1 header values at 500 by default. Production keeps that
parser limit. The historical 8000-key empty-value burst is rejected with 431
before admission, so it no longer measures admitted decoded-map pressure.
The current admission-pressure fixture uses 400 distinct keys with 152-byte
values, retaining roughly 64 KiB of wire headers below the count limit. It
requires successful origin work and rejects any 431 result as qualification.
Its results must not be compared as the same workload as the historical
8000-key rows; matched comparisons must use the same fixture on both versions.

That ordering creates two ownership gaps in the proposed design. Releasing at
activation precedes this proxy's decoded-header admission. Closing a connection
when idle reacquisition fails cannot discard bytes already in the private
HTTP/TLS buffers; another parse can consume those bytes without calling the
wrapped socket reader. A connection-state callback has no error return for
stopping that parse. The active hook can also be skipped when parsing consumes
only buffered bytes. These observations rule out a read-wrapper gate or a
close-on-denial callback as sufficient enforcement.

A future implementation would need an atomic transfer to decoded-request
ownership, a proven permanent connection working bound, and a native mechanism
that stops every subsequent parse before allocation when credit is unavailable.
Indefinite callback blocking, panic-based flow control and a second HTTP parser
are not adopted. HTTP/2 requires separate treatment: its connection state
tracks aggregate activity rather than individual requests.
[Connection-state contract](https://pkg.go.dev/net/http#ConnState).

A parser integration or replacement would require independent qualification of
TLS read-ahead, buffered pipelines, large and many-key headers on initial and
reused connections, pressure rejection, cancellation, idle reclamation and
races. The historical many-key probe showed that twice the wire limit was an
allowance rather than a decoded-map bound under Go 1.26.6. Go 1.27.3 bounds that
fan-out, but this does not establish a tighter permanent connection allowance.
A concurrent header burst within the current parser limits, together with
separate over-limit rejection tests under a real memory limit, must be part of
that work; held-request success alone would not establish parser safety.
