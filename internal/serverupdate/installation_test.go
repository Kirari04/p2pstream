package serverupdate

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
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

func standaloneInstallationFixture(t *testing.T) InstallationRecipe {
	t.Helper()
	raw, _, descriptor, current := installationFixture(t, "v1.0.1", "stable")
	manifest, err := agentupdate.ParseManifest(raw)
	if err != nil {
		t.Fatal(err)
	}
	manifest.ReleaseAssets = append(manifest.ReleaseAssets, agentupdate.ReleaseAsset{Name: SetupAsset, Size: 8192, SHA256: strings.Repeat("c", 64)})
	slices.SortFunc(manifest.ReleaseAssets, func(a, b agentupdate.ReleaseAsset) int { return strings.Compare(a.Name, b.Name) })
	raw, err = agentupdate.CanonicalManifest(manifest)
	if err != nil {
		t.Fatal(err)
	}
	recipe, err := verifyInstallation(raw, descriptor, "test/repo", current.Version, current.Commit, "stable")
	if err != nil {
		t.Fatal(err)
	}
	return recipe
}

func TestStandaloneSetupUsesManifestAdvertisedReleaseAsset(t *testing.T) {
	recipe := standaloneInstallationFixture(t)
	source, _ := NewGitHubSource("test/repo", "stable", "arm64")
	command, err := source.SetupCommand(recipe, "11111111-1111-4111-8111-111111111111")
	if err != nil {
		t.Fatal(err)
	}
	if strings.ContainsAny(command, "\r\n") || len(command) > 650 {
		t.Fatalf("standalone command is not a compact physical line: %s", command)
	}
	for _, expected := range []string{"(set -o pipefail; curl -fsSL ", "--proto '=https' --proto-redir '=https'", "--max-time 120 --max-filesize 65536", "/test/repo/releases/download/v1.0.1/setup-server-updater.sh' | sudo sh -s -- 'test/repo@v1.0.1'", "'" + recipe.ManifestSHA256 + "' 'arm64' --COMPOSE_OPTIONS--)"} {
		if !strings.Contains(command, expected) {
			t.Fatalf("standalone recipe omitted %q", expected)
		}
	}
	for _, asset := range []agentupdate.ReleaseAsset{
		{Name: "other.sh", Size: 1, SHA256: strings.Repeat("c", 64)},
		{Name: SetupAsset, Size: 65537, SHA256: strings.Repeat("c", 64)},
		{Name: SetupAsset, Size: 1, SHA256: "invalid"},
	} {
		recipe.Setup = asset
		if _, err := source.SetupCommand(recipe, uuid.NewString()); err == nil {
			t.Fatalf("accepted invalid bootstrap: %+v", asset)
		}
	}
}

func TestStandaloneSetupPipelinePreservesArgumentsAndFailsClosed(t *testing.T) {
	recipe := standaloneInstallationFixture(t)
	source, _ := NewGitHubSource("test/repo", "stable", "amd64")
	id := "11111111-1111-4111-8111-111111111111"
	command, err := source.SetupCommand(recipe, id)
	if err != nil {
		t.Fatal(err)
	}
	work := t.TempDir()
	receipt := filepath.Join(work, "receipt.json")
	for name, contents := range map[string]string{
		"curl": "#!/bin/sh\ncat -- \"$SETUP_BODY\"\nexit \"${CURL_EXIT:-0}\"\n",
		"sudo": "#!/bin/sh\nexec \"$@\"\n",
	} {
		if err := os.WriteFile(filepath.Join(work, name), []byte(contents), 0700); err != nil {
			t.Fatal(err)
		}
	}
	body := `main() {
python3 -I - "$@" <<'PY'
import json, pathlib, sys
pathlib.Path(sys.argv[-1]).write_text(json.dumps(sys.argv[1:-1]))
PY
}
main "$@"
`
	bodyPath := filepath.Join(work, "bootstrap.sh")
	quote := func(value string) string { return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'" }
	hostile := "project ' $(touch " + filepath.Join(work, "injected") + "); *"
	options := "-- -p " + quote(hostile) + " -f " + quote("relative compose.yaml") + " " + quote(receipt)
	command = strings.Replace(command, "--COMPOSE_OPTIONS--", options, 1)
	for _, test := range []struct {
		name, body, curlExit     string
		wantFailure, wantReceipt bool
	}{
		{"complete", body, "0", false, true},
		{"curl-failed", "", "22", true, false},
		// A successful empty HTTP response gives sh no program; it cannot
		// install anything or print a success claim, although sh exits zero.
		{"empty", "", "0", false, false},
		{"truncated-function", body[:strings.Index(body, "PY\n}")], "0", true, false},
		{"truncated-before-invocation", body[:strings.LastIndex(body, "main ")], "18", true, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			os.Remove(receipt)
			if err := os.WriteFile(bodyPath, []byte(test.body), 0600); err != nil {
				t.Fatal(err)
			}
			cmd := exec.Command("bash", "-c", command)
			cmd.Env = append(os.Environ(), "PATH="+work+":"+os.Getenv("PATH"), "SETUP_BODY="+bodyPath, "CURL_EXIT="+test.curlExit)
			output, err := cmd.CombinedOutput()
			if (err != nil) != test.wantFailure {
				t.Fatalf("pipeline failure=%v; want %v: %s", err, test.wantFailure, output)
			}
			raw, readErr := os.ReadFile(receipt)
			if !test.wantReceipt {
				if !os.IsNotExist(readErr) {
					t.Fatalf("partial pipeline invoked installer: %s, %v", raw, readErr)
				}
				return
			}
			var got []string
			if readErr != nil || json.Unmarshal(raw, &got) != nil {
				t.Fatalf("missing host receipt: %s, %v", raw, readErr)
			}
			want := []string{"test/repo@v1.0.1", id, recipe.ManifestSHA256, "amd64", "--", "-p", hostile, "-f", "relative compose.yaml"}
			if !slices.Equal(got, want) {
				t.Fatalf("changed shell argument boundaries: got %#v want %#v", got, want)
			}
			if _, err := os.Stat(filepath.Join(work, "injected")); !os.IsNotExist(err) {
				t.Fatal("executed hostile Compose value")
			}
		})
	}
}
