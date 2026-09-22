package server

import (
	"context"
	"testing"

	"connectrpc.com/connect"

	p2pstreamv1 "p2pstream/gen/proto/p2pstream/v1"
	"p2pstream/internal/db"
)

func TestPublicListenerCreateAndDeleteRollbackWhenCandidateSnapshotIsInvalid(t *testing.T) {
	ctx := context.Background()
	app := NewApp(nil, newServerTestDB(t))
	defer app.CloseObservabilityRecorder(ctx)
	if _, err := app.DB.ExecContext(ctx, `
		INSERT INTO public_trusted_proxy_sources
		(name,provider,built_in,enabled,cidrs_json,header_name,header_mode)
		VALUES ('invalid-candidate','custom',0,1,'["not-a-cidr"]','X-Forwarded-For','single_ip')
	`); err != nil {
		t.Fatal(err)
	}

	create := connect.NewRequest(&p2pstreamv1.CreatePublicListenerRequest{
		Name: "candidate", BindAddress: "127.0.0.1", Port: 18880,
		Protocol: p2pstreamv1.PublicListenerProtocol_PUBLIC_LISTENER_PROTOCOL_HTTP,
	})
	if _, err := app.publicConfigService().createPublicListener(ctx, create); err == nil {
		t.Fatal("listener creation committed despite invalid candidate snapshot")
	}
	if count, err := app.DB.CountPublicListeners(ctx); err != nil || count != 0 {
		t.Fatalf("listener count after failed create = %d, err=%v", count, err)
	}

	listener, err := app.DB.CreatePublicListener(ctx, db.CreatePublicListenerParams{
		Name: "existing", BindAddress: "127.0.0.1", Port: 18881, Protocol: publicListenerProtocolHTTP,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := app.publicConfigService().deletePublicListener(ctx, connect.NewRequest(&p2pstreamv1.DeletePublicListenerRequest{Id: listener.ID})); err == nil {
		t.Fatal("listener deletion committed despite invalid candidate snapshot")
	}
	if _, err := app.DB.GetPublicListener(ctx, listener.ID); err != nil {
		t.Fatalf("failed delete did not preserve listener: %v", err)
	}
}

func TestPublicListenerDeleteRejectsLifecycleTransitions(t *testing.T) {
	ctx := context.Background()
	app := NewApp(nil, newServerTestDB(t))
	defer app.CloseObservabilityRecorder(ctx)
	listener, err := app.DB.CreatePublicListener(ctx, db.CreatePublicListenerParams{
		Name: "starting", BindAddress: "127.0.0.1", Port: 18882, Protocol: publicListenerProtocolHTTP,
	})
	if err != nil {
		t.Fatal(err)
	}
	app.proxyMu.Lock()
	app.publicListenerState[listener.ID] = &publicListenerRuntime{State: p2pstreamv1.ProxyState_PROXY_STATE_STARTING}
	app.proxyMu.Unlock()

	_, err = app.publicConfigService().deletePublicListener(ctx, connect.NewRequest(&p2pstreamv1.DeletePublicListenerRequest{Id: listener.ID}))
	if connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Fatalf("delete during STARTING code=%v err=%v", connect.CodeOf(err), err)
	}
	if _, err := app.DB.GetPublicListener(ctx, listener.ID); err != nil {
		t.Fatalf("delete during STARTING removed listener: %v", err)
	}
}

func TestPublicListenerProtocolUpdateRejectsBrokenPublishedRedirect(t *testing.T) {
	ctx := context.Background()
	app := NewApp(nil, newServerTestDB(t))
	defer app.CloseObservabilityRecorder(ctx)
	cookie := createTestAdminSession(t, app).Get("Cookie")
	source := siteWorkspaceListener(t, app, "redirect-source", 18883)
	destination, err := app.DB.CreatePublicListener(ctx, db.CreatePublicListenerParams{
		Name: "redirect-destination", BindAddress: "127.0.0.1", Port: 18884,
		Protocol: publicListenerProtocolHTTPS, Enabled: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	siteWorkspaceCertificate(t, app, destination.ID, "app.example.test")
	site := siteWorkspaceCreate(t, app, cookie, &p2pstreamv1.CreatePublicSiteRequest{
		Name: "redirected", Enabled: true,
		Hosts: []*p2pstreamv1.PublicSiteHostInput{{HostnamePattern: "app.example.test"}},
		ListenerBindings: []*p2pstreamv1.PublicSiteListenerBinding{
			{ListenerId: source.ID, Behavior: p2pstreamv1.PublicSiteListenerBehavior_PUBLIC_SITE_LISTENER_BEHAVIOR_REDIRECT_HTTPS, RedirectListenerId: destination.ID},
			{ListenerId: destination.ID, Behavior: p2pstreamv1.PublicSiteListenerBehavior_PUBLIC_SITE_LISTENER_BEHAVIOR_SERVE},
		},
	})
	siteWorkspaceRoute(t, app, cookie, site.Id, "", "/served", true)
	siteWorkspacePublish(t, app, cookie, site.Id)

	update := siteWorkspaceRequest(cookie, &p2pstreamv1.UpdatePublicListenerRequest{
		Id: destination.ID, Name: destination.Name, BindAddress: destination.BindAddress,
		Port: destination.Port, Protocol: p2pstreamv1.PublicListenerProtocol_PUBLIC_LISTENER_PROTOCOL_HTTP,
		Enabled: true,
	})
	if _, err := app.UpdatePublicListener(ctx, update); connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Fatalf("destination HTTPS-to-HTTP update code=%v err=%v", connect.CodeOf(err), err)
	}
	stored, err := app.DB.GetPublicListener(ctx, destination.ID)
	if err != nil || stored.Protocol != publicListenerProtocolHTTPS {
		t.Fatalf("stored destination protocol=%q err=%v", stored.Protocol, err)
	}
	snapshot := app.currentPublicSnapshot()
	if snapshot == nil || snapshot.Listeners[destination.ID].Protocol != publicListenerProtocolHTTPS {
		t.Fatalf("live destination changed after rejected update: %+v", snapshot)
	}
}
