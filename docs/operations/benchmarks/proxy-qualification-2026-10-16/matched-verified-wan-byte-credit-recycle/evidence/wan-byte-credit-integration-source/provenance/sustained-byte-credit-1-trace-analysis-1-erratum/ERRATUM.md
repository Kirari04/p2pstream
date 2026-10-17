# Unit erratum for sustained byte-credit trace analysis 1

This append-only erratum clarifies the display units in the frozen report at:

`tmp/continuation-20261016/wan-byte-credit-integration-source/provenance/sustained-byte-credit-1-trace-analysis-1/REPORT.md`

The original report is preserved byte-for-byte (SHA-256 `571abd6312b5b556cb73ee623083af93e1fdb460f7798ac6f2d288a157d73f87`), and its frozen ledger is preserved (SHA-256 `276df2c138160d1f9c4e20ab44090a2488a69f8475e8054656e2c56e57398486`).

In the local measured-sections table, the header `total/max (µs)` is ambiguous. Every cell formatted as `A / B µs` should be read as:

- `A`: **total in milliseconds**
- `B`: **maximum in microseconds**

For example, `4.274 / 264.0 µs` means **4.274 ms total / 264.0 µs maximum**. Likewise, `0.300 / 16.1 µs` means **0.300 ms total / 16.1 µs maximum**.

The underlying integer nanosecond values in `ANALYSIS.json` are authoritative and unchanged. This erratum changes no result, attribution, acceptance state, or inference.
