package serverupdate

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"p2pstream/internal/releaseversion"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

// ComposeConfig is installed locally in an executor-owned directory. None of
// these fields is accepted over the management API.
type ComposeConfig struct {
	InstanceID           string `json:"instance_id"`
	Repository           string `json:"repository"`
	Channel              string `json:"channel"`
	Project              string `json:"project"`
	BootstrapFloor       Floor  `json:"bootstrap_floor"`
	Token                string `json:"token"`
	StateDir             string `json:"state_dir"`
	ControlDir           string `json:"control_dir"`
	DataDir              string `json:"data_dir"`
	DataVolume           string `json:"data_volume"`
	DeploymentFile       string `json:"deployment_file"`
	Docker               string `json:"docker"`
	Restart              string `json:"restart"`
	HealthTimeoutSeconds int    `json:"health_timeout_seconds"`
	HealthyDwellSeconds  int    `json:"healthy_dwell_seconds"`
}

type ComposeDriver struct{ Config ComposeConfig }

func (c ComposeConfig) Validate() error {
	if c.BootstrapFloor.Sequence == 0 || c.BootstrapFloor.SecurityEpoch == 0 || !digestPattern.MatchString(c.BootstrapFloor.ManifestSHA256) || !releaseversion.Valid(c.BootstrapFloor.MinimumSafeVersion) {
		return errors.New("verified installation floor is missing")
	}
	for _, p := range []string{c.StateDir, c.ControlDir, c.DataDir, c.DeploymentFile, c.Docker} {
		if !filepath.IsAbs(p) || filepath.Clean(p) != p {
			return errors.New("updater paths must be fixed absolute paths")
		}
	}
	if c.StateDir == c.DataDir || strings.HasPrefix(c.StateDir, c.DataDir+"/") || c.ControlDir == c.DataDir || strings.HasPrefix(c.ControlDir, c.DataDir+"/") {
		return errors.New("updater state must be outside application data")
	}
	if len(c.Token) < 32 || c.Project == "" || c.DataVolume == "" || (c.Restart != "unless-stopped" && c.Restart != "always" && c.Restart != "no") {
		return errors.New("invalid updater enrollment")
	}
	if c.HealthTimeoutSeconds < 10 || c.HealthTimeoutSeconds > 1800 || c.HealthyDwellSeconds < 1 || c.HealthyDwellSeconds > 600 || c.HealthyDwellSeconds >= c.HealthTimeoutSeconds {
		return errors.New("invalid updater health deadlines")
	}
	return nil
}

func (d *ComposeDriver) command(ctx context.Context, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, d.Config.Docker, args...)
	cmd.WaitDelay = 5 * time.Second
	cmd.Env = []string{"PATH=/usr/local/bin:/usr/bin:/bin", "HOME=/root", "DOCKER_HOST=unix:///var/run/docker.sock", "COMPOSE_DISABLE_ENV_FILE=1"}
	var out limitedBuffer
	var stderr limitedBuffer
	cmd.Stdout = &out
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("Docker %s failed: %w", args[0], err)
	}
	return out.Bytes(), nil
}

type limitedBuffer struct{ bytes.Buffer }

func (b *limitedBuffer) Write(p []byte) (int, error) {
	if b.Len()+len(p) > 2<<20 {
		return 0, errors.New("Docker response exceeds limit")
	}
	return b.Buffer.Write(p)
}

func (d *ComposeDriver) compose(ctx context.Context, args ...string) ([]byte, error) {
	return d.command(ctx, append([]string{"compose", "--project-name", d.Config.Project, "--project-directory", filepath.Dir(d.Config.DeploymentFile), "-f", d.Config.DeploymentFile}, args...)...)
}

func (d *ComposeDriver) container(ctx context.Context) (string, error) {
	out, err := d.compose(ctx, "ps", "--all", "--quiet", "p2pstream")
	if err != nil {
		return "", err
	}
	ids := strings.Fields(string(out))
	if len(ids) == 0 {
		return "", nil
	}
	if len(ids) != 1 {
		return "", errors.New("expected exactly one managed server")
	}
	return ids[0], nil
}

