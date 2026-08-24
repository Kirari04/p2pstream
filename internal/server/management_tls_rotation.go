package server

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"connectrpc.com/connect"

	p2pstreamv1 "p2pstream/gen/proto/p2pstream/v1"
	"p2pstream/internal/config"
	"p2pstream/internal/db"
)

const (
	managementTLSRotationStateFile = "rotation-state.json"
	managementTLSRotationMaxPEM    = 2 << 20
	managementTLSRotationMaxCAPEM  = 64 << 10
	managementTrustCapability      = "management-trust-v1"
)

type managementTLSRotationDiskState struct {
	Phase                    string   `json:"phase"`
	ActiveGeneration         uint64   `json:"active_generation"`
	StagedGeneration         uint64   `json:"staged_generation,omitempty"`
	RolloutGeneration        uint64   `json:"rollout_generation"`
	ActiveCertFile           string   `json:"active_cert_file"`
	ActiveKeyFile            string   `json:"active_key_file"`
	ActiveCAPEM              string   `json:"active_ca_pem"`
	PreviousCertFile         string   `json:"previous_cert_file,omitempty"`
	PreviousKeyFile          string   `json:"previous_key_file,omitempty"`
	PreviousCAPEM            string   `json:"previous_ca_pem,omitempty"`
	StagedCertFile           string   `json:"staged_cert_file,omitempty"`
	StagedKeyFile            string   `json:"staged_key_file,omitempty"`
	StagedCertPEM            string   `json:"staged_cert_pem,omitempty"`
	StagedTargetCAPEM        string   `json:"staged_target_ca_pem,omitempty"`
	RolloutCAPEM             string   `json:"rollout_ca_pem,omitempty"`
	RolloutBundleSHA256      string   `json:"rollout_bundle_sha256,omitempty"`
	TrustManaged             bool     `json:"trust_managed"`
	DesiredTrustGeneration   uint64   `json:"desired_trust_generation,omitempty"`
	DesiredTrustCAPEM        string   `json:"desired_trust_ca_pem,omitempty"`
	DesiredTrustBundleSHA256 string   `json:"desired_trust_bundle_sha256,omitempty"`
	CleanupReason            string   `json:"cleanup_reason,omitempty"`
	PendingDeleteFiles       []string `json:"pending_delete_files,omitempty"`
	RequiresTrustRollout     bool     `json:"requires_trust_rollout"`
	ForcedActivation         bool     `json:"forced_activation"`
	ForcedRetirement         bool     `json:"forced_retirement"`
	ForcedCleanup            bool     `json:"forced_cleanup"`
}

type ManagementTLSRuntime struct {
	mu             sync.RWMutex
	cfg            *config.Config
	db             *db.DB
	enabled        bool
	stateFile      string
	state          managementTLSRotationDiskState
	cleanupWarning string
	cert           atomic.Pointer[tls.Certificate]
}

func NewManagementTLSRuntime(cfg *config.Config, database *db.DB, tlsConfig *tls.Config, enabled bool) (*ManagementTLSRuntime, error) {
	runtime := &ManagementTLSRuntime{cfg: cfg, db: database, enabled: enabled}
	if !enabled || tlsConfig == nil {
		return runtime, nil
	}
	if len(tlsConfig.Certificates) != 1 {
		return nil, fmt.Errorf("management TLS rotation requires exactly one server certificate")
	}
	rotationDir := filepath.Join(managementCertBaseDir(cfg), managementCertDirName, "rotation")
	if err := os.MkdirAll(rotationDir, 0700); err != nil {
		return nil, fmt.Errorf("create management TLS rotation directory: %w", err)
	}
	if err := os.Chmod(rotationDir, 0700); err != nil {
		return nil, fmt.Errorf("secure management TLS rotation directory: %w", err)
	}
	runtime.stateFile = filepath.Join(rotationDir, managementTLSRotationStateFile)
	certFile, keyFile := configuredManagementTLSFiles(cfg)
	runtime.state = managementTLSRotationDiskState{
		Phase:            "idle",
		ActiveGeneration: 1,
		ActiveCertFile:   certFile,
		ActiveKeyFile:    keyFile,
		ActiveCAPEM:      strings.TrimSpace(cfg.ManagementCAPEM),
	}
	if raw, err := os.ReadFile(runtime.stateFile); err == nil {
		if err := json.Unmarshal(raw, &runtime.state); err != nil {
			return nil, fmt.Errorf("parse management TLS rotation state: %w", err)
		}
		if runtime.state.ActiveGeneration == 0 {
			return nil, errors.New("invalid management TLS rotation state: active generation is zero")
		}
	} else if !os.IsNotExist(err) {
		return nil, fmt.Errorf("read management TLS rotation state: %w", err)
	}
	if runtime.state.StagedGeneration == 0 && runtime.state.Phase == "distributing" {
		runtime.state.StagedGeneration = runtime.state.RolloutGeneration
	}
	if !runtime.state.TrustManaged && runtime.state.Phase == "idle" && runtime.state.ActiveGeneration > 1 && strings.TrimSpace(runtime.state.ActiveCAPEM) != "" {
		runtime.state.TrustManaged = true
		runtime.state.DesiredTrustGeneration = runtime.state.ActiveGeneration
		runtime.state.DesiredTrustCAPEM = normalizeCertificateBundle(runtime.state.ActiveCAPEM)
		runtime.state.DesiredTrustBundleSHA256 = managementCAPEMSHA256(runtime.state.DesiredTrustCAPEM)
	}
	if err := validateManagementTLSRotationState(runtime.state); err != nil {
		return nil, fmt.Errorf("validate management TLS rotation state: %w", err)
	}
	active, err := tls.LoadX509KeyPair(runtime.state.ActiveCertFile, runtime.state.ActiveKeyFile)
	if err != nil {
		return nil, fmt.Errorf("load active management TLS rotation certificate: %w", err)
	}
	runtime.cert.Store(&active)
	runtime.retryPendingFileCleanupLocked()
	tlsConfig.Certificates = nil
	tlsConfig.GetCertificate = func(*tls.ClientHelloInfo) (*tls.Certificate, error) {
		cert := runtime.cert.Load()
		if cert == nil {
			return nil, errors.New("management TLS certificate unavailable")
		}
		return cert, nil
	}
	return runtime, nil
}

