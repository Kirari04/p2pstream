package agent

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/hashicorp/yamux"

	"p2pstream/internal/tunnel"
)

type rollbackLaneTransport struct {
	mu sync.Mutex

	groups   map[string]int
	attempts map[int]map[int]int

	firstControl     chan *yamux.Session
	firstBulk        chan *yamux.Session
	downgradeSeen    chan struct{}
	downgradeRelease chan struct{}
	legacyPrimary    chan *yamux.Session
	finalControl     chan *yamux.Session
	finalBulk        chan *yamux.Session
}

func newRollbackLaneTransport() *rollbackLaneTransport {
	return &rollbackLaneTransport{
		groups:           make(map[string]int),
		attempts:         make(map[int]map[int]int),
		firstControl:     make(chan *yamux.Session, 1),
		firstBulk:        make(chan *yamux.Session, 2),
		downgradeSeen:    make(chan struct{}, 1),
		downgradeRelease: make(chan struct{}),
		legacyPrimary:    make(chan *yamux.Session, 1),
		finalControl:     make(chan *yamux.Session, 1),
		finalBulk:        make(chan *yamux.Session, 2),
	}
}

func (t *rollbackLaneTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	flow, role, offered, err := tunnel.ParseAggregateFlowControlHeaders(r.Header)
	if err != nil {
		return nil, err
	}
	group, lane, err := tunnel.ParseTunnelLaneHeaders(r.Header, offered)
	if err != nil {
		return nil, err
	}
	t.mu.Lock()
	generation, ok := t.groups[group]
	if !ok {
		generation = len(t.groups) + 1
		t.groups[group] = generation
		t.attempts[generation] = make(map[int]int)
	}
	t.attempts[generation][lane]++
	attempt := t.attempts[generation][lane]
	t.mu.Unlock()

	switch generation {
	case 1:
		if !offered || !flow.Equal(tunnel.DefaultAggregateFlowControlConfig()) {
			return nil, fmt.Errorf("invalid aggregate offer: role=%q offered=%t", role, offered)
		}
		if lane == 0 && attempt > 1 {
			t.downgradeSeen <- struct{}{}
			<-t.downgradeRelease
			return rollbackUpgradeResponse(r, false, "", false, nil)
		}
		if lane == 0 && role == tunnel.TunnelLaneRoleControl {
			return rollbackUpgradeResponse(r, true, role, true, t.firstControl)
		}
		if lane > 0 && role == tunnel.TunnelLaneRoleBulk {
			return rollbackUpgradeResponse(r, true, role, true, t.firstBulk)
		}
	case 2:
		if lane == 0 {
			if !offered || !flow.Equal(tunnel.DefaultAggregateFlowControlConfig()) {
				return nil, fmt.Errorf("fresh legacy generation did not offer aggregate")
			}
			return rollbackUpgradeResponse(r, false, "", false, t.legacyPrimary)
		}
		return nil, fmt.Errorf("old single-lane generation attempted lane %d", lane)
	case 3:
		if !offered || !flow.Equal(tunnel.DefaultAggregateFlowControlConfig()) {
			return nil, fmt.Errorf("recovery generation did not offer aggregate")
		}
		if lane == 0 && role == tunnel.TunnelLaneRoleControl {
			return rollbackUpgradeResponse(r, true, role, true, t.finalControl)
		}
		if lane > 0 && role == tunnel.TunnelLaneRoleBulk {
			return rollbackUpgradeResponse(r, true, role, true, t.finalBulk)
		}
	}
	return nil, fmt.Errorf("unexpected generation=%d lane=%d attempt=%d role=%q", generation, lane, attempt, role)
}

func rollbackUpgradeResponse(r *http.Request, aggregate bool, role string, acknowledgeLane bool, ready chan<- *yamux.Session) (*http.Response, error) {
	headers := make(http.Header)
	headers.Set("Upgrade", tunnel.UpgradeToken)
	headers.Set(tunnel.TunnelCapacityModeHeader, tunnel.TunnelCapacityModeFixed)
	headers.Set(tunnel.TunnelMaxConcurrentStreamsHeader, "64")
	if acknowledgeLane {
		headers.Set(tunnel.TunnelGroupHeader, r.Header.Get(tunnel.TunnelGroupHeader))
		headers.Set(tunnel.TunnelLaneHeader, r.Header.Get(tunnel.TunnelLaneHeader))
	}
	if aggregate {
		tunnel.SetAggregateFlowControlHeaders(headers, role, tunnel.DefaultAggregateFlowControlConfig())
	}
	clientConn, serverConn := net.Pipe()
	if ready == nil {
		_ = serverConn.Close()
		return &http.Response{StatusCode: http.StatusSwitchingProtocols, Header: headers, Body: clientConn}, nil
	}
	window, err := tunnel.InitialReceiveWindow(2 << 20)
	if err != nil {
		_ = clientConn.Close()
		_ = serverConn.Close()
		return nil, err
	}
	var config *yamux.Config
	switch {
	case aggregate && role == tunnel.TunnelLaneRoleControl:
		config, err = tunnel.NewNegotiatedControlYamuxConfig(nil, window, -1)
	case aggregate:
		config, err = tunnel.NewAggregateYamuxConfig(nil, 2<<20, -1)
	default:
		config, err = tunnel.NewYamuxConfig(nil, 2<<20)
	}
	if err != nil {
		_ = clientConn.Close()
		_ = serverConn.Close()
		return nil, err
	}
	serverSession, err := yamux.Server(serverConn, config)
	if err != nil {
		_ = clientConn.Close()
		_ = serverConn.Close()
		return nil, err
	}
	ready <- serverSession
	return &http.Response{StatusCode: http.StatusSwitchingProtocols, Header: headers, Body: clientConn}, nil
}

