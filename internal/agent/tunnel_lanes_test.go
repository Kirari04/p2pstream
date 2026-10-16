package agent

import (
	"context"
	"errors"
	"net/http"
	"strconv"
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
	if !errors.Is(err, errTunnelGroupProtocol) || errors.Is(err, errTunnelLaneUnsupported) {
		t.Fatalf("aggregate primary downgrade error = %v", err)
	}
	if !state.aggregate.Load() || !state.known.Load() {
		t.Fatal("aggregate group negotiation mutated after rejected downgrade")
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
