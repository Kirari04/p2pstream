package serverupdate

import (
	"archive/tar"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"github.com/google/uuid"
	_ "github.com/mattn/go-sqlite3"
	"io"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
)

func backupData(ctx context.Context, source, destination string) (string, error) {
	var size uint64
	err := filepath.WalkDir(source, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.IsDir() && !info.Mode().IsRegular() {
			return errors.New("data contains a link or special file; manual backup required")
		}
		name, err := filepath.Rel(source, path)
		if err != nil || !validBackupName(name) {
			return errors.New("data contains an unsupported filename; manual backup required")
		}
		size += uint64(info.Size()) + 1024
		return nil
	})
	if err != nil {
		return "", err
	}
	var space syscall.Statfs_t
	if err := syscall.Statfs(filepath.Dir(destination), &space); err != nil {
		return "", err
	}
	if space.Bavail*uint64(space.Bsize) < size+size/10+(64<<20) {
		return "", errors.New("insufficient disk space for a verified backup")
	}
	f, err := os.CreateTemp(filepath.Dir(destination), ".backup-*")
	if err != nil {
		return "", err
	}
	defer os.Remove(f.Name())
	defer f.Close()
	hash := sha256.New()
	writer := tar.NewWriter(io.MultiWriter(f, hash))
	err = filepath.WalkDir(source, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.IsDir() && !info.Mode().IsRegular() {
			return errors.New("data changed during backup")
		}
		header, err := tar.FileInfoHeader(info, "")
		if err != nil {
			return err
		}
		header.Name, err = filepath.Rel(source, path)
		if err != nil || !validBackupName(header.Name) {
			return errors.New("data changed to an unsupported filename during backup")
		}
		if err := writer.WriteHeader(header); err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		in, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
		if err != nil {
			return err
		}
		defer in.Close()
		_, err = io.Copy(writer, in)
		return err
	})
	if err != nil {
		return "", err
	}
	if err := writer.Close(); err != nil {
		return "", err
	}
	if err := f.Sync(); err != nil {
		return "", err
	}
	if err := f.Close(); err != nil {
		return "", err
	}
	if err := os.Rename(f.Name(), destination); err != nil {
		return "", err
	}
	if err := syncDirectory(filepath.Dir(destination)); err != nil {
		return "", err
	}
	digest := hex.EncodeToString(hash.Sum(nil))
	if err := verifyBackup(destination, digest); err != nil {
		return "", err
	}
	if err := validateBackup(ctx, destination); err != nil {
		return "", err
	}
	return digest, nil
}

func verifyBackup(path, digest string) error {
	if !digestPattern.MatchString(digest) {
		return errors.New("invalid backup digest")
	}
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return err
	}
	if hex.EncodeToString(h.Sum(nil)) != digest {
		return errors.New("backup checksum mismatch")
	}
	return nil
}

func validBackupName(name string) bool {
	return name == "." || fs.ValidPath(name) && !strings.ContainsAny(name, "\\\x00")
}

// Snapshots produced by backupData list the root and each parent directory
// before its children, exactly once. Enforce that format before extraction so
// unsupported names, entry types, duplicates, and truncated data cannot turn
// an accepted backup into a destructive partial restoration.
func validateBackup(ctx context.Context, path string) error {
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return err
	}
	defer f.Close()
	reader := tar.NewReader(f)
	entries := make(map[string]bool) // true for a directory
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		header, err := reader.Next()
		if err == io.EOF {
			if !entries["."] {
				return errors.New("backup root directory is missing")
			}
			return nil
		}
		if err != nil {
			return err
		}
		name := header.Name
		if !validBackupName(name) || header.Mode < 0 || header.Mode > 07777 || header.Uid < 0 || header.Gid < 0 {
			return errors.New("invalid backup entry")
		}
		if _, exists := entries[name]; exists {
			return errors.New("duplicate backup entry")
		}
		if name == "." {
			if len(entries) != 0 || header.Typeflag != tar.TypeDir {
				return errors.New("invalid backup root directory")
			}
		} else if !entries[filepath.Dir(name)] {
			return errors.New("backup parent directory is missing")
		}
		switch header.Typeflag {
		case tar.TypeDir:
			entries[name] = true
		case tar.TypeReg:
			entries[name] = false
			if _, err := io.Copy(io.Discard, reader); err != nil {
				return err
			}
		default:
			return errors.New("unsupported backup entry")
		}
	}
}

