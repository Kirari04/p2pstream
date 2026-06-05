# Build a Multi-Agent Backend Pool

Spread one public backend across multiple remote agents with weighted load balancing and per-agent health behavior.

## Use This When

Use an agent pool when multiple hosts can reach the same upstream, or when you want backup remote paths for one public service.

## Prerequisites

- At least two registered agents.
- Each agent is installed, enabled, and connected.
- Each agent host can reach the backend target origin.

## Steps

1. Open **Agents** and create one agent per host.

   | Agent | Purpose |
   | --- | --- |
   | `home-lab-a` | primary home lab host |
   | `home-lab-b` | secondary host |
   | `workshop` | remote workshop network |

2. Install each agent with its generated setup command and wait until each shows connected.

3. Open **Proxy** and create the backend:

   | Field | Value |
   | --- | --- |
   | Name | `media-pool` |
   | Type | Proxy forward |
   | Forward mode | Agent pool |
   | Target origin | `http://media.local:8096` |
   | Load balancing | Weighted least active requests |
   | Enabled | On |

4. Add agent assignments:

   | Agent | Weight | Enabled |
   | --- | --- | --- |
   | `home-lab-a` | `200` | On |
   | `home-lab-b` | `100` | On |
   | `workshop` | `50` | On |

   Weights must be from `1` to `1000`. Keep at least one enabled assignment on an enabled agent-pool backend.

5. Choose a balancing policy:

   | Policy | Best for |
   | --- | --- |
   | Round-robin | Simple equal rotation. |
   | Weighted round-robin | Predictable split based on capacity. |
   | Random | Low coordination, rough spread. |
   | Weighted random | Rough spread with capacity weighting. |
   | Least active requests | Long-running requests or uneven response times. |
   | Weighted least active requests | Capacity-aware long-running requests. |

## Runtime Effects

For each matching request, p2pstream selects an enabled connected assigned agent using the backend load-balancing policy, sends the request over the agent WebSocket, and the agent connects to the target origin from its own network.

If health checks are enabled, checks run through each enabled assigned connected agent. A loopback origin such as `http://127.0.0.1:8888` means loopback on each agent host.

Agent-pool backends enforce the backend response-header timeout on the selected agent. The default is `60000` ms. Older agents that do not understand the timeout metadata keep their built-in `30000` ms timeout until upgraded.

Agent WebSockets use periodic heartbeat pings. The default ping interval is 20 seconds with a 10 second ping timeout. When management is behind another reverse proxy, allow WebSocket upgrades and configure that proxy's idle timeout above the heartbeat interval so connected agents are not dropped while idle.

<figure class="doc-screenshot">
  <img src="../assets/new/backend_agent_healthcheck_logs.png" alt="p2pstream agent-pool backend health panel showing assigned agents, health state, active requests, and health-check log entries">
  <figcaption>The backend health panel is the fastest place to confirm whether each assigned agent is connected, enabled, healthy, and able to reach the target origin.</figcaption>
</figure>

## Verification

Send repeated requests and inspect **Overview -> Hotspots -> Agents** or **Traffic** tracing to confirm traffic moves across the expected agents. Use **Agents** to check each agent's current uptime or offline duration, retention-window uptime percentage, connection and disconnect counts, and recent connection sessions.

## Troubleshooting

| Symptom | Check |
| --- | --- |
| One agent receives too much traffic | Review load-balancing policy and assignment weights. |
| Requests fail from one site | Test the target origin from that agent host. |
| Agent is skipped | Confirm it is enabled, connected, assigned, and healthy when health checks are on. |
| Agent disconnects while idle | Check management reverse-proxy WebSocket support and idle timeout. |
| Long first-byte delay times out | Increase backend response-header timeout and upgrade agents. |

## Next Steps

- [Backends](../concepts/backends)
- [Agents](../concepts/agents)
- [Trace live traffic](./trace-live-traffic)
