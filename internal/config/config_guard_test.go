package config

import (
	"errors"
	"os"
	"path/filepath"
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
