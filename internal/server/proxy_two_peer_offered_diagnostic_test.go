package server

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"
)

const twoPeerOfferedScheduleSHA = "a579a18d7ee6e1e01d85851d7c322c14e738df925e070867ae9cb2b69ef03ce0"

// This is one opt-in descriptive row, not a throughput/latency/fairness gate.
// Each offer owns an absolute timer; no shared jobs queue or completion drives
// another launch. The authenticated AgentConn, not Host/IP, identifies a peer.
func TestRealAgentIndependentPeerOfferedDiagnostic(t *testing.T) {
	if value := os.Getenv("P2PSTREAM_TWO_PEER_OFFERED_DIAGNOSTIC"); value != "1" {
		if value != "" {
			t.Fatalf("P2PSTREAM_TWO_PEER_OFFERED_DIAGNOSTIC must be empty or 1, got %q", value)
		}
		t.Skip("set P2PSTREAM_TWO_PEER_OFFERED_DIAGNOSTIC=1 for the single fixed row")
	}
	row := newTwoPeerOfferedRow()
	var app *App
	var agents [2]*AgentConn
	ok := t.Run("fixed_row", func(t *testing.T) {
		f := newPeerFairnessFixture(t)
		app = f.app
		agents = [2]*AgentConn{f.agent, mixedSaturationAddAgent(t, f)}
		row.app, row.agents = app, agents
		app.publicProxyRequests = newRequestCapacityLimiter(32, 32)
		app.publicTargetRequests = newKeyedRequestCapacityLimiter(16, 16)
		app.publicClientRequests = newKeyedStringRequestCapacityLimiter(32)
		t.Cleanup(func() {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if err := app.CloseObservabilityRecorder(ctx); err != nil {
				row.fault("cleanup", "recorder close: %v", err)
				t.Error(err)
			}
		})
		row.ctx, row.cancel = context.WithCancel(t.Context())
		// Register the cancellation owner before any server/client/row worker.
		t.Cleanup(row.cancel)
		origin := httptest.NewServer(http.HandlerFunc(row.origin))
		t.Cleanup(origin.Close)
		snapshot := &publicProxySnapshot{
			Listeners:        map[int64]publicListenerConfig{1: {ID: 1, Protocol: publicListenerProtocolHTTP, Enabled: true}},
			RoutesByListener: make(map[int64][]publicRouteConfig), RouteTargets: make(map[int64]publicRouteTargetConfig), Agents: make(map[int64]publicAgentConfig),
		}
		for peer, agent := range agents {
			target := f.target
			target.ID, target.RouteID = int64(570+peer), int64(50+peer)
			target.URL, target.ParsedURL = origin.URL, mustParseAggregateTestURL(t, origin.URL)
			target.AgentSelector = publicAgentSelectorConfig{MatchLabels: map[string]string{"offered-peer": fmt.Sprint(peer)}}
			target.AgentLoadBalancing, target.Weight = publicRouteTargetLoadBalancingRoundRobin, 100
			row.targets[peer] = target
			row.keys[peer] = agentStreamCapacitySessionKey(agent, agent.tunnelSession(1))
			row.control[peer], row.bulk[peer] = agent.tunnelSession(0), agent.tunnelSession(1)
			row.identities[peer] = twoPeerOfferedIdentity{Peer: string(rune('a' + peer)), AgentID: agent.AgentID, PublicID: agent.PublicID, ConnectionDBID: agent.ConnectionDBID, ConnectedAt: agent.ConnectedAt.UTC().Format(time.RFC3339Nano), TunnelGroup: agent.TunnelGroup, GenerationPointer: fmt.Sprintf("%p", agent), ControlPointer: fmt.Sprintf("%p", row.control[peer]), BulkPointer: fmt.Sprintf("%p", row.bulk[peer]), SessionKey: row.keys[peer], RouteID: target.RouteID, TargetID: target.ID}
			snapshot.RouteTargets[target.ID] = target
			snapshot.Agents[agent.AgentID] = publicAgentConfig{ID: agent.AgentID, PublicID: agent.PublicID, Enabled: true, Labels: map[string]string{"offered-peer": fmt.Sprint(peer)}}
			snapshot.RoutesByListener[1] = append(snapshot.RoutesByListener[1], publicRouteConfig{ID: target.RouteID, ListenerID: 1, HostPattern: fmt.Sprintf("*.%c.offered.test", 'a'+peer), PathPrefix: "/", Action: publicRouteActionForward, PathSecurityMode: publicRoutePathSecurityModeStrict, TargetLoadBalancing: publicRouteTargetLoadBalancingRoundRobin, Enabled: true, Targets: []publicRouteTargetConfig{target}})
		}
		setPublicSnapshotForTest(t, app, snapshot)
		app.TargetHealth.reconcile(app, snapshot, false)
		realHandler := app.publicProxyHandler(1)
		public := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			record := row.byHost[r.Host]
			if record == nil || record.path != r.URL.Path {
				row.fault("correctness", "unknown wrapper host/path %q/%q", r.Host, r.URL.Path)
				realHandler.ServeHTTP(w, r)
				return
			}
			record.mu.Lock()
			record.serverEntries++
			record.serverEntry = time.Now()
			record.mu.Unlock()
			defer func() { record.mu.Lock(); record.serverReturns++; record.serverReturn = time.Now(); record.mu.Unlock() }()
			// Observe only: forward the original writer and request unchanged.
			realHandler.ServeHTTP(w, r)
		}))
		public.EnableHTTP2 = true
		public.StartTLS()
		t.Cleanup(public.Close)
		for peer := range agents {
			transport := &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, ForceAttemptHTTP2: true, MaxConnsPerHost: 1} // fixture certificate
			row.transports[peer] = transport
			row.clients[peer] = &http.Client{Transport: transport}
			t.Cleanup(transport.CloseIdleConnections)
		}
		// This cleanup runs before public/origin servers or real-runtime cleanup,
		// including on setup failure. It does not close physical peer sessions.
		t.Cleanup(row.cleanup)
		row.capture("initial")
		row.prepare(public.URL)
		row.run()
		row.cleanup()
	})
	if !ok {
		row.fault("correctness", "fixture/subtest or its cleanup failed")
	}
	// The subtest has canceled and joined both existing runtime owners before
	// requiring receive-pool/session promises to disappear. DB is already closed.
	if app != nil {
		joined := twoPeerOfferedAwait(time.Now().Add(5*time.Second), func() bool {
			s := app.agentStreamCapacity.snapshot()
			if s.RegisteredSessions != 0 || s.Total.InUse != 0 || s.ActiveLeases != 0 || s.Waiters != 0 || s.Granted != s.Released || s.AdaptiveExternalBytes != 0 || s.AdaptiveExternalFDs != 0 {
				return false
			}
			for owner := adaptiveResourceOwner(0); owner < adaptiveResourceOwnerCount; owner++ {
				if s.AdaptiveExternalBytesByOwner[owner] != 0 || s.AdaptiveExternalFDsByOwner[owner] != 0 || s.AdaptiveExternalReservationsByOwner[owner] != 0 {
					return false
				}
			}
			for _, agent := range agents {
				if agent == nil || app.AgentHub.connectedByID(agent.AgentID) != nil || agent.ActiveRequests.Load() != 0 || agent.tunnelResourceSnapshot().SessionResourceBytes != 0 {
					return false
				}
			}
			return true
		})
		row.mu.Lock()
		row.postRuntimeDrained = joined
		row.mu.Unlock()
		if !joined {
			row.fault("cleanup", "post-runtime generation/session/external owners did not drain")
		}
		row.capture("post_runtime")
		if err := app.agentStreamCapacity.validateInvariants(); err != nil {
			row.fault("cleanup", "post-runtime invariants: %v", err)
		}
	}
	result := row.report()
	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	if len(encoded) > 256<<10 {
		t.Fatalf("bounded diagnostic exceeded 256KiB: %d bytes", len(encoded))
	}
	t.Logf("two_peer_offered %s", encoded)
	if !result.ScenarioValid || !result.CorrectnessValid || !result.CleanupValid {
		t.Error("fixed offered row invalid; all observed denominators and reasons retained above")
	}
}

