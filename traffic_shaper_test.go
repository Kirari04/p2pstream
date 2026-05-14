package main_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"connectrpc.com/connect"

	p2pstreamv1 "p2pstream/gen/proto/p2pstream/v1"
	"p2pstream/internal/config"
	"p2pstream/internal/server"
)

func TestPublicTrafficShaperCRUDAndConfig(t *testing.T) {
	app := server.NewApp(&config.Config{}, newTestDB(t))
	_, client := newTestManagementClient(t, app)
	cookie := createAdminSession(t, client)

	invalid := connect.NewRequest(&p2pstreamv1.CreatePublicTrafficShaperRuleRequest{
		Name:        "invalid",
		BudgetScope: p2pstreamv1.PublicTrafficShaperBudgetScope_PUBLIC_TRAFFIC_SHAPER_BUDGET_SCOPE_PER_KEY,
	})
	invalid.Header().Set("Cookie", cookie)
	if _, err := client.CreatePublicTrafficShaperRule(context.Background(), invalid); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("expected invalid argument for unlimited shaper, got %v", err)
	}

	createReq := connect.NewRequest(&p2pstreamv1.CreatePublicTrafficShaperRuleRequest{
		Name:                   "downloads",
		Priority:               25,
		Enabled:                true,
		BudgetScope:            p2pstreamv1.PublicTrafficShaperBudgetScope_PUBLIC_TRAFFIC_SHAPER_BUDGET_SCOPE_PER_KEY,
		DownloadBytesPerSecond: 128 * 1024,
		ResponseExemptBytes:    64 * 1024,
		Match: &p2pstreamv1.PublicRateLimitMatch{
			PathPrefixes: []string{"/downloads"},
		},
		KeyParts: []*p2pstreamv1.PublicRateLimitKeyPart{{
			Source: p2pstreamv1.PublicRateLimitKeySource_PUBLIC_RATE_LIMIT_KEY_SOURCE_REMOTE_IP,
		}},
	})
	createReq.Header().Set("Cookie", cookie)
	createResp, err := client.CreatePublicTrafficShaperRule(context.Background(), createReq)
	if err != nil {
		t.Fatalf("create traffic shaper: %v", err)
	}
	created := createResp.Msg.GetRule()
	if created.GetName() != "downloads" || created.GetDownloadBytesPerSecond() != 128*1024 {
		t.Fatalf("unexpected created shaper: %+v", created)
	}

	cfg := getPublicProxyConfig(t, client, cookie)
	if len(cfg.GetTrafficShaperRules()) != 1 {
		t.Fatalf("config shapers = %+v, want one", cfg.GetTrafficShaperRules())
	}

	updateReq := connect.NewRequest(&p2pstreamv1.UpdatePublicTrafficShaperRuleRequest{
		Id:                   created.GetId(),
		Name:                 "uploads",
		Priority:             30,
		Enabled:              true,
		BudgetScope:          p2pstreamv1.PublicTrafficShaperBudgetScope_PUBLIC_TRAFFIC_SHAPER_BUDGET_SCOPE_PER_REQUEST,
		UploadBytesPerSecond: 256 * 1024,
		RequestExemptBytes:   8 * 1024,
		KeyParts: []*p2pstreamv1.PublicRateLimitKeyPart{{
			Source: p2pstreamv1.PublicRateLimitKeySource_PUBLIC_RATE_LIMIT_KEY_SOURCE_HEADER,
			Name:   "X-User",
		}},
	})
	updateReq.Header().Set("Cookie", cookie)
	updateResp, err := client.UpdatePublicTrafficShaperRule(context.Background(), updateReq)
	if err != nil {
		t.Fatalf("update traffic shaper: %v", err)
	}
	updated := updateResp.Msg.GetRule()
	if updated.GetBudgetScope() != p2pstreamv1.PublicTrafficShaperBudgetScope_PUBLIC_TRAFFIC_SHAPER_BUDGET_SCOPE_PER_REQUEST {
		t.Fatalf("scope = %s, want per request", updated.GetBudgetScope())
	}
	if len(updated.GetKeyParts()) != 0 {
		t.Fatalf("per-request key parts = %+v, want none", updated.GetKeyParts())
	}

	deleteReq := connect.NewRequest(&p2pstreamv1.DeletePublicTrafficShaperRuleRequest{Id: updated.GetId()})
	deleteReq.Header().Set("Cookie", cookie)
	if _, err := client.DeletePublicTrafficShaperRule(context.Background(), deleteReq); err != nil {
		t.Fatalf("delete traffic shaper: %v", err)
	}
	cfg = getPublicProxyConfig(t, client, cookie)
	if len(cfg.GetTrafficShaperRules()) != 0 {
		t.Fatalf("config shapers after delete = %+v, want none", cfg.GetTrafficShaperRules())
	}
}

