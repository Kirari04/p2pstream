package cmd

import (
	"net/http"
	"net/url"
	"path/filepath"
	"testing"
	"time"

	"p2pstream/internal/config"
)

func TestNewAgentUpdateCatalogUsesConfiguredRepository(t *testing.T) {
	directory := t.TempDir()
	catalog, err := newAgentUpdateCatalog(&config.Config{
		AgentUpdatesEnabled: true, AgentUpdateRepository: "owner/repo",
		AgentUpdateCatalogStateFile:     filepath.Join(directory, "state.json"),
		AgentUpdateCatalogRefreshMillis: 10_000, AgentUpdateHTTPTimeoutMillis: 1_000,
	})
	if err != nil || catalog == nil {
		t.Fatalf("catalog = %v, err=%v", catalog, err)
	}
}

func TestAgentUpdateCatalogIgnoresEnvironmentProxy(t *testing.T) {
	t.Setenv("HTTPS_PROXY", "https://attacker.invalid:8443")
	client := newAgentUpdateHTTPClient(time.Second)
	transport, ok := client.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("transport type = %T", client.Transport)
	}
	if transport.Proxy != nil {
		t.Fatal("agent update catalog must not inherit environment proxy settings")
	}
}

func TestTrustedAgentUpdateRedirectBoundary(t *testing.T) {
	for _, raw := range []string{
		"https://github.com/owner/repo",
		"https://api.github.com/repos/owner/repo",
		"https://release-assets.githubusercontent.com/asset",
	} {
		parsed, _ := url.Parse(raw)
		if err := trustedAgentUpdateRedirect(&http.Request{URL: parsed}, nil); err != nil {
			t.Fatalf("trusted redirect %q: %v", raw, err)
		}
	}
	for _, raw := range []string{"http://github.com/owner/repo", "https://attacker.invalid/payload"} {
		parsed, _ := url.Parse(raw)
		if err := trustedAgentUpdateRedirect(&http.Request{URL: parsed}, nil); err == nil {
			t.Fatalf("untrusted redirect %q accepted", raw)
		}
	}
}
