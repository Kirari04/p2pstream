# Agent relay cancellation joins

The relay previously returned immediately on session-context cancellation after
closing its connections. Yamux Close is a half-close, so either copy goroutine
could still be running when its caller continued resource cleanup.

The retained fix sets the stream deadline before cancellation Close and joins
both copy results before returning the context error and completed byte totals.
Cancellation while the normal first-result path waits for its remaining copy
uses the same join. Normal completion keeps the existing error selection; no
additional always-running callback, timer or goroutine is introduced.

Four focused tests cover delayed copy completion after Close/deadline, releasing
only one of two completion barriers, cancellation after the first copy result,
normal error selection, and cancellation of a real TCP-backed Yamux relay after
bidirectional forwarding. The context-error checks exercise both Canceled and
DeadlineExceeded. Fixture cleanup has bounded waits and closes its connections.

`validation.json` records the exact source hashes, pinned Go 1.26.6 executable,
common environment, commands and log hashes. All temporary build paths were on
the owned ext4 worktree before the first compile. Focused normal checks passed
10 repetitions and race checks passed 5; the complete agent and tunnel suites
also passed once normally and once with race detection.

The source was validated over parent `56a455e97b13f33b7293f2a2b753afd6da081cea`.
The separate transport-ceiling runner was untracked and not part of these Go
packages. A preliminary host `go env GOPATH` metadata lookup performed no build
or test; all four recorded test commands used the pinned executable.

This fixes session shutdown ownership. It is not a measured throughput change,
a public-request cancellation exploit claim, or a resolution of the repeated
WAN download acceptance failure.
