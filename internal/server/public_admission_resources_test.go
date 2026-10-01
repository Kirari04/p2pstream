package server

import (
	"bufio"
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"p2pstream/internal/sysmetrics"
)

func TestPublicAdmissionIdleReclaimRecoversDirectSocketReservation(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("ok"))
	}))
	defer upstream.Close()

	app := NewApp(nil, nil)
	usage := sysmetrics.MemoryUsage{
		// The 90% hard watermark leaves exactly 3.75 MiB of headroom, or
		// three default 1.25 MiB admission units. The protected control slot
		// leaves two units for public external owners.
		UsedBytes:  (896 << 20) + (256 << 10),
		LimitBytes: 1000 << 20,
		Source:     "test",
	}
	app.agentStreamCapacity = newAdaptiveServerCapacityForTest(t, 65_536, &usage)
	target := directTransportPoolTestTarget(t, 71, upstream.URL, time.Second)
	transport := app.directTargetTransport(target)
	request, _ := http.NewRequestWithContext(t.Context(), http.MethodGet, upstream.URL, nil)
	response, err := transport.RoundTrip(request)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, response.Body)
	_ = response.Body.Close()
	if got := app.agentStreamCapacity.snapshot().AdaptiveExternalBytes; got != 768<<10 {
		t.Fatalf("idle direct reservation = %d, want %d", got, 768<<10)
	}

	firstRelease, _, constrained := app.tryReservePublicResource(2<<20, 0)
	if !constrained {
		t.Fatal("adaptive reservation unexpectedly unconstrained")
	}
	if firstRelease != nil {
		firstRelease()
	}
	waitForAdaptiveExternalBytes(t, app, 0)
	release, ok, constrained := app.tryReservePublicResource(2<<20, 0)
	if !constrained || !ok {
		t.Fatalf("later reservation after asynchronous idle reclaim = ok %t constrained %t snapshot=%+v", ok, constrained, app.agentStreamCapacity.snapshot())
	}
	if got := app.agentStreamCapacity.snapshot().AdaptiveExternalBytes; got != 2<<20 {
		t.Fatalf("post-reclaim reservation = %d, want %d", got, 2<<20)
	}
	release()
	app.DirectTransports.closeAll()
	if got := app.agentStreamCapacity.snapshot().AdaptiveExternalBytes; got != 0 {
		t.Fatalf("released reservation = %d, want zero", got)
	}
}

func TestPublicAdmissionReclaimCooldownBoundsSequentialMisses(t *testing.T) {
	usage := sysmetrics.MemoryUsage{UsedBytes: 470 << 20, LimitBytes: 512 << 20, Source: "test"}
	now := time.Unix(1_000, 0)
	app := &App{
		agentStreamCapacity:       newAdaptiveServerCapacityForTest(t, 64, &usage),
		AgentTransports:           newAgentTransportPool(),
		publicAdmissionReclaimNow: func() time.Time { return now },
	}
	closeStarted, unblock := installBlockingIdleDirectTransport(t, app)
	defer closeTestChannel(unblock)
	if _, ok, constrained := app.tryReservePublicResource(1, 0); ok || !constrained {
		t.Fatalf("critical admission = ok %t constrained %t", ok, constrained)
	}
	select {
	case <-closeStarted:
	case <-time.After(time.Second):
		t.Fatal("asynchronous idle sweep did not reach blocking close")
	}
	var wg sync.WaitGroup
	for range 32 {
		wg.Go(func() {
			if _, ok, constrained := app.tryReservePublicResource(1, 0); ok || !constrained {
				t.Errorf("concurrent critical admission = ok %t constrained %t", ok, constrained)
			}
		})
	}
	wg.Wait()
	if got := app.AgentTransports.stats().ReclaimAttempts; got != 1 {
		t.Fatalf("concurrent in-flight reclaim attempts = %d, want 1", got)
	}
	closeTestChannel(unblock)
	waitForPublicAdmissionReclaimIdle(t, app)
	if _, ok, _ := app.tryReservePublicResource(1, 0); ok {
		t.Fatal("critical admission recovered without resource recovery")
	}
	waitForPublicAdmissionReclaimIdle(t, app)
	if got := app.AgentTransports.stats().ReclaimAttempts; got != 1 {
		t.Fatalf("cooldown reclaim attempts = %d, want 1", got)
	}
	now = now.Add(publicAdmissionReclaimCooldown)
	if _, ok, _ := app.tryReservePublicResource(1, 0); ok {
		t.Fatal("critical admission recovered without resource recovery")
	}
	waitForPublicAdmissionReclaimIdle(t, app)
	if got := app.AgentTransports.stats().ReclaimAttempts; got != 2 {
		t.Fatalf("next-window reclaim attempts = %d, want 2", got)
	}
}

