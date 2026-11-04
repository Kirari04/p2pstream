package agent

import (
	"context"
	"errors"
	"github.com/hashicorp/yamux"
	"net/http"
	"p2pstream/internal/sysmetrics"
	"p2pstream/internal/tunnel"
	"testing"
	"time"
)

func TestAgentRetirementAcknowledgmentRejectsAndJoinsOwnership(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(http.Header)
	}{
		{"wrong-owner", func(h http.Header) { h.Set(tunnel.LaneOwnerHeader, "wrong-owner-0123456789") }},
		{"unexpected-predecessor", func(h http.Header) { h.Set(tunnel.LaneReplacesHeader, "stale-owner-0123456789") }},
		{"duplicate-capability", func(h http.Header) { h.Add(tunnel.LaneRetirementHeader, "1") }},
		{"duplicate-owner", func(h http.Header) { h.Add(tunnel.LaneOwnerHeader, "another-owner-0123456789") }},
		{"missing-interval", func(h http.Header) { h.Del(tunnel.LaneRetirementIntervalHeader) }},
		{"duplicate-interval", func(h http.Header) { h.Add(tunnel.LaneRetirementIntervalHeader, "200") }},
		{"invalid-interval", func(h http.Header) { h.Set(tunnel.LaneRetirementIntervalHeader, "999999999999999999999") }},
		{"orphan-interval", func(h http.Header) { h.Del(tunnel.LaneRetirementHeader); h.Del(tunnel.LaneOwnerHeader) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			usage := sysmetrics.MemoryUsage{UsedBytes: 64 << 20, LimitBytes: 2 << 30, Source: "test"}
			capacity := newTestAgentAdaptiveCapacity(t, &usage)
			capacity.forceRefresh()
			negotiation := &tunnelGroupNegotiation{}
			negotiation.known.Store(true)
			negotiation.aggregate.Store(true)
			for range 3 {
				var peer *yamux.Session
				client := &http.Client{Transport: laneTestRoundTripper(func(r *http.Request) (*http.Response, error) {
					ready := make(chan *yamux.Session, 1)
					response, err := rollbackUpgradeResponse(r, true, tunnel.TunnelLaneRoleBulk, true, ready)
					if err != nil {
						return nil, err
					}
					peer = <-ready
					response.Header.Set(tunnel.LaneRetirementHeader, "1")
					response.Header.Set(tunnel.LaneOwnerHeader, r.Header.Get(tunnel.LaneOwnerHeader))
					response.Header.Set(tunnel.LaneRetirementIntervalHeader, "200")
					tc.mutate(response.Header)
					return response, nil
				})}
				ctx, cancel := context.WithTimeout(t.Context(), time.Second)
				err := connectAndServe(ctx, client, "https://management.test/agent/tunnel", "id", "name", "token", nil, 2<<20, 64, true, capacity, tunnelLaneRegistration{group: "retirement-contract-012345", lane: 1, negotiation: negotiation, ready: func(laneReady) { t.Error("invalid acknowledgment became serving") }})
				cancel()
				if peer != nil {
					peer.Close()
				}
				if err == nil {
					t.Fatal("invalid acknowledgment accepted")
				}
				if len(negotiation.owners.Snapshot()) != 0 || negotiation.owners.ReservedBytes() != 0 || capacity.snapshot().SessionResourceBytes != 0 {
					t.Fatal("invalid repeated acknowledgment leaked/multiplied owners")
				}
			}
		})
	}
}

func TestAgentAggregatePressureDoesNotOfferLegacy(t *testing.T) {
	usage := sysmetrics.MemoryUsage{UsedBytes: 510 << 20, LimitBytes: 512 << 20, Source: "test"}
	capacity := newTestAgentAdaptiveCapacity(t, &usage)
	capacity.forceRefresh()
	negotiation := &tunnelGroupNegotiation{}
	negotiation.known.Store(true)
	negotiation.aggregate.Store(true)
	calls := 0
	client := &http.Client{Transport: laneTestRoundTripper(func(r *http.Request) (*http.Response, error) {
		calls++
		return nil, errors.New("unexpected network admission")
	})}
	for range 3 {
		err := connectAndServe(t.Context(), client, "https://management.test/agent/tunnel", "id", "name", "token", nil, 2<<20, 64, true, capacity, tunnelLaneRegistration{group: "pressure-contract-012345", lane: 1, negotiation: negotiation, ready: func(laneReady) { t.Error("unreserved session became serving") }})
		if !errors.Is(err, errTunnelSessionResourceUnavailable) {
			t.Fatalf("wrong pressure semantics: %v", err)
		}
	}
	if calls != 0 || !negotiation.aggregate.Load() || capacity.snapshot().LegacySessionEnvelopeBytes != 0 || negotiation.owners.ReservedBytes() != 0 {
		t.Fatal("resource denial downgraded capability or leaked")
	}
}
