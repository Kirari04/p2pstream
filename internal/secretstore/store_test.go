package secretstore

import (
	"context"
	"encoding/base64"
	"path/filepath"
	"strings"
	"testing"

	"p2pstream/internal/db"
	"p2pstream/internal/secrets"
)

func TestStatusAndReconcileEncryptPlaintextAndRewrapOldKeys(t *testing.T) {
	ctx := context.Background()
	database := newSecretStoreTestDB(t)
	current := newSecretStoreTestService(t, secretStoreTestKey(2), "current", "old:"+secretStoreTestKey(1), false)
	old := newSecretStoreTestService(t, secretStoreTestKey(1), "old", "", false)

	insertWAFSettingsSecret(t, database, "cookie-secret")
	oldEnvironmentToken, err := old.Encrypt(secrets.PurposeEnvironmentAccessToken, 100, "p2pat_old")
	if err != nil {
		t.Fatalf("encrypt old environment token: %v", err)
	}
	insertEnvironmentSecret(t, database, 100, "remote-old", oldEnvironmentToken)
	currentCaptchaSecret, err := current.Encrypt(secrets.PurposePublicWAFCaptchaProviderSecretKey, 200, "captcha-current")
	if err != nil {
		t.Fatalf("encrypt current captcha secret: %v", err)
	}
	insertCaptchaSecret(t, database, 200, "captcha-current", currentCaptchaSecret)

	store := New(database.DB, current)
	status, err := store.Status(ctx, 1)
	if err != nil {
		t.Fatalf("Status() error = %v", err)
	}
	if status.Total != 3 || status.Plaintext != 1 || status.Current != 1 || status.NeedsRewrap != 1 {
		t.Fatalf("Status() = %+v, want total=3 plaintext=1 current=1 needs_rewrap=1", status)
	}
	if status.CurrentKeyID != "current" || !status.EncryptionOn || status.Required {
		t.Fatalf("Status key metadata = current=%q enabled=%t required=%t", status.CurrentKeyID, status.EncryptionOn, status.Required)
	}

	dryRun, err := store.Reconcile(ctx, ReconcileOptions{DryRun: true, BatchSize: 1})
	if err != nil {
		t.Fatalf("Reconcile(dry-run) error = %v", err)
	}
	if dryRun.WouldEncrypt != 1 || dryRun.WouldRewrap != 1 || dryRun.Encrypted != 0 || dryRun.Rewrapped != 0 {
		t.Fatalf("dry-run result = %+v, want would_encrypt=1 would_rewrap=1 and no writes", dryRun)
	}
	if got := rawWAFSettingsSecret(t, database); got != "cookie-secret" {
		t.Fatalf("dry-run mutated WAF secret = %q", got)
	}

	result, err := store.Reconcile(ctx, ReconcileOptions{BatchSize: 1})
	if err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
	if result.Encrypted != 1 || result.Rewrapped != 1 || result.Unchanged != 1 {
		t.Fatalf("Reconcile() = %+v, want encrypted=1 rewrapped=1 unchanged=1", result)
	}

	status, err = store.Status(ctx, 2)
	if err != nil {
		t.Fatalf("Status(after) error = %v", err)
	}
	if status.Plaintext != 0 || status.NeedsRewrap != 0 || status.Current != 3 {
		t.Fatalf("Status(after) = %+v, want all three current", status)
	}
	assertDecrypts(t, current, secrets.PurposePublicWAFCookieSigningSecret, 1, rawWAFSettingsSecret(t, database), "cookie-secret")
	assertDecrypts(t, current, secrets.PurposeEnvironmentAccessToken, 100, rawEnvironmentSecret(t, database, 100), "p2pat_old")
}

func TestReconcileFailsBeforeWritingUnsafeRows(t *testing.T) {
	ctx := context.Background()
	database := newSecretStoreTestDB(t)
	current := newSecretStoreTestService(t, secretStoreTestKey(2), "current", "", false)
	unknown := newSecretStoreTestService(t, secretStoreTestKey(1), "unknown", "", false)
	unknownSecret, err := unknown.Encrypt(secrets.PurposePublicWAFCookieSigningSecret, 1, "cookie-secret")
	if err != nil {
		t.Fatalf("encrypt unknown-key secret: %v", err)
	}
	insertWAFSettingsSecret(t, database, unknownSecret)
	insertEnvironmentSecret(t, database, 100, "remote-plain", "p2pat_plain")

	store := New(database.DB, current)
	status, err := store.Status(ctx, 2)
	if err != nil {
		t.Fatalf("Status() error = %v", err)
	}
	if status.MissingKey != 1 || status.Plaintext != 1 {
		t.Fatalf("Status() = %+v, want missing_key=1 plaintext=1", status)
	}
	if _, err := store.reconcileField(ctx, fieldByPurpose(t, secrets.PurposePublicWAFCookieSigningSecret), 1); err == nil || !strings.Contains(err.Error(), "not configured") {
		t.Fatalf("reconcileField() error = %v, want write-pass missing key failure", err)
	}
	err = func() error {
		_, err := store.Reconcile(ctx, ReconcileOptions{BatchSize: 2})
		return err
	}()
	if err == nil || !strings.Contains(err.Error(), "missing_key=1") {
		t.Fatalf("Reconcile() error = %v, want missing key preflight failure", err)
	}
	if got := rawEnvironmentSecret(t, database, 100); got != "p2pat_plain" {
		t.Fatalf("preflight failure mutated plaintext row = %q", got)
	}
}

