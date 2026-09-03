package updater

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"syscall"

	"golang.org/x/sys/unix"
	"p2pstream/internal/releaseversion"
)

var (
	assetPattern            = regexp.MustCompile(`^p2pstream_[v0-9A-Za-z.-]+_linux_(?:amd64|arm64)$`)
	agentIDPattern          = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.:-]{0,127}$`)
	commitPattern           = regexp.MustCompile(`^[0-9a-f]{40}$`)
	digestPattern           = regexp.MustCompile(`^[0-9a-f]{64}$`)
	bootstrapVersionPattern = regexp.MustCompile(`^bootstrap-[0-9a-f]{16}$`)
)

func validVersion(version string) bool { return releaseversion.Valid(version) }

func validVersionForChannel(version, channel string) bool {
	return releaseversion.ValidForChannel(version, channel)
}

func validateArtifact(a Artifact) error {
	if !assetPattern.MatchString(a.Name) || filepath.Base(a.Name) != a.Name {
		return fmt.Errorf("unsafe artifact name %q", a.Name)
	}
	if a.Size <= 0 || a.Size > defaultMaxArtifact {
		return fmt.Errorf("artifact size %d is outside the supported range", a.Size)
	}
	if a.SHA256 == ([32]byte{}) {
		return errors.New("artifact SHA-256 is empty")
	}
	return nil
}

func artifactHex(a Artifact) string { return hex.EncodeToString(a.SHA256[:]) }

func atomicWrite(path string, data []byte, mode os.FileMode) error {
	dir := filepath.Dir(path)
	f, err := os.CreateTemp(dir, ".write-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	ok := false
	defer func() {
		_ = f.Close()
		if !ok {
			_ = os.Remove(tmp)
		}
	}()
	if err := f.Chmod(mode); err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		return err
	}
	if err := f.Sync(); err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		return err
	}
	if err := syncDir(dir); err != nil {
		return err
	}
	ok = true
	return nil
}

func atomicJSON(path string, value any, mode os.FileMode) error {
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	data = append(data, '\n')
	return atomicWrite(path, data, mode)
}

func strictJSON(data []byte, value any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(value); err != nil {
		return err
	}
	if decoder.Decode(&struct{}{}) != io.EOF {
		return errors.New("trailing JSON data")
	}
	return nil
}

func syncDir(path string) error {
	dir, err := os.Open(path)
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}

func removeAndSync(path string) error {
	err := os.Remove(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return syncDir(filepath.Dir(path))
}

func readLimited(path string, maximum int64) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return readBounded(f, maximum)
}

func readBounded(r io.Reader, maximum int64) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(r, maximum+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > maximum {
		return nil, fmt.Errorf("input exceeds %d bytes", maximum)
	}
	return data, nil
}

// openRegularNoFollow rejects symlinks, devices, sockets, directories and
// multiply-linked files before privileged activation consumes worker output.
func openRegularNoFollow(path string, maximum int64) (*os.File, error) {
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	f := os.NewFile(uintptr(fd), path)
	if f == nil {
		_ = unix.Close(fd)
		return nil, errors.New("could not wrap staged file")
	}
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil {
		_ = f.Close()
		return nil, err
	}
	if stat.Mode&unix.S_IFMT != unix.S_IFREG || stat.Nlink != 1 {
		_ = f.Close()
		return nil, errors.New("staged input is not a singly-linked regular file")
	}
	if stat.Size < 0 || stat.Size > maximum {
		_ = f.Close()
		return nil, fmt.Errorf("staged input size %d exceeds limit %d", stat.Size, maximum)
	}
	return f, nil
}

func readRegularNoFollow(path string, maximum int64) ([]byte, error) {
	f, err := openRegularNoFollow(path, maximum)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return readBounded(f, maximum)
}

func copyArtifact(dst *os.File, src io.Reader, expected Artifact) error {
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(dst, h), io.LimitReader(src, expected.Size+1))
	if err != nil {
		return err
	}
	if n != expected.Size {
		return fmt.Errorf("artifact size = %d, want %d", n, expected.Size)
	}
	if !bytes.Equal(h.Sum(nil), expected.SHA256[:]) {
		return errors.New("artifact SHA-256 does not match signed metadata")
	}
	return nil
}

func diskPreflight(path string, bytesNeeded int64) error {
	if bytesNeeded < 0 || bytesNeeded > (1<<62) {
		return errors.New("invalid disk preflight size")
	}
	var stat syscall.Statfs_t
	if err := syscall.Statfs(path, &stat); err != nil {
		return fmt.Errorf("statfs %s: %w", path, err)
	}
	available := uint64(stat.Bavail) * uint64(stat.Bsize)
	required := uint64(bytesNeeded) + defaultDiskHeadroom
	if available < required {
		return fmt.Errorf("insufficient disk space: %d available, %d required", available, required)
	}
	return nil
}

func acquireLock(path string) (*os.File, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if err := unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("another activation is in progress: %w", err)
	}
	return f, nil
}

func runtimeArtifactName(version string) string {
	return fmt.Sprintf("p2pstream_%s_%s_%s", version, runtime.GOOS, runtime.GOARCH)
}
