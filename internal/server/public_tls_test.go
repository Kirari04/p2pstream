package server

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/asn1"
	"encoding/pem"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	p2pstreamv1 "p2pstream/gen/proto/p2pstream/v1"
)

// Check parsed identities, not just PEM text: issuer and subject must contain only
// the intended CN, with no organization, OU, locality, or extra identity fields.
func assertPublicSelfSignedIdentity(t *testing.T, cert *x509.Certificate, commonName string) {
	t.Helper()
	wantName, err := asn1.Marshal(pkix.Name{CommonName: commonName}.ToRDNSequence())
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(cert.RawSubject, wantName) || !bytes.Equal(cert.RawIssuer, wantName) {
		t.Fatalf("unexpected subject/issuer: subject=%v issuer=%v", cert.Subject, cert.Issuer)
	}
	if !bytes.Equal(cert.RawSubject, cert.RawIssuer) {
		t.Fatal("self-signed certificate subject and issuer differ")
	}
	if err := cert.CheckSignature(cert.SignatureAlgorithm, cert.RawTBSCertificate, cert.Signature); err != nil {
		t.Fatalf("certificate does not verify its own signature: %v", err)
	}
	if len(cert.EmailAddresses) != 0 || len(cert.URIs) != 0 {
		t.Fatalf("unexpected SAN identities: emails=%v URIs=%v", cert.EmailAddresses, cert.URIs)
	}
}

func assertGenericPublicCertificate(t *testing.T, cert *x509.Certificate) {
	t.Helper()
	assertPublicSelfSignedIdentity(t, cert, "localhost")
	if !reflect.DeepEqual(cert.DNSNames, []string{"localhost"}) {
		t.Fatalf("default DNS SANs = %v, want only localhost", cert.DNSNames)
	}
	if len(cert.IPAddresses) != 2 || cert.IPAddresses[0].String() != "127.0.0.1" || cert.IPAddresses[1].String() != "::1" {
		t.Fatalf("default IP SANs = %v, want IPv4 and IPv6 loopback", cert.IPAddresses)
	}
	for _, hostname := range []string{"localhost", "127.0.0.1", "::1"} {
		if err := cert.VerifyHostname(hostname); err != nil {
			t.Fatalf("default certificate does not cover %q: %v", hostname, err)
		}
	}
	for _, hostname := range []string{"p2pstream.local", "unknown.example.com"} {
		if err := cert.VerifyHostname(hostname); err == nil {
			t.Fatalf("default certificate unexpectedly covers %q", hostname)
		}
	}
}

func TestDefaultPublicSelfSignedCertificatesHaveGenericIdentity(t *testing.T) {
	for _, tc := range []struct {
		name     string
		generate func() ([]byte, []byte, error)
	}{
		{"fallback", func() ([]byte, []byte, error) { return generateSelfSignedCertificatePEM(24 * time.Hour) }},
		{"seed", generateManagedSelfSignedCertificatePEM},
	} {
		t.Run(tc.name, func(t *testing.T) {
			certPEM, keyPEM, err := tc.generate()
			if err != nil {
				t.Fatal(err)
			}
			pair, err := tls.X509KeyPair(certPEM, keyPEM)
			if err != nil {
				t.Fatalf("parse generated key pair: %v", err)
			}
			cert, err := x509.ParseCertificate(pair.Certificate[0])
			if err != nil {
				t.Fatal(err)
			}
			assertGenericPublicCertificate(t, cert)
		})
	}
}

func TestPublicSelfSignedCertificatePreservesRequestedIdentity(t *testing.T) {
	for _, tc := range []struct {
		pattern string
		match   string
	}{
		{"Public.Example.COM", "public.example.com"},
		{"*.Example.COM", "app.example.com"},
		{"192.0.2.1", "192.0.2.1"},
		{"2001:db8::1", "2001:db8::1"},
	} {
		t.Run(tc.pattern, func(t *testing.T) {
			certPEM, keyPEM, _, err := generatePublicSelfSignedCertificatePEM(tc.pattern, time.Hour)
			if err != nil {
				t.Fatal(err)
			}
			pair, err := tls.X509KeyPair(certPEM, keyPEM)
			if err != nil {
				t.Fatal(err)
			}
			cert, err := x509.ParseCertificate(pair.Certificate[0])
			if err != nil {
				t.Fatal(err)
			}
			assertPublicSelfSignedIdentity(t, cert, strings.ToLower(tc.pattern))
			if err := cert.VerifyHostname(tc.match); err != nil {
				t.Fatalf("requested hostname missing from SANs: %v", err)
			}
			if err := cert.VerifyHostname("unrelated.invalid"); err == nil {
				t.Fatal("certificate covers unrelated hostname")
			}
			if len(cert.DNSNames)+len(cert.IPAddresses) != 1 {
				t.Fatalf("unexpected additional SANs: DNS=%v IP=%v", cert.DNSNames, cert.IPAddresses)
			}
		})
	}
}

