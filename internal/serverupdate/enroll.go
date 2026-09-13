package serverupdate

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/google/uuid"
	"p2pstream/internal/releaseversion"
)

// Enroll adopts a fully resolved Compose model produced on the deployment
// host. The original model is retained verbatim for operator recovery. Only
// trusted local installation calls this function.
func Enroll(directory string, model []byte, image, updaterImage, version, repository string, bootstrap Floor) error {
	if !filepath.IsAbs(directory) || filepath.Clean(directory) != directory {
		return errors.New("enrollment directory must be absolute")
	}
	if _, err := os.Lstat(filepath.Join(directory, "config.json")); err == nil {
		return errors.New("deployment already enrolled; use its saved compose.json")
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	channel := "stable"
	if releaseversion.Prerelease(version) {
		channel = "staging"
	}
	if !validVersion(version, channel) || !strings.HasPrefix(image, "ghcr.io/"+strings.ToLower(repository)+"@sha256:") || !digestPattern.MatchString(strings.TrimPrefix(image, "ghcr.io/"+strings.ToLower(repository)+"@sha256:")) || !strings.HasPrefix(updaterImage, "sha256:") || !digestPattern.MatchString(strings.TrimPrefix(updaterImage, "sha256:")) {
		return errors.New("enrollment requires exact release and updater images")
	}
	var doc map[string]any
	if err := json.Unmarshal(model, &doc); err != nil {
		return err
	}
	project, _ := doc["name"].(string)
	services, _ := doc["services"].(map[string]any)
	service, _ := services["p2pstream"].(map[string]any)
	if project == "" || service == nil || services["p2pstream-server-updater"] != nil {
		return errors.New("expected an existing p2pstream Compose service")
	}
	if service["build"] != nil || service["post_start"] != nil || service["pre_stop"] != nil || service["pre_start"] != nil {
		return errors.New("builds and lifecycle hooks require a manual deployment")
	}
	if service["command"] != nil {
		return errors.New("custom server commands require a manual deployment")
	}
	if doc["configs"] != nil || doc["secrets"] != nil || service["configs"] != nil || service["secrets"] != nil || service["env_file"] != nil {
		return errors.New("file-backed Compose configuration requires a manual deployment")
	}
	if service["entrypoint"] != nil {
		return errors.New("custom entrypoints require a manual deployment")
	}
	if service["volumes_from"] != nil {
		return errors.New("inherited container mounts require a manual deployment")
	}
	env, _ := service["environment"].(map[string]any)
	if env == nil {
		env = map[string]any{}
	}
	if env["CONFIG_DIR"] != "/data" || env["DATABASE_URL"] != nil && env["DATABASE_URL"] != "" {
		return errors.New("enrollment currently requires CONFIG_DIR=/data and its default SQLite database")
	}
	volumes, _ := service["volumes"].([]any)
	volumes, err := prepareRuntimeTmpfs(service, volumes)
	if err != nil {
		return err
	}
	volumeName := ""
	definitions, _ := doc["volumes"].(map[string]any)
	if definitions == nil {
		definitions = map[string]any{}
	}
	for _, value := range volumes {
		v, _ := value.(map[string]any)
		target, _ := v["target"].(string)
		if target == "/data" {
			if volumeName != "" {
				return errors.New("multiple data mounts are unsupported")
			}
			if options, ok := v["volume"].(map[string]any); ok && options["subpath"] != nil {
				return errors.New("data volume subpaths require a manual deployment")
			}
			if v["type"] != "volume" || v["read_only"] == true {
				return errors.New("/data must use a writable named volume")
			}
			source, _ := v["source"].(string)
			definition, _ := definitions[source].(map[string]any)
			volumeName, _ = definition["name"].(string)
			if volumeName == "" {
				return errors.New("Compose snapshot must resolve the data volume name")
			}
		} else if target == "/tmp" && v["type"] == "tmpfs" {
			// The runtime readiness socket is ephemeral, outside the snapshot.
		} else if v["read_only"] != true {
			return errors.New("additional writable mounts require a manual deployment")
		}
	}
	if volumeName == "" {
		return errors.New("persistent /data volume is missing")
	}
	restart, _ := service["restart"].(string)
	if restart == "" {
		restart = "no"
	}
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		return err
	}
	config := ComposeConfig{BootstrapFloor: bootstrap, InstanceID: uuid.NewString(), Repository: repository, Channel: channel, Project: project, Token: hex.EncodeToString(secret),
		StateDir: directory, ControlDir: filepath.Join(directory, "control"), DataDir: "/server-data", DataVolume: volumeName, DeploymentFile: filepath.Join(directory, "compose.json"), Docker: "/usr/local/bin/docker", Restart: restart, HealthTimeoutSeconds: 300, HealthyDwellSeconds: 120}
	if err := config.Validate(); err != nil {
		return err
	}
	if err := os.MkdirAll(directory, 0700); err != nil {
		return err
	}
	if err := os.Chmod(directory, 0700); err != nil {
		return err
	}
	if err := os.MkdirAll(config.ControlDir, 0755); err != nil {
		return err
	}
	env["SERVER_UPDATE_INSTANCE_ID"] = config.InstanceID
	env["SERVER_UPDATE_TOKEN"] = config.Token
	env["SERVER_UPDATE_SOCKET"] = "/run/p2pstream-server-update/control.sock"
	env["SERVER_UPDATE_GATE_FILE"] = "/run/p2pstream-server-update/maintenance"
	service["environment"] = env
	service["image"] = image
	labels, _ := service["labels"].(map[string]any)
	if labels == nil {
		labels = map[string]any{}
	}
	labels["p2pstream.server-update.instance"] = config.InstanceID
	service["labels"] = labels
	service["volumes"] = append(volumes, map[string]any{"type": "bind", "source": config.ControlDir, "target": "/run/p2pstream-server-update", "read_only": true})
	definitions["server-updater-data"] = map[string]any{"external": true, "name": volumeName}
	doc["volumes"] = definitions
	services["p2pstream-server-updater"] = map[string]any{
		"image": updaterImage, "user": "0:0", "restart": "unless-stopped", "read_only": true, "tmpfs": []string{"/tmp"},
		"command":  []string{"/app/p2pstream", "server-updater", "serve", "--config", filepath.Join(directory, "config.json")},
		"labels":   map[string]any{"p2pstream.server-update.executor": config.InstanceID},
		"cap_drop": []string{"ALL"}, "cap_add": []string{"CHOWN", "DAC_OVERRIDE", "FOWNER", "NET_BIND_SERVICE"},
		"volumes": []any{
			map[string]any{"type": "bind", "source": directory, "target": directory},
			map[string]any{"type": "bind", "source": "/var/run/docker.sock", "target": "/var/run/docker.sock"},
			map[string]any{"type": "volume", "source": "server-updater-data", "target": "/server-data"},
		},
	}
	// Compose's config output already escapes literal dollar signs. Preserve
	// those serialized values verbatim so reloading does not change secrets.
	encoded, err := json.Marshal(doc)
	if err != nil {
		return err
	}
	if err := AtomicWrite(filepath.Join(directory, "original-compose.json"), model, 0600); err != nil {
		return err
	}
	if err := AtomicWrite(config.DeploymentFile, encoded, 0600); err != nil {
		return err
	}
	data, err := json.Marshal(config)
	if err != nil {
		return err
	}
	return AtomicWrite(filepath.Join(directory, "config.json"), data, 0600)
}

