package db

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	_ "github.com/mattn/go-sqlite3"
)

type schemaObject struct {
	Type  string
	Name  string
	Table string
	SQL   string
}

func TestSchemaParity(t *testing.T) {
	schemaDB := openSchemaParityDB(t, "schema.db")
	applySchemaSQL(t, schemaDB)
	want := readNormalizedSchema(t, schemaDB)

	tests := []struct {
		name  string
		setup func(t *testing.T, database *sql.DB)
	}{
		{
			name: "Goose migration",
			setup: func(t *testing.T, database *sql.DB) {
				t.Helper()
				if err := runEmbeddedMigrations(database); err != nil {
					t.Fatalf("run embedded migrations: %v", err)
				}
			},
		},
		{
			name: "legacy migration",
			setup: func(t *testing.T, database *sql.DB) {
				t.Helper()
				instance := &DB{
					DB:      database,
					Queries: New(database),
				}
				if err := instance.migrate(); err != nil {
					t.Fatalf("run legacy migrations: %v", err)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			databaseName := strings.ToLower(strings.ReplaceAll(tt.name, " ", "-")) + ".db"
			database := openSchemaParityDB(t, databaseName)
			tt.setup(t, database)
			assertSchemaParity(t, tt.name, readNormalizedSchema(t, database), want)
		})
	}
}

func applySchemaSQL(t *testing.T, database *sql.DB) {
	t.Helper()

	schemaSQL, err := os.ReadFile(repoPath(t, "sql", "schema.sql"))
	if err != nil {
		t.Fatalf("read schema.sql: %v", err)
	}
	if _, err := database.Exec(string(schemaSQL)); err != nil {
		t.Fatalf("apply schema.sql: %v", err)
	}
}

func assertSchemaParity(t *testing.T, label string, got, want []schemaObject) {
	t.Helper()

	if reflect.DeepEqual(got, want) {
		return
	}
	index := firstSchemaDifference(got, want)
	t.Fatalf("%s schema differs from sql/schema.sql at object %d\n%s: %s\nsql/schema.sql: %s", label, index, label, formatSchemaObject(got, index), formatSchemaObject(want, index))
}

func firstSchemaDifference(got, want []schemaObject) int {
	limit := len(got)
	if len(want) < limit {
		limit = len(want)
	}
	for i := 0; i < limit; i++ {
		if got[i] != want[i] {
			return i
		}
	}
	return limit
}

func formatSchemaObject(objects []schemaObject, index int) string {
	if index >= len(objects) {
		return "<missing>"
	}
	object := objects[index]
	return fmt.Sprintf("%s %s on %s: %s", object.Type, object.Name, object.Table, object.SQL)
}

func openSchemaParityDB(t *testing.T, name string) *sql.DB {
	t.Helper()

	database, err := sql.Open("sqlite3", filepath.Join(t.TempDir(), name))
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = database.Close() })
	return database
}

func readNormalizedSchema(t *testing.T, database *sql.DB) []schemaObject {
	t.Helper()

	rows, err := database.Query(`
		SELECT type, name, tbl_name, sql
		FROM sqlite_master
		WHERE name NOT LIKE 'sqlite_%'
		  AND name != 'goose_db_version'
		ORDER BY type, name
	`)
	if err != nil {
		t.Fatalf("query sqlite_master: %v", err)
	}
	defer rows.Close()

	var objects []schemaObject
	for rows.Next() {
		var object schemaObject
		if err := rows.Scan(&object.Type, &object.Name, &object.Table, &object.SQL); err != nil {
			t.Fatalf("scan sqlite_master: %v", err)
		}
		object.SQL = normalizeSchemaSQL(object.SQL)
		objects = append(objects, object)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate sqlite_master: %v", err)
	}
	return objects
}

func normalizeSchemaSQL(value string) string {
	// Keep normalization whitespace-only so parity failures catch schema drift.
	return strings.Join(strings.Fields(value), " ")
}

func repoPath(t *testing.T, elements ...string) string {
	t.Helper()

	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve test file path")
	}
	parts := append([]string{filepath.Dir(filename), "..", ".."}, elements...)
	return filepath.Clean(filepath.Join(parts...))
}
