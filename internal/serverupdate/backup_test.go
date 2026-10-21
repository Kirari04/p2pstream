package serverupdate

import (
	"archive/tar"
	"bytes"
	"context"
	"database/sql"
	"errors"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

type preflightInfoErrorEntry struct {
	fs.DirEntry
	err error
}

func (e preflightInfoErrorEntry) Info() (fs.FileInfo, error) { return nil, e.err }

func TestLiveBackupPreflightContinuesAfterVanishedChild(t *testing.T) {
	for _, disappearance := range []string{"walk", "info"} {
		for _, outcome := range []string{"unsupported", "insufficient-space", "space-error", "success", "strict"} {
			t.Run(disappearance+"/"+outcome, func(t *testing.T) {
				source, state := t.TempDir(), t.TempDir()
				rootInfo, err := os.Lstat(source)
				if err != nil {
					t.Fatal(err)
				}
				late := filepath.Join(source, "z-later")
				if outcome == "unsupported" {
					err = os.Symlink(t.TempDir(), late)
				} else {
					err = os.WriteFile(late, make([]byte, 8192), 0600)
				}
				if err != nil {
					t.Fatal(err)
				}
				lateInfo, err := os.Lstat(late)
				if err != nil {
					t.Fatal(err)
				}
				missingPath := filepath.Join(source, "a-vanished")
				missingErr := &fs.PathError{Op: "lstat", Path: missingPath, Err: fs.ErrNotExist}
				lateVisited, spaceChecked := false, false
				filesystem := backupPreflightIO{
					walkDir: func(root string, visit fs.WalkDirFunc) error {
						if root != source {
							t.Fatalf("unexpected root %q", root)
						}
						if err := visit(source, fs.FileInfoToDirEntry(rootInfo), nil); err != nil {
							return err
						}
						var err error
						if disappearance == "walk" {
							err = visit(missingPath, nil, missingErr)
						} else {
							err = visit(missingPath, preflightInfoErrorEntry{DirEntry: fs.FileInfoToDirEntry(lateInfo), err: missingErr}, nil)
						}
						if err != nil {
							return err
						}
						lateVisited = true
						return visit(late, fs.FileInfoToDirEntry(lateInfo), nil)
					},
					statfs: func(path string, space *syscall.Statfs_t) error {
						spaceChecked = true
						if path != state {
							t.Fatalf("unexpected space-check path %q", path)
						}
						if outcome == "space-error" {
							return fs.ErrPermission
						}
						space.Bsize = 1
						space.Bavail = 1 << 30
						if outcome == "insufficient-space" {
							// Enough for the root alone, but not the later file: the
							// scan must keep counting after skipping the missing child.
							rootSize := uint64(rootInfo.Size()) + 1024
							space.Bavail = rootSize + rootSize/10 + (64 << 20) + 1
						}
						return nil
					},
				}
				err = scanBackupPreflight(context.Background(), source, state, outcome != "strict", filesystem)
				if outcome == "strict" {
					if !errors.Is(err, fs.ErrNotExist) || lateVisited || spaceChecked {
						t.Fatalf("strict scan tolerated disappearance: %v, later=%v space=%v", err, lateVisited, spaceChecked)
					}
					return
				}
				if !lateVisited {
					t.Fatal("vanished child prevented later entry validation")
				}
				switch outcome {
				case "unsupported":
					if err == nil || !strings.Contains(err.Error(), "link or special file") || spaceChecked {
						t.Fatalf("later unsupported entry was masked: %v, space=%v", err, spaceChecked)
					}
				case "insufficient-space":
					if err == nil || !strings.Contains(err.Error(), "insufficient disk space") || !spaceChecked {
						t.Fatalf("later file size or space check was masked: %v, space=%v", err, spaceChecked)
					}
				case "space-error":
					if !errors.Is(err, fs.ErrPermission) || !spaceChecked {
						t.Fatalf("space-check failure was masked: %v, space=%v", err, spaceChecked)
					}
				case "success":
					if err != nil || !spaceChecked {
						t.Fatalf("successful live scan skipped space check: %v, space=%v", err, spaceChecked)
					}
				}
			})
		}
	}
}

func TestLiveBackupPreflightOnlyToleratesVanishedChildren(t *testing.T) {
	for _, name := range []string{"child", "missing-source", "missing-state", "regular-source", "regular-state", "root-error", "outside-error", "permission", "canceled", "unknown-not-exist"} {
		t.Run(name, func(t *testing.T) {
			source, state := t.TempDir(), t.TempDir()
			ctx := context.Background()
			preflightErr := error(&fs.PathError{Op: "lstat", Path: filepath.Join(source, "cache", "vanished"), Err: fs.ErrNotExist})
			switch name {
			case "missing-source", "regular-source":
				if err := os.Remove(source); err != nil {
					t.Fatal(err)
				}
				if name == "regular-source" {
					if err := os.WriteFile(source, nil, 0600); err != nil {
						t.Fatal(err)
					}
				}
			case "missing-state", "regular-state":
				if err := os.Remove(state); err != nil {
					t.Fatal(err)
				}
				if name == "regular-state" {
					if err := os.WriteFile(state, nil, 0600); err != nil {
						t.Fatal(err)
					}
				}
			case "root-error":
				preflightErr = &fs.PathError{Op: "lstat", Path: source, Err: fs.ErrNotExist}
			case "outside-error":
				preflightErr = &fs.PathError{Op: "lstat", Path: filepath.Join(t.TempDir(), "vanished"), Err: fs.ErrNotExist}
			case "permission":
				preflightErr = &fs.PathError{Op: "lstat", Path: filepath.Join(source, "private"), Err: fs.ErrPermission}
			case "canceled":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			case "unknown-not-exist":
				preflightErr = fs.ErrNotExist
			}
			err := liveBackupPreflightResult(ctx, source, state, preflightErr)
			if name == "child" {
				if err != nil {
					t.Fatalf("live child disappearance should be inconclusive: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatal("preflight failure was ignored")
			}
			if name == "canceled" && !errors.Is(err, context.Canceled) {
				t.Fatalf("canceled preflight = %v", err)
			}
		})
	}
}

func TestRestorePreservesRuntimeGuardAcrossCurrentAndHistoricalSnapshots(t *testing.T) {
	for _, historical := range []bool{false, true} {
		name := "current"
		if historical {
			name = "historical"
		}
		t.Run(name, func(t *testing.T) {
			data := t.TempDir()
			lease, err := AcquireRuntimeLease(context.Background(), data)
			if err != nil {
				t.Fatal(err)
			}
			defer lease.Close()
			lockBefore, err := os.Lstat(filepath.Join(data, RuntimeLockName))
			if err != nil {
				t.Fatal(err)
			}
			owner := runtimeTestOwner()
			owner.Allowed = false
			if err := WriteRuntimeOwner(data, owner); err != nil {
				t.Fatal(err)
			}
			ownerBefore, err := os.ReadFile(filepath.Join(data, RuntimeOwnerName))
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(data, "value"), []byte("last-good"), 0600); err != nil {
				t.Fatal(err)
			}
			archive := filepath.Join(t.TempDir(), "snapshot.tar")
			digest, err := backupData(context.Background(), data, archive)
			if err != nil {
				t.Fatal(err)
			}
			contents, err := os.ReadFile(archive)
			if err != nil {
				t.Fatal(err)
			}
			reader := tar.NewReader(bytes.NewReader(contents))
			for {
				header, err := reader.Next()
				if err == io.EOF {
					break
				}
				if err != nil {
					t.Fatal(err)
				}
				if IsRuntimeReservedName(header.Name) {
					t.Fatalf("new snapshot included %q", header.Name)
				}
			}
			if historical {
				var buffer bytes.Buffer
				writer := tar.NewWriter(&buffer)
				if err := writer.WriteHeader(&tar.Header{Name: ".", Typeflag: tar.TypeDir, Mode: 0700, Uid: os.Getuid(), Gid: os.Getgid()}); err != nil {
					t.Fatal(err)
				}
				for _, entry := range []struct{ name, body string }{{RuntimeLockName, "obsolete lock"}, {RuntimeOwnerName, `{"api":2,"allowed":true}`}, {"value", "last-good"}} {
					if err := writer.WriteHeader(&tar.Header{Name: entry.name, Typeflag: tar.TypeReg, Mode: 0600, Uid: os.Getuid(), Gid: os.Getgid(), Size: int64(len(entry.body))}); err != nil {
						t.Fatal(err)
					}
					if _, err := writer.Write([]byte(entry.body)); err != nil {
						t.Fatal(err)
					}
				}
				if err := writer.Close(); err != nil {
					t.Fatal(err)
				}
				contents = buffer.Bytes()
				if err := os.WriteFile(archive, contents, 0600); err != nil {
					t.Fatal(err)
				}
				digest = hash(contents)
			}
			if err := os.WriteFile(filepath.Join(data, "value"), []byte("candidate"), 0600); err != nil {
				t.Fatal(err)
			}
			if err := restoreData(context.Background(), data, archive, digest); err != nil {
				t.Fatal(err)
			}
			lockAfter, err := os.Lstat(filepath.Join(data, RuntimeLockName))
			if err != nil || !os.SameFile(lockBefore, lockAfter) {
				t.Fatalf("restore replaced held lock: %v", err)
			}
			ownerAfter, err := os.ReadFile(filepath.Join(data, RuntimeOwnerName))
			if err != nil || !bytes.Equal(ownerBefore, ownerAfter) {
				t.Fatalf("restore changed admission marker: %q, %v", ownerAfter, err)
			}
			value, err := os.ReadFile(filepath.Join(data, "value"))
			if err != nil || string(value) != "last-good" {
				t.Fatalf("data not restored: %q, %v", value, err)
			}
		})
	}
}

func TestBackupVerificationChecksAllPersistedBytesBeforeRestore(t *testing.T) {
	var archive bytes.Buffer
	writer := tar.NewWriter(&archive)
	if err := writer.WriteHeader(&tar.Header{Name: ".", Typeflag: tar.TypeDir, Mode: 0700, Uid: os.Getuid(), Gid: os.Getgid()}); err != nil {
		t.Fatal(err)
	}
	const body = "last-good"
	if err := writer.WriteHeader(&tar.Header{Name: "value", Typeflag: tar.TypeReg, Mode: 0600, Size: int64(len(body)), Uid: os.Getuid(), Gid: os.Getgid()}); err != nil {
		t.Fatal(err)
	}
	if _, err := writer.Write([]byte(body)); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	// A tar reader ignores bytes after its terminator. They still belong to the
	// protected file and must be included in the recorded and verified digest.
	original := append(append([]byte(nil), archive.Bytes()...), []byte("bound trailer")...)
	for _, name := range []string{"valid", "body", "file-padding", "trailer", "appended", "truncated-body", "truncated-header", "canceled"} {
		t.Run(name, func(t *testing.T) {
			contents := append([]byte(nil), original...)
			digest := hash(original)
			ctx := context.Background()
			switch name {
			case "body":
				contents[1024] ^= 1
			case "file-padding":
				contents[1024+len(body)] ^= 1
			case "trailer":
				contents[len(contents)-1] ^= 1
			case "appended":
				contents = append(contents, 'x')
			case "truncated-body":
				contents = contents[:1024+len(body)-1]
				digest = hash(contents) // A matching checksum cannot excuse invalid tar.
			case "truncated-header":
				contents = contents[:700]
				digest = hash(contents)
			case "canceled":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			}
			path := filepath.Join(t.TempDir(), "snapshot.tar")
			if err := os.WriteFile(path, contents, 0600); err != nil {
				t.Fatal(err)
			}
			data := t.TempDir()
			keep := filepath.Join(data, "candidate-only")
			if err := os.WriteFile(keep, []byte("keep until verified"), 0600); err != nil {
				t.Fatal(err)
			}
			err := restoreData(ctx, data, path, digest)
			if name == "valid" {
				if err != nil {
					t.Fatal(err)
				}
				got, err := os.ReadFile(filepath.Join(data, "value"))
				if err != nil || string(got) != body {
					t.Fatalf("restored body = %q, %v", got, err)
				}
				return
			}
			if err == nil {
				t.Fatal("unverified snapshot restored")
			}
			if name == "canceled" && !errors.Is(err, context.Canceled) {
				t.Fatalf("canceled restore = %v", err)
			}
			got, readErr := os.ReadFile(keep)
			if readErr != nil || string(got) != "keep until verified" {
				t.Fatalf("candidate changed before verification: %q, %v", got, readErr)
			}
		})
	}
}

func TestBackupVerificationRefusesArchiveSymlink(t *testing.T) {
	data := t.TempDir()
	if err := os.WriteFile(filepath.Join(data, "value"), []byte("last-good"), 0600); err != nil {
		t.Fatal(err)
	}
	archive := filepath.Join(t.TempDir(), "snapshot.tar")
	digest, err := backupData(context.Background(), data, archive)
	if err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(t.TempDir(), "linked.tar")
	if err := os.Symlink(archive, link); err != nil {
		t.Fatal(err)
	}
	if err := restoreData(context.Background(), data, link, digest); err == nil {
		t.Fatal("followed archive symlink")
	}
	got, err := os.ReadFile(filepath.Join(data, "value"))
	if err != nil || string(got) != "last-good" {
		t.Fatalf("candidate changed before verification: %q, %v", got, err)
	}
}

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
