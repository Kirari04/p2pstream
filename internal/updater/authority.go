package updater

import (
	"crypto/ed25519"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"p2pstream/internal/agentupdate"
	"p2pstream/internal/agentupdateauth"
)

type pinnedManagementAuthority struct {
	KeyID     string `json:"key_id"`
	Epoch     uint64 `json:"epoch"`
	PublicKey string `json:"public_key"`
}

func (p Paths) authorityPath() string {
	return filepath.Join(filepath.Dir(p.ConfigPath), "management-authority.json")
}

func pinManagementAuthority(paths Paths, rawPublicKey []byte, expectedKeyID string, epoch uint64, owner, group int) error {
	if len(rawPublicKey) != ed25519.PublicKeySize || epoch == 0 {
		return errors.New("out-of-band updater management authority is missing or invalid")
	}
	publicKey := ed25519.PublicKey(append([]byte(nil), rawPublicKey...))
	keyID, err := agentupdateauth.KeyID(publicKey)
	if err != nil {
		return err
	}
	if keyID != expectedKeyID {
		return errors.New("out-of-band updater management authority key ID does not match its public key")
	}
	encoded, err := agentupdate.EncodePublicKey(publicKey)
	if err != nil {
		return err
	}
	want := pinnedManagementAuthority{KeyID: keyID, Epoch: epoch, PublicKey: encoded}
	data, err := readRegularNoFollow(paths.authorityPath(), 64<<10)
	switch {
	case err == nil:
		var existing pinnedManagementAuthority
		if err := strictJSON(data, &existing); err != nil {
			return fmt.Errorf("parse pinned management authority: %w", err)
		}
		if existing != want {
			return errors.New("refusing to replace the pinned updater management authority during bootstrap")
		}
		return requireProtectedRegularFile(paths.authorityPath(), 0640, owner, group)
	case errors.Is(err, os.ErrNotExist):
		if err := atomicJSON(paths.authorityPath(), want, 0640); err != nil {
			return err
		}
		return os.Chown(paths.authorityPath(), owner, group)
	default:
		return err
	}
}

func loadManagementAuthority(paths Paths) (pinnedManagementAuthority, ed25519.PublicKey, error) {
	data, err := readRegularNoFollow(paths.authorityPath(), 64<<10)
	if err != nil {
		return pinnedManagementAuthority{}, nil, err
	}
	var pinned pinnedManagementAuthority
	if err := strictJSON(data, &pinned); err != nil {
		return pinnedManagementAuthority{}, nil, err
	}
	publicKey, err := agentupdate.ParsePublicKey(pinned.PublicKey)
	if err != nil {
		return pinnedManagementAuthority{}, nil, err
	}
	keyID, err := agentupdateauth.KeyID(publicKey)
	if err != nil || pinned.Epoch == 0 || keyID != pinned.KeyID {
		return pinnedManagementAuthority{}, nil, errors.New("pinned updater management authority is inconsistent")
	}
	return pinned, publicKey, nil
}
