# Analysis commands

No workload or tests were run. All commands below read the completed profiles or frozen source.

```text
GO=/home/user/go/pkg/mod/golang.org/toolchain@v0.0.1-go1.26.6.linux-amd64/bin/go
BINARY=/home/user/.codex/worktrees/astra-proxy-performance/p2pstream/tmp/native-go-attribution-validation90c9.VgdXP30B/native-go-attribution-build-248bad09.esfg1qq2/proxy-native-go-attribution.test
RUN=/home/user/.codex/worktrees/astra-proxy-performance/p2pstream/tmp/continuation-20261016/native-profile-results/native-profile-run.gqg05rke
```

Top and cumulative reports were generated for each role with these exact forms:

```bash
$GO tool pprof -top -nodecount=200 -unit=ms $BINARY $RUN/profile-production-http-h1-c16.cpu.pprof
$GO tool pprof -top -cum -nodecount=200 -unit=ms $BINARY $RUN/profile-production-http-h1-c16.cpu.pprof
$GO tool pprof -top -nodecount=200 -unit=ms $BINARY $RUN/profile-native-http-h1-c16.cpu.pprof
$GO tool pprof -top -cum -nodecount=200 -unit=ms $BINARY $RUN/profile-native-http-h1-c16.cpu.pprof
$GO tool pprof -top -nodecount=200 -sample_index=alloc_space -unit=bytes $BINARY $RUN/profile-production-http-h1-c16.mem.pprof
$GO tool pprof -top -cum -nodecount=200 -sample_index=alloc_space -unit=bytes $BINARY $RUN/profile-production-http-h1-c16.mem.pprof
$GO tool pprof -top -nodecount=200 -sample_index=alloc_space -unit=bytes $BINARY $RUN/profile-native-http-h1-c16.mem.pprof
$GO tool pprof -top -cum -nodecount=200 -sample_index=alloc_space -unit=bytes $BINARY $RUN/profile-native-http-h1-c16.mem.pprof
```

Targeted source listings used `go tool pprof -list` for these regular expressions, with `-sample_index=alloc_space -unit=bytes` added for allocation profiles:

```text
driverArgsConnLocked|appendProxyRequestEventArgs|insertProxyRequestEventsWithRollupsAndCacheTouches|SQLiteStmt.*bind|SQLiteStmt.*execSync|recordProxyRequestEvent
selectRouteTarget|resolvePublicRouteFromMatch|activeTargetAccountingAndForwardStage|tryReservePublicOwnedResource|tryReserveAdaptiveResourceMode|initializePublicProxyContext|proxyRequestContextFromHTTP|proxyRequestContextFromResolution
Header.Clone|Request.*WithContext|Request.*Clone|context.withCancel|context.WithValue
runtime.mallocgc|runtime.gcDrain|runtime.gcAssistAlloc|linux.Syscall6|internal/poll.*FD.*Read|internal/poll.*FD.*Write
```

Normalization formulas:

```text
completions = warmup_requests + useful_completed
cpu_us_per_completion = cpu_sample_nanoseconds / 1000 / completions
alloc_bytes_per_completion = alloc_space_bytes / completions
alloc_objects_per_completion = alloc_objects / completions
node_cpu_us_per_completion = pprof_node_milliseconds * 1000 / completions
node_alloc_bytes_per_completion = pprof_node_bytes / completions
```

Frozen source evidence was extracted with numbered reads from:

```text
internal/db/proxy_request_event_inserter.go:1-130
internal/server/observability_recorder.go:1-28,407-580
internal/server/observability_rollup.go:1-85
go.mod:12-22
github.com/mattn/go-sqlite3@v1.14.49/sqlite3-binding.c:14874-14886
```
