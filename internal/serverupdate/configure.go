package serverupdate

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
)

// ConfigureModel validates editable deployment inputs using enrollment's layout
// policy, then retains the current image and exact private executor connection.
// It never applies changes to other services or deployment resources.
func ConfigureModel(config ComposeConfig, input, current []byte) ([]byte, error) {
	if err := config.Validate(); err != nil {
		return nil, err
	}
	var next, old map[string]any
	if err := json.Unmarshal(input, &next); err != nil {
		return nil, err
	}
	if err := json.Unmarshal(current, &old); err != nil {
		return nil, err
	}
	services, _ := next["services"].(map[string]any)
	previous, _ := old["services"].(map[string]any)
	server, _ := previous["p2pstream"].(map[string]any)
	updater, _ := previous["p2pstream-server-updater"].(map[string]any)
	image, _ := server["image"].(string)
	updaterImage, _ := updater["image"].(string)
	if image == "" || !validUpdaterImage(updaterImage, config.Repository) {
		return nil, errors.New("invalid current deployment")
	}
	if next["name"] != config.Project {
		return nil, errors.New("Compose project changed")
	}
	// Other services must still agree with enrollment's authoritative model.
	for key, value := range previous {
		if key != "p2pstream" && key != "p2pstream-server-updater" && !reflect.DeepEqual(services[key], value) {
			return nil, errors.New("changes to other services require a separate deployment")
		}
	}
	for key := range services {
		if key != "p2pstream" && previous[key] == nil {
			return nil, errors.New("additional services require a separate deployment")
		}
	}
	oldVolumes, _ := old["volumes"].(map[string]any)
	resources := map[string]any{}
	for key, value := range oldVolumes {
		if key != "server-updater-data" {
			resources[key] = value
		}
	}
	candidateVolumes, _ := next["volumes"].(map[string]any)
	if candidateVolumes == nil {
		candidateVolumes = map[string]any{}
	}
	if !reflect.DeepEqual(candidateVolumes, resources) {
		return nil, errors.New("data volume definitions changed")
	}
	for _, key := range []string{"networks", "configs", "secrets"} {
		if !reflect.DeepEqual(next[key], old[key]) {
			return nil, errors.New("deployment resources changed")
		}
	}
	// Validate before replacing connection fields. Source files never contain the
	// credentials generated during enrollment.
	directory, err := os.MkdirTemp(config.StateDir, ".validate-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(directory)
	data, err := json.Marshal(next)
	if err != nil {
		return nil, err
	}
	validationVersion := "v1.0.0"
	if config.Channel == "staging" {
		validationVersion += "-staging.1"
	}
	if err := EnrollInstallation(directory, data, image, updaterImage, validationVersion, config.Repository, config.BootstrapFloor, config.InstanceID); err != nil {
		return nil, err
	}
	data, err = os.ReadFile(filepath.Join(directory, "compose.json"))
	if err != nil {
		return nil, err
	}
	var validated map[string]any
	if err = json.Unmarshal(data, &validated); err != nil {
		return nil, err
	}
	managed := validated["services"].(map[string]any)
	candidate := managed["p2pstream"].(map[string]any)
	if dataMount(candidate) != config.DataVolume { // dataMount resolves below using definitions
		definitions := validated["volumes"].(map[string]any)
		if resolveDataVolume(candidate, definitions) != config.DataVolume {
			return nil, errors.New("data volume changed")
		}
	}
	if candidate["platform"] != server["platform"] {
		return nil, errors.New("server platform changed")
	}
	// Pin the configured restart policy too: the executor restores it on commit.
	if candidate["restart"] != server["restart"] {
		return nil, errors.New("restart policy changed; remove enrollment before changing it")
	}
	env := candidate["environment"].(map[string]any)
	oldEnv := server["environment"].(map[string]any)
	for key, value := range oldEnv {
		if strings.HasPrefix(key, "SERVER_UPDATE_") {
			env[key] = value
		}
	}
	labels := candidate["labels"].(map[string]any)
	labels["p2pstream.server-update.instance"] = config.InstanceID
	mounts := candidate["volumes"].([]any)
	for _, value := range mounts {
		mount, _ := value.(map[string]any)
		if mount["target"] == "/run/p2pstream-server-update" {
			mount["source"] = config.ControlDir
		}
	}
	managed["p2pstream-server-updater"] = updater
	return json.Marshal(validated)
}
func dataMount(service map[string]any) string {
	volumes, _ := service["volumes"].([]any)
	for _, value := range volumes {
		v, _ := value.(map[string]any)
		if v["target"] == "/data" {
			source, _ := v["source"].(string)
			return source
		}
	}
	return ""
}
func resolveDataVolume(service, definitions map[string]any) string {
	definition, _ := definitions[dataMount(service)].(map[string]any)
	name, _ := definition["name"].(string)
	return name
}
