package db

import (
	"context"
	"database/sql"
	"os"
	"sort"
	"testing"
)

func publicRoutesColumnNotNull(t *testing.T, database *DB, column string) bool {
	t.Helper()
	rows, err := database.QueryContext(context.Background(), `PRAGMA table_info(public_routes)`)
	if err != nil {
		t.Fatalf("pragma table_info(public_routes): %v", err)
	}
	defer rows.Close()
	for rows.Next() {
		var cid int64
		var name string
		var columnType string
		var notNull int64
		var defaultValue sql.NullString
		var primaryKey int64
		if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &primaryKey); err != nil {
			t.Fatalf("scan public_routes table_info: %v", err)
		}
		if name == column {
			return notNull != 0
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("read public_routes table_info: %v", err)
	}
	t.Fatalf("public_routes missing column %s", column)
	return false
}

func tableColumns(t *testing.T, database *DB, table string) []string {
	t.Helper()
	rows, err := database.QueryContext(context.Background(), `PRAGMA table_info(`+table+`)`)
	if err != nil {
		t.Fatalf("table info %s: %v", table, err)
	}
	defer rows.Close()
	var columns []string
	for rows.Next() {
		var cid int
		var name string
		var typ string
		var notNull int
		var defaultValue any
		var pk int
		if err := rows.Scan(&cid, &name, &typ, &notNull, &defaultValue, &pk); err != nil {
			t.Fatalf("scan table info: %v", err)
		}
		columns = append(columns, name)
	}
	sort.Strings(columns)
	return columns
}

func containsString(values []string, needle string) bool {
	for _, value := range values {
		if value == needle {
			return true
		}
	}
	return false
}

func tableExists(t *testing.T, database *DB, table string) bool {
	t.Helper()
	var count int64
	if err := database.QueryRowContext(context.Background(), `SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = ?`, table).Scan(&count); err != nil {
		t.Fatalf("check table %s exists: %v", table, err)
	}
	return count > 0
}

func countRows(t *testing.T, database *DB, query string, args ...any) int64 {
	t.Helper()
	var count int64
	if err := database.QueryRowContext(context.Background(), query, args...).Scan(&count); err != nil {
		t.Fatalf("count rows with %q: %v", query, err)
	}
	return count
}

func assertForeignKeyCheck(t *testing.T, database *DB) {
	t.Helper()
	rows, err := database.QueryContext(context.Background(), `PRAGMA foreign_key_check`)
	if err != nil {
		t.Fatalf("foreign key check: %v", err)
	}
	defer rows.Close()
	if rows.Next() {
		t.Fatal("foreign key check returned violations")
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("read foreign key check rows: %v", err)
	}
}

func indexExists(t *testing.T, database *DB, name string) bool {
	t.Helper()
	var got string
	err := database.QueryRowContext(context.Background(), `SELECT name FROM sqlite_master WHERE type = 'index' AND name = ?`, name).Scan(&got)
	if err == sql.ErrNoRows {
		return false
	}
	if err != nil {
		t.Fatalf("query index %s: %v", name, err)
	}
	return got == name
}

func assertDBMode(t *testing.T, path string, want os.FileMode) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	if got := info.Mode().Perm(); got != want {
		t.Fatalf("%s mode = %o, want %o", path, got, want)
	}
}
