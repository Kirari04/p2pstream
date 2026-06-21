package server

import (
	"bytes"
	"context"
	"crypto/tls"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"

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
	certPEM, keyPEM, err := generateSelfSignedCertificatePEM(90 * 24 * time.Hour)
	if err != nil {
		t.Fatalf("generate cert: %v", err)
	}
	leaf, err := parseLeafCertificate(certPEM)
	if err != nil {
		t.Fatalf("parse generated cert: %v", err)
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

	logs := captureACMELogs(t, func() {
		app.PublicACME.issueCertificate(context.Background(), cert.ID, publicACMETriggerManual)
	})

	updated, err := database.GetPublicTlsCertificate(context.Background(), cert.ID)
	if err != nil {
		t.Fatalf("get cert: %v", err)
	}
	if updated.Status != publicTLSCertificateStatusReady || updated.CertPath == "" || updated.KeyPath == "" || !updated.ExpiresAt.Valid {
		t.Fatalf("unexpected issued cert row: %+v", updated)
	}
	if !updated.NextRenewalAt.Valid || !updated.NextRenewalAt.Time.Equal(leaf.NotAfter.UTC().Add(-publicACMERenewBefore)) {
		t.Fatalf("next renewal = %+v, want %s", updated.NextRenewalAt, leaf.NotAfter.UTC().Add(-publicACMERenewBefore))
	}
	assertServerFileBytes(t, updated.CertPath, certPEM)
	assertServerFileBytes(t, updated.KeyPath, keyPEM)

	entry := findACMELogEntry(t, logs, "ACME certificate renewal succeeded")
	if entry["component"] != publicACMELogComponent ||
		entry["stage"] != publicACMEStageRecordSuccess ||
		entry["trigger"] != publicACMETriggerManual ||
		entry["hostname"] != "acme.example.com" {
		t.Fatalf("unexpected success log entry: %+v", entry)
	}
	if _, ok := entry["next_renewal_at"]; !ok {
		t.Fatalf("success log missing next_renewal_at: %+v", entry)
	}
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

	logs := captureACMELogs(t, func() {
		app.PublicACME.issueCertificate(context.Background(), cert.ID, publicACMETriggerScheduledScan)
	})

	updated, err := database.GetPublicTlsCertificate(context.Background(), cert.ID)
	if err != nil {
		t.Fatalf("get cert: %v", err)
	}
	if updated.Status != publicTLSCertificateStatusError || updated.CertPath != cert.CertPath || updated.KeyPath != cert.KeyPath || updated.LastError == "" {
		t.Fatalf("unexpected failed cert row: %+v", updated)
	}
	if !strings.Contains(updated.LastError, publicACMEStageIssueCertificate+": issuer failed") {
		t.Fatalf("last error = %q, want staged issuer failure", updated.LastError)
	}
	if !updated.NextRenewalAt.Valid || !updated.LastRenewalAttemptAt.Valid || !updated.NextRenewalAt.Time.After(updated.LastRenewalAttemptAt.Time) {
		t.Fatalf("expected retry schedule after failed attempt: %+v", updated)
	}

	entry := findACMELogEntry(t, logs, "ACME certificate renewal failed")
	if entry["component"] != publicACMELogComponent ||
		entry["stage"] != publicACMEStageIssueCertificate ||
		entry["trigger"] != publicACMETriggerScheduledScan ||
		entry["hostname"] != "acme.example.com" {
		t.Fatalf("unexpected failure log entry: %+v", entry)
	}
	if _, ok := entry["retry_at"]; !ok {
		t.Fatalf("failure log missing retry_at: %+v", entry)
	}
}

func TestPublicACMECertificateScheduleDecisionUsesRetryTime(t *testing.T) {
	now := time.Date(2026, 6, 20, 10, 0, 0, 0, time.UTC)
	cert := db.PublicTlsCertificate{
		ID:              1,
		ListenerID:      2,
		HostnamePattern: "acme.example.com",
		Enabled:         1,
		Source:          publicTLSCertificateSourceACME,
		Status:          publicTLSCertificateStatusError,
		NextRenewalAt:   sqlNullTime(now.Add(30 * time.Minute)),
	}

	decision := publicACMECertificateScheduleDecision(cert, now)
	if decision.Due || decision.Reason != "waiting_for_retry" || !decision.NextAttemptAt.Valid || !decision.NextAttemptAt.Time.Equal(now.Add(30*time.Minute)) {
		t.Fatalf("decision before retry = %+v", decision)
	}

	cert.NextRenewalAt = sqlNullTime(now.Add(-time.Minute))
	decision = publicACMECertificateScheduleDecision(cert, now)
	if !decision.Due || decision.Reason != "retry_due" {
		t.Fatalf("decision after retry = %+v", decision)
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
		false,
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
		false,
		0,
		nil,
		false,
	)
	if err != nil {
		t.Fatalf("DNS-01 wildcard validation failed: %v", err)
	}
}

func TestPublicSelfSignedCertificateGenerationUsesHostPatternSAN(t *testing.T) {
	_, _, wildcardLeaf, err := generatePublicSelfSignedCertificatePEM("*.Example.COM", 24*time.Hour)
	if err != nil {
		t.Fatalf("generate wildcard certificate: %v", err)
	}
	if err := wildcardLeaf.VerifyHostname("app.example.com"); err != nil {
		t.Fatalf("wildcard generated certificate did not verify for app.example.com: %v", err)
	}

	if err := validateHostPattern("127.0.0.1"); err != nil {
		t.Fatalf("expected IPv4 host pattern to be valid: %v", err)
	}
	_, _, ipLeaf, err := generatePublicSelfSignedCertificatePEM("127.0.0.1", 24*time.Hour)
	if err != nil {
		t.Fatalf("generate IPv4 certificate: %v", err)
	}
	if err := ipLeaf.VerifyHostname("127.0.0.1"); err != nil {
		t.Fatalf("IPv4 generated certificate did not verify for 127.0.0.1: %v", err)
	}
}

func TestPublicTLSCertificateValidationRejectsInvalidSelfSignedGeneration(t *testing.T) {
	database := newServerTestDB(t)
	listener := seedServerHTTPSListener(t, database)
	app := NewApp(&config.Config{}, database)
	certPEM, keyPEM, err := generateSelfSignedCertificatePEM(time.Hour)
	if err != nil {
		t.Fatalf("generate uploaded certificate: %v", err)
	}

	tests := []struct {
		name         string
		source       p2pstreamv1.PublicTlsCertificateSource
		certPath     string
		keyPath      string
		certPEM      []byte
		keyPEM       []byte
		generate     bool
		validityDays int64
	}{
		{
			name:         "ACME source",
			source:       p2pstreamv1.PublicTlsCertificateSource_PUBLIC_TLS_CERTIFICATE_SOURCE_ACME,
			generate:     true,
			validityDays: 30,
		},
		{
			name:     "zero validity",
			generate: true,
		},
		{
			name:         "too much validity",
			generate:     true,
			validityDays: maxPublicSelfSignedValidityDays + 1,
		},
		{
			name:         "mixed upload",
			certPEM:      certPEM,
			keyPEM:       keyPEM,
			generate:     true,
			validityDays: 30,
		},
		{
			name:         "mixed paths",
			certPath:     "/tmp/server.crt.pem",
			keyPath:      "/tmp/server.key.pem",
			generate:     true,
			validityDays: 30,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, _, err := app.validatePublicTLSCertificateInput(
				context.Background(),
				listener.ID,
				"generated.example.com",
				tt.certPath,
				tt.keyPath,
				tt.certPEM,
				tt.keyPEM,
				true,
				tt.source,
				p2pstreamv1.PublicAcmeChallengeType_PUBLIC_ACME_CHALLENGE_TYPE_HTTP_01,
				p2pstreamv1.PublicAcmeCa_PUBLIC_ACME_CA_LETS_ENCRYPT_STAGING,
				"admin@example.com",
				0,
				tt.generate,
				tt.validityDays,
				nil,
				false,
			)
			if err == nil {
				t.Fatal("expected validation error")
			}
		})
	}
}

