package updater

import (
	"errors"
	"fmt"
	"os"
	"time"

	"p2pstream/internal/agentupdateauth"
)

// Explicit re-enrollment may upgrade an older rescue runner whose security
// floor predates manifest pins. Recover only an exact authenticated completed
// activation matching that floor. Missing evidence keeps strict upgrade-only
// behavior; no version, sequence, epoch or minimum-safe floor is lowered.
func restoreFloorManifestPin(paths Paths) error {
	floor, err := loadFloor(paths.floorPath())
	if err != nil || floor.ManifestSHA256 != "" || floor.Sequence == 0 {
		return err
	}
	data, err := readRegularNoFollow(paths.lastActivationPath(), 256<<10)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var activation completedActivation
	if err := strictJSON(data, &activation); err != nil {
		return fmt.Errorf("read completed activation for security floor migration: %w", err)
	}
	r := activation.Receipt.Receipt
	if r.RootActionCounter == 0 || r.Action != agentupdateauth.AssignmentActionActivate || r.ResultKind != agentupdateauth.RootActionResultRelease {
		return errors.New("security floor migration requires a completed activation receipt")
	}
	counter, err := loadRootActionCounter(paths.rootActionCounterPath())
	if err != nil {
		return err
	}
	if r.RootActionCounter > counter {
		return errors.New("security floor migration receipt exceeds the committed root counter")
	}
	a := activation.Authorization.Authorization
	if err := verifyAssignmentAuthorizationRecord(paths, activation.Authorization, agentupdateauth.AssignmentActionActivate, time.UnixMilli(a.IssuedAtUnixMillis), 0); err != nil {
		return fmt.Errorf("authenticate security floor migration authorization: %w", err)
	}
	if err := verifyRootActionReceiptRecord(paths, activation.Receipt, activation.Authorization, r.RootActionCounter-1, time.Now().UTC()); err != nil {
		return fmt.Errorf("authenticate security floor migration receipt: %w", err)
	}
	if r.ResultManifestSHA256 != a.ManifestSHA256 || r.ResultVersion != a.TargetVersion || r.ResultCommit != a.TargetCommit ||
		r.ResultReleaseSequence != a.ReleaseSequence || r.ResultSecurityEpoch != a.SecurityEpoch ||
		r.ResultOS != a.OS || r.ResultArch != a.Arch || r.ResultArtifactName != a.ArtifactName ||
		r.ResultArtifactSize != a.ArtifactSize || r.ResultArtifactSHA256 != a.ArtifactSHA256 {
		return errors.New("security floor migration receipt does not match the activated release")
	}
	if floor.Version != r.ResultVersion || floor.Sequence != r.ResultReleaseSequence || floor.SecurityEpoch != r.ResultSecurityEpoch {
		return nil
	}
	floor.ManifestSHA256 = r.ResultManifestSHA256
	return persistFloor(paths, floor)
}
