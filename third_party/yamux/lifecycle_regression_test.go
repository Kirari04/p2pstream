package yamux

import (
	"errors"
	"io"
	"net"
	"testing"
	"time"
)

func TestFailedOpenTerminatesWireAndReleasesTracking(t *testing.T) {
	conn, peer := net.Pipe()
	defer peer.Close()
	config := testConfNoKeepAlive()
	config.ConnectionWriteTimeout = 30 * time.Millisecond
	client, err := Client(conn, config)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	// No reader consumes the SYN before the write timeout.
	if _, err := client.OpenStream(); !errors.Is(err, ErrConnectionWriteTimeout) {
		t.Fatalf("open error = %v", err)
	}
	client.streamLock.Lock()
	streams, inflight := len(client.streams), len(client.inflight)
	client.streamLock.Unlock()
	if streams != 0 || inflight != 0 || len(client.synCh) != 0 {
		t.Fatalf("failed open retained streams=%d inflight=%d tokens=%d", streams, inflight, len(client.synCh))
	}
	_ = peer.SetReadDeadline(time.Now().Add(time.Second))
	if n, err := peer.Read(make([]byte, headerSize)); n != 0 || !errors.Is(err, io.EOF) {
		t.Fatalf("timed-out SYN remained deliverable: n=%d err=%v", n, err)
	}
}

func TestReceiveWindowTimeoutTerminatesSession(t *testing.T) {
	config := testConfNoKeepAlive()
	config.ConnectionWriteTimeout = 30 * time.Millisecond
	clientConn, serverConn := testConnPipe(t)
	client, server := testClientServerConfig(t, clientConn, serverConn, config, config.Clone())
	stream, err := client.OpenStream()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := server.AcceptStream(); err != nil {
		t.Fatal(err)
	}
	conn := clientConn.(*pipeConn)
	conn.writeBlocker.Lock()
	err = stream.GrowReceiveWindow(2 * stream.MaxReceiveWindow())
	conn.writeBlocker.Unlock()
	if !errors.Is(err, ErrConnectionWriteTimeout) {
		t.Fatalf("growth error = %v", err)
	}
	select {
	case <-client.CloseChan():
	case <-time.After(time.Second):
		t.Fatal("uncertain receive credit left the session usable")
	}
	select {
	case <-server.CloseChan():
	case <-time.After(time.Second):
		t.Fatal("peer wire remained usable after failed credit update")
	}
}

func TestRepeatedResetBeforeACKReleasesInflight(t *testing.T) {
	conn, peer := net.Pipe()
	defer peer.Close()
	config := testConfNoKeepAlive()
	client, err := Client(conn, config)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	const attempts = 128
	done := make(chan error, 1)
	go func() {
		for i := 0; i < attempts; i++ {
			hdr := header(make([]byte, headerSize))
			if _, err := io.ReadFull(peer, hdr); err != nil {
				done <- err
				return
			}
			hdr.encode(typeWindowUpdate, flagRST, hdr.StreamID(), 0)
			if _, err := peer.Write(hdr); err != nil {
				done <- err
				return
			}
		}
		done <- nil
	}()
	for i := 0; i < attempts; i++ {
		if _, err := client.OpenStream(); err != nil {
			t.Fatal(err)
		}
		deadline := time.Now().Add(time.Second)
		for client.NumStreams() != 0 && time.Now().Before(deadline) {
			time.Sleep(time.Millisecond)
		}
		client.streamLock.Lock()
		streams, inflight := len(client.streams), len(client.inflight)
		client.streamLock.Unlock()
		if streams != 0 || inflight != 0 || len(client.synCh) != 0 {
			t.Fatalf("reset %d retained streams=%d inflight=%d tokens=%d", i, streams, inflight, len(client.synCh))
		}
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestVerifyConfigRejectsNonPositiveKeepAlive(t *testing.T) {
	for _, interval := range []time.Duration{0, -time.Second} {
		config := DefaultConfig()
		config.KeepAliveInterval = interval
		if err := VerifyConfig(config); err == nil {
			t.Fatalf("accepted keep-alive interval %v", interval)
		}
	}
}
