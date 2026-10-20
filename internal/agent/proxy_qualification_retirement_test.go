//go:build linux

package agent

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hashicorp/yamux"
	"p2pstream/internal/tunnel"
)

// This is an externally quiescent diagnostic, not an atomic production drain.
// All state uses registry.mu. Close, capacity sampling, gate waits and HTTP I/O
// run outside that mutex. Receipts contain scalars, never session references.
type externalRetirementLane struct {
	Aggregate bool                              `json:"aggregate"`
	Lane      int                               `json:"lane"`
	Sequence  uint64                            `json:"sequence"`
	Identity  string                            `json:"identity"`
	Streams   int                               `json:"streams"`
	Closed    bool                              `json:"closed"`
	Pool      yamux.AggregateReceiveWindowStats `json:"pool"`
}
type externalRetirementRequest struct {
	Token            string `json:"token"`
	Action           string `json:"action"`
	Lane             int    `json:"lane"`
	ControlSequence  uint64 `json:"control_sequence"`
	ControlIdentity  string `json:"control_identity"`
	Sequence         uint64 `json:"sequence"`
	Identity         string `json:"identity"`
	ServerRawSlotNil bool   `json:"server_raw_slot_nil"`
}
type externalRetirementEdge struct {
	State    string `json:"state"`
	UnixNano int64  `json:"unix_nano"`
}
type externalRetirementReceipt struct {
	CloseWorkerJoined              bool                        `json:"close_worker_joined"`
	State                          string                      `json:"state"`
	Error                          string                      `json:"error,omitempty"`
	Lanes                          []externalRetirementLane    `json:"lanes"`
	Before                         []externalRetirementLane    `json:"before,omitempty"`
	CapacityBefore                 agentTunnelCapacitySnapshot `json:"capacity_before"`
	CapacityReleased               agentTunnelCapacitySnapshot `json:"capacity_released"`
	CapacityReplacement            agentTunnelCapacitySnapshot `json:"capacity_replacement"`
	CloseReturned                  bool                        `json:"close_returned"`
	CloseReturnedUnixNano          int64                       `json:"close_returned_unix_nano"`
	DisconnectAfterReleaseUnixNano int64                       `json:"disconnect_after_release_unix_nano"`
	DisconnectAfterRelease         bool                        `json:"disconnect_after_release"`
	OldPointerRetained             bool                        `json:"old_pointer_retained"`
	Edges                          []externalRetirementEdge    `json:"edges"`
}
type externalQualificationRetirement struct {
	ctx                                                   context.Context
	registry                                              *externalQualificationSessionRegistry
	capacity                                              func() agentTunnelCapacitySnapshot
	token                                                 string
	state                                                 string
	failure                                               string
	before                                                []externalRetirementLane
	beforeCapacity, releasedCapacity, replacementCapacity agentTunnelCapacitySnapshot
	replacement                                           externalRetirementLane
	old                                                   *yamux.Session
	closeDone, disconnectDone, resume                     chan struct{}
	closeReturned, disconnected, gateOpen                 bool
	closeReturnedAt, disconnectedAt                       int64
	started, resumed                                      time.Time
	edges                                                 []externalRetirementEdge
	// Fixed defaults in the endpoint; controllable short deadlines/Close in tests.
	closeSession                          func(*yamux.Session) error
	closeWait, pauseWait, replacementWait time.Duration
}

