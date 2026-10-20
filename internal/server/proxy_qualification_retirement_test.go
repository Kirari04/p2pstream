//go:build linux

package server

import (
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
	"testing"
	"time"

	"github.com/hashicorp/yamux"
	"p2pstream/internal/tunnel"
)

type qualificationRetirementLane struct {
	LegacyControlSupplementBytes int64                             `json:"legacy_control_supplement_bytes"`
	Lane                         int                               `json:"lane"`
	Identity                     string                            `json:"identity"`
	Present                      bool                              `json:"present"`
	Closed                       bool                              `json:"closed"`
	Streams                      int                               `json:"streams"`
	Pool                         yamux.AggregateReceiveWindowStats `json:"pool"`
	InitialPoolBytes             int64                             `json:"initial_pool_bytes"`
	MetadataBytes                int64                             `json:"metadata_bytes"`
}
type qualificationRetirementRequest struct {
	Token      string `json:"token"`
	Lane       int    `json:"lane"`
	Generation string `json:"generation"`
}
type qualificationRetirementReceipt struct {
	Generation        string                        `json:"generation"`
	AgentID           int64                         `json:"agent_id"`
	ConnectionID      int64                         `json:"connection_id"`
	ConnectedUnixNano int64                         `json:"connected_unix_nano"`
	CapturedUnixNano  int64                         `json:"captured_unix_nano"`
	CapacityUnixNano  int64                         `json:"capacity_unix_nano"`
	RawLaneAbsent     bool                          `json:"raw_lane_absent"`
	ActiveRequests    int64                         `json:"active_requests"`
	Lanes             []qualificationRetirementLane `json:"lanes"`
	TotalInUse        int                           `json:"total_in_use"`
	ActiveLeases      int                           `json:"active_leases"`
	OwnerBytes        map[string]int64              `json:"owner_bytes"`
	OwnerFDs          map[string]int64              `json:"owner_fds"`
	OwnerReservations map[string]int64              `json:"owner_reservations"`
}

// This helper never closes/unregisters/releases anything. The exact AgentConn
// anchor survives subsequent snapshots, but no separate old lane pointer does.
// It reads RAW slots because IsClosed-filtered topology is not a release proof.
type qualificationRetirementSnapshot struct {
	mu      sync.Mutex
	app     *App
	agentID int64
	token   string
	anchor  *AgentConn
}

