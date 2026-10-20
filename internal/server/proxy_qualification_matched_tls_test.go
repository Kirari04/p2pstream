//go:build linux

package server

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"sync"
	"testing"
	"time"
)

const qualificationMatchedTLSPrefix = "P2PSTREAM_QUALIFICATION_"

// All fields are scalar and present, including zero failures and false resume.
// Counts cover only this client's handshakes, including prewarm and warmup.
// Handshake-only accounting adds no lock to reused requests.
type qualificationMatchedTLSReceipt struct {
	ServerName                  string `json:"server_name"`
	ObservedServerName          string `json:"observed_server_name"`
	ExpectedLeafDERSHA256       string `json:"expected_leaf_der_sha256"`
	ObservedLeafDERSHA256       string `json:"observed_leaf_der_sha256"`
	ExpectedALPN                string `json:"expected_alpn"`
	ALPN                        string `json:"alpn"`
	ConnectionAttempts          int64  `json:"connection_attempts"`
	VerifiedConnectionCount     int64  `json:"verified_connection_count"`
	VerificationFailureCount    int64  `json:"verification_failure_count"`
	ActiveHandshakes            int64  `json:"active_handshakes"`
	TLSVersion                  uint16 `json:"tls_version"`
	CipherSuite                 uint16 `json:"cipher_suite"`
	KeyExchangeGroup            uint16 `json:"key_exchange_group"`
	DidResume                   bool   `json:"did_resume"`
	VerifiedChainCount          int    `json:"verified_chain_count"`
	H2PrewarmerVerifiedSameLeaf bool   `json:"h2_prewarmer_verified_same_leaf"`
}

type qualificationMatchedTLS struct {
	authority string
	connect   string
	config    *tls.Config
	mu        sync.Mutex
	observed  qualificationMatchedTLSReceipt
}

