package server

import (
	"bytes"
	"context"
	"crypto/tls"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hashicorp/yamux"

	"p2pstream/internal/db"
)

type aggregateBlockingUploadBody struct {
	sent      atomic.Bool
	closed    chan struct{}
	closeOnce sync.Once
}

func newAggregateBlockingUploadBody() *aggregateBlockingUploadBody {
	return &aggregateBlockingUploadBody{closed: make(chan struct{})}
}

func (b *aggregateBlockingUploadBody) Read(p []byte) (int, error) {
	if !b.sent.Swap(true) {
		n := len(p)
		if n > 64<<10 {
			n = 64 << 10
		}
		for index := range n {
			p[index] = 0x6a
		}
		return n, nil
	}
	<-b.closed
	return 0, context.Canceled
}

func (b *aggregateBlockingUploadBody) Close() error {
	b.closeOnce.Do(func() { close(b.closed) })
	return nil
}

func TestRealAggregateH2UploadCancellationKeepsSessionReusable(t *testing.T) {
	const maximumStreamAllocation = 2 << 20
	f := newRealAgentProxyFixture(t, 1, maximumStreamAllocation, 0)
	// The measured grower caps stream allocation at the fixture maximum and
	// session allocation at four times that maximum. Exceed both, without
	// relying on the much larger protocol ceiling, to require refund progress.
	recoveryPayload := bytes.Repeat([]byte{0x7b}, 4*maximumStreamAllocation+int(yamux.AggregateWindowUnitSize))
	var originMajor atomic.Int64
	payloadSeen := make(chan int, 1)
	uploadStopped := make(chan struct{}, 1)
	origin := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		originMajor.Store(int64(r.ProtoMajor))
		if r.URL.Path == "/credit-progress" {
			_, _ = w.Write(recoveryPayload)
			return
		}
		if r.URL.Path != "/cancel" {
			_, _ = w.Write([]byte("ok"))
			return
		}
		buffer := make([]byte, 32<<10)
		reported := false
		for {
			n, err := r.Body.Read(buffer)
			if n > 0 && !reported {
				reported = true
				payloadSeen <- n
			}
			if err != nil {
				uploadStopped <- struct{}{}
				return
			}
		}
	}))
	origin.EnableHTTP2 = true
	origin.StartTLS()
	defer origin.Close()
	target := f.target
	target.ID = 171
	target.URL = origin.URL
	target.ParsedURL = mustParseAggregateTestURL(t, origin.URL)
	resolution := realAggregateTestResolution(f, target)

	var publicMajor atomic.Int64
	public := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		publicMajor.Store(int64(r.ProtoMajor))
		f.app.proxyAgentTargetRequest(w, r, resolution, nil, nil, nil, proxyRequestObservability{})
	}))
	public.EnableHTTP2 = true
	public.StartTLS()
	defer public.Close()
	transport := &http.Transport{
		TLSClientConfig:   &tls.Config{InsecureSkipVerify: true}, // fixture certificate
		ForceAttemptHTTP2: true,
	}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 5 * time.Second}

	assertAggregatePublicGET(t, client, public.URL+"/warm")
	initialAgent := f.agent
	bulk := initialAgent.tunnelSession(1)
	if bulk == nil || bulk.IsClosed() {
		t.Fatal("aggregate bulk lane is unavailable after warm request")
	}
	baselineStreams := bulk.NumStreams()
	baselineInUse := f.app.agentStreamCapacity.snapshot().Total.InUse
	if baselineStreams < 1 || baselineInUse < 1 {
		t.Fatalf("warm aggregate baseline streams/in-use = %d/%d", baselineStreams, baselineInUse)
	}
	baselineStats := awaitAggregateReceiveIdle(t, bulk)

	uploadCtx, cancelUpload := context.WithCancel(t.Context())
	defer cancelUpload()
	body := newAggregateBlockingUploadBody()
	defer body.Close()
	req, err := http.NewRequestWithContext(uploadCtx, http.MethodPost, public.URL+"/cancel", body)
	if err != nil {
		t.Fatal(err)
	}
	req.ContentLength = 1 << 20
	uploadDone := make(chan error, 1)
	go func() {
		resp, requestErr := client.Do(req)
		if resp != nil {
			_ = resp.Body.Close()
		}
		uploadDone <- requestErr
	}()
	select {
	case n := <-payloadSeen:
		if n <= 0 {
			t.Fatal("origin reported an empty upload before cancellation")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("origin did not receive upload payload before cancellation")
	}
	cancelUpload()
	_ = body.Close()
	select {
	case requestErr := <-uploadDone:
		if !errors.Is(requestErr, context.Canceled) {
			t.Fatalf("cancelled public H2 upload error = %v", requestErr)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("cancelled public H2 upload did not return")
	}
	select {
	case <-uploadStopped:
	case <-time.After(2 * time.Second):
		t.Fatal("origin H2 upload handler did not observe cancellation")
	}
	assertAggregatePublicGET(t, client, public.URL+"/recover")

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if initialAgent.ActiveRequests.Load() == 0 &&
			f.app.agentStreamCapacity.snapshot().Total.InUse == baselineInUse &&
			bulk.NumStreams() == baselineStreams {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if publicMajor.Load() != 2 || originMajor.Load() != 2 {
		t.Fatalf("HTTP protocol public/origin = %d/%d, want H2/H2", publicMajor.Load(), originMajor.Load())
	}
	if got := f.app.AgentHub.connectedByID(initialAgent.AgentID); got != initialAgent || !realAgentAggregateLaneShape(initialAgent, 1) {
		t.Fatal("upload cancellation replaced or disconnected the aggregate session generation")
	}
	if got := initialAgent.ActiveRequests.Load(); got != 0 {
		t.Fatalf("active public requests after cancellation = %d", got)
	}
	if got := f.app.agentStreamCapacity.snapshot().Total.InUse; got != baselineInUse {
		t.Fatalf("stream capacity after cancellation = %d, want warmed baseline %d", got, baselineInUse)
	}
	if got := bulk.NumStreams(); got != baselineStreams {
		t.Fatalf("bulk streams after cancellation = %d, want warmed baseline %d", got, baselineStreams)
	}
	stats := awaitAggregateReceiveIdle(t, bulk)
	// Live H2 reuse intentionally retains a backed, subthreshold refund tail;
	// different traffic need not leave the same unpublished credit amount.
	// Pool ownership and the entire available+pending allowance must conserve.
	if stats.BufferedUnits != baselineStats.BufferedUnits || stats.FreeUnits != baselineStats.FreeUnits ||
		stats.TotalUnits != baselineStats.TotalUnits || stats.ReceiveLimit != baselineStats.ReceiveLimit {
		t.Fatalf("aggregate receive ownership after cancellation = %+v, want warmed baseline %+v", stats, baselineStats)
	}
	if got := f.app.AgentHub.connectedByID(initialAgent.AgentID); got != initialAgent || bulk.IsClosed() {
		t.Fatal("aggregate session was not reusable after H2 upload cancellation")
	}

	assertAggregatePublicGETPayload(t, client, public.URL+"/credit-progress", recoveryPayload)
	progressStats := awaitAggregateReceiveIdle(t, bulk)
	deadline = time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && (bulk.NumStreams() != baselineStreams ||
		f.app.agentStreamCapacity.snapshot().Total.InUse != baselineInUse || initialAgent.ActiveRequests.Load() != 0) {
		time.Sleep(time.Millisecond)
	}
	if uint64(len(recoveryPayload)) <= uint64(progressStats.ReceiveLimit) {
		t.Fatalf("recovery bytes %d did not exceed session allowance %d", len(recoveryPayload), progressStats.ReceiveLimit)
	}
	if f.app.AgentHub.connectedByID(initialAgent.AgentID) != initialAgent || initialAgent.tunnelSession(1) != bulk ||
		bulk.IsClosed() || !realAgentAggregateLaneShape(initialAgent, 1) || bulk.NumStreams() != baselineStreams ||
		f.app.agentStreamCapacity.snapshot().Total.InUse != baselineInUse || initialAgent.ActiveRequests.Load() != 0 {
		t.Fatal("large recovery changed the warmed generation, bulk lane, or pooled ownership")
	}

	// Only after cancellation and live reuse are proven, retire the pooled
	// connection. Terminal FIN/RST must publish every remaining refund while
	// preserving the bulk session for future streams.
	f.app.AgentTransports.closeRouteTarget(target.ID)
	deadline = time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && (bulk.NumStreams() != 0 ||
		f.app.agentStreamCapacity.snapshot().Total.InUse != 0 || initialAgent.ActiveRequests.Load() != 0) {
		time.Sleep(time.Millisecond)
	}
	terminalStats := awaitAggregateReceiveQuiescence(t, bulk)
	if bulk.NumStreams() != 0 || f.app.agentStreamCapacity.snapshot().Total.InUse != 0 ||
		initialAgent.ActiveRequests.Load() != 0 || f.app.AgentTransports.len() != 0 {
		t.Fatal("retired aggregate transport retained stream, lease, request, or pool ownership")
	}
	// A late stream growth may finish after the idle progress snapshot. Its
	// backed pool stays with this live session; it must not shrink or exceed
	// the fixture's four-times-stream allocation cap. Quiescence above still
	// requires every unit to be free and every byte of credit returned.
	if terminalStats.ReceiveLimit < progressStats.ReceiveLimit || terminalStats.TotalUnits < progressStats.TotalUnits ||
		terminalStats.ReceiveLimit > 4*maximumStreamAllocation ||
		f.app.AgentHub.connectedByID(initialAgent.AgentID) != initialAgent || initialAgent.tunnelSession(1) != bulk ||
		bulk.IsClosed() || !realAgentAggregateLaneShape(initialAgent, 1) {
		t.Fatalf("terminal refund cleanup lost bounded receiver ownership or session generation: progress=%+v terminal=%+v", progressStats, terminalStats)
	}
}

func TestRealAggregateCacheMissStoresThenHitWithoutSecondTunnelRequest(t *testing.T) {
	f := newRealAgentProxyFixture(t, 1, 2<<20, 0)
	payload := bytes.Repeat([]byte("aggregate-cache-payload-"), 32*1024)
	const maxObjectBytes = int64(1 << 20)
	if int64(len(payload)) >= maxObjectBytes || len(payload) <= 512<<10 {
		t.Fatalf("cache payload size %d must span the initial stream window and fit below %d", len(payload), maxObjectBytes)
	}
	var originHits atomic.Int64
	var originMajor atomic.Int64
	origin := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		originHits.Add(1)
		originMajor.Store(int64(r.ProtoMajor))
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Cache-Control", "max-age=300")
		_, _ = w.Write(payload)
	}))
	origin.EnableHTTP2 = true
	origin.StartTLS()
	defer origin.Close()
	target := f.target
	target.ID = 172
	target.URL = origin.URL
	target.ParsedURL = mustParseAggregateTestURL(t, origin.URL)
	resolution := configureRealAggregateCache(t, f, target, maxObjectBytes)
	bulk := f.agent.tunnelSession(1)
	if bulk == nil {
		t.Fatal("aggregate bulk lane is unavailable")
	}

	firstReq := httptest.NewRequest(http.MethodGet, "https://assets.example.test/assets/aggregate.txt?v=1", nil)
	firstDecision := f.app.checkPublicCache(firstReq, resolution)
	if firstDecision.Status != publicCacheStatusMiss {
		t.Fatalf("first cache status = %q, want miss", firstDecision.Status)
	}
	firstRec := httptest.NewRecorder()
	f.app.proxyAgentTargetRequest(firstRec, firstReq, resolution, nil, nil, &firstDecision, proxyRequestObservability{})
	if firstRec.Code != http.StatusOK || !bytes.Equal(firstRec.Body.Bytes(), payload) {
		t.Fatalf("aggregate cache miss response = status %d bytes %d", firstRec.Code, firstRec.Body.Len())
	}
	if firstDecision.Status != publicCacheStatusStored || firstDecision.StoredBytes != int64(len(payload)) {
		t.Fatalf("aggregate stored decision = status %q bytes %d", firstDecision.Status, firstDecision.StoredBytes)
	}
	if originHits.Load() != 1 || originMajor.Load() != 2 {
		t.Fatalf("aggregate cache origin hits/protocol = %d/H%d, want 1/H2", originHits.Load(), originMajor.Load())
	}
	streamsAfterStore := bulk.NumStreams()
	inUseAfterStore := f.app.agentStreamCapacity.snapshot().Total.InUse
	if streamsAfterStore < 1 || inUseAfterStore < 1 {
		t.Fatalf("aggregate cache stored without pooled tunnel ownership: streams=%d in-use=%d", streamsAfterStore, inUseAfterStore)
	}

	secondReq := httptest.NewRequest(http.MethodGet, "https://assets.example.test/assets/aggregate.txt?v=1", nil)
	secondDecision := f.app.checkPublicCache(secondReq, resolution)
	if secondDecision.Status != publicCacheStatusHit || secondDecision.Entry == nil {
		t.Fatalf("second cache decision = %+v, want completed hit", secondDecision)
	}
	secondRec := httptest.NewRecorder()
	f.app.servePublicCacheHit(secondRec, secondReq, resolution, nil, nil, secondDecision, proxyRequestObservability{})
	if secondRec.Code != http.StatusOK || !bytes.Equal(secondRec.Body.Bytes(), payload) {
		t.Fatalf("aggregate cache hit response = status %d bytes %d", secondRec.Code, secondRec.Body.Len())
	}
	if secondRec.Header().Get("X-p2pstream-Cache") != "HIT" {
		t.Fatalf("aggregate cache hit header = %q", secondRec.Header().Get("X-p2pstream-Cache"))
	}
	if originHits.Load() != 1 {
		t.Fatalf("cache hit reached aggregate origin %d times", originHits.Load())
	}
	if got := bulk.NumStreams(); got != streamsAfterStore {
		t.Fatalf("cache hit opened a second tunnel stream: %d -> %d", streamsAfterStore, got)
	}
	if got := f.app.agentStreamCapacity.snapshot().Total.InUse; got != inUseAfterStore {
		t.Fatalf("cache hit changed stream ownership: %d -> %d", inUseAfterStore, got)
	}
}

