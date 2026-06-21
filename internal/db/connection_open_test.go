package db

import (
	"context"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNormalizeSQLiteDSNForcesWALAndPrivateCache(t *testing.T) {
	dsn, err := normalizeSQLiteDSN("file:p2pstream.db?cache=shared&mode=rwc")
	if err != nil {
		t.Fatalf("normalize dsn: %v", err)
	}

	_, rawQuery, ok := strings.Cut(dsn, "?")
	if !ok {
		t.Fatalf("expected query params in dsn %q", dsn)
	}
	values, err := url.ParseQuery(rawQuery)
	if err != nil {
		t.Fatalf("parse dsn query: %v", err)
	}
	if values.Get("_journal_mode") != "WAL" {
		t.Fatalf("expected WAL journal mode, got %q", values.Get("_journal_mode"))
	}
	if values.Get("_busy_timeout") != "10000" {
		t.Fatalf("expected 10000 busy timeout, got %q", values.Get("_busy_timeout"))
	}
	if values.Get("cache") != "private" {
		t.Fatalf("expected private cache, got %q", values.Get("cache"))
	}
}

func TestOpenSecuresSQLiteDirectoryAndFiles(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "nested")
	dbPath := filepath.Join(dir, "p2pstream.db")

	database, err := Open(dbPath)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer func() { _ = database.Close() }()

	assertDBMode(t, dir, 0700)
	assertDBMode(t, dbPath, 0600)
	for _, suffix := range []string{"-wal", "-shm"} {
		path := dbPath + suffix
		if _, err := os.Stat(path); err == nil {
			assertDBMode(t, path, 0600)
		} else if !os.IsNotExist(err) {
			t.Fatalf("stat %s: %v", path, err)
		}
	}
}

func TestOpenPreservesExistingSQLiteDirectoryMode(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "loose")
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.Chmod(dir, 0755); err != nil {
		t.Fatalf("chmod setup dir: %v", err)
	}

	database, err := Open(filepath.Join(dir, "p2pstream.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer func() { _ = database.Close() }()

	assertDBMode(t, dir, 0755)
}

func TestOpenConfiguresWALJournalMode(t *testing.T) {
	database, err := Open(filepath.Join(t.TempDir(), "p2pstream-test.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer func() { _ = database.Close() }()

	var journalMode string
	if err := database.QueryRowContext(context.Background(), `PRAGMA journal_mode`).Scan(&journalMode); err != nil {
		t.Fatalf("read journal mode: %v", err)
	}
	if journalMode != "wal" {
		t.Fatalf("expected wal journal mode, got %q", journalMode)
	}

	var busyTimeout int
	if err := database.QueryRowContext(context.Background(), `PRAGMA busy_timeout`).Scan(&busyTimeout); err != nil {
		t.Fatalf("read busy timeout: %v", err)
	}
	if busyTimeout != 10000 {
		t.Fatalf("expected busy timeout 10000, got %d", busyTimeout)
	}
}
