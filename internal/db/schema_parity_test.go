package db

import (
	"database/sql"
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
	migratedDB := openSchemaParityDB(t, "migrated.db")
	if err := runEmbeddedMigrations(migratedDB); err != nil {
		t.Fatalf("run embedded migrations: %v", err)
	}

	schemaDB := openSchemaParityDB(t, "schema.db")
	schemaSQL, err := os.ReadFile(repoPath(t, "sql", "schema.sql"))
	if err != nil {
		t.Fatalf("read schema.sql: %v", err)
	}
	if _, err := schemaDB.Exec(string(schemaSQL)); err != nil {
		t.Fatalf("apply schema.sql: %v", err)
	}

	got := readNormalizedSchema(t, migratedDB)
	want := readNormalizedSchema(t, schemaDB)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Goose migration schema differs from sql/schema.sql\nmigrated: %#v\nschema:   %#v", got, want)
	}
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
