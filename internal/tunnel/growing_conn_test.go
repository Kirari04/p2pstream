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
						wantWindow = 4 << 20
					}
				}
				if reader.MaxReceiveWindow() != wantWindow {
					t.Fatalf("receive window=%d, want %d", reader.MaxReceiveWindow(), wantWindow)
				}
				if reserved.Load() != int64(wantWindow)-DefaultAdaptiveReceiveWindowBytes {
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
