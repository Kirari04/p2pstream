# Kernel idle-policy scope assessment — 2026-10-17

A private-agent-namespace `tcp_slow_start_after_idle` contrast is source-supported as a diagnostic of **both idle restart and application-limited cwnd validation**. It cannot isolate the agent's four bulk data sockets or the idle restart function alone. Root/Astra owns the prospective causal design; no sysctl, source, test, build or traffic was changed by this review.

## Kernel identity and source boundary

Fresh read-only commands report running release `7.0.0-38-generic`; installed common/generic headers are version `7.0.0-38.38`, source package `linux` version `7.0.0-38.38`. The installed image is version `7.0.0-38.38`, source package `linux-signed` of that version. Headers expose the exact running-package `tcp_slow_start_after_idle_check` guard at `/usr/src/linux-headers-7.0.0-38/include/net/tcp.h:1660-1671`, and the per-network-namespace `u8` field at `include/net/netns/ipv4.h:209`.

The retained earlier evidence is `tmp/continuation-20261016/wan-tcp-state-source/provenance/sustained-tcp-state-1-kernel-source-evidence-1/`. It holds exact upstream v7.0 TCP files, Ubuntu's relevant diff and the official source description; its published review binds Ubuntu source version/tag and explains that the relevant threshold changes are access wrappers. The [Ubuntu source description](https://launchpad.net/ubuntu/+archive/primary/+sourcefiles/linux/7.0.0-38.38/linux_7.0.0-38.38.dsc) and [distribution diff](https://launchpad.net/ubuntu/+archive/primary/+sourcefiles/linux/7.0.0-38.38/linux_7.0.0-38.38.diff.gz) are the authoritative package inputs. I did not redownload/rebuild the full Ubuntu source or prove compiled-kernel binary equivalence. Newly checked upstream sysctl/default files are version-pinned v7.0, not a claim that every unrelated Ubuntu source line is identical.

## Mechanism, coordinated with Astra

