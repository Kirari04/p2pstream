package tunnel

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hashicorp/yamux"
)

type gatedWriteFailureConn struct {
	net.Conn
	fail    atomic.Bool
	once    sync.Once
	entered chan struct{}
	release chan struct{}
}

func (c *gatedWriteFailureConn) Write(p []byte) (int, error) {
	if !c.fail.Load() {
		return c.Conn.Write(p)
	}
	c.once.Do(func() { close(c.entered) })
	<-c.release
	return 0, errors.New("synthetic aggregate credit publication failure")
}

func TestGrowingConnTransfersWhenGrowthGrantedOrDenied(t *testing.T) {
	for _, maximum := range []int64{2 << 20, 0} {
		for _, grant := range []bool{false, true} {
			name := "denied"
			if grant {
				name = "granted"
			}
			t.Run(fmt.Sprintf("%s/maximum-%d", name, maximum), func(t *testing.T) {
				left, right := net.Pipe()
				cfg, err := NewYamuxConfig(nil, DefaultAdaptiveReceiveWindowBytes)
				if err != nil {
					t.Fatal(err)
				}
				client, err := yamux.Client(left, cfg)
				if err != nil {
					t.Fatal(err)
				}
				defer client.Close()
				server, err := yamux.Server(right, cfg)
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
				_ = reader.SetDeadline(time.Now().Add(5 * time.Second))
				_ = writer.SetDeadline(time.Now().Add(5 * time.Second))
				var reserved atomic.Int64
				conn := NewGrowingConn(reader, maximum, func(size int64) (func(), bool) {
					if !grant {
						return nil, false
					}
					reserved.Add(size)
					return func() { reserved.Add(-size) }, true
				})
				// Drive measured WAN epochs deterministically, while exercising
				// real window updates and lease lifetime over a Yamux session.
				now := time.Now()
				conn.growth.start = now
				for range 4 {
					now = now.Add(80 * time.Millisecond)
					if err := conn.growMeasured(1<<20, now, 80*time.Millisecond); err != nil {
						t.Fatal(err)
					}
				}
				payload := bytes.Repeat([]byte("window growth"), 400_000)
				done := make(chan error, 1)
				go func() { _, err := writer.Write(payload); done <- err }()
				got := make([]byte, len(payload))
				if _, err := io.ReadFull(conn, got); err != nil {
					t.Fatal(err)
				}
				if !bytes.Equal(got, payload) {
					t.Fatal("transfer corrupted")
				}
				if err := <-done; err != nil {
					t.Fatal(err)
				}
				wantWindow := uint32(DefaultAdaptiveReceiveWindowBytes)
				if grant {
					wantWindow = 2 << 20
					if maximum == 0 {
						// Reads below use a real clock and may legitimately grow
						// further on a fast runner. The deterministic epochs
						// above must have granted at least 2 MiB.
						wantWindow = reader.MaxReceiveWindow()
						if wantWindow < 2<<20 || wantWindow > uint32(DefaultMaxStreamWindowSizeBytes) {
							t.Fatalf("default window escaped bounds: %d", wantWindow)
						}
					}
				}
				if reader.MaxReceiveWindow() != wantWindow {
					t.Fatalf("receive window=%d, want %d", reader.MaxReceiveWindow(), wantWindow)
				}
				if reserved.Load() != int64(yamux.ReceiveWindowMemory(wantWindow)-yamux.ReceiveWindowMemory(uint32(DefaultAdaptiveReceiveWindowBytes))) {
					t.Fatal("unaccounted receive credit")
				}
				_ = conn.Close()
				if grant && reserved.Load() == 0 {
					t.Fatal("local FIN released credit before peer FIN")
				}
				_ = writer.Close()
				if _, err := io.Copy(io.Discard, conn); err != nil {
					t.Fatal(err)
				}
				conn.Release()
				conn.Release()
				if reserved.Load() != 0 {
					t.Fatalf("credit leaked/double released: %d", reserved.Load())
				}
			})
		}
	}
}

