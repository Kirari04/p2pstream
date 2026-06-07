package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"

	p2pstreamv1 "p2pstream/gen/proto/p2pstream/v1"
	"p2pstream/internal/authutil"
	"p2pstream/internal/config"
	"p2pstream/internal/db"
)

func TestPublicCacheRequestBypassesUnsafeRequests(t *testing.T) {
	cases := []struct {
		name string
		req  *http.Request
		want string
	}{
		{
			name: "authorization",
			req: func() *http.Request {
				req := httptest.NewRequest(http.MethodGet, "http://example.test/app.js", nil)
				req.Header.Set("Authorization", "Bearer token")
				return req
			}(),
			want: "authorization",
		},
		{
			name: "request body",
			req:  httptest.NewRequest(http.MethodGet, "http://example.test/app.js", strings.NewReader("body")),
			want: "request_body",
		},
		{
			name: "range",
			req: func() *http.Request {
				req := httptest.NewRequest(http.MethodGet, "http://example.test/app.js", nil)
				req.Header.Set("Range", "bytes=0-10")
				return req
			}(),
			want: "range",
		},
		{
			name: "post",
			req:  httptest.NewRequest(http.MethodPost, "http://example.test/app.js", nil),
			want: "method",
		},
	}
	for _, tc := range cases {
		if got := publicCacheRequestBypassReason(tc.req); got != tc.want {
			t.Fatalf("%s bypass = %q, want %q", tc.name, got, tc.want)
		}
	}
	if got := publicCacheRequestBypassReason(httptest.NewRequest(http.MethodHead, "http://example.test/app.js", nil)); got != "" {
		t.Fatalf("HEAD bypass = %q, want empty", got)
	}
}

func TestPublicCacheCookieRequestBypassesByDefault(t *testing.T) {
	app, resolution, closeDB := newTestPublicCacheApp(t)
	defer closeDB()

	req := httptest.NewRequest(http.MethodGet, "http://assets.example.test/assets/app.txt", nil)
	req.Header.Set("Cookie", "sid=1")

	decision := app.checkPublicCache(req, resolution)
	if decision.Status != publicCacheStatusBypass || decision.BypassReason != "cookie" {
		t.Fatalf("cookie request cache decision = %q/%q, want bypass/cookie", decision.Status, decision.BypassReason)
	}
}

func TestPublicCacheCookieRequestAllowedByRule(t *testing.T) {
	app, resolution, closeDB := newTestPublicCacheApp(t)
	defer closeDB()
	setTestCacheRuleAllowCookieRequests(t, app, true)

	req := httptest.NewRequest(http.MethodGet, "http://assets.example.test/assets/app.txt", nil)
	req.Header.Set("Cookie", "sid=1")

	decision := app.checkPublicCache(req, resolution)
	if decision.Status != publicCacheStatusMiss {
		t.Fatalf("cookie request cache decision = %q/%q, want miss", decision.Status, decision.BypassReason)
	}
	if !decision.CookieRequest {
		t.Fatal("expected cookie request trace marker on decision")
	}
}

func TestPublicCacheCookieIgnoredInKey(t *testing.T) {
	app, resolution, closeDB := newTestPublicCacheApp(t)
	defer closeDB()
	setTestCacheRuleAllowCookieRequests(t, app, true)

	originHits := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		originHits++
		w.Header().Set("Content-Type", "text/plain")
		w.Header().Set("Cache-Control", "max-age=300")
		_, _ = w.Write([]byte("asset-cookie"))
	}))
	defer upstream.Close()

	origin, err := url.Parse(upstream.URL)
	if err != nil {
		t.Fatalf("parse upstream URL: %v", err)
	}
	resolution.Target.ParsedURL = origin

	firstReq := httptest.NewRequest(http.MethodGet, "http://assets.example.test/assets/app.txt", nil)
	firstReq.Header.Set("Cookie", "sid=a")
	firstDecision := app.checkPublicCache(firstReq, resolution)
	if firstDecision.Status != publicCacheStatusMiss {
		t.Fatalf("first cache status = %q/%q, want miss", firstDecision.Status, firstDecision.BypassReason)
	}
	firstRec := httptest.NewRecorder()
	app.proxyDirectTargetRequest(firstRec, firstReq, resolution, nil, nil, &firstDecision, proxyRequestObservability{})
	if firstRec.Code != http.StatusOK {
		t.Fatalf("first response status = %d, want 200", firstRec.Code)
	}
	if firstDecision.Status != publicCacheStatusStored {
		t.Fatalf("first decision after proxy = %q, want stored", firstDecision.Status)
	}

	secondReq := httptest.NewRequest(http.MethodGet, "http://assets.example.test/assets/app.txt", nil)
	secondReq.Header.Set("Cookie", "sid=b")
	secondDecision := app.checkPublicCache(secondReq, resolution)
	if secondDecision.Status != publicCacheStatusHit {
		t.Fatalf("second cache status = %q/%q, want hit", secondDecision.Status, secondDecision.BypassReason)
	}
	if originHits != 1 {
		t.Fatalf("origin hits = %d, want 1", originHits)
	}
}

