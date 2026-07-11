package server

import (
	"bytes"
	"context"
	"crypto/tls"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
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
