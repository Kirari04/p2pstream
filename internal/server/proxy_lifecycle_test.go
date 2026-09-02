package server

import (
	"context"
	"net"
	"net/http"
	"testing"
	"time"

	"p2pstream/internal/config"
	"p2pstream/internal/sysmetrics"
)

func TestPublicConnectionCapacityLimiterIsGlobalAndFairPerClient(t *testing.T) {
	limiter := newPublicConnectionCapacityLimiter(3, 2)
	clientA := &net.TCPAddr{IP: net.ParseIP("192.0.2.1"), Port: 1000}
	clientB := &net.TCPAddr{IP: net.ParseIP("192.0.2.2"), Port: 1000}
	releaseA1, ok := limiter.tryAcquire(clientA, 1, -1, -1)
	if !ok {
		t.Fatal("first client A connection rejected")
	}
	releaseA2, ok := limiter.tryAcquire(&net.TCPAddr{IP: clientA.IP, Port: 1001}, 1, -1, -1)
	if !ok {
		t.Fatal("second client A connection rejected")
	}
	if _, ok := limiter.tryAcquire(&net.TCPAddr{IP: clientA.IP, Port: 1002}, 1, -1, -1); ok {
		t.Fatal("client A exceeded its fair connection guard")
	}
	releaseB1, ok := limiter.tryAcquire(clientB, 1, -1, -1)
	if !ok {
		t.Fatal("client B could not use remaining global capacity")
	}
	if _, ok := limiter.tryAcquire(&net.TCPAddr{IP: clientB.IP, Port: 1001}, 1, -1, -1); ok {
		t.Fatal("global connection guard was exceeded")
	}
	releaseA1()
	releaseA1()
	releaseB2, ok := limiter.tryAcquire(&net.TCPAddr{IP: clientB.IP, Port: 1001}, 1, -1, -1)
	if !ok {
		t.Fatal("released global capacity was not reusable")
	}
	releaseA2()
	releaseB1()
	releaseB2()
}

func TestPublicConnectionCapacityLimiterLeavesDynamicResourceShareForAnotherPeer(t *testing.T) {
	limiter := newPublicConnectionCapacityLimiter(0, 256)
	clientA := &net.TCPAddr{IP: net.ParseIP("192.0.2.10"), Port: 1000}
	clientB := &net.TCPAddr{IP: net.ParseIP("192.0.2.11"), Port: 1000}
	const charge = int64(640 << 10)
	const peerBudget = 2 * charge
	first, ok := limiter.tryAcquire(clientA, charge, peerBudget, 2)
	if !ok {
		t.Fatal("first peer A reservation rejected")
	}
	second, ok := limiter.tryAcquire(clientA, charge, peerBudget, 2)
	if !ok {
		t.Fatal("second peer A reservation rejected")
	}
	if _, ok := limiter.tryAcquire(clientA, charge, peerBudget, 2); ok {
		t.Fatal("peer A consumed the public resource share reserved for peers")
	}
	other, ok := limiter.tryAcquire(clientB, charge, peerBudget, 2)
	if !ok {
		t.Fatal("peer B could not use its independent resource share")
	}
	first()
	second()
	other()
}

func TestPublicConnectionChargesConfiguredHeaderAndReleasesLedger(t *testing.T) {
	usage := sysmetrics.MemoryUsage{UsedBytes: 64 << 20, LimitBytes: 512 << 20, Source: "test"}
	manager := newAdaptiveServerCapacityForTest(t, 256, &usage)
	app := &App{
		Config:              &config.Config{PublicMaxHeaderBytes: 1 << 20},
		agentStreamCapacity: manager,
		publicConnections:   newPublicConnectionCapacityLimiter(64, 16),
	}
	client, peer := net.Pipe()
	defer client.Close()
	defer peer.Close()
	release, ok := app.tryReservePublicConnection(client)
	if !ok {
		t.Fatal("public connection reservation rejected")
	}
	snapshot := manager.snapshot()
	if want := int64(512*1024 + 2*(1<<20)); snapshot.AdaptiveExternalBytes != want || snapshot.AdaptiveExternalFDs != 1 {
		t.Fatalf("external reservation = %d bytes/%d FDs, want %d/1", snapshot.AdaptiveExternalBytes, snapshot.AdaptiveExternalFDs, want)
	}
	release()
	release()
	snapshot = manager.snapshot()
	if snapshot.AdaptiveExternalBytes != 0 || snapshot.AdaptiveExternalFDs != 0 {
		t.Fatalf("released external reservation = %d bytes/%d FDs, want zero", snapshot.AdaptiveExternalBytes, snapshot.AdaptiveExternalFDs)
	}
}