func validateManagementTLSRotationState(state managementTLSRotationDiskState) error {
	if state.ActiveGeneration == 0 || strings.TrimSpace(state.ActiveCertFile) == "" || strings.TrimSpace(state.ActiveKeyFile) == "" {
		return errors.New("active generation, certificate, and key are required")
	}
	switch state.Phase {
	case "idle":
	case "distributing":
		if state.StagedGeneration == 0 || strings.TrimSpace(state.StagedCertFile) == "" || strings.TrimSpace(state.StagedKeyFile) == "" || strings.TrimSpace(state.StagedCertPEM) == "" || strings.TrimSpace(state.StagedTargetCAPEM) == "" {
			return errors.New("distributing phase is missing staged certificate state")
		}
	case "active":
		if strings.TrimSpace(state.PreviousCertFile) == "" || strings.TrimSpace(state.PreviousKeyFile) == "" {
			return errors.New("active phase is missing rollback certificate state")
		}
	case "retiring":
		if strings.TrimSpace(state.PreviousCertFile) == "" || strings.TrimSpace(state.PreviousKeyFile) == "" {
			return errors.New("retiring phase is missing rollback certificate state")
		}
		if !state.RequiresTrustRollout {
			return errors.New("retiring phase is missing its trust rollout")
		}
	case "cleaning":
		if state.CleanupReason != "cancel" && state.CleanupReason != "rollback" {
			return errors.New("cleaning phase has an invalid cleanup reason")
		}
		if !state.RequiresTrustRollout {
			return errors.New("cleaning phase is missing its trust rollout")
		}
	default:
		return fmt.Errorf("unknown rotation phase %q", state.Phase)
	}
	if state.RequiresTrustRollout {
		if state.RolloutGeneration == 0 || strings.TrimSpace(state.RolloutCAPEM) == "" || strings.TrimSpace(state.RolloutBundleSHA256) == "" {
			return errors.New("trust rollout is missing generation, bundle, or digest")
		}
		normalized, err := validateManagementCABundle(state.RolloutCAPEM)
		if err != nil {
			return fmt.Errorf("validate rollout CA bundle: %w", err)
		}
		if managementCAPEMSHA256(normalized) != state.RolloutBundleSHA256 {
			return errors.New("trust rollout bundle digest does not match persisted CA bundle")
		}
	}
	if state.TrustManaged {
		if state.DesiredTrustGeneration == 0 || strings.TrimSpace(state.DesiredTrustCAPEM) == "" || strings.TrimSpace(state.DesiredTrustBundleSHA256) == "" {
			return errors.New("managed idle trust is missing generation, bundle, or digest")
		}
		normalized, err := validateManagementCABundle(state.DesiredTrustCAPEM)
		if err != nil {
			return fmt.Errorf("validate desired CA bundle: %w", err)
		}
		if managementCAPEMSHA256(normalized) != state.DesiredTrustBundleSHA256 {
			return errors.New("desired trust bundle digest does not match persisted CA bundle")
		}
	}
	return nil
}

func configuredManagementTLSFiles(cfg *config.Config) (string, string) {
	if cfg != nil && strings.TrimSpace(cfg.ManagementTLSCertFile) != "" && strings.TrimSpace(cfg.ManagementTLSKeyFile) != "" {
		return strings.TrimSpace(cfg.ManagementTLSCertFile), strings.TrimSpace(cfg.ManagementTLSKeyFile)
	}
	base := filepath.Join(managementCertBaseDir(cfg), managementCertDirName)
	return filepath.Join(base, managementCertFileName), filepath.Join(base, managementKeyFileName)
}

func (m *ManagementTLSRuntime) currentAgentCA() (string, string) {
	if m == nil {
		return "", ""
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.state.RequiresTrustRollout && m.state.RolloutCAPEM != "" {
		return m.state.RolloutCAPEM, m.state.RolloutBundleSHA256
	}
	if m.state.TrustManaged && m.state.DesiredTrustCAPEM != "" {
		return m.state.DesiredTrustCAPEM, m.state.DesiredTrustBundleSHA256
	}
	return m.state.ActiveCAPEM, managementCAPEMSHA256(m.state.ActiveCAPEM)
}

func (m *ManagementTLSRuntime) persistLocked() error {
	raw, err := json.MarshalIndent(m.state, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(m.stateFile), ".rotation-state-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(0600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(raw); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpName, m.stateFile); err != nil {
		return err
	}
	dir, err := os.Open(filepath.Dir(m.stateFile))
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}

func (m *ManagementTLSRuntime) nextGenerationLocked() uint64 {
	next := m.state.ActiveGeneration
	for _, generation := range []uint64{
		m.state.StagedGeneration,
		m.state.RolloutGeneration,
		m.state.DesiredTrustGeneration,
	} {
		if generation > next {
			next = generation
		}
	}
	return next + 1
}

func (m *ManagementTLSRuntime) isManagedRotationFile(path string) bool {
	if strings.TrimSpace(path) == "" || strings.TrimSpace(m.stateFile) == "" {
		return false
	}
	wantedDir, err := filepath.Abs(filepath.Dir(m.stateFile))
	if err != nil {
		return false
	}
	cleanPath, err := filepath.Abs(filepath.Clean(path))
	if err != nil || filepath.Dir(cleanPath) != wantedDir {
		return false
	}
	base := filepath.Base(cleanPath)
	return strings.HasPrefix(base, "server-") &&
		(strings.HasSuffix(base, ".crt.pem") || strings.HasSuffix(base, ".key.pem"))
}

func (m *ManagementTLSRuntime) queueManagedFilesForDeletionLocked(paths ...string) {
	seen := make(map[string]struct{}, len(m.state.PendingDeleteFiles)+len(paths))
	for _, existing := range m.state.PendingDeleteFiles {
		seen[filepath.Clean(existing)] = struct{}{}
	}
	for _, path := range paths {
		path = filepath.Clean(strings.TrimSpace(path))
		if !m.isManagedRotationFile(path) {
			continue
		}
		if _, ok := seen[path]; ok {
			continue
		}
		seen[path] = struct{}{}
		m.state.PendingDeleteFiles = append(m.state.PendingDeleteFiles, path)
	}
}

func (m *ManagementTLSRuntime) retryPendingFileCleanupLocked() {
	if len(m.state.PendingDeleteFiles) == 0 {
		m.cleanupWarning = ""
		return
	}
	referenced := map[string]struct{}{}
	for _, path := range []string{
		m.state.ActiveCertFile,
		m.state.ActiveKeyFile,
		m.state.PreviousCertFile,
		m.state.PreviousKeyFile,
		m.state.StagedCertFile,
		m.state.StagedKeyFile,
	} {
		if strings.TrimSpace(path) != "" {
			referenced[filepath.Clean(path)] = struct{}{}
		}
	}

	remaining := make([]string, 0, len(m.state.PendingDeleteFiles))
	var cleanupErrors []error
	for _, path := range m.state.PendingDeleteFiles {
		path = filepath.Clean(path)
		if _, inUse := referenced[path]; inUse {
			remaining = append(remaining, path)
			continue
		}
		if !m.isManagedRotationFile(path) {
			remaining = append(remaining, path)
			cleanupErrors = append(cleanupErrors, fmt.Errorf("refusing to remove unmanaged TLS file %q", path))
			continue
		}
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			remaining = append(remaining, path)
			cleanupErrors = append(cleanupErrors, fmt.Errorf("remove retired TLS file %q: %w", filepath.Base(path), err))
		}
	}
	changed := len(remaining) != len(m.state.PendingDeleteFiles)
	m.state.PendingDeleteFiles = remaining
	if changed {
		if err := m.persistLocked(); err != nil {
			cleanupErrors = append(cleanupErrors, fmt.Errorf("persist TLS file cleanup: %w", err))
		}
	}
	if err := errors.Join(cleanupErrors...); err != nil {
		m.cleanupWarning = err.Error()
	} else {
		m.cleanupWarning = ""
	}
}

