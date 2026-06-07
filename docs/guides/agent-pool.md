# Build a Multi-Agent Target

Spread one public route target across multiple remote agents with label selectors, weighted load balancing, and per-agent health behavior.

## Use This When

Use an agent-selected target when multiple hosts can reach the same upstream, or when you want backup remote paths for one public service.

## Prerequisites

- At least two registered agents.
- Each agent is installed, enabled, and connected.
- Each agent host can reach the target URL.

## Steps

1. Open **Agents** and create one agent per host.

   | Agent | Suggested label |
   | --- | --- |
   | `home-lab-a` | `site=home-lab` |
   | `home-lab-b` | `site=home-lab` |
   | `workshop` | `site=workshop` |

2. Install each agent with its generated setup command and wait until each shows connected.

3. Edit each agent and add user labels. Labels under `p2pstream.io/` are reserved for p2pstream and are shown read-only, but labels such as `site=home-lab`, `region=eu`, or `capacity=large` are operator-owned. Empty label values are allowed, but they should be intentional because they only match empty selector values.

4. Create or edit a forward route and add an agent proxy target:

   | Field | Value |
   | --- | --- |
   | Name | `media-home-lab` |
   | Type | Proxy |
   | Transport | Agent |
   | URL | `http://media.local:8096` |
   | Agent selector | `site=home-lab` |
   | Agent load balancing | Weighted least active requests |
   | Priority group | `0` |
   | Weight | `100` |
   | Enabled | On |

5. Add another target with a higher priority group for failover, if needed:

   | Field | Value |
   | --- | --- |
   | Name | `media-workshop-fallback` |
   | Transport | Agent |
   | Agent selector | `site=workshop` |
   | Priority group | `1` |

Targets in priority group `1` are only used if every eligible target in group `0` is unavailable. Agent selectors require at least one label. When you add multiple selector labels, every label must match the same agent. For exact-agent targeting, use the system label `p2pstream.io/agent-id=<agent public ID>`.

## Runtime Effects

For each matching request, p2pstream selects an enabled healthy target from the lowest available priority group. For an agent target, it then selects an enabled connected agent whose labels match the target selector, opens a Yamux stream over that agent's authenticated management tunnel, and runs the server-owned HTTP transport over the resulting TCP stream.

If health checks are enabled, checks run through each matching connected agent. A loopback origin such as `http://127.0.0.1:8888` means loopback on each selected agent host.

Agent targets enforce the response-header timeout in the server-side transport. The default is `60000` ms.

Agent tunnels use Yamux keepalives over an HTTP/1.1 upgraded management connection. When management is behind another reverse proxy, allow upgrade streaming for `p2pstream-yamux` on `/agent/tunnel` and configure that proxy's idle timeout high enough for long-lived agent sessions.

Old WebSocket agents are incompatible with Yamux-tunnel servers. Upgrade agents and servers together.

## Verification

Send repeated requests and inspect **Overview -> Hotspots -> Agents** or **Traffic** tracing to confirm traffic moves across the expected agents. Use **Agents** to check each agent's labels, current uptime, offline duration, connection and disconnect counts, and recent sessions.

## Troubleshooting

| Symptom | Check |
| --- | --- |
| One agent receives too much traffic | Review target agent load-balancing policy and labels. |
| Requests fail from one site | Test the target URL from that agent host. |
| Agent is skipped | Confirm it is enabled, connected, label-matched, and healthy when health checks are on. |
| Agent disconnects while idle | Check management reverse-proxy HTTP/1.1 upgrade support and idle timeout for `p2pstream-yamux`. |
| Long first-byte delay times out | Increase target response-header timeout. |

## Next Steps

- [Route targets](../concepts/backends)
- [Agents](../concepts/agents)
- [Trace live traffic](./trace-live-traffic)
