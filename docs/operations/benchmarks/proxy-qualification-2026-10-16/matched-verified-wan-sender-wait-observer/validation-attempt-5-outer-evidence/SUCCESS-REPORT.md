# Sender-duration observer validation attempt 5

The single authorized resumed validation completed successfully without an
endpoint or WAN workload.

## Provenance and status

- Frozen checkpoint: `review/source-checkpoint-10`
- Required and observed `SHA256SUMS.txt` SHA-256:
  `606931a1640153a7317eee7e3eeedd2d039d0720841e6bf62aaf22c65215b5a1`
- Whole-checkpoint verification immediately before and after the run: status 0
- Launcher SHA-256:
  `15f96c31c9d78522215e6764da5e2cb0e34cb41fa57c33b56fb2e7639ceabda6`
- Tool envelope, outer final, resumed driver, and source post-run gates: status 0
- Start: `2026-10-16T23:03:06.681289304Z`
- End: `2026-10-16T23:05:04.774085254Z`

The inner `CP8_RESUME_VALIDATION_FINAL` token is a stale label only. The
checkpoint-10 whole manifest and pre/post checks bind the executed source.

## Exact completed gates

- Extractor contract: status 0 with nine persistent cases. The two positive
  cases returned 0; duplicate marker, missing package pass, oversize, trailing
  fragment, wrong Output package, wrong run/pass package, and wrong direct
  status each returned exactly 1 without accepted outputs.
- Baseline layout: reused the immutable attempt-4 Go JSONL with SHA-256
  `b5a60bffd13f44c0f8da839ce63302442140f852af182428b5f8359d25a59b56`;
  its original direct Go status was 0 and its checkpoint-10 reduction was 0.
- Candidate layout: direct status 0 and audit status 0. Baseline and candidate
  canonical four-type layout hashes both equal
  `d2776d79609c7a3636942a9b997260e59eaaf0eb0c9fc048b244961ec13edf62`.
- Focused tests: 19/19 Yamux normal, 19/19 Yamux race, 2/2 agent schema,
  2/2 server schema, and 1/1 ordinary-layout tests passed with package-pass and
  exact-run audits.
- Baseline and candidate ordinary build, archive export, compiler assembly,
  and escape-analysis commands all returned 0. Both `nm` and both `objdump`
  commands returned 0.
- Ordinary proof accepted all six gates: `code`, `compiler`,
  `compiler_data`, `escape`, `layout`, and `nm`.
- Baseline and candidate post-run inventories and unchanged comparisons all
  returned 0.

The proof normalizes only whole-function load addresses/in-function targets,
source positions in compiler/escape records, and symbol addresses for `nm`.
It retains frames, PCDATA/FUNCDATA, referenced compiler data, complete selected
escape records and costs, exact symbol cardinality/sizes, and all four type
layouts.
