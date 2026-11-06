package tunnel

import (
	"context"
	"io"
	"net"
	"testing"
	"time"

	"github.com/hashicorp/yamux"
)

func idleOwner(t *testing.T, g *LaneOwners, s *yamux.Session, id, replaces string) *LaneOwner {
	t.Helper()
	o, err := g.Acquire(1, id, replaces, 0, func() (func(), bool) { return func() {}, true })
	if err != nil || !o.Attach(s, func(int64) (func(), bool) { return func() {}, true }) {
		t.Fatalf("owner: %v", err)
	}
	return o
}

func TestLaneIdlePublicationRevalidatesRenewedOwnership(t *testing.T) {
	var g LaneOwners
	defer g.Close()
	a, _, x, y := ownerTestPair(t)
	old := idleOwner(t, &g, a, "old", "")
	old.Publish()
	x.Reset()
	y.Reset()
	deadline := time.Now().Add(time.Second)
	for !old.IdleFor(0) {
		if time.Now().After(deadline) {
			t.Fatal("idle not reached")
		}
		time.Sleep(time.Millisecond)
	}
	b, _, bx, by := ownerTestPair(t)
	bx.Reset()
	by.Reset()
	next := idleOwner(t, &g, b, "next", "old")
	// Eligibility was observed before replacement readiness. Work resumes now.
	if !old.BeginOpen() {
		t.Fatal("begin")
	}
	if _, ok := next.PublishWhenIdle(0); ok {
		t.Fatal("published across concurrent opening")
	}
	old.FinishOpen(nil)
	if _, ok := next.PublishWhenIdle(time.Hour); ok {
		t.Fatal("quiet period reused after new activity")
	}
	if _, ok := next.PublishWhenIdle(0); !ok {
		t.Fatal("idle replacement denied")
	}
	if old.BeginOpen() {
		t.Fatal("publication did not fence openings")
	}
}

func TestLaneIdlePublicationRetainsFINApplicationOwner(t *testing.T) {
	var g LaneOwners
	defer g.Close()
	a, _, x, y := ownerTestPair(t)
	old := idleOwner(t, &g, a, "old", "")
	old.Publish()
	old.BeginOpen()
	release, ok := old.FinishOpen(x)
	if !ok {
		t.Fatal("registration")
	}
	if _, err := y.Write([]byte("unread")); err != nil {
		t.Fatal(err)
	}
	x.Close()
	y.Close()
	a.Ping()
	if old.IdleFor(0) {
		t.Fatal("unread FIN ownership considered idle")
	}
	got, err := io.ReadAll(x)
	if err != nil || string(got) != "unread" {
		t.Fatalf("FIN bytes %q %v", got, err)
	}
	if old.IdleFor(0) {
		t.Fatal("live application owner considered idle")
	}
	release()
	if !old.IdleFor(0) {
		t.Fatal("disposed owner not idle")
	}
}

// Use an actual TCP wire and both physical Yamux endpoints. Hold the final
// opening registration after SYN publication so a Ping alone could complete:
// the retirement barrier must also join FinishOpen, without holding group.mu.
func TestLaneOpeningFenceJoinsBeforeAcknowledgment(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	accepted := make(chan net.Conn, 1)
	go func() { c, _ := listener.Accept(); accepted <- c }()
	client, err := net.Dial("tcp", listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	server := <-accepted
	cfg, _ := NewAggregateYamuxConfig(nil, 2<<20, 32)
	cfg.EnableKeepAlive = false
	a, err := yamux.Server(server, cfg.Clone())
	if err != nil {
		t.Fatal(err)
	}
	b, err := yamux.Client(client, cfg.Clone())
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	defer b.Close()
	var g LaneOwners
	defer g.Close()
	old := idleOwner(t, &g, a, "old", "")
	old.Publish()
	if !old.BeginOpen() {
		t.Fatal("begin")
	}
	x, err := a.OpenStream()
	if err != nil {
		t.Fatal(err)
	}
	y, err := b.AcceptStream()
	if err != nil {
		t.Fatal(err)
	}
	n, _, nx, ny := ownerTestPair(t)
	nx.Reset()
	ny.Reset()
	next := idleOwner(t, &g, n, "next", "old")
	next.Publish() // forced protocol-level handoff, independent of automatic idle policy
	a.StopOpening()
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- old.AcknowledgeOpeningFence(ctx) }()
	select {
	case err := <-done:
		old.FinishOpen(x)
		t.Fatalf("fence passed unfinished opening: %v", err)
	case <-time.After(30 * time.Millisecond):
	}
	release, ok := old.FinishOpen(x)
	if !ok {
		t.Fatal("finish")
	}
	defer release()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if err := b.GoAway(); err != nil {
		t.Fatal(err)
	}
	if _, err := x.Write([]byte("exact")); err != nil {
		t.Fatal(err)
	}
	got := make([]byte, 5)
	y.SetReadDeadline(time.Now().Add(time.Second))
	if _, err := io.ReadFull(y, got); err != nil || string(got) != "exact" {
		t.Fatalf("handoff bytes %q %v", got, err)
	}
	x.Reset()
	y.Reset()
}

func TestLaneOpeningFenceCancellationAndFailedSession(t *testing.T) {
	for _, failed := range []bool{false, true} {
		t.Run(map[bool]string{false: "cancel", true: "failed-session"}[failed], func(t *testing.T) {
			var g LaneOwners
			defer g.Close()
			a, _, x, y := ownerTestPair(t)
			x.Reset()
			y.Reset()
			old := idleOwner(t, &g, a, "old", "")
			old.Publish()
			old.BeginOpen()
			n, _, nx, ny := ownerTestPair(t)
			nx.Reset()
			ny.Reset()
			next := idleOwner(t, &g, n, "next", "old")
			next.Publish()
			a.StopOpening()
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			if failed {
				a.Close()
			} else {
				cancel()
			}
			err := old.AcknowledgeOpeningFence(ctx)
			old.FinishOpen(nil)
			if err == nil {
				t.Fatal("barrier certified incomplete opening")
			}
			if next.State() != LaneServing {
				t.Fatal("failed predecessor displaced recovery route")
			}
		})
	}
}

func TestLaneIdlePublicationRejectsStaleReceiveObservation(t *testing.T) {
	var g LaneOwners
	defer g.Close()
	a, _, x, y := ownerTestPair(t)
	x.Reset()
	y.Reset()
	old := idleOwner(t, &g, a, "old", "")
	old.Publish()
	deadline := time.Now().Add(time.Second)
	var epoch uint64
	for {
		e, ok := old.idleObservation(0)
		if ok {
			epoch = e
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("idle")
		}
		time.Sleep(time.Millisecond)
	}
	n, _, nx, ny := ownerTestPair(t)
	nx.Reset()
	ny.Reset()
	next := idleOwner(t, &g, n, "next", "old")
	// Even a failed opening whose lifetime fits entirely between the unlocked
	// receive-map observation and the final group lock invalidates that snapshot.
	old.BeginOpen()
	old.FinishOpen(nil)
	if _, ok := next.publish(old, epoch, true); ok {
		t.Fatal("stale idle snapshot published")
	}
}
