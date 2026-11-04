package server

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"p2pstream/internal/config"
	"p2pstream/internal/sysmetrics"
	"strconv"
	"testing"
	"time"

	"github.com/hashicorp/yamux"
	"p2pstream/internal/tunnel"
)

func TestRealLaneRetirementRejectsInvalidOffersWithoutDisplacement(t *testing.T) {
	f := newRealAgentProxyFixture(t, 1, 2<<20, 0, 100*time.Millisecond)
	old := f.agent.tunnelSession(1)
	owner := f.agent.tunnelOwners.Find(old)
	baseline := f.agent.tunnelOwners.ReservedBytes()
	request := func() *http.Request {
		r, _ := http.NewRequestWithContext(t.Context(), "GET", f.management.URL+"/agent/tunnel", nil)
		r.Header = newAggregateTunnelRequest(t, f.agent.PublicID, "test-token", f.agent.TunnelGroup, 1, true).Header.Clone()
		r.Header.Set(tunnel.TunnelCapacityModeHeader, tunnel.TunnelCapacityModeAdaptive)
		r.Header.Set(tunnel.TunnelMaxConcurrentStreamsHeader, strconv.FormatInt(f.agent.AdvertisedMaxConcurrentStreams, 10))
		r.Header.Set(tunnel.TunnelAgentVersionHeader, f.agent.BuildVersion)
		r.Header.Set(tunnel.TunnelAgentCommitHeader, f.agent.BuildCommit)
		r.Header.Set(tunnel.LaneRetirementHeader, "1")
		r.Header.Set(tunnel.LaneOwnerHeader, "replacement-0123456789")
		r.Header.Set(tunnel.LaneReplacesHeader, owner.ID)
		r.Header.Set(tunnel.LaneGrowthHeader, "1")
		return r
	}
	for _, tc := range []struct {
		name   string
		status int
		mutate func(*http.Request)
	}{
		{"unauthenticated", 401, func(r *http.Request) { r.Header.Set("Authorization", "Bearer invalid") }},
		{"withdraw-owned-retirement", 409, func(r *http.Request) {
			for _, name := range []string{tunnel.LaneRetirementHeader, tunnel.LaneOwnerHeader, tunnel.LaneReplacesHeader, tunnel.LaneGrowthHeader} {
				r.Header.Del(name)
			}
		}},
		{"duplicate-capability", 400, func(r *http.Request) { r.Header.Add(tunnel.LaneRetirementHeader, "1") }},
		{"duplicate-owner", 400, func(r *http.Request) { r.Header.Add(tunnel.LaneOwnerHeader, "another-0123456789") }},
		{"duplicate-replaces", 400, func(r *http.Request) { r.Header.Add(tunnel.LaneReplacesHeader, owner.ID) }},
		{"duplicate-interval", 400, func(r *http.Request) {
			r.Header.Add(tunnel.LaneRetirementIntervalHeader, "100")
			r.Header.Add(tunnel.LaneRetirementIntervalHeader, "100")
		}},
		{"invalid-interval", 400, func(r *http.Request) { r.Header.Set(tunnel.LaneRetirementIntervalHeader, "-1") }},
		{"stale-owner", 409, func(r *http.Request) { r.Header.Set(tunnel.LaneReplacesHeader, "stale-owner-0123456789") }},
		{"stale-generation", 409, func(r *http.Request) { r.Header.Set(tunnel.TunnelGroupHeader, "stale-generation-0123456789") }},
		{"update-cordon", 503, func(r *http.Request) { f.app.setAgentUpdateCordon(f.agent.AgentID) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := request()
			tc.mutate(r)
			response, err := f.management.Client().Do(r)
			if err != nil {
				t.Fatal(err)
			}
			// An unexpected101 is a live upgraded socket, not a finite error
			// body. Close it before failing rather than trying to drain forever.
			if response.StatusCode != tc.status {
				response.Body.Close()
				f.app.clearAgentUpdateCordon(f.agent.AgentID)
				t.Fatalf("status %d want%d", response.StatusCode, tc.status)
			}
			io.Copy(io.Discard, io.LimitReader(response.Body, 1024))
			response.Body.Close()
			f.app.clearAgentUpdateCordon(f.agent.AgentID)
			if f.agent.tunnelSession(1) != old || old.IsClosed() || f.agent.tunnelOwners.ReservedBytes() != baseline || len(f.agent.tunnelOwners.Snapshot()) != 2 {
				t.Fatal("rejected offer changed serving ownership")
			}
		})
	}
	awaitLaneRetirement(t, "predecessor not idle", func() bool { return owner.IdleFor(100 * time.Millisecond) })
	// An authenticated candidate that fails readiness must join before returning
	// its overlap owner, while leaving the exact serving predecessor untouched.
	for range 3 {
		response, err := f.management.Client().Do(request())
		if err != nil {
			t.Fatal(err)
		}
		if response.StatusCode != 101 {
			response.Body.Close()
			t.Fatalf("candidate status%d", response.StatusCode)
		}
		cfg, err := tunnel.NewAggregateYamuxConfig(nil, 2<<20, 64)
		if err != nil {
			t.Fatal(err)
		}
		peer, err := yamux.Client(response.Body.(io.ReadWriteCloser), cfg)
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(t.Context(), time.Second)
		stream, err := peer.AcceptStreamWithContext(ctx)
		cancel()
		if err != nil {
			peer.Close()
			t.Fatal(err)
		}
		stream.SetDeadline(time.Now().Add(time.Second))
		hello := make([]byte, len("lane-ready-v1:replacement-0123456789\n"))
		if _, err := io.ReadFull(stream, hello); err != nil {
			peer.Close()
			t.Fatal(err)
		}
		// A premature commit byte is not an acknowledgment and cannot publish this lane.
		stream.Write([]byte{2})
		stream.Close()
		peer.Close()
		awaitLaneRetirement(t, "failed handshake leaked overlap", func() bool { return len(f.agent.tunnelOwners.Snapshot()) == 2 })
		if f.agent.tunnelSession(1) != old || old.IsClosed() || f.agent.tunnelOwners.ReservedBytes() != baseline {
			t.Fatal("failed readiness displaced predecessor")
		}
	}
	if n, err := f.roundTrip(t.Context(), "/api", nil); err != nil || n != 2 {
		t.Fatalf("serving lane failed after rejected churn: %d %v", n, err)
	}
}

