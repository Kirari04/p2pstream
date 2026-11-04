package server

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/hashicorp/yamux"

	"p2pstream/internal/tunnel"
)

func newAgentHandoffSession(t *testing.T, aggregate bool) (*AgentConn, *yamux.Session) {
	t.Helper()
	agent := &AgentConn{AgentID: 7, Done: make(chan struct{}), AggregateFlowControl: aggregate}
	if aggregate {
		peer, server := newAggregateAgentTunnelTestSession(t)
		agent.installTunnel(1, server)
		return agent, peer
	}
	server, peer := newAgentTunnelTestSession(t)
	agent.Session = server
	return agent, peer
}

// A cancellation may start closing the capacity-managed connection while its
// successful OpenResponse is already in flight. The dial must join that close
// and report failure, never transfer a connection still owned by the watcher.
func TestDialViaAgentHandshakeCancellationCannotReturnSuccess(t *testing.T) {
	for _, tc := range []struct {
		name       string
		aggregate  bool
		disconnect bool
	}{
		{"legacy/context", false, false},
		{"legacy/agent", false, true},
		{"aggregate/context", true, false},
		{"aggregate/agent", true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			agent, peer := newAgentHandoffSession(t, tc.aggregate)
			app := &App{agentStreamCapacity: mustNewDefaultAgentStreamCapacityManager(32)}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			requestRead := make(chan struct{})
			respond := make(chan struct{})
			var respondOnce sync.Once
			allowResponse := func() { respondOnce.Do(func() { close(respond) }) }
			peerDone := make(chan error, 1)
			var workers sync.WaitGroup
			workers.Add(2)
			t.Cleanup(func() {
				cancel()
				allowResponse()
				_ = peer.Close()
				joined := make(chan struct{})
				go func() { workers.Wait(); close(joined) }()
				select {
				case <-joined:
				case <-time.After(2 * time.Second):
					t.Error("handshake workers did not join")
				}
			})
			go func() {
				defer workers.Done()
				stream, err := peer.Accept()
				if err != nil {
					peerDone <- err
					return
				}
				defer stream.Close()
				if _, err = tunnel.ReadOpenRequest(stream); err != nil {
					peerDone <- err
					return
				}
				close(requestRead)
				// Deliver the already queued response even after cancellation.
				<-respond
				if err = tunnel.WriteOpenResponse(stream, tunnel.OpenResponse{OK: true}); err == nil {
					_, err = io.Copy(io.Discard, stream)
				}
				peerDone <- err
			}()
			type dialResult struct {
				conn net.Conn
				err  error
			}
			result := make(chan dialResult, 1)
			go func() {
				defer workers.Done()
				conn, err := app.dialViaAgentWithCapacity(ctx, agent, "tcp", "example.test:80", "handoff", agentStreamCapacityPublicPooled, "handoff")
				result <- dialResult{conn, err}
			}()
			select {
			case <-requestRead:
			case <-time.After(2 * time.Second):
				t.Fatal("OpenRequest was not received")
			}
			// Close marks its lease before sending FIN. Hold that transition
			// while the peer delivers the successful handshake response.
			app.agentStreamCapacity.mu.Lock()
			if tc.disconnect {
				close(agent.Done)
			} else {
				cancel()
			}
			allowResponse()
			var got dialResult
			early := false
			select {
			case got = <-result:
				early = true
			case <-time.After(100 * time.Millisecond):
			}
			app.agentStreamCapacity.mu.Unlock()
			if !early {
				select {
				case got = <-result:
				case <-time.After(2 * time.Second):
					t.Fatal("dial did not finish after cancellation cleanup was released")
				}
			}
			if got.conn != nil {
				_ = got.conn.Close()
			}
			select {
			case <-peerDone:
			case <-time.After(2 * time.Second):
				t.Fatal("peer did not finish")
			}
			if got.conn != nil || got.err == nil {
				t.Fatalf("cancelled handshake returned conn=%v err=%v", got.conn != nil, got.err)
			}
			want := error(context.Canceled)
			if tc.disconnect {
				want = errAgentDisconnected
			}
			if !errors.Is(got.err, want) {
				t.Fatalf("cancelled handshake error = %v, want %v", got.err, want)
			}
			waitForAgentStreamCapacityUsage(t, app, 0)
			assertAgentStreamCapacityClean(t, app.agentStreamCapacity)
		})
	}
}

