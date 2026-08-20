package server

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"connectrpc.com/connect"

	p2pstreamv1 "p2pstream/gen/proto/p2pstream/v1"
	"p2pstream/internal/db"
)

func TestPublicAccessManagementAPIAndRouteAssignment(t *testing.T) {
	app := NewApp(nil, newServerTestDB(t))
	header := createTestAdminSession(t, app)
	ctx := context.Background()

	providerReq := connect.NewRequest(&p2pstreamv1.CreatePublicAccessProviderRequest{
		Name:           "company-sso",
		ProviderType:   p2pstreamv1.PublicAccessProviderType_PUBLIC_ACCESS_PROVIDER_TYPE_FORWARD_AUTH,
		Enabled:        true,
		ForwardAuthUrl: "https://auth.example.test/oauth2/auth",
		TimeoutMillis:  2500,
		ForwardedHeaders: []string{
			"X-Auth-Request-User",
			"X-Auth-Request-Email",
			"X-Auth-Request-Groups",
		},
	})
	providerReq.Header().Set("Cookie", header.Get("Cookie"))
	providerResp, err := app.CreatePublicAccessProvider(ctx, providerReq)
	if err != nil {
		t.Fatalf("create access provider: %v", err)
	}
	if providerResp.Msg.Provider == nil || providerResp.Msg.Provider.SubjectHeader != "X-Auth-Request-Preferred-Username" {
		t.Fatalf("provider readback = %+v", providerResp.Msg.Provider)
	}

	policyReq := connect.NewRequest(&p2pstreamv1.CreatePublicAccessPolicyRequest{
		Name:           "engineering",
		ProviderId:     providerResp.Msg.Provider.Id,
		Enabled:        true,
		RequiredGroups: []string{"operators", "engineering"},
		GroupMatch:     p2pstreamv1.PublicAccessGroupMatch_PUBLIC_ACCESS_GROUP_MATCH_ANY,
	})
	policyReq.Header().Set("Cookie", header.Get("Cookie"))
	policyResp, err := app.CreatePublicAccessPolicy(ctx, policyReq)
	if err != nil {
		t.Fatalf("create access policy: %v", err)
	}
	if policyResp.Msg.Policy == nil || len(policyResp.Msg.Policy.RequiredGroups) != 2 {
		t.Fatalf("policy readback = %+v", policyResp.Msg.Policy)
	}

	listener, err := app.DB.CreatePublicListener(ctx, db.CreatePublicListenerParams{
		Name: "public", Port: 8080, Protocol: publicListenerProtocolHTTP, Enabled: 1,
	})
	if err != nil {
		t.Fatalf("create listener: %v", err)
	}
	routeReq := connect.NewRequest(&p2pstreamv1.CreatePublicRouteRequest{
		ListenerId: listener.ID, Priority: 10, PathPrefix: "/private", Enabled: true,
		Action:             p2pstreamv1.PublicRouteAction_PUBLIC_ROUTE_ACTION_REDIRECT,
		RedirectTargetMode: p2pstreamv1.PublicRouteRedirectTargetMode_PUBLIC_ROUTE_REDIRECT_TARGET_MODE_SAME_HOST_PATH,
		RedirectTarget:     "/signed-in", RedirectStatusCode: http.StatusFound,
		RedirectPreservePathSuffix: true, RedirectPreserveQuery: true,
		AccessPolicyId: policyResp.Msg.Policy.Id,
	})
	routeReq.Header().Set("Cookie", header.Get("Cookie"))
	routeResp, err := app.CreatePublicRoute(ctx, routeReq)
	if err != nil {
		t.Fatalf("create protected route: %v", err)
	}
	if routeResp.Msg.Route == nil || routeResp.Msg.Route.AccessPolicyId != policyResp.Msg.Policy.Id {
		t.Fatalf("route access policy = %+v", routeResp.Msg.Route)
	}

	configReq := connect.NewRequest(&p2pstreamv1.GetPublicProxyConfigRequest{})
	configReq.Header().Set("Cookie", header.Get("Cookie"))
	configResp, err := app.GetPublicProxyConfig(ctx, configReq)
	if err != nil {
		t.Fatalf("get public proxy config: %v", err)
	}
	if len(configResp.Msg.AccessProviders) != 1 || len(configResp.Msg.AccessPolicies) != 1 {
		t.Fatalf("access config = %d providers, %d policies", len(configResp.Msg.AccessProviders), len(configResp.Msg.AccessPolicies))
	}

	deletePolicyReq := connect.NewRequest(&p2pstreamv1.DeletePublicAccessPolicyRequest{Id: policyResp.Msg.Policy.Id})
	deletePolicyReq.Header().Set("Cookie", header.Get("Cookie"))
	if _, err := app.DeletePublicAccessPolicy(ctx, deletePolicyReq); err == nil {
		t.Fatal("deleting a route-assigned access policy succeeded")
	}
	deleteProviderReq := connect.NewRequest(&p2pstreamv1.DeletePublicAccessProviderRequest{Id: providerResp.Msg.Provider.Id})
	deleteProviderReq.Header().Set("Cookie", header.Get("Cookie"))
	if _, err := app.DeletePublicAccessProvider(ctx, deleteProviderReq); err == nil {
		t.Fatal("deleting an access provider used by a policy succeeded")
	}
}

