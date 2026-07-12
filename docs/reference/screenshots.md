# Screenshots

A visual reference for the current p2pstream management console. These images are documentation assets under `docs/assets/new/` and are used throughout the docs where they clarify the current UI.

## Overview And Monitor

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
    <img src="../assets/new/live_traffic_diagram_tracing.png" alt="p2pstream Monitor Traffic page showing live trace state, capture-detail controls, pause action, request flow through listeners and policy to routes and targets, and a keyboard node list">
    <figcaption>Monitor traffic flow</figcaption>
  </figure>

  <figure class="doc-screenshot screenshot-tile">
    <img src="../assets/new/traffic_trace_request_details.png" alt="p2pstream Trace details drawer showing request outcome, status, duration, retained events, resolved listener route target and agent, lifecycle, and policy decisions">
    <figcaption>Incident-first trace details drawer</figcaption>
  </figure>
</div>

## Proxy Configuration

<div class="screenshot-gallery screenshot-gallery-full">
  <figure class="doc-screenshot screenshot-tile">
    <img src="../assets/new/proxy_listeners.png" alt="p2pstream Proxy Public Listeners tab showing proxy summary counts, listener search, HTTP and HTTPS bind addresses, route counts, runtime state, Stop, Edit, and More actions">
    <figcaption>Searchable public-listener table</figcaption>
  </figure>

  <figure class="doc-screenshot screenshot-tile">
    <img src="../assets/new/proxy_edit_interface_listener_modal.png" alt="p2pstream Edit Listener drawer showing name, bind address, port, HTTP or HTTPS protocol, and enabled state">
    <figcaption>Edit Listener drawer</figcaption>
  </figure>

  <figure class="doc-screenshot screenshot-tile">
    <img src="../assets/new/proxy_backends_and_routes.png" alt="p2pstream Proxy Routes tab showing compact rows for listener and match, forward or redirect action, proxy or static targets, priority, enabled state, and edit clone and delete actions">
    <figcaption>Routes and targets table</figcaption>
  </figure>

  <figure class="doc-screenshot screenshot-tile">
    <img src="../assets/new/proxy_edit_backend_modal.png" alt="p2pstream Edit Route drawer showing a direct proxy target's name, type, priority group, weight, URL, transport, header timeout, TLS verification, and enabled state">
    <figcaption>Direct proxy target fields</figcaption>
  </figure>

  <figure class="doc-screenshot screenshot-tile">
    <img src="../assets/new/proxy_edit_route_modal.png" alt="p2pstream Edit Route drawer showing listener, host pattern, path prefix, path security, action, route targets, fallback priority groups, and priority">
    <figcaption>Edit Route drawer</figcaption>
  </figure>

  <figure class="doc-screenshot screenshot-tile">
    <img src="../assets/new/proxy_direct_route_modal.png" alt="p2pstream route drawer showing a direct upstream target for app traffic">
    <figcaption>Direct route target</figcaption>
  </figure>

  <figure class="doc-screenshot screenshot-tile">
    <img src="../assets/new/proxy_agent_route_target_modal.png" alt="p2pstream route drawer showing an agent-selected target with label selectors">
    <figcaption>Agent route target</figcaption>
  </figure>

  <figure class="doc-screenshot screenshot-tile">
    <img src="../assets/new/proxy_redirect_route_modal.png" alt="p2pstream route drawer showing an external redirect target with status, path-suffix preservation, and query preservation">
    <figcaption>Redirect route</figcaption>
  </figure>

  <figure class="doc-screenshot screenshot-tile">
    <img src="../assets/new/proxy_static_response_target_modal.png" alt="p2pstream Edit Route drawer showing the currently available static target controls for name, type, priority group, weight, status, inline body, and enabled state">
    <figcaption>Static target inline-body controls</figcaption>
  </figure>
</div>

## Agents

