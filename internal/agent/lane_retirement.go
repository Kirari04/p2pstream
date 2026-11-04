package agent

import (
	"context"
	"time"

	"github.com/hashicorp/yamux"
	"p2pstream/internal/tunnel"
)

type laneReady struct {
	session  *yamux.Session
	id       string
	interval time.Duration
}
type laneAttempt struct {
	ready  chan laneReady
	done   chan error
	cancel context.CancelFunc
}

// One supervisor per bulk lane, at most serving+replacement/draining attempts.
// connect joins its session, handlers and leases before reporting completion.
func serveRetiringTunnelLane(ctx context.Context, registration tunnelLaneRegistration, connect tunnelLaneConnector) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var attempts []*laneAttempt
	defer func() {
		for _, a := range attempts {
			a.cancel()
			<-a.done
		}
	}()
	start := func(replaces string, grown bool) *laneAttempt {
		child, stop := context.WithCancel(ctx)
		a := &laneAttempt{ready: make(chan laneReady, 1), done: make(chan error, 1), cancel: stop}
		reg := registration
		reg.replaces = replaces
		reg.retireGrown = grown
		reg.ready = func(r laneReady) { a.ready <- r }
		attempts = append(attempts, a)
		go func() { err := connect(child, reg); a.done <- err; close(a.done) }()
		return a
	}
	forget := func(a *laneAttempt) {
		for i, v := range attempts {
			if v == a {
				a.cancel()
				copy(attempts[i:], attempts[i+1:])
				attempts[len(attempts)-1] = nil
				attempts = attempts[:len(attempts)-1]
				return
			}
		}
	}
	current := start("", false)
	var ready laneReady
	select {
	case ready = <-current.ready:
	case err := <-current.done:
		forget(current)
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
	if ready.interval == 0 {
		select {
		case err := <-current.done:
			forget(current)
			return err
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	timer := time.NewTimer(ready.interval)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case err := <-current.done:
			forget(current)
			return err
		case <-timer.C:
		}
		next := start(ready.id, ready.session.AggregateReceiveWindowStats().ReceiveLimit > tunnel.AggregateInitialSessionWindowBytes)
		var nextReady laneReady
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-next.done:
			forget(next)
			timer.Reset(ready.interval)
			continue // preserve serving lane on denial/failure
		case nextReady = <-next.ready:
		}
		old, oldReady := current, ready
		current, ready = next, nextReady
		// The corrected server's idle publication excludes outstanding ownership.
		// Its defensive opening barrier either acknowledges the old wire or observes
		// that the predecessor closed. This GoAway fences further server Opens.
		// The server's setup GoAway has the opposite direction and is NOT reused.
		_ = oldReady.session.GoAway()
		tick := time.NewTicker(100 * time.Millisecond)
		var servingErr error
		servingEnded := false
		servingDone := current.done
	drain:
		for {
			select {
			case <-ctx.Done():
				tick.Stop()
				return ctx.Err()
			case <-old.done:
				forget(old)
				break drain
			case err := <-servingDone:
				if !servingEnded {
					servingErr = err
					servingEnded = true
					forget(current)
				}
				// Avoid a closed-channel spin; old work still drains without truncation.
				servingDone = nil
			case <-tick.C:
				if oldReady.session.ReceiveOwnersDrained() {
					old.cancel()
				}
			}
		}
		tick.Stop()
		if servingEnded {
			return servingErr
		}
		timer.Reset(ready.interval)
	}
}
