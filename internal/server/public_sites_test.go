package server

import (
	"context"
	"crypto/tls"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	p2pstreamv1 "p2pstream/gen/proto/p2pstream/v1"
	"p2pstream/internal/db"
)

func TestNormalizePublicSiteHostnamePatternIDNAAndWildcard(t *testing.T) {
	tests := []struct{ input, want string }{
		{"BÜCHER.example.", "xn--bcher-kva.example"},
		{"*.BÜCHER.example", "*.xn--bcher-kva.example"},
		{"192.0.2.4", "192.0.2.4"},
	}
	for _, tc := range tests {
		got, err := normalizePublicSiteHostnamePattern(tc.input)
		if err != nil {
			t.Fatalf("normalize %q: %v", tc.input, err)
		}
		if got != tc.want {
			t.Fatalf("normalize %q = %q, want %q", tc.input, got, tc.want)
		}
	}
	for _, input := range []string{"*.com", "foo", "*.*.example.com", "https://example.com", "[::1]"} {
		if _, err := normalizePublicSiteHostnamePattern(input); err == nil {
			t.Fatalf("normalize %q unexpectedly succeeded", input)
		}
	}
}

func TestStrictPublicSiteWildcardMatchesOneLabel(t *testing.T) {
	if !strictPublicSiteHostMatches("a.example.com", "*.example.com") {
		t.Fatal("one-label wildcard did not match")
	}
	if strictPublicSiteHostMatches("a.b.example.com", "*.example.com") {
		t.Fatal("wildcard matched multiple labels")
	}
	if strictPublicSiteHostMatches("example.com", "*.example.com") {
		t.Fatal("wildcard matched apex")
	}
}

func TestParsePublicSiteRequestAuthorityIsStrictAndLegacyCompatible(t *testing.T) {
	for input, want := range map[string]string{"LOCALHOST": "localhost", "example.com.": "example.com", "[2001:0db8::1]:443": "2001:db8::1"} {
		got, err := parsePublicSiteRequestAuthority(input)
		if err != nil || got != want {
			t.Fatalf("parse %q = %q, %v; want %q", input, got, err, want)
		}
	}
	for _, input := range []string{"*.example.com", "example.com..", "[192.0.2.1]", "fe80::1%eth0", " example.com", "example.com:0"} {
		if _, err := parsePublicSiteRequestAuthority(input); err == nil {
			t.Fatalf("parse %q unexpectedly succeeded", input)
		}
	}
}

func TestPublicSiteRoutingIsolatedAndNeverFallsThrough(t *testing.T) {
	legacyTarget := publicRouteTargetConfig{ID: 20, RouteID: 10, Enabled: true, TargetType: publicRouteTargetTypeStatic}
	siteTarget := publicRouteTargetConfig{ID: 21, RouteID: 11, Enabled: true, TargetType: publicRouteTargetTypeStatic}
	snap := &publicProxySnapshot{
		Listeners:           map[int64]publicListenerConfig{1: {ID: 1, Protocol: publicListenerProtocolHTTP}},
		Sites:               map[int64]publicSiteConfig{2: {ID: 2, ListenerID: 1, Enabled: true, PrimaryHostname: "app.example.com"}},
		SiteHostsByListener: map[int64][]publicSiteHostConfig{1: {{SiteID: 2, ListenerID: 1, HostnamePattern: "app.example.com", Primary: true, Behavior: publicSiteHostBehaviorServe}}},
		RoutesByListener: map[int64][]publicRouteConfig{1: {
			{ID: 10, ListenerID: 1, Priority: 1, PathPrefix: "/", Enabled: true, Targets: []publicRouteTargetConfig{legacyTarget}},
			{ID: 11, ListenerID: 1, SiteID: 2, Priority: 10, PathPrefix: "/managed", Enabled: true, Targets: []publicRouteTargetConfig{siteTarget}},
		}},
		RouteTargets: map[int64]publicRouteTargetConfig{20: legacyTarget, 21: siteTarget},
	}
	app := NewApp(nil, nil)
	req := httptest.NewRequest(http.MethodGet, "http://app.example.com/managed", nil)
	match, err := app.matchPublicRouteInSnapshot(snap, 1, req)
	if err != nil || match.Route.ID != 11 {
		t.Fatalf("managed route = %d, err=%v", match.Route.ID, err)
	}

	req = httptest.NewRequest(http.MethodGet, "http://app.example.com/missing", nil)
	if _, err := app.matchPublicRouteInSnapshot(snap, 1, req); !errors.Is(err, errNoPublicRouteAvailable) {
		t.Fatalf("site path miss error = %v", err)
	}

	disabled := snap.Sites[2]
	disabled.Enabled = false
	snap.Sites[2] = disabled
	if _, err := app.matchPublicRouteInSnapshot(snap, 1, req); !errors.Is(err, errNoPublicRouteAvailable) {
		t.Fatalf("disabled site error = %v", err)
	}

	req = httptest.NewRequest(http.MethodGet, "http://legacy.example.net/anything", nil)
	match, err = app.matchPublicRouteInSnapshot(snap, 1, req)
	if err != nil || match.Route.ID != 10 {
		t.Fatalf("legacy route = %d, err=%v", match.Route.ID, err)
	}
}

