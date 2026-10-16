# Cold-client held-request capacity observations

These finite measurements use the production public listener at immutable
`fb1cad92c9a814f598171fa181d1a4f230dbd107`, including the agent prepaid-control
fix. Server, agent and driver binaries were built from a clean archive. The
runner copies and hashes its committed inputs, then executes the frozen runner
for every point. No production code or host network configuration changed.

Each point starts fresh processes with a 1 GiB touched server ballast. The
direct fixture includes an idle colocated agent inside the same 4 GiB/no-swap
cgroup, with independent server and agent ledgers. The remote-agent fixture
uses separate server and agent 4 GiB/no-swap cgroups and four bulk lanes. Their
separately sampled peaks are not summed into a combined deployment claim.

The search starts at 2,048, advances to 4,096 after a pass, and refines a failed
point to 256-request resolution. It probes lower points when 2,048 fails and
requires an additional successful repeat of the highest observed pass. A pass
requires every request to be simultaneously inside the origin barrier, exact
request-owner occupancy, correct responses and HTTP version, no admission
misses, exact event persistence, readable zero OOM deltas and 512/512 subsequent
recovery requests. H1 also requires exact physical connection occupancy; H2
reports its physical connections separately from logical requests.

| Path | Confirmed held-request pass | Next observed failure | Failure's useful responses | Failure's origin peak | Failure's server cgroup peak |
| --- | ---: | ---: | ---: | ---: | ---: |
| direct H1 | 1,280, twice | 1,536 | 1,352 / 1,536 | 1,352 | 1,308,069,888 B |
| direct H2 | 2,048, twice | 2,304 | 2,083 / 2,304 | 2,083 | 1,408,294,912 B |
| remote-agent H1 | 1,792, twice | 2,048 | 2,048 / 2,048 | 1,871 | 1,397,006,336 B |
| remote-agent H2 | no confirmed upper point | 4,096 repeat unstable | 3,577 / 4,096 on repeat | 3,577 | 1,748,013,056 B |

Remote-agent H1 illustrates why client success alone does not establish held
capacity: all 2,048 responses eventually succeed, but the origin never holds
all 2,048 together and the server records 177 stream-pressure admission misses.
These counters include admission attempts and retries, not unique failed
requests. The remote agent itself reports no pressure/fixed-limit rejections.

Remote-agent H2 initially passes 2,048 and 4,096. Its 4,096 repeat fails, so the
runner exits 1 and leaves the matrix incomplete. **4,096 is not a qualified
capacity.** All 18 candidate capacity rows still conserve their events exactly,
record readable zero OOM/OOM-kill deltas, and complete 512/512 recovery requests
with exact events. No diagnostic event-count exception was needed.

## Cold H2 connection behavior

The client transport is cold at each point. At the passing 4,096 remote-H2
point, the public-connection owner peaks at 222 while only 26 TCP connections
are used by requests. The failed repeat peaks at 932 public connections while
only 21 carry requests. It records 519 client 503 responses, 49 public-accept
resource misses, nine server stream-pressure misses and 1,929 session-opening
limit misses. The last three are admission-attempt counters, not independent
counts of failed requests. The agent's rejection counters remain unchanged.

This difference is consistent with concurrent TLS dial attempts during cold
HTTP/2 discovery. Direct H2 also observes 665 public connection owners versus
18 request-used TCP connections at its 2,304 failure. These measurements cover
cold connection establishment and stream-opening bursts. They do not establish
steady multiplexed capacity, an exact maximum, or a monotonic boundary: some
higher failed points reach more concurrent origin requests than lower ones.
A separately labeled established-transport check is needed before attributing
the H2 limit to sustained memory capacity.

The failed rows remain well below actual 4 GiB cgroup pressure. For example,
direct H1 fails at about 30.5% actual usage. Its public connection lifetime and
direct-origin socket promises both consume the reservation budget. This is a
conservative direct-socket capacity limitation; it is not evidence that the
process physically exhausted memory, and it does not justify reducing parser
or socket promises without separate safety evidence.

## Evidence and scope

`manifest.json` identifies source, toolchain, topology and binary/runner hashes.
`matrix-summary.json` is the unmodified interrupted matrix decision.
`summary.json` retains every candidate capacity/recovery row and its decision.
`points/` includes every invocation, decision, JSONL row and runner log, including
the HAProxy controls. Those controls are direct host processes without matching
ballast or memory isolation; they are not an agent or memory-capacity parity
gate. Owner peaks can occur at different times and must not be summed as a
simultaneous peak.

The copied source files use `.txt` suffixes to keep saved fixtures out of Go
package discovery. `build-provenance.txt` records the clean build commands and
the unused build discarded before any load. `SHA256SUMS` covers the checked-in
evidence. These held-empty-response results do not qualify large headers,
large bodies, sustained throughput or broad modern-proxy concurrency parity.