func TestAggregateReceiveWindowGrowthSerializesSessionThenStreams(t *testing.T) {
	left, right := net.Pipe()
	clientConfig, err := NewAggregateYamuxConfig(nil, 4<<20, 8)
	if err != nil {
		t.Fatal(err)
	}
	serverConfig, err := NewAggregateYamuxConfig(nil, 4<<20, 8)
	if err != nil {
		t.Fatal(err)
	}
	client, err := yamux.Client(left, clientConfig)
	if err != nil {
		t.Fatal(err)
	}
	server, err := yamux.Server(right, serverConfig)
	if err != nil {
		client.Close()
		t.Fatal(err)
	}
	defer client.Close()

	writers := make([]*yamux.Stream, 2)
	readers := make([]*yamux.Stream, 2)
	for i := range writers {
		writers[i], err = client.OpenStream()
		if err != nil {
			t.Fatal(err)
		}
		readers[i], err = server.AcceptStream()
		if err != nil {
			t.Fatal(err)
		}
	}
	var reserved atomic.Int64
	var reserveCalls atomic.Int32
	growth := NewAggregateReceiveWindowGrowth(server, func(bytes int64) (func(), bool) {
		reserveCalls.Add(1)
		reserved.Add(bytes)
		return func() { reserved.Add(-bytes) }, true
	})

	const target = uint32(1 << 20)
	start := make(chan struct{})
	errs := make(chan error, len(readers))
	var wg sync.WaitGroup
	for _, reader := range readers {
		wg.Add(1)
		go func(reader *yamux.Stream) {
			defer wg.Done()
			<-start
			granted, growErr := growth.Grow(reader, target)
			if !granted && growErr == nil {
				growErr = fmt.Errorf("aggregate growth unexpectedly denied")
			}
			errs <- growErr
		}(reader)
	}
	close(start)
	wg.Wait()
	close(errs)
	for growErr := range errs {
		if growErr != nil {
			t.Fatal(growErr)
		}
	}
	stats := server.AggregateReceiveWindowStats()
	if stats.ReceiveLimit != 4<<20 {
		t.Fatalf("session receive limit = %d, want %d", stats.ReceiveLimit, 4<<20)
	}
	for i, reader := range readers {
		allocation, enabled := reader.AggregateReceiveAllocationWindow()
		if !enabled || allocation != target || reader.MaxReceiveWindow() != target {
			t.Fatalf("stream %d windows: allocation=%d enabled=%t bytes=%d", i, allocation, enabled, reader.MaxReceiveWindow())
		}
	}
	wantReserved := int64(yamux.AggregateReceiveWindowMemory(4<<20) - yamux.AggregateReceiveWindowMemory(AggregateInitialSessionWindowBytes))
	if got := reserved.Load(); got != wantReserved || reserveCalls.Load() != 1 {
		t.Fatalf("session growth reservation = %d in %d calls, want %d in one call", got, reserveCalls.Load(), wantReserved)
	}
	_ = server.Close()
	growth.Release()
	growth.Release()
	if got := reserved.Load(); got != 0 {
		t.Fatalf("session growth reservation retained after close: %d", got)
	}
}

func TestAggregateReceiveWindowGrowthDenialPreservesExistingTrafficAndControl(t *testing.T) {
	left, right := net.Pipe()
	clientConfig, err := NewAggregateYamuxConfig(nil, 4<<20, 8)
	if err != nil {
		t.Fatal(err)
	}
	serverConfig, err := NewAggregateYamuxConfig(nil, 4<<20, 8)
	if err != nil {
		t.Fatal(err)
	}
	client, err := yamux.Client(left, clientConfig)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	server, err := yamux.Server(right, serverConfig)
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
	growth := NewAggregateReceiveWindowGrowth(server, func(int64) (func(), bool) { return nil, false })
	granted, err := growth.Grow(reader, 1<<20)
	if err != nil || granted {
		t.Fatalf("denied growth = granted %t, error %v", granted, err)
	}
	stats := server.AggregateReceiveWindowStats()
	allocation, enabled := reader.AggregateReceiveAllocationWindow()
	if stats.ReceiveLimit != AggregateInitialSessionWindowBytes || !enabled || allocation != AggregateInitialStreamWindowBytes || reader.MaxReceiveWindow() != uint32(DefaultAdaptiveReceiveWindowBytes) {
		t.Fatalf("denied growth changed credit: stats=%+v allocation=%d enabled=%t bytes=%d", stats, allocation, enabled, reader.MaxReceiveWindow())
	}

	payload := bytes.Repeat([]byte{0x7a}, 128<<10)
	writeDone := make(chan error, 1)
	go func() {
		_, writeErr := writer.Write(payload)
		writeDone <- writeErr
	}()
	got := make([]byte, len(payload))
	if _, err := io.ReadFull(reader, got); err != nil {
		t.Fatal(err)
	}
	if err := <-writeDone; err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, payload) {
		t.Fatal("traffic after denied session growth was corrupted")
	}
	if _, err := server.Ping(); err != nil {
		t.Fatalf("control traffic failed after denied session growth: %v", err)
	}
}

