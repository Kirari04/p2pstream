package agentupdate

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"
	"time"
)

func TestVerifyThresholdAndArtifact(t *testing.T) {
	manifestJSON, signaturesJSON, root, artifactBytes := testBundle(t, "stable", 2)
	verified, err := Verify(manifestJSON, signaturesJSON, root, testPolicy())
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if verified.Version != "v1.2.3" || verified.Sequence != 10203 || verified.SecurityEpoch != 2 {
		t.Fatalf("unexpected verified release: %+v", verified)
	}
	manifestDigest := sha256.Sum256(manifestJSON)
	if verified.ManifestSHA256 != hex.EncodeToString(manifestDigest[:]) {
		t.Fatalf("manifest digest = %q", verified.ManifestSHA256)
	}
	if verified.Artifact.Name != "p2pstream_v1.2.3_linux_amd64" {
		t.Fatalf("artifact = %+v", verified.Artifact)
	}
	if err := VerifyArtifact(bytes.NewReader(artifactBytes), verified.Artifact); err != nil {
		t.Fatalf("VerifyArtifact: %v", err)
	}
}

func TestVerifyCatalogPreservesAllArtifactsAndDigest(t *testing.T) {
	manifestJSON, _, root, artifactBytes := testBundle(t, "stable", 2)
	manifest, err := ParseManifest(manifestJSON)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(artifactBytes)
	manifest.Artifacts = append(manifest.Artifacts, Artifact{
		OS: "linux", Arch: "arm64", Name: "p2pstream_v1.2.3_linux_arm64",
		Size: uint64(len(artifactBytes)), SHA256: hex.EncodeToString(digest[:]),
	})
	manifestJSON, err = CanonicalManifest(manifest)
	if err != nil {
		t.Fatal(err)
	}
	keys := []ed25519.PrivateKey{deterministicPrivateKey(1), deterministicPrivateKey(2)}
	envelope, err := SignManifest(manifestJSON, root, keys)
	if err != nil {
		t.Fatal(err)
	}
	signaturesJSON, err := CanonicalSignatures(envelope)
	if err != nil {
		t.Fatal(err)
	}
	verified, err := VerifyCatalog(manifestJSON, signaturesJSON, root, CatalogVerifyPolicy{
		Now:                       time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC),
		MinimumRootVersion:        1,
		CurrentSequence:           10203,
		CurrentSecurityEpoch:      2,
		CurrentMinimumSafeVersion: "v1.0.0",
		ServerVersion:             "v1.5.0",
		ProtocolVersion:           1,
	})
	if err != nil {
		t.Fatalf("VerifyCatalog: %v", err)
	}
	if len(verified.Manifest.Artifacts) != 2 {
		t.Fatalf("catalog artifacts = %d", len(verified.Manifest.Artifacts))
	}
	wantDigest := sha256.Sum256(manifestJSON)
	if verified.ManifestSHA256 != hex.EncodeToString(wantDigest[:]) {
		t.Fatalf("catalog digest = %q", verified.ManifestSHA256)
	}
}

func TestVerifyRejectsInsufficientThreshold(t *testing.T) {
	manifestJSON, _, root, _ := testBundle(t, "stable", 2)
	key := deterministicPrivateKey(1)
	envelope, err := SignManifest(manifestJSON, root, []ed25519.PrivateKey{key})
	if err == nil {
		t.Fatalf("SignManifest unexpectedly satisfied threshold: %+v", envelope)
	}

	root.Threshold = 1
	envelope, err = SignManifest(manifestJSON, root, []ed25519.PrivateKey{key})
	if err != nil {
		t.Fatal(err)
	}
	root.Threshold = 2
	signaturesJSON, err := CanonicalSignatures(envelope)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Verify(manifestJSON, signaturesJSON, root, testPolicy()); err == nil || !strings.Contains(err.Error(), "threshold") {
		t.Fatalf("Verify error = %v, want threshold rejection", err)
	}
}

