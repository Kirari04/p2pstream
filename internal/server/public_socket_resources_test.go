//go:build linux

package server

import (
	"net"
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
