//go:build linux

package server

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"io"
	"log"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// Certificate creation happens only when root schedules these bounded tests.
// Private keys remain in memory; only public leaf trust is written to TempDir.
func qualificationMatchedTLSContractLeaf(t *testing.T, name string) (tls.Certificate, []byte) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{SerialNumber: big.NewInt(1), DNSNames: []string{name}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, BasicConstraintsValid: true}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key, Leaf: parsed}, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
}

func qualificationMatchedTLSContractEnv(t *testing.T, leaf []byte, connect string) map[string]string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "public-leaf.pem")
	if err := os.WriteFile(path, leaf, 0600); err != nil {
		t.Fatal(err)
	}
	block, _ := pem.Decode(leaf)
	digest := sha256.Sum256(block.Bytes)
	return map[string]string{"MATCHED_TLS": "1", "TLS_SERVER_NAME": "public.test", "TLS_CONNECT_ADDRESS": connect, "TLS_LEAF_PEM": path, "TLS_LEAF_DER_SHA256": hex.EncodeToString(digest[:])}
}

func qualificationMatchedTLSContractGetenv(env map[string]string) func(string) string {
	return func(name string) string { return env[strings.TrimPrefix(name, qualificationMatchedTLSPrefix)] }
}

func qualificationMatchedTLSContractServer(t *testing.T, protocol string, handler http.Handler) (*httptest.Server, []byte, *atomic.Bool) {
	t.Helper()
	cert, leaf := qualificationMatchedTLSContractLeaf(t, "public.test")
	seenSNI := new(atomic.Bool)
	server := httptest.NewUnstartedServer(handler)
	server.Config.ErrorLog = log.New(io.Discard, "", 0)
	server.EnableHTTP2 = protocol == "h2"
	server.TLS = &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS13, MaxVersion: tls.VersionTLS13, CurvePreferences: []tls.CurveID{tls.X25519}, NextProtos: []string{protocol}, GetConfigForClient: func(hello *tls.ClientHelloInfo) (*tls.Config, error) {
		seenSNI.Store(hello.ServerName == "public.test")
		return nil, nil
	}}
	if protocol == "h1" {
		server.TLS.NextProtos = []string{"http/1.1"}
	}
	server.StartTLS()
	t.Cleanup(server.Close)
	return server, leaf, seenSNI
}

