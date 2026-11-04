package server

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hashicorp/yamux"
	"github.com/rs/zerolog"

	agentruntime "p2pstream/internal/agent"
	"p2pstream/internal/config"
	"p2pstream/internal/db"
	"p2pstream/internal/tunnel"
)

// One public HTTP listener composes lane loss, in-generation control recovery,
// real reauthentication and duplicate old cleanup. Events have no generation
// field: generation proof uses the hub, raw lanes, pool entries, leases and DB.
func TestPublicAgentReconnectGenerationWorkload(t *testing.T) {
	f := newPublicReconnectFixture(t)
	defer func() {
		if t.Failed() {
			f.logOwnership(t, "test-failure-before-cleanup")
		}
	}()
	g1Runtime := f.startRuntime(t, "reconnect-token-1")
	g1 := f.awaitGeneration(t, nil)
	f.remember(g1)
	g1Key := agentStreamCapacitySessionKey(g1, g1.tunnelSession(1))
	g1Group, g1Connection := g1.TunnelGroup, g1.ConnectionDBID
	if g1Group == "" || g1Connection <= 0 {
		t.Fatal("first real authentication omitted group or connection row")
	}
	f.startPublic(t, g1)

	lost := f.startRequest(t, "lane-loss", 0)
	survivor := f.startRequest(t, "lane-survivor", 1)
	lostOwner := f.heldOwner(t, g1, 0)
	survivorOwner := f.heldOwner(t, g1, 1)
	if lostOwner.lane == survivorOwner.lane || lostOwner.session == survivorOwner.session || lostOwner.leaseID == survivorOwner.leaseID {
		t.Fatal("distinct target keys did not occupy distinct real pooled bulk lanes")
	}
	f.assertCapacity(t, g1Key, 2)
	if lostOwner.session.NumStreams() != 1 || survivorOwner.session.NumStreams() != 1 {
		t.Fatal("held responses did not each own one physical bulk stream")
	}

	// Close returns only after this session's reader/publisher/sender workers
	// join. NumStreams on a closed session can retain force-closed metadata;
	// closed aggregate slabs, raw-slot removal and the old lease ID prove
	// physical/resource retirement instead of treating that metadata as live.
	if err := lostOwner.session.Close(); err != nil {
		t.Fatal(err)
	}
	f.finish(t, lost, false)
	mixedSaturationAwait(t, "failed bulk physical owner retired before refund check", func() bool {
		return reconnectSessionRetired(lostOwner.session, lostOwner.lane) && !reconnectRawHasSession(g1, lostOwner.session) && !f.hasLease(lostOwner.leaseID)
	})
	f.assertCapacity(t, g1Key, 1)
	f.assertHeld(t, survivor)
	if f.heldOwner(t, g1, 1) != survivorOwner || survivorOwner.session.NumStreams() != 1 {
		t.Fatal("closing one bulk lane disturbed the surviving pooled stream")
	}
	mixedSaturationAwait(t, "same generation restores its missing bulk lane", func() bool {
		return f.app.AgentHub.connectedByID(f.row.ID) == g1 && realAgentAggregateLaneShape(g1, 2)
	})
	f.remember(g1)

	oldControl := g1.tunnelSession(0)
	if oldControl == nil {
		t.Fatal("missing initial control session")
	}
	func() {
		// Ordinary forwarding uses a separate traffic fence, not this auth
		// lock. Gate control reauthentication so useful public progress must
		// complete BEFORE a new control session can be installed. Always unlock
		// before any wait for reauthentication or asynchronous lane cleanup.
		unlock := f.app.lockAgentAuth(g1.AgentID)
		defer unlock()
		if err := oldControl.Close(); err != nil {
			t.Fatal(err)
		}
		if g1.tunnelSession(0) != nil {
			t.Fatal("closed control remains usable")
		}
		f.finish(t, f.startRequest(t, "primary-progress", 0), true)
		f.assertHeld(t, survivor)
		if f.app.AgentHub.connectedByID(f.row.ID) != g1 || g1.TunnelGroup != g1Group || g1.tunnelSession(0) != nil || f.heldOwner(t, g1, 1) != survivorOwner {
			t.Fatal("public progress did not preserve the generation and held bulk owner while control reauthentication was gated")
		}
	}()
	mixedSaturationAwait(t, "same group installs a distinct control session", func() bool {
		control := g1.tunnelSession(0)
		return f.app.AgentHub.connectedByID(f.row.ID) == g1 && g1.TunnelGroup == g1Group &&
			control != nil && control != oldControl && realAgentAggregateLaneShape(g1, 2)
	})
	f.remember(g1)
	if g1.ConnectionDBID != g1Connection || f.app.agentStreamCapacity.snapshot().RegisteredSessions != 1 {
		t.Fatal("primary reconnect changed the DB generation or multiplied registration")
	}
	f.assertHeld(t, survivor)
	if f.heldOwner(t, g1, 1) != survivorOwner {
		t.Fatal("primary reconnect replaced the active bulk owner")
	}
	f.gates[survivor.name].open()
	f.finish(t, survivor, true)
	f.awaitNoStreams(t, g1)

	oldRequest := f.startRequest(t, "old-generation", 0)
	oldOwner := f.heldOwner(t, g1, 0)
	oldLanes := reconnectRawLanes(g1)
	// Rotate before starting G2: G1's reconnect loops cannot compete using the
	// old credential after replacement. No auth-lock hook waits for G2.
	if _, err := f.app.DB.ExecContext(f.ctx, "UPDATE agents SET token_hash=? WHERE id=?", hashAgentToken("reconnect-token-2"), f.row.ID); err != nil {
		t.Fatal(err)
	}
	g2Runtime := f.startRuntime(t, "reconnect-token-2")
	g2 := f.awaitGeneration(t, g1)
	f.remember(g2)
	g2Key := agentStreamCapacitySessionKey(g2, g2.tunnelSession(1))
	if g2 == g1 || g2.TunnelGroup == "" || g2.TunnelGroup == g1Group || g2.ConnectionDBID <= 0 || g2.ConnectionDBID == g1Connection || g2Key == g1Key {
		t.Fatal("reauthentication failed to create a distinct pointer/group/key/DB row")
	}
	f.stopRuntime(t, g1Runtime)
	f.finish(t, oldRequest, false)
	mixedSaturationAwait(t, "retired G1 lanes leases and transports fully drained", func() bool {
		if len(reconnectRawLanes(g1)) != 0 || !f.noPoolFor(g1) || f.hasLease(oldOwner.leaseID) {
			return false
		}
		for lane, session := range oldLanes {
			if !reconnectSessionRetired(session, lane) {
				return false
			}
		}
		snapshot := f.app.agentStreamCapacity.snapshot()
		_, oldRegistered := snapshot.SessionLimits[g1Key]
		return !oldRegistered && snapshot.TotalBySession[g1Key] == 0 && snapshot.PublicBySession[g1Key] == 0
	})
	// Serialize behind actual G1 cleanup before inspecting its disconnected row.
	unlock := f.app.lockAgentAuth(f.row.ID)
	unlock()
	f.awaitDisconnectedRow(t, g1Connection)
	f.assertOnlyOpenRow(t, g2.ConnectionDBID)
	f.finish(t, f.startRequest(t, "replacement-progress", 1), true)
	f.awaitNoStreams(t, g2)

	current := f.startRequest(t, "current-generation", 0)
	currentOwner := f.heldOwner(t, g2, 0)
	f.assertCapacity(t, g2Key, 1)
	before, err := f.app.DB.GetAgent(f.ctx, f.row.ID)
	if err != nil {
		t.Fatal(err)
	}
	// These are explicitly delayed, duplicate old-generation cleanup calls
	// AFTER G2 has served a public request and while its next response is held.
	for range 2 {
		for lane := 0; lane <= 2; lane++ {
			f.app.cleanupAgentTunnel(g1, lane, oldLanes[lane])
		}
		if f.app.cleanupAgentConnection(g1) {
			t.Fatal("stale cleanup disconnected G2")
		}
	}
	if f.app.AgentHub.connectedByID(f.row.ID) != g2 || !realAgentAggregateLaneShape(g2, 2) {
		t.Fatal("stale cleanup changed G2 hub/group topology")
	}
	f.assertHeld(t, current)
	if f.heldOwner(t, g2, 0) != currentOwner || !f.noPoolFor(g1) {
		t.Fatal("stale cleanup retired the current pooled transport or restored an old one")
	}
	f.assertCapacity(t, g2Key, 1)
	f.assertOnlyOpenRow(t, g2.ConnectionDBID)
	after, err := f.app.DB.GetAgent(f.ctx, f.row.ID)
	if err != nil || before.LastConnectedAt != after.LastConnectedAt || before.LastDisconnectedAt != after.LastDisconnectedAt {
		t.Fatalf("stale cleanup changed current DB connected state: %v", err)
	}
	f.finish(t, f.startRequest(t, "after-stale-progress", 1), true)
	f.assertHeld(t, current)
	f.gates[current.name].open()
	f.finish(t, current, true)
	f.awaitNoStreams(t, g2)
	f.checkEvents(t)
	f.stopRuntime(t, g2Runtime)
	f.close(t)
	if t.Failed() {
		return
	}
	t.Log("public_reconnect_terminal requests=7 events=7 origin=7 bulk_failures=1 primary_rejoins=1 generations=2 stale_cleanup_rounds=2 truncated_responses=2 retries=0 joined_agents=2 all_physical_streams=0 all_owners=0 registered_sessions=0 active_leases=0 pool_entries=0 waiters=0 recorder_pending=0 recorder_dropped=0")
}

