package agent

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"connectrpc.com/connect"
	"github.com/hashicorp/yamux"
	"github.com/rs/zerolog/log"

	p2pstreamv1 "p2pstream/gen/proto/p2pstream/v1"
	"p2pstream/gen/proto/p2pstream/v1/p2pstreamv1connect"
	"p2pstream/internal/buildinfo"
	"p2pstream/internal/sysmetrics"
	"p2pstream/internal/tunnel"
)

var (
	activeRequests   atomic.Int32
	reqSuccess       atomic.Int32
	reqClientError   atomic.Int32
	reqServerError   atomic.Int32
	reqInternalError atomic.Int32
	bytesReceived    atomic.Uint64
	bytesSent        atomic.Uint64
)

var (
	agentStableConnectionInterval = 20 * time.Second
	agentReconnectBackoffMin      = time.Second
	agentReconnectBackoffMax      = 30 * time.Second
	agentTunnelOpenRequestTimeout = 10 * time.Second
	agentTunnelDialTimeout        = 10 * time.Second
	agentTunnelDialNetwork        = dialTunnelNetwork
	agentStatsReportInterval      = 5 * time.Second
	agentStatsReportTimeout       = 10 * time.Second
)

const (
	agentTunnelResponseHeaderTimeout = 15 * time.Second
	agentCapacityResponseTimeout     = time.Second
)

type agentStatsReportClient interface {
	ReportStats(context.Context, *connect.Request[p2pstreamv1.AgentStatsRequest]) (*connect.Response[p2pstreamv1.AgentStatsResponse], error)
}

type Options struct {
	ManagementURL               string
	PublicID                    string
	Name                        string
	Token                       string
	ManagementCAFile            string
	ManagementCAPEMBase64       string
	ManagementTrustFile         string
	TLSCertFile                 string
	TLSKeyFile                  string
	AllowInsecureManagement     bool
	AllowTargets                []string
	AllowAnyTarget              bool
	TunnelMaxStreamWindowBytes  int64
	TunnelMaxConcurrentRequests int64
}

// Run is the main entry point to start the agent loop
func Run(opts Options) error {
	return RunContext(context.Background(), opts)
}

// RunContext starts the agent loop until the provided context is cancelled.
func RunContext(ctx context.Context, opts Options) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := validateOptions(opts); err != nil {
		return err
	}
	destinationPolicy, err := newAgentDestinationPolicy(opts.AllowTargets, opts.AllowAnyTarget)
	if err != nil {
		return err
	}
	trustStore, err := newManagementTrustStore(opts)
	if err != nil {
		return err
	}
	managementClient, err := managementHTTPClientWithTrust(opts, trustStore)
	if err != nil {
		return err
	}
	tunnelURL, err := managementTunnelURL(opts.ManagementURL)
	if err != nil {
		return err
	}
	tunnelClient, err := managementTunnelHTTPClient(managementClient)
	if err != nil {
		return err
	}

	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	statsDone := make(chan struct{})
	go func() {
		defer close(statsDone)
		startStatsReporter(runCtx, managementClient, opts.ManagementURL, opts.PublicID, opts.Token, trustStore)
	}()
	defer func() {
		cancel()
		<-statsDone
	}()

	backoff := agentReconnectBackoffMin
	for {
		if err := runCtx.Err(); err != nil {
			return err
		}
		log.Info().Str("tunnel_url", tunnelURL).Msg("Attempting to connect to management server...")

		connectedAt := time.Now()
		err := connectAndServe(
			runCtx,
			tunnelClient,
			tunnelURL,
			opts.PublicID,
			opts.Name,
			opts.Token,
			destinationPolicy,
			opts.TunnelMaxStreamWindowBytes,
			opts.TunnelMaxConcurrentRequests,
		)
		if err != nil {
			if runCtx.Err() != nil {
				return runCtx.Err()
			}
			log.Warn().Err(err).Msg("Disconnected")
		}
		if time.Since(connectedAt) >= agentStableConnectionInterval {
			backoff = agentReconnectBackoffMin
		}

		sleep := jitterAgentReconnectBackoff(backoff)
		log.Info().Dur("retry_in", sleep).Msg("Waiting before reconnect")
		timer := time.NewTimer(sleep)
		select {
		case <-timer.C:
		case <-runCtx.Done():
			timer.Stop()
			return runCtx.Err()
		}
		backoff = nextAgentReconnectBackoff(backoff)
	}
}

func nextAgentReconnectBackoff(current time.Duration) time.Duration {
	if current <= 0 {
		return agentReconnectBackoffMin
	}
	next := current * 2
	if next > agentReconnectBackoffMax {
		return agentReconnectBackoffMax
	}
	return next
}

func jitterAgentReconnectBackoff(base time.Duration) time.Duration {
	if base <= 0 {
		return 0
	}
	jitter := int64(float64(base) * 0.2)
	if jitter <= 0 {
		return base
	}
	delta := rand.Int63n(jitter*2+1) - jitter
	return base + time.Duration(delta)
}