func TestPublicSiteRejectsMalformedAuthorityAndSNIMismatch(t *testing.T) {
	target := publicRouteTargetConfig{ID: 21, RouteID: 11, Enabled: true, TargetType: publicRouteTargetTypeStatic}
	snap := &publicProxySnapshot{
		Listeners:           map[int64]publicListenerConfig{1: {ID: 1, Protocol: publicListenerProtocolHTTPS}},
		Sites:               map[int64]publicSiteConfig{2: {ID: 2, ListenerID: 1, Enabled: true, PrimaryHostname: "app.example.com"}},
		SiteHostsByListener: map[int64][]publicSiteHostConfig{1: {{SiteID: 2, HostnamePattern: "app.example.com", Primary: true, Behavior: publicSiteHostBehaviorServe}}},
		RoutesByListener:    map[int64][]publicRouteConfig{1: {{ID: 11, ListenerID: 1, SiteID: 2, IsDefault: true, Enabled: true, Targets: []publicRouteTargetConfig{target}}}},
	}
	app := NewApp(nil, nil)
	malformed := httptest.NewRequest(http.MethodGet, "https://app.example.com/", nil)
	malformed.Host = "app.example.com:not-a-port"
	if _, err := app.matchPublicRouteInSnapshot(snap, 1, malformed); !errors.Is(err, errMalformedPublicAuthority) {
		t.Fatalf("malformed authority error = %v", err)
	}

	mismatch := httptest.NewRequest(http.MethodGet, "https://app.example.com/", nil)
	mismatch.TLS = &tls.ConnectionState{ServerName: "other.example.com"}
	assertMisdirectedAuthorityStage(t, app, snap, mismatch)
	missing := httptest.NewRequest(http.MethodGet, "https://app.example.com/", nil)
	missing.TLS = &tls.ConnectionState{}
	assertMisdirectedAuthorityStage(t, app, snap, missing)
	fronted := httptest.NewRequest(http.MethodGet, "https://unmanaged.example.net/", nil)
	fronted.TLS = &tls.ConnectionState{ServerName: "app.example.com"}
	assertMisdirectedAuthorityStage(t, app, snap, fronted)
}

func assertMisdirectedAuthorityStage(t *testing.T, app *App, snap *publicProxySnapshot, req *http.Request) {
	t.Helper()
	recorder := httptest.NewRecorder()
	ctx := newPublicProxyContext(app, 1, recorder, req)
	ctx.Snapshot = snap
	if result := validatePublicSiteAuthorityStage(ctx); result != publicProxyStageDone || recorder.Code != http.StatusMisdirectedRequest {
		t.Fatalf("authority stage = %v/%d, want done/421", result, recorder.Code)
	}
}

