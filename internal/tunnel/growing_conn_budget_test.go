package tunnel

import (
	"bytes"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hashicorp/yamux"
)

// TestGrowingConnConcurrentBudgetAndResetLifetime exercises the resource
// callback across real Yamux readers. Only one stream may consume the shared
// increment; closing either side does not release a committed increment until
// the owner explicitly releases it, and repeated Release calls are harmless.
func TestGrowingConnConcurrentBudgetAndResetLifetime(t *testing.T) {
	const (
		streamCount = 2
		initial     = uint32(DefaultAdaptiveReceiveWindowBytes)
		maximum     = uint32(2 << 20)
	)

	for _, reset := range []bool{false, true} {
		name := "peer-fin"
		if reset {
			name = "session-reset"
		}
		t.Run(name, func(t *testing.T) {
			left, right := net.Pipe()
			cfg, err := NewYamuxConfig(nil, DefaultAdaptiveReceiveWindowBytes)
			if err != nil {
				t.Fatal(err)
			}
			client, err := yamux.Client(left, cfg)
			if err != nil {
				t.Fatal(err)
			}
			server, err := yamux.Server(right, cfg)
			if err != nil {
				_ = client.Close()
				t.Fatal(err)
			}
			t.Cleanup(func() {
				_ = client.Close()
				_ = server.Close()
			})

			writers := make([]*yamux.Stream, streamCount)
			readers := make([]*yamux.Stream, streamCount)
			for i := range writers {
				writers[i], err = client.OpenStream()
				if err != nil {
					t.Fatal(err)
				}
				readers[i], err = server.AcceptStream()
				if err != nil {
					t.Fatal(err)
				}
				_ = writers[i].SetDeadline(time.Now().Add(5 * time.Second))
				_ = readers[i].SetDeadline(time.Now().Add(5 * time.Second))
			}

			// The second measured epoch proposes a 1 MiB window for each stream.
			// Budget exactly one increment so concurrent callbacks must serialize
			// through the shared accounting boundary.
			increment := int64(yamux.ReceiveWindowMemory(1<<20) - yamux.ReceiveWindowMemory(initial))
			var outstanding atomic.Int64
			var peak atomic.Int64
			reserve := func(size int64) (func(), bool) {
				for {
					current := outstanding.Load()
					if size < 0 || current > increment-size {
						return nil, false
					}
					if !outstanding.CompareAndSwap(current, current+size) {
						continue
					}
					for {
						observed := peak.Load()
						if observed >= current+size || peak.CompareAndSwap(observed, current+size) {
							break
						}
					}
					var once sync.Once
					return func() { once.Do(func() { outstanding.Add(-size) }) }, true
				}
			}

			conns := make([]*GrowingConn, streamCount)
			for i, reader := range readers {
				conns[i] = &GrowingConn{
					Conn:    reader,
					stream:  reader,
					maximum: maximum,
					reserve: reserve,
				}
			}

			payload := bytes.Repeat([]byte("concurrent-window"), 4096)
			var writersWG sync.WaitGroup
			for _, writer := range writers {
				writersWG.Add(1)
				go func(writer *yamux.Stream) {
					defer writersWG.Done()
					if _, writeErr := writer.Write(payload); writeErr != nil {
						t.Errorf("write payload: %v", writeErr)
					}
				}(writer)
			}

			base := time.Now()
			var readersWG sync.WaitGroup
			for _, conn := range conns {
				readersWG.Add(1)
				go func(conn *GrowingConn) {
					defer readersWG.Done()
					got := make([]byte, len(payload))
					if _, readErr := io.ReadFull(conn, got); readErr != nil {
						t.Errorf("read payload: %v", readErr)
						return
					}
					if !bytes.Equal(got, payload) {
						t.Error("payload corrupted")
					}
					conn.mu.Lock()
					conn.growth = receiveWindowGrowth{start: base}
					conn.mu.Unlock()
					if err := conn.growMeasured(1<<20, base.Add(80*time.Millisecond), 80*time.Millisecond); err != nil {
						t.Errorf("first growth epoch: %v", err)
						return
					}
					if err := conn.growMeasured(1<<20, base.Add(160*time.Millisecond), 80*time.Millisecond); err != nil {
						t.Errorf("second growth epoch: %v", err)
					}
				}(conn)
			}
			readersWG.Wait()
			writersWG.Wait()

			if got := peak.Load(); got > increment {
				t.Fatalf("shared growth budget exceeded: peak=%d budget=%d", got, increment)
			}
			if got := outstanding.Load(); got != increment {
				t.Fatalf("exactly one growth reservation should remain: got=%d want=%d", got, increment)
			}

			if reset {
				for _, conn := range conns {
					_ = conn.SetReadDeadline(time.Now().Add(time.Second))
				}
				if err := client.Close(); err != nil {
					t.Fatal(err)
				}
				for _, conn := range conns {
					var one [1]byte
					if _, err := conn.Read(one[:]); err == nil {
						t.Fatal("session reset did not terminate the peer stream")
					}
				}
			} else {
				for _, writer := range writers {
					_ = writer.Close()
				}
				for _, conn := range conns {
					_ = conn.Close()
				}
				for _, conn := range conns {
					_ = conn.SetReadDeadline(time.Now().Add(time.Second))
					var one [1]byte
					if _, err := conn.Read(one[:]); err == nil {
						t.Fatal("peer FIN did not terminate the stream")
					}
				}
			}
			if got := outstanding.Load(); got != increment {
				t.Fatalf("transport close released committed credit before cleanup: %d", got)
			}
			for _, conn := range conns {
				conn.Release()
				conn.Release()
			}
			if got := outstanding.Load(); got != 0 {
				t.Fatalf("reservation after idempotent cleanup = %d, want 0", got)
			}
		})
	}
}
