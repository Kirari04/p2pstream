package server

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	p2pstreamv1 "p2pstream/gen/proto/p2pstream/v1"
	"p2pstream/internal/config"
	"p2pstream/internal/db"
)

func writePublicTLSFactsTestCert(t *testing.T, dir string, name string, hostname string) (string, []byte) {
	t.Helper()
	certPEM, _, _, err := generatePublicSelfSignedCertificatePEM(hostname, 24*time.Hour)
	if err != nil {
		t.Fatalf("generate certificate: %v", err)
	}
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, certPEM, 0o600); err != nil {
		t.Fatalf("write certificate: %v", err)
	}
	return path, certPEM
}

func TestPublicTLSLeafFactsFromStoredCertificate(t *testing.T) {
	cache := &publicTLSLeafFactsCache{}
	path, certPEM := writePublicTLSFactsTestCert(t, t.TempDir(), "app.crt.pem", "app.example.com")
	leaf, err := parseLeafCertificate(certPEM)
	if err != nil {
		t.Fatalf("parse certificate: %v", err)
	}
	digest := sha256.Sum256(leaf.Raw)

	facts := cache.lookup(path)
	if facts.State != p2pstreamv1.PublicTlsCertificateLeafState_PUBLIC_TLS_CERTIFICATE_LEAF_STATE_AVAILABLE {
		t.Fatalf("state = %v, want available", facts.State)
	}
	if facts.Issuer != leaf.Issuer.String() || !strings.Contains(facts.Issuer, "CN=app.example.com") {
		t.Fatalf("issuer = %q, want %q", facts.Issuer, leaf.Issuer.String())
	}
	if len(facts.DNSNames) != 1 || facts.DNSNames[0] != "app.example.com" || len(facts.IPAddresses) != 0 {
		t.Fatalf("names = %v / %v, want [app.example.com] / []", facts.DNSNames, facts.IPAddresses)
	}
	if facts.SHA256 != hex.EncodeToString(digest[:]) {
		t.Fatalf("sha256 = %q, want lowercase hex of the DER leaf", facts.SHA256)
	}
	if facts.SerialNumber != strings.ToUpper(leaf.SerialNumber.Text(16)) || facts.SerialNumber == "" {
		t.Fatalf("serial = %q, want %q", facts.SerialNumber, strings.ToUpper(leaf.SerialNumber.Text(16)))
	}

	// Returned slices are copies; callers cannot corrupt the cache.
	facts.DNSNames[0] = "mutated.example.com"
	if again := cache.lookup(path); again.DNSNames[0] != "app.example.com" {
		t.Fatalf("cache was mutated through a returned slice: %v", again.DNSNames)
	}
}

func TestPublicTLSLeafFactsReportsIPAddressSANs(t *testing.T) {
	cache := &publicTLSLeafFactsCache{}
	path, _ := writePublicTLSFactsTestCert(t, t.TempDir(), "ip.crt.pem", "192.0.2.10")
	facts := cache.lookup(path)
	if len(facts.IPAddresses) != 1 || facts.IPAddresses[0] != "192.0.2.10" || len(facts.DNSNames) != 0 {
		t.Fatalf("names = %v / %v, want [] / [192.0.2.10]", facts.DNSNames, facts.IPAddresses)
	}
}

func TestPublicTLSLeafFactsUnavailableStates(t *testing.T) {
	cache := &publicTLSLeafFactsCache{}
	dir := t.TempDir()
	_, keyPEM, err := generateECDSAPrivateKeyPEM()
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	garbagePath := filepath.Join(dir, "garbage.pem")
	if err := os.WriteFile(garbagePath, []byte("not a certificate"), 0o600); err != nil {
		t.Fatal(err)
	}
	keyOnlyPath := filepath.Join(dir, "key-only.pem")
	if err := os.WriteFile(keyOnlyPath, keyPEM, 0o600); err != nil {
		t.Fatal(err)
	}
	largePath := filepath.Join(dir, "large.pem")
	if err := os.WriteFile(largePath, make([]byte, publicTLSLeafFactsMaxFileBytes+1), 0o600); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name string
		path string
		want p2pstreamv1.PublicTlsCertificateLeafState
	}{
		{name: "no path", path: "  ", want: p2pstreamv1.PublicTlsCertificateLeafState_PUBLIC_TLS_CERTIFICATE_LEAF_STATE_MISSING},
		{name: "missing file", path: filepath.Join(dir, "missing.pem"), want: p2pstreamv1.PublicTlsCertificateLeafState_PUBLIC_TLS_CERTIFICATE_LEAF_STATE_MISSING},
		{name: "directory", path: dir, want: p2pstreamv1.PublicTlsCertificateLeafState_PUBLIC_TLS_CERTIFICATE_LEAF_STATE_UNREADABLE},
		{name: "oversized file", path: largePath, want: p2pstreamv1.PublicTlsCertificateLeafState_PUBLIC_TLS_CERTIFICATE_LEAF_STATE_UNREADABLE},
		{name: "not PEM", path: garbagePath, want: p2pstreamv1.PublicTlsCertificateLeafState_PUBLIC_TLS_CERTIFICATE_LEAF_STATE_INVALID},
		{name: "key only", path: keyOnlyPath, want: p2pstreamv1.PublicTlsCertificateLeafState_PUBLIC_TLS_CERTIFICATE_LEAF_STATE_INVALID},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			facts := cache.lookup(tt.path)
			if facts.State != tt.want {
				t.Fatalf("state = %v, want %v", facts.State, tt.want)
			}
			if facts.Issuer != "" || facts.SHA256 != "" || facts.SerialNumber != "" || len(facts.DNSNames) != 0 || len(facts.IPAddresses) != 0 {
				t.Fatalf("unavailable facts must be empty: %+v", facts)
			}
		})
	}
}

