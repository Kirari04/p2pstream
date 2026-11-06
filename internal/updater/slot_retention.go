package updater

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"p2pstream/internal/agentupdateauth"

	"golang.org/x/sys/unix"
)

// pruneObsoleteSlots removes only updater-managed, unreferenced version slots.
// It deliberately runs after the durable action journal has been cleared: an
// interrupted prune can leave an empty old slot directory, but cannot make the
// completed activation or rollback ambiguous.
func pruneObsoleteSlots(paths Paths) error {
	if err := paths.validate(); err != nil {
		return err
	}
	retained, err := retainedSlotNames(paths)
	if err != nil {
		return err
	}

	installFD, err := unix.Open(paths.InstallRoot, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return fmt.Errorf("open managed install root: %w", err)
	}
	installRoot := os.NewFile(uintptr(installFD), paths.InstallRoot)
	if installRoot == nil {
		_ = unix.Close(installFD)
		return errors.New("wrap managed install root")
	}
	defer installRoot.Close()
	if err := requireProtectedOwnerAndMode(installFD, unix.S_IFDIR, "managed install root"); err != nil {
		return err
	}
	slotsFD, err := unix.Openat(installFD, "slots", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return fmt.Errorf("open managed slots directory: %w", err)
	}
	slots := os.NewFile(uintptr(slotsFD), paths.slotsDir())
	if slots == nil {
		_ = unix.Close(slotsFD)
		return errors.New("wrap managed slots directory")
	}
	defer slots.Close()
	if err := requireProtectedOwnerAndMode(slotsFD, unix.S_IFDIR, "managed slots directory"); err != nil {
		return err
	}
	entries, err := slots.ReadDir(-1)
	if err != nil {
		return fmt.Errorf("enumerate managed slots: %w", err)
	}
	removed := false
	for _, entry := range entries {
		name := entry.Name()
		if !validVersion(name) && !isBootstrapVersion(name) {
			continue
		}
		if _, ok := retained[name]; ok {
			continue
		}
		if err := securelyRemoveSlot(slotsFD, name); err != nil {
			return fmt.Errorf("prune obsolete slot %q: %w", name, err)
		}
		removed = true
	}
	if removed {
		if err := slots.Sync(); err != nil {
			return fmt.Errorf("sync managed slots directory: %w", err)
		}
	}
	return nil
}

func retainedSlotNames(paths Paths) (map[string]struct{}, error) {
	retained := make(map[string]struct{})
	current, err := currentTarget(paths)
	if err != nil {
		return nil, err
	}
	if err := retainSlotTarget(retained, current); err != nil {
		return nil, err
	}

	if err := retainOptionalSlotMetadata(paths.previousSlotPath(), retained); err != nil {
		return nil, fmt.Errorf("retain previous slot: %w", err)
	}
	if err := retainOptionalActivationJournal(paths.journalPath(), retained); err != nil {
		return nil, fmt.Errorf("retain activation journal slots: %w", err)
	}
	if err := retainOptionalRollbackJournal(paths.rollbackJournalPath(), retained); err != nil {
		return nil, fmt.Errorf("retain rollback journal slots: %w", err)
	}
	if err := retainOptionalCompletedActivation(paths.lastActivationPath(), retained); err != nil {
		return nil, fmt.Errorf("retain latest activation slots: %w", err)
	}

	// The updater account controls staging and may replace these files. Only a
	// canonical receipt signed by the local activator is allowed to extend the
	// retained set; invalid staging input is ignored rather than granting a
	// storage pin or blocking an already-durable root action.
	retainOptionalStagingReceipt(paths, paths.rootActionReceiptPath(), retained)
	retainOptionalRollbackResult(paths, retained)
	return retained, nil
}

func retainSlotTarget(retained map[string]struct{}, target string) error {
	if !validSlotTarget(target) {
		return fmt.Errorf("unsafe retained slot target %q", target)
	}
	parts := strings.Split(target, "/")
	if len(parts) != 3 || parts[0] != "slots" || parts[2] != "p2pstream" {
		return fmt.Errorf("non-canonical retained slot target %q", target)
	}
	retained[parts[1]] = struct{}{}
	return nil
}

func retainSlotMetadata(retained map[string]struct{}, slot slotMetadata) error {
	if err := validateSlotMetadata(slot); err != nil {
		return err
	}
	return retainSlotTarget(retained, slot.Target)
}

func retainOptionalSlotMetadata(path string, retained map[string]struct{}) error {
	data, err := readRegularNoFollow(path, 64<<10)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var slot slotMetadata
	if err := strictJSON(data, &slot); err != nil {
		return err
	}
	return retainSlotMetadata(retained, slot)
}

func retainOptionalActivationJournal(path string, retained map[string]struct{}) error {
	data, err := readRegularNoFollow(path, 64<<10)
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
	if journal.Phase != journalPrepared && journal.Phase != journalSwitched && journal.Phase != journalHealthy {
		return errors.New("activation journal phase is invalid")
	}
	if err := retainSlotTarget(retained, journal.PreviousTarget); err != nil {
		return err
	}
	if err := retainSlotTarget(retained, journal.CandidateTarget); err != nil {
		return err
	}
	return retainSlotMetadata(retained, journal.PreviousSlot)
}

