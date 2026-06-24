package cmd

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"p2pstream/internal/db"
	"p2pstream/internal/secrets"
	"p2pstream/internal/secretstore"
)

func TestSecretsGenerateKeyEnvOutput(t *testing.T) {
	var out bytes.Buffer
	if err := runSecretsGenerateKey(secretsGenerateKeyOptions{
		Format: "env",
		Stdout: &out,
	}); err != nil {
		t.Fatalf("generate key: %v", err)
	}

	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 2 {
		t.Fatalf("output lines = %d, want 2: %q", len(lines), out.String())
	}
	key := strings.TrimPrefix(lines[0], "SECRETS_ENCRYPTION_KEY=")
	keyID := strings.TrimPrefix(lines[1], "SECRETS_ENCRYPTION_KEY_ID=")
	raw, err := secrets.ParseKey(key)
	if err != nil {
		t.Fatalf("ParseKey(output) error = %v", err)
	}
	if keyID != secrets.DefaultKeyID(raw) {
		t.Fatalf("key ID = %q, want %q", keyID, secrets.DefaultKeyID(raw))
	}
}

func TestSecretsStatusJSONDoesNotLeakSecretValues(t *testing.T) {
	dbPath := emptySecretsCommandDB(t)
	database := openSecretsCommandDB(t, dbPath)
	insertSecretsCommandWAFSecret(t, database, "cookie-secret")
	configureSecretsCommandEnv(t, secretsCommandTestKey(1), "current", "", false)

	var out bytes.Buffer
	if err := runSecretsStatus(context.Background(), secretsStatusOptions{
		DatabaseURL: dbPath,
		Format:      "json",
		BatchSize:   1,
		Stdout:      &out,
	}); err != nil {
		t.Fatalf("status: %v", err)
	}
	if strings.Contains(out.String(), "cookie-secret") {
		t.Fatalf("status output leaked secret value: %q", out.String())
	}
	var status secretstore.Status
	if err := json.Unmarshal(out.Bytes(), &status); err != nil {
		t.Fatalf("unmarshal status: %v", err)
	}
	if !status.EncryptionOn || status.CurrentKeyID != "current" {
		t.Fatalf("status key metadata = enabled=%t current=%q", status.EncryptionOn, status.CurrentKeyID)
	}
	if status.Plaintext != 1 || status.Total != 1 {
		t.Fatalf("status counts = %+v, want one plaintext secret", status)
	}
}

func TestSecretsRewrapRequiresDryRunOrYes(t *testing.T) {
	err := runSecretsRewrap(context.Background(), secretsRewrapOptions{
		DatabaseURL: "unused.db",
		Stdout:      io.Discard,
	})
	if err == nil || !strings.Contains(err.Error(), "requires --dry-run or --yes") {
		t.Fatalf("rewrap error = %v, want confirmation failure", err)
	}
}

