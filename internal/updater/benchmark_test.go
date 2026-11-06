package updater

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"p2pstream/internal/agentupdate"
)

func BenchmarkAgentUpdateManifestVerification(b *testing.B) {
	manifest := benchmarkManifest(b)
	verifier := AgentUpdateVerifier{}
	policy := VerifyPolicy{
		Now: time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC), CurrentSequence: 10202,
		CurrentSecurityEpoch: 2, CurrentMinimumSafeVersion: "v1.0.0",
		CurrentVersion: "v1.2.2", ServerVersion: "v1.5.0", UpdaterVersion: "v1.1.0",
		ProtocolVersion: 1, RequiredChannel: "stable",
	}
	b.ReportAllocs()
	b.SetBytes(int64(len(manifest)))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := verifier.Verify(manifest, policy); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkUpdaterStageRawBinary(b *testing.B) {
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		b.StopTimer()
		f := newFixture(b)
		b.StartTimer()
		_, err := Stage(context.Background(), StageOptions{
			Paths: f.paths, Source: f.source, Verifier: f.verifier,
			Policy: VerifyPolicy{CurrentVersion: "v1.0.0", ServerVersion: "v1.5.0"}, DiskPreflight: allowDisk,
		})
		b.StopTimer()
		if err != nil {
			b.Fatal(err)
		}
		if err := os.RemoveAll(filepath.Dir(f.paths.StateDir)); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkRootActivationPromoteAndJournal(b *testing.B) {
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		b.StopTimer()
		f := newFixture(b)
		stageAndRequestActivation(b, f)
		b.StartTimer()
		_, err := Activate(context.Background(), ActivateOptions{
			Paths: f.paths, Verifier: f.verifier, Service: &fakeService{},
			Policy: VerifyPolicy{CurrentVersion: "v1.0.0"}, DiskPreflight: allowDisk,
		})
		b.StopTimer()
		if err != nil {
			b.Fatal(err)
		}
		if err := os.RemoveAll(filepath.Dir(f.paths.StateDir)); err != nil {
			b.Fatal(err)
		}
	}
}

func benchmarkManifest(b *testing.B) []byte {
	b.Helper()
	body := []byte("benchmark raw binary")
	digest := sha256.Sum256(body)
	manifest := agentupdate.Manifest{
		SchemaVersion: agentupdate.SchemaVersion, Channel: "stable",
		Version: "v1.2.3", Commit: strings.Repeat("a", 40), Sequence: 10203,
		PublishedAt: "2026-01-01T00:00:00Z", ExpiresAt: "2026-02-01T00:00:00Z",
		MinimumSafeVersion: "v1.0.0", SecurityEpoch: 2,
		Compatibility: agentupdate.Compatibility{
			Server:   agentupdate.VersionRange{Min: "v1.0.0", Max: "v2.0.0"},
			Updater:  agentupdate.VersionRange{Min: "v1.0.0", Max: "v2.0.0"},
			Protocol: agentupdate.ProtocolRange{Min: 1, Max: 2},
		},
		Artifacts: []agentupdate.Artifact{{
			OS: runtime.GOOS, Arch: runtime.GOARCH, Name: runtimeArtifactName("v1.2.3"),
			Size: uint64(len(body)), SHA256: hex.EncodeToString(digest[:]),
		}},
	}
	manifestJSON, err := agentupdate.CanonicalManifest(manifest)
	if err != nil {
		b.Fatal(err)
	}
	return manifestJSON
}
