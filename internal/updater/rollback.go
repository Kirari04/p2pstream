package updater

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"p2pstream/internal/agentupdateauth"
)

type previousSlotRecord = slotMetadata

type rollbackRequest struct {
	Authorization assignmentAuthorizationRecord `json:"authorization"`
}

type rollbackRecord struct {
	Authorization assignmentAuthorizationRecord `json:"authorization"`
	Receipt       rootActionReceiptRecord       `json:"receipt"`
}

type rollbackJournal struct {
	Phase            string                        `json:"phase"`
	Authorization    assignmentAuthorizationRecord `json:"authorization"`
	AuthorizationSHA string                        `json:"authorization_sha256"`
	FromSlot         slotMetadata                  `json:"from_slot"`
	ToSlot           slotMetadata                  `json:"to_slot"`
	Receipt          *rootActionReceiptRecord      `json:"receipt,omitempty"`
}

const (
	rollbackPrepared  = "prepared"
	rollbackSwitched  = "switched"
	rollbackCompleted = "completed"
)

func (p Paths) previousSlotPath() string { return filepath.Join(p.rootStateDir(), "previous.json") }
func (p Paths) rollbackPath() string     { return filepath.Join(p.stagingDir(), "rollback.json") }
func (p Paths) rollbackClaimPath() string {
	return filepath.Join(p.rootStateDir(), "rollback-command.json")
}
func (p Paths) rollbackResultPath() string {
	return filepath.Join(p.stagingDir(), "rollback-result.json")
}
func (p Paths) rollbackJournalPath() string {
	return filepath.Join(p.rootStateDir(), "rollback-journal.json")
}

func RequestRollback(paths Paths, authorization assignmentAuthorizationRecord) error {
	if err := paths.validate(); err != nil {
		return err
	}
	if err := verifyAssignmentAuthorizationRecord(paths, authorization, agentupdateauth.AssignmentActionRollback, time.Now().UTC(), 0); err != nil {
		return err
	}
	return atomicJSON(paths.rollbackPath(), rollbackRequest{Authorization: authorization}, 0600)
}

func Rollback(ctx context.Context, paths Paths, service ServiceController) error {
	return rollbackFromPath(ctx, paths, service, paths.rollbackPath())
}

