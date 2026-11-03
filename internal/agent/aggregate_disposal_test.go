package agent

import (
	"bytes"
	"errors"
	"io"
	"net"
	"testing"

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
