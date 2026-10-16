# Transport ceiling namespace-lifetime preflight

This is a topology-identity fixture preflight. It contains no endpoint process,
payload transfer, performance result, or transport-ceiling acceptance claim.

The final runner and wrapper are frozen here with their extracted ownership
shim, topology functions, and final topology analyzer. The actual preflight
created four fresh server/router/agent topologies in the preregistered
aggregate/raw/raw/aggregate order at the default 80 ms configuration. Each
generation was captured before teardown and appended only after all three
helpers were naturally joined and reaped.

All 12 helper process-lifetime keys `(boot_id, pid, start_ticks)` were unique.
Only eight network namespace inode values were unique: the kernel recycled
inodes after prior generations were joined. The lifetime-aware predicate
accepted that valid reuse while preserving three distinct network namespaces
within every live generation.

The pure analyzer contracts accept the actual and a deterministic modeled
post-join inode-recycle case. They reject a reused process key with a different
inode, an unjoined predecessor, a predecessor chronology mismatch, within-arm
namespace or process aliasing, runner-namespace aliasing, PID 1, and a Boolean
generation. These synthetic rates are inherited fixture data and have no
performance meaning even when the analyzer's diagnostic timing classification
is positive.

A source-derived disappearance negative killed one helper and proved the live
capture fails rather than substituting a new process. The structured
`/proc/PID/stat` contract maps only `FileNotFoundError` to status 3; it
propagates a generic status unchanged. Pre-TERM/live reads remain strict.
After validated TERM, status 3 only advances to the independent wait/reap and
process-group-gone proof.

The first four-arm attempt remains preserved separately and is classified as
orchestration-inconclusive. Its durable inner and top-level statuses were zero,
but the command tool reported one, and its pre-fix optional post-TERM reads
printed process-disappearance tracebacks. A no-workload observation reproduced
the tool-status behavior only when explicit `exit 0` ran with `errexit`
enabled; the evidence states this as observed `exec_command` behavior, not
general Bash semantics. The fresh final-source run ended naturally, returned
tool status zero, had empty stdout/stderr, and passed an independent predicate.