func rollbackFromPath(ctx context.Context, paths Paths, service ServiceController, requestPath string) error {
	if service == nil {
		return errors.New("rollback service controller is required")
	}
	lock, err := acquireLock(paths.lockPath())
	if err != nil {
		return err
	}
	defer lock.Close()
	if _, err := os.Lstat(paths.rollbackJournalPath()); err == nil {
		return recoverRollback(ctx, paths, service, requestPath)
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	requestData, err := readRegularNoFollow(requestPath, 128<<10)
	if err != nil {
		return err
	}
	var request rollbackRequest
	if err := strictJSON(requestData, &request); err != nil {
		return errors.New("invalid rollback request")
	}
	if err := verifyAssignmentAuthorizationRecord(paths, request.Authorization, agentupdateauth.AssignmentActionRollback, time.Now().UTC(), 0); err != nil {
		return err
	}
	rootCounter, err := loadRootActionCounter(paths.rootActionCounterPath())
	if err != nil {
		return err
	}
	current, err := currentTarget(paths)
	if err != nil {
		return err
	}
	currentSlot, err := loadCurrentSlotMetadata(paths, current)
	if err != nil {
		return err
	}
	previous := currentSlot
	if authorizationMatchesSlotExactly(request.Authorization.Authorization, currentSlot) {
		if rootCounter == 0 {
			return errors.New("latest root action counter is unavailable")
		}
		stateData, err := readRegularNoFollow(paths.lastActivationPath(), 256<<10)
		if err != nil {
			return fmt.Errorf("read latest completed activation: %w", err)
		}
		var activation completedActivation
		if err := strictJSON(stateData, &activation); err != nil {
			return err
		}
		if err := verifyRootActionReceiptRecord(paths, activation.Receipt, activation.Authorization, rootCounter-1, time.Now().UTC()); err != nil {
			return fmt.Errorf("verify latest completed activation: %w", err)
		}
		if activation.Receipt.Receipt.Action != agentupdateauth.AssignmentActionActivate || activation.Receipt.Receipt.RootActionCounter != rootCounter ||
			activation.ActivatedSlot != currentSlot {
			return errors.New("latest completed root action is not the current activation")
		}
		data, err := readRegularNoFollow(paths.previousSlotPath(), 64<<10)
		if err != nil {
			return err
		}
		if err := strictJSON(data, &previous); err != nil {
			return err
		}
		if err := validateSlotMetadata(previous); err != nil {
			return err
		}
	}
	if _, err := os.Stat(filepath.Join(paths.InstallRoot, filepath.FromSlash(previous.Target))); err != nil {
		return fmt.Errorf("rollback result slot is unavailable: %w", err)
	}
	digest, err := agentupdateauth.AssignmentAuthorizationDigest(request.Authorization.Authorization)
	if err != nil {
		return err
	}
	journal := rollbackJournal{
		Phase: rollbackPrepared, Authorization: request.Authorization,
		AuthorizationSHA: fmt.Sprintf("%x", digest), FromSlot: currentSlot, ToSlot: previous,
	}
	if err := writeRollbackJournal(paths, journal); err != nil {
		return err
	}
	return continueRollback(ctx, paths, service, journal, requestPath)
}

func recoverRollback(ctx context.Context, paths Paths, service ServiceController, requestPath string) error {
	data, err := readRegularNoFollow(paths.rollbackJournalPath(), 256<<10)
	if err != nil {
		return err
	}
	var journal rollbackJournal
	if err := strictJSON(data, &journal); err != nil {
		return err
	}
	if err := validateRollbackJournal(journal); err != nil {
		return err
	}
	return continueRollback(ctx, paths, service, journal, requestPath)
}

func continueRollback(ctx context.Context, paths Paths, service ServiceController, journal rollbackJournal, requestPath string) error {
	if err := ensureAuthorizationConsumed(paths, journal.Authorization, agentupdateauth.AssignmentActionRollback, journal.AuthorizationSHA); err != nil {
		return err
	}
	if journal.Phase == rollbackPrepared {
		current, err := currentTarget(paths)
		if err != nil {
			return err
		}
		if current != journal.FromSlot.Target && current != journal.ToSlot.Target {
			return errors.New("rollback recovery found an unexpected current slot")
		}
		if current != journal.ToSlot.Target {
			if err := switchCurrent(paths, journal.ToSlot.Target); err != nil {
				return err
			}
		}
		journal.Phase = rollbackSwitched
		if err := writeRollbackJournal(paths, journal); err != nil {
			return err
		}
	}
	if journal.Phase == rollbackSwitched {
		current, err := currentTarget(paths)
		if err != nil {
			return err
		}
		if current != journal.ToSlot.Target {
			if current != journal.FromSlot.Target {
				return errors.New("rollback recovery found an unexpected switched slot")
			}
			if err := switchCurrent(paths, journal.ToSlot.Target); err != nil {
				return err
			}
		}
		if err := restartAndCheck(ctx, service); err != nil {
			if restoreErr := switchCurrent(paths, journal.FromSlot.Target); restoreErr != nil {
				return errors.Join(fmt.Errorf("rolled-back agent failed health check: %w", err), restoreErr)
			}
			if recoveryErr := restartAndCheck(ctx, service); recoveryErr != nil {
				// Preserve the journal so the root recovery service can retry the
				// known-current slot after a transient double failure or reboot.
				return errors.Join(fmt.Errorf("rolled-back agent failed health check: %w", err), fmt.Errorf("restored current agent failed health check: %w", recoveryErr))
			}
			if removeErr := removeAndSync(paths.rollbackJournalPath()); removeErr != nil {
				return errors.Join(fmt.Errorf("rolled-back agent failed health check; current agent recovered: %w", err), removeErr)
			}
			return fmt.Errorf("rolled-back agent failed health check; current agent recovered: %w", err)
		}
		receipt, err := createRootActionReceipt(paths, journal.Authorization, journal.AuthorizationSHA, journal.ToSlot)
		if err != nil {
			return err
		}
		journal.Receipt = &receipt
		journal.Phase = rollbackCompleted
		if err := writeRollbackJournal(paths, journal); err != nil {
			return err
		}
	}
	if journal.Phase != rollbackCompleted || journal.Receipt == nil {
		return errors.New("rollback journal did not reach a completed root action")
	}
	currentCounter, err := loadRootActionCounter(paths.rootActionCounterPath())
	if err != nil {
		return err
	}
	previousCounter := currentCounter
	if currentCounter == journal.Receipt.Receipt.RootActionCounter {
		previousCounter--
	}
	result := rollbackRecord{Authorization: journal.Authorization, Receipt: *journal.Receipt}
	if err := persistRollbackResult(paths, result, journal.ToSlot, previousCounter); err != nil {
		return err
	}
	if err := clearSupersededActivationForRollback(paths, journal.Authorization); err != nil {
		return err
	}
	if err := removeMatchingRollbackRequest(requestPath, journal.Authorization); err != nil {
		return err
	}
	if err := removeAndSync(paths.rollbackJournalPath()); err != nil {
		return err
	}
	return pruneObsoleteSlots(paths)
}

func removeMatchingRollbackRequest(path string, expected assignmentAuthorizationRecord) error {
	data, err := readRegularNoFollow(path, 128<<10)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var request rollbackRequest
	if strictJSON(data, &request) != nil || !sameAssignmentAuthorizationRecord(request.Authorization, expected) {
		return nil
	}
	return removeAndSync(path)
}

func clearSupersededActivationForRollback(paths Paths, rollbackAuthorization assignmentAuthorizationRecord) error {
	for _, path := range []string{paths.activationClaimPath(), paths.readyPath()} {
		data, err := readRegularNoFollow(path, 64<<10)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return err
		}
		var ready readyRecord
		if strictJSON(data, &ready) != nil {
			continue
		}
		old := ready.Authorization.Authorization
		current := rollbackAuthorization.Authorization
		if old.AgentPublicID != current.AgentPublicID || old.CommandSequence >= current.CommandSequence {
			continue
		}
		if path == paths.readyPath() {
			if err := clearStagedIfMatchingAuthorization(paths, path, ready.Authorization); err != nil {
				return err
			}
		} else if err := removeAndSync(path); err != nil {
			return err
		}
	}
	return nil
}

