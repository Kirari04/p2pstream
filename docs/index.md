# Start with Docker Compose

p2pstream is a public reverse proxy and management server with optional remote agents. It can expose services from the server host, tunnel requests through registered agents, serve static responses, redirect traffic, automate public TLS certificates, apply WAF rules, rate-limit or shape traffic, and trace live request flow.

These docs are written for selfhosters operating p2pstream on a VPS, home lab host, or small private fleet. The recommended setup is Docker Compose with persistent state in the `p2pstream-data` volume.

<div class="home-grid">
  <div class="home-card">
    <h3>Run it</h3>
    <p>Start p2pstream with Docker Compose, persist `/data`, and complete the first admin setup.</p>
    <a href="./getting-started/quickstart">Open the Docker Compose quickstart</a>
  </div>
  <div class="home-card">
    <h3>Publish a service</h3>
    <p>Create a backend, listener, route, and TLS mapping for your first app.</p>
    <a href="./guides/publish-a-service">Publish a local service</a>
  </div>
  <div class="home-card">
    <h3>Add an agent</h3>
    <p>Connect a remote machine so p2pstream can reach home lab services over WSS.</p>
    <a href="./guides/expose-a-home-lab-app">Expose a home lab app</a>
  </div>
  <div class="home-card">
    <h3>Harden operations</h3>
    <p>Protect management access, back up SQLite and certificates, and plan upgrades.</p>
    <a href="./operations/security-hardening">Secure the deployment</a>
  </div>
</div>

## Management console

The management UI gives operators one place to inspect runtime health, traffic, agents, listeners, backends, routes, TLS, WAF rules, rate limits, and traffic shaping.

<figure class="doc-screenshot">
  <img src="./assets/overview.png" alt="p2pstream proxy overview dashboard showing proxy status, request metrics, traffic trend, hotspots, and configuration snapshot">
  <figcaption>The overview dashboard summarizes proxy health, recent traffic, active agents, and loaded configuration.</figcaption>
</figure>

## Main capabilities

| Capability | What it does |
| --- | --- |
| Public listeners | Bind HTTP or HTTPS listeners on ports such as `80`, `443`, or any explicitly published Docker port. |
| Routing | Match by host pattern, path prefix, and priority. Send traffic to a backend or issue a redirect. |
| Backends | Forward directly from the server host, return static responses, or route through an agent pool. |
| Agents | Keep outbound HTTPS/WSS connections from remote hosts to the management server and forward selected requests there. |
| TLS | Use manual certificate mappings or ACME HTTP-01, TLS-ALPN-01, and DNS-01 with Cloudflare. |
| Controls | Apply WAF block/captcha/waiting-room rules, request rate limits, and upload/download traffic shaping rules before forwarding. |
| Observability | Use dashboard windows, retained request events, agent stats, and live traffic tracing. |

## Recommended reading order

1. [Docker Compose quickstart](./getting-started/quickstart)
2. [First login](./getting-started/first-login)
3. [Publish a service](./guides/publish-a-service)
4. [WAF](./concepts/waf)
5. [Backup and restore](./operations/backup-restore)
6. [Security hardening](./operations/security-hardening)