func parseAndVerifyManagementRotation(certificatePEM, privateKeyPEM, caPEM, hostname string) (tls.Certificate, *x509.Certificate, string, error) {
	if len(certificatePEM) == 0 || len(privateKeyPEM) == 0 || len(caPEM) == 0 {
		return tls.Certificate{}, nil, "", errors.New("certificate, private key, and CA bundle are required")
	}
	if len(certificatePEM) > managementTLSRotationMaxPEM || len(privateKeyPEM) > managementTLSRotationMaxPEM {
		return tls.Certificate{}, nil, "", errors.New("certificate or private key exceeds the 2 MiB limit")
	}
	if len(caPEM) > managementTLSRotationMaxCAPEM {
		return tls.Certificate{}, nil, "", errors.New("CA bundle exceeds the 64 KiB limit")
	}
	pair, err := tls.X509KeyPair([]byte(certificatePEM), []byte(privateKeyPEM))
	if err != nil {
		return tls.Certificate{}, nil, "", fmt.Errorf("certificate and private key do not form a valid pair: %w", err)
	}
	if len(pair.Certificate) == 0 {
		return tls.Certificate{}, nil, "", errors.New("certificate chain is empty")
	}
	leaf, err := x509.ParseCertificate(pair.Certificate[0])
	if err != nil {
		return tls.Certificate{}, nil, "", fmt.Errorf("parse server certificate: %w", err)
	}
	now := time.Now()
	if now.Before(leaf.NotBefore) || !now.Before(leaf.NotAfter) {
		return tls.Certificate{}, nil, "", errors.New("server certificate is not currently valid")
	}
	if hostname != "" {
		if err := leaf.VerifyHostname(hostname); err != nil {
			return tls.Certificate{}, nil, "", fmt.Errorf("server certificate does not cover management hostname %q: %w", hostname, err)
		}
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM([]byte(caPEM)) {
		return tls.Certificate{}, nil, "", errors.New("CA bundle contains no PEM certificates")
	}
	intermediates := x509.NewCertPool()
	for _, der := range pair.Certificate[1:] {
		cert, err := x509.ParseCertificate(der)
		if err != nil {
			return tls.Certificate{}, nil, "", fmt.Errorf("parse certificate chain: %w", err)
		}
		intermediates.AddCert(cert)
	}
	if _, err := leaf.Verify(x509.VerifyOptions{Roots: roots, Intermediates: intermediates, DNSName: hostname, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}); err != nil {
		return tls.Certificate{}, nil, "", fmt.Errorf("server certificate does not verify with the supplied CA bundle: %w", err)
	}
	pair.Leaf = leaf
	digest := sha256.Sum256([]byte(normalizeCertificateBundle(caPEM)))
	return pair, leaf, hex.EncodeToString(digest[:]), nil
}

func normalizeCertificateBundle(bundles ...string) string {
	seen := make(map[string]struct{})
	var encoded []string
	for _, bundle := range bundles {
		rest := []byte(bundle)
		for {
			block, remaining := pem.Decode(rest)
			if block == nil {
				break
			}
			rest = remaining
			if block.Type != "CERTIFICATE" {
				continue
			}
			sum := sha256.Sum256(block.Bytes)
			key := hex.EncodeToString(sum[:])
			if _, ok := seen[key]; ok {
				continue
			}
			seen[key] = struct{}{}
			encoded = append(encoded, string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: block.Bytes})))
		}
	}
	return strings.Join(encoded, "")
}

func validateManagementCABundle(bundle string) (string, error) {
	rest := []byte(bundle)
	var normalized strings.Builder
	foundCA := false
	for len(bytes.TrimSpace(rest)) > 0 {
		rest = bytes.TrimSpace(rest)
		if !bytes.HasPrefix(rest, []byte("-----BEGIN CERTIFICATE-----")) {
			return "", errors.New("CA bundle contains data other than PEM certificates")
		}
		block, remaining := pem.Decode(rest)
		if block == nil || block.Type != "CERTIFICATE" || len(block.Headers) != 0 {
			return "", errors.New("CA bundle contains an invalid PEM certificate block")
		}
		cert, err := x509.ParseCertificate(block.Bytes)
		if err != nil {
			return "", fmt.Errorf("parse CA bundle certificate: %w", err)
		}
		if !cert.IsCA {
			return "", fmt.Errorf("CA bundle certificate %q is not a CA", cert.Subject.String())
		}
		foundCA = true
		normalized.Write(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: block.Bytes}))
		rest = remaining
	}
	if !foundCA {
		return "", errors.New("CA bundle contains no CA certificates")
	}
	return normalizeCertificateBundle(normalized.String()), nil
}

