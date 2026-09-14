package db

import (
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
)

func TestOpenRejectsUnversionedDatabaseWithoutChangingSchemaOrData(t *testing.T) {
	for _, tt := range []struct {
		name       string
		history    string
		wantRows   int
		hasHistory bool
	}{
		{name: "no history"},
		{name: "empty history", hasHistory: true},
		{name: "unapplied baseline", hasHistory: true, history: `(0, 1)`, wantRows: 1},
		{name: "reverted baseline", hasHistory: true, history: `(0, 1), (1, 1), (1, 0)`, wantRows: 3},
	} {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "unversioned.db")
			raw, err := sql.Open("sqlite3", path)
			if err != nil {
				t.Fatal(err)
			}
			defer raw.Close()
			if _, err := raw.Exec(`
				CREATE TABLE agents (id INTEGER PRIMARY KEY, name TEXT NOT NULL);
				INSERT INTO agents VALUES (1, 'existing-agent');
			`); err != nil {
				t.Fatal(err)
			}
			if tt.hasHistory {
				if _, err := raw.Exec(`CREATE TABLE goose_db_version (
					id INTEGER PRIMARY KEY AUTOINCREMENT,
					version_id INTEGER NOT NULL,
					is_applied INTEGER NOT NULL,
					tstamp TIMESTAMP DEFAULT (datetime('now'))
				)`); err != nil {
					t.Fatal(err)
				}
				if tt.history != "" {
					if _, err := raw.Exec(`INSERT INTO goose_db_version (version_id, is_applied) VALUES ` + tt.history); err != nil {
						t.Fatal(err)
					}
				}
			}
			before := readNormalizedSchema(t, raw)
			for range 2 {
				database, err := Open(path)
				if database != nil {
					database.Close()
					t.Fatal("opened an unsupported database")
				}
				if err == nil || !strings.Contains(err.Error(), "unsupported unversioned database") {
					t.Fatalf("open error = %v", err)
				}
				assertSchemaParity(t, "rejected database", readNormalizedSchema(t, raw), before)
				var name string
				if err := raw.QueryRow(`SELECT name FROM agents WHERE id = 1`).Scan(&name); err != nil || name != "existing-agent" {
					t.Fatalf("existing agent changed: name=%q, err=%v", name, err)
				}
				if got := tableExists(t, &DB{DB: raw}, "goose_db_version"); got != tt.hasHistory {
					t.Fatalf("migration history existence = %v, want %v", got, tt.hasHistory)
				}
				if tt.hasHistory {
					var count int
					if err := raw.QueryRow(`SELECT COUNT(*) FROM goose_db_version`).Scan(&count); err != nil || count != tt.wantRows {
						t.Fatalf("migration history changed: rows=%d, err=%v", count, err)
					}
				}
			}
		})
	}
}

func TestOpenPreservesVersionedDatabase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "current.db")
	database, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if database != nil {
			_ = database.Close()
		}
	}()
	if _, err := database.Exec(`
		INSERT INTO agents (public_id, name, token_hash) VALUES ('existing-agent', 'Existing agent', 'token');
		INSERT INTO proxy_request_events (status_code, duration_ms) VALUES (200, 42);
	`); err != nil {
		t.Fatal(err)
	}
	before := readNormalizedSchema(t, database.DB)
	var historyRows int
	if err := database.QueryRow(`SELECT COUNT(*) FROM goose_db_version`).Scan(&historyRows); err != nil {
		t.Fatal(err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	database, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	assertSchemaParity(t, "reopened database", readNormalizedSchema(t, database.DB), before)
	var name string
	if err := database.QueryRow(`SELECT name FROM agents WHERE public_id = 'existing-agent'`).Scan(&name); err != nil || name != "Existing agent" {
		t.Fatalf("existing agent changed: name=%q, err=%v", name, err)
	}
	var duration int
	if err := database.QueryRow(`SELECT duration_ms FROM proxy_request_events`).Scan(&duration); err != nil || duration != 42 {
		t.Fatalf("existing event changed: duration=%d, err=%v", duration, err)
	}
	var count int
	if err := database.QueryRow(`SELECT COUNT(*) FROM goose_db_version`).Scan(&count); err != nil || count != historyRows {
		t.Fatalf("migration history changed: rows=%d, want=%d, err=%v", count, historyRows, err)
	}
	assertForeignKeyCheck(t, database)
}