<div class="screenshot-gallery screenshot-gallery-full">
  <figure class="doc-screenshot screenshot-tile">
    <img src="../assets/new/agents_page.png" alt="p2pstream Agents Fleet tab showing degraded fleet summary, connection and uptime metrics, attention-first searchable table, identity disclosures, health, load, Investigate, Edit, and More actions">
    <figcaption>Attention-first agent fleet</figcaption>
  </figure>

  <figure class="doc-screenshot screenshot-tile">
    <img src="../assets/new/new_agent_modal_setup.png" alt="p2pstream agent setup handoff showing generated identity, one-time token, copy state, install command options, and advanced settings">
    <figcaption>One-time agent setup handoff</figcaption>
  </figure>

  <figure class="doc-screenshot screenshot-tile">
    <img src="../assets/new/agent_edit_labels_modal.png" alt="p2pstream agent drawer showing user labels and read-only system labels for route target selection">
    <figcaption>Agent labels drawer</figcaption>
  </figure>
</div>

## Traffic Policies

<div class="screenshot-gallery screenshot-gallery-full">
  <figure class="doc-screenshot screenshot-tile">
    <img src="../assets/new/traffic_policies_waf_and_ratelimits.png" alt="p2pstream Traffic Policy WAF tab showing rule counts, execution order, Request Tester, filterable WAF rules, captcha providers, and Visitor identity and GeoIP settings">
    <figcaption>WAF rules, captcha providers, and visitor identity</figcaption>
  </figure>

  <figure class="doc-screenshot screenshot-tile">
    <img src="../assets/new/waf_captcha_provider_modal.png" alt="p2pstream Edit Captcha Provider drawer showing provider type, site key, saved-secret state, and enabled state">
    <figcaption>Edit Captcha Provider drawer</figcaption>
  </figure>

  <figure class="doc-screenshot screenshot-tile">
    <img src="../assets/new/edit_waf_modal.png" alt="p2pstream Edit WAF Rule drawer showing name, priority, enabled state, Captcha action, Automatic activation, CEL match, and geographic targeting">
    <figcaption>WAF action, activation, match, and geographic targeting</figcaption>
  </figure>

  <figure class="doc-screenshot screenshot-tile">
    <img src="../assets/new/edit_ratelimit_modal.png" alt="p2pstream Edit Rate Limit drawer showing name, priority, enabled state, algorithm choices, limit, window, burst, live budget preview, and CEL match">
    <figcaption>Rate-limit algorithm, budget preview, and match</figcaption>
  </figure>

  <figure class="doc-screenshot screenshot-tile">
    <img src="../assets/new/traffic_policies_cache_and_trafficshaper.png" alt="p2pstream Traffic Policy Traffic Shaper tab showing a filterable compact table with match and key, upload and download budget, scope, priority, state, and actions">
    <figcaption>Traffic Shaper tab</figcaption>
  </figure>

  <figure class="doc-screenshot screenshot-tile">
    <img src="../assets/new/cache_settings_section.png" alt="p2pstream Traffic Policy Cache tab showing filterable cache rules plus cache storage enabled, disk, memory, hot-object, entry, cleanup, save, and purge controls">
    <figcaption>Cache rules and storage operations</figcaption>
  </figure>

  <figure class="doc-screenshot screenshot-tile">
    <img src="../assets/new/edit_cache_modal.png" alt="p2pstream cache rule drawer showing match builder, route and target filters, TTL, query handling, vary headers, status codes, and object limits">
    <figcaption>Cache rule drawer</figcaption>
  </figure>

  <figure class="doc-screenshot screenshot-tile">
    <img src="../assets/new/edit_traffic_shaper.png" alt="p2pstream traffic shaper drawer showing request match, budget scope, key parts, upload and download byte rates, burst, and exempt bytes">
    <figcaption>Traffic shaper drawer</figcaption>
  </figure>
</div>

## Response Templates

