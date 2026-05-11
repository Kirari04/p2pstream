package server

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"database/sql"
	"encoding/pem"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/rs/zerolog/log"
	"golang.org/x/crypto/acme"

	"p2pstream/internal/config"
	"p2pstream/internal/db"
)

const (
	publicACMEProductionDirectory = acme.LetsEncryptURL
	publicACMEStagingDirectory    = "https://acme-staging-v02.api.letsencrypt.org/directory"
	publicACMERenewBefore         = 30 * 24 * time.Hour
	publicACMERetryAfterFailure   = time.Hour
	publicACMESchedulerInterval   = 30 * time.Minute
)

type publicACMEManager struct {
	app    *App
	issuer publicACMEIssuer

	mu             sync.RWMutex
	started        bool
	inFlight       map[int64]struct{}
	httpChallenges map[string]string
	tlsChallenges  map[string]*tls.Certificate
}

type publicACMEIssueConfig struct {
	CertificateID int64
	ListenerID    int64
	Domain        string
	ChallengeType string
	CA            string
	Email         string
	DNSCredential *db.PublicTlsDnsCredential
}

type publicACMEIssueResult struct {
	CertPEM []byte
	KeyPEM  []byte
	Leaf    *x509.Certificate
}

type publicACMEIssuer interface {
	Issue(ctx context.Context, store publicACMEChallengeStore, cfg publicACMEIssueConfig) (publicACMEIssueResult, error)
}

type publicACMEChallengeStore interface {
	SetHTTPChallenge(path string, response string) func()
	SetTLSALPNChallenge(hostname string, cert tls.Certificate) func()
}

func newPublicACMEManager(app *App) *publicACMEManager {
	if app == nil || app.DB == nil {
		return nil
	}
	return &publicACMEManager{
		app:            app,
		issuer:         publicRealACMEIssuer{cfg: app.Config},
		inFlight:       make(map[int64]struct{}),
		httpChallenges: make(map[string]string),
		tlsChallenges:  make(map[string]*tls.Certificate),
	}
}

func (m *publicACMEManager) Start(ctx context.Context) {
	if m == nil {
		return
	}
	m.mu.Lock()
	if m.started {
		m.mu.Unlock()
		return
	}
	m.started = true
	m.mu.Unlock()

	go func() {
		m.queueDueCertificates(ctx)
		ticker := time.NewTicker(publicACMESchedulerInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				m.queueDueCertificates(ctx)
			}
		}
	}()
}

func (m *publicACMEManager) QueueIssue(certID int64) {
	if m == nil || certID <= 0 {
		return
	}
	go m.issueCertificate(context.Background(), certID)
}

func (m *publicACMEManager) ServeHTTPChallenge(w http.ResponseWriter, r *http.Request) bool {
	if m == nil || r == nil || !strings.HasPrefix(r.URL.Path, "/.well-known/acme-challenge/") {
		return false
	}
	m.mu.RLock()
	response, ok := m.httpChallenges[r.URL.Path]
	m.mu.RUnlock()
	if !ok {
		return false
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(response))
	return true
}

func (m *publicACMEManager) TLSALPNCertificate(hostname string) *tls.Certificate {
	if m == nil {
		return nil
	}
	hostname = normalizeHostPattern(hostname)
	m.mu.RLock()
	cert := m.tlsChallenges[hostname]
	m.mu.RUnlock()
	return cert
}

func (m *publicACMEManager) SetHTTPChallenge(path string, response string) func() {
	m.mu.Lock()
	m.httpChallenges[path] = response
	m.mu.Unlock()
	return func() {
		m.mu.Lock()
		delete(m.httpChallenges, path)
		m.mu.Unlock()
	}
}

func (m *publicACMEManager) SetTLSALPNChallenge(hostname string, cert tls.Certificate) func() {
	hostname = normalizeHostPattern(hostname)
	m.mu.Lock()
	m.tlsChallenges[hostname] = &cert
	m.mu.Unlock()
	return func() {
		m.mu.Lock()
		delete(m.tlsChallenges, hostname)
		m.mu.Unlock()
	}
}

func (m *publicACMEManager) queueDueCertificates(ctx context.Context) {
	if m == nil || m.app == nil || m.app.DB == nil {
		return
	}
	certs, err := m.app.DB.ListPublicTlsCertificates(ctx)
	if err != nil {
		log.Warn().Err(err).Msg("Failed to list ACME certificates")
		return
	}
	now := time.Now().UTC()
	for _, cert := range certs {
		if !publicACMECertificateDue(cert, now) {
			continue
		}
		go m.issueCertificate(ctx, cert.ID)
	}
}

