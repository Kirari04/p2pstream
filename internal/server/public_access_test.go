package server

import (
	"context"
	"database/sql"
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
	"p2pstream/internal/authutil"
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

func TestPublicAccessLocalUserManagementHashesSecretsAndRevokesSessions(t *testing.T) {
	app := NewApp(nil, newServerTestDB(t))
	header := createTestAdminSession(t, app)
	ctx := context.Background()

	providerReq := connect.NewRequest(&p2pstreamv1.CreatePublicAccessProviderRequest{
		Name: "family", ProviderType: p2pstreamv1.PublicAccessProviderType_PUBLIC_ACCESS_PROVIDER_TYPE_LOCAL,
		Enabled: true, LocalAuthMode: p2pstreamv1.PublicAccessLocalAuthMode_PUBLIC_ACCESS_LOCAL_AUTH_MODE_FORM_AND_BASIC,
		LocalAuthSessionDurationMillis: defaultPublicAccessSessionMillis, LocalAuthRealm: "Family services",
	})
	providerReq.Header().Set("Cookie", header.Get("Cookie"))
	providerResp, err := app.CreatePublicAccessProvider(ctx, providerReq)
	if err != nil {
		t.Fatalf("create local provider: %v", err)
	}
	provider := providerResp.Msg.Provider
	if provider == nil || provider.ProviderType != p2pstreamv1.PublicAccessProviderType_PUBLIC_ACCESS_PROVIDER_TYPE_LOCAL || provider.ForwardAuthUrl != "" {
		t.Fatalf("local provider readback = %+v", provider)
	}
	if provider.LocalAuthLoginTemplateId <= 0 {
		t.Fatalf("local provider login template = %d, want seeded default", provider.LocalAuthLoginTemplateId)
	}

	userReq := connect.NewRequest(&p2pstreamv1.CreatePublicAccessUserRequest{
		ProviderId: provider.Id, Username: "Alice", Password: "correct horse battery staple",
		Enabled: true, Groups: []string{"operators", "engineering"},
	})
	userReq.Header().Set("Cookie", header.Get("Cookie"))
	userResp, err := app.CreatePublicAccessUser(ctx, userReq)
	if err != nil {
		t.Fatalf("create local user: %v", err)
	}
	user := userResp.Msg.User
	if user == nil || user.Username != "alice" || !user.PasswordSet {
		t.Fatalf("local user readback = %+v", user)
	}
	stored, err := app.DB.GetPublicAccessUser(ctx, user.Id)
	if err != nil {
		t.Fatalf("get stored local user: %v", err)
	}
	if stored.PasswordHash == "correct horse battery staple" || authutil.ComparePasswordHash(stored.PasswordHash, "correct horse battery staple") != nil {
		t.Fatal("local user password was not stored as a usable hash")
	}
	originalHash := stored.PasswordHash
	_, tokenHash, err := newSessionToken()
	if err != nil {
		t.Fatalf("create session token: %v", err)
	}
	if _, err := app.DB.CreatePublicAccessSession(ctx, db.CreatePublicAccessSessionParams{
		ProviderID: provider.Id, UserID: user.Id, TokenHash: tokenHash, ExpiresAt: time.Now().Add(time.Hour),
	}); err != nil {
		t.Fatalf("create local access session: %v", err)
	}

	updateReq := connect.NewRequest(&p2pstreamv1.UpdatePublicAccessUserRequest{
		Id: user.Id, Username: "alice", Password: "", Enabled: true, Groups: []string{"engineering"},
	})
	updateReq.Header().Set("Cookie", header.Get("Cookie"))
	if _, err := app.UpdatePublicAccessUser(ctx, updateReq); err != nil {
		t.Fatalf("update local user: %v", err)
	}
	updated, err := app.DB.GetPublicAccessUser(ctx, user.Id)
	if err != nil {
		t.Fatalf("get updated local user: %v", err)
	}
	if updated.PasswordHash != originalHash {
		t.Fatal("blank password update replaced the write-only password")
	}
	if _, err := app.DB.GetActivePublicAccessSession(ctx, db.GetActivePublicAccessSessionParams{ProviderID: provider.Id, TokenHash: tokenHash}); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("updated user session lookup error = %v, want sql.ErrNoRows", err)
	}
}

