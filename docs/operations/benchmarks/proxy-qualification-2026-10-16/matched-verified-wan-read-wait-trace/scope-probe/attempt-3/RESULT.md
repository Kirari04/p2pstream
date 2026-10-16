# Corrected host-created delegated scope probe: accepted attempt 3

This was the single Astra-authorized corrected attempt. It completed without a
retry. The invoking shell and tool envelope both returned status 0.

The transient host-side scope enforced `MemoryMax=4294967296` and
`MemorySwapMax=0`. After the sentinel and `systemd-run` launcher exited and
joined naturally, the exact scope `cgroup.procs` contained one host PID. The
host supervisor bound that member through host `/proc` to the dummy's local PID,
start ticks, private user/PID/mount/network namespace identities, unchanged
cgroup namespace identity, nonce, role, and CPU affinity. All binding checks
passed.

The sentinel left before the measured interval. The dummy, outer namespace
launcher, sentinel, systemd launcher, their process groups, and the transient
unit all exited or joined naturally with status 0. No forced cleanup occurred.
The server-only snapshot recorded 1,179,648 bytes current and 6,029,312 bytes
peak against the 4 GiB limit, no swap allowance, and zero high/max/OOM events.
The persisted `attestation.json` is authoritative for acceptance.
