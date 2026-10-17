# Owned close-race reproduction invocation

Frozen invocation for the reviewed close-race-reproduction-source-1 Go overlay against immutable audited build3 source. It reuses the accepted owned-process helper and source-inventory authority, verifies the reproduction manifest and focused SOURCE-CHAIN inputs plus pinned Go/Python/compiler tools before and after, then runs only the named cancellation test in normal mode followed by race mode. Each mode must report exactly the root and two reviewed subcases once, with natural direct/group status0 and no skipped/failed/unexpected tests. Failure stops the sequence; no retry.

GOMAXPROCS=2, -p=1, count1, 60-second Go test deadline, and a600-second owned outer compile/test deadline per mode. Each mode uses a fresh cache and temporary directory under the fresh euid-owned0700 ext4 result root; existing module cache is read with module downloads disabled. Records and caches remain separate, and the evidence ledger covers records only. No source copy, endpoint binary build, WAN runner, or runtime shutdown change. The test has its disclosed ephemeral loopback certificate fixture and uses net.Pipe for tested transport.

Proposed command after source approval and explicit sole-slot grant, from the authorized worktree:

`/usr/bin/python3.14 -I -B tmp/continuation-20261016/wan-tcp-threshold-history-source/review/close-race-reproduction-invocation-source-1/run-reproduction.py tmp/continuation-20261016/wan-tcp-threshold-history-source/runner-source-1/runs/close-race-reproduction-attempt-1`

No execution has occurred from this invocation. A passing reproduction supports its two bounded synthetic schedules and does not reclassify either failed tiny run or establish WAN scheduling causality.
