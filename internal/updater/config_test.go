package updater

import (
	"crypto/sha256"
	"strings"
	"testing"
)

func TestHostConfigPinsExactGitHubRawArtifactURL(t *testing.T) {
	body := []byte("binary")
	release := VerifiedRelease{
		Version: "v1.2.3", Commit: strings.Repeat("a", 40),
		ManifestSHA256: strings.Repeat("b", 64), Sequence: 2, SecurityEpoch: 1,
		MinimumSafeVersion: "v1.0.0",
		Artifact:           Artifact{Name: runtimeArtifactName("v1.2.3"), Size: int64(len(body)), SHA256: sha256.Sum256(body)},
	}
	config := HostConfig{Repository: "Kirari04/p2pstream", ManagementOrigin: "https://management.example.test:8081", AgentPublicID: "agent-a", Channel: "stable"}
	got, err := config.ArtifactURL(release)
	if err != nil {
		t.Fatal(err)
	}
	want := "https://github.com/Kirari04/p2pstream/releases/download/v1.2.3/" + release.Artifact.Name
	if got != want {
		t.Fatalf("artifact URL = %q, want %q", got, want)
	}
}

func TestHostConfigRejectsArbitraryOriginsAndPaths(t *testing.T) {
	tests := []HostConfig{
		{Repository: "owner/repo/extra", ManagementOrigin: "https://management.example", AgentPublicID: "agent-a", Channel: "stable"},
		{Repository: "owner/repo", ManagementOrigin: "http://management.example", AgentPublicID: "agent-a", Channel: "stable"},
		{Repository: "owner/repo", ManagementOrigin: "https://management.example/path", AgentPublicID: "agent-a", Channel: "stable"},
		{Repository: "owner/repo", ManagementOrigin: "https://user@management.example", AgentPublicID: "agent-a", Channel: "stable"},
		{Repository: "owner/repo", ManagementOrigin: "https://management.example", AgentPublicID: "agent-a", Channel: "nightly"},
		{Repository: "owner/repo", ManagementOrigin: "https://management.example", AgentPublicID: "../agent", Channel: "stable"},
	}
	for i, config := range tests {
		if err := config.Validate(); err == nil {
			t.Fatalf("config %d accepted: %+v", i, config)
		}
	}
}

func TestHostConfigAcceptsIsolatedReleaseChannels(t *testing.T) {
	for _, channel := range []string{"stable", "staging"} {
		config := HostConfig{Repository: "owner/repo", ManagementOrigin: "https://management.example", AgentPublicID: "agent-a", Channel: channel}
		if err := config.Validate(); err != nil {
			t.Fatalf("channel %q rejected: %v", channel, err)
		}
	}
}

func TestManagementOriginRejectsAnythingButExactHTTPSOrigin(t *testing.T) {
	for _, raw := range []string{
		"http://management.example", "https://user@management.example", "https://management.example/path",
		"https://management.example?token=x", "https://management.example#fragment",
	} {
		if _, err := ManagementOrigin(raw); err == nil {
			t.Fatalf("management URL %q was accepted", raw)
		}
	}
	if got, err := ManagementOrigin("https://management.example:8443"); err != nil || got != "https://management.example:8443" {
		t.Fatalf("origin = %q, %v", got, err)
	}
}
