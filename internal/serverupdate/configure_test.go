package serverupdate

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestConfigurationAppliesEditableInputsPreservingUpdatedImageAndPrivateConnection(t *testing.T) {
	doc := enrollmentFixture()
	dir, err := enrollFixture(t, doc)
	if err != nil {
		t.Fatal(err)
	}
	var cfg ComposeConfig
	data, _ := os.ReadFile(filepath.Join(dir, "config.json"))
	json.Unmarshal(data, &cfg)
	current, _ := os.ReadFile(cfg.DeploymentFile)
	var enrolled map[string]any
	json.Unmarshal(current, &enrolled)
	server := enrolled["services"].(map[string]any)["p2pstream"].(map[string]any)
	server["image"] = "ghcr.io/test/repo@sha256:" + strings.Repeat("e", 64)
	current, _ = json.Marshal(enrolled)
	inputServer := doc["services"].(map[string]any)["p2pstream"].(map[string]any)
	inputServer["environment"].(map[string]any)["MANAGEMENT_PUBLIC_URL"] = "https://new.example:9443"
	inputServer["ports"].([]any)[0].(map[string]any)["published"] = "9443"
	input, _ := json.Marshal(doc)
	configured, err := ConfigureModel(cfg, input, current)
	if err != nil {
		t.Fatal(err)
	}
	var result map[string]any
	json.Unmarshal(configured, &result)
	next := result["services"].(map[string]any)["p2pstream"].(map[string]any)
	env := next["environment"].(map[string]any)
	if next["image"] != server["image"] || env["SERVER_UPDATE_TOKEN"] != cfg.Token || env["SERVER_UPDATE_INSTANCE_ID"] != cfg.InstanceID || env["MANAGEMENT_PUBLIC_URL"] != "https://new.example:9443" || env["SECRET"] != "literal-$$HOME-$${SECRET}-$$$$" {
		t.Fatalf("lost pinned/current deployment connection/settings: %s", configured)
	}
	updater, _ := json.Marshal(result["services"].(map[string]any)["p2pstream-server-updater"])
	originalUpdater, _ := json.Marshal(enrolled["services"].(map[string]any)["p2pstream-server-updater"])
	if string(updater) != string(originalUpdater) {
		t.Fatal("executor authority changed")
	}
	for _, mutate := range []func(map[string]any){func(s map[string]any) { s["command"] = []string{"custom"} }, func(s map[string]any) { s["post_start"] = []any{map[string]any{"command": "custom"}} }, func(s map[string]any) { s["environment"].(map[string]any)["DATABASE_URL"] = "postgres://external" }, func(s map[string]any) { s["platform"] = "linux/arm64" }, func(s map[string]any) { s["scale"] = float64(2) }} {
		fixture := enrollmentFixture()
		mutate(fixture["services"].(map[string]any)["p2pstream"].(map[string]any))
		raw, _ := json.Marshal(fixture)
		if _, err := ConfigureModel(cfg, raw, current); err == nil {
			t.Fatal("accepted unsupported apply")
		}
	}
}
func TestEnrollmentRejectsReservedResourceCollisions(t *testing.T) {
	for _, kind := range []string{"volume", "control"} {
		doc := enrollmentFixture()
		if kind == "volume" {
			doc["volumes"].(map[string]any)["server-updater-data"] = map[string]any{"name": "custom"}
		} else {
			server := doc["services"].(map[string]any)["p2pstream"].(map[string]any)
			server["volumes"] = append(server["volumes"].([]any), map[string]any{"type": "bind", "source": "/host", "target": "/run/p2pstream-server-update", "read_only": true})
		}
		if _, err := enrollFixture(t, doc); err == nil {
			t.Fatal("overwrote reserved resource")
		}
	}
}
