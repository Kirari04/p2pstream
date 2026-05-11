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

	t.Run("client CA requires management TLS", func(t *testing.T) {
		workDir := isolatedConfigTestDir(t)
		t.Setenv("CONFIG_DIR", filepath.Join(workDir, "data"))
		t.Setenv("MANAGEMENT_TLS_CLIENT_CA_FILE", filepath.Join(workDir, "agents-ca.pem"))

		if _, err := Load(); err == nil {
			t.Fatal("expected management client CA without cert/key to fail")
		}
	})
}

func isolatedConfigTestDir(t *testing.T) string {
	t.Helper()
	workDir := t.TempDir()
	t.Chdir(workDir)
	unsetEnv(t, "DATABASE_URL")
	unsetEnv(t, "CONFIG_DIR")
	unsetEnv(t, "TARGET_ORIGIN")
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
