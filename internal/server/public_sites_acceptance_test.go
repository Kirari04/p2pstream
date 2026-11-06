package server

import (
	"context"
	"crypto/tls"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"connectrpc.com/connect"

	p2pstreamv1 "p2pstream/gen/proto/p2pstream/v1"
	"p2pstream/internal/db"
)

// Exercise the management-to-runtime boundary rather than constructing a
// snapshot: drafts, publication, rebinding and database cascades must agree.
func TestSiteWorkspaceLifecycleAcrossListeners(t *testing.T) {
	ctx := context.Background()
	app := NewApp(nil, newServerTestDB(t))
	defer app.CloseObservabilityRecorder(ctx)
	cookie := createTestAdminSession(t, app).Get("Cookie")
	first := siteWorkspaceListener(t, app, "first", 18080)
	second := siteWorkspaceListener(t, app, "second", 18081)
	bindings := siteWorkspaceBindings(first.ID, second.ID)

	fallback := siteWorkspaceCreate(t, app, cookie, &p2pstreamv1.CreatePublicSiteRequest{
		Name: "Fallback", Enabled: true, DefaultSite: true, ListenerBindings: bindings,
	})
	fallbackRoute := siteWorkspaceRoute(t, app, cookie, fallback.Id, "", "/fallback", true)
	siteWorkspacePublish(t, app, cookie, fallback.Id)

	named := siteWorkspaceCreate(t, app, cookie, &p2pstreamv1.CreatePublicSiteRequest{
		Name: "Application", Enabled: true, ListenerBindings: bindings,
		Hosts: []*p2pstreamv1.PublicSiteHostInput{{HostnamePattern: "app.example.test"}},
	})
	if named.Published {
		t.Fatal("new Site became published before Publish")
	}
	applicationRoute := siteWorkspaceRoute(t, app, cookie, named.Id, "/api", "/application", false)
	if applicationRoute.ListenerId != 0 || applicationRoute.SiteId != named.Id {
		t.Fatalf("route is not owned independently by Site: %v", applicationRoute)
	}
	for _, listener := range []int64{first.ID, second.ID} {
		siteWorkspaceResponse(t, app, listener, "app.example.test", "/api", 302, "/fallback")
	}
	siteWorkspacePublish(t, app, cookie, named.Id)
	for _, listener := range []int64{first.ID, second.ID} {
		siteWorkspaceResponse(t, app, listener, "app.example.test", "/api", 302, "/application")
		siteWorkspaceResponse(t, app, listener, "app.example.test", "/missing", 404, "")
		siteWorkspaceResponse(t, app, listener, "other.example.test", "/missing", 302, "/fallback")
	}
	// Older clients cannot express the other bindings. A scalar-only edit
	// must not silently remove them from a shared Site.
	_, err := app.UpdatePublicSite(ctx, siteWorkspaceRequest(cookie, &p2pstreamv1.UpdatePublicSiteRequest{
		Id: named.Id, ListenerId: first.ID, Name: named.Name, Enabled: true,
		Hosts: []*p2pstreamv1.PublicSiteHostInput{{HostnamePattern: "app.example.test"}},
	}))
	if connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Fatalf("old client multi-listener update should require an updated client: %v", err)
	}

	update := &p2pstreamv1.UpdatePublicSiteRequest{
		Id: named.Id, Name: named.Name, Enabled: false, ListenerBindings: bindings,
		Hosts: []*p2pstreamv1.PublicSiteHostInput{{HostnamePattern: "app.example.test"}},
	}
	if _, err := app.UpdatePublicSite(ctx, siteWorkspaceRequest(cookie, update)); err != nil {
		t.Fatalf("disable Site: %v", err)
	}
	for _, listener := range []int64{first.ID, second.ID} {
		siteWorkspaceResponse(t, app, listener, "app.example.test", "/api", 404, "")
	}

	// Detaching releases only the removed listener's hostname claim.
	update.ListenerBindings = siteWorkspaceBindings(second.ID)
	if _, err := app.UpdatePublicSite(ctx, siteWorkspaceRequest(cookie, update)); err != nil {
		t.Fatalf("detach first listener: %v", err)
	}
	siteWorkspaceResponse(t, app, first.ID, "app.example.test", "/api", 302, "/fallback")
	siteWorkspaceResponse(t, app, second.ID, "app.example.test", "/api", 404, "")
	update.Enabled = true
	if _, err := app.UpdatePublicSite(ctx, siteWorkspaceRequest(cookie, update)); err != nil {
		t.Fatalf("enable Site on remaining listener: %v", err)
	}
	siteWorkspaceResponse(t, app, second.ID, "app.example.test", "/api", 302, "/application")

	// Deleting one listener must not cascade to a shared Site or its routes.
	if _, err := app.DeletePublicListener(ctx, siteWorkspaceRequest(cookie, &p2pstreamv1.DeletePublicListenerRequest{Id: first.ID})); err != nil {
		t.Fatalf("delete first listener: %v", err)
	}
	for _, id := range []int64{applicationRoute.Id, fallbackRoute.Id} {
		if _, err := app.DB.GetPublicRoute(ctx, id); err != nil {
			t.Fatalf("listener deletion removed shared route %d: %v", id, err)
		}
	}
	siteWorkspaceResponse(t, app, second.ID, "other.example.test", "/", 302, "/fallback")
	siteWorkspaceResponse(t, app, second.ID, "app.example.test", "/api", 302, "/application")

	// Reopening configuration must retain the same request behavior and IDs.
	if err := app.refreshPublicProxySnapshot(ctx); err != nil {
		t.Fatalf("reload configuration: %v", err)
	}
	match, err := app.matchPublicRoute(second.ID, httptest.NewRequest("GET", "http://app.example.test/api", nil))
	if err != nil || match.Route.ID != applicationRoute.Id {
		t.Fatalf("route identity changed after reload: id=%d err=%v", match.Route.ID, err)
	}
	update.ListenerBindings = nil
	if _, err := app.UpdatePublicSite(ctx, siteWorkspaceRequest(cookie, update)); err != nil {
		t.Fatalf("detach final binding from published Site: %v", err)
	}
	siteWorkspaceResponse(t, app, second.ID, "app.example.test", "/api", 302, "/fallback")
	update.ListenerBindings = siteWorkspaceBindings(second.ID)
	if _, err := app.UpdatePublicSite(ctx, siteWorkspaceRequest(cookie, update)); err != nil {
		t.Fatalf("reattach published Site: %v", err)
	}
	siteWorkspaceResponse(t, app, second.ID, "app.example.test", "/api", 302, "/application")
}