type twoPeerOfferedIdentity struct {
	Peer              string `json:"peer"`
	AgentID           int64  `json:"agent_id"`
	PublicID          string `json:"public_id"`
	ConnectionDBID    int64  `json:"connection_db_id"`
	ConnectedAt       string `json:"connected_at_utc"`
	TunnelGroup       string `json:"tunnel_group"`
	GenerationPointer string `json:"generation_pointer"`
	ControlPointer    string `json:"control_pointer"`
	BulkPointer       string `json:"bulk_pointer"`
	SessionKey        string `json:"session_key"`
	RouteID           int64  `json:"route_id"`
	TargetID          int64  `json:"target_id"`
}

type twoPeerOfferedRecord struct {
	mu                                                                                         sync.Mutex
	peer, index                                                                                int
	host, path                                                                                 string
	want                                                                                       []byte
	planned                                                                                    time.Duration
	request                                                                                    *http.Request
	cancel                                                                                     context.CancelFunc
	done, prefixReady                                                                          chan struct{}
	gate                                                                                       chan struct{}
	gateOnce                                                                                   sync.Once
	closeBody                                                                                  func()
	started, header, completed, cancelAt, serverEntry, serverReturn, originEntry, originReturn time.Time
	serverEntries, serverReturns, originEntries, originReturns, terminals                      int
	status, proto                                                                              int
	retryAfter, clientError, originError, category, digest                                     string
	bodyBytes                                                                                  int
	cancelFired, contextCanceled, prefixExact, launched                                        bool
	events                                                                                     []twoPeerOfferedEvent
}

type twoPeerOfferedLease struct {
	ID      uint64 `json:"id"`
	Session string `json:"session_key"`
	Class   string `json:"class"`
	State   string `json:"state"`
}
type twoPeerOfferedPhase struct {
	Phase           string                      `json:"phase"`
	Capacity        agentStreamCapacitySnapshot `json:"capacity"`
	PoolEntries     int                         `json:"pool_entries"`
	PhysicalBulk    [2]int                      `json:"physical_bulk"`
	PhysicalControl [2]int                      `json:"physical_control"`
}
type twoPeerOfferedAction struct {
	PlannedNS int64 `json:"planned_ns"`
	ActualNS  int64 `json:"actual_ns"`
	DelayNS   int64 `json:"delay_ns"`
}
type twoPeerOfferedEvent struct {
	Host             string        `json:"host"`
	PathPrefix       string        `json:"path_prefix"`
	Method           string        `json:"method"`
	Listener         sql.NullInt64 `json:"listener_id"`
	Route            sql.NullInt64 `json:"route_id"`
	Target           sql.NullInt64 `json:"target_id"`
	Agent            sql.NullInt64 `json:"agent_id"`
	Status           int64         `json:"status"`
	Bytes            int64         `json:"response_bytes"`
	RetryCount       int64         `json:"retry_count"`
	Kind             string        `json:"error_kind"`
	RetryOutcome     string        `json:"retry_outcome"`
	RetryKind        string        `json:"retry_error_kind"`
	RetryRule        sql.NullInt64 `json:"retry_rule_id"`
	RetryFailedAgent sql.NullInt64 `json:"retry_failed_agent_id"`
}
type twoPeerOfferedRow struct {
	mu                                                   sync.Mutex
	ctx                                                  context.Context
	cancel                                               context.CancelFunc
	ready, stop                                          chan struct{}
	stopOnce, releaseOnce, cleanupOnce                   sync.Once
	app                                                  *App
	agents                                               [2]*AgentConn
	targets                                              [2]publicRouteTargetConfig
	keys                                                 [2]string
	control, bulk                                        [2]any
	identities                                           [2]twoPeerOfferedIdentity
	clients                                              [2]*http.Client
	transports                                           [2]*http.Transport
	records                                              []*twoPeerOfferedRecord
	byHost, byPath                                       map[string]*twoPeerOfferedRecord
	workerDone                                           []chan struct{}
	t0                                                   time.Time
	actions                                              map[string]twoPeerOfferedAction
	phases                                               []twoPeerOfferedPhase
	holderLeases                                         map[uint64]*agentStreamCapacityLease
	holderIDs                                            []twoPeerOfferedLease
	reasons                                              []string
	scenarioValid, correctnessValid, cleanupValid        bool
	workersJoined, preRuntimeDrained, postRuntimeDrained bool
	eventCount                                           int
	unknownEvents                                        []twoPeerOfferedEvent
}