func (m *ManagementTLSRuntime) stage(certificatePEM, privateKeyPEM, caPEM, currentCAOverride string) error {
	if m == nil || !m.enabled {
		return errors.New("management TLS is disabled")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.state.Phase != "idle" {
		return fmt.Errorf("cannot stage while rotation phase is %s", m.state.Phase)
	}
	hostname := ""
	if parsed, err := url.Parse(m.cfg.ManagementDefaultURL); err == nil {
		hostname = parsed.Hostname()
	}
	if len(caPEM) > managementTLSRotationMaxCAPEM || len(currentCAOverride) > managementTLSRotationMaxCAPEM {
		return errors.New("current or replacement CA bundle exceeds the 64 KiB limit")
	}
	targetCA, err := validateManagementCABundle(caPEM)
	if err != nil {
		return err
	}
	currentCA := normalizeCertificateBundle(m.state.ActiveCAPEM)
	if strings.TrimSpace(currentCAOverride) != "" {
		currentCA, err = validateManagementCABundle(currentCAOverride)
		if err != nil {
			return fmt.Errorf("validate current CA bundle: %w", err)
		}
		if err := verifyManagementCertificateFile(m.state.ActiveCertFile, currentCA, hostname); err != nil {
			return fmt.Errorf("current CA bundle does not verify the active certificate: %w", err)
		}
	}
	if currentCA == "" {
		if err := verifyManagementCertificateFile(m.state.ActiveCertFile, targetCA, hostname); err == nil {
			currentCA = targetCA
		} else {
			return errors.New("the active certificate issuer is not available; stage a custom rotation and include the current agent CA bundle")
		}
	}
	_, _, targetDigest, err := parseAndVerifyManagementRotation(certificatePEM, privateKeyPEM, targetCA, hostname)
	if err != nil {
		return err
	}
	generation := m.nextGenerationLocked()
	dir := filepath.Dir(m.stateFile)
	certFile := filepath.Join(dir, fmt.Sprintf("server-%d.crt.pem", generation))
	keyFile := filepath.Join(dir, fmt.Sprintf("server-%d.key.pem", generation))
	if err := writePEMFile(certFile, []byte(certificatePEM), 0644); err != nil {
		return fmt.Errorf("persist staged certificate: %w", err)
	}
	if err := writePEMFile(keyFile, []byte(privateKeyPEM), 0600); err != nil {
		_ = os.Remove(certFile)
		return fmt.Errorf("persist staged private key: %w", err)
	}
	if _, err := tls.LoadX509KeyPair(certFile, keyFile); err != nil {
		_ = os.Remove(certFile)
		_ = os.Remove(keyFile)
		return fmt.Errorf("read back staged certificate: %w", err)
	}
	rolloutCA := normalizeCertificateBundle(currentCA, targetCA)
	rolloutDigest := managementCAPEMSHA256(rolloutCA)
	previousState := m.state
	m.state.ActiveCAPEM = currentCA
	m.state.Phase = "distributing"
	m.state.StagedGeneration = generation
	m.state.StagedCertFile = certFile
	m.state.StagedKeyFile = keyFile
	m.state.StagedCertPEM = certificatePEM
	m.state.StagedTargetCAPEM = targetCA
	m.state.RequiresTrustRollout = targetDigest != managementCAPEMSHA256(currentCA)
	if m.state.RequiresTrustRollout {
		m.state.RolloutGeneration = generation
		m.state.RolloutCAPEM = rolloutCA
		m.state.RolloutBundleSHA256 = rolloutDigest
	} else {
		m.state.RolloutGeneration = 0
		m.state.RolloutCAPEM = ""
		m.state.RolloutBundleSHA256 = ""
	}
	m.state.ForcedActivation = false
	m.state.ForcedRetirement = false
	m.state.ForcedCleanup = false
	m.state.CleanupReason = ""
	if err := m.persistLocked(); err != nil {
		m.state = previousState
		_ = os.Remove(certFile)
		_ = os.Remove(keyFile)
		return fmt.Errorf("persist management TLS rotation: %w", err)
	}
	return nil
}

func (m *ManagementTLSRuntime) generateAndStage() error {
	if m == nil || !m.enabled {
		return errors.New("management TLS is disabled")
	}
	caPEM, _, caCert, caKey, err := generateManagementCAPEM()
	if err != nil {
		return fmt.Errorf("generate management CA: %w", err)
	}
	certPEM, keyPEM, err := generateManagementServerCertificatePEM(caCert, caKey, managementTLSHosts(m.cfg, detectManagementAdvertiseHost(m.cfg)))
	if err != nil {
		return fmt.Errorf("generate management server certificate: %w", err)
	}
	return m.stage(string(certPEM), string(keyPEM), string(caPEM), "")
}

func verifyManagementCertificateFile(certFile, caPEM, hostname string) error {
	raw, err := os.ReadFile(certFile)
	if err != nil {
		return err
	}
	block, rest := pem.Decode(raw)
	if block == nil || block.Type != "CERTIFICATE" {
		return errors.New("active certificate file contains no PEM certificate")
	}
	leaf, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return err
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM([]byte(caPEM)) {
		return errors.New("CA bundle contains no certificates")
	}
	intermediates := x509.NewCertPool()
	for {
		block, remaining := pem.Decode(rest)
		if block == nil {
			break
		}
		rest = remaining
		if block.Type == "CERTIFICATE" {
			if cert, parseErr := x509.ParseCertificate(block.Bytes); parseErr == nil {
				intermediates.AddCert(cert)
			}
		}
	}
	_, err = leaf.Verify(x509.VerifyOptions{Roots: roots, Intermediates: intermediates, DNSName: hostname, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}})
	return err
}

