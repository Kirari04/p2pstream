package server

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rs/zerolog"
)

func FuzzAgentStreamCapacityStateMachine(f *testing.F) {
	f.Add([]byte{0, 1, 2, 3, 4, 5, 6, 7, 8, 9})
	f.Add(bytes.Repeat([]byte{0xff, 0x01, 0x80, 0x42}, 32))
	f.Add([]byte("opening-live-closing-release-cancel-register"))

	f.Fuzz(func(t *testing.T, actions []byte) {
		manager, err := newAgentStreamCapacityManager(agentStreamCapacityConfig{
			Total: 8, Public: 6, Pooled: 4, Control: 2,
			MaxWaiters: 8, MaxWaitersPerKey: 4, MaxOpeningPerSession: 2,
			ReservedPublicForOtherSessions: 2,
		})
		if err != nil {
			t.Fatalf("new manager: %v", err)
		}
		sessions := []string{"session-a", "session-b", "session-c"}
		for _, session := range sessions {
			manager.registerSession(session)
		}
		leases := make([]*agentStreamCapacityLease, 0, 8)
		for index, action := range actions {
			session := sessions[int(action>>4)%len(sessions)]
			switch action % 9 {
			case 0, 1, 2:
				class := agentStreamCapacityClass(action % byte(agentStreamCapacityClassCount))
				lease, acquireErr := manager.tryAcquire(class, fmt.Sprintf("route-%d", index%5), session)
				if acquireErr == nil {
					leases = append(leases, lease)
				} else {
					var capacityErr *agentStreamCapacityAcquireError
					if !errors.As(acquireErr, &capacityErr) && acquireErr != errAgentStreamCapacityClassDisabled {
						t.Fatalf("unexpected acquire error at %d: %v", index, acquireErr)
					}
				}
			case 3:
				if len(leases) > 0 {
					leases[int(action>>2)%len(leases)].markLive()
				}
			case 4:
				if len(leases) > 0 {
					leases[int(action>>2)%len(leases)].markClosing()
				}
			case 5:
				if len(leases) > 0 {
					leaseIndex := int(action>>2) % len(leases)
					leases[leaseIndex].release()
					leases = append(leases[:leaseIndex], leases[leaseIndex+1:]...)
				}
			case 6:
				manager.registerSession(session)
			case 7:
				manager.unregisterSession(session)
			case 8:
				ctx, cancel := context.WithCancel(context.Background())
				cancel()
				if _, acquireErr := manager.acquire(ctx, agentStreamCapacityPublicOneShot, "canceled", session); acquireErr == nil {
					t.Fatal("pre-canceled acquisition succeeded")
				}
			}
			if invariantErr := manager.validateInvariants(); invariantErr != nil {
				t.Fatalf("action %d (%d): %v", index, action, invariantErr)
			}
		}
		for _, lease := range leases {
			lease.release()
		}
		assertAgentStreamCapacityClean(t, manager)
	})
}

func TestAgentTransportFourAgentMixedTrafficStress(t *testing.T) {
	const (
		requestCount = 480
		workers      = 48
	)
	suppressInfoLogsForTest(t)
	var deliveries atomic.Int64
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		deliveries.Add(1)
		switch {
		case strings.HasPrefix(r.URL.Path, "/slow"):
			time.Sleep(time.Duration(1+deliveries.Load()%7) * time.Millisecond)
		case strings.HasPrefix(r.URL.Path, "/stream"):
			w.WriteHeader(http.StatusOK)
			for chunk := 0; chunk < 3; chunk++ {
				_, _ = w.Write([]byte("chunk"))
				if flusher, ok := w.(http.Flusher); ok {
					flusher.Flush()
				}
				time.Sleep(time.Millisecond)
			}
			return
		case strings.HasPrefix(r.URL.Path, "/close"):
			w.Header().Set("Connection", "close")
		}
		_, _ = w.Write([]byte("ok"))
	}))
	defer upstream.Close()

	app, baseTarget, firstAgent, _ := newAgentProxyTunnelTestApp(t, 7, upstream.URL, 2*time.Second)
	agents := []*AgentConn{firstAgent}
	for index := 1; index < 4; index++ {
		agent, _ := newFakeYamuxAgent(t, int64(7+index), fmt.Sprintf("mixed-agent-%d", index+1))
		if err := app.AgentHub.connect(agent); err != nil {
			t.Fatalf("connect agent %d: %v", index+1, err)
		}
		t.Cleanup(func() { app.AgentHub.disconnect(agent) })
		agents = append(agents, agent)
	}
	for _, agent := range agents {
		app.agentStreamCapacity.registerSession(agentStreamCapacitySessionKey(agent, agent.Session))
	}

	jobs := make(chan int)
	errorsCh := make(chan error, requestCount)
	var group sync.WaitGroup
	for worker := 0; worker < workers; worker++ {
		group.Add(1)
		go func() {
			defer group.Done()
			for index := range jobs {
				target := baseTarget
				if index%10 >= 7 {
					target.ID = int64(10_000 + index%97)
				}
				path := "/hot"
				switch {
				case index%31 == 0:
					path = "/close"
				case index%17 == 0:
					path = "/stream"
				case index%11 == 0:
					path = "/slow"
				}
				method := http.MethodGet
				var body io.Reader
				if index%5 == 0 {
					method = http.MethodPost
					body = strings.NewReader(fmt.Sprintf("body-%d", index))
				}
				rec := httptest.NewRecorder()
				req := httptest.NewRequest(method, "http://public.test"+path, body)
				proxyAgentTargetForTest(app, rec, req, target, agents[index%len(agents)])
				if rec.Code != http.StatusOK {
					errorsCh <- fmt.Errorf("request %d (%s target=%d) status=%d body=%q", index, path, target.ID, rec.Code, rec.Body.String())
				}
			}
		}()
	}
	go func() {
		for index := 0; index < requestCount; index++ {
			jobs <- index
		}
		close(jobs)
	}()

	stopInvariantChecks := make(chan struct{})
	invariantErr := make(chan error, 1)
	go func() {
		ticker := time.NewTicker(time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				if err := app.agentStreamCapacity.validateInvariants(); err != nil {
					invariantErr <- err
					return
				}
			case <-stopInvariantChecks:
				return
			}
		}
	}()
	group.Wait()
	close(stopInvariantChecks)
	close(errorsCh)
	for err := range errorsCh {
		t.Error(err)
	}
	select {
	case err := <-invariantErr:
		t.Fatalf("capacity invariant during mixed stress: %v", err)
	default:
	}
	if got := deliveries.Load(); got != requestCount {
		t.Fatalf("upstream deliveries = %d, want exactly %d", got, requestCount)
	}
	if got, max := app.AgentTransports.len(), app.agentStreamCapacity.snapshot().Pooled.Capacity; got > max {
		t.Fatalf("retained transport shards = %d, pooled budget = %d", got, max)
	}
	app.AgentTransports.closeAll()
	waitForAgentStreamCapacityUsage(t, app, 0)
	if err := app.agentStreamCapacity.validateInvariants(); err != nil {
		t.Fatalf("capacity invariant after mixed stress: %v", err)
	}
}

func suppressInfoLogsForTest(t *testing.T) {
	t.Helper()
	previousLogLevel := zerolog.GlobalLevel()
	zerolog.SetGlobalLevel(zerolog.WarnLevel)
	t.Cleanup(func() { zerolog.SetGlobalLevel(previousLogLevel) })
}
