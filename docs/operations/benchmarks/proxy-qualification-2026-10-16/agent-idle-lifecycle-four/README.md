# Four-agent idle-stream lifecycle

This trace repeats the constrained 1,536-H1 capacity and idle cleanup workload
with four external agents. Candidate `73adc8a4898b85b52decf988112bbfc36e9fa68f`
contains protocol checkpoint `d385925`. The server and each agent ran in a
distinct 4 GiB cgroup, for a configured envelope of 20 GiB across five
processes; the server carried a 1 GiB ballast. This is not evidence that the
five processes fit in one 4 GiB deployment.

The capacity row completed 1,536/1,536 useful responses with no errors or
admission rejections and reached an exact origin high-water of 1,536. Sampled
owner high-water was 1,536 public connections, 1,536 public requests and 20
physical receive-window sessions. The server peaked at 1,328,562,176 bytes of
cgroup memory, 1,338,130,432 bytes RSS and 5,201,920 bytes of cgroup socket
memory. All server and agent cgroup event files were readable and recorded no
OOM or OOM-kill delta. The 512-request recovery row also completed without an
error and conserved its request events.

Each agent retained 384 reusable logical streams after the public wave, 96 on
each of its four bulk lanes. At one and five seconds, agent `InUse` remained
384 for all four agents. By 30 seconds every agent `InUse` value and all 16
server bulk-lane stream counts were zero; they stayed zero at 35 and 65
seconds. All four control and 16 bulk sessions remained connected, and the 20
server receive-window owners remained at the physical-session baseline.

A subsequent 128-request repopulation followed by explicit idle-origin
transport closure also had zero agent ownership and zero bulk stream counts at
the first sample and through 35 seconds, without session teardown. Logical
ownership and cgroup socket memory recovered, but process RSS did not contract:
server RSS rose from about 1.34 to 1.37 GB, and agent RSS also rose across the
trace. This result therefore does not claim immediate Go heap return to the
kernel.

`results.jsonl` preserves the candidate and HAProxy rows plus every lifecycle
sample. `summary.json` extracts the candidate capacity, recovery and cleanup
trace. `manifest.json` records the exact source, binaries, runner and workload.