func TestManagedSNIMismatchPrecedesWAFReservedEndpoint(t *testing.T) {
	target := publicRouteTargetConfig{ID: 21, RouteID: 11, Enabled: true, TargetType: publicRouteTargetTypeStatic}
	snap := &publicProxySnapshot{Listeners: map[int64]publicListenerConfig{1: {ID: 1, Protocol: publicListenerProtocolHTTPS}}, Sites: map[int64]publicSiteConfig{2: {ID: 2, ListenerID: 1, Enabled: true, PrimaryHostname: "app.example.com"}}, SiteHostsByListener: map[int64][]publicSiteHostConfig{1: {{SiteID: 2, HostnamePattern: "app.example.com", Primary: true, Behavior: publicSiteHostBehaviorServe}}}, RoutesByListener: map[int64][]publicRouteConfig{1: {{ID: 11, SiteID: 2, Enabled: true, IsDefault: true, Targets: []publicRouteTargetConfig{target}}}}}
	app := NewApp(nil, nil)
	setPublicSnapshotForTest(t, app, snap)
	req := httptest.NewRequest(http.MethodGet, "https://app.example.com"+publicWafCaptchaVerifyPath, nil)
	req.TLS = &tls.ConnectionState{ServerName: "front.example.net"}
	recorder := httptest.NewRecorder()
	app.publicProxyHandler(1).ServeHTTP(recorder, req)
	if recorder.Code != http.StatusMisdirectedRequest {
		t.Fatalf("reserved endpoint status = %d, want 421", recorder.Code)
	}
}

func TestPublicSiteAliasRedirectUsesStoredPrimaryAndPreservesRequest(t *testing.T) {
	listener := publicListenerConfig{ID: 1, Protocol: publicListenerProtocolHTTPS, Port: 8443}
	site := publicSiteConfig{ID: 2, ListenerID: 1, Enabled: true, PrimaryHostname: "primary.example.com"}
	snap := &publicProxySnapshot{
		Listeners: map[int64]publicListenerConfig{1: listener}, Sites: map[int64]publicSiteConfig{2: site},
		SiteHostsByListener: map[int64][]publicSiteHostConfig{1: {{SiteID: 2, HostnamePattern: "alias.example.net", Behavior: publicSiteHostBehaviorRedirect}}},
	}
	req := httptest.NewRequest(http.MethodPost, "https://alias.example.net/a/b?x=1", strings.NewReader("body"))
	req.TLS = &tls.ConnectionState{ServerName: "alias.example.net"}
	match, err := NewApp(nil, nil).matchPublicRouteInSnapshot(snap, 1, req)
	if err != nil {
		t.Fatalf("match alias: %v", err)
	}
	if match.Route.RedirectStatusCode != http.StatusPermanentRedirect {
		t.Fatalf("redirect status = %d", match.Route.RedirectStatusCode)
	}
	if !publicRouteAllowsEncodedPathSeparators(match.Route) {
		t.Fatal("alias redirect did not allow encoded separators")
	}
	location, err := redirectLocationForRequest(req, match.Route)
	if err != nil {
		t.Fatalf("redirect location: %v", err)
	}
	if location != "https://primary.example.com:8443/a/b?x=1" {
		t.Fatalf("location = %q", location)
	}
	forceQuery := httptest.NewRequest(http.MethodGet, "https://alias.example.net/path?", nil)
	forceQuery.URL.ForceQuery = true
	forceLocation, err := redirectLocationForRequest(forceQuery, match.Route)
	if err != nil || forceLocation != "https://primary.example.com:8443/path?" {
		t.Fatalf("force-query location = %q, err=%v", forceLocation, err)
	}
}

func TestLegacyRoutingDoesNotAdoptStrictAuthorityParsing(t *testing.T) {
	target := publicRouteTargetConfig{ID: 2, RouteID: 1, Enabled: true, TargetType: publicRouteTargetTypeStatic}
	snap := &publicProxySnapshot{Listeners: map[int64]publicListenerConfig{1: {ID: 1}}, RoutesByListener: map[int64][]publicRouteConfig{1: {{ID: 1, Enabled: true, IsDefault: true, Targets: []publicRouteTargetConfig{target}}}}}
	req := httptest.NewRequest(http.MethodGet, "http://example.com/", nil)
	req.Host = "legacy.example.com:not-a-port"
	match, err := NewApp(nil, nil).matchPublicRouteInSnapshot(snap, 1, req)
	if err != nil || match.Route.ID != 1 {
		t.Fatalf("legacy match = %d, err=%v", match.Route.ID, err)
	}
}

