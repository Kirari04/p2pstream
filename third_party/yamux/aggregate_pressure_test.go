package yamux

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
	"testing"
	"time"
)

func pressurePair(t *testing.T, timeout time.Duration) (*Session, *Session) {
	t.Helper()
	config := aggregateTestConfig()
	config.InitialSessionWindowSize = 2 << 20
	config.InitialStreamAllocationWindowSize = 512 << 10
	config.MaxStreamWindowSize = 512 << 10
	config.AggregateReceivePressureTimeout = timeout
	left, right := net.Pipe()
	client, err := Client(left, config)
	if err != nil {
		t.Fatal(err)
	}
	server, err := Server(right, config)
	if err != nil {
		client.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() { client.Close(); server.Close() })
	return client, server
}

func awaitPressure(t *testing.T, description string, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for !condition() {
		if time.Now().After(deadline) {
			t.Fatal(description)
		}
		time.Sleep(time.Millisecond)
	}
}

// All transfers use real session readers/writers. No growth callback is invoked:
// the same fixed pool must make progress even when external growth is denied.
func TestAggregatePressureSameLaneProgress(t *testing.T) {
	for _, reverse := range []bool{false, true} {
		for _, count := range []int{4, 16} {
			for _, trickle := range []int{0, 1, 4096} {
				t.Run(fmt.Sprintf("reverse=%t/holders=%d/trickle=%d", reverse, count, trickle), func(t *testing.T) {
					client, server := pressurePair(t, 120*time.Millisecond)
					sender, receiver := client, server
					if reverse {
						sender, receiver = server, client
					}
					var wg sync.WaitGroup
					stop := make(chan struct{})
					// Join every writer/trickler before returning, including failure paths.
					defer func() { close(stop); client.Close(); server.Close(); wg.Wait() }()
					readers := make([]*Stream, 0, count)
					payload := bytes.Repeat([]byte("x"), 4<<20)
					for i := 0; i < count; i++ {
						writer, err := sender.OpenStream()
						if err != nil {
							t.Fatal(err)
						}
						reader, err := receiver.AcceptStream()
						if err != nil {
							t.Fatal(err)
						}
						readers = append(readers, reader)
						wg.Add(1)
						go func() { defer wg.Done(); writer.Write(payload) }()
					}
					awaitPressure(t, "holders never exhausted bounded pool", func() bool { return receiver.AggregateReceiveWindowStats().FreeUnits == 0 })
					if trickle > 0 {
						for _, reader := range readers {
							wg.Add(1)
							go func(reader *Stream) {
								defer wg.Done()
								tick := time.NewTicker(10 * time.Millisecond)
								defer tick.Stop()
								buf := make([]byte, trickle)
								for {
									select {
									case <-stop:
										return
									case <-tick.C:
										reader.SetReadDeadline(time.Now().Add(20 * time.Millisecond))
										if _, err := reader.Read(buf); err != nil {
											return
										}
									}
								}
							}(reader)
						}
					}
					writer, err := sender.OpenStream()
					if err != nil {
						t.Fatal(err)
					}
					reader, err := receiver.AcceptStream()
					if err != nil {
						t.Fatal(err)
					}
					wg.Add(1)
					go func() { defer wg.Done(); writer.Write([]byte("fresh")) }()
					reader.SetReadDeadline(time.Now().Add(2 * time.Second))
					got := make([]byte, 5)
					if _, err := io.ReadFull(reader, got); err != nil || string(got) != "fresh" {
						t.Fatalf("fresh same-lane read %q: %v", got, err)
					}
					awaitPressure(t, "trickle/refill renewed monopolized ownership", func() bool { return receiver.AggregateReceiveWindowStats().PressureResets > 0 })
					stats := receiver.AggregateReceiveWindowStats()
					if stats.ReceiveLimit != 2<<20 || stats.TotalUnits != 512 {
						t.Fatalf("pool grew: %+v", stats)
					}
					if trickle == 0 && stats.PressureResets == 0 {
						t.Fatal("progress did not exercise eviction")
					}
					awaitPressure(t, "eviction did not reach peer as ordered RST", func() bool { return sender.NumStreams() < count+1 })
					if _, err := sender.Ping(); err != nil {
						t.Fatalf("lane failed after pressure RST: %v", err)
					}
					if sender.IsClosed() || receiver.IsClosed() {
						t.Fatal("eviction killed lane")
					}
				})
			}
		}
	}
}