func TestPublicCacheAuthorizationStillBypasses(t *testing.T) {
	app, resolution, closeDB := newTestPublicCacheApp(t)
	defer closeDB()
	setTestCacheRuleAllowCookieRequests(t, app, true)

	req := httptest.NewRequest(http.MethodGet, "http://assets.example.test/assets/app.txt", nil)
	req.Header.Set("Cookie", "sid=1")
	req.Header.Set("Authorization", "Bearer secret")

	decision := app.checkPublicCache(req, resolution)
	if decision.Status != publicCacheStatusBypass || decision.BypassReason != "authorization" {
		t.Fatalf("authorization cache decision = %q/%q, want bypass/authorization", decision.Status, decision.BypassReason)
	}
}

func TestPublicCacheKeyCanonicalizesDottedHostWithPort(t *testing.T) {
	resolution := publicRouteResolution{
		Listener: publicListenerConfig{Protocol: publicListenerProtocolHTTPS},
		Route:    publicRouteConfig{ID: 10},
		Backend:  publicBackendConfig{ID: 20},
	}
	rule := publicCacheRuleConfig{Scope: publicCacheScopeSelectedBackend}
	plain := httptest.NewRequest(http.MethodGet, "https://example.com/assets/app.js?v=1", nil)
	plain.Host = "example.com:443"
	dotted := httptest.NewRequest(http.MethodGet, "https://example.com/assets/app.js?v=1", nil)
	dotted.Host = "example.com.:443"

	plainKey := publicCacheKeyDigest(plain, resolution, rule, plain.URL.RawQuery, nil)
	dottedKey := publicCacheKeyDigest(dotted, resolution, rule, dotted.URL.RawQuery, nil)
	if dottedKey != plainKey {
		t.Fatalf("dotted host cache key = %s, want %s", dottedKey, plainKey)
	}
}

func TestPublicCacheRulePathSuffixMatching(t *testing.T) {
	rule := publicCacheRuleConfig{
		ID:      1,
		Enabled: true,
		Match: mustPublicPolicyMatchCEL(t, `method == "GET" &&
			host_match(host, "assets.example.test") &&
			path_prefix(path, "/assets") &&
			(path.endsWith(".css") || path.endsWith(".woff2"))`),
	}
	listener := publicListenerConfig{Protocol: publicListenerProtocolHTTPS}
	resolution := publicRouteResolution{Route: publicRouteConfig{ID: 10}, Backend: publicBackendConfig{ID: 20}}

	if !rule.matches(listener, httptest.NewRequest(http.MethodGet, "https://assets.example.test/assets/app.css", nil), resolution) {
		t.Fatal("expected CSS asset to match cache rule")
	}
	if rule.matches(listener, httptest.NewRequest(http.MethodGet, "https://assets.example.test/assets/api.json", nil), resolution) {
		t.Fatal("JSON response unexpectedly matched suffix cache rule")
	}
}

