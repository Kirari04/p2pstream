# Native HTTP/1 agent-stage diagnostic build bundle

This immutable test-build-only bundle layers the HTTP-stage fixture over the accepted `40e1046…` transport-ceiling source. It exercises the production server HTTP transport and agent relay call paths while omitting public TLS/H2, ReverseProxy, and policy/cache/WAF/shaping layers. It contains no production change and no performance result.

The source uses native HTTP pooling with fixed total capacity 16 and the actual pooled partition 11, a 30-second idle timeout, exact 256 MiB payloads, a bounded 32 KiB response pump into an 8 KiB consumer, and the documented native upload `io.Copy` origin behavior. The 64 MiB working ledger is a fixture allowance/promise; it is not a hard TLS, heap, RSS, kernel-memory, or resident-byte bound.

Validation used pinned Go 1.26.6 and owned ext4 temporary/cache/module directories from the first Go invocation. Focused helper, agent, and server checks passed normal and race modes at count 3; tag-off compilation and tagged endpoint builds passed. The portable patches replay forward to the recorded source trees and reverse to the accepted transport tree.

The first 25-case pure-validator run retained one expected failure: Python equality allowed a raw response boolean `false` to compare equal to validated integer `0`. The corrected runner independently phase-validates raw responses, strictly validates status/origin HTTP integers, and independently validates terminal result rows. The final 28-case run passes four classification cases and all fail-closed negative cases. No wrapper, namespace topology, endpoint process, network workload, or traffic was executed.