func validateOptions(opts Options) error {
	if strings.TrimSpace(opts.ManagementURL) == "" {
		return fmt.Errorf("management URL is required")
	}
	parsed, err := url.Parse(strings.TrimSpace(opts.ManagementURL))
	if err != nil {
		return fmt.Errorf("invalid management URL: %w", err)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return fmt.Errorf("unsupported management URL scheme %q", parsed.Scheme)
	}
	if parsed.Scheme == "http" && !opts.AllowInsecureManagement {
		return fmt.Errorf("insecure HTTP management URL rejected; use https or set AGENT_ALLOW_INSECURE_MANAGEMENT=true")
	}
	hasClientCert := strings.TrimSpace(opts.TLSCertFile) != ""
	hasClientKey := strings.TrimSpace(opts.TLSKeyFile) != ""
	if hasClientCert != hasClientKey {
		return fmt.Errorf("AGENT_TLS_CERT_FILE and AGENT_TLS_KEY_FILE must be set together")
	}
	if parsed.Scheme != "https" && (hasClientCert || strings.TrimSpace(opts.ManagementCAFile) != "" || strings.TrimSpace(opts.ManagementCAPEMBase64) != "") {
		return fmt.Errorf("agent TLS files require an https management URL")
	}
	if err := tunnel.ValidateAggregateStreamWindowBudget(opts.TunnelMaxStreamWindowBytes, opts.TunnelMaxConcurrentRequests); err != nil {
		return err
	}
	if _, err := newAgentDestinationPolicy(opts.AllowTargets, opts.AllowAnyTarget); err != nil {
		return err
	}
	return nil
}

func managementTunnelURL(mgmtURL string) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(mgmtURL))
	if err != nil {
		return "", fmt.Errorf("invalid management URL: %w", err)
	}
	switch parsed.Scheme {
	case "http", "https":
	default:
		return "", fmt.Errorf("unsupported management URL scheme %q", parsed.Scheme)
	}
	parsed.Path = strings.TrimRight(parsed.Path, "/") + tunnel.BootstrapPath
	parsed.RawQuery = ""
	parsed.Fragment = ""
	return parsed.String(), nil
}

func managementHTTPClient(opts Options) (*http.Client, error) {
	trustStore, err := newManagementTrustStore(opts)
	if err != nil {
		return nil, err
	}
	return managementHTTPClientWithTrust(opts, trustStore)
}

type managementTrustDiskState struct {
	Generation uint64 `json:"generation"`
	SHA256     string `json:"sha256"`
}

type managementTrustStore struct {
	mu             sync.RWMutex
	managementHost string
	allowUpdates   bool
	persistFile    string
	bundlePEM      string
	status         p2pstreamv1.ManagementTrustStatus
	roots          atomic.Pointer[x509.CertPool]
	transportsMu   sync.Mutex
	transports     []*http.Transport
}

func newManagementTrustStore(opts Options) (*managementTrustStore, error) {
	parsed, err := url.Parse(strings.TrimSpace(opts.ManagementURL))
	if err != nil {
		return nil, fmt.Errorf("parse management URL for trust store: %w", err)
	}
	store := &managementTrustStore{
		managementHost: parsed.Hostname(),
		allowUpdates:   parsed.Scheme == "https",
		persistFile:    strings.TrimSpace(opts.ManagementTrustFile),
		status: p2pstreamv1.ManagementTrustStatus{
			State:        p2pstreamv1.ManagementTrustInstallState_MANAGEMENT_TRUST_INSTALL_STATE_READY,
			AgentVersion: buildinfo.Version,
			Capabilities: []string{"management-trust-v1"},
		},
	}
	if !store.allowUpdates {
		store.status.State = p2pstreamv1.ManagementTrustInstallState_MANAGEMENT_TRUST_INSTALL_STATE_UNSUPPORTED
		store.status.Capabilities = nil
	}
	var bundles []string
	if caFile := strings.TrimSpace(opts.ManagementCAFile); caFile != "" {
		raw, err := os.ReadFile(caFile)
		if err != nil {
			return nil, fmt.Errorf("read management CA file: %w", err)
		}
		bundles = append(bundles, string(raw))
	}
	if caBase64 := strings.TrimSpace(opts.ManagementCAPEMBase64); caBase64 != "" {
		raw, err := base64.StdEncoding.DecodeString(caBase64)
		if err != nil {
			raw, err = base64.RawStdEncoding.DecodeString(caBase64)
		}
		if err != nil {
			return nil, fmt.Errorf("decode MANAGEMENT_CA_PEM_BASE64: %w", err)
		}
		bundles = append(bundles, string(raw))
	}
	if store.persistFile != "" {
		if raw, err := os.ReadFile(store.persistFile); err == nil {
			bundles = []string{string(raw)}
			var disk managementTrustDiskState
			if stateRaw, stateErr := os.ReadFile(store.persistFile + ".state.json"); stateErr == nil && json.Unmarshal(stateRaw, &disk) == nil {
				store.status.InstalledGeneration = disk.Generation
				store.status.InstalledBundleSha256 = disk.SHA256
			}
		} else if !os.IsNotExist(err) {
			return nil, fmt.Errorf("read durable management trust bundle: %w", err)
		}
	}
	validatedBundles := make([]string, 0, len(bundles))
	for _, bundle := range bundles {
		if strings.TrimSpace(bundle) == "" {
			continue
		}
		normalized, err := validateAgentCABundle(bundle)
		if err != nil {
			return nil, err
		}
		validatedBundles = append(validatedBundles, normalized)
	}
	store.bundlePEM = normalizeAgentCertificateBundle(validatedBundles...)
	roots, err := managementRootPool(store.bundlePEM)
	if err != nil {
		return nil, err
	}
	store.roots.Store(roots)
	if store.bundlePEM != "" {
		digest := sha256.Sum256([]byte(store.bundlePEM))
		actual := hex.EncodeToString(digest[:])
		if store.status.InstalledBundleSha256 == "" || store.status.InstalledBundleSha256 != actual {
			store.status.InstalledGeneration = 0
			store.status.InstalledBundleSha256 = actual
		}
	} else {
		store.status.InstalledGeneration = 0
		store.status.InstalledBundleSha256 = ""
	}
	return store, nil
}