func newExternalQualificationRetirement(ctx context.Context, registry *externalQualificationSessionRegistry, capacity func() agentTunnelCapacitySnapshot, token string) *externalQualificationRetirement {
	return &externalQualificationRetirement{ctx: ctx, registry: registry, capacity: capacity, token: token, state: "ready",
		closeSession: func(s *yamux.Session) error { return s.Close() }, closeWait: 5 * time.Second, pauseWait: 10 * time.Second, replacementWait: 10 * time.Second}
}
func externalRetirementTokenValid(token string) bool {
	_, err := hex.DecodeString(token)
	return len(token) == 32 && err == nil
}
func (c *externalQualificationRetirement) edgeLocked(state string) {
	c.state = state
	// One operation has at most eight state edges, including failure.
	c.edges = append(c.edges, externalRetirementEdge{state, time.Now().UnixNano()})
}
func (c *externalQualificationRetirement) openGateLocked() {
	if c.resume != nil && !c.gateOpen {
		close(c.resume)
		c.gateOpen = true
	}
}
func (c *externalQualificationRetirement) failLocked(message string) {
	if c.state != "failed" {
		c.failure = message
		c.edgeLocked("failed")
	}
	c.openGateLocked()
	// The worker/observer own their own references while finishing. A failed
	// controller must not keep slabs reachable throughout the later idle window.
	c.old = nil
}
func (c *externalQualificationRetirement) lanesLocked() []externalRetirementLane {
	result := make([]externalRetirementLane, 0, len(c.registry.sessions))
	for lane := 0; lane < tunnel.MaxTunnelLaneSlots; lane++ {
		if entry, ok := c.registry.sessions[lane]; ok {
			result = append(result, externalRetirementLane{Aggregate: entry.aggregate, Lane: lane, Sequence: entry.sequence, Identity: fmt.Sprintf("%p", entry.session), Streams: entry.session.NumStreams(), Closed: entry.session.IsClosed(), Pool: entry.session.AggregateReceiveWindowStats()})
		}
	}
	return result
}
func externalRetirementIdle(lanes []externalRetirementLane, count int) bool {
	if len(lanes) != count {
		return false
	}
	for _, lane := range lanes {
		if lane.Streams != 0 || lane.Closed {
			return false
		}
		if lane.Lane > 0 && (!lane.Aggregate || !lane.Pool.Enabled || lane.Pool.Closed || lane.Pool.ReceiveLimit == 0 || lane.Pool.ReceiveAvailable != lane.Pool.ReceiveLimit || lane.Pool.PendingRefund != 0 || lane.Pool.BufferedUnits != 0 || lane.Pool.TotalUnits != lane.Pool.FreeUnits) {
			return false
		}
	}
	return true
}
func (c *externalQualificationRetirement) survivorsLocked(lanes []externalRetirementLane) bool {
	for _, before := range c.before {
		if before.Lane == 1 {
			continue
		}
		found := false
		for _, after := range lanes {
			if after.Lane == before.Lane {
				found = after == before
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}
func (c *externalQualificationRetirement) advanceReleasedLocked() {
	if c.state == "closing" && c.closeReturned && c.disconnected {
		select {
		case <-c.closeDone:
		default:
			return
		}
		lanes := c.lanesLocked()
		if len(lanes) != 4 || !externalRetirementIdle(lanes, 4) || !c.survivorsLocked(lanes) || c.releasedCapacity.InUse != 0 || c.releasedCapacity.ControlInUse != 0 {
			c.failLocked("release boundary changed survivors or retained stream ownership")
			return
		}
		old := c.before[1].Pool.ReceiveLimit
		pool := int64(yamux.AggregateReceiveWindowMemory(old))
		initial := int64(yamux.AggregateReceiveWindowMemory(tunnel.DefaultAggregateFlowControlConfig().InitialSessionWindow))
		total, err := tunnel.AggregateSessionResourceBytes(tunnel.DefaultAggregateFlowControlConfig().InitialSessionWindow, tunnel.AggregateAcceptBacklogStreams+tunnel.AggregateRejectedStreamLimit)
		metadata := total - initial
		b, a := c.beforeCapacity, c.releasedCapacity
		if err != nil || b.AggregateCurrentPoolBytes-a.AggregateCurrentPoolBytes != pool || b.AggregateInitialPoolBytes-a.AggregateInitialPoolBytes != initial || b.AggregateGrowthPoolBytes-a.AggregateGrowthPoolBytes != pool-initial || b.AggregateSessionMetadataBytes-a.AggregateSessionMetadataBytes != metadata || b.SessionResourceBytes-a.SessionResourceBytes != pool+metadata || b.ReceiveWindowBytes != a.ReceiveWindowBytes {
			c.failLocked("old agent reservation subtraction differs")
			return
		}
		c.edgeLocked("released_paused")
	}
}
func (c *externalQualificationRetirement) observe(observation tunnelSessionObservation) {
	// Production invokes disconnect only after handler join, Close and BOTH
	// initial/growth releases. Sampling here therefore observes that boundary.
	capacity := agentTunnelCapacitySnapshot{}
	if c.token != "" {
		capacity = c.capacity()
	}
	c.registry.mu.Lock()
	registered, present := c.registry.sessions[observation.Lane]
	disconnectedCurrent := !observation.Connected && present && registered.session == observation.Session
	oldMatch := disconnectedCurrent && !observation.Connected && observation.Lane == 1 && observation.Session == c.old && c.state == "closing" && !c.disconnected
	c.registry.observeLocked(observation)
	if oldMatch {
		c.releasedCapacity = capacity
		c.disconnectedAt = time.Now().UnixNano()
		c.disconnected = true
		close(c.disconnectDone)
		c.advanceReleasedLocked()
		gate := c.resume
		deadline := c.started.Add(c.pauseWait)
		c.registry.mu.Unlock()
		timer := time.NewTimer(time.Until(deadline))
		defer timer.Stop()
		select {
		case <-gate:
		case <-c.ctx.Done():
			c.registry.mu.Lock()
			c.failLocked("endpoint canceled during release pause")
			c.registry.mu.Unlock()
		case <-timer.C:
			c.registry.mu.Lock()
			c.failLocked("release pause deadline exceeded")
			c.registry.mu.Unlock()
		}
		return
	}
	if c.state == "resumed" && observation.Connected && observation.Lane == 1 {
		lanes := c.lanesLocked()
		if time.Since(c.resumed) > c.replacementWait || !externalRetirementFiveLanes(lanes) || !c.survivorsLocked(lanes) || lanes[1].Sequence <= c.before[1].Sequence || lanes[1].Identity == c.before[1].Identity || lanes[1].Pool.ReceiveLimit != tunnel.DefaultAggregateFlowControlConfig().InitialSessionWindow || capacity.InUse != 0 || capacity.ControlInUse != 0 {
			c.failLocked("replacement identity/pool/survivor boundary differs")
		} else {
			c.replacement = lanes[1]
			c.replacementCapacity = capacity
			c.edgeLocked("replacement_ready")
		}
	} else if len(c.before) > 0 && c.state != "failed" && c.state != "complete" && (disconnectedCurrent || observation.Connected) {
		c.failLocked("unexpected session change during retirement")
	}
	c.registry.mu.Unlock()
}
func (c *externalQualificationRetirement) receipt() externalRetirementReceipt {
	c.registry.mu.Lock()
	defer c.registry.mu.Unlock()
	if c.state == "resumed" && time.Since(c.resumed) > c.replacementWait {
		c.failLocked("replacement deadline exceeded")
	}
	joined := false
	if c.closeDone != nil {
		select {
		case <-c.closeDone:
			joined = true
		default:
		}
	}
	return externalRetirementReceipt{CloseWorkerJoined: joined, State: c.state, Error: c.failure, Lanes: c.lanesLocked(), Before: append([]externalRetirementLane(nil), c.before...), CapacityBefore: c.beforeCapacity, CapacityReleased: c.releasedCapacity, CapacityReplacement: c.replacementCapacity, CloseReturned: c.closeReturned, CloseReturnedUnixNano: c.closeReturnedAt, DisconnectAfterReleaseUnixNano: c.disconnectedAt, DisconnectAfterRelease: c.disconnected, OldPointerRetained: c.old != nil, Edges: append([]externalRetirementEdge(nil), c.edges...)}
}
func (c *externalQualificationRetirement) start(ctx context.Context, request externalRetirementRequest) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := c.ctx.Err(); err != nil {
		return err
	}
	capacity := c.capacity()
	c.registry.mu.Lock()
	if c.state != "ready" {
		c.registry.mu.Unlock()
		return errors.New("retirement allowance already consumed")
	}
	lanes := c.lanesLocked()
	if request.Lane != 1 || !externalRetirementFiveLanes(lanes) || capacity.InUse != 0 || capacity.ControlInUse != 0 || request.ControlSequence != lanes[0].Sequence || request.ControlIdentity != lanes[0].Identity || request.Sequence != lanes[1].Sequence || request.Identity != lanes[1].Identity {
		c.registry.mu.Unlock()
		return errors.New("expected idle lane or control generation differs")
	}
	c.before = lanes
	c.beforeCapacity = capacity
	c.old = c.registry.sessions[1].session
	c.closeDone = make(chan struct{})
	c.disconnectDone = make(chan struct{})
	c.resume = make(chan struct{})
	c.started = time.Now()
	c.edgeLocked("armed")
	c.edgeLocked("closing")
	old, done := c.old, c.closeDone
	c.registry.mu.Unlock()
	go func(session *yamux.Session) {
		err := c.closeSession(session)
		session = nil
		c.registry.mu.Lock()
		c.closeReturnedAt = time.Now().UnixNano()
		c.closeReturned = true
		if err != nil {
			c.failLocked("physical Close returned an error")
		}
		c.advanceReleasedLocked()
		c.registry.mu.Unlock()
		close(done)
	}(old)
	old = nil
	timer := time.NewTimer(c.closeWait)
	defer timer.Stop()
	for _, join := range []<-chan struct{}{done, c.disconnectDone} {
		select {
		case <-join:
		case <-ctx.Done():
			c.registry.mu.Lock()
			c.failLocked("retirement request canceled")
			c.registry.mu.Unlock()
			return ctx.Err()
		case <-c.ctx.Done():
			c.registry.mu.Lock()
			c.failLocked("endpoint canceled")
			c.registry.mu.Unlock()
			return c.ctx.Err()
		case <-timer.C:
			c.registry.mu.Lock()
			c.failLocked("close/disconnect deadline exceeded")
			c.registry.mu.Unlock()
			return errors.New("close/disconnect deadline exceeded")
		}
	}
	c.registry.mu.Lock()
	defer c.registry.mu.Unlock()
	c.advanceReleasedLocked()
	if ctx.Err() != nil {
		c.failLocked("retirement request canceled")
		return ctx.Err()
	}
	if c.state != "released_paused" {
		return errors.New("release boundary failed")
	}
	return nil
}
func (c *externalQualificationRetirement) join(ctx context.Context) error {
	c.registry.mu.Lock()
	if c.state != "ready" && c.state != "complete" {
		c.failLocked("shutdown before completed retirement")
	}
	done := c.closeDone
	c.registry.mu.Unlock()
	if done != nil {
		select {
		case <-done:
		case <-ctx.Done():
			return errors.New("physical close worker did not join")
		}
	}
	return nil
}
func (c *externalQualificationRetirement) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if !externalRetirementTokenValid(c.token) {
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "POST required", http.StatusMethodNotAllowed)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 4096)
	var request externalRetirementRequest
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if decoder.Decode(&request) != nil || decoder.Decode(new(any)) != io.EOF || request.Token != c.token || request.Lane != 1 {
		http.Error(w, "invalid retirement request", http.StatusBadRequest)
		return
	}
	var err error
	switch request.Action {
	case "status":
	case "retire":
		c.registry.mu.Lock()
		complete := c.state == "complete"
		if complete && !c.matchesLocked(request) {
			err = errors.New("completed operation identity differs")
		}
		c.registry.mu.Unlock()
		if !complete {
			err = c.start(r.Context(), request)
		}
	case "resume":
		c.registry.mu.Lock()
		if c.state != "released_paused" || time.Since(c.started) >= c.pauseWait || c.ctx.Err() != nil || !request.ServerRawSlotNil || request.ControlSequence != c.before[0].Sequence || request.ControlIdentity != c.before[0].Identity || request.Sequence != c.before[1].Sequence || request.Identity != c.before[1].Identity {
			err = errors.New("resume requires exact released identity and server raw-slot barrier")
		} else {
			c.old = nil
			c.resumed = time.Now()
			c.edgeLocked("resumed")
			c.openGateLocked()
		}
		c.registry.mu.Unlock()
	case "complete":
		c.registry.mu.Lock()
		if !c.matchesLocked(request) {
			err = errors.New("operation identity differs")
		} else if c.state == "replacement_ready" {
			lanes := c.lanesLocked()
			if !externalRetirementFiveLanes(lanes) || !c.survivorsLocked(lanes) || lanes[1] != c.replacement {
				c.failLocked("replacement or surviving idle lanes changed before completion")
				err = errors.New(c.failure)
			} else {
				c.edgeLocked("complete")
			}
		} else if c.state != "complete" {
			err = errors.New("replacement not ready")
		}
		c.registry.mu.Unlock()
	default:
		err = errors.New("unknown retirement action")
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	data, err := json.Marshal(c.receipt())
	if err != nil || len(data) > 64<<10 {
		http.Error(w, "receipt exceeds bound", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(data)
}

const externalRetirementTestToken = "0123456789abcdef0123456789abcdef"

type externalRetirementFixture struct {
	controller *externalQualificationRetirement
	peers      [5]*yamux.Session
	sessions   [5]*yamux.Session
	capacityMu sync.Mutex
	capacity   agentTunnelCapacitySnapshot
}

func externalRetirementPair(t *testing.T, bulk bool) (*yamux.Session, *yamux.Session) {
	t.Helper()
	a, b := net.Pipe()
	config := tunnel.DefaultYamuxConfig(nil)
	if bulk {
		var err error
		config, err = tunnel.NewAggregateYamuxConfig(nil, tunnel.DefaultMaxStreamWindowSizeBytes, 64)
		if err != nil {
			t.Fatal(err)
		}
	}
	client, err := yamux.Client(a, config)
	if err != nil {
		t.Fatal(err)
	}
	server, err := yamux.Server(b, config)
	if err != nil {
		_ = client.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close(); _ = server.Close() })
	return client, server
}
func newExternalRetirementFixture(t *testing.T) *externalRetirementFixture {
	t.Helper()
	f := &externalRetirementFixture{}
	initial := int64(yamux.AggregateReceiveWindowMemory(tunnel.DefaultAggregateFlowControlConfig().InitialSessionWindow))
	total, err := tunnel.AggregateSessionResourceBytes(tunnel.DefaultAggregateFlowControlConfig().InitialSessionWindow, tunnel.AggregateAcceptBacklogStreams+tunnel.AggregateRejectedStreamLimit)
	if err != nil {
		t.Fatal(err)
	}
	f.capacity = agentTunnelCapacitySnapshot{AggregateInitialPoolBytes: 4 * initial, AggregateCurrentPoolBytes: 4 * initial, AggregateSessionMetadataBytes: 4 * (total - initial), SessionResourceBytes: 4 * total}
	registry := newExternalQualificationSessionRegistry(5)
	f.controller = newExternalQualificationRetirement(t.Context(), registry, func() agentTunnelCapacitySnapshot {
		// The observer/caller must not hold the registry across callbacks.
		registry.mu.Lock()
		registry.mu.Unlock()
		f.capacityMu.Lock()
		defer f.capacityMu.Unlock()
		return f.capacity
	}, externalRetirementTestToken)
	for lane := 0; lane < 5; lane++ {
		f.sessions[lane], f.peers[lane] = externalRetirementPair(t, lane > 0)
		f.controller.observe(tunnelSessionObservation{Lane: lane, Aggregate: lane > 0, Connected: true, Session: f.sessions[lane]})
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := f.controller.join(ctx); err != nil {
			t.Error(err)
		}
	})
	return f
}
func (f *externalRetirementFixture) request() externalRetirementRequest {
	lanes := f.controller.receipt().Lanes
	return externalRetirementRequest{Token: externalRetirementTestToken, Action: "retire", Lane: 1, ControlSequence: lanes[0].Sequence, ControlIdentity: lanes[0].Identity, Sequence: lanes[1].Sequence, Identity: lanes[1].Identity}
}
func (f *externalRetirementFixture) release() {
	f.capacityMu.Lock()
	defer f.capacityMu.Unlock()
	initial := int64(yamux.AggregateReceiveWindowMemory(tunnel.DefaultAggregateFlowControlConfig().InitialSessionWindow))
	total, _ := tunnel.AggregateSessionResourceBytes(tunnel.DefaultAggregateFlowControlConfig().InitialSessionWindow, tunnel.AggregateAcceptBacklogStreams+tunnel.AggregateRejectedStreamLimit)
	f.capacity.AggregateInitialPoolBytes -= initial
	f.capacity.AggregateCurrentPoolBytes -= initial
	f.capacity.AggregateSessionMetadataBytes -= total - initial
	f.capacity.SessionResourceBytes -= total
}
func externalRetirementAwait(t *testing.T, done <-chan struct{}) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("owned retirement test worker did not join")
	}
}
func externalRetirementHTTP(t *testing.T, c *externalQualificationRetirement, request externalRetirementRequest) *httptest.ResponseRecorder {
	t.Helper()
	body, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	c.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/retirement", strings.NewReader(string(body))))
	return response
}
func TestExternalQualificationRetirementOrdering(t *testing.T) {
	f := newExternalRetirementFixture(t)
	c := f.controller
	request := f.request()
	closeEntered, allowReturn := make(chan struct{}), make(chan struct{})
	var allowOnce sync.Once
	unblock := func() { allowOnce.Do(func() { close(allowReturn) }) }
	defer unblock()
	var closes atomic.Int64
	c.closeSession = func(session *yamux.Session) error {
		closes.Add(1)
		err := session.Close()
		close(closeEntered)
		<-allowReturn
		return err
	}
	started := make(chan error, 1)
	go func() { started <- c.start(t.Context(), request) }()
	externalRetirementAwait(t, closeEntered)
	observerDone := make(chan struct{})
	defer func() {
		unblock()
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = c.join(ctx)
		externalRetirementAwait(t, observerDone)
	}()
	go func() {
		f.release()
		c.observe(tunnelSessionObservation{Lane: 1, Aggregate: true, Session: f.sessions[1]})
		close(observerDone)
	}()
	deadline := time.Now().Add(time.Second)
	for !c.receipt().DisconnectAfterRelease && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	receipt := c.receipt()
	if receipt.State != "closing" || !receipt.DisconnectAfterRelease || receipt.CloseReturned {
		t.Fatalf("premature released boundary: %+v", receipt)
	}
	// Status/snapshot can take the registry lock while the observer is paused.
	if len(c.registry.snapshot(4)) != 4 {
		t.Fatal("old lane not removed before observer pause")
	}
	unblock()
	select {
	case err := <-started:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("retire did not return")
	}
	if got := c.receipt(); got.State != "released_paused" || !got.OldPointerRetained {
		t.Fatalf("release receipt: %+v", got)
	}
	if err := c.start(t.Context(), request); err == nil {
		t.Fatal("second close accepted")
	}
	request.Action = "resume"
	if response := externalRetirementHTTP(t, c, request); response.Code != http.StatusConflict {
		t.Fatal("resume skipped server barrier")
	}
	request.ServerRawSlotNil = true
	if response := externalRetirementHTTP(t, c, request); response.Code != http.StatusOK {
		t.Fatal(response.Body.String())
	}
	externalRetirementAwait(t, observerDone)
	replacement, peer := externalRetirementPair(t, true)
	_ = peer
	f.capacityMu.Lock()
	f.capacity = c.beforeCapacity
	f.capacityMu.Unlock()
	c.observe(tunnelSessionObservation{Lane: 1, Aggregate: true, Connected: true, Session: replacement})
	receipt = c.receipt()
	if receipt.State != "replacement_ready" || receipt.OldPointerRetained || receipt.Lanes[1].Sequence <= request.Sequence {
		t.Fatalf("replacement: %+v", receipt)
	}
	request.Action = "complete"
	if response := externalRetirementHTTP(t, c, request); response.Code != http.StatusOK {
		t.Fatal(response.Body.String())
	}
	request.Action = "retire"
	if response := externalRetirementHTTP(t, c, request); response.Code != http.StatusOK || closes.Load() != 1 {
		t.Fatalf("completed replay: %d closes=%d", response.Code, closes.Load())
	}
	bad := request
	bad.Identity = "stale"
	if response := externalRetirementHTTP(t, c, bad); response.Code != http.StatusConflict || closes.Load() != 1 {
		t.Fatal("conflicting completed token identity accepted")
	}
	// A stale disconnect after reconnect cannot remove the replacement.
	c.observe(tunnelSessionObservation{Lane: 1, Aggregate: true, Session: f.sessions[1]})
	if c.receipt().Lanes[1].Identity != fmt.Sprintf("%p", replacement) {
		t.Fatal("stale disconnect removed replacement")
	}
}
func TestExternalQualificationRetirementRejectsActiveAndStale(t *testing.T) {
	f := newExternalRetirementFixture(t)
	c := f.controller
	request := f.request()
	for _, change := range []func(*externalRetirementRequest){func(r *externalRetirementRequest) { r.Lane = 0 }, func(r *externalRetirementRequest) { r.ControlSequence++ }, func(r *externalRetirementRequest) { r.ControlIdentity = "stale" }, func(r *externalRetirementRequest) { r.Sequence++ }, func(r *externalRetirementRequest) { r.Identity = "stale" }} {
		bad := request
		change(&bad)
		if c.start(t.Context(), bad) == nil {
			t.Fatal("stale/control operation accepted")
		}
	}
	c.registry.mu.Lock()
	entry := c.registry.sessions[1]
	entry.aggregate = false
	c.registry.sessions[1] = entry
	c.registry.mu.Unlock()
	if c.start(t.Context(), request) == nil {
		t.Fatal("nonaggregate lane accepted")
	}
	c.registry.mu.Lock()
	entry.aggregate = true
	c.registry.sessions[1] = entry
	c.registry.mu.Unlock()
	stream, err := f.sessions[1].OpenStream()
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()
	remote, err := f.peers[1].AcceptStream()
	if err != nil {
		t.Fatal(err)
	}
	defer remote.Close()
	if c.start(t.Context(), request) == nil || f.sessions[1].IsClosed() {
		t.Fatal("active session was retired")
	}
	_ = stream.SetWriteDeadline(time.Now().Add(time.Second))
	wrote := make(chan error, 1)
	go func() { _, err := stream.Write([]byte("still usable")); wrote <- err }()
	_ = remote.SetReadDeadline(time.Now().Add(time.Second))
	buffer := make([]byte, len("still usable"))
	if _, err := io.ReadFull(remote, buffer); err != nil || string(buffer) != "still usable" {
		t.Fatalf("rejected active stream unusable: %q %v", buffer, err)
	}
	if err := <-wrote; err != nil {
		t.Fatal(err)
	}
	for _, body := range []string{`{"token":"wrong","lane":1,"action":"status"}`, strings.Repeat("x", 4097), `{"token":"` + externalRetirementTestToken + `","lane":1,"action":"status","extra":true}`} {
		response := httptest.NewRecorder()
		c.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/retirement", strings.NewReader(body)))
		if response.Code != http.StatusBadRequest {
			t.Fatalf("invalid body status=%d", response.Code)
		}
	}
	response := httptest.NewRecorder()
	c.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/retirement", nil))
	if response.Code != http.StatusMethodNotAllowed {
		t.Fatal("GET accepted")
	}
}
func TestExternalQualificationRetirementCancellationJoins(t *testing.T) {
	for _, delayed := range []string{"close", "disconnect"} {
		t.Run(delayed, func(t *testing.T) {
			f := newExternalRetirementFixture(t)
			c := f.controller
			request := f.request()
			entered, release := make(chan struct{}), make(chan struct{})
			var releaseOnce sync.Once
			unblock := func() { releaseOnce.Do(func() { close(release) }) }
			defer unblock()
			c.closeSession = func(session *yamux.Session) error {
				close(entered)
				if delayed == "close" {
					<-release
				}
				return session.Close()
			}
			ctx, cancel := context.WithCancel(t.Context())
			result := make(chan error, 1)
			go func() { result <- c.start(ctx, request) }()
			externalRetirementAwait(t, entered)
			cancel()
			select {
			case err := <-result:
				if err == nil {
					t.Fatal("canceled retirement succeeded")
				}
			case <-time.After(time.Second):
				t.Fatal("cancel did not return")
			}
			unblock()
			joinCtx, stop := context.WithTimeout(t.Context(), time.Second)
			defer stop()
			if err := c.join(joinCtx); err != nil {
				t.Fatal(err)
			}
			observerDone := make(chan struct{})
			go func() {
				f.release()
				c.observe(tunnelSessionObservation{Lane: 1, Aggregate: true, Session: f.sessions[1]})
				close(observerDone)
			}()
			externalRetirementAwait(t, observerDone)
			if receipt := c.receipt(); receipt.State != "failed" || receipt.OldPointerRetained {
				t.Fatalf("failed cleanup: %+v", receipt)
			}
		})
	}
}