func TestPublicCacheResponseEligibilityTTLAndDenials(t *testing.T) {
	rule := publicCacheRuleConfig{
		TTLMode:          publicCacheTTLModeOrigin,
		TTL:              time.Hour,
		VaryHeaders:      []string{"Accept-Encoding"},
		CacheStatusCodes: []int64{http.StatusOK},
		MaxObjectBytes:   defaultPublicCacheMaxObjectBytes,
	}

	resp := &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Cache-Control": []string{"s-maxage=42"}}}
	ttl, vary, ok := publicCacheResponseEligibility(rule, resp)
	if !ok || ttl != 42*time.Second {
		t.Fatalf("origin s-maxage ttl = %v ok=%v, want 42s true", ttl, ok)
	}
	if len(vary) != 1 || vary[0] != "Accept-Encoding" {
		t.Fatalf("vary = %#v, want Accept-Encoding", vary)
	}

	resp = &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Cache-Control": []string{"private, max-age=60"}}}
	if _, _, ok := publicCacheResponseEligibility(rule, resp); ok {
		t.Fatal("private response should not be cacheable")
	}

	resp = &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Cache-Control": []string{`private="Set-Cookie", max-age=60`}}}
	if _, _, ok := publicCacheResponseEligibility(rule, resp); ok {
		t.Fatal("parameterized private response should not be cacheable")
	}

	resp = &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Cache-Control": []string{`no-cache="Set-Cookie"`}}}
	if _, _, ok := publicCacheResponseEligibility(rule, resp); ok {
		t.Fatal("parameterized no-cache response should not be cacheable")
	}

	resp = &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Cache-Control": []string{"max-age=60", "private"}}}
	if _, _, ok := publicCacheResponseEligibility(rule, resp); ok {
		t.Fatal("private duplicate Cache-Control response should not be cacheable")
	}

	resp = &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Cache-Control": []string{"max-age=300", "s-maxage=60"}}}
	if ttl, _, ok := publicCacheResponseEligibility(rule, resp); !ok || ttl != time.Minute {
		t.Fatalf("duplicate Cache-Control ttl = %v ok=%v, want 1m true", ttl, ok)
	}

	resp = &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Cache-Control": []string{"max-age=0"}}}
	if _, _, ok := publicCacheResponseEligibility(rule, resp); ok {
		t.Fatal("max-age=0 response should not be cacheable")
	}

	resp = &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Cache-Control": []string{"max-age=300, s-maxage=0"}}}
	if _, _, ok := publicCacheResponseEligibility(rule, resp); ok {
		t.Fatal("s-maxage=0 response should not be cacheable")
	}

	resp = &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Set-Cookie": []string{"sid=1"}}}
	if _, _, ok := publicCacheResponseEligibility(rule, resp); ok {
		t.Fatal("Set-Cookie response should not be cacheable")
	}

	resp = &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Vary": []string{"*"}}}
	if _, _, ok := publicCacheResponseEligibility(rule, resp); ok {
		t.Fatal("Vary:* response should not be cacheable")
	}

	resp = &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Vary": []string{"Cookie"}}}
	if _, _, ok := publicCacheResponseEligibility(rule, resp); ok {
		t.Fatal("Vary: Cookie response should not be cacheable")
	}

	resp = &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Vary": []string{"Authorization"}}}
	if _, _, ok := publicCacheResponseEligibility(rule, resp); ok {
		t.Fatal("Vary: Authorization response should not be cacheable")
	}
}

func TestPublicCacheSetCookieResponseNotStoredWithCookieRequestsAllowed(t *testing.T) {
	app, resolution, closeDB := newTestPublicCacheApp(t)
	defer closeDB()
	setTestCacheRuleAllowCookieRequests(t, app, true)

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "max-age=300")
		w.Header().Set("Set-Cookie", "upstream=private")
		_, _ = w.Write([]byte("private-asset"))
	}))
	defer upstream.Close()

	origin, err := url.Parse(upstream.URL)
	if err != nil {
		t.Fatalf("parse upstream URL: %v", err)
	}
	resolution.Target.ParsedURL = origin

	firstReq := httptest.NewRequest(http.MethodGet, "http://assets.example.test/assets/app.txt", nil)
	firstReq.Header.Set("Cookie", "sid=a")
	firstDecision := app.checkPublicCache(firstReq, resolution)
	if firstDecision.Status != publicCacheStatusMiss {
		t.Fatalf("first cache status = %q/%q, want miss", firstDecision.Status, firstDecision.BypassReason)
	}
	firstRec := httptest.NewRecorder()
	app.proxyDirectTargetRequest(firstRec, firstReq, resolution, nil, nil, &firstDecision, proxyRequestObservability{})
	if firstRec.Code != http.StatusOK {
		t.Fatalf("first response status = %d, want 200", firstRec.Code)
	}

	secondReq := httptest.NewRequest(http.MethodGet, "http://assets.example.test/assets/app.txt", nil)
	secondReq.Header.Set("Cookie", "sid=b")
	secondDecision := app.checkPublicCache(secondReq, resolution)
	if secondDecision.Status != publicCacheStatusMiss {
		t.Fatalf("second cache status = %q/%q, want miss because Set-Cookie response was not stored", secondDecision.Status, secondDecision.BypassReason)
	}
}

func TestPublicCacheVaryCookieResponseNotStored(t *testing.T) {
	rule := publicCacheRuleConfig{
		TTL:              time.Hour,
		VaryHeaders:      []string{"Accept-Encoding"},
		CacheStatusCodes: []int64{http.StatusOK},
		MaxObjectBytes:   defaultPublicCacheMaxObjectBytes,
	}
	resp := &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Vary": []string{"Cookie"}}}
	if _, _, ok := publicCacheResponseEligibility(rule, resp); ok {
		t.Fatal("Vary: Cookie response should not be cacheable")
	}
}

func TestPublicCacheVaryAuthorizationResponseNotStored(t *testing.T) {
	rule := publicCacheRuleConfig{
		TTL:              time.Hour,
		VaryHeaders:      []string{"Accept-Encoding"},
		CacheStatusCodes: []int64{http.StatusOK},
		MaxObjectBytes:   defaultPublicCacheMaxObjectBytes,
	}
	resp := &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Vary": []string{"Authorization"}}}
	if _, _, ok := publicCacheResponseEligibility(rule, resp); ok {
		t.Fatal("Vary: Authorization response should not be cacheable")
	}
}