func ensureAuthorizationConsumed(paths Paths, authorization assignmentAuthorizationRecord, action agentupdateauth.AssignmentAction, expectedSHA string) error {
	floor, err := loadRootCommandFloor(paths)
	if err != nil {
		return err
	}
	a := authorization.Authorization
	if floor.Sequence == a.CommandSequence {
		if floor.AuthorizationSHA256 != expectedSHA {
			return errors.New("consumed management command sequence has a different authorization digest")
		}
		return verifyAssignmentAuthorizationRecord(paths, authorization, action, time.UnixMilli(a.IssuedAtUnixMillis), 0)
	}
	if floor.Sequence > a.CommandSequence {
		return errors.New("rollback management authorization was superseded or replayed")
	}
	actual, err := consumeAssignmentAuthorization(paths, authorization, action, time.Now().UTC())
	if err != nil {
		return err
	}
	if actual != expectedSHA {
		return errors.New("rollback management authorization digest changed while consuming it")
	}
	return nil
}

func writeRollbackJournal(paths Paths, journal rollbackJournal) error {
	if err := validateRollbackJournal(journal); err != nil {
		return err
	}
	return atomicJSON(paths.rollbackJournalPath(), journal, 0600)
}

func validateRollbackJournal(journal rollbackJournal) error {
	if journal.Phase != rollbackPrepared && journal.Phase != rollbackSwitched && journal.Phase != rollbackCompleted {
		return errors.New("rollback journal phase is invalid")
	}
	if !digestPattern.MatchString(journal.AuthorizationSHA) || validateSlotMetadata(journal.FromSlot) != nil || validateSlotMetadata(journal.ToSlot) != nil {
		return errors.New("rollback journal authorization or slot metadata is invalid")
	}
	if journal.Phase == rollbackCompleted && journal.Receipt == nil {
		return errors.New("completed rollback journal has no root action receipt")
	}
	return nil
}

func authorizationMatchesSlot(authorization agentupdateauth.AssignmentAuthorization, slot slotMetadata) error {
	if !authorizationMatchesSlotExactly(authorization, slot) {
		return errors.New("signed rollback authorization does not exactly match the active signed release")
	}
	return nil
}

func authorizationMatchesSlotExactly(authorization agentupdateauth.AssignmentAuthorization, slot slotMetadata) bool {
	return authorization.RootVersion == slot.RootVersion && authorization.ManifestSHA256 == slot.ManifestSHA256 &&
		authorization.TargetVersion == slot.Version && authorization.TargetCommit == slot.Commit &&
		authorization.ReleaseSequence == slot.ReleaseSequence && authorization.SecurityEpoch == slot.SecurityEpoch &&
		authorization.OS == slot.OS && authorization.Arch == slot.Arch && authorization.ArtifactName == slot.ArtifactName &&
		authorization.ArtifactSize == slot.ArtifactSize && authorization.ArtifactSHA256 == slot.ArtifactSHA256
}

func persistRollbackResult(paths Paths, result rollbackRecord, current slotMetadata, previousCounter uint64) error {
	if result.Receipt.Receipt.RootActionCounter != previousCounter+1 || result.Receipt.Receipt.RootActionCounter == 0 {
		return errors.New("root rollback receipt counter does not advance exactly once")
	}
	if err := verifyRootActionReceiptRecord(paths, result.Receipt, result.Authorization, previousCounter, time.Now().UTC()); err != nil {
		return err
	}
	if err := atomicJSON(paths.rootActionCounterPath(), rootActionCounter{Counter: result.Receipt.Receipt.RootActionCounter}, 0600); err != nil {
		return err
	}
	if err := atomicJSON(paths.currentSlotMetadataPath(), current, 0600); err != nil {
		return err
	}
	return atomicJSON(paths.rollbackResultPath(), result, 0644)
}
