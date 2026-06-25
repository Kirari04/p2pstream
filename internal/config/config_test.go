package config

import (
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadDerivesDatabaseURLFromConfigDir(t *testing.T) {
	workDir := isolatedConfigTestDir(t)
	configDir := filepath.Join(workDir, "custom-data")
	t.Setenv("CONFIG_DIR", configDir)

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	wantDBPath := filepath.Join(configDir, databaseFileName)
	assertSQLiteURL(t, cfg.DatabaseURL, wantDBPath)
	if cfg.ConfigDir != filepath.Clean(configDir) {
		t.Fatalf("ConfigDir = %q, want %q", cfg.ConfigDir, filepath.Clean(configDir))
	}
	if cfg.CertsDir != filepath.Join(filepath.Clean(configDir), certsDirName) {
		t.Fatalf("CertsDir = %q, want %q", cfg.CertsDir, filepath.Join(filepath.Clean(configDir), certsDirName))
	}
	assertMode(t, cfg.ConfigDir, 0700)
	assertMode(t, cfg.CertsDir, 0700)
}

func TestLoadRespectsExplicitDatabaseURL(t *testing.T) {
	workDir := isolatedConfigTestDir(t)
	configDir := filepath.Join(workDir, "data")
	explicitDatabaseURL := "file:/tmp/p2pstream-custom.db?mode=ro"
	t.Setenv("CONFIG_DIR", configDir)
	t.Setenv("DATABASE_URL", explicitDatabaseURL)

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	if cfg.DatabaseURL != explicitDatabaseURL {
		t.Fatalf("DatabaseURL = %q, want %q", cfg.DatabaseURL, explicitDatabaseURL)
	}
	assertMode(t, cfg.ConfigDir, 0700)
	assertMode(t, cfg.CertsDir, 0700)
}

func TestLoadWorksWithDockerStyleConfigDir(t *testing.T) {
	workDir := isolatedConfigTestDir(t)
	configDir := filepath.Join(workDir, "data")
	t.Setenv("CONFIG_DIR", configDir)

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	assertSQLiteURL(t, cfg.DatabaseURL, filepath.Join(configDir, databaseFileName))
	if _, err := os.Stat(filepath.Join(configDir, certsDirName)); err != nil {
		t.Fatalf("expected Docker-style certs directory to exist: %v", err)
	}
	if cfg.PublicCacheDir != filepath.Join(configDir, "cache", "public") {
		t.Fatalf("PublicCacheDir = %q, want default under CONFIG_DIR", cfg.PublicCacheDir)
	}
}

func TestLoadRespectsExplicitPublicCacheDir(t *testing.T) {
	workDir := isolatedConfigTestDir(t)
	configDir := filepath.Join(workDir, "data")
	cacheDir := filepath.Join(workDir, "public-cache")
	t.Setenv("CONFIG_DIR", configDir)
	t.Setenv("PUBLIC_CACHE_DIR", cacheDir)

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.PublicCacheDir != filepath.Clean(cacheDir) {
		t.Fatalf("PublicCacheDir = %q, want %q", cfg.PublicCacheDir, filepath.Clean(cacheDir))
	}
}

func TestLoadSupportsDisablingManagementUI(t *testing.T) {
	workDir := isolatedConfigTestDir(t)
	t.Setenv("CONFIG_DIR", filepath.Join(workDir, "data"))
	t.Setenv("MANAGEMENT_UI_DISABLED", "true")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if !cfg.ManagementUIDisabled {
		t.Fatal("expected ManagementUIDisabled to be true")
	}
}

func TestLoadManagementBindAndSecurityDefaults(t *testing.T) {
	workDir := isolatedConfigTestDir(t)
	t.Setenv("CONFIG_DIR", filepath.Join(workDir, "data"))

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.ManagementBindAddress != "0.0.0.0" {
		t.Fatalf("ManagementBindAddress = %q, want all-interface default", cfg.ManagementBindAddress)
	}
	if cfg.ObservabilityMaxRows != 1_000_000 {
		t.Fatalf("ObservabilityMaxRows = %d, want 1000000", cfg.ObservabilityMaxRows)
	}
	if cfg.LoginThrottleMaxKeys != 50_000 {
		t.Fatalf("LoginThrottleMaxKeys = %d, want 50000", cfg.LoginThrottleMaxKeys)
	}
}

func TestLoadRespectsExplicitManagementBindAddress(t *testing.T) {
	workDir := isolatedConfigTestDir(t)
	t.Setenv("CONFIG_DIR", filepath.Join(workDir, "data"))
	t.Setenv("MANAGEMENT_BIND_ADDRESS", "127.0.0.1")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.ManagementBindAddress != "127.0.0.1" {
		t.Fatalf("ManagementBindAddress = %q, want 127.0.0.1", cfg.ManagementBindAddress)
	}
}

func TestLoadValidatesSecurityLimitBounds(t *testing.T) {
	t.Run("observability max rows may be disabled explicitly", func(t *testing.T) {
		workDir := isolatedConfigTestDir(t)
		t.Setenv("CONFIG_DIR", filepath.Join(workDir, "data"))
		t.Setenv("OBSERVABILITY_MAX_ROWS", "0")

		cfg, err := Load()
		if err != nil {
			t.Fatalf("Load() error = %v", err)
		}
		if cfg.ObservabilityMaxRows != 0 {
			t.Fatalf("ObservabilityMaxRows = %d, want 0", cfg.ObservabilityMaxRows)
		}
	})

	t.Run("negative observability max rows rejected", func(t *testing.T) {
		workDir := isolatedConfigTestDir(t)
		t.Setenv("CONFIG_DIR", filepath.Join(workDir, "data"))
		t.Setenv("OBSERVABILITY_MAX_ROWS", "-1")

		if _, err := Load(); err == nil {
			t.Fatal("expected negative OBSERVABILITY_MAX_ROWS to fail")
		}
	})

	t.Run("non-positive login throttle max keys rejected", func(t *testing.T) {
		workDir := isolatedConfigTestDir(t)
		t.Setenv("CONFIG_DIR", filepath.Join(workDir, "data"))
		t.Setenv("LOGIN_THROTTLE_MAX_KEYS", "0")

		if _, err := Load(); err == nil {
			t.Fatal("expected zero LOGIN_THROTTLE_MAX_KEYS to fail")
		}
	})
}

func TestLoadValidatesSecretsEncryptionConfig(t *testing.T) {
	t.Run("valid current key", func(t *testing.T) {
		workDir := isolatedConfigTestDir(t)
		t.Setenv("CONFIG_DIR", filepath.Join(workDir, "data"))
		t.Setenv("SECRETS_ENCRYPTION_KEY", "AQIDBAUGBwgJCgsMDQ4PEBESExQVFhcYGRobHB0eHyA")
		t.Setenv("SECRETS_ENCRYPTION_KEY_ID", "primary")

		cfg, err := Load()
		if err != nil {
			t.Fatalf("Load() error = %v", err)
		}
		if cfg.SecretsEncryptionKeyID != "primary" {
			t.Fatalf("SecretsEncryptionKeyID = %q, want primary", cfg.SecretsEncryptionKeyID)
		}
	})

	t.Run("valid key file", func(t *testing.T) {
		workDir := isolatedConfigTestDir(t)
		keyFile := filepath.Join(workDir, "secrets.key")
		if err := os.WriteFile(keyFile, []byte("AQIDBAUGBwgJCgsMDQ4PEBESExQVFhcYGRobHB0eHyA\n"), 0600); err != nil {
			t.Fatalf("write key file: %v", err)
		}
		t.Setenv("CONFIG_DIR", filepath.Join(workDir, "data"))
		t.Setenv("SECRETS_ENCRYPTION_KEY_FILE", keyFile)
		t.Setenv("SECRETS_ENCRYPTION_KEY_ID", "file-key")

		cfg, err := Load()
		if err != nil {
			t.Fatalf("Load() error = %v", err)
		}
		if cfg.SecretsEncryptionKey != "AQIDBAUGBwgJCgsMDQ4PEBESExQVFhcYGRobHB0eHyA" {
			t.Fatalf("SecretsEncryptionKey = %q, want key file contents", cfg.SecretsEncryptionKey)
		}
		if cfg.SecretsEncryptionKeyFile != keyFile {
			t.Fatalf("SecretsEncryptionKeyFile = %q, want %q", cfg.SecretsEncryptionKeyFile, keyFile)
		}
	})

	t.Run("key and key file are mutually exclusive", func(t *testing.T) {
		workDir := isolatedConfigTestDir(t)
		keyFile := filepath.Join(workDir, "secrets.key")
		if err := os.WriteFile(keyFile, []byte("AQIDBAUGBwgJCgsMDQ4PEBESExQVFhcYGRobHB0eHyA"), 0600); err != nil {
			t.Fatalf("write key file: %v", err)
		}
		t.Setenv("CONFIG_DIR", filepath.Join(workDir, "data"))
		t.Setenv("SECRETS_ENCRYPTION_KEY", "AQIDBAUGBwgJCgsMDQ4PEBESExQVFhcYGRobHB0eHyA")
		t.Setenv("SECRETS_ENCRYPTION_KEY_FILE", keyFile)

		if _, err := Load(); err == nil {
			t.Fatal("expected setting key and key file together to fail")
		}
	})

	t.Run("key file rejects group or other permissions", func(t *testing.T) {
		workDir := isolatedConfigTestDir(t)
		keyFile := filepath.Join(workDir, "secrets.key")
		if err := os.WriteFile(keyFile, []byte("AQIDBAUGBwgJCgsMDQ4PEBESExQVFhcYGRobHB0eHyA"), 0644); err != nil {
			t.Fatalf("write key file: %v", err)
		}
		t.Setenv("CONFIG_DIR", filepath.Join(workDir, "data"))
		t.Setenv("SECRETS_ENCRYPTION_KEY_FILE", keyFile)

		err := func() error {
			_, err := Load()
			return err
		}()
		if err == nil || !strings.Contains(err.Error(), "allow group/other access") {
			t.Fatalf("Load() error = %v, want key file permission failure", err)
		}
	})

	t.Run("required without key rejected", func(t *testing.T) {
		workDir := isolatedConfigTestDir(t)
		t.Setenv("CONFIG_DIR", filepath.Join(workDir, "data"))
		t.Setenv("SECRETS_ENCRYPTION_REQUIRED", "true")

		if _, err := Load(); err == nil {
			t.Fatal("expected required secrets encryption without key to fail")
		}
	})

	t.Run("invalid key rejected", func(t *testing.T) {
		workDir := isolatedConfigTestDir(t)
		t.Setenv("CONFIG_DIR", filepath.Join(workDir, "data"))
		t.Setenv("SECRETS_ENCRYPTION_KEY", "not-a-32-byte-key")

		if _, err := Load(); err == nil {
			t.Fatal("expected invalid secrets encryption key to fail")
		}
	})

	t.Run("previous key requires current key", func(t *testing.T) {
		workDir := isolatedConfigTestDir(t)
		t.Setenv("CONFIG_DIR", filepath.Join(workDir, "data"))
		t.Setenv("SECRETS_ENCRYPTION_PREVIOUS_KEYS", "old:AQIDBAUGBwgJCgsMDQ4PEBESExQVFhcYGRobHB0eHyA")

		if _, err := Load(); err == nil {
			t.Fatal("expected previous key without current key to fail")
		}
	})

	t.Run("valid Vault Transit provider with token file", func(t *testing.T) {
		workDir := isolatedConfigTestDir(t)
		tokenFile := filepath.Join(workDir, "vault.token")
		if err := os.WriteFile(tokenFile, []byte("vault-token\n"), 0600); err != nil {
			t.Fatalf("write token file: %v", err)
		}
		t.Setenv("CONFIG_DIR", filepath.Join(workDir, "data"))
		t.Setenv("SECRETS_ENCRYPTION_PROVIDER", "vault-transit")
		t.Setenv("SECRETS_ENCRYPTION_VAULT_ADDR", "https://vault.example.com")
		t.Setenv("SECRETS_ENCRYPTION_VAULT_TOKEN_FILE", tokenFile)
		t.Setenv("SECRETS_ENCRYPTION_VAULT_KEY", "p2pstream")
		t.Setenv("SECRETS_ENCRYPTION_REQUIRED", "true")

		cfg, err := Load()
		if err != nil {
			t.Fatalf("Load() error = %v", err)
		}
		if cfg.SecretsEncryptionProvider != "vault-transit" {
			t.Fatalf("SecretsEncryptionProvider = %q, want vault-transit", cfg.SecretsEncryptionProvider)
		}
		if cfg.SecretsVaultToken != "vault-token" {
			t.Fatalf("SecretsVaultToken = %q, want token file contents", cfg.SecretsVaultToken)
		}
		if cfg.SecretsVaultDEKCacheMaxEntries != 1024 {
			t.Fatalf("SecretsVaultDEKCacheMaxEntries = %d, want 1024", cfg.SecretsVaultDEKCacheMaxEntries)
		}
		if cfg.SecretsVaultDEKCacheTTL.String() != "5m0s" {
			t.Fatalf("SecretsVaultDEKCacheTTL = %s, want 5m0s", cfg.SecretsVaultDEKCacheTTL)
		}
	})

	t.Run("Vault DEK cache can be disabled with both bounds zero", func(t *testing.T) {
		workDir := isolatedConfigTestDir(t)
		t.Setenv("CONFIG_DIR", filepath.Join(workDir, "data"))
		t.Setenv("SECRETS_ENCRYPTION_PROVIDER", "vault-transit")
		t.Setenv("SECRETS_ENCRYPTION_VAULT_ADDR", "https://vault.example.com")
		t.Setenv("SECRETS_ENCRYPTION_VAULT_TOKEN", "vault-token")
		t.Setenv("SECRETS_ENCRYPTION_VAULT_KEY", "p2pstream")
		t.Setenv("SECRETS_ENCRYPTION_VAULT_DEK_CACHE_MAX_ENTRIES", "0")
		t.Setenv("SECRETS_ENCRYPTION_VAULT_DEK_CACHE_TTL", "0s")

		cfg, err := Load()
		if err != nil {
			t.Fatalf("Load() error = %v", err)
		}
		if cfg.SecretsVaultDEKCacheMaxEntries != 0 || cfg.SecretsVaultDEKCacheTTL != 0 {
			t.Fatalf("Vault DEK cache = %d/%s, want disabled", cfg.SecretsVaultDEKCacheMaxEntries, cfg.SecretsVaultDEKCacheTTL)
		}
	})

	t.Run("Vault DEK cache settings require Vault provider", func(t *testing.T) {
		workDir := isolatedConfigTestDir(t)
		t.Setenv("CONFIG_DIR", filepath.Join(workDir, "data"))
		t.Setenv("SECRETS_ENCRYPTION_VAULT_DEK_CACHE_MAX_ENTRIES", "64")

		err := func() error {
			_, err := Load()
			return err
		}()
		if err == nil || !strings.Contains(err.Error(), "Vault DEK cache settings require SECRETS_ENCRYPTION_PROVIDER=vault-transit") {
			t.Fatalf("Load() error = %v, want Vault DEK cache provider failure", err)
		}
	})

	t.Run("direct provider allows Compose default Vault DEK cache pass-through", func(t *testing.T) {
		workDir := isolatedConfigTestDir(t)
		t.Setenv("CONFIG_DIR", filepath.Join(workDir, "data"))
		t.Setenv("SECRETS_ENCRYPTION_VAULT_DEK_CACHE_MAX_ENTRIES", "1024")
		t.Setenv("SECRETS_ENCRYPTION_VAULT_DEK_CACHE_TTL", "5m")

		if _, err := Load(); err != nil {
			t.Fatalf("Load() error = %v, want direct provider to ignore default cache pass-through", err)
		}
	})

	t.Run("Vault DEK cache requires both bounds enabled together", func(t *testing.T) {
		workDir := isolatedConfigTestDir(t)
		t.Setenv("CONFIG_DIR", filepath.Join(workDir, "data"))
		t.Setenv("SECRETS_ENCRYPTION_PROVIDER", "vault-transit")
		t.Setenv("SECRETS_ENCRYPTION_VAULT_ADDR", "https://vault.example.com")
		t.Setenv("SECRETS_ENCRYPTION_VAULT_TOKEN", "vault-token")
		t.Setenv("SECRETS_ENCRYPTION_VAULT_KEY", "p2pstream")
		t.Setenv("SECRETS_ENCRYPTION_VAULT_DEK_CACHE_MAX_ENTRIES", "0")

		err := func() error {
			_, err := Load()
			return err
		}()
		if err == nil || !strings.Contains(err.Error(), "must both be positive or both be zero") {
			t.Fatalf("Load() error = %v, want mixed cache bound failure", err)
		}
	})

	t.Run("Vault DEK cache rejects over-cap values", func(t *testing.T) {
		workDir := isolatedConfigTestDir(t)
		t.Setenv("CONFIG_DIR", filepath.Join(workDir, "data"))
		t.Setenv("SECRETS_ENCRYPTION_PROVIDER", "vault-transit")
		t.Setenv("SECRETS_ENCRYPTION_VAULT_ADDR", "https://vault.example.com")
		t.Setenv("SECRETS_ENCRYPTION_VAULT_TOKEN", "vault-token")
		t.Setenv("SECRETS_ENCRYPTION_VAULT_KEY", "p2pstream")
		t.Setenv("SECRETS_ENCRYPTION_VAULT_DEK_CACHE_TTL", "2h")

		err := func() error {
			_, err := Load()
			return err
		}()
		if err == nil || !strings.Contains(err.Error(), "SECRETS_ENCRYPTION_VAULT_DEK_CACHE_TTL") {
			t.Fatalf("Load() error = %v, want cache TTL cap failure", err)
		}
	})

	t.Run("Vault settings require Vault provider", func(t *testing.T) {
		workDir := isolatedConfigTestDir(t)
		t.Setenv("CONFIG_DIR", filepath.Join(workDir, "data"))
		t.Setenv("SECRETS_ENCRYPTION_VAULT_ADDR", "https://vault.example.com")
		t.Setenv("SECRETS_ENCRYPTION_VAULT_TOKEN", "vault-token")
		t.Setenv("SECRETS_ENCRYPTION_VAULT_KEY", "p2pstream")

		err := func() error {
			_, err := Load()
			return err
		}()
		if err == nil || !strings.Contains(err.Error(), "require SECRETS_ENCRYPTION_PROVIDER=vault-transit") {
			t.Fatalf("Load() error = %v, want provider mismatch failure", err)
		}
	})

	t.Run("Vault mode rejects direct current key material", func(t *testing.T) {
		workDir := isolatedConfigTestDir(t)
		t.Setenv("CONFIG_DIR", filepath.Join(workDir, "data"))
		t.Setenv("SECRETS_ENCRYPTION_PROVIDER", "vault-transit")
		t.Setenv("SECRETS_ENCRYPTION_KEY", "AQIDBAUGBwgJCgsMDQ4PEBESExQVFhcYGRobHB0eHyA")
		t.Setenv("SECRETS_ENCRYPTION_VAULT_ADDR", "https://vault.example.com")
		t.Setenv("SECRETS_ENCRYPTION_VAULT_TOKEN", "vault-token")
		t.Setenv("SECRETS_ENCRYPTION_VAULT_KEY", "p2pstream")

		if _, err := Load(); err == nil {
			t.Fatal("expected Vault provider with direct current key to fail")
		}
	})

	t.Run("Vault token file rejects group or other permissions", func(t *testing.T) {
		workDir := isolatedConfigTestDir(t)
		tokenFile := filepath.Join(workDir, "vault.token")
		if err := os.WriteFile(tokenFile, []byte("vault-token"), 0644); err != nil {
			t.Fatalf("write token file: %v", err)
		}
		t.Setenv("CONFIG_DIR", filepath.Join(workDir, "data"))
		t.Setenv("SECRETS_ENCRYPTION_PROVIDER", "vault-transit")
		t.Setenv("SECRETS_ENCRYPTION_VAULT_ADDR", "https://vault.example.com")
		t.Setenv("SECRETS_ENCRYPTION_VAULT_TOKEN_FILE", tokenFile)
		t.Setenv("SECRETS_ENCRYPTION_VAULT_KEY", "p2pstream")

		err := func() error {
			_, err := Load()
			return err
		}()
		if err == nil || !strings.Contains(err.Error(), "SECRETS_ENCRYPTION_VAULT_TOKEN_FILE") || !strings.Contains(err.Error(), "allow group/other access") {
			t.Fatalf("Load() error = %v, want token file permission failure", err)
		}
	})

	t.Run("Vault address rejects non-loopback http", func(t *testing.T) {
		workDir := isolatedConfigTestDir(t)
		t.Setenv("CONFIG_DIR", filepath.Join(workDir, "data"))
		t.Setenv("SECRETS_ENCRYPTION_PROVIDER", "vault-transit")
		t.Setenv("SECRETS_ENCRYPTION_VAULT_ADDR", "http://vault.example.com")
		t.Setenv("SECRETS_ENCRYPTION_VAULT_TOKEN", "vault-token")
		t.Setenv("SECRETS_ENCRYPTION_VAULT_KEY", "p2pstream")

		if _, err := Load(); err == nil {
			t.Fatal("expected non-loopback http Vault address to fail")
		}
	})
}

func TestLoadMigratesLegacyDefaultDatabase(t *testing.T) {
	workDir := isolatedConfigTestDir(t)
	configDir := filepath.Join(workDir, "data")
	t.Setenv("CONFIG_DIR", configDir)

	legacyFiles := map[string]string{
		"p2pstream.db":     "legacy-db",
		"p2pstream.db-wal": "legacy-wal",
		"p2pstream.db-shm": "legacy-shm",
	}
	for name, contents := range legacyFiles {
		if err := os.WriteFile(name, []byte(contents), 0600); err != nil {
			t.Fatalf("failed to write legacy file %s: %v", name, err)
		}
	}

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	assertSQLiteURL(t, cfg.DatabaseURL, filepath.Join(configDir, databaseFileName))
	for name, contents := range legacyFiles {
		got, err := os.ReadFile(filepath.Join(configDir, name))
		if err != nil {
			t.Fatalf("failed to read migrated %s: %v", name, err)
		}
		if string(got) != contents {
			t.Fatalf("migrated %s = %q, want %q", name, string(got), contents)
		}
		if _, err := os.Stat(name); err != nil {
			t.Fatalf("legacy file %s should remain in place: %v", name, err)
		}
	}
}

func TestWritePublicTLSCertificateFilesUsesConfigCertsDir(t *testing.T) {
	workDir := isolatedConfigTestDir(t)
	configDir := filepath.Join(workDir, "data")
	t.Setenv("CONFIG_DIR", configDir)

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	certPath, keyPath, err := cfg.WritePublicTLSCertificateFiles(12, 34, []byte("cert-pem"), []byte("key-pem"))
	if err != nil {
		t.Fatalf("WritePublicTLSCertificateFiles() error = %v", err)
	}

	wantCertPath := filepath.Join(configDir, certsDirName, "public-listener-12", "tls-34.crt.pem")
	wantKeyPath := filepath.Join(configDir, certsDirName, "public-listener-12", "tls-34.key.pem")
	if certPath != wantCertPath {
		t.Fatalf("certPath = %q, want %q", certPath, wantCertPath)
	}
	if keyPath != wantKeyPath {
		t.Fatalf("keyPath = %q, want %q", keyPath, wantKeyPath)
	}

	assertFileContents(t, certPath, "cert-pem")
	assertFileContents(t, keyPath, "key-pem")
	assertMode(t, filepath.Dir(certPath), 0700)
	assertMode(t, certPath, 0600)
	assertMode(t, keyPath, 0600)
}

func TestLoadValidatesManagementTLSFiles(t *testing.T) {
	t.Run("default mode is auto", func(t *testing.T) {
		workDir := isolatedConfigTestDir(t)
		t.Setenv("CONFIG_DIR", filepath.Join(workDir, "data"))

		cfg, err := Load()
		if err != nil {
			t.Fatalf("Load() error = %v", err)
		}
		if cfg.ManagementTLSMode != "auto" {
			t.Fatalf("ManagementTLSMode = %q, want auto", cfg.ManagementTLSMode)
		}
	})

	t.Run("cert and key pair accepted", func(t *testing.T) {
		workDir := isolatedConfigTestDir(t)
		t.Setenv("CONFIG_DIR", filepath.Join(workDir, "data"))
		t.Setenv("MANAGEMENT_TLS_CERT_FILE", filepath.Join(workDir, "management.crt.pem"))
		t.Setenv("MANAGEMENT_TLS_KEY_FILE", filepath.Join(workDir, "management.key.pem"))
		t.Setenv("MANAGEMENT_TLS_CLIENT_CA_FILE", filepath.Join(workDir, "agents-ca.pem"))

		cfg, err := Load()
		if err != nil {
			t.Fatalf("Load() error = %v", err)
		}
		if cfg.ManagementTLSCertFile == "" || cfg.ManagementTLSKeyFile == "" || cfg.ManagementTLSClientCAFile == "" {
			t.Fatalf("expected management TLS fields to be populated: %+v", cfg)
		}
	})

	t.Run("partial cert and key rejected", func(t *testing.T) {
		workDir := isolatedConfigTestDir(t)
		t.Setenv("CONFIG_DIR", filepath.Join(workDir, "data"))
		t.Setenv("MANAGEMENT_TLS_CERT_FILE", filepath.Join(workDir, "management.crt.pem"))

		if _, err := Load(); err == nil {
			t.Fatal("expected partial management TLS config to fail")
		}
	})

	t.Run("client CA is accepted in auto mode", func(t *testing.T) {
		workDir := isolatedConfigTestDir(t)
		t.Setenv("CONFIG_DIR", filepath.Join(workDir, "data"))
		t.Setenv("MANAGEMENT_TLS_CLIENT_CA_FILE", filepath.Join(workDir, "agents-ca.pem"))

		cfg, err := Load()
		if err != nil {
			t.Fatalf("Load() error = %v", err)
		}
		if cfg.ManagementTLSClientCAFile == "" {
			t.Fatal("expected management client CA file to be retained")
		}
	})

	t.Run("provided mode requires cert and key", func(t *testing.T) {
		workDir := isolatedConfigTestDir(t)
		t.Setenv("CONFIG_DIR", filepath.Join(workDir, "data"))
		t.Setenv("MANAGEMENT_TLS_MODE", "provided")

		if _, err := Load(); err == nil {
			t.Fatal("expected provided mode without cert/key to fail")
		}
	})

	t.Run("off mode requires explicit insecure opt in", func(t *testing.T) {
		workDir := isolatedConfigTestDir(t)
		t.Setenv("CONFIG_DIR", filepath.Join(workDir, "data"))
		t.Setenv("MANAGEMENT_TLS_MODE", "off")

		if _, err := Load(); err == nil {
			t.Fatal("expected off mode without insecure opt in to fail")
		}
	})

	t.Run("off mode with explicit insecure opt in is accepted", func(t *testing.T) {
		workDir := isolatedConfigTestDir(t)
		t.Setenv("CONFIG_DIR", filepath.Join(workDir, "data"))
		t.Setenv("MANAGEMENT_TLS_MODE", "off")
		t.Setenv("MANAGEMENT_ALLOW_INSECURE_HTTP", "true")

		cfg, err := Load()
		if err != nil {
			t.Fatalf("Load() error = %v", err)
		}
		if cfg.ManagementTLSMode != "off" || !cfg.ManagementAllowInsecureHTTP {
			t.Fatalf("unexpected insecure management config: %+v", cfg)
		}
	})
}

func isolatedConfigTestDir(t *testing.T) string {
	t.Helper()
	workDir := t.TempDir()
	t.Chdir(workDir)
	unsetEnv(t, "DATABASE_URL")
	unsetEnv(t, "CONFIG_DIR")
	unsetEnv(t, "MANAGEMENT_BIND_ADDRESS")
	unsetEnv(t, "MANAGEMENT_TLS_CERT_FILE")
	unsetEnv(t, "MANAGEMENT_TLS_KEY_FILE")
	unsetEnv(t, "MANAGEMENT_TLS_CLIENT_CA_FILE")
	unsetEnv(t, "MANAGEMENT_TLS_MODE")
	unsetEnv(t, "MANAGEMENT_ALLOW_INSECURE_HTTP")
	unsetEnv(t, "MANAGEMENT_PUBLIC_URL")
	unsetEnv(t, "MANAGEMENT_ADVERTISE_HOST")
	unsetEnv(t, "MANAGEMENT_TLS_EXTRA_HOSTS")
	unsetEnv(t, "MANAGEMENT_SETUP_TOKEN")
	unsetEnv(t, "OBSERVABILITY_MAX_ROWS")
	unsetEnv(t, "LOGIN_THROTTLE_MAX_KEYS")
	unsetEnv(t, "SECRETS_ENCRYPTION_KEY")
	unsetEnv(t, "SECRETS_ENCRYPTION_KEY_FILE")
	unsetEnv(t, "SECRETS_ENCRYPTION_KEY_ID")
	unsetEnv(t, "SECRETS_ENCRYPTION_PREVIOUS_KEYS")
	unsetEnv(t, "SECRETS_ENCRYPTION_REQUIRED")
	return workDir
}

func unsetEnv(t *testing.T, key string) {
	t.Helper()
	oldValue, hadValue := os.LookupEnv(key)
	if err := os.Unsetenv(key); err != nil {
		t.Fatalf("failed to unset %s: %v", key, err)
	}
	t.Cleanup(func() {
		if hadValue {
			_ = os.Setenv(key, oldValue)
			return
		}
		_ = os.Unsetenv(key)
	})
}

func assertSQLiteURL(t *testing.T, got, wantDBPath string) {
	t.Helper()
	prefix, rawQuery, ok := strings.Cut(got, "?")
	if !ok {
		t.Fatalf("DatabaseURL %q has no query string", got)
	}
	wantPrefix := "file:" + filepath.ToSlash(wantDBPath)
	if prefix != wantPrefix {
		t.Fatalf("DatabaseURL prefix = %q, want %q", prefix, wantPrefix)
	}
	values, err := url.ParseQuery(rawQuery)
	if err != nil {
		t.Fatalf("DatabaseURL query %q did not parse: %v", rawQuery, err)
	}
	wantValues := map[string]string{
		"mode":          "rwc",
		"_journal_mode": "WAL",
		"_synchronous":  "NORMAL",
		"_busy_timeout": "10000",
		"_fk":           "1",
		"cache":         "private",
	}
	for key, want := range wantValues {
		if got := values.Get(key); got != want {
			t.Fatalf("DatabaseURL query %s = %q, want %q", key, got, want)
		}
	}
}

func assertMode(t *testing.T, path string, want os.FileMode) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("failed to stat %s: %v", path, err)
	}
	if got := info.Mode().Perm(); got != want {
		t.Fatalf("%s mode = %o, want %o", path, got, want)
	}
}

func assertFileContents(t *testing.T, path, want string) {
	t.Helper()
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("failed to read %s: %v", path, err)
	}
	if string(got) != want {
		t.Fatalf("%s = %q, want %q", path, string(got), want)
	}
}
