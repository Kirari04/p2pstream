# Trace Live Traffic

Use **Monitor -> Traffic** to see how a live request moves through listeners, WAF, rate limits, shapers, routes, targets, cache, agents, origin connections, and responses.

## Use This When

Use tracing while diagnosing why a request did not match a route, hit a target, use cache, pass WAF, or select the expected agent.

## Prerequisites

- You are logged in to management.
- The client request reaches a p2pstream public listener.
- You can reproduce the request while tracing is enabled.

## Steps

1. Open **Monitor -> Traffic**.
2. Enable **Tracing**.
3. Select a level:

   | Level | Use |
   | --- | --- |
   | Basic | Confirm requests are received and completed. |
   | Detailed | Diagnose route, target, cache, and agent selection. |
   | Headers | Inspect selected request/response headers. |
   | Debug | Temporary deep troubleshooting. |

   :::warning Headers and Debug log sensitive data
   **Headers** and **Debug** levels capture request and response headers, which can include `Authorization` tokens, session cookies, and API keys. Use them only while actively diagnosing an issue and reset to **Basic** or **Detailed** when done.
   :::

4. From another shell, reproduce the request:

   ```bash
   curl -v https://app.example.com/api/health
   ```

5. Select the request in **Recent traces** to open the **Trace details** drawer and inspect its outcome, resolved flow, lifecycle, and policy decisions. You can also select an active request token in the flow diagram.

<figure class="doc-screenshot">
  <img src="../assets/new/live_traffic_diagram_tracing.png" alt="p2pstream Monitor Traffic Flow page with Detailed tracing enabled and request paths rendered across policy, routing, agents, and upstreams">
  <figcaption>With Tracing enabled, Monitor's Traffic Flow view renders sampled requests across policy, routing, target selection, cache decisions, agents, upstreams, and responses.</figcaption>
</figure>

<figure class="doc-screenshot">
  <img src="../assets/new/traffic_trace_request_details.png" alt="p2pstream Trace details drawer showing request outcome, resolved flow, retained lifecycle, and policy decisions">
  <figcaption>Select a recent trace to inspect the request outcome, exact listener, route and target choices, retained lifecycle, policy decisions, and progressively disclosed request or capture details.</figcaption>
</figure>

## Runtime Effects

Traffic tracing is an admin-controlled live stream. It is meant for temporary diagnosis. Turn tracing off when finished, especially at Headers or Debug level.

Common stages include received, WAF evaluated, rate limited, route resolved, target selected, cache lookup, cache hit, cache miss, cache bypass, cache stored, agent selected, upstream started, upstream responded, response sent, and failed.

## Verification

A matching request should appear in **Recent traces** shortly after you reproduce it, while its activity is reflected in **Traffic Flow**. Cache is shown as a decision gateway after target selection: hits exit to response, while misses and bypasses continue to the direct upstream or selected agent. Agent failover emits an **Upstream retry** stage and the trace details show the retry rule, attempt count, and final outcome.

## Troubleshooting

| Symptom | Check |
| --- | --- |
| Request does not appear | Confirm tracing is enabled and the request hits a p2pstream public listener. |
| Expected asset is not cached | Check cache rule match, `Cookie`, `Authorization`, origin cache headers, status code, and object size. |
| Stream reconnects | Check management network, auth session, server restarts, and trace volume. |

## Next Steps

- [Observability](../concepts/observability)
- [Troubleshooting](../operations/troubleshooting)
- [Cache reference](../reference/cache)
- [Request retries](../concepts/request-retries)
