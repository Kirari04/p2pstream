package updater

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"time"

	"p2pstream/internal/agentupdateauth"
)

type StageOptions struct {
	Paths         Paths
	Source        Source
	Verifier      Verifier
	Policy        VerifyPolicy
	TrustedRoot   []byte
	DiskPreflight func(string, int64) error
}

type readyRecord struct {
	Authorization assignmentAuthorizationRecord `json:"authorization"`
	AgentPublicID string                        `json:"agent_public_id"`
	AssignmentID  int64                         `json:"assignment_id"`
	Generation    int64                         `json:"generation"`
	Nonce         string                        `json:"activation_nonce"`
	Version       string                        `json:"version"`
	Commit        string                        `json:"commit"`
	ManifestSHA   string                        `json:"manifest_sha256"`
	RootVersion   uint64                        `json:"root_version"`
	Sequence      uint64                        `json:"sequence"`
	SecurityEpoch uint64                        `json:"security_epoch"`
	ArtifactName  string                        `json:"artifact_name"`
	ArtifactSize  int64                         `json:"artifact_size"`
	ArtifactSHA   string                        `json:"artifact_sha256"`
	ServerVersion string                        `json:"server_version"`
}

type stagedRecord struct {
	Version       string `json:"version"`
	Commit        string `json:"commit"`
	ManifestSHA   string `json:"manifest_sha256"`
	RootVersion   uint64 `json:"root_version"`
	Sequence      uint64 `json:"sequence"`
	SecurityEpoch uint64 `json:"security_epoch"`
	ArtifactName  string `json:"artifact_name"`
	ArtifactSize  int64  `json:"artifact_size"`
	ArtifactSHA   string `json:"artifact_sha256"`
	ServerVersion string `json:"server_version"`
}