func TestPublicAccessLocalProviderSelectsSignInTemplate(t *testing.T) {
	app := NewApp(nil, newServerTestDB(t))
	header := createTestAdminSession(t, app)
	ctx := context.Background()
	const body = `<p>CUSTOM SIGN IN</p><form action="{{ .login_action }}"><input name="{{ .csrf_field_name }}" value="{{ .csrf_token }}"><input name="{{ .username_field_name }}"><input name="{{ .password_field_name }}"></form>`
	templateReq := connect.NewRequest(&p2pstreamv1.CreatePublicResponseTemplateRequest{
		Name: "family-sign-in", Kind: p2pstreamv1.PublicResponseTemplateKind_PUBLIC_RESPONSE_TEMPLATE_KIND_LOCAL_ACCESS_LOGIN_PAGE,
		ContentType: "text/html; charset=utf-8", Body: body,
	})
	templateReq.Header().Set("Cookie", header.Get("Cookie"))
	templateResp, err := app.CreatePublicResponseTemplate(ctx, templateReq)
	if err != nil {
		t.Fatalf("create local sign-in template: %v", err)
	}
	templateID := templateResp.Msg.Template.Id

	providerReq := connect.NewRequest(&p2pstreamv1.CreatePublicAccessProviderRequest{
		Name: "family", ProviderType: p2pstreamv1.PublicAccessProviderType_PUBLIC_ACCESS_PROVIDER_TYPE_LOCAL,
		Enabled: true, LocalAuthMode: p2pstreamv1.PublicAccessLocalAuthMode_PUBLIC_ACCESS_LOCAL_AUTH_MODE_FORM,
		LocalAuthSessionDurationMillis: defaultPublicAccessSessionMillis, LocalAuthRealm: "Family services",
		LocalAuthLoginTemplateId: templateID,
	})
	providerReq.Header().Set("Cookie", header.Get("Cookie"))
	providerResp, err := app.CreatePublicAccessProvider(ctx, providerReq)
	if err != nil {
		t.Fatalf("create local provider with sign-in template: %v", err)
	}
	if got := providerResp.Msg.Provider.LocalAuthLoginTemplateId; got != templateID {
		t.Fatalf("local sign-in template = %d, want %d", got, templateID)
	}
	provider := app.currentPublicSnapshot().AccessProviders[providerResp.Msg.Provider.Id]
	if provider.LocalAuthLoginTemplate == nil || provider.LocalAuthLoginTemplateID != templateID {
		t.Fatalf("runtime local sign-in template was not compiled: %+v", provider)
	}

	deleteReq := connect.NewRequest(&p2pstreamv1.DeletePublicResponseTemplateRequest{Id: templateID})
	deleteReq.Header().Set("Cookie", header.Get("Cookie"))
	if _, err := app.DeletePublicResponseTemplate(ctx, deleteReq); err == nil {
		t.Fatal("deleting a provider-assigned sign-in template succeeded")
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

func TestPublicAccessLocalFormLoginCreatesRevocableSession(t *testing.T) {
	database := newServerTestDB(t)
	providerRow, userRow := createTestPublicLocalAccessIdentity(t, database, publicAccessLocalAuthModeForm)

	upstreamHeaders := make(chan http.Header, 1)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamHeaders <- r.Header.Clone()
		_, _ = w.Write([]byte("local upstream"))
	}))
	t.Cleanup(upstream.Close)
	handler := newTestPublicLocalAccessProxy(t, database, upstream.URL, providerRow, userRow)

	first := httptest.NewRecorder()
	handler.ServeHTTP(first, httptest.NewRequest(http.MethodGet, "http://app.example/private?next=1", nil))
	if first.Code != http.StatusUnauthorized || !strings.Contains(first.Body.String(), "<form") {
		t.Fatalf("initial response = %d %q", first.Code, first.Body.String())
	}
	if got := first.Header().Get("WWW-Authenticate"); got != "" {
		t.Fatalf("form login unexpectedly sent WWW-Authenticate: %q", got)
	}
	csrfCookie := responseCookieNamed(t, first.Result(), publicAccessCSRFCookieName(providerRow.ID))

	form := url.Values{
		publicAccessUsernameField: {userRow.Username},
		publicAccessPasswordField: {"correct horse battery staple"},
		publicAccessCSRFField:     {csrfCookie.Value},
	}
	loginReq := httptest.NewRequest(
		http.MethodPost,
		"http://app.example/private?next=1&"+publicAccessLoginQueryKey+"=1",
		strings.NewReader(form.Encode()),
	)
	loginReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	loginReq.AddCookie(csrfCookie)
	login := httptest.NewRecorder()
	handler.ServeHTTP(login, loginReq)
	if login.Code != http.StatusSeeOther || login.Header().Get("Location") != "?next=1" {
		t.Fatalf("login response = %d location %q body %q", login.Code, login.Header().Get("Location"), login.Body.String())
	}
	sessionCookie := responseCookieNamed(t, login.Result(), publicAccessSessionCookieName(providerRow.ID))
	if sessionCookie.Value == "" {
		t.Fatal("login did not issue a session cookie")
	}

	authedReq := httptest.NewRequest(http.MethodGet, "http://app.example/private", nil)
	authedReq.AddCookie(sessionCookie)
	authedReq.Header.Set("Authorization", "Bearer application-token")
	authedReq.Header.Set("X-Auth-Request-User", "mallory")
	authedReq.Header.Set("X-Auth-Request-Email", "mallory@example.test")
	authed := httptest.NewRecorder()
	handler.ServeHTTP(authed, authedReq)
	if authed.Code != http.StatusOK || authed.Body.String() != "local upstream" {
		t.Fatalf("authenticated response = %d %q", authed.Code, authed.Body.String())
	}
	select {
	case headers := <-upstreamHeaders:
		assertForwardedHeader(t, headers, "X-Auth-Request-User", userRow.Username)
		assertForwardedHeader(t, headers, "X-Auth-Request-Groups", "engineering, operators")
		if got := headers.Get("X-Auth-Request-Email"); got != "" {
			t.Fatalf("spoofed local identity email reached upstream: %q", got)
		}
		if got := headers.Get("Authorization"); got != "Bearer application-token" {
			t.Fatalf("form-authenticated application Authorization = %q", got)
		}
	case <-time.After(time.Second):
		t.Fatal("upstream request was not received")
	}
}