func TestTrafficShaperDirectProxyTrace(t *testing.T) {
	targetSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("shaped ok"))
	}))
	defer targetSrv.Close()

	database := newTestDB(t)
	listener := seedTestHTTPPublicListener(t, database, targetSrv.URL)
	app := server.NewApp(&config.Config{}, database)
	_, client := newTestManagementClient(t, app)
	cookie := createAdminSession(t, client)

	createReq := connect.NewRequest(&p2pstreamv1.CreatePublicTrafficShaperRuleRequest{
		Name:                   "trace-shaper",
		Priority:               10,
		Enabled:                true,
		BudgetScope:            p2pstreamv1.PublicTrafficShaperBudgetScope_PUBLIC_TRAFFIC_SHAPER_BUDGET_SCOPE_PER_KEY,
		DownloadBytesPerSecond: 10 * 1024 * 1024,
		Match: &p2pstreamv1.PublicRateLimitMatch{
			PathPrefixes: []string{"/trace"},
		},
	})
	createReq.Header().Set("Cookie", cookie)
	if _, err := client.CreatePublicTrafficShaperRule(context.Background(), createReq); err != nil {
		t.Fatalf("create traffic shaper: %v", err)
	}
	setTrafficTraceSettings(t, client, cookie, true, p2pstreamv1.TrafficTraceLevel_TRAFFIC_TRACE_LEVEL_DETAILED)

	status, err := app.StartProxyListener(context.Background())
	if err != nil {
		t.Fatalf("start proxy: %v", err)
	}
	t.Cleanup(func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_, _ = app.StopProxyListener(shutdownCtx)
	})

	resp, err := http.Get("http://" + publicListenerBoundAddress(t, status, listener.ID) + "/trace/file")
	if err != nil {
		t.Fatalf("proxy request: %v", err)
	}
	defer resp.Body.Close()
	if body := mustReadResponseBody(t, resp); resp.StatusCode != http.StatusOK || body != "shaped ok" {
		t.Fatalf("unexpected proxy response: status=%d body=%q", resp.StatusCode, body)
	}

	stream, stop := openTrafficTraceStream(t, client, cookie, true, 0, 2*time.Second)
	defer stop()
	_ = receiveTrafficTraceResponse(t, stream)
	events := collectTrafficTraceEventsUntil(t, stream, p2pstreamv1.TrafficTraceStage_TRAFFIC_TRACE_STAGE_RESPONSE_SENT)

	assertTrafficTraceStages(t, events,
		p2pstreamv1.TrafficTraceStage_TRAFFIC_TRACE_STAGE_RECEIVED,
		p2pstreamv1.TrafficTraceStage_TRAFFIC_TRACE_STAGE_TRAFFIC_SHAPER_SELECTED,
		p2pstreamv1.TrafficTraceStage_TRAFFIC_TRACE_STAGE_ROUTE_RESOLVED,
		p2pstreamv1.TrafficTraceStage_TRAFFIC_TRACE_STAGE_BACKEND_SELECTED,
		p2pstreamv1.TrafficTraceStage_TRAFFIC_TRACE_STAGE_CACHE_LOOKUP,
		p2pstreamv1.TrafficTraceStage_TRAFFIC_TRACE_STAGE_CACHE_BYPASS,
		p2pstreamv1.TrafficTraceStage_TRAFFIC_TRACE_STAGE_UPSTREAM_STARTED,
		p2pstreamv1.TrafficTraceStage_TRAFFIC_TRACE_STAGE_UPSTREAM_RESPONDED,
		p2pstreamv1.TrafficTraceStage_TRAFFIC_TRACE_STAGE_RESPONSE_SENT,
	)
	shaperEvent := events[1]
	if shaperEvent.GetTrafficShaperRuleName() != "trace-shaper" {
		t.Fatalf("trace shaper name = %q", shaperEvent.GetTrafficShaperRuleName())
	}
	finalEvent := events[len(events)-1]
	if finalEvent.GetTrafficShaperRuleId() == 0 || finalEvent.GetTrafficShaperDownloadBytesPerSecond() != 10*1024*1024 {
		t.Fatalf("final event missing traffic shaper fields: %+v", finalEvent)
	}
}
