package agent

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"p2pstream/internal/tunnel"
)

type laneTestRoundTripper func(*http.Request) (*http.Response, error)

func (f laneTestRoundTripper) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestExtraTunnelRejectsMissingAcknowledgmentAfterServerDowngrade(t *testing.T) {
	client := &http.Client{Transport: laneTestRoundTripper(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusSwitchingProtocols, Header: http.Header{"Upgrade": {tunnel.UpgradeToken}}, Body: http.NoBody}, nil
	})}
	err := connectAndServe(t.Context(), client, "https://management.test/agent/tunnel", "test-agent", "test-agent", "test-token", nil, 2<<20, 64, false, nil,
		tunnelLaneRegistration{group: "a123456789012345", lane: 1})
	if !errors.Is(err, errTunnelLaneUnsupported) {
		t.Fatalf("extra lane did not stop after lost acknowledgment: %v", err)
	}
}

func TestPrimaryOffersAggregateContractAndMaximumBulkLaneUsesNewBoundary(t *testing.T) {
	for _, tc := range []struct {
		name string
		reg  tunnelLaneRegistration
		role string
	}{
		{
			name: "primary",
			reg:  tunnelLaneRegistration{group: "a123456789012345", lane: 0, negotiation: &tunnelGroupNegotiation{}},
			role: tunnel.TunnelLaneRoleControl,
		},
		{
			name: "maximum_bulk",
			reg: func() tunnelLaneRegistration {
				state := &tunnelGroupNegotiation{}
				state.known.Store(true)
				state.aggregate.Store(true)
				return tunnelLaneRegistration{group: "a123456789012345", lane: tunnel.MaxParallelTunnels, negotiation: state}
			}(),
			role: tunnel.TunnelLaneRoleBulk,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var request *http.Request
			client := &http.Client{Transport: laneTestRoundTripper(func(r *http.Request) (*http.Response, error) {
				request = r.Clone(r.Context())
				return &http.Response{StatusCode: http.StatusServiceUnavailable, Header: make(http.Header), Body: http.NoBody}, nil
			})}
			err := connectAndServe(t.Context(), client, "https://management.test/agent/tunnel", "test-agent", "test-agent", "test-token", nil, 2<<20, 64, false, nil, tc.reg)
			if err == nil {
				t.Fatal("expected synthetic upgrade failure")
			}
			if request == nil {
				t.Fatal("tunnel request not observed")
			}
			config, role, enabled, parseErr := tunnel.ParseAggregateFlowControlHeaders(request.Header)
			if parseErr != nil || !enabled || role != tc.role || !config.Equal(tunnel.DefaultAggregateFlowControlConfig()) {
				t.Fatalf("aggregate request = %#v, %q, %t, %v", config, role, enabled, parseErr)
			}
			if got := request.Header.Get(tunnel.TunnelLaneHeader); got != strconv.Itoa(tc.reg.lane) {
				t.Fatalf("lane header = %q, want %d", got, tc.reg.lane)
			}
		})
	}
}

func TestKnownLegacyGroupDoesNotReofferAggregate(t *testing.T) {
	state := &tunnelGroupNegotiation{}
	state.known.Store(true)
	var request *http.Request
	client := &http.Client{Transport: laneTestRoundTripper(func(r *http.Request) (*http.Response, error) {
		request = r.Clone(r.Context())
		return &http.Response{StatusCode: http.StatusServiceUnavailable, Header: make(http.Header), Body: http.NoBody}, nil
	})}
	_ = connectAndServe(t.Context(), client, "https://management.test/agent/tunnel", "test-agent", "test-agent", "test-token", nil, 2<<20, 64, false, nil,
		tunnelLaneRegistration{group: "a123456789012345", lane: 0, negotiation: state})
	if request == nil {
		t.Fatal("tunnel request not observed")
	}
	if _, _, enabled, err := tunnel.ParseAggregateFlowControlHeaders(request.Header); err != nil || enabled {
		t.Fatalf("known legacy retry offered aggregate: enabled=%t err=%v", enabled, err)
	}
}

