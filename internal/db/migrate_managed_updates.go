package db

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"p2pstream/internal/db/migrations"
)

// Migration 16 was changed during managed-update development without advancing
// its version. Migration 17 repairs empty installations of those older schemas.
// Populated incompatible schemas need an explicit migration of their authority
// and receipt data; never discard that data or invent missing trust bindings.
func repairEmptyManagedUpdateSchema(ctx context.Context, tx *sql.Tx) error {
	checks := []struct {
		table    string
		required []string
		obsolete []string
	}{
		{"agent_update_management_authority", []string{"key_id", "public_key", "epoch"}, nil},
		{"agent_updater_identities", []string{"pinned_repository", "authority_key_id", "authority_epoch", "enrollment_generation", "enrollment_receipt_payload", "enrollment_receipt_signature", "last_command_sequence", "last_root_action_counter"}, []string{"trusted_root_sha256", "trusted_root_version", "last_activation_counter"}},
		{"agent_updater_enrollment_tokens", []string{"pinned_repository", "authority_key_id", "authority_epoch", "enrollment_generation", "receipt_expires_at", "receipt_payload", "receipt_signature"}, []string{"trusted_root_sha256", "trusted_root_version"}},
		{"agent_update_campaigns", []string{"manifest_sha256"}, []string{"root_version"}},
		{"agent_update_assignments", []string{"authorization_action", "command_sequence", "root_action_completed_at", "root_result_kind", "root_result_artifact_sha256", "observed_version", "observed_commit"}, []string{"root_result_root_version"}},
		{"agent_update_events", []string{"assignment_id"}, nil},
	}
	needsRepair := false
	existing := make(map[string]bool)
	for _, check := range checks {
		columns, err := sqliteTxTableColumns(tx, check.table)
		if err != nil {
			return err
		}
		existing[check.table] = len(columns) > 0
		for _, column := range check.required {
			if !sqliteColumnExists(columns, column) {
				needsRepair = true
			}
		}
		for _, column := range check.obsolete {
			if sqliteColumnExists(columns, column) {
				needsRepair = true
			}
		}
	}
	if !needsRepair {
		return nil
	}

	// Child tables come first so foreign-key enforcement stays enabled. Preserve
	// an existing management authority pin even when all other tables are empty.
	tables := []string{"agent_update_events", "agent_update_assignments", "agent_update_campaigns", "agent_updater_enrollment_tokens", "agent_updater_identities"}
	for _, table := range tables {
		if !existing[table] {
			continue
		}
		var count int64
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM `+table).Scan(&count); err != nil {
			return err
		}
		if count != 0 {
			return fmt.Errorf("incompatible managed-update schema: %s contains %d rows; automatic repair requires empty managed-update tables; preserve the database and migrate existing enrollment/campaign state explicitly", table, count)
		}
	}
	for _, table := range tables {
		if _, err := tx.ExecContext(ctx, `DROP TABLE IF EXISTS `+table); err != nil {
			return err
		}
	}
	// Treat this embedded schema as immutable from now on; subsequent changes
	// must use new migration versions instead of editing migration 16 again.
	data, err := migrations.FS.ReadFile("00016_managed_agent_updates.sql")
	if err != nil {
		return err
	}
	up, _, _ := strings.Cut(string(data), "-- +goose Down")
	_, err = tx.ExecContext(ctx, up)
	return err
}