func TestPublicSiteCRUDIsAtomicAndOldRouteUpdatePreservesBinding(t *testing.T) {
	database, err := db.Open(filepath.Join(t.TempDir(), "sites-api.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer database.Close()
	app := NewApp(nil, database)
	header := createTestAdminSession(t, app)
	listener, err := database.CreatePublicListener(context.Background(), db.CreatePublicListenerParams{Name: "http", Port: 8080, Protocol: publicListenerProtocolHTTP, Enabled: 1})
	if err != nil {
		t.Fatalf("create listener: %v", err)
	}
	negativeID := int64(-1)
	negativeRoute := connect.NewRequest(&p2pstreamv1.CreatePublicRouteRequest{ListenerId: listener.ID, SiteId: &negativeID, Priority: 1, PathPrefix: "/", Enabled: true, Action: p2pstreamv1.PublicRouteAction_PUBLIC_ROUTE_ACTION_REDIRECT, RedirectTargetMode: p2pstreamv1.PublicRouteRedirectTargetMode_PUBLIC_ROUTE_REDIRECT_TARGET_MODE_SAME_HOST_PATH, RedirectTarget: "/"})
	negativeRoute.Header().Set("Cookie", header.Get("Cookie"))
	if _, err := app.CreatePublicRoute(context.Background(), negativeRoute); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("negative site code = %v, err=%v", connect.CodeOf(err), err)
	}
	create := connect.NewRequest(&p2pstreamv1.CreatePublicSiteRequest{ListenerId: listener.ID, Name: "app", Enabled: true, Hosts: []*p2pstreamv1.PublicSiteHostInput{
		{HostnamePattern: "app.example.com", Primary: true, Behavior: p2pstreamv1.PublicSiteHostBehavior_PUBLIC_SITE_HOST_BEHAVIOR_SERVE},
		{HostnamePattern: "www.example.com", Behavior: p2pstreamv1.PublicSiteHostBehavior_PUBLIC_SITE_HOST_BEHAVIOR_REDIRECT},
	}})
	create.Header().Set("Cookie", header.Get("Cookie"))
	siteResponse, err := app.CreatePublicSite(context.Background(), create)
	if err != nil {
		t.Fatalf("create site: %v", err)
	}
	siteID := siteResponse.Msg.Site.Id
	routeReq := connect.NewRequest(&p2pstreamv1.CreatePublicRouteRequest{ListenerId: listener.ID, SiteId: &siteID, Priority: 10, IsDefault: true, Enabled: true, Action: p2pstreamv1.PublicRouteAction_PUBLIC_ROUTE_ACTION_REDIRECT, RedirectTargetMode: p2pstreamv1.PublicRouteRedirectTargetMode_PUBLIC_ROUTE_REDIRECT_TARGET_MODE_SAME_HOST_PATH, RedirectTarget: "/ok", RedirectStatusCode: 302, RedirectPreserveQuery: true})
	routeReq.Header().Set("Cookie", header.Get("Cookie"))
	routeResponse, err := app.CreatePublicRoute(context.Background(), routeReq)
	if err != nil {
		t.Fatalf("create site route: %v", err)
	}
	if routeResponse.Msg.Route.SiteId != siteID {
		t.Fatalf("route site = %d", routeResponse.Msg.Route.SiteId)
	}

	// Simulate an older API client: omitted optional site_id must not detach.
	updateRoute := connect.NewRequest(&p2pstreamv1.UpdatePublicRouteRequest{Id: routeResponse.Msg.Route.Id, ListenerId: listener.ID, Priority: 11, IsDefault: true, Enabled: true, Action: p2pstreamv1.PublicRouteAction_PUBLIC_ROUTE_ACTION_REDIRECT, RedirectTargetMode: p2pstreamv1.PublicRouteRedirectTargetMode_PUBLIC_ROUTE_REDIRECT_TARGET_MODE_SAME_HOST_PATH, RedirectTarget: "/ok", RedirectStatusCode: 302, RedirectPreserveQuery: true})
	updateRoute.Header().Set("Cookie", header.Get("Cookie"))
	updated, err := app.UpdatePublicRoute(context.Background(), updateRoute)
	if err != nil {
		t.Fatalf("update route: %v", err)
	}
	if updated.Msg.Route.SiteId != siteID {
		t.Fatalf("old-client update detached site: %d", updated.Msg.Route.SiteId)
	}
	rename := connect.NewRequest(&p2pstreamv1.UpdatePublicSiteRequest{Id: siteID, ListenerId: listener.ID, Name: "app", Enabled: true, Hosts: []*p2pstreamv1.PublicSiteHostInput{{HostnamePattern: "new.example.com", Primary: true}, {HostnamePattern: "www.example.com", Behavior: p2pstreamv1.PublicSiteHostBehavior_PUBLIC_SITE_HOST_BEHAVIOR_REDIRECT}}})
	rename.Header().Set("Cookie", header.Get("Cookie"))
	if _, err := app.UpdatePublicSite(context.Background(), rename); err != nil {
		t.Fatalf("rename primary: %v", err)
	}
	storedRoute, err := database.GetPublicRoute(context.Background(), routeResponse.Msg.Route.Id)
	if err != nil || storedRoute.HostPattern != "new.example.com" {
		t.Fatalf("route primary after rename = %q, err=%v", storedRoute.HostPattern, err)
	}

	deleteSite := connect.NewRequest(&p2pstreamv1.DeletePublicSiteRequest{Id: siteID})
	deleteSite.Header().Set("Cookie", header.Get("Cookie"))
	if _, err := app.DeletePublicSite(context.Background(), deleteSite); connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Fatalf("delete site code = %v, err=%v", connect.CodeOf(err), err)
	}

	// A conflicting replace rolls the entire host set back.
	otherCreate := connect.NewRequest(&p2pstreamv1.CreatePublicSiteRequest{ListenerId: listener.ID, Name: "other", Enabled: true, Hosts: []*p2pstreamv1.PublicSiteHostInput{{HostnamePattern: "other.example.com", Primary: true}}})
	otherCreate.Header().Set("Cookie", header.Get("Cookie"))
	other, err := app.CreatePublicSite(context.Background(), otherCreate)
	if err != nil {
		t.Fatalf("create other site: %v", err)
	}
	conflict := connect.NewRequest(&p2pstreamv1.UpdatePublicSiteRequest{Id: other.Msg.Site.Id, ListenerId: listener.ID, Name: "other", Enabled: true, Hosts: []*p2pstreamv1.PublicSiteHostInput{{HostnamePattern: "other.example.com", Primary: true}, {HostnamePattern: "www.example.com"}}})
	conflict.Header().Set("Cookie", header.Get("Cookie"))
	if _, err := app.UpdatePublicSite(context.Background(), conflict); connect.CodeOf(err) != connect.CodeAlreadyExists {
		t.Fatalf("conflict code = %v, err=%v", connect.CodeOf(err), err)
	}
	hosts, err := database.ListPublicSiteHostsBySite(context.Background(), other.Msg.Site.Id)
	if err != nil || len(hosts) != 1 || hosts[0].HostnamePattern != "other.example.com" {
		t.Fatalf("rolled-back hosts = %+v, err=%v", hosts, err)
	}
}

