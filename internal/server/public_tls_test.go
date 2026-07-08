package server

import (
	"bytes"
	"crypto/tls"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	p2pstreamv1 "p2pstream/gen/proto/p2pstream/v1"
)

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
