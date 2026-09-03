package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"p2pstream/internal/agentupdate"
)

func TestCreateAndVerifyRoundTrip(t *testing.T) {
	directory := t.TempDir()
	artifactPath := filepath.Join(directory, "p2pstream_v1.2.3_linux_amd64")
	archivePath := filepath.Join(directory, "p2pstream_v1.2.3_source.tar.gz")
	ociIndexPath := filepath.Join(directory, "image-index.json")
	manifestPath := filepath.Join(directory, "manifest.json")
	if err := os.WriteFile(artifactPath, []byte("raw executable"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(archivePath, []byte("source archive"), 0o644); err != nil {
		t.Fatal(err)
	}
	ociIndex := `{"schemaVersion":2,"mediaType":"application/vnd.oci.image.index.v1+json","manifests":[{"mediaType":"application/vnd.oci.image.manifest.v1+json","digest":"sha256:` + strings.Repeat("1", 64) + `","size":1234,"platform":{"architecture":"amd64","os":"linux"}},{"mediaType":"application/vnd.oci.image.manifest.v1+json","digest":"sha256:` + strings.Repeat("2", 64) + `","size":2345,"platform":{"architecture":"arm64","os":"linux"}}]}`
	if err := os.WriteFile(ociIndexPath, []byte(ociIndex), 0o644); err != nil {
		t.Fatal(err)
	}
	createArguments := []string{
		"create", "--version", "v1.2.3", "--commit", strings.Repeat("a", 40), "--sequence", "10203",
		"--published-at", "2026-01-01T00:00:00Z", "--expires-at", "2026-02-01T00:00:00Z",
		"--minimum-safe-version", "v1.0.0", "--security-epoch", "2",
		"--server-min", "v1.0.0", "--server-max", "v2.0.0", "--protocol-min", "1", "--protocol-max", "2",
		"--updater-min", "v1.0.0", "--updater-max", "v2.0.0", "--artifact", "linux/amd64=" + artifactPath,
		"--oci-index", "ghcr.io/example/p2pstream=" + ociIndexPath, "--release-asset", archivePath, "--output", manifestPath,
	}
	if err := run(createArguments); err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := run(createArguments); err == nil {
		t.Fatal("create clobbered existing manifest")
	}

	manifestJSON, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	verified, err := agentupdate.Verify(manifestJSON, agentupdate.VerifyPolicy{
		Now: time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC), CurrentSequence: 10202,
		CurrentSecurityEpoch: 1, CurrentMinimumSafeVersion: "v1.0.0", CurrentVersion: "v1.2.2",
		ServerVersion: "v1.5.0", UpdaterVersion: "v1.5.0", ProtocolVersion: 1, GOOS: "linux", GOARCH: "amd64",
	})
	if err != nil {
		t.Fatalf("verify generated metadata: %v", err)
	}
	artifact, err := os.Open(artifactPath)
	if err != nil {
		t.Fatal(err)
	}
	defer artifact.Close()
	if err := agentupdate.VerifyArtifact(artifact, verified.Artifact); err != nil {
		t.Fatalf("verify generated artifact: %v", err)
	}

	verifyArguments := []string{
		"verify-release", "--manifest", manifestPath, "--expected-version", "v1.2.3",
		"--expected-commit", strings.Repeat("a", 40), "--expected-sequence", "10203",
		"--server-version", "v1.5.0", "--protocol-version", "1", "--now", "2026-01-02T00:00:00Z",
		"--artifact", "linux/amd64=" + artifactPath, "--oci-index", "ghcr.io/example/p2pstream=" + ociIndexPath,
		"--release-asset", archivePath,
	}
	if err := run(verifyArguments); err != nil {
		t.Fatalf("verify-release: %v", err)
	}
	wrongIdentity := append([]string(nil), verifyArguments...)
	for i, argument := range wrongIdentity {
		if argument == "v1.2.3" {
			wrongIdentity[i] = "v1.2.4"
			break
		}
	}
	if err := run(wrongIdentity); err == nil || !strings.Contains(err.Error(), "release identity") {
		t.Fatalf("verify-release identity error = %v", err)
	}

	tamperedIndex := filepath.Join(directory, "image-index-tampered.json")
	if err := os.WriteFile(tamperedIndex, []byte(strings.Replace(ociIndex, strings.Repeat("1", 64), strings.Repeat("3", 64), 1)), 0o644); err != nil {
		t.Fatal(err)
	}
	tamperedArguments := append([]string(nil), verifyArguments...)
	for i, argument := range tamperedArguments {
		if strings.HasPrefix(argument, "ghcr.io/example/p2pstream=") {
			tamperedArguments[i] = "ghcr.io/example/p2pstream=" + tamperedIndex
		}
	}
	if err := run(tamperedArguments); err == nil || !strings.Contains(err.Error(), "does not match release metadata") {
		t.Fatalf("verify-release OCI tamper error = %v", err)
	}
}

func TestSigningSubcommandsAreRemoved(t *testing.T) {
	for _, command := range []string{"sign", "sign-partial", "merge"} {
		if err := run([]string{command}); err == nil || !strings.Contains(err.Error(), "unknown subcommand") {
			t.Fatalf("%s error = %v", command, err)
		}
	}
}
