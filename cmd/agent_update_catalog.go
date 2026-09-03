package cmd

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"p2pstream/internal/agentupdate"
	"p2pstream/internal/agentupdatecatalog"
	"p2pstream/internal/buildinfo"
	"p2pstream/internal/config"
	"p2pstream/internal/tunnel"
)

func newAgentUpdateCatalog(cfg *config.Config) (*agentupdatecatalog.Catalog, error) {
	if cfg == nil || !cfg.AgentUpdatesEnabled {
		return nil, nil
	}
	rootJSON, err := readBoundedFile(cfg.AgentUpdateRootFile, agentupdate.MaxRootMetadataBytes)
	if err != nil {
		return nil, fmt.Errorf("read pinned agent update root: %w", err)
	}
	root, err := agentupdate.ParseRoot(rootJSON)
	if err != nil {
		return nil, fmt.Errorf("parse pinned agent update root: %w", err)
	}
	httpClient := newAgentUpdateHTTPClient(time.Duration(cfg.AgentUpdateHTTPTimeoutMillis) * time.Millisecond)
	return agentupdatecatalog.New(agentupdatecatalog.Options{
		Repository:      cfg.AgentUpdateRepository,
		Root:            root,
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

func readBoundedFile(path string, limit int) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, int64(limit)+1))
	if err != nil {
		return nil, err
	}
	if len(data) > limit {
		return nil, fmt.Errorf("file exceeds %d-byte limit", limit)
	}
	return data, nil
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
