package secrets

import (
	"encoding/base64"
	"strings"
	"testing"
)

func TestEncryptDecryptRoundTrip(t *testing.T) {
	service := testService(t, "current")

	stored, err := service.Encrypt(PurposePublicRouteTargetBasicAuthPassword, 42, "top-secret")
	if err != nil {
		t.Fatalf("Encrypt() error = %v", err)
	}
	if !IsEncrypted(stored) {
		t.Fatalf("stored value is not encrypted: %q", stored)
	}
	if strings.Contains(stored, "top-secret") {
		t.Fatalf("stored value leaked plaintext: %q", stored)
	}

	got, state, err := service.Decrypt(PurposePublicRouteTargetBasicAuthPassword, 42, stored)
	if err != nil {
		t.Fatalf("Decrypt() error = %v", err)
	}
	if state != StateEncrypted {
		t.Fatalf("state = %v, want encrypted", state)
	}
	if got != "top-secret" {
		t.Fatalf("plaintext = %q, want top-secret", got)
	}
}

func TestDecryptRejectsWrongPurposeOrOwner(t *testing.T) {
	service := testService(t, "current")
	stored, err := service.Encrypt(PurposePublicRouteTargetBasicAuthPassword, 42, "top-secret")
	if err != nil {
		t.Fatalf("Encrypt() error = %v", err)
	}
	if _, _, err := service.Decrypt(PurposeEnvironmentAccessToken, 42, stored); err == nil {
		t.Fatal("expected wrong purpose to fail")
	}
	if _, _, err := service.Decrypt(PurposePublicRouteTargetBasicAuthPassword, 43, stored); err == nil {
		t.Fatal("expected wrong owner to fail")
	}
}

func TestDecryptRequiresConfiguredKey(t *testing.T) {
	service := testService(t, "current")
	stored, err := service.Encrypt(PurposeEnvironmentAccessToken, 1, "p2pat_secret")
	if err != nil {
		t.Fatalf("Encrypt() error = %v", err)
	}
	disabled := NewDisabledService()
	if _, _, err := disabled.Decrypt(PurposeEnvironmentAccessToken, 1, stored); err == nil {
		t.Fatal("expected encrypted value without key to fail")
	}
}

func TestEncryptTreatsEnvelopePrefixAsPlaintext(t *testing.T) {
	service := testService(t, "current")
	input := EnvelopePrefix + "attacker-controlled"
	stored, err := service.Encrypt(PurposePublicRouteTargetSensitiveHeader, 9, input)
	if err != nil {
		t.Fatalf("Encrypt() error = %v", err)
	}
	if stored == input {
		t.Fatal("envelope-looking input bypassed encryption")
	}
	got, _, err := service.Decrypt(PurposePublicRouteTargetSensitiveHeader, 9, stored)
	if err != nil {
		t.Fatalf("Decrypt() error = %v", err)
	}
	if got != input {
		t.Fatalf("plaintext = %q, want %q", got, input)
	}
}

func TestRequiredModeRejectsPlaintext(t *testing.T) {
	service, err := NewService(KeyConfig{
		CurrentKey:     keyText(1),
		CurrentKeyID:   "current",
		Required:       true,
		AllowPlaintext: false,
	})
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}
	if _, _, err := service.Decrypt(PurposeEnvironmentAccessToken, 1, "legacy-token"); err == nil {
		t.Fatal("expected required mode to reject plaintext")
	}
}

