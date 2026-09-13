package serverupdate

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"p2pstream/internal/agentupdate"
)

func releaseFixture(t *testing.T, version, channel string) ([]byte, []byte, RuntimeStatus) {
	t.Helper()
	meta := NewMetadata(version, strings.Repeat("a", 40))
	metadata, _ := json.Marshal(meta)
	m := agentupdate.Manifest{SchemaVersion: 1, Version: version, Channel: channel, Commit: meta.Commit, Sequence: 12, SecurityEpoch: 1, MinimumSafeVersion: "v1.0.0", PublishedAt: time.Now().UTC().Add(-time.Hour).Format(time.RFC3339), ExpiresAt: time.Now().UTC().Add(time.Hour).Format(time.RFC3339),
		Compatibility: agentupdate.Compatibility{Server: agentupdate.VersionRange{Min: "v1.0.0", Max: "v1.9.9"}, Updater: agentupdate.VersionRange{Min: "v1.0.0", Max: "v1.9.9"}, Protocol: agentupdate.ProtocolRange{Min: 1, Max: 1}},
		Artifacts:     []agentupdate.Artifact{{OS: "linux", Arch: "amd64", Name: "binary", Size: 1, SHA256: strings.Repeat("a", 64)}},
		OCIImages:     []agentupdate.OCIImage{{Repository: "ghcr.io/test/repo", Digest: "sha256:" + strings.Repeat("a", 64), Size: 123, MediaType: "application/vnd.oci.image.index.v1+json", Platforms: []agentupdate.OCIPlatform{{OS: "linux", Arch: "amd64", Digest: "sha256:" + strings.Repeat("b", 64), Size: 12, MediaType: "application/vnd.oci.image.manifest.v1+json"}}}},
		ReleaseAssets: []agentupdate.ReleaseAsset{{Name: MetadataAsset, Size: uint64(len(metadata)), SHA256: hash(metadata)}},
	}
	m.OCIImages[0].Platforms = append(m.OCIImages[0].Platforms, agentupdate.OCIPlatform{OS: "linux", Arch: "arm64", Digest: "sha256:" + strings.Repeat("c", 64), Size: 12, MediaType: "application/vnd.oci.image.manifest.v1+json"})
	data, err := agentupdate.CanonicalManifest(m)
	if err != nil {
		t.Fatal(err)
	}
	return data, metadata, RuntimeStatus{API: API, Schema: Schema, Version: "v1.0.0", Ready: true}
}

func TestServerMetadataBindsImageSchemaAndRecovery(t *testing.T) {
	manifest, metadata, current := releaseFixture(t, "v1.0.1", "stable")
	r, err := VerifyRelease(manifest, metadata, "ghcr.io/test/repo", "stable", "v1.0.1", "amd64", current, Floor{}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(r.Image, "@sha256:") {
		t.Fatal("mutable image selected")
	}
	for _, test := range []struct {
		name                string
		metadata            []byte
		current             RuntimeStatus
		repo, channel, arch string
		floor               Floor
	}{
		{"tampered metadata", append(metadata, ' '), current, "ghcr.io/test/repo", "stable", "amd64", Floor{}},
		{"unknown schema", metadata, RuntimeStatus{API: API, Schema: 18, Version: current.Version}, "ghcr.io/test/repo", "stable", "amd64", Floor{}},
		{"old runtime", metadata, RuntimeStatus{API: 0, Schema: 17, Version: current.Version}, "ghcr.io/test/repo", "stable", "amd64", Floor{}},
		{"wrong repository", metadata, current, "ghcr.io/attacker/repo", "stable", "amd64", Floor{}},
		{"wrong channel", metadata, current, "ghcr.io/test/repo", "staging", "amd64", Floor{}},
		{"wrong platform", metadata, current, "ghcr.io/test/repo", "stable", "s390x", Floor{}},
		{"rollback floor", metadata, current, "ghcr.io/test/repo", "stable", "amd64", Floor{Sequence: 13}},
		{"changed sequence", metadata, current, "ghcr.io/test/repo", "stable", "amd64", Floor{Sequence: 12, ManifestSHA256: strings.Repeat("f", 64)}},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := VerifyRelease(manifest, test.metadata, test.repo, test.channel, "v1.0.1", test.arch, test.current, test.floor, time.Now()); err == nil {
				t.Fatal("accepted invalid release")
			}
		})
	}
}