func TestStatusClassifiesInvalidAndDecryptFailedRows(t *testing.T) {
	ctx := context.Background()
	database := newSecretStoreTestDB(t)
	service := newSecretStoreTestService(t, secretStoreTestKey(1), "current", "", false)
	insertWAFSettingsSecret(t, database, secrets.EnvelopePrefix+"not-base64")
	wrongPurposeSecret, err := service.Encrypt(secrets.PurposePublicWAFCookieSigningSecret, 1, "cookie-secret")
	if err != nil {
		t.Fatalf("encrypt wrong-purpose secret: %v", err)
	}
	insertEnvironmentSecret(t, database, 100, "remote-wrong-purpose", wrongPurposeSecret)

	store := New(database.DB, service)
	status, err := store.Status(ctx, 2)
	if err != nil {
		t.Fatalf("Status() error = %v", err)
	}
	if status.Invalid != 1 || status.DecryptFailed != 1 {
		t.Fatalf("Status() = %+v, want invalid=1 decrypt_failed=1", status)
	}
	_, err = store.Reconcile(ctx, ReconcileOptions{DryRun: true, BatchSize: 2})
	if err == nil || !strings.Contains(err.Error(), "invalid=1 decrypt_failed=1") {
		t.Fatalf("Reconcile(dry-run) error = %v, want invalid/decrypt failure", err)
	}
}

func TestReconcileRewrapsLegacyTargetScopedHeaderAAD(t *testing.T) {
	ctx := context.Background()
	database := newSecretStoreTestDB(t)
	service := newSecretStoreTestService(t, secretStoreTestKey(1), "current", "", false)
	legacy, err := service.Encrypt(secrets.PurposePublicRouteTargetSensitiveHeader, 20, "session=legacy")
	if err != nil {
		t.Fatalf("encrypt legacy header: %v", err)
	}
	insertRouteHeaderSecret(t, database, 20, 30, legacy)

	store := New(database.DB, service)
	status, err := store.Status(ctx, 2)
	if err != nil {
		t.Fatalf("Status() error = %v", err)
	}
	if status.NeedsRewrap != 1 || status.DecryptFailed != 0 {
		t.Fatalf("Status() = %+v, want legacy header to need rewrap", status)
	}
	result, err := store.Reconcile(ctx, ReconcileOptions{BatchSize: 1})
	if err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
	if result.Rewrapped != 1 {
		t.Fatalf("Reconcile().Rewrapped = %d, want 1", result.Rewrapped)
	}
	stored := rawHeaderSecret(t, database, 30)
	assertDecrypts(t, service, secrets.PurposePublicRouteTargetSensitiveHeader, 30, stored, "session=legacy")
	if _, _, err := service.Decrypt(secrets.PurposePublicRouteTargetSensitiveHeader, 20, stored); err == nil {
		t.Fatal("expected rewrapped header to reject legacy target-scoped AAD")
	}
}

func TestReconcileRejectsPlaintextWhenRequired(t *testing.T) {
	ctx := context.Background()
	database := newSecretStoreTestDB(t)
	insertWAFSettingsSecret(t, database, "cookie-secret")
	service := newSecretStoreTestService(t, secretStoreTestKey(1), "current", "", true)

	_, err := New(database.DB, service).Reconcile(ctx, ReconcileOptions{})
	if err == nil || !strings.Contains(err.Error(), "plaintext but secrets encryption is required") {
		t.Fatalf("Reconcile() error = %v, want required plaintext failure", err)
	}
	if got := rawWAFSettingsSecret(t, database); got != "cookie-secret" {
		t.Fatalf("required-mode failure mutated plaintext row = %q", got)
	}
}

func newSecretStoreTestDB(t *testing.T) *db.DB {
	t.Helper()
	database, err := db.Open(filepath.Join(t.TempDir(), "secretstore-test.db"))
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	t.Cleanup(func() {
		_ = database.Close()
	})
	return database
}

func newSecretStoreTestService(t *testing.T, currentKey, currentID, previousKeys string, required bool) *secrets.Service {
	t.Helper()
	service, err := secrets.NewService(secrets.KeyConfig{
		CurrentKey:     currentKey,
		CurrentKeyID:   currentID,
		PreviousKeys:   previousKeys,
		Required:       required,
		AllowPlaintext: !required,
	})
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}
	return service
}

