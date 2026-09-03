package cmd

import (
	"crypto/ed25519"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"p2pstream/internal/agentupdate"
	"p2pstream/internal/config"
)

func TestNewAgentUpdateCatalogFailsClosedOnMissingOrMalformedRoot(t *testing.T) {
	cfg := &config.Config{
		AgentUpdatesEnabled: true, AgentUpdateRepository: "owner/repo",
		AgentUpdateRootFile:             filepath.Join(t.TempDir(), "missing.json"),
		AgentUpdateCatalogStateFile:     filepath.Join(t.TempDir(), "state.json"),
		AgentUpdateCatalogRefreshMillis: 10_000, AgentUpdateHTTPTimeoutMillis: 1_000,
	}
	if _, err := newAgentUpdateCatalog(cfg); err == nil || !strings.Contains(err.Error(), "pinned") {
		t.Fatalf("missing root error = %v", err)
	}
	if err := os.WriteFile(cfg.AgentUpdateRootFile, []byte(`{"untrusted":true}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := newAgentUpdateCatalog(cfg); err == nil || !strings.Contains(err.Error(), "parse pinned") {
		t.Fatalf("malformed root error = %v", err)
	}
}

func TestNewAgentUpdateCatalogAcceptsCanonicalPinnedRoot(t *testing.T) {
	key := ed25519.NewKeyFromSeed(make([]byte, ed25519.SeedSize))
	root, err := agentupdate.NewRootMetadata(1, time.Now().Add(24*time.Hour).UTC().Format(time.RFC3339), 1, []ed25519.PublicKey{key.Public().(ed25519.PublicKey)})
	if err != nil {
		t.Fatal(err)
	}
	rootJSON, err := agentupdate.CanonicalRoot(root)
	if err != nil {
		t.Fatal(err)
	}
	directory := t.TempDir()
	rootPath := filepath.Join(directory, "root.json")
	if err := os.WriteFile(rootPath, rootJSON, 0o600); err != nil {
		t.Fatal(err)
	}
	catalog, err := newAgentUpdateCatalog(&config.Config{
		AgentUpdatesEnabled: true, AgentUpdateRepository: "owner/repo", AgentUpdateRootFile: rootPath,
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
