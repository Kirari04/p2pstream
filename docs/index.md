# p2pstream Self-Hosting Docs

Run p2pstream as a self-hosted public reverse proxy with a management console, optional remote agents, TLS automation, WAF controls, rate limits, traffic shaping, public asset caching, and live traffic tracing.

<p class="home-kicker">Start by outcome</p>

<div class="home-grid">
  <div class="home-card">
    <h3>Install the server</h3>
    <p>Start the Docker Compose deployment, persist runtime state in <code>p2pstream-data</code>, and open management over HTTPS.</p>
    <a href="./getting-started/quickstart">Use the quickstart</a>
  </div>
  <div class="home-card">
    <h3>Create the first admin</h3>
    <p>Use the 5 minute setup window, learn the login rules, and recover with the local password reset command if needed.</p>
    <a href="./getting-started/first-login">Complete first login</a>
  </div>
  <div class="home-card">
    <h3>Expose an app from the server</h3>
    <p>Create a backend, listener, route, and TLS mapping for a service reachable from the p2pstream host.</p>
    <a href="./guides/publish-a-service">Publish a direct backend</a>
  </div>
  <div class="home-card">
    <h3>Expose a home lab app</h3>
    <p>Register an agent, install it on the remote host, and route public traffic through the outbound agent connection.</p>
    <a href="./guides/expose-a-home-lab-app">Use an agent</a>
  </div>
  <div class="home-card">
    <h3>Set up public TLS</h3>
    <p>Use ACME HTTP-01, TLS-ALPN-01, or Cloudflare DNS-01 for trusted public certificates.</p>
    <a href="./concepts/tls">Choose a TLS path</a>
  </div>
  <div class="home-card">
    <h3>Harden and operate</h3>
    <p>Restrict management access, back up <code>/data</code>, plan upgrades, and troubleshoot failed routes, TLS, agents, and cache rules.</p>
    <a href="./operations/security-hardening">Review operations</a>
  </div>
</div>

## What You Run

<div class="home-strip">
  <div class="home-strip-item">
    <strong>Server</strong>
    <span>Management UI/API on <code>8081</code> plus public listeners stored in SQLite.</span>
  </div>
  <div class="home-strip-item">
    <strong>Data</strong>
    <span>SQLite, generated management TLS, public TLS, ACME state, and cache files under <code>/data</code>.</span>
  </div>
  <div class="home-strip-item">
    <strong>Agents</strong>
    <span>Outbound HTTPS/WSS clients that forward selected public traffic from remote networks.</span>
  </div>
</div>

## Management Console

The management UI is where selfhosters inspect runtime health and manage agents, listeners, backends, routes, TLS, WAF rules, rate limits, cache rules, traffic shapers, and live traces.

<figure class="doc-screenshot">
  <img src="./assets/overview.png" alt="p2pstream proxy overview dashboard showing proxy status, request metrics, traffic trend, hotspots, and configuration snapshot">
  <figcaption>Overview summarizes proxy health, recent traffic, active agents, and loaded public proxy configuration.</figcaption>
</figure>

## Reading Order

1. [Docker Compose quickstart](./getting-started/quickstart)
2. [First login](./getting-started/first-login)
3. [Publish a service](./guides/publish-a-service)
4. [Expose a home lab app through an agent](./guides/expose-a-home-lab-app)
5. [TLS](./concepts/tls)
6. [Security hardening](./operations/security-hardening)
7. [Backup and restore](./operations/backup-restore)
8. [Troubleshooting](./operations/troubleshooting)

## Fast Reference

| Need | Open |
| --- | --- |
| Environment variables | [Configuration reference](./reference/configuration) |
| CLI commands | [CLI reference](./reference/cli) |
| Docker image and ports | [Docker reference](./reference/docker) |
| Route matching behavior | [Routing rules reference](./reference/routing-rules) |
| WAF, rate limits, shapers, cache | [Traffic controls](./concepts/limits-and-shaping) |
| Visual tour | [Screenshots](./reference/screenshots) |
