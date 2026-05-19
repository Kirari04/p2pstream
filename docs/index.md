---
layout: home
title: p2pstream
titleTemplate: Self-Hosting Docs

hero:
  name: p2pstream
  text: Self-hosted public reverse proxy
  tagline: Run public apps from direct backends or private networks with a management UI, TLS automation, traffic controls, and live tracing.
  image:
    src: /logo-mark.svg
    alt: p2pstream logo mark
  actions:
    - theme: brand
      text: Start with Docker Compose
      link: /getting-started/quickstart

features:
  - title: Install the server
    details: Start the Compose deployment, persist state, and open management over HTTPS.
    link: /getting-started/quickstart
    linkText: Use the quickstart
  - title: Publish an app
    details: Create a backend, listener, route, and TLS mapping for a service the server can reach.
    link: /guides/publish-a-service
    linkText: Publish a direct backend
  - title: Use a remote agent
    details: Expose a private-network service through an outbound agent connection.
    link: /guides/expose-a-home-lab-app
    linkText: Expose a home lab app
---

<p class="home-description">p2pstream is a self-hosted reverse proxy that routes public HTTPS traffic to direct backends or to services on private networks through outbound agent connections. A single binary handles public TLS, ACME certificate automation, WAF rules, rate limiting, traffic shaping, and live request tracing — all configured through a browser management UI.</p>

<nav class="home-reference-strip" aria-label="Fast reference">
  <a href="./concepts/tls">TLS</a>
  <a href="./reference/environments">Settings</a>
  <a href="./concepts/limits-and-shaping">Traffic controls</a>
  <a href="./operations/troubleshooting">Troubleshooting</a>
  <a href="./reference/configuration">Configuration</a>
  <a href="./reference/screenshots">Screenshots</a>
</nav>
