package serverupdate

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"
)

// Deployment protocol is independent of the existing private HTTP protocol.
// Legacy executors and servers continue to use their original wire messages;
// ownership migration is an explicit host transaction with both capabilities
// checked before it changes the saved deployment.
type DeploymentCapabilities struct {
	CanonicalCompose    int    `json:"canonical_compose,omitempty"`
	EnrollmentPreflight int    `json:"enrollment_preflight,omitempty"`
	RuntimeGuard        int    `json:"runtime_guard"`
	Version             string `json:"version"`
	Commit              string `json:"commit"`
}

func newRuntimeGeneration() string { return uuid.NewString() }

func (d *ComposeDriver) deploymentMode() os.FileMode {
	if d.Config.DeploymentProtocol == 2 {
		return 0640
	}
	return 0600
}

func (d *ComposeDriver) guardedImage(ctx context.Context, image string) (DeploymentCapabilities, error) {
	var capability DeploymentCapabilities
	out, err := d.command(ctx, "image", "inspect", image)
	if err != nil {
		return capability, err
	}
	var images []struct {
		ID     string `json:"Id"`
		Config struct{ Labels map[string]string }
	}
	if err := json.Unmarshal(out, &images); err != nil || len(images) != 1 || images[0].ID == "" {
		return capability, errors.New("invalid guarded image metadata")
	}
	labels := images[0].Config.Labels
	if !validVersion(labels["org.opencontainers.image.version"], d.Config.Channel) ||
		!commitPattern.MatchString(labels["org.opencontainers.image.revision"]) ||
		!strings.EqualFold(labels["org.opencontainers.image.source"], "https://github.com/"+d.Config.Repository) {
		return capability, errors.New("guarded image release identity is invalid")
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	// No application volume or host socket is exposed to this capability probe.
	// Probe the immutable local image ID, never a tag that could change mid-call.
	out, err = d.command(ctx, "run", "--rm", "--network", "none", "--read-only", "--cap-drop", "ALL", "--cap-add", "NET_BIND_SERVICE", "--security-opt", "no-new-privileges", "--entrypoint", "/app/p2pstream", images[0].ID, "server-deployment-capabilities")
	if err != nil {
		// A Docker failure or deadline does not establish missing image support.
		// command deliberately excludes arbitrary subprocess output from errors.
		if ctx.Err() != nil {
			return capability, fmt.Errorf("image deployment capability probe interrupted: %w", ctx.Err())
		}
		return capability, fmt.Errorf("image deployment capability probe failed: %w", err)
	}
	if err := decode(out, &capability); err != nil || capability.RuntimeGuard != 2 ||
		capability.Version != labels["org.opencontainers.image.version"] || capability.Commit != labels["org.opencontainers.image.revision"] {
		return capability, errors.New("image deployment capability differs from its immutable identity")
	}
	return capability, nil
}

func (d *ComposeDriver) modelRuntimeOwner(ctx context.Context) (RuntimeOwner, error) {
	data, err := readProtected(d.Config.DeploymentFile, 2<<20)
	if err != nil {
		return RuntimeOwner{}, err
	}
	owner, image, err := d.parseRuntimeBinding(data)
	if err != nil {
		return owner, err
	}
	capability, err := d.guardedImage(ctx, image)
	if err != nil {
		return RuntimeOwner{}, err
	}
	owner.Version, owner.Commit = capability.Version, capability.Commit
	return owner, nil
}

// parseRuntimeBinding checks model identity without probing images or reading
// deployment paths. Callers select the canonical or private staged authority.
func (d *ComposeDriver) parseRuntimeBinding(data []byte) (RuntimeOwner, string, error) {
	var owner RuntimeOwner
	var doc struct {
		Name     string `json:"name"`
		Services map[string]struct {
			Image       string            `json:"image"`
			Environment map[string]string `json:"environment"`
		} `json:"services"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		return owner, "", err
	}
	service, ok := doc.Services["p2pstream"]
	env := service.Environment
	if !ok || doc.Name != d.Config.Project || env["SERVER_UPDATE_INSTANCE_ID"] != d.Config.InstanceID ||
		env["SERVER_UPDATE_RUNTIME_PROTOCOL"] != "2" || env["SERVER_UPDATE_TOKEN"] != "" ||
		env["SERVER_UPDATE_TOKEN_FILE"] != "/run/p2pstream-server-update/token" ||
		env["SERVER_UPDATE_SOCKET"] != "/run/p2pstream-server-update/control.sock" ||
		env["SERVER_UPDATE_GATE_FILE"] != "/run/p2pstream-server-update/maintenance" {
		return owner, "", errors.New("canonical deployment runtime binding is invalid")
	}
	if _, err := uuid.Parse(env["SERVER_UPDATE_GENERATION"]); err != nil {
		return owner, "", errors.New("canonical deployment generation is invalid")
	}
	prefix := "ghcr.io/" + strings.ToLower(d.Config.Repository) + "@sha256:"
	if !strings.HasPrefix(service.Image, prefix) || !digestPattern.MatchString(strings.TrimPrefix(service.Image, prefix)) {
		return owner, "", errors.New("canonical deployment image is not a pinned release")
	}
	owner = RuntimeOwner{API: 2, InstanceID: d.Config.InstanceID, Generation: env["SERVER_UPDATE_GENERATION"]}
	return owner, service.Image, nil
}

// BlockRuntime is durable before stopping the existing writer. It deliberately
// does not take that writer's lease: already-running servers finish normally,
// while newly started containers must observe the blocked admission marker.
func (d *ComposeDriver) BlockRuntime(ctx context.Context) error {
	if d.Config.DeploymentProtocol != 2 {
		return nil
	}
	old, err := ReadRuntimeOwner(d.Config.DataDir)
	if err != nil {
		return err
	}
	if old != nil && old.InstanceID != d.Config.InstanceID {
		return errors.New("data volume belongs to another deployment")
	}
	if old != nil {
		// Blocking never authorizes a new generation. Reuse the durable owner
		// even if an interrupted host transaction already exported a detached
		// model or the next image is temporarily unavailable.
		old.Allowed = false
		return WriteRuntimeOwner(d.Config.DataDir, *old)
	}
	owner, err := d.modelRuntimeOwner(ctx)
	if err != nil {
		return err
	}
	return WriteRuntimeOwner(d.Config.DataDir, owner)
}

func (d *ComposeDriver) acquireOfflineLease(ctx context.Context) error {
	if d.Config.DeploymentProtocol != 2 || d.runtimeLease != nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	lease, err := AcquireRuntimeLease(ctx, d.Config.DataDir)
	if err != nil {
		return err
	}
	d.runtimeLease = lease
	if err := d.requireOfflineLease(); err != nil {
		d.releaseRuntimeLease()
		return err
	}
	return nil
}

func (d *ComposeDriver) releaseRuntimeLease() {
	if d.runtimeLease != nil {
		d.runtimeLease.Close()
		d.runtimeLease = nil
	}
}

func (d *ComposeDriver) requireOfflineLease() error {
	if d.Config.DeploymentProtocol != 2 {
		return nil
	}
	if d.runtimeLease == nil {
		return errors.New("offline data operation requires the runtime writer lease")
	}
	owner, err := ReadRuntimeOwner(d.Config.DataDir)
	if err != nil {
		return err
	}
	if owner == nil || owner.InstanceID != d.Config.InstanceID || owner.Allowed {
		return errors.New("offline data operation requires blocked deployment admission")
	}
	return nil
}

// AllowRuntime publishes one exact model generation under the writer lease.
// Containers created from an earlier model cannot wake and open the database.
func (d *ComposeDriver) AllowRuntime(ctx context.Context) error {
	if d.Config.DeploymentProtocol != 2 {
		return nil
	}
	if err := d.acquireRuntimeLease(ctx); err != nil {
		return err
	}
	defer d.releaseRuntimeLease()
	owner, err := d.modelRuntimeOwner(ctx)
	if err != nil {
		return err
	}
	owner.Allowed = true
	old, err := ReadRuntimeOwner(d.Config.DataDir)
	if err != nil {
		return err
	}
	if old == nil || old.InstanceID != d.Config.InstanceID || (old.Allowed && *old != owner) {
		return errors.New("runtime allowance requires a blocked owner or the exact already-allowed generation")
	}
	return WriteRuntimeOwner(d.Config.DataDir, owner)
}

func (d *ComposeDriver) acquireRuntimeLease(ctx context.Context) error {
	if d.runtimeLease != nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	lease, err := AcquireRuntimeLease(ctx, d.Config.DataDir)
	if err == nil {
		d.runtimeLease = lease
	}
	return err
}

// DetachRuntime is host-only and follows stopping the managed server. The
// stable lock inode remains, so subsequent standalone processes still exclude
// other writers even after managed deployment ownership has been removed.
func (d *ComposeDriver) DetachRuntime(ctx context.Context) error {
	if d.Config.DeploymentProtocol != 2 {
		return nil
	}
	if err := d.acquireRuntimeLease(ctx); err != nil {
		return err
	}
	defer d.releaseRuntimeLease()
	owner, err := ReadRuntimeOwner(d.Config.DataDir)
	if err != nil || owner == nil {
		return err
	}
	if owner.InstanceID != d.Config.InstanceID || owner.Allowed {
		return errors.New("runtime detach requires a blocked owner of this deployment")
	}
	if err := os.Remove(filepath.Join(d.Config.DataDir, RuntimeOwnerName)); err != nil {
		return err
	}
	return syncDirectory(d.Config.DataDir)
}

func (d *ComposeDriver) RecordCurrentRuntimeReceipt(ctx context.Context) error {
	if d.Config.DeploymentProtocol != 2 {
		return nil
	}
	data, err := readProtected(d.Config.DeploymentFile, 2<<20)
	if err != nil {
		return err
	}
	id, err := d.container(ctx)
	if err != nil || id == "" {
		return errors.New("managed server is missing for runtime receipt")
	}
	return d.RecordRuntimeReceipt(ctx, data, id)
}
