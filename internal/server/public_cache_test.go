package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
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
			name: "cookie",
			req: func() *http.Request {
				req := httptest.NewRequest(http.MethodGet, "http://example.test/app.js", nil)
				req.Header.Set("Cookie", "sid=abc")
				return req
			}(),
			want: "cookie",
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

func TestPublicCacheCookieRequestAllowedByLegacyRuleStillBypasses(t *testing.T) {
	app, resolution, closeDB := newTestPublicCacheApp(t)
	defer closeDB()
	setTestCacheRuleAllowCookieRequests(t, app, true)

	req := httptest.NewRequest(http.MethodGet, "http://assets.example.test/assets/app.txt", nil)
	req.Header.Set("Cookie", "sid=1")

	decision := app.checkPublicCache(req, resolution)
	if decision.Status != publicCacheStatusBypass || decision.BypassReason != "cookie" {
		t.Fatalf("cookie request cache decision = %q/%q, want bypass/cookie", decision.Status, decision.BypassReason)
	}
	if !decision.CookieRequest {
		t.Fatal("expected cookie request trace marker on decision")
	}
	if decision.Cacheable {
		t.Fatal("cookie request with legacy allow flag should not be cacheable")
	}
}

func TestPublicCacheCookieRequestDoesNotPopulateOrHitCache(t *testing.T) {
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
	if firstDecision.Status != publicCacheStatusBypass || firstDecision.BypassReason != "cookie" {
		t.Fatalf("first cache status = %q/%q, want bypass/cookie", firstDecision.Status, firstDecision.BypassReason)
	}
	firstRec := httptest.NewRecorder()
	app.proxyDirectTargetRequest(firstRec, firstReq, resolution, nil, nil, &firstDecision, proxyRequestObservability{})
	if firstRec.Code != http.StatusOK {
		t.Fatalf("first response status = %d, want 200", firstRec.Code)
	}
	if firstDecision.Status != publicCacheStatusBypass {
		t.Fatalf("first decision after proxy = %q, want bypass", firstDecision.Status)
	}

	secondReq := httptest.NewRequest(http.MethodGet, "http://assets.example.test/assets/app.txt", nil)
	secondDecision := app.checkPublicCache(secondReq, resolution)
	if secondDecision.Status != publicCacheStatusMiss {
		t.Fatalf("second cache status = %q/%q, want miss because cookie request did not populate cache", secondDecision.Status, secondDecision.BypassReason)
	}
	secondRec := httptest.NewRecorder()
	app.proxyDirectTargetRequest(secondRec, secondReq, resolution, nil, nil, &secondDecision, proxyRequestObservability{})
	if secondRec.Code != http.StatusOK {
		t.Fatalf("second response status = %d, want 200", secondRec.Code)
	}
	if secondDecision.Status != publicCacheStatusStored {
		t.Fatalf("second decision after proxy = %q, want stored", secondDecision.Status)
	}

	thirdReq := httptest.NewRequest(http.MethodGet, "http://assets.example.test/assets/app.txt", nil)
	thirdReq.Header.Set("Cookie", "sid=b")
	thirdDecision := app.checkPublicCache(thirdReq, resolution)
	if thirdDecision.Status != publicCacheStatusBypass || thirdDecision.BypassReason != "cookie" {
		t.Fatalf("third cache status = %q/%q, want bypass/cookie despite stored non-cookie response", thirdDecision.Status, thirdDecision.BypassReason)
	}
	thirdRec := httptest.NewRecorder()
	app.proxyDirectTargetRequest(thirdRec, thirdReq, resolution, nil, nil, &thirdDecision, proxyRequestObservability{})
	if thirdRec.Code != http.StatusOK {
		t.Fatalf("third response status = %d, want 200", thirdRec.Code)
	}
	if originHits != 3 {
		t.Fatalf("origin hits = %d, want 3", originHits)
	}
}

