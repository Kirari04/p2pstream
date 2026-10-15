//go:build linux

package server

import (
	"net"
	"os"
	"runtime"
	"runtime/debug"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"p2pstream/internal/sysmetrics"
)

const (
	publicMemoryStressCgroup  = "/sys/fs/cgroup/"
	publicMemoryStressLimit   = int64(512 << 20)
	publicMemoryStressSockets = 24
)

type publicMemoryStressPair struct {
	client, server net.Conn
	release        func()
}

type publicMemoryStressConnections struct {
	listener net.Listener
	pairs    []publicMemoryStressPair
	writers  sync.WaitGroup
}

func newPublicMemoryStressConnections(t *testing.T, app *App) *publicMemoryStressConnections {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	connections := &publicMemoryStressConnections{listener: ln}
	for range publicMemoryStressSockets {
		client, err := net.DialTimeout("tcp", ln.Addr().String(), time.Second)
		if err != nil {
			connections.close()
			t.Fatal(err)
		}
		server, err := ln.Accept()
		if err != nil {
			_ = client.Close()
			connections.close()
			t.Fatal(err)
		}
		release, active, ok := app.tryReservePublicConnectionPhased(server)
		if !ok {
			_ = client.Close()
			_ = server.Close()
			connections.close()
			t.Fatalf("baseline rejected socket %d: %+v", len(connections.pairs), app.agentStreamCapacity.snapshot())
		}
		// These raw TCP pairs model established streaming connections. Drop
		// only the transient handshake promise, as the HTTP StateActive hook does.
		active()
		connections.pairs = append(connections.pairs, publicMemoryStressPair{client, server, release})
	}
	return connections
}

func (c *publicMemoryStressConnections) close() {
	if c == nil {
		return
	}
	for _, pair := range c.pairs {
		_ = pair.client.Close()
		_ = pair.server.Close()
		pair.release()
	}
	c.writers.Wait()
	c.pairs = nil
	if c.listener != nil {
		_ = c.listener.Close()
		c.listener = nil
	}
}

