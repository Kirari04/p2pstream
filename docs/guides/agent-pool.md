# Build a Multi-Agent Target

Spread one public route target across multiple remote agents with label selectors, weighted load balancing, and per-agent health behavior.

## Use This When

Use an agent-selected target when multiple hosts can reach the same upstream, or when you want backup remote paths for one public service.

## Prerequisites

- At least two registered agents.
- Each agent is installed, enabled, and connected.
- Each agent host can reach the target URL.

## Steps

1. Open **Agents -> Fleet**, select **Add Agent**, and create one agent per host.

   | Agent | Suggested label |
   | --- | --- |
   | `home-lab-a` | `site=home-lab` |
   | `home-lab-b` | `site=home-lab` |
   | `workshop` | `site=workshop` |

2. In each **Agent Setup** modal, copy and run the generated setup command, then wait until each agent shows connected.

   <figure class="doc-screenshot">
     <img src="../assets/new/agents_page.png" alt="p2pstream Agents Fleet page showing fleet health and a searchable table of connected, offline, and disabled agents">
     <figcaption>The Fleet page is the pool inventory. Check connection state, uptime, active requests, and selectors here; use the Activity tab for runtime pressure and recent connection sessions.</figcaption>
   </figure>

3. Select **Edit** for each agent and add user labels in the **Edit Agent** drawer. Labels under `p2pstream.io/` are reserved for p2pstream and are shown read-only, but labels such as `site=home-lab`, `region=eu`, or `capacity=large` are operator-owned. Empty label values are allowed, but they should be intentional because they only match empty selector values.

   <figure class="doc-screenshot">
     <img src="../assets/new/agent_edit_labels_modal.png" alt="p2pstream Edit Agent drawer showing editable user labels and a read-only exact-agent system label">
     <figcaption>Use shared labels for pools and the reserved exact-agent label only when a route must pin traffic to one specific registered agent.</figcaption>
   </figure>

4. Open **Proxy -> Routes**, select **Add Route** or a route's **Edit** action, then add an agent proxy target in the route drawer:

   | Field | Value |
   | --- | --- |
   | Name | `media-home-lab` |
   | Type | Proxy |
   | Transport | Agent |
   | URL | `http://media.local:8096` |
   | Agent selector | `site=home-lab` |
   | Priority group | `0` |
   | Weight | `100` |
   | Enabled | On |

   <figure class="doc-screenshot">
     <img src="../assets/new/proxy_agent_route_target_modal.png" alt="p2pstream Edit Route drawer showing an agent target, label selectors, exact-agent chooser, and live match preview">
     <figcaption>The route drawer previews how many enabled and connected agents match all selector labels. The Target balancing control above the target governs route-target selection, not selection among agents.</figcaption>
   </figure>

   New agent targets use round-robin selection among matching agents by default. The public configuration API supports additional agent-selection policies; the redesigned route drawer retains an existing policy but does not currently expose a control to change it.

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

For flaky VPN paths, add an opt-in rule under **Traffic Policy → Retries**. Start with `GET` and `HEAD`, one retry, and **Connection establishment only**. A retry stays on this target, excludes the failed agent, and uses the target's agent load-balancing policy to choose a replacement. At least two matching connected agents are required. See [Request Retries](../concepts/request-retries) before enabling broader failures, request-body replay, or methods with side effects.

Agent tunnels use Yamux keepalives over an HTTP/1.1 upgraded management connection. When management is behind another reverse proxy, allow upgrade streaming for `p2pstream-yamux` on `/agent/tunnel` and configure that proxy's idle timeout high enough for long-lived agent sessions.

Old WebSocket agents are incompatible with Yamux-tunnel servers. Upgrade agents and servers together.

## Verification

Send repeated requests and inspect **Overview -> Hotspots -> Agents** or **Monitor -> Traffic** tracing to confirm traffic moves across the expected agents. Use **Agents -> Fleet** to check labels, current uptime, offline duration, and connection counts; use **Agents -> Activity** for recent connection sessions.

<figure class="doc-screenshot">
  <img src="../assets/new/live_traffic_diagram_tracing.png" alt="p2pstream Monitor Traffic Flow page showing traced requests across listeners, policy, routes, targets, an agent, and upstreams">
  <figcaption>Monitor traffic tracing shows the route target and agent selected for a request, which is the fastest way to verify that the pool selector and failover groups behave as intended.</figcaption>
</figure>

## Troubleshooting

| Symptom | Check |
| --- | --- |
| One agent receives too much traffic | Review selector labels and the target's API-configured agent load-balancing policy. |
| Requests fail from one site | Test the target URL from that agent host. |
| Agent is skipped | Confirm it is enabled, connected, label-matched, and healthy when health checks are on. |
| Agent disconnects while idle | Check management reverse-proxy HTTP/1.1 upgrade support and idle timeout for `p2pstream-yamux`. |
| Long first-byte delay times out | Increase target response-header timeout. |
| A retry rule never recovers | Confirm another enabled, connected, label-matched agent is available in the same target and inspect retry samples in **Monitor → Diagnostics**. |

## Next Steps

- [Route targets](../concepts/backends)
- [Agents](../concepts/agents)
- [Trace live traffic](./trace-live-traffic)
- [Request retries](../concepts/request-retries)