type publicReconnectRuntime struct {
	cancel context.CancelFunc
	done   chan struct{}
	err    error
	joined bool
}

type publicReconnectGate struct {
	prefix  []byte
	tail    []byte
	release chan struct{}
	stopped chan struct{}
	once    sync.Once
	calls   atomic.Int64
}

func (g *publicReconnectGate) open() { g.once.Do(func() { close(g.release) }) }

type publicReconnectRequest struct {
	name   string
	host   string
	target int
	cancel context.CancelFunc
	ready  chan error
	done   chan struct{}
	body   []byte
	status int
	err    error
}

type publicReconnectOwner struct {
	entry   *pooledAgentTransport
	lane    int
	session *yamux.Session
	leaseID uint64
}

type publicReconnectFixture struct {
	app                 *App
	row                 db.Agent
	ctx                 context.Context
	cancel              context.CancelFunc
	management          *httptest.Server
	ca                  string
	origin              *httptest.Server
	transport           *http.Transport
	client              *http.Client
	address             string
	targets             [2]publicRouteTargetConfig
	gates               map[string]*publicReconnectGate
	requests            []*publicReconnectRequest
	runtimes            []*publicReconnectRuntime
	runtimeAgentIDs     []int64
	targetAgentIDs      [2]int64
	generations         []*AgentConn
	sessions            map[*yamux.Session]int
	originErrors        chan error
	originErrorOverflow atomic.Bool
	closed              bool
}

