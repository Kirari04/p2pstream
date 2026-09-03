package updater

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"p2pstream/internal/agentupdateauth"
)

type ActivateOptions struct {
	Paths         Paths
	Verifier      Verifier
	Service       ServiceController
	Policy        VerifyPolicy
	TrustedRoot   []byte
	DiskPreflight func(string, int64) error
	// ReadyPath is an internal root-owned claimed command path. Direct callers
	// leave it empty to use the ordinary staging edge.
	ReadyPath string
}

type activationJournal struct {
	Phase              string                        `json:"phase"`
	PreviousTarget     string                        `json:"previous_target"`
	CandidateTarget    string                        `json:"candidate_target"`
	Version            string                        `json:"version"`
	Sequence           uint64                        `json:"sequence"`
	SecurityEpoch      uint64                        `json:"security_epoch"`
	MinimumSafeVersion string                        `json:"minimum_safe_version"`
	RootVersion        uint64                        `json:"root_version"`
	Authorization      assignmentAuthorizationRecord `json:"authorization"`
	AuthorizationSHA   string                        `json:"authorization_sha256"`
	PreviousSlot       slotMetadata                  `json:"previous_slot"`
	Receipt            *rootActionReceiptRecord      `json:"receipt,omitempty"`
}

const (
	journalPrepared = "prepared"
	journalSwitched = "switched"
	journalHealthy  = "healthy"
)

