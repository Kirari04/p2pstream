package db

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
)

func TestPublicSitesSchemaEnforcesOwnershipAndPreservesSharedSites(t *testing.T) {
	database, err := Open(filepath.Join(t.TempDir(), "sites.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer database.Close()
	ctx := context.Background()
	listener, err := database.CreatePublicListener(ctx, CreatePublicListenerParams{Name: "https", Port: 443, Protocol: "https", Enabled: 1})
	if err != nil {
		t.Fatalf("create listener: %v", err)
	}
	other, err := database.CreatePublicListener(ctx, CreatePublicListenerParams{Name: "other", Port: 444, Protocol: "https", Enabled: 1})
	if err != nil {
		t.Fatalf("create other listener: %v", err)
	}
	site, err := database.CreatePublicSite(ctx, CreatePublicSiteParams{Name: "app", Enabled: 1, Published: 1})
	if err != nil {
		t.Fatalf("create site: %v", err)
	}
	for _, listenerID := range []int64{listener.ID, other.ID} {
		if _, err := database.CreatePublicSiteListenerBinding(ctx, CreatePublicSiteListenerBindingParams{SiteID: site.ID, ListenerID: listenerID, Behavior: "serve"}); err != nil {
			t.Fatalf("assign listener %d: %v", listenerID, err)
		}
	}
	if _, err := database.CreatePublicSiteListenerBinding(ctx, CreatePublicSiteListenerBindingParams{SiteID: site.ID, ListenerID: other.ID, Behavior: "serve"}); err == nil {
		t.Fatal("duplicate listener binding unexpectedly succeeded")
	}
	if _, err := database.CreatePublicSiteHost(ctx, CreatePublicSiteHostParams{SiteID: site.ID, HostnamePattern: "app.example.com", Role: "primary", Behavior: "redirect"}); err == nil {
		t.Fatal("redirecting primary unexpectedly succeeded")
	}
	if _, err := database.CreatePublicSiteHost(ctx, CreatePublicSiteHostParams{SiteID: site.ID, HostnamePattern: "app.example.com", Role: "primary", Behavior: "serve"}); err != nil {
		t.Fatalf("create primary: %v", err)
	}
	if _, err := database.CreatePublicSiteHost(ctx, CreatePublicSiteHostParams{SiteID: site.ID, HostnamePattern: "App.Example.Com", Role: "alias", Behavior: "serve"}); err == nil {
		t.Fatal("non-normalized hostname unexpectedly succeeded")
	}
	if _, err := database.CreatePublicSiteHost(ctx, CreatePublicSiteHostParams{SiteID: site.ID, HostnamePattern: "app.example.com", Role: "alias", Behavior: "serve"}); err == nil {
		t.Fatal("duplicate hostname within Site unexpectedly succeeded")
	}
	// A draft may propose the same hostname; publication owns collision checks.
	draft, err := database.CreatePublicSite(ctx, CreatePublicSiteParams{Name: "draft", Enabled: 1})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.CreatePublicSiteHost(ctx, CreatePublicSiteHostParams{SiteID: draft.ID, HostnamePattern: "app.example.com", Role: "alias", Behavior: "serve"}); err != nil {
		t.Fatalf("draft cannot propose existing hostname: %v", err)
	}
	params := CreatePublicRouteParams{ListenerID: int64(0), SiteID: sql.NullInt64{Int64: site.ID, Valid: true}, Priority: 1, PathPrefix: "/", TargetLoadBalancing: "round_robin", IsDefault: 1, Action: "forward", RedirectStatusCode: 302, RedirectPreservePathSuffix: 1, RedirectPreserveQuery: 1, PathSecurityMode: "strict", Enabled: 1}
	route, err := database.CreatePublicRoute(ctx, params)
	if err != nil {
		t.Fatalf("create site route: %v", err)
	}
	params.IsDefault = 0
	params.ListenerID = listener.ID
	if _, err := database.CreatePublicRoute(ctx, params); err == nil {
		t.Fatal("route cannot belong both to a Site and a standalone listener")
	}
	if err := database.MarkPublicListenerSiteMigrated(ctx, listener.ID); err != nil {
		t.Fatalf("mark listener migrated: %v", err)
	}
	params.SiteID = sql.NullInt64{}
	if _, err := database.CreatePublicRoute(ctx, params); err == nil {
		t.Fatal("storage accepted a new standalone route after listener cutover")
	}
	if _, err := database.ExecContext(ctx, `UPDATE public_routes SET site_id = NULL, listener_id = ? WHERE id = ?`, listener.ID, route.ID); err == nil {
		t.Fatal("storage detached a Site route to a migrated listener")
	}
	params.ListenerID = other.ID
	if _, err := database.CreatePublicRoute(ctx, params); err != nil {
		t.Fatalf("create route on an unmigrated listener: %v", err)
	}
	if err := database.MarkPublicListenerSiteMigrated(ctx, other.ID); err == nil {
		t.Fatal("storage marked a listener migrated while standalone routes remained")
	}
	if _, err := database.ExecContext(ctx, `UPDATE public_site_migrated_listeners SET listener_id = ? WHERE listener_id = ?`, other.ID, listener.ID); err == nil {
		t.Fatal("storage moved a migration marker onto a listener with standalone routes")
	}
	if err := database.DeletePublicSite(ctx, site.ID); err == nil {
		t.Fatal("site with route unexpectedly deleted")
	}
	if err := database.DeletePublicListener(ctx, listener.ID); err != nil {
		t.Fatalf("listener cascade: %v", err)
	}
	if _, err := database.GetPublicSite(ctx, site.ID); err != nil {
		t.Fatalf("shared Site removed with listener: %v", err)
	}
	if _, err := database.GetPublicRoute(ctx, route.ID); err != nil {
		t.Fatalf("shared route removed with listener: %v", err)
	}
	bindings, err := database.ListPublicSiteListenerBindingsBySite(ctx, site.ID)
	if err != nil || len(bindings) != 1 || bindings[0].ListenerID != other.ID {
		t.Fatalf("remaining bindings = %+v err=%v", bindings, err)
	}
	if err := database.DeletePublicListener(ctx, other.ID); err != nil {
		t.Fatalf("delete last listener: %v", err)
	}
	if _, err := database.GetPublicSite(ctx, site.ID); err != nil {
		t.Fatalf("unassigned Site removed: %v", err)
	}
	if _, err := database.GetPublicRoute(ctx, route.ID); err != nil {
		t.Fatalf("unassigned Site route removed: %v", err)
	}
	assertForeignKeyCheck(t, database)
}
