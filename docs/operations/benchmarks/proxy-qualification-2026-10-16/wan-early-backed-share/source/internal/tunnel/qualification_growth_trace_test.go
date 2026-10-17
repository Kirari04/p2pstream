//go:build p2pstream_qualification

package tunnel

import (
	"encoding/binary"
	"encoding/json"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hashicorp/yamux"
)

func newQualificationGrowthTestConn(
	t *testing.T,
	reserve func(int64) (func(), bool),
) (*GrowingConn, *yamux.Stream, *yamux.Session) {
	t.Helper()
	return newQualificationGrowthTestConnWithTransport(t, reserve, nil)
}

func newQualificationGrowthTestConnWithTransport(
	t *testing.T,
	reserve func(int64) (func(), bool),
	wrapServer func(net.Conn) net.Conn,
) (*GrowingConn, *yamux.Stream, *yamux.Session) {
	t.Helper()
	left, right := net.Pipe()
	if wrapServer != nil {
		right = wrapServer(right)
	}
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
	writer, err := client.OpenStream()
	if err != nil {
		t.Fatal(err)
	}
	reader, err := server.AcceptStream()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = writer.Close()
		_ = reader.Close()
	})
	return NewGrowingConn(reader, 2<<20, reserve), writer, server
}

func TestQualificationGrowthTraceRecordsControllerOutcomes(t *testing.T) {
	t.Run("grown", func(t *testing.T) {
		DrainQualificationReceiveWindowGrowthEvents()
		conn, _, _ := newQualificationGrowthTestConn(t, func(int64) (func(), bool) {
			return func() {}, true
		})
		now := time.Unix(1_800_000_000, 123)
		conn.growth.start = now
		if err := conn.growMeasured(1<<20, now.Add(80*time.Millisecond), 80*time.Millisecond); err != nil {
			t.Fatal(err)
		}
		if err := conn.growMeasured(1<<20, now.Add(160*time.Millisecond), 80*time.Millisecond); err != nil {
			t.Fatal(err)
		}
		drain := DrainQualificationReceiveWindowGrowthEvents()
		if len(drain.Events) != 2 {
			t.Fatalf("events=%d, want 2", len(drain.Events))
		}
		first, second := drain.Events[0], drain.Events[1]
		if first.Outcome != "no_proposal" || second.Outcome != "grown" {
			t.Fatalf("outcomes=%q,%q", first.Outcome, second.Outcome)
		}
		if first.DeniedUntilUnixNano != 0 || first.DenialActiveAfterOutcome {
			t.Fatalf("ordinary sample reported denial: %+v", first)
		}
		if first.SampleUnixNano != now.Add(80*time.Millisecond).UnixNano() {
			t.Fatalf("sample timestamp=%d, want controller sample-start timestamp", first.SampleUnixNano)
		}
		if first.RTTNanoseconds != (80*time.Millisecond).Nanoseconds() || first.RateBytesPerSecond <= 0 || first.Previous <= first.Current {
			t.Fatalf("first controller scalars=%+v", first)
		}
		if second.Proposed <= second.Current || second.Session == nil || second.StreamID == 0 || second.SessionIdentity == "" {
			t.Fatalf("grown event=%+v", second)
		}
	})

	t.Run("reservation_denied", func(t *testing.T) {
		DrainQualificationReceiveWindowGrowthEvents()
		conn, _, _ := newQualificationGrowthTestConn(t, func(int64) (func(), bool) {
			return nil, false
		})
		now := time.Unix(1_800_000_100, 0)
		conn.growth.start = now
		if err := conn.growMeasured(1<<20, now.Add(80*time.Millisecond), 80*time.Millisecond); err != nil {
			t.Fatal(err)
		}
		if err := conn.growMeasured(1<<20, now.Add(160*time.Millisecond), 80*time.Millisecond); err != nil {
			t.Fatal(err)
		}
		events := DrainQualificationReceiveWindowGrowthEvents().Events
		denied := events[len(events)-1]
		if denied.Outcome != "reservation_denied" || !denied.DenialActiveAfterOutcome || denied.DeniedUntilUnixNano != now.Add(1160*time.Millisecond).UnixNano() {
			t.Fatalf("denial event=%+v", denied)
		}
	})

	t.Run("publication_error", func(t *testing.T) {
		DrainQualificationReceiveWindowGrowthEvents()
		var failing *gatedWriteFailureConn
		var reserved atomic.Int64
		conn, _, server := newQualificationGrowthTestConnWithTransport(t, func(bytes int64) (func(), bool) {
			reserved.Add(bytes)
			return func() { reserved.Add(-bytes) }, true
		}, func(raw net.Conn) net.Conn {
			failing = &gatedWriteFailureConn{Conn: raw, entered: make(chan struct{}), release: make(chan struct{})}
			return failing
		})
		var unblockOnce sync.Once
		unblockWriter := func() { unblockOnce.Do(func() { close(failing.release) }) }
		defer unblockWriter()
		t.Cleanup(func() { _ = server.Close(); conn.Release() })
		failing.failWrite = func(header []byte) bool {
			return len(header) == 12 && header[1] == 1 && // positive WINDOW_UPDATE on this live stream
				binary.BigEndian.Uint32(header[4:8]) == conn.stream.StreamID() &&
				binary.BigEndian.Uint32(header[8:12]) > 0
		}
		failing.fail.Store(true)
		now := time.Unix(1_800_000_200, 0)
		conn.growth.start = now
		if err := conn.growMeasured(1<<20, now.Add(80*time.Millisecond), 80*time.Millisecond); err != nil {
			t.Fatal(err)
		}
		growDone := make(chan error, 1)
		go func() { growDone <- conn.growMeasured(1<<20, now.Add(160*time.Millisecond), 80*time.Millisecond) }()
		joined := false
		defer func() {
			unblockWriter()
			if !joined {
				_ = server.Close()
				select {
				case <-growDone:
				case <-time.After(time.Second):
					t.Error("growth producer did not join after failure cleanup")
				}
			}
		}()
		select {
		case <-failing.entered:
		case <-time.After(time.Second):
			t.Fatal("live window publication did not reach transport failure gate")
		}
		if server.IsClosed() || reserved.Load() == 0 {
			t.Fatal("fixture did not reach live, already-reserved publication")
		}
		unblockWriter()
		select {
		case err := <-growDone:
			joined = true
			if err == nil {
				t.Fatal("live transport publication unexpectedly succeeded")
			}
		case <-time.After(time.Second):
			t.Fatal("growth producer did not join after transport failure")
		}
		if reserved.Load() == 0 {
			t.Fatal("ambiguous publication released its reservation before cleanup")
		}
		events := DrainQualificationReceiveWindowGrowthEvents().Events
		if len(events) != 2 || events[0].Outcome != "no_proposal" || events[1].Outcome != "publication_error" {
			t.Fatalf("live failure events=%+v, want no_proposal then publication_error", events)
		}
		if err := server.Close(); err != nil {
			t.Fatal(err)
		}
		conn.Release()
		if reserved.Load() != 0 {
			t.Fatal("publication reservation retained after session close and Release")
		}
	})
}