func Stage(ctx context.Context, options StageOptions) (Result, error) {
	if err := options.Paths.validate(); err != nil {
		return Result{}, err
	}
	if options.Source == nil || options.Verifier == nil {
		return Result{}, errors.New("updater source and verifier are required")
	}
	if !validVersion(options.Policy.ServerVersion) {
		return Result{}, errors.New("a semantic management server version is required for update verification")
	}
	if options.Policy.Now.IsZero() {
		options.Policy.Now = time.Now().UTC()
	}
	if options.DiskPreflight == nil {
		options.DiskPreflight = diskPreflight
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

	manifest, signatures, err := options.Source.FetchMetadata(ctx)
	if err != nil {
		return Result{}, err
	}
	if len(manifest) > defaultMaxMetadata || len(signatures) > defaultMaxMetadata {
		return Result{}, errors.New("update metadata exceeds size limit")
	}
	release, err := options.Verifier.Verify(manifest, signatures, options.TrustedRoot, options.Policy)
	if err != nil {
		return Result{}, fmt.Errorf("verify update metadata before download: %w", err)
	}
	if err := validateRelease(release); err != nil {
		return Result{}, err
	}
	if release.Sequence == floor.Sequence && release.SecurityEpoch == floor.SecurityEpoch && release.Version == floor.Version {
		return Result{Version: release.Version, Sequence: release.Sequence, SecurityEpoch: release.SecurityEpoch}, nil
	}
	if err := os.MkdirAll(options.Paths.stagingDir(), 0700); err != nil {
		return Result{}, err
	}
	if err := options.DiskPreflight(options.Paths.stagingDir(), release.Artifact.Size); err != nil {
		return Result{}, err
	}
	// A ready record is the activation edge. Remove and fsync it before changing
	// any candidate component so an activator never consumes a partial update.
	if err := removeAndSync(options.Paths.readyPath()); err != nil {
		return Result{}, err
	}
	if err := os.MkdirAll(options.Paths.candidateDir(), 0700); err != nil {
		return Result{}, err
	}

	body, err := options.Source.FetchArtifact(ctx, release.Artifact)
	if err != nil {
		return Result{}, err
	}
	defer body.Close()
	tmp, err := os.CreateTemp(options.Paths.candidateDir(), ".artifact-*")
	if err != nil {
		return Result{}, err
	}
	tmpName := tmp.Name()
	keep := false
	defer func() {
		_ = tmp.Close()
		if !keep {
			_ = os.Remove(tmpName)
		}
	}()
	if err := tmp.Chmod(0700); err != nil {
		return Result{}, err
	}
	if err := copyArtifact(tmp, body, release.Artifact); err != nil {
		return Result{}, err
	}
	if _, err := tmp.Seek(0, 0); err != nil {
		return Result{}, err
	}
	if err := options.Verifier.VerifyArtifact(tmp, release.Artifact); err != nil {
		return Result{}, fmt.Errorf("verify downloaded artifact: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		return Result{}, err
	}
	if err := tmp.Close(); err != nil {
		return Result{}, err
	}

	// Recheck signed policy after a potentially long download. This also keeps
	// staging and activation symmetric when metadata expiry is near.
	options.Policy.Now = time.Now().UTC()
	rechecked, err := options.Verifier.Verify(manifest, signatures, options.TrustedRoot, options.Policy)
	if err != nil {
		return Result{}, fmt.Errorf("verify update metadata after download: %w", err)
	}
	if rechecked != release {
		return Result{}, errors.New("verified release changed between staging checks")
	}

	artifactPath := filepath.Join(options.Paths.candidateDir(), "artifact.bin")
	if err := os.Rename(tmpName, artifactPath); err != nil {
		return Result{}, err
	}
	keep = true
	if err := atomicWrite(filepath.Join(options.Paths.candidateDir(), "manifest.json"), manifest, 0600); err != nil {
		return Result{}, err
	}
	if err := atomicWrite(filepath.Join(options.Paths.candidateDir(), "manifest.signatures.json"), signatures, 0600); err != nil {
		return Result{}, err
	}
	if err := syncDir(options.Paths.candidateDir()); err != nil {
		return Result{}, err
	}
	staged := stagedRecord{
		Version: release.Version, Commit: release.Commit, ManifestSHA: release.ManifestSHA256,
		RootVersion: release.RootVersion, Sequence: release.Sequence, SecurityEpoch: release.SecurityEpoch,
		ArtifactName: release.Artifact.Name, ArtifactSize: release.Artifact.Size,
		ArtifactSHA: artifactHex(release.Artifact), ServerVersion: options.Policy.ServerVersion,
	}
	if err := atomicJSON(options.Paths.stagedPath(), staged, 0600); err != nil {
		return Result{}, err
	}
	return Result{Version: release.Version, Sequence: release.Sequence, SecurityEpoch: release.SecurityEpoch, Changed: true}, nil
}

func RequestActivation(paths Paths, authorization assignmentAuthorizationRecord, expected VerifiedRelease, serverVersion string) error {
	if err := paths.validate(); err != nil {
		return err
	}
	if err := validateRelease(expected); err != nil {
		return err
	}
	if !validVersion(serverVersion) {
		return errors.New("activation assignment has an invalid management server version")
	}
	data, err := readRegularNoFollow(paths.stagedPath(), 64<<10)
	if err != nil {
		return fmt.Errorf("read staged release record: %w", err)
	}
	var staged stagedRecord
	if err := strictJSON(data, &staged); err != nil {
		return err
	}
	if !validVersion(staged.ServerVersion) {
		return errors.New("staged release has an invalid management server version")
	}
	want := stagedRecord{
		Version: expected.Version, Commit: expected.Commit, ManifestSHA: expected.ManifestSHA256,
		RootVersion: expected.RootVersion, Sequence: expected.Sequence, SecurityEpoch: expected.SecurityEpoch,
		ArtifactName: expected.Artifact.Name, ArtifactSize: expected.Artifact.Size,
		ArtifactSHA: artifactHex(expected.Artifact), ServerVersion: serverVersion,
	}
	if staged != want {
		return errors.New("activation assignment does not exactly match the staged signed release")
	}
	if err := verifyAssignmentAuthorizationRecord(paths, authorization, agentupdateauth.AssignmentActionActivate, time.Now().UTC(), 0); err != nil {
		return err
	}
	if err := authorizationMatchesRelease(authorization.Authorization, expected, serverVersion); err != nil {
		return err
	}
	a := authorization.Authorization
	ready := readyRecord{
		Authorization: authorization,
		AgentPublicID: a.AgentPublicID, AssignmentID: a.AssignmentID,
		Generation: a.Generation, Nonce: base64.StdEncoding.EncodeToString(a.Nonce),
		Version: staged.Version, Commit: staged.Commit, ManifestSHA: staged.ManifestSHA,
		RootVersion: staged.RootVersion, Sequence: staged.Sequence, SecurityEpoch: staged.SecurityEpoch,
		ArtifactName: staged.ArtifactName, ArtifactSize: staged.ArtifactSize, ArtifactSHA: staged.ArtifactSHA,
		ServerVersion: staged.ServerVersion,
	}
	return atomicJSON(paths.readyPath(), ready, 0600)
}

func authorizationMatchesRelease(authorization agentupdateauth.AssignmentAuthorization, release VerifiedRelease, serverVersion string) error {
	if authorization.ServerVersion != serverVersion || authorization.RootVersion != release.RootVersion ||
		authorization.ManifestSHA256 != release.ManifestSHA256 || authorization.TargetVersion != release.Version ||
		authorization.TargetCommit != release.Commit || authorization.ReleaseSequence != release.Sequence ||
		authorization.SecurityEpoch != release.SecurityEpoch || authorization.OS != runtime.GOOS ||
		authorization.Arch != runtime.GOARCH || authorization.ArtifactName != release.Artifact.Name ||
		authorization.ArtifactSize != release.Artifact.Size || authorization.ArtifactSHA256 != artifactHex(release.Artifact) {
		return errors.New("signed root-action authorization does not exactly match the staged signed release")
	}
	return nil
}

func validateAssignment(assignment Assignment) error {
	if !agentIDPattern.MatchString(assignment.AgentPublicID) || assignment.AssignmentID <= 0 || assignment.Generation <= 0 {
		return errors.New("signed update assignment identity/generation is invalid")
	}
	if len(assignment.Nonce) != 32 {
		return errors.New("signed update assignment nonce must be exactly 32 bytes")
	}
	return nil
}

func validateRelease(release VerifiedRelease) error {
	if !validVersion(release.Version) {
		return fmt.Errorf("unsafe release version %q", release.Version)
	}
	if !commitPattern.MatchString(release.Commit) || !digestPattern.MatchString(release.ManifestSHA256) {
		return errors.New("verified release commit or manifest digest is invalid")
	}
	if err := validateArtifact(release.Artifact); err != nil {
		return err
	}
	if release.Artifact.Name != runtimeArtifactName(release.Version) {
		return fmt.Errorf("artifact %q does not match release/platform", release.Artifact.Name)
	}
	return nil
}

func loadFloor(path string) (Floor, error) {
	data, err := readRegularNoFollow(path, 64<<10)
	if errors.Is(err, os.ErrNotExist) {
		return Floor{}, nil
	}
	if err != nil {
		return Floor{}, fmt.Errorf("read updater security floor: %w", err)
	}
	var floor Floor
	if err := strictJSON(data, &floor); err != nil {
		return Floor{}, fmt.Errorf("parse updater security floor: %w", err)
	}
	if floor.Version != "" && !validVersion(floor.Version) {
		return Floor{}, errors.New("updater security floor contains an invalid version")
	}
	if floor.MinimumSafeVersion != "" && !validVersion(floor.MinimumSafeVersion) {
		return Floor{}, errors.New("updater security floor contains an invalid minimum safe version")
	}
	return floor, nil
}

func applyFloor(policy *VerifyPolicy, floor Floor) {
	if floor.Sequence > policy.CurrentSequence {
		policy.CurrentSequence = floor.Sequence
	}
	if floor.SecurityEpoch > policy.CurrentSecurityEpoch {
		policy.CurrentSecurityEpoch = floor.SecurityEpoch
	}
	if floor.MinimumSafeVersion != "" {
		policy.CurrentMinimumSafeVersion = floor.MinimumSafeVersion
	}
	if floor.RootVersion > policy.MinimumRootVersion {
		policy.MinimumRootVersion = floor.RootVersion
	}
	if floor.Version != "" {
		policy.CurrentVersion = floor.Version
	}
}
