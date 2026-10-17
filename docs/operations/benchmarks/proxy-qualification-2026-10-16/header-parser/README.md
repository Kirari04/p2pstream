# HTTP/1 header parser allocation diagnostic

Measured on 2026-10-16 with Go 1.26.6 on the qualification host. This standalone check calls the same standard-library `http.ReadRequest` parser used by the HTTP/1 server; it does not run the public listener or impose its wire limit. Both generated requests are below the configured 64 KiB wire limit.

The one-value request used 65,000 wire bytes and allocated 323,787 bytes per parse. The distinct-key request used 64,997 wire bytes, produced 8,666 header keys, and allocated 1,709,479 bytes per parse. These are ten-parse averages with parsed requests held and garbage collection disabled during measurement. They include parser temporaries and are **not** post-GC retained sizes, concurrent cgroup highwaters, or evidence of safe admission under a burst. Timing was not measured. Map allocation and runtime details can change between runs/toolchains.

The connection's twice-wire-limit header allowance is therefore not a hard decoded-map allocation bound. Request-level header accounting begins after native parsing. Mandatory actual-memory pressure checks remain separate protection; a constrained real-listener burst and recovery test is still required before reducing public TCP allowances.

Replay from the repository root:

```sh
cp docs/operations/benchmarks/proxy-qualification-2026-10-16/header-parser/allocation-check.go.txt /tmp/p2p-header-allocation-check.go
go run /tmp/p2p-header-allocation-check.go
```

The preserved program differs from the executed program only by renaming the output field `retained_heap_delta_per_parse` to the accurate `heap_delta_without_gc_per_parse`; the sanitized result uses the corrected name. The measured allocation field is unchanged.
