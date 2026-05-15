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

Traffic shapers reuse the same match and key concepts as rate limits:

- method,
- protocol,
- host pattern,
- path prefix,
- headers,
- cookies,
- query parameters,
- remote IP and other key parts.

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
