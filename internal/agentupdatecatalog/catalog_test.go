package agentupdatecatalog

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"p2pstream/internal/agentupdate"
)

func TestCatalogAuthenticatesPersistsAndUsesBoundedStaleFallback(t *testing.T) {
	now := time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC)
	bundle := newCatalogBundle(t, now, "v1.2.3", 10203)
	server := newCatalogServer(t, bundle)
	defer server.Close()

	catalog, err := New(Options{
		Repository:      "owner/repo",
		StatePath:       filepath.Join(t.TempDir(), "catalog-state.json"),
		RefreshInterval: 10 * time.Second,
		HTTPClient:      server.Client(),
		ServerVersion:   "v1.2.0",
		ProtocolVersion: 1,
		Now:             func() time.Time { return now },
		APIBaseURL:      server.URL,
		DownloadBaseURL: server.URL,
	})
	if err != nil {
		t.Fatal(err)
	}
	verified, err := catalog.Latest(context.Background())
	if err != nil {
		t.Fatalf("Latest: %v", err)
	}
	if verified.Manifest.Version != "v1.2.3" || len(verified.Manifest.Artifacts) != 2 {
		t.Fatalf("verified release = %+v", verified)
	}
	if floor, err := readFloor(catalog.options.StatePath); err != nil || floor.Sequence != 10203 || floor.ManifestSHA256 != verified.ManifestSHA256 {
		t.Fatalf("persisted floor = %+v, err=%v", floor, err)
	}

	server.fail(true)
	now = now.Add(11 * time.Second)
	stale, err := catalog.Latest(context.Background())
	if err != nil || stale.ManifestSHA256 != verified.ManifestSHA256 {
		t.Fatalf("stale Latest = %+v, %v", stale, err)
	}
	if snapshot := catalog.Snapshot(); !snapshot.UsingStale || snapshot.LastError == "" {
		t.Fatalf("stale snapshot = %+v", snapshot)
	}

	now = bundle.expiresAt
	if _, err := catalog.Latest(context.Background()); err == nil {
		t.Fatal("Latest accepted stale metadata at its expiry")
	}
}

func TestStagingCatalogPinsTheRunningImmutablePrerelease(t *testing.T) {
	now := time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC)
	bundle := newCatalogBundle(t, now, "v1.3.0-staging.17", 10300017)
	server := newCatalogServer(t, bundle)
	defer server.Close()

	catalog, err := New(Options{
		Repository: "owner/repo", Channel: "staging",
		StatePath: filepath.Join(t.TempDir(), "catalog-state.json"), RefreshInterval: 10 * time.Second,
		HTTPClient: server.Client(), ServerVersion: bundle.tag, ProtocolVersion: 1,
		Now: func() time.Time { return now }, APIBaseURL: server.URL, DownloadBaseURL: server.URL,
	})
	if err != nil {
		t.Fatal(err)
	}
	verified, err := catalog.Latest(context.Background())
	if err != nil {
		t.Fatalf("Latest staging: %v", err)
	}
	if verified.Manifest.Channel != "staging" || verified.Manifest.Version != bundle.tag {
		t.Fatalf("verified staging release = %+v", verified.Manifest)
	}
	floor, err := readFloor(catalog.options.StatePath)
	if err != nil || floor.Channel != "staging" {
		t.Fatalf("persisted staging floor = %+v, err=%v", floor, err)
	}
	if _, err := New(Options{
		Repository: "owner/repo", Channel: "stable", StatePath: catalog.options.StatePath,
		RefreshInterval: 10 * time.Second, HTTPClient: server.Client(),
	}); err == nil || !strings.Contains(err.Error(), "different release channel") {
		t.Fatalf("cross-channel state error = %v", err)
	}
}