func TestPublicConnectionDynamicPeerShareSurvivesGlobalResourceAdmission(t *testing.T) {
	for _, test := range []struct {
		name           string
		maxHeaderBytes int
	}{
		{name: "FD-bound small headers", maxHeaderBytes: 16 << 10},
		{name: "fractional memory-slot rounding", maxHeaderBytes: 320 << 10},
	} {
		t.Run(test.name, func(t *testing.T) {
			usage := sysmetrics.MemoryUsage{UsedBytes: 64 << 20, LimitBytes: 512 << 20, Source: "test"}
			manager := mustNewDefaultAgentStreamCapacityManager(65_536)
			resourceConfig := sysmetrics.DefaultAdaptiveMemoryConfig()
			resourceConfig.SampleInterval = time.Hour
			controller, err := sysmetrics.NewAdaptiveMemoryController(resourceConfig, sysmetrics.MemoryUsageSamplerFunc(func() (sysmetrics.MemoryUsage, error) {
				return usage, nil
			}))
			if err != nil {
				t.Fatal(err)
			}
			manager.enableAdaptiveMemory(controller)
			manager.publishAdaptiveCapacity(sysmetrics.AdaptiveMemorySnapshot{
				Generation:       100,
				Level:            sysmetrics.MemoryPressureSoft,
				AdmissionLimit:   9,
				Maximum:          65_536,
				Usage:            usage,
				StreamChargeByte: resourceConfig.EstimatedBytesPerAdmission,
			})
			app := &App{
				Config:              &config.Config{PublicMaxHeaderBytes: test.maxHeaderBytes},
				agentStreamCapacity: manager,
				publicConnections:   newPublicConnectionCapacityLimiter(0, 256),
			}
			base, peer := net.Pipe()
			defer base.Close()
			defer peer.Close()
			clientA := &publicConnectionTestRemoteConn{Conn: base, remote: &net.TCPAddr{IP: net.ParseIP("192.0.2.20"), Port: 1000}}
			clientB := &publicConnectionTestRemoteConn{Conn: base, remote: &net.TCPAddr{IP: net.ParseIP("192.0.2.21"), Port: 1000}}

			releases := make([]func(), 0, 256)
			for range 256 {
				release, ok := app.tryReservePublicConnection(clientA)
				if !ok {
					break
				}
				releases = append(releases, release)
			}
			if len(releases) == 0 || len(releases) >= 256 {
				t.Fatalf("peer A reservations = %d, want a positive resource-bounded share below static guard", len(releases))
			}
			releaseB, ok := app.tryReservePublicConnection(clientB)
			if !ok {
				t.Fatal("peer B could not use the public resource capacity left by peer A's dynamic share")
			}
			releaseB()
			for _, release := range releases {
				release()
			}
			if snapshot := manager.snapshot(); snapshot.AdaptiveExternalBytes != 0 || snapshot.AdaptiveExternalFDs != 0 {
				t.Fatalf("released connection ledger = %d bytes/%d FDs, want zero", snapshot.AdaptiveExternalBytes, snapshot.AdaptiveExternalFDs)
			}
		})
	}
}

type publicConnectionTestRemoteConn struct {
	net.Conn
	remote net.Addr
}

func (c *publicConnectionTestRemoteConn) RemoteAddr() net.Addr { return c.remote }

func TestShutdownPublicHTTPServerForcesCloseAfterTimeout(t *testing.T) {
	server, handlerDone, serveErr := startBlockingPublicHTTPServer(t)

	started := time.Now()
	if err := shutdownPublicHTTPServer(context.Background(), server, 25*time.Millisecond); err != nil {
		t.Fatalf("shutdownPublicHTTPServer() error = %v", err)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("shutdown took %s, want forced close within one second", elapsed)
	}
	assertBlockingPublicHTTPServerClosed(t, handlerDone, serveErr)
}

func TestShutdownPublicHTTPServerForcesCloseWhenParentCanceled(t *testing.T) {
	server, handlerDone, serveErr := startBlockingPublicHTTPServer(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if err := shutdownPublicHTTPServer(ctx, server, time.Hour); err != nil {
		t.Fatalf("shutdownPublicHTTPServer() error = %v", err)
	}
	assertBlockingPublicHTTPServerClosed(t, handlerDone, serveErr)
}

func TestPublicListenerShutdownContextUsesEarlierDeadline(t *testing.T) {
	t.Run("configured timeout is earlier", func(t *testing.T) {
		parent, cancelParent := context.WithTimeout(context.Background(), time.Hour)
		defer cancelParent()
		started := time.Now()
		ctx, cancel := publicListenerShutdownContext(parent, 50*time.Millisecond)
		defer cancel()
		deadline, ok := ctx.Deadline()
		if !ok {
			t.Fatal("shutdown context has no deadline")
		}
		if remaining := deadline.Sub(started); remaining < 25*time.Millisecond || remaining > 250*time.Millisecond {
			t.Fatalf("configured shutdown deadline remaining = %s, want about 50ms", remaining)
		}
	})

	t.Run("parent deadline is earlier", func(t *testing.T) {
		parentDeadline := time.Now().Add(50 * time.Millisecond)
		parent, cancelParent := context.WithDeadline(context.Background(), parentDeadline)
		defer cancelParent()
		ctx, cancel := publicListenerShutdownContext(parent, time.Hour)
		defer cancel()
		deadline, ok := ctx.Deadline()
		if !ok {
			t.Fatal("shutdown context has no deadline")
		}
		if !deadline.Equal(parentDeadline) {
			t.Fatalf("shutdown deadline = %s, want parent deadline %s", deadline, parentDeadline)
		}
	})
}

func startBlockingPublicHTTPServer(t *testing.T) (*http.Server, <-chan struct{}, <-chan error) {
	t.Helper()
	requestStarted := make(chan struct{})
	handlerDone := make(chan struct{})
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(requestStarted)
		_, _ = w.Write([]byte("started"))
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
		<-r.Context().Done()
		close(handlerDone)
	})}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	serveErr := make(chan error, 1)
	go func() {
		err := server.Serve(listener)
		if err == http.ErrServerClosed {
			err = nil
		}
		serveErr <- err
	}()

	resp, err := http.Get("http://" + listener.Addr().String())
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	t.Cleanup(func() { _ = resp.Body.Close() })
	select {
	case <-requestStarted:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for active request")
	}
	return server, handlerDone, serveErr
}

func assertBlockingPublicHTTPServerClosed(t *testing.T, handlerDone <-chan struct{}, serveErr <-chan error) {
	t.Helper()
	select {
	case <-handlerDone:
	case <-time.After(time.Second):
		t.Fatal("handler was not closed after shutdown timeout")
	}
	select {
	case err := <-serveErr:
		if err != nil {
			t.Fatalf("Serve() error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for Serve to exit")
	}
}
