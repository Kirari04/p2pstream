package updater

import (
	"bytes"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"p2pstream/internal/agentupdateauth"
)

type rootCommandFloor struct {
	Sequence            uint64 `json:"sequence"`
	AuthorizationSHA256 string `json:"authorization_sha256"`
}

func sameAssignmentAuthorizationRecord(left, right assignmentAuthorizationRecord) bool {
	return bytes.Equal(left.CanonicalPayload, right.CanonicalPayload) && bytes.Equal(left.Signature, right.Signature)
}

func (p Paths) rootCommandFloorPath() string {
	return filepath.Join(p.rootStateDir(), "management-command-floor.json")
}

func loadRootCommandFloor(paths Paths) (rootCommandFloor, error) {
	data, err := readRegularNoFollow(paths.rootCommandFloorPath(), 64<<10)
	if errors.Is(err, os.ErrNotExist) {
		return rootCommandFloor{}, nil
	}
	if err != nil {
		return rootCommandFloor{}, err
	}
	var floor rootCommandFloor
	if err := strictJSON(data, &floor); err != nil {
		return rootCommandFloor{}, err
	}
	if floor.Sequence == 0 || !digestPattern.MatchString(floor.AuthorizationSHA256) {
		return rootCommandFloor{}, errors.New("root management command floor is invalid")
	}
	return floor, nil
}

func verifyAssignmentAuthorizationRecord(paths Paths, record assignmentAuthorizationRecord, expectedAction agentupdateauth.AssignmentAction, now time.Time, lastSequence uint64) error {
	payload, err := agentupdateauth.AssignmentAuthorizationPayload(record.Authorization)
	if err != nil {
		return err
	}
	if !bytes.Equal(payload, record.CanonicalPayload) {
		return errors.New("signed root-action authorization canonical payload mismatch")
	}
	pinned, publicKey, err := loadManagementAuthority(paths)
	if err != nil {
		return fmt.Errorf("load pinned updater management authority: %w", err)
	}
	config, err := LoadHostConfig(paths.ConfigPath)
	if err != nil {
		return fmt.Errorf("load pinned updater host config: %w", err)
	}
	if err := agentupdateauth.VerifyAssignmentAuthorization(publicKey, record.Authorization, record.Signature, agentupdateauth.AssignmentAuthorizationVerifyPolicy{
		Now: now, ExpectedAgentPublicID: config.AgentPublicID, ExpectedAction: expectedAction,
		ExpectedAuthorityEpoch: pinned.Epoch, LastCommandSequence: lastSequence,
	}); err != nil {
		return fmt.Errorf("verify signed root-action authorization: %w", err)
	}
	if record.Authorization.AuthorityKeyID != pinned.KeyID {
		return errors.New("signed root-action authorization does not match pinned authority key")
	}
	return nil
}

func consumeAssignmentAuthorization(paths Paths, record assignmentAuthorizationRecord, expectedAction agentupdateauth.AssignmentAction, now time.Time) (string, error) {
	floor, err := loadRootCommandFloor(paths)
	if err != nil {
		return "", err
	}
	if err := verifyAssignmentAuthorizationRecord(paths, record, expectedAction, now, floor.Sequence); err != nil {
		return "", err
	}
	digest, err := agentupdateauth.AssignmentAuthorizationDigest(record.Authorization)
	if err != nil {
		return "", err
	}
	digestText := hex.EncodeToString(digest[:])
	next := rootCommandFloor{Sequence: record.Authorization.CommandSequence, AuthorizationSHA256: digestText}
	if err := atomicJSON(paths.rootCommandFloorPath(), next, 0600); err != nil {
		return "", err
	}
	return digestText, nil
}