func TestPublicAdmissionMissDoesNotWaitForSweepLock(t *testing.T) {
	usage := sysmetrics.MemoryUsage{UsedBytes: 470 << 20, LimitBytes: 512 << 20, Source: "test"}
	app := &App{agentStreamCapacity: newAdaptiveServerCapacityForTest(t, 64, &usage)}
	app.publicAdmissionReclaimMu.Lock()
	defer app.publicAdmissionReclaimMu.Unlock()

	done := make(chan struct{})
	go func() {
		_, _, _ = app.tryReservePublicResource(1, 0)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(100 * time.Millisecond):
		t.Fatal("public admission waited behind an in-progress idle sweep")
	}
}

func TestPublicListenerAdmissionDoesNotWaitForBlockingIdleClose(t *testing.T) {
	usage := sysmetrics.MemoryUsage{UsedBytes: 470 << 20, LimitBytes: 512 << 20, Source: "test"}
	app := &App{
		agentStreamCapacity: newAdaptiveServerCapacityForTest(t, 64, &usage),
		AgentTransports:     newAgentTransportPool(),
	}
	closeStarted, unblock := installBlockingIdleDirectTransport(t, app)
	defer closeTestChannel(unblock)
	queued := &queuedPublicTestListener{connections: make(chan net.Conn, 2)}
	first, firstPeer := net.Pipe()
	defer firstPeer.Close()
	second, secondPeer := net.Pipe()
	defer secondPeer.Close()
	queued.connections <- first
	queued.connections <- second
	var calls atomic.Int32
	listener := resourceBoundedPublicListener{
		Listener: queued,
		acquire: func(net.Conn) (func(), bool) {
			if calls.Add(1) == 1 {
				release, ok, _ := app.tryReservePublicResource(1, 0)
				return release, ok
			}
			return func() {}, true
		},
	}
	accepted := make(chan net.Conn, 1)
	errs := make(chan error, 1)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			errs <- err
			return
		}
		accepted <- conn
	}()
	select {
	case conn := <-accepted:
		_ = conn.Close()
	case err := <-errs:
		t.Fatal(err)
	case <-time.After(200 * time.Millisecond):
		t.Fatal("public listener accept waited for blocking idle connection close")
	}
	select {
	case <-closeStarted:
	case <-time.After(time.Second):
		t.Fatal("listener miss did not start the asynchronous idle sweep")
	}
}

func TestPublicAdmissionFailsClosedWhenResourceSensorIsDegraded(t *testing.T) {
	manager := mustNewDefaultAgentStreamCapacityManager(64)
	config := sysmetrics.DefaultAdaptiveMemoryConfig()
	controller, err := sysmetrics.NewAdaptiveMemoryController(config, sysmetrics.MemoryUsageSamplerFunc(func() (sysmetrics.MemoryUsage, error) {
		return sysmetrics.MemoryUsage{}, errors.New("sensor unavailable")
	}))
	if err != nil {
		t.Fatal(err)
	}
	manager.enableAdaptiveMemory(controller)
	app := &App{agentStreamCapacity: manager}
	if _, ok, constrained := app.tryReservePublicResource(1, 0); ok || !constrained {
		t.Fatalf("degraded-sensor admission = ok %t constrained %t", ok, constrained)
	}
	if snapshot := manager.snapshot(); !snapshot.Adaptive || snapshot.MemoryPressure != "unknown" || snapshot.ResourceSampleError == "" {
		t.Fatalf("degraded-sensor snapshot = %+v", snapshot)
	}
}

