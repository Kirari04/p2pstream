package serverupdate

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/google/uuid"
	"p2pstream/internal/releaseversion"
)

const (
	RuntimeProtocol      = 2
	RuntimeLockName      = ".p2pstream-runtime.lock"
	RuntimeOwnerName     = ".p2pstream-runtime-owner.json"
	RuntimeControlDir    = "/run/p2pstream-server-update"
	RuntimeTokenFile     = RuntimeControlDir + "/token"
	RuntimeControlSocket = RuntimeControlDir + "/control.sock"
	RuntimeGateFile      = RuntimeControlDir + "/maintenance"
)

// RuntimeOwner survives rollback. Its generation identifies the only server
// configuration that may write this data volume after a deployment change.
type RuntimeOwner struct {
	API        int    `json:"api"`
	InstanceID string `json:"instance_id"`
	Generation string `json:"generation"`
	Allowed    bool   `json:"allowed"`
	Version    string `json:"version"`
	Commit     string `json:"commit"`
}

type RuntimeAdmission struct {
	Protocol   int
	InstanceID string
	Generation string
	Version    string
	Commit     string
	Socket     string
	GateFile   string
	TokenFile  string
}

// RuntimeLease owns the stable lock inode for a server lifetime or an updater
// data operation. Closing it releases the lease but never unlinks the inode.
type RuntimeLease struct {
	file *os.File
	once sync.Once
	err  error
}

func AcquireRuntimeLease(ctx context.Context, dataDir string) (*RuntimeLease, error) {
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
	path := filepath.Join(dataDir, RuntimeLockName)
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_RDWR|syscall.O_NOFOLLOW, 0600)
	created := err == nil
	if errors.Is(err, os.ErrExist) {
		file, err = os.OpenFile(path, os.O_RDWR|syscall.O_NOFOLLOW, 0)
	}
	if err != nil {
		return nil, err
	}
	if created {
		// The privileged updater may create the lock before an older server has
		// ever used it. Give that new inode to the data owner so the non-root
		// runtime can open it later. Never replace or chmod/chown existing locks.
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
	lease := &RuntimeLease{file: file}
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
			current, statErr := os.Lstat(filepath.Join(dataDir, RuntimeLockName))
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

func (lease *RuntimeLease) Close() error {
	if lease == nil {
		return nil
	}
	lease.once.Do(func() { lease.err = lease.file.Close() })
	return lease.err
}

func IsRuntimeReservedName(name string) bool {
	return name == RuntimeLockName || name == RuntimeOwnerName ||
		strings.HasPrefix(name, RuntimeLockName+"/") || strings.HasPrefix(name, RuntimeOwnerName+"/")
}

func validRuntimeOwner(owner RuntimeOwner) bool {
	instance, instanceErr := uuid.Parse(owner.InstanceID)
	generation, generationErr := uuid.Parse(owner.Generation)
	return owner.API == RuntimeProtocol && instanceErr == nil && instance.String() == owner.InstanceID &&
		generationErr == nil && generation.String() == owner.Generation && releaseversion.Valid(owner.Version) && commitPattern.MatchString(owner.Commit)
}

// ReadRuntimeOwner returns nil only when no owner has ever been published.
// Existing unreadable, malformed, linked or writable markers fail closed.
func ReadRuntimeOwner(dataDir string) (*RuntimeOwner, error) {
	file, err := os.OpenFile(filepath.Join(dataDir, RuntimeOwnerName), os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0022 != 0 || info.Size() > 4096 || info.Sys().(*syscall.Stat_t).Nlink != 1 {
		return nil, errors.New("unsafe runtime owner marker")
	}
	data, err := io.ReadAll(io.LimitReader(file, 4097))
	if err != nil {
		return nil, err
	}
	var owner RuntimeOwner
	if len(data) > 4096 || decode(data, &owner) != nil || !validRuntimeOwner(owner) {
		return nil, errors.New("invalid runtime owner marker")
	}
	return &owner, nil
}

// WriteRuntimeOwner is controlled by the updater's deployment transaction.
// It can block admission before stopping the current server. The marker is
// replaceable; the separately held runtime lock never is.
func WriteRuntimeOwner(dataDir string, owner RuntimeOwner) error {
	if !validRuntimeOwner(owner) {
		return errors.New("invalid runtime owner marker")
	}
	data, err := json.Marshal(owner)
	if err != nil {
		return err
	}
	return AtomicWrite(filepath.Join(dataDir, RuntimeOwnerName), data, 0644)
}

func ValidateRuntimeAdmission(owner *RuntimeOwner, admission RuntimeAdmission) error {
	if owner == nil {
		if admission.Protocol == 0 && admission.Generation == "" {
			return nil // Standalone and existing protocol-zero enrollments.
		}
		return errors.New("runtime enrollment owner is missing")
	}
	if !validRuntimeOwner(*owner) || !owner.Allowed || admission.Protocol != RuntimeProtocol ||
		owner.InstanceID != admission.InstanceID || owner.Generation != admission.Generation ||
		owner.Version != admission.Version || owner.Commit != admission.Commit ||
		admission.Socket != RuntimeControlSocket || admission.GateFile != RuntimeGateFile || admission.TokenFile != RuntimeTokenFile {
		return errors.New("runtime start is blocked or does not match the enrolled deployment")
	}
	return nil
}