func TestPublicAccessLocalLoginReferencesCannotEscapeOrigin(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "http://app.example//evil.example/collect?next=1", nil)
	if got := publicAccessLoginAction(req); got != "?__p2pstream_access_login=1&next=1" {
		t.Fatalf("login action = %q", got)
	}
	req.URL.RawQuery = "next=1&__p2pstream_access_login=1"
	if got := publicAccessReturnURI(req); got != "?next=1" {
		t.Fatalf("return location = %q", got)
	}
}

func TestPublicAccessLocalFormOriginValidation(t *testing.T) {
	listener := publicListenerConfig{Protocol: publicListenerProtocolHTTPS, Port: 8443}
	tests := []struct {
		name    string
		origins []string
		want    bool
	}{
		{name: "same origin", origins: []string{"https://app.example:8443"}, want: true},
		{name: "case insensitive host", origins: []string{"https://APP.EXAMPLE:8443"}, want: true},
		{name: "missing for non-browser client", want: true},
		{name: "sibling origin", origins: []string{"https://evil.example:8443"}},
		{name: "scheme mismatch", origins: []string{"http://app.example:8443"}},
		{name: "origin with path", origins: []string{"https://app.example:8443/login"}},
		{name: "duplicate origin", origins: []string{"https://app.example:8443", "https://app.example:8443"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "https://app.example:8443/private", nil)
			for _, origin := range test.origins {
				req.Header.Add("Origin", origin)
			}
			if got := publicAccessFormOriginAllowed(req, listener); got != test.want {
				t.Fatalf("publicAccessFormOriginAllowed() = %v, want %v", got, test.want)
			}
		})
	}
}

func TestPublicAccessLocalFormLoginRejectsMissingCSRF(t *testing.T) {
	database := newServerTestDB(t)
	providerRow, userRow := createTestPublicLocalAccessIdentity(t, database, publicAccessLocalAuthModeForm)
	handler := newTestPublicLocalAccessProxy(t, database, "http://127.0.0.1:1", providerRow, userRow)
	form := url.Values{
		publicAccessUsernameField: {userRow.Username},
		publicAccessPasswordField: {"correct horse battery staple"},
		publicAccessCSRFField:     {"attacker-controlled-token-without-cookie-000"},
	}
	req := httptest.NewRequest(http.MethodPost, "http://app.example/private?"+publicAccessLoginQueryKey+"=1", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, req)
	if recorder.Code != http.StatusBadRequest || !strings.Contains(recorder.Body.String(), "expired") {
		t.Fatalf("response = %d %q, want CSRF rejection", recorder.Code, recorder.Body.String())
	}
	if cookie := findResponseCookie(recorder.Result(), publicAccessSessionCookieName(providerRow.ID)); cookie != nil && cookie.Value != "" {
		t.Fatal("CSRF rejection issued a session cookie")
	}
}