func newPublicReconnectFixture(t *testing.T) *publicReconnectFixture {
	t.Helper()
	previousLevel := zerolog.GlobalLevel()
	zerolog.SetGlobalLevel(zerolog.WarnLevel)
	t.Cleanup(func() { zerolog.SetGlobalLevel(previousLevel) })
	database, err := db.Open(filepath.Join(t.TempDir(), "public-reconnect.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := database.Close(); err != nil {
			t.Error(err)
		}
	})
	ctx, cancel := context.WithTimeout(t.Context(), 75*time.Second)
	f := &publicReconnectFixture{ctx: ctx, cancel: cancel, sessions: make(map[*yamux.Session]int),
		gates: make(map[string]*publicReconnectGate), originErrors: make(chan error, 16)}
	f.app = NewApp(&config.Config{ManagementUIDisabled: true, ConfigDir: t.TempDir(),
		ServerTunnelMaxConcurrentStreams: 4, ServerTunnelCapacityAuto: true, TunnelMaxStreamWindowBytes: 2 << 20}, database)
	t.Cleanup(func() { f.close(t) })
	installDeterministicServerResourceController(t, f.app)
	f.app.StartAdaptiveTunnelCapacity(ctx)
	snapshot := f.app.agentStreamCapacity.snapshot()
	if snapshot.Total.Capacity != 4 || snapshot.Public.Capacity != 3 || snapshot.Pooled.Capacity != 2 || snapshot.Control.Capacity != 1 {
		t.Fatalf("unexpected fixed reconnect fixture partition: %+v", snapshot)
	}
	f.app.publicProxyRequests = newRequestCapacityLimiter(8, 8)
	f.app.publicTargetRequests = newKeyedRequestCapacityLimiter(8, 8)
	f.app.publicClientRequests = newKeyedStringRequestCapacityLimiter(8)
	f.row, err = database.CreateAgent(ctx, db.CreateAgentParams{PublicID: "public-reconnect-agent", Name: "Public reconnect agent", TokenHash: hashAgentToken("reconnect-token-1"), Enabled: 1})
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	f.app.RegisterManagementRoutes(mux)
	f.management = httptest.NewUnstartedServer(mux)
	f.management.StartTLS()
	f.ca = base64.StdEncoding.EncodeToString(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: f.management.Certificate().Raw}))
	for id, name := range []string{"lane-loss", "lane-survivor", "primary-progress", "old-generation", "replacement-progress", "current-generation", "after-stale-progress"} {
		gate := &publicReconnectGate{prefix: bytes.Repeat([]byte{byte('a' + id)}, mixedSaturationPrefix), tail: []byte("tail:" + name), release: make(chan struct{}), stopped: make(chan struct{})}
		if name == "primary-progress" || name == "replacement-progress" || name == "after-stale-progress" {
			gate.open()
		}
		f.gates[name] = gate
	}
	f.origin = httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		name := request.URL.Path
		if len(name) > 0 && name[0] == '/' {
			name = name[1:]
		}
		gate := f.gates[name]
		bad := func(message string) {
			select {
			case f.originErrors <- fmt.Errorf("origin %s: %s", name, message):
			default:
				f.originErrorOverflow.Store(true)
			}
			http.Error(w, message, 500)
		}
		if gate == nil {
			bad("unknown request")
			return
		}
		if gate.calls.Add(1) != 1 {
			bad("duplicate request/retry")
			return
		}
		defer close(gate.stopped)
		if request.Method != http.MethodGet || request.ProtoMajor != 1 || request.ContentLength != 0 || request.Host != f.origin.Listener.Addr().String() ||
			request.Header.Get("X-Forwarded-For") != "127.0.0.1" || request.Header.Get("X-Real-Ip") != "127.0.0.1" || request.Header.Get("X-Forwarded-Proto") != "http" {
			bad("HTTP method/framing/trusted forwarding differs")
			return
		}
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("X-Reconnect-Response", name)
		// Make successful completion retire its physical lease rather than
		// retain a net/http idle connection across phases.
		w.Header().Set("Connection", "close")
		if _, err := w.Write(gate.prefix); err != nil {
			return
		}
		w.(http.Flusher).Flush()
		select {
		case <-gate.release:
			_, _ = w.Write(gate.tail)
		case <-request.Context().Done():
		case <-ctx.Done():
		}
	}))
	f.origin.Config.ReadTimeout, f.origin.Config.WriteTimeout = mixedSaturationWait, 45*time.Second
	f.origin.Start()
	f.transport = &http.Transport{Proxy: nil, DisableCompression: true, DisableKeepAlives: true}
	f.client = &http.Client{Transport: f.transport, CheckRedirect: func(*http.Request, []*http.Request) error { return errors.New("fixture redirects forbidden") }}
	return f
}

func (f *publicReconnectFixture) startRuntime(t *testing.T, token string) *publicReconnectRuntime {
	t.Helper()
	return f.startRuntimeForAgent(t, f.row, token)
}

func (f *publicReconnectFixture) startRuntimeForAgent(t *testing.T, row db.Agent, token string) *publicReconnectRuntime {
	t.Helper()
	// Register the immutable DB identity before launching: even a runtime that
	// never publishes a generation must join and cross its cleanup barrier.
	found := false
	for _, id := range f.runtimeAgentIDs {
		found = found || id == row.ID
	}
	if !found {
		f.runtimeAgentIDs = append(f.runtimeAgentIDs, row.ID)
	}
	ctx, cancel := context.WithCancel(f.ctx)
	runtime := &publicReconnectRuntime{cancel: cancel, done: make(chan struct{})}
	f.runtimes = append(f.runtimes, runtime)
	go func() {
		defer close(runtime.done)
		runtime.err = agentruntime.RunContext(ctx, agentruntime.Options{ManagementURL: f.management.URL, PublicID: row.PublicID, Token: token, AllowAnyTarget: true,
			ManagementCAPEMBase64: f.ca, TunnelConnections: 2, TunnelMaxStreamWindowBytes: 2 << 20,
			TunnelMaxConcurrentRequests: tunnel.MaxConcurrentAgentRequestsLimit, TunnelCapacityAdaptive: true})
	}()
	return runtime
}