func TestPublicCacheRejectsSensitiveConfiguredVaryHeaders(t *testing.T) {
	app, _, closeDB := newTestPublicCacheApp(t)
	defer closeDB()

	for _, header := range []string{"Cookie", "Authorization", "Set-Cookie"} {
		if _, err := app.validatePublicCacheRuleInput(context.Background(), "bad-vary", 10, true, nil, nil, p2pstreamv1.PublicCacheScope_PUBLIC_CACHE_SCOPE_SELECTED_BACKEND, p2pstreamv1.PublicCacheTtlMode_PUBLIC_CACHE_TTL_MODE_FIXED, defaultPublicCacheTTLMillis, p2pstreamv1.PublicCacheQueryMode_PUBLIC_CACHE_QUERY_MODE_FULL, nil, []string{header}, []int64{http.StatusOK}, defaultPublicCacheMaxObjectBytes, true, false, false, nil); err == nil {
			t.Fatalf("expected validation error for configured vary header %q", header)
		}
	}
}

func TestPublicCacheManagementAPIAllowCookieRequestsReadback(t *testing.T) {
	app, _, closeDB := newTestPublicCacheApp(t)
	defer closeDB()

	header := createTestAdminSession(t, app)
	createReq := connect.NewRequest(&p2pstreamv1.CreatePublicCacheRuleRequest{
		Name:                            "cookie-assets",
		Priority:                        20,
		Enabled:                         true,
		MatchRule:                       &p2pstreamv1.PublicPolicyMatchRule{CelExpression: `method == "GET" && path.endsWith(".js")`},
		Scope:                           p2pstreamv1.PublicCacheScope_PUBLIC_CACHE_SCOPE_SELECTED_BACKEND,
		TtlMode:                         p2pstreamv1.PublicCacheTtlMode_PUBLIC_CACHE_TTL_MODE_FIXED,
		TtlMillis:                       defaultPublicCacheTTLMillis,
		QueryMode:                       p2pstreamv1.PublicCacheQueryMode_PUBLIC_CACHE_QUERY_MODE_FULL,
		VaryHeaders:                     []string{"Accept-Encoding"},
		CacheStatusCodes:                []int64{http.StatusOK},
		MaxObjectBytes:                  defaultPublicCacheMaxObjectBytes,
		AddCacheStatusHeader:            true,
		AllowCookieRequests:             true,
		AllowCookieRequestsAcknowledged: true,
	})
	createReq.Header().Set("Cookie", header.Get("Cookie"))
	createResp, err := app.CreatePublicCacheRule(context.Background(), createReq)
	if err != nil {
		t.Fatalf("create cache rule: %v", err)
	}
	if !createResp.Msg.Rule.AllowCookieRequests {
		t.Fatal("create readback allowCookieRequests = false, want true")
	}

	updateReq := connect.NewRequest(&p2pstreamv1.UpdatePublicCacheRuleRequest{
		Id:                   createResp.Msg.Rule.Id,
		Name:                 "cookie-assets",
		Priority:             20,
		Enabled:              true,
		MatchRule:            &p2pstreamv1.PublicPolicyMatchRule{CelExpression: `method == "GET" && path.endsWith(".js")`},
		Scope:                p2pstreamv1.PublicCacheScope_PUBLIC_CACHE_SCOPE_SELECTED_BACKEND,
		TtlMode:              p2pstreamv1.PublicCacheTtlMode_PUBLIC_CACHE_TTL_MODE_FIXED,
		TtlMillis:            defaultPublicCacheTTLMillis,
		QueryMode:            p2pstreamv1.PublicCacheQueryMode_PUBLIC_CACHE_QUERY_MODE_FULL,
		VaryHeaders:          []string{"Accept-Encoding"},
		CacheStatusCodes:     []int64{http.StatusOK},
		MaxObjectBytes:       defaultPublicCacheMaxObjectBytes,
		AddCacheStatusHeader: true,
		AllowCookieRequests:  false,
	})
	updateReq.Header().Set("Cookie", header.Get("Cookie"))
	updateResp, err := app.UpdatePublicCacheRule(context.Background(), updateReq)
	if err != nil {
		t.Fatalf("update cache rule: %v", err)
	}
	if updateResp.Msg.Rule.AllowCookieRequests {
		t.Fatal("update readback allowCookieRequests = true, want false")
	}
}