func TestSecretsRewrapDryRunAndYes(t *testing.T) {
	dbPath := emptySecretsCommandDB(t)
	database := openSecretsCommandDB(t, dbPath)
	insertSecretsCommandWAFSecret(t, database, "cookie-secret")
	configureSecretsCommandEnv(t, secretsCommandTestKey(1), "current", "", false)

	var dryRunOut bytes.Buffer
	if err := runSecretsRewrap(context.Background(), secretsRewrapOptions{
		DatabaseURL: dbPath,
		Format:      "json",
		BatchSize:   1,
		DryRun:      true,
		Stdout:      &dryRunOut,
	}); err != nil {
		t.Fatalf("rewrap dry-run: %v", err)
	}
	if got := rawSecretsCommandWAFSecret(t, database); got != "cookie-secret" {
		t.Fatalf("dry-run mutated secret = %q", got)
	}
	var dryRun secretstore.ReconcileResult
	if err := json.Unmarshal(dryRunOut.Bytes(), &dryRun); err != nil {
		t.Fatalf("unmarshal dry-run result: %v", err)
	}
	if !dryRun.DryRun || dryRun.WouldEncrypt != 1 || dryRun.Encrypted != 0 {
		t.Fatalf("dry-run result = %+v, want would_encrypt=1 and no writes", dryRun)
	}

	var rewrapOut bytes.Buffer
	if err := runSecretsRewrap(context.Background(), secretsRewrapOptions{
		DatabaseURL: dbPath,
		Format:      "json",
		BatchSize:   1,
		Yes:         true,
		Stdout:      &rewrapOut,
	}); err != nil {
		t.Fatalf("rewrap --yes: %v", err)
	}
	if strings.Contains(rewrapOut.String(), "cookie-secret") {
		t.Fatalf("rewrap output leaked secret value: %q", rewrapOut.String())
	}
	var result secretstore.ReconcileResult
	if err := json.Unmarshal(rewrapOut.Bytes(), &result); err != nil {
		t.Fatalf("unmarshal rewrap result: %v", err)
	}
	if result.Encrypted != 1 || result.Rewrapped != 0 {
		t.Fatalf("rewrap result = %+v, want encrypted=1", result)
	}
	stored := rawSecretsCommandWAFSecret(t, database)
	if !secrets.IsEncrypted(stored) {
		t.Fatalf("stored secret = %q, want encrypted", stored)
	}
	service, err := secrets.NewService(secrets.KeyConfig{
		CurrentKey:     secretsCommandTestKey(1),
		CurrentKeyID:   "current",
		AllowPlaintext: true,
	})
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}
	got, _, err := service.Decrypt(secrets.PurposePublicWAFCookieSigningSecret, 1, stored)
	if err != nil {
		t.Fatalf("decrypt stored secret: %v", err)
	}
	if got != "cookie-secret" {
		t.Fatalf("decrypted stored secret = %q, want cookie-secret", got)
	}
}

func TestSecretsStatusAndRewrapIncludePrivateKeyFiles(t *testing.T) {
	dbPath := emptySecretsCommandDB(t)
	database := openSecretsCommandDB(t, dbPath)
	configDir := configureSecretsCommandEnv(t, secretsCommandTestKey(1), "current", "", false)
	keyPath := filepath.Join(configDir, "certs", "public-listener-12", "tls-34.key.pem")
	if err := os.MkdirAll(filepath.Dir(keyPath), 0700); err != nil {
		t.Fatalf("create key dir: %v", err)
	}
	privateKeyPEM := "-----BEGIN PRIVATE KEY-----\ncmd-test-key\n-----END PRIVATE KEY-----\n"
	if err := os.WriteFile(keyPath, []byte(privateKeyPEM), 0600); err != nil {
		t.Fatalf("write private key: %v", err)
	}
	if _, err := database.ExecContext(context.Background(), `INSERT INTO public_listeners (id, name, port, protocol, enabled) VALUES (12, 'https', 443, 'https', 1)`); err != nil {
		t.Fatalf("insert public listener: %v", err)
	}
	if _, err := database.ExecContext(context.Background(), `INSERT INTO public_tls_certificates (id, listener_id, hostname_pattern, cert_path, key_path, enabled, source, status) VALUES (34, 12, 'cmd.example.com', '', ?, 1, 'manual', 'ready')`, keyPath); err != nil {
		t.Fatalf("insert public TLS certificate: %v", err)
	}

	var statusOut bytes.Buffer
	if err := runSecretsStatus(context.Background(), secretsStatusOptions{
		DatabaseURL: dbPath,
		Format:      "json",
		BatchSize:   1,
		Stdout:      &statusOut,
	}); err != nil {
		t.Fatalf("status: %v", err)
	}
	if strings.Contains(statusOut.String(), "PRIVATE KEY") || strings.Contains(statusOut.String(), "cmd-test-key") {
		t.Fatalf("status output leaked key material: %q", statusOut.String())
	}
	var status secretstore.Status
	if err := json.Unmarshal(statusOut.Bytes(), &status); err != nil {
		t.Fatalf("unmarshal status: %v", err)
	}
	if status.Total != 1 || status.Plaintext != 1 {
		t.Fatalf("status = %+v, want one plaintext private key file", status)
	}

	var dryRunOut bytes.Buffer
	if err := runSecretsRewrap(context.Background(), secretsRewrapOptions{
		DatabaseURL: dbPath,
		Format:      "json",
		BatchSize:   1,
		DryRun:      true,
		Stdout:      &dryRunOut,
	}); err != nil {
		t.Fatalf("rewrap dry-run: %v", err)
	}
	if got, err := os.ReadFile(keyPath); err != nil || string(got) != privateKeyPEM {
		t.Fatalf("dry-run key file = %q/%v, want unchanged plaintext", got, err)
	}

	var rewrapOut bytes.Buffer
	if err := runSecretsRewrap(context.Background(), secretsRewrapOptions{
		DatabaseURL: dbPath,
		Format:      "json",
		BatchSize:   1,
		Yes:         true,
		Stdout:      &rewrapOut,
	}); err != nil {
		t.Fatalf("rewrap --yes: %v", err)
	}
	if strings.Contains(rewrapOut.String(), "PRIVATE KEY") || strings.Contains(rewrapOut.String(), "cmd-test-key") {
		t.Fatalf("rewrap output leaked key material: %q", rewrapOut.String())
	}
	var result secretstore.ReconcileResult
	if err := json.Unmarshal(rewrapOut.Bytes(), &result); err != nil {
		t.Fatalf("unmarshal rewrap result: %v", err)
	}
	if result.Encrypted != 1 {
		t.Fatalf("rewrap result = %+v, want one encrypted key file", result)
	}
	stored, err := os.ReadFile(keyPath)
	if err != nil {
		t.Fatalf("read encrypted key: %v", err)
	}
	if !secrets.IsEncrypted(string(stored)) || strings.Contains(string(stored), "PRIVATE KEY") {
		t.Fatalf("private key file was not encrypted: %q", stored)
	}
	service, err := secrets.NewService(secrets.KeyConfig{
		CurrentKey:     secretsCommandTestKey(1),
		CurrentKeyID:   "current",
		AllowPlaintext: true,
	})
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}
	got, _, err := service.Decrypt(secrets.PurposeFilePublicTLSPrivateKey, 34, string(stored))
	if err != nil {
		t.Fatalf("decrypt key file: %v", err)
	}
	if got != privateKeyPEM {
		t.Fatalf("decrypted key = %q, want original PEM", got)
	}
}