func TestCatalogRejectsRollbackAcrossRestartAndInvalidLatestTag(t *testing.T) {
	now := time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC)
	statePath := filepath.Join(t.TempDir(), "catalog-state.json")
	current := newCatalogBundle(t, now, "v1.2.3", 10203)
	server := newCatalogServer(t, current)
	defer server.Close()
	options := Options{
		Repository: "owner/repo", StatePath: statePath,
		RefreshInterval: 10 * time.Second, HTTPClient: server.Client(),
		ServerVersion: "v1.2.0", ProtocolVersion: 1, Now: func() time.Time { return now },
		APIBaseURL: server.URL, DownloadBaseURL: server.URL,
	}
	catalog, err := New(options)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := catalog.Latest(context.Background()); err != nil {
		t.Fatal(err)
	}

	rollback := newCatalogBundle(t, now, "v1.2.2", 10202)
	server.setBundle(rollback)
	restarted, err := New(options)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := restarted.Latest(context.Background()); err == nil || !strings.Contains(err.Error(), "sequence") {
		t.Fatalf("rollback error = %v", err)
	}

	server.setTag("latest;curl attacker.invalid")
	if _, err := restarted.Latest(context.Background()); err == nil || !strings.Contains(err.Error(), "invalid tag") {
		t.Fatalf("invalid tag error = %v", err)
	}
}

func TestCatalogRejectsOversizedMetadataAndNonLoopbackOverride(t *testing.T) {
	now := time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC)
	bundle := newCatalogBundle(t, now, "v1.2.3", 10203)
	server := newCatalogServer(t, bundle)
	defer server.Close()
	server.setManifest(bytes.Repeat([]byte("x"), agentupdate.MaxManifestBytes+1))
	catalog, err := New(Options{
		Repository: "owner/repo", StatePath: filepath.Join(t.TempDir(), "state"),
		RefreshInterval: 10 * time.Second, HTTPClient: server.Client(), Now: func() time.Time { return now },
		APIBaseURL: server.URL, DownloadBaseURL: server.URL,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := catalog.Latest(context.Background()); err == nil || !strings.Contains(err.Error(), "limit") {
		t.Fatalf("oversized manifest error = %v", err)
	}

	_, err = New(Options{
		Repository: "owner/repo", StatePath: filepath.Join(t.TempDir(), "state"),
		RefreshInterval: 10 * time.Second, HTTPClient: server.Client(),
		APIBaseURL: "https://attacker.invalid", DownloadBaseURL: "https://github.com",
	})
	if err == nil || !strings.Contains(err.Error(), "loopback") {
		t.Fatalf("custom origin error = %v", err)
	}
	_, err = New(Options{
		Repository: "owner/repo", StatePath: filepath.Join(t.TempDir(), "state"),
		RefreshInterval: 10 * time.Second, HTTPClient: server.Client(),
		APIBaseURL: "http://127.0.0.1:123@attacker.invalid", DownloadBaseURL: server.URL,
	})
	if err == nil || !strings.Contains(err.Error(), "loopback") {
		t.Fatalf("userinfo origin smuggling error = %v", err)
	}
}

func TestServerAdapterResolvesOnlyExactAuthenticatedDigest(t *testing.T) {
	now := time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC)
	bundle := newCatalogBundle(t, now, "v1.2.3", 10203)
	server := newCatalogServer(t, bundle)
	defer server.Close()
	catalog, err := New(Options{
		Repository: "owner/repo", StatePath: filepath.Join(t.TempDir(), "state"),
		RefreshInterval: 10 * time.Second, HTTPClient: server.Client(), ServerVersion: "v1.2.0", ProtocolVersion: 1,
		Now: func() time.Time { return now }, APIBaseURL: server.URL, DownloadBaseURL: server.URL,
	})
	if err != nil {
		t.Fatal(err)
	}
	targets, err := catalog.ListTrustedAgentUpdateTargets(context.Background())
	if err != nil || len(targets) != 1 {
		t.Fatalf("targets = %+v, err=%v", targets, err)
	}
	target := targets[0]
	if target.Version != "v1.2.3" || len(target.Artifacts) != 2 || target.Artifacts[0].Name == "" {
		t.Fatalf("target = %+v", target)
	}
	resolved, err := catalog.ResolveTrustedAgentUpdateTarget(context.Background(), target.ManifestSha256)
	if err != nil || resolved.ManifestSha256 != target.ManifestSha256 {
		t.Fatalf("resolved = %+v, err=%v", resolved, err)
	}
	resolved.Version = "tampered"
	resolvedAgain, err := catalog.ResolveTrustedAgentUpdateTarget(context.Background(), target.ManifestSha256)
	if err != nil || resolvedAgain.Version != "v1.2.3" {
		t.Fatalf("catalog leaked mutable target: %+v, %v", resolvedAgain, err)
	}
	if _, err := catalog.ResolveTrustedAgentUpdateTarget(context.Background(), strings.Repeat("0", 64)); err == nil {
		t.Fatal("untrusted digest resolved")
	}
	repository, err := catalog.AgentUpdateBootstrapConfig(context.Background())
	if err != nil || repository != "owner/repo" {
		t.Fatalf("bootstrap config = %q, err=%v", repository, err)
	}
}