func TestSignManifestAcceptsThresholdSubsetOfRootKeys(t *testing.T) {
	manifestJSON, _, root, _ := testBundle(t, "stable", 2)
	thirdKey := deterministicPrivateKey(3)
	root, err := NewRootMetadata(root.Version, root.ExpiresAt, 2, []ed25519.PublicKey{
		deterministicPrivateKey(1).Public().(ed25519.PublicKey),
		deterministicPrivateKey(2).Public().(ed25519.PublicKey),
		thirdKey.Public().(ed25519.PublicKey),
	})
	if err != nil {
		t.Fatal(err)
	}
	envelope, err := SignManifest(manifestJSON, root, []ed25519.PrivateKey{
		deterministicPrivateKey(1), deterministicPrivateKey(2),
	})
	if err != nil {
		t.Fatalf("SignManifest threshold subset: %v", err)
	}
	signaturesJSON, err := CanonicalSignatures(envelope)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Verify(manifestJSON, signaturesJSON, root, testPolicy()); err != nil {
		t.Fatalf("Verify threshold subset: %v", err)
	}
}

func TestMergeManifestSignaturesRequiresIndependentThreshold(t *testing.T) {
	manifestJSON, _, root, _ := testBundle(t, "stable", 2)
	first, err := SignManifestPartial(manifestJSON, root, []ed25519.PrivateKey{deterministicPrivateKey(1)})
	if err != nil {
		t.Fatalf("SignManifestPartial first: %v", err)
	}
	second, err := SignManifestPartial(manifestJSON, root, []ed25519.PrivateKey{deterministicPrivateKey(2)})
	if err != nil {
		t.Fatalf("SignManifestPartial second: %v", err)
	}
	if _, err := MergeManifestSignatures(manifestJSON, root, first); err == nil || !strings.Contains(err.Error(), "threshold") {
		t.Fatalf("single contribution error = %v, want threshold rejection", err)
	}
	merged, err := MergeManifestSignatures(manifestJSON, root, second, first)
	if err != nil {
		t.Fatalf("MergeManifestSignatures: %v", err)
	}
	if len(merged.Signatures) != 2 || merged.Signatures[0].KeyID >= merged.Signatures[1].KeyID {
		t.Fatalf("merged signatures are not canonical: %+v", merged)
	}
	signaturesJSON, err := CanonicalSignatures(merged)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Verify(manifestJSON, signaturesJSON, root, testPolicy()); err != nil {
		t.Fatalf("Verify merged signatures: %v", err)
	}
	if _, err := MergeManifestSignatures(manifestJSON, root, first, first); err == nil || !strings.Contains(err.Error(), "duplicate") {
		t.Fatalf("duplicate contribution error = %v", err)
	}

	mutated := append([]byte(nil), manifestJSON...)
	mutated[len(mutated)-2] ^= 1
	if _, err := MergeManifestSignatures(mutated, root, first, second); err == nil {
		t.Fatal("mutated manifest unexpectedly accepted")
	}
}