func emptySecretsCommandDB(t *testing.T) string {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "p2pstream-secrets-test.db")
	database, err := db.Open(dbPath)
	if err != nil {
		t.Fatalf("open empty db: %v", err)
	}
	if err := database.Close(); err != nil {
		t.Fatalf("close empty db: %v", err)
	}
	return dbPath
}

func openSecretsCommandDB(t *testing.T, dbPath string) *db.DB {
	t.Helper()
	database, err := db.Open(dbPath)
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	t.Cleanup(func() {
		_ = database.Close()
	})
	return database
}

func insertSecretsCommandWAFSecret(t *testing.T, database *db.DB, value string) {
	t.Helper()
	if _, err := database.ExecContext(context.Background(), `INSERT INTO public_waf_settings (id, cookie_signing_secret) VALUES (1, ?)`, value); err != nil {
		t.Fatalf("insert WAF settings: %v", err)
	}
}

func rawSecretsCommandWAFSecret(t *testing.T, database *db.DB) string {
	t.Helper()
	var value string
	if err := database.QueryRowContext(context.Background(), `SELECT cookie_signing_secret FROM public_waf_settings WHERE id = 1`).Scan(&value); err != nil {
		t.Fatalf("query WAF settings: %v", err)
	}
	return value
}

func configureSecretsCommandEnv(t *testing.T, key, keyID, previousKeys string, required bool) string {
	t.Helper()
	configDir := t.TempDir()
	t.Setenv("CONFIG_DIR", configDir)
	t.Setenv("SECRETS_ENCRYPTION_KEY", key)
	t.Setenv("SECRETS_ENCRYPTION_KEY_ID", keyID)
	t.Setenv("SECRETS_ENCRYPTION_PREVIOUS_KEYS", previousKeys)
	if required {
		t.Setenv("SECRETS_ENCRYPTION_REQUIRED", "true")
	} else {
		t.Setenv("SECRETS_ENCRYPTION_REQUIRED", "false")
	}
	return configDir
}

func secretsCommandTestKey(seed byte) string {
	key := make([]byte, 32)
	for i := range key {
		key[i] = seed + byte(i)
	}
	return base64.RawURLEncoding.EncodeToString(key)
}
