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

func ownerTestPair(t *testing.T) (*yamux.Session, *yamux.Session, *yamux.Stream, *yamux.Stream) {
	t.Helper()
	c, err := NewAggregateYamuxConfig(nil, 16<<20, 32)
	if err != nil {
		t.Fatal(err)
	}
	c.EnableKeepAlive = false
	c.ConnectionWriteTimeout = time.Second
	left, right := net.Pipe()
	a, err := yamux.Client(left, c.Clone())
	if err != nil {
		t.Fatal(err)
	}
	b, err := yamux.Server(right, c.Clone())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { a.Close(); b.Close() })
	x, err := a.OpenStream()
	if err != nil {
		t.Fatal(err)
	}
	y, err := b.AcceptStream()
	if err != nil {
		t.Fatal(err)
	}
	return a, b, x, y
}
func TestLaneOwnersGrowthCapsOverlapAndPhysicalCleanup(t *testing.T) {
	for _, reverse := range []bool{false, true} {
		t.Run(fmt.Sprint(reverse), func(t *testing.T) {
			var group LaneOwners
			defer group.Close()
			var charged atomic.Int64
			reserve := func(n int64) (func(), bool) {
				charged.Add(n)
				var once sync.Once
				return func() { once.Do(func() { charged.Add(-n) }) }, true
			}
			base, _ := AggregateSessionResourceBytes(AggregateInitialSessionWindowBytes, 0)
			acquire := func(lane int, id, previous string) *LaneOwner {
				o, err := group.Acquire(lane, id, previous, 2*base, func() (func(), bool) { return reserve(2 * base) })
				if err != nil {
					t.Fatal(err)
				}
				return o
			}
			a, b, x, y := ownerTestPair(t)
			if reverse {
				a, b, x, y = b, a, y, x
			}
			owner := acquire(1, "first", "")
			if !owner.Attach(a, reserve) {
				t.Fatal("attach")
			}
			owner.Publish()
			if ok, err := owner.Growth().Grow(x, 4<<20); !ok || err != nil {
				t.Fatalf("grow=%v %v", ok, err)
			}
			if a.AggregateReceiveWindowStats().ReceiveLimit != AggregateLanePoolLimit {
				t.Fatal("pool did not actually grow")
			}
			before := charged.Load()
			if ok, err := owner.Growth().Grow(x, 8<<20); ok || err != nil || charged.Load() != before {
				t.Fatal("per-lane cap changed credit/charge")
			}
			nextSession, _, nextStream, _ := ownerTestPair(t)
			next := acquire(1, "second", "first")
			if !next.Attach(nextSession, reserve) {
				t.Fatal("attach replacement")
			}
			if _, err := group.Acquire(1, "third", "first", 2*base, func() (func(), bool) { t.Fatal("spent prepaid replacement headroom twice"); return nil, false }); err == nil {
				t.Fatal("unbounded overlap")
			}
			old, ok := next.Publish()
			if !ok || old != owner || owner.State() != LaneDraining {
				t.Fatal("publication")
			}
			if ok, _ := next.Growth().Grow(nextStream, 1<<20); ok {
				t.Fatal("draining growth escaped shared lane cap")
			}
			if ok, _ := owner.Growth().Grow(x, 2<<20); ok {
				t.Fatal("retiring owner grew")
			}
			payload := bytes.Repeat([]byte("exact"), 20000)
			done := make(chan error, 1)
			go func() { _, err := y.Write(payload); done <- err }()
			got := make([]byte, len(payload))
			if _, err := io.ReadFull(x, got); err != nil || !bytes.Equal(got, payload) {
				t.Fatalf("old active bytes: %v", err)
			}
			if err := <-done; err != nil {
				t.Fatal(err)
			}
			owner.Close()
			if stats := a.AggregateReceiveWindowStats(); !stats.Closed || stats.TotalUnits != 0 || stats.BufferedUnits != 0 {
				t.Fatalf("fake pool refund: %+v", stats)
			}
			if charged.Load() != 2*base || group.ReservedBytes() != 2*base {
				t.Fatalf("grown reservation retained: %d", charged.Load())
			}
			if ok, err := next.Growth().Grow(nextStream, 1<<20); !ok || err != nil {
				t.Fatalf("released cap not reusable: %v %v", ok, err)
			}
			group.Close()
			if charged.Load() != 0 || group.ReservedBytes() != 0 || len(group.Snapshot()) != 0 {
				t.Fatal("generation leases retained")
			}
			if _, err := group.Acquire(1, "late", "", 2*base, func() (func(), bool) { t.Fatal("closed group reserved"); return nil, false }); err == nil {
				t.Fatal("closed group admitted")
			}
		})
	}
}
func TestLaneOwnersGenerationCapAndDeniedHandshake(t *testing.T) {
	var g LaneOwners
	defer g.Close()
	var charged atomic.Int64
	reserve := func(n int64) (func(), bool) { charged.Add(n); return func() { charged.Add(-n) }, true }
	base, _ := AggregateSessionResourceBytes(AggregateInitialSessionWindowBytes, 0)
	if _, err := g.Acquire(1, "denied", "", 2*base, func() (func(), bool) { return nil, false }); err == nil || len(g.Snapshot()) != 0 {
		t.Fatal("denied reservation admitted")
	}
	for lane := 1; lane <= 4; lane++ {
		o, err := g.Acquire(lane, fmt.Sprint(lane), "", 2*base, func() (func(), bool) { return reserve(2 * base) })
		if err != nil {
			t.Fatal(err)
		}
		a, _, x, _ := ownerTestPair(t)
		if !o.Attach(a, reserve) {
			t.Fatal("attach")
		}
		o.Publish()
		ok, err := o.Growth().Grow(x, 4<<20)
		if err != nil || ok != (lane < 4) {
			t.Fatalf("lane%d granted=%v err=%v", lane, ok, err)
		}
	}
	// A handshake owner that never obtains a session owns one bounded baseline
	// slot; failure returns the complete promise without affecting serving lanes.
	before := charged.Load()
	failed, err := g.Acquire(5, "failed", "", 2*base, func() (func(), bool) { return reserve(2 * base) })
	if err != nil {
		t.Fatal(err)
	}
	failed.Close()
	failed.Close()
	if charged.Load() != before {
		t.Fatal("failed handshake lease leaked")
	}
	g.Close()
	if charged.Load() != 0 {
		t.Fatal("generation cap charges leaked")
	}
}
func TestLaneOwnersGrowRetireShutdownRace(t *testing.T) {
	for range 12 {
		var g LaneOwners
		var charged atomic.Int64
		reserve := func(n int64) (func(), bool) { charged.Add(n); return func() { charged.Add(-n) }, true }
		o, err := g.Acquire(1, "owner", "", 100, func() (func(), bool) { return reserve(100) })
		if err != nil {
			t.Fatal(err)
		}
		a, _, x, _ := ownerTestPair(t)
		o.Attach(a, reserve)
		o.Publish()
		var wg sync.WaitGroup
		wg.Add(3)
		go func() { defer wg.Done(); o.Growth().Grow(x, 4<<20) }()
		go func() { defer wg.Done(); o.Close() }()
		go func() { defer wg.Done(); g.Close() }()
		wg.Wait()
		if charged.Load() != 0 || g.ReservedBytes() != 0 {
			t.Fatal("racing cleanup leaked")
		}
	}
}
func TestRetirementWaitsForFINBufferAndExplicitDisposal(t *testing.T) {
	for _, dispose := range []bool{false, true} {
		t.Run(fmt.Sprint(dispose), func(t *testing.T) {
			a, b, x, y := ownerTestPair(t)
			payload := bytes.Repeat([]byte("fin"), 1024)
			if _, err := y.Write(payload); err != nil {
				t.Fatal(err)
			}
			x.Close()
			y.Close()
			a.Ping()
			b.Ping()
			a.StopOpening()
			if a.NumStreams() != 0 {
				t.Fatal("FIN fixture still mapped")
			}
			if a.ReceiveOwnersDrained() {
				t.Fatal("unread FIN owner lost")
			}
			if _, err := a.OpenStream(); !errors.Is(err, yamux.ErrRemoteGoAway) {
				t.Fatal("outbound retirement fence missing")
			}
			conn := NewAggregateGrowingConn(x, 4<<20, nil)
			if dispose {
				conn.Dispose()
				if _, err := conn.Read(make([]byte, 8)); !errors.Is(err, yamux.ErrConnectionReset) {
					t.Fatalf("disposed buffer became EOF: %v", err)
				}
			} else {
				got, err := io.ReadAll(conn)
				if err != nil || !bytes.Equal(got, payload) {
					t.Fatalf("FIN drain lost bytes: %v", err)
				}
			}
			if !a.ReceiveOwnersDrained() {
				t.Fatal("owner not released after consume/dispose")
			}
		})
	}
}

