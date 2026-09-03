package updater

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"strings"
)

const (
	manifestAssetName = "p2pstream_agent_update_manifest.json"
)

type GitHubSource struct {
	config  HostConfig
	version string
	client  *http.Client
}

func NewGitHubSource(config HostConfig, version string, client *http.Client) (*GitHubSource, error) {
	if err := config.Validate(); err != nil {
		return nil, err
	}
	if !validVersionForChannel(version, config.Channel) {
		return nil, fmt.Errorf("GitHub update source requires an exact %s release version", config.Channel)
	}
	if client == nil {
		client = newGitHubHTTPClient()
	}
	return &GitHubSource{config: config, version: version, client: client}, nil
}

func newGitHubHTTPClient() *http.Client {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	// Do not inherit HTTPS_PROXY for the release channel. Explicit proxy
	// support, if added later, must be pinned updater configuration.
	transport.Proxy = nil
	transport.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS12}
	return &http.Client{
		Transport: transport,
		Timeout:   defaultRequestTimeout,
		CheckRedirect: func(request *http.Request, via []*http.Request) error {
			if len(via) >= 5 || request.URL.Scheme != "https" || !trustedGitHubRedirectHost(request.URL.Hostname()) {
				return errors.New("update download redirected outside trusted GitHub asset hosts")
			}
			return nil
		},
	}
}

func trustedGitHubRedirectHost(host string) bool {
	return host == "github.com" || host == "objects.githubusercontent.com" || host == "release-assets.githubusercontent.com"
}

func (s *GitHubSource) FetchMetadata(ctx context.Context) ([]byte, error) {
	manifest, err := s.fetch(ctx, manifestAssetName, defaultMaxMetadata)
	if err != nil {
		return nil, err
	}
	return manifest, nil
}

func (s *GitHubSource) FetchArtifact(ctx context.Context, artifact Artifact) (io.ReadCloser, error) {
	if err := validateArtifact(artifact); err != nil {
		return nil, err
	}
	response, err := s.do(ctx, artifact.Name)
	if err != nil {
		return nil, err
	}
	if response.ContentLength >= 0 && response.ContentLength != artifact.Size {
		_ = response.Body.Close()
		return nil, fmt.Errorf("GitHub artifact Content-Length = %d, want %d", response.ContentLength, artifact.Size)
	}
	return response.Body, nil
}

func (s *GitHubSource) fetch(ctx context.Context, asset string, maximum int64) ([]byte, error) {
	response, err := s.do(ctx, asset)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	return readBounded(response.Body, maximum)
}

func (s *GitHubSource) do(ctx context.Context, asset string) (*http.Response, error) {
	if path.Base(asset) != asset || strings.Contains(asset, "..") {
		return nil, errors.New("unsafe GitHub release asset name")
	}
	endpoint := &url.URL{
		Scheme: "https", Host: "github.com",
		Path: path.Join("/", s.config.Repository, "releases", "download", s.version, asset),
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("Accept", "application/octet-stream")
	request.Header.Set("Accept-Encoding", "identity")
	request.Header.Set("User-Agent", "p2pstream-host-updater/1")
	response, err := s.client.Do(request)
	if err != nil {
		return nil, err
	}
	if response.StatusCode != http.StatusOK {
		_ = response.Body.Close()
		return nil, fmt.Errorf("GitHub release asset HTTP status %d", response.StatusCode)
	}
	return response, nil
}