func TestNegotiatedAggregatePrimaryRejectsDowngradeWithinGeneration(t *testing.T) {
	state := &tunnelGroupNegotiation{}
	state.known.Store(true)
	state.aggregate.Store(true)
	client := &http.Client{Transport: laneTestRoundTripper(func(r *http.Request) (*http.Response, error) {
		headers := make(http.Header)
		headers.Set("Upgrade", tunnel.UpgradeToken)
		headers.Set(tunnel.TunnelGroupHeader, "a123456789012345")
		headers.Set(tunnel.TunnelLaneHeader, "0")
		return &http.Response{StatusCode: http.StatusSwitchingProtocols, Header: headers, Body: http.NoBody}, nil
	})}
	err := connectAndServe(t.Context(), client, "https://management.test/agent/tunnel", "test-agent", "test-agent", "test-token", nil, 2<<20, 64, false, nil,
		tunnelLaneRegistration{group: "a123456789012345", lane: 0, negotiation: state})
	if !errors.Is(err, errTunnelGroupDowngrade) || errors.Is(err, errTunnelLaneUnsupported) {
		t.Fatalf("aggregate primary downgrade error = %v", err)
	}
	if !state.aggregate.Load() || !state.known.Load() {
		t.Fatal("aggregate group negotiation mutated after rejected downgrade")
	}
}

func TestNegotiatedAggregatePrimaryDowngradeRequiresCompleteLegacyAcknowledgment(t *testing.T) {
	for name, mutate := range map[string]func(http.Header){
		"old_server_absent": func(h http.Header) {
			h.Del(tunnel.TunnelGroupHeader)
			h.Del(tunnel.TunnelLaneHeader)
		},
		"missing_group": func(h http.Header) { h.Del(tunnel.TunnelGroupHeader) },
		"missing_lane":  func(h http.Header) { h.Del(tunnel.TunnelLaneHeader) },
		"wrong_group":   func(h http.Header) { h.Set(tunnel.TunnelGroupHeader, "b123456789012345") },
		"wrong_lane":    func(h http.Header) { h.Set(tunnel.TunnelLaneHeader, "1") },
		"duplicate":     func(h http.Header) { h.Add(tunnel.TunnelLaneHeader, "0") },
	} {
		t.Run(name, func(t *testing.T) {
			state := &tunnelGroupNegotiation{}
			state.known.Store(true)
			state.aggregate.Store(true)
			client := &http.Client{Transport: laneTestRoundTripper(func(r *http.Request) (*http.Response, error) {
				headers := make(http.Header)
				headers.Set("Upgrade", tunnel.UpgradeToken)
				headers.Set(tunnel.TunnelGroupHeader, "a123456789012345")
				headers.Set(tunnel.TunnelLaneHeader, "0")
				mutate(headers)
				return &http.Response{StatusCode: http.StatusSwitchingProtocols, Header: headers, Body: http.NoBody}, nil
			})}
			err := connectAndServe(t.Context(), client, "https://management.test/agent/tunnel", "test-agent", "test-agent", "test-token", nil, 2<<20, 64, false, nil,
				tunnelLaneRegistration{group: "a123456789012345", lane: 0, negotiation: state})
			if name == "old_server_absent" {
				if !errors.Is(err, errTunnelGroupDowngrade) {
					t.Fatalf("old-server downgrade error = %v", err)
				}
				return
			}
			if !errors.Is(err, errTunnelGroupProtocol) || errors.Is(err, errTunnelGroupDowngrade) {
				t.Fatalf("malformed downgrade error = %v", err)
			}
		})
	}
}

