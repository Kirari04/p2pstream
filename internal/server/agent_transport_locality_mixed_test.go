package server

import (
	"io"
	"net"
	"net/http"
	"net/http/httptrace"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

// This combines states that the single-key tests cover separately: one warm
// shard is busy, another target is idle, and a third target has no pool entry.
// Gates hold actual HTTP bodies open; no assertion depends on scheduler order
// or on a uniform distribution that idle preference does not promise.
func TestAgentRouteLocalityMixedWarmColdRetirement(t *testing.T) {
	ids := []string{"old-a", "mixed-a", "mixed-b", "mixed-c", "replacement-a"}
	gates := make(map[string]chan struct{}, len(ids))
	for _, id := range ids {
		gates[id] = make(chan struct{})
	}
	app, targetA, agent, _, upstream := localityLiveFixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.URL.Query().Get("id")
		if gate := gates[id]; gate != nil {
			_, _ = io.WriteString(w, "begin:"+id)
			w.(http.Flusher).Flush()
			<-gate
			_, _ = io.WriteString(w, "|end:"+id)
			return
		}
		_, _ = io.WriteString(w, "warm")
	}))
	pool := app.AgentTransports
	targetB := targetA
	targetB.ID++
	targetB.UpstreamResponseHeaderTimeout += time.Second
	targetC := targetA
	targetC.ID += 2
	warmA := app.agentTargetTransport(agent, targetA).(*publicRouteAgentTransport)
	warmB := app.agentTargetTransport(agent, targetB).(*publicRouteAgentTransport)
	coldC := app.agentTargetTransport(agent, targetC).(*publicRouteAgentTransport)
	if warmA.key == warmB.key || warmA.key == coldC.key || warmB.key == coldC.key {
		t.Fatal("fixture requires distinct complete target keys")
	}
	type result struct {
		id     string
		resp   *http.Response
		conn   net.Conn
		reused bool
		err    error
	}
	request := func(id string, rt http.RoundTripper) result {
		got := result{id: id}
		req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, upstream.URL+"/?id="+id, nil)
		if err != nil {
			got.err = err
			return got
		}
		req = req.WithContext(httptrace.WithClientTrace(req.Context(), &httptrace.ClientTrace{
			GotConn: func(info httptrace.GotConnInfo) { got.conn, got.reused = info.Conn, info.Reused },
		}))
		got.resp, got.err = rt.RoundTrip(req)
		return got
	}
	var bodies []*http.Response
	var requests sync.WaitGroup
	t.Cleanup(func() {
		for _, gate := range gates {
			select {
			case <-gate:
			default:
				close(gate)
			}
		}
		for _, resp := range bodies {
			_ = resp.Body.Close()
		}
		done := make(chan struct{})
		go func() { requests.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("mixed request goroutines did not join")
		}
	})
	accept := func(got result) result {
		t.Helper()
		if got.err != nil {
			t.Fatalf("%s RoundTrip: %v", got.id, got.err)
		}
		if got.resp.StatusCode != http.StatusOK || got.conn == nil {
			t.Fatalf("%s status/connection = %d/%v", got.id, got.resp.StatusCode, got.conn)
		}
		bodies = append(bodies, got.resp)
		return got
	}
	warm := func(id string, rt http.RoundTripper) result {
		t.Helper()
		got := accept(request(id, rt))
		data, err := io.ReadAll(got.resp.Body)
		_ = got.resp.Body.Close()
		if err != nil || string(data) != "warm" {
			t.Fatalf("warmup %s body=%q err=%v", id, data, err)
		}
		return got
	}
	firstA, firstB := warm("warm-a", warmA), warm("warm-b", warmB)
	if firstA.conn == firstB.conn {
		t.Fatal("different target keys borrowed the same upstream connection")
	}
	oldA := accept(request("old-a", warmA))
	if oldA.conn != firstA.conn || !oldA.reused {
		t.Fatal("initial held A response did not use A's warm connection")
	}
	if borrowed := warmA.claimIdle(); borrowed != nil {
		borrowed.roundTripDone(false)
		t.Fatal("busy A or another target's idle entry was preferred")
	}
	if borrowed := coldC.claimIdle(); borrowed != nil {
		borrowed.roundTripDone(false)
		t.Fatal("cold target borrowed an unrelated warm entry")
	}
	start := make(chan struct{})
	results := make(chan result, 3)
	for _, job := range []struct {
		id string
		rt http.RoundTripper
	}{{"mixed-a", warmA}, {"mixed-b", warmB}, {"mixed-c", coldC}} {
		requests.Add(1)
		go func() {
			defer requests.Done()
			<-start
			results <- request(job.id, job.rt)
		}()
	}
	close(start)
	held := map[string]result{"old-a": oldA}
	for range 3 {
		select {
		case got := <-results:
			got = accept(got)
			held[got.id] = got
		case <-time.After(5 * time.Second):
			t.Fatal("mixed warm/cold requests failed to return headers together")
		}
	}
	if held["mixed-b"].conn != firstB.conn || !held["mixed-b"].reused {
		t.Fatal("warm B did not retain its own idle connection beside busy A")
	}
	if held["mixed-a"].conn == firstA.conn || held["mixed-a"].conn == firstB.conn || held["mixed-c"].conn == firstA.conn || held["mixed-c"].conn == firstB.conn || held["mixed-a"].conn == held["mixed-c"].conn {
		t.Fatal("concurrent cold/busy fallback reused an occupied or different-target connection")
	}
	pool.mu.Lock()
	var retiredA []*pooledAgentTransport
	otherEntries := make(map[agentTransportKey]*pooledAgentTransport)
	for key, entry := range pool.entries {
		if key.RouteTargetID == targetA.ID {
			retiredA = append(retiredA, entry)
		} else {
			otherEntries[key] = entry
		}
	}
	beforeEntries := len(pool.entries)
	pool.mu.Unlock()
	if len(retiredA) != 2 || len(otherEntries) != 2 || beforeEntries != 4 || pool.inFlightForAgent(agent.AgentID) != 4 {
		t.Fatal("mixed bodies do not each own one of four separate target shards")
	}
	pool.closeRouteTarget(targetA.ID)
	pool.mu.Lock()
	valid := len(pool.entries) == 2 && len(pool.draining) == 2
	for _, entry := range retiredA {
		_, draining := pool.draining[entry]
		valid = valid && entry.retired && entry.inFlight == 1 && draining
	}
	for key, entry := range otherEntries {
		valid = valid && pool.entries[key] == entry && !entry.retired && entry.inFlight == 1
	}
	pool.mu.Unlock()
	if !valid || pool.inFlightForAgent(agent.AgentID) != 4 {
		t.Fatal("route retirement lost body ownership or changed unrelated targets")
	}
	// A factory retained before retirement must look up fresh state too.
	if borrowed := warmA.claimIdle(); borrowed != nil {
		borrowed.roundTripDone(false)
		t.Fatal("retained factory borrowed a retired entry")
	}
	held["replacement-a"] = accept(request("replacement-a", warmA))
	if held["replacement-a"].conn == oldA.conn || held["replacement-a"].conn == held["mixed-a"].conn {
		t.Fatal("post-retirement request borrowed a draining connection")
	}
	pool.mu.Lock()
	var replacement *pooledAgentTransport
	for key, entry := range pool.entries {
		if key.RouteTargetID == targetA.ID {
			replacement = entry
		}
	}
	valid = replacement != nil && !replacement.retired && replacement.inFlight == 1 && len(pool.entries) == 3 && len(pool.draining) == 2
	for _, old := range retiredA {
		valid = valid && replacement != old && old.inFlight == 1
	}
	pool.mu.Unlock()
	if !valid || pool.inFlightForAgent(agent.AgentID) != 5 {
		t.Fatal("replacement did not acquire independent ownership beside draining bodies")
	}
	for _, id := range ids {
		close(gates[id])
		got := held[id]
		data, err := io.ReadAll(got.resp.Body)
		_ = got.resp.Body.Close()
		_ = got.resp.Body.Close()
		if err != nil || string(data) != "begin:"+id+"|end:"+id {
			t.Fatalf("%s active body interrupted by retirement: %q, %v", id, data, err)
		}
	}
	pool.mu.Lock()
	valid = len(pool.draining) == 0
	for _, entry := range retiredA {
		valid = valid && entry.inFlight == 0 && entry.retired
	}
	for _, entry := range pool.entries {
		valid = valid && entry.inFlight == 0 && !entry.retired
	}
	pool.mu.Unlock()
	if !valid || pool.inFlightForAgent(agent.AgentID) != 0 {
		t.Fatal("EOF plus repeated Close leaked or double-released mixed ownership")
	}
	pool.closeAll()
	if pool.len() != 0 {
		t.Fatal("final retirement retained a shard after all bodies completed")
	}
	waitForAgentStreamCapacityUsage(t, app, 0)
}

