package updater

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"p2pstream/internal/agentupdate"
	"p2pstream/internal/agentupdateauth"
)

type rootActionCounter struct {
	Counter uint64 `json:"counter"`
}

type slotMetadata struct {
	Target          string                               `json:"target"`
	ResultKind      agentupdateauth.RootActionResultKind `json:"result_kind"`
	RootVersion     uint64                               `json:"root_version"`
	ManifestSHA256  string                               `json:"manifest_sha256"`
	Version         string                               `json:"version"`
	Commit          string                               `json:"commit"`
	BuildVersion    string                               `json:"build_version"`
	BuildCommit     string                               `json:"build_commit"`
	ReleaseSequence uint64                               `json:"release_sequence"`
	SecurityEpoch   uint64                               `json:"security_epoch"`
	OS              string                               `json:"os"`
	Arch            string                               `json:"arch"`
	ArtifactName    string                               `json:"artifact_name"`
	ArtifactSize    int64                                `json:"artifact_size"`
	ArtifactSHA256  string                               `json:"artifact_sha256"`
}

type completedActivation struct {
	Authorization assignmentAuthorizationRecord `json:"authorization"`
	Receipt       rootActionReceiptRecord       `json:"receipt"`
	PreviousSlot  slotMetadata                  `json:"previous_slot"`
	ActivatedSlot slotMetadata                  `json:"activated_slot"`
}

func (p Paths) rootActionCounterPath() string {
	return filepath.Join(p.rootStateDir(), "root-action-counter.json")
}
func (p Paths) rootActionReceiptPath() string {
	return filepath.Join(p.stagingDir(), "root-action-receipt.json")
}
func (p Paths) lastActivationPath() string {
	return filepath.Join(p.rootStateDir(), "last-activation.json")
}
func (p Paths) currentSlotMetadataPath() string {
	return filepath.Join(p.rootStateDir(), "current-slot.json")
}

func signedReleaseSlotMetadata(release VerifiedRelease) slotMetadata {
	return slotMetadata{
		Target:      filepath.ToSlash(filepath.Join("slots", release.Version, "p2pstream")),
		ResultKind:  agentupdateauth.RootActionResultSignedRelease,
		RootVersion: release.RootVersion, ManifestSHA256: release.ManifestSHA256, Version: release.Version,
		Commit: release.Commit, BuildVersion: release.Version, BuildCommit: release.Commit,
		ReleaseSequence: release.Sequence, SecurityEpoch: release.SecurityEpoch,
		OS: runtime.GOOS, Arch: runtime.GOARCH, ArtifactName: release.Artifact.Name,
		ArtifactSize: release.Artifact.Size, ArtifactSHA256: artifactHex(release.Artifact),
	}
}

func createRootActionReceipt(paths Paths, authorization assignmentAuthorizationRecord, authorizationSHA string, result slotMetadata) (rootActionReceiptRecord, error) {
	if err := validateSlotMetadata(result); err != nil {
		return rootActionReceiptRecord{}, err
	}
	digest, err := agentupdateauth.AssignmentAuthorizationDigest(authorization.Authorization)
	if err != nil {
		return rootActionReceiptRecord{}, err
	}
	if authorizationSHA != hex.EncodeToString(digest[:]) {
		return rootActionReceiptRecord{}, errors.New("root action result does not bind the consumed management authorization")
	}
	previous, err := loadRootActionCounter(paths.rootActionCounterPath())
	if err != nil {
		return rootActionReceiptRecord{}, err
	}
	if previous == math.MaxUint64 {
		return rootActionReceiptRecord{}, errors.New("root action counter is exhausted")
	}
	private, err := loadActivatorPrivateKey(paths.activatorPrivateKeyPath())
	if err != nil {
		return rootActionReceiptRecord{}, err
	}
	activatorKeyID, err := agentupdateauth.KeyID(private.Public().(ed25519.PublicKey))
	if err != nil {
		return rootActionReceiptRecord{}, err
	}
	a := authorization.Authorization
	resultVersion, resultCommit := result.BuildVersion, result.BuildCommit
	receipt := agentupdateauth.RootActionReceipt{
		AgentPublicID: a.AgentPublicID, AssignmentID: a.AssignmentID, CampaignID: a.CampaignID,
		Generation: a.Generation, Action: a.Action, CommandSequence: a.CommandSequence,
		AuthorizationSHA256: authorizationSHA, AuthorizationNonce: append([]byte(nil), a.Nonce...),
		AuthorityKeyID: a.AuthorityKeyID, AuthorityEpoch: a.AuthorityEpoch, ActivatorKeyID: activatorKeyID,
		RootActionCounter: previous + 1, CompletedAtUnixMillis: time.Now().UTC().UnixMilli(),
		ResultKind: result.ResultKind, ResultRootVersion: result.RootVersion,
		ResultManifestSHA256: result.ManifestSHA256, ResultVersion: resultVersion, ResultCommit: resultCommit,
		ResultReleaseSequence: result.ReleaseSequence, ResultSecurityEpoch: result.SecurityEpoch,
		ResultOS: result.OS, ResultArch: result.Arch, ResultArtifactName: result.ArtifactName,
		ResultArtifactSize: result.ArtifactSize, ResultArtifactSHA256: result.ArtifactSHA256,
	}
	payload, err := agentupdateauth.RootActionReceiptPayload(receipt)
	if err != nil {
		return rootActionReceiptRecord{}, err
	}
	signature, err := agentupdateauth.SignRootActionReceipt(private, receipt)
	if err != nil {
		return rootActionReceiptRecord{}, err
	}
	record := rootActionReceiptRecord{Receipt: receipt, CanonicalPayload: payload, Signature: signature}
	if err := verifyRootActionReceiptRecord(paths, record, authorization, previous, time.Now().UTC()); err != nil {
		return rootActionReceiptRecord{}, err
	}
	return record, nil
}