func TestSiteWorkspacePublishAndRebindConflictsAreAtomic(t *testing.T) {
	ctx := context.Background()
	app := NewApp(nil, newServerTestDB(t))
	defer app.CloseObservabilityRecorder(ctx)
	cookie := createTestAdminSession(t, app).Get("Cookie")
	first := siteWorkspaceListener(t, app, "first", 18180)
	second := siteWorkspaceListener(t, app, "second", 18181)
	create := func(name string, listener int64, destination string) *p2pstreamv1.PublicSite {
		site := siteWorkspaceCreate(t, app, cookie, &p2pstreamv1.CreatePublicSiteRequest{
			Name: name, Enabled: true, ListenerBindings: siteWorkspaceBindings(listener),
			Hosts: []*p2pstreamv1.PublicSiteHostInput{{HostnamePattern: "shared.example.test"}},
		})
		siteWorkspaceRoute(t, app, cookie, site.Id, "", destination, true)
		return site
	}
	one := create("One", first.ID, "/one")
	two := create("Two", second.ID, "/two")
	siteWorkspacePublish(t, app, cookie, one.Id)
	siteWorkspacePublish(t, app, cookie, two.Id)
	conflictingDraft := create("Conflicting-draft", first.ID, "/draft")
	if _, err := app.PublishPublicSite(ctx, siteWorkspaceRequest(cookie, &p2pstreamv1.PublishPublicSiteRequest{Id: conflictingDraft.Id})); err == nil {
		t.Fatal("published a draft over an existing hostname owner")
	}
	siteWorkspaceResponse(t, app, first.ID, "shared.example.test", "/", 302, "/one")

	// A failure on the added listener must not release the existing binding.
	_, err := app.UpdatePublicSite(ctx, siteWorkspaceRequest(cookie, &p2pstreamv1.UpdatePublicSiteRequest{
		Id: one.Id, Name: one.Name, Enabled: true,
		ListenerBindings: siteWorkspaceBindings(first.ID, second.ID),
		Hosts:            []*p2pstreamv1.PublicSiteHostInput{{HostnamePattern: "shared.example.test"}},
	}))
	if err == nil {
		t.Fatal("published Site stole another Site's hostname on new listener")
	}
	if err := app.refreshPublicProxySnapshot(ctx); err != nil {
		t.Fatalf("reload after rejected mutation: %v", err)
	}
	siteWorkspaceResponse(t, app, first.ID, "shared.example.test", "/", 302, "/one")
	siteWorkspaceResponse(t, app, second.ID, "shared.example.test", "/", 302, "/two")
	config, err := app.publicProxyConfigResponse(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, site := range config.Sites {
		if site.Id == conflictingDraft.Id && site.Published {
			t.Fatal("rejected publication was persisted")
		}
		if site.Id == one.Id && (len(site.ListenerBindings) != 1 || site.ListenerBindings[0].ListenerId != first.ID) {
			t.Fatalf("rejected binding change was partially persisted: %v", site.ListenerBindings)
		}
	}
}

func TestSiteWorkspaceDisabledDraftStillRequiresValidPublication(t *testing.T) {
	ctx := context.Background()
	app := NewApp(nil, newServerTestDB(t))
	defer app.CloseObservabilityRecorder(ctx)
	cookie := createTestAdminSession(t, app).Get("Cookie")
	listener := siteWorkspaceListener(t, app, "incoming", 18279)
	for _, request := range []*p2pstreamv1.CreatePublicSiteRequest{
		{Name: "Disabled-unbound", Enabled: false, DefaultSite: true},
		{Name: "Disabled-no-hostnames", Enabled: false, ListenerBindings: siteWorkspaceBindings(listener.ID)},
	} {
		site := siteWorkspaceCreate(t, app, cookie, request)
		_, err := app.PublishPublicSite(ctx, siteWorkspaceRequest(cookie, &p2pstreamv1.PublishPublicSiteRequest{Id: site.Id}))
		if connect.CodeOf(err) != connect.CodeFailedPrecondition {
			t.Fatalf("incomplete disabled draft %q should not publish: %v", site.Name, err)
		}
		stored, err := app.DB.GetPublicSite(ctx, site.Id)
		if err != nil || stored.Published != 0 {
			t.Fatalf("failed publication changed draft %q: published=%d err=%v", site.Name, stored.Published, err)
		}
	}
}

func TestSiteWorkspaceUnboundDraftAndDisabledDefault(t *testing.T) {
	ctx := context.Background()
	app := NewApp(nil, newServerTestDB(t))
	defer app.CloseObservabilityRecorder(ctx)
	cookie := createTestAdminSession(t, app).Get("Cookie")
	listener := siteWorkspaceListener(t, app, "incoming", 18280)
	// An unmigrated route must not take over when a published Default Site is
	// disabled. The reserved default binding remains an isolation boundary.
	if _, err := app.DB.ExecContext(ctx, `INSERT INTO public_routes
		(listener_id, priority, path_prefix, action, redirect_target_mode, redirect_target, redirect_status_code, redirect_preserve_path_suffix, enabled)
		VALUES (?, 1, '/', 'redirect', 'same_host_path', '/standalone', 302, 0, 1)`, listener.ID); err != nil {
		t.Fatal(err)
	}
	draft := siteWorkspaceCreate(t, app, cookie, &p2pstreamv1.CreatePublicSiteRequest{
		Name: "Default-draft", Enabled: true, DefaultSite: true,
	})
	route := siteWorkspaceRoute(t, app, cookie, draft.Id, "", "/site-default", true)
	if _, err := app.PublishPublicSite(ctx, siteWorkspaceRequest(cookie, &p2pstreamv1.PublishPublicSiteRequest{Id: draft.Id})); err == nil {
		t.Fatal("published an unassigned draft with no listener")
	}
	siteWorkspaceResponse(t, app, listener.ID, "unknown.example.test", "/", 302, "/standalone")
	update := &p2pstreamv1.UpdatePublicSiteRequest{
		Id: draft.Id, Name: draft.Name, Enabled: true, DefaultSite: true,
		ListenerBindings: siteWorkspaceBindings(listener.ID),
	}
	if _, err := app.UpdatePublicSite(ctx, siteWorkspaceRequest(cookie, update)); err != nil {
		t.Fatalf("attach draft: %v", err)
	}
	siteWorkspacePublish(t, app, cookie, draft.Id)
	siteWorkspaceResponse(t, app, listener.ID, "unknown.example.test", "/", 302, "/site-default")
	update.Enabled = false
	if _, err := app.UpdatePublicSite(ctx, siteWorkspaceRequest(cookie, update)); err != nil {
		t.Fatalf("disable Default Site: %v", err)
	}
	siteWorkspaceResponse(t, app, listener.ID, "unknown.example.test", "/", 404, "")

	// A conflicting draft is allowed but cannot publish over the reserved slot.
	conflict := siteWorkspaceCreate(t, app, cookie, &p2pstreamv1.CreatePublicSiteRequest{
		Name: "Second-default", Enabled: true, DefaultSite: true,
		ListenerBindings: siteWorkspaceBindings(listener.ID),
	})
	siteWorkspaceRoute(t, app, cookie, conflict.Id, "", "/replacement", true)
	if _, err := app.PublishPublicSite(ctx, siteWorkspaceRequest(cookie, &p2pstreamv1.PublishPublicSiteRequest{Id: conflict.Id})); err == nil {
		t.Fatal("disabled published Default Site lost its reservation")
	}

	// Removing the final listener retains the published Site and its routes.
	if _, err := app.DeletePublicListener(ctx, siteWorkspaceRequest(cookie, &p2pstreamv1.DeletePublicListenerRequest{Id: listener.ID})); err != nil {
		t.Fatalf("delete final listener: %v", err)
	}
	if _, err := app.DB.GetPublicRoute(ctx, route.Id); err != nil {
		t.Fatalf("last-listener deletion removed the Site route: %v", err)
	}
	if _, err := app.DB.GetPublicSite(ctx, draft.Id); err != nil {
		t.Fatalf("last-listener deletion removed the Site: %v", err)
	}
}

func TestSiteWorkspaceRedirectBindingsPreserveAuthorityAndPath(t *testing.T) {
	ctx := context.Background()
	app := NewApp(nil, newServerTestDB(t))
	defer app.CloseObservabilityRecorder(ctx)
	cookie := createTestAdminSession(t, app).Get("Cookie")
	plain := siteWorkspaceListener(t, app, "plain", 18380)
	secure, err := app.DB.CreatePublicListener(ctx, db.CreatePublicListenerParams{
		Name: "secure", BindAddress: "127.0.0.1", Port: 18443, Protocol: publicListenerProtocolHTTPS, Enabled: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	siteWorkspaceCertificate(t, app, secure.ID, "*.example.test")
	bindings := siteWorkspaceBindings(plain.ID, secure.ID)
	bindings[0].Behavior = p2pstreamv1.PublicSiteListenerBehavior_PUBLIC_SITE_LISTENER_BEHAVIOR_REDIRECT_HTTPS
	bindings[0].RedirectListenerId = secure.ID
	site := siteWorkspaceCreate(t, app, cookie, &p2pstreamv1.CreatePublicSiteRequest{
		Name: "Secure-app", Enabled: true, ListenerBindings: bindings,
		Hosts: []*p2pstreamv1.PublicSiteHostInput{{HostnamePattern: "*.example.test"}},
	})
	siteWorkspaceRoute(t, app, cookie, site.Id, "", "/served", true)
	siteWorkspacePublish(t, app, cookie, site.Id)
	siteWorkspaceResponse(t, app, plain.ID, "APP.example.test:1234", "/a%2Fb?q=one%20two", 308, "https://app.example.test:18443/a%2Fb?q=one%20two")
	siteWorkspaceResponse(t, app, plain.ID, "deep.app.example.test", "/", 404, "")

	request := httptest.NewRequest(http.MethodGet, "https://app.example.test/", nil)
	request.TLS = &tls.ConnectionState{ServerName: "app.example.test"}
	recorder := httptest.NewRecorder()
	app.publicProxyHandler(secure.ID)(recorder, request)
	if recorder.Code != 302 || recorder.Header().Get("Location") != "/served" {
		t.Fatalf("HTTPS Serve binding did not use shared routes: status=%d location=%q", recorder.Code, recorder.Header().Get("Location"))
	}

	// Turning the target into another redirect would create a cycle. Reject
	// the entire edit and retain the existing live redirect destination.
	bindings[1].Behavior = p2pstreamv1.PublicSiteListenerBehavior_PUBLIC_SITE_LISTENER_BEHAVIOR_REDIRECT_HTTPS
	bindings[1].RedirectListenerId = secure.ID
	_, err = app.UpdatePublicSite(ctx, siteWorkspaceRequest(cookie, &p2pstreamv1.UpdatePublicSiteRequest{
		Id: site.Id, Name: site.Name, Enabled: true, ListenerBindings: bindings,
		Hosts: []*p2pstreamv1.PublicSiteHostInput{{HostnamePattern: "*.example.test"}},
	}))
	if err == nil {
		t.Fatal("accepted a redirect binding that targets itself")
	}
	siteWorkspaceResponse(t, app, plain.ID, "app.example.test", "/", 308, "https://app.example.test:18443/")

	// An exact hostname on only the destination listener would send a
	// wildcard redirect into a different Site. Publication must account for
	// existing redirects as well as the new Site's own bindings.
	shadow := siteWorkspaceCreate(t, app, cookie, &p2pstreamv1.CreatePublicSiteRequest{
		Name: "Destination-shadow", Enabled: true, ListenerBindings: siteWorkspaceBindings(secure.ID),
		Hosts: []*p2pstreamv1.PublicSiteHostInput{{HostnamePattern: "app.example.test"}},
	})
	siteWorkspaceRoute(t, app, cookie, shadow.Id, "", "/shadow", true)
	if _, err := app.PublishPublicSite(ctx, siteWorkspaceRequest(cookie, &p2pstreamv1.PublishPublicSiteRequest{Id: shadow.Id})); err == nil {
		t.Fatal("new hostname ownership redirected an existing Site into another Site")
	}
	// Exact ownership on the source can make different ownership on the
	// destination valid. Removing that source claim must revalidate redirects.
	sourceClaim := siteWorkspaceCreate(t, app, cookie, &p2pstreamv1.CreatePublicSiteRequest{
		Name: "Source-claim", Enabled: true, ListenerBindings: siteWorkspaceBindings(plain.ID),
		Hosts: []*p2pstreamv1.PublicSiteHostInput{{HostnamePattern: "app.example.test"}},
	})
	siteWorkspacePublish(t, app, cookie, sourceClaim.Id)
	siteWorkspacePublish(t, app, cookie, shadow.Id)
	_, err = app.DeletePublicSite(ctx, siteWorkspaceRequest(cookie, &p2pstreamv1.DeletePublicSiteRequest{Id: sourceClaim.Id}))
	if connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Fatalf("deletion would redirect a wildcard hostname into another Site: %v", err)
	}
	siteWorkspaceResponse(t, app, plain.ID, "app.example.test", "/", 404, "")
}

func TestSiteWorkspaceExactOwnershipWinsOverWildcard(t *testing.T) {
	ctx := context.Background()
	app := NewApp(nil, newServerTestDB(t))
	defer app.CloseObservabilityRecorder(ctx)
	cookie := createTestAdminSession(t, app).Get("Cookie")
	listener := siteWorkspaceListener(t, app, "incoming", 18580)
	for _, fixture := range []struct{ name, pattern, destination string }{
		{"Wildcard", "*.example.test", "/wildcard"},
		{"Exact", "app.example.test", "/exact"},
	} {
		site := siteWorkspaceCreate(t, app, cookie, &p2pstreamv1.CreatePublicSiteRequest{
			Name: fixture.name, Enabled: true, ListenerBindings: siteWorkspaceBindings(listener.ID),
			Hosts: []*p2pstreamv1.PublicSiteHostInput{{HostnamePattern: fixture.pattern}},
		})
		siteWorkspaceRoute(t, app, cookie, site.Id, "", fixture.destination, true)
		siteWorkspacePublish(t, app, cookie, site.Id)
		if fixture.name == "Exact" {
			siteWorkspaceResponse(t, app, listener.ID, "app.example.test", "/", 302, "/exact")
			_, err := app.UpdatePublicSite(ctx, siteWorkspaceRequest(cookie, &p2pstreamv1.UpdatePublicSiteRequest{
				Id: site.Id, Name: site.Name, Enabled: false, ListenerBindings: siteWorkspaceBindings(listener.ID),
				Hosts: []*p2pstreamv1.PublicSiteHostInput{{HostnamePattern: fixture.pattern}},
			}))
			if err != nil {
				t.Fatal(err)
			}
		}
	}
	siteWorkspaceResponse(t, app, listener.ID, "app.example.test", "/", 404, "")
	siteWorkspaceResponse(t, app, listener.ID, "other.example.test", "/", 302, "/wildcard")
	siteWorkspaceResponse(t, app, listener.ID, "deep.other.example.test", "/", 404, "")
}

func TestSiteWorkspaceDefaultRedirectNeedsExplicitDestination(t *testing.T) {
	ctx := context.Background()
	app := NewApp(nil, newServerTestDB(t))
	defer app.CloseObservabilityRecorder(ctx)
	cookie := createTestAdminSession(t, app).Get("Cookie")
	plain := siteWorkspaceListener(t, app, "redirect-source", 18581)
	secure, err := app.DB.CreatePublicListener(ctx, db.CreatePublicListenerParams{
		Name: "redirect-destination", Port: 18444, Protocol: publicListenerProtocolHTTPS, Enabled: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	siteWorkspaceCertificate(t, app, secure.ID, "secure.example.test")
	bindings := siteWorkspaceBindings(plain.ID, secure.ID)
	bindings[0].Behavior = p2pstreamv1.PublicSiteListenerBehavior_PUBLIC_SITE_LISTENER_BEHAVIOR_REDIRECT_HTTPS
	bindings[0].RedirectListenerId = secure.ID
	site := siteWorkspaceCreate(t, app, cookie, &p2pstreamv1.CreatePublicSiteRequest{
		Name: "Default-redirect", Enabled: true, DefaultSite: true, ListenerBindings: bindings,
	})
	siteWorkspaceRoute(t, app, cookie, site.Id, "", "/served", true)
	_, err = app.PublishPublicSite(ctx, siteWorkspaceRequest(cookie, &p2pstreamv1.PublishPublicSiteRequest{Id: site.Id}))
	if connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Fatalf("Default redirect must require a fixed destination hostname: %v", err)
	}
	bindings[0].RedirectHostname = "secure.example.test"
	if _, err := app.UpdatePublicSite(ctx, siteWorkspaceRequest(cookie, &p2pstreamv1.UpdatePublicSiteRequest{
		Id: site.Id, Name: site.Name, Enabled: true, DefaultSite: true, ListenerBindings: bindings,
	})); err != nil {
		t.Fatal(err)
	}
	siteWorkspacePublish(t, app, cookie, site.Id)
	siteWorkspaceResponse(t, app, plain.ID, "untrusted.example:1234", "/a%2Fb?q=one", 308, "https://secure.example.test:18444/a%2Fb?q=one")
	shadow := siteWorkspaceCreate(t, app, cookie, &p2pstreamv1.CreatePublicSiteRequest{
		Name: "Default-destination-shadow", Enabled: true, ListenerBindings: siteWorkspaceBindings(secure.ID),
		Hosts: []*p2pstreamv1.PublicSiteHostInput{{HostnamePattern: "secure.example.test"}},
	})
	_, err = app.PublishPublicSite(ctx, siteWorkspaceRequest(cookie, &p2pstreamv1.PublishPublicSiteRequest{Id: shadow.Id}))
	if connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Fatalf("named Site must not take over an existing Default redirect destination: %v", err)
	}
}

func TestSiteWorkspaceConcurrentPublishHasOneOwner(t *testing.T) {
	ctx := context.Background()
	app := NewApp(nil, newServerTestDB(t))
	defer app.CloseObservabilityRecorder(ctx)
	cookie := createTestAdminSession(t, app).Get("Cookie")
	listener := siteWorkspaceListener(t, app, "incoming", 18680)
	first := siteWorkspaceCreate(t, app, cookie, &p2pstreamv1.CreatePublicSiteRequest{
		Name: "First", Enabled: true, ListenerBindings: siteWorkspaceBindings(listener.ID),
		Hosts: []*p2pstreamv1.PublicSiteHostInput{{HostnamePattern: "shared.example.test"}},
	})
	second := siteWorkspaceCreate(t, app, cookie, &p2pstreamv1.CreatePublicSiteRequest{
		Name: "Second", Enabled: true, ListenerBindings: siteWorkspaceBindings(listener.ID),
		Hosts: []*p2pstreamv1.PublicSiteHostInput{{HostnamePattern: "shared.example.test"}},
	})
	siteWorkspaceRoute(t, app, cookie, first.Id, "", "/first", true)
	siteWorkspaceRoute(t, app, cookie, second.Id, "", "/second", true)
	type outcome struct {
		id  int64
		err error
	}
	start := make(chan struct{})
	results := make(chan outcome, 2)
	for _, id := range []int64{first.Id, second.Id} {
		go func(id int64) {
			<-start
			_, err := app.PublishPublicSite(ctx, siteWorkspaceRequest(cookie, &p2pstreamv1.PublishPublicSiteRequest{Id: id}))
			results <- outcome{id: id, err: err}
		}(id)
	}
	close(start)
	winner, successes := int64(0), 0
	for range 2 {
		result := <-results
		if result.err == nil {
			winner, successes = result.id, successes+1
		} else if connect.CodeOf(result.err) != connect.CodeFailedPrecondition {
			t.Errorf("losing publication returned unexpected error: %v", result.err)
		}
	}
	if successes != 1 {
		t.Fatalf("concurrent publications produced %d owners", successes)
	}
	destination := "/first"
	if winner == second.Id {
		destination = "/second"
	}
	siteWorkspaceResponse(t, app, listener.ID, "shared.example.test", "/", 302, destination)
}

func siteWorkspaceCertificate(t *testing.T, app *App, listenerID int64, hostname string) {
	t.Helper()
	certPEM, keyPEM, _, err := generatePublicSelfSignedCertificatePEM(hostname, 24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	certPath, keyPath := filepath.Join(dir, "cert.pem"), filepath.Join(dir, "key.pem")
	if err := os.WriteFile(certPath, certPEM, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyPath, keyPEM, 0o600); err != nil {
		t.Fatal(err)
	}
	_, err = app.DB.CreatePublicTlsCertificate(context.Background(), db.CreatePublicTlsCertificateParams{
		ListenerID: listenerID, HostnamePattern: hostname, CertPath: certPath, KeyPath: keyPath,
		Enabled: 1, Source: publicTLSCertificateSourceManual, Status: publicTLSCertificateStatusReady,
	})
	if err != nil {
		t.Fatal(err)
	}
}

func siteWorkspaceRequest[T any](cookie string, message *T) *connect.Request[T] {
	request := connect.NewRequest(message)
	request.Header().Set("Cookie", cookie)
	return request
}

func siteWorkspaceListener(t *testing.T, app *App, name string, port int64) db.PublicListener {
	t.Helper()
	listener, err := app.DB.CreatePublicListener(context.Background(), db.CreatePublicListenerParams{
		Name: name, BindAddress: "127.0.0.1", Port: port, Protocol: publicListenerProtocolHTTP, Enabled: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	return listener
}

func siteWorkspaceBindings(ids ...int64) []*p2pstreamv1.PublicSiteListenerBinding {
	bindings := make([]*p2pstreamv1.PublicSiteListenerBinding, 0, len(ids))
	for _, id := range ids {
		bindings = append(bindings, &p2pstreamv1.PublicSiteListenerBinding{ListenerId: id, Behavior: p2pstreamv1.PublicSiteListenerBehavior_PUBLIC_SITE_LISTENER_BEHAVIOR_SERVE})
	}
	return bindings
}

func siteWorkspaceCreate(t *testing.T, app *App, cookie string, input *p2pstreamv1.CreatePublicSiteRequest) *p2pstreamv1.PublicSite {
	t.Helper()
	response, err := app.CreatePublicSite(context.Background(), siteWorkspaceRequest(cookie, input))
	if err != nil {
		t.Fatalf("create Site: %v", err)
	}
	return response.Msg.Site
}

func siteWorkspaceRoute(t *testing.T, app *App, cookie string, siteID int64, path, target string, isDefault bool) *p2pstreamv1.PublicRoute {
	t.Helper()
	response, err := app.CreatePublicRoute(context.Background(), siteWorkspaceRequest(cookie, &p2pstreamv1.CreatePublicRouteRequest{
		SiteId: &siteID, Priority: 10, PathPrefix: path, IsDefault: isDefault, Enabled: true,
		Action:             p2pstreamv1.PublicRouteAction_PUBLIC_ROUTE_ACTION_REDIRECT,
		RedirectTargetMode: p2pstreamv1.PublicRouteRedirectTargetMode_PUBLIC_ROUTE_REDIRECT_TARGET_MODE_SAME_HOST_PATH,
		RedirectTarget:     target, RedirectStatusCode: 302, RedirectPreserveQuery: true,
	}))
	if err != nil {
		t.Fatalf("create Site route: %v", err)
	}
	return response.Msg.Route
}

func siteWorkspacePublish(t *testing.T, app *App, cookie string, id int64) {
	t.Helper()
	response, err := app.PublishPublicSite(context.Background(), siteWorkspaceRequest(cookie, &p2pstreamv1.PublishPublicSiteRequest{Id: id}))
	if err != nil {
		t.Fatalf("publish Site: %v", err)
	}
	if !response.Msg.Site.Published {
		t.Fatal("Publish did not report a published Site")
	}
}

func siteWorkspaceResponse(t *testing.T, app *App, listenerID int64, host, path string, status int, location string) {
	t.Helper()
	request := httptest.NewRequest(http.MethodGet, "http://"+host+path, nil)
	recorder := httptest.NewRecorder()
	app.publicProxyHandler(listenerID)(recorder, request)
	if recorder.Code != status || recorder.Header().Get("Location") != location {
		t.Fatalf("listener=%d %s%s returned %d Location=%q body=%q; want %d Location=%q", listenerID, host, path, recorder.Code, recorder.Header().Get("Location"), recorder.Body.String(), status, location)
	}
}