type containerInfo struct {
	ID     string `json:"Id"`
	Image  string
	State  struct{ Running bool }
	Config struct {
		Image  string
		Labels map[string]string
	}
	Mounts []struct {
		Name, Destination string
		RW                bool
	}
}

func (d *ComposeDriver) inspectContainer(ctx context.Context, id string) (containerInfo, error) {
	out, err := d.command(ctx, "inspect", id)
	if err != nil {
		return containerInfo{}, err
	}
	var info []containerInfo
	if err := json.Unmarshal(out, &info); err != nil || len(info) != 1 {
		return containerInfo{}, errors.New("invalid Docker inspection")
	}
	if info[0].Config.Labels["com.docker.compose.project"] != d.Config.Project || info[0].Config.Labels["com.docker.compose.service"] != "p2pstream" || info[0].Config.Labels["p2pstream.server-update.instance"] != d.Config.InstanceID {
		return containerInfo{}, errors.New("container is outside the enrolled deployment")
	}
	return info[0], nil
}

func (d *ComposeDriver) Inspect(ctx context.Context) (RuntimeStatus, string, string, error) {
	data, err := readProtected(d.Config.DeploymentFile, 2<<20)
	if err != nil {
		return RuntimeStatus{}, "", "", err
	}
	id, err := d.container(ctx)
	if err != nil || id == "" {
		return RuntimeStatus{}, "", "", errors.New("managed server container is missing")
	}
	info, err := d.inspectContainer(ctx, id)
	if err != nil {
		return RuntimeStatus{}, "", "", err
	}
	hashes, err := d.compose(ctx, "config", "--hash", "p2pstream")
	if err != nil {
		return RuntimeStatus{}, "", "", err
	}
	fields := strings.Fields(string(hashes))
	if len(fields) != 2 || fields[1] != info.Config.Labels["com.docker.compose.config-hash"] {
		return RuntimeStatus{}, "", "", errors.New("Compose configuration changed; re-enroll the deployment on the host")
	}
	if !strings.HasPrefix(info.Config.Image, "ghcr.io/"+strings.ToLower(d.Config.Repository)+"@sha256:") {
		return RuntimeStatus{}, "", "", errors.New("installed image must be pinned to the enrolled registry digest")
	}
	// Reject unsupported concurrent writers while the current server is still
	// serving. Backup and restore repeat the check with no server exception.
	if err := d.requireDataAccess(ctx, info.ID); err != nil {
		return RuntimeStatus{}, "", "", err
	}
	status, err := d.runtime(ctx, "status")
	return status, info.Config.Image, hash(data), err
}

func (d *ComposeDriver) runtime(ctx context.Context, action string) (RuntimeStatus, error) {
	id, err := d.container(ctx)
	if err != nil || id == "" {
		return RuntimeStatus{}, errors.New("server container unavailable")
	}
	container, err := d.inspectContainer(ctx, id)
	if err != nil {
		return RuntimeStatus{}, err
	}
	out, err := d.command(ctx, "exec", id, "/app/p2pstream", "server-update-status", action)
	if err != nil {
		return RuntimeStatus{}, err
	}
	var status RuntimeStatus
	if err := decode(out, &status); err != nil {
		return RuntimeStatus{}, err
	}
	// Running application code is not the authority for the installed version.
	// Read immutable image labels from Docker, independently of the process and
	// of any labels overridden on its container by Compose.
	imageData, err := d.command(ctx, "image", "inspect", container.Image)
	if err != nil {
		return RuntimeStatus{}, err
	}
	var images []struct {
		Config struct{ Labels map[string]string }
	}
	if err := json.Unmarshal(imageData, &images); err != nil || len(images) != 1 {
		return RuntimeStatus{}, errors.New("invalid installed image metadata")
	}
	if err := d.validateRuntimeIdentity(status, images[0].Config.Labels); err != nil {
		return RuntimeStatus{}, err
	}
	return status, nil
}