func verifyRootActionReceiptRecord(paths Paths, record rootActionReceiptRecord, authorization assignmentAuthorizationRecord, previous uint64, now time.Time) error {
	payload, err := agentupdateauth.RootActionReceiptPayload(record.Receipt)
	if err != nil {
		return err
	}
	if !bytes.Equal(payload, record.CanonicalPayload) {
		return errors.New("root action receipt canonical payload mismatch")
	}
	digest, err := agentupdateauth.AssignmentAuthorizationDigest(authorization.Authorization)
	if err != nil {
		return err
	}
	r := record.Receipt
	a := authorization.Authorization
	if r.AssignmentID != a.AssignmentID || r.CampaignID != a.CampaignID || r.Generation != a.Generation ||
		r.CommandSequence != a.CommandSequence || !bytes.Equal(r.AuthorizationNonce, a.Nonce) {
		return errors.New("root action receipt assignment context does not match its management authorization")
	}
	private, err := loadActivatorPrivateKey(paths.activatorPrivateKeyPath())
	if err != nil {
		return err
	}
	return agentupdateauth.VerifyRootActionReceipt(private.Public().(ed25519.PublicKey), record.Receipt, record.Signature, agentupdateauth.RootActionReceiptVerifyPolicy{
		Now: now, ExpectedAgentPublicID: authorization.Authorization.AgentPublicID,
		ExpectedAction: authorization.Authorization.Action, ExpectedAuthorizationSHA256: hex.EncodeToString(digest[:]),
		ExpectedAuthorityKeyID: authorization.Authorization.AuthorityKeyID,
		ExpectedAuthorityEpoch: authorization.Authorization.AuthorityEpoch, LastRootActionCounter: previous,
	})
}

func loadActivatorPrivateKey(path string) (ed25519.PrivateKey, error) {
	data, err := readRegularNoFollow(path, 128)
	if err != nil {
		return nil, fmt.Errorf("read root activator identity: %w", err)
	}
	key, err := agentupdate.ParsePrivateKey(strings.TrimSuffix(string(data), "\n"))
	if err != nil {
		return nil, fmt.Errorf("parse root activator identity: %w", err)
	}
	return key, nil
}