func TestPreviousKeyDecryptsAndNeedsRewrap(t *testing.T) {
	oldKey := keyText(1)
	newKey := keyText(2)
	oldService, err := NewService(KeyConfig{CurrentKey: oldKey, CurrentKeyID: "old", AllowPlaintext: true})
	if err != nil {
		t.Fatalf("old NewService() error = %v", err)
	}
	stored, err := oldService.Encrypt(PurposePublicWAFCaptchaProviderSecretKey, 7, "captcha-secret")
	if err != nil {
		t.Fatalf("Encrypt() error = %v", err)
	}

	newService, err := NewService(KeyConfig{
		CurrentKey:     newKey,
		CurrentKeyID:   "new",
		PreviousKeys:   "old:" + oldKey,
		AllowPlaintext: true,
	})
	if err != nil {
		t.Fatalf("new NewService() error = %v", err)
	}
	got, _, err := newService.Decrypt(PurposePublicWAFCaptchaProviderSecretKey, 7, stored)
	if err != nil {
		t.Fatalf("Decrypt() error = %v", err)
	}
	if got != "captcha-secret" {
		t.Fatalf("plaintext = %q, want captcha-secret", got)
	}
	if !newService.NeedsRewrap(stored) {
		t.Fatal("expected old-key envelope to need rewrap")
	}
}

func TestPreviousKeyAcceptsDefaultKeyID(t *testing.T) {
	oldKey := keyText(1)
	newKey := keyText(2)
	oldRaw, err := ParseKey(oldKey)
	if err != nil {
		t.Fatalf("ParseKey() error = %v", err)
	}
	oldKeyID := DefaultKeyID(oldRaw)
	oldService, err := NewService(KeyConfig{CurrentKey: oldKey, AllowPlaintext: true})
	if err != nil {
		t.Fatalf("old NewService() error = %v", err)
	}
	stored, err := oldService.Encrypt(PurposeEnvironmentAccessToken, 11, "p2pat_secret")
	if err != nil {
		t.Fatalf("Encrypt() error = %v", err)
	}

	newService, err := NewService(KeyConfig{
		CurrentKey:     newKey,
		CurrentKeyID:   "new",
		PreviousKeys:   oldKeyID + ":" + oldKey,
		AllowPlaintext: true,
	})
	if err != nil {
		t.Fatalf("new NewService() error = %v", err)
	}
	got, _, err := newService.Decrypt(PurposeEnvironmentAccessToken, 11, stored)
	if err != nil {
		t.Fatalf("Decrypt() error = %v", err)
	}
	if got != "p2pat_secret" {
		t.Fatalf("plaintext = %q, want p2pat_secret", got)
	}
}

func TestKeyConfigValidation(t *testing.T) {
	if _, err := NewService(KeyConfig{Required: true}); err == nil {
		t.Fatal("expected required key without current key to fail")
	}
	if _, err := NewService(KeyConfig{CurrentKey: "not-a-key"}); err == nil {
		t.Fatal("expected invalid current key to fail")
	}
	if _, err := NewService(KeyConfig{CurrentKey: keyText(1), CurrentKeyID: "same", PreviousKeys: "same:" + keyText(2)}); err == nil {
		t.Fatal("expected duplicate key id to fail")
	}
	if _, err := NewService(KeyConfig{CurrentKey: keyText(1), PreviousKeys: "missing-colon"}); err == nil {
		t.Fatal("expected malformed previous key to fail")
	}
}

func TestPlaintextCompatibility(t *testing.T) {
	service := NewDisabledService()
	got, state, err := service.Decrypt(PurposePublicTLSDNSCredentialAPIToken, 5, "legacy-token")
	if err != nil {
		t.Fatalf("Decrypt() error = %v", err)
	}
	if got != "legacy-token" || state != StatePlaintext {
		t.Fatalf("Decrypt() = %q/%v, want legacy-token/plaintext", got, state)
	}
	stored, err := service.Encrypt(PurposePublicTLSDNSCredentialAPIToken, 5, "legacy-token")
	if err != nil {
		t.Fatalf("Encrypt() error = %v", err)
	}
	if stored != "legacy-token" {
		t.Fatalf("disabled Encrypt() = %q, want plaintext passthrough", stored)
	}
}

func testService(t *testing.T, keyID string) *Service {
	t.Helper()
	service, err := NewService(KeyConfig{
		CurrentKey:     keyText(1),
		CurrentKeyID:   keyID,
		AllowPlaintext: true,
	})
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}
	return service
}

func keyText(seed byte) string {
	key := make([]byte, keySize)
	for i := range key {
		key[i] = seed + byte(i)
	}
	return base64.RawURLEncoding.EncodeToString(key)
}