func TestPublicSiteAuthorityStageCanonicalizesEveryDownstreamView(t *testing.T) {
	snap := &publicProxySnapshot{SiteHostsByListener: map[int64][]publicSiteHostConfig{1: {{SiteID: 2, HostnamePattern: "xn--bcher-kva.example"}}}}
	app := NewApp(nil, nil)
	req := httptest.NewRequest(http.MethodGet, "http://example.test/", nil)
	req.Host = "BÜCHER.example:8080"
	recorder := httptest.NewRecorder()
	ctx := newPublicProxyContext(app, 1, recorder, req)
	ctx.Snapshot = snap
	if got := validatePublicSiteAuthorityStage(ctx); got != publicProxyStageContinue {
		t.Fatalf("stage result = %v", got)
	}
	if ctx.Request.Host != "xn--bcher-kva.example" || ctx.RequestContext.Host != "xn--bcher-kva.example" {
		t.Fatalf("canonical request/context = %q/%q", ctx.Request.Host, ctx.RequestContext.Host)
	}
	parsed := ctx.Request.Context().Value(publicSiteAuthorityContextKey{}).(publicSiteAuthorityContext)
	if parsed.RawAuthority != "BÜCHER.example:8080" {
		t.Fatalf("raw authority = %q", parsed.RawAuthority)
	}
}