func TestRealYamuxTunnelGroupRollbackReleasesOldGeneration(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	transport := newRollbackLaneTransport()
	var releaseDowngradeOnce sync.Once
	releaseDowngrade := func() { releaseDowngradeOnce.Do(func() { close(transport.downgradeRelease) }) }
	defer releaseDowngrade()
	client := &http.Client{Transport: transport}
	capacity := newAgentTunnelCapacityRuntime(64, false, nil)
	var mu sync.Mutex
	seenGroups := make(map[string]struct{})
	var boundaryErrors []string
	observerConnected, observerDisconnected := 0, 0
	observer := func(observation tunnelSessionObservation) {
		mu.Lock()
		defer mu.Unlock()
		if observation.Connected {
			observerConnected++
			return
		}
		observerDisconnected++
		if !observation.Session.IsClosed() {
			boundaryErrors = append(boundaryErrors, fmt.Sprintf("lane %d observer disconnected before Session.Close", observation.Lane))
		}
		if observation.Aggregate && observation.Lane > 0 {
			stats := observation.Session.AggregateReceiveWindowStats()
			if !stats.Closed || stats.TotalUnits != 0 || stats.BufferedUnits != 0 || stats.PendingRefund != 0 {
				boundaryErrors = append(boundaryErrors, fmt.Sprintf("lane %d observer aggregate stats %+v", observation.Lane, stats))
			}
		}
	}
	connect := func(groupCtx context.Context, lane tunnelLaneRegistration) error {
		mu.Lock()
		_, seen := seenGroups[lane.group]
		if !seen {
			seenGroups[lane.group] = struct{}{}
			snapshot := capacity.snapshot()
			if snapshot.InUse != 0 || snapshot.SessionResourceBytes != 0 || snapshot.ReceiveWindowBytes != 0 {
				boundaryErrors = append(boundaryErrors, fmt.Sprintf("group %d started with snapshot %+v", len(seenGroups), snapshot))
			}
		}
		mu.Unlock()
		return connectAndServe(groupCtx, client, "https://management.test/agent/tunnel", "rollback-agent", "rollback-agent", "token", nil, 2<<20, 64, false, capacity, lane)
	}
	done := make(chan error, 1)
	go func() {
		done <- runTunnelGroupGenerations(ctx, "https://management.test/agent/tunnel", 2, connect, func(waitCtx context.Context, _ time.Duration) error {
			select {
			case <-waitCtx.Done():
				return waitCtx.Err()
			default:
				return nil
			}
		}, observer)
	}()

	firstControl := receiveRollbackSession(t, transport.firstControl, "first control")
	defer firstControl.Close()
	firstBulk := []*yamux.Session{
		receiveRollbackSession(t, transport.firstBulk, "first bulk lane"),
		receiveRollbackSession(t, transport.firstBulk, "second bulk lane"),
	}
	defer func() {
		for _, session := range firstBulk {
			_ = session.Close()
		}
	}()
	_ = firstControl.Close()
	select {
	case <-transport.downgradeSeen:
	case <-time.After(time.Second):
		t.Fatal("aggregate primary did not reconnect into downgrade response")
	}
	for _, session := range firstBulk {
		if session.IsClosed() {
			t.Fatal("bulk session closed before the primary downgrade was classified")
		}
	}
	releaseDowngrade()
	legacy := receiveRollbackSession(t, transport.legacyPrimary, "legacy primary")
	defer legacy.Close()
	for _, session := range firstBulk {
		select {
		case <-session.CloseChan():
		case <-time.After(time.Second):
			t.Fatal("old bulk session was not joined before the legacy group started")
		}
	}
	// The fully absent old-server lane acknowledgment must keep this generation
	// single-lane. Connector-level tests make this deterministic; this real
	// session check catches accidental eager worker wakeups in the full path.
	time.Sleep(25 * time.Millisecond)
	transport.mu.Lock()
	legacyExtraAttempts := transport.attempts[2][1]
	transport.mu.Unlock()
	if legacyExtraAttempts != 0 {
		t.Fatalf("old single-lane server received %d extra lane attempts", legacyExtraAttempts)
	}
	_ = legacy.Close()
	finalControl := receiveRollbackSession(t, transport.finalControl, "final control")
	defer finalControl.Close()
	finalBulk := []*yamux.Session{
		receiveRollbackSession(t, transport.finalBulk, "final bulk lane"),
		receiveRollbackSession(t, transport.finalBulk, "second final bulk lane"),
	}
	defer func() {
		for _, session := range finalBulk {
			_ = session.Close()
		}
	}()

	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("group loop shutdown = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out joining rollback group loop")
	}
	_ = finalControl.Close()
	for _, session := range finalBulk {
		_ = session.Close()
	}
	mu.Lock()
	if len(boundaryErrors) != 0 {
		t.Fatalf("generation resource boundary errors: %v", boundaryErrors)
	}
	if len(seenGroups) != 3 {
		t.Fatalf("distinct group generations = %d, want 3", len(seenGroups))
	}
	if observerConnected != 7 || observerDisconnected != 7 {
		t.Fatalf("session observer connected/disconnected = %d/%d, want 7/7", observerConnected, observerDisconnected)
	}
	mu.Unlock()
	snapshot := capacity.snapshot()
	if snapshot.InUse != 0 || snapshot.SessionResourceBytes != 0 || snapshot.ReceiveWindowBytes != 0 {
		t.Fatalf("resources after group shutdown = %+v", snapshot)
	}
}

func receiveRollbackSession(t *testing.T, sessions <-chan *yamux.Session, name string) *yamux.Session {
	t.Helper()
	select {
	case session := <-sessions:
		return session
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out waiting for %s", name)
		return nil
	}
}