func assertAggregatePublicGET(t *testing.T, client *http.Client, requestURL string) {
	t.Helper()
	assertAggregatePublicGETPayload(t, client, requestURL, []byte("ok"))
}

func assertAggregatePublicGETPayload(t *testing.T, client *http.Client, requestURL string, want []byte) {
	t.Helper()
	resp, err := client.Get(requestURL)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK || !bytes.Equal(body, want) || resp.ProtoMajor != 2 {
		t.Fatalf("aggregate public GET = status %d protocol H%d bytes %d, want status200/H2/exact %d bytes", resp.StatusCode, resp.ProtoMajor, len(body), len(want))
	}
}

// aggregateReceiveIdleConserved checks one atomic receiver-ledger snapshot.
// PendingRefund includes both queued and physically publishing backed credit;
// selection and commit move that credit without changing this total.
func aggregateReceiveIdleConserved(stats yamux.AggregateReceiveWindowStats) bool {
	unit := uint64(yamux.AggregateWindowUnitSize)
	return stats.Enabled && !stats.Closed && stats.ReceiveLimit > 0 && stats.BufferedUnits == 0 && stats.FreeUnits == stats.TotalUnits &&
		uint64(stats.TotalUnits)*unit == uint64(stats.ReceiveLimit) && stats.ReceiveAvailable <= stats.ReceiveLimit &&
		stats.PendingRefund <= uint64(stats.ReceiveLimit) && stats.PendingRefund%unit == 0 &&
		uint64(stats.ReceiveAvailable)%unit == 0 && uint64(stats.ReceiveAvailable)+stats.PendingRefund == uint64(stats.ReceiveLimit)
}