func restoreData(ctx context.Context, destination, backup, digest string) error {
	if err := verifyBackup(backup, digest); err != nil {
		return err
	}
	// A checksum proves identity, not restorability. Validate the complete tar
	// structure before removing even one candidate file.
	if err := validateBackup(ctx, backup); err != nil {
		return err
	}
	// os.Root keeps every removal and extraction inside the fixed data volume,
	// including if the failed candidate left attacker-controlled symlinks.
	root, err := os.OpenRoot(destination)
	if err != nil {
		return err
	}
	defer root.Close()
	entries, err := os.ReadDir(destination)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if err := root.RemoveAll(entry.Name()); err != nil {
			return err
		}
	}
	f, err := os.OpenFile(backup, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return err
	}
	defer f.Close()
	reader := tar.NewReader(f)
	var directories []string
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		header, err := reader.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		name := header.Name
		if !validBackupName(name) {
			return errors.New("invalid backup entry")
		}
		switch header.Typeflag {
		case tar.TypeDir:
			directories = append(directories, name)
			if name != "." {
				if err := root.MkdirAll(name, os.FileMode(header.Mode)&0777); err != nil {
					return err
				}
			}
		case tar.TypeReg:
			out, err := root.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, os.FileMode(header.Mode)&0777)
			if err != nil {
				return err
			}
			_, err = io.Copy(out, reader)
			if err == nil {
				err = out.Sync()
			}
			closeErr := out.Close()
			if err != nil {
				return err
			}
			if closeErr != nil {
				return closeErr
			}
		default:
			return errors.New("unsupported backup entry")
		}
		if err := root.Chown(name, header.Uid, header.Gid); err != nil {
			return err
		}
		if err := root.Chmod(name, os.FileMode(header.Mode)&0777); err != nil {
			return err
		}
		if header.Typeflag == tar.TypeReg {
			saved, err := root.Open(name)
			if err != nil {
				return err
			}
			err = saved.Sync()
			_ = saved.Close()
			if err != nil {
				return err
			}
		}
	}
	for i := len(directories) - 1; i >= 0; i-- {
		dir, err := root.Open(directories[i])
		if err != nil {
			return err
		}
		err = dir.Sync()
		_ = dir.Close()
		if err != nil {
			return err
		}
	}
	return syncDirectory(destination)
}

// Called only after Docker confirms no other container is using /data. Closing
// the read-only connection before archiving preserves a consistent WAL set.
func checkDatabase(ctx context.Context, path string) (resultErr error) {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() {
		return errors.New("SQLite database is missing or is not a regular file")
	}
	// A read-only WAL connection may create shared-memory files. Reject links
	// before SQLite opens them, and preserve application ownership on any new
	// sidecars so a root-run integrity check cannot break the next server start.
	var missing []string
	for _, suffix := range []string{"-wal", "-shm", "-journal"} {
		sidecar := path + suffix
		entry, err := os.Lstat(sidecar)
		if errors.Is(err, os.ErrNotExist) {
			missing = append(missing, sidecar)
			continue
		}
		if err != nil {
			return err
		}
		if !entry.Mode().IsRegular() {
			return errors.New("SQLite sidecar is not a regular file")
		}
	}
	database, err := sql.Open("sqlite3", (&url.URL{Scheme: "file", Path: path, RawQuery: "mode=ro"}).String())
	if err != nil {
		return err
	}
	defer func() {
		closeErr := database.Close()
		if resultErr == nil {
			resultErr = closeErr
		}
		owner := info.Sys().(*syscall.Stat_t)
		for _, sidecar := range missing {
			if _, err := os.Lstat(sidecar); errors.Is(err, os.ErrNotExist) {
				continue
			}
			if err := os.Chown(sidecar, int(owner.Uid), int(owner.Gid)); err != nil {
				if resultErr == nil {
					resultErr = err
				}
				continue
			}
			if err := os.Chmod(sidecar, info.Mode().Perm()&0666); err != nil && resultErr == nil {
				resultErr = err
			}
		}
	}()
	var result string
	if err := database.QueryRowContext(ctx, "PRAGMA quick_check").Scan(&result); err != nil {
		return fmt.Errorf("SQLite backup check failed: %w", err)
	}
	if result != "ok" {
		return errors.New("SQLite backup integrity check failed")
	}
	return nil
}

// Keep the newest three completed snapshots, including the active operation.
// Unknown files and partial archives are left for the host operator to inspect.
func pruneBackups(directory, currentID string) error {
	entries, err := os.ReadDir(directory)
	if err != nil {
		return err
	}
	var backups []fs.FileInfo
	for _, entry := range entries {
		if !strings.HasPrefix(entry.Name(), "backup-") || !strings.HasSuffix(entry.Name(), ".tar") {
			continue
		}
		id := strings.TrimSuffix(strings.TrimPrefix(entry.Name(), "backup-"), ".tar")
		if _, err := uuid.Parse(id); err != nil {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if info.Mode().IsRegular() {
			backups = append(backups, info)
		}
	}
	sort.Slice(backups, func(i, j int) bool { return backups[i].ModTime().After(backups[j].ModTime()) })
	for i, info := range backups {
		if i < 3 || info.Name() == "backup-"+currentID+".tar" {
			continue
		}
		if err := os.Remove(filepath.Join(directory, info.Name())); err != nil {
			return err
		}
	}
	return syncDirectory(directory)
}
