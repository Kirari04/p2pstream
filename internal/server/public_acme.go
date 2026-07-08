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

	"github.com/rs/zerolog"
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

const (
	publicACMELogComponent = "public_acme"

	publicACMETriggerStartupScan   = "startup_scan"
	publicACMETriggerScheduledScan = "scheduled_scan"
	publicACMETriggerManual        = "manual"
	publicACMETriggerConfigChange  = "config_change"

	publicACMEStageSchedulerScan        = "scheduler_scan"
	publicACMEStageSchedulerSkip        = "scheduler_skip"
	publicACMEStageQueued               = "queued"
	publicACMEStageDuplicateInFlight    = "duplicate_in_flight"
	publicACMEStageLoadCertificate      = "load_certificate"
	publicACMEStageMarkRenewing         = "mark_renewing"
	publicACMEStageIssueConfig          = "issue_config"
	publicACMEStageIssueCertificate     = "issue_certificate"
	publicACMEStageParseCertificate     = "parse_certificate"
	publicACMEStageWriteCertificate     = "write_certificate"
	publicACMEStageRecordSuccess        = "record_success"
	publicACMEStageRecordFailure        = "record_failure"
	publicACMEStageRefreshProxySnapshot = "refresh_proxy_snapshot"
	publicACMEStageAccountKey           = "account_key"
	publicACMEStageAccountRegistration  = "account_registration"
	publicACMEStageOrderCreate          = "order_create"
	publicACMEStageAuthorizationLookup  = "authorization_lookup"
	publicACMEStageChallengeSelection   = "challenge_selection"
	publicACMEStageChallengeProvision   = "challenge_provision"
	publicACMEStageChallengeAccept      = "challenge_accept"
	publicACMEStageAuthorizationWait    = "authorization_wait"
	publicACMEStageChallengeCleanup     = "challenge_cleanup"
	publicACMEStageOrderWait            = "order_wait"
	publicACMEStageCSRCreate            = "csr_create"
	publicACMEStageFinalizeCertificate  = "certificate_finalization"
	publicACMEStageMarshalKey           = "marshal_private_key"
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
	Trigger       string
	AttemptAt     time.Time
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

type publicACMEScheduleDecision struct {
	Due           bool
	Reason        string
	NextAttemptAt sql.NullTime
}

type publicACMEStageError struct {
	stage string
	err   error
}

func (e publicACMEStageError) Error() string {
	if e.err == nil {
		return e.stage
	}
	return e.stage + ": " + e.err.Error()
}

func (e publicACMEStageError) Unwrap() error {
	return e.err
}

func publicACMEStageWrap(stage string, err error) error {
	if err == nil {
		return nil
	}
	var stageErr publicACMEStageError
	if errors.As(err, &stageErr) {
		return err
	}
	return publicACMEStageError{stage: stage, err: err}
}

func publicACMEErrorStage(err error, fallback string) string {
	var stageErr publicACMEStageError
	if errors.As(err, &stageErr) && stageErr.stage != "" {
		return stageErr.stage
	}
	return fallback
}

func publicACMELog(event *zerolog.Event, trigger string, stage string) *zerolog.Event {
	event = event.Str("component", publicACMELogComponent)
	if trigger != "" {
		event = event.Str("trigger", trigger)
	}
	if stage != "" {
		event = event.Str("stage", stage)
	}
	return event
}

func publicACMELogCertificate(event *zerolog.Event, cert db.PublicTlsCertificate, trigger string, stage string) *zerolog.Event {
	return publicACMELog(event, trigger, stage).
		Int64("cert_id", cert.ID).
		Int64("listener_id", cert.ListenerID).
		Str("hostname", cert.HostnamePattern).
		Str("challenge_type", normalizePublicACMEChallengeType(cert.AcmeChallengeType)).
		Str("ca", normalizePublicACMECA(cert.AcmeCa))
}

func publicACMELogIssueConfig(event *zerolog.Event, cfg publicACMEIssueConfig, stage string) *zerolog.Event {
	event = publicACMELog(event, cfg.Trigger, stage).
		Int64("cert_id", cfg.CertificateID).
		Int64("listener_id", cfg.ListenerID).
		Str("hostname", cfg.Domain).
		Str("challenge_type", cfg.ChallengeType).
		Str("ca", cfg.CA)
	if !cfg.AttemptAt.IsZero() {
		event = event.Time("attempt_at", cfg.AttemptAt.UTC())
	}
	return event
}

func publicACMELogOptionalTime(event *zerolog.Event, field string, value sql.NullTime) *zerolog.Event {
	if value.Valid {
		return event.Time(field, value.Time.UTC())
	}
	return event
}

func publicACMEMinNullTime(current sql.NullTime, candidate sql.NullTime) sql.NullTime {
	if !candidate.Valid {
		return current
	}
	candidate.Time = candidate.Time.UTC()
	if !current.Valid || candidate.Time.Before(current.Time) {
		return candidate
	}
	return current
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
		m.queueDueCertificates(ctx, publicACMETriggerStartupScan)
		ticker := time.NewTicker(publicACMESchedulerInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				m.queueDueCertificates(ctx, publicACMETriggerScheduledScan)
			}
		}
	}()
}