func TestAggregatePrimaryRequiresExactGroupAndLaneAcknowledgment(t *testing.T) {
	for name, mutate := range map[string]func(http.Header){
		"missing_group":   func(h http.Header) { h.Del(tunnel.TunnelGroupHeader) },
		"wrong_group":     func(h http.Header) { h.Set(tunnel.TunnelGroupHeader, "b123456789012345") },
		"duplicate_group": func(h http.Header) { h.Add(tunnel.TunnelGroupHeader, "a123456789012345") },
		"wrong_lane":      func(h http.Header) { h.Set(tunnel.TunnelLaneHeader, "1") },
		"duplicate_lane":  func(h http.Header) { h.Add(tunnel.TunnelLaneHeader, "0") },
	} {
		t.Run(name, func(t *testing.T) {
			state := &tunnelGroupNegotiation{}
			client := &http.Client{Transport: laneTestRoundTripper(func(r *http.Request) (*http.Response, error) {
				headers := make(http.Header)
				headers.Set("Upgrade", tunnel.UpgradeToken)
				headers.Set(tunnel.TunnelGroupHeader, "a123456789012345")
				headers.Set(tunnel.TunnelLaneHeader, "0")
				tunnel.SetAggregateFlowControlHeaders(headers, tunnel.TunnelLaneRoleControl, tunnel.DefaultAggregateFlowControlConfig())
				mutate(headers)
				return &http.Response{StatusCode: http.StatusSwitchingProtocols, Header: headers, Body: http.NoBody}, nil
			})}
			err := connectAndServe(t.Context(), client, "https://management.test/agent/tunnel", "test-agent", "test-agent", "test-token", nil, 2<<20, 64, false, nil,
				tunnelLaneRegistration{group: "a123456789012345", lane: 0, negotiation: state})
			if !errors.Is(err, errTunnelGroupProtocol) || errors.Is(err, errTunnelLaneUnsupported) {
				t.Fatalf("malformed primary ACK error = %v", err)
			}
			if state.known.Load() {
				t.Fatal("malformed primary ACK published group negotiation")
			}
		})
	}
}

func TestNegotiatedPrimaryProtocolFailureRetriesInsteadOfStoppingAgent(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	attempts := 0
	err := runTunnelLaneLoop(ctx, "https://management.test/agent/tunnel", tunnelLaneRegistration{lane: 0}, func() error {
		attempts++
		if attempts == 2 {
			cancel()
		}
		return errTunnelGroupProtocol
	}, func(context.Context, time.Duration) error { return nil })
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("primary retry loop error = %v, want context cancellation", err)
	}
	if attempts != 2 {
		t.Fatalf("primary retry attempts = %d, want 2", attempts)
	}
}

func TestTunnelGroupDowngradeJoinsBulkBeforeReplacement(t *testing.T) {
	bulkReady := make(chan struct{})
	var activeBulk atomic.Int32
	connect := func(ctx context.Context, lane tunnelLaneRegistration) error {
		if lane.lane == 0 {
			lane.accepted(true, true)
			<-bulkReady
			return errTunnelGroupDowngrade
		}
		activeBulk.Add(1)
		defer activeBulk.Add(-1)
		close(bulkReady)
		<-ctx.Done()
		return ctx.Err()
	}
	err := runTunnelGroupGeneration(t.Context(), "https://management.test/agent/tunnel", 1, connect, func(ctx context.Context, _ time.Duration) error {
		return ctx.Err()
	}, nil)
	if !errors.Is(err, errTunnelGroupDowngrade) {
		t.Fatalf("group error = %v", err)
	}
	if got := activeBulk.Load(); got != 0 {
		t.Fatalf("bulk connectors still active after generation join: %d", got)
	}
}

func TestMalformedPrimaryAcknowledgmentKeepsBulkUntilCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	bulkReady := make(chan struct{})
	malformedRetry := make(chan struct{})
	var malformedOnce sync.Once
	var activeBulk atomic.Int32
	connect := func(groupCtx context.Context, lane tunnelLaneRegistration) error {
		if lane.lane == 0 {
			lane.accepted(true, true)
			<-bulkReady
			return errTunnelGroupProtocol
		}
		activeBulk.Add(1)
		defer activeBulk.Add(-1)
		close(bulkReady)
		<-groupCtx.Done()
		return groupCtx.Err()
	}
	done := make(chan error, 1)
	go func() {
		done <- runTunnelGroupGeneration(ctx, "https://management.test/agent/tunnel", 1, connect, func(waitCtx context.Context, _ time.Duration) error {
			malformedOnce.Do(func() { close(malformedRetry) })
			<-waitCtx.Done()
			return waitCtx.Err()
		}, nil)
	}()
	select {
	case <-malformedRetry:
	case <-time.After(time.Second):
		t.Fatal("primary did not enter protocol-error retry")
	}
	if got := activeBulk.Load(); got != 1 {
		t.Fatalf("bulk connectors during malformed primary retry = %d, want 1", got)
	}
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("group cancellation error = %v", err)
	}
	if got := activeBulk.Load(); got != 0 {
		t.Fatalf("bulk connectors after cancellation = %d", got)
	}
}

