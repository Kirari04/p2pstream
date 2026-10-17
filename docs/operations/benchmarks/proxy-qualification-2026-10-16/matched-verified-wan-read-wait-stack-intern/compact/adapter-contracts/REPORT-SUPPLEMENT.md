# Adapter lifecycle report supplement

This supplement preserves `REPORT.md` and `FINAL-SHA256SUMS.txt` unchanged and
clarifies the complete test-only monkeypatch surface used by attempt 1.

The driver replaced `subprocess.Popen` with tiny fixture children. For the two
spawn-registration signal cases it also wrapped `fresh_write`; the wrapper
sent a second `SIGTERM` immediately before the first publication and then
called the frozen adapter's original `fresh_write` with the original path and
payload. This exercised the adapter's post-cleanup ignored-signal publication
window without changing publication behavior.

The raw-size negative case set test-only `MAX_RAW_TRACE_BYTES=16` and supplied
a 17-byte raw file. The pinned-tool negative case set test-only
`PINNED_GO_SHA256` to 64 zeroes. These mutations were confined to each imported
test process. They prove the corresponding adapter gates reject before child
spawn; they do not validate the production 64 MiB value or production Go hash
by substitution. The run-all preflight independently bound the unmodified
checkpoint 7 adapter and checkpoint manifest before starting any case.

All other overrides and their limits remain as recorded in `REPORT.md`. The
accepted result is limited to these explicit adapter lifecycle mocks.