func TestDirectIdleReclaimRotatesPastActiveOldestTransport(t *testing.T) {
	activeFinish := make(chan struct{})
	var finishOnce sync.Once
	finish := func() { finishOnce.Do(func() { close(activeFinish) }) }
	activeUpstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("start"))
		w.(http.Flusher).Flush()
		<-activeFinish
		_, _ = w.Write([]byte("done"))
	}))
	defer func() {
		finish()
		activeUpstream.Close()
	}()
	idleUpstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("idle"))
	}))
	defer idleUpstream.Close()

	app := NewApp(nil, nil)
	usage := sysmetrics.MemoryUsage{UsedBytes: 64 << 20, LimitBytes: 512 << 20, Source: "test"}
	app.agentStreamCapacity = newAdaptiveServerCapacityForTest(t, 65_536, &usage)
	activeTarget := directTransportPoolTestTarget(t, 80, activeUpstream.URL, time.Second)
	idleTarget := directTransportPoolTestTarget(t, 81, idleUpstream.URL, time.Second)

	activeRequest, _ := http.NewRequestWithContext(t.Context(), http.MethodGet, activeUpstream.URL, nil)
	activeResponse, err := app.directTargetTransport(activeTarget).RoundTrip(activeRequest)
	if err != nil {
		t.Fatal(err)
	}
	idleRequest, _ := http.NewRequestWithContext(t.Context(), http.MethodGet, idleUpstream.URL, nil)
	idleResponse, err := app.directTargetTransport(idleTarget).RoundTrip(idleRequest)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, idleResponse.Body)
	_ = idleResponse.Body.Close()
	if got := app.agentStreamCapacity.snapshot().AdaptiveExternalBytes; got != 2*(768<<10) {
		t.Fatalf("two direct reservations = %d, want %d", got, 2*(768<<10))
	}

	if !app.DirectTransports.reclaimOldestIdle() {
		t.Fatal("first bounded reclaim found no transport")
	}
	if got := app.agentStreamCapacity.snapshot().AdaptiveExternalBytes; got != 2*(768<<10) {
		t.Fatalf("active-only reclaim released live work: %d", got)
	}
	if !app.DirectTransports.reclaimOldestIdle() {
		t.Fatal("second bounded reclaim found no transport")
	}
	if got := app.agentStreamCapacity.snapshot().AdaptiveExternalBytes; got != 768<<10 {
		t.Fatalf("rotated reclaim left idle reservation: %d", got)
	}

	finish()
	data, err := io.ReadAll(activeResponse.Body)
	if err != nil || string(data) != "startdone" {
		t.Fatalf("active response after reclaim = %q, %v", data, err)
	}
	_ = activeResponse.Body.Close()
	app.DirectTransports.closeAll()
}

func installBlockingIdleDirectTransport(t testing.TB, app *App) (<-chan struct{}, chan struct{}) {
	t.Helper()
	client, server := net.Pipe()
	closeStarted := make(chan struct{})
	unblock := make(chan struct{})
	blocking := &blockingCloseConn{Conn: client, started: closeStarted, unblock: unblock}
	transport := &http.Transport{
		DialContext: func(context.Context, string, string) (net.Conn, error) { return blocking, nil },
	}
	go func() {
		reader := bufio.NewReader(server)
		request, err := http.ReadRequest(reader)
		if err != nil {
			return
		}
		_ = request.Body.Close()
		_, _ = io.WriteString(server, "HTTP/1.1 200 OK\r\nContent-Length: 2\r\n\r\nok")
		_, _ = io.Copy(io.Discard, server)
	}()
	request, _ := http.NewRequest(http.MethodGet, "http://idle.test/", nil)
	response, err := transport.RoundTrip(request)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, response.Body)
	_ = response.Body.Close()
	now := time.Now()
	app.DirectTransports = &directTransportPool{entries: map[directTransportKey]*pooledDirectTransport{
		{RouteTargetID: 1}: {key: directTransportKey{RouteTargetID: 1}, transport: transport, createdAt: now, lastUsed: now},
	}}
	t.Cleanup(func() {
		closeTestChannel(unblock)
		_ = server.Close()
		_ = client.Close()
	})
	return closeStarted, unblock
}

type blockingCloseConn struct {
	net.Conn
	started chan struct{}
	unblock chan struct{}
	once    sync.Once
}

func (c *blockingCloseConn) Close() error {
	c.once.Do(func() {
		close(c.started)
		<-c.unblock
	})
	return c.Conn.Close()
}

type queuedPublicTestListener struct {
	connections chan net.Conn
}

func (l *queuedPublicTestListener) Accept() (net.Conn, error) { return <-l.connections, nil }
func (l *queuedPublicTestListener) Close() error              { return nil }
func (l *queuedPublicTestListener) Addr() net.Addr            { return publicTestAddr("listener") }

type publicTestAddr string

func (a publicTestAddr) Network() string { return "test" }
func (a publicTestAddr) String() string  { return string(a) }

func waitForAdaptiveExternalBytes(t testing.TB, app *App, want int64) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if app.agentStreamCapacity.snapshot().AdaptiveExternalBytes == want {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("adaptive external bytes = %d, want %d", app.agentStreamCapacity.snapshot().AdaptiveExternalBytes, want)
}

func waitForPublicAdmissionReclaimIdle(t testing.TB, app *App) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		app.publicAdmissionReclaimMu.Lock()
		inFlight := app.publicAdmissionReclaimInFlight
		app.publicAdmissionReclaimMu.Unlock()
		if !inFlight {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("public admission reclaim remained in flight")
}

func closeTestChannel(channel chan struct{}) {
	select {
	case <-channel:
	default:
		close(channel)
	}
}