func qualificationMatchedTLSContractClient(t *testing.T, server *httptest.Server, leaf []byte, protocol string) (*qualificationMatchedTLS, *http.Client, string, *http.Transport) {
	t.Helper()
	_, port, err := net.SplitHostPort(server.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	connect := net.JoinHostPort("127.0.0.1", port)
	target := "https://" + net.JoinHostPort("public.test", port) + "/direct/small"
	env := qualificationMatchedTLSContractEnv(t, leaf, connect)
	matched, err := qualificationMatchedTLSFromEnvironment(qualificationMatchedTLSContractGetenv(env), target, protocol)
	if err != nil {
		t.Fatal(err)
	}
	transport := &http.Transport{MaxIdleConns: 4, MaxIdleConnsPerHost: 4, MaxConnsPerHost: 4}
	client := &http.Client{Transport: transport, Timeout: 3 * time.Second}
	matched.configureClient(transport, client)
	t.Cleanup(transport.CloseIdleConnections)
	return matched, client, target, transport
}

func TestQualificationMatchedTLSVerifiedLoadAndPrewarm(t *testing.T) {
	for _, protocol := range []string{"h1", "h2"} {
		t.Run(protocol, func(t *testing.T) {
			var requests atomic.Int64
			server, leaf, seenSNI := qualificationMatchedTLSContractServer(t, protocol, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				if r.Host != "public.test" {
					t.Error("HTTP Host changed")
				}
				_, _ = w.Write(make([]byte, 256))
			}))
			matched, client, target, transport := qualificationMatchedTLSContractClient(t, server, leaf, protocol)
			if matched.config.InsecureSkipVerify || matched.config.ClientSessionCache != nil || !matched.config.SessionTicketsDisabled || len(matched.config.RootCAs.Subjects()) != 1 {
				t.Fatal("verification/trust/resumption configuration differs")
			}
			var first net.Conn
			for range 2 {
				if protocol == "h2" {
					ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
					conn, err := qualificationPrewarmH2Small(ctx, client, target)
					cancel()
					if err != nil {
						t.Fatal(err)
					}
					if first != nil && conn != first {
						t.Fatal("H2 prewarm did not reuse the same verified connection")
					}
					first = conn
					matched.verifiedH2Prewarm()
				} else {
					req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, target, nil)
					if err != nil {
						t.Fatal(err)
					}
					req.Host = "public.test"
					response, err := client.Do(req)
					if err != nil {
						t.Fatal(err)
					}
					body, readErr := io.ReadAll(io.LimitReader(response.Body, 257))
					_ = response.Body.Close()
					if readErr != nil || response.ProtoMajor != 1 || response.StatusCode != 200 || len(body) != 256 || !bytes.Equal(body, make([]byte, 256)) {
						t.Fatal("H1 exact payload/protocol failed")
					}
				}
			}
			receipt := matched.receipt()
			if !seenSNI.Load() || requests.Load() != 2 || receipt.ConnectionAttempts != 1 || receipt.VerifiedConnectionCount != 1 || receipt.VerificationFailureCount != 0 || receipt.ActiveHandshakes != 0 || receipt.ObservedLeafDERSHA256 != receipt.ExpectedLeafDERSHA256 || receipt.ObservedServerName != "public.test" || receipt.TLSVersion != tls.VersionTLS13 || receipt.KeyExchangeGroup != uint16(tls.X25519) || receipt.DidResume || receipt.VerifiedChainCount != 1 || receipt.CipherSuite == 0 || receipt.ALPN != receipt.ExpectedALPN || receipt.H2PrewarmerVerifiedSameLeaf != (protocol == "h2") {
				t.Fatalf("verified receipt=%+v requests=%d SNI=%v", receipt, requests.Load(), seenSNI.Load())
			}
			transport.CloseIdleConnections()
			conn, err := matched.dialTLSContext(t.Context(), "tcp", matched.authority)
			if err != nil {
				t.Fatal(err)
			}
			_ = conn.Close()
			if got := matched.receipt(); got.ConnectionAttempts != 2 || got.VerifiedConnectionCount != 2 || got.DidResume || got.VerificationFailureCount != 0 {
				t.Fatalf("fresh connection/resumption receipt=%+v", got)
			}
		})
	}
}

