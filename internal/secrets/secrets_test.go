package secrets

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

func TestGenerateKeyProducesUsableKeyAndDefaultID(t *testing.T) {
	key, keyID, err := GenerateKey()
	if err != nil {
		t.Fatalf("GenerateKey() error = %v", err)
	}
	raw, err := ParseKey(key)
	if err != nil {
		t.Fatalf("ParseKey(generated) error = %v", err)
	}
	if len(raw) != keySize {
		t.Fatalf("generated key length = %d, want %d", len(raw), keySize)
	}
	if keyID != DefaultKeyID(raw) {
		t.Fatalf("generated key ID = %q, want %q", keyID, DefaultKeyID(raw))
	}
}

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

func TestVaultTransitEnvelopeRoundTrip(t *testing.T) {
	vault := newFakeVaultTransit(t)
	service := testVaultTransitService(t, vault)

	stored, err := service.EncryptContext(context.Background(), PurposePublicRouteTargetBasicAuthPassword, 42, "top-secret")
	if err != nil {
		t.Fatalf("EncryptContext() error = %v", err)
	}
	if !IsEncrypted(stored) {
		t.Fatalf("stored value is not encrypted: %q", stored)
	}
	if strings.Contains(stored, "top-secret") {
		t.Fatalf("stored value leaked plaintext: %q", stored)
	}

	meta, err := Inspect(stored)
	if err != nil {
		t.Fatalf("Inspect() error = %v", err)
	}
	if meta.State != StateEncrypted ||
		meta.Version != envelopeVersionV2 ||
		meta.Provider != ProviderVaultTransit ||
		meta.KeyID != "vault-transit:transit/p2pstream" ||
		meta.WrapAlgorithm != wrapAlgVaultTransitDEK {
		t.Fatalf("Inspect() = %+v, want Vault Transit v2 metadata", meta)
	}

	got, state, err := service.DecryptContext(context.Background(), PurposePublicRouteTargetBasicAuthPassword, 42, stored)
	if err != nil {
		t.Fatalf("DecryptContext() error = %v", err)
	}
	if state != StateEncrypted || got != "top-secret" {
		t.Fatalf("DecryptContext() = %q/%v, want top-secret/encrypted", got, state)
	}
	if _, _, err := service.DecryptContext(context.Background(), PurposePublicRouteTargetBasicAuthPassword, 43, stored); err == nil {
		t.Fatal("expected wrong owner to fail")
	}
	needsRewrap, err := service.NeedsRewrapContext(context.Background(), stored)
	if err != nil {
		t.Fatalf("NeedsRewrapContext() error = %v", err)
	}
	if needsRewrap {
		t.Fatal("fresh Vault Transit envelope should not need rewrap")
	}
}

func TestVaultTransitWrappedDataKeyRewrap(t *testing.T) {
	vault := newFakeVaultTransit(t)
	service := testVaultTransitService(t, vault)
	stored, err := service.EncryptContext(context.Background(), PurposeEnvironmentAccessToken, 7, "p2pat_secret")
	if err != nil {
		t.Fatalf("EncryptContext() error = %v", err)
	}
	vault.setLatestVersion(2)

	needsRewrap, err := service.NeedsRewrapContext(context.Background(), stored)
	if err != nil {
		t.Fatalf("NeedsRewrapContext() error = %v", err)
	}
	if !needsRewrap {
		t.Fatal("expected old Vault key version to need rewrap")
	}
	rewrapped, err := service.RewrapContext(context.Background(), PurposeEnvironmentAccessToken, 7, stored)
	if err != nil {
		t.Fatalf("RewrapContext() error = %v", err)
	}
	if rewrapped == stored {
		t.Fatal("RewrapContext() returned unchanged envelope")
	}
	env, err := parseEnvelope(rewrapped)
	if err != nil {
		t.Fatalf("parseEnvelope(rewrapped) error = %v", err)
	}
	if version := vaultCiphertextVersion(env.EncryptedDataKey); version != 2 {
		t.Fatalf("rewrapped data key version = %d, want 2", version)
	}
	got, _, err := service.DecryptContext(context.Background(), PurposeEnvironmentAccessToken, 7, rewrapped)
	if err != nil {
		t.Fatalf("DecryptContext(rewrapped) error = %v", err)
	}
	if got != "p2pat_secret" {
		t.Fatalf("DecryptContext(rewrapped) = %q, want p2pat_secret", got)
	}
}

