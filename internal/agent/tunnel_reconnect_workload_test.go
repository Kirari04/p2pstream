package agent

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hashicorp/yamux"

	"p2pstream/internal/sysmetrics"
	"p2pstream/internal/tunnel"
)

const reconnectWorkloadWait = 5 * time.Second

type reconnectWorkloadPeer struct {
	generation, lane, attempt int
	session                   *yamux.Session
}

type reconnectWorkloadTransport struct {
	mu       sync.Mutex
	groups   map[string]int
	attempts map[int]map[int]int
	peers    []*yamux.Session
	ready    chan reconnectWorkloadPeer
	blocked  [3]chan struct{}
	resume   [3]chan struct{}
}

func newReconnectWorkloadTransport() *reconnectWorkloadTransport {
	result := &reconnectWorkloadTransport{groups: make(map[string]int), attempts: make(map[int]map[int]int), ready: make(chan reconnectWorkloadPeer, 16)}
	for i := range result.blocked {
		result.blocked[i], result.resume[i] = make(chan struct{}), make(chan struct{})
	}
	return result
}

func (p *reconnectWorkloadTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	flow, role, offered, err := tunnel.ParseAggregateFlowControlHeaders(r.Header)
	if err != nil {
		return nil, err
	}
	group, lane, err := tunnel.ParseTunnelLaneHeaders(r.Header, offered)
	if err != nil {
		return nil, err
	}
	if !offered || !flow.Equal(tunnel.DefaultAggregateFlowControlConfig()) ||
		(lane == 0 && role != tunnel.TunnelLaneRoleControl) || (lane > 0 && role != tunnel.TunnelLaneRoleBulk) {
		return nil, fmt.Errorf("invalid reconnect aggregate offer lane=%d role=%q", lane, role)
	}
	p.mu.Lock()
	generation, present := p.groups[group]
	if !present {
		generation = len(p.groups) + 1
		p.groups[group], p.attempts[generation] = generation, make(map[int]int)
	}
	p.attempts[generation][lane]++
	attempt := p.attempts[generation][lane]
	p.mu.Unlock()
	if generation > 3 || lane > 2 || attempt > 2 || (lane == 2 && attempt != 1) {
		return nil, fmt.Errorf("unexpected reconnect attempt generation=%d lane=%d attempt=%d", generation, lane, attempt)
	}
	if lane == 0 && attempt == 2 {
		// This is a protocol-valid legacy downgrade: the exact group and lane
		// acknowledgment remain present, while aggregate negotiation is absent.
		// Malformed acknowledgments must retry within the same generation and
		// are intentionally covered by the separate tunnel-lane protocol tests.
		return rollbackUpgradeResponse(r, false, "", true, nil)
	}
	if lane == 1 && attempt == 2 {
		close(p.blocked[generation-1])
		select {
		case <-p.resume[generation-1]:
		case <-r.Context().Done():
			return nil, r.Context().Err()
		}
	}
	ready := make(chan *yamux.Session, 1)
	response, err := rollbackUpgradeResponse(r, true, role, true, ready)
	if err != nil {
		return nil, err
	}
	session := <-ready
	p.mu.Lock()
	p.peers = append(p.peers, session)
	p.mu.Unlock()
	p.ready <- reconnectWorkloadPeer{generation, lane, attempt, session}
	return response, nil
}

func (p *reconnectWorkloadTransport) closePeers() {
	p.mu.Lock()
	peers := append([]*yamux.Session(nil), p.peers...)
	p.mu.Unlock()
	for _, peer := range peers {
		_ = peer.Close()
	}
}

type reconnectWorkloadPlan struct {
	prefix, tail []byte
	release      chan struct{}
	done         chan struct{}
	calls        atomic.Int32
	canceled     atomic.Bool
}

type reconnectWorkloadRead struct {
	body []byte
	err  error
}

type reconnectWorkloadTransfer struct {
	stream net.Conn
	result chan reconnectWorkloadRead
	joined chan struct{}
	plan   *reconnectWorkloadPlan
}

