package tunnel

import (
	"bytes"
	"io"
	"net"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hashicorp/yamux"
)

func TestAggregateGrowingConnDrainsFinalPayloadWithoutGrowth(t *testing.T) {
	left, right := net.Pipe()
	config, err := NewAggregateYamuxConfig(nil, 4<<20, 8)
	if err != nil {
		t.Fatal(err)
	}
	config.EnableKeepAlive = false
	config.ConnectionWriteTimeout = time.Second
	client, err := yamux.Client(left, config.Clone())
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	server, err := yamux.Server(right, config.Clone())
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	writer, err := client.OpenStream()
	if err != nil {
		t.Fatal(err)
	}
	reader, err := server.AcceptStream()
	if err != nil {
		t.Fatal(err)
	}
	_ = reader.SetReadDeadline(time.Now().Add(2 * time.Second))
	payload := bytes.Repeat([]byte{0x6b}, 32<<10)
	if _, err := writer.Write(payload); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	// Its ordered Ping is processed only after the server has consumed FIN.
	if _, err := client.Ping(); err != nil {
		t.Fatal(err)
	}
	if _, err := server.Ping(); err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	var charged atomic.Int64
	growth := NewAggregateReceiveWindowGrowth(server, func(n int64) (func(), bool) {
		calls.Add(1)
		charged.Add(n)
		return func() { charged.Add(-n) }, true
	})
	defer func() {
		_ = server.Close()
		growth.Release()
		if charged.Load() != 0 {
			t.Errorf("retained growth charge=%d", charged.Load())
		}
	}()
	conn := NewAggregateGrowingConn(reader, 4<<20, growth)
	before := server.AggregateReceiveWindowStats().ReceiveLimit
	// Seed the preceding rate sample, so a useful Read would normally propose
	// growth. A terminal receive side must skip it even with pending demand.
	conn.growth.start = time.Now().Add(-20 * time.Millisecond)
	conn.growth.bytes = 4 << 20
	conn.growth.previous = 4 << 20
	conn.growth.rate = 1 << 30
	buf := make([]byte, len(payload))
	n, err := conn.Read(buf)
	if err != nil || n != len(payload) || !bytes.Equal(buf[:n], payload) {
		t.Fatalf("final payload n=%d err=%v", n, err)
	}
	if n, err := conn.Read(buf); n != 0 || err != io.EOF {
		t.Fatalf("final EOF n=%d err=%v", n, err)
	}
	// Explicit growth is also denied without reserving/growing a pool that
	// cannot serve any more bytes to this already-FINished stream.
	if granted, err := growth.Grow(reader, 1<<20); granted || err != nil {
		t.Fatalf("terminal growth granted=%t err=%v", granted, err)
	}
	if after := server.AggregateReceiveWindowStats().ReceiveLimit; after != before || calls.Load() != 0 {
		t.Fatalf("terminal growth changed pool %d->%d with %d reservations", before, after, calls.Load())
	}
}

func TestAggregateGrowthCrossedByFINKeepsPublishedPoolOwned(t *testing.T) {
	left, right := net.Pipe()
	config, err := NewAggregateYamuxConfig(nil, 4<<20, 8)
	if err != nil {
		t.Fatal(err)
	}
	config.EnableKeepAlive = false
	config.ConnectionWriteTimeout = time.Second
	client, err := yamux.Client(left, config.Clone())
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	server, err := yamux.Server(right, config.Clone())
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	writer, err := client.OpenStream()
	if err != nil {
		t.Fatal(err)
	}
	reader, err := server.AcceptStream()
	if err != nil {
		t.Fatal(err)
	}
	var charged atomic.Int64
	var calls atomic.Int32
	growth := NewAggregateReceiveWindowGrowth(server, func(n int64) (func(), bool) {
		calls.Add(1)
		// Cross the terminal transition after Grow's state snapshot but before
		// session publication, without a timing race or production test hook.
		if err := writer.Close(); err != nil {
			t.Fatal(err)
		}
		if _, err := client.Ping(); err != nil {
			t.Fatal(err)
		}
		charged.Add(n)
		return func() { charged.Add(-n) }, true
	})
	defer func() {
		_ = server.Close()
		growth.Release()
		if charged.Load() != 0 {
			t.Errorf("retained charge=%d", charged.Load())
		}
	}()
	if granted, err := growth.Grow(reader, 1<<20); granted || err != nil {
		t.Fatalf("crossed FIN growth granted=%t err=%v", granted, err)
	}
	if calls.Load() != 1 || charged.Load() <= 0 {
		t.Fatalf("published session ownership calls=%d charged=%d", calls.Load(), charged.Load())
	}
	if got := server.AggregateReceiveWindowStats().ReceiveLimit; got != 4<<20 {
		t.Fatalf("published session credit=%d", got)
	}
	if n, err := reader.Read(make([]byte, 1)); n != 0 || err != io.EOF {
		t.Fatalf("terminal read n=%d err=%v", n, err)
	}
	_ = server.Close()
	growth.Release()
	growth.Release()
	if charged.Load() != 0 {
		t.Fatalf("charge survived session retirement: %d", charged.Load())
	}
}
