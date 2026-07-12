# Shape Bandwidth

Limit upload or download throughput for matched public requests without rejecting them.

## Use This When

Use traffic shaping for large uploads, public file downloads, or protecting small upstreams from bandwidth-heavy clients.

## Prerequisites

- A host/path/method match that isolates the traffic to slow.
- A throughput budget in KiB/s for upload, download, or both.

## Steps

1. Open **Traffic Policy -> Traffic Shaper** and select **Add Traffic Shaper**.

   <figure class="doc-screenshot">
     <img src="../assets/new/traffic_policies_cache_and_trafficshaper.png" alt="p2pstream Traffic Policy Traffic Shaper page showing its separate tab, rule filters, and compact rule table">
     <figcaption>Traffic Shaper and Cache are separate Traffic Policy tabs. The execution-order strip shows shaping before route resolution and cache after route-target selection.</figcaption>
   </figure>

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

4. Set the KiB budgets:

   | Field | Value |
   | --- | --- |
   | Download KiB/s | `1024` |
   | Upload KiB/s | `0` |
   | Burst KiB | `2048` |
   | Request free KiB | `0` |
   | Response free KiB | `64` |

   `0` means unlimited for an upload or download rate. Free KiB are sent without delay and do not consume the shaper budget.

   :::info Burst KiB
   Burst allows a connection to temporarily exceed the configured rate by up to this many KiB before the token bucket enforces the limit. Use it to absorb the initial burst of a download without throttling the entire transfer from byte one. Set it to `0` to disable burst.
   :::

<figure class="doc-screenshot">
  <img src="../assets/new/edit_traffic_shaper.png" alt="p2pstream Edit Traffic Shaper drawer showing per-key scope, KiB bandwidth budgets, CEL match, and key parts">
  <figcaption>The traffic-shaper drawer defines which requests are slowed, whether budgets are shared per key or per request, and the upload, download, burst, and free-data amounts in KiB.</figcaption>
</figure>

## Verification

Download a large matching file and watch transfer speed. Use **Monitor -> Traffic** tracing to confirm the shaper rule is selected.

## Troubleshooting

| Symptom | Check |
| --- | --- |
| Small responses appear unshaped | They may fit within the configured free KiB or finish before the rate is visible. |
| Clients share bandwidth unexpectedly | Review key parts and budget scope. |
| Rule does not match | Confirm host, path, protocol, method, and priority. |

## Next Steps

- [Traffic shaping reference](../reference/traffic-shaping)
- [Limits and shaping](../concepts/limits-and-shaping)
- [Trace live traffic](./trace-live-traffic)
