# H1 smoke 3 execution-trace export-safety review

Verdict: the three closed raw Go execution traces are acceptable for a diagnostic evidence export with their hashes, rejected reduction records, parser/analyzer statuses, and this review provenance. The 73,579,046 bytes of full parsed text and the 1,989,688 bytes of wire text add no necessary evidence for the export and should remain review intermediates.

The immutable reduction index is SHA-256 `22956c32da48ad42e74766ec81227ab3782e1eec99238e421d18a124618a8477`. It binds the raw traces to server source `8ac4c8df8237e37777ba2fac0da6f949da17bdea`, runner `e9e453206cd479e1d29b78c05cf4d0eaf0cbaefe3c6ff13f0ba9594235c3bea0`, and server binary `57d77ddc5ee8ec6ab49cab0b7d0dd29641a70fa8598f0c7ba3eac73f46632ff5`.

| Sequence | Raw bytes | Raw SHA-256 | Parsed status | Analyzer status | Reduction SHA-256 |
| --- | ---: | --- | ---: | ---: | --- |
| 9 | 155,433 | `c3f9f63ea873a1a1f0ebcb8de293d252c7de1126ab790142ac038311241aaeb5` | 0 | 1 | `9ff27caf87a4fd2674879034d38fd8ade707cf4099f312e9b3fd328d9b926d01` |
| 11 | 156,367 | `67ae2b06badb01e358be0d69ebb6ea2b1d3c44bc7c6375472203f889e98636de` | 0 | 1 | `bd88dd4e766019bd22c102dabe54ce9a1be81b5642c46657e1eeeddf5cf7c167` |
| 13 | 132,528 | `3268b31959925b62675f8863aadb4ab758218e0f4db2d3b84afb6745dc65e216` | 0 | 1 | `fd50d1988bdb138cebc722121f3fb0145a792778a923eda99efddd4b70150058` |

Pinned Go 1.26.6 `go tool trace -d=wire` returned status 0 with empty stderr for every raw trace. Each raw, parsed, and reduction hash matched the immutable index. Every trace contained exactly one task begin/end pair named `wan-read-wait-request`, exactly one region begin/end pair named `public-proxy-handler`, and zero user-log events. Labels were absent for sequences 9 and 11; sequence 13 contained only the pinned runtime labels `GC (dedicated)` and `GC (idle)`. The exact runtime reasons, metrics, and range names are retained in `export-safety-report.json`.

The nonprinting scan loaded the closed-run agent authentication token, public certificate, and private key. It checked raw, wire, parsed, and reduction artifacts for the token and its bearer form, PEM/DER/base64 key and certificate forms, authorization and diagnostic header names, public host/path values, and repeated payload markers. Every check returned no match. Raw, wire, and parsed artifacts also contained no 64-character lowercase hexadecimal value.

The generated trace-control bearer was process-local and was not persisted in the closed H1 run, so an exact comparison against its decoded random bytes was impossible. The zero-user-log result, exact annotation set, complete wire string inventory, and absence of any ASCII hexadecimal bearer-shaped value support the export verdict, but this remains an explicit limitation.

Raw Go traces disclose whole-process scheduler timing, wall-clock synchronization, goroutine/thread/proc identifiers, GC and heap metrics, concurrency, function names, and absolute source/build/module-cache paths. They do not establish a performance cause. The analyzer status 1 and known zero-PC-frame rejection remain authoritative; the reductions must be labeled rejected diagnostic records until the separately owned parser correction is reviewed.

Recommended export set:

- `results.read-wait-traces/runtime-trace-sequence-{9,11,13}.out`
- `read-wait-reductions/sequence-{9,11,13}.reduction.json`, labeled analyzer-rejected
- corresponding parser and analyzer status sidecars
- `read-wait-reductions/index.json`
- `export-safety-report.json`, `PROVENANCE.json`, `REVIEW.md`, and `SHA256SUMS.txt`

The original run directory was read only. This review started no endpoint, build, test, or traffic workload.
