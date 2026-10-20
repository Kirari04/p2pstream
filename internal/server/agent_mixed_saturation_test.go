package server

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"database/sql"
	"encoding/base64"
	"encoding/pem"
	"fmt"
	"hash"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	agentruntime "p2pstream/internal/agent"
	"p2pstream/internal/db"
	"p2pstream/internal/tunnel"
)

// Fixture responses have both explicit and failure-cleanup owners. Bind the
// callback to the original body and share it across copies; io.Closer does not
// promise that an underlying second Close is safe.
func ownedSaturationBodyClose(body io.Closer) func() {
	var once sync.Once
	return func() { once.Do(func() { _ = body.Close() }) }
}

const (
	mixedSaturationBulkBytes = 4 << 20
	mixedSaturationSlowBytes = 32 << 20
	mixedSaturationPrefix    = 32 << 10
	mixedSaturationStep      = 256 << 10
	mixedSaturationWait      = 5 * time.Second
)

type mixedSaturationBulk struct {
	advance chan struct{}
	release chan struct{}
	once    sync.Once
}

type mixedSaturationExpectedEvent struct {
	path   string
	route  int
	status int
	bytes  int64
	slow   bool
}

func (b *mixedSaturationBulk) finish() { b.once.Do(func() { close(b.release) }) }

