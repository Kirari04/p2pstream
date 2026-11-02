package config

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

func TestConfigGuardRunsBeforeDirectoryChanges(t *testing.T) {
	work := isolatedConfigTestDir(t)
	data := filepath.Join(work, "not-created")
	t.Setenv("CONFIG_DIR", data)
	denied := errors.New("guard denied")
	called := false
	_, err := LoadWithGuard(func(cfg *Config) error {
		called = true
		if cfg.ConfigDir != data {
			t.Fatalf("guard sees wrong data path: %q", cfg.ConfigDir)
		}
		if _, err := os.Stat(data); !os.IsNotExist(err) {
			t.Fatalf("config wrote before guard: %v", err)
		}
		return denied
	})
	if !called || !errors.Is(err, denied) {
		t.Fatalf("guard outcome = %v, called=%v", err, called)
	}
	if _, err := os.Stat(data); !os.IsNotExist(err) {
		t.Fatalf("denied config still wrote data: %v", err)
	}
}

func TestReadOnlyConfigNeverCreatesOrChmodsData(t *testing.T) {
	work := isolatedConfigTestDir(t)
	data := filepath.Join(work, "data")
	t.Setenv("CONFIG_DIR", data)
	if _, err := LoadReadOnly(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(data); !os.IsNotExist(err) {
		t.Fatalf("read-only load created data: %v", err)
	}
	if err := os.Mkdir(data, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(data, 0755); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadReadOnly(); err != nil {
		t.Fatal(err)
	}
	assertMode(t, data, 0755)
	if _, err := os.Stat(filepath.Join(data, certsDirName)); !os.IsNotExist(err) {
		t.Fatalf("read-only load created certs: %v", err)
	}
}

func TestServerUpdateTokenFileSecureReadAndRedactedErrors(t *testing.T) {
	for _, name := range []string{"valid", "short", "large", "control-byte", "symlink", "hardlink", "writable", "directory", "fifo", "relative"} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "token")
			secret := strings.Repeat("a", 64)
			contents := secret + "\n"
			if name == "short" {
				contents = "private-short-secret"
			}
			if name == "large" {
				contents = strings.Repeat("a", 4097)
			}
			if name == "control-byte" {
				contents = secret + "\r\nsecond-secret"
			}
			if err := os.WriteFile(path, []byte(contents), 0600); err != nil {
				t.Fatal(err)
			}
			switch name {
			case "symlink", "hardlink":
				other := filepath.Join(t.TempDir(), "source")
				if err := os.Rename(path, other); err != nil {
					t.Fatal(err)
				}
				var err error
				if name == "symlink" {
					err = os.Symlink(other, path)
				} else {
					err = os.Link(other, path)
				}
				if err != nil {
					t.Fatal(err)
				}
			case "writable":
				if err := os.Chmod(path, 0666); err != nil {
					t.Fatal(err)
				}
			case "directory", "fifo":
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
				var err error
				if name == "directory" {
					err = os.Mkdir(path, 0700)
				} else {
					err = syscall.Mkfifo(path, 0600)
				}
				if err != nil {
					t.Fatal(err)
				}
			case "relative":
				path = "relative-token"
			}
			token, err := readServerUpdateTokenFile(path)
			if name == "valid" {
				if err != nil || token != secret {
					t.Fatalf("token read failed: %v", err)
				}
				return
			}
			if err == nil || token != "" {
				t.Fatal("unsafe token file accepted")
			}
			if strings.Contains(err.Error(), secret) || strings.Contains(err.Error(), "private-short-secret") || strings.Contains(err.Error(), "second-secret") {
				t.Fatal("error disclosed token contents")
			}
		})
	}
}

func TestServerUpdateTokenFileAndRawTokenAreExclusive(t *testing.T) {
	secret := strings.Repeat("private", 8)
	cfg := &Config{ServerUpdateToken: secret, ServerUpdateTokenFile: "/unused/token"}
	if err := validateServerUpdaterConfig(cfg); err == nil || strings.Contains(err.Error(), secret) {
		t.Fatalf("expected redacted conflict error: %v", err)
	}
}

func TestProtocolTwoRequiresGenerationAndFixedPrivatePaths(t *testing.T) {
	// Supplying a legacy raw token must never satisfy protocol two admission.
	cfg := &Config{ServerUpdateSocket: "/run/p2pstream-server-update/control.sock", ServerUpdateGateFile: "/run/p2pstream-server-update/maintenance", ServerUpdateToken: strings.Repeat("a", 64), ServerUpdateInstanceID: "2cdd2454-3a41-421e-ab22-a1346a32a629", ServerUpdateRuntimeProtocol: 2, ServerUpdateGeneration: "b44a12d5-9fb9-4433-b5db-b6f1503e5d27"}
	if err := validateServerUpdaterConfig(cfg); err == nil {
		t.Fatal("raw token satisfied protocol two private mount requirement")
	}
	cfg.ServerUpdateRuntimeProtocol = 0
	cfg.ServerUpdateGeneration = ""
	if err := validateServerUpdaterConfig(cfg); err != nil {
		t.Fatalf("legacy enrollment rejected: %v", err)
	}
}
