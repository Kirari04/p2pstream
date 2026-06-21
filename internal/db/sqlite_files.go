package db

import (
	"fmt"
	"os"
	"path/filepath"
)

func ensureSQLiteDir(dsn string) error {
	path, ok := sqliteFilePathFromDSN(dsn)
	if !ok {
		return nil
	}
	dir := filepath.Dir(path)
	if dir == "." || dir == "" {
		return nil
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	return nil
}

func hardenSQLiteFiles(dsn string) error {
	path, ok := sqliteFilePathFromDSN(dsn)
	if !ok {
		return nil
	}
	for _, candidate := range []string{path, path + "-wal", path + "-shm"} {
		if _, err := os.Stat(candidate); err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return fmt.Errorf("failed to stat sqlite file %q: %w", candidate, err)
		}
		if err := os.Chmod(candidate, 0600); err != nil {
			return fmt.Errorf("failed to secure sqlite file %q: %w", candidate, err)
		}
	}
	return nil
}
