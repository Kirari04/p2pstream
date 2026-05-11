package main_test

import (
	"context"
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
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/coder/websocket"

	p2pstreamv1 "p2pstream/gen/proto/p2pstream/v1"
	"p2pstream/gen/proto/p2pstream/v1/p2pstreamv1connect"
	"p2pstream/internal/config"
	"p2pstream/internal/server"
)

const (
	mtlsAgentID    = "mtls-agent"
	mtlsAgentToken = "mtls-token"
)

func TestManagementAgentMTLSReportStats(t *testing.T) {
	fixture := newManagementMTLSTestFixture(t)

	tests := []struct {
		name        string
		certURI     string
		token       string
		wantErrCode connect.Code
	}{
		{
			name:        "no client certificate rejected",
			token:       mtlsAgentToken,
			wantErrCode: connect.CodeUnauthenticated,
		},
		{
			name:        "wrong URI SAN rejected",
			certURI:     "spiffe://p2pstream/agent/wrong-agent",
			token:       mtlsAgentToken,
			wantErrCode: connect.CodeUnauthenticated,
		},
		{
			name:    "matching URI SAN accepted",
			certURI: "spiffe://p2pstream/agent/" + mtlsAgentID,
			token:   mtlsAgentToken,
		},
		{
			name:        "valid certificate still requires bearer token",
			certURI:     "spiffe://p2pstream/agent/" + mtlsAgentID,
			token:       "wrong-token",
			wantErrCode: connect.CodeUnauthenticated,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := p2pstreamv1connect.NewAgentManagementServiceClient(
				fixture.httpClient(t, tt.certURI),
				fixture.server.URL,
				connect.WithGRPC(),
			)
			req := connect.NewRequest(&p2pstreamv1.AgentStatsRequest{AgentPublicId: mtlsAgentID})
			req.Header().Set("Authorization", "Bearer "+tt.token)
			_, err := client.ReportStats(context.Background(), req)
			if tt.wantErrCode != 0 {
				requireConnectCode(t, err, tt.wantErrCode)
				return
			}
			if err != nil {
				t.Fatalf("ReportStats() error = %v", err)
			}
		})
	}
}

func TestManagementAgentMTLSWebSocket(t *testing.T) {
	fixture := newManagementMTLSTestFixture(t)
	wsURL := "wss" + fixture.server.URL[len("https"):] + "/ws"

	tests := []struct {
		name    string
		certURI string
		token   string
		wantErr bool
	}{
		{
			name:    "no client certificate rejected",
			token:   mtlsAgentToken,
			wantErr: true,
		},
		{
			name:    "wrong URI SAN rejected",
			certURI: "spiffe://p2pstream/agent/wrong-agent",
			token:   mtlsAgentToken,
			wantErr: true,
		},
		{
			name:    "matching URI SAN accepted",
			certURI: "spiffe://p2pstream/agent/" + mtlsAgentID,
			token:   mtlsAgentToken,
		},
		{
			name:    "valid certificate still requires bearer token",
			certURI: "spiffe://p2pstream/agent/" + mtlsAgentID,
			token:   "wrong-token",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			conn, _, err := websocket.Dial(context.Background(), wsURL, &websocket.DialOptions{
				HTTPClient: fixture.httpClient(t, tt.certURI),
				HTTPHeader: http.Header{
					"Authorization":        []string{"Bearer " + tt.token},
					"X-P2PStream-Agent-ID": []string{mtlsAgentID},
				},
			})
			if tt.wantErr {
				if err == nil {
					conn.Close(websocket.StatusNormalClosure, "test complete")
					t.Fatal("expected WebSocket dial to fail")
				}
				return
			}
			if err != nil {
				t.Fatalf("WebSocket dial error = %v", err)
			}
			conn.Close(websocket.StatusNormalClosure, "test complete")
		})
	}
}

type managementMTLSTestFixture struct {
	server *httptest.Server
	caCert *x509.Certificate
	caKey  *rsa.PrivateKey
}

func newManagementMTLSTestFixture(t *testing.T) managementMTLSTestFixture {
	t.Helper()

	caCert, caKey := managementTestCA(t)
	serverCertPEM, serverKeyPEM := managementTestCertificate(t, caCert, caKey, managementTestCertificateOptions{
		dnsNames: []string{"localhost"},
		ips:      []net.IP{net.ParseIP("127.0.0.1")},
		usage:    []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	})
	serverCert, err := tls.X509KeyPair(serverCertPEM, serverKeyPEM)
	if err != nil {
		t.Fatalf("server key pair: %v", err)
	}

	app := server.NewApp(&config.Config{
		ManagementTLSClientCAFile: "agents-ca.pem",
		BootstrapAgentID:          mtlsAgentID,
		BootstrapAgentName:        "mTLS Agent",
		BootstrapAgentToken:       mtlsAgentToken,
	}, newTestDB(t))
	mux := http.NewServeMux()
	app.RegisterManagementRoutes(mux)

	clientCAs := x509.NewCertPool()
	clientCAs.AddCert(caCert)
	protocols := new(http.Protocols)
	protocols.SetHTTP1(true)
	protocols.SetHTTP2(true)

	srv := httptest.NewUnstartedServer(server.ManagementClientCertificateMiddleware(mux))
	srv.Config.Protocols = protocols
	srv.TLS = &tls.Config{
		MinVersion:   tls.VersionTLS12,
		Certificates: []tls.Certificate{serverCert},
		ClientCAs:    clientCAs,
		ClientAuth:   tls.VerifyClientCertIfGiven,
	}
	srv.StartTLS()
	t.Cleanup(srv.Close)

	return managementMTLSTestFixture{server: srv, caCert: caCert, caKey: caKey}
}

func (f managementMTLSTestFixture) httpClient(t *testing.T, clientURI string) *http.Client {
	t.Helper()

	rootCAs := x509.NewCertPool()
	rootCAs.AddCert(f.caCert)
	tlsConfig := &tls.Config{
		MinVersion: tls.VersionTLS12,
		RootCAs:    rootCAs,
	}
	if clientURI != "" {
		certPEM, keyPEM := managementTestCertificate(t, f.caCert, f.caKey, managementTestCertificateOptions{
			uris:  []string{clientURI},
			usage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
		})
		cert, err := tls.X509KeyPair(certPEM, keyPEM)
		if err != nil {
			t.Fatalf("client key pair: %v", err)
		}
		tlsConfig.Certificates = []tls.Certificate{cert}
	}
	base := http.DefaultTransport.(*http.Transport).Clone()
	base.TLSClientConfig = tlsConfig
	return &http.Client{Transport: base}
}

type managementTestCertificateOptions struct {
	dnsNames []string
	ips      []net.IP
	uris     []string
	usage    []x509.ExtKeyUsage
}

func managementTestCA(t *testing.T) (*x509.Certificate, *rsa.PrivateKey) {
	t.Helper()

	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate CA key: %v", err)
	}
	template := &x509.Certificate{
		SerialNumber:          managementTestSerial(t),
		Subject:               pkix.Name{CommonName: "p2pstream management test CA"},
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

func managementTestCertificate(t *testing.T, caCert *x509.Certificate, caKey *rsa.PrivateKey, opts managementTestCertificateOptions) ([]byte, []byte) {
	t.Helper()

	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate certificate key: %v", err)
	}
	template := &x509.Certificate{
		SerialNumber: managementTestSerial(t),
		Subject:      pkix.Name{CommonName: "p2pstream management test certificate"},
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

func managementTestSerial(t *testing.T) *big.Int {
	t.Helper()
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		t.Fatalf("generate serial: %v", err)
	}
	return serial
}
