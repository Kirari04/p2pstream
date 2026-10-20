package server

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha1"
	"database/sql"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	p2pstreamv1 "p2pstream/gen/proto/p2pstream/v1"
	"p2pstream/internal/db"
)

const webSocketWorkloadGUID = "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"

type webSocketWorkloadFrames struct {
	Frames, DataFrames, DataBytes, Ping, Pong, Close, PayloadBytes int
}

func (c *webSocketWorkloadFrames) add(op byte, size int) {
	c.Frames++
	c.PayloadBytes += size
	switch op {
	case 1, 2:
		c.DataFrames++
		c.DataBytes += size
	case 8:
		c.Close++
	case 9:
		c.Ping++
	case 10:
		c.Pong++
	}
}

func (c *webSocketWorkloadFrames) sum(other webSocketWorkloadFrames) {
	c.Frames += other.Frames
	c.DataFrames += other.DataFrames
	c.DataBytes += other.DataBytes
	c.Ping += other.Ping
	c.Pong += other.Pong
	c.Close += other.Close
	c.PayloadBytes += other.PayloadBytes
}

type webSocketWorkloadPeer struct {
	conn    net.Conn
	reader  *bufio.Reader
	client  bool
	written webSocketWorkloadFrames
	read    webSocketWorkloadFrames
}

// FIN-only, fixed-size fixture codec. Bounds precede allocation; this is not a
// general WebSocket implementation, extension/fragmentation or compliance test.
func (p *webSocketWorkloadPeer) write(op byte, payload []byte) error {
	if len(payload) > 4096 || (op >= 8 && len(payload) > 125) {
		return errors.New("fixture frame exceeds bound")
	}
	frame := []byte{0x80 | op}
	maskBit := byte(0)
	if p.client {
		maskBit = 0x80
	}
	if len(payload) < 126 {
		frame = append(frame, maskBit|byte(len(payload)))
	} else {
		frame = append(frame, maskBit|126, byte(len(payload)>>8), byte(len(payload)))
	}
	var mask [4]byte
	if p.client {
		if _, err := rand.Read(mask[:]); err != nil {
			return err
		}
		frame = append(frame, mask[:]...)
	}
	for i, value := range payload {
		if p.client {
			value ^= mask[i%4]
		}
		frame = append(frame, value)
	}
	n, err := p.conn.Write(frame)
	if err != nil {
		return err
	}
	if n != len(frame) {
		return io.ErrShortWrite
	}
	p.written.add(op, len(payload))
	return nil
}

func (p *webSocketWorkloadPeer) frame() (byte, []byte, error) {
	var header [2]byte
	if _, err := io.ReadFull(p.reader, header[:]); err != nil {
		return 0, nil, err
	}
	op := header[0] & 15
	if header[0]&0xf0 != 0x80 || (op != 1 && op != 2 && op != 8 && op != 9 && op != 10) || (header[1]&0x80 != 0) == p.client {
		return 0, nil, errors.New("invalid fixture frame flags/opcode/masking")
	}
	size := int(header[1] & 127)
	if size == 127 {
		return 0, nil, errors.New("64-bit fixture frame length rejected")
	}
	if size == 126 {
		var extended [2]byte
		if _, err := io.ReadFull(p.reader, extended[:]); err != nil {
			return 0, nil, err
		}
		size = int(binary.BigEndian.Uint16(extended[:]))
		if size < 126 {
			return 0, nil, errors.New("noncanonical fixture length")
		}
	}
	if size > 4096 || (op >= 8 && size > 125) {
		return 0, nil, errors.New("received fixture frame exceeds bound")
	}
	var mask [4]byte
	if !p.client {
		if _, err := io.ReadFull(p.reader, mask[:]); err != nil {
			return 0, nil, err
		}
	}
	payload := make([]byte, size)
	if _, err := io.ReadFull(p.reader, payload); err != nil {
		return 0, nil, err
	}
	if !p.client {
		for i := range payload {
			payload[i] ^= mask[i%4]
		}
	}
	p.read.add(op, size)
	return op, payload, nil
}

func (p *webSocketWorkloadPeer) expect(op byte, payload []byte) error {
	gotOp, got, err := p.frame()
	if err != nil || gotOp != op || !bytes.Equal(got, payload) {
		return fmt.Errorf("frame opcode=%d/%d payload=%d/%d equal=%v err=%v", gotOp, op, len(got), len(payload), bytes.Equal(got, payload), err)
	}
	return nil
}