func normalizeAgentCertificateBundle(bundles ...string) string {
	seen := make(map[string]struct{})
	var result strings.Builder
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
			digest := sha256.Sum256(block.Bytes)
			key := hex.EncodeToString(digest[:])
			if _, ok := seen[key]; ok {
				continue
			}
			seen[key] = struct{}{}
			result.Write(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: block.Bytes}))
		}
	}
	return result.String()
}

func validateAgentCABundle(bundle string) (string, error) {
	rest := []byte(bundle)
	var normalized strings.Builder
	found := false
	for len(bytes.TrimSpace(rest)) > 0 {
		rest = bytes.TrimSpace(rest)
		if !bytes.HasPrefix(rest, []byte("-----BEGIN CERTIFICATE-----")) {
			return "", errors.New("management trust bundle contains data other than PEM certificates")
		}
		block, remaining := pem.Decode(rest)
		if block == nil || block.Type != "CERTIFICATE" || len(block.Headers) != 0 {
			return "", errors.New("management trust bundle contains an invalid PEM certificate block")
		}
		rest = remaining
		cert, err := x509.ParseCertificate(block.Bytes)
		if err != nil {
			return "", fmt.Errorf("parse management CA certificate: %w", err)
		}
		if !cert.IsCA {
			return "", fmt.Errorf("management trust certificate %q is not a CA", cert.Subject.String())
		}
		found = true
		normalized.Write(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: block.Bytes}))
	}
	if !found {
		return "", errors.New("management trust bundle contains no PEM certificates")
	}
	return normalizeAgentCertificateBundle(normalized.String()), nil
}

func managementRootPool(customPEM string) (*x509.CertPool, error) {
	roots, err := x509.SystemCertPool()
	if err != nil {
		roots = x509.NewCertPool()
	}
	if customPEM != "" && !roots.AppendCertsFromPEM([]byte(customPEM)) {
		return nil, errors.New("management trust bundle contains no PEM certificates")
	}
	return roots, nil
}

func (s *managementTrustStore) verifyConnection(state tls.ConnectionState) error {
	if len(state.PeerCertificates) == 0 {
		return errors.New("management server presented no certificate")
	}
	roots := s.roots.Load()
	if roots == nil {
		return errors.New("management trust roots are unavailable")
	}
	intermediates := x509.NewCertPool()
	for _, cert := range state.PeerCertificates[1:] {
		intermediates.AddCert(cert)
	}
	_, err := state.PeerCertificates[0].Verify(x509.VerifyOptions{
		Roots: roots, Intermediates: intermediates, DNSName: s.managementHost,
		KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	})
	return err
}

func (s *managementTrustStore) registerTransport(transport *http.Transport) {
	if s == nil || transport == nil {
		return
	}
	s.transportsMu.Lock()
	s.transports = append(s.transports, transport)
	s.transportsMu.Unlock()
}

func (s *managementTrustStore) closeIdleConnections() {
	s.transportsMu.Lock()
	transports := append([]*http.Transport(nil), s.transports...)
	s.transportsMu.Unlock()
	for _, transport := range transports {
		transport.CloseIdleConnections()
	}
}