func newTwoPeerOfferedRow() *twoPeerOfferedRow {
	r := &twoPeerOfferedRow{ready: make(chan struct{}), stop: make(chan struct{}), byHost: make(map[string]*twoPeerOfferedRecord), byPath: make(map[string]*twoPeerOfferedRecord), actions: make(map[string]twoPeerOfferedAction), scenarioValid: true, correctnessValid: true, cleanupValid: true}
	for peer := range 2 {
		for index := -1; index < 12; index++ {
			x := &twoPeerOfferedRecord{peer: peer, index: index, done: make(chan struct{}), prefixReady: make(chan struct{}), gate: make(chan struct{}), category: "not_started_on_abort"}
			if index < 0 {
				x.host, x.path = fmt.Sprintf("holder.%c.offered.test", 'a'+peer), fmt.Sprintf("/peer-%c/holder", 'a'+peer)
				x.want = append(bytes.Repeat([]byte{byte('a' + peer)}, 32768), 'y')
			} else {
				x.host, x.path = fmt.Sprintf("r-%02d.%c.offered.test", index, 'a'+peer), fmt.Sprintf("/peer-%c/r-%02d", 'a'+peer, index)
				x.want = []byte(fmt.Sprintf("peer-%c/r-%02d:", 'a'+peer, index))
				x.want = append(x.want, bytes.Repeat([]byte{'x'}, 256-len(x.want))...)
				x.planned = time.Duration(index) * 50 * time.Millisecond
			}
			r.records = append(r.records, x)
			r.byHost[x.host], r.byPath[x.path] = x, x
		}
	}
	return r
}

func (r *twoPeerOfferedRow) fault(kind, format string, values ...any) {
	r.mu.Lock()
	defer r.mu.Unlock()
	switch kind {
	case "scenario":
		r.scenarioValid = false
	case "cleanup":
		r.cleanupValid = false
	default:
		r.correctnessValid = false
	}
	if len(r.reasons) < 64 {
		r.reasons = append(r.reasons, kind+": "+fmt.Sprintf(format, values...))
	}
}

func (r *twoPeerOfferedRow) abort(format string, values ...any) {
	r.fault("scenario", format, values...)
	r.cancel()
	r.stopOnce.Do(func() { close(r.stop) })
	r.openHolders()
}

func (r *twoPeerOfferedRow) openHolders() {
	r.releaseOnce.Do(func() {
		if !r.t0.IsZero() {
			r.action("release", 750*time.Millisecond)
		}
		for peer := range 2 {
			x := r.holder(peer)
			x.gateOnce.Do(func() { close(x.gate) })
		}
	})
}

func (r *twoPeerOfferedRow) origin(w http.ResponseWriter, request *http.Request) {
	x := r.byPath[request.URL.Path]
	if x == nil {
		r.fault("correctness", "unknown origin path %q", request.URL.Path)
		http.Error(w, "unexpected path", 500)
		return
	}
	x.mu.Lock()
	x.originEntries++
	duplicate := x.originEntries != 1
	x.originEntry = time.Now()
	x.mu.Unlock()
	defer func() { x.mu.Lock(); x.originReturns++; x.originReturn = time.Now(); x.mu.Unlock() }()
	if duplicate {
		r.fault("correctness", "duplicate origin %s", x.path)
		http.Error(w, "duplicate path", 500)
		return
	}
	if request.ProtoMajor != 1 || request.Method != http.MethodGet {
		r.fault("correctness", "origin protocol/method %s/%s", request.Proto, request.Method)
	}
	w.Header().Set("Connection", "close")
	write := func(body []byte) {
		if _, err := w.Write(body); err != nil {
			x.mu.Lock()
			x.originError = err.Error()
			x.mu.Unlock()
		}
	}
	if x.index < 0 {
		write(x.want[:32768])
		w.(http.Flusher).Flush()
		select {
		case <-request.Context().Done():
			return
		case <-r.stop:
			return
		case <-x.gate:
			write(x.want[32768:])
		}
		return
	}
	timer := time.NewTimer(50 * time.Millisecond)
	defer timer.Stop()
	select {
	case <-request.Context().Done():
		return
	case <-r.stop:
		return
	case <-timer.C:
		write(x.want)
	}
}

func (r *twoPeerOfferedRow) prepare(publicURL string) {
	// Allocate every context/request and register every join channel before launch.
	for _, x := range r.records {
		r.workerDone = append(r.workerDone, x.done)
		ctx, cancel := context.WithCancel(r.ctx)
		x.cancel = cancel
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, publicURL+x.path, nil)
		if err != nil {
			r.fault("correctness", "prepare %s: %v", x.path, err)
			continue
		}
		request.Host = x.host
		x.request = request
	}
	for _, x := range r.records {
		x.launched = true
		go r.call(x)
	}
	for peer := range 2 {
		done := make(chan struct{})
		r.workerDone = append(r.workerDone, done)
		go func(peer int) { defer close(done); r.cancelDesignated(peer) }(peer)
	}
	done := make(chan struct{})
	r.workerDone = append(r.workerDone, done)
	go func() { defer close(done); r.releaseHolders() }()
}

func (r *twoPeerOfferedRow) at(offset time.Duration) bool {
	select {
	case <-r.ctx.Done():
		return false
	case <-r.ready:
	}
	timer := time.NewTimer(time.Until(r.t0.Add(offset)))
	defer timer.Stop()
	select {
	case <-r.ctx.Done():
		return false
	case <-timer.C:
		return r.ctx.Err() == nil
	}
}

func (r *twoPeerOfferedRow) call(x *twoPeerOfferedRecord) {
	defer close(x.done)
	var payload []byte
	var callErr error
	defer func() {
		digest := sha256.Sum256(payload)
		x.mu.Lock()
		x.terminals++
		x.completed = time.Now()
		x.bodyBytes, x.digest = len(payload), hex.EncodeToString(digest[:])
		x.contextCanceled = errors.Is(callErr, context.Canceled)
		if callErr != nil {
			x.clientError = callErr.Error()
			if len(x.clientError) > 512 {
				x.clientError = x.clientError[:512]
			}
		}
		switch {
		case x.started.IsZero():
			x.category = "not_started_on_abort"
		case callErr == nil && x.status == 200 && x.proto == 2 && bytes.Equal(payload, x.want):
			x.category = "exact_success"
		case x.index == 2 && x.cancelFired && x.contextCanceled && x.status == 0 && len(payload) == 0 && x.originEntries == 0:
			x.category = "scheduled_cancel"
		default:
			x.category = "unexpected_cancel_or_error"
		}
		x.mu.Unlock()
	}()
	if x.index >= 0 && !r.at(x.planned) {
		return
	}
	if x.request == nil || r.ctx.Err() != nil {
		if x.index < 0 {
			close(x.prefixReady)
		}
		return
	}
	x.mu.Lock()
	x.started = time.Now()
	x.mu.Unlock()
	response, err := r.clients[x.peer].Do(x.request)
	if err != nil {
		callErr = err
		if response != nil && response.Body != nil {
			response.Body.Close()
		}
		if x.index < 0 {
			close(x.prefixReady)
		}
		return
	}
	closeBody := ownedSaturationBodyClose(response.Body)
	x.mu.Lock()
	x.header = time.Now()
	x.status, x.proto, x.retryAfter, x.closeBody = response.StatusCode, response.ProtoMajor, response.Header.Get("Retry-After"), closeBody
	x.mu.Unlock()
	defer closeBody()
	if x.index < 0 {
		prefix := make([]byte, 32768)
		n, err := io.ReadFull(response.Body, prefix)
		payload = append(payload, prefix[:n]...)
		x.mu.Lock()
		x.prefixExact = err == nil && response.StatusCode == 200 && response.ProtoMajor == 2 && bytes.Equal(prefix, x.want[:32768])
		x.mu.Unlock()
		close(x.prefixReady)
		if err != nil {
			callErr = err
			return
		}
		body, err := io.ReadAll(io.LimitReader(response.Body, 2))
		payload = append(payload, body...)
		callErr = err
	} else {
		payload, callErr = io.ReadAll(io.LimitReader(response.Body, 257))
	}
}