func TestPublicCacheCompatibilityRouteEncodedSeparatorBypasses(t *testing.T) {
	app, resolution, closeDB := newTestPublicCacheApp(t)
	defer closeDB()
	resolution.Route.PathSecurityMode = publicRoutePathSecurityModeAllowEncodedSeparators

	req := httptest.NewRequest(http.MethodGet, "http://assets.example.test/assets/group%2Fproject.txt", nil)
	decision := app.checkPublicCache(req, resolution)
	if decision.Status != publicCacheStatusBypass || decision.BypassReason != "encoded_path" {
		t.Fatalf("encoded separator cache decision = %q/%q, want bypass/encoded_path", decision.Status, decision.BypassReason)
	}
	if decision.Cacheable {
		t.Fatal("encoded separator compatibility request should not be cacheable")
	}
}

func TestPublicCacheCompatibilityRouteEncodedSeparatorDoesNotPopulateOrHitCache(t *testing.T) {
	app, resolution, closeDB := newTestPublicCacheApp(t)
	defer closeDB()
	resolution.Route.PathSecurityMode = publicRoutePathSecurityModeAllowEncodedSeparators

	originHits := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		originHits++
		w.Header().Set("Content-Type", "text/plain")
		w.Header().Set("Cache-Control", "max-age=300")
		_, _ = w.Write([]byte("encoded-asset"))
	}))
	defer upstream.Close()

	origin, err := url.Parse(upstream.URL)
	if err != nil {
		t.Fatalf("parse upstream URL: %v", err)
	}
	resolution.Target.ParsedURL = origin

	for i := 0; i < 2; i++ {
		req := httptest.NewRequest(http.MethodGet, "http://assets.example.test/assets/group%2Fproject.txt", nil)
		decision := app.checkPublicCache(req, resolution)
		if decision.Status != publicCacheStatusBypass || decision.BypassReason != "encoded_path" {
			t.Fatalf("request %d cache decision = %q/%q, want bypass/encoded_path", i+1, decision.Status, decision.BypassReason)
		}
		rec := httptest.NewRecorder()
		app.proxyDirectTargetRequest(rec, req, resolution, nil, nil, &decision, proxyRequestObservability{})
		if rec.Code != http.StatusOK || rec.Body.String() != "encoded-asset" {
			t.Fatalf("request %d response = status %d body %q, want 200 encoded-asset", i+1, rec.Code, rec.Body.String())
		}
	}
	if originHits != 2 {
		t.Fatalf("origin hits = %d, want 2", originHits)
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
		Target:   publicRouteTargetConfig{ID: 20},
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
	resolution := publicRouteResolution{Route: publicRouteConfig{ID: 10}, Target: publicRouteTargetConfig{ID: 20}}

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

	for _, header := range []string{"X-Forwarded-Host", "X-Forwarded-Port", "Forwarded", "X-Real-IP"} {
		resp = &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Vary": []string{header}}}
		if _, _, ok := publicCacheResponseEligibility(rule, resp); ok {
			t.Fatalf("Vary: %s response should not be cacheable", header)
		}
	}
}

func TestPublicCacheSetCookieResponseNotStored(t *testing.T) {
	app, resolution, closeDB := newTestPublicCacheApp(t)
	defer closeDB()

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

	for _, header := range []string{"Cookie", "Authorization", "Set-Cookie", "Forwarded", "X-Forwarded-For", "X-Forwarded-Host", "X-Forwarded-Proto", "X-Forwarded-Port", "X-Real-IP"} {
		if _, err := app.validatePublicCacheRuleInput(context.Background(), "bad-vary", 10, true, nil, nil, p2pstreamv1.PublicCacheScope_PUBLIC_CACHE_SCOPE_SELECTED_BACKEND, p2pstreamv1.PublicCacheTtlMode_PUBLIC_CACHE_TTL_MODE_FIXED, defaultPublicCacheTTLMillis, p2pstreamv1.PublicCacheQueryMode_PUBLIC_CACHE_QUERY_MODE_FULL, nil, []string{header}, []int64{http.StatusOK}, defaultPublicCacheMaxObjectBytes, true, false, false, nil); err == nil {
			t.Fatalf("expected validation error for configured vary header %q", header)
		}
	}
}

