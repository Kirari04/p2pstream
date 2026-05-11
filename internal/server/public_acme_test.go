package server

import (
	"context"
	"crypto/tls"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	p2pstreamv1 "p2pstream/gen/proto/p2pstream/v1"
	"p2pstream/internal/config"
	"p2pstream/internal/db"
)

type fakeACMEIssuer struct {
	certPEM []byte
	keyPEM  []byte
	err     error
}

func (i fakeACMEIssuer) Issue(context.Context, publicACMEChallengeStore, publicACMEIssueConfig) (publicACMEIssueResult, error) {
	if i.err != nil {
		return publicACMEIssueResult{}, i.err
	}
	leaf, err := parseLeafCertificate(i.certPEM)
	if err != nil {
		return publicACMEIssueResult{}, err
	}
	return publicACMEIssueResult{CertPEM: i.certPEM, KeyPEM: i.keyPEM, Leaf: leaf}, nil
}

func TestPublicACMEManagerIssuesCertificateWithFakeIssuer(t *testing.T) {
	database := newServerTestDB(t)
	listener := seedServerHTTPSListener(t, database)
	certPEM, keyPEM, err := generateSelfSignedCertificatePEM(24 * time.Hour)
	if err != nil {
		t.Fatalf("generate cert: %v", err)
	}

	configDir := t.TempDir()
	app := NewApp(&config.Config{ConfigDir: configDir, CertsDir: filepath.Join(configDir, "certs")}, database)
	app.PublicACME.issuer = fakeACMEIssuer{certPEM: certPEM, keyPEM: keyPEM}
	cert, err := database.CreatePublicTlsCertificate(context.Background(), db.CreatePublicTlsCertificateParams{
		ListenerID:        listener.ID,
		HostnamePattern:   "acme.example.com",
		Enabled:           1,
		Source:            publicTLSCertificateSourceACME,
		AcmeChallengeType: publicACMEChallengeHTTP01,
		AcmeCa:            publicACMECAStaging,
		AcmeEmail:         "admin@example.com",
		Status:            publicTLSCertificateStatusPending,
	})
	if err != nil {
		t.Fatalf("create ACME cert: %v", err)
	}

	app.PublicACME.issueCertificate(context.Background(), cert.ID)

	updated, err := database.GetPublicTlsCertificate(context.Background(), cert.ID)
	if err != nil {
		t.Fatalf("get cert: %v", err)
	}
	if updated.Status != publicTLSCertificateStatusReady || updated.CertPath == "" || updated.KeyPath == "" || !updated.ExpiresAt.Valid {
		t.Fatalf("unexpected issued cert row: %+v", updated)
	}
	assertServerFileBytes(t, updated.CertPath, certPEM)
	assertServerFileBytes(t, updated.KeyPath, keyPEM)
}

func TestPublicACMEManagerFailureKeepsExistingCertificateFiles(t *testing.T) {
	database := newServerTestDB(t)
	listener := seedServerHTTPSListener(t, database)
	configDir := t.TempDir()
	app := NewApp(&config.Config{ConfigDir: configDir, CertsDir: filepath.Join(configDir, "certs")}, database)
	app.PublicACME.issuer = fakeACMEIssuer{err: errors.New("issuer failed")}
	cert, err := database.CreatePublicTlsCertificate(context.Background(), db.CreatePublicTlsCertificateParams{
		ListenerID:        listener.ID,
		HostnamePattern:   "acme.example.com",
		CertPath:          "/tmp/current.crt",
		KeyPath:           "/tmp/current.key",
		Enabled:           1,
		Source:            publicTLSCertificateSourceACME,
		AcmeChallengeType: publicACMEChallengeHTTP01,
		AcmeCa:            publicACMECAStaging,
		AcmeEmail:         "admin@example.com",
		Status:            publicTLSCertificateStatusReady,
	})
	if err != nil {
		t.Fatalf("create ACME cert: %v", err)
	}

	app.PublicACME.issueCertificate(context.Background(), cert.ID)

	updated, err := database.GetPublicTlsCertificate(context.Background(), cert.ID)
	if err != nil {
		t.Fatalf("get cert: %v", err)
	}
	if updated.Status != publicTLSCertificateStatusError || updated.CertPath != cert.CertPath || updated.KeyPath != cert.KeyPath || updated.LastError == "" {
		t.Fatalf("unexpected failed cert row: %+v", updated)
	}
}

func TestPublicACMEHTTPChallengeServedBeforeRouting(t *testing.T) {
	app := NewApp(&config.Config{}, newServerTestDB(t))
	cleanup := app.PublicACME.SetHTTPChallenge("/.well-known/acme-challenge/token", "response")
	defer cleanup()

	req := httptest.NewRequest(http.MethodGet, "/.well-known/acme-challenge/token", nil)
	rec := httptest.NewRecorder()
	if !app.PublicACME.ServeHTTPChallenge(rec, req) {
		t.Fatal("expected challenge to be handled")
	}
	if rec.Code != http.StatusOK || rec.Body.String() != "response" {
		t.Fatalf("unexpected challenge response: code=%d body=%q", rec.Code, rec.Body.String())
	}
}