func TestQualificationGrowthTraceRingIsBoundedOrderedAndDestructive(t *testing.T) {
	DrainQualificationReceiveWindowGrowthEvents()
	conn, _, server := newQualificationGrowthTestConn(t, func(int64) (func(), bool) {
		return func() {}, true
	})
	base := time.Unix(1_900_000_000, 0)
	for i := 0; i < qualificationGrowthTraceCapacity+3; i++ {
		conn.growth.rate = float64(i)
		conn.growth.previous = uint32(i)
		conn.recordQualificationGrowthSample(
			base.Add(time.Duration(i)*time.Nanosecond),
			50*time.Millisecond,
			uint32(i),
			uint32(i+1),
			qualificationGrowthOutcomeNoProposal,
		)
	}
	drain := DrainQualificationReceiveWindowGrowthEvents()
	if drain.Capacity != qualificationGrowthTraceCapacity || drain.DroppedEvents != 3 || len(drain.Events) != qualificationGrowthTraceCapacity {
		t.Fatalf("drain capacity=%d dropped=%d events=%d", drain.Capacity, drain.DroppedEvents, len(drain.Events))
	}
	if drain.Events[0].Current != 3 || drain.Events[len(drain.Events)-1].Current != qualificationGrowthTraceCapacity+2 {
		t.Fatalf("retained range=%d..%d", drain.Events[0].Current, drain.Events[len(drain.Events)-1].Current)
	}
	wantIdentity := drain.Events[0].SessionIdentity
	if wantIdentity == "" || !strings.HasPrefix(wantIdentity, "0x") || drain.Events[0].Session != server {
		t.Fatalf("session identity=%q pointer=%p, want server %p", wantIdentity, drain.Events[0].Session, server)
	}
	encoded, err := json.Marshal(drain.Events[0])
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), `"Session":`) {
		t.Fatalf("diagnostic JSON exposed raw session pointer: %s", encoded)
	}
	for i, event := range drain.Events {
		if event.SampleUnixNano != base.Add(time.Duration(i+3)*time.Nanosecond).UnixNano() {
			t.Fatalf("event %d timestamp=%d", i, event.SampleUnixNano)
		}
		if event.SessionIdentity != wantIdentity {
			t.Fatalf("event %d identity=%q, want %q", i, event.SessionIdentity, wantIdentity)
		}
	}
	empty := DrainQualificationReceiveWindowGrowthEvents()
	if empty.DroppedEvents != 0 || len(empty.Events) != 0 {
		t.Fatalf("second drain=%+v, want empty/reset", empty)
	}
	qualificationReceiveWindowGrowthRing.mu.Lock()
	defer qualificationReceiveWindowGrowthRing.mu.Unlock()
	for i, event := range qualificationReceiveWindowGrowthRing.events {
		if event.session != nil {
			t.Fatalf("ring slot %d retained session after drain", i)
		}
	}
}

func TestQualificationGrowthTraceSampleHasNoAllocation(t *testing.T) {
	DrainQualificationReceiveWindowGrowthEvents()
	conn, _, _ := newQualificationGrowthTestConn(t, func(int64) (func(), bool) {
		return func() {}, true
	})
	now := time.Unix(2_000_000_000, 0)
	allocations := testing.AllocsPerRun(1000, func() {
		conn.recordQualificationGrowthSample(now, 50*time.Millisecond, 1, 2, qualificationGrowthOutcomeNoProposal)
	})
	if allocations != 0 {
		t.Fatalf("sample allocations=%f, want zero", allocations)
	}
	DrainQualificationReceiveWindowGrowthEvents()
}
