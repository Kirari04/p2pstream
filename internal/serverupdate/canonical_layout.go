package serverupdate

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/google/uuid"
)

const DeploymentLayoutSingleFileV1 = "single-file-v1"

// StagedDeploymentFile is the private enrollment candidate. Enrollment never
// publishes it: the host must first durably journal its transaction, block
// runtime admission, and stop the old deployment, then atomically publish these
// bytes to ComposeConfig.DeploymentFile with mode 0640. It must not rename this
// 0600 file across directories or bind-mount its inode into the executor.
func StagedDeploymentFile(stateDirectory string) string {
	return filepath.Join(stateDirectory, "staged-compose.yaml")
}

// EnrollSingleFileInstallation explicitly selects the single-file-v1 layout.
// Both directories must already exist, be root-owned and protected, and have
// root-owned, non-writable ancestors. The production host chooses its fixed
// deployment directory (/opt/p2pstream/deployment); these are trusted local
// installation parameters, never management API inputs. Editable inputs belong
// outside the executor mounts (normally /opt/p2pstream/compose.inputs.yaml).
// This function prepares private state only; the host owns initial publication.
func EnrollSingleFileInstallation(directory, deploymentDirectory string, model []byte, image, updaterImage, version, repository string, bootstrap Floor, installationID string) error {
	if _, err := uuid.Parse(installationID); err != nil {
		return err
	}
	return enrollDeploymentLayout(directory, deploymentDirectory, model, image, updaterImage, version, repository, bootstrap, installationID, DeploymentLayoutSingleFileV1, os.Lstat)
}

// BlockStagedRuntime is the host-only initial enrollment block. The caller must
// durably journal the transaction first and supply its verified installation and
// published image digest. Only the fixed private candidate is read; the canonical
// model is never published or used to derive the initial owner. Existing owners
// retain their generation and release identity on replay. This does not take the
// writer lease, stop the managed server, or allow admission.
func (d *ComposeDriver) BlockStagedRuntime(ctx context.Context, expectedInstallationID, expectedImage string) error {
	return d.blockStagedRuntime(ctx, expectedInstallationID, expectedImage, os.Lstat, d.guardedImage)
}

func (d *ComposeDriver) blockStagedRuntime(ctx context.Context, expectedInstallationID, expectedImage string, lstat layoutLstat, guardedImage func(context.Context, string) (DeploymentCapabilities, error)) error {
	if err := d.Config.validate(lstat); err != nil {
		return err
	}
	if d.Config.DeploymentProtocol != 2 || d.Config.DeploymentLayout != DeploymentLayoutSingleFileV1 {
		return errors.New("staged runtime block requires protocol 2 single-file-v1")
	}
	instance, err := uuid.Parse(expectedInstallationID)
	if err != nil || instance.String() != expectedInstallationID || d.Config.InstanceID != expectedInstallationID {
		return errors.New("staged runtime block installation identity does not match")
	}
	data, err := readStagedDeployment(d.Config.StateDir)
	if err != nil {
		return err
	}
	return d.blockPrivateRuntime(ctx, data, expectedInstallationID, expectedImage, "staged", guardedImage)
}

// BlockRollbackRuntime blocks admission from the exact private rollback snapshot
// durably journaled by the host. A completed detach may have removed the owner
// and left a standalone canonical model, so neither canonical nor enrollment-era
// staged bytes are authority here. This only blocks: it never takes the writer
// lease, publishes a model, stops a server or allows a generation.
func (d *ComposeDriver) BlockRollbackRuntime(ctx context.Context, expectedInstallationID, expectedImage, expectedSnapshotSHA256 string) error {
	return d.blockRollbackRuntime(ctx, expectedInstallationID, expectedImage, expectedSnapshotSHA256, os.Lstat, d.guardedImage)
}

