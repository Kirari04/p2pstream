#!/bin/sh
# Exact command environment; commands are run from the selected B or C source root.
export GOTOOLCHAIN=local
export GOWORK=off
export GOFLAGS=
export GOMAXPROCS=4
export GOMODCACHE=/home/user/go/pkg/mod
export TMPDIR="$RUN_ROOT/tmp"
export GOTMPDIR="$RUN_ROOT/gotmp"
export GOCACHE="$RUN_ROOT/gocache"
GO=/home/user/go/pkg/mod/golang.org/toolchain@v0.0.1-go1.26.6.linux-amd64/bin/go
FOCUSED='^(TestPreparedProxyRequestEventBatchPreservesAtomicRowsRollupsAndTouches|TestPreparedProxyRequestEventHierarchicalBatchPreserves131RowsRollupsAndTouches|TestPreparedProxyRequestEventBatchCancellationRollsBack|TestPreparedProxyRequestEventHierarchicalBatchCancellationAfterFirst64RollsBack|TestPreparedProxyRequestEventHierarchicalBatchFitsActualSQLiteVariableCeiling)$'
taskset -c 0-3 "$GO" test ./internal/server -run "$FOCUSED" -count=1 -timeout=5m
taskset -c 0-3 "$GO" test -race ./internal/server -run "$FOCUSED" -count=1 -timeout=5m
taskset -c 0-3 "$GO" test -c -o "$BINARY" ./internal/server
taskset -c 0-3 "$BINARY" -test.run '^$' -test.bench '^BenchmarkPreparedProxyRequestEventBatchFlush$/^production_deadline$/^events_(63|127|512)$' -test.benchtime=1s -test.count=1 -test.benchmem
