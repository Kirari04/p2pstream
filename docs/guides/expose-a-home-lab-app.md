# Expose a Home Lab App Through an Agent

Expose a service from a private network by installing an agent that connects outbound to the p2pstream management server.

## Use This When

Use an agent-pool backend when the upstream service is reachable from a home lab or remote host, but not from the public p2pstream server.

Example:

| Role | Value |
| --- | --- |
| p2pstream management | `https://proxy.example.com:8081` |
| Home lab service | `http://homeassistant.local:8123` |
| Public URL | `https://ha.example.com` |

## Prerequisites

- The p2pstream server is reachable by the agent over management HTTPS/WSS.
- `MANAGEMENT_PUBLIC_URL` is set to the external management URL.
- The home lab host can reach the upstream service.
- Public DNS for `ha.example.com` points to the p2pstream server.

## Steps

1. Open **Agents** and select **Add Agent**.

   | Field | Value |
   | --- | --- |
   | Name | `home-lab` |
   | Enabled | On |

   After saving, the setup dialog shows the generated `AGENT_ID` and one-time `AGENT_TOKEN`.

   <figure class="doc-screenshot">
     <img src="../assets/new/new_agent_modal_setup.png" alt="p2pstream new agent setup modal showing generated agent identity, one-time token, and install command options">
     <figcaption>The setup dialog shows the one-time token and generated installer snippets. Copy the command before closing the dialog because the token is not shown again.</figcaption>
   </figure>

2. On the home lab host, run the generated Linux installer from the Agent Setup dialog. It has this shape:

   ```bash
   curl -fsSL https://raw.githubusercontent.com/Kirari04/p2pstream/main/scripts/install-agent.sh | sudo env \
     MANAGEMENT_URL='https://proxy.example.com:8081' \
     MANAGEMENT_CA_PEM_BASE64='...' \
     AGENT_ID='agent-...' \
     AGENT_TOKEN='...' \
     P2PSTREAM_REPOSITORY='Kirari04/p2pstream' \
     bash
   ```

   The installer creates `/usr/local/bin/p2pstream`, `/etc/p2pstream/agent.env`, and `p2pstream-agent.service`.

3. Check the agent service:

   ```bash
   sudo systemctl status p2pstream-agent
   sudo journalctl -u p2pstream-agent -f
   ```

4. Open **Proxy** and create an agent-pool backend:

   | Field | Value |
   | --- | --- |
   | Name | `homeassistant` |
   | Type | Proxy forward |
   | Forward mode | Agent pool |
   | Target origin | `http://homeassistant.local:8123` |
   | Load balancing | Round-robin |
   | Agent assignment | `home-lab`, weight `100`, enabled |
   | Enabled | On |

   The target origin is resolved from the agent host, not from the VPS.

5. In **Proxy**, route public traffic to the backend:

   | Field | Value |
   | --- | --- |
   | Listener | `public-https` |
   | Host pattern | `ha.example.com` |
   | Path prefix | `/` |
   | Backend | `homeassistant` |

6. Open **TLS** and add an ACME certificate for `ha.example.com`.

## Verification

Run:

```bash
curl -I https://ha.example.com
```

The **Agents** page should show the agent connected, and **Traffic** tracing should show the selected agent when the route uses the agent-pool backend.

## Troubleshooting

| Symptom | Check |
| --- | --- |
| Agent offline | Confirm `MANAGEMENT_URL`, CA material, token, and outbound firewall access. |
| Backend fails | Test `http://homeassistant.local:8123` from the agent host. |
| Health check unhealthy | Health checks run from each assigned connected agent. |
| Need to remove the agent | Use the uninstall command from **Agents** or [Systemd uninstall](../operations/systemd#uninstall-agent). |

If the agent is offline, requests to this backend fail until an enabled assigned agent reconnects.

## Next Steps

- [Build a multi-agent backend pool](./agent-pool)
- [Agents](../concepts/agents)
- [Systemd operations](../operations/systemd)
