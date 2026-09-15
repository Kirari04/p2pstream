package tunnel

import (
	"bytes"
	"fmt"
	"io"
	"net"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hashicorp/yamux"
)

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
