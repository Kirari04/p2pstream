package server

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"connectrpc.com/connect"

	p2pstreamv1 "p2pstream/gen/proto/p2pstream/v1"
	"p2pstream/gen/proto/p2pstream/v1/p2pstreamv1connect"
	"p2pstream/internal/agentupdateauth"
	"p2pstream/internal/config"
)

func TestAgentUpdateRateLimiterBoundsKeysWithoutPerRequestEvictionScan(t *testing.T) {
	limiter := newAgentUpdateRateLimiter(2, time.Minute, 2, 2)
	now := time.Unix(100, 0)
	if !limiter.allow("a", now) || !limiter.allow("a", now) || limiter.allow("a", now) {
		t.Fatal("token bucket did not enforce its burst")
	}
	if !limiter.allow("b", now) {
		t.Fatal("second bounded key was not admitted")
	}
	if limiter.allow("attacker-churn", now) {
		t.Fatal("full bounded key set admitted an attacker-controlled replacement")
	}
	if len(limiter.buckets) != 2 {
		t.Fatalf("bucket count = %d, want 2", len(limiter.buckets))
	}
	if !limiter.allow("recovered", now.Add(2*time.Minute+time.Nanosecond)) {
		t.Fatal("idle bounded keys were not pruned after the refill window")
	}
	if len(limiter.buckets) > 2 {
		t.Fatalf("bucket count grew beyond bound: %d", len(limiter.buckets))
	}
}

func TestAgentUpdatePeerThrottleCannotDrainFleetRateBudget(t *testing.T) {
	app := NewApp(&config.Config{ManagementUIDisabled: true}, nil)
	app.agentUpdatePeerRate = newAgentUpdateRateLimiter(1, time.Hour, 1, 16)
	app.agentUpdateGlobalRate = newAgentUpdateRateLimiter(3, time.Hour, 3, 1)
	var handled atomic.Int64
	handler := app.agentUpdateHTTPAdmission(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		handled.Add(1)
		w.WriteHeader(http.StatusNoContent)
	}))
	request := func(peer string) int {
		req := httptest.NewRequest(http.MethodPost, p2pstreamv1connect.AgentManagementServiceCheckAgentUpdateProcedure, strings.NewReader("x"))
		req.RemoteAddr = peer + ":1234"
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, req)
		return recorder.Code
	}
	if status := request("192.0.2.1"); status != http.StatusNoContent {
		t.Fatalf("first attacker request status = %d", status)
	}
	for i := 0; i < 20; i++ {
		if status := request("192.0.2.1"); status != http.StatusTooManyRequests {
			t.Fatalf("throttled attacker request status = %d", status)
		}
	}
	if status := request("192.0.2.2"); status != http.StatusNoContent {
		t.Fatalf("second peer was suppressed by attacker traffic: status=%d", status)
	}
	if got := handled.Load(); got != 2 {
		t.Fatalf("handler calls = %d, want 2", got)
	}
}

func TestManagementHandlerRejectsCompressedUpdaterMessageBeforeDecode(t *testing.T) {
	app := NewApp(&config.Config{ManagementUIDisabled: true}, nil)
	var handled atomic.Int64
	handler := app.agentUpdateHTTPAdmission(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		handled.Add(1)
	}))
	for _, header := range []string{"Content-Encoding", "Connect-Content-Encoding"} {
		req := httptest.NewRequest(http.MethodPost, p2pstreamv1connect.AgentManagementServiceReportAgentUpdateProcedure, strings.NewReader("compressed"))
		req.RemoteAddr = "192.0.2.10:1234"
		req.Header.Set(header, "gzip")
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, req)
		if recorder.Code != http.StatusUnsupportedMediaType {
			t.Fatalf("%s response = %d, want 415", header, recorder.Code)
		}
	}
	if handled.Load() != 0 || len(app.agentUpdateRequestGate) != 0 {
		t.Fatal("compressed request reached decoder or control-plane admission")
	}
}

func TestAgentUpdateIdentityRateLimitDoesNotConsumeCounter(t *testing.T) {
	ctx := context.Background()
	database := newServerTestDB(t)
	agent := createAgentUpdateTestAgent(t, database, "agent-rate-counter")
	app := newAgentUpdateTestApp(t, database)
	app.agentUpdateIdentityRate = newAgentUpdateRateLimiter(1, time.Hour, 1, agentUpdateMaxAgents)
	updaterPublic, updaterPrivate, _ := ed25519.GenerateKey(rand.Reader)
	activatorPublic, _, _ := ed25519.GenerateKey(rand.Reader)
	insertAgentUpdateIdentity(t, app, database, agent.ID, updaterPublic, activatorPublic)
	check := func(counter uint64) error {
		request := &p2pstreamv1.CheckAgentUpdateRequest{AgentPublicId: agent.PublicID, Counter: counter}
		request.Signature = ed25519.Sign(updaterPrivate, agentupdateauth.CheckPayload(request.AgentPublicId, request.Counter))
		_, err := app.CheckAgentUpdate(ctx, connect.NewRequest(request))
		return err
	}
	if err := check(1); err != nil {
		t.Fatal(err)
	}
	if err := check(2); connect.CodeOf(err) != connect.CodeResourceExhausted {
		t.Fatalf("rate-limited check error = %v", err)
	}
	var counter int64
	if err := database.QueryRowContext(ctx, `SELECT last_counter FROM agent_updater_identities WHERE agent_id=?`, agent.ID).Scan(&counter); err != nil {
		t.Fatal(err)
	}
	if counter != 1 {
		t.Fatalf("rate-limited request consumed counter %d", counter)
	}
}

func TestAgentUpdateDefaultRatesCarryThousandAgentSharedEgressPoll(t *testing.T) {
	global := newAgentUpdateRateLimiter(60_000, time.Minute, 1024, 1)
	peer := newAgentUpdateRateLimiter(6_000, time.Minute, 128, 4096)
	start := time.Unix(100, 0)
	for index := 0; index < 1000; index++ {
		now := start.Add(time.Duration(index) * 30 * time.Millisecond)
		if !peer.allow("198.51.100.1", now) || !global.allow("global", now) {
			t.Fatalf("shared-egress fleet poll %d was throttled", index)
		}
	}
}

func BenchmarkAgentUpdateIdentityRateLimiter(b *testing.B) {
	limiter := newAgentUpdateRateLimiter(1_000_000_000, time.Minute, 1_000_000_000, agentUpdateMaxAgents)
	now := time.Now()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if !limiter.allow(strconv.Itoa(i%agentUpdateMaxAgents), now) {
			b.Fatal("benchmark limiter unexpectedly rejected")
		}
	}
}