func (m *ManagementTLSRuntime) activate(ctx context.Context, force bool) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.state.Phase != "distributing" {
		return errors.New("no staged management certificate is ready for activation")
	}
	blockers, err := m.blockingAgentCountLocked(ctx)
	if err != nil {
		return err
	}
	if m.state.RequiresTrustRollout && blockers > 0 && !force {
		return fmt.Errorf("%d enabled agent(s) have not durably installed the new trust bundle", blockers)
	}
	cert, err := tls.LoadX509KeyPair(m.state.StagedCertFile, m.state.StagedKeyFile)
	if err != nil {
		return fmt.Errorf("load staged management certificate: %w", err)
	}
	previousState := m.state
	requiresTrustRollout := m.state.RequiresTrustRollout
	m.state.PreviousCertFile = m.state.ActiveCertFile
	m.state.PreviousKeyFile = m.state.ActiveKeyFile
	m.state.PreviousCAPEM = m.state.ActiveCAPEM
	m.state.ActiveCertFile = m.state.StagedCertFile
	m.state.ActiveKeyFile = m.state.StagedKeyFile
	m.state.ActiveCAPEM = m.state.StagedTargetCAPEM
	m.state.ActiveGeneration = m.state.StagedGeneration
	m.state.StagedGeneration = 0
	m.state.StagedCertFile = ""
	m.state.StagedKeyFile = ""
	m.state.StagedCertPEM = ""
	m.state.StagedTargetCAPEM = ""
	m.state.Phase = "active"
	m.state.ForcedActivation = force && blockers > 0
	m.state.ForcedRetirement = false
	m.state.ForcedCleanup = false
	if !requiresTrustRollout {
		m.state.RolloutGeneration = 0
		m.state.RolloutCAPEM = ""
		m.state.RolloutBundleSHA256 = ""
	}
	if err := m.persistLocked(); err != nil {
		m.state = previousState
		return err
	}
	m.cert.Store(&cert)
	return nil
}

func (m *ManagementTLSRuntime) prepareCleanupRolloutLocked(reason string) error {
	activeCA := normalizeCertificateBundle(m.state.ActiveCAPEM)
	if activeCA == "" {
		return errors.New("active management CA bundle is unavailable for trust cleanup")
	}
	m.state.Phase = "cleaning"
	m.state.CleanupReason = reason
	m.state.RolloutGeneration = m.nextGenerationLocked()
	m.state.RolloutCAPEM = activeCA
	m.state.RolloutBundleSHA256 = managementCAPEMSHA256(activeCA)
	m.state.RequiresTrustRollout = true
	m.state.ForcedActivation = false
	m.state.ForcedRetirement = false
	m.state.ForcedCleanup = false
	return nil
}

func (m *ManagementTLSRuntime) rollback() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.state.Phase != "active" || m.state.PreviousCertFile == "" {
		return errors.New("rollback is only available before old trust is retired")
	}
	cert, err := tls.LoadX509KeyPair(m.state.PreviousCertFile, m.state.PreviousKeyFile)
	if err != nil {
		return fmt.Errorf("load rollback management certificate: %w", err)
	}
	previousState := m.state
	retiredCertFile := m.state.ActiveCertFile
	retiredKeyFile := m.state.ActiveKeyFile
	sameTrust := managementCAPEMSHA256(normalizeCertificateBundle(m.state.PreviousCAPEM)) == managementCAPEMSHA256(normalizeCertificateBundle(m.state.ActiveCAPEM))
	m.state.ActiveCertFile = m.state.PreviousCertFile
	m.state.ActiveKeyFile = m.state.PreviousKeyFile
	m.state.ActiveCAPEM = m.state.PreviousCAPEM
	m.state.ActiveGeneration = m.nextGenerationLocked()
	m.state.StagedGeneration = 0
	m.state.StagedCertFile = ""
	m.state.StagedKeyFile = ""
	m.state.StagedCertPEM = ""
	m.state.StagedTargetCAPEM = ""
	m.state.PreviousCertFile = ""
	m.state.PreviousKeyFile = ""
	m.state.PreviousCAPEM = ""
	m.state.RolloutGeneration = 0
	m.state.RolloutCAPEM = ""
	m.state.RolloutBundleSHA256 = ""
	m.state.RequiresTrustRollout = false
	m.state.CleanupReason = ""
	m.state.ForcedActivation = false
	m.state.ForcedRetirement = false
	m.state.ForcedCleanup = false
	if sameTrust {
		m.state.Phase = "idle"
	} else if err := m.prepareCleanupRolloutLocked("rollback"); err != nil {
		m.state = previousState
		return err
	}
	m.queueManagedFilesForDeletionLocked(retiredCertFile, retiredKeyFile)
	if err := m.persistLocked(); err != nil {
		m.state = previousState
		return err
	}
	m.cert.Store(&cert)
	m.retryPendingFileCleanupLocked()
	return nil
}

func (m *ManagementTLSRuntime) beginRetirement(ctx context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.state.Phase != "active" {
		return errors.New("old trust can only be retired after activation")
	}
	previousState := m.state
	if managementCAPEMSHA256(normalizeCertificateBundle(m.state.PreviousCAPEM)) == managementCAPEMSHA256(normalizeCertificateBundle(m.state.ActiveCAPEM)) {
		retiredCertFile := m.state.PreviousCertFile
		retiredKeyFile := m.state.PreviousKeyFile
		m.state.Phase = "idle"
		m.state.PreviousCertFile = ""
		m.state.PreviousKeyFile = ""
		m.state.PreviousCAPEM = ""
		m.state.RequiresTrustRollout = false
		m.state.CleanupReason = ""
		m.state.ForcedActivation = false
		m.state.ForcedRetirement = false
		m.state.ForcedCleanup = false
		m.queueManagedFilesForDeletionLocked(retiredCertFile, retiredKeyFile)
		if err := m.persistLocked(); err != nil {
			m.state = previousState
			return err
		}
		m.retryPendingFileCleanupLocked()
		return nil
	}
	blockers, err := m.blockingAgentCountLocked(ctx)
	if err != nil {
		return err
	}
	if blockers > 0 {
		return fmt.Errorf("%d enabled agent(s) must install dual trust before old trust can be retired", blockers)
	}
	m.state.Phase = "retiring"
	m.state.RolloutGeneration = m.nextGenerationLocked()
	m.state.RolloutCAPEM = normalizeCertificateBundle(m.state.ActiveCAPEM)
	m.state.RolloutBundleSHA256 = managementCAPEMSHA256(m.state.RolloutCAPEM)
	m.state.RequiresTrustRollout = true
	m.state.CleanupReason = ""
	m.state.ForcedActivation = false
	m.state.ForcedRetirement = false
	m.state.ForcedCleanup = false
	if err := m.persistLocked(); err != nil {
		m.state = previousState
		return err
	}
	return nil
}

