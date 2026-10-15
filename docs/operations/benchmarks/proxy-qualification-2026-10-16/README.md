# Reverse-proxy qualification evidence, 2026-10-16

These artifacts preserve the first bounded reverse-proxy qualification pass.
They support workload-specific conclusions only. The loopback origin and load
generator ran on the same 16-CPU host; proxy processes were pinned to CPUs
`0-3`, load/origin work to CPUs `4-15`, and Go proxies used `GOMAXPROCS=4`.
HAProxy used `nbthread 4` on the same proxy CPU set. p2pstream capacity runs
used a 4 GiB Docker cgroup with swap disabled. All successful responses were
validated by expected length and content or hash before counting as useful.
On this host logical CPUs `0-1` share physical core 0 and `2-3` share physical
core 1. The proxy set therefore supplies four matched logical CPUs on two
physical cores to both p2pstream and HAProxy. A later distinct-core diagnostic
may improve absolute throughput, but does not retroactively change this matched
comparison.

## Files

- `capacity-and-profile.jsonl` is the initial candidate `095c923` correctness,
  fixed-rate, streaming, H1/H2 1,536-barrier and recovery pass.
- `warmed-throughput.jsonl` is one alternating baseline/candidate pass with a
  two-second warmup and ten-second unbuffered closed-loop timed interval.
- `stdlib-and-postsql-diagnostic.jsonl` is the later `d3767c9` H1 diagnostic
  against default `httputil.ReverseProxy` controls and HAProxy.
- `stdlib-pooled-diagnostic.jsonl` uses the same reusable 32 KiB copy-buffer
  pool as production.
- `final-h1-profile.jsonl` is the profiled `b4dd36b` H1 comparison. Its raw CPU,
  heap and allocation profiles remain in the ignored local run directory named
  in the operations report; profile binaries are not checked in.
- `ballast-capacity-boundary.jsonl` contains the H1 agent 1 GiB-ballast boundary
  search: baseline and candidate at 1,536, then candidate points at 1,024,
  1,280 and 1,400. The highest measured candidate pass is 1,280 and the lowest
  measured failure is 1,400.
- `ballast-capacity-corrected.jsonl` supersedes the failure-row status counts
  with a three-second barrier hold, exact HTTP status codes and a separate
  512-request recovery row after each capacity point.
- `h1-concurrency-sweep.jsonl` compares the `d1d952f` context-reuse candidate,
  pooled standard-library controls and HAProxy at 1, 4, 16, 32, 64, 128 and
  256 closed-loop clients. The 128-client candidate row was CPU/allocation
  profiled.
- `lowat-validation.jsonl` is the sanitized paired socket-memory, controlled
  pressure and WAN result set for `TCP_NOTSENT_LOWAT`, preserving byte counts
  and MiB conversions separately.

The initial 1,400 and 1,536 failure rows used a 10-second barrier release equal
to the upstream response-header timeout. Treat their origin high-water, owner
samples and admission-miss counters as capacity evidence; their final status,
error and useful-completion counts include the resulting timeout cascade.
Corrected reruns use a three-second maximum barrier hold and carry an explicit
`barrier_max_hold_seconds` field.

In the corrected 1,400 candidate row, 1,340 responses were 204 and 60 were 503;
origin high-water was 1,315 and the recorder counted 145 resource-pressure
admission misses. At 1,536, the baseline returned 688 204 and 848 503 responses
with origin high-water 573; the candidate returned 1,189 204 and 347 503 with
origin high-water 1,146. Both completed the following 512-request recovery row
without errors. Admission-miss counters can exceed final 503 counts because a
request may miss a transient acquisition and subsequently acquire on retry.

The completed H1 sweep found the highest sampled candidate rate at 16 clients:
29,614 useful requests/s, versus 46,917 for the stronger pooled
standard-library control and 70,055 for HAProxy. Candidate throughput was
5,864 at one client, 19,345 at four, 28,574 at 32, 28,140 at 64, 24,810 at 128
and 21,232 at 256. At the candidate optimum it reached 42.3% of HAProxy and
63.1% of the Go control, so limiting active requests may improve tail latency
but cannot close the throughput qualification gap. The 128-client profile
allocated 3.70 GiB across 298,703 warmup and timed requests; the earlier
pre-context-pool profile allocated 4.38 GiB across 305,506.
`newPublicProxyContext` no longer appeared as a material allocation site, but
the single profiled throughput sample did not establish a throughput gain.

