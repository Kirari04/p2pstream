package server

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"time"
)

const dataLockName = ".p2pstream-runtime.lock"

// DataLease prevents concurrent server processes from writing shared state.
// Closing it releases the lease but never unlinks the stable lock inode.
type DataLease struct {
	file *os.File
	once sync.Once
	err  error
}

func AcquireDataLease(ctx context.Context, dataDir string) (*DataLease, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(dataDir, 0700); err != nil {
		return nil, err
	}
	info, err := os.Lstat(dataDir)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return nil, errors.New("runtime data path is not a directory")
	}
	path := filepath.Join(dataDir, dataLockName)
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_RDWR|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0600)
	created := err == nil
	if errors.Is(err, os.ErrExist) {
		file, err = os.OpenFile(path, os.O_RDWR|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	}
	if err != nil {
		return nil, err
	}
	if created {
		// Give a new lock inode to the data directory owner so subsequent
		// server processes can open it. Never replace or chmod/chown existing locks.
		owner := info.Sys().(*syscall.Stat_t)
		if err := file.Chown(int(owner.Uid), int(owner.Gid)); err != nil {
			_ = file.Close()
			return nil, err
		}
		if err := file.Sync(); err != nil {
			_ = file.Close()
			return nil, err
		}
		if err := syncDirectory(dataDir); err != nil {
			_ = file.Close()
			return nil, err
		}
	}
	lease := &DataLease{file: file}
	info, err = file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0022 != 0 || info.Sys().(*syscall.Stat_t).Nlink != 1 {
		_ = file.Close()
		return nil, errors.New("unsafe runtime data lock")
	}
	for {
		if err := ctx.Err(); err != nil {
			_ = file.Close()
			return nil, err
		}
		err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			// A replaced path must never create a second independently held lock.
			current, statErr := os.Lstat(filepath.Join(dataDir, dataLockName))
			if statErr != nil || !os.SameFile(info, current) {
				_ = lease.Close()
				return nil, errors.New("runtime data lock was replaced")
			}
			return lease, nil
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) && !errors.Is(err, syscall.EAGAIN) {
			_ = file.Close()
			return nil, err
		}
		select {
		case <-ctx.Done():
			_ = file.Close()
			return nil, ctx.Err()
		case <-time.After(20 * time.Millisecond):
		}
	}
}

func (lease *DataLease) Close() error {
	if lease == nil {
		return nil
	}
	lease.once.Do(func() { lease.err = lease.file.Close() })
	return lease.err
}

func syncDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}