func retainOptionalRollbackJournal(path string, retained map[string]struct{}) error {
	data, err := readRegularNoFollow(path, 256<<10)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
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
	if err := retainSlotMetadata(retained, journal.FromSlot); err != nil {
		return err
	}
	return retainSlotMetadata(retained, journal.ToSlot)
}

func retainOptionalCompletedActivation(path string, retained map[string]struct{}) error {
	data, err := readRegularNoFollow(path, 256<<10)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var state completedActivation
	if err := strictJSON(data, &state); err != nil {
		return err
	}
	if err := retainSlotMetadata(retained, state.PreviousSlot); err != nil {
		return err
	}
	return retainSlotMetadata(retained, state.ActivatedSlot)
}

func retainOptionalStagingReceipt(paths Paths, path string, retained map[string]struct{}) {
	data, err := readRegularNoFollow(path, 128<<10)
	if err != nil {
		return
	}
	var record rootActionReceiptRecord
	if strictJSON(data, &record) != nil ||
		verifyRootActionReceiptForWorker(paths, record, record.Receipt.AgentPublicID, record.Receipt.Action) != nil {
		return
	}
	_ = retainReceiptResult(retained, record.Receipt)
}

func retainOptionalRollbackResult(paths Paths, retained map[string]struct{}) {
	data, err := readRegularNoFollow(paths.rollbackResultPath(), 256<<10)
	if err != nil {
		return
	}
	var result rollbackRecord
	if strictJSON(data, &result) != nil ||
		verifyRootActionReceiptForWorker(paths, result.Receipt, result.Receipt.Receipt.AgentPublicID, agentupdateauth.AssignmentActionRollback) != nil {
		return
	}
	_ = retainReceiptResult(retained, result.Receipt.Receipt)
}

func retainReceiptResult(retained map[string]struct{}, receipt agentupdateauth.RootActionReceipt) error {
	var slotName string
	switch receipt.ResultKind {
	case agentupdateauth.RootActionResultRelease:
		slotName = receipt.ResultVersion
	case agentupdateauth.RootActionResultBootstrap:
		if !digestPattern.MatchString(receipt.ResultArtifactSHA256) {
			return errors.New("bootstrap receipt has an invalid artifact digest")
		}
		slotName = "bootstrap-" + receipt.ResultArtifactSHA256[:16]
	default:
		return errors.New("root action receipt result kind is invalid")
	}
	return retainSlotTarget(retained, "slots/"+slotName+"/p2pstream")
}

func requireProtectedOwnerAndMode(fd int, expectedType uint32, description string) error {
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil {
		return fmt.Errorf("stat %s: %w", description, err)
	}
	if stat.Mode&unix.S_IFMT != expectedType {
		return fmt.Errorf("%s has an unexpected file type", description)
	}
	if stat.Uid != uint32(os.Geteuid()) || stat.Mode&0022 != 0 {
		return fmt.Errorf("%s is not exclusively writable by the activator owner", description)
	}
	return nil
}

func securelyRemoveSlot(slotsFD int, name string) error {
	if !validVersion(name) && !isBootstrapVersion(name) {
		return errors.New("refusing to remove an invalid slot name")
	}
	slotFD, err := unix.Openat(slotsFD, name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return fmt.Errorf("open slot without following links: %w", err)
	}
	slot := os.NewFile(uintptr(slotFD), name)
	if slot == nil {
		_ = unix.Close(slotFD)
		return errors.New("wrap slot directory")
	}
	defer slot.Close()
	if err := requireProtectedOwnerAndMode(slotFD, unix.S_IFDIR, "slot directory"); err != nil {
		return err
	}
	entries, err := slot.ReadDir(-1)
	if err != nil {
		return fmt.Errorf("enumerate slot: %w", err)
	}
	if len(entries) > 1 || len(entries) == 1 && entries[0].Name() != "p2pstream" {
		return errors.New("slot contains files other than the managed executable")
	}
	if len(entries) == 1 {
		binaryFD, err := unix.Openat(slotFD, "p2pstream", unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
		if err != nil {
			return fmt.Errorf("open slot executable without following links: %w", err)
		}
		if err := requireProtectedOwnerAndMode(binaryFD, unix.S_IFREG, "slot executable"); err != nil {
			_ = unix.Close(binaryFD)
			return err
		}
		var binaryStat unix.Stat_t
		if err := unix.Fstat(binaryFD, &binaryStat); err != nil {
			_ = unix.Close(binaryFD)
			return err
		}
		if binaryStat.Nlink != 1 || binaryStat.Size <= 0 {
			_ = unix.Close(binaryFD)
			return errors.New("slot executable is not a non-empty singly-linked file")
		}
		if err := unix.Close(binaryFD); err != nil {
			return err
		}
		if err := unix.Unlinkat(slotFD, "p2pstream", 0); err != nil {
			return fmt.Errorf("remove slot executable: %w", err)
		}
		if err := slot.Sync(); err != nil {
			return fmt.Errorf("sync emptied slot: %w", err)
		}
	}
	if err := unix.Unlinkat(slotsFD, name, unix.AT_REMOVEDIR); err != nil {
		return fmt.Errorf("remove empty slot directory: %w", err)
	}
	return nil
}
