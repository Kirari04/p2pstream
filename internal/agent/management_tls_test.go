package agent

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
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

func TestManagementTunnelURL(t *testing.T) {
	tests := []struct {
		name    string
		mgmtURL string
		want    string
		wantErr bool
	}{
		{name: "http", mgmtURL: "http://example.test:8081", want: "http://example.test:8081/agent/tunnel"},
		{name: "https", mgmtURL: "https://example.test:8081", want: "https://example.test:8081/agent/tunnel"},
		{name: "base path", mgmtURL: "https://example.test/base/", want: "https://example.test/base/agent/tunnel"},
		{name: "unsupported", mgmtURL: "ftp://example.test", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := managementTunnelURL(tt.mgmtURL)
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error")
				}
				return
			}
			if err != nil {
				t.Fatalf("managementTunnelURL() error = %v", err)
			}
			if got != tt.want {
				t.Fatalf("managementTunnelURL() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestManagementTunnelHTTPClientForcesHTTP1ALPN(t *testing.T) {
	protoCh := make(chan string, 1)
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		protoCh <- r.Proto
		w.WriteHeader(http.StatusOK)
	}))
	protocols := new(http.Protocols)
	protocols.SetHTTP1(true)
	protocols.SetHTTP2(true)
	srv.Config.Protocols = protocols
	srv.StartTLS()
	defer srv.Close()

	baseTransport := srv.Client().Transport.(*http.Transport).Clone()
	if baseTransport.TLSClientConfig == nil {
		baseTransport.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS12}
	} else {
		baseTransport.TLSClientConfig = baseTransport.TLSClientConfig.Clone()
	}
	baseTransport.TLSClientConfig.NextProtos = []string{"h2", "http/1.1"}

	client, err := managementTunnelHTTPClient(&http.Client{Transport: baseTransport})
	if err != nil {
		t.Fatalf("managementTunnelHTTPClient() error = %v", err)
	}
	resp, err := client.Get(srv.URL + "/agent/tunnel")
	if err != nil {
		t.Fatalf("tunnel GET: %v", err)
	}
	resp.Body.Close()

	select {
	case got := <-protoCh:
		if got != "HTTP/1.1" {
			t.Fatalf("tunnel request protocol = %q, want HTTP/1.1", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for tunnel request")
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

	t.Run("private CA verifies from base64 PEM", func(t *testing.T) {
		client, err := managementHTTPClient(Options{
			ManagementURL:         srv.URL,
			ManagementCAPEMBase64: base64.StdEncoding.EncodeToString([]byte(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caCert.Raw}))),
		})
		if err != nil {
			t.Fatalf("managementHTTPClient() error = %v", err)
		}
		resp, err := client.Get(srv.URL)
		if err != nil {
			t.Fatalf("GET with private CA base64: %v", err)
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

	t.Run("wrong CA fails", func(t *testing.T) {
		wrongCACert, _ := agentTestCA(t)
		wrongCAPath := agentWriteTestFile(t, "wrong-ca.pem", pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: wrongCACert.Raw}))
		client, err := managementHTTPClient(Options{
			ManagementURL:    srv.URL,
			ManagementCAFile: wrongCAPath,
		})
		if err != nil {
			t.Fatalf("managementHTTPClient() error = %v", err)
		}
		resp, err := client.Get(srv.URL)
		if err == nil {
			resp.Body.Close()
			t.Fatal("expected private CA server verification to fail with wrong CA")
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
			name: "http rejected by default",
			opts: Options{ManagementURL: "http://example.test"},
		},
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
			name: "base64 CA with http",
			opts: Options{ManagementURL: "http://example.test", ManagementCAPEMBase64: "abc"},
		},
		{
			name: "invalid base64 CA",
			opts: Options{ManagementURL: "https://example.test", ManagementCAPEMBase64: "not base64!"},
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

func TestManagementHTTPClientAllowsExplicitInsecureHTTP(t *testing.T) {
	client, err := managementHTTPClient(Options{
		ManagementURL:           "http://example.test",
		AllowInsecureManagement: true,
	})
	if err != nil {
		t.Fatalf("managementHTTPClient() error = %v", err)
	}
	if client == nil {
		t.Fatal("managementHTTPClient() returned nil client")
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
