# Sustained H1 independent compact partition audit

The sole independent audit execution returned status 0. It consumed the four
closed H1 canonical streams for sequences 9, 11, 14, and 15 through the exact
reviewed `CanonicalReader` and did not parse traces during traffic. H2 was not
started by the primary workload.

The audit source SHA-256 is
`00e8f76014037e1ba6ea1518860fd1f743080455a61875da2d0615e2eb9b273c`;
the reviewed format module SHA-256 is
`f370243b753a94a122157e9b0b13eb535065fe88746269bd70de5475d82e4dbf`.
The input index SHA-256 is
`43e4f9dc7be2dda55b7e388c2dbc951a2ff8f87a4b0b7ac758f4dcb152863dc4`.
`audit.json` SHA-256 is
`b8d6fd60e3d5ce66613b439d3197835ecfa124e986aee61af636d83de7053ab7`.

All four independently computed marker task IDs, handler GIDs, marker
timestamps and ordinals, region durations, partition sums, and exact
Running/Runnable/Syscall/Waiting totals equal the primary reductions:

| Sequence | Handler GID | Duration ns | Running ns | Runnable ns | Syscall ns | Waiting ns |
|---:|---:|---:|---:|---:|---:|---:|
| 9 | 3750 | 16,834,737,728 | 208,194,728 | 105,559,432 | 238,305,189 | 16,282,678,379 |
| 11 | 5162 | 8,806,179,136 | 171,700,684 | 60,945,528 | 206,019,593 | 8,367,513,331 |
| 14 | 6550 | 8,775,656,576 | 177,644,021 | 61,711,107 | 214,182,552 | 8,322,118,896 |
| 15 | 7064 | 7,634,729,216 | 161,622,125 | 47,243,329 | 197,924,695 | 7,227,939,067 |

The first comparison attempt is preserved with status 1. Its requested marker,
duration, partition-sum, and state-total fields all matched, but its driver
also required equality of handler transition counts with different scopes: the
independent audit counts handler transitions across the whole trace, while the
primary reports its own narrower analysis count. The mechanical v2 records
those counts descriptively and returned status 0. Its JSON SHA-256 is
`e37068d053429df0594d23d12f220590bbe3d7a23c1b004453258331b875f049`.

The bounded missing-wait context extraction returned status 0. It found the
same positive missing-stack totals as the primary: sequence 9 has 16 intervals
and 12,032 ns; sequence 11 has 8 and 4,224 ns; sequence 14 has 8 and 7,168 ns;
sequence 15 has 7 and 3,392 ns. Every event is a handler-resource
`Waiting->Waiting Reason=""` transition without `TransitionStack`, immediately
followed by a same-resource `Waiting->Runnable` transition. Thirty-four of 39
are immediately preceded by another resource's `Syscall->Running` event; the
remaining five are immediately preceded by a metric event. Exact prior,
current, next-global, and next-handler headers and times are retained in
`missing-wait-context.json`, SHA-256
`542c61cadaa212f20c5c352b489163be31f09caa91390311c4f0ef9539ac67c3`.
This event adjacency is an observation and does not establish runtime cause.

The partition logic is independent above decoding. It shares the compact
decoder with the primary analyzer and does not independently verify the
canonical format, dictionary expansion, or footer implementation. The
primary analyzer statuses remain 1 because every sequence has positive
waiting duration without a transition stack; this audit does not clear that
gate.