// qualificationMatchedTLSFromEnvironment is opt-in and rejects partial config.
// It does not consult system roots or DNS. The same helper backs load, prewarm
// and the finite handshake-only readiness subprocess below.
func qualificationMatchedTLSFromEnvironment(getenv func(string) string, target, protocol string) (*qualificationMatchedTLS, error) {
	names := []string{"MATCHED_TLS", "TLS_SERVER_NAME", "TLS_CONNECT_ADDRESS", "TLS_LEAF_PEM", "TLS_LEAF_DER_SHA256"}
	values := make([]string, len(names))
	present := false
	for n, name := range names {
		values[n] = getenv(qualificationMatchedTLSPrefix + name)
		present = present || values[n] != ""
	}
	if !present {
		return nil, nil
	}
	for n, value := range values {
		if value == "" {
			return nil, fmt.Errorf("matched TLS requires %s", names[n])
		}
	}
	if values[0] != "1" || values[1] != "public.test" {
		return nil, errors.New("matched TLS requires mode1 and server name public.test")
	}
	parsed, err := url.Parse(target)
	if err != nil || parsed.Scheme != "https" || parsed.User != nil || parsed.Hostname() != "public.test" || parsed.Fragment != "" || parsed.Opaque != "" || parsed.RawQuery != "" || parsed.EscapedPath() != "/direct/small" {
		return nil, errors.New("matched TLS requires the exact HTTPS public.test /direct/small URL without credentials/query/fragment")
	}
	port, err := strconv.Atoi(parsed.Port())
	if err != nil || port < 1 || port > 65535 || parsed.Host != net.JoinHostPort("public.test", strconv.Itoa(port)) || target != "https://"+parsed.Host+"/direct/small" {
		return nil, errors.New("matched TLS requires an explicit canonical numeric authority port")
	}
	connect := net.JoinHostPort("127.0.0.1", strconv.Itoa(port))
	if values[2] != connect {
		return nil, errors.New("matched TLS requires exact same-port 127.0.0.1 connect address")
	}
	if protocol != "h1" && protocol != "h2" {
		return nil, errors.New("matched TLS protocol must be h1 or h2")
	}
	file, err := os.Open(values[3])
	if err != nil {
		return nil, fmt.Errorf("matched TLS trust file: %w", err)
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > 64<<10 {
		return nil, errors.New("matched TLS trust must be a regular file <=64KiB")
	}
	data, err := io.ReadAll(io.LimitReader(file, (64<<10)+1))
	if err != nil || len(data) > 64<<10 {
		return nil, errors.New("matched TLS trust read failed or exceeded64KiB")
	}
	block, rest := pem.Decode(data)
	if block == nil || !bytes.HasPrefix(bytes.TrimSpace(data), []byte("-----BEGIN CERTIFICATE-----")) || block.Type != "CERTIFICATE" || len(block.Headers) != 0 || len(bytes.TrimSpace(rest)) != 0 {
		return nil, errors.New("matched TLS trust requires exactly one certificate PEM")
	}
	leaf, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("matched TLS leaf: %w", err)
	}
	digest := sha256.Sum256(leaf.Raw)
	if values[4] != hex.EncodeToString(digest[:]) {
		return nil, errors.New("matched TLS expected leaf DER digest differs or is not canonical SHA256")
	}
	key, ok := leaf.PublicKey.(*ecdsa.PublicKey)
	if !ok || key.Curve != elliptic.P256() || leaf.IsCA || len(leaf.DNSNames) != 1 || leaf.DNSNames[0] != "public.test" || len(leaf.IPAddresses) != 0 || leaf.KeyUsage != x509.KeyUsageDigitalSignature || len(leaf.ExtKeyUsage) != 1 || leaf.ExtKeyUsage[0] != x509.ExtKeyUsageServerAuth {
		return nil, errors.New("matched TLS requires the one public.test SAN P256 serverAuth signing leaf")
	}
	if err := leaf.CheckSignature(leaf.SignatureAlgorithm, leaf.RawTBSCertificate, leaf.Signature); err != nil || !bytes.Equal(leaf.RawIssuer, leaf.RawSubject) {
		return nil, errors.New("matched TLS leaf must be self-signed")
	}
	roots := x509.NewCertPool()
	roots.AddCert(leaf)
	alpn := "http/1.1"
	if protocol == "h2" {
		alpn = "h2"
	}
	matched := &qualificationMatchedTLS{authority: parsed.Host, connect: connect, observed: qualificationMatchedTLSReceipt{ServerName: "public.test", ExpectedLeafDERSHA256: values[4], ExpectedALPN: alpn}}
	matched.config = &tls.Config{RootCAs: roots, ServerName: "public.test", MinVersion: tls.VersionTLS13, MaxVersion: tls.VersionTLS13, CurvePreferences: []tls.CurveID{tls.X25519}, NextProtos: []string{alpn}, ClientSessionCache: nil, SessionTicketsDisabled: true}
	matched.config.VerifyConnection = func(state tls.ConnectionState) error {
		if state.ServerName != "public.test" || state.Version != tls.VersionTLS13 || state.CurveID != tls.X25519 || state.DidResume || state.NegotiatedProtocol != alpn {
			return errors.New("matched TLS SNI/version/group/resumption/ALPN contract failed")
		}
		if len(state.PeerCertificates) != 1 || len(state.VerifiedChains) != 1 || len(state.VerifiedChains[0]) != 1 {
			return errors.New("matched TLS requires one standard-verified leaf chain")
		}
		observed := sha256.Sum256(state.PeerCertificates[0].Raw)
		verified := sha256.Sum256(state.VerifiedChains[0][0].Raw)
		if observed != digest || verified != digest {
			return errors.New("matched TLS verified leaf DER pin failed")
		}
		return nil
	}
	return matched, nil
}

func (m *qualificationMatchedTLS) dialContext(ctx context.Context, network, authority string) (net.Conn, error) {
	if network != "tcp" || authority != m.authority {
		return nil, errors.New("matched TLS refuses a different network or authority")
	}
	dialer := net.Dialer{Timeout: 5 * time.Second, KeepAlive: 30 * time.Second}
	return dialer.DialContext(ctx, "tcp", m.connect)
}