func TestTunnelGroupCancellationInterruptsGenerationBackoff(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	backoffStarted := make(chan struct{})
	var attempts atomic.Int32
	done := make(chan error, 1)
	go func() {
		done <- runTunnelGroupGenerations(ctx, "https://management.test/agent/tunnel", 0, func(_ context.Context, lane tunnelLaneRegistration) error {
			if lane.lane != 0 {
				return errors.New("unexpected bulk attempt before downgrade")
			}
			attempts.Add(1)
			lane.accepted(true, true)
			return errTunnelGroupDowngrade
		}, func(waitCtx context.Context, _ time.Duration) error {
			close(backoffStarted)
			<-waitCtx.Done()
			return waitCtx.Err()
		}, nil)
	}()
	select {
	case <-backoffStarted:
	case <-time.After(time.Second):
		t.Fatal("generation backoff did not start")
	}
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("backoff cancellation error = %v", err)
	}
	if got := attempts.Load(); got != 1 {
		t.Fatalf("group attempts after cancellation = %d, want 1", got)
	}
}

func TestTunnelGroupReplacementRecoversLegacyThenAggregate(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	recovered := make(chan struct{})
	var mu sync.Mutex
	groups := make(map[string]int)
	groupOrder := make([]string, 0, 3)
	attempts := make(map[int]map[int]int)
	active := 0
	waits := 0
	connect := func(groupCtx context.Context, lane tunnelLaneRegistration) error {
		mu.Lock()
		generation, ok := groups[lane.group]
		if !ok {
			if active != 0 {
				mu.Unlock()
				return errors.New("new group started before old connectors joined")
			}
			generation = len(groupOrder) + 1
			groups[lane.group] = generation
			groupOrder = append(groupOrder, lane.group)
			attempts[generation] = make(map[int]int)
		}
		attempts[generation][lane.lane]++
		active++
		mu.Unlock()
		defer func() {
			mu.Lock()
			active--
			mu.Unlock()
		}()

		if lane.lane > 0 {
			<-groupCtx.Done()
			return groupCtx.Err()
		}
		switch generation {
		case 1:
			lane.accepted(true, true)
			return errTunnelGroupDowngrade
		case 2:
			lane.accepted(false, false)
			return errors.New("synthetic legacy disconnect")
		case 3:
			lane.accepted(true, true)
			close(recovered)
			<-groupCtx.Done()
			return groupCtx.Err()
		default:
			return errors.New("unexpected extra tunnel generation")
		}
	}
	done := make(chan error, 1)
	go func() {
		done <- runTunnelGroupGenerations(ctx, "https://management.test/agent/tunnel", 1, connect, func(ctx context.Context, _ time.Duration) error {
			mu.Lock()
			waits++
			mu.Unlock()
			return nil
		}, nil)
	}()
	select {
	case <-recovered:
	case <-time.After(time.Second):
		t.Fatal("fresh aggregate group did not recover")
	}
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("group loop error = %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(groupOrder) != 3 {
		t.Fatalf("group generations = %d, want 3", len(groupOrder))
	}
	if waits != 2 {
		t.Fatalf("generation backoffs = %d, want 2", waits)
	}
	if got := attempts[2][1]; got != 0 {
		t.Fatalf("old single-lane generation made %d extra lane attempts", got)
	}
	if active != 0 {
		t.Fatalf("connectors still active after cancellation: %d", active)
	}
}
