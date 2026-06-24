package secretfiles

import (
	"context"
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"p2pstream/internal/secrets"
)

const testPrivateKeyPEM = "-----BEGIN PRIVATE KEY-----\ntest-private-key\n-----END PRIVATE KEY-----\n"

func TestWriteReadPrivateKeyEncryptsAndBindsPurposeOwner(t *testing.T) {
	ctx := context.Background()
	service := testSecretFilesService(t, testSecretFilesKey(1), "current", "", false)
	path := filepath.Join(t.TempDir(), "server.key.pem")

	if err := WritePrivateKey(ctx, service, secrets.PurposeFilePublicTLSPrivateKey, 42, path, []byte(testPrivateKeyPEM)); err != nil {
		t.Fatalf("WritePrivateKey() error = %v", err)
	}
	stored := readSecretFilesTestFile(t, path)
	if !secrets.IsEncrypted(stored) || strings.Contains(stored, "PRIVATE KEY") {
		t.Fatalf("stored key was not encrypted: %q", stored)
	}

	got, state, err := ReadPrivateKey(ctx, service, secrets.PurposeFilePublicTLSPrivateKey, 42, path)
	if err != nil {
		t.Fatalf("ReadPrivateKey() error = %v", err)
	}
	if state != secrets.StateEncrypted || string(got) != testPrivateKeyPEM {
		t.Fatalf("ReadPrivateKey() = %q/%v, want plaintext/encrypted", got, state)
	}
	if _, _, err := ReadPrivateKey(ctx, service, secrets.PurposeFilePublicTLSPrivateKey, 43, path); err == nil {
		t.Fatal("expected wrong owner to fail")
	}
	if _, _, err := ReadPrivateKey(ctx, service, secrets.PurposeFileManagementTLSCAKey, 42, path); err == nil {
		t.Fatal("expected wrong purpose to fail")
	}
}

func TestReadPrivateKeyMigratesPlaintextWhenRequiredWithKey(t *testing.T) {
	ctx := context.Background()
	service := testSecretFilesService(t, testSecretFilesKey(1), "current", "", true)
	path := filepath.Join(t.TempDir(), "ca.key.pem")
	if err := os.WriteFile(path, []byte(testPrivateKeyPEM), 0600); err != nil {
		t.Fatalf("write plaintext key: %v", err)
	}

	got, state, err := ReadPrivateKey(ctx, service, secrets.PurposeFileManagementTLSCAKey, ManagementCAKeyOwnerID, path)
	if err != nil {
		t.Fatalf("ReadPrivateKey() error = %v", err)
	}
	if state != secrets.StatePlaintext || string(got) != testPrivateKeyPEM {
		t.Fatalf("ReadPrivateKey() = %q/%v, want plaintext migration", got, state)
	}
	stored := readSecretFilesTestFile(t, path)
	if !secrets.IsEncrypted(stored) || strings.Contains(stored, "PRIVATE KEY") {
		t.Fatalf("migrated key was not encrypted: %q", stored)
	}
}

func TestStatusAndReconcileEncryptPlaintextAndRewrapOldKey(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	plainPath := filepath.Join(dir, "plain.key.pem")
	oldPath := filepath.Join(dir, "old.key.pem")
	if err := os.WriteFile(plainPath, []byte(testPrivateKeyPEM), 0600); err != nil {
		t.Fatalf("write plaintext key: %v", err)
	}

	oldKey := testSecretFilesKey(1)
	newKey := testSecretFilesKey(2)
	oldService := testSecretFilesService(t, oldKey, "old", "", false)
	if err := WritePrivateKey(ctx, oldService, secrets.PurposeFileACMEAccountKey, 100, oldPath, []byte(testPrivateKeyPEM)); err != nil {
		t.Fatalf("write old encrypted key: %v", err)
	}
	service := testSecretFilesService(t, newKey, "new", "old:"+oldKey, false)
	specs := []FileSpec{
		{Name: "plain key", Purpose: secrets.PurposeFilePublicTLSPrivateKey, OwnerID: 10, Path: plainPath},
		{Name: "old key", Purpose: secrets.PurposeFileACMEAccountKey, OwnerID: 100, Path: oldPath},
	}

	status, err := StatusFiles(ctx, service, specs)
	if err != nil {
		t.Fatalf("StatusFiles() error = %v", err)
	}
	if status.Plaintext != 1 || status.NeedsRewrap != 1 {
		t.Fatalf("StatusFiles() = %+v, want plaintext=1 needs_rewrap=1", status)
	}

	dryRun, err := Reconcile(ctx, service, specs, ReconcileOptions{DryRun: true})
	if err != nil {
		t.Fatalf("Reconcile(dry-run) error = %v", err)
	}
	if !dryRun.DryRun || dryRun.WouldEncrypt != 1 || dryRun.WouldRewrap != 1 {
		t.Fatalf("dry-run result = %+v, want would encrypt/rewrap", dryRun)
	}
	if got := readSecretFilesTestFile(t, plainPath); got != testPrivateKeyPEM {
		t.Fatalf("dry-run mutated plaintext key = %q", got)
	}

	result, err := Reconcile(ctx, service, specs, ReconcileOptions{})
	if err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
	if result.Encrypted != 1 || result.Rewrapped != 1 {
		t.Fatalf("Reconcile() = %+v, want encrypted=1 rewrapped=1", result)
	}
	for _, spec := range specs {
		stored := readSecretFilesTestFile(t, spec.Path)
		if !secrets.IsEncrypted(stored) || strings.Contains(stored, "PRIVATE KEY") {
			t.Fatalf("%s not encrypted after reconcile: %q", spec.Path, stored)
		}
		got, _, err := ReadPrivateKey(ctx, service, spec.Purpose, spec.OwnerID, spec.Path)
		if err != nil {
			t.Fatalf("ReadPrivateKey(%s) error = %v", spec.Path, err)
		}
		if string(got) != testPrivateKeyPEM {
			t.Fatalf("decrypted %s = %q, want test key", spec.Path, got)
		}
	}
}

func testSecretFilesService(t *testing.T, key, keyID, previousKeys string, required bool) *secrets.Service {
	t.Helper()
	service, err := secrets.NewService(secrets.KeyConfig{
		CurrentKey:     key,
		CurrentKeyID:   keyID,
		PreviousKeys:   previousKeys,
		Required:       required,
		AllowPlaintext: !required,
	})
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}
	return service
}

func testSecretFilesKey(seed byte) string {
	key := make([]byte, 32)
	for i := range key {
		key[i] = seed + byte(i)
	}
	return base64.RawURLEncoding.EncodeToString(key)
}

func readSecretFilesTestFile(t *testing.T, path string) string {
	t.Helper()
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(got)
}