// This composes the agent connector, real Yamux sessions and real upstream TCP
// relays. It does not qualify the public listener, server generation fencing or
// observability event attribution. Aggregate snapshots prove whole-boundary
// ownership only; they cannot identify the release of an individual lease.
func TestRealYamuxRepeatedReconnectWithActiveTransfers(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	transport := newReconnectWorkloadTransport()
	usage := sysmetrics.MemoryUsage{UsedBytes: 64 << 20, LimitBytes: 4 << 30, Source: "test"}
	capacity := newTestAgentAdaptiveCapacity(t, &usage)
	capacity.forceRefresh()
	plans := make(map[string]*reconnectWorkloadPlan)
	for generation := 1; generation <= 3; generation++ {
		for _, phase := range []string{"failed-lane", "survivor", "fresh-lane", "cancel", "generation-one", "generation-two"} {
			id := fmt.Sprintf("%d/%s", generation, phase)
			plans[id] = &reconnectWorkloadPlan{prefix: bytes.Repeat([]byte(id+"/prefix\n"), 256), tail: bytes.Repeat([]byte(id+"/tail\n"), 256), release: make(chan struct{}), done: make(chan struct{})}
		}
	}
	stop := make(chan struct{})
	originErrors := make(chan error, 32)
	origin := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		plan := plans[strings.TrimPrefix(r.URL.Path, "/")]
		if plan == nil || r.Method != http.MethodGet || plan.calls.Add(1) != 1 {
			originErrors <- fmt.Errorf("unexpected or duplicate origin request %s %s", r.Method, r.URL.Path)
			http.Error(w, "unexpected request", http.StatusBadRequest)
			return
		}
		defer close(plan.done)
		w.Header().Set("Content-Length", strconv.Itoa(len(plan.prefix)+len(plan.tail)))
		w.Header().Set("Connection", "close")
		if _, err := w.Write(plan.prefix); err != nil {
			originErrors <- fmt.Errorf("origin prefix: %w", err)
			return
		}
		w.(http.Flusher).Flush()
		select {
		case <-plan.release:
			if _, err := w.Write(plan.tail); err != nil {
				originErrors <- fmt.Errorf("origin completed tail: %w", err)
			}
		case <-r.Context().Done():
			plan.canceled.Store(true)
		case <-stop:
		}
	}))
	origin.Config.WriteTimeout = reconnectWorkloadWait
	origin.Start()
	var mu sync.Mutex
	seenGroups := make(map[string]struct{})
	connected, disconnected := make(map[*yamux.Session]int), make(map[*yamux.Session]int)
	var boundaryErrors []string
	observer := func(event tunnelSessionObservation) {
		mu.Lock()
		defer mu.Unlock()
		if event.Connected {
			connected[event.Session]++
			if !event.Aggregate || event.Session.IsClosed() {
				boundaryErrors = append(boundaryErrors, "connected session was closed or not aggregate")
			}
			return
		}
		disconnected[event.Session]++
		if !event.Session.IsClosed() {
			boundaryErrors = append(boundaryErrors, "disconnect published before session close")
		}
		if event.Lane > 0 {
			pool := event.Session.AggregateReceiveWindowStats()
			if !pool.Closed || pool.TotalUnits != 0 || pool.BufferedUnits != 0 || pool.PendingRefund != 0 {
				boundaryErrors = append(boundaryErrors, fmt.Sprintf("disconnected pool still owns memory: %+v", pool))
			}
		}
	}
	connect := func(groupCtx context.Context, lane tunnelLaneRegistration) error {
		mu.Lock()
		if _, present := seenGroups[lane.group]; !present {
			seenGroups[lane.group] = struct{}{}
			snapshot := capacity.snapshot()
			if snapshot.InUse != 0 || snapshot.ControlInUse != 0 || snapshot.SessionResourceBytes != 0 || snapshot.ReceiveWindowBytes != 0 || len(connected) != len(disconnected) {
				boundaryErrors = append(boundaryErrors, fmt.Sprintf("generation entry retains old ownership: %+v, sessions=%d/%d", snapshot, len(connected), len(disconnected)))
			}
		}
		mu.Unlock()
		return connectAndServe(groupCtx, &http.Client{Transport: transport}, "https://management.test/agent/tunnel", "reconnect-agent", "reconnect-agent", "token", nil, 2<<20, 64, false, capacity, lane)
	}
	done, loopJoined := make(chan error, 1), make(chan struct{})
	go func() {
		defer close(loopJoined)
		done <- runTunnelGroupGenerations(ctx, "https://management.test/agent/tunnel", 2, connect, func(waitCtx context.Context, _ time.Duration) error { return waitCtx.Err() }, observer)
	}()
	var transfers []*reconnectWorkloadTransfer
	t.Cleanup(func() {
		cancel()
		close(stop)
		for _, transfer := range transfers {
			_ = transfer.stream.Close()
		}
		transport.closePeers()
		reconnectWorkloadAwait(t, "connector loop cleanup", loopJoined)
		for _, transfer := range transfers {
			reconnectWorkloadAwait(t, "response reader cleanup", transfer.joined)
			reconnectWorkloadAwait(t, "origin handler cleanup", transfer.plan.done)
		}
		origin.Close()
	})
	start := func(session *yamux.Session, id string) *reconnectWorkloadTransfer {
		t.Helper()
		stream, err := session.Open()
		if err != nil {
			t.Fatal(err)
		}
		transfer := &reconnectWorkloadTransfer{stream: stream, result: make(chan reconnectWorkloadRead, 1), joined: make(chan struct{}), plan: plans[id]}
		// Register every opened stream before a synchronous read can fail.
		// Only readers actually launched below are included in cleanup joins.
		t.Cleanup(func() { _ = stream.Close() })
		_ = stream.SetDeadline(time.Now().Add(reconnectWorkloadWait))
		if err := tunnel.WriteOpenRequest(stream, tunnel.NewOpenRequest(id, "tcp", origin.Listener.Addr().String())); err != nil {
			t.Fatal(err)
		}
		if response, err := tunnel.ReadOpenResponse(stream); err != nil || !response.OK {
			t.Fatalf("Open %s: %+v %v", id, response, err)
		}
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, origin.URL+"/"+id, nil)
		if err != nil {
			t.Fatal(err)
		}
		if err := request.Write(stream); err != nil {
			t.Fatal(err)
		}
		response, err := http.ReadResponse(bufio.NewReader(stream), request)
		if err != nil {
			t.Fatal(err)
		}
		prefix := make([]byte, len(transfer.plan.prefix))
		if response.StatusCode != http.StatusOK || response.ContentLength != int64(len(prefix)+len(transfer.plan.tail)) {
			t.Fatalf("unexpected committed response %s: %s length=%d", id, response.Status, response.ContentLength)
		}
		if _, err := io.ReadFull(response.Body, prefix); err != nil || !bytes.Equal(prefix, transfer.plan.prefix) {
			t.Fatalf("response prefix %s: %v", id, err)
		}
		transfers = append(transfers, transfer)
		go func() {
			defer close(transfer.joined)
			defer stream.Close()
			defer response.Body.Close()
			body, err := io.ReadAll(io.LimitReader(response.Body, int64(len(transfer.plan.tail)+1)))
			transfer.result <- reconnectWorkloadRead{body, err}
		}()
		return transfer
	}
	completed, interrupted := 0, 0
	finish := func(transfer *reconnectWorkloadTransfer, success bool) {
		t.Helper()
		select {
		case result := <-transfer.result:
			if success {
				if result.err != nil || !bytes.Equal(result.body, transfer.plan.tail) {
					t.Fatalf("successful response suffix bytes=%d: %v", len(result.body), result.err)
				}
			} else if len(result.body) != 0 || result.err == nil || isTimeoutError(result.err) {
				t.Fatalf("failed committed response must terminate without withheld tail or timeout: bytes=%d error=%v", len(result.body), result.err)
			}
		case <-time.After(reconnectWorkloadWait):
			t.Fatal("response reader did not terminate")
		}
		reconnectWorkloadAwait(t, "response reader", transfer.joined)
		reconnectWorkloadAwait(t, "origin handler", transfer.plan.done)
		if transfer.plan.calls.Load() != 1 || transfer.plan.canceled.Load() == success {
			t.Fatalf("origin calls=%d canceled=%v successful=%v", transfer.plan.calls.Load(), transfer.plan.canceled.Load(), success)
		}
		if success {
			completed++
		} else {
			interrupted++
		}
	}
	receive := func(generation, lane, attempt int) *yamux.Session {
		t.Helper()
		select {
		case peer := <-transport.ready:
			if peer.generation != generation || peer.lane != lane || peer.attempt != attempt {
				t.Fatalf("unexpected ready peer: %+v, want %d/%d/%d", peer, generation, lane, attempt)
			}
			return peer.session
		case <-time.After(reconnectWorkloadWait):
			t.Fatalf("peer %d/%d/%d did not connect", generation, lane, attempt)
			return nil
		}
	}
	var previous []*yamux.Session
	for generation := 1; generation <= 3; generation++ {
		control := receive(generation, 0, 1)
		var bulk [3]*yamux.Session
		// Bulk worker scheduling is deliberately unordered.
		for range 2 {
			select {
			case peer := <-transport.ready:
				if peer.generation != generation || peer.lane < 1 || peer.lane > 2 || peer.attempt != 1 || bulk[peer.lane] != nil {
					t.Fatalf("unexpected initial bulk peer: %+v", peer)
				}
				bulk[peer.lane] = peer.session
			case <-time.After(reconnectWorkloadWait):
				t.Fatal("initial bulk lanes did not connect")
			}
		}
		for _, old := range previous {
			if !old.IsClosed() || old == control || old == bulk[1] || old == bulk[2] {
				t.Fatal("generation replacement retained an old peer identity")
			}
		}
		id := func(phase string) string { return fmt.Sprintf("%d/%s", generation, phase) }
		failed, survivor := start(bulk[1], id("failed-lane")), start(bulk[2], id("survivor"))
		old := bulk[1]
		_ = old.Close()
		reconnectWorkloadAwait(t, "bulk reconnect gate", transport.blocked[generation-1])
		finish(failed, false)
		// The other bulk lane must deliver useful bytes while replacement is
		// blocked, without acquiring a fresh session identity.
		close(survivor.plan.release)
		finish(survivor, true)
		if bulk[2].IsClosed() || control.IsClosed() {
			t.Fatal("independent lane reconnect closed a surviving session")
		}
		close(transport.resume[generation-1])
		bulk[1] = receive(generation, 1, 2)
		if bulk[1] == old || bulk[1].IsClosed() {
			t.Fatal("bulk reconnect did not create a fresh live identity")
		}
		fresh := start(bulk[1], id("fresh-lane"))
		close(fresh.plan.release)
		finish(fresh, true)
		canceled := start(bulk[2], id("cancel"))
		_ = canceled.stream.Close()
		finish(canceled, false)
		first, second := start(bulk[1], id("generation-one")), start(bulk[2], id("generation-two"))
		previous = append(previous, control, old, bulk[1], bulk[2])
		if generation < 3 {
			_ = control.Close()
		} else {
			cancel()
		}
		finish(first, false)
		finish(second, false)
	}
	reconnectWorkloadAwait(t, "final connector join", loopJoined)
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("connector loop shutdown: %v", err)
		}
	case <-time.After(reconnectWorkloadWait):
		t.Fatal("connector loop result did not arrive")
	}
	mu.Lock()
	if len(seenGroups) != 3 || len(connected) != 12 || len(disconnected) != 12 {
		t.Errorf("generations=%d session identities connected/disconnected=%d/%d", len(seenGroups), len(connected), len(disconnected))
	}
	for session, count := range connected {
		if count != 1 || disconnected[session] != 1 {
			t.Errorf("session %p connected/disconnected=%d/%d", session, count, disconnected[session])
		}
	}
	if len(boundaryErrors) != 0 {
		t.Errorf("ownership boundary errors: %v", boundaryErrors)
	}
	mu.Unlock()
	snapshot := capacity.snapshot()
	if snapshot.InUse != 0 || snapshot.ControlInUse != 0 || snapshot.ControlReservedSlots != 0 || snapshot.ControlReservedBaseBytes != 0 || snapshot.ControlReservedBaseFDs != 0 || snapshot.ReceiveWindowBytes != 0 || snapshot.SessionResourceBytes != 0 || snapshot.AggregateInitialPoolBytes != 0 || snapshot.AggregateGrowthPoolBytes != 0 || snapshot.AggregateCurrentPoolBytes != 0 || snapshot.AggregateSessionMetadataBytes != 0 || snapshot.LegacyControlSupplementBytes != 0 {
		t.Fatalf("joined connector retained capacity ownership: %+v", snapshot)
	}
	select {
	case err := <-originErrors:
		t.Fatal(err)
	default:
	}
	if len(transfers) != 18 || completed != 6 || interrupted != 12 {
		t.Fatalf("terminal transfers=%d completed=%d interrupted=%d", len(transfers), completed, interrupted)
	}
	if !t.Failed() {
		t.Logf("reconnect_workload generations=%d sessions=%d transfers=%d completed=%d interrupted=%d terminal_ownership=zero", len(seenGroups), len(connected), len(transfers), completed, interrupted)
	}
}

func reconnectWorkloadAwait(t *testing.T, name string, done <-chan struct{}) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(reconnectWorkloadWait):
		t.Errorf("timed out joining %s", name)
	}
}