func (f *publicReconnectFixture) stopRuntime(t *testing.T, runtime *publicReconnectRuntime) {
	t.Helper()
	if runtime.joined {
		return
	}
	runtime.cancel()
	select {
	case <-runtime.done:
		runtime.joined = true
		if runtime.err != nil && !errors.Is(runtime.err, context.Canceled) {
			t.Errorf("real agent returned unexpected terminal error: %v", runtime.err)
		}
	case <-time.After(mixedSaturationWait):
		t.Error("real agent did not naturally join after cancellation")
	}
}

func (f *publicReconnectFixture) awaitGeneration(t *testing.T, previous *AgentConn) *AgentConn {
	t.Helper()
	var current *AgentConn
	mixedSaturationAwait(t, "real authenticated generation establishes control plus two bulk lanes", func() bool {
		current = f.app.AgentHub.connectedByID(f.row.ID)
		return current != nil && current != previous && realAgentAggregateLaneShape(current, 2)
	})
	return current
}

func (f *publicReconnectFixture) startPublic(t *testing.T, agent *AgentConn) {
	t.Helper()
	f.startPublicForAgents(t, [2]*AgentConn{agent, agent})
}

func (f *publicReconnectFixture) startPublicForAgents(t *testing.T, agents [2]*AgentConn) {
	t.Helper()
	listener := publicListenerConfig{ID: 1, Name: "public-reconnect", BindAddress: "127.0.0.1", Protocol: publicListenerProtocolHTTP, Enabled: true}
	snapshot := &publicProxySnapshot{Listeners: map[int64]publicListenerConfig{1: listener}, RoutesByListener: make(map[int64][]publicRouteConfig),
		RouteTargets: make(map[int64]publicRouteTargetConfig), Agents: make(map[int64]publicAgentConfig),
		CacheSettings: defaultPublicCacheSettings()}
	for id, agent := range agents {
		label := fmt.Sprintf("reconnect-fixture-target-%d", id)
		configured, exists := snapshot.Agents[agent.AgentID]
		if !exists {
			configured = publicAgentConfig{ID: agent.AgentID, PublicID: agent.PublicID, Enabled: true, Labels: make(map[string]string)}
		}
		configured.Labels[label] = "yes"
		snapshot.Agents[agent.AgentID] = configured
		f.targetAgentIDs[id] = agent.AgentID
		target := publicRouteTargetConfig{ID: int64(70 + id), RouteID: int64(10 + id), Enabled: true, Weight: 100, TargetType: publicRouteTargetTypeProxy,
			Transport: publicRouteTargetTransportAgent, AgentLoadBalancing: publicRouteTargetLoadBalancingRoundRobin,
			AgentSelector: publicAgentSelectorConfig{MatchLabels: map[string]string{label: "yes"}},
			URL:           f.origin.URL, ParsedURL: mustParseAggregateTestURL(t, f.origin.URL), UpstreamResponseHeaderTimeout: mixedSaturationWait}
		f.targets[id] = target
		snapshot.RouteTargets[target.ID] = target
		snapshot.RoutesByListener[1] = append(snapshot.RoutesByListener[1], publicRouteConfig{ID: target.RouteID, ListenerID: 1, Enabled: true,
			HostPattern: fmt.Sprintf("*.%c.reconnect.test", 'a'+id), PathPrefix: "/", PathSecurityMode: publicRoutePathSecurityModeStrict,
			Action: publicRouteActionForward, TargetLoadBalancing: publicRouteTargetLoadBalancingRoundRobin, Targets: []publicRouteTargetConfig{target}})
	}
	// No retry, cache, WAF, health or shaping rule is configured.
	setPublicSnapshotForTest(t, f.app, snapshot)
	f.app.TargetHealth.reconcile(f.app, snapshot, false)
	if _, err := f.app.startPublicListenerFromSnapshot(listener, snapshot); err != nil {
		t.Fatal(err)
	}
	f.app.proxyMu.Lock()
	f.address = f.app.publicListenerState[1].BoundAddress
	f.app.proxyMu.Unlock()
}

func (f *publicReconnectFixture) startRequest(t *testing.T, name string, target int) *publicReconnectRequest {
	t.Helper()
	ctx, cancel := context.WithTimeout(f.ctx, 45*time.Second)
	item := &publicReconnectRequest{name: name, target: target, host: fmt.Sprintf("%s.%c.reconnect.test", name, 'a'+target), cancel: cancel, ready: make(chan error, 1), done: make(chan struct{})}
	f.requests = append(f.requests, item)
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+f.address+"/"+name, nil)
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	request.Host = item.host
	go func() {
		defer close(item.done)
		response, err := f.client.Do(request)
		if err != nil {
			item.err = err
			item.ready <- err
			return
		}
		defer response.Body.Close()
		item.status = response.StatusCode
		gate := f.gates[name]
		prefix := make([]byte, len(gate.prefix))
		_, err = io.ReadFull(response.Body, prefix)
		if err == nil && (response.StatusCode != 200 || response.ProtoMajor != 1 || response.ContentLength != -1 || response.Header.Get("X-Reconnect-Response") != name || !bytes.Equal(prefix, gate.prefix)) {
			err = errors.New("public status/protocol/headers/flushed prefix differs")
		}
		item.body = prefix
		item.ready <- err
		if err != nil {
			item.err = err
			return
		}
		tail, err := io.ReadAll(io.LimitReader(response.Body, int64(len(gate.tail)+1)))
		item.body = append(item.body, tail...)
		item.err = err
	}()
	select {
	case err := <-item.ready:
		if err != nil {
			t.Fatalf("public %s prefix: %v", name, err)
		}
	case <-time.After(mixedSaturationWait):
		t.Fatalf("public %s did not receive exact flushed prefix", name)
	}
	return item
}