func (s *managementTrustStore) snapshot() *p2pstreamv1.ManagementTrustStatus {
	if s == nil {
		return &p2pstreamv1.ManagementTrustStatus{State: p2pstreamv1.ManagementTrustInstallState_MANAGEMENT_TRUST_INSTALL_STATE_UNSUPPORTED}
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return &p2pstreamv1.ManagementTrustStatus{
		InstalledGeneration:   s.status.InstalledGeneration,
		InstalledBundleSha256: s.status.InstalledBundleSha256,
		State:                 s.status.State,
		ErrorCode:             s.status.ErrorCode,
		ErrorDetail:           s.status.ErrorDetail,
		AgentVersion:          s.status.AgentVersion,
		Capabilities:          append([]string(nil), s.status.Capabilities...),
	}
}

func (s *managementTrustStore) fail(code p2pstreamv1.ManagementTrustErrorCode, err error) error {
	s.mu.Lock()
	s.status.State = p2pstreamv1.ManagementTrustInstallState_MANAGEMENT_TRUST_INSTALL_STATE_FAILED
	s.status.ErrorCode = code
	s.status.ErrorDetail = err.Error()
	s.mu.Unlock()
	return err
}

func (s *managementTrustStore) apply(update *p2pstreamv1.ManagementTrustUpdate) error {
	if s == nil || update == nil {
		return nil
	}
	if !s.allowUpdates {
		return errors.New("management trust updates require an authenticated HTTPS management connection")
	}
	s.mu.RLock()
	alreadyInstalled := s.status.State == p2pstreamv1.ManagementTrustInstallState_MANAGEMENT_TRUST_INSTALL_STATE_READY && s.status.InstalledGeneration == update.Generation && s.status.InstalledBundleSha256 == update.BundleSha256
	installedGeneration := s.status.InstalledGeneration
	installedDigest := s.status.InstalledBundleSha256
	s.mu.RUnlock()
	if alreadyInstalled {
		return nil
	}
	if update.Generation < installedGeneration {
		if strings.TrimSpace(update.BundleSha256) == installedDigest {
			return nil
		}
		return s.fail(p2pstreamv1.ManagementTrustErrorCode_MANAGEMENT_TRUST_ERROR_CODE_INVALID_BUNDLE, fmt.Errorf("management trust update generation %d is older than installed generation %d", update.Generation, installedGeneration))
	}
	if update.Generation == 0 || strings.TrimSpace(update.CaBundlePem) == "" || strings.TrimSpace(update.ServerCertificatePem) == "" {
		return s.fail(p2pstreamv1.ManagementTrustErrorCode_MANAGEMENT_TRUST_ERROR_CODE_INVALID_BUNDLE, errors.New("management trust update is incomplete"))
	}
	normalized, err := validateAgentCABundle(update.CaBundlePem)
	if err != nil {
		return s.fail(p2pstreamv1.ManagementTrustErrorCode_MANAGEMENT_TRUST_ERROR_CODE_INVALID_BUNDLE, err)
	}
	digest := sha256.Sum256([]byte(normalized))
	digestHex := hex.EncodeToString(digest[:])
	if digestHex != strings.TrimSpace(update.BundleSha256) {
		return s.fail(p2pstreamv1.ManagementTrustErrorCode_MANAGEMENT_TRUST_ERROR_CODE_INVALID_BUNDLE, errors.New("management trust bundle digest does not match the authenticated server response"))
	}
	rootOnly := x509.NewCertPool()
	if !rootOnly.AppendCertsFromPEM([]byte(normalized)) {
		return s.fail(p2pstreamv1.ManagementTrustErrorCode_MANAGEMENT_TRUST_ERROR_CODE_INVALID_BUNDLE, errors.New("management trust update contains no parseable CA certificates"))
	}
	certBlock, rest := pem.Decode([]byte(update.ServerCertificatePem))
	if certBlock == nil || certBlock.Type != "CERTIFICATE" {
		return s.fail(p2pstreamv1.ManagementTrustErrorCode_MANAGEMENT_TRUST_ERROR_CODE_INVALID_BUNDLE, errors.New("management trust update contains no server certificate"))
	}
	leaf, err := x509.ParseCertificate(certBlock.Bytes)
	if err != nil {
		return s.fail(p2pstreamv1.ManagementTrustErrorCode_MANAGEMENT_TRUST_ERROR_CODE_INVALID_BUNDLE, fmt.Errorf("parse management server certificate: %w", err))
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
	if err := leaf.VerifyHostname(s.managementHost); err != nil {
		return s.fail(p2pstreamv1.ManagementTrustErrorCode_MANAGEMENT_TRUST_ERROR_CODE_HOSTNAME_MISMATCH, fmt.Errorf("new management certificate cannot be verified for %q: %w", s.managementHost, err))
	}
	if _, err := leaf.Verify(x509.VerifyOptions{Roots: rootOnly, Intermediates: intermediates, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}); err != nil {
		return s.fail(p2pstreamv1.ManagementTrustErrorCode_MANAGEMENT_TRUST_ERROR_CODE_INVALID_BUNDLE, fmt.Errorf("new management certificate does not verify with the supplied CA bundle: %w", err))
	}
	if strings.TrimSpace(s.persistFile) == "" {
		return s.fail(p2pstreamv1.ManagementTrustErrorCode_MANAGEMENT_TRUST_ERROR_CODE_PERSIST_FAILED, errors.New("agent has no writable MANAGEMENT_TRUST_FILE; reinstall or repair this agent before activation"))
	}
	if err := atomicWriteAgentTrustFile(s.persistFile, []byte(normalized), 0644); err != nil {
		return s.fail(p2pstreamv1.ManagementTrustErrorCode_MANAGEMENT_TRUST_ERROR_CODE_PERSIST_FAILED, fmt.Errorf("persist management trust bundle: %w", err))
	}
	diskRaw, _ := json.Marshal(managementTrustDiskState{Generation: update.Generation, SHA256: digestHex})
	if err := atomicWriteAgentTrustFile(s.persistFile+".state.json", diskRaw, 0600); err != nil {
		return s.fail(p2pstreamv1.ManagementTrustErrorCode_MANAGEMENT_TRUST_ERROR_CODE_PERSIST_FAILED, fmt.Errorf("persist management trust generation: %w", err))
	}
	readback, err := os.ReadFile(s.persistFile)
	readbackNormalized := ""
	if err == nil {
		readbackNormalized, err = validateAgentCABundle(string(readback))
	}
	if err != nil || readbackNormalized != normalized {
		if err == nil {
			err = errors.New("durable trust readback differs from requested bundle")
		}
		return s.fail(p2pstreamv1.ManagementTrustErrorCode_MANAGEMENT_TRUST_ERROR_CODE_RELOAD_FAILED, err)
	}
	stateReadback, err := os.ReadFile(s.persistFile + ".state.json")
	var persistedState managementTrustDiskState
	if err != nil || json.Unmarshal(stateReadback, &persistedState) != nil || persistedState.Generation != update.Generation || persistedState.SHA256 != digestHex {
		if err == nil {
			err = errors.New("durable trust generation readback differs from requested update")
		}
		return s.fail(p2pstreamv1.ManagementTrustErrorCode_MANAGEMENT_TRUST_ERROR_CODE_RELOAD_FAILED, err)
	}
	roots, err := managementRootPool(normalized)
	if err != nil {
		return s.fail(p2pstreamv1.ManagementTrustErrorCode_MANAGEMENT_TRUST_ERROR_CODE_RELOAD_FAILED, err)
	}
	s.roots.Store(roots)
	s.mu.Lock()
	s.bundlePEM = normalized
	s.status.InstalledGeneration = update.Generation
	s.status.InstalledBundleSha256 = digestHex
	s.status.State = p2pstreamv1.ManagementTrustInstallState_MANAGEMENT_TRUST_INSTALL_STATE_READY
	s.status.ErrorCode = p2pstreamv1.ManagementTrustErrorCode_MANAGEMENT_TRUST_ERROR_CODE_UNSPECIFIED
	s.status.ErrorDetail = ""
	s.mu.Unlock()
	s.closeIdleConnections()
	return nil
}

func atomicWriteAgentTrustFile(path string, contents []byte, mode os.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".management-trust-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(mode); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(contents); err != nil {
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
	if err := os.Rename(tmpName, path); err != nil {
		return err
	}
	directory, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}

func managementHTTPClientWithTrust(opts Options, trustStore *managementTrustStore) (*http.Client, error) {
	if err := validateOptions(opts); err != nil {
		return nil, err
	}
	parsedManagementURL, _ := url.Parse(strings.TrimSpace(opts.ManagementURL))
	if parsedManagementURL.Scheme != "https" && strings.TrimSpace(opts.ManagementCAFile) == "" &&
		strings.TrimSpace(opts.ManagementCAPEMBase64) == "" && strings.TrimSpace(opts.ManagementTrustFile) == "" &&
		strings.TrimSpace(opts.TLSCertFile) == "" &&
		strings.TrimSpace(opts.TLSKeyFile) == "" {
		return http.DefaultClient, nil
	}

	baseTransport, ok := http.DefaultTransport.(*http.Transport)
	if !ok {
		return nil, fmt.Errorf("default transport is %T, want *http.Transport", http.DefaultTransport)
	}
	transport := baseTransport.Clone()
	tlsConfig := &tls.Config{MinVersion: tls.VersionTLS12}
	if parsedManagementURL.Scheme == "https" && trustStore != nil {
		// Verification is performed in VerifyConnection against an atomically
		// replaceable root pool. This is equivalent to the standard verifier but
		// permits a running agent to durably reload a staged CA bundle.
		tlsConfig.InsecureSkipVerify = true //nolint:gosec -- full verification below
		tlsConfig.VerifyConnection = trustStore.verifyConnection
	}

	if certFile := strings.TrimSpace(opts.TLSCertFile); certFile != "" {
		cert, err := tls.LoadX509KeyPair(certFile, strings.TrimSpace(opts.TLSKeyFile))
		if err != nil {
			return nil, fmt.Errorf("load agent TLS certificate: %w", err)
		}
		tlsConfig.Certificates = []tls.Certificate{cert}
	}

	transport.TLSClientConfig = tlsConfig
	trustStore.registerTransport(transport)
	return &http.Client{Transport: transport}, nil
}

func managementTunnelHTTPClient(base *http.Client) (*http.Client, error) {
	if base == nil {
		base = http.DefaultClient
	}
	var transport *http.Transport
	if base.Transport == nil {
		defaultTransport, ok := http.DefaultTransport.(*http.Transport)
		if !ok {
			return nil, fmt.Errorf("default transport is %T, want *http.Transport", http.DefaultTransport)
		}
		transport = defaultTransport.Clone()
	} else {
		baseTransport, ok := base.Transport.(*http.Transport)
		if !ok {
			return nil, fmt.Errorf("management tunnel transport is %T, want *http.Transport", base.Transport)
		}
		transport = baseTransport.Clone()
	}
	transport.ForceAttemptHTTP2 = false
	transport.TLSNextProto = map[string]func(string, *tls.Conn) http.RoundTripper{}
	if transport.ResponseHeaderTimeout == 0 {
		transport.ResponseHeaderTimeout = agentTunnelResponseHeaderTimeout
	}
	protocols := new(http.Protocols)
	protocols.SetHTTP1(true)
	transport.Protocols = protocols
	if transport.TLSClientConfig == nil {
		transport.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS12}
	} else {
		transport.TLSClientConfig = transport.TLSClientConfig.Clone()
	}
	transport.TLSClientConfig.NextProtos = []string{"http/1.1"}
	return &http.Client{
		Transport:     transport,
		CheckRedirect: base.CheckRedirect,
		Jar:           base.Jar,
	}, nil
}

func connectAndServe(ctx context.Context, client *http.Client, tunnelURL string, agentPublicID string, agentName string, agentToken string, destinationPolicy *agentDestinationPolicy, maxStreamWindowSizeBytes int64, maxConcurrentRequests int64) error {
	if ctx == nil {
		ctx = context.Background()
	}
	serveCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	req, err := http.NewRequestWithContext(serveCtx, http.MethodGet, tunnelURL, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+agentToken)
	req.Header.Set("X-P2PStream-Agent-ID", agentPublicID)
	req.Header.Set("X-P2PStream-Agent-Name", agentName)
	req.Header.Set("Connection", "Upgrade")
	req.Header.Set("Upgrade", tunnel.UpgradeToken)
	req.Header.Set(tunnel.TunnelVersionHeader, strconv.Itoa(tunnel.ProtocolVersion))

	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("failed to dial tunnel: %w", err)
	}
	if resp.StatusCode != http.StatusSwitchingProtocols {
		body := ""
		if resp.Body != nil {
			data, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
			body = strings.TrimSpace(string(data))
			_ = resp.Body.Close()
		}
		if body != "" {
			return fmt.Errorf("agent tunnel upgrade failed: status %d: %s", resp.StatusCode, body)
		}
		return fmt.Errorf("agent tunnel upgrade failed: status %d", resp.StatusCode)
	}
	if got := resp.Header.Get("Upgrade"); !strings.EqualFold(got, tunnel.UpgradeToken) {
		_ = resp.Body.Close()
		return fmt.Errorf("agent tunnel upgrade response header = %q", got)
	}
	rwc, ok := resp.Body.(io.ReadWriteCloser)
	if !ok {
		_ = resp.Body.Close()
		return fmt.Errorf("agent tunnel response body is %T, want io.ReadWriteCloser", resp.Body)
	}
	yamuxConfig, err := tunnel.NewYamuxConfig(nil, maxStreamWindowSizeBytes)
	if err != nil {
		_ = rwc.Close()
		return fmt.Errorf("invalid tunnel yamux configuration: %w", err)
	}
	session, err := yamux.Client(rwc, yamuxConfig)
	if err != nil {
		_ = rwc.Close()
		return fmt.Errorf("failed to initialize tunnel session: %w", err)
	}
	defer session.Close()

	log.Info().Msg("Connected tunnel successfully")

	go func() {
		<-serveCtx.Done()
		_ = session.Close()
	}()

	if err := serveTunnelSessionWithPolicyAndLimit(serveCtx, session, destinationPolicy, maxConcurrentRequests); err != nil {
		log.Debug().Err(err).Msg("Tunnel session ended")
		return err
	}
	log.Debug().Msg("Tunnel session ended")
	return nil
}