// This finite cohort saturates a configured public target guard, not physical
// stream or machine capacity. Gates keep exact ownership independent of speed.
// Latencies are diagnostics; the deadlines only guard progress and teardown.
func TestRealAgentMixedTargetSaturationRecovery(t *testing.T) {
	f := newRealAgentProxyFixture(t, 1, 2<<20, 0)
	second := mixedSaturationAddAgent(t, f)
	app := f.app
	installDeterministicServerResourceController(t, app)
	app.publicProxyRequests = newRequestCapacityLimiter(8, 8)
	app.publicTargetRequests = newKeyedRequestCapacityLimiter(4, 4)
	app.publicClientRequests = newKeyedStringRequestCapacityLimiter(8)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), mixedSaturationWait)
		defer cancel()
		if err := app.CloseObservabilityRecorder(ctx); err != nil {
			t.Error(err)
		}
	})

	bulks := [2]*mixedSaturationBulk{}
	for i := range bulks {
		bulks[i] = &mixedSaturationBulk{advance: make(chan struct{}), release: make(chan struct{})}
		defer bulks[i].finish()
	}
	stopped := [2]chan struct{}{make(chan struct{}), make(chan struct{})}
	var originCalls atomic.Int64
	var nonH2Origin atomic.Bool
	block := bytes.Repeat([]byte{0x6d}, mixedSaturationStep)
	origin := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		originCalls.Add(1)
		if r.ProtoMajor != 2 {
			nonH2Origin.Store(true)
		}
		w.Header().Set("Content-Type", "application/octet-stream")
		switch r.URL.Path {
		case "/bulk/0", "/bulk/1":
			index := int(r.URL.Path[len(r.URL.Path)-1] - '0')
			w.Header().Set("Content-Length", fmt.Sprint(mixedSaturationBulkBytes))
			written, err := w.Write(block[:mixedSaturationPrefix])
			w.(http.Flusher).Flush()
			for err == nil && written < mixedSaturationBulkBytes {
				select {
				case <-r.Context().Done():
					return
				case <-bulks[index].release:
					n, writeErr := w.Write(block[:min(len(block), mixedSaturationBulkBytes-written)])
					written, err = written+n, writeErr
				case <-bulks[index].advance:
					n, writeErr := w.Write(block[:min(len(block), mixedSaturationBulkBytes-written)])
					written, err = written+n, writeErr
				}
				w.(http.Flusher).Flush()
			}
		case "/slow/0", "/slow/1":
			index := int(r.URL.Path[len(r.URL.Path)-1] - '0')
			// Withhold the last byte even if an implementation buffers all of
			// this bounded prefix. Cancellation remains directly observable;
			// outstanding ownership alone is not proof of socket backpressure.
			w.Header().Set("Content-Length", fmt.Sprint(mixedSaturationSlowBytes+1))
			for written := 0; written < mixedSaturationSlowBytes; {
				n, err := w.Write(block[:min(len(block), mixedSaturationSlowBytes-written)])
				written += n
				w.(http.Flusher).Flush()
				if err != nil {
					break
				}
			}
			<-r.Context().Done()
			close(stopped[index])
		default:
			_, _ = io.WriteString(w, "small:"+r.URL.Path)
		}
	}))
	origin.EnableHTTP2 = true
	origin.StartTLS()
	// Close public clients before waiting for origin/agent cleanup, including
	// failure paths. Register cleanup after these servers are constructed.
	t.Cleanup(origin.Close)
	const listenerID = int64(1)
	targets := [2]publicRouteTargetConfig{}
	agents := [2]*AgentConn{f.agent, second}
	snapshot := &publicProxySnapshot{
		Listeners:        map[int64]publicListenerConfig{listenerID: {ID: listenerID, Protocol: publicListenerProtocolHTTP, Enabled: true}},
		RoutesByListener: make(map[int64][]publicRouteConfig),
		RouteTargets:     make(map[int64]publicRouteTargetConfig), Agents: make(map[int64]publicAgentConfig),
	}
	for i, agent := range agents {
		label := fmt.Sprint(i)
		target := f.target
		target.ID, target.RouteID = int64(170+i), int64(10+i)
		target.URL, target.ParsedURL = origin.URL, mustParseAggregateTestURL(t, origin.URL)
		target.AgentSelector = publicAgentSelectorConfig{MatchLabels: map[string]string{"mixed-route": label}}
		target.AgentLoadBalancing = publicRouteTargetLoadBalancingRoundRobin
		target.Weight = 100
		targets[i] = target
		snapshot.RouteTargets[target.ID] = target
		snapshot.Agents[agent.AgentID] = publicAgentConfig{
			ID: agent.AgentID, PublicID: agent.PublicID, Enabled: true, Labels: map[string]string{"mixed-route": label},
		}
		snapshot.RoutesByListener[listenerID] = append(snapshot.RoutesByListener[listenerID], publicRouteConfig{
			ID: target.RouteID, ListenerID: listenerID, HostPattern: "*." + string(rune('a'+i)) + ".mixed.test",
			PathPrefix: "/", Action: publicRouteActionForward, PathSecurityMode: publicRoutePathSecurityModeStrict,
			TargetLoadBalancing: publicRouteTargetLoadBalancingRoundRobin, Enabled: true, Targets: []publicRouteTargetConfig{target},
		})
	}
	setPublicSnapshotForTest(t, app, snapshot)
	app.TargetHealth.reconcile(app, snapshot, false)
	public := httptest.NewUnstartedServer(app.publicProxyHandler(listenerID))
	public.EnableHTTP2 = true
	public.StartTLS()
	t.Cleanup(public.Close)
	transport := &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, ForceAttemptHTTP2: true} // fixture certificate
	client := &http.Client{Transport: transport}
	t.Cleanup(transport.CloseIdleConnections)
	var cancelRequests []context.CancelFunc
	t.Cleanup(func() {
		for _, cancel := range cancelRequests {
			cancel()
		}
	})
	requestCount := 0
	expectedEvents := make(map[string]mixedSaturationExpectedEvent)
	request := func(route int, path string) (*http.Response, context.CancelFunc, func()) {
		t.Helper()
		ctx, cancel := context.WithTimeout(t.Context(), 45*time.Second)
		cancelRequests = append(cancelRequests, cancel)
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, public.URL+path, nil)
		if err != nil {
			t.Fatal(err)
		}
		requestCount++
		// Event paths deliberately contain only the matched route prefix.
		// Give every request a unique, bounded host under its route's wildcard
		// so each persisted terminal event maps to exactly one expected path.
		req.Host = fmt.Sprintf("request-%d.%c.mixed.test", requestCount, 'a'+route)
		expected := mixedSaturationExpectedEvent{path: path, route: route, status: http.StatusOK, bytes: int64(len("small:" + path))}
		switch path {
		case "/overflow":
			expected.status, expected.bytes = http.StatusServiceUnavailable, int64(len("Route target capacity reached\n"))
		case "/bulk/0", "/bulk/1":
			expected.bytes = mixedSaturationBulkBytes
		case "/slow/0", "/slow/1":
			expected.slow = true
		}
		expectedEvents[req.Host] = expected
		// Header acquisition has its own bound, separate from the lifetime of
		// the intentionally held body. Every request goroutine joins here.
		timer := time.AfterFunc(mixedSaturationWait, cancel)
		resp, err := client.Do(req)
		timer.Stop()
		if err != nil {
			t.Fatalf("route %d %s: %v", route, path, err)
		}
		closeBody := ownedSaturationBodyClose(resp.Body)
		if resp.ProtoMajor != 2 {
			closeBody()
			cancel()
			t.Fatalf("public protocol = %s, want H2", resp.Proto)
		}
		return resp, cancel, closeBody
	}
	small := func(route int, path string) time.Duration {
		t.Helper()
		start := time.Now()
		resp, cancel, closeBody := request(route, path)
		defer cancel()
		defer closeBody()
		timer := time.AfterFunc(mixedSaturationWait, cancel)
		defer timer.Stop()
		payload, err := io.ReadAll(io.LimitReader(resp.Body, 1024))
		if err != nil || resp.StatusCode != http.StatusOK || string(payload) != "small:"+path {
			t.Fatalf("small route %d %s: status=%d payload=%q err=%v", route, path, resp.StatusCode, payload, err)
		}
		return time.Since(start)
	}
	for route := range agents {
		small(route, "/warm")
	}
	control := [2]any{agents[0].tunnelSession(0), agents[1].tunnelSession(0)}
	bulkSessions := [2]any{agents[0].tunnelSession(1), agents[1].tunnelSession(1)}

	type heldBulk struct {
		response *http.Response
		cancel   context.CancelFunc
		close    func()
		hash     hash.Hash
		read     int64
	}
	held := [2]heldBulk{}
	consume := func(index, size int) {
		t.Helper()
		item := &held[index]
		chunk := make([]byte, size)
		timer := time.AfterFunc(mixedSaturationWait, item.cancel)
		n, err := io.ReadFull(item.response.Body, chunk)
		timer.Stop()
		if err != nil || !bytes.Equal(chunk, bytes.Repeat([]byte{0x6d}, size)) {
			t.Fatalf("bulk %d step: bytes=%d err=%v", index, n, err)
		}
		_, _ = item.hash.Write(chunk)
		item.read += int64(n)
	}
	for i := range held {
		resp, cancel, closeBody := request(0, fmt.Sprintf("/bulk/%d", i))
		t.Cleanup(closeBody)
		if resp.StatusCode != http.StatusOK || resp.ContentLength != mixedSaturationBulkBytes {
			t.Fatalf("bulk %d status/length = %d/%d", i, resp.StatusCode, resp.ContentLength)
		}
		held[i] = heldBulk{response: resp, cancel: cancel, close: closeBody, hash: sha256.New()}
		consume(i, mixedSaturationPrefix)
	}
	slowResponses := [2]*http.Response{}
	slowCancel := [2]context.CancelFunc{}
	slowClose := [2]func(){}
	for i := range slowResponses {
		resp, cancel, closeBody := request(0, fmt.Sprintf("/slow/%d", i))
		t.Cleanup(closeBody)
		if resp.StatusCode != http.StatusOK || resp.ContentLength != mixedSaturationSlowBytes+1 {
			t.Fatalf("slow %d status/length = %d/%d", i, resp.StatusCode, resp.ContentLength)
		}
		slowResponses[i], slowCancel[i], slowClose[i] = resp, cancel, closeBody
		prefix := make([]byte, mixedSaturationPrefix)
		timer := time.AfterFunc(mixedSaturationWait, cancel)
		_, err := io.ReadFull(resp.Body, prefix)
		timer.Stop()
		if err != nil || !bytes.Equal(prefix, block[:mixedSaturationPrefix]) {
			t.Fatalf("slow %d prefix: %v", i, err)
		}
	}
	mixedSaturationAwait(t, "A owns four target slots and bodies", func() bool {
		return mixedSaturationTargetUsage(app, targets[0].ID) == 4 && agents[0].ActiveRequests.Load() == 4 && app.AgentTransports.inFlightForAgent(agents[0].AgentID) == 4
	})
	beforeRejects := originCalls.Load()
	for range 3 {
		resp, cancel, closeBody := request(0, "/overflow")
		timer := time.AfterFunc(mixedSaturationWait, cancel)
		payload, err := io.ReadAll(io.LimitReader(resp.Body, 1024))
		timer.Stop()
		closeBody()
		cancel()
		if err != nil || resp.StatusCode != http.StatusServiceUnavailable || string(payload) != "Route target capacity reached\n" || resp.Header.Get("Retry-After") != "1" {
			t.Fatalf("overflow status=%d payload=%q retry=%q err=%v", resp.StatusCode, payload, resp.Header.Get("Retry-After"), err)
		}
	}
	if got := originCalls.Load(); got != beforeRejects {
		t.Fatalf("overflow reached origin: calls %d -> %d", beforeRejects, got)
	}
	probe := func(route, heldSlots int, phase string) {
		t.Helper()
		samples := make([]time.Duration, 0, 12)
		for i := range 12 {
			if got := mixedSaturationTargetUsage(app, targets[0].ID); got != heldSlots {
				t.Fatalf("%s A slots=%d, want %d", phase, got, heldSlots)
			}
			if active, owned := agents[0].ActiveRequests.Load(), app.AgentTransports.inFlightForAgent(agents[0].AgentID); active != int64(heldSlots) || owned != heldSlots {
				t.Fatalf("%s A active/owned bodies=%d/%d, want %d", phase, active, owned, heldSlots)
			}
			samples = append(samples, small(route, fmt.Sprintf("/small/%s/%d", phase, i)))
			index := i % len(bulks)
			select {
			case bulks[index].advance <- struct{}{}:
			case <-time.After(mixedSaturationWait):
				t.Fatal("bulk did not accept finite progress step")
			}
			consume(index, mixedSaturationStep)
		}
		mixedSaturationLogLatency(t, phase, samples)
	}
	probe(1, 4, "B_with_A_full")
	slowCancel[0]()
	slowClose[0]()
	mixedSaturationAwaitChannel(t, "first slow origin cancellation", stopped[0])
	mixedSaturationAwait(t, "one A slot returned", func() bool { return mixedSaturationTargetUsage(app, targets[0].ID) == 3 })
	probe(0, 3, "A_after_one_cancel")
	for i, item := range held {
		bulks[i].finish()
		timer := time.AfterFunc(mixedSaturationWait, item.cancel)
		n, err := io.Copy(item.hash, item.response.Body)
		timer.Stop()
		item.close()
		item.cancel()
		wantHash := sha256.Sum256(bytes.Repeat([]byte{0x6d}, mixedSaturationBulkBytes))
		if err != nil || item.read+n != mixedSaturationBulkBytes || !bytes.Equal(item.hash.Sum(nil), wantHash[:]) {
			t.Fatalf("bulk %d final bytes=%d err=%v hash=%x", i, item.read+n, err, item.hash.Sum(nil))
		}
	}
	slowCancel[1]()
	slowClose[1]()
	mixedSaturationAwaitChannel(t, "second slow origin cancellation", stopped[1])
	mixedSaturationAwait(t, "all held target slots returned", func() bool { return mixedSaturationTargetUsage(app, targets[0].ID) == 0 })
	for route := range agents {
		samples := make([]time.Duration, 0, 12)
		for i := range 12 {
			samples = append(samples, small(route, fmt.Sprintf("/recovery/%d", i)))
		}
		mixedSaturationLogLatency(t, fmt.Sprintf("recovery_%d", route), samples)
	}
	for i, agent := range agents {
		if app.AgentHub.connectedByID(agent.AgentID) != agent || !realAgentAggregateLaneShape(agent, 1) ||
			agent.tunnelSession(0) != control[i] || agent.tunnelSession(1) != bulkSessions[i] {
			t.Fatal("mixed traffic replaced an original aggregate agent or session")
		}
	}
	app.CloseAgentTransports()
	mixedSaturationAwait(t, "terminal ownership cleanup", func() bool {
		if len(app.publicProxyRequests.slots) != 0 || app.AgentTransports.len() != 0 || app.agentStreamCapacity.snapshot().Total.InUse != 0 {
			return false
		}
		capacity := app.agentStreamCapacity.snapshot()
		if capacity.AdaptiveExternalBytesByOwner[adaptiveResourceOwnerPublicRequest] != 0 ||
			capacity.AdaptiveExternalBytesByOwner[adaptiveResourceOwnerPublicResponse] != 0 {
			return false
		}
		app.publicClientRequests.mu.Lock()
		clients := len(app.publicClientRequests.inUse)
		app.publicClientRequests.mu.Unlock()
		for i, agent := range agents {
			if agent.ActiveRequests.Load() != 0 || app.AgentTransports.inFlightForAgent(agent.AgentID) != 0 ||
				mixedSaturationTargetUsage(app, targets[i].ID) != 0 || agent.tunnelSession(1).NumStreams() != 0 {
				return false
			}
		}
		return clients == 0
	})
	if err := app.agentStreamCapacity.validateInvariants(); err != nil {
		t.Fatal(err)
	}
	if got := originCalls.Load(); got != int64(requestCount-3) {
		t.Fatalf("origin requests=%d, want exactly %d accepted public requests", got, requestCount-3)
	}
	if nonH2Origin.Load() {
		t.Fatal("origin received a request without HTTP/2")
	}
	mixedSaturationCheckEvents(t, app, targets, agents, expectedEvents)
	t.Logf("mixed target saturation: requests=%d origin=%d rejections=3 bulk_bytes=%d agents=2 target_limit=4 global_limit=8", requestCount, originCalls.Load(), 2*mixedSaturationBulkBytes)
}

