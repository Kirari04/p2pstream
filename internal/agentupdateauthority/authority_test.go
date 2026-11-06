package agentupdateauthority

import (
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"p2pstream/internal/agentupdateauth"
)

func TestGenerateLoadAndSignAuthority(t *testing.T) {
	path := filepath.Join(secureTestDirectory(t), "management-authority.json")
	authority, err := Generate(path, 7)
	if err != nil {
		t.Fatal(err)
	}
	identity := authority.Identity()
	if identity.Epoch != 7 || len(identity.PublicKey) != ed25519.PublicKeySize || identity.KeyID == "" {
		t.Fatalf("generated identity = %+v", identity)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 {
		t.Fatalf("generated key mode = %v, err=%v", info.Mode(), err)
	}

	loaded, err := Load(path, identity)
	if err != nil {
		t.Fatal(err)
	}
	payload := []byte("canonical-domain-separated-payload")
	signature, err := loaded.Sign(payload)
	if err != nil {
		t.Fatal(err)
	}
	if !ed25519.Verify(identity.PublicKey, payload, signature) {
		t.Fatal("loaded authority produced an invalid signature")
	}

	copyIdentity := loaded.Identity()
	copyIdentity.PublicKey[0] ^= 0xff
	if loaded.Identity().PublicKey[0] != identity.PublicKey[0] {
		t.Fatal("Identity exposed mutable authority state")
	}
	if _, err := Generate(path, 8); !errors.Is(err, ErrKeyExists) {
		t.Fatalf("regeneration error = %v", err)
	}
}

func TestLoadAuthorityFailsClosedOnMissingMismatchAndUnsafeFiles(t *testing.T) {
	directory := secureTestDirectory(t)
	path := filepath.Join(directory, "management-authority.json")
	if _, err := LoadExisting(path); !errors.Is(err, ErrKeyMissing) {
		t.Fatalf("missing key error = %v", err)
	}
	authority, err := Generate(path, 3)
	if err != nil {
		t.Fatal(err)
	}
	expected := authority.Identity()

	wrongEpoch := cloneIdentity(expected)
	wrongEpoch.Epoch++
	if _, err := Load(path, wrongEpoch); !errors.Is(err, ErrKeyMismatch) {
		t.Fatalf("epoch mismatch error = %v", err)
	}
	otherPublic, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	wrongKey := Identity{Epoch: expected.Epoch, PublicKey: otherPublic}
	wrongKey.KeyID, _ = agentKeyID(otherPublic)
	if _, err := Load(path, wrongKey); !errors.Is(err, ErrKeyMismatch) {
		t.Fatalf("public-key mismatch error = %v", err)
	}

	if err := os.Chmod(path, 0o640); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path, expected); !errors.Is(err, ErrUnsafeKeyFile) {
		t.Fatalf("weak key mode error = %v", err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}

	symlink := filepath.Join(directory, "authority-link.json")
	if err := os.Symlink(path, symlink); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadExisting(symlink); !errors.Is(err, ErrUnsafeKeyFile) {
		t.Fatalf("symlink key error = %v", err)
	}
	hardlink := filepath.Join(directory, "authority-hardlink.json")
	if err := os.Link(path, hardlink); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path, expected); !errors.Is(err, ErrUnsafeKeyFile) {
		t.Fatalf("multiply-linked key error = %v", err)
	}
}

func TestGenerateAuthorityIsExclusiveUnderConcurrency(t *testing.T) {
	path := filepath.Join(secureTestDirectory(t), "management-authority.json")
	const contenders = 8
	results := make(chan error, contenders)
	var wait sync.WaitGroup
	for index := 0; index < contenders; index++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			_, err := Generate(path, 1)
			results <- err
		}()
	}
	wait.Wait()
	close(results)
	succeeded := 0
	for err := range results {
		switch {
		case err == nil:
			succeeded++
		case errors.Is(err, ErrKeyExists):
		default:
			t.Fatalf("concurrent generation error = %v", err)
		}
	}
	if succeeded != 1 {
		t.Fatalf("successful generators = %d, want 1", succeeded)
	}
	if _, err := LoadExisting(path); err != nil {
		t.Fatalf("load winning key: %v", err)
	}
}

func BenchmarkAuthoritySign(b *testing.B) {
	directory := b.TempDir()
	if err := os.Chmod(directory, 0o700); err != nil {
		b.Fatal(err)
	}
	authority, err := Generate(filepath.Join(directory, "management-authority.json"), 1)
	if err != nil {
		b.Fatal(err)
	}
	payload := []byte("p2pstream-agent-update-control-v1 canonical assignment authorization")
	b.ResetTimer()
	for index := 0; index < b.N; index++ {
		if _, err := authority.Sign(payload); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkAuthorityLoad(b *testing.B) {
	directory := b.TempDir()
	if err := os.Chmod(directory, 0o700); err != nil {
		b.Fatal(err)
	}
	path := filepath.Join(directory, "management-authority.json")
	authority, err := Generate(path, 1)
	if err != nil {
		b.Fatal(err)
	}
	expected := authority.Identity()
	b.ResetTimer()
	for index := 0; index < b.N; index++ {
		if _, err := Load(path, expected); err != nil {
			b.Fatal(err)
		}
	}
}

func agentKeyID(publicKey ed25519.PublicKey) (string, error) {
	return agentupdateauth.KeyID(publicKey)
}

func secureTestDirectory(t *testing.T) string {
	t.Helper()
	directory := t.TempDir()
	if err := os.Chmod(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	return directory
}
