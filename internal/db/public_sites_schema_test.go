package db

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
)

func TestPublicSitesSchemaEnforcesOwnershipAndListenerCascade(t *testing.T) {
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
	site, err := database.CreatePublicSite(ctx, CreatePublicSiteParams{ListenerID: listener.ID, Name: "app", Enabled: 1})
	if err != nil {
		t.Fatalf("create site: %v", err)
	}
	if _, err := database.CreatePublicSiteHost(ctx, CreatePublicSiteHostParams{SiteID: site.ID, ListenerID: other.ID, HostnamePattern: "app.example.com", Role: "primary", Behavior: "serve"}); err == nil {
		t.Fatal("cross-listener host unexpectedly succeeded")
	}
	if _, err := database.CreatePublicSiteHost(ctx, CreatePublicSiteHostParams{SiteID: site.ID, ListenerID: listener.ID, HostnamePattern: "app.example.com", Role: "primary", Behavior: "redirect"}); err == nil {
		t.Fatal("redirecting primary unexpectedly succeeded")
	}
	if _, err := database.CreatePublicSiteHost(ctx, CreatePublicSiteHostParams{SiteID: site.ID, ListenerID: listener.ID, HostnamePattern: "app.example.com", Role: "primary", Behavior: "serve"}); err != nil {
		t.Fatalf("create primary: %v", err)
	}
	otherSite, err := database.CreatePublicSite(ctx, CreatePublicSiteParams{ListenerID: listener.ID, Name: "case-test", Enabled: 1})
	if err != nil { t.Fatalf("create case site: %v", err) }
	if _, err := database.CreatePublicSiteHost(ctx, CreatePublicSiteHostParams{SiteID: otherSite.ID, ListenerID: listener.ID, HostnamePattern: "App.Example.Com", Role: "primary", Behavior: "serve"}); err == nil { t.Fatal("non-normalized case or case-insensitive duplicate unexpectedly succeeded") }
	_, err = database.CreatePublicRoute(ctx, CreatePublicRouteParams{ListenerID: listener.ID, SiteID: sql.NullInt64{Int64: site.ID, Valid: true}, Priority: 1, HostPattern: "app.example.com", PathPrefix: "/", TargetLoadBalancing: "round_robin", IsDefault: 1, Action: "forward", RedirectStatusCode: 302, RedirectPreservePathSuffix: 1, RedirectPreserveQuery: 1, PathSecurityMode: "strict", Enabled: 1})
	if err != nil {
		t.Fatalf("create site route: %v", err)
	}
	if err := database.DeletePublicSite(ctx, site.ID); err == nil {
		t.Fatal("site with route unexpectedly deleted")
	}
	if err := database.DeletePublicListener(ctx, listener.ID); err != nil {
		t.Fatalf("listener cascade: %v", err)
	}
	if _, err := database.GetPublicSite(ctx, site.ID); err != sql.ErrNoRows {
		t.Fatalf("site after listener delete error = %v", err)
	}
}