// Called only after the ordinary runtime has joined. No forced collection or
// credentials are involved; scope exit is not used as a proxy for this ledger.
func (c *externalQualificationRetirement) terminal(t *testing.T) {
	t.Helper()
	if !externalRetirementTokenValid(c.token) {
		return
	}
	capacity := c.capacity()
	receipt := c.receipt()
	c.registry.mu.Lock()
	joined := c.closeDone != nil
	if joined {
		select {
		case <-c.closeDone:
		default:
			joined = false
		}
	}
	c.registry.mu.Unlock()
	if receipt.State != "complete" || len(receipt.Lanes) != 0 || !joined || receipt.OldPointerRetained || capacity.InUse != 0 || capacity.ControlInUse != 0 || capacity.SessionResourceBytes != 0 || capacity.ReceiveWindowBytes != 0 {
		t.Error("retirement terminal ownership did not drain")
	}
	data, err := json.Marshal(struct {
		State             string                      `json:"state"`
		Sessions          int                         `json:"sessions"`
		CloseWorkerJoined bool                        `json:"close_worker_joined"`
		Capacity          agentTunnelCapacitySnapshot `json:"capacity"`
	}{receipt.State, len(receipt.Lanes), joined, capacity})
	if err != nil || len(data) > 64<<10 {
		t.Error("terminal retirement receipt exceeds bound")
		return
	}
	fmt.Printf("qualification_retirement_terminal %s\n", data)
}

