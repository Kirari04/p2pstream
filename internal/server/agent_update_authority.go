package server

import (
	"context"
	"crypto/ed25519"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"time"

	"connectrpc.com/connect"

	p2pstreamv1 "p2pstream/gen/proto/p2pstream/v1"
	"p2pstream/internal/agentupdateauthority"
	"p2pstream/internal/db"
)

// AgentUpdateManagementAuthority is deliberately narrower than the concrete
// key owner so tests and the server state machine cannot obtain private bytes.
type AgentUpdateManagementAuthority interface {
	Identity() agentupdateauthority.Identity
	Sign([]byte) ([]byte, error)
}

// InitializeAgentUpdateManagementAuthority loads the key against the public
// identity pinned in SQLite. On first install only, it creates (or recovers) a
// key and atomically pins its identity. A populated database is always the
// independent source of truth: a missing or replaced key is never adopted.
func InitializeAgentUpdateManagementAuthority(ctx context.Context, database *db.DB, keyPath string) (*agentupdateauthority.Authority, error) {
	if database == nil {
		return nil, errors.New("management update authority requires a database")
	}
	var keyID string
	var publicKey []byte
	var epoch int64
	err := database.QueryRowContext(ctx, `SELECT key_id,public_key,epoch FROM agent_update_management_authority WHERE id=1`).Scan(&keyID, &publicKey, &epoch)
	switch {
	case err == nil:
		if epoch <= 0 || len(publicKey) != ed25519.PublicKeySize {
			return nil, errors.New("pinned management update authority identity is invalid")
		}
		return agentupdateauthority.Load(keyPath, agentupdateauthority.Identity{
			KeyID: keyID, Epoch: uint64(epoch), PublicKey: ed25519.PublicKey(publicKey),
		})
	case !errors.Is(err, sql.ErrNoRows):
		return nil, fmt.Errorf("read pinned management update authority: %w", err)
	}
	// An unpinned key may only be adopted during a genuinely pristine managed-
	// update install (or first-install crash recovery). Existing enrollment or
	// campaign state without the independent database pin is an integrity fault,
	// not permission to establish a new authority from the filesystem.
	var updateStateRows int64
	if err := database.QueryRowContext(ctx, `SELECT
		(SELECT COUNT(*) FROM agent_updater_identities) +
		(SELECT COUNT(*) FROM agent_updater_enrollment_tokens) +
		(SELECT COUNT(*) FROM agent_update_campaigns) +
		(SELECT COUNT(*) FROM agent_update_assignments)`).Scan(&updateStateRows); err != nil {
		return nil, fmt.Errorf("verify pristine managed update state: %w", err)
	}
	if updateStateRows != 0 {
		return nil, errors.New("management update authority identity is missing from a non-pristine database")
	}

	authority, err := agentupdateauthority.LoadExisting(keyPath)
	if errors.Is(err, agentupdateauthority.ErrKeyMissing) {
		authority, err = agentupdateauthority.Generate(keyPath, 1)
		if errors.Is(err, agentupdateauthority.ErrKeyExists) {
			authority, err = agentupdateauthority.LoadExisting(keyPath)
		}
	}
	if err != nil {
		return nil, err
	}
	identity := authority.Identity()
	if identity.Epoch == 0 || identity.Epoch > math.MaxInt64 || len(identity.PublicKey) != ed25519.PublicKeySize {
		return nil, errors.New("generated management update authority identity is invalid")
	}
	now := time.Now().UTC()
	_, err = database.ExecContext(ctx, `INSERT INTO agent_update_management_authority (id,key_id,public_key,epoch,created_at,updated_at) VALUES (1,?,?,?,?,?)`, identity.KeyID, []byte(identity.PublicKey), int64(identity.Epoch), now, now)
	if err == nil {
		return authority, nil
	}
	// Another initializer may have won. It is safe only if the committed public
	// identity is exactly the identity of this already-protected key file.
	if readErr := database.QueryRowContext(ctx, `SELECT key_id,public_key,epoch FROM agent_update_management_authority WHERE id=1`).Scan(&keyID, &publicKey, &epoch); readErr != nil {
		return nil, fmt.Errorf("pin management update authority: %w", err)
	}
	return agentupdateauthority.Load(keyPath, agentupdateauthority.Identity{
		KeyID: keyID, Epoch: uint64(epoch), PublicKey: ed25519.PublicKey(publicKey),
	})
}

func (a *App) SetAgentUpdateManagementAuthority(authority AgentUpdateManagementAuthority, warning error) {
	if a == nil {
		return
	}
	a.AgentUpdateAuthority = authority
	if warning == nil {
		a.AgentUpdateAuthorityWarning = ""
		return
	}
	a.AgentUpdateAuthorityWarning = boundedString(warning.Error(), 512)
}

func (a *App) requireAgentUpdateAuthority() (AgentUpdateManagementAuthority, agentupdateauthority.Identity, error) {
	if a == nil || a.AgentUpdateAuthority == nil {
		return nil, agentupdateauthority.Identity{}, connect.NewError(connect.CodeFailedPrecondition, errors.New("managed update authority is unavailable"))
	}
	identity := a.AgentUpdateAuthority.Identity()
	if identity.Epoch == 0 || identity.Epoch > math.MaxInt64 || len(identity.PublicKey) != ed25519.PublicKeySize || identity.KeyID == "" {
		return nil, agentupdateauthority.Identity{}, connect.NewError(connect.CodeFailedPrecondition, errors.New("managed update authority identity is invalid"))
	}
	return a.AgentUpdateAuthority, identity, nil
}

func agentUpdateAuthorityProto(identity agentupdateauthority.Identity) *p2pstreamv1.AgentUpdateManagementAuthority {
	if identity.Epoch == 0 || len(identity.PublicKey) != ed25519.PublicKeySize || identity.KeyID == "" {
		return nil
	}
	return &p2pstreamv1.AgentUpdateManagementAuthority{
		KeyId: identity.KeyID, Epoch: identity.Epoch, PublicKey: append([]byte(nil), identity.PublicKey...),
	}
}
