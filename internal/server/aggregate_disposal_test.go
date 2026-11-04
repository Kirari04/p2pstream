package server

import (
	"bytes"
	"errors"
	"io"
	"testing"

	"github.com/hashicorp/yamux"
	"p2pstream/internal/tunnel"
)

type failedServerDrainConn struct{ *tunnel.GrowingConn }

func (*failedServerDrainConn) Read([]byte) (int, error) { return 0, io.ErrUnexpectedEOF }
func TestAggregateServerCleanupDisposesAbandonedFINAfterReadError(t *testing.T) {
	session, peer := newAggregateAgentTunnelTestSession(t)
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
	managed := &agentTunnelStreamConn{Conn: &failedServerDrainConn{growing}, release: func() { released = true }}
	managed.releaseAfterStreamClosed()
	if !released || !session.ReceiveOwnersDrained() {
		t.Fatal("terminal cleanup leaked ownership")
	}
	if _, err := reader.Read(make([]byte, 1)); !errors.Is(err, yamux.ErrConnectionReset) {
		t.Fatalf("abandonment returned successful EOF: %v", err)
	}
}