func TestPublicAccessForwardAuthInjectsTrustedIdentity(t *testing.T) {
	var authRequest atomic.Pointer[http.Request]
	auth := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authRequest.Store(r.Clone(r.Context()))
		w.Header().Set("X-Auth-Request-User", "alice")
		w.Header().Set("X-Auth-Request-Email", "alice@example.test")
		w.Header().Set("X-Auth-Request-Groups", "engineering, operators")
		w.Header().Set("X-Auth-Request-Preferred-Username", "alice")
		w.Header().Add("Set-Cookie", "auth_session=renewed; Path=/; HttpOnly")
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(auth.Close)

	upstreamHeaders := make(chan http.Header, 1)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamHeaders <- r.Header.Clone()
		_, _ = w.Write([]byte("upstream ok"))
	}))
	t.Cleanup(upstream.Close)

	handler := newTestPublicAccessProxy(t, upstream.URL, publicAccessProviderConfig{
		ID:               30,
		Name:             "sso",
		ProviderType:     publicAccessProviderTypeForwardAuth,
		Enabled:          true,
		ForwardAuthURL:   auth.URL + "/verify",
		ParsedURL:        mustParsePublicAccessTestURL(t, auth.URL+"/verify"),
		Timeout:          time.Second,
		SubjectHeader:    "X-Auth-Request-Preferred-Username",
		UserHeader:       "X-Auth-Request-User",
		EmailHeader:      "X-Auth-Request-Email",
		GroupsHeader:     "X-Auth-Request-Groups",
		ForwardedHeaders: append([]string(nil), defaultPublicAccessForwardedHeaders...),
		client:           auth.Client(),
	}, publicAccessPolicyConfig{
		ID:             40,
		Name:           "operators",
		ProviderID:     30,
		Enabled:        true,
		RequiredGroups: []string{"operators"},
		GroupMatch:     publicAccessGroupMatchAny,
	})

	req := httptest.NewRequest(http.MethodGet, "http://app.example/private/report?format=json", nil)
	req.RemoteAddr = "192.0.2.44:41000"
	req.Header.Set("Authorization", "Bearer browser-token")
	req.Header.Set("Cookie", "auth_session=old")
	req.Header.Set("X-Auth-Request-User", "mallory")
	req.Header.Set("X-Auth-Request-Email", "mallory@example.test")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, req)

	if recorder.Code != http.StatusOK || recorder.Body.String() != "upstream ok" {
		t.Fatalf("response = %d %q, want 200 upstream response", recorder.Code, recorder.Body.String())
	}
	if cookies := recorder.Header().Values("Set-Cookie"); len(cookies) != 1 || !strings.Contains(cookies[0], "auth_session=renewed") {
		t.Fatalf("Set-Cookie = %#v, want renewed auth session", cookies)
	}

	select {
	case headers := <-upstreamHeaders:
		assertForwardedHeader(t, headers, "X-Auth-Request-User", "alice")
		assertForwardedHeader(t, headers, "X-Auth-Request-Email", "alice@example.test")
		assertForwardedHeader(t, headers, "X-Auth-Request-Groups", "engineering, operators")
	case <-time.After(time.Second):
		t.Fatal("upstream request was not received")
	}

	checked := authRequest.Load()
	if checked == nil {
		t.Fatal("forward-auth request was not received")
	}
	if checked.Method != http.MethodGet || checked.URL.Path != "/verify" {
		t.Fatalf("forward-auth request = %s %s, want GET /verify", checked.Method, checked.URL.Path)
	}
	assertForwardedHeader(t, checked.Header, "Authorization", "Bearer browser-token")
	assertForwardedHeader(t, checked.Header, "Cookie", "auth_session=old")
	assertForwardedHeader(t, checked.Header, "X-Forwarded-Method", http.MethodGet)
	assertForwardedHeader(t, checked.Header, "X-Forwarded-Uri", "/private/report?format=json")
	assertForwardedHeader(t, checked.Header, "X-Forwarded-Host", "app.example")
	assertForwardedHeader(t, checked.Header, "X-Forwarded-For", "192.0.2.44")
	assertForwardedHeader(t, checked.Header, "X-Original-Url", "http://app.example/private/report?format=json")
}