func TestPublicProxyConfigBackfillsLegacyCertificateValidityFromFile(t *testing.T) {
	database := newServerTestDB(t)
	listener := seedServerHTTPSListener(t, database)
	certPEM, keyPEM, leaf, err := generatePublicSelfSignedCertificatePEM("legacy.example.com", 24*time.Hour)
	if err != nil {
		t.Fatalf("generate legacy certificate: %v", err)
	}
	certPath := filepath.Join(t.TempDir(), "legacy.crt.pem")
	keyPath := filepath.Join(t.TempDir(), "legacy.key.pem")
	if err := os.WriteFile(certPath, certPEM, 0600); err != nil {
		t.Fatalf("write legacy certificate: %v", err)
	}
	if err := os.WriteFile(keyPath, keyPEM, 0600); err != nil {
		t.Fatalf("write legacy key: %v", err)
	}
	row, err := database.CreatePublicTlsCertificate(context.Background(), db.CreatePublicTlsCertificateParams{
		ListenerID:      listener.ID,
		HostnamePattern: "legacy.example.com",
		CertPath:        certPath,
		KeyPath:         keyPath,
		Enabled:         1,
		Source:          publicTLSCertificateSourceManual,
		Status:          publicTLSCertificateStatusReady,
	})
	if err != nil {
		t.Fatalf("create legacy certificate row: %v", err)
	}

	app := NewApp(&config.Config{}, database)
	resp, err := app.publicProxyConfigResponse(context.Background())
	if err != nil {
		t.Fatalf("load public proxy config: %v", err)
	}
	var gotIssued, gotExpires int64
	for _, cert := range resp.GetTlsCertificates() {
		if cert.GetId() == row.ID {
			gotIssued = cert.GetIssuedAtUnixMillis()
			gotExpires = cert.GetExpiresAtUnixMillis()
			break
		}
	}
	if gotIssued == 0 || gotExpires == 0 {
		t.Fatalf("expected legacy certificate validity from file, got issued=%d expires=%d", gotIssued, gotExpires)
	}
	if gotIssued != leaf.NotBefore.UnixMilli() || gotExpires != leaf.NotAfter.UnixMilli() {
		t.Fatalf("validity = %d/%d, want %d/%d", gotIssued, gotExpires, leaf.NotBefore.UnixMilli(), leaf.NotAfter.UnixMilli())
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
	listener, err := database.CreatePublicListener(context.Background(), db.CreatePublicListenerParams{
		Name:     "https",
		Port:     443,
		Protocol: publicListenerProtocolHTTPS,
		Enabled:  1,
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

func sqlNullTime(value time.Time) sql.NullTime {
	return sql.NullTime{Time: value, Valid: true}
}

func captureACMELogs(t *testing.T, fn func()) []map[string]any {
	t.Helper()
	var buf bytes.Buffer
	previousLogger := log.Logger
	previousLevel := zerolog.GlobalLevel()
	log.Logger = zerolog.New(&buf)
	zerolog.SetGlobalLevel(zerolog.DebugLevel)
	t.Cleanup(func() {
		log.Logger = previousLogger
		zerolog.SetGlobalLevel(previousLevel)
	})

	fn()

	rawLines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	entries := make([]map[string]any, 0, len(rawLines))
	for _, line := range rawLines {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var entry map[string]any
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			t.Fatalf("parse log line %q: %v", line, err)
		}
		entries = append(entries, entry)
	}
	return entries
}

func findACMELogEntry(t *testing.T, entries []map[string]any, message string) map[string]any {
	t.Helper()
	for _, entry := range entries {
		if entry["message"] == message {
			return entry
		}
	}
	t.Fatalf("missing log entry %q in %+v", message, entries)
	return nil
}
