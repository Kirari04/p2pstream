package server

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"os"

	"p2pstream/internal/config"
)

// NewManagementTLSConfig builds the TLS configuration for the management API.
// The boolean return value is true when the management listener should serve TLS.
func NewManagementTLSConfig(cfg *config.Config) (*tls.Config, bool, error) {
	if cfg == nil {
		return nil, false, nil
	}
	hasCert := cfg.ManagementTLSCertFile != ""
	hasKey := cfg.ManagementTLSKeyFile != ""
	if hasCert != hasKey {
		return nil, false, fmt.Errorf("MANAGEMENT_TLS_CERT_FILE and MANAGEMENT_TLS_KEY_FILE must be set together")
	}
	if cfg.ManagementTLSClientCAFile != "" && (!hasCert || !hasKey) {
		return nil, false, fmt.Errorf("MANAGEMENT_TLS_CLIENT_CA_FILE requires MANAGEMENT_TLS_CERT_FILE and MANAGEMENT_TLS_KEY_FILE")
	}
	if !hasCert {
		return nil, false, nil
	}

	cert, err := tls.LoadX509KeyPair(cfg.ManagementTLSCertFile, cfg.ManagementTLSKeyFile)
	if err != nil {
		return nil, false, fmt.Errorf("load management TLS certificate: %w", err)
	}

	tlsConfig := &tls.Config{
		MinVersion:   tls.VersionTLS12,
		Certificates: []tls.Certificate{cert},
	}

	if cfg.ManagementTLSClientCAFile != "" {
		clientCAs, err := loadCertPool(cfg.ManagementTLSClientCAFile)
		if err != nil {
			return nil, false, fmt.Errorf("load management client CA: %w", err)
		}
		tlsConfig.ClientCAs = clientCAs
		tlsConfig.ClientAuth = tls.VerifyClientCertIfGiven
	}

	return tlsConfig, true, nil
}

func loadCertPool(path string) (*x509.CertPool, error) {
	pemBytes, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(pemBytes) {
		return nil, fmt.Errorf("no PEM certificates found in %q", path)
	}
	return pool, nil
}