func TestPublicTLSLeafFactsSkipsLeadingPrivateKeyBlock(t *testing.T) {
	cache := &publicTLSLeafFactsCache{}
	certPEM, keyPEM, _, err := generatePublicSelfSignedCertificatePEM("combined.example.com", 24*time.Hour)
	if err != nil {
		t.Fatalf("generate certificate: %v", err)
	}
	path := filepath.Join(t.TempDir(), "combined.pem")
	if err := os.WriteFile(path, append(append([]byte{}, keyPEM...), certPEM...), 0o600); err != nil {
		t.Fatal(err)
	}
	facts := cache.lookup(path)
	if facts.State != p2pstreamv1.PublicTlsCertificateLeafState_PUBLIC_TLS_CERTIFICATE_LEAF_STATE_AVAILABLE || len(facts.DNSNames) != 1 || facts.DNSNames[0] != "combined.example.com" {
		t.Fatalf("facts = %+v, want the certificate after the key block", facts)
	}
}

func TestPublicTLSLeafFactsCacheTracksReplacedFiles(t *testing.T) {
	cache := &publicTLSLeafFactsCache{}
	dir := t.TempDir()
	path, _ := writePublicTLSFactsTestCert(t, dir, "rotating.pem", "first.example.com")
	first := cache.lookup(path)
	if len(first.DNSNames) != 1 || first.DNSNames[0] != "first.example.com" {
		t.Fatalf("first facts = %+v", first)
	}
	if cached := cache.entries[path]; cached.facts.SHA256 != first.SHA256 {
		t.Fatalf("facts were not cached for %s", path)
	}

	replacementPEM, _, _, err := generatePublicSelfSignedCertificatePEM("second.example.com", 24*time.Hour)
	if err != nil {
		t.Fatalf("generate replacement: %v", err)
	}
	if err := os.WriteFile(path, replacementPEM, 0o600); err != nil {
		t.Fatal(err)
	}
	// Force a distinct modification time even on coarse-grained filesystems.
	future := time.Now().Add(time.Minute)
	if err := os.Chtimes(path, future, future); err != nil {
		t.Fatal(err)
	}
	second := cache.lookup(path)
	if len(second.DNSNames) != 1 || second.DNSNames[0] != "second.example.com" || second.SHA256 == first.SHA256 {
		t.Fatalf("replacement facts = %+v, want second.example.com", second)
	}

	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if gone := cache.lookup(path); gone.State != p2pstreamv1.PublicTlsCertificateLeafState_PUBLIC_TLS_CERTIFICATE_LEAF_STATE_MISSING {
		t.Fatalf("removed file state = %v, want missing", gone.State)
	}
	if _, ok := cache.entries[path]; ok {
		t.Fatalf("removed file should be evicted from the cache")
	}
}

