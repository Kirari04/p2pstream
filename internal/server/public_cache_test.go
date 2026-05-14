package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

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
				req.Header.Set("Cookie", "sid=1")
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

func TestPublicCacheRulePathSuffixMatching(t *testing.T) {
	rule := publicCacheRuleConfig{
		ID:      1,
		Enabled: true,
		Match: publicRateLimitMatchConfig{
			Methods:      []string{http.MethodGet},
			HostPatterns: []string{"assets.example.test"},
			PathPrefixes: []string{"/assets"},
			PathSuffixes: []string{".css", ".woff2"},
		},
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

	resp = &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Set-Cookie": []string{"sid=1"}}}
	if _, _, ok := publicCacheResponseEligibility(rule, resp); ok {
		t.Fatal("Set-Cookie response should not be cacheable")
	}

	resp = &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Vary": []string{"*"}}}
	if _, _, ok := publicCacheResponseEligibility(rule, resp); ok {
		t.Fatal("Vary:* response should not be cacheable")
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
	resolution.Backend.ParsedOrigin = origin

	firstReq := httptest.NewRequest(http.MethodGet, "http://assets.example.test/assets/app.txt?v=1", nil)
	firstDecision := app.checkPublicCache(firstReq, resolution)
	if firstDecision.Status != publicCacheStatusMiss {
		t.Fatalf("first cache status = %q, want miss", firstDecision.Status)
	}
	firstRec := httptest.NewRecorder()
	app.proxyDirectRequest(firstRec, firstReq, resolution, nil, nil, &firstDecision, proxyRequestObservability{})
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
		BackendID:           sql.NullInt64{Int64: resolution.Backend.ID, Valid: true},
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

	matchJSON, err := json.Marshal(publicRateLimitMatchConfig{
		Methods:      []string{http.MethodGet, http.MethodHead},
		HostPatterns: []string{"assets.example.test"},
		PathPrefixes: []string{"/assets"},
		PathSuffixes: []string{".txt"},
	})
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
		Scope:                publicCacheScopeSelectedBackend,
		TtlMode:              publicCacheTTLModeFixed,
		TtlMillis:            defaultPublicCacheTTLMillis,
		QueryMode:            publicCacheQueryModeFull,
		QueryParamsJson:      "[]",
		VaryHeadersJson:      "[]",
		CacheStatusCodesJson: "[200]",
		MaxObjectBytes:       defaultPublicCacheMaxObjectBytes,
		AddCacheStatusHeader: 1,
	})
	if err != nil {
		t.Fatalf("create cache rule: %v", err)
	}
	rule, err := publicCacheRuleRowToConfig(row)
	if err != nil {
		t.Fatalf("convert cache rule: %v", err)
	}

	resolution := publicRouteResolution{
		ListenerID: sql.NullInt64{Int64: 1, Valid: true},
		RouteID:    sql.NullInt64{Int64: 10, Valid: true},
		BackendID:  sql.NullInt64{Int64: 20, Valid: true},
		Listener:   publicListenerConfig{ID: 1, Protocol: publicListenerProtocolHTTP},
		Route:      publicRouteConfig{ID: 10},
		Backend: publicBackendConfig{
			ID:                            20,
			Name:                          "assets-backend",
			Enabled:                       true,
			BackendType:                   publicBackendTypeProxyForward,
			ForwardMode:                   publicBackendForwardModeDirect,
			UpstreamResponseHeaderTimeout: time.Second,
		},
		CacheRuleID: rule.ID,
	}
	app.proxyMu.Lock()
	app.publicSnapshot = &publicProxySnapshot{
		CacheSettings: defaultPublicCacheSettings(),
		CacheRules:    []publicCacheRuleConfig{rule},
	}
	app.proxyMu.Unlock()
	app.PublicCache.reconcile(defaultPublicCacheSettings())

	return app, resolution, func() { database.Close() }
}
