package config

import (
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/caarlos0/env/v10"
	"github.com/joho/godotenv"
)

const (
	DefaultConfigDir = "p2pstream-data"

	databaseFileName = "p2pstream.db"
	certsDirName     = "certs"
)

type Config struct {
	Port                       string `env:"PORT" envDefault:"80"`
	ManagementPort             string `env:"MANAGEMENT_PORT" envDefault:"8081"`
	TargetOrigin               string `env:"TARGET_ORIGIN" envDefault:"https://httpbin.org"`
	ConfigDir                  string `env:"CONFIG_DIR" envDefault:"p2pstream-data"`
	DatabaseURL                string `env:"DATABASE_URL"`
	Env                        string `env:"ENV" envDefault:"development"` // development or production
	ManagementUIDevProxy       string `env:"MANAGEMENT_UI_DEV_PROXY"`
	ManagementUIDistDir        string `env:"MANAGEMENT_UI_DIST_DIR" envDefault:"web/management/dist"`
	ManagementCookieSecure     bool   `env:"MANAGEMENT_COOKIE_SECURE" envDefault:"false"`
	AgentToken                 string `env:"AGENT_TOKEN"`
	ObservabilityRetentionDays int    `env:"OBSERVABILITY_RETENTION_DAYS" envDefault:"30"`

	ParsedTargetOrigin *url.URL `env:"-"`
	CertsDir           string   `env:"-"`
}

// Load reads .env files and environment variables into the Config struct.
func Load() (*Config, error) {
	// Attempt to load .env file; it's okay if it doesn't exist.
	_ = godotenv.Load()

	_, explicitDatabaseURL := os.LookupEnv("DATABASE_URL")

	cfg := &Config{}
	if err := env.Parse(cfg); err != nil {
		return nil, fmt.Errorf("failed to parse environment variables: %w", err)
	}

	if strings.TrimSpace(cfg.ConfigDir) == "" {
		cfg.ConfigDir = DefaultConfigDir
	}
	cfg.ConfigDir = filepath.Clean(cfg.ConfigDir)
	cfg.CertsDir = filepath.Join(cfg.ConfigDir, certsDirName)

	if err := prepareConfigDir(cfg.ConfigDir, cfg.CertsDir); err != nil {
		return nil, err
	}

	if !explicitDatabaseURL || strings.TrimSpace(cfg.DatabaseURL) == "" {
		dbPath := filepath.Join(cfg.ConfigDir, databaseFileName)
		if err := migrateLegacyDefaultDatabase(dbPath); err != nil {
			return nil, err
		}
		cfg.DatabaseURL = defaultDatabaseURL(dbPath)
	}

	parsed, err := url.Parse(cfg.TargetOrigin)
	if err != nil {
		return nil, fmt.Errorf("invalid TARGET_ORIGIN %q: %w", cfg.TargetOrigin, err)
	}
	cfg.ParsedTargetOrigin = parsed

	return cfg, nil
}

func prepareConfigDir(configDir, certsDir string) error {
	if err := os.MkdirAll(configDir, 0700); err != nil {
		return fmt.Errorf("failed to create CONFIG_DIR %q: %w", configDir, err)
	}
	if err := os.Chmod(configDir, 0700); err != nil {
		return fmt.Errorf("failed to set permissions on CONFIG_DIR %q: %w", configDir, err)
	}
	if err := os.MkdirAll(certsDir, 0700); err != nil {
		return fmt.Errorf("failed to create certs directory %q: %w", certsDir, err)
	}
	if err := os.Chmod(certsDir, 0700); err != nil {
		return fmt.Errorf("failed to set permissions on certs directory %q: %w", certsDir, err)
	}
	return nil
}

func (c *Config) PublicTLSCertificatePaths(listenerID, mappingID int64) (certPath, keyPath string) {
	certsDir := c.CertsDir
	if strings.TrimSpace(certsDir) == "" {
		configDir := c.ConfigDir
		if strings.TrimSpace(configDir) == "" {
			configDir = DefaultConfigDir
		}
		certsDir = filepath.Join(filepath.Clean(configDir), certsDirName)
	}

	dir := filepath.Join(certsDir, fmt.Sprintf("public-listener-%d", listenerID))
	return filepath.Join(dir, fmt.Sprintf("tls-%d.crt.pem", mappingID)),
		filepath.Join(dir, fmt.Sprintf("tls-%d.key.pem", mappingID))
}

func (c *Config) WritePublicTLSCertificateFiles(listenerID, mappingID int64, certPEM, keyPEM []byte) (certPath, keyPath string, err error) {
	certPath, keyPath = c.PublicTLSCertificatePaths(listenerID, mappingID)
	dir := filepath.Dir(certPath)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return "", "", fmt.Errorf("failed to create TLS certificate directory %q: %w", dir, err)
	}
	if err := os.Chmod(dir, 0700); err != nil {
		return "", "", fmt.Errorf("failed to set permissions on TLS certificate directory %q: %w", dir, err)
	}
	if err := os.WriteFile(certPath, certPEM, 0600); err != nil {
		return "", "", fmt.Errorf("failed to write TLS certificate %q: %w", certPath, err)
	}
	if err := os.Chmod(certPath, 0600); err != nil {
		return "", "", fmt.Errorf("failed to set permissions on TLS certificate %q: %w", certPath, err)
	}
	if err := os.WriteFile(keyPath, keyPEM, 0600); err != nil {
		return "", "", fmt.Errorf("failed to write TLS private key %q: %w", keyPath, err)
	}
	if err := os.Chmod(keyPath, 0600); err != nil {
		return "", "", fmt.Errorf("failed to set permissions on TLS private key %q: %w", keyPath, err)
	}
	return certPath, keyPath, nil
}

func defaultDatabaseURL(dbPath string) string {
	values := url.Values{}
	values.Set("mode", "rwc")
	values.Set("_journal_mode", "WAL")
	values.Set("_synchronous", "NORMAL")
	values.Set("_busy_timeout", "10000")
	values.Set("_fk", "1")
	values.Set("cache", "private")
	return "file:" + filepath.ToSlash(dbPath) + "?" + values.Encode()
}

func migrateLegacyDefaultDatabase(newDBPath string) error {
	if _, err := os.Stat(newDBPath); err == nil {
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("failed to inspect configured database %q: %w", newDBPath, err)
	}

	legacyDBPath := databaseFileName
	if samePath(legacyDBPath, newDBPath) {
		return nil
	}
	if _, err := os.Stat(legacyDBPath); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("failed to inspect legacy database %q: %w", legacyDBPath, err)
	}

	for _, suffix := range []string{"", "-wal", "-shm"} {
		src := legacyDBPath + suffix
		dst := newDBPath + suffix
		if err := copyFileIfExists(src, dst); err != nil {
			return fmt.Errorf("failed to migrate legacy database file %q to %q: %w", src, dst, err)
		}
	}
	return nil
}

func samePath(a, b string) bool {
	absA, errA := filepath.Abs(a)
	absB, errB := filepath.Abs(b)
	if errA != nil || errB != nil {
		return filepath.Clean(a) == filepath.Clean(b)
	}
	return absA == absB
}

func copyFileIfExists(src, dst string) error {
	info, err := os.Stat(src)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	if info.IsDir() {
		return fmt.Errorf("source is a directory")
	}

	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	mode := info.Mode().Perm()
	if mode == 0 {
		mode = 0600
	}
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(out, in)
	closeErr := out.Close()
	if copyErr != nil {
		_ = os.Remove(dst)
		return copyErr
	}
	if closeErr != nil {
		_ = os.Remove(dst)
		return closeErr
	}
	return nil
}