func TestPublicTLSConfigSkipsMissingManagedCertificateAndServesALPNChallenge(t *testing.T) {
	app := NewApp(&config.Config{}, newServerTestDB(t))
	challengeCert, err := generateFallbackCertificate()
	if err != nil {
		t.Fatalf("challenge cert: %v", err)
	}
	cleanup := app.PublicACME.SetTLSALPNChallenge("acme.example.com", *challengeCert)
	defer cleanup()
	snap := &publicProxySnapshot{
		CertsByListener: map[int64][]publicTLSCertificateConfig{
			1: {{
				ID:              10,
				ListenerID:      1,
				HostnamePattern: "acme.example.com",
				Source:          publicTLSCertificateSourceACME,
				Enabled:         true,
			}},
		},
	}
	tlsConfig, err := newPublicTLSConfig(1, snap, app.PublicACME)
	if err != nil {
		t.Fatalf("newPublicTLSConfig() error = %v", err)
	}
	got, err := tlsConfig.GetCertificate(&tls.ClientHelloInfo{
		ServerName:      "acme.example.com",
		SupportedProtos: []string{"acme-tls/1"},
	})
	if err != nil {
		t.Fatalf("GetCertificate() error = %v", err)
	}
	if len(got.Certificate) == 0 || len(challengeCert.Certificate) == 0 || string(got.Certificate[0]) != string(challengeCert.Certificate[0]) {
		t.Fatal("expected TLS-ALPN challenge certificate")
	}
}

func TestPublicTLSCertificateValidationAllowsWildcardOnlyForDNS01(t *testing.T) {
	database := newServerTestDB(t)
	listener := seedServerHTTPSListener(t, database)
	credential, err := database.CreatePublicTlsDnsCredential(context.Background(), db.CreatePublicTlsDnsCredentialParams{
		Name:             "cf",
		Provider:         publicDNSProviderCloudflare,
		CloudflareZoneID: "zone",
		ApiToken:         "token",
		Enabled:          1,
	})
	if err != nil {
		t.Fatalf("create credential: %v", err)
	}
	app := NewApp(&config.Config{}, database)

	_, _, err = app.validatePublicTLSCertificateInput(
		context.Background(),
		listener.ID,
		"*.example.com",
		"",
		"",
		nil,
		nil,
		true,
		p2pstreamv1.PublicTlsCertificateSource_PUBLIC_TLS_CERTIFICATE_SOURCE_ACME,
		p2pstreamv1.PublicAcmeChallengeType_PUBLIC_ACME_CHALLENGE_TYPE_HTTP_01,
		p2pstreamv1.PublicAcmeCa_PUBLIC_ACME_CA_LETS_ENCRYPT_STAGING,
		"admin@example.com",
		0,
		nil,
		false,
	)
	if err == nil {
		t.Fatal("expected wildcard HTTP-01 validation to fail")
	}

	_, _, err = app.validatePublicTLSCertificateInput(
		context.Background(),
		listener.ID,
		"*.example.com",
		"",
		"",
		nil,
		nil,
		true,
		p2pstreamv1.PublicTlsCertificateSource_PUBLIC_TLS_CERTIFICATE_SOURCE_ACME,
		p2pstreamv1.PublicAcmeChallengeType_PUBLIC_ACME_CHALLENGE_TYPE_DNS_01,
		p2pstreamv1.PublicAcmeCa_PUBLIC_ACME_CA_LETS_ENCRYPT_STAGING,
		"admin@example.com",
		credential.ID,
		nil,
		false,
	)
	if err != nil {
		t.Fatalf("DNS-01 wildcard validation failed: %v", err)
	}
}

func newServerTestDB(t *testing.T) *db.DB {
	t.Helper()
	database, err := db.Open(filepath.Join(t.TempDir(), "server-test.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = database.Close() })
	return database
}

func seedServerHTTPSListener(t *testing.T, database *db.DB) db.PublicListener {
	t.Helper()
	backend, err := database.CreatePublicBackend(context.Background(), db.CreatePublicBackendParams{
		Name:             "default",
		TargetOrigin:     "https://example.com",
		BackendType:      publicBackendTypeProxyForward,
		ForwardMode:      publicBackendForwardModeDirect,
		LoadBalancing:    publicBackendLoadBalancingRoundRobin,
		StaticStatusCode: defaultStaticStatusCode,
		Enabled:          1,
	})
	if err != nil {
		t.Fatalf("create backend: %v", err)
	}
	listener, err := database.CreatePublicListener(context.Background(), db.CreatePublicListenerParams{
		Name:             "https",
		Port:             443,
		Protocol:         publicListenerProtocolHTTPS,
		Enabled:          1,
		DefaultBackendID: backend.ID,
	})
	if err != nil {
		t.Fatalf("create listener: %v", err)
	}
	return listener
}

func assertServerFileBytes(t *testing.T, path string, want []byte) {
	t.Helper()
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	if string(got) != string(want) {
		t.Fatalf("file %s contents mismatch", path)
	}
}
