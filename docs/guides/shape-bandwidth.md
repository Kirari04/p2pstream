# Shape Bandwidth

Traffic shaping limits upload and download throughput for matched requests. Use it for large uploads, public file downloads, or protecting small upstreams.

## 1. Open traffic shapers

Open **Management -> Traffic Policy -> Traffic Shapers** and create a rule.

## 2. Match the traffic

Example for a downloads path:

| Field | Value |
| --- | --- |
| Name | `downloads-shaper` |
| Priority | `20` |
| Enabled | On |
| Protocols | HTTPS |
| Host patterns | `files.example.com` |
| Path prefixes | `/download` |

## 3. Choose budget scope

| Scope | Use when |
| --- | --- |
| Per key | All requests for the same key should share one budget. |
| Per request | Every request should get an independent budget. |

For public downloads, use per key and key by remote IP. For one-off large imports, per request may be simpler.

## 4. Set byte rates

Values are bytes per second.

Example:

| Field | Value |
| --- | --- |
| Download bytes per second | `1048576` |
| Upload bytes per second | `0` |
| Burst bytes | `2097152` |
| Request exempt bytes | `0` |
| Response exempt bytes | `65536` |

`0` means unlimited for that direction.

## 5. Verify

Download a large file and watch transfer speed. Use **Traffic** tracing if you need to confirm the rule is selected.
