# Screenshots

A visual reference for the current p2pstream management console. These images are documentation assets under `docs/assets/new/` and are used throughout the docs where they clarify the current UI.

## Overview And Traffic

<div class="screenshot-gallery screenshot-gallery-full">
  <figure class="doc-screenshot screenshot-tile">
    <img src="../assets/new/first_login_setup_admin.png" alt="p2pstream first-run setup screen for creating the initial administrator account">
    <figcaption>First login setup</figcaption>
  </figure>

  <figure class="doc-screenshot screenshot-tile">
    <img src="../assets/new/login_page.png" alt="p2pstream login page with username and password fields">
    <figcaption>Login page</figcaption>
  </figure>

  <figure class="doc-screenshot screenshot-tile">
    <img src="../assets/new/dashboard_overview.png" alt="p2pstream Overview dashboard showing proxy status, request totals, success rate, traffic trend, hotspots, and problem signals">
    <figcaption>Overview dashboard</figcaption>
  </figure>

  <figure class="doc-screenshot screenshot-tile">
    <img src="../assets/new/live_traffic_diagram_tracing.png" alt="p2pstream Traffic page showing tracing enabled and a live request path through policy, routing, cache, agents, upstreams, and response">
    <figcaption>Live traffic diagram</figcaption>
  </figure>

  <figure class="doc-screenshot screenshot-tile">
    <img src="../assets/new/traffic_trace_request_details.png" alt="p2pstream traffic trace request details modal showing stage timing, selected route and target, cache status, headers, and response metadata">
    <figcaption>Trace request details</figcaption>
  </figure>
</div>

## Proxy Configuration

<div class="screenshot-gallery screenshot-gallery-full">
  <figure class="doc-screenshot screenshot-tile">
    <img src="../assets/new/proxy_listeners.png" alt="p2pstream Proxy listeners section showing HTTP and HTTPS listeners with protocol, bind address, port, runtime state, and route count">
    <figcaption>Listeners list</figcaption>
  </figure>

  <figure class="doc-screenshot screenshot-tile">
    <img src="../assets/new/proxy_edit_interface_listener_modal.png" alt="p2pstream listener editor showing protocol, bind address, port, and enabled state">
    <figcaption>Listener editor</figcaption>
  </figure>

  <figure class="doc-screenshot screenshot-tile">
    <img src="../assets/new/proxy_backends_and_routes.png" alt="p2pstream Proxy page showing route cards with status, health, priority, listener match, and route targets">
    <figcaption>Routes and targets</figcaption>
  </figure>

  <figure class="doc-screenshot screenshot-tile">
    <img src="../assets/new/proxy_edit_backend_modal.png" alt="p2pstream route target editor showing target type, URL, transport, load balancing, timeout, health checks, and enabled state">
    <figcaption>Target editor</figcaption>
  </figure>

  <figure class="doc-screenshot screenshot-tile">
    <img src="../assets/new/proxy_edit_route_modal.png" alt="p2pstream route editor showing listener, host pattern, path prefix, action, route targets, fallback priority groups, and priority">
    <figcaption>Route editor</figcaption>
  </figure>

  <figure class="doc-screenshot screenshot-tile">
    <img src="../assets/new/proxy_direct_route_modal.png" alt="p2pstream route editor showing a direct upstream target for app traffic">
    <figcaption>Direct route target</figcaption>
  </figure>

  <figure class="doc-screenshot screenshot-tile">
    <img src="../assets/new/proxy_agent_route_target_modal.png" alt="p2pstream route editor showing an agent-selected target with label selectors">
    <figcaption>Agent route target</figcaption>
  </figure>

  <figure class="doc-screenshot screenshot-tile">
    <img src="../assets/new/proxy_redirect_route_modal.png" alt="p2pstream route editor showing an external redirect target with status and query preservation">
    <figcaption>Redirect route</figcaption>
  </figure>

  <figure class="doc-screenshot screenshot-tile">
    <img src="../assets/new/proxy_static_response_target_modal.png" alt="p2pstream route editor showing a static response target with template-backed response body">
    <figcaption>Static response target</figcaption>
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
    <img src="../assets/new/agent_edit_labels_modal.png" alt="p2pstream agent editor showing user labels and read-only system labels for route target selection">
    <figcaption>Agent labels editor</figcaption>
  </figure>
</div>

## Traffic Policies

<div class="screenshot-gallery screenshot-gallery-full">
  <figure class="doc-screenshot screenshot-tile">
    <img src="../assets/new/traffic_policies_waf_and_ratelimits.png" alt="p2pstream Traffic Policy page showing WAF rules and rate limits with priority, match summaries, actions, algorithms, and enabled state">
    <figcaption>WAF and rate limits</figcaption>
  </figure>

  <figure class="doc-screenshot screenshot-tile">
    <img src="../assets/new/waf_captcha_provider_modal.png" alt="p2pstream captcha provider editor showing provider type, site key, saved secret, and enabled state">
    <figcaption>Captcha provider editor</figcaption>
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
    <img src="../assets/new/cache_settings_section.png" alt="p2pstream cache settings section showing disk, memory, hot-object, entry, and cleanup controls">
    <figcaption>Cache settings</figcaption>
  </figure>

  <figure class="doc-screenshot screenshot-tile">
    <img src="../assets/new/edit_cache_modal.png" alt="p2pstream cache rule editor showing match builder, route and target filters, TTL, query handling, vary headers, status codes, and object limits">
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

  <figure class="doc-screenshot screenshot-tile">
    <img src="../assets/new/tls_dns_credential_modal.png" alt="p2pstream DNS credential editor showing Cloudflare zone ID, API token, provider, and enabled state">
    <figcaption>DNS credential editor</figcaption>
  </figure>

  <figure class="doc-screenshot screenshot-tile">
    <img src="../assets/new/tls_dnschallenge_cloudflare_modal.png" alt="p2pstream TLS certificate mapping modal showing DNS-01 challenge with a Cloudflare DNS credential">
    <figcaption>ACME DNS challenge editor</figcaption>
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
    <img src="../assets/new/settings_environment_editor_modal.png" alt="p2pstream environment editor showing management URL, transport, access token, enabled state, and timeout">
    <figcaption>Environment editor</figcaption>
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
