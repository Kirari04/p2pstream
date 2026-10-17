package tunnel

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"math"
	"net"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hashicorp/yamux"
)

func TestEarlyShareDemandThresholdSaturatesAndAttemptsOnce(t *testing.T) {
	c := &GrowingConn{}
	if c.takeEarlyShareAttempt(earlyShareThreshold - 1) {
		t.Fatal("attempt before threshold")
	}
	if c.earlyShareConsumed != earlyShareThreshold-1 {
		t.Fatal("lost consumed bytes")
	}
	if !c.takeEarlyShareAttempt(math.MaxUint64) {
		t.Fatal("threshold did not trigger")
	}
	if c.earlyShareConsumed != earlyShareThreshold {
		t.Fatal("counter did not saturate")
	}
	if c.takeEarlyShareAttempt(math.MaxUint64) || c.takeEarlyShareAttempt(1) {
		t.Fatal("attempt repeated")
	}
	otherDirection := &GrowingConn{}
	if otherDirection.takeEarlyShareAttempt(1) {
		t.Fatal("directions shared threshold")
	}
}

func TestEarlyShareTargetUsesOnlyBoundedWholeUnits(t *testing.T) {
	for _, tt := range []struct {
		name                            string
		pool, maximum, negotiated, want uint32
	}{
		{"initial_pool", 2 << 20, 64 << 20, 64 << 20, 512 << 10},
		{"grown_pool", 8 << 20, 64 << 20, 64 << 20, 2 << 20},
		{"absolute_cap", 256 << 20, 64 << 20, 64 << 20, 4 << 20},
		{"operator", 32 << 20, 1 << 20, 64 << 20, 1 << 20},
		{"negotiated", 32 << 20, 64 << 20, 1 << 20, 1 << 20},
		{"round_down", 32 << 20, (1 << 20) + 4095, 64 << 20, 1 << 20},
		{"subunit", 16383, 64 << 20, 64 << 20, 0},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := backedEarlyShareTarget(tt.pool, tt.maximum, tt.negotiated); got != tt.want {
				t.Fatalf("target=%d want=%d", got, tt.want)
			}
		})
	}
}

// New helper only for the new ownership contracts; old fixtures are unchanged.
func earlySharePair(t *testing.T) (*yamux.Session, *yamux.Session, *yamux.Stream, *yamux.Stream) {
	t.Helper()
	left, right := net.Pipe()
	deadline := time.Now().Add(5 * time.Second)
	_ = left.SetDeadline(deadline)
	_ = right.SetDeadline(deadline)
	cfg, err := NewAggregateYamuxConfig(nil, 64<<20, 8)
	if err != nil {
		t.Fatal(err)
	}
	client, err := yamux.Client(left, cfg)
	if err != nil {
		t.Fatal(err)
	}
	server, err := yamux.Server(right, cfg)
	if err != nil {
		client.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close(); _ = server.Close() })
	writer, err := client.OpenStream()
	if err != nil {
		t.Fatal(err)
	}
	reader, err := server.AcceptStream()
	if err != nil {
		t.Fatal(err)
	}
	return client, server, writer, reader
}

func TestEarlyShareDoesNotReserveOrGrowInitialPool(t *testing.T) {
	_, server, _, reader := earlySharePair(t)
	owner := NewAggregateReceiveWindowGrowth(server, func(int64) (func(), bool) { t.Fatal("early share reserved"); return nil, false })
	got, err := owner.tryBackedReceiveShare(reader, 64<<20, true)
	if err != nil || got.Outcome != "no_larger_target" {
		t.Fatalf("result=%+v err=%v", got, err)
	}
	if got.Before != got.After || got.Target != 512<<10 {
		t.Fatalf("no-op changed state: %+v", got)
	}
}