func TestPublicAccessLocalBasicUsesUsersAndStripsCredentials(t *testing.T) {
	database := newServerTestDB(t)
	providerRow, userRow := createTestPublicLocalAccessIdentity(t, database, publicAccessLocalAuthModeBasic)
	upstreamHeaders := make(chan http.Header, 1)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamHeaders <- r.Header.Clone()
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(upstream.Close)
	handler := newTestPublicLocalAccessProxy(t, database, upstream.URL, providerRow, userRow)

	challenge := httptest.NewRecorder()
	handler.ServeHTTP(challenge, httptest.NewRequest(http.MethodGet, "http://app.example/private", nil))
	if challenge.Code != http.StatusUnauthorized || !strings.HasPrefix(challenge.Header().Get("WWW-Authenticate"), "Basic ") {
		t.Fatalf("challenge = %d %#v", challenge.Code, challenge.Header())
	}

	badReq := httptest.NewRequest(http.MethodGet, "http://app.example/private", nil)
	badReq.SetBasicAuth(userRow.Username, "incorrect password")
	bad := httptest.NewRecorder()
	handler.ServeHTTP(bad, badReq)
	if bad.Code != http.StatusUnauthorized {
		t.Fatalf("bad password status = %d, want 401", bad.Code)
	}

	goodReq := httptest.NewRequest(http.MethodGet, "http://app.example/private", nil)
	goodReq.SetBasicAuth(userRow.Username, "correct horse battery staple")
	good := httptest.NewRecorder()
	handler.ServeHTTP(good, goodReq)
	if good.Code != http.StatusNoContent {
		t.Fatalf("valid Basic status = %d body %q", good.Code, good.Body.String())
	}
	select {
	case headers := <-upstreamHeaders:
		assertForwardedHeader(t, headers, "X-Auth-Request-User", userRow.Username)
		if got := headers.Get("Authorization"); got != "" {
			t.Fatalf("Basic credentials reached upstream: %q", got)
		}
	case <-time.After(time.Second):
		t.Fatal("upstream request was not received")
	}
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

func TestCheckPublicForwardAuthPreservesEscapedOriginalURL(t *testing.T) {
	for _, requestTarget := range []string{
		"http://app.example/private%2Fadmin?next=%2F",
		"http://app.example/private%5Cadmin?next=%5C",
	} {
		t.Run(requestTarget, func(t *testing.T) {
			var checked *http.Request
			provider := testPublicAccessProvider(t, httpClientFunc(func(req *http.Request) (*http.Response, error) {
				checked = req.Clone(req.Context())
				return &http.Response{
					StatusCode: http.StatusNoContent,
					Header:     make(http.Header),
					Body:       io.NopCloser(strings.NewReader("")),
				}, nil
			}))
			incoming := httptest.NewRequest(http.MethodGet, requestTarget, nil)

			if _, err := checkPublicForwardAuth(
				context.Background(),
				provider,
				publicListenerConfig{Protocol: publicListenerProtocolHTTP, Port: 80},
				incoming,
			); err != nil {
				t.Fatalf("check forward auth: %v", err)
			}
			if checked == nil {
				t.Fatal("forward-auth request was not captured")
			}
			want := requestTarget
			assertForwardedHeader(t, checked.Header, "X-Forwarded-Uri", incoming.URL.RequestURI())
			assertForwardedHeader(t, checked.Header, "X-Original-Url", want)
			assertForwardedHeader(t, checked.Header, "X-Auth-Request-Redirect", want)
		})
	}
}

func TestReconcilePublicAccessProviderTransports(t *testing.T) {
	unchangedPreviousTransport := &testIdleConnectionsCloser{}
	unchangedCurrentTransport := &testIdleConnectionsCloser{}
	changedPreviousTransport := &testIdleConnectionsCloser{}
	changedCurrentTransport := &testIdleConnectionsCloser{}
	removedTransport := &testIdleConnectionsCloser{}
	newTransport := &testIdleConnectionsCloser{}
	unchangedPreviousClient := &http.Client{}
	unchangedCurrentClient := &http.Client{}
	changedPreviousClient := &http.Client{}
	changedCurrentClient := &http.Client{}
	newClient := &http.Client{}

	previous := &publicProxySnapshot{AccessProviders: map[int64]publicAccessProviderConfig{
		1: {
			ID: 1, ForwardAuthURL: "https://auth.example.test/verify", Timeout: time.Second,
			client: unchangedPreviousClient, transport: unchangedPreviousTransport,
		},
		2: {
			ID: 2, ForwardAuthURL: "https://old-auth.example.test/verify", Timeout: time.Second,
			client: changedPreviousClient, transport: changedPreviousTransport,
		},
		3: {
			ID: 3, ForwardAuthURL: "https://removed-auth.example.test/verify", Timeout: time.Second,
			client: &http.Client{}, transport: removedTransport,
		},
	}}
	current := &publicProxySnapshot{AccessProviders: map[int64]publicAccessProviderConfig{
		1: {
			ID: 1, ForwardAuthURL: "https://auth.example.test/verify", Timeout: time.Second,
			client: unchangedCurrentClient, transport: unchangedCurrentTransport,
		},
		2: {
			ID: 2, ForwardAuthURL: "https://new-auth.example.test/verify", Timeout: time.Second,
			client: changedCurrentClient, transport: changedCurrentTransport,
		},
		4: {
			ID: 4, ForwardAuthURL: "https://new-provider.example.test/verify", Timeout: time.Second,
			client: newClient, transport: newTransport,
		},
	}}

	reconcilePublicAccessProviderTransports(previous, current)

	if current.AccessProviders[1].client != unchangedPreviousClient || current.AccessProviders[1].transport != unchangedPreviousTransport {
		t.Fatal("unchanged provider did not reuse its existing client and transport")
	}
	if unchangedPreviousTransport.closeCalls.Load() != 0 || unchangedCurrentTransport.closeCalls.Load() != 1 {
		t.Fatalf(
			"unchanged transport close calls = previous %d, replacement %d; want 0, 1",
			unchangedPreviousTransport.closeCalls.Load(),
			unchangedCurrentTransport.closeCalls.Load(),
		)
	}
	if current.AccessProviders[2].client != changedCurrentClient || current.AccessProviders[2].transport != changedCurrentTransport {
		t.Fatal("changed provider did not keep its replacement client and transport")
	}
	if changedPreviousTransport.closeCalls.Load() != 1 || changedCurrentTransport.closeCalls.Load() != 0 {
		t.Fatalf(
			"changed transport close calls = previous %d, replacement %d; want 1, 0",
			changedPreviousTransport.closeCalls.Load(),
			changedCurrentTransport.closeCalls.Load(),
		)
	}
	if removedTransport.closeCalls.Load() != 1 {
		t.Fatalf("removed transport close calls = %d, want 1", removedTransport.closeCalls.Load())
	}
	if current.AccessProviders[4].client != newClient || current.AccessProviders[4].transport != newTransport || newTransport.closeCalls.Load() != 0 {
		t.Fatal("new provider transport was unexpectedly replaced or closed")
	}

	reconcilePublicAccessProviderTransports(current, nil)
	if unchangedPreviousTransport.closeCalls.Load() != 1 || changedCurrentTransport.closeCalls.Load() != 1 || newTransport.closeCalls.Load() != 1 {
		t.Fatalf(
			"shutdown transport close calls = unchanged %d, changed %d, new %d; want 1 each",
			unchangedPreviousTransport.closeCalls.Load(),
			changedCurrentTransport.closeCalls.Load(),
			newTransport.closeCalls.Load(),
		)
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

func createTestPublicLocalAccessIdentity(t *testing.T, database *db.DB, mode string) (db.PublicAccessProvider, db.PublicAccessUser) {
	t.Helper()
	ctx := context.Background()
	provider, err := database.CreatePublicAccessProvider(ctx, db.CreatePublicAccessProviderParams{
		Name: "local-users", ProviderType: publicAccessProviderTypeLocal, Enabled: 1,
		ForwardAuthUrl: "", TimeoutMillis: defaultPublicAccessTimeoutMillis, TlsSkipVerify: 0,
		SubjectHeader: "X-Auth-Request-Preferred-Username", UserHeader: "X-Auth-Request-User",
		EmailHeader: "X-Auth-Request-Email", GroupsHeader: "X-Auth-Request-Groups",
		ForwardedHeadersJson:           `["X-Auth-Request-Groups","X-Auth-Request-Preferred-Username","X-Auth-Request-User"]`,
		LocalAuthMode:                  mode,
		LocalAuthSessionDurationMillis: defaultPublicAccessSessionMillis,
		LocalAuthRealm:                 "Private test",
	})
	if err != nil {
		t.Fatalf("create local access provider: %v", err)
	}
	passwordHash, err := authutil.HashPassword("correct horse battery staple")
	if err != nil {
		t.Fatalf("hash local access password: %v", err)
	}
	user, err := database.CreatePublicAccessUser(ctx, db.CreatePublicAccessUserParams{
		ProviderID: provider.ID, Username: "alice", PasswordHash: passwordHash, Enabled: 1,
		GroupsJson: `["engineering","operators"]`,
	})
	if err != nil {
		t.Fatalf("create local access user: %v", err)
	}
	return provider, user
}

func newTestPublicLocalAccessProxy(
	t *testing.T,
	database *db.DB,
	upstreamURL string,
	providerRow db.PublicAccessProvider,
	userRow db.PublicAccessUser,
) http.Handler {
	t.Helper()
	provider, err := publicAccessProviderRowToConfig(providerRow)
	if err != nil {
		t.Fatalf("local access provider config: %v", err)
	}
	if err := configurePublicAccessLocalLoginTemplate(&provider, nil); err != nil {
		t.Fatalf("local access login template config: %v", err)
	}
	user, err := publicAccessUserRowToConfig(userRow)
	if err != nil {
		t.Fatalf("local access user config: %v", err)
	}
	provider.LocalUsers[user.Username] = user
	origin := mustParsePublicAccessTestURL(t, upstreamURL)
	target := publicRouteTargetConfig{
		ID: 20, RouteID: 10, Name: "upstream", Enabled: true,
		TargetType: publicRouteTargetTypeProxy, Transport: publicRouteTargetTransportDirect, ParsedURL: origin,
	}
	route := publicRouteConfig{
		ID: 10, Enabled: true, PathPrefix: "/", Action: publicRouteActionForward,
		PathSecurityMode: publicRoutePathSecurityModeStrict, AccessPolicyID: 40,
		Targets: []publicRouteTargetConfig{target},
	}
	app := NewApp(nil, database)
	setPublicSnapshotForTest(t, app, &publicProxySnapshot{
		Listeners: map[int64]publicListenerConfig{1: {
			ID: 1, Port: 80, Protocol: publicListenerProtocolHTTP, Enabled: true,
		}},
		RoutesByListener: map[int64][]publicRouteConfig{1: {route}},
		RouteTargets:     map[int64]publicRouteTargetConfig{target.ID: target},
		AccessProviders:  map[int64]publicAccessProviderConfig{provider.ID: provider},
		AccessPolicies: map[int64]publicAccessPolicyConfig{40: {
			ID: 40, Name: "signed-in", ProviderID: provider.ID, Enabled: true, GroupMatch: publicAccessGroupMatchAny,
		}},
		AccessHeaderNames: append([]string(nil), localPublicAccessForwardedHeaders...),
	})
	return app.publicProxyHandler(1)
}

func responseCookieNamed(t *testing.T, response *http.Response, name string) *http.Cookie {
	t.Helper()
	cookie := findResponseCookie(response, name)
	if cookie == nil {
		t.Fatalf("response did not include cookie %q", name)
	}
	return cookie
}

func findResponseCookie(response *http.Response, name string) *http.Cookie {
	if response == nil {
		return nil
	}
	for _, cookie := range response.Cookies() {
		if cookie.Name == name {
			return cookie
		}
	}
	return nil
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

type testIdleConnectionsCloser struct {
	closeCalls atomic.Int64
}

func (c *testIdleConnectionsCloser) CloseIdleConnections() {
	c.closeCalls.Add(1)
}
