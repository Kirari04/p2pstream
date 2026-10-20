package server

import (
	"errors"
	"net"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// This gate models a pollable connection whose first Close waits for physical
// destruction, while another underlying Close can immediately report closed.
type gatedPublicPhysicalCloseConn struct {
	net.Conn
	entered  chan struct{}
	finish   chan struct{}
	calls    atomic.Int64
	physical atomic.Bool
	closeErr error
}

func (c *gatedPublicPhysicalCloseConn) Close() error {
	if c.calls.Add(1) != 1 {
		return net.ErrClosed
	}
	close(c.entered)
	<-c.finish
	err := c.Conn.Close()
	c.physical.Store(true)
	if c.closeErr != nil {
		return c.closeErr
	}
	return err
}

func TestPublicConcurrentCloseRetainsCreditUntilPhysicalCompletion(t *testing.T) {
	for _, mode := range []string{"success", "returned-error"} {
		t.Run(mode, func(t *testing.T) {
			app := newPublicOwnedResourceTestApp(t)
			local, peer := net.Pipe()
			t.Cleanup(func() { _ = local.Close(); _ = peer.Close() })
			underlying := &gatedPublicPhysicalCloseConn{Conn: local, entered: make(chan struct{}), finish: make(chan struct{})}
			if mode == "returned-error" {
				underlying.closeErr = errors.New("physical Close returned an error")
			}
			release, active, ok := app.tryReservePublicConnectionPhased(underlying)
			if !ok {
				t.Fatal("initial connection reservation rejected")
			}
			var releases atomic.Int64
			var early atomic.Bool
			tracked := &resourceBoundedPublicConn{Conn: underlying, active: active, release: func() {
				if !underlying.physical.Load() {
					early.Store(true)
				}
				releases.Add(1)
				release()
			}}
			var open sync.Once
			var workers []<-chan struct{}
			t.Cleanup(func() {
				open.Do(func() { close(underlying.finish) })
				joined := true
				for _, done := range workers {
					select {
					case <-done:
					case <-time.After(5 * time.Second):
						t.Error("public Close worker did not join during cleanup")
						joined = false
					}
				}
				if joined {
					_ = tracked.Close()
				}
			})
			publicResourceConnState(tracked, http.StateActive)
			before := app.agentStreamCapacity.snapshot()
			if before.AdaptiveExternalBytes <= 0 || before.AdaptiveExternalFDs != 1 || app.publicConnections.inUse() != 1 {
				t.Fatal("active connection did not retain its parser/socket credit")
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
			select {
			case <-underlying.entered:
			case <-time.After(5 * time.Second):
				t.Fatal("first physical Close did not enter its gate")
			}
			started, second := start(1)
			select {
			case <-started:
			case <-time.After(5 * time.Second):
				t.Fatal("second public Close worker did not start")
			}
			// The gate establishes physical-close ordering. This fixed scheduling
			// window permits the contender to run; absence of its return is not a
			// claim about every possible scheduler. Baseline execution is required.
			timer := time.NewTimer(100 * time.Millisecond)
			select {
			case <-second:
				t.Error("second public Close returned before physical completion")
			case <-timer.C:
			}
			timer.Stop()
			after := app.agentStreamCapacity.snapshot()
			if early.Load() || releases.Load() != 0 || after.AdaptiveExternalBytes != before.AdaptiveExternalBytes || after.AdaptiveExternalFDs != before.AdaptiveExternalFDs || app.publicConnections.inUse() != 1 {
				t.Error("concurrent public Close refunded before physical completion")
				t.Log("public_concurrent_close_held early_refund=true")
			}
			open.Do(func() { close(underlying.finish) })
			for _, done := range []<-chan struct{}{first, second} {
				select {
				case <-done:
				case <-time.After(5 * time.Second):
					t.Fatal("public Close worker did not join")
				}
			}
			if results[0] != underlying.closeErr || results[1] != underlying.closeErr || tracked.Close() != underlying.closeErr || underlying.calls.Load() != 1 || releases.Load() != 1 || early.Load() {
				t.Errorf("public Close result/count differs: results=%v calls=%d releases=%d early=%t", results, underlying.calls.Load(), releases.Load(), early.Load())
			}
			if s := app.agentStreamCapacity.snapshot(); s.AdaptiveExternalBytes != 0 || s.AdaptiveExternalFDs != 0 || app.publicConnections.inUse() != 0 {
				t.Fatal("physical public Close retained accounting credit")
			}
			if t.Failed() {
				return
			}
			t.Logf("public_concurrent_close_terminal mode=%s callers=2 physical_closes=1 releases=1 early_refund=false joined=2", mode)
		})
	}
}