func webSocketWorkloadTerminated(err error) bool {
	// A reset/closed connection is also a bounded peer termination, never a
	// successful close exchange. Partial frame/timeout errors are not accepted.
	return errors.Is(err, io.EOF) || errors.Is(err, net.ErrClosed) || errors.Is(err, syscall.ECONNRESET)
}

type webSocketWorkloadSockets struct {
	mu      sync.Mutex
	stopped bool
	live    map[net.Conn]func()
}

func (s *webSocketWorkloadSockets) own(conn net.Conn) func() {
	var once sync.Once
	closeConn := func() {
		once.Do(func() {
			_ = conn.Close()
			s.mu.Lock()
			delete(s.live, conn)
			s.mu.Unlock()
		})
	}
	s.mu.Lock()
	stopped := s.stopped
	if !stopped {
		s.live[conn] = closeConn
	}
	s.mu.Unlock()
	if stopped {
		closeConn()
	}
	return closeConn
}

func (s *webSocketWorkloadSockets) closeAll() {
	s.mu.Lock()
	s.stopped = true
	callbacks := make([]func(), 0, len(s.live))
	for _, closeConn := range s.live {
		callbacks = append(callbacks, closeConn)
	}
	s.mu.Unlock()
	for _, closeConn := range callbacks {
		closeConn()
	}
}

type webSocketWorkloadRequest struct {
	mode         string
	calls        atomic.Int64
	done         chan struct{}
	clientFrames webSocketWorkloadPeer
	originFrames webSocketWorkloadPeer
}

func webSocketWorkloadAccept(key string) string {
	sum := sha1.Sum([]byte(key + webSocketWorkloadGUID))
	return base64.StdEncoding.EncodeToString(sum[:])
}

func webSocketWorkloadOrigin(peer *webSocketWorkloadPeer, mode string, binaryPayload, closePayload []byte) error {
	if err := peer.write(1, []byte("hello")); err != nil {
		return err
	}
	for _, item := range []struct {
		op           byte
		in, response []byte
	}{
		{1, []byte("request"), []byte("reply")}, {2, binaryPayload, binaryPayload}, {9, []byte("c"), []byte("c")},
	} {
		if err := peer.expect(item.op, item.in); err != nil {
			return err
		}
		op := item.op
		if op == 9 {
			op = 10
		}
		if err := peer.write(op, item.response); err != nil {
			return err
		}
	}
	if err := peer.write(9, []byte("s")); err != nil {
		return err
	}
	if err := peer.expect(10, []byte("s")); err != nil {
		return err
	}
	// Acknowledges the complete client data/control sequence before an abort.
	if err := peer.write(1, []byte("ready")); err != nil {
		return err
	}
	if mode == "abort" || mode == "half-close" {
		_, _, err := peer.frame()
		if !webSocketWorkloadTerminated(err) {
			return fmt.Errorf("client stop did not terminate origin: %v", err)
		}
		return nil
	}
	if mode == "origin-close" {
		if err := peer.write(8, closePayload); err != nil {
			return err
		}
		return peer.expect(8, closePayload)
	}
	if err := peer.expect(8, closePayload); err != nil {
		return err
	}
	return peer.write(8, closePayload)
}

