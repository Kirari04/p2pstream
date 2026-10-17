# Latest-source resource capacity requalification preparation

Frozen candidate: `8fa16378af4ac0117276726fe6e51d8bc4c74c85`. This preparation does not turn any older measurement into latest-source evidence. Only a fresh successful phase decision qualifies this exact candidate.

Root coordinates every build/test/traffic window. No production source is changed by this preparation. Do not start a development server. No automatic retries are present.

## Phase 1: original absolute candidate held capacity

Run once, exclusively, from the repository:

```sh
python3 -I -B tmp/proxy-capacity-followup-20261017/capacity-continuation/run-capacity.py --execute-coordinator-scheduled-once
```

The original first entrypoint stopped in source-closure preflight before launching endpoints: its unrestricted file-set scan found two ignored runtime certificate filenames. Its runner, frozen map, failure decision and root launch output/status remain unchanged. The separate continuation excludes exactly those two names, documented in its FROZEN.json. No credential contents are read, copied or published. All unknown additional inputs, including ignored Go files and potential embed inputs, still cause rejection. The only four Go embed directives cover migration SQL, schema SQL and the two server HTML templates; none covers runtime p2pstream-data.

The continuation reuses accepted ordinary Go1.26.6 binaries: server `5d7552f7a51f907dcbbd4c7b3e73590a3a887cbf1333cc7da058a8e23ee27577`, agent `c8f541da6c4be5ea3ed4ae3cc1df9bfb188da210d78ffc00962602354ff41537`. All 430 compile-source bytes and the full file set are checked before/after. The build was ordinary, CGO=1, linux/amd64/v1, empty GOFLAGS, no observer tags. Receipt/settings and original build commands are preserved. Any changed compile input requires a new reviewed ordinary build and freeze; this launcher never rebuilds silently.

The three original topologies each require 1,536 concurrently held H1 requests, three-second barrier and original 200ms post-release hold, followed by 512 H2 recovery requests: combined server+colocated agent; separate one agent; separate four agents. Original separate/combined 4GiB no-swap envelopes, 1GiB touched server ballast, CPU role assignments, event cardinalities, origin/request/connection ownership and zero-OOM assertions remain unchanged. The original one/four-lane current-agent/legacy-server 128-H1 bursts and 512 recovery are retained, along with verified legacy binary/overlay manifest provenance. The entire 10,831-byte workload/validator tail is unchanged.

The original mature runner owns endpoints and Docker containers. The copied accepted subreaper supervisor adds a 600-second outer deadline, 16MiB per stdout/stderr stream, 512MiB new-proof rejection threshold and natural descendant joins. Shared dependencies plus proof must remain below 4GiB before/after; no compiler/cache is run. Exact 22 natural zero-status process receipts and empty qualification Docker inventory are independently required. Thresholds are polled rejection bounds, not filesystem quotas; any observed overflow rejects, retains full logs, and attempts bounded cleanup. Prior same original cohort took 23 seconds; reserve 1–3 minutes for this phase.

## Phase 2: original grown-pool pressure

Review and run independently after phase 1 releases ownership:

```sh
python3 -I -B tmp/proxy-capacity-followup-20261017/pressure-prep/run-pressure.py --execute-coordinator-scheduled-once
```

The original pressure runner and independent validator are byte-identical to the accepted owner-aware versions (`43315047b3b545d8f45a1186e48792c740f587226180c8d8dc418731466938f3` / `3ae4060839ea4ae01b93314710fd93bad95c2de768f5676e722785450158419e`). `pressure-prep/reuse-vs-original.patch` changes only checkpoint fresh builds into copies of proven ordinary current binaries and the previously accepted legacy pressure binary, plus truthful receipt metadata. Exact current/legacy archives and original canonical legacy fixture overlay are retained. Current git-archive compile roots must equal all 430 ordinary build inputs. No new compiler/dependency lookup occurs.

Keep original user/net namespace entry, host PID topology, separate 4GiB no-swap systemd user scopes, four bulk plus one control lane, 80ms/1Gbit path, four 256MiB warmups each direction, one retained 256MiB each direction, all four receiving pools above 80MiB, actual agent memory at least90% with at least256MiB safety margin, one recorded503, reserved cold control200 while ordinary admission is zero, canceled32MiB upload/refund quiescence, and exact16MiB recovery each direction. Require original13 literal-true gates, exact12 transfer rows/14 conserved event rows, six natural owner exits, unchanged session identities and two inactive/empty endpoint scopes. Natural full process shutdown provides final retirement evidence; this does not measure idle lease retirement latency.

Outer deadline5,100s includes original inner4,800s plus cleanup/archive/verification overhead. Per-stream16MiB and new-pressure-proof2GiB polled thresholds; shared proof/dependency4GiB check inherited from source freeze. Original127-second pressure workload suggests3–5minutes plus archive/verification. Do not add a private PID namespace wrapper: it changes the accepted user-scope topology.

## Optional phase 3: standing16/32

```sh
python3 -I -B tmp/proxy-capacity-followup-20261017/standing-prep/run-standing.py --execute-coordinator-scheduled-once
```

This separately scheduled phase uses the unchanged standing wrapper: exact16 then32 aggregate agents, five sessions each,19 literal-true assertions per cohort and54 total natural process receipts. Same ordinary binaries/430 source closure, original cgroups/CPU roles;600s outer,16MiB streams,512MiB proof. It measures standing resource retention and health control progress. It is secondary to affected held-capacity and grown-pool pressure paths because route transport factories allocate/claim no pool entry before RoundTrip.

## Host prerequisites checked read-only

Docker28.3.3 is available to uid1000 via docker group; cgroupv2/systemd;16 online CPUs,32,309,121,024 bytes host memory; about173GB filesystem space at inspection. HAProxy/jq/openssl/taskset/systemd-run/unshare are installed. User systemd manager responds (state degraded, not unavailable). Unprivileged user namespaces are enabled by sysctl; actual namespace creation/scope enforcement is established by the original runner at execution, not claimed by read-only checks. Pressure needs working user/net namespaces and user systemd scopes, or the original sudo-n namespace fallback. Capacity/standing need Docker and the existing debian:trixie-slim image. No installation or privilege changes were performed.

Only source syntax/static identity review has been performed by this worker; root owns actual runtime and offline-contract execution. Existing old results under docs remain historical. No performance parity, universal maximum, cold4096 or broad hostile-traffic capacity is implied by these phases.