func TestEarlyShareUsesGrownBackingWithoutNewLease(t *testing.T) {
	client, server, _, donor := earlySharePair(t)
	var calls atomic.Int32
	owner := NewAggregateReceiveWindowGrowth(server, func(int64) (func(), bool) { calls.Add(1); return func() {}, true })
	if ok, err := owner.Grow(donor, 2<<20); !ok || err != nil {
		t.Fatalf("prime backing: %v %v", ok, err)
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
	beforeCalls := calls.Load()
	got, err := owner.tryBackedReceiveShare(reader, 64<<20, true)
	if err != nil || got.Outcome != "granted" || got.PublicationStage != "byte_complete" {
		t.Fatalf("result=%+v err=%v", got, err)
	}
	if got.Target != 2<<20 || got.After.ByteWindow != 2<<20 || got.After.AllocationWindow != 2<<20 {
		t.Fatalf("incorrect grant: %+v", got)
	}
	if calls.Load() != beforeCalls || got.Before.PoolReceiveLimit != got.After.PoolReceiveLimit || got.Before.PoolTotalUnits != got.After.PoolTotalUnits || got.Before.ReservationLeases != got.After.ReservationLeases {
		t.Fatalf("grant changed backing: %+v", got)
	}
	// An already-larger stream never shrinks, even with a lower operator target.
	smaller, err := owner.tryBackedReceiveShare(reader, 512<<10, true)
	if err != nil || smaller.Before != smaller.After || smaller.Outcome != "no_larger_target" {
		t.Fatalf("shrink: %+v %v", smaller, err)
	}
	disabled, err := owner.tryBackedReceiveShare(donor, 64<<20, false)
	if err != nil || disabled.Before != disabled.After || disabled.Outcome != "disabled" || disabled.PublicationStage != "none" {
		t.Fatalf("disabled: %+v %v", disabled, err)
	}
}

func TestEarlyShareValidatesOwnerBeforeNoop(t *testing.T) {
	client, server, writer, reader := earlySharePair(t)
	owner := NewAggregateReceiveWindowGrowth(server, func(int64) (func(), bool) { t.Fatal("unexpected reserve"); return nil, false })
	if _, err := owner.tryBackedReceiveShare(writer, 512<<10, true); err == nil {
		t.Fatal("accepted opposite session")
	}
	if _, err := owner.tryBackedReceiveShare(nil, 512<<10, true); err == nil {
		t.Fatal("accepted nil stream")
	}
	_ = client.Close()
	_ = server.Close()
	got, err := owner.tryBackedReceiveShare(reader, 512<<10, true)
	if !errors.Is(err, yamux.ErrSessionShutdown) || got.Outcome != "owner_closed" {
		t.Fatalf("closed owner: %+v %v", got, err)
	}
}

func TestEarlyShareStalledConsumerLeavesOtherAdmittedStreamUsable(t *testing.T) {
	client, server, stalledWriter, stalledReader := earlySharePair(t)
	owner := NewAggregateReceiveWindowGrowth(server, func(int64) (func(), bool) { return func() {}, true })
	if ok, err := owner.Grow(stalledReader, 2<<20); !ok || err != nil {
		t.Fatalf("prime: %v %v", ok, err)
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
	got, err := owner.tryBackedReceiveShare(reader, 64<<20, true)
	if err != nil || got.Outcome != "granted" {
		t.Fatalf("grant: %+v %v", got, err)
	}
	// This is an intentionally hostile stall after eligibility. Total advertised
	// stream shares do not bypass the shared pool's allocation credit.
	done := make(chan error, 1)
	go func() { _, err := stalledWriter.Write(make([]byte, 2<<20)); done <- err }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("stalled stream did not fill its permitted share")
	}
	stats := server.AggregateReceiveWindowStats()
	if stats.BufferedUnits > stats.TotalUnits || stats.TotalUnits != got.Before.PoolTotalUnits || stats.ReceiveLimit != got.Before.PoolReceiveLimit {
		t.Fatalf("shared backing exceeded: %+v", stats)
	}
	payload := bytes.Repeat([]byte{0x5a}, 128<<10)
	go func() { _, err := writer.Write(payload); done <- err }()
	received := make([]byte, len(payload))
	if _, err := io.ReadFull(reader, received); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(received, payload) {
		t.Fatal("other stream corrupted")
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("other admitted stream stalled despite available shared pool")
	}
}

type earlyShareSentinelFailureConn struct {
	net.Conn
	armed    atomic.Bool
	streamID atomic.Uint32
	failType byte
}

func (c *earlyShareSentinelFailureConn) Write(p []byte) (int, error) {
	if c.armed.Load() && len(p) == 12 && p[1] == c.failType && binary.BigEndian.Uint32(p[4:8]) == c.streamID.Load() && binary.BigEndian.Uint32(p[8:12]) > 0 {
		return 0, yamux.ErrStreamClosed // A transport error, NOT a state check.
	}
	return c.Conn.Write(p)
}

func TestEarlyShareDoesNotSuppressAmbiguousAllocationOrByteSentinel(t *testing.T) {
	for _, tt := range []struct {
		name    string
		kind    byte
		outcome string
	}{
		{"allocation_transport", 5, "allocation_error"},
		{"byte_transport", 1, "byte_error"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			left, right := net.Pipe()
			deadline := time.Now().Add(5 * time.Second)
			_ = left.SetDeadline(deadline)
			_ = right.SetDeadline(deadline)
			failing := &earlyShareSentinelFailureConn{Conn: right, failType: tt.kind}
			cfg, err := NewAggregateYamuxConfig(nil, 64<<20, 8)
			if err != nil {
				t.Fatal(err)
			}
			client, err := yamux.Client(left, cfg)
			if err != nil {
				t.Fatal(err)
			}
			defer client.Close()
			server, err := yamux.Server(failing, cfg)
			if err != nil {
				t.Fatal(err)
			}
			defer server.Close()
			var leases atomic.Int32
			owner := NewAggregateReceiveWindowGrowth(server, func(int64) (func(), bool) { leases.Add(1); return func() { leases.Add(-1) }, true })
			_, err = client.OpenStream()
			if err != nil {
				t.Fatal(err)
			}
			donor, err := server.AcceptStream()
			if err != nil {
				t.Fatal(err)
			}
			if ok, err := owner.Grow(donor, 2<<20); !ok || err != nil {
				t.Fatalf("prime: %v %v", ok, err)
			}
			_, err = client.OpenStream()
			if err != nil {
				t.Fatal(err)
			}
			reader, err := server.AcceptStream()
			if err != nil {
				t.Fatal(err)
			}
			failing.streamID.Store(reader.StreamID())
			failing.armed.Store(true)
			got, err := owner.tryBackedReceiveShare(reader, 64<<20, true)
			if err == nil || got.Outcome != tt.outcome {
				t.Fatalf("ambiguous error suppressed: %+v %v", got, err)
			}
			if got.After.AllocationWindow <= got.Before.AllocationWindow {
				t.Fatalf("allocation mutation missing: %+v", got)
			}
			if leases.Load() != 1 {
				t.Fatal("ambiguous publication released backing")
			}
			_ = server.Close()
			owner.Release()
			if leases.Load() != 0 {
				t.Fatal("backing not released after session close")
			}
		})
	}
}