func TestLaneOwnerShutdownJoinsOpeningAndDisposesDetachedControlBuffer(t *testing.T) {
	for _, finishBeforeClose := range []bool{false, true} {
		t.Run(fmt.Sprint(finishBeforeClose), func(t *testing.T) {
			config, err := NewNegotiatedControlYamuxConfig(nil, DefaultAdaptiveReceiveWindowBytes, 4)
			if err != nil {
				t.Fatal(err)
			}
			left, right := net.Pipe()
			a, err := yamux.Client(left, config.Clone())
			if err != nil {
				t.Fatal(err)
			}
			defer a.Close()
			b, err := yamux.Server(right, config.Clone())
			if err != nil {
				t.Fatal(err)
			}
			defer b.Close()
			var group LaneOwners
			defer group.Close()
			var charged atomic.Int64
			owner, err := group.Acquire(0, "control", "", 100, func() (func(), bool) { charged.Add(100); return func() { charged.Add(-100) }, true })
			if err != nil {
				t.Fatal(err)
			}
			if !owner.Attach(a, nil) {
				t.Fatal("attach")
			}
			owner.Publish()
			if !owner.BeginOpen() {
				t.Fatal("open pin")
			}
			openingFinished := false
			defer func() {
				if !openingFinished {
					owner.FinishOpen(nil)
				}
			}()
			x, err := a.OpenStream()
			if err != nil {
				t.Fatal(err)
			}
			y, err := b.AcceptStream()
			if err != nil {
				t.Fatal(err)
			}
			payload := bytes.Repeat([]byte("detached control"), 1024)
			if _, err := y.Write(payload); err != nil {
				t.Fatal(err)
			}
			x.Close()
			y.Close()
			a.Ping()
			b.Ping()
			if a.NumStreams() != 0 {
				t.Fatal("control FIN fixture still mapped")
			}
			if finishBeforeClose {
				release, ok := owner.FinishOpen(x)
				openingFinished = true
				if !ok {
					t.Fatal("register")
				}
				defer release()
			}
			done := make(chan struct{})
			go func() { owner.Close(); close(done) }()
			<-a.CloseChan()
			if !finishBeforeClose {
				select {
				case <-done:
					t.Fatal("close refunded unfinished opener")
				default:
				}
				if charged.Load() != 100 {
					t.Fatal("opening reservation refunded")
				}
				release, ok := owner.FinishOpen(x)
				openingFinished = true
				release()
				if ok {
					t.Fatal("closing owner handed buffer to app")
				}
			}
			select {
			case <-done:
			case <-time.After(time.Second):
				t.Fatal("owner did not join opening")
			}
			if charged.Load() != 0 {
				t.Fatal("reservation retained")
			}
			if _, err := x.Read(make([]byte, 1)); !errors.Is(err, yamux.ErrConnectionReset) {
				t.Fatalf("abandoned detached buffer still readable or EOF: %v", err)
			}
		})
	}
}