func mixedSaturationTargetUsage(app *App, targetID int64) int {
	app.publicTargetRequests.mu.Lock()
	defer app.publicTargetRequests.mu.Unlock()
	if limiter := app.publicTargetRequests.entries[targetID]; limiter != nil {
		return len(limiter.slots)
	}
	return 0
}

func mixedSaturationAwait(t *testing.T, reason string, ready func() bool) {
	t.Helper()
	deadline := time.Now().Add(mixedSaturationWait)
	for time.Now().Before(deadline) {
		if ready() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("timed out: %s", reason)
}

func mixedSaturationAwaitChannel(t *testing.T, reason string, ch <-chan struct{}) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(mixedSaturationWait):
		t.Fatalf("timed out: %s", reason)
	}
}

func mixedSaturationLogLatency(t *testing.T, phase string, samples []time.Duration) {
	t.Helper()
	// Retain the raw finite samples as well as nearest-rank quantiles. These
	// diagnostics do not change the deterministic correctness/progress gates.
	t.Logf("mixed_latency phase=%s samples=%v", phase, samples)
	slices.Sort(samples)
	at := func(percent int) time.Duration { return samples[(len(samples)*percent+99)/100-1] }
	t.Logf("mixed_latency phase=%s count=%d p50=%s p95=%s p99=%s max=%s", phase, len(samples), at(50), at(95), at(99), samples[len(samples)-1])
}

