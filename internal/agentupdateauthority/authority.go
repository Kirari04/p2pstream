// Package agentupdateauthority owns the persistent management Ed25519 authority
// used to authorize privileged agent-update actions. Key creation is exclusive;
// steady-state loading requires an independently persisted expected identity.
package agentupdateauthority

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"syscall"

	"golang.org/x/sys/unix"

	"p2pstream/internal/agentupdateauth"
)

const (
	keyFileVersion = 1
	maxKeyFileSize = 4 << 10
)

var (
	ErrKeyMissing    = errors.New("management update authority key is missing")
	ErrKeyExists     = errors.New("management update authority key already exists")
	ErrKeyMismatch   = errors.New("management update authority key does not match pinned identity")
	ErrUnsafeKeyFile = errors.New("management update authority key file is unsafe")
)

// Identity is safe to persist in the database and distribute during authenticated
// enrollment. PublicKey is always returned as a defensive copy.
type Identity struct {
	KeyID     string
	Epoch     uint64
	PublicKey ed25519.PublicKey
}

// Authority keeps the private key unexported and exposes only signing and its
// public, database-pinnable identity.
type Authority struct {
	identity   Identity
	privateKey ed25519.PrivateKey
}

type keyFile struct {
	Version    uint64 `json:"version"`
	Epoch      uint64 `json:"epoch"`
	PrivateKey string `json:"private_key"`
}

// Generate atomically creates a new authority at path. It never overwrites an
// existing filesystem object. Callers must durably persist Identity before using
// the key to sign enrollment receipts or assignment authorizations.
func Generate(path string, epoch uint64) (*Authority, error) {
	if epoch == 0 || epoch > math.MaxInt64 {
		return nil, errors.New("management update authority epoch must be 1..MaxInt64")
	}
	if err := validatePathAndParent(path); err != nil {
		return nil, err
	}
	if info, err := os.Lstat(path); err == nil {
		if info.Mode()&os.ModeSymlink != 0 {
			return nil, fmt.Errorf("%w: target is a symbolic link", ErrUnsafeKeyFile)
		}
		return nil, ErrKeyExists
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}

	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	authority, err := newAuthority(epoch, privateKey)
	if err != nil {
		return nil, err
	}
	encoded, err := json.Marshal(keyFile{
		Version:    keyFileVersion,
		Epoch:      epoch,
		PrivateKey: base64.StdEncoding.EncodeToString(privateKey),
	})
	if err != nil {
		return nil, err
	}
	if subtle.ConstantTimeCompare(publicKey, authority.identity.PublicKey) != 1 {
		return nil, errors.New("generated management update authority is inconsistent")
	}
	if err := createExclusiveAtomic(path, encoded); err != nil {
		return nil, err
	}
	return authority, nil
}

// Load reads an existing authority without following a final symlink and
// requires it to match independently persisted public identity and epoch state.
// Missing or mismatched state always fails closed.
func Load(path string, expected Identity) (*Authority, error) {
	if err := validateIdentity(expected); err != nil {
		return nil, fmt.Errorf("invalid expected management authority identity: %w", err)
	}
	authority, err := LoadExisting(path)
	if err != nil {
		return nil, err
	}
	if !identitiesEqual(authority.identity, expected) {
		return nil, ErrKeyMismatch
	}
	return authority, nil
}

// LoadExisting securely inspects a key when no external identity exists yet,
// such as first-install crash recovery. Production restarts with a database row
// should use Load so loss or replacement cannot silently establish new trust.
func LoadExisting(path string) (*Authority, error) {
	if err := validatePathAndParent(path); err != nil {
		return nil, err
	}
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		switch {
		case errors.Is(err, syscall.ENOENT):
			return nil, ErrKeyMissing
		case errors.Is(err, syscall.ELOOP):
			return nil, fmt.Errorf("%w: refusing symbolic link", ErrUnsafeKeyFile)
		default:
			return nil, err
		}
	}
	file := os.NewFile(uintptr(fd), path)
	if file == nil {
		_ = unix.Close(fd)
		return nil, errors.New("open management update authority key")
	}
	defer file.Close()
	if err := validateOpenedKeyFile(fd); err != nil {
		return nil, err
	}
	raw, err := io.ReadAll(io.LimitReader(file, maxKeyFileSize+1))
	if err != nil {
		return nil, err
	}
	if len(raw) == 0 || len(raw) > maxKeyFileSize {
		return nil, fmt.Errorf("%w: key file size is invalid", ErrUnsafeKeyFile)
	}
	var stored keyFile
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&stored); err != nil {
		return nil, fmt.Errorf("parse management update authority key: %w", err)
	}
	if err := requireJSONEOF(decoder); err != nil {
		return nil, err
	}
	canonical, err := json.Marshal(stored)
	if err != nil || !bytes.Equal(canonical, raw) {
		return nil, fmt.Errorf("%w: key file is not canonical JSON", ErrUnsafeKeyFile)
	}
	if stored.Version != keyFileVersion || stored.Epoch == 0 || stored.Epoch > math.MaxInt64 {
		return nil, fmt.Errorf("%w: key file version or epoch is invalid", ErrUnsafeKeyFile)
	}
	privateKey, err := base64.StdEncoding.Strict().DecodeString(stored.PrivateKey)
	if err != nil || base64.StdEncoding.EncodeToString(privateKey) != stored.PrivateKey || len(privateKey) != ed25519.PrivateKeySize {
		return nil, fmt.Errorf("%w: private key encoding is invalid", ErrUnsafeKeyFile)
	}
	return newAuthority(stored.Epoch, ed25519.PrivateKey(privateKey))
}

