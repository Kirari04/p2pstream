package server

import (
	"database/sql"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync/atomic"
	"testing"

	p2pstreamv1 "p2pstream/gen/proto/p2pstream/v1"
)

func TestNormalizeRequestHostCanonicalizesPortsAndTrailingDots(t *testing.T) {
	tests := []struct {
		name string
		host string
		want string
	}{
		{name: "dns", host: "example.com", want: "example.com"},
		{name: "dns trailing dot", host: "example.com.", want: "example.com"},
		{name: "dns with port", host: "example.com:443", want: "example.com"},
		{name: "dns trailing dot with port", host: "example.com.:443", want: "example.com"},
		{name: "ipv6 with port", host: "[2001:db8::1]:443", want: "2001:db8::1"},
		{name: "bracketed ipv6 without port", host: "[2001:db8::1]", want: "2001:db8::1"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := normalizeRequestHost(tc.host); got != tc.want {
				t.Fatalf("normalizeRequestHost(%q) = %q, want %q", tc.host, got, tc.want)
			}
		})
	}
}

func TestDottedHostWithPortMatchesRouteAndPolicyKey(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "http://app.example.:443/assets/app.js", nil)
	req.Host = "app.example.:443"
	listener := publicListenerConfig{ID: 1, Protocol: publicListenerProtocolHTTP}
	target := publicRouteTargetConfig{ID: 20, RouteID: 10, Enabled: true, TargetType: publicRouteTargetTypeStatic}
	route := publicRouteConfig{ID: 10, Enabled: true, HostPattern: "app.example", Targets: []publicRouteTargetConfig{target}}
	app := NewApp(nil, nil)
	app.publicSnapshot = &publicProxySnapshot{
		Listeners: map[int64]publicListenerConfig{1: {ID: 1, Protocol: publicListenerProtocolHTTP}},
		RoutesByListener: map[int64][]publicRouteConfig{
			1: {route},
		},
		RouteTargets: map[int64]publicRouteTargetConfig{20: target},
	}

	resolution, err := app.resolvePublicRoute(1, req)
	if err != nil {
		t.Fatalf("resolve route: %v", err)
	}
	if resolution.RouteID != (sql.NullInt64{Int64: 10, Valid: true}) {
		t.Fatalf("route id = %+v, want 10", resolution.RouteID)
	}

	match, err := validatePublicPolicyMatch(&p2pstreamv1.PublicPolicyMatchRule{CelExpression: `host == "app.example"`})
	if err != nil {
		t.Fatalf("compile match: %v", err)
	}
	if !match.matches(listener, req) {
		t.Fatal("policy host did not match canonical dotted host")
	}
}

func TestPublicProxyRejectsAmbiguousPathBeforePolicyAndRoute(t *testing.T) {
	wafRule := testWafRule(1, publicWafActionBlock)
	app, handler, hits := newTestPublicPathProxy(t, "/public", []publicWafRuleConfig{wafRule})

	app.PublicWAF.reconcile(app.publicSnapshot)

	for _, target := range []string{
		"http://app.example/public/%2e%2e/admin",
		"http://app.example/public/%2E%2E/admin",
		"http://app.example/public/%2fadmin",
		"http://app.example/public/%2Fadmin",
		"http://app.example/public/%5cadmin",
		"http://app.example/public/%5Cadmin",
		"http://app.example/public/../admin",
		"http://app.example/public/./admin",
	} {
		t.Run(target, func(t *testing.T) {
			rec := httptest.NewRecorder()
			handler(rec, httptest.NewRequest(http.MethodGet, target, nil))

			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want %d", rec.Code, http.StatusBadRequest)
			}
			if got := hits.Load(); got != 0 {
				t.Fatalf("upstream hits = %d, want 0", got)
			}
		})
	}
}