func (m *ManagementTLSRuntime) finalizeRetirement(ctx context.Context, force bool) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.state.Phase != "retiring" {
		return errors.New("management trust retirement is not in progress")
	}
	blockers, err := m.blockingAgentCountLocked(ctx)
	if err != nil {
		return err
	}
	if blockers > 0 && !force {
		return fmt.Errorf("%d enabled agent(s) have not durably removed old trust", blockers)
	}
	previousState := m.state
	retiredCertFile := m.state.PreviousCertFile
	retiredKeyFile := m.state.PreviousKeyFile
	m.state.TrustManaged = true
	m.state.DesiredTrustGeneration = m.state.RolloutGeneration
	m.state.DesiredTrustCAPEM = normalizeCertificateBundle(m.state.ActiveCAPEM)
	m.state.DesiredTrustBundleSHA256 = managementCAPEMSHA256(m.state.DesiredTrustCAPEM)
	m.state.Phase = "idle"
	m.state.PreviousCertFile = ""
	m.state.PreviousKeyFile = ""
	m.state.PreviousCAPEM = ""
	m.state.RolloutGeneration = 0
	m.state.RolloutCAPEM = ""
	m.state.RolloutBundleSHA256 = ""
	m.state.RequiresTrustRollout = false
	m.state.CleanupReason = ""
	m.state.ForcedActivation = false
	m.state.ForcedRetirement = force && blockers > 0
	m.state.ForcedCleanup = false
	m.queueManagedFilesForDeletionLocked(retiredCertFile, retiredKeyFile)
	if err := m.persistLocked(); err != nil {
		m.state = previousState
		return err
	}
	m.retryPendingFileCleanupLocked()
	return nil
}

func (m *ManagementTLSRuntime) cancel() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.state.Phase != "distributing" {
		return errors.New("only a staged, unactivated rotation can be cancelled")
	}
	previousState := m.state
	stagedCertFile := m.state.StagedCertFile
	stagedKeyFile := m.state.StagedKeyFile
	if m.state.RequiresTrustRollout {
		if err := m.prepareCleanupRolloutLocked("cancel"); err != nil {
			return err
		}
	} else {
		m.state.Phase = "idle"
		m.state.RolloutGeneration = 0
		m.state.RolloutCAPEM = ""
		m.state.RolloutBundleSHA256 = ""
		m.state.RequiresTrustRollout = false
		m.state.CleanupReason = ""
	}
	m.state.StagedGeneration = 0
	m.state.StagedCertFile = ""
	m.state.StagedKeyFile = ""
	m.state.StagedCertPEM = ""
	m.state.StagedTargetCAPEM = ""
	m.state.ForcedActivation = false
	m.state.ForcedRetirement = false
	m.state.ForcedCleanup = false
	m.queueManagedFilesForDeletionLocked(stagedCertFile, stagedKeyFile)
	if err := m.persistLocked(); err != nil {
		m.state = previousState
		return err
	}
	m.retryPendingFileCleanupLocked()
	return nil
}

func (m *ManagementTLSRuntime) finalizeCleanup(ctx context.Context, force bool) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.state.Phase != "cleaning" {
		return errors.New("management trust cleanup is not in progress")
	}
	blockers, err := m.blockingAgentCountLocked(ctx)
	if err != nil {
		return err
	}
	if blockers > 0 && !force {
		return fmt.Errorf("%d enabled compatible agent(s) have not durably removed abandoned trust", blockers)
	}
	previousState := m.state
	m.state.TrustManaged = true
	m.state.DesiredTrustGeneration = m.state.RolloutGeneration
	m.state.DesiredTrustCAPEM = normalizeCertificateBundle(m.state.ActiveCAPEM)
	m.state.DesiredTrustBundleSHA256 = managementCAPEMSHA256(m.state.DesiredTrustCAPEM)
	m.state.Phase = "idle"
	m.state.RolloutGeneration = 0
	m.state.RolloutCAPEM = ""
	m.state.RolloutBundleSHA256 = ""
	m.state.RequiresTrustRollout = false
	m.state.CleanupReason = ""
	m.state.ForcedActivation = false
	m.state.ForcedRetirement = false
	m.state.ForcedCleanup = force && blockers > 0
	if err := m.persistLocked(); err != nil {
		m.state = previousState
		return err
	}
	m.retryPendingFileCleanupLocked()
	return nil
}

func (m *ManagementTLSRuntime) trustUpdate(status *p2pstreamv1.ManagementTrustStatus) *p2pstreamv1.ManagementTrustUpdate {
	m.mu.RLock()
	defer m.mu.RUnlock()
	generation := m.state.RolloutGeneration
	caPEM := m.state.RolloutCAPEM
	digest := m.state.RolloutBundleSHA256
	requireExactGeneration := m.state.RequiresTrustRollout
	if !requireExactGeneration {
		if !m.state.TrustManaged {
			return nil
		}
		generation = m.state.DesiredTrustGeneration
		caPEM = m.state.DesiredTrustCAPEM
		digest = m.state.DesiredTrustBundleSHA256
	}
	if generation == 0 || caPEM == "" || digest == "" {
		return nil
	}
	if status != nil && status.State == p2pstreamv1.ManagementTrustInstallState_MANAGEMENT_TRUST_INSTALL_STATE_READY &&
		status.InstalledBundleSha256 == digest && (!requireExactGeneration || status.InstalledGeneration == generation) {
		return nil
	}
	hostname := ""
	if parsed, err := url.Parse(m.cfg.ManagementDefaultURL); err == nil {
		hostname = parsed.Hostname()
	}
	serverPEM := ""
	if m.state.Phase == "distributing" && m.state.RequiresTrustRollout {
		serverPEM = m.state.StagedCertPEM
	}
	if serverPEM == "" {
		if raw, err := os.ReadFile(m.state.ActiveCertFile); err == nil {
			serverPEM = string(raw)
		}
	}
	return &p2pstreamv1.ManagementTrustUpdate{Generation: generation, CaBundlePem: caPEM, BundleSha256: digest, ServerCertificatePem: serverPEM, ManagementHostname: hostname}
}

type managementTrustReportRow struct {
	Generation   uint64
	Digest       string
	State        string
	ErrorDetail  string
	AgentVersion string
	Capabilities []string
	ReportedAt   time.Time
}