type handshakeBlockedCloseConn struct {
	net.Conn
	started chan struct{}
	release chan struct{}
	closed  chan struct{}
}

func (c *handshakeBlockedCloseConn) Close() error {
	defer close(c.closed)
	close(c.started)
	<-c.release
	return nil
}

func TestAgentOpenHandshakeStopJoinsCancellationClose(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	conn := &handshakeBlockedCloseConn{started: make(chan struct{}), release: make(chan struct{}), closed: make(chan struct{})}
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(conn.release) }) }
	defer release()
	stop := watchAgentOpenHandshake(ctx, nil, conn)
	cancel()
	select {
	case <-conn.started:
	case <-time.After(2 * time.Second):
		t.Fatal("watcher did not begin cancellation close")
	}
	stopped := make(chan struct{})
	go func() { stop(); close(stopped) }()
	early := false
	select {
	case <-stopped:
		early = true
	case <-time.After(50 * time.Millisecond):
	}
	release()
	select {
	case <-stopped:
	case <-time.After(2 * time.Second):
		t.Fatal("stop did not join after close finished")
	}
	select {
	case <-conn.closed:
	case <-time.After(2 * time.Second):
		t.Fatal("cancellation close did not finish")
	}
	stop() // The success-path call and deferred call must both be safe.
	if early {
		t.Fatal("stop returned while the cancellation watcher still owned the connection")
	}
}

func TestDialViaAgentTransferredConnectionSurvivesDialCancellation(t *testing.T) {
	for _, aggregate := range []bool{false, true} {
		name := "legacy"
		if aggregate {
			name = "aggregate"
		}
		t.Run(name, func(t *testing.T) {
			agent, peer := newAgentHandoffSession(t, aggregate)
			app := &App{agentStreamCapacity: mustNewDefaultAgentStreamCapacityManager(32)}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			peerDone := make(chan error, 1)
			go func() {
				var err error
				defer func() { peerDone <- err }()
				stream, err := peer.Accept()
				if err != nil {
					return
				}
				defer stream.Close()
				if _, err = tunnel.ReadOpenRequest(stream); err != nil {
					return
				}
				if err = tunnel.WriteOpenResponse(stream, tunnel.OpenResponse{OK: true}); err == nil {
					_, err = io.Copy(stream, stream)
				}
			}()
			t.Cleanup(func() {
				_ = peer.Close()
				select {
				case <-peerDone:
				case <-time.After(2 * time.Second):
					t.Error("echo worker did not join")
				}
			})
			conn, err := app.dialViaAgentWithCapacity(ctx, agent, "tcp", "example.test:80", "transferred", agentStreamCapacityPublicPooled, "handoff")
			if err != nil {
				t.Fatal(err)
			}
			defer conn.Close()
			// net/http may cancel its dial context after accepting this
			// connection. The caller now owns its lifetime, including Close.
			cancel()
			_ = conn.SetDeadline(time.Now().Add(2 * time.Second))
			payload := bytes.Repeat([]byte("handed-off-stream"), 4096)
			if _, err := conn.Write(payload); err != nil {
				t.Fatalf("write after dial-context cancellation: %v", err)
			}
			got := make([]byte, len(payload))
			if _, err := io.ReadFull(conn, got); err != nil || !bytes.Equal(got, payload) {
				t.Fatalf("echo after dial-context cancellation: exact=%v err=%v", bytes.Equal(got, payload), err)
			}
			_ = conn.Close()
			waitForAgentStreamCapacityUsage(t, app, 0)
			assertAgentStreamCapacityClean(t, app.agentStreamCapacity)
		})
	}
}