func serveTunnelSession(ctx context.Context, session *yamux.Session, destinationPolicy *agentDestinationPolicy) error {
	return serveTunnelSessionWithPolicyAndLimit(ctx, session, destinationPolicy, 0)
}

func serveTunnelSessionWithLimit(ctx context.Context, session *yamux.Session, maxConcurrentRequests int64) error {
	return serveTunnelSessionWithPolicyAndLimit(ctx, session, nil, maxConcurrentRequests)
}

func serveTunnelSessionWithPolicyAndLimit(ctx context.Context, session *yamux.Session, destinationPolicy *agentDestinationPolicy, maxConcurrentRequests int64) error {
	limiter, err := tunnel.NewStreamLimiter(maxConcurrentRequests)
	if err != nil {
		return fmt.Errorf("invalid tunnel request limit: %w", err)
	}
	var handlers sync.WaitGroup
	defer func() {
		_ = session.Close()
		handlers.Wait()
	}()
	for {
		stream, err := session.Accept()
		if err != nil {
			if ctx.Err() != nil {
				log.Debug().Err(ctx.Err()).Msg("Tunnel stream accept loop stopped by context")
				return ctx.Err()
			}
			log.Debug().Err(err).Msg("Tunnel stream accept loop stopped")
			return fmt.Errorf("accept tunnel stream: %w", err)
		}
		release, ok := limiter.TryAcquire()
		if !ok {
			reqServerError.Add(1)
			rejectTunnelStreamAtCapacity(stream)
			continue
		}
		handlers.Add(1)
		go func(stream net.Conn) {
			defer handlers.Done()
			defer release()
			handleTunnelStream(ctx, stream, destinationPolicy)
		}(stream)
	}
}

