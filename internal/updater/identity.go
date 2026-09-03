package updater

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/user"
	"path"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"

	"golang.org/x/mod/semver"
	"golang.org/x/sys/unix"

	"p2pstream/internal/agentupdate"
	"p2pstream/internal/agentupdateauth"
	"p2pstream/internal/buildinfo"
)

const bootstrapRootMinLifetime = 24*time.Hour + 5*time.Minute

type PublicIdentities struct {
	WorkerPublicKey    string `json:"worker_public_key"`
	ActivatorPublicKey string `json:"activator_public_key"`
}

type BootstrapOptions struct {
	Paths              Paths
	UpdaterUser        string
	Config             HostConfig
	EnrollmentToken    string
	TrustedRootJSON    []byte
	AuthorityPublicKey []byte
	AuthorityKeyID     string
	AuthorityEpoch     uint64
	CurrentVersion     string
	// ExistingTunnelVersion/Commit identify the binary already serving tunnel
	// traffic when an unmanaged installation is enrolled. They let the new
	// pinned rescue runner bootstrap rollback metadata without replacing that
	// running tunnel binary.
	ExistingTunnelVersion string
	ExistingTunnelCommit  string
	Reenroll              bool
}

func (p Paths) workerStateDir() string { return filepath.Join(p.StateDir, "worker") }
func (p Paths) workerPrivateKeyPath() string {
	return filepath.Join(p.workerStateDir(), "identity.key")
}
func (p Paths) workerPublicKeyPath() string { return filepath.Join(p.workerStateDir(), "identity.pub") }
func (p Paths) activatorPrivateKeyPath() string {
	return filepath.Join(p.rootStateDir(), "activation.key")
}
func (p Paths) activatorPublicKeyPath() string {
	return filepath.Join(filepath.Dir(p.ConfigPath), "activator.pub")
}

// BootstrapHost creates local identities and fixed storage roots, and pins the
// explicitly supplied out-of-band trust root. It does not enroll, enable a
// timer, or consume the tunnel agent token.