LOWAT reduced the 24-slow-reader socket peak from 66,355,200 bytes (63.28125
MiB) to 5,996,544 bytes (5.71875 MiB). The split controlled-pressure test then
peaked at 461,434,880 of 536,870,912 cgroup bytes, rejected six acquisitions,
and recovered healthy with zero socket bytes and no OOM. Candidate and parent
WAN download/upload measurements were effectively identical; see the raw rows
for each H1/H2 direct, agent and control value.

SHA-256 digests:

```text
1d5a88150b38dfa730621a9e53e09268410080667d6c1c3061dd4e0a75997357  capacity-and-profile.jsonl
272ef696c69706eb0e6868370dbc7fa6e66d14b4493bcd9d8088ad8d33ce62ff  warmed-throughput.jsonl
17b676d7ca564d9f62bb4a862dfee25a566bebe93dbe679f3c10d78eb3426e9d  stdlib-and-postsql-diagnostic.jsonl
c4465a2f75d07d90174e307f4623d18201ef6a102cb4a4f60cc6b71b8d91e156  stdlib-pooled-diagnostic.jsonl
c27f2eb6295a2145195947164876e546849587fe000ab45a58fd209b00132cb5  final-h1-profile.jsonl
e3e6ff8dd4c417716068c303a391efafbed17b6a5f124e8d5affb62318666df7  ballast-capacity-boundary.jsonl
c18c1de6751ae59597f20186035b15097d8a7fe96b5d3ffc6f2bd864f6789b0c  ballast-capacity-corrected.jsonl
d7c3e70e4b290a304c738c149f60f75d1e2a93695ca9c3c81cb8c1110d470fa1  h1-concurrency-sweep.jsonl
2d11a48da396e11b2d438fe097cc72f79a467b0bba4251c236c7ad8fb711cc3e  lowat-validation.jsonl
```

## Source and binary provenance

| Label | Production source | Harness SHA-256 | Binary SHA-256 |
| --- | --- | --- | --- |
| `baseline-ee1aba7` | `ee1aba7b9091b65f42fc6529041507a8410141c2` | `dbde050725e44d1c9b85bd41b08c04dc259d0b381493ead038bc2b84950d1af6` | `235b331c7d96fa181cec74f138d37aa72fe6724569f65e694a29422002ac006c` |
| `candidate-095c923` | `095c9239edb8e007db3f64ba2e757b8900d00cd4` | same frozen comparison harness | `b5a57d3f037cb78d898a43d08861072c9b44b1a06452a985027c2942770096ff` |
| `candidate-d3767c9` | `d3767c930c52d49693a1bd8e8fcd2fb4f66c53b4` | `a94bea3b3f3cf5a8a1757a475d908ecfd051772561219e900206790d114e1a9b` | `266b9d2f01fe8c1f521e57feee4cf998b6085235fea52c513dccf9b20580354b` |
| `candidate-b4dd36b` | `b4dd36bffd57dc959c936e2eac1cced55a8cae7a` | `96b85f9adb748835e9922ef4b539311af9ba2e71afae1ffca3ce626d567343c3` | `ddd5dea5b8f9cf219ca0ffaf13370dc786f4a40ca2f4b247e16146226b7a0e57` |
| `candidate-b4dd36b` ballast capacity | `b4dd36bffd57dc959c936e2eac1cced55a8cae7a` | `cdbc6007a7dc5ea0979219af5dbd9dd153f9c259fdb4608d62e302d1547d0dd4` | `71a3268b7cbd048a3055064b32a6f5b47a58434b597a4144f2c0ee1990f2a86a` |
| `candidate-d1d952f` H1 sweep | `d1d952f0bbe18481a65c3b45e9e16fc6f9738382` | `27d5185a1e2d1923eb8183ef87f694b45b862989746f349aa44c5591ff3d25c9` | `2965a9af3f48907eeaafa364e13bcd019d634914be7f99969150b98ac462659b` |