func rejectTunnelStreamAtCapacity(stream net.Conn) {
	defer stream.Close()
	_ = stream.SetDeadline(time.Now().Add(agentCapacityResponseTimeout))
	if _, err := tunnel.ReadOpenRequest(stream); err != nil {
		return
	}
	_ = tunnel.WriteOpenResponse(stream, tunnel.OpenResponse{
		OK:        false,
		ErrorKind: "agent_capacity",
		Error:     "agent tunnel request capacity reached",
	})
}

func handleTunnelStream(ctx context.Context, stream net.Conn, destinationPolicy *agentDestinationPolicy) {
	defer stream.Close()

	setTunnelOpenRequestReadDeadline(stream)
	openReq, err := tunnel.ReadOpenRequest(stream)
	clearTunnelOpenRequestReadDeadline(stream)
	if err != nil {
		kind := tunnelOpenRequestErrorKind(err)
		reqInternalError.Add(1)
		_ = tunnel.WriteOpenResponse(stream, tunnel.OpenResponse{
			OK:        false,
			ErrorKind: kind,
			Error:     err.Error(),
		})
		return
	}
	startedAt := time.Now()
	log.Debug().
		Str("request_id", openReq.RequestID).
		Str("network", openReq.Network).
		Str("address", redactTunnelAddress(openReq.Address)).
		Msg("Tunnel stream accepted")

	activeRequests.Add(1)
	defer activeRequests.Add(-1)

	upstream, err := dialTunnelDestination(ctx, openReq.Network, openReq.Address, destinationPolicy)
	if err != nil {
		kind := tunnelDialErrorKind(err)
		reqInternalError.Add(1)
		log.Debug().
			Err(err).
			Str("request_id", openReq.RequestID).
			Str("error_kind", kind).
			Str("address", redactTunnelAddress(openReq.Address)).
			Msg("Tunnel stream dial failed")
		_ = tunnel.WriteOpenResponse(stream, tunnel.OpenResponse{
			OK:        false,
			ErrorKind: kind,
			Error:     err.Error(),
		})
		return
	}
	defer upstream.Close()

	if err := tunnel.WriteOpenResponse(stream, tunnel.OpenResponse{OK: true}); err != nil {
		reqInternalError.Add(1)
		return
	}

	received, sent, err := relayTunnelStream(ctx, stream, upstream)
	log.Debug().
		Str("request_id", openReq.RequestID).
		Uint64("bytes_received", received).
		Uint64("bytes_sent", sent).
		Dur("duration", time.Since(startedAt)).
		Err(err).
		Msg("Tunnel stream relay finished")
	if err != nil && ctx.Err() == nil {
		reqInternalError.Add(1)
		log.Debug().Err(err).Str("request_id", openReq.RequestID).Msg("Tunnel stream relay failed")
		return
	}
	reqSuccess.Add(1)
}