func (r *twoPeerOfferedRow) designated(peer int) *twoPeerOfferedRecord { return r.records[peer*13+3] }
func (r *twoPeerOfferedRow) holder(peer int) *twoPeerOfferedRecord     { return r.records[peer*13] }

func (r *twoPeerOfferedRow) action(name string, planned time.Duration) {
	now := time.Now()
	r.mu.Lock()
	r.actions[name] = twoPeerOfferedAction{int64(planned), int64(now.Sub(r.t0)), int64(now.Sub(r.t0.Add(planned)))}
	r.mu.Unlock()
}

func (r *twoPeerOfferedRow) cancelDesignated(peer int) {
	if !r.at(350 * time.Millisecond) {
		return
	}
	r.action(fmt.Sprintf("cancel_%c", 'a'+peer), 350*time.Millisecond)
	x := r.designated(peer)
	x.mu.Lock()
	x.cancelAt, x.cancelFired = time.Now(), true
	entered, origin := x.serverEntries, x.originEntries
	x.mu.Unlock()
	// Cancel at the fixed instant even if the topology is invalid; never wait
	// for entry to make this checkpoint pass.
	x.cancel()
	if entered != 1 || origin != 0 {
		r.abort("cancel peer=%d server_entries=%d origin_entries=%d", peer, entered, origin)
	}
}

func (r *twoPeerOfferedRow) originalHolders() bool {
	manager := r.app.agentStreamCapacity
	manager.mu.Lock()
	defer manager.mu.Unlock()
	for id, lease := range r.holderLeases {
		if manager.activeLeases[id] != lease || lease.state != agentStreamLeaseLive {
			return false
		}
	}
	return len(r.holderLeases) == 2
}

func (r *twoPeerOfferedRow) samePeers() bool {
	for peer, agent := range r.agents {
		if r.app.AgentHub.connectedByID(agent.AgentID) != agent || !realAgentAggregateLaneShape(agent, 1) || agent.tunnelSession(0) != r.control[peer] || agent.tunnelSession(1) != r.bulk[peer] {
			return false
		}
	}
	return true
}

func (r *twoPeerOfferedRow) releaseHolders() {
	if !r.at(750 * time.Millisecond) {
		return
	}
	r.action("release_checkpoint", 750*time.Millisecond)
	valid := true
	for peer := range 2 {
		x := r.designated(peer)
		select {
		case <-x.done:
		default:
			valid = false
		}
		x.mu.Lock()
		valid = valid && x.serverEntries == 1 && x.serverReturns == 1 && x.originEntries == 0 && x.category == "scheduled_cancel"
		x.mu.Unlock()
	}
	valid = valid && r.originalHolders() && r.samePeers()
	r.capture("release_checkpoint")
	if !valid {
		r.abort("750ms cancellation/holder/generation checkpoint absent; no extension or reschedule")
		return
	}
	r.openHolders()
}

func (r *twoPeerOfferedRow) capture(phase string) {
	if r.app == nil {
		return
	}
	s := twoPeerOfferedPhase{Phase: phase, Capacity: r.app.agentStreamCapacity.snapshot(), PoolEntries: r.app.AgentTransports.len()}
	for peer, agent := range r.agents {
		s.PhysicalBulk[peer], s.PhysicalControl[peer] = twoPeerOfferedPhysical(agent, 1), twoPeerOfferedPhysical(agent, 0)
	}
	r.mu.Lock()
	r.phases = append(r.phases, s)
	r.mu.Unlock()
}

func twoPeerOfferedPhysical(agent *AgentConn, lane int) int {
	if agent == nil {
		return -1
	}
	session := agent.tunnelSession(lane)
	if session == nil {
		return -1
	}
	return session.NumStreams()
}

func (r *twoPeerOfferedRow) run() {
	deadline := time.Now().Add(5 * time.Second)
	setup := twoPeerOfferedAwait(deadline, func() bool {
		for peer := range 2 {
			x := r.holder(peer)
			select {
			case <-x.prefixReady:
			default:
				return false
			}
			x.mu.Lock()
			valid := x.prefixExact
			x.mu.Unlock()
			if !valid {
				return false
			}
		}
		s := r.app.agentStreamCapacity.snapshot()
		return s.Total.Capacity == 3 && s.Public.Capacity == 2 && s.Pooled.Capacity == 1 && s.Control.Capacity == 1 && s.RegisteredSessions == 2 && s.Total.InUse == 2 && s.Public.InUse == 2 && s.Control.InUse == 0 && s.ActiveLeases == 2 && s.Waiters == 0 && s.PublicBySession[r.keys[0]] == 1 && s.PublicBySession[r.keys[1]] == 1 && twoPeerOfferedPhysical(r.agents[0], 1) == 1 && twoPeerOfferedPhysical(r.agents[1], 1) == 1 && twoPeerOfferedPhysical(r.agents[0], 0) == 0 && twoPeerOfferedPhysical(r.agents[1], 0) == 0
	})
	if !setup {
		r.abort("5s setup prefix/physical-holder barrier failed")
		return
	}
	manager := r.app.agentStreamCapacity
	manager.mu.Lock()
	r.holderLeases = make(map[uint64]*agentStreamCapacityLease)
	for id, lease := range manager.activeLeases {
		if (lease.sessionKey == r.keys[0] || lease.sessionKey == r.keys[1]) && lease.state == agentStreamLeaseLive && (lease.class == agentStreamCapacityPublicPooled || lease.class == agentStreamCapacityPublicOneShot) {
			r.holderLeases[id] = lease
			r.holderIDs = append(r.holderIDs, twoPeerOfferedLease{id, lease.sessionKey, lease.class.String(), lease.state.String()})
		}
	}
	manager.mu.Unlock()
	if !r.originalHolders() || !r.samePeers() {
		r.abort("exact initial holder/generation identities not established")
		return
	}
	r.capture("held")
	// All 29 row workers exist and all 26 requests/contexts are prepared. T0 is
	// published once; no observed completion can move any absolute launch.
	r.t0 = time.Now().Add(100 * time.Millisecond)
	close(r.ready)
	deadline = r.t0.Add(15 * time.Second)
	for _, done := range r.workerDone {
		if !twoPeerOfferedChannel(deadline, done) {
			r.abort("row workers exceeded T0+15s")
			return
		}
	}
}