func (d *ComposeDriver) blockRollbackRuntime(ctx context.Context, expectedInstallationID, expectedImage, expectedSnapshotSHA256 string, lstat layoutLstat, guardedImage func(context.Context, string) (DeploymentCapabilities, error)) error {
	if err := d.Config.validate(lstat); err != nil {
		return err
	}
	if d.Config.DeploymentProtocol != 2 || d.Config.DeploymentLayout != DeploymentLayoutSingleFileV1 {
		return errors.New("rollback runtime block requires protocol 2 single-file-v1")
	}
	instance, err := uuid.Parse(expectedInstallationID)
	if err != nil || instance == uuid.Nil || instance.String() != expectedInstallationID || d.Config.InstanceID != expectedInstallationID {
		return errors.New("rollback runtime block installation identity does not match")
	}
	if !digestPattern.MatchString(expectedSnapshotSHA256) {
		return errors.New("rollback runtime block snapshot hash is invalid")
	}
	path := filepath.Join(d.Config.StateDir, "rollback-compose.json")
	// Unlike the initial staged leaf, this transaction-local leaf is not part
	// of Config.validate. Check root ownership here without extending unrelated
	// healthy configuration validation to historical rollback snapshots.
	if err := protectedLayoutPath(path, false, false, 0077, lstat); err != nil {
		return err
	}
	data, err := readPrivateDeployment(path, "rollback")
	if err != nil {
		return err
	}
	if fmt.Sprintf("%x", sha256.Sum256(data)) != expectedSnapshotSHA256 {
		return errors.New("rollback runtime block snapshot differs from the recorded transaction")
	}
	return d.blockPrivateRuntime(ctx, data, expectedInstallationID, expectedImage, "rollback", guardedImage)
}

