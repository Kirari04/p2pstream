package agent

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestManagementWebSocketURL(t *testing.T) {
	tests := []struct {
		name    string
		mgmtURL string
		want    string
		wantErr bool
	}{
		{name: "http", mgmtURL: "http://example.test:8081", want: "ws://example.test:8081/ws"},
		{name: "https", mgmtURL: "https://example.test:8081", want: "wss://example.test:8081/ws"},
		{name: "base path", mgmtURL: "https://example.test/base/", want: "wss://example.test/base/ws"},
		{name: "unsupported", mgmtURL: "ftp://example.test", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := managementWebSocketURL(tt.mgmtURL)
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error")
				}
				return
			}
			if err != nil {
				t.Fatalf("managementWebSocketURL() error = %v", err)
			}
			if got != tt.want {
				t.Fatalf("managementWebSocketURL() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestManagementHTTPClientPrivateCATrustAndClientCertificate(t *testing.T) {
	caCert, caKey := agentTestCA(t)
	serverCertPEM, serverKeyPEM := agentTestCertificate(t, caCert, caKey, agentTestCertificateOptions{
		dnsNames: []string{"localhost"},
		ips:      []net.IP{net.ParseIP("127.0.0.1")},
		usage:    []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	})
	clientCertPEM, clientKeyPEM := agentTestCertificate(t, caCert, caKey, agentTestCertificateOptions{
		usage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	})

	caPath := agentWriteTestFile(t, "ca.pem", pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caCert.Raw}))
	clientCertPath := agentWriteTestFile(t, "agent.crt.pem", clientCertPEM)
	clientKeyPath := agentWriteTestFile(t, "agent.key.pem", clientKeyPEM)

	serverCert, err := tls.X509KeyPair(serverCertPEM, serverKeyPEM)
	if err != nil {
		t.Fatalf("server key pair: %v", err)
	}
	clientCAs := x509.NewCertPool()
	clientCAs.AddCert(caCert)

	sawClientCert := make(chan bool, 1)
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sawClientCert <- r.TLS != nil && len(r.TLS.VerifiedChains) > 0
		_, _ = w.Write([]byte("ok"))
	}))
	srv.TLS = &tls.Config{
		MinVersion:   tls.VersionTLS12,
		Certificates: []tls.Certificate{serverCert},
		ClientCAs:    clientCAs,
		ClientAuth:   tls.VerifyClientCertIfGiven,
	}
	srv.StartTLS()
	defer srv.Close()

	t.Run("private CA verifies", func(t *testing.T) {
		client, err := managementHTTPClient(Options{
			ManagementURL:    srv.URL,
			ManagementCAFile: caPath,
		})
		if err != nil {
			t.Fatalf("managementHTTPClient() error = %v", err)
		}
		resp, err := client.Get(srv.URL)
		if err != nil {
			t.Fatalf("GET with private CA: %v", err)
		}
		resp.Body.Close()
		<-sawClientCert
	})

	t.Run("missing CA fails", func(t *testing.T) {
		client, err := managementHTTPClient(Options{ManagementURL: srv.URL})
		if err != nil {
			t.Fatalf("managementHTTPClient() error = %v", err)
		}
		resp, err := client.Get(srv.URL)
		if err == nil {
			resp.Body.Close()
			t.Fatal("expected private CA server verification to fail without CA file")
		}
	})

	t.Run("client certificate is sent", func(t *testing.T) {
		client, err := managementHTTPClient(Options{
			ManagementURL:    srv.URL,
			ManagementCAFile: caPath,
			TLSCertFile:      clientCertPath,
			TLSKeyFile:       clientKeyPath,
		})
		if err != nil {
			t.Fatalf("managementHTTPClient() error = %v", err)
		}
		resp, err := client.Get(srv.URL)
		if err != nil {
			t.Fatalf("GET with client cert: %v", err)
		}
		resp.Body.Close()
		if got := <-sawClientCert; !got {
			t.Fatal("server did not see a verified client certificate")
		}
	})
}

func TestManagementHTTPClientValidation(t *testing.T) {
	tests := []struct {
		name string
		opts Options
	}{
		{
			name: "partial client certificate",
			opts: Options{ManagementURL: "https://example.test", TLSCertFile: "/tmp/agent.crt.pem"},
		},
		{
			name: "client certificate with http",
			opts: Options{ManagementURL: "http://example.test", TLSCertFile: "/tmp/agent.crt.pem", TLSKeyFile: "/tmp/agent.key.pem"},
		},
		{
			name: "CA with http",
			opts: Options{ManagementURL: "http://example.test", ManagementCAFile: "/tmp/ca.pem"},
		},
		{
			name: "unsupported scheme",
			opts: Options{ManagementURL: "unix:///tmp/socket"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := managementHTTPClient(tt.opts); err == nil {
				t.Fatal("expected validation error")
			}
		})
	}
}

type agentTestCertificateOptions struct {
	dnsNames []string
	ips      []net.IP
	uris     []string
	usage    []x509.ExtKeyUsage
}

func agentTestCA(t *testing.T) (*x509.Certificate, *rsa.PrivateKey) {
	t.Helper()

	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate CA key: %v", err)
	}
	template := &x509.Certificate{
		SerialNumber:          agentTestSerial(t),
		Subject:               pkix.Name{CommonName: "p2pstream agent test CA"},
		NotBefore:             time.Now().Add(-time.Minute),
		NotAfter:              time.Now().Add(time.Hour),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		IsCA:                  true,
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("create CA certificate: %v", err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatalf("parse CA certificate: %v", err)
	}
	return cert, key
}

func agentTestCertificate(t *testing.T, caCert *x509.Certificate, caKey *rsa.PrivateKey, opts agentTestCertificateOptions) ([]byte, []byte) {
	t.Helper()

	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate certificate key: %v", err)
	}
	template := &x509.Certificate{
		SerialNumber: agentTestSerial(t),
		Subject:      pkix.Name{CommonName: "p2pstream agent test certificate"},
		NotBefore:    time.Now().Add(-time.Minute),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  opts.usage,
		DNSNames:     opts.dnsNames,
		IPAddresses:  opts.ips,
	}
	for _, rawURI := range opts.uris {
		uri, err := url.Parse(rawURI)
		if err != nil {
			t.Fatalf("parse URI %q: %v", rawURI, err)
		}
		template.URIs = append(template.URIs, uri)
	}
	der, err := x509.CreateCertificate(rand.Reader, template, caCert, &key.PublicKey, caKey)
	if err != nil {
		t.Fatalf("create certificate: %v", err)
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
	return certPEM, keyPEM
}

func agentTestSerial(t *testing.T) *big.Int {
	t.Helper()
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		t.Fatalf("generate serial: %v", err)
	}
	return serial
}

func agentWriteTestFile(t *testing.T, name string, contents []byte) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, contents, 0600); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
	return path
}