func loadRootActionCounter(path string) (uint64, error) {
	data, err := readRegularNoFollow(path, 64<<10)
	if errors.Is(err, os.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	var counter rootActionCounter
	if err := strictJSON(data, &counter); err != nil {
		return 0, err
	}
	return counter.Counter, nil
}

func persistHealthyActivation(paths Paths, journal activationJournal, readyPath string) error {
	if journal.Receipt == nil {
		return errors.New("healthy activation journal has no signed root action receipt")
	}
	previousCounter, err := loadRootActionCounter(paths.rootActionCounterPath())
	if err != nil {
		return err
	}
	if previousCounter == journal.Receipt.Receipt.RootActionCounter {
		previousCounter--
	}
	if journal.Receipt.Receipt.RootActionCounter == 0 || journal.Receipt.Receipt.RootActionCounter != previousCounter+1 {
		return errors.New("root activation receipt counter does not advance exactly once")
	}
	if err := verifyRootActionReceiptRecord(paths, *journal.Receipt, journal.Authorization, previousCounter, time.Now().UTC()); err != nil {
		return err
	}
	activated := signedReleaseSlotMetadata(VerifiedRelease{
		Version: journal.Version, Commit: journal.Receipt.Receipt.ResultCommit,
		ManifestSHA256: journal.Receipt.Receipt.ResultManifestSHA256, RootVersion: journal.RootVersion,
		Sequence: journal.Sequence, SecurityEpoch: journal.SecurityEpoch,
		Artifact: Artifact{Name: journal.Receipt.Receipt.ResultArtifactName, Size: journal.Receipt.Receipt.ResultArtifactSize,
			SHA256: mustArtifactDigest(journal.Receipt.Receipt.ResultArtifactSHA256)},
	})
	activated.Target = journal.CandidateTarget
	state := completedActivation{Authorization: journal.Authorization, Receipt: *journal.Receipt, PreviousSlot: journal.PreviousSlot, ActivatedSlot: activated}
	if err := atomicJSON(paths.lastActivationPath(), state, 0600); err != nil {
		return err
	}
	if err := atomicJSON(paths.rootActionReceiptPath(), journal.Receipt, 0644); err != nil {
		return err
	}
	if err := atomicJSON(paths.rootActionCounterPath(), rootActionCounter{Counter: journal.Receipt.Receipt.RootActionCounter}, 0600); err != nil {
		return err
	}
	floor := Floor{Sequence: journal.Sequence, SecurityEpoch: journal.SecurityEpoch, MinimumSafeVersion: journal.MinimumSafeVersion, RootVersion: journal.RootVersion, Version: journal.Version}
	if err := atomicJSON(paths.floorPath(), floor, 0640); err != nil {
		return err
	}
	if err := atomicJSON(paths.previousSlotPath(), journal.PreviousSlot, 0600); err != nil {
		return err
	}
	if err := atomicJSON(paths.currentSlotMetadataPath(), activated, 0600); err != nil {
		return err
	}
	if err := clearStagedIfMatchingAuthorization(paths, readyPath, journal.Authorization); err != nil {
		return err
	}
	if err := removeAndSync(paths.journalPath()); err != nil {
		return err
	}
	return pruneObsoleteSlots(paths)
}

func mustArtifactDigest(value string) [sha256.Size]byte {
	var result [sha256.Size]byte
	decoded, _ := hex.DecodeString(value)
	copy(result[:], decoded)
	return result
}

func validateSlotMetadata(slot slotMetadata) error {
	if !validSlotTarget(slot.Target) || slot.OS == "" || slot.Arch == "" || slot.ArtifactName == "" ||
		slot.ArtifactSize <= 0 || !digestPattern.MatchString(slot.ArtifactSHA256) {
		return errors.New("slot metadata is invalid")
	}
	switch slot.ResultKind {
	case agentupdateauth.RootActionResultSignedRelease:
		if !validVersion(slot.Version) || !commitPattern.MatchString(slot.Commit) || !digestPattern.MatchString(slot.ManifestSHA256) ||
			slot.BuildVersion != slot.Version || slot.BuildCommit != slot.Commit || slot.RootVersion == 0 || slot.ReleaseSequence == 0 || slot.SecurityEpoch == 0 {
			return errors.New("signed release slot metadata is invalid")
		}
	case agentupdateauth.RootActionResultBootstrap:
		if !bootstrapVersionPattern.MatchString(slot.Version) || slot.RootVersion != 0 || slot.ManifestSHA256 != "" ||
			slot.ReleaseSequence != 0 || slot.SecurityEpoch != 0 || slot.Commit != "" || !validVersion(slot.BuildVersion) || !commitPattern.MatchString(slot.BuildCommit) {
			return errors.New("bootstrap slot metadata is invalid")
		}
	default:
		return errors.New("slot metadata result kind is invalid")
	}
	return nil
}

func LoadRootActionReceipt(paths Paths) (rootActionReceiptRecord, error) {
	data, err := readRegularNoFollow(paths.rootActionReceiptPath(), 128<<10)
	if err != nil {
		return rootActionReceiptRecord{}, err
	}
	var record rootActionReceiptRecord
	if err := strictJSON(data, &record); err != nil {
		return rootActionReceiptRecord{}, err
	}
	return record, nil
}

func verifyRootActionReceiptForWorker(paths Paths, record rootActionReceiptRecord, agentPublicID string, action agentupdateauth.AssignmentAction) error {
	payload, err := agentupdateauth.RootActionReceiptPayload(record.Receipt)
	if err != nil {
		return err
	}
	if !bytes.Equal(payload, record.CanonicalPayload) {
		return errors.New("root action receipt canonical payload mismatch")
	}
	publicText, err := readRegularNoFollow(paths.activatorPublicKeyPath(), 128)
	if err != nil {
		return err
	}
	publicKey, err := parsePublicKeyText(publicText)
	if err != nil {
		return err
	}
	keyID, err := agentupdateauth.KeyID(publicKey)
	if err != nil {
		return err
	}
	if record.Receipt.AgentPublicID != agentPublicID || record.Receipt.Action != action || record.Receipt.ActivatorKeyID != keyID ||
		len(record.Signature) != ed25519.SignatureSize || !ed25519.Verify(publicKey, payload, record.Signature) {
		return errors.New("root action receipt does not match the local agent, action, or activator identity")
	}
	return nil
}
