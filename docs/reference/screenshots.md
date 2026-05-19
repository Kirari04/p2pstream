# Screenshots

A visual reference for the current p2pstream management console. These images are documentation assets under `docs/assets/new/` and are used throughout the docs where they clarify the current UI.

## Overview And Traffic

<div class="screenshot-gallery screenshot-gallery-full">
  <figure class="doc-screenshot screenshot-tile">
    <img src="../assets/new/dashboard_overview.png" alt="p2pstream Overview dashboard showing proxy status, request totals, success rate, traffic trend, hotspots, and problem signals">
    <figcaption>Overview dashboard</figcaption>
  </figure>

  <figure class="doc-screenshot screenshot-tile">
    <img src="../assets/new/live_traffic_diagram_tracing.png" alt="p2pstream Traffic page showing tracing enabled and a live request path through policy, routing, cache, agents, upstreams, and response">
    <figcaption>Live traffic diagram</figcaption>
  </figure>

  <figure class="doc-screenshot screenshot-tile">
    <img src="../assets/new/traffic_trace_request_details.png" alt="p2pstream traffic trace request details modal showing stage timing, selected route and backend, cache status, headers, and response metadata">
    <figcaption>Trace request details</figcaption>
  </figure>
</div>

## Proxy Configuration

<div class="screenshot-gallery screenshot-gallery-full">
  <figure class="doc-screenshot screenshot-tile">
    <img src="../assets/new/proxy_listeners.png" alt="p2pstream Proxy listeners section showing HTTP and HTTPS listeners with protocol, bind address, port, runtime state, and default backend">
    <figcaption>Listeners list</figcaption>
  </figure>

  <figure class="doc-screenshot screenshot-tile">
    <img src="../assets/new/proxy_edit_interface_listener_modal.png" alt="p2pstream listener editor showing protocol, bind address, port, default backend, and enabled state">
    <figcaption>Listener editor</figcaption>
  </figure>

  <figure class="doc-screenshot screenshot-tile">
    <img src="../assets/new/proxy_backends_and_routes.png" alt="p2pstream Proxy page showing backend cards and route cards with status, health, priority, listener match, and backend assignments">
    <figcaption>Backends and routes</figcaption>
  </figure>

  <figure class="doc-screenshot screenshot-tile">
    <img src="../assets/new/proxy_edit_backend_modal.png" alt="p2pstream backend editor showing forward mode, target origin, load balancing, timeout, health checks, and enabled state">
    <figcaption>Backend editor</figcaption>
  </figure>

  <figure class="doc-screenshot screenshot-tile">
    <img src="../assets/new/proxy_edit_route_modal.png" alt="p2pstream route editor showing listener, host pattern, path prefix, action, backend assignments, fallback backend, and priority">
    <figcaption>Route editor</figcaption>
  </figure>
</div>

## Agents

<div class="screenshot-gallery screenshot-gallery-full">
  <figure class="doc-screenshot screenshot-tile">
    <img src="../assets/new/agents_page.png" alt="p2pstream Agents page showing connection state, active requests, runtime metrics, token actions, and connection history">
    <figcaption>Agents page</figcaption>
  </figure>

  <figure class="doc-screenshot screenshot-tile">
    <img src="../assets/new/new_agent_modal_setup.png" alt="p2pstream new agent setup modal showing generated agent identity, one-time token, and install command options">
    <figcaption>Agent setup modal</figcaption>
  </figure>

  <figure class="doc-screenshot screenshot-tile">
    <img src="../assets/new/backend_agent_healthcheck_logs.png" alt="p2pstream backend health panel showing assigned agents, health state, active requests, and health-check log entries">
    <figcaption>Backend agent health</figcaption>
  </figure>
</div>

## Traffic Policies