func BootstrapHost(options BootstrapOptions) (PublicIdentities, error) {
	paths := options.Paths
	if err := paths.validate(); err != nil {
		return PublicIdentities{}, err
	}
	if os.Geteuid() != 0 {
		return PublicIdentities{}, errors.New("updater host bootstrap must run as root")
	}
	if options.UpdaterUser != DefaultUpdaterUser {
		return PublicIdentities{}, errors.New("refusing nonstandard updater user")
	}
	if err := options.Config.Validate(); err != nil {
		return PublicIdentities{}, err
	}
	if options.EnrollmentToken == "" || len(options.EnrollmentToken) > 4096 || strings.ContainsAny(options.EnrollmentToken, "\r\n") {
		return PublicIdentities{}, errors.New("single-use updater enrollment token is missing or invalid")
	}
	trustedRoot, err := agentupdate.ParseRoot(options.TrustedRootJSON)
	if err != nil {
		return PublicIdentities{}, fmt.Errorf("validate out-of-band update root: %w", err)
	}
	if err := requireBootstrapRootLifetime(trustedRoot.ExpiresAt, time.Now().UTC()); err != nil {
		return PublicIdentities{}, err
	}
	if !validVersionForChannel(options.CurrentVersion, options.Config.Channel) {
		return PublicIdentities{}, fmt.Errorf("managed updater current version must match the %s release channel", options.Config.Channel)
	}
	if options.CurrentVersion != buildinfo.Version {
		return PublicIdentities{}, errors.New("managed updater current version does not match the executing agent binary")
	}
	if (options.ExistingTunnelVersion == "") != (options.ExistingTunnelCommit == "") {
		return PublicIdentities{}, errors.New("existing tunnel build version and commit must be supplied together")
	}
	if options.ExistingTunnelVersion != "" && (!validVersion(options.ExistingTunnelVersion) || !commitPattern.MatchString(options.ExistingTunnelCommit)) {
		return PublicIdentities{}, errors.New("existing tunnel build identity is invalid")
	}
	account, err := user.Lookup(options.UpdaterUser)
	if err != nil {
		return PublicIdentities{}, fmt.Errorf("look up updater user: %w", err)
	}
	uid, err := parseAccountID(account.Uid)
	if err != nil {
		return PublicIdentities{}, fmt.Errorf("parse updater user ID: %w", err)
	}
	gid, err := parseAccountID(account.Gid)
	if err != nil {
		return PublicIdentities{}, fmt.Errorf("parse updater group ID: %w", err)
	}
	for _, directory := range []struct {
		path         string
		mode         os.FileMode
		owner, group int
	}{
		{paths.StateDir, 0750, 0, gid},
		{filepath.Dir(paths.ConfigPath), 0750, 0, gid},
		{paths.workerStateDir(), 0700, uid, gid},
		{paths.stagingDir(), 0700, uid, gid},
		{paths.rootStateDir(), 0700, 0, 0},
		{paths.InstallRoot, 0755, 0, 0},
		{paths.slotsDir(), 0755, 0, 0},
	} {
		if err := ensureDirectory(directory.path, directory.mode, directory.owner, directory.group); err != nil {
			return PublicIdentities{}, err
		}
	}
	// Validate immutable trust/config pins before mutating enrollment tokens or
	// identities, so a rejected re-bootstrap leaves the existing host intact.
	if options.Reenroll {
		if _, err := readRegularNoFollow(paths.enrolledPath(), 64<<10); err != nil {
			return PublicIdentities{}, fmt.Errorf("managed updater re-enrollment requires an existing finalized enrollment: %w", err)
		}
	}
	initialVersionFloor := bootstrapVersionFloor(options.CurrentVersion, options.ExistingTunnelVersion)
	if err := pinBootstrapState(paths, options.TrustedRootJSON, trustedRoot.Version, initialVersionFloor, !options.Reenroll, 0, gid); err != nil {
		return PublicIdentities{}, err
	}
	if err := pinHostConfig(paths.ConfigPath, options.Config, 0, gid); err != nil {
		return PublicIdentities{}, err
	}
	if err := pinManagementAuthority(paths, options.AuthorityPublicKey, options.AuthorityKeyID, options.AuthorityEpoch, 0, gid); err != nil {
		return PublicIdentities{}, err
	}
	if !options.Reenroll {
		buildVersion, buildCommit := buildinfo.Version, buildinfo.Commit
		if options.ExistingTunnelVersion != "" || options.ExistingTunnelCommit != "" {
			buildVersion, buildCommit = options.ExistingTunnelVersion, options.ExistingTunnelCommit
		}
		if err := pinBootstrapSlotMetadata(paths, buildVersion, buildCommit); err != nil {
			return PublicIdentities{}, err
		}
	}
	worker, err := ensureIdentity(paths.workerPrivateKeyPath(), paths.workerPublicKeyPath(), uid, gid)
	if err != nil {
		return PublicIdentities{}, err
	}
	activator, err := ensureIdentity(paths.activatorPrivateKeyPath(), paths.activatorPublicKeyPath(), 0, 0)
	if err != nil {
		return PublicIdentities{}, err
	}
	tokenPath := filepath.Join(filepath.Dir(paths.ConfigPath), "enrollment.token")
	if err := atomicWrite(tokenPath, []byte(options.EnrollmentToken+"\n"), 0640); err != nil {
		return PublicIdentities{}, err
	}
	if err := os.Chown(tokenPath, 0, gid); err != nil {
		return PublicIdentities{}, err
	}
	return PublicIdentities{WorkerPublicKey: worker, ActivatorPublicKey: activator}, nil
}

func parseAccountID(value string) (int, error) {
	id, err := strconv.Atoi(value)
	if err != nil {
		return 0, err
	}
	if id < 0 || uint64(id) > uint64(^uint32(0)) {
		return 0, errors.New("account ID is outside the supported unsigned 32-bit range")
	}
	return id, nil
}

func bootstrapVersionFloor(rescueVersion, existingTunnelVersion string) string {
	if existingTunnelVersion != "" && semver.Compare(existingTunnelVersion, rescueVersion) > 0 {
		return existingTunnelVersion
	}
	return rescueVersion
}

