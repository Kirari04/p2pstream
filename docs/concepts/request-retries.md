# Request Retries

Request retry rules recover selected public requests from transient agent-tunnel and VPN failures by making another upstream attempt through a different agent.

## What It Is

Retries are opt-in global traffic-policy rules. They apply only to agent-backed proxy targets and are evaluated after route/target selection and a cache miss. The first enabled matching rule wins by priority, then rule ID.

The safe starting configuration is:

| Setting | Recommended value |
| --- | --- |
| Methods | `GET`, `HEAD` |
| Retry attempts | `1` |
| Failure boundary | Connection establishment only |
| Request body replay | Do not buffer |

This gives one alternate agent a chance when the initial tunnel, stream, or origin connection fails before p2pstream knows the request was written.

## Runtime Guarantees

For every retry, p2pstream:

- keeps the already-selected route target, upstream URL, headers, TLS policy, and cache identity;
- excludes every agent already attempted by that request;
- asks the target's configured agent load balancer to choose among the remaining eligible agents;
- never retries after response headers have been received;
- never retries an HTTP response status, including `429`, `502`, `503`, or `504`;
- never retries `CONNECT`, `TRACE`, or protocol-upgrade requests;
- counts request concurrency once for the logical client request, while bandwidth shaping accounts for bytes transferred by each actual attempt.

Retries do not jump to a different route target. Target failover can change URL, headers, TLS, or application semantics, so it remains the responsibility of normal route-target selection on the next client request.

## Failure Boundaries

**Connection establishment only** retries when the attempt failed before the HTTP transport reported the request written and before it consumed request-body bytes. It covers agent disconnects, tunnel stream failures, agent capacity responses, and dial failures/timeouts. Certificate validation failures, forbidden destinations, server capacity, request-body read failures, and client cancellation are not retried.

**Any failure before response headers** also retries failures after the request may have reached the upstream, such as a response-header timeout or EOF. This can recover more VPN failures, but it can duplicate an upstream operation. The management API requires an explicit duplicate-risk acknowledgement for this mode.

## Request Bodies And Memory

Without buffering, a body-bearing request can retry only when the failed attempt consumed zero body bytes. With buffering enabled, bodies at or below the rule's replay limit are read once and supplied from a fresh reader to each attempt.

The per-rule replay limit is capped at 4 MiB. The server also reserves at most 64 MiB globally for concurrent retry buffers. If a body exceeds its rule limit or the global reservation is unavailable, the request still streams normally, but retry is skipped if the attempt consumes body bytes. The client request is not rejected merely because replay buffering was unavailable.

## Duplicate Requests

No proxy can prove that an upstream did not process a request when the connection disappears before its response arrives. Broad failure mode, `PUT`, `DELETE`, `POST`, `PATCH`, and the all-methods wildcard therefore require an explicit duplicate-risk acknowledgement.

Use an application idempotency key when retrying operations with side effects. An idempotency key lets the upstream recognize the second delivery as the same logical operation; the proxy itself does not manufacture or interpret those keys.

## Scope And Matching

Rules can filter by HTTP methods, route IDs, agent target IDs, and the shared request-only CEL matcher. Empty route and target filters mean all agent-backed proxy targets. Direct proxy targets, static responses, and redirects never match retry rules.

The **Traffic Policy → Retries** table shows exact scope, failure boundary, replay limit, priority, state, and duplicate-risk warnings. The Request Tester previews method, matcher, route, target, and agent-transport eligibility without sending traffic.

## Observability

One logical request produces one retained proxy event. Retry metadata records the selected rule, number of retries, outcome (`recovered`, `exhausted`, or `skipped`), and the first retry error kind. Recovered requests are retained in **Monitor → Diagnostics** even when the final response is successful.

Live tracing emits an **Upstream retry** stage with the failed agent, replacement agent, attempt number, and error kind. Structured proxy logs include the request ID, attempt number, maximum attempts, and selected agent.

## Common Mistakes

- Enabling all methods without application-level idempotency protection.
- Expecting response status codes to trigger a retry.
- Configuring only one matching connected agent; no alternate agent is then available.
- Treating retry rules as cross-target failover.
- Setting a replay limit larger than needed and consuming memory for bodies that should not be repeated.

## Related Links

- [Build an Agent Pool](../guides/agent-pool)
- [Limits and Shaping](./limits-and-shaping)
- [Observability](./observability)
- [Trace Live Traffic](../guides/trace-live-traffic)
