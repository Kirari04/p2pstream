package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"p2pstream/internal/agentupdate"
)

func TestCreateAppliesFixedUpdaterCompatibilityFloor(t *testing.T) {
	for _, test := range []struct {
		name, version, configured, want string
	}{
		{"first corrected release", "v0.1.53-staging.88", "v0.1.52", "v0.1.53-staging.88"},
		{"later release keeps compatible runner", "v0.1.53-staging.89", "v0.1.52", "v0.1.53-staging.88"},
		{"numeric prerelease ordering", "v0.1.53-staging.100", "v0.1.53-staging.99", "v0.1.53-staging.99"},
		{"stricter configured floor retained", "v0.1.54-staging.1", "v0.1.53", "v0.1.53"},
	} {
		t.Run(test.name, func(t *testing.T) {
			directory := t.TempDir()
			artifactPath := filepath.Join(directory, "p2pstream_"+test.version+"_linux_amd64")
			output := filepath.Join(directory, "manifest.json")
			if err := os.WriteFile(artifactPath, []byte("test executable"), 0755); err != nil {
				t.Fatal(err)
			}
			err := run([]string{
				"create", "--channel", "staging", "--version", test.version, "--commit", strings.Repeat("a", 40), "--sequence", "88",
				"--published-at", "2026-09-11T00:00:00Z", "--expires-at", "2026-10-11T00:00:00Z",
				"--minimum-safe-version", "v0.1.52", "--security-epoch", "1",
				"--server-min", "v0.1.52", "--server-max", "v0.1.999", "--protocol-min", "1", "--protocol-max", "1",
				"--updater-min", test.configured, "--updater-max", "v0.1.999", "--artifact", "linux/amd64=" + artifactPath,
				"--output", output,
			})
			if err != nil {
				t.Fatal(err)
			}
			data, err := os.ReadFile(output)
			if err != nil {
				t.Fatal(err)
			}
			manifest, err := agentupdate.ParseManifest(data)
			if err != nil {
				t.Fatal(err)
			}
			if manifest.Compatibility.Updater.Min != test.want {
				t.Fatalf("manifest minimum updater = %s, want %s", manifest.Compatibility.Updater.Min, test.want)
			}
		})
	}
}

func TestCreateRejectsImpossibleOrMalformedUpdaterCompatibility(t *testing.T) {
	for _, test := range []struct{ minimum, maximum string }{
		{"v0.1.52", "v0.1.53-staging.87"},
		{"v0.1.54", "v0.1.53"},
		{"", "v0.1.999"},
		{"v0.1", "v0.1.999"},
		{"v0.1.52", "v0.1.53-staging.088"},
	} {
		t.Run(test.minimum+"/"+test.maximum, func(t *testing.T) {
			output := filepath.Join(t.TempDir(), "manifest.json")
			err := run([]string{"create", "--updater-min", test.minimum, "--updater-max", test.maximum, "--output", output})
			if err == nil || !strings.Contains(err.Error(), "updater compatibility") {
				t.Fatalf("error = %v, want updater compatibility rejection", err)
			}
			if _, err := os.Stat(output); !os.IsNotExist(err) {
				t.Fatal("invalid compatibility wrote a release manifest")
			}
		})
	}
}