func TestPublicFeatureWebSocketWorkload(t *testing.T) {
	var app *App
	var agents [2]*AgentConn
	ok := t.Run("workload", func(t *testing.T) {
		f := newPeerFairnessFixture(t)
		app = f.app
		agents = [2]*AgentConn{f.agent, mixedSaturationAddAgent(t, f)}
		budget := app.agentStreamCapacity.snapshot()
		if budget.Total.Capacity != 3 || budget.Public.Capacity != 2 || budget.Pooled.Capacity != 1 || budget.Control.Capacity != 1 {
			t.Fatal("unexpected two-worker WebSocket envelope")
		}
		app.publicProxyRequests = newRequestCapacityLimiter(8, 8)
		app.publicTargetRequests = newKeyedRequestCapacityLimiter(8, 8)
		app.publicClientRequests = newKeyedStringRequestCapacityLimiter(8)
		app.PublicCache = newPublicProxyCache(t.TempDir())
		work, cancelWork := context.WithTimeout(t.Context(), 45*time.Second)
		t.Cleanup(cancelWork)
		sockets := &webSocketWorkloadSockets{live: make(map[net.Conn]func())}
		t.Cleanup(sockets.closeAll)
		var requests [10]webSocketWorkloadRequest
		for id, mode := range []string{"client-close", "client-close", "origin-close", "origin-close", "abort", "half-close", "reject", "reject", "deny", "deny"} {
			requests[id].mode, requests[id].done = mode, make(chan struct{})
		}
		binaryPayload := bytes.Repeat([]byte{0, 1, 0x7f, 0xff}, 1024)
		closePayload := []byte{3, 232, 'd', 'o', 'n', 'e'} // 1000 / done
		originErrors := make(chan error, 64)
		var originCalls atomic.Int64
		var originWorkers sync.WaitGroup
		origin := httptest.NewUnstartedServer(nil)
		authority := origin.Listener.Addr().String()
		origin.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			originWorkers.Add(1)
			defer originWorkers.Done()
			originCalls.Add(1)
			bad := func(err error) { originErrors <- err; http.Error(w, "fixture origin error", 500) }
			id, err := strconv.Atoi(r.Header.Get("X-Workload-Id"))
			if err != nil || id < 0 || id >= len(requests) {
				bad(errors.New("invalid untrusted WebSocket fixture ID"))
				return
			}
			item := &requests[id]
			if item.calls.Add(1) != 1 || item.mode == "deny" {
				bad(errors.New("extra/denied WebSocket origin attempt"))
				return
			}
			defer close(item.done)
			key := r.Header.Get("Sec-WebSocket-Key")
			nonce, decodeErr := base64.StdEncoding.DecodeString(key)
			if r.Method != "GET" || r.ProtoMajor != 1 || r.URL.RequestURI() != "/ws" || r.Host != authority ||
				!headerHasToken(r.Header, "Connection", "upgrade") || r.Header.Get("Upgrade") != "websocket" ||
				r.Header.Get("Sec-WebSocket-Version") != "13" || decodeErr != nil || len(nonce) != 16 ||
				r.Header.Get("X-Forwarded-For") != "127.0.0.1" || r.Header.Get("X-Real-Ip") != "127.0.0.1" ||
				r.Header.Get("X-Forwarded-Host") != "websocket.example.test" || r.Header.Get("X-Forwarded-Proto") != "http" {
				bad(errors.New("WebSocket origin handshake/trusted forwarding differs"))
				return
			}
			for _, name := range []string{"Forwarded", "Cf-Connecting-Ip", "X-Auth-Request-User", "X-Workload-Hop"} {
				if r.Header.Get(name) != "" {
					bad(fmt.Errorf("retained spoofed/hop header %s", name))
					return
				}
			}
			if item.mode == "reject" {
				w.Header().Set("Connection", "close")
				w.WriteHeader(http.StatusServiceUnavailable)
				_, _ = io.WriteString(w, "retry\n")
				return
			}
			conn, rw, err := w.(http.Hijacker).Hijack()
			if err != nil {
				originErrors <- err
				return
			}
			// A hijacked socket is owned before any further assertion/write.
			closeConn := sockets.own(conn)
			defer closeConn()
			if err := conn.SetDeadline(time.Now().Add(mixedSaturationWait)); err != nil {
				originErrors <- err
				return
			}
			if _, err := fmt.Fprintf(rw, "HTTP/1.1 101 Switching Protocols\r\nConnection: Upgrade\r\nUpgrade: websocket\r\nSec-WebSocket-Accept: %s\r\n\r\n", webSocketWorkloadAccept(key)); err != nil {
				originErrors <- err
				return
			}
			if err := rw.Flush(); err != nil {
				originErrors <- err
				return
			}
			peer := webSocketWorkloadPeer{conn: conn, reader: rw.Reader}
			if err := webSocketWorkloadOrigin(&peer, item.mode, binaryPayload, closePayload); err != nil {
				originErrors <- fmt.Errorf("origin ID %d: %w", id, err)
			}
			// Retain counts only, never sockets/readers past the handler join.
			item.originFrames = webSocketWorkloadPeer{written: peer.written, read: peer.read}
		})
		origin.Start()
		t.Cleanup(func() {
			// Stop accepts and close tracked hijacked sockets before waiting.
			// httptest.Close alone does not join hijacked handlers.
			sockets.closeAll()
			origin.Close()
			done := make(chan struct{})
			go func() { originWorkers.Wait(); close(done) }()
			select {
			case <-done:
			case <-time.After(mixedSaturationWait):
				t.Error("WebSocket origin handlers did not join")
			}
		})
		target := f.target
		target.RouteID, target.Weight = 10, 100
		target.URL, target.ParsedURL = origin.URL, mustParseAggregateTestURL(t, origin.URL)
		target.AgentLoadBalancing = publicRouteTargetLoadBalancingRoundRobin
		target.AgentSelector = publicAgentSelectorConfig{MatchLabels: map[string]string{"websocket-workload": "yes"}}
		cache, retry, shaper := webSocketWorkloadRules(t, app)
		listener := publicListenerConfig{ID: 1, Name: "websocket-workload", BindAddress: "127.0.0.1", Protocol: publicListenerProtocolHTTP, Enabled: true}
		waf := testWafRule(71, publicWafActionBlock)
		waf.Match = mustPublicPolicyMatchCEL(t, `headers["x-workload-mode"].exists(v, v == "deny")`)
		waf.Fingerprint = publicWafRuleFingerprint(waf)
		settings := defaultPublicCacheSettings()
		snapshot := &publicProxySnapshot{
			Listeners: map[int64]publicListenerConfig{1: listener},
			RoutesByListener: map[int64][]publicRouteConfig{1: {{ID: 10, ListenerID: 1, Enabled: true,
				HostPattern: "websocket.example.test", PathPrefix: "/", PathSecurityMode: publicRoutePathSecurityModeStrict,
				Action: publicRouteActionForward, TargetLoadBalancing: publicRouteTargetLoadBalancingRoundRobin,
				Targets: []publicRouteTargetConfig{target}}}},
			RouteTargets: map[int64]publicRouteTargetConfig{target.ID: target}, Agents: make(map[int64]publicAgentConfig),
			WafRules: []publicWafRuleConfig{waf}, RetryRules: []publicRetryRuleConfig{retry}, TrafficShaperRules: []publicTrafficShaperRuleConfig{shaper},
			CacheSettings: settings, CacheRules: []publicCacheRuleConfig{cache}, CacheFingerprint: publicCacheRuntimeFingerprint(settings, []publicCacheRuleConfig{cache}),
			AccessHeaderNames: []string{"X-Auth-Request-User"},
		}
		for _, agent := range agents {
			snapshot.Agents[agent.AgentID] = publicAgentConfig{ID: agent.AgentID, PublicID: agent.PublicID, Enabled: true, Labels: map[string]string{"websocket-workload": "yes"}}
		}
		setPublicSnapshotForTest(t, app, snapshot)
		app.PublicCache.reconcile(settings, snapshot.CacheRules)
		app.PublicWAF.reconcile(snapshot)
		app.TrafficShaper.reconcile(snapshot)
		app.TargetHealth.reconcile(app, snapshot, false)
		if _, err := app.startPublicListenerFromSnapshot(listener, snapshot); err != nil {
			t.Fatal(err)
		}
		app.proxyMu.Lock()
		address := app.publicListenerState[1].BoundAddress
		app.proxyMu.Unlock()
		closed := false
		closeRuntime := func() {
			if closed {
				return
			}
			closed = true
			cancelWork()
			sockets.closeAll()
			ctx, cancel := context.WithTimeout(context.Background(), mixedSaturationWait)
			defer cancel()
			if _, err := app.stopPublicListenerRuntime(ctx, 1); err != nil {
				t.Error(err)
			}
			app.CloseAgentTransports()
			if err := app.CloseObservabilityRecorder(ctx); err != nil {
				t.Error(err)
			}
		}
		t.Cleanup(closeRuntime)
		var clientWritten, originWritten webSocketWorkloadFrames
		for phase, name := range []string{"client-close", "origin-close", "client-stop", "reject", "deny"} {
			results := make(chan error, 2)
			var workers sync.WaitGroup
			for id := phase * 2; id < phase*2+2; id++ {
				workers.Go(func() {
					results <- webSocketWorkloadClient(work, address, id, &requests[id], sockets, binaryPayload, closePayload, app, agents)
				})
			}
			done := make(chan struct{})
			go func() { workers.Wait(); close(done) }()
			t.Cleanup(func() {
				cancelWork()
				sockets.closeAll()
				select {
				case <-done:
				case <-time.After(mixedSaturationWait):
					t.Error("WebSocket clients did not join")
				}
			})
			select {
			case <-done:
			case <-work.Done():
				t.Fatal(work.Err())
			}
			for range 2 {
				if err := <-results; err != nil {
					t.Fatalf("phase %s: %v", name, err)
				}
			}
			for id := phase * 2; id < phase*2+2; id++ {
				item := &requests[id]
				want := int64(1)
				if name == "deny" {
					want = 0
				} else {
					mixedSaturationAwaitChannel(t, "WebSocket origin joined", item.done)
				}
				if item.calls.Load() != want || item.clientFrames.written != item.originFrames.read || item.clientFrames.read != item.originFrames.written {
					t.Fatalf("ID %d origin attempts/counts differ: calls=%d/%d client=%+v origin=%+v", id, item.calls.Load(), want, item.clientFrames, item.originFrames)
				}
				clientWritten.sum(item.clientFrames.written)
				originWritten.sum(item.originFrames.written)
			}
			select {
			case err := <-originErrors:
				t.Fatal(err)
			default:
			}
			mixedSaturationAwait(t, "WebSocket HTTP owners drained", func() bool {
				return sustainedIdle(app, agents, [2]publicRouteTargetConfig{target, target}) && app.TargetHealth.activeRequests(target.ID) == 0 && app.PublicWAF.proxyActiveRequests.Load() == 0 && app.retryReplayBudget.used.Load() == 0
			})
			webSocketWorkloadEvents(t, app, name, phase*2, target.ID, waf.ID, agents)
			assertPublicCacheStorageStats(t, app, 0, 0, 0)
			t.Logf("websocket_phase name=%s requests=2 events_total=%d origin_total=%d", name, phase*2+2, originCalls.Load())
		}
		if originCalls.Load() != 8 || clientWritten != (webSocketWorkloadFrames{28, 12, 24618, 6, 6, 4, 24654}) || originWritten != (webSocketWorkloadFrames{40, 24, 24666, 6, 6, 4, 24702}) {
			t.Fatalf("WebSocket aggregate origin=%d client=%+v origin=%+v", originCalls.Load(), clientWritten, originWritten)
		}
		closeRuntime()
		mixedSaturationAwait(t, "WebSocket transports drained", func() bool {
			s := app.agentStreamCapacity.snapshot()
			sockets.mu.Lock()
			live := len(sockets.live)
			sockets.mu.Unlock()
			return live == 0 && sustainedIdle(app, agents, [2]publicRouteTargetConfig{target, target}) && app.AgentTransports.len() == 0 && s.Total.InUse == 0 && s.ActiveLeases == 0 && s.Waiters == 0 && s.Granted == s.Released
		})
	})
	if !ok || app == nil {
		return
	}
	mixedSaturationAwait(t, "WebSocket joined peers refund all owners", func() bool {
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
			if app.AgentHub.connectedByID(agent.AgentID) != nil || agent.ActiveRequests.Load() != 0 || agent.tunnelResourceSnapshot().SessionResourceBytes != 0 {
				return false
			}
		}
		return true
	})
	if err := app.agentStreamCapacity.validateInvariants(); err != nil {
		t.Fatal(err)
	}
	sustainedRecorderDrained(t, app)
	r := app.observabilityRecorder
	if r.pendingTouches.Load() != 0 || r.droppedTouches.Load() != 0 || len(r.touches) != 0 {
		t.Fatal("WebSocket recorder retained/dropped touches")
	}
	t.Log("websocket_terminal requests=10 events=10 origin=8 upgraded=6 close_exchanges=4 aborted=1 half_closed=1 upstream_rejected=2 waf_denied=2 client_frames=28 origin_frames=40 client_data_bytes=24618 origin_data_bytes=24666 client_control_bytes=36 origin_control_bytes=36 http_response_bytes=28 cache_entries=0 retries=0 health=0 capacity_rejected=0 joined_agents=2 all_owners=0 recorder_pending=0 recorder_dropped=0")
}