func TestQualificationMatchedTLSRejectsInvalidConfiguration(t *testing.T) {
	_, leaf := qualificationMatchedTLSContractLeaf(t, "public.test")
	base := qualificationMatchedTLSContractEnv(t, leaf, "127.0.0.1:4443")
	target := "https://public.test:4443/direct/small"
	clone := func() map[string]string {
		env := map[string]string{}
		for k, v := range base {
			env[k] = v
		}
		return env
	}
	if matched, err := qualificationMatchedTLSFromEnvironment(func(string) string { return "" }, target, "h1"); err != nil || matched != nil {
		t.Fatal("ordinary historical mode changed")
	}
	for name := range base {
		t.Run("missing-"+name, func(t *testing.T) {
			env := clone()
			delete(env, name)
			if _, err := qualificationMatchedTLSFromEnvironment(qualificationMatchedTLSContractGetenv(env), target, "h1"); err == nil {
				t.Fatal("partial config accepted")
			}
		})
	}
	for _, test := range []struct{ name, key, value, target, protocol string }{
		{name: "disabled", key: "MATCHED_TLS", value: "0"}, {name: "wrong-server-name", key: "TLS_SERVER_NAME", value: "other.test"}, {name: "missing-trust", key: "TLS_LEAF_PEM", value: filepath.Join(t.TempDir(), "absent")}, {name: "bad-pin", key: "TLS_LEAF_DER_SHA256", value: strings.Repeat("0", 64)},
		{name: "non-loopback", key: "TLS_CONNECT_ADDRESS", value: "192.0.2.1:4443"}, {name: "DNS-connect", key: "TLS_CONNECT_ADDRESS", value: "localhost:4443"}, {name: "IPv6-connect", key: "TLS_CONNECT_ADDRESS", value: "[::1]:4443"}, {name: "wrong-port", key: "TLS_CONNECT_ADDRESS", value: "127.0.0.1:4444"}, {name: "partial-address", key: "TLS_CONNECT_ADDRESS", value: "127.0.0.1"},
		{name: "IP-target", target: "https://127.0.0.1:4443/direct/small"}, {name: "wrong-host", target: "https://other.test:4443/direct/small"}, {name: "missing-port", target: "https://public.test/direct/small"}, {name: "noncanonical-port", target: "https://public.test:04443/direct/small"}, {name: "plain-http", target: "http://public.test:4443/direct/small"}, {name: "userinfo", target: "https://user@public.test:4443/direct/small"}, {name: "fragment", target: target + "#fragment"}, {name: "query", target: target + "?x=1"}, {name: "empty-query", target: target + "?"}, {name: "empty-fragment", target: target + "#"}, {name: "other-path", target: "https://public.test:4443/other"}, {name: "bad-protocol", protocol: "h3"},
	} {
		t.Run(test.name, func(t *testing.T) {
			env := clone()
			url := target
			protocol := "h1"
			if test.key != "" {
				env[test.key] = test.value
			}
			if test.target != "" {
				url = test.target
			}
			if test.protocol != "" {
				protocol = test.protocol
			}
			if _, err := qualificationMatchedTLSFromEnvironment(qualificationMatchedTLSContractGetenv(env), url, protocol); err == nil {
				t.Fatal("invalid TLS contract accepted")
			}
		})
	}
	for name, data := range map[string][]byte{"not-PEM": []byte("garbage"), "bad-DER": pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: []byte("bad")}), "two-leaves": append(append([]byte(nil), leaf...), leaf...), "leading-junk": append([]byte("ignored garbage\n"), leaf...), "oversized": make([]byte, (64<<10)+1)} {
		t.Run(name, func(t *testing.T) {
			env := clone()
			path := filepath.Join(t.TempDir(), "invalid.pem")
			if err := os.WriteFile(path, data, 0600); err != nil {
				t.Fatal(err)
			}
			env["TLS_LEAF_PEM"] = path
			if _, err := qualificationMatchedTLSFromEnvironment(qualificationMatchedTLSContractGetenv(env), target, "h1"); err == nil {
				t.Fatal("invalid or multiple trust accepted")
			}
		})
	}
	_, wrongSAN := qualificationMatchedTLSContractLeaf(t, "other.test")
	env := qualificationMatchedTLSContractEnv(t, wrongSAN, "127.0.0.1:4443")
	if _, err := qualificationMatchedTLSFromEnvironment(qualificationMatchedTLSContractGetenv(env), target, "h1"); err == nil {
		t.Fatal("wrong SAN trust accepted")
	}
}

