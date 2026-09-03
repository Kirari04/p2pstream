package cmd

import (
	"context"
	"crypto/tls"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"

	"p2pstream/internal/agentupdatecatalog"
	"p2pstream/internal/buildinfo"
	"p2pstream/internal/config"
	"p2pstream/internal/tunnel"
)

func newAgentUpdateCatalog(cfg *config.Config) (*agentupdatecatalog.Catalog, error) {
	if cfg == nil || !cfg.AgentUpdatesEnabled {
		return nil, nil
	}
	httpClient := newAgentUpdateHTTPClient(time.Duration(cfg.AgentUpdateHTTPTimeoutMillis) * time.Millisecond)
	return agentupdatecatalog.New(agentupdatecatalog.Options{
		Repository:      cfg.AgentUpdateRepository,
		Channel:         cfg.AgentUpdateChannel,
		StatePath:       cfg.AgentUpdateCatalogStateFile,
		RefreshInterval: time.Duration(cfg.AgentUpdateCatalogRefreshMillis) * time.Millisecond,
		HTTPClient:      httpClient,
		ServerVersion:   buildinfo.Version,
		ProtocolVersion: tunnel.ProtocolVersion,
	})
}

func newAgentUpdateHTTPClient(timeout time.Duration) *http.Client {
	return &http.Client{
		Timeout: timeout,
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12},
		},
		CheckRedirect: trustedAgentUpdateRedirect,
	}
}

func primeAgentUpdateCatalog(ctx context.Context, catalog *agentupdatecatalog.Catalog) error {
	if catalog == nil {
		return nil
	}
	_, err := catalog.Latest(ctx)
	return err
}

func trustedAgentUpdateRedirect(request *http.Request, via []*http.Request) error {
	if len(via) >= 5 {
		return errors.New("too many agent update catalog redirects")
	}
	if request.URL.Scheme != "https" || !trustedAgentUpdateHost(request.URL) {
		return errors.New("agent update catalog redirect left the trusted GitHub asset boundary")
	}
	return nil
}

func trustedAgentUpdateHost(value *url.URL) bool {
	host := strings.ToLower(value.Hostname())
	switch host {
	case "github.com", "api.github.com", "release-assets.githubusercontent.com", "objects.githubusercontent.com":
		return true
	default:
		return false
	}
}