func webSocketWorkloadClient(work context.Context, address string, id int, item *webSocketWorkloadRequest, sockets *webSocketWorkloadSockets, binaryPayload, closePayload []byte, app *App, agents [2]*AgentConn) error {
	ctx, cancel := context.WithTimeout(work, mixedSaturationWait)
	defer cancel()
	conn, err := (&net.Dialer{}).DialContext(ctx, "tcp", address)
	if err != nil {
		return err
	}
	closeConn := sockets.own(conn)
	defer closeConn()
	if err := conn.SetDeadline(time.Now().Add(mixedSaturationWait)); err != nil {
		return err
	}
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return err
	}
	key := base64.StdEncoding.EncodeToString(nonce[:])
	mode := "allow"
	if item.mode == "deny" {
		mode = "deny"
	}
	_, err = fmt.Fprintf(conn, "GET /ws HTTP/1.1\r\nHost: websocket.example.test\r\nConnection: keep-alive, Upgrade, X-Workload-Hop\r\nUpgrade: websocket\r\nSec-WebSocket-Version: 13\r\nSec-WebSocket-Key: %s\r\nX-Workload-Id: %d\r\nX-Workload-Mode: %s\r\nForwarded: for=198.51.100.90\r\nX-Forwarded-For: 198.51.100.91\r\nX-Real-Ip: 198.51.100.92\r\nCf-Connecting-Ip: 198.51.100.93\r\nX-Auth-Request-User: forged-principal\r\nX-Workload-Hop: remove-me\r\n\r\n", key, id, mode)
	if err != nil {
		return err
	}
	reader := bufio.NewReader(conn)
	resp, err := http.ReadResponse(reader, &http.Request{Method: "GET"})
	if err != nil {
		return err
	}
	if item.mode == "reject" || item.mode == "deny" {
		closeBody := ownedSaturationBodyClose(resp.Body)
		defer closeBody()
		status, payload := 503, []byte("retry\n")
		if item.mode == "deny" {
			status, payload = 403, []byte("blocked\n")
		}
		got, err := io.ReadAll(io.LimitReader(resp.Body, int64(len(payload)+1)))
		if err != nil || resp.StatusCode != status || !bytes.Equal(got, payload) || resp.Header.Get("X-P2pstream-Cache") != "" {
			return fmt.Errorf("HTTP rejection status=%d/%d payload=%q err=%v", resp.StatusCode, status, got, err)
		}
		return work.Err()
	}
	if resp.StatusCode != 101 || resp.ProtoMajor != 1 || !headerHasToken(resp.Header, "Connection", "upgrade") || resp.Header.Get("Upgrade") != "websocket" || resp.Header.Get("Sec-WebSocket-Accept") != webSocketWorkloadAccept(key) || resp.Header.Get("Sec-WebSocket-Extensions") != "" || resp.Header.Get("X-P2pstream-Cache") != "" {
		return fmt.Errorf("WebSocket public handshake differs: status=%d headers=%v", resp.StatusCode, resp.Header)
	}
	// Preserve the handshake reader: it may already contain the first frame.
	peer := webSocketWorkloadPeer{conn: conn, reader: reader, client: true}
	defer func() { item.clientFrames = webSocketWorkloadPeer{written: peer.written, read: peer.read} }()
	if err := peer.expect(1, []byte("hello")); err != nil {
		return err
	}
	for _, frame := range []struct {
		op      byte
		payload []byte
		wantOp  byte
		want    []byte
	}{{1, []byte("request"), 1, []byte("reply")}, {2, binaryPayload, 2, binaryPayload}, {9, []byte("c"), 10, []byte("c")}} {
		if err := peer.write(frame.op, frame.payload); err != nil {
			return err
		}
		if err := peer.expect(frame.wantOp, frame.want); err != nil {
			return err
		}
	}
	if err := peer.expect(9, []byte("s")); err != nil {
		return err
	}
	if err := peer.write(10, []byte("s")); err != nil {
		return err
	}
	if err := peer.expect(1, []byte("ready")); err != nil {
		return err
	}
	s := app.agentStreamCapacity.snapshot()
	if s.AdaptiveExternalReservationsByOwner[adaptiveResourceOwnerPublicResponse] == 0 || agents[0].ActiveRequests.Load()+agents[1].ActiveRequests.Load() == 0 || app.retryReplayBudget.used.Load() != 0 {
		return errors.New("live upgraded response lost ownership or retained replay memory")
	}
	if work.Err() != nil {
		return work.Err()
	}
	if item.mode == "abort" {
		closeConn()
		return nil
	}
	if item.mode == "half-close" {
		if err := conn.(*net.TCPConn).CloseWrite(); err != nil {
			return err
		}
	} else if item.mode == "client-close" {
		if err := peer.write(8, closePayload); err != nil {
			return err
		}
		if err := peer.expect(8, closePayload); err != nil {
			return err
		}
	} else {
		if err := peer.expect(8, closePayload); err != nil {
			return err
		}
		if err := peer.write(8, closePayload); err != nil {
			return err
		}
	}
	_, _, err = peer.frame()
	if !webSocketWorkloadTerminated(err) {
		return fmt.Errorf("WebSocket did not terminate without extra frames: %v", err)
	}
	return work.Err()
}

