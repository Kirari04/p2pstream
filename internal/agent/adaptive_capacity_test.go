package agent

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hashicorp/yamux"
	"github.com/rs/zerolog"

	"p2pstream/internal/buildinfo"
	"p2pstream/internal/sysmetrics"
	"p2pstream/internal/tunnel"
)

func newTestAgentAdaptiveCapacity(t testing.TB, usage *sysmetrics.MemoryUsage) *agentTunnelCapacityRuntime {
	t.Helper()
	config := sysmetrics.DefaultAdaptiveMemoryConfig()
	config.SampleInterval = time.Hour
	controller, err := sysmetrics.NewAdaptiveMemoryController(config, sysmetrics.MemoryUsageSamplerFunc(func() (sysmetrics.MemoryUsage, error) {
		return *usage, nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	return newAgentTunnelCapacityRuntime(2048, true, controller)
}

func TestAdaptiveAgentCapacityUsesHealthyResourceHeadroom(t *testing.T) {
	usage := sysmetrics.MemoryUsage{UsedBytes: 64 << 20, LimitBytes: 512 << 20, Source: "test"}
	capacity := newTestAgentAdaptiveCapacity(t, &usage)
	capacity.forceRefresh()

	releases := make([]func(), 0, 300)
	for range 300 {
		release, snapshot, ok := capacity.tryAcquire()
		if !ok {
			t.Fatalf("healthy adaptive admission stopped at %d/%d", snapshot.InUse, snapshot.AdmissionLimit)
		}
		releases = append(releases, release)
	}
	if got := capacity.snapshot(); got.InUse != 300 || got.AdmissionLimit <= 300 || got.Pressure != sysmetrics.MemoryPressureHealthy {
		t.Fatalf("healthy snapshot = %+v", got)
	}
	for _, release := range releases {
		release()
		release() // release ownership must be idempotent
	}
	if got := capacity.snapshot().InUse; got != 0 {
		t.Fatalf("in-use after release = %d, want 0", got)
	}
}

func TestAdaptiveAgentCapacityExceedsLegacyFixedProtocolMaximum(t *testing.T) {
	usage := sysmetrics.MemoryUsage{UsedBytes: 128 << 20, LimitBytes: 16 << 30, Source: "test"}
	config := sysmetrics.DefaultAdaptiveMemoryConfig()
	config.SampleInterval = time.Hour
	controller, err := sysmetrics.NewAdaptiveMemoryController(config, sysmetrics.MemoryUsageSamplerFunc(func() (sysmetrics.MemoryUsage, error) {
		return usage, nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	capacity := newAgentTunnelCapacityRuntime(tunnel.MaxAdaptiveConcurrentStreamsLimit, true, controller)
	capacity.forceRefresh()

	const wanted = 4_096
	releases := make([]func(), 0, wanted)
	for index := range wanted {
		release, snapshot, ok := capacity.tryAcquire()
		if !ok {
			t.Fatalf("adaptive agent admission %d stopped at allowance %d", index, snapshot.AdmissionLimit)
		}
		releases = append(releases, release)
	}
	for _, release := range releases {
		release()
	}
}

func TestAgentTunnelCapacityMaximumConversionIsArchitectureSafe(t *testing.T) {
	if got := agentTunnelCapacityMaximumInt(-1); got != 1 {
		t.Fatalf("negative maximum normalized to %d, want 1", got)
	}
	wantMaximum := int(tunnel.MaxAdaptiveConcurrentStreamsLimit)
	if got := agentTunnelCapacityMaximumInt(math.MaxInt64); got != wantMaximum {
		t.Fatalf("maximum normalized to %d, want implementation guard %d", got, wantMaximum)
	}
	runtime := newAgentTunnelCapacityRuntime(math.MaxInt64, false, nil)
	if runtime.maximum != wantMaximum {
		t.Fatalf("runtime maximum = %d, want %d", runtime.maximum, wantMaximum)
	}
}

func TestProductionAgentNegotiationSustains100RPSWithGeoLatency(t *testing.T) {
	const (
		requestCount = 500
		requestRate  = 100
	)
	previousLogLevel := zerolog.GlobalLevel()
	zerolog.SetGlobalLevel(zerolog.WarnLevel)
	t.Cleanup(func() { zerolog.SetGlobalLevel(previousLogLevel) })

	upstream, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = upstream.Close() })
	go func() {
		for {
			conn, acceptErr := upstream.Accept()
			if acceptErr != nil {
				return
			}
			go func() {
				defer conn.Close()
				line, readErr := bufio.NewReader(conn).ReadString('\n')
				if readErr != nil {
					return
				}
				delayMillis, parseErr := strconv.Atoi(strings.TrimSpace(line))
				if parseErr != nil || delayMillis < 800 || delayMillis > 2500 {
					return
				}
				time.Sleep(time.Duration(delayMillis) * time.Millisecond)
				_, _ = io.WriteString(conn, "ok\n")
			}()
		}
	}()

	runCtx, cancel := context.WithCancel(t.Context())
	defer cancel()
	sessionReady := make(chan *yamux.Session, 1)
	management := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get(tunnel.TunnelCapacityModeHeader) != tunnel.TunnelCapacityModeAdaptive ||
			r.Header.Get(tunnel.TunnelMaxConcurrentStreamsHeader) != strconv.FormatInt(tunnel.MaxConcurrentAgentRequestsLimit, 10) ||
			r.Header.Get(tunnel.TunnelAgentVersionHeader) != buildinfo.Version ||
			r.Header.Get(tunnel.TunnelAgentCommitHeader) != buildinfo.Commit {
			http.Error(w, "adaptive capacity headers missing", http.StatusBadRequest)
			return
		}
		hijacker, ok := w.(http.Hijacker)
		if !ok {
			http.Error(w, "hijack unavailable", http.StatusInternalServerError)
			return
		}
		conn, rw, hijackErr := hijacker.Hijack()
		if hijackErr != nil {
			return
		}
		_, _ = fmt.Fprintf(rw, "HTTP/1.1 101 Switching Protocols\r\nConnection: Upgrade\r\nUpgrade: %s\r\n%s: %s\r\n%s: %d\r\n\r\n",
			tunnel.UpgradeToken,
			tunnel.TunnelCapacityModeHeader, tunnel.TunnelCapacityModeAdaptive,
			tunnel.TunnelMaxConcurrentStreamsHeader, tunnel.MaxAdaptiveConcurrentStreamsLimit,
		)
		if flushErr := rw.Flush(); flushErr != nil {
			_ = conn.Close()
			return
		}
		serverSession, sessionErr := yamux.Server(conn, tunnel.DefaultYamuxConfig(nil))
		if sessionErr != nil {
			_ = conn.Close()
			return
		}
		sessionReady <- serverSession
		<-runCtx.Done()
		_ = serverSession.Close()
	}))
	t.Cleanup(management.Close)

	usage := sysmetrics.MemoryUsage{UsedBytes: 64 << 20, LimitBytes: 512 << 20, Source: "test"}
	config := sysmetrics.DefaultAdaptiveMemoryConfig()
	controller, err := sysmetrics.NewAdaptiveMemoryController(config, sysmetrics.MemoryUsageSamplerFunc(func() (sysmetrics.MemoryUsage, error) {
		return usage, nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	capacity := newAgentTunnelCapacityRuntime(tunnel.MaxConcurrentAgentRequestsLimit, true, controller)
	policy, err := newAgentDestinationPolicy(nil, true)
	if err != nil {
		t.Fatal(err)
	}
	agentDone := make(chan error, 1)
	go func() {
		agentDone <- connectAndServe(
			runCtx, http.DefaultClient, management.URL, "load-agent", "Load agent", "token",
			policy, tunnel.DefaultMaxStreamWindowSizeBytes, tunnel.MaxConcurrentAgentRequestsLimit, true, capacity,
		)
	}()

	var serverSession *yamux.Session
	select {
	case serverSession = <-sessionReady:
	case err := <-agentDone:
		t.Fatalf("agent failed before handshake: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for negotiated production agent session")
	}

	var peak atomic.Int64
	monitorDone := make(chan struct{})
	go func() {
		ticker := time.NewTicker(2 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-monitorDone:
				return
			case <-ticker.C:
				current := int64(capacity.snapshot().InUse)
				for observed := peak.Load(); current > observed && !peak.CompareAndSwap(observed, current); observed = peak.Load() {
				}
			}
		}
	}()

	errorsByRequest := make(chan error, requestCount)
	var requests sync.WaitGroup
	interval := time.Second / requestRate
	started := time.Now()
	for index := range requestCount {
		due := started.Add(time.Duration(index) * interval)
		if wait := time.Until(due); wait > 0 {
			time.Sleep(wait)
		}
		requests.Add(1)
		go func(index int) {
			defer requests.Done()
			stream, openErr := serverSession.Open()
			if openErr != nil {
				errorsByRequest <- fmt.Errorf("open stream: %w", openErr)
				return
			}
			defer stream.Close()
			_ = stream.SetDeadline(time.Now().Add(10 * time.Second))
			if writeErr := tunnel.WriteOpenRequest(stream, tunnel.NewOpenRequest(strconv.Itoa(index), "tcp", upstream.Addr().String())); writeErr != nil {
				errorsByRequest <- fmt.Errorf("write open request: %w", writeErr)
				return
			}
			response, readErr := tunnel.ReadOpenResponse(stream)
			if readErr != nil {
				errorsByRequest <- fmt.Errorf("read open response: %w", readErr)
				return
			}
			if !response.OK {
				errorsByRequest <- fmt.Errorf("open rejected: %s: %s", response.ErrorKind, response.Error)
				return
			}
			delayMillis := 800 + (index%18)*100
			if _, writeErr := fmt.Fprintf(stream, "%d\n", delayMillis); writeErr != nil {
				errorsByRequest <- fmt.Errorf("write upstream payload: %w", writeErr)
				return
			}
			line, readErr := bufio.NewReader(stream).ReadString('\n')
			if readErr != nil || line != "ok\n" {
				errorsByRequest <- fmt.Errorf("upstream response %q: %w", line, readErr)
				return
			}
			errorsByRequest <- nil
		}(index)
	}
	requests.Wait()
	close(monitorDone)
	close(errorsByRequest)
	for requestErr := range errorsByRequest {
		if requestErr != nil {
			t.Fatal(requestErr)
		}
	}
	if got := peak.Load(); got <= tunnel.DefaultMaxConcurrentAgentRequests {
		t.Fatalf("peak adaptive concurrency = %d, want above legacy %d", got, tunnel.DefaultMaxConcurrentAgentRequests)
	} else {
		t.Logf("served %d requests at %d req/s with 800-2500ms lifetimes; peak live agent streams=%d", requestCount, requestRate, got)
	}
	if snapshot := capacity.snapshot(); snapshot.Pressure != sysmetrics.MemoryPressureHealthy || snapshot.RejectedPressure != 0 || snapshot.RejectedFixedLimit != 0 {
		t.Fatalf("production load capacity snapshot = %+v", snapshot)
	} else if snapshot.Maximum != int(tunnel.MaxAdaptiveConcurrentStreamsLimit) {
		t.Fatalf("negotiated adaptive guard = %d, want %d", snapshot.Maximum, tunnel.MaxAdaptiveConcurrentStreamsLimit)
	}

	// Compose the production handshake, real Yamux stream, live resource
	// transition, structured rejection, preservation of in-flight work, and
	// recovery in one test. Unit tests cover these pieces independently; this
	// protects the wiring between them.
	held, err := serverSession.Open()
	if err != nil {
		t.Fatal(err)
	}
	defer held.Close()
	_ = held.SetDeadline(time.Now().Add(10 * time.Second))
	if err := tunnel.WriteOpenRequest(held, tunnel.NewOpenRequest("held-through-pressure", "tcp", upstream.Addr().String())); err != nil {
		t.Fatal(err)
	}
	if response, err := tunnel.ReadOpenResponse(held); err != nil || !response.OK {
		t.Fatalf("held stream open response = %+v, err=%v", response, err)
	}
	if _, err := io.WriteString(held, "800\n"); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(time.Second)
	for capacity.snapshot().InUse < 1 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	usage.UsedBytes = 470 << 20
	if pressured := capacity.forceRefresh(); pressured.Pressure != sysmetrics.MemoryPressureCritical || !pressured.Adaptive {
		t.Fatalf("production critical transition = %+v", pressured)
	}
	blocked, err := serverSession.Open()
	if err != nil {
		t.Fatal(err)
	}
	_ = blocked.SetDeadline(time.Now().Add(5 * time.Second))
	if err := tunnel.WriteOpenRequest(blocked, tunnel.NewOpenRequest("blocked-by-pressure", "tcp", upstream.Addr().String())); err != nil {
		t.Fatal(err)
	}
	blockedResponse, err := tunnel.ReadOpenResponse(blocked)
	_ = blocked.Close()
	if err != nil || blockedResponse.OK || blockedResponse.ErrorKind != "agent_resource_pressure" {
		t.Fatalf("production pressure response = %+v, err=%v", blockedResponse, err)
	}
	if line, err := bufio.NewReader(held).ReadString('\n'); err != nil || line != "ok\n" {
		t.Fatalf("in-flight stream across pressure = %q, err=%v", line, err)
	}
	usage.UsedBytes = 300 << 20
	if recovered := capacity.forceRefresh(); recovered.Pressure != sysmetrics.MemoryPressureHealthy || recovered.AdmissionLimit <= recovered.InUse {
		t.Fatalf("production recovery = %+v", recovered)
	}
	recoveredStream, err := serverSession.Open()
	if err != nil {
		t.Fatal(err)
	}
	_ = recoveredStream.SetDeadline(time.Now().Add(5 * time.Second))
	if err := tunnel.WriteOpenRequest(recoveredStream, tunnel.NewOpenRequest("after-recovery", "tcp", upstream.Addr().String())); err != nil {
		t.Fatal(err)
	}
	if response, err := tunnel.ReadOpenResponse(recoveredStream); err != nil || !response.OK {
		t.Fatalf("recovered stream response = %+v, err=%v", response, err)
	}
	_, _ = io.WriteString(recoveredStream, "800\n")
	if line, err := bufio.NewReader(recoveredStream).ReadString('\n'); err != nil || line != "ok\n" {
		t.Fatalf("recovered stream result = %q, err=%v", line, err)
	}
	_ = recoveredStream.Close()

	cancel()
	_ = serverSession.Close()
	select {
	case serveErr := <-agentDone:
		if serveErr != nil && !errors.Is(serveErr, context.Canceled) {
			t.Fatalf("agent shutdown: %v", serveErr)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("agent did not stop after load test")
	}
}

func TestAdaptiveAgentCapacityThrottlesAndRecoversWithoutRevokingWork(t *testing.T) {
	usage := sysmetrics.MemoryUsage{UsedBytes: 64 << 20, LimitBytes: 512 << 20, Source: "test"}
	capacity := newTestAgentAdaptiveCapacity(t, &usage)
	firstRelease, _, ok := capacity.tryAcquire()
	if !ok {
		t.Fatal("initial healthy admission failed")
	}

	usage.UsedBytes = 470 << 20
	critical := capacity.forceRefresh()
	if critical.Pressure != sysmetrics.MemoryPressureCritical || critical.AdmissionLimit != 1 {
		t.Fatalf("critical snapshot = %+v", critical)
	}
	if _, rejected, ok := capacity.tryAcquire(); ok || rejected.RejectedPressure != 1 {
		t.Fatalf("critical acquire = ok %t snapshot %+v", ok, rejected)
	}
	if got := capacity.snapshot().InUse; got != 1 {
		t.Fatalf("critical transition revoked existing work: in-use=%d", got)
	}

	usage.UsedBytes = 300 << 20
	recovered := capacity.forceRefresh()
	if recovered.Pressure != sysmetrics.MemoryPressureHealthy || recovered.AdmissionLimit <= recovered.InUse {
		t.Fatalf("recovered snapshot = %+v", recovered)
	}
	secondRelease, _, ok := capacity.tryAcquire()
	if !ok {
		t.Fatal("admission did not recover")
	}
	secondRelease()
	firstRelease()
}

func TestAdaptiveAgentCapacityScavengesAfterEachPressuredDrain(t *testing.T) {
	usage := sysmetrics.MemoryUsage{UsedBytes: 64 << 20, LimitBytes: 512 << 20, Source: "test"}
	capacity := newTestAgentAdaptiveCapacity(t, &usage)
	scavenged := make(chan struct{}, 2)
	capacity.freeOSMemory = func() { scavenged <- struct{}{} }
	release, _, ok := capacity.tryAcquire()
	if !ok {
		t.Fatal("initial admission failed")
	}
	usage.UsedBytes = 470 << 20
	capacity.forceRefresh()
	release()
	select {
	case <-scavenged:
	case <-time.After(time.Second):
		t.Fatal("pressured drain did not request memory scavenging")
	}
	deadline := time.Now().Add(time.Second)
	for capacity.scavengeRunning.Load() && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}

	usage.UsedBytes = 64 << 20
	capacity.forceRefresh()
	release, _, ok = capacity.tryAcquire()
	if !ok {
		t.Fatal("recovered admission failed")
	}
	usage.UsedBytes = 470 << 20
	capacity.forceRefresh()
	release()
	select {
	case <-scavenged:
	case <-time.After(time.Second):
		t.Fatal("second pressured drain did not request memory scavenging")
	}
}

func TestAdaptiveAgentCapacityScavengesHeadroomRejectionBelowSoftThreshold(t *testing.T) {
	usage := sysmetrics.MemoryUsage{UsedBytes: 400 << 20, LimitBytes: 512 << 20, Source: "test"}
	config := sysmetrics.DefaultAdaptiveMemoryConfig()
	config.SampleInterval = time.Hour
	controller, err := sysmetrics.NewAdaptiveMemoryController(config, sysmetrics.MemoryUsageSamplerFunc(func() (sysmetrics.MemoryUsage, error) {
		return usage, nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	capacity := newAgentTunnelCapacityRuntime(64, true, controller)
	scavenged := make(chan struct{}, 1)
	capacity.freeOSMemory = func() { scavenged <- struct{}{} }
	initial := capacity.forceRefresh()
	if initial.Pressure != sysmetrics.MemoryPressureHealthy || initial.AdmissionLimit < 1 || initial.AdmissionLimit >= 64 {
		t.Fatalf("healthy headroom-limited snapshot = %+v", initial)
	}
	releases := make([]func(), 0, initial.AdmissionLimit)
	for index := 0; index < initial.AdmissionLimit; index++ {
		release, _, ok := capacity.tryAcquire()
		if !ok {
			t.Fatalf("admission %d of %d failed", index+1, initial.AdmissionLimit)
		}
		releases = append(releases, release)
	}
	if _, rejected, ok := capacity.tryAcquire(); ok || rejected.Pressure != sysmetrics.MemoryPressureHealthy {
		t.Fatalf("healthy headroom rejection = %t snapshot %+v", ok, rejected)
	}
	for _, release := range releases {
		release()
	}
	select {
	case <-scavenged:
	case <-time.After(time.Second):
		t.Fatal("headroom-constrained healthy generation was not scavenged after drain")
	}
}

func TestAdaptiveAgentCapacityReplaysScavengeRequestedDuringActivePass(t *testing.T) {
	usage := sysmetrics.MemoryUsage{UsedBytes: 64 << 20, LimitBytes: 512 << 20, Source: "test"}
	capacity := newTestAgentAdaptiveCapacity(t, &usage)
	started := make(chan int, 2)
	unblockFirst := make(chan struct{})
	var calls atomic.Int64
	capacity.freeOSMemory = func() {
		call := int(calls.Add(1))
		started <- call
		if call == 1 {
			<-unblockFirst
		}
	}

	firstRelease, _, ok := capacity.tryAcquire()
	if !ok {
		t.Fatal("first admission failed")
	}
	usage.UsedBytes = 470 << 20
	capacity.forceRefresh()
	firstRelease()
	select {
	case call := <-started:
		if call != 1 {
			t.Fatalf("first scavenge call = %d", call)
		}
	case <-time.After(time.Second):
		t.Fatal("first scavenge did not start")
	}

	// Simulate a second constrained generation draining while the first pass
	// is still inside FreeOSMemory. The pending signal must not be lost.
	capacity.scavengeNeeded.Store(true)
	if capacity.requestMemoryScavenge() {
		t.Fatal("overlapping request unexpectedly started another worker")
	}
	close(unblockFirst)
	select {
	case call := <-started:
		if call != 2 {
			t.Fatalf("replayed scavenge call = %d", call)
		}
	case <-time.After(time.Second):
		t.Fatal("pending scavenge was lost after active pass")
	}
	deadline := time.Now().Add(time.Second)
	for capacity.scavengeRunning.Load() && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if capacity.scavengeRunning.Load() || capacity.scavengeNeeded.Load() {
		t.Fatalf("scavenge state did not clear: running=%t needed=%t", capacity.scavengeRunning.Load(), capacity.scavengeNeeded.Load())
	}
}

func TestProductionAgentHeldStreamsRespect512MiBResourceEnvelope(t *testing.T) {
	if os.Getenv("P2PSTREAM_RUN_RESOURCE_STRESS") != "1" {
		t.Skip("set P2PSTREAM_RUN_RESOURCE_STRESS=1 and run in a 512 MiB cgroup")
	}
	// 100 requests/second with every request held for the worst-case 2.5s
	// requires 250 concurrent streams. Keep a six-stream scheduling margin while
	// filling every bounded Yamux receive window in three repeated bursts.
	const heldStreams = 256
	previousLogLevel := zerolog.GlobalLevel()
	zerolog.SetGlobalLevel(zerolog.WarnLevel)
	t.Cleanup(func() { zerolog.SetGlobalLevel(previousLogLevel) })

	memoryEventsBefore := currentCgroupMemoryEvents(t)

	runCtx, cancel := context.WithCancel(t.Context())
	defer cancel()
	sessionReady := make(chan *yamux.Session, 1)
	management := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hijacker, ok := w.(http.Hijacker)
		if !ok {
			http.Error(w, "hijack unavailable", http.StatusInternalServerError)
			return
		}
		conn, rw, hijackErr := hijacker.Hijack()
		if hijackErr != nil {
			return
		}
		_, _ = fmt.Fprintf(rw, "HTTP/1.1 101 Switching Protocols\r\nConnection: Upgrade\r\nUpgrade: %s\r\n%s: %s\r\n%s: %d\r\n\r\n",
			tunnel.UpgradeToken,
			tunnel.TunnelCapacityModeHeader, tunnel.TunnelCapacityModeAdaptive,
			tunnel.TunnelMaxConcurrentStreamsHeader, tunnel.MaxAdaptiveConcurrentStreamsLimit,
		)
		if err := rw.Flush(); err != nil {
			_ = conn.Close()
			return
		}
		window, err := tunnel.AdaptiveMaxStreamWindowSizeBytes(0, tunnel.DefaultAdaptiveStreamChargeBytes)
		if err != nil {
			_ = conn.Close()
			return
		}
		cfg, err := tunnel.NewYamuxConfig(nil, window)
		if err != nil {
			_ = conn.Close()
			return
		}
		serverSession, err := yamux.Server(conn, cfg)
		if err != nil {
			_ = conn.Close()
			return
		}
		sessionReady <- serverSession
		<-runCtx.Done()
		_ = serverSession.Close()
	}))
	t.Cleanup(management.Close)

	controller, err := sysmetrics.NewAdaptiveMemoryController(sysmetrics.DefaultAdaptiveMemoryConfig(), nil)
	if err != nil {
		t.Fatal(err)
	}
	capacity := newAgentTunnelCapacityRuntime(tunnel.MaxAdaptiveConcurrentStreamsLimit, true, controller)
	initial := capacity.forceRefresh()
	if initial.AdmissionLimit <= heldStreams {
		t.Fatalf("512 MiB resource fixture starts with snapshot %+v, need allowance above %d", initial, heldStreams)
	}
	policy, err := newAgentDestinationPolicy(nil, true)
	if err != nil {
		t.Fatal(err)
	}
	agentDone := make(chan error, 1)
	go func() {
		agentDone <- connectAndServe(
			runCtx, http.DefaultClient, management.URL, "resource-agent", "Resource agent", "token",
			policy, tunnel.DefaultMaxStreamWindowSizeBytes, tunnel.MaxConcurrentAgentRequestsLimit, true, capacity,
		)
	}()
	var session *yamux.Session
	select {
	case session = <-sessionReady:
	case err := <-agentDone:
		t.Fatalf("agent failed before resource test: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for resource-test tunnel")
	}

	runBurst := func(burst int) agentTunnelCapacitySnapshot {
		t.Helper()
		releaseUpstreams := make(chan struct{})
		upstream, listenErr := net.Listen("tcp", "127.0.0.1:0")
		if listenErr != nil {
			t.Fatal(listenErr)
		}
		var upstreamHandlers sync.WaitGroup
		acceptDone := make(chan struct{})
		go func() {
			defer close(acceptDone)
			for {
				conn, acceptErr := upstream.Accept()
				if acceptErr != nil {
					return
				}
				upstreamHandlers.Add(1)
				go func() {
					defer upstreamHandlers.Done()
					defer conn.Close()
					<-releaseUpstreams
					_, _ = io.Copy(io.Discard, conn)
				}()
			}
		}()

		streams := make([]net.Conn, 0, heldStreams)
		for index := range heldStreams {
			stream, openErr := session.Open()
			if openErr != nil {
				t.Fatalf("burst %d open held stream %d: %v", burst, index, openErr)
			}
			_ = stream.SetDeadline(time.Now().Add(20 * time.Second))
			requestID := strconv.Itoa(burst) + "-" + strconv.Itoa(index)
			if writeErr := tunnel.WriteOpenRequest(stream, tunnel.NewOpenRequest(requestID, "tcp", upstream.Addr().String())); writeErr != nil {
				t.Fatal(writeErr)
			}
			response, readErr := tunnel.ReadOpenResponse(stream)
			if readErr != nil || !response.OK {
				t.Fatalf("burst %d held stream %d response=%+v err=%v", burst, index, response, readErr)
			}
			streams = append(streams, stream)
		}

		payload := bytes.Repeat([]byte{'x'}, int(tunnel.DefaultAdaptiveStreamChargeBytes-tunnel.AdaptivePerStreamOverheadBytes))
		var writers sync.WaitGroup
		for _, stream := range streams {
			writers.Add(1)
			go func(stream net.Conn) {
				defer writers.Done()
				_, _ = stream.Write(payload)
			}(stream)
		}
		time.Sleep(500 * time.Millisecond)
		pressured := capacity.forceRefresh()
		blocked, openErr := session.Open()
		if openErr != nil {
			t.Fatal(openErr)
		}
		_ = blocked.SetDeadline(time.Now().Add(5 * time.Second))
		if writeErr := tunnel.WriteOpenRequest(blocked, tunnel.NewOpenRequest("pressure-proof-"+strconv.Itoa(burst), "tcp", upstream.Addr().String())); writeErr != nil {
			t.Fatal(writeErr)
		}
		response, readErr := tunnel.ReadOpenResponse(blocked)
		_ = blocked.Close()
		if readErr != nil || response.OK || response.ErrorKind != "agent_resource_pressure" {
			t.Fatalf("burst %d pressure response=%+v err=%v snapshot=%+v", burst, response, readErr, pressured)
		}
		if pressured.MemoryLimitBytes > 0 {
			hardBytes := pressured.MemoryLimitBytes * 90 / 100
			if pressured.MemoryUsedBytes >= hardBytes {
				t.Fatalf("burst %d crossed 90%% hard watermark: %+v", burst, pressured)
			}
		}
		t.Logf("burst=%d held=%d allowance=%d memory=%d/%d fds=%d/%d pressure=%s reason=%s",
			burst, heldStreams, pressured.AdmissionLimit, pressured.MemoryUsedBytes, pressured.MemoryLimitBytes,
			pressured.FileDescriptorsUsed, pressured.FileDescriptorsLimit, pressured.Pressure, pressured.PressureReason)

		close(releaseUpstreams)
		for _, stream := range streams {
			_ = stream.Close()
		}
		writers.Wait()
		_ = upstream.Close()
		<-acceptDone
		upstreamHandlers.Wait()
		deadline := time.Now().Add(10 * time.Second)
		for capacity.snapshot().InUse != 0 && time.Now().Before(deadline) {
			time.Sleep(10 * time.Millisecond)
		}
		if got := capacity.snapshot().InUse; got != 0 {
			t.Fatalf("burst %d held streams leaked after drain: %d", burst, got)
		}
		return pressured
	}

	for burst := 1; burst <= 3; burst++ {
		if burst > 1 {
			deadline := time.Now().Add(15 * time.Second)
			for {
				recovered := capacity.forceRefresh()
				if recovered.InUse == 0 && recovered.AdmissionLimit > heldStreams && recovered.Pressure == sysmetrics.MemoryPressureHealthy {
					break
				}
				if time.Now().After(deadline) {
					t.Fatalf("resource usage did not recover before burst %d: %+v", burst, recovered)
				}
				time.Sleep(100 * time.Millisecond)
			}
		}
		runBurst(burst)
	}
	recoveryDeadline := time.Now().Add(15 * time.Second)
	for {
		recovered := capacity.forceRefresh()
		if recovered.InUse == 0 && recovered.AdmissionLimit > heldStreams && recovered.Pressure == sysmetrics.MemoryPressureHealthy {
			break
		}
		if time.Now().After(recoveryDeadline) {
			t.Fatalf("resource usage did not recover after final burst: %+v", recovered)
		}
		time.Sleep(100 * time.Millisecond)
	}
	assertNoCgroupMemoryEventIncrease(t, memoryEventsBefore, currentCgroupMemoryEvents(t), "max", "oom", "oom_kill")
	cancel()
	_ = session.Close()
	select {
	case err := <-agentDone:
		if err != nil && !errors.Is(err, context.Canceled) {
			t.Fatalf("agent shutdown: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("resource-test agent did not stop")
	}
}

func TestAdaptiveAgentCapacityConcurrentAcquireReleaseConservesLeases(t *testing.T) {
	usage := sysmetrics.MemoryUsage{UsedBytes: 64 << 20, LimitBytes: 512 << 20, Source: "test"}
	capacity := newTestAgentAdaptiveCapacity(t, &usage)
	capacity.forceRefresh()
	var wg sync.WaitGroup
	for range 64 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 500 {
				release, _, ok := capacity.tryAcquire()
				if ok {
					release()
				}
			}
		}()
	}
	wg.Wait()
	if got := capacity.snapshot().InUse; got != 0 {
		t.Fatalf("in-use after concurrent churn = %d, want 0", got)
	}
}

func currentCgroupMemoryEvents(t testing.TB) map[string]uint64 {
	t.Helper()
	cgroupData, err := os.ReadFile("/proc/self/cgroup")
	if err != nil {
		return nil
	}
	var processPath string
	for _, line := range strings.Split(string(cgroupData), "\n") {
		if strings.HasPrefix(line, "0::") {
			processPath = strings.TrimPrefix(line, "0::")
			break
		}
	}
	if processPath == "" {
		return nil
	}
	mountData, err := os.ReadFile("/proc/self/mountinfo")
	if err != nil {
		return nil
	}
	var eventsPath string
	for _, line := range strings.Split(string(mountData), "\n") {
		before, after, ok := strings.Cut(line, " - ")
		if !ok || !strings.HasPrefix(after, "cgroup2 ") {
			continue
		}
		fields := strings.Fields(before)
		if len(fields) < 5 {
			continue
		}
		root := filepath.Clean(fields[3])
		relative, relErr := filepath.Rel(root, filepath.Clean(processPath))
		if relErr != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			continue
		}
		eventsPath = filepath.Join(fields[4], relative, "memory.events")
		break
	}
	if eventsPath == "" {
		return nil
	}
	data, err := os.ReadFile(eventsPath)
	if err != nil {
		return nil
	}
	events := make(map[string]uint64)
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 {
			continue
		}
		value, parseErr := strconv.ParseUint(fields[1], 10, 64)
		if parseErr != nil {
			t.Fatalf("parse cgroup memory event %q: %v", line, parseErr)
		}
		events[fields[0]] = value
	}
	return events
}

func assertNoCgroupMemoryEventIncrease(t testing.TB, before, after map[string]uint64, names ...string) {
	t.Helper()
	if before == nil || after == nil {
		t.Log("cgroup memory.events unavailable; skipping event-delta assertion")
		return
	}
	for _, name := range names {
		if after[name] != before[name] {
			t.Fatalf("cgroup memory.events %s changed from %d to %d", name, before[name], after[name])
		}
	}
}

func TestAdaptiveAgentSessionReturnsStructuredResourcePressure(t *testing.T) {
	usage := sysmetrics.MemoryUsage{UsedBytes: 470 << 20, LimitBytes: 512 << 20, Source: "test"}
	capacity := newTestAgentAdaptiveCapacity(t, &usage)
	capacity.forceRefresh()

	agentConn, serverConn := net.Pipe()
	agentSession, err := yamux.Client(agentConn, tunnel.DefaultYamuxConfig(nil))
	if err != nil {
		t.Fatal(err)
	}
	serverSession, err := yamux.Server(serverConn, tunnel.DefaultYamuxConfig(nil))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- serveTunnelSessionWithPolicyAndCapacity(ctx, agentSession, nil, capacity) }()
	t.Cleanup(func() {
		cancel()
		_ = serverSession.Close()
		_ = agentSession.Close()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Error("adaptive session did not stop")
		}
	})

	stream, err := serverSession.Open()
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()
	if err := tunnel.WriteOpenRequest(stream, tunnel.NewOpenRequest("pressure", "tcp", "127.0.0.1:1")); err != nil {
		t.Fatal(err)
	}
	response, err := tunnel.ReadOpenResponse(stream)
	if err != nil {
		t.Fatal(err)
	}
	if response.OK || response.ErrorKind != "agent_resource_pressure" {
		t.Fatalf("pressure response = %+v, want agent_resource_pressure", response)
	}
}

func BenchmarkAdaptiveAgentCapacityAcquireRelease(b *testing.B) {
	usage := sysmetrics.MemoryUsage{UsedBytes: 64 << 20, LimitBytes: 512 << 20, Source: "benchmark"}
	capacity := newTestAgentAdaptiveCapacity(b, &usage)
	capacity.forceRefresh()
	b.ReportAllocs()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			release, _, ok := capacity.tryAcquire()
			if ok {
				release()
			}
		}
	})
}

func BenchmarkFixedAgentCapacityAcquireRelease(b *testing.B) {
	capacity := newAgentTunnelCapacityRuntime(2048, false, nil)
	b.ReportAllocs()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			release, _, ok := capacity.tryAcquire()
			if ok {
				release()
			}
		}
	})
}
