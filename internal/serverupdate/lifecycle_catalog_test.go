package serverupdate

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"p2pstream/internal/agentupdate"
)

// Exercise the rehearsal's actual Python serialization through the same offline
// verifier as server-updater enroll. Mocked host transport tests cannot detect a
// catalog that the Go helper rejects before preparing its private candidate.
func TestLifecycleCatalogOfflineEnrollment(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Fatal("lifecycle catalog contract test requires python3 on PATH")
	}
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	directory := t.TempDir()
	images := map[string]string{}
	for name, digit := range map[string]string{"baseline": "a", "candidate": "b", "updater": "d", "candidate_updater": "e"} {
		repository := "ghcr.io/review/p2pstream"
		if strings.Contains(name, "updater") {
			repository += "-updater"
		}
		images[name] = repository + "@sha256:" + strings.Repeat(digit, 64)
	}
	encodedImages, err := json.Marshal(images)
	if err != nil {
		t.Fatal(err)
	}
	const generate = `
import importlib.util, json, shutil, sys
from pathlib import Path
root, directory = Path(sys.argv[1]), Path(sys.argv[2])
def load(name, path):
    spec = importlib.util.spec_from_file_location(name, path)
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module
host = load('host', root / 'scripts/server-updater-host.py')
harness = load('harness', root / 'scripts/test-server-lifecycle-docker.py')
bundle = directory / 'bundle'
bundle.mkdir()
for name in host.BUNDLE_FILES:
    source = root / 'scripts' / name
    if not source.is_file():
        source = root / name
    shutil.copyfile(source, bundle / name)
harness.ROOT, harness.BUNDLE = directory, bundle
harness.fixture_catalog(host, json.loads(sys.argv[3]))
`
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, python, "-I", "-B", "-c", generate, root, directory, string(encodedImages))
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("generate actual lifecycle catalog: %v (deadline: %v)\n%s", err, ctx.Err(), output)
	}
	var catalog map[string]struct {
		Release Release           `json:"release"`
		Files   map[string][]byte `json:"files"`
	}
	rawCatalog, err := os.ReadFile(filepath.Join(directory, "catalog.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(rawCatalog, &catalog); err != nil {
		t.Fatal(err)
	}
	for index, name := range []string{"baseline", "candidate"} {
		t.Run(name, func(t *testing.T) {
			entry := catalog[name]
			raw := entry.Files["p2pstream_agent_update_manifest.json"]
			manifest, err := agentupdate.ParseManifest(raw)
			if err != nil {
				t.Fatalf("actual Python fixture must pass production ParseManifest: %v", err)
			}
			version, commit := fmt.Sprintf("v0.1.53-staging.%d", 9000+index), strings.Repeat(string(rune('a'+index)), 40)
			if manifest.Version != version || manifest.Commit != commit {
				t.Fatal("fixture release identity changed")
			}
			if entry.Release.Metadata != NewMetadata(version, commit) {
				t.Fatal("fixture metadata differs from the actual server API/schema contract")
			}
			assets := t.TempDir()
			for asset, data := range entry.Files {
				if err := os.WriteFile(filepath.Join(assets, asset), data, 0600); err != nil {
					t.Fatal(err)
				}
			}
			updater := images["updater"]
			if name == "candidate" {
				updater = images["candidate_updater"]
			}
			current := RuntimeStatus{API: API, Schema: Schema, Version: version, Commit: commit}
			verify := func(image, updater string) (Floor, error) {
				return VerifyEnrollmentInputs(assets, "review/p2pstream", "staging", "amd64", image, updater, current)
			}
			floor, err := verify(images[name], updater)
			if err != nil {
				t.Fatalf("actual Python fixture must pass enroll's offline verifier: %v", err)
			}
			want := Floor{Sequence: uint64(9000 + index), SecurityEpoch: uint64(1 + index),
				MinimumSafeVersion: fmt.Sprintf("v0.1.%d", 51+index), ManifestSHA256: hash(raw)}
			if floor != want || entry.Release.VerificationFloor() != want || entry.Release.Image != images[name] {
				t.Fatalf("offline enrollment and engine fixture must share exact floor/hash/image: got %+v, release %+v, want %+v", floor, entry.Release, want)
			}
			otherImage, otherUpdater := images["candidate"], images["candidate_updater"]
			if name == "candidate" {
				otherImage, otherUpdater = images["baseline"], images["updater"]
			}
			if _, err := verify(otherImage, updater); err == nil {
				t.Fatal("offline verifier accepted a different server image")
			}
			if _, err := verify(images[name], otherUpdater); err == nil {
				t.Fatal("offline verifier accepted a different updater image")
			}
			metadataPath := filepath.Join(assets, MetadataAsset)
			if err := os.WriteFile(metadataPath, append(append([]byte{}, entry.Files[MetadataAsset]...), ' '), 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := verify(images[name], updater); err == nil || !strings.Contains(err.Error(), "server metadata digest mismatch") {
				t.Fatalf("offline verifier must retain metadata hash binding: %v", err)
			}
			if err := os.WriteFile(metadataPath, entry.Files[MetadataAsset], 0600); err != nil {
				t.Fatal(err)
			}

			// Recreate the failing field order without changing any JSON values.
			// The real parser and offline verifier must keep rejecting these bytes.
			epoch := []byte(fmt.Sprintf(`,"security_epoch":%d`, manifest.SecurityEpoch))
			sequence := []byte(fmt.Sprintf(`"sequence":%d`, manifest.Sequence))
			misordered := bytes.Replace(raw, epoch, nil, 1)
			misordered = bytes.Replace(misordered, sequence, append(append([]byte{}, sequence...), epoch...), 1)
			if _, err := agentupdate.ParseManifest(misordered); err == nil || !strings.Contains(err.Error(), "not canonical JSON") {
				t.Fatalf("production parser must reject malformed field order: %v", err)
			}
			if err := os.WriteFile(filepath.Join(assets, "p2pstream_agent_update_manifest.json"), misordered, 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := verify(images[name], updater); err == nil || !strings.Contains(err.Error(), "not canonical JSON") {
				t.Fatalf("offline verifier must reject malformed field order: %v", err)
			}
		})
	}
}
