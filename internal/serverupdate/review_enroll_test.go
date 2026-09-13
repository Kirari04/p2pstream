package serverupdate

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestEnrollmentReadOnlyRuntimeGetsWritableSocketTmpfs(t *testing.T) {
	doc := enrollmentFixture()
	doc["services"].(map[string]any)["p2pstream"].(map[string]any)["read_only"] = true
	directory, err := enrollFixture(t, doc)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(directory, "compose.json"))
	if err != nil {
		t.Fatal(err)
	}
	var saved map[string]any
	if err := json.Unmarshal(data, &saved); err != nil {
		t.Fatal(err)
	}
	service := saved["services"].(map[string]any)["p2pstream"].(map[string]any)
	if service["read_only"] != true {
		t.Fatal("disabled the server's read-only root filesystem")
	}
	var sockets int
	for _, raw := range service["volumes"].([]any) {
		v := raw.(map[string]any)
		if v["target"] == "/tmp" {
			sockets++
			if v["type"] != "tmpfs" || v["read_only"] == true || v["tmpfs"].(map[string]any)["mode"] != float64(01777) {
				t.Fatalf("unusable readiness tmpfs: %v", v)
			}
		}
	}
	if sockets != 1 {
		t.Fatalf("readiness tmpfs count = %d", sockets)
	}
	if _, err := exec.LookPath("docker"); err == nil {
		out, err := exec.Command("docker", "compose", "-f", filepath.Join(directory, "compose.json"), "config", "--quiet").CombinedOutput()
		if err != nil {
			t.Fatalf("Compose rejected readiness tmpfs: %v: %s", err, out)
		}
	}
}

func TestEnrollmentChecksExistingRuntimeSocketMounts(t *testing.T) {
	for _, test := range []struct {
		mount string
		valid bool
	}{
		{"/tmp", true}, {"/tmp:rw,nosuid,size=32m,mode=1777", true},
		{"/tmp:ro", false}, {"/tmp:mode=0700", false}, {"/", false},
		{"/data", false}, {"/data/certs", false},
		{RuntimeSocket, false},
	} {
		t.Run(test.mount, func(t *testing.T) {
			doc := enrollmentFixture()
			service := doc["services"].(map[string]any)["p2pstream"].(map[string]any)
			service["read_only"] = true
			service["tmpfs"] = []any{test.mount}
			_, err := enrollFixture(t, doc)
			if (err == nil) != test.valid {
				t.Fatalf("mount %q: %v", test.mount, err)
			}
		})
	}
	doc := enrollmentFixture()
	service := doc["services"].(map[string]any)["p2pstream"].(map[string]any)
	service["volumes"] = append(service["volumes"].([]any), map[string]any{"type": "bind", "source": "/host/tmp", "target": "/tmp", "read_only": true})
	if _, err := enrollFixture(t, doc); err == nil {
		t.Fatal("accepted a read-only bind covering the readiness socket")
	}
	doc = enrollmentFixture()
	doc["services"].(map[string]any)["p2pstream"].(map[string]any)["volumes_from"] = []any{"other-service"}
	if _, err := enrollFixture(t, doc); err == nil {
		t.Fatal("accepted inherited mounts which can hide the snapshotted data")
	}
}

func TestInstallerRefusesUnappliedComposeBeforeEnrollment(t *testing.T) {
	source, err := os.ReadFile("../../scripts/install-server-updater.sh")
	if err != nil {
		t.Fatal(err)
	}
	helper, err := os.ReadFile("../../scripts/server-updater-compose.sh")
	if err != nil {
		t.Fatal(err)
	}
	for _, matches := range []bool{true, false} {
		t.Run(map[bool]string{true: "matching", false: "drifted"}[matches], func(t *testing.T) {
			directory := t.TempDir()
			// Only relocate installation and bypass the host-root prerequisite in
			// this copy; execute the real installer flow against an inert CLI.
			script := strings.Replace(string(source), "if [[ ${EUID} -ne 0 ]]; then", "if false; then", 1)
			script = strings.Replace(script, "state_dir=/etc/p2pstream-server-updater", "state_dir="+filepath.Join(directory, "state"), 1)
			for name, data := range map[string][]byte{"install.sh": []byte(script), "server-updater-compose.sh": helper, "Dockerfile.updater": {}} {
				if err := os.WriteFile(filepath.Join(directory, name), data, 0700); err != nil {
					t.Fatal(err)
				}
			}
			// The Dockerfile redirection is opened before the stub runs.
			script = strings.Replace(script, "$script_dir/../Dockerfile.updater", "$script_dir/Dockerfile.updater", 1)
			if err := os.WriteFile(filepath.Join(directory, "install.sh"), []byte(script), 0700); err != nil {
				t.Fatal(err)
			}
			stub := `#!/bin/bash
set -eu
case "$1" in
 compose)
  if [[ "$*" == *"--hash"* ]]; then printf 'p2pstream %s\n' "$REVIEW_SNAPSHOT_HASH"
  elif [[ "$*" == *"--format json"* ]]; then printf '{}\n'
  elif [[ "$*" == *" ps "* ]]; then printf 'server-container\n'
  fi ;;
 inspect)
  if [[ "$*" == *config-hash* ]]; then printf '%s\n' "$REVIEW_RUNNING_HASH"
  else printf 'image-id\n'; fi ;;
 image) printf 'ghcr.io/test/repo@sha256:%064d\n' 0 ;;
 run) exit 0 ;;
 build) touch "$REVIEW_BUILD_MARKER"; exit 42 ;;
 *) exit 98 ;;
esac
`
			if err := os.WriteFile(filepath.Join(directory, "docker"), []byte(stub), 0700); err != nil {
				t.Fatal(err)
			}
			running := strings.Repeat("a", 64)
			snapshot := running
			if !matches {
				snapshot = strings.Repeat("b", 64)
			}
			marker := filepath.Join(directory, "build-attempted")
			cmd := exec.Command("bash", filepath.Join(directory, "install.sh"))
			cmd.Env = append(os.Environ(), "PATH="+directory+":"+os.Getenv("PATH"), "REVIEW_RUNNING_HASH="+running, "REVIEW_SNAPSHOT_HASH="+snapshot, "REVIEW_BUILD_MARKER="+marker)
			out, err := cmd.CombinedOutput()
			if err == nil {
				t.Fatal("expected preflight failure or intentional build stop")
			}
			_, built := os.Stat(marker)
			if matches && built != nil {
				t.Fatalf("matching model did not reach build: %s", out)
			}
			if !matches && (built == nil || !strings.Contains(string(out), "differs from the running server")) {
				t.Fatalf("drift passed preflight: %s", out)
			}
		})
	}
}