func insertWAFSettingsSecret(t *testing.T, database *db.DB, value string) {
	t.Helper()
	if _, err := database.ExecContext(context.Background(), `INSERT INTO public_waf_settings (id, cookie_signing_secret) VALUES (1, ?)`, value); err != nil {
		t.Fatalf("insert WAF settings: %v", err)
	}
}

func insertEnvironmentSecret(t *testing.T, database *db.DB, id int64, name, value string) {
	t.Helper()
	if _, err := database.ExecContext(context.Background(), `INSERT INTO environments (id, name, management_url, transport, access_token) VALUES (?, ?, 'https://remote.example:8081', 'direct', ?)`, id, name, value); err != nil {
		t.Fatalf("insert environment: %v", err)
	}
}

func insertCaptchaSecret(t *testing.T, database *db.DB, id int64, name, value string) {
	t.Helper()
	if _, err := database.ExecContext(context.Background(), `INSERT INTO public_waf_captcha_providers (id, name, provider_type, site_key, secret_key) VALUES (?, ?, 'turnstile', 'site', ?)`, id, name, value); err != nil {
		t.Fatalf("insert captcha provider: %v", err)
	}
}

func insertRouteHeaderSecret(t *testing.T, database *db.DB, targetID, headerID int64, value string) {
	t.Helper()
	statements := []struct {
		sql  string
		args []interface{}
	}{
		{`INSERT INTO public_listeners (id, name, port, protocol) VALUES (1, 'listener', 8080, 'http')`, nil},
		{`INSERT INTO public_routes (id, listener_id, priority, path_prefix) VALUES (10, 1, 10, '/legacy')`, nil},
		{`INSERT INTO public_route_targets (id, route_id, position, name, target_type, url) VALUES (?, 10, 0, 'target', 'proxy', 'http://127.0.0.1:9000')`, []interface{}{targetID}},
		{`INSERT INTO public_route_target_upstream_headers (id, target_id, position, name, value, sensitive) VALUES (?, ?, 0, 'Cookie', ?, 1)`, []interface{}{headerID, targetID, value}},
	}
	for _, statement := range statements {
		if _, err := database.ExecContext(context.Background(), statement.sql, statement.args...); err != nil {
			t.Fatalf("insert route header fixture %q: %v", statement.sql, err)
		}
	}
}

func rawWAFSettingsSecret(t *testing.T, database *db.DB) string {
	t.Helper()
	var value string
	if err := database.QueryRowContext(context.Background(), `SELECT cookie_signing_secret FROM public_waf_settings WHERE id = 1`).Scan(&value); err != nil {
		t.Fatalf("query WAF settings: %v", err)
	}
	return value
}

func rawEnvironmentSecret(t *testing.T, database *db.DB, id int64) string {
	t.Helper()
	var value string
	if err := database.QueryRowContext(context.Background(), `SELECT access_token FROM environments WHERE id = ?`, id).Scan(&value); err != nil {
		t.Fatalf("query environment secret: %v", err)
	}
	return value
}

func rawHeaderSecret(t *testing.T, database *db.DB, id int64) string {
	t.Helper()
	var value string
	if err := database.QueryRowContext(context.Background(), `SELECT value FROM public_route_target_upstream_headers WHERE id = ?`, id).Scan(&value); err != nil {
		t.Fatalf("query header secret: %v", err)
	}
	return value
}

func assertDecrypts(t *testing.T, service *secrets.Service, purpose secrets.Purpose, ownerID int64, stored, want string) {
	t.Helper()
	if !secrets.IsEncrypted(stored) {
		t.Fatalf("stored %s/%d is not encrypted: %q", purpose, ownerID, stored)
	}
	if strings.Contains(stored, want) {
		t.Fatalf("stored %s/%d leaked plaintext %q", purpose, ownerID, want)
	}
	got, _, err := service.Decrypt(purpose, ownerID, stored)
	if err != nil {
		t.Fatalf("Decrypt(%s, %d) error = %v", purpose, ownerID, err)
	}
	if got != want {
		t.Fatalf("Decrypt(%s, %d) = %q, want %q", purpose, ownerID, got, want)
	}
}

func fieldByPurpose(t *testing.T, purpose secrets.Purpose) Field {
	t.Helper()
	for _, field := range Fields {
		if field.Purpose == purpose {
			return field
		}
	}
	t.Fatalf("field for purpose %s not found", purpose)
	return Field{}
}

func secretStoreTestKey(seed byte) string {
	key := make([]byte, 32)
	for i := range key {
		key[i] = seed + byte(i)
	}
	return base64.RawURLEncoding.EncodeToString(key)
}