func TestPublicTLSSelectorRefreshesRunningListenerWithoutRestart(t *testing.T) {
	const hostname = "rotate.example.com"
	firstSnap, firstDER := testPublicTLSSnapshot(t, 1, hostname, "first")
	secondSnap, secondDER := testPublicTLSSnapshot(t, 1, hostname, "second")

	tlsConfig, selector, err := newPublicTLSConfigWithSelectorStore(1, firstSnap, nil)
	if err != nil {
		t.Fatalf("newPublicTLSConfigWithSelectorStore() error = %v", err)
	}
	assertPublicTLSCertificateDER(t, tlsConfig, hostname, firstDER)

	app := NewApp(nil, nil)
	server := &http.Server{}
	app.proxyMu.Lock()
	app.setPublicSnapshotLocked(firstSnap)
	app.publicListenerState = map[int64]*publicListenerRuntime{
		1: {
			Server:      server,
			TLSSelector: selector,
			State:       p2pstreamv1.ProxyState_PROXY_STATE_RUNNING,
		},
	}
	app.proxyMu.Unlock()

	app.applyPublicProxySnapshot(secondSnap)

	assertPublicTLSCertificateDER(t, tlsConfig, hostname, secondDER)
	app.proxyMu.Lock()
	runtime := app.publicListenerState[1]
	app.proxyMu.Unlock()
	if runtime == nil || runtime.Server != server {
		t.Fatal("TLS selector refresh replaced the running listener server")
	}
	if runtime.State != p2pstreamv1.ProxyState_PROXY_STATE_RUNNING || runtime.LastError != "" {
		t.Fatalf("runtime after selector refresh = state %v error %q, want running with no error", runtime.State, runtime.LastError)
	}
}

func TestPublicTLSFallbackCertificateIsCachedAcrossConfigBuilds(t *testing.T) {
	snap := &publicProxySnapshot{
		Listeners: map[int64]publicListenerConfig{
			1: {
				ID:       1,
				Name:     "https",
				Protocol: publicListenerProtocolHTTPS,
				Enabled:  true,
			},
		},
		RouteTargets:     map[int64]publicRouteTargetConfig{},
		RoutesByListener: map[int64][]publicRouteConfig{},
		CertsByListener:  map[int64][]publicTLSCertificateConfig{},
	}

	firstConfig, err := newPublicTLSConfig(1, snap, nil)
	if err != nil {
		t.Fatalf("first newPublicTLSConfig() error = %v", err)
	}
	secondConfig, err := newPublicTLSConfig(1, snap, nil)
	if err != nil {
		t.Fatalf("second newPublicTLSConfig() error = %v", err)
	}

	first, err := firstConfig.GetCertificate(&tls.ClientHelloInfo{ServerName: "unknown.example.com"})
	if err != nil {
		t.Fatalf("first fallback GetCertificate() error = %v", err)
	}
	second, err := secondConfig.GetCertificate(&tls.ClientHelloInfo{ServerName: "another.example.com"})
	if err != nil {
		t.Fatalf("second fallback GetCertificate() error = %v", err)
	}
	if first == nil || second == nil || len(first.Certificate) == 0 || len(second.Certificate) == 0 {
		t.Fatalf("missing fallback certificates: first=%+v second=%+v", first, second)
	}
	if !bytes.Equal(first.Certificate[0], second.Certificate[0]) {
		t.Fatal("fallback certificate was regenerated across public TLS config builds")
	}
	assertECDSAP256CertificateDER(t, first.Certificate[0])
	leaf, err := x509.ParseCertificate(first.Certificate[0])
	if err != nil {
		t.Fatal(err)
	}
	assertGenericPublicCertificate(t, leaf)
	noSNI, err := firstConfig.GetCertificate(&tls.ClientHelloInfo{})
	if err != nil || noSNI != first {
		t.Fatalf("no-SNI certificate differs from generic fallback: %v", err)
	}
}