func TestPublicCacheManagementAPIRequiresCookieRequestAcknowledgement(t *testing.T) {
	app, _, closeDB := newTestPublicCacheApp(t)
	defer closeDB()

	header := createTestAdminSession(t, app)
	createReq := connect.NewRequest(&p2pstreamv1.CreatePublicCacheRuleRequest{
		Name:                 "cookie-assets",
		Priority:             20,
		Enabled:              true,
		MatchRule:            &p2pstreamv1.PublicPolicyMatchRule{CelExpression: `method == "GET"`},
		Scope:                p2pstreamv1.PublicCacheScope_PUBLIC_CACHE_SCOPE_SELECTED_BACKEND,
		TtlMode:              p2pstreamv1.PublicCacheTtlMode_PUBLIC_CACHE_TTL_MODE_FIXED,
		TtlMillis:            defaultPublicCacheTTLMillis,
		QueryMode:            p2pstreamv1.PublicCacheQueryMode_PUBLIC_CACHE_QUERY_MODE_FULL,
		VaryHeaders:          []string{"Accept-Encoding"},
		CacheStatusCodes:     []int64{http.StatusOK},
		MaxObjectBytes:       defaultPublicCacheMaxObjectBytes,
		AddCacheStatusHeader: true,
		AllowCookieRequests:  true,
	})
	createReq.Header().Set("Cookie", header.Get("Cookie"))
	if _, err := app.CreatePublicCacheRule(context.Background(), createReq); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("expected acknowledgement validation error, got %v", err)
	}
}

func TestPublicCacheDirectBackendMissStoresThenHit(t *testing.T) {
	app, resolution, closeDB := newTestPublicCacheApp(t)
	defer closeDB()

	originHits := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		originHits++
		w.Header().Set("Content-Type", "text/plain")
		w.Header().Set("Cache-Control", "max-age=300")
		_, _ = w.Write([]byte("asset-v1"))
	}))
	defer upstream.Close()

	origin, err := url.Parse(upstream.URL)
	if err != nil {
		t.Fatalf("parse upstream URL: %v", err)
	}
	resolution.Target.ParsedURL = origin

	firstReq := httptest.NewRequest(http.MethodGet, "http://assets.example.test/assets/app.txt?v=1", nil)
	firstDecision := app.checkPublicCache(firstReq, resolution)
	if firstDecision.Status != publicCacheStatusMiss {
		t.Fatalf("first cache status = %q, want miss", firstDecision.Status)
	}
	firstRec := httptest.NewRecorder()
	app.proxyDirectTargetRequest(firstRec, firstReq, resolution, nil, nil, &firstDecision, proxyRequestObservability{})
	if firstRec.Code != http.StatusOK || firstRec.Body.String() != "asset-v1" {
		t.Fatalf("first response = status %d body %q", firstRec.Code, firstRec.Body.String())
	}
	if firstDecision.Status != publicCacheStatusStored || firstDecision.StoredBytes != int64(len("asset-v1")) {
		t.Fatalf("stored decision = status %q bytes %d", firstDecision.Status, firstDecision.StoredBytes)
	}

	var eventStatus string
	var eventBytes int64
	if err := app.DB.QueryRowContext(context.Background(), `SELECT cache_status, cache_bytes FROM proxy_request_events ORDER BY id DESC LIMIT 1`).Scan(&eventStatus, &eventBytes); err != nil {
		t.Fatalf("query proxy event cache fields: %v", err)
	}
	if eventStatus != publicCacheStatusStored || eventBytes != int64(len("asset-v1")) {
		t.Fatalf("proxy event cache = %q/%d, want stored/%d", eventStatus, eventBytes, len("asset-v1"))
	}

	secondReq := httptest.NewRequest(http.MethodGet, "http://assets.example.test/assets/app.txt?v=1", nil)
	secondDecision := app.checkPublicCache(secondReq, resolution)
	if secondDecision.Status != publicCacheStatusHit {
		t.Fatalf("second cache status = %q, want hit", secondDecision.Status)
	}
	secondRec := httptest.NewRecorder()
	app.servePublicCacheHit(secondRec, secondReq, resolution, nil, nil, secondDecision, proxyRequestObservability{})
	if secondRec.Code != http.StatusOK || secondRec.Body.String() != "asset-v1" {
		t.Fatalf("second response = status %d body %q", secondRec.Code, secondRec.Body.String())
	}
	if got := secondRec.Header().Get("X-p2pstream-Cache"); got != "HIT" {
		t.Fatalf("cache header = %q, want HIT", got)
	}
	if originHits != 1 {
		t.Fatalf("origin hits = %d, want 1", originHits)
	}
}