func TestPublicCacheGeneratedForwardedVaryHeadersAreCaseInsensitive(t *testing.T) {
	rule := publicCacheRuleConfig{
		TTL:              time.Hour,
		VaryHeaders:      []string{"Accept-Encoding"},
		CacheStatusCodes: []int64{http.StatusOK},
		MaxObjectBytes:   defaultPublicCacheMaxObjectBytes,
	}
	for _, header := range []string{"x-forwarded-host", "X-FoRwArDeD-PoRt"} {
		resp := &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Vary": []string{header}}}
		if _, _, ok := publicCacheResponseEligibility(rule, resp); ok {
			t.Fatalf("Vary: %s response should not be cacheable", header)
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

func TestPublicCacheManagementAPIAcceptsLegacyCookieRequestFlagWithoutAcknowledgement(t *testing.T) {
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
	// allow_cookie_requests is deprecated and ineffective at runtime, so the legacy
	// acknowledgement is no longer required: creating a rule with the flag set (and no
	// acknowledgement) now succeeds.
	if _, err := app.CreatePublicCacheRule(context.Background(), createReq); err != nil {
		t.Fatalf("creating rule with legacy allow_cookie_requests flag failed: %v", err)
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

	if secondDecision.Entry == nil {
		t.Fatal("second decision missing cache entry")
	}
	if err := app.DB.DeletePublicCacheEntry(context.Background(), secondDecision.Entry.KeyDigest); err != nil {
		t.Fatalf("delete warmed cache DB row: %v", err)
	}
	if err := os.Remove(secondDecision.Entry.BodyPath); err != nil {
		t.Fatalf("remove warmed cache body: %v", err)
	}
	thirdReq := httptest.NewRequest(http.MethodGet, "http://assets.example.test/assets/app.txt?v=1", nil)
	thirdDecision := app.checkPublicCache(thirdReq, resolution)
	if thirdDecision.Status != publicCacheStatusHit {
		t.Fatalf("third warmed cache status = %q, want hit", thirdDecision.Status)
	}
	thirdRec := httptest.NewRecorder()
	app.servePublicCacheHit(thirdRec, thirdReq, resolution, nil, nil, thirdDecision, proxyRequestObservability{})
	if thirdRec.Code != http.StatusOK || thirdRec.Body.String() != "asset-v1" {
		t.Fatalf("third warmed response = status %d body %q, want cached asset", thirdRec.Code, thirdRec.Body.String())
	}
}

func TestPublicCacheStaleIndexedBodyFallsBackToMiss(t *testing.T) {
	app, resolution, closeDB := newTestPublicCacheApp(t)
	defer closeDB()

	req := httptest.NewRequest(http.MethodGet, "http://assets.example.test/assets/stale.txt", nil)
	app.proxyMu.Lock()
	rule := app.publicSnapshot.CacheRules[0]
	app.proxyMu.Unlock()
	keyDigest := publicCacheKeyDigest(req, resolution, rule, "", nil)
	bodyPath := app.PublicCache.bodyPath(keyDigest)
	if err := os.MkdirAll(filepath.Dir(bodyPath), 0700); err != nil {
		t.Fatalf("create cache body dir: %v", err)
	}
	if err := os.WriteFile(bodyPath, []byte("stale-body"), 0600); err != nil {
		t.Fatalf("write cache body: %v", err)
	}
	if _, err := app.DB.UpsertPublicCacheEntry(context.Background(), db.UpsertPublicCacheEntryParams{
		KeyDigest:           keyDigest,
		RuleID:              resolution.CacheRuleID,
		Scope:               publicCacheScopeSelectedBackend,
		ListenerProtocol:    resolution.Listener.Protocol,
		Host:                "assets.example.test",
		Path:                "/assets/stale.txt",
		QueryKey:            "",
		RouteID:             sql.NullInt64{Int64: resolution.Route.ID, Valid: true},
		RouteTargetID:       sql.NullInt64{Int64: resolution.Target.ID, Valid: true},
		Method:              http.MethodGet,
		VaryHeadersJson:     "[]",
		ResponseHeadersJson: `{"Content-Type":["text/plain"]}`,
		StatusCode:          http.StatusOK,
		BodyPath:            bodyPath,
		SizeBytes:           int64(len("stale-body")),
		ExpiresAt:           time.Now().Add(time.Hour),
	}); err != nil {
		t.Fatalf("insert cache entry: %v", err)
	}

	firstDecision := app.checkPublicCache(req, resolution)
	if firstDecision.Status != publicCacheStatusHit {
		t.Fatalf("first cache status = %q, want hit", firstDecision.Status)
	}
	if err := os.Remove(bodyPath); err != nil {
		t.Fatalf("remove cache body: %v", err)
	}
	secondDecision := app.checkPublicCache(req, resolution)
	if secondDecision.Status != publicCacheStatusHit {
		t.Fatalf("second indexed status = %q, want hit before body preparation", secondDecision.Status)
	}
	if ok := app.preparePublicCacheHitBody(req, &secondDecision); ok {
		t.Fatal("prepare cache hit body succeeded after body file was removed")
	}
	thirdDecision := app.checkPublicCache(req, resolution)
	if thirdDecision.Status != publicCacheStatusMiss {
		t.Fatalf("third cache status = %q, want miss after stale entry invalidation", thirdDecision.Status)
	}
}

func TestPublicCacheEmptyMissUsesWarmedIndex(t *testing.T) {
	app, resolution, closeDB := newTestPublicCacheApp(t)
	defer closeDB()

	req := httptest.NewRequest(http.MethodGet, "http://assets.example.test/assets/later.txt", nil)
	firstDecision := app.checkPublicCache(req, resolution)
	if firstDecision.Status != publicCacheStatusMiss {
		t.Fatalf("first cache status = %q, want miss", firstDecision.Status)
	}
	app.proxyMu.Lock()
	rule := app.publicSnapshot.CacheRules[0]
	app.proxyMu.Unlock()
	keyDigest := publicCacheKeyDigest(req, resolution, rule, "", nil)
	bodyPath := app.PublicCache.bodyPath(keyDigest)
	if err := os.MkdirAll(filepath.Dir(bodyPath), 0700); err != nil {
		t.Fatalf("create cache body dir: %v", err)
	}
	if err := os.WriteFile(bodyPath, []byte("later-body"), 0600); err != nil {
		t.Fatalf("write cache body: %v", err)
	}
	entry, err := app.DB.UpsertPublicCacheEntry(context.Background(), db.UpsertPublicCacheEntryParams{
		KeyDigest:           keyDigest,
		RuleID:              resolution.CacheRuleID,
		Scope:               publicCacheScopeSelectedBackend,
		ListenerProtocol:    resolution.Listener.Protocol,
		Host:                "assets.example.test",
		Path:                "/assets/later.txt",
		QueryKey:            "",
		RouteID:             sql.NullInt64{Int64: resolution.Route.ID, Valid: true},
		RouteTargetID:       sql.NullInt64{Int64: resolution.Target.ID, Valid: true},
		Method:              http.MethodGet,
		VaryHeadersJson:     "[]",
		ResponseHeadersJson: `{"Content-Type":["text/plain"]}`,
		StatusCode:          http.StatusOK,
		BodyPath:            bodyPath,
		SizeBytes:           int64(len("later-body")),
		ExpiresAt:           time.Now().Add(time.Hour),
	})
	if err != nil {
		t.Fatalf("insert cache entry: %v", err)
	}

	secondDecision := app.checkPublicCache(req, resolution)
	if secondDecision.Status != publicCacheStatusMiss {
		t.Fatalf("second warmed-empty cache status = %q, want miss", secondDecision.Status)
	}
	app.PublicCache.putIndexEntry(entry)
	thirdDecision := app.checkPublicCache(req, resolution)
	if thirdDecision.Status != publicCacheStatusHit {
		t.Fatalf("third indexed cache status = %q, want hit", thirdDecision.Status)
	}
}

func TestPublicCacheNegativeLookupIndexIsBoundedAndExpires(t *testing.T) {
	newCache := func(t *testing.T, maxEntries int64) (*publicProxyCache, uint64, string) {
		t.Helper()
		cache := newPublicProxyCache(t.TempDir())
		settings := defaultPublicCacheSettings()
		settings.MaxEntries = maxEntries
		rules := []publicCacheRuleConfig{{ID: 1, Enabled: true, Fingerprint: "rule-v1"}}
		fingerprint := publicCacheRuntimeFingerprint(settings, rules)
		cache.reconcile(settings, rules)
		generation, ok := cache.captureGeneration(fingerprint)
		if !ok {
			t.Fatal("cache generation was not current after reconcile")
		}
		return cache, generation, fingerprint
	}

	t.Run("configured max entries and ttl", func(t *testing.T) {
		cache, generation, fingerprint := newCache(t, 3)
		now := time.Unix(1_700_000_000, 0)
		lookups := make([]publicCacheLookupKey, 5)
		for i := range lookups {
			lookups[i] = publicCacheLookupKey{
				RuleID:           1,
				ListenerProtocol: publicListenerProtocolHTTP,
				Host:             "attacker.example",
				Path:             "/uncached/" + strconv.Itoa(i),
				QueryKey:         "nonce=" + strconv.Itoa(i),
			}
			cache.storeIndexedCandidates(lookups[i], nil, now, generation, fingerprint)
		}

		cache.mu.Lock()
		negativeCount := len(cache.negativeLookups)
		orderCount := cache.negativeOrder.Len()
		cache.mu.Unlock()
		if negativeCount != 3 || orderCount != 3 {
			t.Fatalf("negative lookup cardinality = map %d/order %d, want 3/3", negativeCount, orderCount)
		}
		if _, loaded := cache.lookupIndexedCandidates(lookups[0], now, generation, fingerprint); loaded {
			t.Fatal("oldest negative lookup was not evicted at MaxEntries")
		}
		if _, loaded := cache.lookupIndexedCandidates(lookups[len(lookups)-1], now, generation, fingerprint); !loaded {
			t.Fatal("newest negative lookup was not cached")
		}
		if _, loaded := cache.lookupIndexedCandidates(lookups[len(lookups)-1], now.Add(publicCacheNegativeLookupTTL), generation, fingerprint); loaded {
			t.Fatal("negative lookup remained loaded at its TTL boundary")
		}
	})

	t.Run("hard safety cap", func(t *testing.T) {
		cache, generation, fingerprint := newCache(t, defaultPublicCacheMaxEntries)
		now := time.Unix(1_700_000_000, 0)
		for i := 0; i < maxPublicCacheNegativeLookups+64; i++ {
			cache.storeIndexedCandidates(publicCacheLookupKey{
				RuleID:           1,
				ListenerProtocol: publicListenerProtocolHTTP,
				Host:             "attacker.example",
				Path:             "/uncached/" + strconv.Itoa(i),
			}, nil, now, generation, fingerprint)
		}
		cache.mu.Lock()
		negativeCount := len(cache.negativeLookups)
		cache.mu.Unlock()
		if negativeCount != maxPublicCacheNegativeLookups {
			t.Fatalf("negative lookup cardinality = %d, want hard cap %d", negativeCount, maxPublicCacheNegativeLookups)
		}
	})
}

func TestPublicCacheIndexMaintainsPositiveAndNegativeLookupConsistency(t *testing.T) {
	cache := newPublicProxyCache(t.TempDir())
	settings := defaultPublicCacheSettings()
	settings.MaxEntries = 2
	rules := []publicCacheRuleConfig{{ID: 1, Enabled: true, Fingerprint: "rule-v1"}}
	fingerprint := publicCacheRuntimeFingerprint(settings, rules)
	cache.reconcile(settings, rules)
	generation, ok := cache.captureGeneration(fingerprint)
	if !ok {
		t.Fatal("cache generation was not current after reconcile")
	}
	now := time.Now()
	lookupA := publicCacheLookupKey{RuleID: 1, ListenerProtocol: publicListenerProtocolHTTP, Host: "a.example", Path: "/asset"}
	lookupB := publicCacheLookupKey{RuleID: 1, ListenerProtocol: publicListenerProtocolHTTP, Host: "b.example", Path: "/asset"}
	lookupC := publicCacheLookupKey{RuleID: 1, ListenerProtocol: publicListenerProtocolHTTP, Host: "c.example", Path: "/asset"}
	cache.storeIndexedCandidates(lookupA, nil, now, generation, fingerprint)
	cache.storeIndexedCandidates(lookupB, nil, now, generation, fingerprint)

	entry := db.PublicCacheEntry{
		KeyDigest:        "digest",
		RuleID:           lookupB.RuleID,
		ListenerProtocol: lookupB.ListenerProtocol,
		Host:             lookupB.Host,
		Path:             lookupB.Path,
		ExpiresAt:        now.Add(time.Hour),
	}
	cache.putIndexEntry(entry)
	cache.mu.Lock()
	if _, exists := cache.negativeLookups[publicCacheLookupKeyHash(lookupB)]; exists {
		cache.mu.Unlock()
		t.Fatal("positive insertion retained a contradictory negative lookup")
	}
	if got := len(cache.negativeLookups) + len(cache.indexEntries); got != 2 {
		cache.mu.Unlock()
		t.Fatalf("combined index cardinality = %d, want MaxEntries 2", got)
	}
	cache.mu.Unlock()

	entry.Host = lookupC.Host
	cache.putIndexEntry(entry)
	cache.mu.Lock()
	_, oldLookupPresent := cache.indexLookups[lookupB]
	newDigests := cache.indexLookups[lookupC]
	_, newDigestPresent := newDigests[entry.KeyDigest]
	cache.mu.Unlock()
	if oldLookupPresent || !newDigestPresent {
		t.Fatalf("moved positive index consistency = old lookup %v/new digest %v, want false/true", oldLookupPresent, newDigestPresent)
	}

	cache.deleteEntry(entry.KeyDigest)
	cache.mu.Lock()
	_, entryPresent := cache.indexEntries[entry.KeyDigest]
	_, lookupPresent := cache.indexLookups[lookupC]
	cache.mu.Unlock()
	if entryPresent || lookupPresent {
		t.Fatalf("deleted positive index retained state = entry %v/lookup %v", entryPresent, lookupPresent)
	}
}

func TestPublicCacheStaleConcurrentLookupLoadsCannotRepopulateAfterReconcile(t *testing.T) {
	cache := newPublicProxyCache(t.TempDir())
	settings := defaultPublicCacheSettings()
	oldRules := []publicCacheRuleConfig{{ID: 1, Enabled: true, Fingerprint: "rule-v1"}}
	oldFingerprint := publicCacheRuntimeFingerprint(settings, oldRules)
	cache.reconcile(settings, oldRules)
	oldGeneration, ok := cache.captureGeneration(oldFingerprint)
	if !ok {
		t.Fatal("old cache generation was not current after reconcile")
	}
	now := time.Now()
	entry := db.PublicCacheEntry{
		KeyDigest:        "stale-digest",
		RuleID:           1,
		ListenerProtocol: publicListenerProtocolHTTP,
		Host:             "assets.example",
		Path:             "/asset",
		ExpiresAt:        now.Add(time.Hour),
	}
	lookup := publicCacheLookupKeyFromEntry(entry)

	start := make(chan struct{})
	var workers sync.WaitGroup
	for i := 0; i < 32; i++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			<-start
			for j := 0; j < 100; j++ {
				cache.storeIndexedCandidates(lookup, []db.PublicCacheEntry{entry}, now, oldGeneration, oldFingerprint)
			}
		}()
	}
	workers.Add(1)
	go func() {
		defer workers.Done()
		<-start
		cache.reconcile(settings, []publicCacheRuleConfig{{ID: 1, Enabled: true, Fingerprint: "rule-v2"}})
	}()
	close(start)
	workers.Wait()

	cache.mu.Lock()
	entryCount := len(cache.indexEntries)
	lookupCount := len(cache.indexLookups)
	negativeCount := len(cache.negativeLookups)
	cache.mu.Unlock()
	if entryCount != 0 || lookupCount != 0 || negativeCount != 0 {
		t.Fatalf("stale lookup load repopulated index after reconcile: entries=%d lookups=%d negatives=%d", entryCount, lookupCount, negativeCount)
	}
}

func TestPublicCacheInFlightOldRuleResponseIsDiscardedAfterReconcile(t *testing.T) {
	app, resolution, closeDB := newTestPublicCacheApp(t)
	defer closeDB()

	req := httptest.NewRequest(http.MethodGet, "http://assets.example.test/assets/config-race.txt", nil)
	decision := app.checkPublicCache(req, resolution)
	if decision.Status != publicCacheStatusMiss {
		t.Fatalf("cache status = %q, want miss", decision.Status)
	}
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header: http.Header{
			"Cache-Control": []string{"max-age=300"},
			"Content-Type":  []string{"text/plain"},
			"Vary":          []string{"Accept-Encoding"},
		},
		Body: io.NopCloser(strings.NewReader("old-rule-body")),
	}
	body := app.capturePublicCacheResponseBody(context.Background(), req, resolution, &decision, resp, nil)
	store, ok := body.(*publicCacheStoreReadCloser)
	if !ok {
		t.Fatalf("cache response body type = %T, want *publicCacheStoreReadCloser", body)
	}

	newRule := decision.Rule
	newRule.Scope = publicCacheScopeRoute
	newRule.TTL = 2 * time.Hour
	newRule.VaryHeaders = []string{"Accept-Language"}
	newRule.Fingerprint = publicCacheRuleFingerprint(newRule)
	settings := defaultPublicCacheSettings()
	newRules := []publicCacheRuleConfig{newRule}
	newSnapshot := &publicProxySnapshot{
		CacheSettings:    settings,
		CacheRules:       newRules,
		CacheFingerprint: publicCacheRuntimeFingerprint(settings, newRules),
	}
	setPublicSnapshotForTest(t, app, newSnapshot)
	app.PublicCache.reconcile(settings, newRules)

	if _, err := io.ReadAll(body); err != nil {
		t.Fatalf("read old-rule response: %v", err)
	}
	if err := body.Close(); err != nil {
		t.Fatalf("close old-rule response: %v", err)
	}
	if decision.Status != publicCacheStatusMiss {
		t.Fatalf("stale response decision status = %q, want miss", decision.Status)
	}
	if _, err := app.DB.GetPublicCacheEntry(context.Background(), store.keyDigest); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("stale cache DB lookup error = %v, want sql.ErrNoRows", err)
	}
	if _, err := os.Stat(store.finalPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("stale cache body stat error = %v, want os.ErrNotExist", err)
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
		for {
			select {
			case <-stopReconcile:
				return
			default:
			}
			settings := defaultPublicCacheSettings()
			app.proxyMu.Lock()
			rules := append([]publicCacheRuleConfig(nil), app.publicSnapshot.CacheRules...)
			app.proxyMu.Unlock()
			app.PublicCache.reconcile(settings, rules)
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
			TargetLoadBalancing: publicRouteTargetLoadBalancingRoundRobin,
		},
		Target: publicRouteTargetConfig{
			ID:                            30,
			RouteID:                       10,
			Name:                          "assets-target",
			Enabled:                       true,
			TargetType:                    publicRouteTargetTypeProxy,
			Transport:                     publicRouteTargetTransportDirect,
			AgentLoadBalancing:            publicRouteTargetLoadBalancingRoundRobin,
			UpstreamResponseHeaderTimeout: time.Second,
		},
		CacheRuleID: rule.ID,
	}
	resolution.Route.Targets = []publicRouteTargetConfig{resolution.Target}
	settings := defaultPublicCacheSettings()
	rules := []publicCacheRuleConfig{rule}
	setPublicSnapshotForTest(t, app, &publicProxySnapshot{
		CacheSettings:    settings,
		CacheRules:       rules,
		CacheFingerprint: publicCacheRuntimeFingerprint(settings, rules),
		RouteTargets:     map[int64]publicRouteTargetConfig{resolution.Target.ID: resolution.Target},
	})
	app.PublicCache.reconcile(settings, rules)

	return app, resolution, func() { database.Close() }
}

func setTestCacheRuleAllowCookieRequests(t *testing.T, app *App, allowed bool) {
	t.Helper()
	app.proxyMu.Lock()
	if app.publicSnapshot == nil || len(app.publicSnapshot.CacheRules) == 0 {
		app.proxyMu.Unlock()
		t.Fatal("test cache snapshot missing rule")
	}
	app.publicSnapshot.CacheRules[0].AllowCookieRequests = allowed
	app.publicSnapshot.CacheRules[0].Fingerprint = publicCacheRuleFingerprint(app.publicSnapshot.CacheRules[0])
	settings := app.publicSnapshot.CacheSettings
	rules := append([]publicCacheRuleConfig(nil), app.publicSnapshot.CacheRules...)
	app.publicSnapshot.CacheFingerprint = publicCacheRuntimeFingerprint(settings, rules)
	app.proxyMu.Unlock()
	app.PublicCache.reconcile(settings, rules)
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