func TestPublicTLSSelectorRefreshPreservesFallbackCertificate(t *testing.T) {
	snap := publicTLSFallbackOnlySnapshot(1)
	tlsConfig, selector, err := newPublicTLSConfigWithSelectorStore(1, snap, nil)
	if err != nil {
		t.Fatalf("newPublicTLSConfigWithSelectorStore() error = %v", err)
	}
	first, err := tlsConfig.GetCertificate(&tls.ClientHelloInfo{ServerName: "unknown.example.com"})
	if err != nil {
		t.Fatalf("first fallback GetCertificate() error = %v", err)
	}
	if err := selector.refresh(1, snap, nil); err != nil {
		t.Fatalf("selector refresh error = %v", err)
	}
	second, err := tlsConfig.GetCertificate(&tls.ClientHelloInfo{ServerName: "unknown.example.com"})
	if err != nil {
		t.Fatalf("second fallback GetCertificate() error = %v", err)
	}
	if first == nil || second == nil || len(first.Certificate) == 0 || len(second.Certificate) == 0 {
		t.Fatalf("missing fallback certificates: first=%+v second=%+v", first, second)
	}
	if !bytes.Equal(first.Certificate[0], second.Certificate[0]) {
		t.Fatal("fallback certificate changed during selector refresh")
	}
}

func TestGeneratedPublicTLSCertificatesUseECDSAP256(t *testing.T) {
	certPEM, keyPEM, err := generateSelfSignedCertificatePEM(time.Hour)
	if err != nil {
		t.Fatalf("generate fallback certificate PEM: %v", err)
	}
	cert, err := parseLeafCertificate(certPEM)
	if err != nil {
		t.Fatalf("parse fallback certificate: %v", err)
	}
	assertECDSAP256Certificate(t, cert)
	assertECDSAP256PrivateKeyPEM(t, keyPEM)

	certPEM, keyPEM, err = generateManagedSelfSignedCertificatePEM()
	if err != nil {
		t.Fatalf("generate managed certificate PEM: %v", err)
	}
	cert, err = parseLeafCertificate(certPEM)
	if err != nil {
		t.Fatalf("parse managed certificate: %v", err)
	}
	assertECDSAP256Certificate(t, cert)
	assertECDSAP256PrivateKeyPEM(t, keyPEM)

	certPEM, keyPEM, cert, err = generatePublicSelfSignedCertificatePEM("public.example.com", time.Hour)
	if err != nil {
		t.Fatalf("generate public certificate PEM: %v", err)
	}
	if _, err := tls.X509KeyPair(certPEM, keyPEM); err != nil {
		t.Fatalf("generated public certificate key pair did not load: %v", err)
	}
	assertECDSAP256Certificate(t, cert)
	assertECDSAP256PrivateKeyPEM(t, keyPEM)

	fallback, err := generateFallbackCertificate()
	if err != nil {
		t.Fatalf("generate fallback certificate: %v", err)
	}
	if fallback == nil || len(fallback.Certificate) == 0 {
		t.Fatalf("missing fallback certificate: %+v", fallback)
	}
	assertECDSAP256CertificateDER(t, fallback.Certificate[0])
}