func (c *externalQualificationRetirement) matchesLocked(request externalRetirementRequest) bool {
	return len(c.before) == 5 && request.Lane == 1 && request.ControlSequence == c.before[0].Sequence && request.ControlIdentity == c.before[0].Identity && request.Sequence == c.before[1].Sequence && request.Identity == c.before[1].Identity
}

func TestExternalQualificationRetirementDeadlineOpensGate(t *testing.T) {
	f := newExternalRetirementFixture(t)
	c := f.controller
	c.pauseWait = 100 * time.Millisecond
	request := f.request()
	observerDone := make(chan struct{})
	go func() {
		<-f.sessions[1].CloseChan()
		_ = f.sessions[1].Close()
		f.release()
		c.observe(tunnelSessionObservation{Lane: 1, Aggregate: true, Session: f.sessions[1]})
		close(observerDone)
	}()
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = c.join(ctx)
		externalRetirementAwait(t, observerDone)
	}()
	// A heavily delayed scheduler may consume the pause budget before start
	// returns; either way the gate must open as failed, never strand reconnect.
	_ = c.start(t.Context(), request)
	externalRetirementAwait(t, observerDone)
	if receipt := c.receipt(); receipt.State != "failed" || receipt.OldPointerRetained {
		t.Fatalf("expired pause accepted: %+v", receipt)
	}
}