type catalogBundle struct {
	tag       string
	manifest  []byte
	expiresAt time.Time
}

func newCatalogBundle(t *testing.T, now time.Time, version string, sequence uint64) catalogBundle {
	t.Helper()
	expiresAt := now.Add(time.Hour)
	artifacts := make([]agentupdate.Artifact, 0, 2)
	for _, arch := range []string{"amd64", "arm64"} {
		payload := []byte("binary-" + arch)
		digest := sha256.Sum256(payload)
		artifacts = append(artifacts, agentupdate.Artifact{
			OS: "linux", Arch: arch, Name: fmt.Sprintf("p2pstream_%s_linux_%s", version, arch),
			Size: uint64(len(payload)), SHA256: hex.EncodeToString(digest[:]),
		})
	}
	channel := "stable"
	if strings.Contains(version, "-") {
		channel = "staging"
	}
	manifest, err := agentupdate.CanonicalManifest(agentupdate.Manifest{
		SchemaVersion: agentupdate.SchemaVersion, Channel: channel,
		Version: version, Commit: strings.Repeat("a", 40), Sequence: sequence,
		PublishedAt: now.Add(-time.Minute).Format(time.RFC3339), ExpiresAt: expiresAt.Format(time.RFC3339),
		MinimumSafeVersion: "v1.0.0", SecurityEpoch: 1,
		Compatibility: agentupdate.Compatibility{
			Server:   agentupdate.VersionRange{Min: "v1.0.0", Max: "v2.0.0"},
			Updater:  agentupdate.VersionRange{Min: "v1.0.0", Max: "v2.0.0"},
			Protocol: agentupdate.ProtocolRange{Min: 1, Max: 2},
		},
		Artifacts: artifacts,
	})
	if err != nil {
		t.Fatal(err)
	}
	return catalogBundle{tag: version, manifest: manifest, expiresAt: expiresAt}
}

type catalogServer struct {
	*httptest.Server
	mu       sync.Mutex
	bundle   catalogBundle
	tag      string
	manifest []byte
	failing  bool
}

func newCatalogServer(t *testing.T, bundle catalogBundle) *catalogServer {
	t.Helper()
	server := &catalogServer{bundle: bundle, tag: bundle.tag, manifest: bundle.manifest}
	server.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		server.mu.Lock()
		defer server.mu.Unlock()
		if server.failing {
			http.Error(w, "temporary failure", http.StatusServiceUnavailable)
			return
		}
		switch {
		case strings.HasSuffix(r.URL.Path, "/releases/latest"):
			fmt.Fprintf(w, `{"tag_name":%q}`, server.tag)
		case strings.Contains(r.URL.Path, "/releases/tags/"):
			fmt.Fprintf(w, `{"tag_name":%q,"draft":false,"prerelease":true}`, server.tag)
		case strings.HasSuffix(r.URL.Path, "/p2pstream_agent_update_manifest.json"):
			w.Write(server.manifest)
		default:
			http.NotFound(w, r)
		}
	}))
	return server
}

func (s *catalogServer) fail(value bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.failing = value
}

func (s *catalogServer) setBundle(bundle catalogBundle) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.bundle = bundle
	s.tag = bundle.tag
	s.manifest = bundle.manifest
}

func (s *catalogServer) setTag(tag string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.tag = tag
}

func (s *catalogServer) setManifest(manifest []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.manifest = manifest
}
