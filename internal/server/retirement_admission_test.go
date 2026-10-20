package server

import (
	"bufio"
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/hashicorp/yamux"

	"p2pstream/internal/config"
	"p2pstream/internal/sysmetrics"
	"p2pstream/internal/tunnel"
)

// Only the victim bulk connection is gated. CloseChan/IsClosed must not turn
// its still-owned receive promise into admission credit before physical Close.
type serverRetirementCloseGate struct {
	net.Conn
	entered chan struct{}
	proceed chan struct{}
	once    sync.Once
	unblock sync.Once
	err     error
}

func (c *serverRetirementCloseGate) Close() error {
	c.once.Do(func() {
		close(c.entered)
		<-c.proceed
		c.err = c.Conn.Close()
	})
	return c.err
}

func (c *serverRetirementCloseGate) release() {
	c.unblock.Do(func() { close(c.proceed) })
}

type serverRetirementHijacker struct {
	*httptest.ResponseRecorder
	conn net.Conn
}

func (w *serverRetirementHijacker) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	return w.conn, bufio.NewReadWriter(bufio.NewReader(w.conn), bufio.NewWriter(w.conn)), nil
}

type serverRetirementLane struct {
	server net.Conn
	client net.Conn
	peer   *yamux.Session
	done   chan struct{}
}