func TestVerifyRejectsDowngradesAndIncompatibleRuntime(t *testing.T) {
	manifestJSON, signaturesJSON, root, _ := testBundle(t, "stable", 2)
	tests := []struct {
		name   string
		mutate func(*VerifyPolicy)
		want   string
	}{
		{"sequence", func(p *VerifyPolicy) { p.CurrentSequence = 10203 }, "sequence"},
		{"security epoch", func(p *VerifyPolicy) { p.CurrentSecurityEpoch = 3 }, "security epoch"},
		{"minimum safe version", func(p *VerifyPolicy) { p.CurrentMinimumSafeVersion = "v1.1.0" }, "minimum safe"},
		{"root", func(p *VerifyPolicy) { p.MinimumRootVersion = 2 }, "root version floor"},
		{"installed version", func(p *VerifyPolicy) { p.CurrentVersion = "v1.2.3" }, "installed version"},
		{"server", func(p *VerifyPolicy) { p.ServerVersion = "v2.0.1" }, "server version"},
		{"updater", func(p *VerifyPolicy) { p.UpdaterVersion = "v0.9.9" }, "updater version"},
		{"protocol", func(p *VerifyPolicy) { p.ProtocolVersion = 3 }, "protocol version"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			policy := testPolicy()
			test.mutate(&policy)
			if _, err := Verify(manifestJSON, signaturesJSON, root, policy); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Verify error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestVerifyBootstrapFloorsDoNotDisableManifestFloors(t *testing.T) {
	manifestJSON, signaturesJSON, root, _ := testBundle(t, "stable", 2)
	policy := testPolicy()
	policy.CurrentSequence = 0
	policy.CurrentSecurityEpoch = 0
	policy.CurrentMinimumSafeVersion = ""
	policy.MinimumRootVersion = 0
	if _, err := Verify(manifestJSON, signaturesJSON, root, policy); err != nil {
		t.Fatalf("Verify bootstrap: %v", err)
	}

	manifest, err := ParseManifest(manifestJSON)
	if err != nil {
		t.Fatal(err)
	}
	manifest.SecurityEpoch = 0
	if _, err := CanonicalManifest(manifest); err == nil || !strings.Contains(err.Error(), "security epoch") {
		t.Fatalf("CanonicalManifest error = %v, want positive security epoch rejection", err)
	}
}

func TestProductionPolicyRejectsStaging(t *testing.T) {
	manifestJSON, signaturesJSON, root, _ := testBundle(t, "staging", 2)
	if _, err := Verify(manifestJSON, signaturesJSON, root, testPolicy()); err == nil || !strings.Contains(err.Error(), "channel") {
		t.Fatalf("Verify error = %v, want channel rejection", err)
	}
}

func TestStrictCanonicalParsingRejectsUnknownURLAndWhitespace(t *testing.T) {
	manifestJSON, _, _, _ := testBundle(t, "stable", 2)
	withURL := bytes.Replace(manifestJSON, []byte(`"size":14`), []byte(`"url":"https://attacker.invalid/a","size":14`), 1)
	if _, err := ParseManifest(withURL); err == nil {
		t.Fatal("ParseManifest accepted an artifact URL")
	}
	if _, err := ParseManifest(append(manifestJSON, '\n')); err == nil || !strings.Contains(err.Error(), "canonical") {
		t.Fatalf("ParseManifest error = %v, want canonical rejection", err)
	}
	if _, err := ParseManifest(bytes.Repeat([]byte("x"), MaxManifestBytes+1)); err == nil || !strings.Contains(err.Error(), "limit") {
		t.Fatalf("ParseManifest error = %v, want size limit", err)
	}
}

func TestVerifyRejectsExpiredMetadataAndTampering(t *testing.T) {
	manifestJSON, signaturesJSON, root, _ := testBundle(t, "stable", 2)
	policy := testPolicy()
	policy.Now = time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC)
	if _, err := Verify(manifestJSON, signaturesJSON, root, policy); err == nil || !strings.Contains(err.Error(), "root metadata is expired") {
		t.Fatalf("Verify root expiry error = %v", err)
	}

	policy = testPolicy()
	policy.Now = time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	if _, err := Verify(manifestJSON, signaturesJSON, root, policy); err == nil || !strings.Contains(err.Error(), "manifest is expired") {
		t.Fatalf("Verify manifest expiry error = %v", err)
	}

	tampered := bytes.Replace(manifestJSON, []byte(`"sequence":10203`), []byte(`"sequence":10204`), 1)
	if _, err := Verify(tampered, signaturesJSON, root, testPolicy()); err == nil || !strings.Contains(err.Error(), "invalid signature") {
		t.Fatalf("Verify tamper error = %v", err)
	}
}

func TestVerifyArtifactRejectsSizeAndDigestMismatch(t *testing.T) {
	_, _, _, artifactBytes := testBundle(t, "stable", 2)
	digest := sha256.Sum256(artifactBytes)
	artifact := Artifact{Size: uint64(len(artifactBytes)), SHA256: hex.EncodeToString(digest[:])}
	if err := VerifyArtifact(bytes.NewReader(append(append([]byte{}, artifactBytes...), 'x')), artifact); err == nil || !strings.Contains(err.Error(), "size") {
		t.Fatalf("VerifyArtifact long error = %v", err)
	}
	if err := VerifyArtifact(bytes.NewReader(artifactBytes[:len(artifactBytes)-1]), artifact); err == nil || !strings.Contains(err.Error(), "size") {
		t.Fatalf("VerifyArtifact short error = %v", err)
	}
	artifact.SHA256 = strings.Repeat("0", 64)
	if err := VerifyArtifact(bytes.NewReader(artifactBytes), artifact); err == nil || !strings.Contains(err.Error(), "SHA-256") {
		t.Fatalf("VerifyArtifact digest error = %v", err)
	}
}

func testBundle(t *testing.T, channel string, threshold uint32) ([]byte, []byte, RootMetadata, []byte) {
	t.Helper()
	keys := []ed25519.PrivateKey{deterministicPrivateKey(1), deterministicPrivateKey(2)}
	publicKeys := []ed25519.PublicKey{keys[0].Public().(ed25519.PublicKey), keys[1].Public().(ed25519.PublicKey)}
	root, err := NewRootMetadata(1, "2027-01-01T00:00:00Z", threshold, publicKeys)
	if err != nil {
		t.Fatal(err)
	}
	artifactBytes := []byte("binary payload")
	digest := sha256.Sum256(artifactBytes)
	manifest := Manifest{
		SchemaVersion:      SchemaVersion,
		Channel:            channel,
		RootVersion:        root.Version,
		Version:            "v1.2.3",
		Commit:             strings.Repeat("a", 40),
		Sequence:           10203,
		PublishedAt:        "2026-01-01T00:00:00Z",
		ExpiresAt:          "2026-02-01T00:00:00Z",
		MinimumSafeVersion: "v1.0.0",
		SecurityEpoch:      2,
		Compatibility: Compatibility{
			Server:   VersionRange{Min: "v1.0.0", Max: "v2.0.0"},
			Protocol: ProtocolRange{Min: 1, Max: 2},
			Updater:  VersionRange{Min: "v1.0.0", Max: "v2.0.0"},
		},
		Artifacts: []Artifact{{
			OS:     "linux",
			Arch:   "amd64",
			Name:   "p2pstream_v1.2.3_linux_amd64",
			Size:   uint64(len(artifactBytes)),
			SHA256: hex.EncodeToString(digest[:]),
		}},
	}
	manifestJSON, err := CanonicalManifest(manifest)
	if err != nil {
		t.Fatal(err)
	}
	envelope, err := SignManifest(manifestJSON, root, keys)
	if err != nil {
		t.Fatal(err)
	}
	signaturesJSON, err := CanonicalSignatures(envelope)
	if err != nil {
		t.Fatal(err)
	}
	return manifestJSON, signaturesJSON, root, artifactBytes
}

func deterministicPrivateKey(value byte) ed25519.PrivateKey {
	return ed25519.NewKeyFromSeed(bytes.Repeat([]byte{value}, ed25519.SeedSize))
}

func testPolicy() VerifyPolicy {
	return VerifyPolicy{
		Now:                       time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC),
		CurrentSequence:           10202,
		CurrentSecurityEpoch:      2,
		CurrentMinimumSafeVersion: "v1.0.0",
		MinimumRootVersion:        1,
		CurrentVersion:            "v1.2.2",
		ServerVersion:             "v1.5.0",
		UpdaterVersion:            "v1.1.0",
		ProtocolVersion:           1,
		GOOS:                      "linux",
		GOARCH:                    "amd64",
	}
}