func twoPeerOfferedChannel(deadline time.Time, done <-chan struct{}) bool {
	timer := time.NewTimer(time.Until(deadline))
	defer timer.Stop()
	select {
	case <-done:
		return true
	case <-timer.C:
		return false
	}
}

func twoPeerOfferedAwait(deadline time.Time, predicate func() bool) bool {
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	for {
		if predicate() {
			return true
		}
		if !time.Now().Before(deadline) {
			return false
		}
		<-ticker.C
	}
}

func (r *twoPeerOfferedRow) cleanup() {
	r.cleanupOnce.Do(func() {
		deadline := time.Now().Add(12 * time.Second)
		r.cancel()
		r.stopOnce.Do(func() { close(r.stop) })
		r.openHolders()
		for _, x := range r.records {
			if x.cancel != nil {
				x.cancel()
			}
			x.mu.Lock()
			closeBody := x.closeBody
			x.mu.Unlock()
			if closeBody != nil {
				closeBody()
			}
		}
		joined := true
		for _, done := range r.workerDone {
			if !twoPeerOfferedChannel(deadline, done) {
				joined = false
			}
		}
		r.mu.Lock()
		r.workersJoined = joined
		r.mu.Unlock()
		if !joined {
			r.fault("cleanup", "row client/cancel/controller workers did not join within12s")
		}
		handlers := twoPeerOfferedAwait(deadline, func() bool {
			for _, x := range r.records {
				x.mu.Lock()
				idle := x.serverEntries == x.serverReturns && x.originEntries == x.originReturns
				x.mu.Unlock()
				if !idle {
					return false
				}
			}
			return true
		})
		if !handlers {
			r.fault("cleanup", "public/origin handlers did not return within12s")
		}
		for _, transport := range r.transports {
			if transport != nil {
				transport.CloseIdleConnections()
			}
		}
		// Existing transport retirement may cancel no-longer-wanted dials.
		// Physical/ledger drain does not prove every detached net/http dial
		// goroutine joined or exhausted10s. Never close AgentConn/yamux sessions.
		r.app.CloseAgentTransports()
		drained := twoPeerOfferedAwait(deadline, func() bool {
			s := r.app.agentStreamCapacity.snapshot()
			if !sustainedIdle(r.app, r.agents, r.targets) || r.app.AgentTransports.len() != 0 || s.Total.InUse != 0 || s.Public.InUse != 0 || s.Control.InUse != 0 || s.ActiveLeases != 0 || s.Waiters != 0 || s.Granted != s.Released || !r.samePeers() {
				return false
			}
			for peer, agent := range r.agents {
				if twoPeerOfferedPhysical(agent, 0) != 0 || twoPeerOfferedPhysical(agent, 1) != 0 || r.app.TargetHealth.activeRequests(r.targets[peer].ID) != 0 {
					return false
				}
			}
			return r.app.PublicWAF.proxyActiveRequests.Load() == 0
		})
		r.mu.Lock()
		r.preRuntimeDrained = drained
		r.mu.Unlock()
		if !drained {
			r.fault("cleanup", "pre-runtime physical/ledger/request/pool owners did not naturally drain within12s")
		}
		r.capture("pre_runtime_end")
		if err := r.app.agentStreamCapacity.validateInvariants(); err != nil {
			r.fault("cleanup", "pre-runtime invariants: %v", err)
		}
		r.collectEvents(deadline)
	})
}