func TestPublicProxyConfigIncludesLeafFacts(t *testing.T) {
	database := newServerTestDB(t)
	listener := seedServerHTTPSListener(t, database)
	certPath, certPEM := writePublicTLSFactsTestCert(t, t.TempDir(), "facts.crt.pem", "facts.example.com")
	leaf, err := parseLeafCertificate(certPEM)
	if err != nil {
		t.Fatalf("parse certificate: %v", err)
	}
	manual, err := database.CreatePublicTlsCertificate(context.Background(), db.CreatePublicTlsCertificateParams{
		ListenerID:      listener.ID,
		HostnamePattern: "facts.example.com",
		CertPath:        certPath,
		KeyPath:         certPath + ".key",
		Enabled:         1,
		Source:          publicTLSCertificateSourceManual,
		Status:          publicTLSCertificateStatusReady,
	})
	if err != nil {
		t.Fatalf("create manual certificate row: %v", err)
	}
	pending, err := database.CreatePublicTlsCertificate(context.Background(), db.CreatePublicTlsCertificateParams{
		ListenerID:        listener.ID,
		HostnamePattern:   "pending.example.com",
		Enabled:           1,
		Source:            publicTLSCertificateSourceACME,
		AcmeChallengeType: publicACMEChallengeHTTP01,
		AcmeCa:            publicACMECAStaging,
		AcmeEmail:         "admin@example.com",
		Status:            publicTLSCertificateStatusPending,
	})
	if err != nil {
		t.Fatalf("create pending certificate row: %v", err)
	}

	app := NewApp(&config.Config{}, database)
	resp, err := app.publicProxyConfigResponse(context.Background())
	if err != nil {
		t.Fatalf("load public proxy config: %v", err)
	}
	byID := map[int64]*p2pstreamv1.PublicTlsCertificate{}
	for _, cert := range resp.GetTlsCertificates() {
		byID[cert.GetId()] = cert
	}

	got := byID[manual.ID]
	if got == nil {
		t.Fatalf("manual certificate missing from response")
	}
	digest := sha256.Sum256(leaf.Raw)
	if got.GetLeafState() != p2pstreamv1.PublicTlsCertificateLeafState_PUBLIC_TLS_CERTIFICATE_LEAF_STATE_AVAILABLE ||
		got.GetLeafIssuer() != leaf.Issuer.String() ||
		got.GetLeafSha256() != hex.EncodeToString(digest[:]) ||
		got.GetLeafSerialNumber() == "" ||
		len(got.GetLeafDnsNames()) != 1 || got.GetLeafDnsNames()[0] != "facts.example.com" {
		t.Fatalf("unexpected leaf facts: %+v", got)
	}

	gotPending := byID[pending.ID]
	if gotPending == nil {
		t.Fatalf("pending certificate missing from response")
	}
	if gotPending.GetLeafState() != p2pstreamv1.PublicTlsCertificateLeafState_PUBLIC_TLS_CERTIFICATE_LEAF_STATE_MISSING || gotPending.GetLeafSha256() != "" {
		t.Fatalf("pending ACME certificate should report missing facts: %+v", gotPending)
	}
}

func TestPublicACMERenewBeforeIsLifetimeRelative(t *testing.T) {
	notBefore := time.Date(2027, 3, 1, 12, 0, 0, 0, time.UTC)
	day := 24 * time.Hour
	tests := []struct {
		name     string
		notAfter time.Time
		start    time.Time
		want     time.Duration
	}{
		{name: "90-day Let's Encrypt inclusive end", start: notBefore, notAfter: notBefore.Add(90*day - time.Second), want: 30 * day},
		{name: "90-day exact", start: notBefore, notAfter: notBefore.Add(90 * day), want: 30 * day},
		{name: "1-year capped", start: notBefore, notAfter: notBefore.Add(365 * day), want: 30 * day},
		{name: "64-day", start: notBefore, notAfter: notBefore.Add(64*day - time.Second), want: 64 * day / 3},
		{name: "45-day", start: notBefore, notAfter: notBefore.Add(45*day - time.Second), want: 15 * day},
		{name: "6-day short-lived", start: notBefore, notAfter: notBefore.Add(6*day - time.Second), want: 2 * day},
		{name: "unknown not-before", start: time.Time{}, notAfter: notBefore.Add(6 * day), want: 30 * day},
		{name: "inverted validity", start: notBefore, notAfter: notBefore.Add(-time.Hour), want: 30 * day},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := publicACMERenewBefore(tt.start, tt.notAfter); got != tt.want {
				t.Fatalf("renew before = %s, want %s", got, tt.want)
			}
		})
	}
}

func readyACMECertificateForSchedule(issuedAt time.Time, expiresAt time.Time) db.PublicTlsCertificate {
	return db.PublicTlsCertificate{
		ID:              1,
		ListenerID:      2,
		HostnamePattern: "acme.example.com",
		CertPath:        "/tmp/acme.crt",
		KeyPath:         "/tmp/acme.key",
		Enabled:         1,
		Source:          publicTLSCertificateSourceACME,
		Status:          publicTLSCertificateStatusReady,
		IssuedAt:        sqlNullTime(issuedAt),
		ExpiresAt:       sqlNullTime(expiresAt),
	}
}