func TestAggregateGrowthPublicationFailureRetainsLeaseThroughClose(t *testing.T) {
	left, right := net.Pipe()
	failing := &gatedWriteFailureConn{
		Conn:    right,
		entered: make(chan struct{}),
		release: make(chan struct{}),
	}
	var unblockOnce sync.Once
	unblockWriter := func() { unblockOnce.Do(func() { close(failing.release) }) }
	clientConfig, err := NewAggregateYamuxConfig(nil, 4<<20, 8)
	if err != nil {
		t.Fatal(err)
	}
	serverConfig, err := NewAggregateYamuxConfig(nil, 4<<20, 8)
	if err != nil {
		t.Fatal(err)
	}
	client, err := yamux.Client(left, clientConfig)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	server, err := yamux.Server(failing, serverConfig)
	if err != nil {
		t.Fatal(err)
	}
	writer, err := client.OpenStream()
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()
	reader, err := server.AcceptStream()
	if err != nil {
		t.Fatal(err)
	}
	var reserved atomic.Int64
	var releases atomic.Int32
	growth := NewAggregateReceiveWindowGrowth(server, func(bytes int64) (func(), bool) {
		reserved.Add(bytes)
		return func() {
			releases.Add(1)
			reserved.Add(-bytes)
		}, true
	})
	defer func() {
		unblockWriter()
		_ = server.Close()
		growth.Release()
	}()

	failing.fail.Store(true)
	growDone := make(chan error, 1)
	go func() {
		granted, growErr := growth.Grow(reader, 1<<20)
		if !granted && growErr == nil {
			growErr = errors.New("ambiguous aggregate growth was not committed")
		}
		growDone <- growErr
	}()
	select {
	case <-failing.entered:
	case <-time.After(time.Second):
		t.Fatal("aggregate credit publication did not reach the gated writer")
	}
	if got := reserved.Load(); got == 0 {
		t.Fatal("session growth lease was not retained before publication")
	}
	closeDone := make(chan struct{})
	go func() {
		_ = server.Close()
		close(closeDone)
	}()
	select {
	case <-closeDone:
		t.Fatal("Session.Close returned while the physical writer still owned publication")
	case <-time.After(25 * time.Millisecond):
	}
	if got := reserved.Load(); got == 0 || releases.Load() != 0 {
		t.Fatalf("growth lease released before close completed: reserved=%d releases=%d", got, releases.Load())
	}
	unblockWriter()
	select {
	case err := <-growDone:
		if err == nil {
			t.Fatal("aggregate growth unexpectedly published through failing writer")
		}
	case <-time.After(time.Second):
		t.Fatal("aggregate growth did not return after writer failure")
	}
	select {
	case <-closeDone:
	case <-time.After(time.Second):
		t.Fatal("Session.Close did not join after the blocked writer returned")
	}
	stats := server.AggregateReceiveWindowStats()
	if !stats.Closed || stats.TotalUnits != 0 || stats.FreeUnits != 0 || stats.BufferedUnits != 0 {
		t.Fatalf("aggregate pool retained after Close: %+v", stats)
	}
	if reserved.Load() == 0 {
		t.Fatal("ambiguous publication lease released before explicit growth cleanup")
	}
	growth.Release()
	growth.Release()
	if got := reserved.Load(); got != 0 || releases.Load() != 1 {
		t.Fatalf("growth cleanup reserved=%d releases=%d, want zero/one", got, releases.Load())
	}
}

func TestAggregateGrowingConnMeasuresAllocationWindowIndependently(t *testing.T) {
	left, right := net.Pipe()
	clientConfig, err := NewAggregateYamuxConfig(nil, 4<<20, 8)
	if err != nil {
		t.Fatal(err)
	}
	serverConfig, err := NewAggregateYamuxConfig(nil, 4<<20, 8)
	if err != nil {
		t.Fatal(err)
	}
	client, err := yamux.Client(left, clientConfig)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	server, err := yamux.Server(right, serverConfig)
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	writer, err := client.OpenStream()
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()
	reader, err := server.AcceptStream()
	if err != nil {
		t.Fatal(err)
	}
	var reserved atomic.Int64
	growth := NewAggregateReceiveWindowGrowth(server, func(bytes int64) (func(), bool) {
		reserved.Add(bytes)
		return func() { reserved.Add(-bytes) }, true
	})
	conn := NewAggregateGrowingConn(reader, 4<<20, growth)
	now := time.Now()
	conn.growth.start = now
	if err := conn.growMeasured(1<<20, now.Add(80*time.Millisecond), 80*time.Millisecond); err != nil {
		t.Fatal(err)
	}
	if err := conn.growMeasured(1<<20, now.Add(160*time.Millisecond), 80*time.Millisecond); err != nil {
		t.Fatal(err)
	}
	allocation, enabled := reader.AggregateReceiveAllocationWindow()
	if !enabled || allocation != 1<<20 || reader.MaxReceiveWindow() != 1<<20 {
		t.Fatalf("measured aggregate growth: allocation=%d enabled=%t bytes=%d", allocation, enabled, reader.MaxReceiveWindow())
	}
	if reserved.Load() == 0 {
		t.Fatal("measured aggregate growth did not reserve the session pool")
	}
	_ = server.Close()
	growth.Release()
	if reserved.Load() != 0 {
		t.Fatal("measured aggregate growth leaked its session reservation")
	}
}