func (m *ManagementTLSRuntime) recordTrustReport(ctx context.Context, agentID int64, status *p2pstreamv1.ManagementTrustStatus) error {
	if m == nil || m.db == nil || status == nil {
		return nil
	}
	detail := truncateProxyRequestContextValue(status.ErrorDetail, 512)
	version := truncateProxyRequestContextValue(status.AgentVersion, 128)
	digest := strings.ToLower(strings.TrimSpace(status.InstalledBundleSha256))
	if len(digest) != sha256.Size*2 {
		digest = ""
	} else if _, err := hex.DecodeString(digest); err != nil {
		digest = ""
	}
	generation := status.InstalledGeneration
	if generation > uint64(1<<63-1) {
		generation = 0
	}
	cleanCapabilities := make([]string, 0, min(len(status.Capabilities), 32))
	for _, capability := range status.Capabilities {
		capability = truncateProxyRequestContextValue(capability, 64)
		if capability == "" {
			continue
		}
		cleanCapabilities = append(cleanCapabilities, capability)
		if len(cleanCapabilities) == 32 {
			break
		}
	}
	capabilities, _ := json.Marshal(cleanCapabilities)
	_, err := m.db.ExecContext(ctx, `INSERT INTO management_agent_trust_reports
        (agent_id, installed_generation, installed_bundle_sha256, install_state, error_code, error_detail, agent_version, capabilities_json, reported_at)
        VALUES (?, ?, ?, ?, ?, ?, ?, ?, CURRENT_TIMESTAMP)
        ON CONFLICT(agent_id) DO UPDATE SET installed_generation=excluded.installed_generation,
        installed_bundle_sha256=excluded.installed_bundle_sha256, install_state=excluded.install_state,
        error_code=excluded.error_code, error_detail=excluded.error_detail, agent_version=excluded.agent_version,
        capabilities_json=excluded.capabilities_json, reported_at=CURRENT_TIMESTAMP`, agentID, generation,
		digest, status.State.String(), status.ErrorCode.String(), detail,
		version, string(capabilities))
	return err
}

func (m *ManagementTLSRuntime) reports(ctx context.Context) (map[int64]managementTrustReportRow, error) {
	result := make(map[int64]managementTrustReportRow)
	if m == nil || m.db == nil {
		return result, nil
	}
	rows, err := m.db.QueryContext(ctx, `SELECT agent_id, installed_generation, installed_bundle_sha256, install_state,
        error_detail, agent_version, capabilities_json, reported_at FROM management_agent_trust_reports`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id, generation int64
		var row managementTrustReportRow
		var capabilities string
		if err := rows.Scan(&id, &generation, &row.Digest, &row.State, &row.ErrorDetail, &row.AgentVersion, &capabilities, &row.ReportedAt); err != nil {
			return nil, err
		}
		if generation > 0 {
			row.Generation = uint64(generation)
		}
		_ = json.Unmarshal([]byte(capabilities), &row.Capabilities)
		result[id] = row
	}
	return result, rows.Err()
}

func containsCapability(capabilities []string, wanted string) bool {
	for _, capability := range capabilities {
		if capability == wanted {
			return true
		}
	}
	return false
}

func (m *ManagementTLSRuntime) blockingAgentCountLocked(ctx context.Context) (int, error) {
	if !m.state.RequiresTrustRollout {
		return 0, nil
	}
	if m.db == nil {
		return 0, errors.New("management TLS rotation requires a database")
	}
	agents, err := m.db.ListAgents(ctx)
	if err != nil {
		return 0, err
	}
	reports, err := m.reports(ctx)
	if err != nil {
		return 0, err
	}
	blockers := 0
	for _, agent := range agents {
		if agent.Enabled == 0 {
			continue
		}
		report, ok := reports[agent.ID]
		if m.state.Phase == "cleaning" && (!ok || !containsCapability(report.Capabilities, managementTrustCapability)) {
			// Agents without the capability could not have installed the abandoned
			// dual bundle, so they are not cleanup participants.
			continue
		}
		if !ok || !containsCapability(report.Capabilities, managementTrustCapability) || report.State != p2pstreamv1.ManagementTrustInstallState_MANAGEMENT_TRUST_INSTALL_STATE_READY.String() || report.Generation != m.state.RolloutGeneration || report.Digest != m.state.RolloutBundleSHA256 {
			blockers++
		}
	}
	return blockers, nil
}

func certificateSummary(certFile string) *p2pstreamv1.ManagementTlsCertificateSummary {
	raw, err := os.ReadFile(certFile)
	if err != nil {
		return nil
	}
	block, _ := pem.Decode(raw)
	if block == nil {
		return nil
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return nil
	}
	digest := sha256.Sum256(cert.Raw)
	ips := make([]string, 0, len(cert.IPAddresses))
	for _, ip := range cert.IPAddresses {
		ips = append(ips, ip.String())
	}
	return &p2pstreamv1.ManagementTlsCertificateSummary{Sha256: hex.EncodeToString(digest[:]), Subject: cert.Subject.String(), Issuer: cert.Issuer.String(), NotBeforeUnixMillis: cert.NotBefore.UnixMilli(), NotAfterUnixMillis: cert.NotAfter.UnixMilli(), DnsNames: cert.DNSNames, IpAddresses: ips}
}