// The server used to need writes only under /data. Enrollment also needs a
// container-local Unix socket under /tmp, including on hardened deployments
// with a read-only root filesystem. Preserve a usable existing tmpfs, reject
// mounts which hide it, and add a bounded one when the root filesystem is RO.
func prepareRuntimeTmpfs(service map[string]any, volumes []any) ([]any, error) {
	writableTmp := false
	unsafeMount := errors.New("server updater requires a writable /tmp tmpfs; custom mounts covering its readiness socket require a manual deployment")
	for _, value := range volumes {
		v, _ := value.(map[string]any)
		target, _ := v["target"].(string)
		if !coversRuntimeSocket(target) {
			continue
		}
		if target != "/tmp" || v["type"] != "tmpfs" || v["read_only"] == true {
			return nil, unsafeMount
		}
		if options, ok := v["tmpfs"].(map[string]any); ok {
			if mode, ok := options["mode"].(float64); ok && uint32(mode)&0003 != 0003 {
				return nil, unsafeMount
			}
		}
		writableTmp = true
	}
	if mounts, ok := service["tmpfs"].([]any); ok {
		for _, value := range mounts {
			mount, _ := value.(string)
			target, options, _ := strings.Cut(mount, ":")
			if target == "/data" || strings.HasPrefix(filepath.Clean(target), "/data/") {
				return nil, errors.New("temporary mounts inside /data are not covered by the server snapshot; use a manual deployment")
			}
			if !coversRuntimeSocket(target) {
				continue
			}
			if target != "/tmp" || writableTmp {
				return nil, unsafeMount
			}
			for _, option := range strings.Split(options, ",") {
				if option == "ro" {
					return nil, unsafeMount
				}
				if raw, ok := strings.CutPrefix(option, "mode="); ok {
					mode, err := strconv.ParseUint(raw, 8, 32)
					if err != nil || mode&0003 != 0003 {
						return nil, unsafeMount
					}
				}
			}
			writableTmp = true
		}
	}
	if service["read_only"] == true && !writableTmp {
		volumes = append(volumes, map[string]any{"type": "tmpfs", "target": "/tmp", "tmpfs": map[string]any{"size": 16 << 20, "mode": 01777}})
	}
	return volumes, nil
}

func coversRuntimeSocket(target string) bool {
	return target != "" && (target == RuntimeSocket || strings.HasPrefix(RuntimeSocket, strings.TrimRight(filepath.Clean(target), "/")+"/"))
}