func (f *publicReconnectFixture) assertHeld(t *testing.T, item *publicReconnectRequest) {
	t.Helper()
	select {
	case <-item.done:
		t.Fatalf("public %s terminated before its explicit gate release", item.name)
	default:
	}
}

func (f *publicReconnectFixture) finish(t *testing.T, item *publicReconnectRequest, complete bool) {
	t.Helper()
	mixedSaturationAwaitChannel(t, "public "+item.name+" client joined", item.done)
	gate := f.gates[item.name]
	want := append([]byte(nil), gate.prefix...)
	if complete {
		want = append(want, gate.tail...)
	}
	if item.status != 200 || !bytes.Equal(item.body, want) || (complete && item.err != nil) || (!complete && !errors.Is(item.err, io.ErrUnexpectedEOF)) {
		t.Fatalf("public %s complete=%t status=%d bytes=%d/%d error=%v", item.name, complete, item.status, len(item.body), len(want), item.err)
	}
	mixedSaturationAwaitChannel(t, "origin "+item.name+" handler joined", gate.stopped)
	if gate.calls.Load() != 1 {
		t.Fatalf("origin %s attempts=%d, want 1 with retries disabled", item.name, gate.calls.Load())
	}
}

func (f *publicReconnectFixture) heldOwner(t *testing.T, agent *AgentConn, target int) publicReconnectOwner {
	t.Helper()
	var owner publicReconnectOwner
	f.app.AgentTransports.mu.Lock()
	count := 0
	for key, entry := range f.app.AgentTransports.entries {
		if entry != nil && key.Kind == agentTransportKindRouteTarget && key.RouteTargetID == f.targets[target].ID && entry.agent == agent && entry.inFlight == 1 && !entry.retired && entry.transport != nil {
			owner.entry, owner.lane = entry, key.TunnelLane
			count++
		}
	}
	f.app.AgentTransports.mu.Unlock()
	if count != 1 || owner.lane < 1 || owner.lane > 2 {
		t.Fatalf("target %d has %d live pooled bulk claims", target, count)
	}
	owner.session = agent.tunnelSession(owner.lane)
	if owner.session == nil {
		t.Fatal("pooled key does not name a connected physical lane")
	}
	manager := f.app.agentStreamCapacity
	queueKey := agentStreamCapacityQueueKey(agentStreamCapacityPublicPooled, fmt.Sprintf("route-target:%d", f.targets[target].ID))
	manager.mu.Lock()
	count = 0
	for id, lease := range manager.activeLeases {
		if lease.class == agentStreamCapacityPublicPooled && lease.queueKey == queueKey && lease.sessionKey == agentStreamCapacitySessionKey(agent, owner.session) && lease.state == agentStreamLeaseLive {
			owner.leaseID = id
			count++
		}
	}
	manager.mu.Unlock()
	if count != 1 {
		t.Fatalf("target %d live pooled physical leases=%d", target, count)
	}
	return owner
}

func (f *publicReconnectFixture) hasLease(id uint64) bool {
	manager := f.app.agentStreamCapacity
	manager.mu.Lock()
	defer manager.mu.Unlock()
	_, ok := manager.activeLeases[id]
	return ok
}

func (f *publicReconnectFixture) assertCapacity(t *testing.T, key string, live int) {
	t.Helper()
	snapshot := f.app.agentStreamCapacity.snapshot()
	if snapshot.RegisteredSessions != 1 || len(snapshot.SessionLimits) != 1 || snapshot.SessionLimits[key] != 4 || snapshot.Total.InUse != live || snapshot.Public.InUse != live || snapshot.Pooled.InUse != live ||
		snapshot.ActiveLeases != live || snapshot.TotalBySession[key] != live || snapshot.PublicBySession[key] != live || snapshot.States.Live != live || snapshot.States.Opening != 0 || snapshot.States.Closing != 0 || snapshot.Waiters != 0 {
		t.Fatalf("generation %s physical capacity differs from %d held pooled streams: %+v", key, live, snapshot)
	}
	if err := f.app.agentStreamCapacity.validateInvariants(); err != nil {
		t.Fatal(err)
	}
}

func reconnectRawLanes(agent *AgentConn) map[int]*yamux.Session {
	agent.tunnelMu.RLock()
	defer agent.tunnelMu.RUnlock()
	result := make(map[int]*yamux.Session)
	for lane, session := range agent.tunnels {
		if session != nil {
			result[lane] = session
		}
	}
	return result
}

func reconnectRawHasSession(agent *AgentConn, want *yamux.Session) bool {
	for _, session := range reconnectRawLanes(agent) {
		if session == want {
			return true
		}
	}
	return false
}