func TestExternalQualificationRetirementCloseDisconnectDeadline(t *testing.T) {
	for _, delayed := range []string{"close", "disconnect"} {
		t.Run(delayed, func(t *testing.T) {
			f := newExternalRetirementFixture(t)
			c := f.controller
			c.closeWait = 20 * time.Millisecond
			release := make(chan struct{})
			var once sync.Once
			unblock := func() { once.Do(func() { close(release) }) }
			defer unblock()
			c.closeSession = func(session *yamux.Session) error {
				if delayed == "close" {
					<-release
				}
				return session.Close()
			}
			if err := c.start(t.Context(), f.request()); err == nil {
				t.Fatal("missing close/disconnect boundary accepted")
			}
			if c.receipt().State != "failed" {
				t.Fatal("deadline did not fail operation")
			}
			unblock()
			ctx, cancel := context.WithTimeout(t.Context(), time.Second)
			defer cancel()
			if err := c.join(ctx); err != nil {
				t.Fatal(err)
			}
			observerDone := make(chan struct{})
			go func() {
				f.release()
				c.observe(tunnelSessionObservation{Lane: 1, Aggregate: true, Session: f.sessions[1]})
				close(observerDone)
			}()
			externalRetirementAwait(t, observerDone)
		})
	}
}

func externalRetirementFiveLanes(lanes []externalRetirementLane) bool {
	if !externalRetirementIdle(lanes, 5) {
		return false
	}
	for index, lane := range lanes {
		if lane.Lane != index {
			return false
		}
	}
	return true
}

