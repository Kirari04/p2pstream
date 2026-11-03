package agent

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"testing"
	"time"

	"github.com/hashicorp/yamux"
	"p2pstream/internal/tunnel"
)

type failedAgentDrainConn struct{ *tunnel.GrowingConn }

func (*failedAgentDrainConn) Read([]byte) (int, error) { return 0, io.ErrUnexpectedEOF }
func TestAggregateAgentCleanupDisposesAbandonedFINAfterReadError(t *testing.T) {
	config, err := tunnel.NewAggregateYamuxConfig(nil, 2<<20, 32)
	if err != nil {
		t.Fatal(err)
	}
	left, right := net.Pipe()
	session, err := yamux.Client(left, config.Clone())
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	peer, err := yamux.Server(right, config.Clone())
	if err != nil {
		t.Fatal(err)
	}
	defer peer.Close()
	writer, err := peer.OpenStream()
	if err != nil {
		t.Fatal(err)
	}
	reader, err := session.AcceptStream()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := writer.Write(bytes.Repeat([]byte("abandoned"), 1024)); err != nil {
		t.Fatal(err)
	}
	writer.Close()
	reader.Close()
	session.Ping()
	peer.Ping()
	if session.NumStreams() != 0 || session.ReceiveOwnersDrained() {
		t.Fatal("FIN buffer fixture not retained")
	}
	growing := tunnel.NewAggregateGrowingConn(reader, 2<<20, nil)
	released := false
	releaseAgentTunnelCapacityAfterPeerClose(&failedAgentDrainConn{growing}, func() { released = true })
	if !released || !session.ReceiveOwnersDrained() {
		t.Fatal("terminal cleanup leaked ownership")
	}
	if _, err := reader.Read(make([]byte, 1)); !errors.Is(err, yamux.ErrConnectionReset) {
		t.Fatalf("abandonment returned successful EOF: %v", err)
	}
}

// Exercise the application's relay with a real TCP-backed aggregate stream.
// Disposing a fully received FIN buffer on peer session loss must make the relay
// return an error. The origin still sees a normal EOF after the upstream closes;
// detecting truncation there requires application framing.
func TestAggregateRelayPropagatesSessionDisposalOverTCP(t *testing.T) {
	config, err := tunnel.NewAggregateYamuxConfig(nil, 2<<20, 32)
	if err != nil {
		t.Fatal(err)
	}
	left, right := relayTestTCPPair(t)
	client, err := yamux.Client(left, config.Clone())
	if err != nil {
		t.Fatal(err)
	}
	server, err := yamux.Server(right, config.Clone())
	if err != nil {
		client.Close()
		t.Fatal(err)
	}
	defer client.Close()
	defer server.Close()
	writer, err := client.OpenStream()
	if err != nil {
		t.Fatal(err)
	}
	reader, err := server.AcceptStream()
	if err != nil {
		t.Fatal(err)
	}
	payload := bytes.Repeat([]byte("owned-upload"), 8192)
	if n, err := writer.Write(payload); n != len(payload) || err != nil {
		t.Fatal(n, err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := client.Ping(); err != nil {
		t.Fatal(err)
	}
	if n, _ := reader.ReceiveBufferStats(); n != uint64(len(payload)) || !reader.ReceiveClosed() {
		t.Fatalf("accepted FIN buffer=%d", n)
	}
	client.Close()
	select {
	case <-server.CloseChan():
	case <-time.After(time.Second):
		t.Fatal("no peer-initiated shutdown")
	}
	server.Close()
	upstream, origin := relayTestTCPPair(t)
	defer upstream.Close()
	defer origin.Close()
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	received, sent, err := relayTunnelStream(ctx, tunnel.NewAggregateGrowingConn(reader, 2<<20, nil), upstream)
	if !errors.Is(err, yamux.ErrSessionShutdown) || received != 0 || sent != 0 {
		t.Fatalf("disposed upload relay: received=%d sent=%d err=%v", received, sent, err)
	}
	origin.SetReadDeadline(time.Now().Add(time.Second))
	if n, err := origin.Read(make([]byte, 1)); n != 0 || err != io.EOF {
		t.Fatalf("origin read after joined relay: %d %v", n, err)
	}
	if stats := server.AggregateReceiveWindowStats(); !stats.Closed || stats.TotalUnits != 0 {
		t.Fatalf("relay retained pool: %+v", stats)
	}
}