func publicACMECertificateDue(cert db.PublicTlsCertificate, now time.Time) bool {
	if normalizePublicTLSCertificateSource(cert.Source) != publicTLSCertificateSourceACME || cert.Enabled == 0 {
		return false
	}
	if cert.LastRenewalAttemptAt.Valid && normalizePublicTLSCertificateStatus(cert.Status) == publicTLSCertificateStatusError &&
		now.Sub(cert.LastRenewalAttemptAt.Time) < publicACMERetryAfterFailure {
		return false
	}
	if cert.CertPath == "" || cert.KeyPath == "" || !cert.ExpiresAt.Valid {
		return true
	}
	return !cert.ExpiresAt.Time.After(now.Add(publicACMERenewBefore))
}

func (m *publicACMEManager) issueCertificate(ctx context.Context, certID int64) {
	if !m.beginIssue(certID) {
		return
	}
	defer m.endIssue(certID)

	cert, err := m.app.DB.GetPublicTlsCertificate(ctx, certID)
	if err != nil {
		log.Warn().Err(err).Int64("cert_id", certID).Msg("Failed to load ACME certificate")
		return
	}
	if normalizePublicTLSCertificateSource(cert.Source) != publicTLSCertificateSourceACME || cert.Enabled == 0 {
		return
	}
	now := time.Now().UTC()
	cert, err = m.app.DB.UpdatePublicTlsCertificateStatus(ctx, db.UpdatePublicTlsCertificateStatusParams{
		ID:                   cert.ID,
		Status:               publicTLSCertificateStatusRenewing,
		LastError:            "",
		LastRenewalAttemptAt: sql.NullTime{Time: now, Valid: true},
	})
	if err != nil {
		log.Warn().Err(err).Int64("cert_id", certID).Msg("Failed to mark ACME certificate renewing")
		return
	}

	issueCfg, err := m.issueConfig(ctx, cert)
	if err != nil {
		m.markIssueFailed(ctx, cert, err)
		return
	}
	result, err := m.issuer.Issue(ctx, m, issueCfg)
	if err != nil {
		m.markIssueFailed(ctx, cert, err)
		return
	}
	if result.Leaf == nil {
		result.Leaf, err = parseLeafCertificate(result.CertPEM)
		if err != nil {
			m.markIssueFailed(ctx, cert, err)
			return
		}
	}

	certPath, keyPath, err := m.config().WritePublicTLSCertificateFiles(cert.ListenerID, cert.ID, result.CertPEM, result.KeyPEM)
	if err != nil {
		m.markIssueFailed(ctx, cert, err)
		return
	}
	expiresAt := result.Leaf.NotAfter.UTC()
	issuedAt := now
	if !result.Leaf.NotBefore.IsZero() {
		issuedAt = result.Leaf.NotBefore.UTC()
	}
	nextRenewalAt := expiresAt.Add(-publicACMERenewBefore)
	if nextRenewalAt.Before(now) {
		nextRenewalAt = now.Add(publicACMERetryAfterFailure)
	}
	updated, err := m.app.DB.UpdatePublicTlsCertificateIssueState(ctx, db.UpdatePublicTlsCertificateIssueStateParams{
		ID:                   cert.ID,
		CertPath:             certPath,
		KeyPath:              keyPath,
		Status:               publicTLSCertificateStatusReady,
		LastError:            "",
		IssuedAt:             sql.NullTime{Time: issuedAt, Valid: true},
		ExpiresAt:            sql.NullTime{Time: expiresAt, Valid: true},
		NextRenewalAt:        sql.NullTime{Time: nextRenewalAt, Valid: true},
		LastRenewalAttemptAt: sql.NullTime{Time: now, Valid: true},
	})
	if err != nil {
		m.markIssueFailed(ctx, cert, err)
		return
	}
	if err := m.app.refreshPublicProxySnapshot(ctx); err != nil {
		log.Warn().Err(err).Int64("cert_id", cert.ID).Msg("Failed to refresh public proxy after ACME issue")
	}
	_, _ = m.app.restartTLSListenerIfActive(ctx, updated.ListenerID)
}

func (m *publicACMEManager) beginIssue(certID int64) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.inFlight[certID]; ok {
		return false
	}
	m.inFlight[certID] = struct{}{}
	return true
}

func (m *publicACMEManager) endIssue(certID int64) {
	m.mu.Lock()
	delete(m.inFlight, certID)
	m.mu.Unlock()
}