func awaitAggregateReceiveIdle(t *testing.T, session *yamux.Session) yamux.AggregateReceiveWindowStats {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		stats := session.AggregateReceiveWindowStats()
		if aggregateReceiveIdleConserved(stats) {
			return stats
		}
		if time.Now().After(deadline) {
			t.Fatalf("live aggregate receive pool did not conserve idle ownership: %+v", stats)
		}
		time.Sleep(time.Millisecond)
	}
}

func awaitAggregateReceiveQuiescence(t *testing.T, session *yamux.Session) yamux.AggregateReceiveWindowStats {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		stats := session.AggregateReceiveWindowStats()
		if aggregateReceiveIdleConserved(stats) && stats.PendingRefund == 0 && stats.ReceiveAvailable == stats.ReceiveLimit {
			return stats
		}
		if time.Now().After(deadline) {
			t.Fatalf("aggregate receive pool did not quiesce: %+v", stats)
		}
		time.Sleep(time.Millisecond)
	}
}

func realAggregateTestResolution(f *realAgentProxyFixture, target publicRouteTargetConfig) publicRouteResolution {
	resolution := publicRouteResolution{
		ListenerID:    sql.NullInt64{Int64: 1, Valid: true},
		RouteID:       sql.NullInt64{Int64: 10, Valid: true},
		RouteTargetID: sql.NullInt64{Int64: target.ID, Valid: true},
		AgentID:       sql.NullInt64{Int64: f.agent.AgentID, Valid: true},
		Listener:      publicListenerConfig{ID: 1, Protocol: publicListenerProtocolHTTP},
		Route: publicRouteConfig{
			ID:                  10,
			TargetLoadBalancing: publicRouteTargetLoadBalancingRoundRobin,
		},
		Target: target,
		Agent:  f.agent,
	}
	resolution.Route.Targets = []publicRouteTargetConfig{target}
	return resolution
}