func mixedSaturationAddAgent(t *testing.T, f *realAgentProxyFixture) *AgentConn {
	t.Helper()
	ctx, cancel := context.WithCancel(t.Context())
	row, err := f.app.DB.CreateAgent(ctx, db.CreateAgentParams{
		PublicID: "mixed-second-agent", Name: "Mixed second agent", TokenHash: hashAgentToken("mixed-test-token"), Enabled: 1,
	})
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	ca := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: f.management.Certificate().Raw})
	done := make(chan error, 1)
	go func() {
		done <- agentruntime.RunContext(ctx, agentruntime.Options{
			ManagementURL: f.management.URL, PublicID: row.PublicID, Token: "mixed-test-token", AllowAnyTarget: true,
			ManagementCAPEMBase64: base64.StdEncoding.EncodeToString(ca), TunnelConnections: 1,
			TunnelMaxStreamWindowBytes: 2 << 20, TunnelMaxConcurrentRequests: tunnel.MaxConcurrentAgentRequestsLimit, TunnelCapacityAdaptive: true,
		})
	}()
	t.Cleanup(func() {
		cancel()
		f.app.CloseAgentTransports()
		select {
		case <-done:
		case <-time.After(mixedSaturationWait):
			t.Error("second mixed agent did not exit naturally")
		}
		mixedSaturationAwait(t, "second agent disconnect", func() bool { return f.app.AgentHub.connectedByID(row.ID) == nil })
		unlock := f.app.lockAgentAuth(row.ID)
		unlock()
	})
	var connected *AgentConn
	mixedSaturationAwait(t, "second aggregate agent connection", func() bool {
		connected = f.app.AgentHub.connectedByID(row.ID)
		return realAgentAggregateLaneShape(connected, 1)
	})
	return connected
}

