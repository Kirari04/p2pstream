//go:build linux

package server

import (
	"net"
	"os"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"p2pstream/internal/sysmetrics"
)

// TestPublicAdmissionKernelMemoryBurst exercises real autotuned TCP queues and
// production admission in a disposable 512 MiB cgroup. It is one bounded burst,
// not a proof that sampled admission bounds every possible kernel overshoot.
func TestPublicAdmissionKernelMemoryBurst(t *testing.T) {
	if os.Getenv("P2PSTREAM_MEMORY_STRESS") != "isolated-512m" {
		t.Skip("run scripts/test-proxy-memory.sh in a disposable memory-limited container")
	}
	const cgroup = "/sys/fs/cgroup/"
	const limit = int64(512 << 20)
	if got := stressCgroupValue(t, cgroup+"memory.max", ""); got != limit {
		t.Fatalf("refusing stress outside a 512 MiB cgroup: limit=%d", got)
	}
	oomBefore := stressCgroupValue(t, cgroup+"memory.events", "oom")
	killBefore := stressCgroupValue(t, cgroup+"memory.events", "oom_kill")
	initial := stressCgroupValue(t, cgroup+"memory.current", "")
	const targetBaseline = int64(376 << 20)
	if initial >= targetBaseline {
		t.Fatalf("unexpected initial memory charge %d", initial)
	}
	ballast := make([]byte, targetBaseline-initial)
	for index := 0; index < len(ballast); index += os.Getpagesize() {
		ballast[index] = 1
	}
	defer runtime.KeepAlive(ballast)

	sampler := sysmetrics.NewSystemMemoryUsageSampler()
	controller, err := sysmetrics.NewAdaptiveMemoryController(sysmetrics.DefaultAdaptiveMemoryConfig(), sampler)
	if err != nil {
		t.Fatal(err)
	}
	app := NewApp(nil, nil)
	app.agentStreamCapacity = mustNewDefaultAgentStreamCapacityManager(65_536)
	app.agentStreamCapacity.enableAdaptiveMemory(controller)
	initialSample := app.agentStreamCapacity.refreshAdaptiveCapacity(true)
	if initialSample.SampleError != "" || initialSample.Level != sysmetrics.MemoryPressureHealthy {
		t.Fatalf("initial resource sample is not healthy: %+v", initialSample)
	}
	usages, err := sampler.(sysmetrics.MemoryUsageSetSampler).SampleMemoryUsages()
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("initial independent memory constraints: %+v", usages)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	type pair struct {
		client, server net.Conn
		release        func()
	}
	var pairs []pair
	var writers sync.WaitGroup
	cleanup := func() {
		for _, pair := range pairs {
			_ = pair.client.Close()
			_ = pair.server.Close()
			pair.release()
		}
		writers.Wait()
		pairs = nil
	}
	defer cleanup()
	const sockets = 24
	for range sockets {
		client, err := net.DialTimeout("tcp", ln.Addr().String(), time.Second)
		if err != nil {
			t.Fatal(err)
		}
		server, err := ln.Accept()
		if err != nil {
			_ = client.Close()
			t.Fatal(err)
		}
		release, ok := app.tryReservePublicConnection(server)
		if !ok {
			_ = client.Close()
			_ = server.Close()
			t.Fatalf("baseline rejected socket %d: %+v", len(pairs), app.agentStreamCapacity.snapshot())
		}
		pairs = append(pairs, pair{client, server, release})
	}
	baseline := stressCgroupValue(t, cgroup+"memory.current", "")
	sockBaseline := stressCgroupValue(t, cgroup+"memory.stat", "sock")
	// All writers share one buffer so growing Go payload allocations cannot
	// substitute for the kernel memory signal this regression needs to prove.
	block := make([]byte, 64<<10)
	start := make(chan struct{})
	for _, pair := range pairs {
		writers.Add(1)
		go func(conn net.Conn) {
			defer writers.Done()
			<-start
			_ = conn.SetWriteDeadline(time.Now().Add(2 * time.Second))
			for range 256 {
				if _, err := conn.Write(block); err != nil {
					return
				}
			}
		}(pair.server)
	}
	close(start)

	var peak, sockPeak int64
	var rejected bool
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		peak = max(peak, stressCgroupValue(t, cgroup+"memory.current", ""))
		sockPeak = max(sockPeak, stressCgroupValue(t, cgroup+"memory.stat", "sock"))
		// This duplicate reservation is a candidate connection, never an
		// additional writer. The real sampler retains its default 100 ms cadence.
		release, ok := app.tryReservePublicConnection(pairs[0].server)
		if ok {
			release()
		} else {
			rejected = true
		}
		time.Sleep(20 * time.Millisecond)
	}
	writers.Wait()
	pressured := app.agentStreamCapacity.snapshot()
	if !rejected || pressured.ResourceSampleError != "" || app.publicConnectionResourceReject.Load() == 0 || app.publicConnectionLimitRejected.Load() != 0 {
		t.Fatalf("kernel burst did not cause memory admission rejection: %+v", pressured)
	}
	if sockPeak-sockBaseline < 8<<20 || peak >= limit || pressured.ResourcePressureReason != "memory" {
		t.Fatalf("invalid kernel burst: baseline=%d peak=%d sock baseline=%d peak=%d reason=%s", baseline, peak, sockBaseline, sockPeak, pressured.ResourcePressureReason)
	}
	t.Logf("100ms sampling, %d slow readers: cgroup baseline=%d peak=%d/%d, sock baseline=%d peak=%d, reservations=%d, pressure=%s, source=%s, resource rejects=%d", sockets, baseline, peak, limit, sockBaseline, sockPeak, pressured.AdaptiveExternalBytes, pressured.MemoryPressure, pressured.MemorySource, app.publicConnectionResourceReject.Load())
	// Retain the fully queued connections across several additional samples.
	for range 5 {
		time.Sleep(100 * time.Millisecond)
		if release, ok := app.tryReservePublicConnection(pairs[0].server); ok {
			release()
			t.Fatal("admission recovered while kernel queues and reservations remained owned")
		}
	}
	cleanup()
	deadline = time.Now().Add(5 * time.Second)
	recovered := false
	for time.Now().Before(deadline) {
		app.agentStreamCapacity.refreshAdaptiveCapacity(true)
		release, ok, _ := app.tryReservePublicResource(1152<<10, 1)
		if ok {
			release()
			recovered = true
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	final := app.agentStreamCapacity.snapshot()
	if !recovered || final.ResourceSampleError != "" || final.MemoryPressure != "healthy" || final.AdaptiveExternalBytes != 0 || final.AdaptiveExternalFDs != 0 {
		t.Fatalf("admission failed to recover after socket close: %+v", final)
	}
	if stressCgroupValue(t, cgroup+"memory.events", "oom") != oomBefore || stressCgroupValue(t, cgroup+"memory.events", "oom_kill") != killBefore {
		t.Fatal("kernel burst caused a cgroup OOM event")
	}
	t.Logf("recovered: cgroup=%d, sock=%d, pressure=%s", stressCgroupValue(t, cgroup+"memory.current", ""), stressCgroupValue(t, cgroup+"memory.stat", "sock"), final.MemoryPressure)
}

func stressCgroupValue(t *testing.T, path, key string) int64 {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	value := strings.TrimSpace(string(data))
	if key != "" {
		value = ""
		for line := range strings.SplitSeq(string(data), "\n") {
			fields := strings.Fields(line)
			if len(fields) == 2 && fields[0] == key {
				value = fields[1]
				break
			}
		}
	}
	parsed, err := strconv.ParseInt(value, 10, 64)
	if err != nil || parsed < 0 {
		t.Fatalf("invalid cgroup metric %s %s: %q (%v)", path, key, value, err)
	}
	return parsed
}
