//go:build linux

package server

import (
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"

	"p2pstream/internal/sysmetrics"
	"p2pstream/internal/tcpsocket"
)

func TestPublicTCPQueuesRemainChargedUntilPhysicalClose(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	usage := sysmetrics.MemoryUsage{UsedBytes: 64 << 20, LimitBytes: 16 << 30, Source: "test"}
	app := NewApp(nil, nil)
	app.agentStreamCapacity = newAdaptiveServerCapacityForTest(t, 65536, &usage)
	client, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	listener := resourceBoundedPublicListener{Listener: ln, acquire: app.tryReservePublicConnection}
	conn, err := listener.Accept()
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	tcp := conn.(*resourceBoundedPublicTCPConn)
	queues, err := tcpsocket.Configure(tcp.tcp, 0)
	if err != nil {
		t.Fatal(err)
	}
	if s := app.agentStreamCapacity.snapshot(); s.AdaptiveExternalBytes != queues+640*1024 || s.AdaptiveExternalFDs != 1 {
		t.Fatalf("unaccounted public socket: %+v", s)
	}
	_ = tcp.CloseWrite()
	if s := app.agentStreamCapacity.snapshot(); s.AdaptiveExternalBytes == 0 {
		t.Fatal("half-close released live receive queues")
	}
	_ = conn.Close()
	_ = conn.Close()
	if s := app.agentStreamCapacity.snapshot(); s.AdaptiveExternalBytes != 0 || s.AdaptiveExternalFDs != 0 || app.publicConnections.inUse() != 0 {
		t.Fatalf("leaked public socket credit: %+v", s)
	}
}

func TestProductionMemoryProfileAdmitsOrdinaryPublicSocketsStreamsAndHTTP2Requests(t *testing.T) {
	const concurrent = 80
	usage := sysmetrics.MemoryUsage{
		UsedBytes:  252 * (1 << 30) / 100,
		LimitBytes: 382 * (1 << 30) / 100,
		Source:     "production-profile",
	}
	app := NewApp(nil, nil)
	app.agentStreamCapacity = newAdaptiveServerCapacityForTest(t, 65_536, &usage)
	app.agentStreamCapacity.registerSessionWithLimit("profile-session", 65_536)
	defer app.agentStreamCapacity.unregisterSession("profile-session")

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	type accepted struct {
		conn    net.Conn
		release func()
	}
	acceptedConnections := make(chan accepted, concurrent)
	acceptErrors := make(chan error, 1)
	go func() {
		for range concurrent {
			conn, acceptErr := ln.Accept()
			if acceptErr != nil {
				acceptErrors <- acceptErr
				return
			}
			release, ok := app.tryReservePublicConnection(conn)
			if !ok {
				_ = conn.Close()
				acceptErrors <- errPublicTestAdmissionRejected
				return
			}
			acceptedConnections <- accepted{conn: conn, release: release}
		}
	}()
	clients := make([]net.Conn, 0, concurrent)
	servers := make([]accepted, 0, concurrent)
	for range concurrent {
		client, dialErr := net.Dial("tcp", ln.Addr().String())
		if dialErr != nil {
			t.Fatal(dialErr)
		}
		clients = append(clients, client)
		select {
		case server := <-acceptedConnections:
			servers = append(servers, server)
		case acceptErr := <-acceptErrors:
			t.Fatal(acceptErr)
		}
	}

	leases := make([]*agentStreamCapacityLease, 0, concurrent)
	requests := make([]*publicProxyContext, 0, concurrent)
	for index := range concurrent {
		lease, acquireErr := app.agentStreamCapacity.tryAcquire(agentStreamCapacityPublicPooled, "profile", "profile-session")
		if acquireErr != nil || !lease.markLive() {
			t.Fatalf("pooled stream %d admission: lease=%v err=%v", index, lease, acquireErr)
		}
		leases = append(leases, lease)

		request := httptest.NewRequest(http.MethodPost, "https://public.test/upload", http.NoBody)
		request.ProtoMajor = 2
		request.ProtoMinor = 0
		request.ContentLength = 1 << 20
		ctx := &publicProxyContext{App: app, Request: request, ResponseWriter: httptest.NewRecorder()}
		if result := publicRequestAdmissionStage(ctx); result != publicProxyStageContinue {
			t.Fatalf("HTTP/2 request %d rejected at measured %.2f%% memory usage", index, usage.Percent())
		}
		requests = append(requests, ctx)
	}
	if snapshot := app.agentStreamCapacity.snapshot(); snapshot.MemoryPressure != "healthy" || snapshot.Total.InUse != concurrent {
		t.Fatalf("production-profile capacity = %+v", snapshot)
	}

	for _, ctx := range requests {
		ctx.runCleanup()
	}
	for _, lease := range leases {
		lease.release()
	}
	for _, server := range servers {
		server.release()
		_ = server.conn.Close()
	}
	for _, client := range clients {
		_ = client.Close()
	}
	if snapshot := app.agentStreamCapacity.snapshot(); snapshot.AdaptiveExternalBytes != 0 || snapshot.AdaptiveExternalFDs != 0 || snapshot.Total.InUse != 0 {
		t.Fatalf("production-profile resources leaked: %+v", snapshot)
	}
}

var errPublicTestAdmissionRejected = errors.New("public connection admission rejected")