func mixedSaturationCheckEvents(t *testing.T, app *App, targets [2]publicRouteTargetConfig, agents [2]*AgentConn, expected map[string]mixedSaturationExpectedEvent) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), mixedSaturationWait)
	defer cancel()
	want := len(expected)
	// A body/pool lease can return just before the handler enqueues its event.
	// Flush repeatedly within the same finite deadline until every terminal
	// event is persisted; do not treat zero request ownership as enqueue proof.
	mixedSaturationAwait(t, "terminal event conservation", func() bool {
		if err := app.FlushObservabilityRecorder(ctx); err != nil {
			t.Fatal(err)
		}
		var count int
		if err := app.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM proxy_request_events`).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count > want {
			t.Fatalf("terminal event count=%d exceeds expected=%d", count, want)
		}
		return count == want
	})
	rows, err := app.DB.QueryContext(ctx, `SELECT host, path_prefix, route_id, route_target_id, agent_id, status_code, error_kind, response_bytes FROM proxy_request_events ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	count, rejected, successful, partial := 0, 0, [2]int{}, 0
	for rows.Next() {
		var host, prefix, kind string
		var routeID, targetID, agentID sql.NullInt64
		var status, responseBytes int64
		if err := rows.Scan(&host, &prefix, &routeID, &targetID, &agentID, &status, &kind, &responseBytes); err != nil {
			t.Fatal(err)
		}
		item, ok := expected[host]
		if !ok || prefix != "/" {
			t.Fatalf("unexpected/duplicate terminal event host=%q prefix=%q", host, prefix)
		}
		delete(expected, host)
		index := item.route
		if !routeID.Valid || routeID.Int64 != targets[index].RouteID || !targetID.Valid || targetID.Int64 != targets[index].ID {
			t.Fatalf("terminal event route/target attribution: %q %v %v", host, routeID, targetID)
		}
		count++
		if item.status == http.StatusServiceUnavailable {
			// Target admission follows route/agent selection. A local rejection
			// retains that attribution even though it never reaches the agent.
			if index != 0 || status != int64(item.status) || kind != "route_target_capacity" || !agentID.Valid || agentID.Int64 != agents[index].AgentID || responseBytes != item.bytes {
				t.Fatalf("invalid target-capacity event: host=%s status=%d agent=%v bytes=%d", host, status, agentID, responseBytes)
			}
			rejected++
			continue
		}
		if !agentID.Valid || agentID.Int64 != agents[index].AgentID || status != http.StatusOK || kind != "" {
			t.Fatalf("terminal event attribution/status: host=%s agent=%v status=%d kind=%q", host, agentID, status, kind)
		}
		if item.slow && index == 0 && responseBytes >= mixedSaturationPrefix && responseBytes <= mixedSaturationSlowBytes {
			// With no retry rule, body-copy cancellation after headers does
			// not run ReverseProxy.ErrorHandler: the stored status/kind remains
			// 200/empty. A partial body is allowed only for these two paths.
			partial++
		} else if item.slow || responseBytes != item.bytes {
			t.Fatalf("unexpected successful response event path=%s bytes=%d want=%d host=%s", item.path, responseBytes, item.bytes, host)
		}
		successful[index]++
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	// A: warm + four held + twelve under load + twelve recovery; B:
	// warm + twelve under full A + twelve recovery. Exactly three rejects.
	if count != want || count != 57 || rejected != 3 || successful != [2]int{29, 25} || partial != 2 {
		t.Fatalf("terminal events total=%d want=%d rejections=%d successes=%v large-partials=%d", count, want, rejected, successful, partial)
	}
	if len(expected) != 0 {
		t.Fatalf("missing terminal event hosts: %v", expected)
	}
	if recorder := app.observabilityRecorder; recorder == nil || recorder.droppedEvents.Load() != 0 || recorder.pendingEvents.Load() != 0 || len(recorder.events) != 0 {
		t.Fatal("terminal recorder retained or dropped events")
	}
}