func TestLaneOwnersRetryLostCommittedReplacementWithinSameBudget(t *testing.T) {
	var g LaneOwners
	defer g.Close()
	var reservations int
	reserve := func() (func(), bool) { reservations++; return func() { reservations-- }, true }
	old, err := g.Acquire(1, "old", "", 100, reserve)
	if err != nil {
		t.Fatal(err)
	}
	a, _, _, _ := ownerTestPair(t)
	old.Attach(a, func(int64) (func(), bool) { return nil, false })
	old.Publish()
	lost, err := g.Acquire(1, "lost", "old", 100, reserve)
	if err != nil {
		t.Fatal(err)
	}
	b, _, _, _ := ownerTestPair(t)
	lost.Attach(b, func(int64) (func(), bool) { return nil, false })
	lost.Publish()
	lost.Close()
	if g.Predecessor(1, "old") != old || old.Session().IsClosed() {
		t.Fatal("failed successor destroyed predecessor")
	}
	recovered, err := g.Acquire(1, "recovered", "old", 100, reserve)
	if err != nil {
		t.Fatal(err)
	}
	if reservations != 1 || g.ReservedBytes() != 100 {
		t.Fatal("retry allocated another headroom budget")
	}
	c, _, _, _ := ownerTestPair(t)
	recovered.Attach(c, func(int64) (func(), bool) { return nil, false })
	recovered.Publish()
	if g.Predecessor(1, "old") != nil {
		t.Fatal("stale predecessor could displace serving successor")
	}
	g.Close()
	if reservations != 0 {
		t.Fatal("retry leaked budget")
	}
}