func webSocketWorkloadRules(t *testing.T, app *App) (publicCacheRuleConfig, publicRetryRuleConfig, publicTrafficShaperRuleConfig) {
	t.Helper()
	match, err := json.Marshal(mustPublicPolicyMatchCEL(t, `method == "GET" && host_match(host, "websocket.example.test") && path == "/ws"`))
	if err != nil {
		t.Fatal(err)
	}
	cacheRow, err := app.DB.CreatePublicCacheRule(t.Context(), db.CreatePublicCacheRuleParams{
		Name: "websocket-workload", Priority: 10, Enabled: 1, MatchJson: string(match), RouteIdsJson: "[]", TargetIdsJson: "[]",
		Scope: publicCacheScopeSelectedBackend, TtlMode: publicCacheTTLModeFixed, TtlMillis: 300000,
		QueryMode: publicCacheQueryModeFull, QueryParamsJson: "[]", VaryHeadersJson: "[]", CacheStatusCodesJson: "[200,503]", MaxObjectBytes: 8192, AddCacheStatusHeader: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	cache, err := publicCacheRuleRowToConfig(cacheRow)
	if err != nil {
		t.Fatal(err)
	}
	input, err := app.validatePublicRetryRuleInput(t.Context(), "websocket-workload", 10, true, []string{"GET"}, 1,
		p2pstreamv1.PublicRetryFailureMode_PUBLIC_RETRY_FAILURE_MODE_CONNECTION_FAILURES,
		p2pstreamv1.PublicRetryBodyMode_PUBLIC_RETRY_BODY_MODE_NEVER, 0,
		p2pstreamv1.PublicRetryResponseBodyMode_PUBLIC_RETRY_RESPONSE_BODY_MODE_BUFFERED, 8192, 1000, []int64{503}, nil, nil, nil, true)
	if err != nil {
		t.Fatal(err)
	}
	retryRow, err := app.DB.CreatePublicRetryRule(t.Context(), db.CreatePublicRetryRuleParams{
		Name: input.Name, Priority: input.Priority, Enabled: input.Enabled, MethodsJson: input.MethodsJSON, MaxRetries: input.MaxRetries,
		FailureMode: input.FailureMode, BodyMode: input.BodyMode, MaxReplayBodyBytes: input.MaxReplayBodyBytes,
		RetryStatusCodesJson: input.RetryStatusJSON, RouteIdsJson: input.RouteIDsJSON, TargetIdsJson: input.TargetIDsJSON, MatchJson: input.MatchJSON,
		ResponseBodyMode: input.ResponseBodyMode, MaxBufferedResponseBodyBytes: input.MaxBufferedResponseBodyBytes, MaxBufferedResponseWaitMillis: input.MaxBufferedResponseWaitMillis,
	})
	if err != nil {
		t.Fatal(err)
	}
	retry, err := publicRetryRuleRowToConfig(retryRow)
	if err != nil {
		t.Fatal(err)
	}
	shape, err := validatePublicTrafficShaperRuleInput("websocket-workload", 10, true,
		p2pstreamv1.PublicTrafficShaperBudgetScope_PUBLIC_TRAFFIC_SHAPER_BUDGET_SCOPE_PER_REQUEST,
		p2pstreamv1.PublicTrafficShaperProtocolScope_PUBLIC_TRAFFIC_SHAPER_PROTOCOL_SCOPE_WEBSOCKET_ONLY,
		1<<20, 1<<20, 1<<20, 0, 0, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	shapeRow, err := app.DB.CreatePublicTrafficShaperRule(t.Context(), db.CreatePublicTrafficShaperRuleParams{
		Name: shape.Name, Priority: shape.Priority, Enabled: shape.Enabled, BudgetScope: shape.BudgetScope, ProtocolScope: shape.ProtocolScope,
		UploadBytesPerSecond: shape.UploadBytesPerSecond, DownloadBytesPerSecond: shape.DownloadBytesPerSecond, BurstBytes: shape.BurstBytes,
		RequestExemptBytes: shape.RequestExemptBytes, ResponseExemptBytes: shape.ResponseExemptBytes, MatchJson: shape.MatchJSON, KeyPartsJson: shape.KeyPartsJSON,
	})
	if err != nil {
		t.Fatal(err)
	}
	shaper, err := publicTrafficShaperRuleRowToConfig(shapeRow)
	if err != nil {
		t.Fatal(err)
	}
	return cache, retry, shaper
}

func webSocketWorkloadEvents(t *testing.T, app *App, phase string, previous int, targetID, wafID int64, agents [2]*AgentConn) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), mixedSaturationWait)
	defer cancel()
	mixedSaturationAwait(t, "exact WebSocket terminal events", func() bool {
		if err := app.FlushObservabilityRecorder(ctx); err != nil {
			t.Fatal(err)
		}
		var count int
		if err := app.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM proxy_request_events`).Scan(&count); err != nil || count > previous+2 {
			t.Fatalf("WebSocket event count=%d/%d err=%v", count, previous+2, err)
		}
		return count == previous+2
	})
	rows, err := app.DB.QueryContext(ctx, `SELECT listener_id,route_id,route_target_id,agent_id,status_code,error_kind,method,host,path_prefix,request_bytes,response_bytes,cache_rule_id,cache_status,cache_bytes,waf_rule_id,waf_action,retry_rule_id,retry_count,retry_outcome,retry_error_kind,retry_failed_agent_id FROM proxy_request_events ORDER BY id LIMIT -1 OFFSET ?`, previous)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	count := 0
	for rows.Next() {
		var listener, route, target, agent, cache, waf, retry, failed sql.NullInt64
		var status, requestBytes, responseBytes, cacheBytes, retries int64
		var kind, method, host, prefix, cacheStatus, action, outcome, retryKind string
		if err := rows.Scan(&listener, &route, &target, &agent, &status, &kind, &method, &host, &prefix, &requestBytes, &responseBytes, &cache, &cacheStatus, &cacheBytes, &waf, &action, &retry, &retries, &outcome, &retryKind, &failed); err != nil {
			t.Fatal(err)
		}
		count++
		if listener != (sql.NullInt64{Int64: 1, Valid: true}) || method != "GET" || host != "websocket.example.test" || requestBytes != 0 || cache.Valid || cacheBytes != 0 || retry.Valid || retries != 0 || outcome != "" || retryKind != "" || failed.Valid {
			t.Fatalf("phase %s common event fields listener=%v method=%s host=%s request=%d cache=%v/%d retry=%v/%d/%s/%s failed=%v", phase, listener, method, host, requestBytes, cache, cacheBytes, retry, retries, outcome, retryKind, failed)
		}
		if phase == "deny" {
			if status != 403 || kind != "waf_blocked" || waf != (sql.NullInt64{Int64: wafID, Valid: true}) || action != publicWafActionBlock || route.Valid || target.Valid || agent.Valid || cacheStatus != "" || responseBytes != 8 || prefix != "/..." {
				t.Fatalf("WebSocket WAF boundary changed: status=%d kind=%s route=%v target=%v agent=%v cache=%s bytes=%d", status, kind, route, target, agent, cacheStatus, responseBytes)
			}
			continue
		}
		wantStatus, wantBytes, wantKind := int64(101), int64(0), ""
		if phase == "reject" {
			wantStatus, wantBytes, wantKind = 503, 6, "upstream_status_503"
		}
		// After ready, upgrade copier errors do not call ErrorHandler and no
		// retry observer is installed: abort/CloseWrite remain 101/empty error.
		// Hijacked frame traffic bypasses HTTP-body recorder.Write counters.
		if status != wantStatus || kind != wantKind || responseBytes != wantBytes || route != (sql.NullInt64{Int64: 10, Valid: true}) || target != (sql.NullInt64{Int64: targetID, Valid: true}) || !agent.Valid || (agent.Int64 != agents[0].AgentID && agent.Int64 != agents[1].AgentID) || cacheStatus != publicCacheStatusBypass || waf.Valid || action != "" || prefix != "/" {
			t.Fatalf("phase %s forwarded event status=%d/%d kind=%s/%s bytes=%d/%d route=%v target=%v agent=%v cache=%s waf=%v/%s", phase, status, wantStatus, kind, wantKind, responseBytes, wantBytes, route, target, agent, cacheStatus, waf, action)
		}
	}
	if err := rows.Err(); err != nil || count != 2 {
		t.Fatalf("phase %s rows=%d/2 err=%v", phase, count, err)
	}
}