func Activate(ctx context.Context, options ActivateOptions) (Result, error) {
	if err := options.Paths.validate(); err != nil {
		return Result{}, err
	}
	if options.Verifier == nil || options.Service == nil {
		return Result{}, errors.New("updater verifier and service controller are required")
	}
	if options.Policy.Now.IsZero() {
		options.Policy.Now = time.Now().UTC()
	}
	if options.DiskPreflight == nil {
		options.DiskPreflight = diskPreflight
	}
	readyPath := options.ReadyPath
	if readyPath == "" {
		readyPath = options.Paths.readyPath()
	}
	if err := os.MkdirAll(options.Paths.rootStateDir(), 0700); err != nil {
		return Result{}, err
	}
	lock, err := acquireLock(options.Paths.lockPath())
	if err != nil {
		return Result{}, err
	}
	defer lock.Close()

	if err := recoverActivation(ctx, options); err != nil {
		return Result{}, fmt.Errorf("recover interrupted activation: %w", err)
	}
	if _, err := os.Lstat(readyPath); errors.Is(err, os.ErrNotExist) {
		floor, floorErr := loadFloor(options.Paths.floorPath())
		return Result{Version: floor.Version, Sequence: floor.Sequence, SecurityEpoch: floor.SecurityEpoch}, floorErr
	} else if err != nil {
		return Result{}, err
	}
	if len(options.TrustedRoot) == 0 {
		root, err := readRegularNoFollow(options.Paths.TrustPath, defaultMaxMetadata)
		if err != nil {
			return Result{}, fmt.Errorf("read updater trust root: %w", err)
		}
		options.TrustedRoot = root
	}
	floor, err := loadFloor(options.Paths.floorPath())
	if err != nil {
		return Result{}, err
	}
	applyFloor(&options.Policy, floor)

	readyData, err := readRegularNoFollow(readyPath, 64<<10)
	if err != nil {
		return Result{}, fmt.Errorf("read staged ready record: %w", err)
	}
	var ready readyRecord
	if err := strictJSON(readyData, &ready); err != nil {
		return Result{}, fmt.Errorf("parse staged ready record: %w", err)
	}
	if !validVersion(ready.ServerVersion) {
		return Result{}, errors.New("staged ready record has an invalid management server version")
	}
	options.Policy.ServerVersion = ready.ServerVersion
	manifest, err := readRegularNoFollow(filepath.Join(options.Paths.candidateDir(), "manifest.json"), defaultMaxMetadata)
	if err != nil {
		return Result{}, fmt.Errorf("read staged manifest: %w", err)
	}
	signatures, err := readRegularNoFollow(filepath.Join(options.Paths.candidateDir(), "manifest.signatures.json"), defaultMaxMetadata)
	if err != nil {
		return Result{}, fmt.Errorf("read staged signatures: %w", err)
	}
	release, err := options.Verifier.Verify(manifest, signatures, options.TrustedRoot, options.Policy)
	if err != nil {
		return Result{}, fmt.Errorf("independently verify staged metadata: %w", err)
	}
	if err := validateRelease(release); err != nil {
		return Result{}, err
	}
	if ready.Version != release.Version || ready.Commit != release.Commit || ready.ManifestSHA != release.ManifestSHA256 ||
		ready.RootVersion != release.RootVersion || ready.Sequence != release.Sequence ||
		ready.SecurityEpoch != release.SecurityEpoch || ready.ArtifactName != release.Artifact.Name ||
		ready.ArtifactSize != release.Artifact.Size || ready.ArtifactSHA != artifactHex(release.Artifact) {
		return Result{}, errors.New("staged ready record does not match verified metadata")
	}
	authorization := ready.Authorization.Authorization
	if ready.AgentPublicID != authorization.AgentPublicID || ready.AssignmentID != authorization.AssignmentID ||
		ready.Generation != authorization.Generation || ready.Nonce != base64.StdEncoding.EncodeToString(authorization.Nonce) {
		return Result{}, errors.New("staged ready record assignment context does not match its signed authorization")
	}
	if err := authorizationMatchesRelease(authorization, release, ready.ServerVersion); err != nil {
		return Result{}, err
	}
	if err := verifyAssignmentAuthorizationRecord(options.Paths, ready.Authorization, agentupdateauth.AssignmentActionActivate, time.Now().UTC(), 0); err != nil {
		return Result{}, err
	}
	if err := options.DiskPreflight(options.Paths.InstallRoot, release.Artifact.Size); err != nil {
		return Result{}, err
	}

	artifact, err := openRegularNoFollow(filepath.Join(options.Paths.candidateDir(), "artifact.bin"), release.Artifact.Size)
	if err != nil {
		return Result{}, fmt.Errorf("open staged artifact: %w", err)
	}
	defer artifact.Close()
	if err := options.Verifier.VerifyArtifact(artifact, release.Artifact); err != nil {
		return Result{}, fmt.Errorf("independently verify staged artifact: %w", err)
	}
	if _, err := artifact.Seek(0, io.SeekStart); err != nil {
		return Result{}, err
	}
	previous, err := currentTarget(options.Paths)
	if err != nil {
		return Result{}, err
	}
	previousSlot, err := loadCurrentSlotMetadata(options.Paths, previous)
	if err != nil {
		return Result{}, err
	}
	candidate := filepath.ToSlash(filepath.Join("slots", release.Version, "p2pstream"))
	authorizationDigest, err := agentupdateauth.AssignmentAuthorizationDigest(ready.Authorization.Authorization)
	if err != nil {
		return Result{}, err
	}
	authorizationSHA := hex.EncodeToString(authorizationDigest[:])
	journal := activationJournal{
		Phase: journalPrepared, PreviousTarget: previous, CandidateTarget: candidate,
		Version: release.Version, Sequence: release.Sequence, SecurityEpoch: release.SecurityEpoch,
		MinimumSafeVersion: release.MinimumSafeVersion, RootVersion: release.RootVersion,
		Authorization: ready.Authorization, AuthorizationSHA: authorizationSHA, PreviousSlot: previousSlot,
	}
	if err := writeJournal(options.Paths, journal); err != nil {
		return Result{}, err
	}
	consumedSHA, err := consumeAssignmentAuthorization(options.Paths, ready.Authorization, agentupdateauth.AssignmentActionActivate, time.Now().UTC())
	if err != nil {
		_ = removeAndSync(options.Paths.journalPath())
		return Result{}, err
	}
	if consumedSHA != authorizationSHA {
		return Result{}, errors.New("consumed management authorization digest changed")
	}
	slotPath, err := installSlot(options.Paths, release, artifact)
	if err != nil {
		return Result{}, err
	}
	if slotPath != filepath.Join(options.Paths.InstallRoot, filepath.FromSlash(candidate)) {
		return Result{}, errors.New("internal slot target mismatch")
	}
	if err := switchCurrent(options.Paths, candidate); err != nil {
		return Result{}, err
	}
	journal.Phase = journalSwitched
	if err := writeJournal(options.Paths, journal); err != nil {
		_ = switchCurrent(options.Paths, previous)
		return Result{}, err
	}
	if err := restartAndCheck(ctx, options.Service); err != nil {
		rollbackErr := rollback(ctx, options, journal)
		if rollbackErr != nil {
			return Result{}, errors.Join(fmt.Errorf("candidate failed health check: %w", err), rollbackErr)
		}
		return Result{}, fmt.Errorf("candidate failed health check and was rolled back: %w", err)
	}
	journal.Phase = journalHealthy
	receipt, err := createRootActionReceipt(options.Paths, ready.Authorization, authorizationSHA, signedReleaseSlotMetadata(release))
	if err != nil {
		return Result{}, err
	}
	journal.Receipt = &receipt
	if err := writeJournal(options.Paths, journal); err != nil {
		return Result{}, err
	}
	if err := persistHealthyActivation(options.Paths, journal, readyPath); err != nil {
		return Result{}, err
	}
	return Result{Version: release.Version, Sequence: release.Sequence, SecurityEpoch: release.SecurityEpoch, Changed: true}, nil
}