func pinBootstrapSlotMetadata(paths Paths, buildVersion, buildCommit string) error {
	target, err := currentTarget(paths)
	if err != nil {
		return err
	}
	version := path.Base(path.Dir(target))
	if !bootstrapVersionPattern.MatchString(version) {
		return errors.New("initial managed updater slot must use its bootstrap content identity")
	}
	binaryPath := filepath.Join(paths.InstallRoot, filepath.FromSlash(target))
	file, err := openRegularNoFollow(binaryPath, defaultMaxArtifact)
	if err != nil {
		return err
	}
	h := sha256.New()
	size, copyErr := io.Copy(h, io.LimitReader(file, defaultMaxArtifact+1))
	closeErr := file.Close()
	if copyErr != nil {
		return copyErr
	}
	if closeErr != nil {
		return closeErr
	}
	if size <= 0 || size > defaultMaxArtifact {
		return errors.New("bootstrap agent binary size is invalid")
	}
	digestText := hex.EncodeToString(h.Sum(nil))
	if version != "bootstrap-"+digestText[:16] {
		return errors.New("bootstrap slot content identity does not match the installed agent binary")
	}
	slot := slotMetadata{
		Target: target, ResultKind: agentupdateauth.RootActionResultBootstrap, Version: version,
		BuildVersion: buildVersion, BuildCommit: buildCommit,
		OS: runtime.GOOS, Arch: runtime.GOARCH, ArtifactName: "p2pstream_bootstrap_" + runtime.GOOS + "_" + runtime.GOARCH,
		ArtifactSize: size, ArtifactSHA256: digestText,
	}
	if err := validateSlotMetadata(slot); err != nil {
		return err
	}
	data, err := readRegularNoFollow(paths.currentSlotMetadataPath(), 64<<10)
	switch {
	case err == nil:
		var existing slotMetadata
		if err := strictJSON(data, &existing); err != nil {
			return err
		}
		if existing != slot {
			return errors.New("refusing to replace existing managed current-slot metadata during bootstrap")
		}
		return requireProtectedRegularFile(paths.currentSlotMetadataPath(), 0600, 0, 0)
	case errors.Is(err, os.ErrNotExist):
		return atomicJSON(paths.currentSlotMetadataPath(), slot, 0600)
	default:
		return err
	}
}

func requireBootstrapRootLifetime(expiresText string, now time.Time) error {
	expiresAt, err := time.Parse(time.RFC3339, expiresText)
	if err != nil || expiresAt.Before(now.Add(bootstrapRootMinLifetime)) {
		return errors.New("out-of-band update root expires too soon for safe enrollment")
	}
	return nil
}

// pinBootstrapState permits first-use pinning but never implicit root
// replacement. Root rotation requires the separately threshold-authorized
// rotation path. Re-bootstrap may advance the installed version floor, but it
// cannot reset sequence, epoch, minimum-safe-version, or root floors.
func pinBootstrapState(paths Paths, trustedRootJSON []byte, rootVersion uint64, currentVersion string, advanceVersionFloor bool, owner, group int) error {
	existingRoot, err := readRegularNoFollow(paths.TrustPath, defaultMaxMetadata)
	switch {
	case err == nil:
		if !bytes.Equal(existingRoot, trustedRootJSON) {
			return errors.New("refusing to replace the pinned updater trust root during bootstrap")
		}
		if err := requireProtectedRegularFile(paths.TrustPath, 0640, owner, group); err != nil {
			return fmt.Errorf("pinned updater trust root is not protected: %w", err)
		}
	case errors.Is(err, os.ErrNotExist):
		if err := atomicWrite(paths.TrustPath, trustedRootJSON, 0640); err != nil {
			return err
		}
		if err := os.Chown(paths.TrustPath, owner, group); err != nil {
			return err
		}
	default:
		return err
	}
	floor, err := loadFloor(paths.floorPath())
	if err != nil {
		return err
	}
	if floor.RootVersion > rootVersion {
		return errors.New("bootstrap trust root is below the persisted root version floor")
	}
	if floor.RootVersion < rootVersion {
		floor.RootVersion = rootVersion
	}
	if advanceVersionFloor && (floor.Version == "" || semver.Compare(currentVersion, floor.Version) > 0) {
		floor.Version = currentVersion
	}
	if err := atomicJSON(paths.floorPath(), floor, 0640); err != nil {
		return err
	}
	return os.Chown(paths.floorPath(), owner, group)
}

