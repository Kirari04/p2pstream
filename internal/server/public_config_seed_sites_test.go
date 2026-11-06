package server

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

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

func TestFreshPublicProxySeedUsesGenericCertificate(t *testing.T) {
	ctx := context.Background()
	app := NewApp(&config.Config{CertsDir: t.TempDir()}, newServerTestDB(t))
	defer app.CloseObservabilityRecorder(ctx)
	if err := app.ensurePublicProxySeeded(ctx); err != nil {
		t.Fatal(err)
	}
	certs, err := app.DB.ListPublicTlsCertificates(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(certs) != 1 || certs[0].HostnamePattern != "localhost" || certs[0].Source != publicTLSCertificateSourceManual {
		t.Fatalf("fresh certificates = %+v", certs)
	}
	pair, err := tls.LoadX509KeyPair(certs[0].CertPath, certs[0].KeyPath)
	if err != nil {
		t.Fatal(err)
	}
	leaf, err := x509.ParseCertificate(pair.Certificate[0])
	if err != nil {
		t.Fatal(err)
	}
	assertGenericPublicCertificate(t, leaf)
	if !certs[0].IssuedAt.Valid || !certs[0].ExpiresAt.Valid {
		t.Fatal("seed did not persist certificate validity")
	}
}

func TestPublicProxySeedPreservesPersistedManualCertificates(t *testing.T) {
	for _, hostname := range []string{"p2pstream.local", "customer.example.com"} {
		t.Run(hostname, func(t *testing.T) {
			ctx := context.Background()
			app := NewApp(&config.Config{CertsDir: t.TempDir()}, newServerTestDB(t))
			defer app.CloseObservabilityRecorder(ctx)
			listener := seedServerHTTPSListener(t, app.DB)
			certPEM, keyPEM, _, err := generatePublicSelfSignedCertificatePEM(hostname, time.Hour)
			if err != nil {
				t.Fatal(err)
			}
			// Both the legacy seed and uploaded customer material are persisted
			// as manual mappings. Reseeding must preserve the row and both files.
			before, err := app.createUploadedPublicTLSCertificate(ctx, publicTLSCertificateMutationInput{
				ListenerID: listener.ID, HostnamePattern: hostname, Enabled: 1,
				Source: publicTLSCertificateSourceManual, Status: publicTLSCertificateStatusReady,
			}, certPEM, keyPEM)
			if err != nil {
				t.Fatal(err)
			}
			if err := app.ensurePublicProxySeeded(ctx); err != nil {
				t.Fatal(err)
			}
			after, err := app.DB.GetPublicTlsCertificate(ctx, before.ID)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(before, after) {
				t.Fatalf("persisted certificate mapping changed: before=%+v after=%+v", before, after)
			}
			for _, file := range []struct {
				path string
				want []byte
			}{{after.CertPath, certPEM}, {after.KeyPath, keyPEM}} {
				got, err := os.ReadFile(file.path)
				if err != nil {
					t.Fatal(err)
				}
				if !bytes.Equal(got, file.want) {
					t.Fatalf("persisted certificate material changed: %s", file.path)
				}
			}
			certs, err := app.DB.ListPublicTlsCertificates(ctx)
			if err != nil || len(certs) != 1 {
				t.Fatalf("seeding changed certificate count: %d, err=%v", len(certs), err)
			}
			// A persisted manual default has no automatic renewal schedule.
			if after.Source != publicTLSCertificateSourceManual || after.NextRenewalAt.Valid {
				t.Fatalf("unexpected renewal state: %+v", after)
			}
		})
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