func TestAggregateGrowingConnClampsMeasuredTargetToNegotiatedAllocationMaximum(t *testing.T) {
	newConfig := func() *yamux.Config {
		cfg := yamux.DefaultConfig()
		cfg.MaxStreamWindowSize = uint32(DefaultAdaptiveReceiveWindowBytes)
		cfg.InitialSessionWindowSize = 2 << 20
		cfg.MaxSessionWindowSize = 4 << 20
		cfg.MaxDataFrameSize = yamux.AggregateMaxDataFrameSize
		cfg.InitialStreamAllocationWindowSize = 512 << 10
		cfg.MaxStreamAllocationWindowSize = 1 << 20
		cfg.MaxIncomingStreams = 8
		return cfg
	}
	left, right := net.Pipe()
	client, err := yamux.Client(left, newConfig())
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	server, err := yamux.Server(right, newConfig())
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	writer, err := client.OpenStream()
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()
	reader, err := server.AcceptStream()
	if err != nil {
		t.Fatal(err)
	}
	growth := NewAggregateReceiveWindowGrowth(server, func(int64) (func(), bool) { return func() {}, true })
	conn := NewAggregateGrowingConn(reader, DefaultMaxStreamWindowSizeBytes, growth)
	if conn.maximum != 1<<20 {
		t.Fatalf("measured aggregate maximum = %d, want negotiated allocation ceiling %d", conn.maximum, 1<<20)
	}
	now := time.Now()
	conn.growth.start = now
	for epoch := 1; epoch <= 4; epoch++ {
		if err := conn.growMeasured(1<<20, now.Add(time.Duration(epoch)*80*time.Millisecond), 80*time.Millisecond); err != nil {
			t.Fatalf("measured growth epoch %d failed at negotiated ceiling: %v", epoch, err)
		}
	}
	allocation, enabled := reader.AggregateReceiveAllocationWindow()
	if !enabled || allocation != 1<<20 || reader.MaxReceiveWindow() != 1<<20 {
		t.Fatalf("clamped aggregate windows: allocation=%d enabled=%t bytes=%d", allocation, enabled, reader.MaxReceiveWindow())
	}
}

func TestGrowingConnRetainsReservationWhenWindowUpdateFails(t *testing.T) {
	left, right := net.Pipe()
	cfg, err := NewYamuxConfig(nil, DefaultAdaptiveReceiveWindowBytes)
	if err != nil {
		t.Fatal(err)
	}
	client, err := yamux.Client(left, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	server, err := yamux.Server(right, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = server.Close() })
	writer, err := client.OpenStream()
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()
	reader, err := server.AcceptStream()
	if err != nil {
		t.Fatal(err)
	}
	var reserved atomic.Int64
	conn := NewGrowingConn(reader, 2<<20, func(n int64) (func(), bool) {
		reserved.Add(n)
		return func() { reserved.Add(-n) }, true
	})
	_ = server.Close()
	now := time.Now()
	conn.growth.start = now
	if err := conn.growMeasured(1<<20, now.Add(80*time.Millisecond), 80*time.Millisecond); err != nil {
		t.Fatal(err)
	}
	if err := conn.growMeasured(1<<20, now.Add(160*time.Millisecond), 80*time.Millisecond); err == nil {
		t.Fatal("expected window update failure on closed session")
	}
	if reserved.Load() == 0 {
		t.Fatal("uncertain window update released committed credit")
	}
	conn.Release()
	conn.Release()
	if reserved.Load() != 0 {
		t.Fatal("failed update reservation leaked or released twice")
	}
}