func TestQualificationMatchedTLSStandardVerificationAndDERPin(t *testing.T) {
	server, leaf, _ := qualificationMatchedTLSContractServer(t, "h1", http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	matched, _, _, _ := qualificationMatchedTLSContractClient(t, server, leaf, "h1")
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	wrongHost := matched.config.Clone()
	wrongHost.ServerName = "other.test"
	dialer := tls.Dialer{Config: wrongHost, NetDialer: &net.Dialer{Timeout: time.Second}}
	conn, err := dialer.DialContext(ctx, "tcp", matched.connect)
	if conn != nil {
		_ = conn.Close()
	}
	var hostnameError x509.HostnameError
	if !errors.As(err, &hostnameError) {
		t.Fatalf("wrong hostname error=%v, want standard x509.HostnameError", err)
	}
	other, otherPEM := qualificationMatchedTLSContractLeaf(t, "public.test")
	otherEnv := qualificationMatchedTLSContractEnv(t, otherPEM, matched.connect)
	untrusted, err := qualificationMatchedTLSFromEnvironment(qualificationMatchedTLSContractGetenv(otherEnv), "https://"+matched.authority+"/direct/small", "h1")
	if err != nil {
		t.Fatal(err)
	}
	conn, err = untrusted.dialTLSContext(ctx, "tcp", untrusted.authority)
	if conn != nil {
		_ = conn.Close()
	}
	var unknown x509.UnknownAuthorityError
	if !errors.As(err, &unknown) || untrusted.receipt().VerificationFailureCount != 1 || untrusted.receipt().VerifiedConnectionCount != 0 {
		t.Fatalf("wrong leaf trust error=%v receipt=%+v", err, untrusted.receipt())
	}
	conn, err = matched.dialTLSContext(ctx, "tcp", matched.authority)
	if err != nil {
		t.Fatal(err)
	}
	state := conn.(*tls.Conn).ConnectionState()
	_ = conn.Close()
	for _, name := range []string{"pin", "no-standard-chain", "SNI", "ALPN", "version", "group", "resumption"} {
		t.Run(name, func(t *testing.T) {
			bad := state
			switch name {
			case "pin":
				bad.PeerCertificates = []*x509.Certificate{other.Leaf}
				bad.VerifiedChains = [][]*x509.Certificate{{other.Leaf}}
			case "no-standard-chain":
				bad.VerifiedChains = nil
			case "SNI":
				bad.ServerName = "other.test"
			case "ALPN":
				bad.NegotiatedProtocol = "h2"
			case "version":
				bad.Version = tls.VersionTLS12
			case "group":
				bad.CurveID = tls.CurveP256
			case "resumption":
				bad.DidResume = true
			}
			if err := matched.config.VerifyConnection(bad); err == nil {
				t.Fatal("invalid verified connection accepted")
			}
		})
	}
}

func TestQualificationMatchedTLSProtocolAndRedirectEnforcement(t *testing.T) {
	for _, serverProtocol := range []string{"h1", "h2"} {
		t.Run("wrong-ALPN-"+serverProtocol, func(t *testing.T) {
			var requests atomic.Int64
			server, leaf, _ := qualificationMatchedTLSContractServer(t, serverProtocol, http.HandlerFunc(func(http.ResponseWriter, *http.Request) { requests.Add(1) }))
			clientProtocol := "h1"
			if serverProtocol == "h1" {
				clientProtocol = "h2"
			}
			matched, client, target, _ := qualificationMatchedTLSContractClient(t, server, leaf, clientProtocol)
			response, err := client.Get(target)
			if response != nil {
				_ = response.Body.Close()
			}
			if err == nil || requests.Load() != 0 || matched.receipt().VerificationFailureCount != 1 || matched.receipt().VerifiedConnectionCount != 0 {
				t.Fatalf("ALPN mismatch error=%v requests=%d receipt=%+v", err, requests.Load(), matched.receipt())
			}
		})
	}
	var requests atomic.Int64
	server, leaf, _ := qualificationMatchedTLSContractServer(t, "h1", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		http.Redirect(w, r, "https://other.test:4443/direct/small", http.StatusFound)
	}))
	matched, client, target, _ := qualificationMatchedTLSContractClient(t, server, leaf, "h1")
	response, err := client.Get(target)
	if response != nil {
		_ = response.Body.Close()
	}
	if err == nil || !strings.Contains(err.Error(), "redirects are forbidden") || requests.Load() != 1 || matched.receipt().ConnectionAttempts != 1 {
		t.Fatalf("redirect error=%v requests=%d receipt=%+v", err, requests.Load(), matched.receipt())
	}
	for _, authority := range []string{"other.test:4443", matched.connect, "public.test:1"} {
		if conn, err := matched.dialContext(t.Context(), "tcp", authority); err == nil || conn != nil {
			if conn != nil {
				_ = conn.Close()
			}
			t.Fatal("different dial authority accepted")
		}
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if conn, err := matched.dialContext(ctx, "tcp", matched.authority); !errors.Is(err, context.Canceled) || conn != nil {
		if conn != nil {
			_ = conn.Close()
		}
		t.Fatalf("dial cancellation=%v", err)
	}
}