func (d *ComposeDriver) validateRuntimeIdentity(status RuntimeStatus, labels map[string]string) error {
	version, commit := labels["org.opencontainers.image.version"], labels["org.opencontainers.image.revision"]
	if !validVersion(version, d.Config.Channel) || !commitPattern.MatchString(commit) ||
		!strings.EqualFold(labels["org.opencontainers.image.source"], "https://github.com/"+d.Config.Repository) ||
		status.Version != version || status.Commit != commit {
		return errors.New("server runtime identity differs from its immutable release image")
	}
	return nil
}

func (d *ComposeDriver) Pull(ctx context.Context, image string) error {
	prefix := "ghcr.io/" + strings.ToLower(d.Config.Repository) + "@sha256:"
	if !strings.HasPrefix(image, prefix) || !digestPattern.MatchString(strings.TrimPrefix(image, prefix)) {
		return errors.New("image outside pinned repository")
	}
	// Previously installed images remain usable during registry outages.
	if _, err := d.command(ctx, "image", "inspect", image); err == nil {
		return nil
	}
	_, err := d.command(ctx, "pull", "--quiet", image)
	return err
}

func (d *ComposeDriver) Gate(enabled bool) error {
	path := filepath.Join(d.Config.ControlDir, "maintenance")
	if enabled {
		return AtomicWrite(path, []byte("server update in progress\n"), 0644)
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return syncDirectory(d.Config.ControlDir)
}

func (d *ComposeDriver) Prepare(ctx context.Context) (RuntimeStatus, error) {
	return d.runtime(ctx, "prepare")
}

func (d *ComposeDriver) Stop(ctx context.Context) error {
	id, err := d.container(ctx)
	if err != nil {
		return err
	}
	if id == "" {
		return nil
	}
	if _, err := d.inspectContainer(ctx, id); err != nil {
		return err
	}
	if _, err := d.command(ctx, "update", "--restart=no", id); err != nil {
		return err
	}
	if _, err := d.command(ctx, "stop", "--time", "30", id); err != nil {
		return err
	}
	info, err := d.inspectContainer(ctx, id)
	if err != nil {
		return err
	}
	if info.State.Running {
		return errors.New("server did not stop")
	}
	return nil
}

func (d *ComposeDriver) requireExclusiveData(ctx context.Context) error {
	return d.requireDataAccess(ctx, "")
}

func (d *ComposeDriver) requireDataAccess(ctx context.Context, currentID string) error {
	ids, err := d.command(ctx, "ps", "--filter", "volume="+d.Config.DataVolume, "--format", "{{.ID}}")
	if err != nil {
		return err
	}
	for _, id := range strings.Fields(string(ids)) {
		out, err := d.command(ctx, "inspect", id)
		if err != nil {
			return err
		}
		var info []containerInfo
		if err := json.Unmarshal(out, &info); err != nil || len(info) != 1 {
			return errors.New("invalid data-volume container inspection")
		}
		if !d.dataAccessAllowed(info[0], currentID) {
			return errors.New("another running container can write the server data volume")
		}
	}
	return nil
}

func (d *ComposeDriver) dataAccessAllowed(info containerInfo, currentID string) bool {
	for _, mount := range info.Mounts {
		if mount.Name != d.Config.DataVolume || !mount.RW {
			continue
		}
		// The executor is the sole writer during snapshots/restoration. A
		// preflight may additionally permit the currently enrolled server.
		if info.Config.Labels["p2pstream.server-update.executor"] == d.Config.InstanceID {
			continue
		}
		if currentID != "" && info.ID == currentID &&
			info.Config.Labels["com.docker.compose.project"] == d.Config.Project &&
			info.Config.Labels["com.docker.compose.service"] == "p2pstream" &&
			info.Config.Labels["p2pstream.server-update.instance"] == d.Config.InstanceID {
			continue
		}
		return false
	}
	return true
}

func (d *ComposeDriver) Backup(ctx context.Context, id string) (string, error) {
	if err := d.requireExclusiveData(ctx); err != nil {
		return "", err
	}
	if err := checkDatabase(ctx, filepath.Join(d.Config.DataDir, "p2pstream.db")); err != nil {
		return "", err
	}
	digest, err := backupData(ctx, d.Config.DataDir, filepath.Join(d.Config.StateDir, "backup-"+id+".tar"))
	if err == nil {
		err = pruneBackups(d.Config.StateDir, id)
	}
	return digest, err
}
func (d *ComposeDriver) Restore(ctx context.Context, id, digest string) error {
	if err := d.requireExclusiveData(ctx); err != nil {
		return err
	}
	if err := restoreData(ctx, d.Config.DataDir, filepath.Join(d.Config.StateDir, "backup-"+id+".tar"), digest); err != nil {
		return err
	}
	return checkDatabase(ctx, filepath.Join(d.Config.DataDir, "p2pstream.db"))
}

func (d *ComposeDriver) Deploy(ctx context.Context, image string) error {
	if !strings.HasPrefix(image, "ghcr.io/"+strings.ToLower(d.Config.Repository)+"@sha256:") {
		return errors.New("invalid deployment image")
	}
	data, err := readProtected(d.Config.DeploymentFile, 2<<20)
	if err != nil {
		return err
	}
	var doc map[string]any
	if err := json.Unmarshal(data, &doc); err != nil {
		return err
	}
	services, ok := doc["services"].(map[string]any)
	if !ok {
		return errors.New("invalid deployment services")
	}
	service, ok := services["p2pstream"].(map[string]any)
	if !ok {
		return errors.New("managed service missing")
	}
	service["image"] = image
	data, err = json.Marshal(doc)
	if err != nil {
		return err
	}
	if err := AtomicWrite(d.Config.DeploymentFile, data, 0600); err != nil {
		return err
	}
	// Create stopped, then suppress daemon restart before the first start.
	// Otherwise a host reboot can restart a candidate during data restoration.
	if _, err := d.compose(ctx, "up", "--no-start", "--no-deps", "--no-build", "--pull", "never", "p2pstream"); err != nil {
		return err
	}
	id, err := d.container(ctx)
	if err != nil || id == "" {
		return errors.New("replacement container missing")
	}
	if _, err := d.inspectContainer(ctx, id); err != nil {
		return err
	}
	if _, err := d.command(ctx, "update", "--restart=no", id); err != nil {
		return err
	}
	_, err = d.command(ctx, "start", id)
	return err
}

func (d *ComposeDriver) Healthy(ctx context.Context, release Release, baseline RuntimeStatus) error {
	ctx, cancel := context.WithTimeout(ctx, time.Duration(d.Config.HealthTimeoutSeconds)*time.Second)
	defer cancel()
	var since time.Time
	for {
		current, image, _, err := d.Inspect(ctx)
		ok := err == nil && image == release.Image && current.Ready && len(current.Blocked) == 0 && current.InstanceID == baseline.InstanceID && current.Version == release.Version && current.Commit == release.Commit && current.Schema == release.Metadata.TargetSchema && current.Maintenance
		for _, id := range baseline.Agents {
			ok = ok && slices.Contains(current.Agents, id)
		}
		for _, id := range baseline.Listeners {
			ok = ok && slices.Contains(current.Listeners, id)
		}
		if ok {
			if since.IsZero() {
				since = time.Now()
			}
			if time.Since(since) >= time.Duration(d.Config.HealthyDwellSeconds)*time.Second {
				return nil
			}
		} else {
			since = time.Time{}
		}
		select {
		case <-ctx.Done():
			return errors.New("server did not pass readiness and reconnection checks before the deadline")
		case <-time.After(time.Second):
		}
	}
}

func (d *ComposeDriver) Commit(ctx context.Context) error {
	id, err := d.container(ctx)
	if err != nil || id == "" {
		return errors.New("server container missing at commit")
	}
	if _, err := d.inspectContainer(ctx, id); err != nil {
		return err
	}
	_, err = d.command(ctx, "update", "--restart="+d.Config.Restart, id)
	return err
}
