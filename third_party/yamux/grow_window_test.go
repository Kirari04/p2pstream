package yamux

import (
	"bytes"
	"io"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type growWindowRecorder struct {
	io.ReadWriteCloser
	mu     sync.Mutex
	writes [][]byte
}

func (r *growWindowRecorder) Write(p []byte) (int, error) {
	r.mu.Lock()
	r.writes = append(r.writes, append([]byte(nil), p...))
	r.mu.Unlock()
	return r.ReadWriteCloser.Write(p)
}

func (r *growWindowRecorder) countWindowUpdates(streamID uint32, length uint32) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	count := 0
	for _, payload := range r.writes {
		if len(payload) != headerSize {
			continue
		}
		hdr := header(payload)
		if hdr.MsgType() == typeWindowUpdate && hdr.StreamID() == streamID && hdr.Length() == length {
			count++
		}
	}
	return count
}

func TestGrowReceiveWindowPublishesSmallDelta(t *testing.T) {
	clientConn, rawServerConn := testConnPipe(t)
	serverConn := &growWindowRecorder{ReadWriteCloser: rawServerConn}
	config := testConfNoKeepAlive()
	config.MaxStreamWindowSize = 512 * 1024
	config.StreamOpenTimeout = 5 * time.Second
	client, server := testClientServerConfig(t, clientConn, serverConn, config, config.Clone())

	writer, err := client.OpenStream()
	if err != nil {
		t.Fatal(err)
	}
	reader, err := server.AcceptStream()
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()
	defer reader.Close()
	_ = writer.SetDeadline(time.Now().Add(5 * time.Second))
	_ = reader.SetDeadline(time.Now().Add(5 * time.Second))

	const (
		initial = 512 * 1024
		target  = 576 * 1024
		delta   = target - initial
	)
	// Configure the stream at 512 KiB, then explicitly grow beyond that
	// configured baseline. Growth is caller-reserved credit and is published
	// independently of a later application read.
	if got := reader.MaxReceiveWindow(); got != initial {
		t.Fatalf("initial receive window=%d, want %d", got, initial)
	}
	waitForSendWindow(t, writer, initial)
	baselineUpdates := serverConn.countWindowUpdates(reader.StreamID(), delta)

	if err := reader.GrowReceiveWindow(target); err != nil {
		t.Fatal(err)
	}

	deadline := time.Now().Add(time.Second)
	for atomic.LoadUint32(&writer.sendWindow) != target && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if got := atomic.LoadUint32(&writer.sendWindow); got != target {
		t.Fatalf("peer send window=%d, want %d", got, target)
	}
	if got := serverConn.countWindowUpdates(reader.StreamID(), delta); got != baselineUpdates+1 {
		t.Fatalf("growth update count=%d, want %d delta=%d", got, baselineUpdates+1, delta)
	}

	// Repeating the same request and requesting a lower window must not enqueue
	// another control frame, even though no application read follows growth.
	if err := reader.GrowReceiveWindow(target); err != nil {
		t.Fatal(err)
	}
	if err := reader.GrowReceiveWindow(initial); err != nil {
		t.Fatal(err)
	}
	time.Sleep(20 * time.Millisecond)
	if got := serverConn.countWindowUpdates(reader.StreamID(), delta); got != baselineUpdates+1 {
		t.Fatalf("duplicate/lower growth update count=%d, want %d", got, baselineUpdates+1)
	}
	if got := atomic.LoadUint32(&writer.sendWindow); got != target {
		t.Fatalf("peer send window changed after duplicate/lower growth: %d", got)
	}
}

func TestReadPublishesQuarterWindowCredit(t *testing.T) {
	clientConn, rawServerConn := testConnPipe(t)
	serverConn := &growWindowRecorder{ReadWriteCloser: rawServerConn}
	config := testConfNoKeepAlive()
	config.MaxStreamWindowSize = 512 * 1024
	config.StreamOpenTimeout = 5 * time.Second
	client, server := testClientServerConfig(t, clientConn, serverConn, config, config.Clone())

	writer, err := client.OpenStream()
	if err != nil {
		t.Fatal(err)
	}
	reader, err := server.AcceptStream()
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()
	defer reader.Close()
	_ = writer.SetDeadline(time.Now().Add(5 * time.Second))
	_ = reader.SetDeadline(time.Now().Add(5 * time.Second))
	waitForSendWindow(t, writer, 512*1024)

	const maxWindow = 512 * 1024
	const belowQuarter = maxWindow/4 - 1
	const exactQuarter = maxWindow / 4
	const thresholdByte = 1
	streamID := reader.StreamID()
	belowCount := serverConn.countWindowUpdates(streamID, belowQuarter)
	zeroCount := serverConn.countWindowUpdates(streamID, 0)

	if n, err := writer.Write(bytes.Repeat([]byte{0x41}, belowQuarter)); err != nil || n != belowQuarter {
		t.Fatalf("below-quarter write: n=%d err=%v", n, err)
	}
	readBuf := make([]byte, belowQuarter)
	if n, err := reader.Read(readBuf); err != nil || n != belowQuarter {
		t.Fatalf("below-quarter read: n=%d err=%v", n, err)
	}
	if got := serverConn.countWindowUpdates(streamID, belowQuarter); got != belowCount {
		t.Fatalf("below-quarter update count=%d, want %d", got, belowCount)
	}

	if n, err := writer.Write(bytes.Repeat([]byte{0x42}, thresholdByte)); err != nil || n != thresholdByte {
		t.Fatalf("quarter write: n=%d err=%v", n, err)
	}
	if n, err := reader.Read(make([]byte, thresholdByte)); err != nil || n != thresholdByte {
		t.Fatalf("quarter read: n=%d err=%v", n, err)
	}
	if got := serverConn.countWindowUpdates(streamID, exactQuarter); got != 1 {
		t.Fatalf("quarter update count=%d, want one", got)
	}
	if got := serverConn.countWindowUpdates(streamID, 0); got != zeroCount {
		t.Fatalf("zero-credit update count=%d, want %d", got, zeroCount)
	}
}

func waitForSendWindow(t *testing.T, stream *Stream, want uint32) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for atomic.LoadUint32(&stream.sendWindow) != want && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if got := atomic.LoadUint32(&stream.sendWindow); got != want {
		t.Fatalf("peer send window=%d, want %d", got, want)
	}
}
