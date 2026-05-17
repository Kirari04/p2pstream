# Traffic Shaping Reference

Traffic shaper rules limit upload and/or download throughput for matching requests.

## Exact Fields And Defaults

| Setting | Default | Description |
| --- | --- | --- |
| Name | `traffic-shaper` when empty | Operator label. |
| Priority | `100` in database defaults | Lower numbers are evaluated first. |
| Budget scope | `per_key` | `per_key` or `per_request`. |
| Upload bytes per second | `0` | Request body throughput limit; `0` means unlimited. |
| Download bytes per second | `0` | Response body throughput limit; `0` means unlimited. |
| Burst bytes | `0` | Token bucket burst; defaults to the configured rate at runtime when unset. |
| Request exempt bytes | `0` | Initial request bytes sent without shaping. |
| Response exempt bytes | `0` | Initial response bytes sent without shaping. |

## Validation Rules

Traffic shapers use the same request-only CEL `match_rule` rules as rate limits. Empty match rules match every request.

`match_rule` is the only supported policy match shape. Legacy `match` is removed from the public API; existing stored legacy rows are migrated automatically to CEL/builder JSON.

Available CEL variables:

| Variable | Type | Notes |
| --- | --- | --- |
| `method` | string | Uppercase request method, such as `GET` or `POST`. |
| `protocol` | string | Listener protocol: `http` or `https`. |
| `host` | string | Normalized request host without port. |
| `path` | string | URL path. |
| `remote_ip` | string | Client remote IP. |
| `headers` | map string to list string | Header names are lowercase. Repeated headers keep all values. |
| `cookies` | map string to string | First cookie value by name. |
| `query` | map string to list string | Query parameter values by name. |

Helper functions:

- `host_match(host, pattern)` for exact and wildcard host patterns such as `*.example.com`.
- `path_prefix(path, prefix)` for path-prefix checks with segment boundaries.
- `cidr(remote_ip, cidr)` for IP range checks such as `203.0.113.0/24`.

CEL examples:

```cel
method == "GET" && host_match(host, "files.example.com") && path_prefix(path, "/download")
```

```cel
headers["x-client-tier"].exists(v, v in ["free", "trial"])
```

```cel
query["quality"].exists(v, v.matches("^(720p|1080p)$")) && !("bypass" in cookies)
```

```cel
cidr(remote_ip, "2001:db8::/32")
```

Route data, backend data, backend health, and load-balancer state are not available inside shaper match CEL. Traffic shapers still run before route resolution.

Key parts still identify the per-key budget. They can use remote IP, host, method, path, protocol, header, cookie, and query parameter values.

Byte rates and exempt bytes must be non-negative. Use realistic rates so operational debugging remains clear.

## Runtime Effects

Traffic shapers run after WAF and rate-limit checks and before route/backend forwarding. Shaping wraps streaming request and response bodies, so very small responses may finish before the limit is noticeable.

`per_key` shares buckets for matching requests with the same key. `per_request` creates fresh buckets for each request. Editing a rule resets its in-memory buckets.

## Examples

One MiB/s public download limit:

```text
Host pattern: files.example.com
Path prefix: /download
Budget scope: per_key
Download bytes per second: 1048576
Upload bytes per second: 0
Response exempt bytes: 65536
```

## Related Tasks

- [Shape bandwidth](../guides/shape-bandwidth)
- [Limits and shaping](../concepts/limits-and-shaping)
- [Trace live traffic](../guides/trace-live-traffic)