// Generation replacement must preserve a new generation even when a stale
// cleanup retires an old generation's active body under the same agent ID.
// Alternate protocols isolate this pool/accounting test from socket timing.
func TestAgentRouteLocalityMixedGenerationReplacement(t *testing.T) {
	pool, oldAgent, oldA := localityFixture(t, false)
	pool.limitInitialized, pool.maxEntries = true, 8
	keyA := oldA.key
	keyA.TunnelLane = 0
	entryA := localityEntry(pool, oldAgent, keyA)
	keyB := keyA
	keyB.RouteTargetID++
	keyB.TunnelLane = 1
	entryB := localityEntry(pool, oldAgent, keyB)
	entryA.transport.RegisterProtocol("locality", localityRoundTripper(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("old-active"))}, nil
	}))
	req, _ := http.NewRequestWithContext(t.Context(), http.MethodGet, "locality://origin.test/", nil)
	oldResponse, err := oldA.RoundTrip(req)
	if err != nil {
		t.Fatal(err)
	}
	defer oldResponse.Body.Close()
	replacement := &AgentConn{AgentID: oldAgent.AgentID}
	session, _ := newAgentTunnelTestSession(t)
	replacement.installTunnel(1, session)
	newB := &publicRouteAgentTransport{pool: pool, agent: replacement, key: keyB, timeout: time.Second}
	newB.key.TunnelLane = -1
	if borrowed := newB.claimIdle(); borrowed != nil {
		borrowed.roundTripDone(false)
		t.Fatal("replacement generation borrowed the old generation's idle target")
	}
	fresh := pool.getOrCreate(nil, replacement, keyB, nil, time.Second).(*pooledAgentTransport)
	fresh.transport.RegisterProtocol("locality", localityRoundTripper(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("new-active"))}, nil
	}))
	newResponse, err := newB.RoundTrip(req)
	if err != nil {
		t.Fatal(err)
	}
	defer newResponse.Body.Close()
	pool.closeAgentConnection(oldAgent)
	pool.mu.Lock()
	_, draining := pool.draining[entryA]
	valid := entryA.retired && entryA.inFlight == 1 && draining && entryB.retired && entryB.inFlight == 0 && pool.entries[keyB] == fresh && fresh != entryB && fresh.agent == replacement && !fresh.retired && fresh.inFlight == 1 && len(pool.entries) == 1 && len(pool.draining) == 1
	pool.mu.Unlock()
	if !valid || pool.inFlightForAgent(oldAgent.AgentID) != 2 {
		t.Fatal("stale generation cleanup changed replacement or old body ownership")
	}
	if borrowed := newB.claimIdle(); borrowed != nil {
		borrowed.roundTripDone(false)
		t.Fatal("replacement generation's busy entry became idle-preferred")
	}
	for _, item := range []struct {
		response *http.Response
		want     string
	}{{oldResponse, "old-active"}, {newResponse, "new-active"}} {
		data, err := io.ReadAll(item.response.Body)
		_ = item.response.Body.Close()
		_ = item.response.Body.Close()
		if err != nil || !reflect.DeepEqual(data, []byte(item.want)) {
			t.Fatalf("generation body=%q err=%v, want %q", data, err, item.want)
		}
	}
	pool.mu.Lock()
	valid = entryA.inFlight == 0 && fresh.inFlight == 0 && len(pool.draining) == 0 && pool.entries[keyB] == fresh
	pool.mu.Unlock()
	if !valid || pool.inFlightForAgent(oldAgent.AgentID) != 0 {
		t.Fatal("old/new generation releases were not conserved")
	}
	pool.closeAll()
	if pool.len() != 0 {
		t.Fatal("generation test retained entries after final retirement")
	}
}