func (m *publicACMEManager) issueConfig(ctx context.Context, cert db.PublicTlsCertificate) (publicACMEIssueConfig, error) {
	cfg := publicACMEIssueConfig{
		CertificateID: cert.ID,
		ListenerID:    cert.ListenerID,
		Domain:        cert.HostnamePattern,
		ChallengeType: normalizePublicACMEChallengeType(cert.AcmeChallengeType),
		CA:            normalizePublicACMECA(cert.AcmeCa),
		Email:         cert.AcmeEmail,
	}
	if cfg.ChallengeType == publicACMEChallengeDNS01 {
		if !cert.DnsCredentialID.Valid {
			return cfg, errors.New("DNS-01 requires a DNS credential")
		}
		credential, err := m.app.DB.GetPublicTlsDnsCredential(ctx, cert.DnsCredentialID.Int64)
		if err != nil {
			return cfg, err
		}
		if credential.Enabled == 0 {
			return cfg, errors.New("DNS credential is disabled")
		}
		cfg.DNSCredential = &credential
	}
	return cfg, nil
}

func (m *publicACMEManager) markIssueFailed(ctx context.Context, cert db.PublicTlsCertificate, issueErr error) {
	if issueErr == nil {
		return
	}
	now := time.Now().UTC()
	_, err := m.app.DB.UpdatePublicTlsCertificateStatus(ctx, db.UpdatePublicTlsCertificateStatusParams{
		ID:                   cert.ID,
		Status:               publicTLSCertificateStatusError,
		LastError:            issueErr.Error(),
		LastRenewalAttemptAt: sql.NullTime{Time: now, Valid: true},
	})
	if err != nil {
		log.Warn().Err(err).Int64("cert_id", cert.ID).Msg("Failed to record ACME certificate issue error")
	}
}

func (m *publicACMEManager) config() *config.Config {
	if m.app != nil && m.app.Config != nil {
		return m.app.Config
	}
	return &config.Config{}
}

type publicRealACMEIssuer struct {
	cfg *config.Config
}

func (i publicRealACMEIssuer) Issue(ctx context.Context, store publicACMEChallengeStore, cfg publicACMEIssueConfig) (publicACMEIssueResult, error) {
	accountKey, err := loadOrCreateACMEAccountKey(i.cfg, cfg.CA, cfg.Email)
	if err != nil {
		return publicACMEIssueResult{}, err
	}
	client := &acme.Client{
		Key:          accountKey,
		DirectoryURL: acmeDirectoryURL(cfg.CA),
	}
	if _, err := client.GetReg(ctx, ""); err != nil {
		if _, registerErr := client.Register(ctx, &acme.Account{Contact: []string{"mailto:" + cfg.Email}}, acme.AcceptTOS); registerErr != nil {
			return publicACMEIssueResult{}, fmt.Errorf("register ACME account: %w", registerErr)
		}
	}

	order, err := client.AuthorizeOrder(ctx, acme.DomainIDs(cfg.Domain))
	if err != nil {
		return publicACMEIssueResult{}, err
	}
	challengeType := acmeChallengeTypeForCA(cfg.ChallengeType)
	for _, authzURL := range order.AuthzURLs {
		authz, err := client.GetAuthorization(ctx, authzURL)
		if err != nil {
			return publicACMEIssueResult{}, err
		}
		if authz.Status == acme.StatusValid {
			continue
		}
		challenge := findACMEChallenge(authz, challengeType)
		if challenge == nil {
			return publicACMEIssueResult{}, fmt.Errorf("ACME authorization for %q does not offer %s", authz.Identifier.Value, challengeType)
		}
		cleanup, err := provisionACMEChallenge(ctx, client, store, cfg, authz, challenge)
		if err != nil {
			return publicACMEIssueResult{}, err
		}
		if _, err := client.Accept(ctx, challenge); err != nil {
			cleanup()
			return publicACMEIssueResult{}, err
		}
		_, waitErr := client.WaitAuthorization(ctx, authz.URI)
		cleanup()
		if waitErr != nil {
			return publicACMEIssueResult{}, waitErr
		}
	}
	order, err = client.WaitOrder(ctx, order.URI)
	if err != nil {
		return publicACMEIssueResult{}, err
	}

	certKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return publicACMEIssueResult{}, err
	}
	csr, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{
		Subject:  pkix.Name{CommonName: strings.TrimPrefix(cfg.Domain, "*.")},
		DNSNames: []string{cfg.Domain},
	}, certKey)
	if err != nil {
		return publicACMEIssueResult{}, err
	}
	derChain, _, err := client.CreateOrderCert(ctx, order.FinalizeURL, csr, true)
	if err != nil {
		return publicACMEIssueResult{}, err
	}
	if len(derChain) == 0 {
		return publicACMEIssueResult{}, errors.New("ACME CA returned no certificate")
	}
	certPEM := make([]byte, 0)
	for _, der := range derChain {
		certPEM = append(certPEM, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})...)
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(certKey)
	if err != nil {
		return publicACMEIssueResult{}, err
	}
	leaf, err := x509.ParseCertificate(derChain[0])
	if err != nil {
		return publicACMEIssueResult{}, err
	}
	return publicACMEIssueResult{
		CertPEM: certPEM,
		KeyPEM:  pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}),
		Leaf:    leaf,
	}, nil
}

