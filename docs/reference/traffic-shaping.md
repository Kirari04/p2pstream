# Traffic Shaping Reference

Traffic shaper rules limit upload and/or download throughput for matching requests.

## Settings

| Setting | Description |
| --- | --- |
| Priority | Lower numbers are evaluated first. |
| Budget scope | `per_key` or `per_request`. |
| Upload bytes per second | Request body throughput limit. `0` means unlimited. |
| Download bytes per second | Response body throughput limit. `0` means unlimited. |
| Burst bytes | Token bucket burst. Defaults to the configured rate. |
| Request exempt bytes | Initial request bytes sent without shaping. |
| Response exempt bytes | Initial response bytes sent without shaping. |

Maximum configured byte rates are large enough for normal self-hosted deployments; use realistic values so troubleshooting remains clear.

## Match and key behavior

Traffic shapers reuse the same match and key concepts as rate limits:

- method,
- protocol,
- host pattern,
- path prefix,
- headers,
- cookies,
- query parameters,
- remote IP and other key parts.

## Per-key vs per-request

`per_key` shares buckets for matching requests with the same key. This is best for limiting a client, user, or API token.

`per_request` creates fresh buckets for each request. This is best when every transfer should get its own limit.

## Operational notes

- Shaping wraps streaming bodies, so very small responses may finish before the limit is noticeable.
- Use exempt bytes for headers or small health responses.
- Rules are in-memory runtime state; editing a rule resets its buckets.