func (m *qualificationMatchedTLS) dialTLSContext(ctx context.Context, network, authority string) (net.Conn, error) {
	m.mu.Lock()
	m.observed.ConnectionAttempts++
	m.observed.ActiveHandshakes++
	m.mu.Unlock()
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	raw, err := m.dialContext(ctx, network, authority)
	var conn *tls.Conn
	var state tls.ConnectionState
	if err == nil {
		// Clone the immutable policy: net/http may adjust its own ALPN slice.
		conn = tls.Client(raw, m.config.Clone())
		err = conn.HandshakeContext(ctx)
		state = conn.ConnectionState()
	}
	m.mu.Lock()
	m.observed.ActiveHandshakes--
	if err == nil && m.observed.VerifiedConnectionCount > 0 && m.observed.CipherSuite != state.CipherSuite {
		err = errors.New("matched TLS negotiated inconsistent cipher suites")
	}
	if err == nil || m.observed.VerifiedConnectionCount == 0 {
		m.observed.ObservedServerName = state.ServerName
		m.observed.ALPN = state.NegotiatedProtocol
		m.observed.TLSVersion = state.Version
		m.observed.CipherSuite = state.CipherSuite
		m.observed.KeyExchangeGroup = uint16(state.CurveID)
		m.observed.DidResume = state.DidResume
		m.observed.VerifiedChainCount = len(state.VerifiedChains)
		if len(state.PeerCertificates) > 0 {
			digest := sha256.Sum256(state.PeerCertificates[0].Raw)
			m.observed.ObservedLeafDERSHA256 = hex.EncodeToString(digest[:])
		}
	}
	if err == nil {
		m.observed.VerifiedConnectionCount++
	} else {
		m.observed.VerificationFailureCount++
	}
	m.mu.Unlock()
	if err != nil {
		if raw != nil {
			_ = raw.Close()
		}
		return nil, err
	}
	return conn, nil
}

func (m *qualificationMatchedTLS) configureClient(transport *http.Transport, client *http.Client) {
	transport.Proxy = nil
	transport.TLSClientConfig = m.config.Clone()
	transport.DialContext = m.dialContext
	transport.DialTLSContext = m.dialTLSContext
	transport.Protocols = new(http.Protocols)
	if m.observed.ExpectedALPN == "h2" {
		transport.Protocols.SetHTTP2(true)
		transport.ForceAttemptHTTP2 = true
		transport.MaxConnsPerHost = 1
		transport.TLSNextProto = nil
	} else {
		transport.Protocols.SetHTTP1(true)
		transport.ForceAttemptHTTP2 = false
		transport.TLSNextProto = make(map[string]func(string, *tls.Conn) http.RoundTripper)
	}
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return errors.New("matched TLS redirects are forbidden") }
}

func (m *qualificationMatchedTLS) receipt() qualificationMatchedTLSReceipt {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.observed
}

func (m *qualificationMatchedTLS) verifiedH2Prewarm() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.observed.H2PrewarmerVerifiedSameLeaf = m.observed.ExpectedALPN == "h2" && m.observed.VerifiedConnectionCount == 1 && m.observed.VerificationFailureCount == 0 && m.observed.ActiveHandshakes == 0 && m.observed.ObservedLeafDERSHA256 == m.observed.ExpectedLeafDERSHA256
}

// TestProxyQualificationMatchedTLSReadiness verifies a single handshake without
// sending an HTTP request or recording a proxy event. The role is explicit and
// bounded, and uses exactly the load client's verification and loopback mapping.
func TestProxyQualificationMatchedTLSReadiness(t *testing.T) {
	if os.Getenv("P2PSTREAM_QUALIFICATION_ROLE") != "matched-tls-readiness" {
		t.Skip("matched TLS readiness subprocess only")
	}
	matched, err := qualificationMatchedTLSFromEnvironment(os.Getenv, requiredQualificationEnv(t, "P2PSTREAM_QUALIFICATION_URL"), requiredQualificationEnv(t, "P2PSTREAM_QUALIFICATION_PROTOCOL"))
	if err != nil || matched == nil {
		t.Fatalf("matched TLS readiness configuration: %v", err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	conn, handshakeErr := matched.dialTLSContext(ctx, "tcp", matched.authority)
	if conn != nil {
		_ = conn.Close()
	}
	if err := writeQualificationJSON(requiredQualificationEnv(t, "P2PSTREAM_QUALIFICATION_RESULT"), matched.receipt()); err != nil {
		t.Fatal(err)
	}
	if handshakeErr != nil {
		t.Fatal(handshakeErr)
	}
}
