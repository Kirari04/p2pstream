package db

import (
	"context"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
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
	if values.Get("_txlock") != "immediate" {
		t.Fatalf("expected immediate transaction locking, got %q", values.Get("_txlock"))
	}
	if values.Get("cache") != "private" {
		t.Fatalf("expected private cache, got %q", values.Get("cache"))
	}
}

func TestOpenReadThenWriteTransactionDoesNotLoseToConcurrentWriter(t *testing.T) {
	database, err := Open(filepath.Join(t.TempDir(), "transaction-lock-test.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer func() { _ = database.Close() }()
	if _, err := database.Exec(`CREATE TABLE transaction_lock_test (id INTEGER PRIMARY KEY, value TEXT NOT NULL)`); err != nil {
		t.Fatalf("create test table: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	tx, err := database.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin transaction: %v", err)
	}
	defer tx.Rollback()
	var count int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM transaction_lock_test`).Scan(&count); err != nil {
		t.Fatalf("establish read snapshot: %v", err)
	}

	writerDone := make(chan error, 1)
	go func() {
		_, err := database.ExecContext(ctx, `INSERT INTO transaction_lock_test (id, value) VALUES (2, 'concurrent')`)
		writerDone <- err
	}()

	writerFinished := false
	select {
	case err := <-writerDone:
		writerFinished = true
		if err != nil {
			t.Fatalf("concurrent writer: %v", err)
		}
	case <-time.After(50 * time.Millisecond):
	}

	if _, err := tx.ExecContext(ctx, `INSERT INTO transaction_lock_test (id, value) VALUES (1, 'transaction')`); err != nil {
		t.Fatalf("write after read snapshot: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit transaction: %v", err)
	}
	if !writerFinished {
		select {
		case err := <-writerDone:
			if err != nil {
				t.Fatalf("concurrent writer after commit: %v", err)
			}
		case <-ctx.Done():
			t.Fatalf("concurrent writer did not finish: %v", ctx.Err())
		}
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