func pinHostConfig(path string, config HostConfig, owner, group int) error {
	existing, err := LoadHostConfig(path)
	switch {
	case err == nil:
		if existing != config {
			return errors.New("refusing to replace pinned updater repository, management origin, or agent identity during bootstrap")
		}
		return requireProtectedRegularFile(path, 0640, owner, group)
	case errors.Is(err, os.ErrNotExist):
		if err := atomicJSON(path, config, 0640); err != nil {
			return err
		}
		return os.Chown(path, owner, group)
	default:
		return err
	}
}

func requireProtectedRegularFile(path string, mode os.FileMode, owner, group int) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || !info.Mode().IsRegular() || info.Mode().Perm() != mode.Perm() ||
		!accountIDMatches(stat.Uid, owner) || !accountIDMatches(stat.Gid, group) {
		return errors.New("unsafe file type, ownership, or permissions")
	}
	return nil
}

func ensureDirectory(path string, mode os.FileMode, uid, gid int) error {
	if err := os.MkdirAll(path, mode); err != nil {
		return err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("updater path %s is not a directory", path)
	}
	if err := os.Chown(path, uid, gid); err != nil {
		return err
	}
	return os.Chmod(path, mode)
}

func ensureIdentity(privatePath, publicPath string, uid, gid int) (string, error) {
	private, err := loadPrivateIdentity(privatePath, uid, gid)
	if errors.Is(err, os.ErrNotExist) {
		_, generated, generateErr := ed25519.GenerateKey(rand.Reader)
		if generateErr != nil {
			return "", generateErr
		}
		encoded, encodeErr := agentupdate.EncodePrivateKey(generated)
		if encodeErr != nil {
			return "", encodeErr
		}
		if writeErr := createExclusiveSynced(privatePath, []byte(encoded+"\n"), 0600, uid, gid); writeErr != nil {
			if !errors.Is(writeErr, os.ErrExist) {
				return "", writeErr
			}
			private, err = loadPrivateIdentity(privatePath, uid, gid)
		} else {
			private = generated
			err = nil
		}
	}
	if err != nil {
		return "", err
	}
	public := private.Public().(ed25519.PublicKey)
	encoded, err := agentupdate.EncodePublicKey(public)
	if err != nil {
		return "", err
	}
	if err := atomicWrite(publicPath, []byte(encoded+"\n"), 0644); err != nil {
		return "", err
	}
	if err := os.Chown(publicPath, uid, gid); err != nil {
		return "", err
	}
	return encoded, nil
}

func loadPrivateIdentity(path string, uid, gid int) (ed25519.PrivateKey, error) {
	f, err := openRegularNoFollow(path, 128)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || !accountIDMatches(stat.Uid, uid) || !accountIDMatches(stat.Gid, gid) || info.Mode().Perm() != 0600 {
		return nil, errors.New("updater private key has unsafe owner or permissions")
	}
	data, err := readBounded(f, 128)
	if err != nil {
		return nil, err
	}
	key, err := agentupdate.ParsePrivateKey(strings.TrimSuffix(string(data), "\n"))
	if err != nil {
		return nil, fmt.Errorf("parse updater private key: %w", err)
	}
	public := key.Public().(ed25519.PublicKey)
	if !key.Equal(ed25519.NewKeyFromSeed(key.Seed())) || len(public) != ed25519.PublicKeySize {
		return nil, errors.New("updater private key is invalid")
	}
	return key, nil
}

func accountIDMatches(actual uint32, expected int) bool {
	return expected >= 0 && uint64(actual) == uint64(expected)
}

func createExclusiveSynced(path string, data []byte, mode os.FileMode, uid, gid int) error {
	fd, err := unix.Open(path, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, uint32(mode.Perm()))
	if err != nil {
		return err
	}
	f := os.NewFile(uintptr(fd), path)
	if f == nil {
		_ = unix.Close(fd)
		return errors.New("could not wrap updater identity file")
	}
	ok := false
	defer func() {
		_ = f.Close()
		if !ok {
			_ = os.Remove(path)
		}
	}()
	if err := f.Chown(uid, gid); err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		return err
	}
	if err := f.Sync(); err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := syncDir(filepath.Dir(path)); err != nil {
		return err
	}
	ok = true
	return nil
}