// Identity returns a defensive copy of the public authority identity.
func (a *Authority) Identity() Identity {
	if a == nil {
		return Identity{}
	}
	return cloneIdentity(a.identity)
}

// Sign signs an already canonical, domain-separated payload.
func (a *Authority) Sign(payload []byte) ([]byte, error) {
	if a == nil || len(a.privateKey) != ed25519.PrivateKeySize {
		return nil, errors.New("management update authority is unavailable")
	}
	if len(payload) == 0 {
		return nil, errors.New("management update authority payload is empty")
	}
	return ed25519.Sign(a.privateKey, payload), nil
}

func newAuthority(epoch uint64, privateKey ed25519.PrivateKey) (*Authority, error) {
	if len(privateKey) != ed25519.PrivateKeySize {
		return nil, fmt.Errorf("%w: private key length is invalid", ErrUnsafeKeyFile)
	}
	canonical := ed25519.NewKeyFromSeed(privateKey.Seed())
	if subtle.ConstantTimeCompare(canonical, privateKey) != 1 {
		return nil, fmt.Errorf("%w: private key is not canonical", ErrUnsafeKeyFile)
	}
	publicKey := canonical.Public().(ed25519.PublicKey)
	keyID, err := agentupdateauth.KeyID(publicKey)
	if err != nil {
		return nil, err
	}
	return &Authority{
		identity:   Identity{KeyID: keyID, Epoch: epoch, PublicKey: append(ed25519.PublicKey(nil), publicKey...)},
		privateKey: append(ed25519.PrivateKey(nil), canonical...),
	}, nil
}

func validateIdentity(identity Identity) error {
	if identity.Epoch == 0 || identity.Epoch > math.MaxInt64 {
		return errors.New("authority epoch is invalid")
	}
	keyID, err := agentupdateauth.KeyID(identity.PublicKey)
	if err != nil {
		return err
	}
	if identity.KeyID != keyID {
		return errors.New("authority key ID does not match public key")
	}
	return nil
}

func identitiesEqual(left, right Identity) bool {
	return left.Epoch == right.Epoch && left.KeyID == right.KeyID &&
		len(left.PublicKey) == ed25519.PublicKeySize && len(right.PublicKey) == ed25519.PublicKeySize &&
		subtle.ConstantTimeCompare(left.PublicKey, right.PublicKey) == 1
}

func cloneIdentity(identity Identity) Identity {
	identity.PublicKey = append(ed25519.PublicKey(nil), identity.PublicKey...)
	return identity
}

func validatePathAndParent(path string) error {
	if path == "" || !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return errors.New("management update authority path must be clean and absolute")
	}
	parent := filepath.Dir(path)
	info, err := os.Lstat(parent)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return fmt.Errorf("%w: parent path must be a non-symlink directory", ErrUnsafeKeyFile)
	}
	if info.Mode().Perm()&0o022 != 0 {
		return fmt.Errorf("%w: parent directory is group/world writable", ErrUnsafeKeyFile)
	}
	if stat, ok := info.Sys().(*syscall.Stat_t); !ok || stat.Uid != uint32(os.Geteuid()) {
		return fmt.Errorf("%w: parent directory is not owned by the service user", ErrUnsafeKeyFile)
	}
	return nil
}

func validateOpenedKeyFile(fd int) error {
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil {
		return err
	}
	if stat.Mode&unix.S_IFMT != unix.S_IFREG || stat.Mode&0o777 != 0o600 || stat.Uid != uint32(os.Geteuid()) || stat.Nlink != 1 {
		return fmt.Errorf("%w: key must be an owner-owned, single-link regular file with mode 0600", ErrUnsafeKeyFile)
	}
	return nil
}

func createExclusiveAtomic(path string, contents []byte) (returnErr error) {
	parent := filepath.Dir(path)
	randomName := make([]byte, 16)
	if _, err := rand.Read(randomName); err != nil {
		return err
	}
	temporaryPath := filepath.Join(parent, "."+filepath.Base(path)+".tmp-"+hex.EncodeToString(randomName))
	fd, err := unix.Open(temporaryPath, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0o600)
	if err != nil {
		return err
	}
	temporary := os.NewFile(uintptr(fd), temporaryPath)
	if temporary == nil {
		_ = unix.Close(fd)
		return errors.New("create management update authority temporary key")
	}
	defer func() {
		_ = temporary.Close()
		_ = os.Remove(temporaryPath)
	}()
	if err := temporary.Chmod(0o600); err != nil {
		return err
	}
	if written, err := temporary.Write(contents); err != nil {
		return err
	} else if written != len(contents) {
		return io.ErrShortWrite
	}
	if err := temporary.Sync(); err != nil {
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := os.Link(temporaryPath, path); err != nil {
		if errors.Is(err, os.ErrExist) {
			return ErrKeyExists
		}
		return err
	}
	if err := os.Remove(temporaryPath); err != nil {
		return err
	}
	if err := syncDirectory(parent); err != nil {
		return err
	}
	return nil
}

func syncDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}

func requireJSONEOF(decoder *json.Decoder) error {
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return fmt.Errorf("%w: key file contains multiple JSON values", ErrUnsafeKeyFile)
		}
		return fmt.Errorf("parse management update authority key: %w", err)
	}
	return nil
}