func TestPublicACMEScheduleDecisionUsesLifetimeRelativeWindow(t *testing.T) {
	day := 24 * time.Hour
	issued := time.Date(2027, 3, 1, 12, 0, 0, 0, time.UTC)

	tests := []struct {
		name        string
		lifetime    time.Duration
		renewBefore time.Duration
	}{
		{name: "90-day", lifetime: 90*day - time.Second, renewBefore: 30 * day},
		{name: "45-day", lifetime: 45*day - time.Second, renewBefore: 15 * day},
		{name: "6-day", lifetime: 6*day - time.Second, renewBefore: 2 * day},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			expires := issued.Add(tt.lifetime)
			cert := readyACMECertificateForSchedule(issued, expires)
			wantNext := expires.Add(-tt.renewBefore)

			// Right after issuance the certificate is not due.
			decision := publicACMECertificateScheduleDecision(cert, issued.Add(time.Hour))
			if decision.Due || decision.Reason != "scheduled" || !decision.NextAttemptAt.Time.Equal(wantNext) {
				t.Fatalf("decision after issuance = %+v, want scheduled at %s", decision, wantNext)
			}
			// Just before the window opens it is still not due.
			if decision := publicACMECertificateScheduleDecision(cert, wantNext.Add(-time.Minute)); decision.Due {
				t.Fatalf("decision before window = %+v, want not due", decision)
			}
			// Inside the window it is due.
			decision = publicACMECertificateScheduleDecision(cert, wantNext.Add(time.Minute))
			if !decision.Due || decision.Reason != "renewal_window" {
				t.Fatalf("decision inside window = %+v, want renewal_window", decision)
			}
			if got := publicACMEEffectiveNextRenewalAt(cert); !got.Valid || !got.Time.Equal(wantNext) {
				t.Fatalf("effective next renewal = %+v, want %s", got, wantNext)
			}
		})
	}
}

func TestPublicACMEEffectiveNextRenewalAtKeepsRetryAndPendingTimes(t *testing.T) {
	day := 24 * time.Hour
	issued := time.Date(2027, 3, 1, 12, 0, 0, 0, time.UTC)
	cert := readyACMECertificateForSchedule(issued, issued.Add(45*day))
	// A row persisted under the old fixed 30-day window reports the current schedule.
	cert.NextRenewalAt = sqlNullTime(issued.Add(15 * day))
	if got := publicACMEEffectiveNextRenewalAt(cert); !got.Time.Equal(issued.Add(30 * day)) {
		t.Fatalf("ready effective next renewal = %s, want %s", got.Time, issued.Add(30*day))
	}

	retryAt := issued.Add(2 * time.Hour)
	cert.Status = publicTLSCertificateStatusError
	cert.NextRenewalAt = sqlNullTime(retryAt)
	if got := publicACMEEffectiveNextRenewalAt(cert); !got.Time.Equal(retryAt) {
		t.Fatalf("error effective next renewal = %s, want stored retry %s", got.Time, retryAt)
	}

	manual := cert
	manual.Source = publicTLSCertificateSourceManual
	manual.Status = publicTLSCertificateStatusReady
	manual.NextRenewalAt = sql.NullTime{}
	if got := publicACMEEffectiveNextRenewalAt(manual); got.Valid {
		t.Fatalf("manual certificate should not get a renewal time: %+v", got)
	}
}

func TestPublicACMEManagerSchedulesShortLivedCertificateRenewal(t *testing.T) {
	database := newServerTestDB(t)
	listener := seedServerHTTPSListener(t, database)
	certPEM, keyPEM, err := generateSelfSignedCertificatePEM(6 * 24 * time.Hour)
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
		HostnamePattern:   "short.example.com",
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
	captureACMELogs(t, func() {
		app.PublicACME.issueCertificate(context.Background(), cert.ID, publicACMETriggerManual)
	})

	updated, err := database.GetPublicTlsCertificate(context.Background(), cert.ID)
	if err != nil {
		t.Fatalf("get cert: %v", err)
	}
	wantNext := leaf.NotAfter.UTC().Add(-publicACMERenewBefore(leaf.NotBefore, leaf.NotAfter))
	if !updated.NextRenewalAt.Valid || !updated.NextRenewalAt.Time.Equal(wantNext) {
		t.Fatalf("next renewal = %+v, want %s", updated.NextRenewalAt, wantNext)
	}
	if remaining := leaf.NotAfter.Sub(updated.NextRenewalAt.Time); remaining < 47*time.Hour || remaining > 49*time.Hour {
		t.Fatalf("6-day certificate renews %s before expiry, want about two days", remaining)
	}
	if publicACMECertificateDue(updated, time.Now()) {
		t.Fatalf("freshly issued 6-day certificate must not be due again immediately")
	}

	facts := publicTLSCertificateToProto(updated)
	if facts.GetLeafState() != p2pstreamv1.PublicTlsCertificateLeafState_PUBLIC_TLS_CERTIFICATE_LEAF_STATE_AVAILABLE || facts.GetLeafSha256() == "" {
		t.Fatalf("issued ACME certificate should expose leaf facts: %+v", facts)
	}
	if strings.Contains(facts.String(), "PRIVATE KEY") {
		t.Fatalf("leaf facts must never include key material")
	}
}