func TestPublicProxyAllowsSafeEncodedNonSeparatorPath(t *testing.T) {
	_, handler, hits := newTestPublicPathProxy(t, "/assets", nil)

	rec := httptest.NewRecorder()
	handler(rec, httptest.NewRequest(http.MethodGet, "http://app.example/assets/a%20b.txt", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%q, want %d", rec.Code, rec.Body.String(), http.StatusOK)
	}
	if got := hits.Load(); got != 1 {
		t.Fatalf("upstream hits = %d, want 1", got)
	}
}

func TestPublicProxyPlainSafePathStillWorks(t *testing.T) {
	_, handler, hits := newTestPublicPathProxy(t, "/assets", nil)

	rec := httptest.NewRecorder()
	handler(rec, httptest.NewRequest(http.MethodGet, "http://app.example/assets/app.js", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%q, want %d", rec.Code, rec.Body.String(), http.StatusOK)
	}
	if got := hits.Load(); got != 1 {
		t.Fatalf("upstream hits = %d, want 1", got)
	}
}

func TestPublicProxyACMEChallengeExactLiteralPathStillWorks(t *testing.T) {
	app := NewApp(nil, nil)
	app.PublicACME = &publicACMEManager{httpChallenges: make(map[string]string)}
	cleanup := app.PublicACME.SetHTTPChallenge("/.well-known/acme-challenge/token", "response")
	defer cleanup()

	rec := httptest.NewRecorder()
	app.publicProxyHandler(1)(rec, httptest.NewRequest(http.MethodGet, "http://app.example/.well-known/acme-challenge/token", nil))

	if rec.Code != http.StatusOK || rec.Body.String() != "response" {
		t.Fatalf("ACME response = status %d body %q, want 200 response", rec.Code, rec.Body.String())
	}
}

func TestPublicProxyWAFReservedExactLiteralPathStillHandled(t *testing.T) {
	app := NewApp(nil, nil)

	rec := httptest.NewRecorder()
	app.publicProxyHandler(1)(rec, httptest.NewRequest(http.MethodPost, "http://app.example"+publicWafCaptchaVerifyPath, nil))

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("WAF reserved response status = %d body=%q, want %d", rec.Code, rec.Body.String(), http.StatusBadRequest)
	}
}

func TestAgentSelectorMatchesAllLabelsAndExactAgent(t *testing.T) {
	tests := []struct {
		name     string
		selector publicAgentSelectorConfig
		labels   map[string]string
		want     bool
	}{
		{
			name:     "all user labels match",
			selector: publicAgentSelectorConfig{MatchLabels: map[string]string{"site": "home", "role": "app"}},
			labels:   map[string]string{"site": "home", "role": "app", "zone": "dmz"},
			want:     true,
		},
		{
			name:     "missing one user label",
			selector: publicAgentSelectorConfig{MatchLabels: map[string]string{"site": "home", "role": "app"}},
			labels:   map[string]string{"site": "home"},
			want:     false,
		},
		{
			name:     "system exact-agent label matches",
			selector: publicAgentSelectorConfig{MatchLabels: map[string]string{"p2pstream.io/agent-id": "agent-abc"}},
			labels:   map[string]string{"p2pstream.io/agent-id": "agent-abc"},
			want:     true,
		},
		{
			name:     "empty value matches only empty value",
			selector: publicAgentSelectorConfig{MatchLabels: map[string]string{"role": ""}},
			labels:   map[string]string{"role": ""},
			want:     true,
		},
		{
			name:     "empty selector never matches",
			selector: publicAgentSelectorConfig{MatchLabels: map[string]string{}},
			labels:   map[string]string{"site": "home"},
			want:     false,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := agentSelectorMatchesLabels(tc.selector, tc.labels); got != tc.want {
				t.Fatalf("agentSelectorMatchesLabels = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestRedirectPathSuffixNormalizesLocalTargets(t *testing.T) {
	for _, tc := range []struct {
		name   string
		target string
		prefix string
		want   string
	}{
		{name: "matched suffix", target: "http://example.com/api/users", prefix: "/api", want: "/users"},
		{name: "segment boundary mismatch", target: "http://example.com/apiv2", prefix: "/api", want: ""},
		{name: "scheme relative request path", target: "http://example.com//evil.example/path", prefix: "", want: "/evil.example/path"},
		{name: "backslash request path", target: `http://example.com/\evil.example/path`, prefix: "", want: "/evil.example/path"},
		{name: "scheme relative suffix", target: "http://example.com/app//evil.example/path", prefix: "/app", want: "/evil.example/path"},
		{name: "backslash suffix", target: `http://example.com/app/\evil.example/path`, prefix: "/app", want: "/evil.example/path"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, tc.target, nil)

			if got := redirectPathSuffix(req, tc.prefix); got != tc.want {
				t.Fatalf("redirectPathSuffix(%q, %q) = %q, want %q", tc.target, tc.prefix, got, tc.want)
			}
		})
	}
}

func TestJoinRedirectPathNormalizesUnsafeSuffixes(t *testing.T) {
	for _, tc := range []struct {
		name   string
		base   string
		suffix string
		want   string
	}{
		{name: "root with scheme relative suffix", base: "/", suffix: "//evil.example/path", want: "/evil.example/path"},
		{name: "root with backslash suffix", base: "/", suffix: `/\evil.example/path`, want: "/evil.example/path"},
		{name: "base with safe suffix", base: "/base", suffix: "/users", want: "/base/users"},
		{name: "empty base with relative suffix", base: "", suffix: "users", want: "/users"},
		{name: "unsafe base is normalized", base: "//configured.example", suffix: "users", want: "/configured.example/users"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := joinRedirectPath(tc.base, tc.suffix); got != tc.want {
				t.Fatalf("joinRedirectPath(%q, %q) = %q, want %q", tc.base, tc.suffix, got, tc.want)
			}
			if got := joinRedirectPath("/", tc.suffix); got != "" && !isSafeLocalRedirectTarget(got) {
				t.Fatalf("joinRedirectPath returned unsafe local redirect target %q", got)
			}
		})
	}
}

func TestRedirectLocationPreservesSafePathSuffix(t *testing.T) {
	route := publicRouteConfig{
		PathPrefix:                 "/api",
		RedirectTargetMode:         publicRouteRedirectTargetModeSameHostPath,
		RedirectTarget:             "/target",
		RedirectPreservePathSuffix: true,
	}
	req := httptest.NewRequest(http.MethodGet, "http://example.com/api/users?debug=1", nil)

	location, err := redirectLocationForRequest(req, route)
	if err != nil {
		t.Fatalf("redirectLocationForRequest returned error: %v", err)
	}
	if location != "/target/users" {
		t.Fatalf("location = %q, want %q", location, "/target/users")
	}
}

func newTestPublicPathProxy(t *testing.T, pathPrefix string, wafRules []publicWafRuleConfig) (*App, http.HandlerFunc, *atomic.Int64) {
	t.Helper()
	var hits atomic.Int64
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		_, _ = w.Write([]byte("ok"))
	}))
	t.Cleanup(upstream.Close)

	origin, err := url.Parse(upstream.URL)
	if err != nil {
		t.Fatalf("parse upstream URL: %v", err)
	}
	target := publicRouteTargetConfig{
		ID:         20,
		RouteID:    10,
		Name:       "upstream",
		Enabled:    true,
		TargetType: publicRouteTargetTypeProxy,
		Transport:  publicRouteTargetTransportDirect,
		ParsedURL:  origin,
	}
	route := publicRouteConfig{
		ID:         10,
		Enabled:    true,
		PathPrefix: pathPrefix,
		Action:     publicRouteActionForward,
		Targets:    []publicRouteTargetConfig{target},
	}
	app := NewApp(nil, nil)
	app.publicSnapshot = &publicProxySnapshot{
		Listeners: map[int64]publicListenerConfig{1: {ID: 1, Protocol: publicListenerProtocolHTTP, Enabled: true}},
		RoutesByListener: map[int64][]publicRouteConfig{
			1: {route},
		},
		RouteTargets:    map[int64]publicRouteTargetConfig{target.ID: target},
		WafRules:        wafRules,
		WafCookieSecret: []byte("test-secret"),
	}
	return app, app.publicProxyHandler(1), &hits
}
