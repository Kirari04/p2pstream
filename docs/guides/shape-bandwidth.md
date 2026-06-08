# Shape Bandwidth

Limit upload or download throughput for matched public requests without rejecting them.

## Use This When

Use traffic shaping for large uploads, public file downloads, or protecting small upstreams from bandwidth-heavy clients.

## Prerequisites

- A host/path/method match that isolates the traffic to slow.
- A byte-per-second budget for upload, download, or both.

## Steps

1. Open **Traffic Policy -> Traffic Shapers** and create a rule.

   <figure class="doc-screenshot">
     <img src="../assets/new/traffic_policies_cache_and_trafficshaper.png" alt="p2pstream Traffic Policy page showing cache rules and traffic shapers">
     <figcaption>The Cache and Traffic Shapers sections sit together because both act after early deny policies and before or during route target forwarding.</figcaption>
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

4. Set byte rates:

   | Field | Value |
   | --- | --- |
   | Download bytes per second | `1048576` |
   | Upload bytes per second | `0` |
   | Burst bytes | `2097152` |
   | Request exempt bytes | `0` |
   | Response exempt bytes | `65536` |

   `0` means unlimited for upload or download rates.

   :::info Burst bytes
   Burst allows a connection to temporarily exceed the configured byte-per-second rate by up to this many bytes before the token bucket enforces the limit. Use it to absorb the initial burst of a download without throttling the entire transfer from byte one. Set to `0` to disable burst.
   :::

<figure class="doc-screenshot">
  <img src="../assets/new/edit_traffic_shaper.png" alt="p2pstream traffic shaper editor showing request match, budget scope, key parts, upload and download byte rates, burst, and exempt bytes">
  <figcaption>The traffic shaper editor defines which requests are slowed, whether budgets are shared per key or per request, and how much upload or download throughput is available.</figcaption>
</figure>

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