<div class="screenshot-gallery screenshot-gallery-full">
  <figure class="doc-screenshot screenshot-tile">
    <img src="../assets/new/traffic_policies_waf_and_ratelimits.png" alt="p2pstream Traffic Policy page showing WAF rules and rate limits with priority, match summaries, actions, algorithms, and enabled state">
    <figcaption>WAF and rate limits</figcaption>
  </figure>

  <figure class="doc-screenshot screenshot-tile">
    <img src="../assets/new/edit_waf_modal.png" alt="p2pstream WAF rule editor showing match builder, action, activation mode, response template, captcha, and waiting-room settings">
    <figcaption>WAF rule editor</figcaption>
  </figure>

  <figure class="doc-screenshot screenshot-tile">
    <img src="../assets/new/edit_ratelimit_modal.png" alt="p2pstream rate-limit rule editor showing match builder, algorithm, limit, window, burst, key parts, and response settings">
    <figcaption>Rate-limit editor</figcaption>
  </figure>

  <figure class="doc-screenshot screenshot-tile">
    <img src="../assets/new/traffic_policies_cache_and_trafficshaper.png" alt="p2pstream Traffic Policy page showing cache rules and traffic shapers with priorities, match summaries, byte rates, and cache controls">
    <figcaption>Cache and shapers</figcaption>
  </figure>

  <figure class="doc-screenshot screenshot-tile">
    <img src="../assets/new/edit_cache_modal.png" alt="p2pstream cache rule editor showing match builder, route and backend filters, TTL, query handling, vary headers, status codes, and object limits">
    <figcaption>Cache rule editor</figcaption>
  </figure>

  <figure class="doc-screenshot screenshot-tile">
    <img src="../assets/new/edit_traffic_shaper.png" alt="p2pstream traffic shaper editor showing request match, budget scope, key parts, upload and download byte rates, burst, and exempt bytes">
    <figcaption>Traffic shaper editor</figcaption>
  </figure>
</div>

## Response Templates

<div class="screenshot-gallery screenshot-gallery-full">
  <figure class="doc-screenshot screenshot-tile">
    <img src="../assets/new/response_template_page.png" alt="p2pstream Templates page showing reusable response templates, template kinds, usage counts, content type, and actions">
    <figcaption>Templates page</figcaption>
  </figure>

  <figure class="doc-screenshot screenshot-tile">
    <img src="../assets/new/edit_template_modal.png" alt="p2pstream response template editor showing name, kind, content type, description, body editor, and preview controls">
    <figcaption>Template editor</figcaption>
  </figure>

  <figure class="doc-screenshot screenshot-tile">
    <img src="../assets/new/edit_template_modal_with_dynamic_values_waf.png" alt="p2pstream WAF response template editor showing allowed dynamic placeholders and rendered sample values for captcha or waiting-room pages">
    <figcaption>WAF template placeholders</figcaption>
  </figure>
</div>

## TLS

<div class="screenshot-gallery screenshot-gallery-full">
  <figure class="doc-screenshot screenshot-tile">
    <img src="../assets/new/tls_page.png" alt="p2pstream TLS page showing certificate mappings, ACME status, DNS credentials, renewal details, and certificate metadata">
    <figcaption>TLS page</figcaption>
  </figure>

  <figure class="doc-screenshot screenshot-tile">
    <img src="../assets/new/tls_httpchallenge_letsencrypt_modal.png" alt="p2pstream TLS certificate mapping modal showing HTTP challenge, Let's Encrypt CA, hostname pattern, listener, email, and enabled state">
    <figcaption>ACME HTTP challenge editor</figcaption>
  </figure>
</div>

## Settings And Environments

<div class="screenshot-gallery screenshot-gallery-full">
  <figure class="doc-screenshot screenshot-tile">
    <img src="../assets/new/settings_api_tokens.png" alt="p2pstream Settings API Tokens page showing token names, enabled state, expiry, last used time, and create or revoke actions">
    <figcaption>API tokens</figcaption>
  </figure>

  <figure class="doc-screenshot screenshot-tile">
    <img src="../assets/new/environment_settings_page.png" alt="p2pstream Settings Environments page showing local and remote environments, transport type, status, certificate trust state, and actions">
    <figcaption>Environment settings</figcaption>
  </figure>

  <figure class="doc-screenshot screenshot-tile">
    <img src="../assets/new/environment_trust_certificate.png" alt="p2pstream environment certificate trust dialog showing fingerprint, subject, issuer, SANs, validity dates, and trust action">
    <figcaption>Environment certificate trust</figcaption>
  </figure>
</div>

## Runtime Effects

These images are documentation assets only. They do not change product behavior and should be refreshed when the management UI layout changes.

## Related Tasks

- [First login](../getting-started/first-login)
- [Trace live traffic](../guides/trace-live-traffic)
- [Observability](../concepts/observability)
