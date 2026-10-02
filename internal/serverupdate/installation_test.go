package serverupdate

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"p2pstream/internal/agentupdate"
)

func installationFixture(t *testing.T, version, channel string) ([]byte, []byte, []byte, RuntimeStatus) {
	t.Helper()
	raw, metadata, current := releaseFixture(t, version, channel)
	m, err := agentupdate.ParseManifest(raw)
	if err != nil {
		t.Fatal(err)
	}
	i := Installation{API: 1, Version: version, Commit: m.Commit, Channel: channel, Bundle: "p2pstream_" + version + "_docker.tar.gz", UpdaterImages: map[string]string{"linux/amd64": "ghcr.io/test/repo-updater@sha256:" + strings.Repeat("d", 64), "linux/arm64": "ghcr.io/test/repo-updater@sha256:" + strings.Repeat("e", 64)}}
	descriptor, _ := json.Marshal(i)
	m.ReleaseAssets = append(m.ReleaseAssets, agentupdate.ReleaseAsset{Name: InstallationAsset, Size: uint64(len(descriptor)), SHA256: hash(descriptor)}, agentupdate.ReleaseAsset{Name: i.Bundle, Size: 123, SHA256: strings.Repeat("f", 64)})
	slices.SortFunc(m.ReleaseAssets, func(a, b agentupdate.ReleaseAsset) int { return strings.Compare(a.Name, b.Name) })
	raw, err = agentupdate.CanonicalManifest(m)
	if err != nil {
		t.Fatal(err)
	}
	current.Version = version
	current.Commit = m.Commit
	return raw, metadata, descriptor, current
}
func TestInstallationIdentityExistsBeforeEnrollmentAndIsConcurrent(t *testing.T) {
	dir := t.TempDir()
	var wg sync.WaitGroup
	ids := make(chan string, 12)
	for range 12 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			id, err := InstallationIdentity(dir, true)
			if err != nil {
				t.Error(err)
				return
			}
			ids <- id
		}()
	}
	wg.Wait()
	close(ids)
	first := ""
	for id := range ids {
		if first == "" {
			first = id
		}
		if id != first {
			t.Fatal("concurrent server startup created distinct identities")
		}
	}
	if _, err := uuid.Parse(first); err != nil {
		t.Fatal(err)
	}
	read, err := InstallationIdentity(dir, false)
	if err != nil || read != first {
		t.Fatal("identity did not persist")
	}
	if _, err = os.Stat(filepath.Join(dir, "config.json")); !os.IsNotExist(err) {
		t.Fatal("identity depended on enrollment")
	}
}
func TestInstallationAssetsBindPlatformChannelCommitAndOfflineEnrollment(t *testing.T) {
	for _, channel := range []string{"stable", "staging"} {
		version := "v1.0.1"
		if channel == "staging" {
			version += "-staging.10"
		}
		raw, meta, descriptor, current := installationFixture(t, version, channel)
		for _, arch := range []string{"amd64", "arm64"} {
			recipe, err := verifyInstallation(raw, descriptor, "test/repo", version, current.Commit, channel)
			if err != nil {
				t.Fatal(err)
			}
			dir := t.TempDir()
			for name, data := range map[string][]byte{"p2pstream_agent_update_manifest.json": raw, MetadataAsset: meta, InstallationAsset: descriptor} {
				if err = os.WriteFile(filepath.Join(dir, name), data, 0600); err != nil {
					t.Fatal(err)
				}
			}
			floor, err := VerifyEnrollmentInputs(dir, "test/repo", channel, arch, "ghcr.io/test/repo@sha256:"+strings.Repeat("a", 64), recipe.Descriptor.UpdaterImages["linux/"+arch], current)
			if err != nil || floor.Sequence != 12 {
				t.Fatalf("%s/%s: %v", channel, arch, err)
			}
			if _, err = VerifyEnrollmentInputs(dir, "test/repo", channel, arch, "ghcr.io/test/repo@sha256:"+strings.Repeat("b", 64), recipe.Descriptor.UpdaterImages["linux/"+arch], current); err == nil {
				t.Fatal("accepted different server image")
			}
			if _, err = VerifyEnrollmentInputs(dir, "test/repo", channel, arch, "ghcr.io/test/repo@sha256:"+strings.Repeat("a", 64), recipe.Descriptor.UpdaterImages["linux/"+arch]+"0", current); err == nil {
				t.Fatal("accepted different updater")
			}
		}
		for _, mutation := range []func([]byte) []byte{func(data []byte) []byte { return append(data, ' ') }, func(data []byte) []byte {
			return []byte(strings.Replace(string(data), current.Commit, strings.Repeat("b", 40), 1))
		}} {
			if _, err := verifyInstallation(raw, mutation(descriptor), "test/repo", version, current.Commit, channel); err == nil {
				t.Fatal("accepted altered descriptor")
			}
		}
		if _, err := verifyInstallation(raw, descriptor, "test/repo", version, strings.Repeat("b", 40), channel); err == nil {
			t.Fatal("accepted stale command")
		}
	}
}
func TestCopiedSetupUsesServerRecipeAndFixedArchiveExtraction(t *testing.T) {
	raw, _, descriptor, current := installationFixture(t, "v1.0.1", "stable")
	recipe, err := verifyInstallation(raw, descriptor, "test/repo", current.Version, current.Commit, "stable")
	if err != nil {
		t.Fatal(err)
	}
	source, _ := NewGitHubSource("test/repo", "stable", "arm64")
	id := uuid.NewString()
	command, err := source.SetupCommand(recipe, id)
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{id, "--expect-arch 'arm64'", "--expect-version 'v1.0.1'", "sha256sum --check --status", "all(m.isfile()", "sudo python3 -I", "trap"} {
		if !strings.Contains(command, expected) {
			t.Fatalf("recipe omitted %q", expected)
		}
	}
	for _, bad := range []string{"git clone", "docker build", "curl |", "filter=", "whole_source"} {
		if strings.Contains(command, bad) {
			t.Fatalf("unsupported bootstrap %s", bad)
		}
	}
	if _, err = source.SetupCommand(recipe, "bad'\n$(true)"); err == nil {
		t.Fatal("accepted untrusted shell field")
	}
}
func TestInterruptedHostTransactionBlocksUpdateAcceptance(t *testing.T) {
	dir := t.TempDir()
	os.Chmod(dir, 0700)
	driver := &fakeDriver{current: RuntimeStatus{API: API, Version: "v1.0.0", InstanceID: uuid.NewString(), Ready: true}, image: "current"}
	source := &fakeSource{release: Release{Version: "v1.0.1", Image: "target", ExpiresAt: time.Now().Add(time.Hour)}}
	engine, err := NewEngine(dir, driver.current.InstanceID, "stable", driver, source, Floor{})
	if err != nil {
		t.Fatal(err)
	}
	for _, phase := range []string{"prepared", "activating", "applying", "removing", "recovery_required", "removed"} {
		atomic, _ := json.Marshal(map[string]string{"phase": phase})
		if err = AtomicWrite(filepath.Join(dir, "host.json"), atomic, 0600); err != nil {
			t.Fatal(err)
		}
		overview, overviewErr := engine.Overview(context.Background())
		if overviewErr != nil || overview.HostPhase != phase || overview.Current.Ready || !strings.Contains(overview.Warning, "manage repair") {
			t.Fatalf("interrupted %s advertised ready: %+v, %v", phase, overview, overviewErr)
		}
		if _, err = engine.Start(context.Background(), StartRequest{OperationID: uuid.NewString(), Actor: "admin"}); err == nil || !strings.Contains(err.Error(), "host deployment setup is incomplete") {
			t.Fatalf("accepted interrupted %s: %v", phase, err)
		}
	}
}
