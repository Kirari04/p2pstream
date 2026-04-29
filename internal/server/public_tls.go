package server

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net"
	"sort"
	"strings"
	"time"
)

type publicTLSSelector struct {
	exact    map[string]*tls.Certificate
	wildcard []publicWildcardCertificate
	fallback *tls.Certificate
}

type publicWildcardCertificate struct {
	pattern string
	suffix  string
	cert    *tls.Certificate
}

func newPublicTLSConfig(listenerID int64, snap *publicProxySnapshot) (*tls.Config, error) {
	selector := &publicTLSSelector{
		exact: make(map[string]*tls.Certificate),
	}

	for _, certConfig := range snap.CertsByListener[listenerID] {
		if !certConfig.Enabled {
			continue
		}
		cert, err := tls.LoadX509KeyPair(certConfig.CertPath, certConfig.KeyPath)
		if err != nil {
			return nil, err
		}
		pattern := normalizeHostPattern(certConfig.HostnamePattern)
		if strings.HasPrefix(pattern, "*.") {
			selector.wildcard = append(selector.wildcard, publicWildcardCertificate{
				pattern: pattern,
				suffix:  strings.TrimPrefix(pattern, "*"),
				cert:    &cert,
			})
			continue
		}
		selector.exact[pattern] = &cert
	}

	sort.SliceStable(selector.wildcard, func(i, j int) bool {
		return len(selector.wildcard[i].suffix) > len(selector.wildcard[j].suffix)
	})

	fallback, err := generateFallbackCertificate()
	if err != nil {
		return nil, err
	}
	selector.fallback = fallback

	return &tls.Config{
		MinVersion:     tls.VersionTLS12,
		GetCertificate: selector.GetCertificate,
	}, nil
}

func (s *publicTLSSelector) GetCertificate(hello *tls.ClientHelloInfo) (*tls.Certificate, error) {
	serverName := normalizeHostPattern(hello.ServerName)
	if serverName != "" {
		if cert := s.exact[serverName]; cert != nil {
			return cert, nil
		}
		for _, wildcard := range s.wildcard {
			if strings.HasSuffix(serverName, wildcard.suffix) &&
				len(serverName) > len(strings.TrimPrefix(wildcard.suffix, ".")) {
				return wildcard.cert, nil
			}
		}
	}
	return s.fallback, nil
}

func generateFallbackCertificate() (*tls.Certificate, error) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return nil, err
	}

	serialLimit := new(big.Int).Lsh(big.NewInt(1), 128)
	serial, err := rand.Int(rand.Reader, serialLimit)
	if err != nil {
		return nil, err
	}

	now := time.Now()
	template := &x509.Certificate{
		SerialNumber: serial,
		Subject: pkix.Name{
			CommonName: "p2pstream.local",
		},
		NotBefore:             now.Add(-time.Minute),
		NotAfter:              now.Add(24 * time.Hour),
		KeyUsage:              x509.KeyUsageKeyEncipherment | x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		DNSNames:              []string{"localhost", "p2pstream.local"},
		IPAddresses: []net.IP{
			net.ParseIP("127.0.0.1"),
			net.ParseIP("::1"),
		},
	}

	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		return nil, err
	}

	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
	cert, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		return nil, err
	}
	return &cert, nil
}
