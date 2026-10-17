# Host-created delegated scope probe: failed attempt 2

This was the sole authorized attempt of the distinct host-created mechanism. It
was not retried.

## Established before failure

- The transient host-side scope was active with `Delegate=yes`,
  `MemoryMax=4294967296`, `EffectiveMemoryMax=4294967296`,
  `MemorySwapMax=0`, and `MemoryHigh=infinity`.
- The destination `cgroup.procs` and the nearest-common-ancestor
  `app.slice/cgroup.procs` were both UID/GID 1000, mode 0644, and writable to
  the host supervisor.
- The outer user, PID, mount, and network namespaces were private. Its cgroup
  namespace was deliberately unchanged.
- From the private namespaces, the dummy wrote `0` to the exact destination
  `cgroup.procs`. The write returned zero status and `/proc/self/cgroup`
  changed from the Codex scope to the exact delegated scope. Its namespace
  identities remained unchanged and it retained CPUs 0-3 and `GOMAXPROCS=4`.
- The sentinel and `systemd-run` launcher exited naturally with status 0; both
  identities and process groups joined.
- The pre-release memory event snapshot contained zero `high`, `max`, `oom`,
  `oom_kill`, and `oom_group_kill` events.

## Failure

The private `--mount-proc` reports PIDs relative to the private PID namespace,
so the dummy recorded `NSpid: 7` rather than a host-visible PID vector. The host
supervisor incorrectly compared that `7` with host-visible destination
`cgroup.procs`. It therefore did not accept the post-sentinel membership and
never released the dummy's measurement gate.

The dummy reached its finite internal deadline and exited with status 42. The
dummy identity and process group joined, and the transient unit subsequently
became not-found/inactive/dead with no residual matching process. No forced
cleanup was issued. The probe exited before it could persist `outer.status`,
the server-only cgroup snapshot, final unit polling, or an attestation.

The attempt proves that host-side scope creation and namespaced `write(0)`
migration work on this host. It does not satisfy the required post-sentinel
server-only interval or full natural lifecycle attestation, so the prerequisite
is not accepted.
