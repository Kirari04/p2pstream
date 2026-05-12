# Observability

p2pstream records enough operational data to answer whether traffic is flowing, which routes are hot, and where failures occur.

## Dashboard windows

The Overview page summarizes retained proxy request events over:

- `5m`,
- `1h`,
- `24h`,
- `30d`.

The default retention period is controlled by:

```text
OBSERVABILITY_RETENTION_DAYS=30
```

<figure class="doc-screenshot">
  <img src="../assets/overview.png" alt="p2pstream overview dashboard with request counts, success rate, throughput, traffic trend, and hotspots">
  <figcaption>Overview combines retained request events with current proxy and agent state.</figcaption>
</figure>

## Recorded request fields

Proxy request events include:

- status code,
- duration,
- error kind,
- listener ID,
- backend ID,
- route ID,
- agent ID,
- request bytes,
- response bytes.

## Agent stats

Agents report runtime stats, including:

- memory,
- goroutine count,
- active requests,
- request outcome counters,
- bytes received and sent.

## Traffic tracing

Traffic tracing is an admin-controlled live stream. Levels are:

| Level | Includes |
| --- | --- |
| Basic | High-level request stages. |
| Detailed | Host, query, target origin, backend type, and error kind. |
| Headers | Request and response headers, redacted where known. |
| Debug | More detailed event attributes. |

Use headers and debug tracing temporarily. They can expose operational details and request metadata.

<figure class="doc-screenshot">
  <img src="../assets/traffic_flow_diagram.png" alt="p2pstream traffic flow view showing a live request path through listener, rate limit, shaper, route, backend, agent, upstream, and response">
  <figcaption>Traffic Flow renders sampled request paths across listeners, policy checks, routes, backends, agents, and upstreams.</figcaption>
</figure>