func TestPublicSiteAuthorityStageCanonicalizesManagedHostBeforeSNIRejection(t *testing.T) {
	snap := &publicProxySnapshot{
		Listeners:           map[int64]publicListenerConfig{1: {ID: 1, Protocol: publicListenerProtocolHTTPS}},
		SiteHostsByListener: map[int64][]publicSiteHostConfig{1: {{SiteID: 2, HostnamePattern: "xn--bcher-kva.example"}}},
	}
	app := NewApp(nil, nil)
	req := httptest.NewRequest(http.MethodGet, "https://example.test/", nil)
	req.Host = "BÜCHER.example"
	req.TLS = &tls.ConnectionState{ServerName: "front.example.net"}
	ctx := newPublicProxyContext(app, 1, httptest.NewRecorder(), req)
	ctx.Snapshot = snap
	if got := validatePublicSiteAuthorityStage(ctx); got != publicProxyStageDone {
		t.Fatalf("stage result = %v, want done", got)
	}
	if ctx.Request.Host != "xn--bcher-kva.example" || ctx.RequestContext.Host != "xn--bcher-kva.example" {
		t.Fatalf("rejected request/context = %q/%q", ctx.Request.Host, ctx.RequestContext.Host)
	}
}

func TestVerifyPublicTLSCertificateHostnameRejectsWrongSAN(t *testing.T) {
	certPEM, _, _, err := generatePublicSelfSignedCertificatePEM("covered.example.com", 24*time.Hour)
	if err != nil {
		t.Fatalf("generate cert: %v", err)
	}
	if err := verifyPublicTLSCertificateHostname(certPEM, "covered.example.com"); err != nil {
		t.Fatalf("verify covered SAN: %v", err)
	}
	if err := verifyPublicTLSCertificateHostname(certPEM, "other.example.com"); err == nil {
		t.Fatal("wrong SAN unexpectedly accepted")
	}
}

func TestPublicSiteTLSCoverageMirrorsEffectiveSelector(t *testing.T) {
	dir := t.TempDir()
	certPEM, keyPEM, _, err := generatePublicSelfSignedCertificatePEM("*.example.com", 24*time.Hour)
	if err != nil {
		t.Fatalf("generate wildcard: %v", err)
	}
	certPath, keyPath := filepath.Join(dir, "wild.crt"), filepath.Join(dir, "wild.key")
	if err := os.WriteFile(certPath, certPEM, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyPath, keyPEM, 0o600); err != nil {
		t.Fatal(err)
	}
	wildcard := db.PublicTlsCertificate{ID: 2, ListenerID: 1, HostnamePattern: "*.example.com", CertPath: certPath, KeyPath: keyPath, Enabled: 1, Source: publicTLSCertificateSourceManual, Status: publicTLSCertificateStatusError}
	coverage, _ := publicSiteTLSCoverage(publicListenerProtocolHTTPS, 1, "app.example.com", []db.PublicTlsCertificate{wildcard})
	if coverage != p2pstreamv1.PublicSiteTlsCoverage_PUBLIC_SITE_TLS_COVERAGE_COVERED {
		t.Fatalf("valid retained material coverage = %v", coverage)
	}
	invalidExact := db.PublicTlsCertificate{ID: 1, ListenerID: 1, HostnamePattern: "app.example.com", CertPath: filepath.Join(dir, "missing.crt"), KeyPath: filepath.Join(dir, "missing.key"), Enabled: 1, Source: publicTLSCertificateSourceManual, Status: publicTLSCertificateStatusReady}
	coverage, _ = publicSiteTLSCoverage(publicListenerProtocolHTTPS, 1, "app.example.com", []db.PublicTlsCertificate{invalidExact, wildcard})
	if coverage != p2pstreamv1.PublicSiteTlsCoverage_PUBLIC_SITE_TLS_COVERAGE_INVALID {
		t.Fatalf("invalid exact shadow coverage = %v", coverage)
	}
	pendingACME := invalidExact
	pendingACME.Source, pendingACME.CertPath, pendingACME.KeyPath = publicTLSCertificateSourceACME, "", ""
	coverage, _ = publicSiteTLSCoverage(publicListenerProtocolHTTPS, 1, "app.example.com", []db.PublicTlsCertificate{pendingACME, wildcard})
	if coverage != p2pstreamv1.PublicSiteTlsCoverage_PUBLIC_SITE_TLS_COVERAGE_COVERED {
		t.Fatalf("missing ACME fallback coverage = %v", coverage)
	}

	// Exact mappings are loaded in ID order and overwrite the selector map,
	// so the higher-ID duplicate is authoritative even when it is invalid.
	validExact := wildcard
	validExact.ID, validExact.HostnamePattern = 3, "app.example.com"
	invalidDuplicate := invalidExact
	invalidDuplicate.ID = 4
	coverage, _ = publicSiteTLSCoverage(publicListenerProtocolHTTPS, 1, "app.example.com", []db.PublicTlsCertificate{validExact, invalidDuplicate, wildcard})
	if coverage != p2pstreamv1.PublicSiteTlsCoverage_PUBLIC_SITE_TLS_COVERAGE_INVALID {
		t.Fatalf("duplicate exact winner coverage = %v", coverage)
	}
}

