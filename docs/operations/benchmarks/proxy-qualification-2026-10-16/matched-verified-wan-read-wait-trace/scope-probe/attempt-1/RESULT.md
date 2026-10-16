# Scope-only prerequisite probe: failed attempt 1

Start UTC: `2026-10-16T18:38:46.304055669Z`  
End UTC: `2026-10-16T18:38:46.582964139Z`

This was the sole authorized attempt. It was not retried.

The owned outer namespace boundary succeeded: its shell was PID 1, the metadata
writer's parent was PID 1, and user, PID, mount, and network namespace inodes
all differed from the parent. The outer launcher exited and its process group
joined naturally. No timeout or signal cleanup occurred.

The scoped dummy did not start. `systemd-run --user --scope` returned:

```
Failed to connect to user scope bus via local transport: No data available
```

The launcher disappeared before the identity recorder could read `/proc/3/stat`.
Consequently no scoped dummy, cgroup membership, memory limit/event snapshot,
natural dummy exit, or unit-join evidence exists. The final host-side unit read
reported `LoadState=not-found`, `ActiveState=inactive`, and `SubState=dead`, so
there is no residual transient unit.

Statuses:

- caller: `1`
- owned outer wrapper: `1`
- forced cleanup: `false`
- attestation accepted: `false`
- final host unit query: `0` (successful not-found/inactive observation)

This attempt rejects invoking the user scope client from inside this private
user namespace as the prerequisite route. It does not test or reject the 4 GiB
scope limits themselves.