func newQualificationRetirementSnapshot(app *App, ids []int64, token string) *qualificationRetirementSnapshot {
	result := &qualificationRetirementSnapshot{app: app, token: token}
	if len(ids) == 1 {
		result.agentID = ids[0]
	}
	return result
}
func qualificationRetirementGeneration(agent *AgentConn) string {
	return fmt.Sprintf("%p:%d:%d:%d", agent, agent.AgentID, agent.ConnectionDBID, agent.ConnectedAt.UnixNano())
}
func (c *qualificationRetirementSnapshot) snapshot(expected string) (qualificationRetirementReceipt, error) {
	result := qualificationRetirementReceipt{}
	if c.app == nil || c.app.AgentHub == nil || c.agentID == 0 {
		return result, errors.New("one qualification agent required")
	}
	c.mu.Lock()
	// Same auth -> hub -> tunnel order as decisive production lane transitions.
	unlockAuth := c.app.lockAgentAuth(c.agentID)
	current := c.app.AgentHub.connectedByID(c.agentID)
	if current == nil || (c.anchor != nil && current != c.anchor) {
		unlockAuth()
		c.mu.Unlock()
		return result, errors.New("agent generation replaced")
	}
	generation := qualificationRetirementGeneration(current)
	if expected != "" && expected != generation {
		unlockAuth()
		c.mu.Unlock()
		return result, errors.New("stale expected generation")
	}
	unlockHub, ok := c.app.AgentHub.lockCurrentConnection(c.agentID, current)
	if !ok {
		unlockAuth()
		c.mu.Unlock()
		return result, errors.New("agent generation changed")
	}
	current.tunnelMu.RLock()
	if !current.AggregateFlowControl || !current.tunnelsInitialized || current.tunnels[0] == nil || current.tunnels[0].IsClosed() {
		current.tunnelMu.RUnlock()
		unlockHub()
		unlockAuth()
		c.mu.Unlock()
		return result, errors.New("live aggregate control required")
	}
	result.Generation = generation
	result.AgentID = current.AgentID
	result.ConnectionID = current.ConnectionDBID
	result.ConnectedUnixNano = current.ConnectedAt.UnixNano()
	result.CapturedUnixNano = time.Now().UnixNano()
	result.RawLaneAbsent = current.tunnels[1] == nil
	result.ActiveRequests = current.ActiveRequests.Load()
	for lane, session := range current.tunnels {
		if lane > 4 {
			if session != nil {
				current.tunnelMu.RUnlock()
				unlockHub()
				unlockAuth()
				c.mu.Unlock()
				return qualificationRetirementReceipt{}, errors.New("unexpected extra bulk lane")
			}
			continue
		}
		row := qualificationRetirementLane{Lane: lane}
		if session != nil {
			row.Present = true
			row.Identity = fmt.Sprintf("%p", session)
			row.Closed = session.IsClosed()
			row.Streams = session.NumStreams()
			row.Pool = session.AggregateReceiveWindowStats()
			resource := current.tunnelResources[lane]
			row.InitialPoolBytes = resource.AggregateInitialPoolBytes
			row.MetadataBytes = resource.SessionMetadataBytes
			row.LegacyControlSupplementBytes = resource.LegacyControlSupplementBytes
		}
		result.Lanes = append(result.Lanes, row)
	}
	c.anchor = current
	current.tunnelMu.RUnlock()
	unlockHub()
	unlockAuth()
	c.mu.Unlock()
	// Capacity telemetry may sample the OS. Never call it under generation locks.
	capacity := c.app.agentStreamCapacity.snapshot()
	result.CapacityUnixNano = time.Now().UnixNano()
	result.TotalInUse = capacity.Total.InUse
	result.ActiveLeases = capacity.ActiveLeases
	result.OwnerBytes = qualificationOwnerValues(capacity, "AdaptiveExternalBytesByOwner")
	result.OwnerFDs = qualificationOwnerValues(capacity, "AdaptiveExternalFDsByOwner")
	result.OwnerReservations = qualificationOwnerValues(capacity, "AdaptiveExternalReservationsByOwner")
	return result, nil
}
func (c *qualificationRetirementSnapshot) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	_, tokenErr := hex.DecodeString(c.token)
	if len(c.token) != 32 || tokenErr != nil {
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "POST required", http.StatusMethodNotAllowed)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 4096)
	var request qualificationRetirementRequest
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if decoder.Decode(&request) != nil || decoder.Decode(new(any)) != io.EOF || request.Token != c.token || request.Lane != 1 {
		http.Error(w, "invalid retirement request", http.StatusBadRequest)
		return
	}
	result, err := c.snapshot(request.Generation)
	if err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	data, err := json.Marshal(result)
	if err != nil || len(data) > 64<<10 {
		http.Error(w, "receipt exceeds bound", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(data)
}

func qualificationRetirementTestPair(t *testing.T, bulk bool) (*yamux.Session, *yamux.Session) {
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
func TestQualificationRetirementRawSlotFence(t *testing.T) {
	app := &App{AgentHub: newAgentHub(), agentAuthLocks: newAgentAuthLockMap()}
	agent := &AgentConn{AgentID: 1, PublicID: "retirement", AggregateFlowControl: true, ConnectedAt: time.Now(), ConnectionDBID: 7, Done: make(chan struct{})}
	for lane := 0; lane <= 4; lane++ {
		session, _ := qualificationRetirementTestPair(t, lane > 0)
		agent.installTunnel(lane, session)
	}
	if err := app.AgentHub.connect(agent); err != nil {
		t.Fatal(err)
	}
	helper := newQualificationRetirementSnapshot(app, []int64{1}, "0123456789abcdef0123456789abcdef")
	before, err := helper.snapshot("")
	if err != nil {
		t.Fatal(err)
	}
	old := agent.tunnelSession(1)
	if err := old.Close(); err != nil {
		t.Fatal(err)
	}
	closed, err := helper.snapshot(before.Generation)
	if err != nil {
		t.Fatal(err)
	}
	if closed.RawLaneAbsent || !closed.Lanes[1].Present || !closed.Lanes[1].Closed {
		t.Fatalf("closed slot mistaken for released: %+v", closed)
	}
	if removed, last := agent.removeTunnel(1, old); !removed || last {
		t.Fatal("exact slot removal failed")
	}
	released, err := helper.snapshot(before.Generation)
	if err != nil {
		t.Fatal(err)
	}
	if !released.RawLaneAbsent || released.Lanes[1].Present {
		t.Fatal("raw nil not observed")
	}
	replacement, _ := qualificationRetirementTestPair(t, true)
	agent.installTunnel(1, replacement)
	if removed, _ := agent.removeTunnel(1, old); removed {
		t.Fatal("stale teardown removed replacement")
	}
	after, err := helper.snapshot(before.Generation)
	if err != nil {
		t.Fatal(err)
	}
	if after.RawLaneAbsent || after.Lanes[1].Identity == before.Lanes[1].Identity {
		t.Fatal("replacement not distinguished")
	}
	for _, lane := range []int{0, 2, 3, 4} {
		if before.Lanes[lane] != after.Lanes[lane] {
			t.Fatalf("surviving lane %d changed", lane)
		}
	}
	if _, err := helper.snapshot("stale"); err == nil {
		t.Fatal("stale generation accepted")
	}
	replaced := &AgentConn{AgentID: 1, PublicID: "retirement", ConnectedAt: time.Now(), Done: make(chan struct{})}
	if _, err := app.AgentHub.replace(replaced); err != nil {
		t.Fatal(err)
	}
	if _, err := helper.snapshot(before.Generation); err == nil {
		t.Fatal("replaced AgentConn accepted")
	}
}
func TestQualificationRetirementHTTPBounds(t *testing.T) {
	helper := newQualificationRetirementSnapshot(nil, nil, "0123456789abcdef0123456789abcdef")
	for _, body := range []string{strings.Repeat("x", 4097), `{"token":"wrong","lane":1}`, `{"token":"0123456789abcdef0123456789abcdef","lane":0}`, `{"token":"0123456789abcdef0123456789abcdef","lane":1,"unknown":true}`} {
		response := httptest.NewRecorder()
		helper.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/retirement", strings.NewReader(body)))
		if response.Code != http.StatusBadRequest {
			t.Fatalf("bad request status=%d", response.Code)
		}
	}
	response := httptest.NewRecorder()
	helper.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/retirement", nil))
	if response.Code != http.StatusMethodNotAllowed {
		t.Fatal("GET accepted")
	}
	disabled := newQualificationRetirementSnapshot(nil, nil, "")
	response = httptest.NewRecorder()
	disabled.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/retirement", nil))
	if response.Code != http.StatusNotFound {
		t.Fatal("disabled hook exposed")
	}
}
