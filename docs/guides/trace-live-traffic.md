# Trace Live Traffic

Traffic tracing shows how a live request moves through listener, WAF, rate-limit, shaper, route, backend, agent, and upstream stages.

## 1. Open Traffic

Open **Traffic** in the management UI.

Enable **Tracing**.

<figure class="doc-screenshot">
  <img src="../assets/traffic_flow_diagram.png" alt="p2pstream traffic flow dashboard with tracing enabled and a rendered request path through policy, routing, cache, agents, and upstreams">
  <figcaption>With tracing enabled, the Traffic view shows how sampled requests move through policy, routing, backend selection, cache decisions, agents, upstreams, and responses.</figcaption>
</figure>

## 2. Select a level

| Level | Use |
| --- | --- |
| Basic | Confirm requests are received and completed. |
| Detailed | Diagnose route/backend/agent selection. |
| Headers | Inspect selected request/response headers. |
| Debug | Temporary deep troubleshooting. |

Use Headers and Debug only while diagnosing. Turn tracing off when finished.

## 3. Reproduce the request

From another shell:

```bash
curl -v https://app.example.com/api/health
```

Watch for stages:

- received,
- WAF evaluated, blocked, challenged, or waiting-room queued,
- rate limited,
- route resolved,
- backend selected,
- cache lookup,
- cache hit,
- cache miss,
- cache bypass,
- cache stored,
- agent selected when using an agent pool,
- upstream started,
- upstream responded,
- response sent,
- failed.

## 4. Use the diagram

The Traffic page renders recent requests across listeners, routes, backends, agents, and upstreams. Select a request to inspect details.

Cache is shown as a decision gateway after backend selection:

| Cache result | Diagram behavior |
| --- | --- |
| `HIT` | The dot enters Cache and exits directly to Response. |
| `MISS` | The dot enters Cache and continues to the direct upstream or selected agent. |
| `BYPASS` | The dot enters Cache and continues upstream with muted cache styling. |
| `STORED` | The Cache node shows a stored pulse while the request dot continues forward. |

If an expected asset is not hitting cache, check:

- no cache rule matched the host, path prefix, suffix, method, route, or backend,
- the browser sent cookies and the matching rule does not enable `allow_cookie_requests`,
- the request includes `Authorization`,
- the origin response includes `Set-Cookie`,
- the origin response includes `Cache-Control: no-store`, `private`, or `no-cache`,
- the origin response includes `Vary: Cookie`, `Vary: Authorization`, or `Vary: *`,
- the status code or object size is not allowed by the rule.

If the request does not appear, check that:

- tracing is enabled,
- you are hitting a p2pstream public listener,
- the browser or client is not using cached redirects,
- the listener port is published and reachable.