func TestInspectReportsEncryptedMetadata(t *testing.T) {
	service := testService(t, "current")
	stored, err := service.Encrypt(PurposePublicWAFCookieSigningSecret, 1, "cookie-secret")
	if err != nil {
		t.Fatalf("Encrypt() error = %v", err)
	}
	meta, err := Inspect(stored)
	if err != nil {
		t.Fatalf("Inspect() error = %v", err)
	}
	if meta.State != StateEncrypted || meta.KeyID != "current" || meta.Version != envelopeVersionV1 || meta.Algorithm != envelopeAlg {
		t.Fatalf("Inspect() = %+v, want encrypted metadata for current key", meta)
	}

	plain, err := Inspect("legacy-secret")
	if err != nil {
		t.Fatalf("Inspect(plaintext) error = %v", err)
	}
	if plain.State != StatePlaintext {
		t.Fatalf("Inspect(plaintext).State = %v, want plaintext", plain.State)
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

func TestVaultTransitDecryptsDirectPreviousKeyAndNeedsRewrap(t *testing.T) {
	oldKey := keyText(1)
	oldService, err := NewService(KeyConfig{CurrentKey: oldKey, CurrentKeyID: "old", AllowPlaintext: true})
	if err != nil {
		t.Fatalf("old NewService() error = %v", err)
	}
	stored, err := oldService.Encrypt(PurposePublicWAFCaptchaProviderSecretKey, 7, "captcha-secret")
	if err != nil {
		t.Fatalf("Encrypt() error = %v", err)
	}

	vault := newFakeVaultTransit(t)
	newService, err := NewService(KeyConfig{
		PreviousKeys:   "old:" + oldKey,
		AllowPlaintext: true,
		Provider:       ProviderVaultTransit,
		VaultTransit: VaultTransitConfig{
			Address:   vault.server.URL,
			Token:     vault.token,
			MountPath: "transit",
			KeyName:   "p2pstream",
		},
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
		t.Fatal("expected direct v1 envelope to need Vault rewrap")
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

type fakeVaultTransit struct {
	t       *testing.T
	server  *httptest.Server
	token   string
	mu      sync.Mutex
	latest  int
	counter int
	keys    map[string][]byte
}

func newFakeVaultTransit(t *testing.T) *fakeVaultTransit {
	t.Helper()
	fake := &fakeVaultTransit{
		t:      t,
		token:  "test-token",
		latest: 1,
		keys:   make(map[string][]byte),
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/transit/keys/p2pstream", fake.handleKey)
	mux.HandleFunc("/v1/transit/datakey/plaintext/p2pstream", fake.handleDataKey)
	mux.HandleFunc("/v1/transit/decrypt/p2pstream", fake.handleDecrypt)
	mux.HandleFunc("/v1/transit/rewrap/p2pstream", fake.handleRewrap)
	fake.server = httptest.NewServer(mux)
	t.Cleanup(fake.server.Close)
	return fake
}

func (f *fakeVaultTransit) setLatestVersion(version int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.latest = version
}

func (f *fakeVaultTransit) handleKey(w http.ResponseWriter, r *http.Request) {
	if !f.authorized(w, r) {
		return
	}
	f.mu.Lock()
	latest := f.latest
	f.mu.Unlock()
	writeVaultJSON(w, map[string]interface{}{"data": map[string]int{"latest_version": latest}})
}

func (f *fakeVaultTransit) handleDataKey(w http.ResponseWriter, r *http.Request) {
	if !f.authorized(w, r) {
		return
	}
	var body map[string]interface{}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid json", http.StatusBadRequest)
		return
	}
	if body["context"] == "" || fmt.Sprint(body["bits"]) != "256" {
		http.Error(w, "invalid datakey request", http.StatusBadRequest)
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.counter++
	key := make([]byte, keySize)
	for i := range key {
		key[i] = byte(f.counter + i)
	}
	wrapped := fmt.Sprintf("vault:v%d:%s", f.latest, base64.RawURLEncoding.EncodeToString(key))
	f.keys[wrapped] = cloneBytes(key)
	writeVaultJSON(w, map[string]interface{}{
		"data": map[string]string{
			"plaintext":  base64.StdEncoding.EncodeToString(key),
			"ciphertext": wrapped,
		},
	})
}

func (f *fakeVaultTransit) handleDecrypt(w http.ResponseWriter, r *http.Request) {
	if !f.authorized(w, r) {
		return
	}
	var body struct {
		Ciphertext string `json:"ciphertext"`
		Context    string `json:"context"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid json", http.StatusBadRequest)
		return
	}
	if body.Context == "" {
		http.Error(w, "missing context", http.StatusBadRequest)
		return
	}
	f.mu.Lock()
	key := cloneBytes(f.keys[body.Ciphertext])
	f.mu.Unlock()
	if len(key) != keySize {
		writeVaultError(w, http.StatusBadRequest, "ciphertext is invalid")
		return
	}
	writeVaultJSON(w, map[string]interface{}{
		"data": map[string]string{"plaintext": base64.StdEncoding.EncodeToString(key)},
	})
}

func (f *fakeVaultTransit) handleRewrap(w http.ResponseWriter, r *http.Request) {
	if !f.authorized(w, r) {
		return
	}
	var body struct {
		Ciphertext string `json:"ciphertext"`
		Context    string `json:"context"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid json", http.StatusBadRequest)
		return
	}
	if body.Context == "" {
		http.Error(w, "missing context", http.StatusBadRequest)
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	key := cloneBytes(f.keys[body.Ciphertext])
	if len(key) != keySize {
		writeVaultError(w, http.StatusBadRequest, "ciphertext is invalid")
		return
	}
	wrapped := fmt.Sprintf("vault:v%d:%s", f.latest, base64.RawURLEncoding.EncodeToString(key))
	f.keys[wrapped] = key
	writeVaultJSON(w, map[string]interface{}{"data": map[string]string{"ciphertext": wrapped}})
}

func (f *fakeVaultTransit) authorized(w http.ResponseWriter, r *http.Request) bool {
	if r.Header.Get("X-Vault-Token") != f.token {
		writeVaultError(w, http.StatusForbidden, "permission denied")
		return false
	}
	return true
}

func writeVaultJSON(w http.ResponseWriter, value interface{}) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(value)
}

func writeVaultError(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string][]string{"errors": []string{message}})
}

func testVaultTransitService(t *testing.T, vault *fakeVaultTransit) *Service {
	t.Helper()
	service, err := NewService(KeyConfig{
		AllowPlaintext: true,
		Provider:       ProviderVaultTransit,
		VaultTransit: VaultTransitConfig{
			Address:   vault.server.URL,
			Token:     vault.token,
			MountPath: "transit",
			KeyName:   "p2pstream",
		},
	})
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}
	return service
}