func TestEarlyShareFourStallsStayBoundedControlAndDrainRecover(t *testing.T) {
	client, server, firstWriter, firstReader := earlySharePair(t)
	var reservations atomic.Int32
	owner := NewAggregateReceiveWindowGrowth(server, func(int64) (func(), bool) { reservations.Add(1); return func() {}, true })
	if ok, err := owner.Grow(firstReader, 2<<20); !ok || err != nil {
		t.Fatalf("prime: %v %v", ok, err)
	}
	writers := []*yamux.Stream{firstWriter}
	readers := []*yamux.Stream{firstReader}
	for i := 0; i < 4; i++ {
		writer, err := client.OpenStream()
		if err != nil {
			t.Fatal(err)
		}
		reader, err := server.AcceptStream()
		if err != nil {
			t.Fatal(err)
		}
		writers = append(writers, writer)
		readers = append(readers, reader)
	}
	beforeReservations := reservations.Load()
	for _, reader := range readers[:4] {
		result, err := owner.tryBackedReceiveShare(reader, 64<<20, true)
		if err != nil || result.Target != 2<<20 {
			t.Fatalf("quarter-share: %+v %v", result, err)
		}
	}
	if reservations.Load() != beforeReservations {
		t.Fatal("early share reserved more backing")
	}
	done := make(chan error, 4)
	for _, writer := range writers[:4] {
		go func(writer *yamux.Stream) { _, err := writer.Write(make([]byte, 2<<20)); done <- err }(writer)
	}
	for i := 0; i < 4; i++ {
		select {
		case err := <-done:
			if err != nil {
				t.Fatal(err)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("quarter-share stalls did not fill backing")
		}
	}
	stats := server.AggregateReceiveWindowStats()
	if stats.ReceiveLimit != 8<<20 || stats.FreeUnits != 0 || stats.BufferedUnits != stats.TotalUnits {
		t.Fatalf("expected bounded full pool: %+v", stats)
	}
	fifth := make(chan error, 1)
	go func() { _, err := writers[4].Write([]byte{0x7b}); fifth <- err }()
	select {
	case err := <-fifth:
		t.Fatalf("fifth data write escaped full shared pool: %v", err)
	case <-time.After(20 * time.Millisecond):
	}
	// No fifth-stream data progress is promised at full occupancy; control
	// frames must still be processed independently of payload allocation.
	if _, err := client.Ping(); err != nil {
		t.Fatalf("control stalled behind full payload pool: %v", err)
	}
	if _, err := io.CopyN(io.Discard, readers[0], 2<<20); err != nil {
		t.Fatal(err)
	}
	one := make([]byte, 1)
	if _, err := io.ReadFull(readers[4], one); err != nil || one[0] != 0x7b {
		t.Fatalf("data recovery after drain: %x %v", one, err)
	}
	select {
	case err := <-fifth:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("fifth write did not recover after drain")
	}
	stats = server.AggregateReceiveWindowStats()
	if stats.ReceiveLimit != 8<<20 || stats.BufferedUnits > stats.TotalUnits || reservations.Load() != beforeReservations {
		t.Fatalf("recovery changed global backing: %+v", stats)
	}
}