func TestServerAggregateLaneRetirementFencesCompetingAdmission(t *testing.T) {
	database := newAgentRegistryTestDB(t)
	app := NewApp(&config.Config{ManagementUIDisabled: true, TunnelMaxStreamWindowBytes: 2 << 20}, database)
	row := createAgentRegistryTestAgent(t, database, "server-retirement-admission", "Server retirement", "token")
	window, err := effectiveServerTunnelStreamWindowBytes(app.Config)
	if err != nil {
		t.Fatal(err)
	}
	controlBytes, err := tunnel.NegotiatedControlSessionResourceBytes(window)
	if err != nil {
		t.Fatal(err)
	}
	flow := tunnel.DefaultAggregateFlowControlConfig()
	bulkBytes, err := tunnel.AggregateSessionResourceBytes(flow.InitialSessionWindow, 0)
	if err != nil {
		t.Fatal(err)
	}
	// Include the configured single control reserve in the fixed headroom for
	// both physical session promises. The sample starts with zero streams; no
	// old sampled in-use floor or actual RSS reduction is used for recovery.
	charge := tunnel.ServerStreamBaseResourceModel().MemoryBytes
	headroom := (((controlBytes + bulkBytes + charge - 1) / charge) + 1) * charge
	const limit = int64(4 << 30)
	settings := sysmetrics.DefaultAdaptiveMemoryConfig()
	hard := (limit/100)*settings.HardLimitPercent + (limit%100)*settings.HardLimitPercent/100
	usage := sysmetrics.MemoryUsage{UsedBytes: hard - headroom, LimitBytes: limit, Source: "server-retirement-fixed-budget"}
	manager := newAdaptiveServerCapacityForTest(t, 64, &usage)
	app.agentStreamCapacity = manager
	manager.mu.Lock()
	sample := manager.resourceSnapshot
	manager.mu.Unlock()
	if sample.RejectNew || sample.ReservedStreamByte != 0 || sample.ReservedStreamFD != 0 ||
		sample.StreamChargeByte != charge || sample.StreamChargeFD != 0 || sample.HeadroomToHardByte != headroom {
		t.Fatalf("unexpected zero-stream server sample: %+v", sample)
	}

	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	gate := &serverRetirementCloseGate{entered: make(chan struct{}), proceed: make(chan struct{})}
	var lanes []*serverRetirementLane
	var agent *AgentConn
	closed := make(chan struct{})
	closeStarted := false
	// Register after the DB cleanup: release the injected barrier before closing
	// either peer, then join every test-owned handler/Close goroutine on failure.
	t.Cleanup(func() {
		cancel()
		gate.release()
		for _, lane := range lanes {
			_ = lane.client.Close()
			_ = lane.server.Close()
			if lane.peer != nil {
				_ = lane.peer.Close()
			}
			select {
			case <-lane.done:
			case <-time.After(2 * time.Second):
				t.Error("server tunnel handler did not join during cleanup")
			}
		}
		if closeStarted {
			select {
			case <-closed:
			case <-time.After(2 * time.Second):
				t.Error("victim session Close did not join during cleanup")
			}
		}
		current := agent
		if current == nil {
			current = app.AgentHub.connectedByID(row.ID)
		}
		if !serverRetirementUntil(func() bool {
			s := manager.snapshot()
			rawEmpty := current == nil || len(reconnectRawLanes(current)) == 0
			return rawEmpty && app.AgentHub.connectedByID(row.ID) == nil && s.RegisteredSessions == 0 && len(s.SessionLimits) == 0 &&
				s.AdaptiveExternalBytes == 0 && s.AdaptiveExternalFDs == 0 && s.ActiveLeases == 0 && s.Total.InUse == 0 && s.Waiters == 0 &&
				s.AdaptiveExternalReservationsByOwner[adaptiveResourceOwnerReceiveWindow] == 0
		}) {
			t.Error("server cleanup retained raw lanes, registration or resource owners")
		}
		if err := manager.validateInvariants(); err != nil {
			t.Error(err)
		}
		// Raw removal precedes DB side effects under this lock. Cross it before
		// the older DB cleanup registered by newAgentRegistryTestDB can execute.
		unlock := app.lockAgentAuth(row.ID)
		unlock()
	})
	const group = "0123456789abcdef"
	startLane := func(laneID int, barrier *serverRetirementCloseGate) *yamux.Session {
		t.Helper()
		serverConn, clientConn := net.Pipe()
		if barrier != nil {
			barrier.Conn = serverConn
			serverConn = barrier
		}
		lane := &serverRetirementLane{server: serverConn, client: clientConn, done: make(chan struct{})}
		lanes = append(lanes, lane)
		_ = serverConn.SetDeadline(time.Now().Add(2 * time.Second))
		_ = clientConn.SetDeadline(time.Now().Add(2 * time.Second))
		request := newAggregateTunnelRequest(t, row.PublicID, "token", group, laneID, true).WithContext(ctx)
		writer := &serverRetirementHijacker{ResponseRecorder: httptest.NewRecorder(), conn: serverConn}
		go func() {
			defer close(lane.done)
			app.agentTunnelHandler(writer, request)
		}()
		reader := bufio.NewReader(clientConn)
		response, err := http.ReadResponse(reader, request)
		if err != nil || response.StatusCode != http.StatusSwitchingProtocols || reader.Buffered() != 0 {
			t.Fatalf("lane %d upgrade: response=%v err=%v buffered=%d", laneID, response, err, reader.Buffered())
		}
		var peerConfig *yamux.Config
		if laneID == 0 {
			peerConfig, err = tunnel.NewNegotiatedControlYamuxConfig(nil, window, 64)
		} else {
			peerConfig, err = tunnel.NewAggregateYamuxConfig(nil, app.Config.TunnelMaxStreamWindowBytes, 64)
		}
		if err != nil {
			t.Fatal(err)
		}
		peerConfig.EnableKeepAlive = false
		lane.peer, err = yamux.Client(clientConn, peerConfig)
		if err != nil {
			t.Fatal(err)
		}
		select {
		case <-lane.done:
		case <-time.After(2 * time.Second):
			t.Fatal("server handler did not publish lane")
		}
		_ = serverConn.SetDeadline(time.Time{})
		_ = clientConn.SetDeadline(time.Time{})
		current := app.AgentHub.connectedByID(row.ID)
		if current == nil || !current.AggregateFlowControl || current.TunnelGroup != group {
			t.Fatal("real handler did not publish the negotiated generation")
		}
		session := current.tunnelSession(laneID)
		if session == nil {
			t.Fatalf("real handler did not publish lane %d", laneID)
		}
		return session
	}
	control := startLane(0, nil)
	agent = app.AgentHub.connectedByID(row.ID)
	victim := startLane(1, gate)
	poolBytes := int64(yamux.AggregateReceiveWindowMemory(flow.InitialSessionWindow))
	resource := agent.tunnelResourceSnapshot()
	if !resource.ControlConnected || resource.BulkSessions != 1 || resource.AggregateInitialPoolBytes != poolBytes ||
		resource.AggregateCurrentPoolBytes != poolBytes || resource.SessionMetadataBytes != bulkBytes-poolBytes ||
		resource.LegacyControlSupplementBytes != controlBytes || resource.SessionResourceBytes != controlBytes+bulkBytes {
		t.Fatalf("published server lane promises: %+v", resource)
	}
	key := agentStreamCapacitySessionKey(agent, control)
	assertOwners := func(phase string, bytes, owners int64) {
		t.Helper()
		s := manager.snapshot()
		if s.AdaptiveExternalBytes != bytes || s.AdaptiveExternalBytesByOwner[adaptiveResourceOwnerReceiveWindow] != bytes ||
			s.AdaptiveExternalReservationsByOwner[adaptiveResourceOwnerReceiveWindow] != owners || s.AdaptiveExternalFDs != 0 ||
			s.LegacyStreamSupplementBytes != 0 || s.Total.InUse != 0 || s.ActiveLeases != 0 || s.Waiters != 0 ||
			s.RegisteredSessions != 1 || s.SessionLimits[key] != 64 || s.MemoryUsedBytes != usage.UsedBytes {
			t.Fatalf("server physical owner accounting: %+v", s)
		}
		manager.mu.Lock()
		sameSample := manager.resourceSnapshot.Generation == sample.Generation && manager.resourceSnapshot.SampledAt == sample.SampledAt
		manager.mu.Unlock()
		if !sameSample {
			t.Fatal("test changed the resource sample during owner retirement")
		}
		if err := manager.validateInvariants(); err != nil {
			t.Fatal(err)
		}
		t.Logf("server_retirement phase=%s receive_window_bytes=%d reservations=%d external_fds=%d logical_streams=%d active_leases=%d registered_sessions=%d memory_used_bytes=%d sample_generation=%d",
			phase, bytes, owners, s.AdaptiveExternalFDs, s.Total.InUse, s.ActiveLeases, s.RegisteredSessions, s.MemoryUsedBytes, sample.Generation)
	}
	assertDenied := func(phase string) {
		t.Helper()
		writer := &failingHijackResponseWriter{}
		request := newAggregateTunnelRequest(t, row.PublicID, "token", group, 2, true).WithContext(ctx)
		app.agentTunnelHandler(writer, request)
		if writer.status != http.StatusServiceUnavailable || writer.body.String() != "aggregate tunnel resource capacity reached\n" {
			t.Fatalf("competing same-group bulk lane status=%d body=%q", writer.status, writer.body.String())
		}
		if app.AgentHub.connectedByID(row.ID) != agent || control.IsClosed() || agent.tunnelSession(0) != control || reconnectRawLanes(agent)[2] != nil {
			t.Fatal("denied competing lane disturbed the surviving generation")
		}
		assertOwners(phase, controlBytes+bulkBytes, 2)
	}
	assertOwners("published", controlBytes+bulkBytes, 2)
	assertDenied("before_close_denied")
	closeStarted = true
	go func() {
		defer close(closed)
		_ = victim.Close()
	}()
	select {
	case <-gate.entered:
	case <-time.After(2 * time.Second):
		t.Fatal("victim did not enter the physical Close barrier")
	}
	if !victim.IsClosed() || agent.tunnelSession(1) != nil || reconnectRawLanes(agent)[1] != victim {
		t.Fatal("shutdown initiation did not hide only routing while retaining the physical raw slot")
	}
	resource = agent.tunnelResourceSnapshot()
	if !resource.ControlConnected || resource.BulkSessions != 0 || resource.AggregateCurrentPoolBytes != 0 || resource.SessionResourceBytes != controlBytes {
		t.Fatalf("closing lane remained in usable topology: %+v", resource)
	}
	select {
	case <-closed:
		t.Fatal("session Close returned through the physical barrier")
	default:
	}
	assertDenied("physical_close_blocked_denied")
	gate.release()
	select {
	case <-closed:
	case <-time.After(2 * time.Second):
		t.Fatal("victim session Close did not join")
	}
	if !serverRetirementUntil(func() bool {
		return reconnectRawLanes(agent)[1] == nil && manager.snapshot().AdaptiveExternalBytes == controlBytes
	}) {
		t.Fatal("server watcher did not remove the retired lane and refund its exact owner")
	}
	stats := victim.AggregateReceiveWindowStats()
	if !stats.Closed || stats.TotalUnits != 0 || stats.FreeUnits != 0 || stats.BufferedUnits != 0 || stats.PendingRefund != 0 {
		t.Fatalf("retired physical aggregate pool still owns units: %+v", stats)
	}
	assertOwners("victim_retired", controlBytes, 1)
	replacement := startLane(2, nil)
	if replacement == victim || agent.tunnelSession(2) != replacement {
		t.Fatal("fresh competing lane did not receive a new physical owner")
	}
	assertOwners("fresh_competitor_published", controlBytes+bulkBytes, 2)
}

// Polling is only for the exact terminal watcher predicate, never the Close
// interleaving oracle. The latter is the entered/proceed barrier above.
func serverRetirementUntil(ready func() bool) bool {
	deadline := time.NewTimer(2 * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(time.Millisecond)
	defer tick.Stop()
	for {
		if ready() {
			return true
		}
		select {
		case <-tick.C:
		case <-deadline.C:
			return false
		}
	}
}
