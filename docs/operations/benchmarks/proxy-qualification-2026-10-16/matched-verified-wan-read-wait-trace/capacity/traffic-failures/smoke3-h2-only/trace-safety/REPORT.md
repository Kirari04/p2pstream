# H2 smoke3 execution-trace export-safety review

Verdict: the three closed H2 raw Go execution traces are acceptable for the
diagnostic evidence export when accompanied by their exact hashes, reduction
index, original analyzer/parser statuses, independent partition report, and
this safety report. The original run was read only.

| Sequence | Raw bytes | Raw SHA-256 | Parser | Original analyzer |
|---:|---:|---|---:|---:|
| 9 | 233,691 | `f002fca480f697b0acf6027cdbdff50353af36b94ac2903cd6679ab3db65c9aa` | 0 | 1 |
| 11 | 238,665 | `097446f00dfc8bdaee6f50ac862adea4dcb8ef5cc78fba663798975a0182bddb` | 0 | 1 |
| 13 | 190,193 | `5c1210d27747dd6c9b89e963e335757150fc5afa021a643b25b69381620039a8` | 0 | 0 |

Pinned Go 1.26.6 `go tool trace -d=wire` returned status `0` with empty stderr
for each trace. Raw, parsed, and original-reduction hashes match index
`92d33ce0540671ca168d3f8a978fead362a85509ea65c2bae4e762900ccbbe20`.
Each parsed trace has exactly one task pair named `wan-read-wait-request`, one
region pair named `public-proxy-handler`, and zero user-log events. The wire
string inventories contain the fixed task/region names, no NUL strings, and no
64-character lowercase hexadecimal values.

The nonprinting scan loaded the closed-run agent token, public certificate, and
private key only in memory. It found no token or bearer form; PEM, DER, or
base64 key/certificate form; authorization/diagnostic header name; public
host/path; or payload marker in raw, wire, parsed, or reduction artifacts. No
secret value, fragment, length, or secret-derived hash was emitted.

The generated trace-control bearer was process-local and was not persisted, so
exact decoded-byte comparison remains unavailable. Zero user logs, the exact
annotation inventory, full wire-string inventory, and absence of bearer-shaped
ASCII hex are supporting controls, not proof against an arbitrary binary
encoding of that unavailable value. This is the same explicit ephemeral-bearer
limitation as the H1 review.

Raw traces disclose whole-process scheduler/timing events, Go IDs, threads,
processors, GC/heap metrics, function names, absolute source/build/module-cache
paths, and trace wall-clock synchronization. Full parsed traces and wire dumps
are review intermediates and should be omitted from the evidence export.

Original analyzer statuses remain authoritative: sequences 9 and 11 are
rejected by the original zero-PC handling and sequence 13 is accepted. The
separate corrected offline reductions accepted all three. Export safety and
the independent state partition do not replace either status record and do not
establish a performance cause.

Machine report: `export-safety-report.json`, SHA-256
`583e55978f580b10fb778683a4f131d18b77a5980087899ea63908b99ad9ad62`.