func (m *ManagementTLSRuntime) snapshot(ctx context.Context, app *App) (*p2pstreamv1.ManagementTlsRotation, error) {
	if m == nil {
		return &p2pstreamv1.ManagementTlsRotation{}, nil
	}
	m.mu.Lock()
	m.retryPendingFileCleanupLocked()
	state := m.state
	enabled := m.enabled
	cleanupWarning := m.cleanupWarning
	m.mu.Unlock()
	result := &p2pstreamv1.ManagementTlsRotation{
		TlsEnabled:               enabled,
		ManagedRotationAvailable: enabled && m.db != nil,
		ActiveGeneration:         state.ActiveGeneration,
		RolloutGeneration:        state.RolloutGeneration,
		RolloutBundleSha256:      state.RolloutBundleSHA256,
		ActiveCertificate:        certificateSummary(state.ActiveCertFile),
		ForcedActivation:         state.ForcedActivation,
		ForcedRetirement:         state.ForcedRetirement,
		ForcedCleanup:            state.ForcedCleanup,
		TrustManagementActive:    state.TrustManaged,
		DesiredTrustGeneration:   state.DesiredTrustGeneration,
		DesiredTrustBundleSha256: state.DesiredTrustBundleSHA256,
		SecretCleanupPending:     len(state.PendingDeleteFiles) > 0,
		StatusMessage:            cleanupWarning,
	}
	switch state.Phase {
	case "idle":
		result.Phase = p2pstreamv1.ManagementTlsRotationPhase_MANAGEMENT_TLS_ROTATION_PHASE_IDLE
	case "distributing":
		result.Phase = p2pstreamv1.ManagementTlsRotationPhase_MANAGEMENT_TLS_ROTATION_PHASE_DISTRIBUTING
	case "active":
		result.Phase = p2pstreamv1.ManagementTlsRotationPhase_MANAGEMENT_TLS_ROTATION_PHASE_ACTIVE
	case "retiring":
		result.Phase = p2pstreamv1.ManagementTlsRotationPhase_MANAGEMENT_TLS_ROTATION_PHASE_RETIRING
	case "cleaning":
		result.Phase = p2pstreamv1.ManagementTlsRotationPhase_MANAGEMENT_TLS_ROTATION_PHASE_CLEANING_UP
	}
	switch state.CleanupReason {
	case "cancel":
		result.CleanupReason = p2pstreamv1.ManagementTlsCleanupReason_MANAGEMENT_TLS_CLEANUP_REASON_CANCELLED_STAGING
	case "rollback":
		result.CleanupReason = p2pstreamv1.ManagementTlsCleanupReason_MANAGEMENT_TLS_CLEANUP_REASON_ROLLED_BACK_CERTIFICATE
	}
	if state.StagedCertFile != "" {
		result.StagedCertificate = certificateSummary(state.StagedCertFile)
	}
	if state.RequiresTrustRollout {
		result.RepairCaBundlePem = state.RolloutCAPEM
	} else if state.TrustManaged {
		result.RepairCaBundlePem = state.DesiredTrustCAPEM
	}
	if m.db == nil {
		return result, nil
	}
	agents, err := m.db.ListAgents(ctx)
	if err != nil {
		return nil, err
	}
	reports, err := m.reports(ctx)
	if err != nil {
		return nil, err
	}
	expectedGeneration := state.RolloutGeneration
	expectedDigest := state.RolloutBundleSHA256
	requireExactGeneration := state.RequiresTrustRollout
	if !requireExactGeneration && state.TrustManaged {
		expectedGeneration = state.DesiredTrustGeneration
		expectedDigest = state.DesiredTrustBundleSHA256
	}
	for _, agent := range agents {
		report, hasReport := reports[agent.ID]
		hasCapability := hasReport && containsCapability(report.Capabilities, managementTrustCapability)
		ready := hasCapability && report.State == p2pstreamv1.ManagementTrustInstallState_MANAGEMENT_TRUST_INSTALL_STATE_READY.String() && report.Digest == expectedDigest && (!requireExactGeneration || report.Generation == expectedGeneration)
		included := agent.Enabled != 0 && state.RequiresTrustRollout && !(state.Phase == "cleaning" && !hasCapability)
		needsTrustAttention := expectedDigest != "" && !ready && !(state.Phase == "cleaning" && !hasCapability)
		rollout := &p2pstreamv1.ManagementTlsAgentRollout{AgentId: agent.ID, AgentPublicId: agent.PublicID, AgentName: agent.Name, Enabled: agent.Enabled != 0, Connected: app != nil && app.AgentHub != nil && app.AgentHub.connectedByID(agent.ID) != nil, InstalledGeneration: report.Generation, InstalledBundleSha256: report.Digest, AgentVersion: report.AgentVersion, ErrorDetail: report.ErrorDetail, NeedsTrustAttention: needsTrustAttention, IncludedInRollout: included}
		if !report.ReportedAt.IsZero() {
			rollout.ReportedAtUnixMillis = report.ReportedAt.UnixMilli()
		}
		switch {
		case agent.Enabled == 0:
			rollout.State = p2pstreamv1.ManagementTlsAgentRolloutState_MANAGEMENT_TLS_AGENT_ROLLOUT_STATE_DISABLED
		case expectedDigest == "":
			rollout.State = p2pstreamv1.ManagementTlsAgentRolloutState_MANAGEMENT_TLS_AGENT_ROLLOUT_STATE_READY
		case !hasCapability:
			rollout.State = p2pstreamv1.ManagementTlsAgentRolloutState_MANAGEMENT_TLS_AGENT_ROLLOUT_STATE_INCOMPATIBLE
		case report.State == p2pstreamv1.ManagementTrustInstallState_MANAGEMENT_TRUST_INSTALL_STATE_FAILED.String():
			rollout.State = p2pstreamv1.ManagementTlsAgentRolloutState_MANAGEMENT_TLS_AGENT_ROLLOUT_STATE_FAILED
		case ready:
			rollout.State = p2pstreamv1.ManagementTlsAgentRolloutState_MANAGEMENT_TLS_AGENT_ROLLOUT_STATE_READY
		default:
			rollout.State = p2pstreamv1.ManagementTlsAgentRolloutState_MANAGEMENT_TLS_AGENT_ROLLOUT_STATE_PENDING
		}
		if state.ForcedActivation && state.Phase == "active" && agent.Enabled != 0 && rollout.State != p2pstreamv1.ManagementTlsAgentRolloutState_MANAGEMENT_TLS_AGENT_ROLLOUT_STATE_READY {
			rollout.State = p2pstreamv1.ManagementTlsAgentRolloutState_MANAGEMENT_TLS_AGENT_ROLLOUT_STATE_STRANDED
		}
		if rollout.NeedsTrustAttention {
			result.TrustAttentionAgentCount++
		}
		result.Agents = append(result.Agents, rollout)
	}
	sort.Slice(result.Agents, func(i, j int) bool { return result.Agents[i].AgentName < result.Agents[j].AgentName })
	for _, agent := range result.Agents {
		if agent.IncludedInRollout && agent.State != p2pstreamv1.ManagementTlsAgentRolloutState_MANAGEMENT_TLS_AGENT_ROLLOUT_STATE_READY {
			result.BlockingAgentCount++
		}
	}
	return result, nil
}

func managementTLSConnectError(err error) error {
	if err == nil {
		return nil
	}
	return connect.NewError(connect.CodeFailedPrecondition, err)
}