func TestExternalQualificationRetirementReplacementLoss(t *testing.T) {
	for _, loss := range []string{"observed-disconnect", "closed-before-observer"} {
		t.Run(loss, func(t *testing.T) {
			f := newExternalRetirementFixture(t)
			c := f.controller
			request := f.request()
			observerDone := make(chan struct{})
			go func() {
				<-f.sessions[1].CloseChan()
				_ = f.sessions[1].Close()
				f.release()
				c.observe(tunnelSessionObservation{Lane: 1, Aggregate: true, Session: f.sessions[1]})
				close(observerDone)
			}()
			defer func() {
				ctx, cancel := context.WithTimeout(context.Background(), time.Second)
				defer cancel()
				_ = c.join(ctx)
				externalRetirementAwait(t, observerDone)
			}()
			if err := c.start(t.Context(), request); err != nil {
				t.Fatal(err)
			}
			request.Action = "resume"
			request.ServerRawSlotNil = true
			if response := externalRetirementHTTP(t, c, request); response.Code != http.StatusOK {
				t.Fatal(response.Body.String())
			}
			externalRetirementAwait(t, observerDone)
			replacement, _ := externalRetirementPair(t, true)
			f.capacityMu.Lock()
			f.capacity = c.beforeCapacity
			f.capacityMu.Unlock()
			c.observe(tunnelSessionObservation{Lane: 1, Aggregate: true, Connected: true, Session: replacement})
			// The stale retired pointer must be ignored even before completion, whereas
			// losing the exact current replacement invalidates that pending completion.
			c.observe(tunnelSessionObservation{Lane: 1, Aggregate: true, Session: f.sessions[1]})
			if receipt := c.receipt(); receipt.State != "replacement_ready" || len(receipt.Lanes) != 5 || receipt.Lanes[1].Identity != fmt.Sprintf("%p", replacement) {
				t.Fatalf("stale disconnect affected replacement: %+v", receipt)
			}
			if err := replacement.Close(); err != nil {
				t.Fatal(err)
			}
			if loss == "observed-disconnect" {
				c.observe(tunnelSessionObservation{Lane: 1, Aggregate: true, Session: replacement})
				if c.receipt().State != "failed" {
					t.Fatal("current replacement loss not rejected by observer")
				}
			}
			request.Action = "complete"
			if response := externalRetirementHTTP(t, c, request); response.Code != http.StatusConflict {
				t.Fatalf("lost replacement completed: status=%d body=%s", response.Code, response.Body.String())
			}
			if c.receipt().State != "failed" {
				t.Fatal("completion failed to revalidate replacement")
			}
		})
	}
}