func TestPublicSiteTLSCoverageDoesNotClaimIPLiteralSNI(t *testing.T) {
	for _, hostname := range []string{"192.0.2.10", "2001:db8::10"} {
		coverage, detail := publicSiteTLSCoverage(publicListenerProtocolHTTPS, 1, hostname, []db.PublicTlsCertificate{{
			ID: 1, ListenerID: 1, HostnamePattern: hostname, Enabled: 1,
		}})
		if coverage != p2pstreamv1.PublicSiteTlsCoverage_PUBLIC_SITE_TLS_COVERAGE_MISSING || !strings.Contains(detail, "DNS hostname") {
			t.Fatalf("IP %q coverage = %v, %q", hostname, coverage, detail)
		}
	}
}

func TestPublicTLSSelectorWildcardMatchesExactlyOneLabel(t *testing.T) {
	wildcard, fallback := &tls.Certificate{}, &tls.Certificate{}
	selector := &publicTLSSelector{wildcard: []publicWildcardCertificate{{pattern: "*.example.com", suffix: ".example.com", cert: wildcard}}, fallback: fallback}
	for host, want := range map[string]*tls.Certificate{"a.example.com": wildcard, "example.com": fallback, "a.b.example.com": fallback} {
		got, err := selector.GetCertificate(&tls.ClientHelloInfo{ServerName: host})
		if err != nil || got != want {
			t.Fatalf("selector %q = %p, %v; want %p", host, got, err, want)
		}
	}
}

