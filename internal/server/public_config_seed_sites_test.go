package server

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"p2pstream/internal/config"
)

func TestFreshPublicProxySeedUsesPublishedDefaultSite(t *testing.T) {
	ctx := context.Background()
	certsDir := t.TempDir()
	app := NewApp(&config.Config{CertsDir: certsDir}, newServerTestDB(t))
	defer app.CloseObservabilityRecorder(ctx)
	if err := app.ensurePublicProxySeeded(ctx); err != nil {
		t.Fatal(err)
	}
	sites, err := app.DB.ListPublicSites(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(sites) != 1 || sites[0].Published == 0 || sites[0].DefaultSite == 0 {
		t.Fatalf("fresh Sites = %+v", sites)
	}
	bindings, err := app.DB.ListPublicSiteListenerBindingsBySite(ctx, sites[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(bindings) != 2 {
		t.Fatalf("fresh Site bindings = %+v", bindings)
	}
	routes, err := app.DB.ListPublicRoutes(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(routes) != 1 || !routes[0].SiteID.Valid || routes[0].SiteID.Int64 != sites[0].ID || routes[0].ListenerID != 0 {
		t.Fatalf("fresh routes = %+v", routes)
	}
	for _, binding := range bindings {
		migrated, err := app.DB.IsPublicListenerSiteMigrated(ctx, binding.ListenerID)
		if err != nil || migrated == 0 {
			t.Fatalf("listener %d migration marker=%d err=%v", binding.ListenerID, migrated, err)
		}
	}
}

func TestFreshPublicProxySeedRollsBackAndRetriesAfterMidSeedFailure(t *testing.T) {
	ctx := context.Background()
	certsDir := t.TempDir()
	app := NewApp(&config.Config{CertsDir: certsDir}, newServerTestDB(t))
	defer app.CloseObservabilityRecorder(ctx)

	injected := errors.New("injected seed failure")
	if err := app.ensurePublicProxySeededWithHook(ctx, func() error { return injected }); err == nil {
		t.Fatal("seed unexpectedly succeeded")
	}
	listeners, err := app.DB.ListPublicListeners(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(listeners) != 0 {
		t.Fatalf("listeners survived failed seed: %+v", listeners)
	}
	sites, err := app.DB.ListPublicSites(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(sites) != 0 {
		t.Fatalf("Sites survived failed seed: %+v", sites)
	}
	routes, err := app.DB.ListPublicRoutes(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(routes) != 0 {
		t.Fatalf("routes survived failed seed: %+v", routes)
	}
	certificates, err := app.DB.ListPublicTlsCertificates(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(certificates) != 0 {
		t.Fatalf("TLS certificates survived failed seed: %+v", certificates)
	}
	certificateFiles, err := filepath.Glob(filepath.Join(certsDir, "public-listener-*", "tls-*"))
	if err != nil {
		t.Fatal(err)
	}
	if len(certificateFiles) != 0 {
		t.Fatalf("certificate files survived failed seed: %v", certificateFiles)
	}

	if err := app.ensurePublicProxySeeded(ctx); err != nil {
		t.Fatalf("retry seed: %v", err)
	}
	listeners, err = app.DB.ListPublicListeners(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(listeners) != 2 {
		t.Fatalf("retry listeners = %+v", listeners)
	}
	sites, err = app.DB.ListPublicSites(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(sites) != 1 || sites[0].Published == 0 || sites[0].DefaultSite == 0 {
		t.Fatalf("retry Sites = %+v", sites)
	}
}
