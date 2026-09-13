package serverupdate

import (
	"context"
	"database/sql"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestBackupRestoresDatabaseWALAndAuthorityTogether(t *testing.T) {
	data := t.TempDir()
	backup := filepath.Join(t.TempDir(), "backup.tar")
	files := map[string]string{"p2pstream.db": "old schema", "p2pstream.db-wal": "committed pages", "agent-update-management-authority.json": "original authority", "certs/management/ca.key.pem": "original CA"}
	for name, value := range files {
		path := filepath.Join(data, name)
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(value), 0600); err != nil {
			t.Fatal(err)
		}
	}
	digest, err := backupData(context.Background(), data, backup)
	if err != nil {
		t.Fatal(err)
	}
	for name := range files {
		if err := os.WriteFile(filepath.Join(data, name), []byte("candidate data"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(data, "candidate-only"), []byte("remove"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := restoreData(context.Background(), data, backup, digest); err != nil {
		t.Fatal(err)
	}
	for name, value := range files {
		got, err := os.ReadFile(filepath.Join(data, name))
		if err != nil || string(got) != value {
			t.Fatalf("%s not restored: %s %v", name, got, err)
		}
	}
	if _, err := os.Stat(filepath.Join(data, "candidate-only")); !os.IsNotExist(err) {
		t.Fatal("candidate state survived restoration")
	}
}

func TestRestoreRejectsCorruptBackupBeforeDeletingData(t *testing.T) {
	data := t.TempDir()
	backup := filepath.Join(t.TempDir(), "backup.tar")
	path := filepath.Join(data, "state")
	_ = os.WriteFile(path, []byte("keep"), 0600)
	digest, err := backupData(context.Background(), data, backup)
	if err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(backup, []byte("corrupt"), 0600)
	if err := restoreData(context.Background(), data, backup, digest); err == nil {
		t.Fatal("accepted corrupt backup")
	}
	got, _ := os.ReadFile(path)
	if string(got) != "keep" {
		t.Fatal("deleted data before backup verification")
	}
}

func TestBackupRejectsSymlinksAndRestoreCannotEscape(t *testing.T) {
	data := t.TempDir()
	outside := t.TempDir()
	backup := filepath.Join(t.TempDir(), "backup.tar")
	_ = os.WriteFile(filepath.Join(outside, "keep"), []byte("outside"), 0600)
	_ = os.Symlink(outside, filepath.Join(data, "link"))
	if _, err := backupData(context.Background(), data, backup); err == nil {
		t.Fatal("followed application-controlled symlink")
	}
	_ = os.Remove(filepath.Join(data, "link"))
	_ = os.WriteFile(filepath.Join(data, "keep"), []byte("inside"), 0600)
	digest, err := backupData(context.Background(), data, backup)
	if err != nil {
		t.Fatal(err)
	}
	_ = os.Remove(filepath.Join(data, "keep"))
	_ = os.Symlink(filepath.Join(outside, "keep"), filepath.Join(data, "keep"))
	if err := restoreData(context.Background(), data, backup, digest); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(filepath.Join(outside, "keep"))
	if string(got) != "outside" {
		t.Fatal("modified a path outside the volume")
	}
}

func TestSQLiteCheckAndSnapshotRestoreRealWALDatabase(t *testing.T) {
	ctx := context.Background()
	source := t.TempDir()
	path := filepath.Join(source, "p2pstream.db")
	database, err := sql.Open("sqlite3", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = database.Exec("PRAGMA journal_mode=WAL; CREATE TABLE value(v TEXT); INSERT INTO value VALUES('last-good')"); err != nil {
		t.Fatal(err)
	}
	database.Close()
	if err := checkDatabase(ctx, path); err != nil {
		t.Fatal(err)
	}
	backup := filepath.Join(t.TempDir(), "snapshot.tar")
	digest, err := backupData(ctx, source, backup)
	if err != nil {
		t.Fatal(err)
	}
	database, _ = sql.Open("sqlite3", path)
	_, err = database.Exec("ALTER TABLE value ADD COLUMN migrated INTEGER; UPDATE value SET v='candidate'")
	if err != nil {
		t.Fatal(err)
	}
	database.Close()
	if err := restoreData(ctx, source, backup, digest); err != nil {
		t.Fatal(err)
	}
	if err := checkDatabase(ctx, path); err != nil {
		t.Fatal(err)
	}
	database, _ = sql.Open("sqlite3", path)
	defer database.Close()
	var value string
	if err := database.QueryRow("SELECT v FROM value").Scan(&value); err != nil || value != "last-good" {
		t.Fatalf("restored value: %q %v", value, err)
	}
	if _, err := database.Exec("SELECT migrated FROM value"); err == nil {
		t.Fatal("candidate migration survived restore")
	}
	corrupt := filepath.Join(t.TempDir(), "p2pstream.db")
	if err := os.WriteFile(corrupt, []byte("corrupt database"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := checkDatabase(ctx, corrupt); err == nil {
		t.Fatal("accepted corrupt SQLite")
	}
}

func TestDatabaseIntegrityCheckRejectsSidecarSymlinks(t *testing.T) {
	for _, suffix := range []string{"-wal", "-shm", "-journal"} {
		t.Run(suffix, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "p2pstream.db")
			database, err := sql.Open("sqlite3", path)
			if err != nil {
				t.Fatal(err)
			}
			_, err = database.Exec("CREATE TABLE value(v TEXT)")
			if err != nil {
				t.Fatal(err)
			}
			database.Close()
			protected := filepath.Join(t.TempDir(), "protected")
			if err := os.WriteFile(protected, []byte("untouched"), 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(protected, path+suffix); err != nil {
				t.Fatal(err)
			}
			if err := checkDatabase(context.Background(), path); err == nil {
				t.Fatal("followed SQLite sidecar symlink")
			}
			data, err := os.ReadFile(protected)
			if err != nil || string(data) != "untouched" {
				t.Fatal("changed file outside data directory")
			}
		})
	}
}

func TestCheckDatabaseWithUncheckpointedWAL(t *testing.T) {
	const variable = "P2PSTREAM_TEST_CRASHED_WAL"
	if path := os.Getenv(variable); path != "" {
		database, err := sql.Open("sqlite3", path)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := database.Exec("PRAGMA journal_mode=WAL; CREATE TABLE value(v TEXT); INSERT INTO value VALUES('committed-in-wal')"); err != nil {
			t.Fatal(err)
		}
		// Simulate process loss without closing SQLite/checkpointing its WAL.
		os.Exit(0)
	}
	source := t.TempDir()
	path := filepath.Join(source, "p2pstream.db")
	child := exec.Command(os.Args[0], "-test.run=^TestCheckDatabaseWithUncheckpointedWAL$")
	child.Env = append(os.Environ(), variable+"="+path)
	if out, err := child.CombinedOutput(); err != nil {
		t.Fatalf("create interrupted database: %v %s", err, out)
	}
	if info, err := os.Stat(path + "-wal"); err != nil || info.Size() == 0 {
		t.Fatal("missing uncheckpointed WAL")
	}
	if err := os.Remove(path + "-shm"); err != nil {
		t.Fatal(err)
	}
	if err := checkDatabase(context.Background(), path); err != nil {
		t.Fatal(err)
	}
	archive := filepath.Join(t.TempDir(), "snapshot.tar")
	digest, err := backupData(context.Background(), source, archive)
	if err != nil {
		t.Fatal(err)
	}
	restored := t.TempDir()
	if err := restoreData(context.Background(), restored, archive, digest); err != nil {
		t.Fatal(err)
	}
	database, err := sql.Open("sqlite3", filepath.Join(restored, "p2pstream.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	var value string
	if err := database.QueryRow("SELECT v FROM value").Scan(&value); err != nil || value != "committed-in-wal" {
		t.Fatalf("WAL transaction was lost: %q %v", value, err)
	}
}