func TestManualCertificateRetainedMaterialRenameChecksSAN(t *testing.T) {
	database, err := db.Open(filepath.Join(t.TempDir(), "cert-rename.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	listener, err := database.CreatePublicListener(context.Background(), db.CreatePublicListenerParams{Name: "https", Port: 443, Protocol: publicListenerProtocolHTTPS, Enabled: 1})
	if err != nil {
		t.Fatal(err)
	}
	certPEM, keyPEM, _, err := generatePublicSelfSignedCertificatePEM("old.example.com", 24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	certPath, keyPath := filepath.Join(t.TempDir(), "old.crt"), filepath.Join(t.TempDir(), "old.key")
	if err := os.WriteFile(certPath, certPEM, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyPath, keyPEM, 0o600); err != nil {
		t.Fatal(err)
	}
	existing := &db.PublicTlsCertificate{ListenerID: listener.ID, HostnamePattern: "old.example.com", CertPath: certPath, KeyPath: keyPath, Source: publicTLSCertificateSourceManual, Status: publicTLSCertificateStatusReady}
	_, _, err = NewApp(nil, database).validatePublicTLSCertificateInput(context.Background(), listener.ID, "new.example.com", "", "", nil, nil, true, p2pstreamv1.PublicTlsCertificateSource_PUBLIC_TLS_CERTIFICATE_SOURCE_UNSPECIFIED, 0, 0, "", 0, false, 0, existing, true)
	if connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("retained rename code = %v, err=%v", connect.CodeOf(err), err)
	}
}

func TestManualCertificateWithUnavailableMaterialCanBeDisabled(t *testing.T) {
	database, err := db.Open(filepath.Join(t.TempDir(), "cert-disable.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	listener, err := database.CreatePublicListener(context.Background(), db.CreatePublicListenerParams{Name: "https", Port: 443, Protocol: publicListenerProtocolHTTPS, Enabled: 1})
	if err != nil {
		t.Fatal(err)
	}
	existing := &db.PublicTlsCertificate{
		ListenerID: listener.ID, HostnamePattern: "broken.example.com", CertPath: filepath.Join(t.TempDir(), "missing.crt"), KeyPath: filepath.Join(t.TempDir(), "missing.key"), Enabled: 1, Source: publicTLSCertificateSourceManual, Status: publicTLSCertificateStatusError, LastError: "material unavailable",
	}
	params, _, err := NewApp(nil, database).validatePublicTLSCertificateInput(context.Background(), listener.ID, existing.HostnamePattern, existing.CertPath, existing.KeyPath, nil, nil, false, p2pstreamv1.PublicTlsCertificateSource_PUBLIC_TLS_CERTIFICATE_SOURCE_UNSPECIFIED, 0, 0, "", 0, false, 0, existing, true)
	if err != nil {
		t.Fatalf("disable broken mapping: %v", err)
	}
	if params.Enabled != 0 || params.Status != publicTLSCertificateStatusError || params.LastError != existing.LastError {
		t.Fatalf("disable params = %+v", params)
	}
	existing.Enabled = 0
	if _, _, err := NewApp(nil, database).validatePublicTLSCertificateInput(context.Background(), listener.ID, existing.HostnamePattern, "", "", nil, nil, false, p2pstreamv1.PublicTlsCertificateSource_PUBLIC_TLS_CERTIFICATE_SOURCE_UNSPECIFIED, 0, 0, "", 0, false, 0, existing, true); err != nil {
		t.Fatalf("idempotent disabled save: %v", err)
	}
	_, _, err = NewApp(nil, database).validatePublicTLSCertificateInput(context.Background(), listener.ID, existing.HostnamePattern, "", "", nil, nil, true, p2pstreamv1.PublicTlsCertificateSource_PUBLIC_TLS_CERTIFICATE_SOURCE_UNSPECIFIED, 0, 0, "", 0, false, 0, existing, true)
	if connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Fatalf("re-enable broken mapping code = %v, err=%v", connect.CodeOf(err), err)
	}
}

func TestUnmanagedUnicodeHostKeepsLegacySemanticsWithUnrelatedSite(t *testing.T) {
	target := publicRouteTargetConfig{ID: 2, RouteID: 1, Enabled: true, TargetType: publicRouteTargetTypeStatic}
	snap := &publicProxySnapshot{Listeners: map[int64]publicListenerConfig{1: {ID: 1, Protocol: publicListenerProtocolHTTP}}, Sites: map[int64]publicSiteConfig{2: {ID: 2, ListenerID: 1, Enabled: true, PrimaryHostname: "other.example.com"}}, SiteHostsByListener: map[int64][]publicSiteHostConfig{1: {{SiteID: 2, HostnamePattern: "other.example.com", Primary: true, Behavior: publicSiteHostBehaviorServe}}}, RoutesByListener: map[int64][]publicRouteConfig{1: {{ID: 1, ListenerID: 1, HostPattern: "bücher.example", Enabled: true, Targets: []publicRouteTargetConfig{target}}}}}
	req := httptest.NewRequest(http.MethodGet, "http://example.test/", nil)
	req.Host = "BÜCHER.example"
	ctx := newPublicProxyContext(NewApp(nil, nil), 1, httptest.NewRecorder(), req)
	ctx.Snapshot = snap
	if validatePublicSiteAuthorityStage(ctx) != publicProxyStageContinue {
		t.Fatal("unmanaged host rejected")
	}
	if ctx.Request.Host != "BÜCHER.example" {
		t.Fatalf("legacy host rewritten to %q", ctx.Request.Host)
	}
	legacyHostPolicy := mustPublicPolicyMatchCEL(t, `host == "bücher.example"`)
	if !legacyHostPolicy.matches(snap.Listeners[1], ctx.Request) {
		t.Fatal("unrelated site changed legacy CEL host matching")
	}
	match, err := ctx.App.matchPublicRouteInSnapshot(snap, 1, ctx.Request)
	if err != nil || match.Route.ID != 1 {
		t.Fatalf("legacy unicode route = %d, err=%v", match.Route.ID, err)
	}
}

func TestPublicAccessOriginalURLBracketsCanonicalIPv6SiteHost(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "https://[2001:db8::1]/login?next=1", nil)
	req.Host = "2001:db8::1"
	got := publicAccessOriginalURL(req, publicListenerConfig{Protocol: publicListenerProtocolHTTPS, Port: 443})
	if got != "https://[2001:db8::1]/login?next=1" {
		t.Fatalf("original URL = %q", got)
	}
}