func setTunnelOpenRequestReadDeadline(conn net.Conn) {
	if agentTunnelOpenRequestTimeout <= 0 {
		return
	}
	_ = conn.SetReadDeadline(time.Now().Add(agentTunnelOpenRequestTimeout))
}

func clearTunnelOpenRequestReadDeadline(conn net.Conn) {
	_ = conn.SetReadDeadline(time.Time{})
}

func dialTunnelDestination(ctx context.Context, network string, address string, destinationPolicy *agentDestinationPolicy) (net.Conn, error) {
	dialAddress := address
	if agentTunnelDialTimeout <= 0 {
		if destinationPolicy != nil {
			var err error
			dialAddress, err = destinationPolicy.dialAddress(ctx, network, address)
			if err != nil {
				return nil, err
			}
		}
		return agentTunnelDialNetwork(ctx, network, dialAddress)
	}
	dialCtx, cancel := context.WithTimeout(ctx, agentTunnelDialTimeout)
	defer cancel()
	if destinationPolicy != nil {
		var err error
		dialAddress, err = destinationPolicy.dialAddress(dialCtx, network, address)
		if err != nil {
			return nil, err
		}
	}
	return agentTunnelDialNetwork(dialCtx, network, dialAddress)
}

func dialTunnelNetwork(ctx context.Context, network string, address string) (net.Conn, error) {
	dialer := agentTunnelDialer()
	return dialer.DialContext(ctx, network, address)
}

func agentTunnelDialer() net.Dialer {
	return net.Dialer{Timeout: agentTunnelDialTimeout}
}

func tunnelDialErrorKind(err error) string {
	if errors.Is(err, errAgentDestinationForbidden) {
		return "dial_forbidden"
	}
	if isTimeoutError(err) {
		return "dial_timeout"
	}
	return "dial_failed"
}

func tunnelOpenRequestErrorKind(err error) string {
	if errors.Is(err, tunnel.ErrUnsupportedVersion) {
		return "unsupported_version"
	}
	if isTimeoutError(err) {
		return "open_request_timeout"
	}
	return "invalid_open_request"
}