func TestPublicCacheAgentBackendMissStoresThenHit(t *testing.T) {
	app, resolution, closeDB := newTestPublicCacheApp(t)
	defer closeDB()

	originHits := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		originHits++
		w.Header().Set("Content-Type", "text/plain")
		w.Header().Set("Cache-Control", "max-age=300")
		_, _ = w.Write([]byte("agent-asset-v1"))
	}))
	defer upstream.Close()

	origin, err := url.Parse(upstream.URL)
	if err != nil {
		t.Fatalf("parse upstream URL: %v", err)
	}
	agentRow, err := app.DB.CreateAgent(context.Background(), db.CreateAgentParams{
		PublicID:  "agent-cache-test",
		Name:      "Agent Cache Test",
		TokenHash: hashAgentToken("agent-token"),
		Enabled:   1,
	})
	if err != nil {
		t.Fatalf("seed cache agent: %v", err)
	}
	agentID := agentRow.ID
	agent, _ := newFakeYamuxAgent(t, agentID, agentRow.PublicID)
	if err := app.AgentHub.connect(agent); err != nil {
		t.Fatalf("connect agent: %v", err)
	}
	t.Cleanup(func() { app.AgentHub.disconnect(agent) })

	resolution.Target.ParsedURL = origin
	resolution.Target.Transport = publicRouteTargetTransportAgent
	resolution.Target.AgentSelector = publicAgentSelectorConfig{MatchLabels: map[string]string{agentIDSystemLabelKey: agentRow.PublicID}}
	resolution.Agent = agent
	resolution.AgentID = sql.NullInt64{Int64: agentID, Valid: true}
	resolution.Route.Targets = []publicRouteTargetConfig{resolution.Target}
	app.proxyMu.Lock()
	snap := app.publicSnapshot
	snap.RouteTargets = map[int64]publicRouteTargetConfig{resolution.Target.ID: resolution.Target}
	snap.Agents = map[int64]publicAgentConfig{
		agentID: {ID: agentID, PublicID: agent.PublicID, Enabled: true, Labels: map[string]string{agentIDSystemLabelKey: agentRow.PublicID}},
	}
	app.proxyMu.Unlock()

	firstReq := httptest.NewRequest(http.MethodGet, "http://assets.example.test/assets/agent.txt?v=1", nil)
	firstDecision := app.checkPublicCache(firstReq, resolution)
	if firstDecision.Status != publicCacheStatusMiss {
		t.Fatalf("first cache status = %q, want miss", firstDecision.Status)
	}
	firstRec := httptest.NewRecorder()
	app.proxyAgentTargetRequest(firstRec, firstReq, resolution, nil, nil, &firstDecision, proxyRequestObservability{})
	if firstRec.Code != http.StatusOK || firstRec.Body.String() != "agent-asset-v1" {
		t.Fatalf("first agent response = status %d body %q, want 200 agent-asset-v1", firstRec.Code, firstRec.Body.String())
	}
	if got := firstRec.Header().Get("X-p2pstream-Cache"); got != "MISS" {
		t.Fatalf("first cache header = %q, want MISS", got)
	}
	if firstDecision.Status != publicCacheStatusStored || firstDecision.StoredBytes != int64(len("agent-asset-v1")) {
		t.Fatalf("stored decision = status %q bytes %d", firstDecision.Status, firstDecision.StoredBytes)
	}

	var eventAgentID sql.NullInt64
	var eventStatus string
	var eventBytes int64
	if err := app.DB.QueryRowContext(context.Background(), `SELECT agent_id, cache_status, cache_bytes FROM proxy_request_events ORDER BY id DESC LIMIT 1`).Scan(&eventAgentID, &eventStatus, &eventBytes); err != nil {
		t.Fatalf("query proxy event cache fields: %v", err)
	}
	if !eventAgentID.Valid || eventAgentID.Int64 != agentID {
		t.Fatalf("proxy event agent id = %+v, want %d", eventAgentID, agentID)
	}
	if eventStatus != publicCacheStatusStored || eventBytes != int64(len("agent-asset-v1")) {
		t.Fatalf("proxy event cache = %q/%d, want stored/%d", eventStatus, eventBytes, len("agent-asset-v1"))
	}

	secondReq := httptest.NewRequest(http.MethodGet, "http://assets.example.test/assets/agent.txt?v=1", nil)
	secondDecision := app.checkPublicCache(secondReq, resolution)
	if secondDecision.Status != publicCacheStatusHit {
		t.Fatalf("second cache status = %q, want hit", secondDecision.Status)
	}
	secondRec := httptest.NewRecorder()
	app.servePublicCacheHit(secondRec, secondReq, resolution, nil, nil, secondDecision, proxyRequestObservability{})
	if secondRec.Code != http.StatusOK || secondRec.Body.String() != "agent-asset-v1" {
		t.Fatalf("second cache response = status %d body %q, want 200 agent-asset-v1", secondRec.Code, secondRec.Body.String())
	}
	if got := secondRec.Header().Get("X-p2pstream-Cache"); got != "HIT" {
		t.Fatalf("second cache header = %q, want HIT", got)
	}
	if originHits != 1 {
		t.Fatalf("origin hits = %d, want 1", originHits)
	}
}