func (m *publicACMEManager) QueueIssue(certID int64, trigger string) {
	if m == nil || certID <= 0 {
		return
	}
	publicACMELog(log.Info(), trigger, publicACMEStageQueued).
		Int64("cert_id", certID).
		Msg("ACME certificate renewal queued")
	go m.issueCertificate(context.Background(), certID, trigger)
}

func (m *publicACMEManager) QueueCertificate(cert db.PublicTlsCertificate, trigger string) {
	if m == nil || cert.ID <= 0 {
		return
	}
	publicACMELogCertificate(log.Info(), cert, trigger, publicACMEStageQueued).
		Msg("ACME certificate renewal queued")
	go m.issueCertificate(context.Background(), cert.ID, trigger)
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

func (m *publicACMEManager) queueDueCertificates(ctx context.Context, trigger string) {
	if m == nil || m.app == nil || m.app.DB == nil {
		return
	}
	certs, err := m.app.DB.ListPublicTlsCertificates(ctx)
	if err != nil {
		publicACMELog(log.Warn().Err(err), trigger, publicACMEStageSchedulerScan).
			Msg("Failed to list ACME certificates")
		return
	}
	now := time.Now().UTC()
	enabledACMECount := 0
	dueCount := 0
	queuedCount := 0
	var earliestNextAttempt sql.NullTime
	for _, cert := range certs {
		decision := publicACMECertificateScheduleDecision(cert, now)
		if normalizePublicTLSCertificateSource(cert.Source) != publicTLSCertificateSourceACME || cert.Enabled == 0 {
			continue
		}
		enabledACMECount++
		earliestNextAttempt = publicACMEMinNullTime(earliestNextAttempt, decision.NextAttemptAt)
		if !decision.Due {
			publicACMELogOptionalTime(
				publicACMELogCertificate(log.Debug(), cert, trigger, publicACMEStageSchedulerSkip).
					Str("reason", decision.Reason),
				"next_renewal_at",
				decision.NextAttemptAt,
			).Msg("ACME certificate renewal skipped")
			continue
		}
		dueCount++
		queuedCount++
		publicACMELogCertificate(log.Info(), cert, trigger, publicACMEStageQueued).
			Str("reason", decision.Reason).
			Msg("ACME certificate renewal queued")
		go m.issueCertificate(ctx, cert.ID, trigger)
	}
	publicACMELogOptionalTime(
		publicACMELog(log.Info(), trigger, publicACMEStageSchedulerScan).
			Int("certificates_scanned", len(certs)).
			Int("enabled_acme_certificates", enabledACMECount).
			Int("due_certificates", dueCount).
			Int("queued_certificates", queuedCount),
		"next_renewal_at",
		earliestNextAttempt,
	).Msg("ACME renewal scheduler scan completed")
}

func publicACMECertificateDue(cert db.PublicTlsCertificate, now time.Time) bool {
	return publicACMECertificateScheduleDecision(cert, now).Due
}

func publicACMECertificateScheduleDecision(cert db.PublicTlsCertificate, now time.Time) publicACMEScheduleDecision {
	now = now.UTC()
	if normalizePublicTLSCertificateSource(cert.Source) != publicTLSCertificateSourceACME || cert.Enabled == 0 {
		return publicACMEScheduleDecision{Reason: "not_enabled_acme"}
	}
	if normalizePublicTLSCertificateStatus(cert.Status) == publicTLSCertificateStatusError {
		if cert.NextRenewalAt.Valid {
			nextAttemptAt := sql.NullTime{Time: cert.NextRenewalAt.Time.UTC(), Valid: true}
			if now.Before(nextAttemptAt.Time) {
				return publicACMEScheduleDecision{Reason: "waiting_for_retry", NextAttemptAt: nextAttemptAt}
			}
			return publicACMEScheduleDecision{Due: true, Reason: "retry_due", NextAttemptAt: nextAttemptAt}
		}
		if cert.LastRenewalAttemptAt.Valid {
			nextAttemptAt := sql.NullTime{Time: cert.LastRenewalAttemptAt.Time.UTC().Add(publicACMERetryAfterFailure), Valid: true}
			if now.Before(nextAttemptAt.Time) {
				return publicACMEScheduleDecision{Reason: "waiting_for_retry", NextAttemptAt: nextAttemptAt}
			}
			return publicACMEScheduleDecision{Due: true, Reason: "retry_due", NextAttemptAt: nextAttemptAt}
		}
		return publicACMEScheduleDecision{Due: true, Reason: "retry_due"}
	}
	if cert.CertPath == "" || cert.KeyPath == "" {
		return publicACMEScheduleDecision{Due: true, Reason: "missing_certificate_material"}
	}
	if !cert.ExpiresAt.Valid {
		return publicACMEScheduleDecision{Due: true, Reason: "missing_expiry"}
	}
	expiresAt := cert.ExpiresAt.Time.UTC()
	nextAttemptAt := sql.NullTime{Time: expiresAt.Add(-publicACMERenewBefore), Valid: true}
	if !expiresAt.After(now) {
		return publicACMEScheduleDecision{Due: true, Reason: "expired", NextAttemptAt: nextAttemptAt}
	}
	if !expiresAt.After(now.Add(publicACMERenewBefore)) {
		return publicACMEScheduleDecision{Due: true, Reason: "renewal_window", NextAttemptAt: nextAttemptAt}
	}
	return publicACMEScheduleDecision{Reason: "scheduled", NextAttemptAt: nextAttemptAt}
}

func (m *publicACMEManager) issueCertificate(ctx context.Context, certID int64, trigger string) {
	attemptAt := time.Now().UTC()
	if !m.beginIssue(certID) {
		publicACMELog(log.Info(), trigger, publicACMEStageDuplicateInFlight).
			Int64("cert_id", certID).
			Time("attempt_at", attemptAt).
			Msg("ACME certificate renewal skipped because another attempt is in flight")
		return
	}
	defer m.endIssue(certID)

	cert, err := m.app.DB.GetPublicTlsCertificate(ctx, certID)
	if err != nil {
		publicACMELog(log.Warn().Err(err), trigger, publicACMEStageLoadCertificate).
			Int64("cert_id", certID).
			Time("attempt_at", attemptAt).
			Msg("Failed to load ACME certificate")
		return
	}
	if normalizePublicTLSCertificateSource(cert.Source) != publicTLSCertificateSourceACME || cert.Enabled == 0 {
		return
	}
	publicACMELogCertificate(log.Info(), cert, trigger, publicACMEStageIssueCertificate).
		Time("attempt_at", attemptAt).
		Msg("ACME certificate renewal started")
	cert, err = m.app.DB.UpdatePublicTlsCertificateRenewalStatus(ctx, db.UpdatePublicTlsCertificateRenewalStatusParams{
		ID:                   cert.ID,
		Status:               publicTLSCertificateStatusRenewing,
		LastError:            "",
		NextRenewalAt:        sql.NullTime{},
		LastRenewalAttemptAt: sql.NullTime{Time: attemptAt, Valid: true},
	})
	if err != nil {
		publicACMELogCertificate(log.Warn().Err(err), cert, trigger, publicACMEStageMarkRenewing).
			Time("attempt_at", attemptAt).
			Msg("Failed to mark ACME certificate renewing")
		return
	}

	issueCfg, err := m.issueConfig(ctx, cert)
	if err != nil {
		m.markIssueFailed(ctx, cert, trigger, attemptAt, publicACMEStageWrap(publicACMEStageIssueConfig, err))
		return
	}
	issueCfg.Trigger = trigger
	issueCfg.AttemptAt = attemptAt
	result, err := m.issuer.Issue(ctx, m, issueCfg)
	if err != nil {
		m.markIssueFailed(ctx, cert, trigger, attemptAt, publicACMEStageWrap(publicACMEStageIssueCertificate, err))
		return
	}
	if result.Leaf == nil {
		result.Leaf, err = parseLeafCertificate(result.CertPEM)
		if err != nil {
			m.markIssueFailed(ctx, cert, trigger, attemptAt, publicACMEStageWrap(publicACMEStageParseCertificate, err))
			return
		}
	}

	certPath, keyPath, err := m.config().WritePublicTLSCertificateFiles(cert.ListenerID, cert.ID, result.CertPEM, result.KeyPEM)
	if err != nil {
		m.markIssueFailed(ctx, cert, trigger, attemptAt, publicACMEStageWrap(publicACMEStageWriteCertificate, err))
		return
	}
	expiresAt := result.Leaf.NotAfter.UTC()
	issuedAt := attemptAt
	if !result.Leaf.NotBefore.IsZero() {
		issuedAt = result.Leaf.NotBefore.UTC()
	}
	nextRenewalAt := expiresAt.Add(-publicACMERenewBefore)
	if nextRenewalAt.Before(attemptAt) {
		nextRenewalAt = attemptAt.Add(publicACMERetryAfterFailure)
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
		LastRenewalAttemptAt: sql.NullTime{Time: attemptAt, Valid: true},
	})
	if err != nil {
		m.markIssueFailed(ctx, cert, trigger, attemptAt, publicACMEStageWrap(publicACMEStageRecordSuccess, err))
		return
	}
	duration := time.Since(attemptAt)
	publicACMELogCertificate(log.Info(), updated, trigger, publicACMEStageRecordSuccess).
		Time("attempt_at", attemptAt).
		Dur("duration", duration).
		Time("expires_at", expiresAt).
		Time("next_renewal_at", nextRenewalAt).
		Msg("ACME certificate renewal succeeded")
	if err := m.app.refreshPublicProxySnapshot(ctx); err != nil {
		publicACMELogCertificate(log.Warn().Err(err), updated, trigger, publicACMEStageRefreshProxySnapshot).
			Time("attempt_at", attemptAt).
			Msg("Failed to refresh public proxy after ACME issue")
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

func (m *publicACMEManager) markIssueFailed(ctx context.Context, cert db.PublicTlsCertificate, trigger string, attemptAt time.Time, issueErr error) {
	if issueErr == nil {
		return
	}
	now := time.Now().UTC()
	retryAt := now.Add(publicACMERetryAfterFailure)
	_, err := m.app.DB.UpdatePublicTlsCertificateRenewalStatus(ctx, db.UpdatePublicTlsCertificateRenewalStatusParams{
		ID:                   cert.ID,
		Status:               publicTLSCertificateStatusError,
		LastError:            issueErr.Error(),
		NextRenewalAt:        sql.NullTime{Time: retryAt, Valid: true},
		LastRenewalAttemptAt: sql.NullTime{Time: attemptAt, Valid: true},
	})
	stage := publicACMEErrorStage(issueErr, publicACMEStageIssueCertificate)
	publicACMELogCertificate(log.Warn().Err(issueErr), cert, trigger, stage).
		Time("attempt_at", attemptAt).
		Dur("duration", now.Sub(attemptAt)).
		Time("retry_at", retryAt).
		Msg("ACME certificate renewal failed")
	if err != nil {
		publicACMELogCertificate(log.Warn().Err(err), cert, trigger, publicACMEStageRecordFailure).
			Time("attempt_at", attemptAt).
			Time("retry_at", retryAt).
			Msg("Failed to record ACME certificate issue error")
		return
	}
	if err := m.app.refreshPublicProxySnapshot(ctx); err != nil {
		publicACMELogCertificate(log.Warn().Err(err), cert, trigger, publicACMEStageRefreshProxySnapshot).
			Time("attempt_at", attemptAt).
			Msg("Failed to refresh public proxy after ACME issue failure")
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
	accountKey, accountKeyCreated, err := loadOrCreateACMEAccountKey(i.cfg, cfg.CA, cfg.Email)
	if err != nil {
		return publicACMEIssueResult{}, publicACMEStageWrap(publicACMEStageAccountKey, err)
	}
	accountKeyState := "loaded"
	if accountKeyCreated {
		accountKeyState = "created"
	}
	publicACMELogIssueConfig(log.Info(), cfg, publicACMEStageAccountKey).
		Str("account_key_state", accountKeyState).
		Msg("ACME account key ready")
	client := &acme.Client{
		Key:          accountKey,
		DirectoryURL: acmeDirectoryURL(cfg.CA),
	}
	if _, err := client.GetReg(ctx, ""); err != nil {
		publicACMELogIssueConfig(log.Info(), cfg, publicACMEStageAccountRegistration).
			Msg("Registering ACME account")
		if _, registerErr := client.Register(ctx, &acme.Account{Contact: []string{"mailto:" + cfg.Email}}, acme.AcceptTOS); registerErr != nil {
			return publicACMEIssueResult{}, publicACMEStageWrap(publicACMEStageAccountRegistration, fmt.Errorf("register ACME account: %w", registerErr))
		}
		publicACMELogIssueConfig(log.Info(), cfg, publicACMEStageAccountRegistration).
			Msg("ACME account registered")
	} else {
		publicACMELogIssueConfig(log.Info(), cfg, publicACMEStageAccountRegistration).
			Msg("ACME account ready")
	}

	order, err := client.AuthorizeOrder(ctx, acme.DomainIDs(cfg.Domain))
	if err != nil {
		return publicACMEIssueResult{}, publicACMEStageWrap(publicACMEStageOrderCreate, err)
	}
	publicACMELogIssueConfig(log.Info(), cfg, publicACMEStageOrderCreate).
		Int("authorization_count", len(order.AuthzURLs)).
		Msg("ACME order created")
	challengeType := acmeChallengeTypeForCA(cfg.ChallengeType)
	for _, authzURL := range order.AuthzURLs {
		authz, err := client.GetAuthorization(ctx, authzURL)
		if err != nil {
			return publicACMEIssueResult{}, publicACMEStageWrap(publicACMEStageAuthorizationLookup, err)
		}
		publicACMELogIssueConfig(log.Info(), cfg, publicACMEStageAuthorizationLookup).
			Str("authorization_domain", authz.Identifier.Value).
			Str("authorization_status", authz.Status).
			Msg("ACME authorization loaded")
		if authz.Status == acme.StatusValid {
			continue
		}
		challenge := findACMEChallenge(authz, challengeType)
		if challenge == nil {
			return publicACMEIssueResult{}, publicACMEStageWrap(publicACMEStageChallengeSelection, fmt.Errorf("ACME authorization for %q does not offer %s", authz.Identifier.Value, challengeType))
		}
		publicACMELogIssueConfig(log.Info(), cfg, publicACMEStageChallengeSelection).
			Str("authorization_domain", authz.Identifier.Value).
			Str("acme_challenge_type", challengeType).
			Msg("ACME challenge selected")
		cleanup, err := provisionACMEChallenge(ctx, client, store, cfg, authz, challenge)
		if err != nil {
			return publicACMEIssueResult{}, publicACMEStageWrap(publicACMEStageChallengeProvision, err)
		}
		publicACMELogIssueConfig(log.Info(), cfg, publicACMEStageChallengeProvision).
			Str("authorization_domain", authz.Identifier.Value).
			Str("acme_challenge_type", challengeType).
			Msg("ACME challenge provisioned")
		if _, err := client.Accept(ctx, challenge); err != nil {
			cleanup()
			publicACMELogIssueConfig(log.Info(), cfg, publicACMEStageChallengeCleanup).
				Str("authorization_domain", authz.Identifier.Value).
				Msg("ACME challenge cleanup completed")
			return publicACMEIssueResult{}, publicACMEStageWrap(publicACMEStageChallengeAccept, err)
		}
		publicACMELogIssueConfig(log.Info(), cfg, publicACMEStageChallengeAccept).
			Str("authorization_domain", authz.Identifier.Value).
			Msg("ACME challenge accepted")
		_, waitErr := client.WaitAuthorization(ctx, authz.URI)
		cleanup()
		publicACMELogIssueConfig(log.Info(), cfg, publicACMEStageChallengeCleanup).
			Str("authorization_domain", authz.Identifier.Value).
			Msg("ACME challenge cleanup completed")
		if waitErr != nil {
			return publicACMEIssueResult{}, publicACMEStageWrap(publicACMEStageAuthorizationWait, waitErr)
		}
		publicACMELogIssueConfig(log.Info(), cfg, publicACMEStageAuthorizationWait).
			Str("authorization_domain", authz.Identifier.Value).
			Msg("ACME authorization validated")
	}
	order, err = client.WaitOrder(ctx, order.URI)
	if err != nil {
		return publicACMEIssueResult{}, publicACMEStageWrap(publicACMEStageOrderWait, err)
	}
	publicACMELogIssueConfig(log.Info(), cfg, publicACMEStageOrderWait).
		Str("order_status", order.Status).
		Msg("ACME order ready")

	certKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return publicACMEIssueResult{}, publicACMEStageWrap(publicACMEStageCSRCreate, err)
	}
	csr, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{
		Subject:  pkix.Name{CommonName: strings.TrimPrefix(cfg.Domain, "*.")},
		DNSNames: []string{cfg.Domain},
	}, certKey)
	if err != nil {
		return publicACMEIssueResult{}, publicACMEStageWrap(publicACMEStageCSRCreate, err)
	}
	publicACMELogIssueConfig(log.Info(), cfg, publicACMEStageCSRCreate).
		Msg("ACME certificate signing request created")
	derChain, _, err := client.CreateOrderCert(ctx, order.FinalizeURL, csr, true)
	if err != nil {
		return publicACMEIssueResult{}, publicACMEStageWrap(publicACMEStageFinalizeCertificate, err)
	}
	if len(derChain) == 0 {
		return publicACMEIssueResult{}, publicACMEStageWrap(publicACMEStageFinalizeCertificate, errors.New("ACME CA returned no certificate"))
	}
	publicACMELogIssueConfig(log.Info(), cfg, publicACMEStageFinalizeCertificate).
		Int("certificate_chain_length", len(derChain)).
		Msg("ACME certificate finalized")
	certPEM := make([]byte, 0)
	for _, der := range derChain {
		certPEM = append(certPEM, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})...)
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(certKey)
	if err != nil {
		return publicACMEIssueResult{}, publicACMEStageWrap(publicACMEStageMarshalKey, err)
	}
	leaf, err := x509.ParseCertificate(derChain[0])
	if err != nil {
		return publicACMEIssueResult{}, publicACMEStageWrap(publicACMEStageParseCertificate, err)
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

func loadOrCreateACMEAccountKey(cfg *config.Config, ca string, email string) (*ecdsa.PrivateKey, bool, error) {
	path := acmeAccountKeyPath(cfg, ca, email)
	pemBytes, err := os.ReadFile(path)
	if err == nil {
		key, err := parseACMEAccountKey(pemBytes)
		return key, false, err
	}
	if !errors.Is(err, os.ErrNotExist) {
		return nil, false, err
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, false, err
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return nil, false, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return nil, false, err
	}
	if err := os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}), 0600); err != nil {
		return nil, false, err
	}
	return key, true, nil
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
