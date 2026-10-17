# Numerical scope erratum

This append-only erratum corrects one sentence in `sustained-byte-credit-1-source-feedback-review-1/REPORT.md`. The original report remains unchanged.

The statement that receiver eligibility-to-send-completion was no more than 264 microseconds overall and 209 microseconds on clean sequences conflated scopes.

The exact scopes are:

- Across the four clean rows used for cross-arm comparison (H1/H2 sequences 9 and 11), receiver eligibility-to-completion has a 264.0 microsecond maximum. The clean H1 maximum is 264.0 microseconds (H1 sequence 9); the clean H2 maximum is 208.5 microseconds (H2 sequence 9).
- Across all eight selected rows, including conditioning-mixed rows, the receiver maximum is 315.1 microseconds (H1 sequence 14).
- Across the four clean rows, peer handler-to-notify has a 17.3 microsecond maximum (H2 sequence 9). Across all eight selected rows, the maximum is 26.8 microseconds (conditioning-mixed H2 sequence 14).

The source/path analysis and observability gap are unchanged. The retained data does not establish that public-write feedback is the dominant cause, nor does it rule that feedback out as dominant; it shows sub-millisecond individual public writes and bounds transfer-wide feedback without providing an exact per-wait interval partition.
