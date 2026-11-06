package tcpsocket

import (
	"errors"
	"net"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestAccountedConcurrentCloseRetainsCreditThroughFDReference(t *testing.T) {
	conn, _ := tcpPair(t)
	var releases atomic.Int64
	var referenceHeld, early atomic.Bool
	tracked := &AccountedConn{TCPConn: conn, Release: func() {
		if referenceHeld.Load() {
			early.Store(true)
		}
		releases.Add(1)
	}}
	if err := tracked.CloseWrite(); err != nil {
		t.Fatal(err)
	}
	if releases.Load() != 0 {
		t.Fatal("half-close released live socket credit")
	}
	raw, err := tracked.SyscallConn()
	if err != nil {
		t.Fatal(err)
	}
	entered, finish, controlDone := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var open sync.Once
	var workers []<-chan struct{}
	t.Cleanup(func() {
		open.Do(func() { close(finish) })
		joined := true
		for _, done := range workers {
			select {
			case <-done:
			case <-time.After(5 * time.Second):
				t.Error("accounted Close/control worker did not join during cleanup")
				joined = false
			}
		}
		if joined {
			_ = tracked.Close()
		}
	})
	var controlErr error
	workers = append(workers, controlDone)
	go func() {
		defer close(controlDone)
		controlErr = raw.Control(func(_ uintptr) {
			referenceHeld.Store(true)
			close(entered)
			<-finish
			referenceHeld.Store(false)
		})
	}()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("RawConn.Control did not retain its FD reference")
	}
	var results [2]error
	start := func(index int) (<-chan struct{}, <-chan struct{}) {
		started, done := make(chan struct{}), make(chan struct{})
		workers = append(workers, done)
		go func() {
			defer close(done)
			close(started)
			results[index] = tracked.Close()
		}()
		return started, done
	}
	_, first := start(0)
	// The pinned poll.FD.Close sets its closing bit before waiting for held
	// Control references. Observe that actual onset, not a scheduling delay.
	deadline := time.Now().Add(5 * time.Second)
	closing := false
	for time.Now().Before(deadline) {
		err := conn.SetDeadline(time.Time{})
		if errors.Is(err, net.ErrClosed) {
			closing = true
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		runtime.Gosched()
	}
	if !closing || !referenceHeld.Load() {
		t.Fatal("first Close did not reach reference-held physical destruction")
	}
	started, second := start(1)
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("second accounted Close worker did not start")
	}
	// This explicit window is a bounded opportunity for the contender, not
	// an all-scheduler proof. Root must verify failure on the original wrapper.
	timer := time.NewTimer(100 * time.Millisecond)
	select {
	case <-second:
		t.Error("second accounted Close returned before FD reference release")
	case <-timer.C:
	}
	timer.Stop()
	if early.Load() || releases.Load() != 0 {
		t.Error("concurrent accounted Close refunded before FD reference release")
		t.Log("accounted_concurrent_close_held early_refund=true")
	}
	open.Do(func() { close(finish) })
	for _, done := range []<-chan struct{}{controlDone, first, second} {
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatal("accounted Close/control worker did not join")
		}
	}
	if controlErr != nil || results[0] != nil || results[1] != nil || tracked.Close() != nil || releases.Load() != 1 || early.Load() || referenceHeld.Load() {
		t.Errorf("accounted Close result/refund differs: control=%v results=%v releases=%d early=%t", controlErr, results, releases.Load(), early.Load())
	}
	if t.Failed() {
		return
	}
	t.Log("accounted_concurrent_close_terminal callers=2 releases=1 early_refund=false joined=3 half_close_retained=true")
}