func installSlot(paths Paths, release VerifiedRelease, artifact *os.File) (string, error) {
	if err := os.MkdirAll(paths.slotsDir(), 0755); err != nil {
		return "", err
	}
	slotDir := filepath.Join(paths.slotsDir(), release.Version)
	slotPath := filepath.Join(slotDir, "p2pstream")
	if info, err := os.Lstat(slotPath); err == nil {
		if !info.Mode().IsRegular() || info.Mode().Perm()&0022 != 0 {
			return "", errors.New("existing version slot is not a protected regular file")
		}
		if err := verifyFile(slotPath, release.Artifact); err != nil {
			return "", fmt.Errorf("existing version slot does not match signed artifact: %w", err)
		}
		return slotPath, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	tmpDir, err := os.MkdirTemp(paths.slotsDir(), ".slot-*")
	if err != nil {
		return "", err
	}
	keep := false
	defer func() {
		if !keep {
			_ = os.RemoveAll(tmpDir)
		}
	}()
	tmpPath := filepath.Join(tmpDir, "p2pstream")
	out, err := os.OpenFile(tmpPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0755)
	if err != nil {
		return "", err
	}
	if _, err := artifact.Seek(0, io.SeekStart); err != nil {
		_ = out.Close()
		return "", err
	}
	if err := copyArtifact(out, artifact, release.Artifact); err != nil {
		_ = out.Close()
		return "", err
	}
	if err := out.Sync(); err != nil {
		_ = out.Close()
		return "", err
	}
	if err := out.Close(); err != nil {
		return "", err
	}
	if err := syncDir(tmpDir); err != nil {
		return "", err
	}
	if err := os.Rename(tmpDir, slotDir); err != nil {
		if !errors.Is(err, os.ErrExist) {
			return "", err
		}
		if err := verifyFile(slotPath, release.Artifact); err != nil {
			return "", err
		}
	} else {
		keep = true
		if err := syncDir(paths.slotsDir()); err != nil {
			return "", err
		}
	}
	return slotPath, nil
}

func verifyFile(path string, artifact Artifact) error {
	f, err := openRegularNoFollow(path, artifact.Size)
	if err != nil {
		return err
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, io.LimitReader(f, artifact.Size+1))
	if err != nil {
		return err
	}
	if n != artifact.Size || hex.EncodeToString(h.Sum(nil)) != artifactHex(artifact) {
		return errors.New("file digest or size mismatch")
	}
	return nil
}

func currentTarget(paths Paths) (string, error) {
	target, err := os.Readlink(paths.currentPath())
	if err != nil {
		return "", fmt.Errorf("current agent command is not a managed symlink: %w", err)
	}
	if !validSlotTarget(target) {
		return "", fmt.Errorf("current agent symlink has unsafe target %q", target)
	}
	return filepath.ToSlash(target), nil
}

func validSlotTarget(target string) bool {
	clean := filepath.ToSlash(filepath.Clean(target))
	if clean != target || filepath.IsAbs(target) {
		return false
	}
	parts := filepath.SplitList(target)
	_ = parts
	dir, file := filepath.Split(clean)
	if file != "p2pstream" {
		return false
	}
	dir = filepath.ToSlash(filepath.Clean(dir))
	if len(dir) <= len("slots/") || dir[:len("slots/")] != "slots/" {
		return false
	}
	version := dir[len("slots/"):]
	return validVersion(version) || isBootstrapVersion(version)
}

func isBootstrapVersion(version string) bool {
	if len(version) != len("bootstrap-")+16 || version[:len("bootstrap-")] != "bootstrap-" {
		return false
	}
	_, err := hex.DecodeString(version[len("bootstrap-"):])
	return err == nil
}

func switchCurrent(paths Paths, target string) error {
	if !validSlotTarget(target) {
		return errors.New("refusing unsafe agent symlink target")
	}
	tmp := filepath.Join(paths.InstallRoot, ".current-next")
	_ = os.Remove(tmp)
	if err := os.Symlink(target, tmp); err != nil {
		return err
	}
	if err := os.Rename(tmp, paths.currentPath()); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return syncDir(paths.InstallRoot)
}

func writeJournal(paths Paths, journal activationJournal) error {
	if !validSlotTarget(journal.PreviousTarget) || !validSlotTarget(journal.CandidateTarget) {
		return errors.New("activation journal contains an unsafe target")
	}
	return atomicJSON(paths.journalPath(), journal, 0600)
}

func loadCurrentSlotMetadata(paths Paths, target string) (slotMetadata, error) {
	data, err := readRegularNoFollow(paths.currentSlotMetadataPath(), 64<<10)
	if err != nil {
		return slotMetadata{}, fmt.Errorf("read current slot metadata: %w", err)
	}
	var slot slotMetadata
	if err := strictJSON(data, &slot); err != nil {
		return slotMetadata{}, err
	}
	if err := validateSlotMetadata(slot); err != nil {
		return slotMetadata{}, err
	}
	if slot.Target != target {
		return slotMetadata{}, errors.New("current slot metadata does not match current agent symlink")
	}
	return slot, nil
}

func recoverActivation(ctx context.Context, options ActivateOptions) error {
	data, err := readRegularNoFollow(options.Paths.journalPath(), 64<<10)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var journal activationJournal
	if err := strictJSON(data, &journal); err != nil {
		return err
	}
	if !validSlotTarget(journal.PreviousTarget) || !validSlotTarget(journal.CandidateTarget) || !validVersion(journal.Version) {
		return errors.New("activation journal contains invalid targets or version")
	}
	switch journal.Phase {
	case journalHealthy:
		return persistHealthyActivation(options.Paths, journal, options.ReadyPath)
	case journalPrepared, journalSwitched:
		return rollback(ctx, options, journal)
	default:
		return fmt.Errorf("unknown activation journal phase %q", journal.Phase)
	}
}

func rollback(ctx context.Context, options ActivateOptions, journal activationJournal) error {
	if err := switchCurrent(options.Paths, journal.PreviousTarget); err != nil {
		return fmt.Errorf("restore previous slot: %w", err)
	}
	if err := restartAndCheck(ctx, options.Service); err != nil {
		return fmt.Errorf("previous slot failed health check: %w", err)
	}
	return removeAndSync(options.Paths.journalPath())
}

func restartAndCheck(ctx context.Context, service ServiceController) error {
	if err := service.Restart(ctx); err != nil {
		return err
	}
	return service.Healthy(ctx)
}

func clearStaged(paths Paths, readyPath string) error {
	if readyPath == "" {
		readyPath = paths.readyPath()
	}
	if err := removeAndSync(readyPath); err != nil {
		return err
	}
	if err := removeAndSync(paths.stagedPath()); err != nil {
		return err
	}
	for _, name := range []string{"artifact.bin", "manifest.json", "manifest.signatures.json"} {
		if err := removeAndSync(filepath.Join(paths.candidateDir(), name)); err != nil {
			return err
		}
	}
	return nil
}

func clearStagedIfMatchingAuthorization(paths Paths, readyPath string, expected assignmentAuthorizationRecord) error {
	if readyPath == "" {
		readyPath = paths.readyPath()
	}
	data, err := readRegularNoFollow(readyPath, 64<<10)
	if errors.Is(err, os.ErrNotExist) {
		// A stale completed journal must never treat a later campaign's shared
		// candidate directory as its own when its command edge is already gone.
		return nil
	}
	if err != nil {
		return err
	}
	var ready readyRecord
	if strictJSON(data, &ready) != nil || !sameAssignmentAuthorizationRecord(ready.Authorization, expected) {
		return nil
	}
	if readyPath != paths.readyPath() {
		if _, liveErr := os.Lstat(paths.readyPath()); liveErr == nil {
			// A newer staging command owns the shared candidate directory.
			return removeAndSync(readyPath)
		} else if !errors.Is(liveErr, os.ErrNotExist) {
			return liveErr
		}
	}
	return clearStaged(paths, readyPath)
}