func relayTunnelStream(ctx context.Context, stream net.Conn, upstream net.Conn) (uint64, uint64, error) {
	type relayResult struct {
		direction string
		bytes     int64
		err       error
	}
	errCh := make(chan relayResult, 2)
	go func() {
		n, err := io.Copy(upstream, stream)
		bytesReceived.Add(uint64(n))
		errCh <- relayResult{direction: "received", bytes: n, err: err}
	}()
	go func() {
		n, err := io.Copy(stream, upstream)
		bytesSent.Add(uint64(n))
		errCh <- relayResult{direction: "sent", bytes: n, err: err}
	}()

	var received uint64
	var sent uint64
	select {
	case first := <-errCh:
		_ = stream.Close()
		_ = upstream.Close()
		second := <-errCh
		for _, result := range []relayResult{first, second} {
			if result.direction == "received" {
				received = uint64(result.bytes)
			} else {
				sent = uint64(result.bytes)
			}
		}
		err := first.err
		if err == nil || errors.Is(err, net.ErrClosed) {
			err = second.err
		}
		if errors.Is(err, net.ErrClosed) {
			return received, sent, nil
		}
		return received, sent, err
	case <-ctx.Done():
		_ = stream.Close()
		_ = upstream.Close()
		return 0, 0, ctx.Err()
	}
}

func redactTunnelAddress(address string) string {
	_, port, err := net.SplitHostPort(strings.TrimSpace(address))
	if err != nil {
		return "<invalid>"
	}
	if port == "" {
		return "<host>"
	}
	return net.JoinHostPort("<host>", port)
}

func isTimeoutError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	var netErr net.Error
	return errors.As(err, &netErr) && netErr.Timeout()
}

func startStatsReporter(ctx context.Context, httpClient *http.Client, mgmtURL string, agentPublicID string, agentToken string, trustStores ...*managementTrustStore) {
	if ctx == nil {
		ctx = context.Background()
	}
	client := p2pstreamv1connect.NewAgentManagementServiceClient(
		httpClient,
		mgmtURL,
		connect.WithGRPC(), // We can use gRPC or Connect protocol, let's use default Connect or GRPC
	)
	runStatsReporter(ctx, client, agentPublicID, agentToken, sysmetrics.NewProcessCPUSampler(), trustStores...)
}

func runStatsReporter(ctx context.Context, client agentStatsReportClient, agentPublicID string, agentToken string, cpuSampler *sysmetrics.ProcessCPUSampler, trustStores ...*managementTrustStore) {
	if ctx == nil {
		ctx = context.Background()
	}
	interval := agentStatsReportInterval
	if interval <= 0 {
		interval = 5 * time.Second
	}

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := reportAgentStats(ctx, client, agentPublicID, agentToken, cpuSampler, trustStores...); err != nil && ctx.Err() == nil {
				log.Debug().Err(err).Msg("Failed to report stats")
			}
		}
	}
}

func reportAgentStats(ctx context.Context, client agentStatsReportClient, agentPublicID string, agentToken string, cpuSampler *sysmetrics.ProcessCPUSampler, trustStores ...*managementTrustStore) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	var trustStore *managementTrustStore
	if len(trustStores) > 0 {
		trustStore = trustStores[0]
	}
	req := buildAgentStatsRequest(agentPublicID, cpuSampler, trustStore)

	connectReq := connect.NewRequest(req)
	connectReq.Header().Set("Authorization", "Bearer "+agentToken)

	reportCtx := ctx
	cancel := func() {}
	if agentStatsReportTimeout > 0 {
		reportCtx, cancel = context.WithTimeout(ctx, agentStatsReportTimeout)
	}
	defer cancel()

	response, err := client.ReportStats(reportCtx, connectReq)
	if err != nil {
		return err
	}
	if trustStore != nil && response != nil && response.Msg != nil && response.Msg.ManagementTrustUpdate != nil {
		if err := trustStore.apply(response.Msg.ManagementTrustUpdate); err != nil {
			log.Warn().Err(err).Uint64("generation", response.Msg.ManagementTrustUpdate.Generation).Msg("Rejected management trust update")
		}
	}
	return nil
}

func buildAgentStatsRequest(agentPublicID string, cpuSampler *sysmetrics.ProcessCPUSampler, trustStores ...*managementTrustStore) *p2pstreamv1.AgentStatsRequest {
	var mem runtime.MemStats
	runtime.ReadMemStats(&mem)
	cpuPercent := 0.0
	if cpuSampler != nil {
		if sampled, ok, err := cpuSampler.Sample(); err == nil && ok {
			cpuPercent = sampled
		}
	}

	request := &p2pstreamv1.AgentStatsRequest{
		MemorySysMb:      agentStatsMemorySysMB(mem),
		NumGoroutine:     int64(runtime.NumGoroutine()),
		CpuPercent:       cpuPercent,
		ActiveRequests:   activeRequests.Load(),
		ReqSuccess:       int64(reqSuccess.Swap(0)),
		ReqClientError:   int64(reqClientError.Swap(0)),
		ReqServerError:   int64(reqServerError.Swap(0)),
		ReqInternalError: int64(reqInternalError.Swap(0)),
		BytesReceived:    bytesReceived.Swap(0),
		BytesSent:        bytesSent.Swap(0),
		AgentPublicId:    agentPublicID,
		AgentVersion:     buildinfo.Version,
		AgentCommit:      buildinfo.Commit,
	}
	if len(trustStores) > 0 && trustStores[0] != nil {
		request.ManagementTrustStatus = trustStores[0].snapshot()
	}
	return request
}

func agentStatsMemorySysMB(mem runtime.MemStats) int64 {
	return int64(mem.Sys / 1024 / 1024)
}
