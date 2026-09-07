package db

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pressly/goose/v3"

	"p2pstream/internal/db/migrations"
)

func TestManagedUpdateMigrationRepairsEmptyVersion16Schemas(t *testing.T) {
	wantDB := openSchemaParityDB(t, "schema.db")
	applySchemaSQL(t, wantDB)
	want := readNormalizedSchema(t, wantDB)
	for _, fixture := range []string{"managed_updates_early.sql", "managed_updates_signed.sql"} {
		t.Run(fixture, func(t *testing.T) {
			database := seedManagedUpdateVersion16(t, fixture)
			for i := 0; i < 2; i++ {
				if err := runEmbeddedMigrations(database); err != nil {
					t.Fatalf("migrate version 16 (run %d): %v", i, err)
				}
				assertSchemaParity(t, fixture, readNormalizedSchema(t, database), want)
			}
			var name, keyID string
			if err := database.QueryRow(`SELECT name FROM agents WHERE public_id='existing-agent'`).Scan(&name); err != nil || name != "Existing agent" {
				t.Fatalf("agent changed during migration: name=%q, err=%v", name, err)
			}
			if err := database.QueryRow(`SELECT key_id FROM agent_update_management_authority WHERE id=1`).Scan(&keyID); err != nil || keyID != "existing-pin" {
				t.Fatalf("authority pin changed during migration: key=%q, err=%v", keyID, err)
			}
			var version int64
			if err := database.QueryRow(`SELECT MAX(version_id) FROM goose_db_version WHERE is_applied=1`).Scan(&version); err != nil || version != 17 {
				t.Fatalf("migration version=%d, err=%v", version, err)
			}
			rows, err := database.Query(`PRAGMA foreign_key_check`)
			if err != nil {
				t.Fatal(err)
			}
			defer rows.Close()
			if rows.Next() || rows.Err() != nil {
				t.Fatalf("foreign key check failed: %v", rows.Err())
			}
		})
	}
}

func TestManagedUpdateMigrationPreservesPopulatedIncompatibleSchema(t *testing.T) {
	database := seedManagedUpdateVersion16(t, "managed_updates_early.sql")
	if _, err := database.Exec(`INSERT INTO agent_updater_enrollment_tokens (agent_id,token_hash,expires_at) SELECT id,'existing-token',CURRENT_TIMESTAMP FROM agents WHERE public_id='existing-agent'`); err != nil {
		t.Fatal(err)
	}
	before := readNormalizedSchema(t, database)
	err := runEmbeddedMigrations(database)
	if err == nil || !strings.Contains(err.Error(), "automatic repair requires empty managed-update tables") {
		t.Fatalf("populated incompatible schema error = %v", err)
	}
	assertSchemaParity(t, "unchanged incompatible schema", readNormalizedSchema(t, database), before)
	var token string
	if err := database.QueryRow(`SELECT token_hash FROM agent_updater_enrollment_tokens`).Scan(&token); err != nil || token != "existing-token" {
		t.Fatalf("existing token changed: token=%q, err=%v", token, err)
	}
	var version int64
	if err := database.QueryRow(`SELECT MAX(version_id) FROM goose_db_version WHERE is_applied=1`).Scan(&version); err != nil || version != 16 {
		t.Fatalf("failed migration must not advance version: version=%d, err=%v", version, err)
	}
}

func TestManagedUpdateMigrationLeavesPopulatedCurrentSchemaIntact(t *testing.T) {
	database := seedManagedUpdateVersion16(t, "")
	if _, err := database.Exec(`INSERT INTO agent_updater_enrollment_tokens (agent_id,token_hash,pinned_repository,authority_key_id,authority_epoch,enrollment_generation,expires_at) SELECT id,'existing-token','owner/repo','existing-pin',1,1,CURRENT_TIMESTAMP FROM agents WHERE public_id='existing-agent'`); err != nil {
		t.Fatal(err)
	}
	before := readNormalizedSchema(t, database)
	if err := runEmbeddedMigrations(database); err != nil {
		t.Fatal(err)
	}
	assertSchemaParity(t, "unchanged current schema", readNormalizedSchema(t, database), before)
	var token string
	if err := database.QueryRow(`SELECT token_hash FROM agent_updater_enrollment_tokens`).Scan(&token); err != nil || token != "existing-token" {
		t.Fatalf("existing token changed: token=%q, err=%v", token, err)
	}
}

func seedManagedUpdateVersion16(t *testing.T, fixture string) *sql.DB {
	t.Helper()
	database := openSchemaParityDB(t, "version16.db")
	database.SetMaxOpenConns(1)
	if _, err := database.Exec(`PRAGMA foreign_keys=ON`); err != nil {
		t.Fatal(err)
	}
	provider, err := goose.NewProvider(goose.DialectSQLite3, database, migrations.FS)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := provider.UpTo(context.Background(), 15); err != nil {
		t.Fatal(err)
	}
	var data []byte
	if fixture == "" {
		data, err = migrations.FS.ReadFile("00016_managed_agent_updates.sql")
	} else {
		data, err = os.ReadFile(filepath.Join("testdata", fixture))
	}
	if err != nil {
		t.Fatal(err)
	}
	up, _, _ := strings.Cut(string(data), "-- +goose Down")
	if _, err := database.Exec(up); err != nil {
		t.Fatal(err)
	}
	// Preserve the independent authority pin when present; early builds did not
	// create this table, so also exercise its missing-table upgrade separately.
	if _, err := database.Exec(`
		CREATE TABLE IF NOT EXISTS agent_update_management_authority (
			id INTEGER PRIMARY KEY CHECK(id = 1),
			key_id TEXT NOT NULL UNIQUE,
			public_key BLOB NOT NULL CHECK(length(public_key) = 32),
			epoch INTEGER NOT NULL CHECK(epoch > 0),
			created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
			updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
		);
		INSERT INTO agent_update_management_authority (id,key_id,public_key,epoch) VALUES (1,'existing-pin',zeroblob(32),1);
		INSERT INTO agents (public_id,name,token_hash) VALUES ('existing-agent','Existing agent','agent-token');
		INSERT INTO goose_db_version (version_id,is_applied) VALUES (16,1);
	`); err != nil {
		t.Fatal(err)
	}
	return database
}

func TestManagedUpdateMigrationCreatesMissingAuthorityTable(t *testing.T) {
	database := seedManagedUpdateVersion16(t, "managed_updates_early.sql")
	if _, err := database.Exec(`DROP TABLE agent_update_management_authority`); err != nil {
		t.Fatal(err)
	}
	if err := runEmbeddedMigrations(database); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := database.QueryRow(`SELECT COUNT(*) FROM agent_update_management_authority`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("authority table count=%d, err=%v", count, err)
	}
}
