package server

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"math/big"
	"net"
	"os"
	"sort"
	"strings"
	"sync/atomic"
	"time"

	"golang.org/x/crypto/acme"
)

type publicTLSSelector struct {
	exact    map[string]*tls.Certificate
	wildcard []publicWildcardCertificate
	fallback *tls.Certificate
	acme     *publicACMEManager
}

type publicWildcardCertificate struct {
	pattern string
	suffix  string
	cert    *tls.Certificate
}

func newPublicTLSConfig(listenerID int64, snap *publicProxySnapshot, acmeManager *publicACMEManager) (*tls.Config, error) {
	tlsConfig, _, err := newPublicTLSConfigWithSelectorStore(listenerID, snap, acmeManager)
	return tlsConfig, err
}

func newPublicTLSConfigWithSelectorStore(listenerID int64, snap *publicProxySnapshot, acmeManager *publicACMEManager) (*tls.Config, *publicTLSSelectorStore, error) {
	selector, err := newPublicTLSSelector(listenerID, snap, acmeManager, nil)
	if err != nil {
		return nil, nil, err
	}
	store := newPublicTLSSelectorStore(selector)
	return &tls.Config{
		MinVersion:     tls.VersionTLS12,
		NextProtos:     []string{acme.ALPNProto, "h2", "http/1.1"},
		GetCertificate: store.GetCertificate,
	}, store, nil
}

type publicTLSSelectorStore struct {
	selector atomic.Pointer[publicTLSSelector]
}

func newPublicTLSSelectorStore(selector *publicTLSSelector) *publicTLSSelectorStore {
	store := &publicTLSSelectorStore{}
	store.selector.Store(selector)
	return store
}

func (s *publicTLSSelectorStore) GetCertificate(hello *tls.ClientHelloInfo) (*tls.Certificate, error) {
	if s == nil {
		return nil, errors.New("public TLS selector store is not initialized")
	}
	selector := s.selector.Load()
	if selector == nil {
		return nil, errors.New("public TLS selector is not initialized")
	}
	return selector.GetCertificate(hello)
}

func (s *publicTLSSelectorStore) refresh(listenerID int64, snap *publicProxySnapshot, acmeManager *publicACMEManager) error {
	selector, err := s.buildRefresh(listenerID, snap, acmeManager)
	if err != nil {
		return err
	}
	s.publish(selector)
	return nil
}

func (s *publicTLSSelectorStore) buildRefresh(listenerID int64, snap *publicProxySnapshot, acmeManager *publicACMEManager) (*publicTLSSelector, error) {
	if s == nil {
		return nil, errors.New("public TLS selector store is not initialized")
	}
	selector, err := newPublicTLSSelector(listenerID, snap, acmeManager, s.fallbackCertificate())
	if err != nil {
		return nil, err
	}
	return selector, nil
}

func (s *publicTLSSelectorStore) publish(selector *publicTLSSelector) {
	if s == nil || selector == nil {
		return
	}
	s.selector.Store(selector)
}

func (s *publicTLSSelectorStore) fallbackCertificate() *tls.Certificate {
	if s == nil {
		return nil
	}
	selector := s.selector.Load()
	if selector == nil {
		return nil
	}
	return selector.fallback
}

func newPublicTLSSelector(listenerID int64, snap *publicProxySnapshot, acmeManager *publicACMEManager, fallback *tls.Certificate) (*publicTLSSelector, error) {
	if snap == nil {
		return nil, errors.New("public proxy snapshot is required")
	}
	selector := &publicTLSSelector{
		exact: make(map[string]*tls.Certificate),
		acme:  acmeManager,
	}

	for _, certConfig := range snap.CertsByListener[listenerID] {
		if !certConfig.Enabled {
			continue
		}
		cert, err := tls.LoadX509KeyPair(certConfig.CertPath, certConfig.KeyPath)
		if err != nil {
			if certConfig.Source == publicTLSCertificateSourceACME && (errors.Is(err, os.ErrNotExist) || certConfig.CertPath == "" || certConfig.KeyPath == "") {
				continue
			}
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

	if fallback == nil {
		var err error
		fallback, err = generateFallbackCertificate()
		if err != nil {
			return nil, err
		}
	}
	selector.fallback = fallback

	return selector, nil
}

func (s *publicTLSSelector) GetCertificate(hello *tls.ClientHelloInfo) (*tls.Certificate, error) {
	serverName := normalizeHostPattern(hello.ServerName)
	if s.acme != nil && serverName != "" && clientSupportsALPN(hello, acme.ALPNProto) {
		if cert := s.acme.TLSALPNCertificate(serverName); cert != nil {
			return cert, nil
		}
	}
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

func clientSupportsALPN(hello *tls.ClientHelloInfo, proto string) bool {
	for _, supported := range hello.SupportedProtos {
		if supported == proto {
			return true
		}
	}
	return false
}

func generateFallbackCertificate() (*tls.Certificate, error) {
	certPEM, keyPEM, err := generateSelfSignedCertificatePEM(24 * time.Hour)
	if err != nil {
		return nil, err
	}
	cert, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		return nil, err
	}
	return &cert, nil
}

func generateManagedSelfSignedCertificatePEM() ([]byte, []byte, error) {
	return generateSelfSignedCertificatePEM(time.Duration(defaultPublicSelfSignedValidityDays) * 24 * time.Hour)
}

func generatePublicSelfSignedCertificatePEM(hostnamePattern string, validFor time.Duration) ([]byte, []byte, *x509.Certificate, error) {
	hostnamePattern = normalizeHostPattern(hostnamePattern)
	if hostnamePattern == "" {
		return nil, nil, nil, errors.New("hostname pattern is required")
	}
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return nil, nil, nil, err
	}

	serialLimit := new(big.Int).Lsh(big.NewInt(1), 128)
	serial, err := rand.Int(rand.Reader, serialLimit)
	if err != nil {
		return nil, nil, nil, err
	}

	now := time.Now().UTC()
	template := &x509.Certificate{
		SerialNumber: serial,
		Subject: pkix.Name{
			CommonName: hostnamePattern,
		},
		NotBefore:             now.Add(-time.Minute),
		NotAfter:              now.Add(validFor),
		KeyUsage:              x509.KeyUsageKeyEncipherment | x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
	}
	if ip := net.ParseIP(hostnamePattern); ip != nil {
		template.IPAddresses = []net.IP{ip}
	} else {
		template.DNSNames = []string{hostnamePattern}
	}

	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		return nil, nil, nil, err
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, nil, nil, err
	}

	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
	return certPEM, keyPEM, cert, nil
}

func generateSelfSignedCertificatePEM(validFor time.Duration) ([]byte, []byte, error) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return nil, nil, err
	}

	serialLimit := new(big.Int).Lsh(big.NewInt(1), 128)
	serial, err := rand.Int(rand.Reader, serialLimit)
	if err != nil {
		return nil, nil, err
	}

	now := time.Now()
	template := &x509.Certificate{
		SerialNumber: serial,
		Subject: pkix.Name{
			CommonName: "p2pstream.local",
		},
		NotBefore:             now.Add(-time.Minute),
		NotAfter:              now.Add(validFor),
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
		return nil, nil, err
	}

	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
	return certPEM, keyPEM, nil
}