func reconnectSessionRetired(session *yamux.Session, lane int) bool {
	if session == nil || !session.IsClosed() {
		return false
	}
	pool := session.AggregateReceiveWindowStats()
	if lane == 0 {
		// Negotiated control deliberately uses legacy per-stream flow control.
		// Its aggregate stats stay zero, including Enabled and Closed. Callers
		// also require raw-slot removal after the server's synchronous Close.
		return pool == (yamux.AggregateReceiveWindowStats{})
	}
	return lane >= 1 && lane <= 2 && pool.Enabled && pool.Closed && pool.TotalUnits == 0 && pool.FreeUnits == 0 && pool.BufferedUnits == 0 && pool.PendingRefund == 0
}

func (f *publicReconnectFixture) remember(agent *AgentConn) {
	found := false
	for _, old := range f.generations {
		if old == agent {
			found = true
		}
	}
	if !found {
		f.generations = append(f.generations, agent)
	}
	for lane, session := range reconnectRawLanes(agent) {
		f.sessions[session] = lane
	}
}

func (f *publicReconnectFixture) noPoolFor(agent *AgentConn) bool {
	pool := f.app.AgentTransports
	pool.mu.Lock()
	defer pool.mu.Unlock()
	for _, entry := range pool.entries {
		if entry.agent == agent {
			return false
		}
	}
	for entry := range pool.draining {
		if entry.agent == agent {
			return false
		}
	}
	return true
}

func (f *publicReconnectFixture) awaitNoStreams(t *testing.T, agent *AgentConn) {
	t.Helper()
	mixedSaturationAwait(t, "successful physical streams and leases retire", func() bool {
		for _, session := range reconnectRawLanes(agent) {
			if !session.IsClosed() && session.NumStreams() != 0 {
				return false
			}
		}
		snapshot := f.app.agentStreamCapacity.snapshot()
		return snapshot.Total.InUse == 0 && snapshot.ActiveLeases == 0 && snapshot.States.Opening == 0 && snapshot.States.Live == 0 && snapshot.States.Closing == 0 && snapshot.Waiters == 0 && agent.ActiveRequests.Load() == 0 && f.app.AgentTransports.inFlightForAgent(agent.AgentID) == 0
	})
}

func (f *publicReconnectFixture) assertOnlyOpenRow(t *testing.T, connectionID int64) {
	t.Helper()
	ctx, cancel := context.WithTimeout(f.ctx, mixedSaturationWait)
	defer cancel()
	var count int
	var id int64
	if err := f.app.DB.QueryRowContext(ctx, "SELECT COUNT(*),COALESCE(MAX(id),0) FROM connections WHERE agent_id=? AND disconnected_at IS NULL", f.row.ID).Scan(&count, &id); err != nil {
		t.Fatal(err)
	}
	if count != 1 || id != connectionID {
		t.Fatalf("open connection count/id=%d/%d want=1/%d", count, id, connectionID)
	}
}

func (f *publicReconnectFixture) awaitDisconnectedRow(t *testing.T, connectionID int64) {
	t.Helper()
	ctx, cancel := context.WithTimeout(f.ctx, mixedSaturationWait)
	defer cancel()
	mixedSaturationAwait(t, "G1 DB connection row records terminal disconnection", func() bool {
		var disconnected bool
		if err := f.app.DB.QueryRowContext(ctx, "SELECT disconnected_at IS NOT NULL FROM connections WHERE id=? AND agent_id=?", connectionID, f.row.ID).Scan(&disconnected); err != nil {
			t.Fatal(err)
		}
		return disconnected
	})
}