<div class="screenshot-gallery screenshot-gallery-full">
  <figure class="doc-screenshot screenshot-tile">
    <img src="../assets/new/response_template_page.png" alt="p2pstream Response Templates page showing kind summary cards and a compact table with description, kind, content type, usage count, required placeholders, updated time, and actions">
    <figcaption>Response template inventory</figcaption>
  </figure>

  <figure class="doc-screenshot screenshot-tile">
    <img src="../assets/new/edit_template_modal.png" alt="p2pstream Edit Response Template drawer showing name, locked kind with immutability guidance, content type, description, body editor, and preview">
    <figcaption>Template drawer with immutable kind</figcaption>
  </figure>

  <figure class="doc-screenshot screenshot-tile">
    <img src="../assets/new/edit_template_modal_with_dynamic_values_waf.png" alt="p2pstream WAF response template drawer showing allowed dynamic placeholders and rendered sample values for captcha or waiting-room pages">
    <figcaption>WAF template placeholders</figcaption>
  </figure>
</div>

## TLS

<div class="screenshot-gallery screenshot-gallery-full">
  <figure class="doc-screenshot screenshot-tile">
    <img src="../assets/new/tls_page.png" alt="p2pstream TLS page showing HTTPS listener and certificate summary cards, mapping status and lifecycle, ACME errors and retry actions, and a separate DNS credentials table">
    <figcaption>TLS certificate and DNS credential inventory</figcaption>
  </figure>

  <figure class="doc-screenshot screenshot-tile">
    <img src="../assets/new/tls_httpchallenge_letsencrypt_modal.png" alt="p2pstream Edit TLS Mapping drawer showing HTTP-01 selected, Let's Encrypt CA environment, hostname pattern, HTTPS listener, ACME email, and enabled state">
    <figcaption>Edit HTTP-01 mapping</figcaption>
  </figure>

  <figure class="doc-screenshot screenshot-tile">
    <img src="../assets/new/tls_dns_credential_modal.png" alt="p2pstream Edit DNS Credential drawer showing name, Cloudflare zone ID, saved API-token state, and enabled state">
    <figcaption>Edit DNS credential</figcaption>
  </figure>

  <figure class="doc-screenshot screenshot-tile">
    <img src="../assets/new/tls_dnschallenge_cloudflare_modal.png" alt="p2pstream Edit TLS Mapping drawer showing DNS-01 challenge with a saved Cloudflare DNS credential">
    <figcaption>Edit DNS-01 mapping</figcaption>
  </figure>
</div>

## System Settings

<div class="screenshot-gallery screenshot-gallery-full">
  <figure class="doc-screenshot screenshot-tile">
    <img src="../assets/new/settings_api_tokens.png" alt="p2pstream API Tokens page under System showing the selected environment, issued-token table, expiry, last-used time, status, Create Token, refresh, and revoke actions">
    <figcaption>Selected-environment API tokens</figcaption>
  </figure>

  <figure class="doc-screenshot screenshot-tile">
    <img src="../assets/new/environment_settings_page.png" alt="p2pstream Environments page under System showing a registered remote's endpoint and transport, certificate trust, reachability, Review trust, Test, and More actions while Local remains in the header selector">
    <figcaption>Remote environment registry</figcaption>
  </figure>

  <figure class="doc-screenshot screenshot-tile">
    <img src="../assets/new/settings_environment_editor_modal.png" alt="p2pstream Edit Environment drawer showing management URL, Direct or Agent transport, retained access-token state, enabled state, and response-header timeout">
    <figcaption>Edit Environment drawer</figcaption>
  </figure>

  <figure class="doc-screenshot screenshot-tile">
    <img src="../assets/new/environment_trust_certificate.png" alt="p2pstream Trust Certificate dialog showing the environment, full SHA-256 fingerprint, subject, issuer, certificate names, valid-until time, and Trust Certificate action">
    <figcaption>Environment certificate trust</figcaption>
  </figure>
</div>

## Runtime Effects

These images are documentation assets only. They do not change product behavior and should be refreshed when the management UI layout changes.

## Related Tasks

- [First login](../getting-started/first-login)
- [Trace live traffic](../guides/trace-live-traffic)
- [Observability](../concepts/observability)
