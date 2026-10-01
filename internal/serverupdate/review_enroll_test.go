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

// The former checkout-based installer has been replaced by the release host
// controller. Its preflight/lifecycle checks run against deterministic Docker
// boundaries in scripts/test-server-updater-host.py and the real Docker rehearsal.
func TestInstallerRequiresExplicitVerifiedServerBinding(t *testing.T) {
	command := exec.Command("bash", "../../scripts/install-server-updater.sh")
	out, err := command.CombinedOutput()
	if err == nil || !strings.Contains(string(out), "--expect-installation") {
		t.Fatalf("unbound setup did not fail before Docker work: %v %s", err, out)
	}
}
