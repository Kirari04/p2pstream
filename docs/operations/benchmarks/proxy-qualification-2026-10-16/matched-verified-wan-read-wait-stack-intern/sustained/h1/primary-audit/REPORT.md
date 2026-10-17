# Sustained matched-protocol attempt 1 offline audit

- Run: `/home/user/.codex/worktrees/astra-proxy-performance/p2pstream/tmp/continuation-20261016/wan-read-wait-stack-intern-source/integration-source/runs/sustained-matched-protocol-1`
- Accepted: **false**; H1 status 1; H2 not started.
- H1 rows: 16; pre/post source and binary hashes equal: True.

## Performance and reuse

- Warmup download range: 33831365.847–34100322.893 B/s.
- Retained-warmup sequence 9: 15780557.495 B/s, below the 29,700,000 B/s diagnostic floor.
- Steady downloads: seq 11 29883939.197 B/s, seq 14 29979696.421 B/s, seq 15 34361498.165 B/s.
- All same-key revisits sequences 13–16 retained exact origin identity and were reused/idle at 25.787–26.142 s, below 30 s.

## Trace reductions

- Sequence 9: raw 2694361 B; canonical 60641119 B; expanded 906836962 B; adapter/parser/canonicalizer 0; analyzer 1; missing stack 12032 ns; partition delta 0 ns.
- Sequence 11: raw 2142077 B; canonical 54874184 B; expanded 847095155 B; adapter/parser/canonicalizer 0; analyzer 1; missing stack 4224 ns; partition delta 0 ns.
- Sequence 14: raw 2184944 B; canonical 55126816 B; expanded 851406311 B; adapter/parser/canonicalizer 0; analyzer 1; missing stack 7168 ns; partition delta 0 ns.
- Sequence 15: raw 2057661 B; canonical 54585021 B; expanded 847056641 B; adapter/parser/canonicalizer 0; analyzer 1; missing stack 3392 ns; partition delta 0 ns.

Every reducer rejected `positive waiting interval has no TransitionStack`; no classification was relaxed.

## Lifecycle and resources

- Endpoint/namespace exit rows: 6, all status 0, unforced, groups joined.
- Outer supervisor: group joined True, forced False; unit `not-found/inactive/dead`.
- Server scope: 4 GiB, no swap, peak 235331584 B, memory events unchanged at zero.

The attestation field `outer_runner_group_joined=false` reflects a composite success gate that includes analyzer status. Raw supervisor and endpoint lifecycle evidence proves natural cleanup.

This is diagnostic evidence only. It cannot attribute the retained-warmup slowdown to tracing from one cohort.