func (d *ComposeDriver) blockPrivateRuntime(ctx context.Context, data []byte, expectedInstallationID, expectedImage, kind string, guardedImage func(context.Context, string) (DeploymentCapabilities, error)) error {
	owner, image, err := d.parseRuntimeBinding(data)
	if err != nil {
		return err
	}
	if image != expectedImage {
		return fmt.Errorf("%s runtime block image differs from the verified published image", kind)
	}
	old, err := ReadRuntimeOwner(d.Config.DataDir)
	if err != nil {
		return err
	}
	if old != nil {
		if old.InstanceID != expectedInstallationID {
			return errors.New("data volume belongs to another deployment")
		}
		// The private binding and exact image are checked even on replay. Do not
		// probe again or replace an existing owner's generation/release identity.
		owner = *old
	} else {
		capability, err := guardedImage(ctx, image)
		if err != nil {
			return err
		}
		owner.Version, owner.Commit = capability.Version, capability.Commit
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	owner.Allowed = false
	return WriteRuntimeOwner(d.Config.DataDir, owner)
}

// Ancestors and root ownership are checked by Config.validate immediately before
// this read. Open the fixed leaf without following links, reject special files,
// and bound the actual read as well as the initial size check.
func readStagedDeployment(stateDirectory string) ([]byte, error) {
	return readPrivateDeployment(StagedDeploymentFile(stateDirectory), "staged")
}

func readPrivateDeployment(path, kind string) ([]byte, error) {
	file, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	const limit = 2 << 20
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !info.Mode().IsRegular() || info.Mode().Perm() != 0600 || info.Size() > limit || !ok || stat.Nlink != 1 {
		return nil, fmt.Errorf("unsafe private %s deployment file", kind)
	}
	data, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil {
		return nil, err
	}
	if len(data) > limit {
		return nil, fmt.Errorf("private %s deployment file exceeds limit", kind)
	}
	return data, nil
}

// Tests may substitute metadata traversal without changing process privileges.
// Every public entry point always uses os.Lstat; no caller-controlled bypass is
// stored in configuration or exposed over the management API.
type layoutLstat func(string) (os.FileInfo, error)

func layoutContains(parent, child string) bool {
	return parent == child || strings.HasPrefix(child, parent+string(filepath.Separator))
}

func validateSingleFilePaths(c ComposeConfig, lstat layoutLstat) error {
	deployment := filepath.Dir(c.DeploymentFile)
	if c.DeploymentProtocol != 2 || filepath.Base(c.DeploymentFile) != "compose.yaml" || c.ControlDir != filepath.Join(c.StateDir, "control") {
		return errors.New("single-file-v1 requires protocol 2, compose.yaml and the private control directory")
	}
	// DataDir is the executor's named-volume mount, not a host bind path. Its
	// contents may be application-owned. Its ancestors must still be protected,
	// and the mount must be an ordinary directory when present. The existing
	// runtime guard validates the volume and writer ownership.
	trees := []string{c.StateDir, c.DataDir, deployment}
	for i, path := range trees {
		if !filepath.IsAbs(path) || filepath.Clean(path) != path || path == "/" {
			return errors.New("single-file-v1 directories must be clean absolute non-root paths")
		}
		for _, other := range trees[:i] {
			if layoutContains(path, other) || layoutContains(other, path) {
				return errors.New("single-file-v1 private, data and deployment trees must be disjoint")
			}
		}
	}
	if err := protectedLayoutPath(filepath.Dir(c.DataDir), true, false, 0022, lstat); err != nil {
		return err
	}
	if info, err := lstat(c.DataDir); err != nil {
		// During host enrollment the executor's named volume is not mounted.
		if !errors.Is(err, os.ErrNotExist) {
			return err
		}
	} else if !info.IsDir() {
		return errors.New("single-file-v1 data mount must be an ordinary directory")
	}
	if err := protectedLayoutPath(c.StateDir, true, false, 0077, lstat); err != nil {
		return err
	}
	if err := protectedLayoutPath(deployment, true, false, 0027, lstat); err != nil {
		return err
	}
	if err := protectedLayoutPath(c.ControlDir, true, true, 0022, lstat); err != nil {
		return err
	}
	for _, path := range []string{c.DeploymentFile, filepath.Join(c.StateDir, "config.json"), StagedDeploymentFile(c.StateDir), filepath.Join(c.StateDir, "original-compose.json")} {
		if err := protectedLayoutPath(path, false, true, 0027, lstat); err != nil {
			return err
		}
	}
	// Token is intentionally readable through the server's read-only control
	// mount, but every enclosing host directory is still protected.
	if _, err := lstat(c.ControlDir); err == nil {
		return protectedLayoutPath(filepath.Join(c.ControlDir, "token"), false, true, 0022, lstat)
	}
	return nil
}

// Check every component without following symlinks. Ordinary directories owned
// by root and not writable by other users cannot be swapped by those users
// between this check and a subsequent read/atomic rename. Do not repair modes:
// accepting an unsafe ancestor and chmodding it would mask a prior takeover.
func protectedLayoutPath(path string, directory, optional bool, forbidden os.FileMode, lstat layoutLstat) error {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return errors.New("single-file-v1 paths must be clean and absolute")
	}
	parts := strings.Split(strings.TrimPrefix(path, "/"), "/")
	current := "/"
	for i := -1; i < len(parts); i++ {
		if i >= 0 {
			current = filepath.Join(current, parts[i])
		}
		leaf := i == len(parts)-1
		info, err := lstat(current)
		if err != nil {
			if leaf && optional && errors.Is(err, os.ErrNotExist) {
				return nil
			}
			return fmt.Errorf("unsafe single-file-v1 path %q: %w", current, err)
		}
		stat, ok := info.Sys().(*syscall.Stat_t)
		mask := os.FileMode(0022)
		if leaf {
			mask = forbidden
		}
		ordinary := info.IsDir()
		if leaf && !directory {
			ordinary = info.Mode().IsRegular()
		}
		if !ordinary || !ok || stat.Uid != 0 || info.Mode().Perm()&mask != 0 {
			return fmt.Errorf("unsafe single-file-v1 path %q: require ordinary root-owned path with protected permissions", current)
		}
	}
	return nil
}

func validateSingleFileModel(doc map[string]any) error {
	if doc["include"] != nil {
		return errors.New("single-file-v1 requires a fully resolved model without include")
	}
	services, _ := doc["services"].(map[string]any)
	for _, value := range services {
		service, _ := value.(map[string]any)
		if service["extends"] != nil {
			return errors.New("single-file-v1 requires a fully resolved model without extends")
		}
	}
	return nil
}

func marshalDeploymentModel(c ComposeConfig, doc map[string]any) ([]byte, error) {
	if c.DeploymentLayout == DeploymentLayoutSingleFileV1 {
		if err := validateSingleFileModel(doc); err != nil {
			return nil, err
		}
		data, err := json.MarshalIndent(doc, "", "  ")
		if err != nil {
			return nil, err
		}
		// JSON is valid YAML. Preserve Compose's already-escaped dollar strings;
		// serialization changes whitespace only, not the native service model.
		return append(data, '\n'), nil
	}
	return json.Marshal(doc)
}