func provisionACMEChallenge(ctx context.Context, client *acme.Client, store publicACMEChallengeStore, cfg publicACMEIssueConfig, authz *acme.Authorization, challenge *acme.Challenge) (func(), error) {
	switch cfg.ChallengeType {
	case publicACMEChallengeHTTP01:
		response, err := client.HTTP01ChallengeResponse(challenge.Token)
		if err != nil {
			return nil, err
		}
		return store.SetHTTPChallenge(client.HTTP01ChallengePath(challenge.Token), response), nil
	case publicACMEChallengeTLSALPN01:
		challengeCert, err := client.TLSALPN01ChallengeCert(challenge.Token, authz.Identifier.Value)
		if err != nil {
			return nil, err
		}
		return store.SetTLSALPNChallenge(authz.Identifier.Value, challengeCert), nil
	case publicACMEChallengeDNS01:
		if cfg.DNSCredential == nil {
			return nil, errors.New("DNS-01 requires a DNS credential")
		}
		recordValue, err := client.DNS01ChallengeRecord(challenge.Token)
		if err != nil {
			return nil, err
		}
		solver := cloudflareDNSSolver{credential: *cfg.DNSCredential}
		return solver.Present(ctx, authz.Identifier.Value, recordValue)
	default:
		return nil, fmt.Errorf("unsupported ACME challenge type %q", cfg.ChallengeType)
	}
}

func findACMEChallenge(authz *acme.Authorization, challengeType string) *acme.Challenge {
	for _, challenge := range authz.Challenges {
		if challenge.Type == challengeType {
			return challenge
		}
	}
	return nil
}

func acmeChallengeTypeForCA(challengeType string) string {
	switch normalizePublicACMEChallengeType(challengeType) {
	case publicACMEChallengeHTTP01:
		return "http-01"
	case publicACMEChallengeTLSALPN01:
		return "tls-alpn-01"
	case publicACMEChallengeDNS01:
		return "dns-01"
	default:
		return challengeType
	}
}

func acmeDirectoryURL(ca string) string {
	if normalizePublicACMECA(ca) == publicACMECAStaging {
		return publicACMEStagingDirectory
	}
	return publicACMEProductionDirectory
}

func loadOrCreateACMEAccountKey(cfg *config.Config, ca string, email string) (*ecdsa.PrivateKey, error) {
	path := acmeAccountKeyPath(cfg, ca, email)
	pemBytes, err := os.ReadFile(path)
	if err == nil {
		return parseACMEAccountKey(pemBytes)
	}
	if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return nil, err
	}
	if err := os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}), 0600); err != nil {
		return nil, err
	}
	return key, nil
}

func parseACMEAccountKey(pemBytes []byte) (*ecdsa.PrivateKey, error) {
	block, _ := pem.Decode(pemBytes)
	if block == nil {
		return nil, errors.New("ACME account key is not PEM encoded")
	}
	return x509.ParseECPrivateKey(block.Bytes)
}

func acmeAccountKeyPath(cfg *config.Config, ca string, email string) string {
	certsDir := ""
	if cfg != nil {
		certsDir = cfg.CertsDir
		if strings.TrimSpace(certsDir) == "" && strings.TrimSpace(cfg.ConfigDir) != "" {
			certsDir = filepath.Join(filepath.Clean(cfg.ConfigDir), "certs")
		}
	}
	if strings.TrimSpace(certsDir) == "" {
		certsDir = filepath.Join(config.DefaultConfigDir, "certs")
	}
	return filepath.Join(certsDir, "acme", "accounts", normalizePublicACMECA(ca), safeACMEAccountName(email)+".key")
}

func safeACMEAccountName(email string) string {
	email = strings.TrimSpace(strings.ToLower(email))
	if email == "" {
		return "default"
	}
	var b strings.Builder
	for _, r := range email {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '.' || r == '-' || r == '_' {
			b.WriteRune(r)
			continue
		}
		b.WriteByte('_')
	}
	return b.String()
}

func parseLeafCertificate(certPEM []byte) (*x509.Certificate, error) {
	block, _ := pem.Decode(certPEM)
	if block == nil || block.Type != "CERTIFICATE" {
		return nil, errors.New("certificate PEM does not contain a leaf certificate")
	}
	return x509.ParseCertificate(block.Bytes)
}