func configureRealAggregateCache(t *testing.T, f *realAgentProxyFixture, target publicRouteTargetConfig, maxObjectBytes int64) publicRouteResolution {
	t.Helper()
	f.app.PublicCache = newPublicProxyCache(t.TempDir())
	matchJSON, err := json.Marshal(mustPublicPolicyMatchCEL(t, `method in ["GET", "HEAD"] &&
		host_match(host, "assets.example.test") &&
		path_prefix(path, "/assets") &&
		path.endsWith(".txt")`))
	if err != nil {
		t.Fatal(err)
	}
	row, err := f.app.DB.CreatePublicCacheRule(context.Background(), db.CreatePublicCacheRuleParams{
		Name:                 "aggregate-assets",
		Priority:             10,
		Enabled:              1,
		MatchJson:            string(matchJSON),
		RouteIdsJson:         "[]",
		TargetIdsJson:        "[]",
		Scope:                publicCacheScopeSelectedBackend,
		TtlMode:              publicCacheTTLModeFixed,
		TtlMillis:            defaultPublicCacheTTLMillis,
		QueryMode:            publicCacheQueryModeFull,
		QueryParamsJson:      "[]",
		VaryHeadersJson:      "[]",
		CacheStatusCodesJson: "[200]",
		MaxObjectBytes:       maxObjectBytes,
		AddCacheStatusHeader: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	rule, err := publicCacheRuleRowToConfig(row)
	if err != nil {
		t.Fatal(err)
	}
	resolution := realAggregateTestResolution(f, target)
	resolution.CacheRuleID = rule.ID
	settings := defaultPublicCacheSettings()
	rules := []publicCacheRuleConfig{rule}
	snapshot := &publicProxySnapshot{
		CacheSettings:    settings,
		CacheRules:       rules,
		CacheFingerprint: publicCacheRuntimeFingerprint(settings, rules),
		RouteTargets:     map[int64]publicRouteTargetConfig{target.ID: target},
		Agents: map[int64]publicAgentConfig{
			f.agent.AgentID: {ID: f.agent.AgentID, PublicID: f.agent.PublicID, Enabled: true},
		},
	}
	resolution.Snapshot = snapshot
	setPublicSnapshotForTest(t, f.app, snapshot)
	f.app.PublicCache.reconcile(settings, rules)
	return resolution
}

func mustParseAggregateTestURL(t *testing.T, raw string) *url.URL {
	t.Helper()
	parsed, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	return parsed
}