func (f *publicReconnectFixture) checkEvents(t *testing.T) {
	t.Helper()
	ctx, cancel := context.WithTimeout(f.ctx, mixedSaturationWait)
	defer cancel()
	mixedSaturationAwait(t, "exact public reconnect event conservation", func() bool {
		if err := f.app.FlushObservabilityRecorder(ctx); err != nil {
			t.Fatal(err)
		}
		var count int
		if err := f.app.DB.QueryRowContext(ctx, "SELECT COUNT(*) FROM proxy_request_events").Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count > len(f.requests) {
			t.Fatalf("reconnect events=%d exceed requests=%d", count, len(f.requests))
		}
		return count == len(f.requests)
	})
	rows, err := f.app.DB.QueryContext(ctx, `SELECT listener_id,route_id,route_target_id,agent_id,status_code,error_kind,method,host,path_prefix,request_bytes,response_bytes,retry_rule_id,retry_count,retry_outcome,retry_error_kind,retry_failed_agent_id,cache_rule_id,cache_status,cache_bytes,waf_rule_id,waf_action FROM proxy_request_events ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	expected := make(map[string]*publicReconnectRequest)
	for _, item := range f.requests {
		expected[item.host] = item
	}
	for rows.Next() {
		var listener, route, target, agent, retry, failed, cache, waf sql.NullInt64
		var status, requestBytes, responseBytes, retries, cacheBytes int64
		var kind, method, host, prefix, outcome, retryKind, cacheStatus, wafAction string
		if err := rows.Scan(&listener, &route, &target, &agent, &status, &kind, &method, &host, &prefix, &requestBytes, &responseBytes, &retry, &retries, &outcome, &retryKind, &failed, &cache, &cacheStatus, &cacheBytes, &waf, &wafAction); err != nil {
			t.Fatal(err)
		}
		item := expected[host]
		if item == nil {
			t.Fatalf("duplicate or unknown public event host %q", host)
		}
		delete(expected, host)
		want := func(id int64) sql.NullInt64 { return sql.NullInt64{Int64: id, Valid: true} }
		// Separate integration with the qualified no-rule observer: classify the
		// actual server-side stream error while preserving committed status/body
		// and all disabled-retry fields. Client unexpected EOF is not this error.
		wantErrorKind := ""
		switch item.name {
		case "lane-loss":
			wantErrorKind = "upstream_response_body_failed"
		case "old-generation":
			wantErrorKind = "agent_disconnected_during_response"
		}
		if listener != want(1) || route != want(f.targets[item.target].RouteID) || target != want(f.targets[item.target].ID) || agent != want(f.targetAgentIDs[item.target]) || status != 200 || kind != wantErrorKind || method != "GET" || prefix != "/" || requestBytes != 0 || responseBytes != int64(len(item.body)) ||
			retry.Valid || retries != 0 || outcome != "" || retryKind != "" || failed.Valid || cache.Valid || cacheStatus != publicCacheStatusBypass || cacheBytes != 0 || waf.Valid || wafAction != "" {
			t.Fatalf("public reconnect event attribution differs for %s: listener/route/target/agent=%v/%v/%v/%v status/error=%d/%s bytes=%d/%d retry=%v/%d/%s/%s cache=%v/%s waf=%v/%s", host, listener, route, target, agent, status, kind, requestBytes, responseBytes, retry, retries, outcome, retryKind, cache, cacheStatus, waf, wafAction)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if len(expected) != 0 {
		t.Fatalf("missing public reconnect events=%d", len(expected))
	}
	for name, gate := range f.gates {
		if gate.calls.Load() != 1 {
			t.Fatalf("origin %s calls=%d", name, gate.calls.Load())
		}
	}
	if f.originErrorOverflow.Load() {
		t.Fatal("origin error channel overflow")
	}
	select {
	case err := <-f.originErrors:
		t.Fatal(err)
	default:
	}
}

func (f *publicReconnectFixture) close(t *testing.T) {
	t.Helper()
	if f.closed {
		return
	}
	f.closed = true
	for _, id := range f.runtimeAgentIDs {
		if current := f.app.AgentHub.connectedByID(id); current != nil {
			f.remember(current)
		}
	}
	for _, gate := range f.gates {
		gate.open()
	}
	for _, item := range f.requests {
		item.cancel()
	}
	for _, item := range f.requests {
		select {
		case <-item.done:
		case <-time.After(mixedSaturationWait):
			t.Errorf("public %s client did not join", item.name)
		}
	}
	if f.transport != nil {
		f.transport.CloseIdleConnections()
	}
	if f.origin != nil {
		f.origin.Close()
	}
	if f.address != "" {
		ctx, cancel := context.WithTimeout(context.Background(), mixedSaturationWait)
		if _, err := f.app.stopPublicListenerRuntime(ctx, 1); err != nil {
			t.Error(err)
		}
		cancel()
	}
	for _, agent := range f.generations {
		f.remember(agent)
	}
	for _, runtime := range f.runtimes {
		f.stopRuntime(t, runtime)
	}
	f.cancel()
	f.app.CloseAgentTransports()
	if f.management != nil {
		f.management.Close()
	}
	for _, id := range f.runtimeAgentIDs {
		unlock := f.app.lockAgentAuth(id)
		unlock()
	}
	recorderCtx, cancelRecorder := context.WithTimeout(context.Background(), mixedSaturationWait)
	if err := f.app.CloseObservabilityRecorder(recorderCtx); err != nil {
		t.Error(err)
	}
	cancelRecorder()
	deadline := time.Now().Add(mixedSaturationWait)
	for !f.terminal() && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if !f.terminal() {
		f.logOwnership(t, "cleanup-terminal-timeout")
		t.Error("reconnect terminal retained streams, registrations, leases, waiters, pooled entries or adaptive resource owners")
	}
	// Raw lane removal precedes DB cleanup while holding the auth lock. Once
	// terminal topology converges, cross that lock again before DB close.
	for _, id := range f.runtimeAgentIDs {
		unlock := f.app.lockAgentAuth(id)
		unlock()
	}
	// A failed terminal predicate must not consume the DB assertions' own
	// timeout and replace their useful state with two expired-context errors.
	ctx, cancel := context.WithTimeout(context.Background(), mixedSaturationWait)
	defer cancel()
	var events int
	if err := f.app.DB.QueryRowContext(ctx, "SELECT COUNT(*) FROM proxy_request_events").Scan(&events); err != nil {
		t.Error(err)
	} else if events != len(f.requests) {
		t.Errorf("terminal public events=%d requests=%d", events, len(f.requests))
	}
	for _, id := range f.runtimeAgentIDs {
		var openConnections int
		if err := f.app.DB.QueryRowContext(ctx, "SELECT COUNT(*) FROM connections WHERE agent_id=? AND disconnected_at IS NULL", id).Scan(&openConnections); err != nil {
			t.Error(err)
		} else if openConnections != 0 {
			t.Errorf("terminal open agent %d connection rows=%d", id, openConnections)
		}
	}
	if err := f.app.agentStreamCapacity.validateInvariants(); err != nil {
		t.Error(err)
	}
	sustainedRecorderDrained(t, f.app)
}

func (f *publicReconnectFixture) terminal() bool {
	snapshot := f.app.agentStreamCapacity.snapshot()
	if snapshot.Total.InUse != 0 || snapshot.Public.InUse != 0 || snapshot.Pooled.InUse != 0 || snapshot.Control.InUse != 0 || snapshot.ActiveLeases != 0 || snapshot.RegisteredSessions != 0 || snapshot.Waiters != 0 || len(snapshot.WaitersByKey) != 0 || len(snapshot.OpeningBySession) != 0 || len(snapshot.PublicOpeningBySession) != 0 ||
		snapshot.States.Opening != 0 || snapshot.States.Live != 0 || snapshot.States.Closing != 0 || len(snapshot.SessionLimits) != 0 || len(snapshot.TotalBySession) != 0 || len(snapshot.PublicBySession) != 0 || f.app.AgentTransports.len() != 0 || len(f.app.publicProxyRequests.slots) != 0 || f.app.publicConnections.inUse() != 0 {
		return false
	}
	for owner := adaptiveResourceOwner(0); owner < adaptiveResourceOwnerCount; owner++ {
		if snapshot.AdaptiveExternalBytesByOwner[owner] != 0 || snapshot.AdaptiveExternalFDsByOwner[owner] != 0 || snapshot.AdaptiveExternalReservationsByOwner[owner] != 0 {
			return false
		}
	}
	for _, id := range f.runtimeAgentIDs {
		if f.app.AgentHub.connectedByID(id) != nil {
			return false
		}
	}
	for _, agent := range f.generations {
		agent.controlGateMu.Lock()
		control := len(agent.controlGate)
		agent.controlGateMu.Unlock()
		agent.streamOpenMu.Lock()
		opening := len(agent.streamOpenGate)
		agent.streamOpenMu.Unlock()
		if control != 0 || opening != 0 {
			return false
		}
		if f.app.AgentHub.connectedByID(agent.AgentID) != nil || len(reconnectRawLanes(agent)) != 0 || agent.ActiveRequests.Load() != 0 || agent.tunnelResourceSnapshot().SessionResourceBytes != 0 {
			return false
		}
	}
	for session, lane := range f.sessions {
		if !reconnectSessionRetired(session, lane) {
			return false
		}
	}
	f.app.publicClientRequests.mu.Lock()
	clients := len(f.app.publicClientRequests.inUse)
	f.app.publicClientRequests.mu.Unlock()
	if clients != 0 {
		return false
	}
	for _, target := range f.targets {
		if target.ID != 0 && mixedSaturationTargetUsage(f.app, target.ID) != 0 {
			return false
		}
	}
	for _, runtime := range f.runtimes {
		if !runtime.joined {
			return false
		}
	}
	return true
}

// Failure-only, read-only diagnostics. Each lock is released before acquiring
// another; this does not close sessions, release owners or alter any predicate.
func (f *publicReconnectFixture) logOwnership(t *testing.T, phase string) {
	t.Helper()
	manager := f.app.agentStreamCapacity
	manager.mu.Lock()
	capacity := manager.snapshotLocked()
	leases := make([]string, 0, len(manager.activeLeases))
	for id, lease := range manager.activeLeases {
		leases = append(leases, fmt.Sprintf("id=%d class=%v state=%v queue=%q session=%q", id, lease.class, lease.state, lease.queueKey, lease.sessionKey))
	}
	manager.mu.Unlock()
	t.Logf("reconnect_ownership phase=%s capacity=%+v", phase, capacity)
	t.Logf("reconnect_ownership phase=%s leases=%v", phase, leases)
	pool := f.app.AgentTransports
	pool.mu.Lock()
	entries := make([]string, 0, len(pool.entries)+len(pool.draining))
	for key, entry := range pool.entries {
		if entry == nil {
			entries = append(entries, fmt.Sprintf("live key=%+v nil-entry", key))
			continue
		}
		entries = append(entries, fmt.Sprintf("live key=%+v agent=%p inFlight=%d retired=%t", key, entry.agent, entry.inFlight, entry.retired))
	}
	for entry := range pool.draining {
		if entry == nil {
			entries = append(entries, "draining nil-entry")
			continue
		}
		entries = append(entries, fmt.Sprintf("draining key=%+v agent=%p inFlight=%d retired=%t", entry.key, entry.agent, entry.inFlight, entry.retired))
	}
	pool.mu.Unlock()
	t.Logf("reconnect_ownership phase=%s transports=%v", phase, entries)
	for _, agent := range f.generations {
		agent.controlGateMu.Lock()
		control := len(agent.controlGate)
		agent.controlGateMu.Unlock()
		agent.streamOpenMu.Lock()
		opening := len(agent.streamOpenGate)
		agent.streamOpenMu.Unlock()
		t.Logf("reconnect_ownership phase=%s agent=%p group=%q db=%d hub=%p active=%d control=%d opening=%d raw=%v resource=%+v", phase, agent, agent.TunnelGroup, agent.ConnectionDBID, f.app.AgentHub.connectedByID(agent.AgentID), agent.ActiveRequests.Load(), control, opening, reconnectRawLanes(agent), agent.tunnelResourceSnapshot())
	}
	for session, lane := range f.sessions {
		t.Logf("reconnect_ownership phase=%s session=%p lane=%d closed=%t streams=%d pool=%+v retired=%t", phase, session, lane, session.IsClosed(), session.NumStreams(), session.AggregateReceiveWindowStats(), reconnectSessionRetired(session, lane))
	}
	t.Logf("reconnect_ownership phase=%s public_connections=%d public_requests=%d", phase, f.app.publicConnections.inUse(), len(f.app.publicProxyRequests.slots))
	f.app.publicClientRequests.mu.Lock()
	clients := len(f.app.publicClientRequests.inUse)
	f.app.publicClientRequests.mu.Unlock()
	for _, target := range f.targets {
		if target.ID != 0 {
			t.Logf("reconnect_ownership phase=%s target=%d requests=%d", phase, target.ID, mixedSaturationTargetUsage(f.app, target.ID))
		}
	}
	t.Logf("reconnect_ownership phase=%s public_clients=%d", phase, clients)
}
