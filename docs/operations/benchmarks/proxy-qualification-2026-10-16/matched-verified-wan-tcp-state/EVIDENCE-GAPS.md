# TCP-state diagnostic evidence gaps

This qualification-only observer does not change production runtime behavior,
flow-control policy, transfer scheduling, TLS, proxy topology, or acceptance
floors. The generated workload runner and timed transfer body remain unchanged.

The accepted tiny run proves bounded authenticated transport, schema and join
validation, provider availability, protocol/certificate identity, exact
five-owner terminal closure and natural cleanup for 4 MiB transfers. It is not
throughput evidence and does not clear the existing WAN blocker.

TCP snapshots are discrete synchronous samples. Cursor state may progress
between the existing byte-credit record and the TCP sample; the validator keeps
that progress visible rather than claiming atomic equality. Provider capture
duration covers only the synchronous TCP information query, not cursor lookup,
owner locking, copying, serialization, delivery, or end-to-end delay. Clean
local samples do not exclude unsampled transients or public write/inter-read
feedback amplification.

The sustained process closed with native status 1 because the overall cohort
was not accepted. H1 was observational-complete and passed its fixed floors and
reuse checks. H2 failed retained-warmup and steady floors and is inconclusive
because sequences 15 and 16 did not retain origin reuse after roughly 35
seconds. Conditioning-clean H2 sequence 14 is valid observational evidence but
its 16.810103 MB/s result does not isolate cause. Sampled retransmission deltas
also prevent a categorical no-loss claim. First-wait slow-start thresholds were
466/462/460 for selected H1 sequences 9/11/14 versus 240/246/246 for H2. H2
sequences 9/11 failed with zero cumulative retransmission delta, while H1
sequence 9 passed despite retransmission. These discrepancies do not establish
the threshold as the sole cause. The complete cohort remains observational and
does not establish a production fix, parity, release, or merge readiness.

The source review narrows possible creators of the retained finite slow-start
threshold but does not identify one atomically. Namespace HyStart counters are
shared rather than per socket, and the proposed transition discriminator is
future source work rather than execution authority.
