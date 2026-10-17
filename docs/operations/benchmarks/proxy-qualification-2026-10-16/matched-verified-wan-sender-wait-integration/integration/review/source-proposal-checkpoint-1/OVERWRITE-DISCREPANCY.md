# Source proposal checkpoint 1 overwrite discrepancy

The top-level proposal was called frozen and reported before a later precision
edit overwrote `DESIGN-PROPOSAL.md` and regenerated `SHA256SUMS.txt`. No
separate copy of the original bytes was preserved. They are not reconstructed
here.

The exact hashes reported by the successful tool call before the overwrite
were:

```
d6de1a22993334419ec89aaa1b0e731a68603e6ab77bd47dc0e60c675bbc0a9d  DESIGN-PROPOSAL.md
098b6b0e2be30e8fefd2b93548ec57e98f612a52a96774865b76bb6d7765df32  INPUT-SHA256SUMS.txt
01d479e24a263a6994ce6bbe29c1feed7a59b2bff5f8207a952eb877accfda6b  PROSPECTIVE-CHANGES.txt
ff951115fb3adf1308f31f26a96bc9b187e6041007aecf084e60e02b4b5c1dc0  SHA256SUMS.txt
2b0f8ef3a322da3438099a7ea50db5ad7895e179c543b3b541ecc1df89e14ccb  STATUS.txt
```

The same call ran `sha256sum -c` successfully for all four manifest-bound
files and all twelve input files. This note records only that observed tool
evidence; it does not claim that the missing original bytes were recovered.

The exact semantic edit made afterward is preserved in
`../source-proposal-checkpoint-2/DESIGN-DELTA-FROM-RECORDED-CHECKPOINT-1.diff`.
