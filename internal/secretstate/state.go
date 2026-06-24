package secretstate

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"p2pstream/internal/db"
	"p2pstream/internal/secretfiles"
	"p2pstream/internal/secrets"
	"p2pstream/internal/secretstore"
)

const SchemaVersion int64 = 1

type Store interface {
	GetSecretEncryptionState(ctx context.Context) (db.SecretEncryptionState, error)
	UpsertSecretEncryptionState(ctx context.Context, arg db.UpsertSecretEncryptionStateParams) (db.SecretEncryptionState, error)
}

type Snapshot struct {
	SchemaVersion            int64     `json:"schema_version"`
	Provider                 string    `json:"provider,omitempty"`
	CurrentKeyID             string    `json:"current_key_id,omitempty"`
	EncryptionEnabled        bool      `json:"encryption_enabled"`
	EncryptionRequired       bool      `json:"encryption_required"`
	DatabaseScanned          int64     `json:"database_scanned"`
	DatabaseEncrypted        int64     `json:"database_encrypted"`
	DatabaseRewrapped        int64     `json:"database_rewrapped"`
	DatabaseUnchanged        int64     `json:"database_unchanged"`
	PrivateKeyFilesScanned   int64     `json:"private_key_files_scanned"`
	PrivateKeyFilesEncrypted int64     `json:"private_key_files_encrypted"`
	PrivateKeyFilesRewrapped int64     `json:"private_key_files_rewrapped"`
	PrivateKeyFilesUnchanged int64     `json:"private_key_files_unchanged"`
	LastReconciledAt         time.Time `json:"last_reconciled_at"`
	UpdatedAt                time.Time `json:"updated_at"`
}

func Get(ctx context.Context, store Store) (*Snapshot, error) {
	if store == nil {
		return nil, nil
	}
	row, err := store.GetSecretEncryptionState(ctx)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return SnapshotFromRow(row), nil
}

func Record(ctx context.Context, store Store, service *secrets.Service, databaseResult secretstore.ReconcileResult, fileResult secretfiles.ReconcileResult) (*Snapshot, error) {
	if store == nil {
		return nil, nil
	}
	row, err := store.UpsertSecretEncryptionState(ctx, db.UpsertSecretEncryptionStateParams{
		SchemaVersion:            SchemaVersion,
		Provider:                 provider(service),
		CurrentKeyID:             currentKeyID(service),
		EncryptionEnabled:        boolToInt64(service != nil && service.Enabled()),
		EncryptionRequired:       boolToInt64(service != nil && service.Required()),
		DatabaseScanned:          int64(databaseResult.Scanned),
		DatabaseEncrypted:        int64(databaseResult.Encrypted),
		DatabaseRewrapped:        int64(databaseResult.Rewrapped),
		DatabaseUnchanged:        int64(databaseResult.Unchanged),
		PrivateKeyFilesScanned:   int64(fileResult.Scanned),
		PrivateKeyFilesEncrypted: int64(fileResult.Encrypted),
		PrivateKeyFilesRewrapped: int64(fileResult.Rewrapped),
		PrivateKeyFilesUnchanged: int64(fileResult.Unchanged),
	})
	if err != nil {
		return nil, err
	}
	return SnapshotFromRow(row), nil
}

func SnapshotFromRow(row db.SecretEncryptionState) *Snapshot {
	return &Snapshot{
		SchemaVersion:            row.SchemaVersion,
		Provider:                 row.Provider,
		CurrentKeyID:             row.CurrentKeyID,
		EncryptionEnabled:        row.EncryptionEnabled != 0,
		EncryptionRequired:       row.EncryptionRequired != 0,
		DatabaseScanned:          row.DatabaseScanned,
		DatabaseEncrypted:        row.DatabaseEncrypted,
		DatabaseRewrapped:        row.DatabaseRewrapped,
		DatabaseUnchanged:        row.DatabaseUnchanged,
		PrivateKeyFilesScanned:   row.PrivateKeyFilesScanned,
		PrivateKeyFilesEncrypted: row.PrivateKeyFilesEncrypted,
		PrivateKeyFilesRewrapped: row.PrivateKeyFilesRewrapped,
		PrivateKeyFilesUnchanged: row.PrivateKeyFilesUnchanged,
		LastReconciledAt:         row.LastReconciledAt,
		UpdatedAt:                row.UpdatedAt,
	}
}

func provider(service *secrets.Service) string {
	if service == nil || !service.Enabled() {
		return ""
	}
	return service.Provider()
}

func currentKeyID(service *secrets.Service) string {
	if service == nil || !service.Enabled() {
		return ""
	}
	return service.CurrentKeyID()
}

func boolToInt64(value bool) int64 {
	if value {
		return 1
	}
	return 0
}
