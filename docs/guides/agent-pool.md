# Build a Multi-Agent Backend Pool

Agent pools let one public backend spread traffic across multiple remote networks or multiple hosts in the same network.

## 1. Register agents

Open **Agent Health** and create one agent per host.

Example:

| Agent | Purpose |
| --- | --- |
| `home-lab-a` | primary home lab host |
| `home-lab-b` | secondary host |
| `workshop` | remote workshop network |

Install each agent with its generated setup command and wait until each shows as connected.

## 2. Create the backend

Open **Management -> Backends**.

Create:

| Field | Value |
| --- | --- |
| Name | `media-pool` |
| Type | Proxy forward |
| Forward mode | Agent pool |
| Target origin | `http://media.local:8096` |
| Load balancing | Weighted least active requests |
| Enabled | On |

## 3. Assign agents

Add assignments:

| Agent | Weight | Enabled |
| --- | --- | --- |
| `home-lab-a` | `200` | On |
| `home-lab-b` | `100` | On |
| `workshop` | `50` | On |

Weights must be from `1` to `1000`. Keep at least one enabled assignment on an enabled backend.

## 4. Choose a balancing policy

| Policy | Best for |
| --- | --- |
| Round-robin | Simple equal rotation. |
| Weighted round-robin | Predictable split based on capacity. |
| Random | Low coordination, rough spread. |
| Weighted random | Rough spread with capacity weighting. |
| Least active requests | Long-running requests or uneven response times. |
| Weighted least active requests | Capacity-aware long-running requests. |

## 5. Operate the pool

Disable an assignment before taking an agent host down. Do not delete a connected agent; disconnect or disable it first.

If you enable health checks on an agent-pool backend, p2pstream checks the target origin from each enabled assigned connected agent. A loopback origin such as `http://127.0.0.1:8888` means loopback on each agent host.

Agent-pool backends also enforce the backend response-header timeout on the selected agent. The default is `60000` ms and it controls only the wait for first upstream response headers, not the total duration of an already-started response. Health-check timeout remains separate. Upgrade agents after changing this setting; older agents keep their built-in `30000` ms timeout.

Watch **Overview -> Hotspots -> Agents** to see which agents are serving traffic.