func TestAggregatePressureFINBufferReportsErrorAndIdleStreamSurvives(t *testing.T) {
	client, server := pressurePair(t, 100*time.Millisecond)
	idle, err := client.OpenStream()
	if err != nil {
		t.Fatal(err)
	}
	idlePeer, err := server.AcceptStream()
	if err != nil {
		t.Fatal(err)
	}
	readers := make([]*Stream, 0, 4)
	for i := 0; i < 4; i++ {
		writer, err := client.OpenStream()
		if err != nil {
			t.Fatal(err)
		}
		reader, err := server.AcceptStream()
		if err != nil {
			t.Fatal(err)
		}
		if _, err := writer.Write(make([]byte, 512<<10)); err != nil {
			t.Fatal(err)
		}
		if err := writer.Close(); err != nil {
			t.Fatal(err)
		}
		if err := reader.Close(); err != nil {
			t.Fatal(err)
		}
		readers = append(readers, reader)
	}
	awaitPressure(t, "FIN owners not evicted", func() bool { return server.AggregateReceiveWindowStats().PressureResets > 0 })
	evicted := false
	for _, reader := range readers {
		reader.stateLock.Lock()
		err := reader.pressureErr
		reader.stateLock.Unlock()
		if err != nil {
			evicted = true
			if _, err := reader.Read(make([]byte, 1)); !errors.Is(err, ErrReceivePressure) {
				t.Fatalf("eviction returned successful EOF: %v", err)
			}
		}
	}
	if !evicted {
		t.Fatal("no FIN-buffer owner evicted")
	}
	if _, err := idle.Write([]byte("idle healthy")); err != nil {
		t.Fatal(err)
	}
	idlePeer.SetReadDeadline(time.Now().Add(time.Second))
	got := make([]byte, 12)
	if _, err := io.ReadFull(idlePeer, got); err != nil || string(got) != "idle healthy" {
		t.Fatalf("idle empty stream penalized: %q,%v", got, err)
	}
	server.Close()
	client.Close()
	select {
	case <-server.aggregate.pressureDone:
	default:
		t.Fatal("Close did not join monitor")
	}
	stats := server.AggregateReceiveWindowStats()
	if stats.BufferedUnits != 0 || stats.TotalUnits != 0 {
		t.Fatalf("shutdown retained units: %+v", stats)
	}
}

func TestAggregatePressureResetReadGrowthShutdownRace(t *testing.T) {
	for iteration := 0; iteration < 8; iteration++ {
		client, server := pressurePair(t, 8*time.Millisecond)
		var wg sync.WaitGroup
		for i := 0; i < 4; i++ {
			writer, err := client.OpenStream()
			if err != nil {
				t.Fatal(err)
			}
			reader, err := server.AcceptStream()
			if err != nil {
				t.Fatal(err)
			}
			wg.Add(2)
			go func() { defer wg.Done(); writer.Write(make([]byte, 1<<20)); writer.Close() }()
			go func() {
				defer wg.Done()
				time.Sleep(10 * time.Millisecond)
				reader.Read(make([]byte, 4096))
				reader.Reset()
			}()
		}
		wg.Add(1)
		go func() { defer wg.Done(); server.GrowReceiveWindow(4 << 20) }()
		time.Sleep(15 * time.Millisecond)
		client.Close()
		server.Close()
		wg.Wait()
		if stats := server.AggregateReceiveWindowStats(); stats.TotalUnits != 0 || stats.PendingRefund != 0 {
			t.Fatalf("cleanup: %+v", stats)
		}
	}
}