func TestPublicCacheStoreReadCloserDoesNotRaceWithReconcile(t *testing.T) {
	app, resolution, closeDB := newTestPublicCacheApp(t)
	defer closeDB()

	req := httptest.NewRequest(http.MethodGet, "http://assets.example.test/assets/race.txt", nil)
	decision := app.checkPublicCache(req, resolution)
	if decision.Status != publicCacheStatusMiss {
		t.Fatalf("cache status = %q/%q, want miss", decision.Status, decision.BypassReason)
	}

	reader, writer := io.Pipe()
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header: http.Header{
			"Cache-Control": []string{"max-age=300"},
			"Content-Type":  []string{"text/plain"},
		},
		Body: reader,
	}
	body := app.capturePublicCacheResponseBody(context.Background(), req, resolution, &decision, resp, nil)
	if body == nil {
		t.Fatal("expected cache store wrapper")
	}
	if _, ok := body.(*publicCacheStoreReadCloser); !ok {
		t.Fatalf("cache response body type = %T, want *publicCacheStoreReadCloser", body)
	}

	readDone := make(chan error, 1)
	go func() {
		_, err := io.Copy(io.Discard, body)
		if closeErr := body.Close(); err == nil {
			err = closeErr
		}
		readDone <- err
	}()

	stopReconcile := make(chan struct{})
	reconcileDone := make(chan struct{})
	go func() {
		defer close(reconcileDone)
		for i := 0; ; i++ {
			select {
			case <-stopReconcile:
				return
			default:
			}
			settings := defaultPublicCacheSettings()
			if i%2 == 0 {
				settings.MemoryHotObjectMaxBytes = 32
			} else {
				settings.MemoryHotObjectMaxBytes = defaultPublicCacheMemoryHotObjectBytes
			}
			app.PublicCache.reconcile(settings)
		}
	}()
	defer func() {
		close(stopReconcile)
		<-reconcileDone
	}()

	chunk := []byte("cache-race-body-chunk\n")
	for i := 0; i < 512; i++ {
		if _, err := writer.Write(chunk); err != nil {
			_ = writer.CloseWithError(err)
			t.Fatalf("write response chunk: %v", err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close response writer: %v", err)
	}
	if err := <-readDone; err != nil {
		t.Fatalf("read cached response body: %v", err)
	}
	if decision.Status != publicCacheStatusStored {
		t.Fatalf("final cache status = %q, want stored", decision.Status)
	}
}

func TestPublicCacheHeadServedFromCachedGet(t *testing.T) {
	app, resolution, closeDB := newTestPublicCacheApp(t)
	defer closeDB()

	req := httptest.NewRequest(http.MethodHead, "http://assets.example.test/assets/app.txt", nil)
	app.proxyMu.Lock()
	rule := app.publicSnapshot.CacheRules[0]
	app.proxyMu.Unlock()
	keyDigest := publicCacheKeyDigest(req, resolution, rule, "", nil)
	bodyPath := app.PublicCache.bodyPath(keyDigest)
	if err := os.MkdirAll(filepath.Dir(bodyPath), 0700); err != nil {
		t.Fatalf("create cache body dir: %v", err)
	}
	if err := os.WriteFile(bodyPath, []byte("head-body"), 0600); err != nil {
		t.Fatalf("write cache body: %v", err)
	}
	if _, err := app.DB.UpsertPublicCacheEntry(context.Background(), db.UpsertPublicCacheEntryParams{
		KeyDigest:           keyDigest,
		RuleID:              resolution.CacheRuleID,
		Scope:               publicCacheScopeSelectedBackend,
		ListenerProtocol:    resolution.Listener.Protocol,
		Host:                "assets.example.test",
		Path:                "/assets/app.txt",
		QueryKey:            "",
		RouteID:             sql.NullInt64{Int64: resolution.Route.ID, Valid: true},
		BackendID:           sql.NullInt64{},
		RouteTargetID:       sql.NullInt64{Int64: resolution.Target.ID, Valid: true},
		Method:              http.MethodGet,
		VaryHeadersJson:     "[]",
		ResponseHeadersJson: `{"Content-Type":["text/plain"]}`,
		StatusCode:          http.StatusOK,
		BodyPath:            bodyPath,
		SizeBytes:           int64(len("head-body")),
		ExpiresAt:           time.Now().Add(time.Hour),
	}); err != nil {
		t.Fatalf("insert cache entry: %v", err)
	}

	decision := app.checkPublicCache(req, resolution)
	if decision.Status != publicCacheStatusHit {
		t.Fatalf("HEAD cache status = %q, want hit", decision.Status)
	}
	rec := httptest.NewRecorder()
	app.servePublicCacheHit(rec, req, resolution, nil, nil, decision, proxyRequestObservability{})
	if rec.Code != http.StatusOK {
		t.Fatalf("HEAD status = %d, want 200", rec.Code)
	}
	if rec.Body.Len() != 0 {
		t.Fatalf("HEAD body length = %d, want 0", rec.Body.Len())
	}
}

func newTestPublicCacheApp(t *testing.T) (*App, publicRouteResolution, func()) {
	t.Helper()
	database, err := db.Open(filepath.Join(t.TempDir(), "cache-test.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	cacheDir := filepath.Join(t.TempDir(), "cache")
	app := NewApp(&config.Config{PublicCacheDir: cacheDir}, database)

	matchJSON, err := json.Marshal(mustPublicPolicyMatchCEL(t, `method in ["GET", "HEAD"] &&
		host_match(host, "assets.example.test") &&
		path_prefix(path, "/assets") &&
		path.endsWith(".txt")`))
	if err != nil {
		t.Fatalf("marshal match: %v", err)
	}
	row, err := database.CreatePublicCacheRule(context.Background(), db.CreatePublicCacheRuleParams{
		Name:                 "assets",
		Priority:             10,
		Enabled:              1,
		MatchJson:            string(matchJSON),
		RouteIdsJson:         "[]",
		BackendIdsJson:       "[]",
		TargetIdsJson:        "[]",
		Scope:                publicCacheScopeSelectedBackend,
		TtlMode:              publicCacheTTLModeFixed,
		TtlMillis:            defaultPublicCacheTTLMillis,
		QueryMode:            publicCacheQueryModeFull,
		QueryParamsJson:      "[]",
		VaryHeadersJson:      "[]",
		CacheStatusCodesJson: "[200]",
		MaxObjectBytes:       defaultPublicCacheMaxObjectBytes,
		AddCacheStatusHeader: 1,
		AllowCookieRequests:  0,
	})
	if err != nil {
		t.Fatalf("create cache rule: %v", err)
	}
	rule, err := publicCacheRuleRowToConfig(row)
	if err != nil {
		t.Fatalf("convert cache rule: %v", err)
	}

	resolution := publicRouteResolution{
		ListenerID:    sql.NullInt64{Int64: 1, Valid: true},
		RouteID:       sql.NullInt64{Int64: 10, Valid: true},
		RouteTargetID: sql.NullInt64{Int64: 30, Valid: true},
		Listener:      publicListenerConfig{ID: 1, Protocol: publicListenerProtocolHTTP},
		Route: publicRouteConfig{
			ID:                  10,
			TargetLoadBalancing: publicBackendLoadBalancingRoundRobin,
		},
		Target: publicRouteTargetConfig{
			ID:                            30,
			RouteID:                       10,
			Name:                          "assets-target",
			Enabled:                       true,
			TargetType:                    publicRouteTargetTypeProxy,
			Transport:                     publicRouteTargetTransportDirect,
			AgentLoadBalancing:            publicBackendLoadBalancingRoundRobin,
			UpstreamResponseHeaderTimeout: time.Second,
		},
		CacheRuleID: rule.ID,
	}
	resolution.Route.Targets = []publicRouteTargetConfig{resolution.Target}
	app.proxyMu.Lock()
	app.publicSnapshot = &publicProxySnapshot{
		CacheSettings: defaultPublicCacheSettings(),
		CacheRules:    []publicCacheRuleConfig{rule},
		RouteTargets:  map[int64]publicRouteTargetConfig{resolution.Target.ID: resolution.Target},
	}
	app.proxyMu.Unlock()
	app.PublicCache.reconcile(defaultPublicCacheSettings())

	return app, resolution, func() { database.Close() }
}

func setTestCacheRuleAllowCookieRequests(t *testing.T, app *App, allowed bool) {
	t.Helper()
	app.proxyMu.Lock()
	defer app.proxyMu.Unlock()
	if app.publicSnapshot == nil || len(app.publicSnapshot.CacheRules) == 0 {
		t.Fatal("test cache snapshot missing rule")
	}
	app.publicSnapshot.CacheRules[0].AllowCookieRequests = allowed
	app.publicSnapshot.CacheRules[0].Fingerprint = publicCacheRuleFingerprint(app.publicSnapshot.CacheRules[0])
}

func createTestAdminSession(t *testing.T, app *App) http.Header {
	t.Helper()
	passwordHash, err := authutil.HashPassword("very-good-test-password")
	if err != nil {
		t.Fatalf("hash password: %v", err)
	}
	user, err := app.DB.CreateUser(context.Background(), db.CreateUserParams{
		Username:     "admin",
		PasswordHash: passwordHash,
		Role:         "admin",
	})
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	token := "test-session-token"
	if _, err := app.DB.CreateSession(context.Background(), db.CreateSessionParams{
		UserID:    user.ID,
		TokenHash: hashSessionToken(token),
		ExpiresAt: time.Now().Add(time.Hour),
	}); err != nil {
		t.Fatalf("create session: %v", err)
	}
	header := http.Header{}
	header.Set("Cookie", (&http.Cookie{Name: sessionCookieName, Value: token}).String())
	return header
}
