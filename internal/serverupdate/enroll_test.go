package serverupdate

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func enrollmentFixture() map[string]any {
	return map[string]any{"name": "fixture", "services": map[string]any{"p2pstream": map[string]any{
		"image": "ghcr.io/test/repo:latest", "restart": "unless-stopped", "ports": []any{map[string]any{"target": 8081, "published": "18081", "protocol": "tcp"}},
		"environment": map[string]any{"CONFIG_DIR": "/data", "SECRET": "literal-$$HOME-$${SECRET}-$$$$"},
		"volumes":     []any{map[string]any{"type": "volume", "source": "data", "target": "/data"}},
	}}, "volumes": map[string]any{"data": map[string]any{"name": "fixture-data"}}}
}
func enrollFixture(t *testing.T, doc map[string]any) (string, error) {
	t.Helper()
	directory := t.TempDir()
	data, _ := json.Marshal(doc)
	err := Enroll(directory, data, "ghcr.io/test/repo@sha256:"+strings.Repeat("a", 64), "sha256:"+strings.Repeat("b", 64), "v1.0.0", "test/repo", Floor{Sequence: 1, SecurityEpoch: 1, MinimumSafeVersion: "v1.0.0", ManifestSHA256: strings.Repeat("c", 64)})
	return directory, err
}
func TestEnrollmentPreservesDeploymentAndSeparatesDockerAuthority(t *testing.T) {
	directory, err := enrollFixture(t, enrollmentFixture())
	if err != nil {
		t.Fatal(err)
	}
	data, err := readProtected(filepath.Join(directory, "compose.json"), 2<<20)
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatal(err)
	}
	services := doc["services"].(map[string]any)
	server := services["p2pstream"].(map[string]any)
	updater := services["p2pstream-server-updater"].(map[string]any)
	mounts, _ := json.Marshal(server["volumes"])
	if strings.Contains(string(mounts), "docker.sock") || !strings.Contains(string(mounts), `"read_only":true`) {
		t.Fatalf("unsafe manager mounts: %s", mounts)
	}
	mounts, _ = json.Marshal(updater["volumes"])
	if !strings.Contains(string(mounts), "docker.sock") || updater["ports"] != nil || updater["read_only"] != true {
		t.Fatal("invalid independent executor")
	}
	if server["environment"].(map[string]any)["SECRET"] != "literal-$$HOME-$${SECRET}-$$$$" {
		t.Fatal("interpolation was not escaped")
	}
	if _, err := os.Stat(filepath.Join(directory, "original-compose.json")); err != nil {
		t.Fatal(err)
	}
	// Compose parses the saved model without starting any containers or servers.
	if _, err := exec.LookPath("docker"); err == nil {
		cmd := exec.Command("docker", "compose", "-f", filepath.Join(directory, "compose.json"), "config", "--format", "json")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("Compose rejected enrolled model: %v %s", err, out)
		}
		var resolved map[string]any
		if err := json.Unmarshal(out, &resolved); err != nil {
			t.Fatal(err)
		}
		got := resolved["services"].(map[string]any)["p2pstream"].(map[string]any)["environment"].(map[string]any)["SECRET"]
		if got != "literal-$$HOME-$${SECRET}-$$$$" {
			t.Fatalf("Compose changed literal secret: %q", got)
		}
	}
	if err := Enroll(directory, nil, "", "", "", "", Floor{}); err == nil {
		t.Fatal("re-enrolled deployment")
	}
}
func TestEnrollmentRejectsUnrecoverableLayouts(t *testing.T) {
	for _, key := range []string{"build", "command", "entrypoint", "post_start", "pre_stop", "secrets", "configs", "env_file"} {
		t.Run(key, func(t *testing.T) {
			doc := enrollmentFixture()
			doc["services"].(map[string]any)["p2pstream"].(map[string]any)[key] = "custom"
			if _, err := enrollFixture(t, doc); err == nil {
				t.Fatal("accepted custom layout")
			}
		})
	}
	for _, mutation := range []func(map[string]any){
		func(s map[string]any) { s["environment"].(map[string]any)["DATABASE_URL"] = "/external/db" },
		func(s map[string]any) { s["volumes"].([]any)[0].(map[string]any)["type"] = "bind" },
		func(s map[string]any) {
			s["volumes"] = append(s["volumes"].([]any), map[string]any{"type": "bind", "target": "/other", "source": "/etc"})
		},
	} {
		doc := enrollmentFixture()
		mutation(doc["services"].(map[string]any)["p2pstream"].(map[string]any))
		if _, err := enrollFixture(t, doc); err == nil {
			t.Fatal("accepted unrecoverable data layout")
		}
	}
}
