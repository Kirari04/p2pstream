# Shape Bandwidth

Limit upload or download throughput for matched public requests without rejecting them.

## Use This When

Use traffic shaping for large uploads, public file downloads, or protecting small upstreams from bandwidth-heavy clients.

## Prerequisites

- A host/path/method match that isolates the traffic to slow.
- A byte-per-second budget for upload, download, or both.

## Steps

1. Open **Traffic Policy -> Traffic Shapers** and create a rule.

2. Match the traffic. Example for a downloads path:

   | Field | Value |
   | --- | --- |
   | Name | `downloads-shaper` |
   | Priority | `20` |
   | Enabled | On |
   | Protocols | HTTPS |
   | Host patterns | `files.example.com` |
   | Path prefixes | `/download` |

3. Choose budget scope:

   | Scope | Use when |
   | --- | --- |
   | Per key | All requests for the same key should share one budget. |
   | Per request | Every request should get an independent budget. |

   For public downloads, use per key and key by remote IP. For one-off large imports, per request may be simpler.

4. Set byte rates:

   | Field | Value |
   | --- | --- |
   | Download bytes per second | `1048576` |
   | Upload bytes per second | `0` |
   | Burst bytes | `2097152` |
   | Request exempt bytes | `0` |
   | Response exempt bytes | `65536` |

   `0` means unlimited for upload or download rates.

## Verification

Download a large matching file and watch transfer speed. Use **Traffic** tracing to confirm the shaper rule is selected.

## Troubleshooting

| Symptom | Check |
| --- | --- |
| Small responses appear unshaped | They may finish inside exempt bytes or before the rate is visible. |
| Clients share bandwidth unexpectedly | Review key parts and budget scope. |
| Rule does not match | Confirm host, path, protocol, method, and priority. |

## Next Steps

- [Traffic shaping reference](../reference/traffic-shaping)
- [Limits and shaping](../concepts/limits-and-shaping)
- [Trace live traffic](./trace-live-traffic)