func TestServerAggregatePressureDoesNotDowngradeToLegacy(t *testing.T) {
	database := newAgentRegistryTestDB(t)
	app := NewApp(&config.Config{ManagementUIDisabled: true}, database)
	row := createAgentRegistryTestAgent(t, database, "pressure-no-downgrade", "Pressure", "token")
	usage := sysmetrics.MemoryUsage{UsedBytes: 510 << 20, LimitBytes: 512 << 20, Source: "test"}
	app.agentStreamCapacity = newAdaptiveServerCapacityForTest(t, 64, &usage)
	mux := http.NewServeMux()
	app.RegisterManagementRoutes(mux)
	management := httptest.NewTLSServer(mux)
	defer management.Close()
	for range 3 {
		r, _ := http.NewRequestWithContext(t.Context(), "GET", management.URL+"/agent/tunnel", nil)
		r.Header = newAggregateTunnelRequest(t, row.PublicID, "token", "pressure-group-012345", 0, true).Header.Clone()
		response, err := management.Client().Do(r)
		if err != nil {
			t.Fatal(err)
		}
		io.Copy(io.Discard, response.Body)
		response.Body.Close()
		if response.StatusCode != http.StatusServiceUnavailable {
			t.Fatalf("pressure status%d, want retryable503 without legacy downgrade", response.StatusCode)
		}
	}
	if app.AgentHub.connectedByID(row.ID) != nil || app.agentStreamCapacity.snapshot().AdaptiveExternalBytes != 0 {
		t.Fatal("pressure admitted an unreserved generation")
	}
}
