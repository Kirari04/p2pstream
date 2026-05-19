package server

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
	"fmt"
	"net"
	"net/url"
	"strings"
	"time"

	p2pstreamv1 "p2pstream/gen/proto/p2pstream/v1"
)

func discoverCertificateDirect(ctx context.Context, rawURL string, timeout time.Duration) (*x509.Certificate, string, error) {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return nil, "", err
	}
	if parsed.Scheme != "https" {
		return nil, "", fmt.Errorf("environment certificate discovery requires https")
	}
	addr := parsed.Host
	if _, _, err := net.SplitHostPort(addr); err != nil {
		addr = net.JoinHostPort(addr, "443")
	}
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, "", err
	}
	if timeout <= 0 {
		timeout = defaultEnvironmentResponseHeaderTimeout
	}
	dialCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	// Discovery intentionally skips verification only to collect the unknown
	// certificate for explicit TOFU review. No authorization token or
	// management RPC is sent on this connection.
	dialer := tls.Dialer{Config: &tls.Config{
		MinVersion:         tls.VersionTLS12,
		ServerName:         host,
		InsecureSkipVerify: true,
	}}
	conn, err := dialer.DialContext(dialCtx, "tcp", addr)
	if err != nil {
		return nil, "", err
	}
	defer conn.Close()
	tlsConn, ok := conn.(*tls.Conn)
	if !ok {
		return nil, "", fmt.Errorf("connection did not use TLS")
	}
	state := tlsConn.ConnectionState()
	if len(state.PeerCertificates) == 0 {
		return nil, "", fmt.Errorf("remote endpoint did not present a certificate")
	}
	cert := state.PeerCertificates[0]
	return cert, certificateSHA256Fingerprint(cert), nil
}

func parseEnvironmentCertificatePEM(certPEM string) (*x509.Certificate, error) {
	block, _ := pem.Decode([]byte(strings.TrimSpace(certPEM)))
	if block == nil {
		return nil, fmt.Errorf("certificate is not PEM")
	}
	return x509.ParseCertificate(block.Bytes)
}

func environmentCertificatePEM(cert *x509.Certificate) string {
	if cert == nil {
		return ""
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: cert.Raw}))
}

func environmentCertificateProtoFromPEM(certPEM string) *p2pstreamv1.EnvironmentCertificate {
	cert, err := parseEnvironmentCertificatePEM(certPEM)
	if err != nil {
		return nil
	}
	return environmentCertificateProto(cert)
}

func environmentCertificateProto(cert *x509.Certificate) *p2pstreamv1.EnvironmentCertificate {
	if cert == nil {
		return nil
	}
	ips := make([]string, 0, len(cert.IPAddresses))
	for _, ip := range cert.IPAddresses {
		ips = append(ips, ip.String())
	}
	return &p2pstreamv1.EnvironmentCertificate{
		Pem:                 environmentCertificatePEM(cert),
		Sha256Fingerprint:   certificateSHA256Fingerprint(cert),
		Subject:             cert.Subject.String(),
		Issuer:              cert.Issuer.String(),
		DnsNames:            append([]string(nil), cert.DNSNames...),
		IpAddresses:         ips,
		NotBeforeUnixMillis: cert.NotBefore.UnixMilli(),
		NotAfterUnixMillis:  cert.NotAfter.UnixMilli(),
	}
}

func certificateSHA256Fingerprint(cert *x509.Certificate) string {
	if cert == nil {
		return ""
	}
	sum := sha256.Sum256(cert.Raw)
	encoded := strings.ToUpper(hex.EncodeToString(sum[:]))
	var b strings.Builder
	for i := 0; i < len(encoded); i += 2 {
		if i > 0 {
			b.WriteByte(':')
		}
		b.WriteString(encoded[i : i+2])
	}
	return b.String()
}

func normalizeEnvironmentCertificateFingerprint(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	value = strings.ReplaceAll(value, ":", "")
	value = strings.ReplaceAll(value, " ", "")
	return value
}

func verifyEnvironmentCertificateForURL(cert *x509.Certificate, rawURL string) error {
	if cert == nil {
		return fmt.Errorf("certificate is required")
	}
	now := time.Now()
	if now.Before(cert.NotBefore) || now.After(cert.NotAfter) {
		return fmt.Errorf("certificate is not currently valid")
	}
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return err
	}
	host := parsed.Hostname()
	if host == "" {
		return fmt.Errorf("management URL host is required")
	}
	return cert.VerifyHostname(host)
}

func trustedEnvironmentTLSConfig(envURL string, certPEM string, fingerprint string) (*tls.Config, error) {
	cert, err := parseEnvironmentCertificatePEM(certPEM)
	if err != nil {
		return nil, err
	}
	if err := verifyEnvironmentCertificateForURL(cert, envURL); err != nil {
		return nil, err
	}
	wantFingerprint := normalizeEnvironmentCertificateFingerprint(fingerprint)
	if wantFingerprint == "" {
		wantFingerprint = normalizeEnvironmentCertificateFingerprint(certificateSHA256Fingerprint(cert))
	}
	parsed, err := url.Parse(envURL)
	if err != nil {
		return nil, err
	}
	return &tls.Config{
		MinVersion: tls.VersionTLS12,
		ServerName: parsed.Hostname(),
		// Verification is handled below so we can pin the trusted leaf
		// fingerprint while still enforcing hostname and validity checks.
		InsecureSkipVerify: true,
		VerifyConnection: func(state tls.ConnectionState) error {
			if len(state.PeerCertificates) == 0 {
				return fmt.Errorf("remote endpoint did not present a certificate")
			}
			leaf := state.PeerCertificates[0]
			if normalizeEnvironmentCertificateFingerprint(certificateSHA256Fingerprint(leaf)) != wantFingerprint {
				return fmt.Errorf("remote certificate fingerprint changed")
			}
			return verifyEnvironmentCertificateForURL(leaf, envURL)
		},
	}, nil
}