func TestStagingCatalogDiscoversNextPublishedRelease(t *testing.T) {
	manifest, metadata, current := releaseFixture(t, "v1.0.1-staging.10", "staging")
	current.Version = "v1.0.1-staging.8"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/test/repo/releases":
			fmt.Fprint(w, `[{"tag_name":"v1.0.1-staging.9","prerelease":true},{"tag_name":"v1.0.1-staging.10","prerelease":true},{"tag_name":"v1.0.1-staging.11","prerelease":true,"draft":true},{"tag_name":"v9.0.0","prerelease":false}]`)
		case "/repos/test/repo/releases/tags/v1.0.1-staging.10":
			fmt.Fprint(w, `{"tag_name":"v1.0.1-staging.10","prerelease":true}`)
		case "/test/repo/releases/download/v1.0.1-staging.10/p2pstream_agent_update_manifest.json":
			w.Write(manifest)
		case "/test/repo/releases/download/v1.0.1-staging.10/" + MetadataAsset:
			w.Write(metadata)
		default:
			t.Errorf("unexpected source path %s", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	s, _ := NewGitHubSource("test/repo", "staging", "amd64")
	s.client = srv.Client()
	s.api = srv.URL
	s.download = srv.URL
	r, err := s.Latest(context.Background(), current, Floor{})
	if err != nil {
		t.Fatal(err)
	}
	if r == nil || r.Version != "v1.0.1-staging.10" {
		t.Fatalf("target = %+v", r)
	}
}

func TestReleaseSourceRejectsArbitraryOriginsAndRedirects(t *testing.T) {
	for _, repo := range []string{"../attacker", "test/repo?x=1", "https://github.com/test/repo", "test/repo/other"} {
		if _, err := NewGitHubSource(repo, "stable", "amd64"); err == nil {
			t.Fatalf("accepted %s", repo)
		}
	}
	s, _ := NewGitHubSource("test/repo", "stable", "amd64")
	for _, address := range []string{"http://github.com/asset", "https://attacker.test/asset", "https://github.com:8443/asset"} {
		r, _ := http.NewRequest(http.MethodGet, address, nil)
		if err := s.client.CheckRedirect(r, nil); err == nil {
			t.Fatalf("accepted redirect %s", address)
		}
	}
}

func TestEnrollmentSeedsOnlyTheExactInstalledRelease(t *testing.T) {
	manifest, metadata, current := releaseFixture(t, "v1.0.0", "stable")
	current.Commit = strings.Repeat("a", 40)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.Contains(r.URL.Path, "/releases/tags/"):
			fmt.Fprint(w, `{"tag_name":"v1.0.0","prerelease":false}`)
		case strings.HasSuffix(r.URL.Path, "p2pstream_agent_update_manifest.json"):
			w.Write(manifest)
		case strings.HasSuffix(r.URL.Path, MetadataAsset):
			w.Write(metadata)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	s, _ := NewGitHubSource("test/repo", "stable", "amd64")
	s.client = srv.Client()
	s.api = srv.URL
	s.download = srv.URL
	image := "ghcr.io/test/repo@sha256:" + strings.Repeat("a", 64)
	installed, err := s.Installed(context.Background(), image, current)
	if err != nil {
		t.Fatal(err)
	}
	if installed.VerificationFloor().Sequence != 12 {
		t.Fatal("installed security floor not recorded")
	}
	if _, err := s.Resolve(context.Background(), current.Version, current, Floor{}); err == nil {
		t.Fatal("ordinary update accepted the installed version")
	}
	if _, err := s.Installed(context.Background(), image+"0", current); err == nil {
		t.Fatal("accepted different installed image")
	}
	current.Commit = strings.Repeat("b", 40)
	if _, err := s.Installed(context.Background(), image, current); err == nil {
		t.Fatal("accepted different installed commit")
	}
}
