package main

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"p2pstream/internal/agentupdate"
)

func TestCreateAndSignRoundTrip(t *testing.T) {
	directory := t.TempDir()
	artifactPath := filepath.Join(directory, "p2pstream_v1.2.3_linux_amd64")
	archivePath := filepath.Join(directory, "p2pstream_v1.2.3_source.tar.gz")
	ociIndexPath := filepath.Join(directory, "image-index.json")
	manifestPath := filepath.Join(directory, "manifest.json")
	rootPath := filepath.Join(directory, "root.json")
	signaturesPath := filepath.Join(directory, "signatures.json")
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
	seed1 := bytes.Repeat([]byte{1}, ed25519.SeedSize)
	seed2 := bytes.Repeat([]byte{2}, ed25519.SeedSize)
	key1 := ed25519.NewKeyFromSeed(seed1)
	key2 := ed25519.NewKeyFromSeed(seed2)
	root, err := agentupdate.NewRootMetadata(1, "2027-01-01T00:00:00Z", 2, []ed25519.PublicKey{
		key1.Public().(ed25519.PublicKey),
		key2.Public().(ed25519.PublicKey),
	})
	if err != nil {
		t.Fatal(err)
	}
	rootJSON, err := agentupdate.CanonicalRoot(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(rootPath, rootJSON, 0o644); err != nil {
		t.Fatal(err)
	}
	createArguments := []string{
		"create",
		"--root", rootPath,
		"--version", "v1.2.3",
		"--commit", strings.Repeat("a", 40),
		"--sequence", "10203",
		"--published-at", "2026-01-01T00:00:00Z",
		"--expires-at", "2026-02-01T00:00:00Z",
		"--minimum-safe-version", "v1.0.0",
		"--security-epoch", "2",
		"--server-min", "v1.0.0",
		"--server-max", "v2.0.0",
		"--protocol-min", "1",
		"--protocol-max", "2",
		"--updater-min", "v1.0.0",
		"--updater-max", "v2.0.0",
		"--artifact", "linux/amd64=" + artifactPath,
		"--oci-index", "ghcr.io/example/p2pstream=" + ociIndexPath,
		"--release-asset", archivePath,
		"--output", manifestPath,
	}
	if err := run(createArguments); err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := run(createArguments); err == nil {
		t.Fatal("create clobbered existing manifest")
	}

	t.Setenv("TEST_SIGNING_KEYS", base64.StdEncoding.EncodeToString(seed1)+"\n"+base64.StdEncoding.EncodeToString(seed2))
	if err := run([]string{
		"sign",
		"--manifest", manifestPath,
		"--root", rootPath,
		"--signatures-output", signaturesPath,
		"--keys-env", "TEST_SIGNING_KEYS",
	}); err != nil {
		t.Fatalf("sign: %v", err)
	}

	manifestJSON, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	signaturesJSON, err := os.ReadFile(signaturesPath)
	if err != nil {
		t.Fatal(err)
	}
	root, err = agentupdate.ParseRoot(rootJSON)
	if err != nil {
		t.Fatal(err)
	}
	verified, err := agentupdate.Verify(manifestJSON, signaturesJSON, root, agentupdate.VerifyPolicy{
		Now:                       time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC),
		CurrentSequence:           10202,
		CurrentSecurityEpoch:      1,
		CurrentMinimumSafeVersion: "v1.0.0",
		MinimumRootVersion:        1,
		CurrentVersion:            "v1.2.2",
		ServerVersion:             "v1.5.0",
		UpdaterVersion:            "v1.5.0",
		ProtocolVersion:           1,
		GOOS:                      "linux",
		GOARCH:                    "amd64",
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

	partialOne := filepath.Join(directory, "signature-one.json")
	partialTwo := filepath.Join(directory, "signature-two.json")
	mergedPath := filepath.Join(directory, "signatures-merged.json")
	for _, signer := range []struct {
		path string
		seed []byte
	}{{partialOne, seed1}, {partialTwo, seed2}} {
		t.Setenv("TEST_ONE_SIGNING_KEY", base64.StdEncoding.EncodeToString(signer.seed))
		if err := run([]string{
			"sign-partial",
			"--manifest", manifestPath,
			"--root", rootPath,
			"--signatures-output", signer.path,
			"--keys-env", "TEST_ONE_SIGNING_KEY",
		}); err != nil {
			t.Fatalf("sign-partial: %v", err)
		}
	}
	if err := run([]string{
		"merge",
		"--manifest", manifestPath,
		"--root", rootPath,
		"--signature", partialTwo,
		"--signature", partialOne,
		"--output", mergedPath,
	}); err != nil {
		t.Fatalf("merge: %v", err)
	}
	mergedJSON, err := os.ReadFile(mergedPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := agentupdate.Verify(manifestJSON, mergedJSON, root, agentupdate.VerifyPolicy{
		Now:                       time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC),
		CurrentSequence:           10202,
		CurrentSecurityEpoch:      1,
		CurrentMinimumSafeVersion: "v1.0.0",
		MinimumRootVersion:        1,
		CurrentVersion:            "v1.2.2",
		ServerVersion:             "v1.5.0",
		UpdaterVersion:            "v1.5.0",
		ProtocolVersion:           1,
		GOOS:                      "linux",
		GOARCH:                    "amd64",
	}); err != nil {
		t.Fatalf("verify merged metadata: %v", err)
	}
	if err := run([]string{
		"verify-release",
		"--manifest", manifestPath,
		"--root", rootPath,
		"--signatures", mergedPath,
		"--expected-version", "v1.2.3",
		"--expected-commit", strings.Repeat("a", 40),
		"--expected-sequence", "10203",
		"--server-version", "v1.5.0",
		"--protocol-version", "1",
		"--now", "2026-01-02T00:00:00Z",
		"--artifact", "linux/amd64=" + artifactPath,
		"--oci-index", "ghcr.io/example/p2pstream=" + ociIndexPath,
		"--release-asset", archivePath,
	}); err != nil {
		t.Fatalf("verify-release: %v", err)
	}
	if err := run([]string{
		"verify-release",
		"--manifest", manifestPath,
		"--root", rootPath,
		"--signatures", mergedPath,
		"--expected-version", "v1.2.4",
		"--expected-commit", strings.Repeat("a", 40),
		"--expected-sequence", "10203",
		"--server-version", "v1.5.0",
		"--protocol-version", "1",
		"--now", "2026-01-02T00:00:00Z",
		"--artifact", "linux/amd64=" + artifactPath,
		"--oci-index", "ghcr.io/example/p2pstream=" + ociIndexPath,
		"--release-asset", archivePath,
	}); err == nil || !strings.Contains(err.Error(), "release identity") {
		t.Fatalf("verify-release identity error = %v", err)
	}

	tamperedIndex := filepath.Join(directory, "image-index-tampered.json")
	if err := os.WriteFile(tamperedIndex, []byte(strings.Replace(ociIndex, strings.Repeat("1", 64), strings.Repeat("3", 64), 1)), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := run([]string{
		"verify-release",
		"--manifest", manifestPath,
		"--root", rootPath,
		"--signatures", mergedPath,
		"--expected-version", "v1.2.3",
		"--expected-commit", strings.Repeat("a", 40),
		"--expected-sequence", "10203",
		"--server-version", "v1.5.0",
		"--protocol-version", "1",
		"--now", "2026-01-02T00:00:00Z",
		"--artifact", "linux/amd64=" + artifactPath,
		"--oci-index", "ghcr.io/example/p2pstream=" + tamperedIndex,
		"--release-asset", archivePath,
	}); err == nil || !strings.Contains(err.Error(), "does not match signed") {
		t.Fatalf("verify-release OCI tamper error = %v", err)
	}
	if err := os.WriteFile(archivePath, []byte("changed source archive"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := run([]string{
		"verify-release",
		"--manifest", manifestPath,
		"--root", rootPath,
		"--signatures", mergedPath,
		"--expected-version", "v1.2.3",
		"--expected-commit", strings.Repeat("a", 40),
		"--expected-sequence", "10203",
		"--server-version", "v1.5.0",
		"--protocol-version", "1",
		"--now", "2026-01-02T00:00:00Z",
		"--artifact", "linux/amd64=" + artifactPath,
		"--oci-index", "ghcr.io/example/p2pstream=" + ociIndexPath,
		"--release-asset", archivePath,
	}); err == nil || !strings.Contains(err.Error(), "release asset") {
		t.Fatalf("verify-release auxiliary asset tamper error = %v", err)
	}
}

func TestSignRejectsInconsistentPrivateKey(t *testing.T) {
	key := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{3}, ed25519.SeedSize))
	key[len(key)-1] ^= 1
	t.Setenv("TEST_BAD_SIGNING_KEY", base64.StdEncoding.EncodeToString(key))
	if _, err := privateKeysFromEnvironment("TEST_BAD_SIGNING_KEY"); err == nil || !strings.Contains(err.Error(), "inconsistent") {
		t.Fatalf("privateKeysFromEnvironment error = %v", err)
	}
}

func TestPrivateKeysFromFileRequiresOwnedNoFollowPrivateFile(t *testing.T) {
	directory := t.TempDir()
	keyPath := filepath.Join(directory, "offline-key")
	seed := bytes.Repeat([]byte{4}, ed25519.SeedSize)
	if err := os.WriteFile(keyPath, []byte(base64.StdEncoding.EncodeToString(seed)), 0o600); err != nil {
		t.Fatal(err)
	}
	keys, err := privateKeysFromFile(keyPath)
	if err != nil || len(keys) != 1 {
		t.Fatalf("privateKeysFromFile = %d, %v", len(keys), err)
	}
	if err := os.Chmod(keyPath, 0o640); err != nil {
		t.Fatal(err)
	}
	if _, err := privateKeysFromFile(keyPath); err == nil || !strings.Contains(err.Error(), "permissions") {
		t.Fatalf("permissive key error = %v", err)
	}
	if err := os.Chmod(keyPath, 0o600); err != nil {
		t.Fatal(err)
	}
	symlinkPath := filepath.Join(directory, "offline-key-link")
	if err := os.Symlink(keyPath, symlinkPath); err != nil {
		t.Fatal(err)
	}
	if _, err := privateKeysFromFile(symlinkPath); err == nil {
		t.Fatal("privateKeysFromFile followed a symlink")
	}
}