func TestPublicAccessPolicyDeniesMissingGroupsBeforeUpstream(t *testing.T) {
	var upstreamHits atomic.Int64
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		upstreamHits.Add(1)
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(upstream.Close)

	provider := testPublicAccessProvider(t, httpClientFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusNoContent,
			Header: http.Header{
				"X-Auth-Request-User":   []string{"alice"},
				"X-Auth-Request-Groups": []string{"engineering"},
			},
			Body: io.NopCloser(strings.NewReader("")),
		}, nil
	}))
	handler := newTestPublicAccessProxy(t, upstream.URL, provider, publicAccessPolicyConfig{
		ID:             40,
		Name:           "administrators",
		ProviderID:     provider.ID,
		Enabled:        true,
		RequiredGroups: []string{"administrators", "security"},
		GroupMatch:     publicAccessGroupMatchAll,
	})

	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "http://app.example/private", nil))
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", recorder.Code)
	}
	if upstreamHits.Load() != 0 {
		t.Fatalf("upstream hits = %d, want 0", upstreamHits.Load())
	}
}

func TestPublicAccessStripsConfiguredIdentityHeadersFromPublicRoutes(t *testing.T) {
	upstreamHeaders := make(chan http.Header, 1)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamHeaders <- r.Header.Clone()
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(upstream.Close)

	var authCalls atomic.Int64
	provider := testPublicAccessProvider(t, httpClientFunc(func(*http.Request) (*http.Response, error) {
		authCalls.Add(1)
		return nil, errors.New("unexpected forward-auth request")
	}))
	handler := newTestPublicAccessProxy(t, upstream.URL, provider, publicAccessPolicyConfig{
		ID: 0, Name: "public", ProviderID: provider.ID, Enabled: true, GroupMatch: publicAccessGroupMatchAny,
	})
	req := httptest.NewRequest(http.MethodGet, "http://app.example/public", nil)
	req.Header.Set("X-Auth-Request-User", "mallory")
	req.Header.Set("X-Auth-Request-Email", "mallory@example.test")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, req)
	if recorder.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", recorder.Code)
	}
	if authCalls.Load() != 0 {
		t.Fatalf("forward-auth calls = %d, want 0", authCalls.Load())
	}
	select {
	case headers := <-upstreamHeaders:
		if got := headers.Get("X-Auth-Request-User"); got != "" {
			t.Fatalf("spoofed identity header reached public upstream: %q", got)
		}
		if got := headers.Get("X-Auth-Request-Email"); got != "" {
			t.Fatalf("spoofed identity header reached public upstream: %q", got)
		}
	case <-time.After(time.Second):
		t.Fatal("upstream request was not received")
	}
}

func TestPublicAccessRelaysAuthenticationChallenge(t *testing.T) {
	provider := testPublicAccessProvider(t, httpClientFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusFound,
			Header: http.Header{
				"Location":   []string{"https://login.example.test/start"},
				"Set-Cookie": []string{"auth_nonce=nonce; Path=/; HttpOnly"},
			},
			Body: io.NopCloser(strings.NewReader("sign in")),
		}, nil
	}))
	handler := newTestPublicAccessProxy(t, "http://127.0.0.1:1", provider, publicAccessPolicyConfig{
		ID: 40, Name: "signed-in", ProviderID: provider.ID, Enabled: true, GroupMatch: publicAccessGroupMatchAny,
	})

	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "http://app.example/private", nil))
	if recorder.Code != http.StatusFound {
		t.Fatalf("status = %d, want 302", recorder.Code)
	}
	if got := recorder.Header().Get("Location"); got != "https://login.example.test/start" {
		t.Fatalf("Location = %q", got)
	}
	if got := recorder.Body.String(); got != "sign in" {
		t.Fatalf("body = %q, want challenge body", got)
	}
}

func TestPublicAccessFailsClosedWhenProviderFails(t *testing.T) {
	provider := testPublicAccessProvider(t, httpClientFunc(func(*http.Request) (*http.Response, error) {
		return nil, errors.New("provider offline")
	}))
	handler := newTestPublicAccessProxy(t, "http://127.0.0.1:1", provider, publicAccessPolicyConfig{
		ID: 40, Name: "signed-in", ProviderID: provider.ID, Enabled: true, GroupMatch: publicAccessGroupMatchAny,
	})

	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "http://app.example/private", nil))
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", recorder.Code)
	}
}

