package agent

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hashicorp/yamux"
	"p2pstream/internal/sysmetrics"
	"p2pstream/internal/tunnel"
)

// Actual connector/supervisor, physical Yamux workers and capacity ledger. The
// in-memory management transport injects one failed replacement handshake;
// the public authenticated management path is covered by server runtime tests.
func TestAgentLaneRetirementReclaimsGrownPoolAfterFINDrain(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 8*time.Second)
	defer cancel()
	usage := sysmetrics.MemoryUsage{UsedBytes: 64 << 20, LimitBytes: 4 << 30, Source: "test"}
	capacity := newTestAgentAdaptiveCapacity(t, &usage)
	capacity.forceRefresh()
	negotiation := &tunnelGroupNegotiation{}
	negotiation.known.Store(true)
	negotiation.aggregate.Store(true)
	var mu sync.Mutex
	var peers []*yamux.Session
	var handshakes sync.WaitGroup
	readyPeers := make(chan *yamux.Session, 4)
	var attempts atomic.Int32
	transport := laneTestRoundTripper(func(r *http.Request) (*http.Response, error) {
		if r.Header.Get("Authorization") != "Bearer token" {
			return nil, fmt.Errorf("missing authentication")
		}
		enabled, id, replaces, err := tunnel.ParseLaneRetirement(r.Header)
		if err != nil || !enabled {
			return nil, fmt.Errorf("bad offer: %v", err)
		}
		attempt := attempts.Add(1)
		if attempt == 2 {
			return &http.Response{StatusCode: 503, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("replacement denied"))}, nil
		}
		if attempt > 1 && (replaces == "" || r.Header.Get(tunnel.LaneGrowthHeader) != "1") {
			return nil, fmt.Errorf("replacement missing owner/growth")
		}
		created := make(chan *yamux.Session, 1)
		response, err := rollbackUpgradeResponse(r, true, tunnel.TunnelLaneRoleBulk, true, created)
		if err != nil {
			return nil, err
		}
		peer := <-created
		mu.Lock()
		var predecessor *yamux.Session
		if len(peers) != 0 {
			predecessor = peers[len(peers)-1]
		}
		peers = append(peers, peer)
		mu.Unlock()
		readyPeers <- peer
		handshakes.Add(1)
		go func() {
			defer handshakes.Done()
			stream, err := tunnel.PrepareLaneRetirement(peer, id)
			if err == nil {
				if predecessor != nil {
					// This fixture has no outstanding Open calls at readiness. Model the
					// corrected server's local fence and old-wire acknowledgment.
					predecessor.StopOpening()
					if _, err := predecessor.Ping(); err != nil {
						stream.Reset()
						return
					}
				}
				tunnel.CommitLaneRetirement(stream)
			}
		}()
		response.Header.Set(tunnel.LaneRetirementHeader, "1")
		response.Header.Set(tunnel.LaneOwnerHeader, id)
		response.Header.Set(tunnel.LaneRetirementIntervalHeader, "200")
		if replaces != "" {
			response.Header.Set(tunnel.LaneReplacesHeader, replaces)
		}
		return response, nil
	})
	type held struct {
		session        *yamux.Session
		reader, writer *yamux.Stream
		baseline       int64
	}
	first := make(chan held, 1)
	second := make(chan struct{}, 1)
	problems := make(chan error, 8)
	var connected atomic.Int32
	observer := func(event tunnelSessionObservation) {
		if !event.Connected {
			return
		}
		peer := <-readyPeers
		if connected.Add(1) != 1 {
			second <- struct{}{}
			return
		}
		opened := make(chan *yamux.Stream, 1)
		go func() {
			stream, err := peer.OpenStream()
			if err != nil {
				problems <- err
			}
			opened <- stream
		}()
		reader, err := event.Session.AcceptStream()
		if err != nil {
			problems <- err
			return
		}
		writer := <-opened
		owner := negotiation.owners.Find(event.Session)
		baseline := capacity.snapshot().SessionResourceBytes
		// Original64MiB retained-pool demand is now refused before allocation.
		if granted, err := owner.Growth().Grow(reader, 16<<20); granted || err != nil {
			problems <- fmt.Errorf("64MiB request escaped cap: %v %v", granted, err)
		}
		if granted, err := owner.Growth().Grow(reader, 2<<20); !granted || err != nil {
			problems <- fmt.Errorf("growth denied: %v %v", granted, err)
		}
		first <- held{event.Session, reader, writer, baseline}
	}
	done := make(chan error, 1)
	joined := false
	go func() {
		done <- serveRetiringTunnelLane(ctx, tunnelLaneRegistration{group: "retirement-test-group", lane: 1, negotiation: negotiation, sessionObserver: observer}, func(ctx context.Context, reg tunnelLaneRegistration) error {
			return connectAndServe(ctx, &http.Client{Transport: transport}, "https://management.test/agent/tunnel", "retirement-agent", "agent", "token", nil, 2<<20, 64, false, capacity, reg)
		})
	}()
	defer func() {
		cancel()
		if !joined {
			select {
			case <-done:
			case <-time.After(3 * time.Second):
				t.Error("supervisor not joined")
			}
		}
		negotiation.owners.Close()
		mu.Lock()
		for _, peer := range peers {
			peer.Close()
		}
		mu.Unlock()
		handshakes.Wait()
	}()
	var h held
	select {
	case h = <-first:
	case err := <-problems:
		t.Fatal(err)
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	payload := bytes.Repeat([]byte("FIN retained"), 2048)
	if _, err := h.writer.Write(payload); err != nil {
		t.Fatal(err)
	}
	h.writer.Close()
	h.reader.Close()
	h.session.Ping()
	select {
	case <-second:
	case err := <-problems:
		t.Fatal(err)
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	if attempts.Load() != 3 || h.session.IsClosed() || h.session.NumStreams() != 0 || h.session.ReceiveOwnersDrained() {
		t.Fatal("handshake failure or FIN buffer mishandled")
	}
	snapshot := capacity.snapshot()
	if snapshot.AggregateGrowthPoolBytes <= 0 || snapshot.SessionResourceBytes <= h.baseline {
		t.Fatal("growth refunded while bytes readable")
	}
	got, err := io.ReadAll(h.reader)
	if err != nil || !bytes.Equal(got, payload) {
		t.Fatalf("draining FIN corrupted: %v", err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for (!h.session.IsClosed() || capacity.snapshot().AggregateGrowthPoolBytes != 0) && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if stats := h.session.AggregateReceiveWindowStats(); !stats.Closed || stats.TotalUnits != 0 {
		t.Fatalf("pool physically retained: %+v", stats)
	}
	if snapshot := capacity.snapshot(); snapshot.AggregateGrowthPoolBytes != 0 || snapshot.SessionResourceBytes != h.baseline {
		t.Fatalf("capacity did not return to prepaid baseline: %+v", snapshot)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("cancel did not join owners")
	}
	joined = true
	if snapshot := capacity.snapshot(); snapshot.SessionResourceBytes != 0 || snapshot.AggregateCurrentPoolBytes != 0 {
		t.Fatalf("shutdown retained reservations: %+v", snapshot)
	}
	select {
	case err := <-problems:
		t.Fatal(err)
	default:
	}
}

func TestRetiringSupervisorCancelsCompletedAttempt(t *testing.T) {
	var child context.Context
	err := serveRetiringTunnelLane(t.Context(), tunnelLaneRegistration{}, func(ctx context.Context, _ tunnelLaneRegistration) error { child = ctx; return io.EOF })
	if err != io.EOF {
		t.Fatalf("result=%v", err)
	}
	select {
	case <-child.Done():
	default:
		t.Fatal("completed attempt retained parent context child")
	}
}