// TestPublicOutboundTCPQueueLowatBurst measures real slow-reader TCP queue
// growth independently of admission pressure. TCP_NOTSENT_LOWAT bounds unsent
// payload, not total per-socket kernel memory, so this keeps a conservative
// aggregate ceiling rather than treating 32 KiB as a complete socket bound.
func TestPublicOutboundTCPQueueLowatBurst(t *testing.T) {
	stressRequireIsolatedCgroup(t)
	oomBefore, killBefore := stressOOMEvents(t)
	app := newPublicMemoryStressApp(t)
	connections := newPublicMemoryStressConnections(t, app)
	defer connections.close()
	baseline := stressCgroupValue(t, publicMemoryStressCgroup+"memory.current", "")
	sockBaseline := stressCgroupValue(t, publicMemoryStressCgroup+"memory.stat", "sock")
	// All writers share one buffer so growing Go payload allocations cannot
	// substitute for the kernel socket signal this regression measures.
	block := make([]byte, 64<<10)
	start := make(chan struct{})
	for _, pair := range connections.pairs {
		connections.writers.Add(1)
		go func(conn net.Conn) {
			defer connections.writers.Done()
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
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		peak = max(peak, stressCgroupValue(t, publicMemoryStressCgroup+"memory.current", ""))
		sockPeak = max(sockPeak, stressCgroupValue(t, publicMemoryStressCgroup+"memory.stat", "sock"))
		time.Sleep(20 * time.Millisecond)
	}
	connections.writers.Wait()
	sockGrowth := sockPeak - sockBaseline
	if sockGrowth < 1<<20 || sockGrowth > 16<<20 || peak >= publicMemoryStressLimit {
		t.Fatalf("invalid LOWAT burst: cgroup baseline=%d peak=%d, sock baseline=%d peak=%d growth=%d", baseline, peak, sockBaseline, sockPeak, sockGrowth)
	}
	if app.publicConnectionResourceReject.Load() != 0 || app.publicConnectionLimitRejected.Load() != 0 {
		t.Fatalf("unpressured LOWAT burst rejected a connection: resource=%d limit=%d", app.publicConnectionResourceReject.Load(), app.publicConnectionLimitRejected.Load())
	}
	connections.close()
	if snapshot := app.agentStreamCapacity.snapshot(); snapshot.AdaptiveExternalBytes != 0 || snapshot.AdaptiveExternalFDs != 0 {
		t.Fatalf("outbound socket cleanup leaked reservations: %+v", snapshot)
	}
	stressRequireNoOOM(t, oomBefore, killBefore)
	t.Logf("LOWAT outbound burst, %d slow readers: cgroup baseline=%d peak=%d/%d, sock baseline=%d peak=%d growth=%d", publicMemoryStressSockets, baseline, peak, publicMemoryStressLimit, sockBaseline, sockPeak, sockGrowth)
}

// TestPublicAdmissionControlledMemoryPressure verifies production admission
// against actual cgroup pressure created after sockets were admitted. The
// pressure stimulus is independent of TCP queue behavior so queue tuning cannot
// accidentally remove the fail-closed safety and recovery coverage.
func TestPublicAdmissionControlledMemoryPressure(t *testing.T) {
	stressRequireIsolatedCgroup(t)
	oomBefore, killBefore := stressOOMEvents(t)
	initial := stressCgroupValue(t, publicMemoryStressCgroup+"memory.current", "")
	// Leave room below the controller's 75% recovery threshold for app and
	// connection setup overhead, which varies across kernels and CI runners.
	const targetBaseline = int64(352 << 20)
	if initial >= targetBaseline {
		t.Fatalf("unexpected initial memory charge %d", initial)
	}
	baselineBallast := stressTouchedBytes(targetBaseline - initial)
	defer runtime.KeepAlive(baselineBallast)

	app := newPublicMemoryStressApp(t)
	connections := newPublicMemoryStressConnections(t, app)
	defer connections.close()
	beforePressure := stressCgroupValue(t, publicMemoryStressCgroup+"memory.current", "")
	const targetPressure = int64(440 << 20)
	if beforePressure >= targetPressure {
		t.Fatalf("unexpected pre-pressure memory charge %d", beforePressure)
	}
	pressureBallast := stressTouchedBytes(targetPressure - beforePressure)

	var peak int64
	var rejected bool
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		peak = max(peak, stressCgroupValue(t, publicMemoryStressCgroup+"memory.current", ""))
		// This duplicate reservation is a candidate connection, never an
		// additional owner. The real sampler retains its 100 ms cadence.
		release, ok := app.tryReservePublicConnection(connections.pairs[0].server)
		if ok {
			release()
		} else {
			rejected = true
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	pressured := app.agentStreamCapacity.snapshot()
	if !rejected || pressured.ResourceSampleError != "" || app.publicConnectionResourceReject.Load() == 0 || app.publicConnectionLimitRejected.Load() != 0 {
		t.Fatalf("controlled pressure did not cause memory admission rejection: %+v", pressured)
	}
	if peak < publicMemoryStressLimit*80/100 || peak >= publicMemoryStressLimit || pressured.ResourcePressureReason != "memory" {
		t.Fatalf("invalid controlled pressure: before=%d peak=%d/%d reason=%s", beforePressure, peak, publicMemoryStressLimit, pressured.ResourcePressureReason)
	}
	// Keep both the post-admission memory and socket reservations owned across
	// multiple fresh samples; admission must remain closed.
	for range 5 {
		time.Sleep(100 * time.Millisecond)
		if release, ok := app.tryReservePublicConnection(connections.pairs[0].server); ok {
			release()
			t.Fatal("admission recovered while pressure and socket reservations remained owned")
		}
	}
	runtime.KeepAlive(pressureBallast)
	pressureBallast = nil
	connections.close()
	debug.FreeOSMemory()
	deadline = time.Now().Add(5 * time.Second)
	recovered := false
	for time.Now().Before(deadline) {
		sample := app.agentStreamCapacity.refreshAdaptiveCapacity(true)
		release, ok, _ := app.tryReservePublicResource(1152<<10, 1)
		if ok {
			release()
			if sample.Level == sysmetrics.MemoryPressureHealthy {
				recovered = true
				break
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	final := app.agentStreamCapacity.snapshot()
	if !recovered || final.ResourceSampleError != "" || final.MemoryPressure != "healthy" || final.AdaptiveExternalBytes != 0 || final.AdaptiveExternalFDs != 0 {
		t.Fatalf("admission failed to recover after pressure and socket release: %+v", final)
	}
	stressRequireNoOOM(t, oomBefore, killBefore)
	t.Logf("100ms controlled pressure after %d admitted sockets: before=%d peak=%d/%d, reservations=%d, pressure=%s, source=%s, resource rejects=%d", publicMemoryStressSockets, beforePressure, peak, publicMemoryStressLimit, pressured.AdaptiveExternalBytes, pressured.MemoryPressure, pressured.MemorySource, app.publicConnectionResourceReject.Load())
	t.Logf("recovered: cgroup=%d, sock=%d, pressure=%s", stressCgroupValue(t, publicMemoryStressCgroup+"memory.current", ""), stressCgroupValue(t, publicMemoryStressCgroup+"memory.stat", "sock"), final.MemoryPressure)
}

func stressRequireIsolatedCgroup(t *testing.T) {
	t.Helper()
	if os.Getenv("P2PSTREAM_MEMORY_STRESS") != "isolated-512m" {
		t.Skip("run scripts/test-proxy-memory.sh in a disposable memory-limited container")
	}
	if got := stressCgroupValue(t, publicMemoryStressCgroup+"memory.max", ""); got != publicMemoryStressLimit {
		t.Fatalf("refusing stress outside a 512 MiB cgroup: limit=%d", got)
	}
}

func newPublicMemoryStressApp(t *testing.T) *App {
	t.Helper()
	controller := newServerAdaptiveMemoryController(nil)
	if controller == nil {
		t.Fatal("production server resource controller unavailable")
	}
	app := NewApp(nil, nil)
	app.agentStreamCapacity = mustNewDefaultAgentStreamCapacityManager(65_536)
	app.agentStreamCapacity.enableAdaptiveMemory(controller)
	initialSample := app.agentStreamCapacity.refreshAdaptiveCapacity(true)
	if initialSample.SampleError != "" || initialSample.Level != sysmetrics.MemoryPressureHealthy {
		t.Fatalf("initial resource sample is not healthy: %+v", initialSample)
	}
	sampler := sysmetrics.NewSystemMemoryUsageSampler()
	usages, err := sampler.(sysmetrics.MemoryUsageSetSampler).SampleMemoryUsages()
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("initial independent memory constraints: %+v", usages)
	return app
}

func stressTouchedBytes(size int64) []byte {
	ballast := make([]byte, size)
	for index := 0; index < len(ballast); index += os.Getpagesize() {
		ballast[index] = 1
	}
	return ballast
}

func stressOOMEvents(t *testing.T) (oom, killed int64) {
	t.Helper()
	return stressCgroupValue(t, publicMemoryStressCgroup+"memory.events", "oom"), stressCgroupValue(t, publicMemoryStressCgroup+"memory.events", "oom_kill")
}

func stressRequireNoOOM(t *testing.T, beforeOOM, beforeKilled int64) {
	t.Helper()
	if stressCgroupValue(t, publicMemoryStressCgroup+"memory.events", "oom") != beforeOOM || stressCgroupValue(t, publicMemoryStressCgroup+"memory.events", "oom_kill") != beforeKilled {
		t.Fatal("memory stress caused a cgroup OOM event")
	}
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