func (r *twoPeerOfferedRow) collectEvents(deadline time.Time) {
	ctx, cancel := context.WithDeadline(context.Background(), deadline)
	defer cancel()
	want := 0
	for _, x := range r.records {
		x.mu.Lock()
		want += x.serverEntries
		x.mu.Unlock()
	}
	complete := twoPeerOfferedAwait(deadline, func() bool {
		if err := r.app.FlushObservabilityRecorder(ctx); err != nil {
			return false
		}
		var count int
		if err := r.app.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM proxy_request_events`).Scan(&count); err != nil {
			return false
		}
		r.mu.Lock()
		r.eventCount = count
		r.mu.Unlock()
		return count >= want
	})
	if !complete {
		r.fault("cleanup", "terminal event flush/count deadline: observed server entries=%d", want)
	}
	rows, err := r.app.DB.QueryContext(ctx, `SELECT host,path_prefix,method,listener_id,route_id,route_target_id,agent_id,status_code,error_kind,response_bytes,retry_rule_id,retry_count,retry_outcome,retry_error_kind,retry_failed_agent_id FROM proxy_request_events ORDER BY id LIMIT 128`)
	if err != nil {
		r.fault("correctness", "event query: %v", err)
		return
	}
	defer rows.Close()
	for rows.Next() {
		var event twoPeerOfferedEvent
		if err := rows.Scan(&event.Host, &event.PathPrefix, &event.Method, &event.Listener, &event.Route, &event.Target, &event.Agent, &event.Status, &event.Kind, &event.Bytes, &event.RetryRule, &event.RetryCount, &event.RetryOutcome, &event.RetryKind, &event.RetryFailedAgent); err != nil {
			r.fault("correctness", "event scan: %v", err)
			return
		}
		x := r.byHost[event.Host]
		if x == nil {
			r.mu.Lock()
			r.unknownEvents = append(r.unknownEvents, event)
			r.mu.Unlock()
			r.fault("correctness", "unknown event host %q", event.Host)
			continue
		}
		x.mu.Lock()
		x.events = append(x.events, event)
		if x.status == 503 && x.retryAfter == "1" && x.originEntries == 0 && twoPeerOfferedAdmissionKind(event.Kind) {
			x.category = "recognized_admission_rejection"
		}
		x.mu.Unlock()
	}
	if err := rows.Err(); err != nil {
		r.fault("correctness", "event iteration: %v", err)
	}
	r.mu.Lock()
	count := r.eventCount
	r.mu.Unlock()
	if count != want {
		r.fault("correctness", "events=%d observed server entries=%d (event rows retained up to128)", count, want)
	}
	recorder := r.app.observabilityRecorder
	if recorder == nil || recorder.pendingEvents.Load() != 0 || recorder.droppedEvents.Load() != 0 || len(recorder.events) != 0 {
		r.fault("cleanup", "recorder retained/dropped events")
	}
}

func twoPeerOfferedAdmissionKind(kind string) bool {
	switch kind {
	case "public_request_capacity", "public_client_request_capacity", "route_target_capacity", "agent_capacity", "agent_resource_pressure", "agent_server_capacity", "agent_server_pooled_capacity":
		return true
	}
	return false
}

type twoPeerOfferedRaw struct {
	Peer                 int                   `json:"peer"`
	Index                int                   `json:"index"`
	Setup                bool                  `json:"setup"`
	Host                 string                `json:"host"`
	Path                 string                `json:"path"`
	ClientStarted        bool                  `json:"client_started"`
	PlannedNS            *int64                `json:"planned_launch_ns"`
	Times                map[string]*int64     `json:"observed_offsets_ns"`
	EnteredBeforeRelease bool                  `json:"server_entered_before_actual_release"`
	Category             string                `json:"terminal_class"`
	Status               int                   `json:"status"`
	Protocol             int                   `json:"public_protocol_major"`
	RetryAfter           string                `json:"retry_after"`
	Error                string                `json:"error"`
	OriginError          string                `json:"origin_error"`
	ContextCanceled      bool                  `json:"context_canceled"`
	Bytes                int                   `json:"body_bytes"`
	SHA256               string                `json:"body_sha256"`
	ExpectedBytes        int                   `json:"expected_bytes"`
	ExpectedSHA256       string                `json:"expected_sha256"`
	BodyCaptureLimit     int                   `json:"body_capture_limit_bytes"`
	BodyLimitReached     bool                  `json:"body_capture_limit_reached"`
	Terminals            int                   `json:"client_terminal_count"`
	ServerEntries        int                   `json:"server_entries"`
	ServerReturns        int                   `json:"server_returns"`
	OriginEntries        int                   `json:"origin_entries"`
	OriginReturns        int                   `json:"origin_returns"`
	Events               []twoPeerOfferedEvent `json:"events"`
}
type twoPeerOfferedCounts struct {
	Requested         int `json:"requested"`
	Started           int `json:"started"`
	ServerEntered     int `json:"server_entered"`
	OriginAdmitted    int `json:"origin_admitted"`
	ExactCompleted    int `json:"exact_completed"`
	Rejected          int `json:"rejected"`
	ScheduledCanceled int `json:"scheduled_canceled"`
	OtherError        int `json:"other_error"`
	NotStarted        int `json:"not_started"`
	Events            int `json:"events"`
}
type twoPeerOfferedSummary struct {
	Setup    twoPeerOfferedCounts `json:"setup"`
	Offered  twoPeerOfferedCounts `json:"offered"`
	Combined twoPeerOfferedCounts `json:"combined"`
}
type twoPeerOfferedDistribution struct {
	Count   int     `json:"count"`
	Samples []int64 `json:"sorted_samples_ns"`
	Min     *int64  `json:"min_ns"`
	Median  *int64  `json:"median_ns"`
	Max     *int64  `json:"max_ns"`
}
type twoPeerOfferedReport struct {
	ScheduleSHA256     string                                   `json:"schedule_sha256"`
	T0UTC              string                                   `json:"t0_utc"`
	ScenarioValid      bool                                     `json:"scenario_valid"`
	CorrectnessValid   bool                                     `json:"correctness_valid"`
	CleanupValid       bool                                     `json:"cleanup_valid"`
	Reasons            []string                                 `json:"reasons"`
	Peers              [2]twoPeerOfferedIdentity                `json:"authenticated_peers"`
	Records            []twoPeerOfferedRaw                      `json:"requests"`
	Counts             [2]twoPeerOfferedSummary                 `json:"per_peer_counts"`
	Samples            [2]map[string]twoPeerOfferedDistribution `json:"offered_descriptive_samples"`
	Actions            map[string]twoPeerOfferedAction          `json:"actions"`
	Phases             []twoPeerOfferedPhase                    `json:"phase_snapshots"`
	HolderLeases       []twoPeerOfferedLease                    `json:"setup_holder_leases"`
	EventCount         int                                      `json:"event_count"`
	UnknownEvents      []twoPeerOfferedEvent                    `json:"unknown_events"`
	WorkersJoined      bool                                     `json:"all_row_workers_joined"`
	PreRuntimeDrained  bool                                     `json:"pre_runtime_owners_drained"`
	PostRuntimeDrained bool                                     `json:"post_runtime_session_owners_drained"`
	Interpretation     string                                   `json:"interpretation"`
}

func (r *twoPeerOfferedRow) report() twoPeerOfferedReport {
	result := twoPeerOfferedReport{ScheduleSHA256: twoPeerOfferedScheduleSHA, Peers: r.identities, Interpretation: "One finite descriptive row. Setup excluded from offered samples. Origin entry is admission; manager grants may include detached canceled dials. Existing pool retirement may cancel unwanted dials; physical/ledger drain is not a net/http dial-goroutine join proof. Pre-origin interval is not pure scheduler wait. Origin service includes intentional50ms. Shared Go process generators expose launch delay. No latency, equal-throughput, tenant-entitlement, RSS or performance threshold."}
	r.mu.Lock()
	t0 := r.t0
	actions := make(map[string]twoPeerOfferedAction)
	for k, v := range r.actions {
		actions[k] = v
	}
	result.Actions = actions
	result.Phases = append([]twoPeerOfferedPhase(nil), r.phases...)
	result.HolderLeases = append([]twoPeerOfferedLease(nil), r.holderIDs...)
	result.WorkersJoined, result.PreRuntimeDrained, result.PostRuntimeDrained, result.EventCount = r.workersJoined, r.preRuntimeDrained, r.postRuntimeDrained, r.eventCount
	result.UnknownEvents = append([]twoPeerOfferedEvent(nil), r.unknownEvents...)
	r.mu.Unlock()
	if !t0.IsZero() {
		result.T0UTC = t0.UTC().Format(time.RFC3339Nano)
	}
	offset := func(t time.Time) *int64 {
		if t.IsZero() || t0.IsZero() {
			return nil
		}
		n := int64(t.Sub(t0))
		return &n
	}
	samples := [2]map[string][]int64{{}, {}}
	for _, x := range r.records {
		x.mu.Lock()
		// If fixture setup failed before launch, explicitly finalize its known
		// non-started records. Never manufacture a terminal for a launched worker.
		if !x.launched && x.terminals == 0 {
			x.terminals, x.category = 1, "not_started_on_abort"
		}
		wantHash := sha256.Sum256(x.want)
		raw := twoPeerOfferedRaw{Peer: x.peer, Index: x.index, Setup: x.index < 0, Host: x.host, Path: x.path, ClientStarted: !x.started.IsZero(), Times: map[string]*int64{"actual_launch": offset(x.started), "actual_cancel": offset(x.cancelAt), "first_header": offset(x.header), "client_complete": offset(x.completed), "server_entry": offset(x.serverEntry), "server_return": offset(x.serverReturn), "origin_entry": offset(x.originEntry), "origin_finish": offset(x.originReturn)}, Category: x.category, Status: x.status, Protocol: x.proto, RetryAfter: x.retryAfter, Error: x.clientError, OriginError: x.originError, ContextCanceled: x.contextCanceled, Bytes: x.bodyBytes, SHA256: x.digest, ExpectedBytes: len(x.want), ExpectedSHA256: hex.EncodeToString(wantHash[:]), Terminals: x.terminals, ServerEntries: x.serverEntries, ServerReturns: x.serverReturns, OriginEntries: x.originEntries, OriginReturns: x.originReturns, Events: append([]twoPeerOfferedEvent(nil), x.events...)}
		if release, ok := actions["release"]; ok && !x.serverEntry.IsZero() && !t0.IsZero() {
			raw.EnteredBeforeRelease = x.serverEntry.Sub(t0) <= time.Duration(release.ActualNS)
		}
		raw.BodyCaptureLimit = len(x.want) + 1
		raw.BodyLimitReached = x.bodyBytes >= raw.BodyCaptureLimit
		if x.index >= 0 {
			n := int64(x.planned)
			raw.PlannedNS = &n
			if !t0.IsZero() && !x.started.IsZero() {
				samples[x.peer]["launch_delay"] = append(samples[x.peer]["launch_delay"], int64(x.started.Sub(t0.Add(x.planned))))
			}
			if !t0.IsZero() && !x.started.IsZero() && !x.completed.IsZero() {
				samples[x.peer]["scheduled_to_terminal_all_started"] = append(samples[x.peer]["scheduled_to_terminal_all_started"], int64(x.completed.Sub(t0.Add(x.planned))))
				samples[x.peer]["actual_start_to_terminal_all_started"] = append(samples[x.peer]["actual_start_to_terminal_all_started"], int64(x.completed.Sub(x.started)))
			}
			if x.category == "exact_success" && !t0.IsZero() {
				samples[x.peer]["scheduled_to_exact_completion"] = append(samples[x.peer]["scheduled_to_exact_completion"], int64(x.completed.Sub(t0.Add(x.planned))))
				samples[x.peer]["actual_start_to_exact_completion"] = append(samples[x.peer]["actual_start_to_exact_completion"], int64(x.completed.Sub(x.started)))
			}
			if !x.originEntry.IsZero() && !x.serverEntry.IsZero() {
				samples[x.peer]["pre_origin_interval_admitted"] = append(samples[x.peer]["pre_origin_interval_admitted"], int64(x.originEntry.Sub(x.serverEntry)))
			}
			if !x.originEntry.IsZero() && !x.originReturn.IsZero() {
				samples[x.peer]["origin_service_including_intentional_50ms"] = append(samples[x.peer]["origin_service_including_intentional_50ms"], int64(x.originReturn.Sub(x.originEntry)))
			}
		}
		x.mu.Unlock()
		result.Records = append(result.Records, raw)
		if raw.Terminals != 1 {
			r.fault("correctness", "%s terminal count=%d", raw.Host, raw.Terminals)
		}
		if raw.ServerEntries > 1 || raw.ServerReturns != raw.ServerEntries || raw.OriginEntries > 1 || raw.OriginReturns != raw.OriginEntries || len(raw.Events) != raw.ServerEntries {
			r.fault("correctness", "%s duplicate/missing server/origin/event ownership", raw.Host)
		}
		if raw.Category != "exact_success" && raw.Category != "scheduled_cancel" {
			r.fault("correctness", "%s terminal=%s status=%d", raw.Host, raw.Category, raw.Status)
		}
		if raw.Category == "exact_success" && (raw.OriginEntries != 1 || raw.OriginError != "") {
			r.fault("correctness", "%s success origin ownership/error mismatch", raw.Host)
		}
		if raw.Category == "scheduled_cancel" && (raw.Index != 2 || !raw.ContextCanceled || raw.Status != 0 || raw.Bytes != 0 || raw.OriginEntries != 0) {
			r.fault("correctness", "%s invalid designated pre-origin cancel", raw.Host)
		}
		for _, event := range raw.Events {
			identity := r.identities[raw.Peer]
			validID := event.Listener == (sql.NullInt64{Int64: 1, Valid: true}) && event.Route == (sql.NullInt64{Int64: identity.RouteID, Valid: true}) && event.Target == (sql.NullInt64{Int64: identity.TargetID, Valid: true}) && event.Agent == (sql.NullInt64{Int64: identity.AgentID, Valid: true})
			if !validID || event.PathPrefix != "/" || event.Method != "GET" || event.RetryRule.Valid || event.RetryCount != 0 || event.RetryOutcome != "" {
				r.fault("correctness", "%s event identity/retry mismatch", raw.Host)
			}
			if raw.Category == "exact_success" {
				if event.Status != 200 || event.Kind != "" || event.Bytes != int64(raw.ExpectedBytes) || event.RetryKind != "" || event.RetryFailedAgent.Valid {
					r.fault("correctness", "%s success event mismatch", raw.Host)
				}
			} else if raw.Category == "scheduled_cancel" {
				// First-attempt diagnostics survive even with no policy retry.
				// ErrorHandler separately records the terminal client cancellation.
				if event.Status != 504 || event.Kind != "client_cancelled" || event.Bytes != 0 || event.RetryKind != "agent_proxy_failed" || event.RetryFailedAgent != event.Agent {
					r.fault("correctness", "%s scheduled cancel event mismatch", raw.Host)
				}
			}
		}
		add := func(c *twoPeerOfferedCounts) {
			if err := twoPeerOfferedCount(c, raw); err != nil {
				r.fault("correctness", "%s denominator: %v", raw.Host, err)
			}
		}
		summary := &result.Counts[raw.Peer]
		add(&summary.Combined)
		if raw.Setup {
			add(&summary.Setup)
		} else {
			add(&summary.Offered)
		}
	}
	for peer := range 2 {
		c := result.Counts[peer].Combined
		if c != (twoPeerOfferedCounts{Requested: 13, Started: 13, ServerEntered: 13, OriginAdmitted: 12, ExactCompleted: 12, ScheduledCanceled: 1, Events: 13}) {
			r.fault("correctness", "peer%d denominators=%+v", peer, c)
		}
		if c.Requested != c.ExactCompleted+c.Rejected+c.ScheduledCanceled+c.OtherError+c.NotStarted || c.Started != c.Requested-c.NotStarted {
			r.fault("correctness", "peer%d denominator conservation", peer)
		}
		result.Samples[peer] = make(map[string]twoPeerOfferedDistribution)
		for key, values := range samples[peer] {
			sort.Slice(values, func(i, j int) bool { return values[i] < values[j] })
			d := twoPeerOfferedDistribution{Count: len(values), Samples: values}
			if len(values) > 0 {
				lo, hi := values[0], values[len(values)-1]
				median := values[len(values)/2]
				if len(values)%2 == 0 {
					median = values[len(values)/2-1] + (median-values[len(values)/2-1])/2
				}
				d.Min, d.Median, d.Max = &lo, &median, &hi
			}
			result.Samples[peer][key] = d
		}
	}
	if !result.WorkersJoined || !result.PreRuntimeDrained || !result.PostRuntimeDrained {
		r.fault("cleanup", "not all natural cleanup boundaries passed")
	}
	r.mu.Lock()
	result.ScenarioValid, result.CorrectnessValid, result.CleanupValid = r.scenarioValid, r.correctnessValid, r.cleanupValid
	result.Reasons = append([]string(nil), r.reasons...)
	r.mu.Unlock()
	return result
}

func twoPeerOfferedCount(c *twoPeerOfferedCounts, raw twoPeerOfferedRaw) error {
	// Count bad rows too, then reject their integrity; never filter them out.
	c.Requested++
	if raw.ClientStarted {
		c.Started++
	}
	c.ServerEntered += raw.ServerEntries
	c.OriginAdmitted += raw.OriginEntries
	c.Events += len(raw.Events)
	switch raw.Category {
	case "exact_success":
		c.ExactCompleted++
	case "scheduled_cancel":
		c.ScheduledCanceled++
	case "recognized_admission_rejection":
		c.Rejected++
	case "not_started_on_abort":
		c.NotStarted++
	default:
		c.OtherError++
	}
	if raw.Terminals != 1 {
		return fmt.Errorf("terminal count=%d, want1", raw.Terminals)
	}
	if raw.ClientStarted == (raw.Category == "not_started_on_abort") {
		return fmt.Errorf("start/category mismatch")
	}
	if raw.ServerEntries > 1 || raw.OriginEntries > 1 || len(raw.Events) > 1 {
		return fmt.Errorf("duplicate server/origin/event")
	}
	if raw.ServerEntries != raw.ServerReturns || raw.OriginEntries != raw.OriginReturns || len(raw.Events) != raw.ServerEntries {
		return fmt.Errorf("missing handler return/event")
	}
	switch raw.Category {
	case "exact_success", "scheduled_cancel", "recognized_admission_rejection", "not_started_on_abort", "unexpected_cancel_or_error":
	default:
		return fmt.Errorf("unknown terminal class %q", raw.Category)
	}
	return nil
}

// Pure bounded denominator/schedule contracts run without launching peers.
func TestTwoPeerOfferedFixedRecords(t *testing.T) {
	r := newTwoPeerOfferedRow()
	if len(r.records) != 26 || len(r.byHost) != 26 || len(r.byPath) != 26 {
		t.Fatal("immutable record table must have26 unique entries")
	}
	for peer := range 2 {
		if r.holder(peer).index != -1 || len(r.holder(peer).want) != 32769 || r.designated(peer).index != 2 {
			t.Fatal("holder/cancel mapping")
		}
		for index := range 12 {
			x := r.records[peer*13+index+1]
			if x.planned != time.Duration(index)*50*time.Millisecond || x.index != index || len(x.want) != 256 || !strings.HasPrefix(string(x.want), fmt.Sprintf("peer-%c/r-%02d:", 'a'+peer, index)) {
				t.Fatalf("schedule/body peer%d index%d", peer, index)
			}
		}
	}
}

func TestTwoPeerOfferedDenominatorContracts(t *testing.T) {
	var count twoPeerOfferedCounts
	for _, category := range []string{"exact_success", "scheduled_cancel", "recognized_admission_rejection", "unexpected_cancel_or_error", "not_started_on_abort"} {
		if err := twoPeerOfferedCount(&count, twoPeerOfferedRaw{Terminals: 1, Category: category, ClientStarted: category != "not_started_on_abort"}); err != nil {
			t.Fatal(err)
		}
	}
	if count.Requested != 5 || count.Started != 4 || count.ExactCompleted != 1 || count.ScheduledCanceled != 1 || count.Rejected != 1 || count.OtherError != 1 || count.NotStarted != 1 {
		t.Fatalf("cancellation/error/abort denominator lost: %+v", count)
	}
	for _, bad := range []twoPeerOfferedRaw{
		{Terminals: 0, Category: "exact_success", ClientStarted: true},
		{Terminals: 2, Category: "scheduled_cancel", ClientStarted: true},
		{Terminals: 1, Category: "not_started_on_abort", ClientStarted: true},
		{Terminals: 1, Category: "exact_success", ClientStarted: true, ServerEntries: 2},
		{Terminals: 1, Category: "exact_success", ClientStarted: true, OriginEntries: 2},
		{Terminals: 1, Category: "exact_success", ClientStarted: true, Events: make([]twoPeerOfferedEvent, 2)},
		{Terminals: 1, Category: "exact_success", ClientStarted: true, ServerEntries: 1, ServerReturns: 1},
		{Terminals: 1, Category: "exact_success", ClientStarted: true, OriginEntries: 1},
		{Terminals: 1, Category: "unknown", ClientStarted: true},
	} {
		var observed twoPeerOfferedCounts
		if twoPeerOfferedCount(&observed, bad) == nil || observed.Requested != 1 {
			t.Fatalf("bad record escaped or disappeared: %+v %+v", bad, observed)
		}
	}
}