func TestPublicTLSSelectorRefreshFailureKeepsLiveServerRegistered(t *testing.T) {
	const hostname = "rotate-failure.example.com"
	firstSnap, firstDER := testPublicTLSSnapshot(t, 1, hostname, "first")
	invalidSnap, _ := testPublicTLSSnapshot(t, 1, hostname, "invalid")
	invalidSnap.CertsByListener[1][0].CertPath = filepath.Join(t.TempDir(), "missing.crt.pem")

	tlsConfig, selector, err := newPublicTLSConfigWithSelectorStore(1, firstSnap, nil)
	if err != nil {
		t.Fatalf("newPublicTLSConfigWithSelectorStore() error = %v", err)
	}
	server := &http.Server{}
	app := NewApp(nil, nil)
	app.proxyMu.Lock()
	app.setPublicSnapshotLocked(firstSnap)
	app.publicListenerState = map[int64]*publicListenerRuntime{
		1: {
			Server:      server,
			TLSSelector: selector,
			State:       p2pstreamv1.ProxyState_PROXY_STATE_RUNNING,
		},
	}
	app.proxyMu.Unlock()

	app.applyPublicProxySnapshot(invalidSnap)
	assertPublicTLSCertificateDER(t, tlsConfig, hostname, firstDER)

	app.proxyMu.Lock()
	runtime := app.publicListenerState[1]
	app.proxyMu.Unlock()
	if runtime == nil || runtime.Server != server || runtime.TLSSelector != selector {
		t.Fatalf("live runtime was orphaned after selector failure: %+v", runtime)
	}
	if runtime.State != p2pstreamv1.ProxyState_PROXY_STATE_RUNNING || !strings.Contains(runtime.LastError, "refresh TLS certificates") {
		t.Fatalf("runtime after selector failure = state %v error %q, want running with refresh error", runtime.State, runtime.LastError)
	}

	if _, err := app.startPublicListenerRuntime(context.Background(), 1, false); err != nil {
		t.Fatalf("startPublicListenerRuntime() with live degraded listener error = %v", err)
	}
	app.proxyMu.Lock()
	runtime = app.publicListenerState[1]
	app.proxyMu.Unlock()
	if runtime == nil || runtime.Server != server || runtime.TLSSelector != selector {
		t.Fatalf("start retry orphaned live runtime after selector failure: %+v", runtime)
	}
}

func TestPublicTLSSelectorConcurrentRefreshPublishesNewestSnapshot(t *testing.T) {
	const hostname = "rotate-concurrent.example.com"
	firstSnap, _ := testPublicTLSSnapshot(t, 1, hostname, "first")
	secondSnap, _ := testPublicTLSSnapshot(t, 1, hostname, "second")
	thirdSnap, thirdDER := testPublicTLSSnapshot(t, 1, hostname, "third")

	tlsConfig, selector, err := newPublicTLSConfigWithSelectorStore(1, firstSnap, nil)
	if err != nil {
		t.Fatalf("newPublicTLSConfigWithSelectorStore() error = %v", err)
	}
	app := NewApp(nil, nil)
	server := &http.Server{}
	app.proxyMu.Lock()
	app.setPublicSnapshotLocked(firstSnap)
	blockedGeneration := app.publicSnapshotGeneration + 1
	app.publicListenerState = map[int64]*publicListenerRuntime{
		1: {
			Server:      server,
			TLSSelector: selector,
			State:       p2pstreamv1.ProxyState_PROXY_STATE_RUNNING,
		},
	}
	app.proxyMu.Unlock()

	firstBuilt := make(chan struct{})
	releaseFirst := make(chan struct{})
	var blockOnce sync.Once
	app.publicTLSSelectorRefreshBeforePublish = func(generation uint64) {
		if generation != blockedGeneration {
			return
		}
		blockOnce.Do(func() {
			close(firstBuilt)
			<-releaseFirst
		})
	}

	firstDone := make(chan struct{})
	go func() {
		defer close(firstDone)
		app.applyPublicProxySnapshot(secondSnap)
	}()
	select {
	case <-firstBuilt:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for first selector refresh build")
	}

	app.applyPublicProxySnapshot(thirdSnap)
	close(releaseFirst)
	select {
	case <-firstDone:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for stale selector refresh")
	}

	assertPublicTLSCertificateDER(t, tlsConfig, hostname, thirdDER)
	app.proxyMu.Lock()
	runtime := app.publicListenerState[1]
	currentSnap := app.publicSnapshot
	app.proxyMu.Unlock()
	if currentSnap != thirdSnap {
		t.Fatal("concurrent refresh replaced the newest public snapshot")
	}
	if runtime == nil || runtime.Server != server || runtime.State != p2pstreamv1.ProxyState_PROXY_STATE_RUNNING || runtime.LastError != "" {
		t.Fatalf("runtime after concurrent selector refresh = %+v", runtime)
	}
}