func TestPublicAccessGroupMatching(t *testing.T) {
	policy := publicAccessPolicyConfig{RequiredGroups: []string{"engineering", "operators"}, GroupMatch: publicAccessGroupMatchAny}
	if !publicAccessPolicyAllowsGroups(policy, []string{"operators"}) {
		t.Fatal("ANY policy should allow one matching group")
	}
	policy.GroupMatch = publicAccessGroupMatchAll
	if publicAccessPolicyAllowsGroups(policy, []string{"operators"}) {
		t.Fatal("ALL policy should reject a partial group match")
	}
	if !publicAccessPolicyAllowsGroups(policy, []string{"operators", "engineering", "extra"}) {
		t.Fatal("ALL policy should allow every required group")
	}
}

func TestPublicAccessOriginalURLIncludesNonDefaultListenerPort(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "https://app.example/private?next=1", nil)
	listener := publicListenerConfig{Protocol: publicListenerProtocolHTTPS, Port: 8443}
	if got := publicAccessOriginalURL(req, listener); got != "https://app.example:8443/private?next=1" {
		t.Fatalf("original URL = %q", got)
	}
}

func TestPublicAccessForwardedHeaderValidationRejectsSecurityHeaders(t *testing.T) {
	for _, name := range []string{"Authorization", "Cookie", "Set-Cookie", "X-Forwarded-For", "Connection", "Content-Length"} {
		t.Run(name, func(t *testing.T) {
			if _, err := normalizePublicAccessHeaderList([]string{name}, false); err == nil {
				t.Fatalf("header %q was accepted", name)
			}
		})
	}
}

func TestPublicAccessProtectedRouteBypassesSharedCache(t *testing.T) {
	app, resolution, closeDB := newTestPublicCacheApp(t)
	defer closeDB()
	resolution.Route.AccessPolicyID = 40
	req := httptest.NewRequest(http.MethodGet, "http://assets.example.test/assets/app.txt", nil)

	decision := app.checkPublicCache(req, resolution)
	if decision.Status != publicCacheStatusBypass || decision.BypassReason != "access_control" {
		t.Fatalf("cache decision = %q/%q, want bypass/access_control", decision.Status, decision.BypassReason)
	}
}

func newTestPublicAccessProxy(
	t *testing.T,
	upstreamURL string,
	provider publicAccessProviderConfig,
	policy publicAccessPolicyConfig,
) http.Handler {
	t.Helper()
	origin := mustParsePublicAccessTestURL(t, upstreamURL)
	target := publicRouteTargetConfig{
		ID: 20, RouteID: 10, Name: "upstream", Enabled: true,
		TargetType: publicRouteTargetTypeProxy, Transport: publicRouteTargetTransportDirect, ParsedURL: origin,
	}
	route := publicRouteConfig{
		ID: 10, Enabled: true, PathPrefix: "/", Action: publicRouteActionForward,
		PathSecurityMode: publicRoutePathSecurityModeStrict, AccessPolicyID: policy.ID,
		Targets: []publicRouteTargetConfig{target},
	}
	app := NewApp(nil, nil)
	setPublicSnapshotForTest(t, app, &publicProxySnapshot{
		Listeners: map[int64]publicListenerConfig{1: {
			ID: 1, Port: 80, Protocol: publicListenerProtocolHTTP, Enabled: true,
		}},
		RoutesByListener: map[int64][]publicRouteConfig{1: {route}},
		RouteTargets:     map[int64]publicRouteTargetConfig{target.ID: target},
		AccessProviders:  map[int64]publicAccessProviderConfig{provider.ID: provider},
		AccessPolicies:   map[int64]publicAccessPolicyConfig{policy.ID: policy},
	})
	return app.publicProxyHandler(1)
}

func testPublicAccessProvider(t *testing.T, client HTTPClient) publicAccessProviderConfig {
	t.Helper()
	return publicAccessProviderConfig{
		ID:               30,
		Name:             "sso",
		ProviderType:     publicAccessProviderTypeForwardAuth,
		Enabled:          true,
		ForwardAuthURL:   "https://auth.example.test/verify",
		ParsedURL:        mustParsePublicAccessTestURL(t, "https://auth.example.test/verify"),
		Timeout:          time.Second,
		SubjectHeader:    "X-Auth-Request-Preferred-Username",
		UserHeader:       "X-Auth-Request-User",
		EmailHeader:      "X-Auth-Request-Email",
		GroupsHeader:     "X-Auth-Request-Groups",
		ForwardedHeaders: append([]string(nil), defaultPublicAccessForwardedHeaders...),
		client:           client,
	}
}

func mustParsePublicAccessTestURL(t *testing.T, value string) *url.URL {
	t.Helper()
	parsed, err := url.Parse(value)
	if err != nil {
		t.Fatalf("parse URL %q: %v", value, err)
	}
	return parsed
}

type httpClientFunc func(*http.Request) (*http.Response, error)

func (fn httpClientFunc) Do(req *http.Request) (*http.Response, error) {
	return fn(req)
}