The baseline and candidates were built from clean archives. Only the
qualification test harness was overlaid, so unrelated shared-worktree edits
could not enter the server binary:

```sh
mkdir -p tmp/proxy-qualification/bin tmp/proxy-qualification/builds
build_dir=$(mktemp -d -p tmp/proxy-qualification/builds baseline-ee1aba7.XXXXXX)
git archive ee1aba7 | tar -x -C "$build_dir"
install -m 0644 internal/server/proxy_qualification_test.go \
  "$build_dir/internal/server/proxy_qualification_test.go"
output=$(realpath tmp/proxy-qualification/bin/server-ee1aba7.test)
(cd "$build_dir" && go test -c -o "$output" ./internal/server)
sha256sum "$output" "$build_dir/internal/server/proxy_qualification_test.go"
```

Replace `ee1aba7` and the output label to replay a candidate. The current
orchestrator accepts frozen server and driver binaries explicitly:

```sh
scripts/qualify-proxy-performance.sh \
  --baseline-binary tmp/proxy-qualification/bin/server-ee1aba7.test \
  --candidate-binary tmp/proxy-qualification/bin/server-candidate.test \
  --driver-binary tmp/proxy-qualification/bin/server-driver.test \
  --candidate-label candidate-095c923
```

The constrained H1 boundary command substitutes the desired concurrency and
uses the clean-archive driver containing owner instrumentation:

```sh
scripts/qualify-proxy-performance.sh \
  --capacity-only --capacity-concurrency 1280 --capacity-protocol h1 \
  --path-filter agent --ballast-mib 1024 \
  --candidate-binary tmp/proxy-qualification/bin/server-harness-checkpoint.test \
  --driver-binary tmp/proxy-qualification/bin/server-harness-checkpoint.test \
  --candidate-label candidate-b4dd36b
```

The current harness reads newer owner arrays by reflection and was verified to
compile when overlaid on a clean `ee1aba7` archive. The exact uncommitted
historical harness used for the first baseline binary is preserved as
`internal/server/testdata/proxy_qualification_ee1aba7_test.go.txt`, SHA-256
`dbde050725e44d1c9b85bd41b08c04dc259d0b381493ead038bc2b84950d1af6`.
Copy that file instead when auditing the original executable's source. Go test
binary hashes can differ when rebuilt from another absolute archive directory
because debug/build metadata includes build context; the source and production
commit hashes are the reproducibility anchors.

Timed runs flush the observability recorder before warmup, again between
warmup and timed counter reset, and after the load. The final flush wall and
proxy CPU time are reported separately and excluded from timed useful
requests/s and proxy CPU. Older JSONL rows predate these counters; their process
logs recorded event drops and the operations report calls that limitation out.

## Environment and HAProxy control

```text
Linux 7.0.0-38-generic x86_64
Go go1.26.6 linux/amd64
Docker Engine 28.3.3
HAProxy 3.2.9-1ubuntu2.2
```

Each run generated an ephemeral P-256 certificate shared by p2pstream and
HAProxy. The sanitized HAProxy configuration was:

```text
global
  maxconn 4096
  nbthread 4
defaults
  mode http
  timeout connect 5s
  timeout client 30s
  timeout server 30s
frontend public
  bind 127.0.0.1:<ephemeral> ssl crt <ephemeral-bundle> alpn h2,http/1.1
  default_backend origin
backend origin
  server origin 127.0.0.1:<ephemeral>
```

HAProxy is a same-TLS, same-hop direct forwarding control. It does not execute
p2pstream's access, WAF, cache, Sites, observability or resource-accounting
pipeline and has no agent-tunnel equivalent. Agent rows contain the server and
real in-process agent in one process, so their RSS and CPU are combined.
HAProxy's host-cgroup socket counters are not process-isolated and must not be
compared with p2pstream's Docker-cgroup `memory.stat` values.