func testPublicTLSSnapshot(t *testing.T, listenerID int64, hostname string, name string) (*publicProxySnapshot, []byte) {
	t.Helper()
	certPEM, keyPEM, leaf, err := generatePublicSelfSignedCertificatePEM(hostname, time.Hour)
	if err != nil {
		t.Fatalf("generate %s certificate: %v", name, err)
	}
	dir := t.TempDir()
	certPath := filepath.Join(dir, name+".crt.pem")
	keyPath := filepath.Join(dir, name+".key.pem")
	if err := os.WriteFile(certPath, certPEM, 0600); err != nil {
		t.Fatalf("write %s certificate: %v", name, err)
	}
	if err := os.WriteFile(keyPath, keyPEM, 0600); err != nil {
		t.Fatalf("write %s key: %v", name, err)
	}
	return &publicProxySnapshot{
		Listeners: map[int64]publicListenerConfig{
			listenerID: {
				ID:       listenerID,
				Name:     "https",
				Protocol: publicListenerProtocolHTTPS,
				Enabled:  true,
			},
		},
		RouteTargets:     map[int64]publicRouteTargetConfig{},
		RoutesByListener: map[int64][]publicRouteConfig{},
		CertsByListener: map[int64][]publicTLSCertificateConfig{
			listenerID: {{
				ID:              10,
				ListenerID:      listenerID,
				HostnamePattern: hostname,
				CertPath:        certPath,
				KeyPath:         keyPath,
				Enabled:         true,
				Source:          publicTLSCertificateSourceManual,
				Status:          publicTLSCertificateStatusReady,
			}},
		},
	}, leaf.Raw
}

func publicTLSFallbackOnlySnapshot(listenerID int64) *publicProxySnapshot {
	return &publicProxySnapshot{
		Listeners: map[int64]publicListenerConfig{
			listenerID: {
				ID:       listenerID,
				Name:     "https",
				Protocol: publicListenerProtocolHTTPS,
				Enabled:  true,
			},
		},
		RouteTargets:     map[int64]publicRouteTargetConfig{},
		RoutesByListener: map[int64][]publicRouteConfig{},
		CertsByListener:  map[int64][]publicTLSCertificateConfig{},
	}
}

func assertECDSAP256CertificateDER(t *testing.T, der []byte) {
	t.Helper()
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatalf("parse certificate DER: %v", err)
	}
	assertECDSAP256Certificate(t, cert)
}

func assertECDSAP256Certificate(t *testing.T, cert *x509.Certificate) {
	t.Helper()
	if cert.SerialNumber == nil || cert.SerialNumber.Sign() <= 0 {
		t.Fatalf("certificate serial = %v, want positive non-zero", cert.SerialNumber)
	}
	if cert.PublicKeyAlgorithm != x509.ECDSA {
		t.Fatalf("certificate public key algorithm = %s, want ECDSA", cert.PublicKeyAlgorithm)
	}
	publicKey, ok := cert.PublicKey.(*ecdsa.PublicKey)
	if !ok {
		t.Fatalf("certificate public key is %T, want *ecdsa.PublicKey", cert.PublicKey)
	}
	if publicKey.Curve != elliptic.P256() {
		t.Fatalf("certificate curve = %v, want P-256", publicKey.Curve)
	}
	if !cert.IsCA {
		if cert.KeyUsage&x509.KeyUsageDigitalSignature == 0 {
			t.Fatalf("certificate key usage = %v, want digital signature", cert.KeyUsage)
		}
		if cert.KeyUsage&x509.KeyUsageKeyEncipherment != 0 {
			t.Fatalf("certificate key usage = %v, did not expect key encipherment for ECDSA", cert.KeyUsage)
		}
	}
}

func assertECDSAP256PrivateKeyPEM(t *testing.T, keyPEM []byte) {
	t.Helper()
	block, _ := pem.Decode(keyPEM)
	if block == nil {
		t.Fatal("private key PEM did not parse")
	}
	key, err := x509.ParseECPrivateKey(block.Bytes)
	if err != nil {
		t.Fatalf("parse EC private key: %v", err)
	}
	if key.Curve != elliptic.P256() {
		t.Fatalf("private key curve = %v, want P-256", key.Curve)
	}
}

func assertPublicTLSCertificateDER(t *testing.T, tlsConfig *tls.Config, hostname string, want []byte) {
	t.Helper()
	got, err := tlsConfig.GetCertificate(&tls.ClientHelloInfo{ServerName: hostname})
	if err != nil {
		t.Fatalf("GetCertificate(%q) error = %v", hostname, err)
	}
	if got == nil || len(got.Certificate) == 0 {
		t.Fatalf("GetCertificate(%q) returned no certificate", hostname)
	}
	if !bytes.Equal(got.Certificate[0], want) {
		t.Fatalf("GetCertificate(%q) returned unexpected certificate", hostname)
	}
}