The local header agrees with [upstream tcp.h:1659-1670](https://github.com/torvalds/linux/blob/v7.0/include/net/tcp.h#L1659-L1670): the check reads the socket's network namespace setting and requires no packets out, no `cong_control` implementation, and elapsed send idle exceeding RTO. [tcp_output.c:166-181](https://github.com/torvalds/linux/blob/v7.0/net/ipv4/tcp_output.c#L166-L181) halves cwnd toward the restart window, retains/nondecreases the current threshold through `tcp_current_ssthresh`, and emits `CA_EVENT_CWND_RESTART`. The same setting also gates application-limited reduction at [tcp_output.c:2165-2168](https://github.com/torvalds/linux/blob/v7.0/net/ipv4/tcp_output.c#L2165-L2168). Disabling it therefore changes both paths. It does not directly clear or create the finite slow-start threshold.

[CUBIC's event handler:142-160](https://github.com/torvalds/linux/blob/v7.0/net/ipv4/tcp_cubic.c#L142-L160) handles `CA_EVENT_TX_START` by shifting its epoch after idle; it has no separate `CA_EVENT_CWND_RESTART` action. Removing the generic restart does not remove that TX-start epoch behavior or CUBIC's other startup/recovery logic. Astra separately owns the complete mechanism/causal assessment.

[sysctl_net_ipv4.c:1243-1247](https://github.com/torvalds/linux/blob/v7.0/net/ipv4/sysctl_net_ipv4.c#L1243-L1247) binds the writable entry to `struct net`; its namespace registration rebases each data pointer into the new namespace at [1647-1665](https://github.com/torvalds/linux/blob/v7.0/net/ipv4/sysctl_net_ipv4.c#L1647-L1665). [tcp_ipv4.c:3544](https://github.com/torvalds/linux/blob/v7.0/net/ipv4/tcp_ipv4.c#L3544) initializes each namespace's setting to 1. Readback of the actual disposable namespace is still required; the host value is not a substitute.

## Actual fixture scope and sender direction

References below are to `tmp/wan-controller-screen-20261017/runner-source/integration/generated/qualify-proxy-agent-growth-wan-below-tls.sh` unless stated otherwise.

The runner/server/load namespace is already disposable and distinct from its parent. It creates private agent and router namespace holders as independent owned groups (`436-460`); validates boot ID, PID start ticks and namespace identity (`408-429,469-478`); and `agent_ns` revalidates the live holder before entering it (`479`). Addresses are server `10.211.0.1`, router `10.211.0.2`/`10.212.0.1`, agent `10.212.0.2` (`484-498`). The origin launches in the **same agent namespace**, binding `10.212.0.2` (`883-891`). The agent launches there too (`1766-1784`).

| Socket/traffic | Agent-netns setting changes local sender behavior? | Scope implication |
|---|---|---|
| Agent→server bulk tunnel DATA, public downloads | Yes, agent endpoints on lanes 1–4 | Directly covers the sender that carries the slow download body. |
| Server→agent bulk tunnel DATA, public uploads | No for server sender; yes for agent's opposite-direction traffic | Upload tunnel bulk congestion state stays under server namespace policy, but upload is still an affected-workload guard. |
| Tunnel lane 0 and management connections | Yes for agent-side sends | Namespace intervention cannot preserve a different lane-0 idle policy. |
| Agent↔origin TCP, same namespace/local address | Yes for **both** local endpoints | Changes origin download sending and agent upload relay sending as well; cannot label result data-tunnel-only. |
| Agent/origin admin and observation TCP | Yes for processes/sockets entered into that namespace | Possible conditioning/scheduling effects remain part of the intervention. |
| Public H2 server↔load TCP | No | Both remain in runner/server namespace. |
| Router forwarding | No agent policy applied to forwarded sockets | Router retains its own namespace; it is not the TCP endpoint. |

Setting the value once before endpoints start and retaining it through all sixteen transfers tests the whole history, including conditioning and direction reversal. Setting it only before selected retained downloads would be a different intervention and would not address history already created during warmups. A private namespace confines persistence: the fixture joins origin and namespace holders (`1873-1876`) after joining endpoints, and the owned-group cleanup rejects forced/unfinished lifecycle (`310-395`). Namespace death removes its setting; no host restoration should be needed if namespace targeting is correct. Existing identity guards should be retained before any prospective setter/readback.

## Existing setup/readbacks and missing expectation

The generated fixture currently sets only router `net.ipv4.ip_forward=1` (`499`); no source setter for this idle knob was found in the runner. It does not explicitly force or read back 1 before endpoint startup.

The accepted agent observer already reads `/proc/sys/net/ipv4/tcp_slow_start_after_idle` strictly as 0 or 1: `test-source/internal/agent/qualification_tcp_state_qualification.go:15-47`. `qualification_raw_write_qualification.go:353` reads it at observer setup; `:282-294` reads again at terminal publication and rejects mismatch/read error. `qualification_tcp_state_qualification.go:214,708` stores and later emits the **cached setup value** in each owner snapshot. Those snapshot fields are not repeated live sysctl reads. Existing `validate-raw-write-activity.py:339-345,421-422` accepts either 0 or 1, requires setup/terminal equality and owner consistency; it does not independently enforce a planned arm value.

A later reviewed derivative therefore needs an explicit exact-enum expected value bound to each arm and independently checked against real agent-namespace readback and existing setup/terminal values. It should retain an unchanged runner/server-namespace value check to substantiate the directional boundary. This is source scope advice, not authorization to edit, execute or expand capture.

## Interpretation and adverse-effect limits

A positive all-CUBIC 1/0/1 contrast would concern this namespace-wide idle/application-limited cwnd policy, including its influence during conditioning; it would not uniquely identify HyStart, prove which of the two guarded paths mattered, or identify the original finite-threshold creator. A negative contrast would not rule out TCP history generally. Retaining a large prior cwnd after idle can produce larger bursts when current path capacity differs, changing queueing/loss and competition. Those effects and the origin/control socket scope are reasons to preserve all transfer, payload, origin-reuse, resource and natural-cleanup outcomes, rather than interpreting selected download improvements alone. No production tuning recommendation follows.
