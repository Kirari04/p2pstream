package cmd

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"p2pstream/internal/server"
)

func setupServerGuardConfig(t *testing.T) string {
	t.Helper()
	t.Chdir(t.TempDir())
	data := filepath.Join(t.TempDir(), "data")
	if err := os.Mkdir(data, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(data, 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CONFIG_DIR", data)
	t.Setenv("DATABASE_URL", "")
	for _, key := range []string{"SERVER_UPDATE_SOCKET", "SERVER_UPDATE_TOKEN", "SERVER_UPDATE_INSTANCE_ID", "SERVER_UPDATE_GATE_FILE", "SERVER_UPDATE_TOKEN_FILE", "SERVER_UPDATE_RUNTIME_PROTOCOL", "SERVER_UPDATE_GENERATION"} {
		t.Setenv(key, "")
	}
	return data
}

func TestServerConfigWaitsForDataLeaseBeforeAnyConfigWrites(t *testing.T) {
	data := setupServerGuardConfig(t)
	lease, err := server.AcquireDataLease(context.Background(), data)
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	_, contender, err := loadServerConfig(ctx)
	if !errors.Is(err, context.DeadlineExceeded) || contender != nil {
		t.Fatalf("competing startup = %v, lease=%v", err, contender)
	}
	assertServerGuardNoConfigWrites(t, data)
}

func TestStandaloneServerConfigIgnoresObsoleteUpdateState(t *testing.T) {
	data := setupServerGuardConfig(t)
	for _, name := range []string{".p2pstream-runtime-owner.json", "server-installation-id", "maintenance"} {
		if err := os.WriteFile(filepath.Join(data, name), []byte("obsolete updater state"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("SERVER_UPDATE_RUNTIME_PROTOCOL", "obsolete")
	t.Setenv("SERVER_UPDATE_TOKEN_FILE", "/missing/token")
	t.Setenv("SERVER_UPDATE_GATE_FILE", filepath.Join(data, "maintenance"))
	_, lease, err := loadServerConfig(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Close()
}

func TestServerConfigFailureReleasesDataLease(t *testing.T) {
	data := setupServerGuardConfig(t)
	if err := os.WriteFile("p2pstream.db", nil, 0600); err != nil {
		t.Fatal(err)
	}
	_, lease, err := loadServerConfig(context.Background())
	if err == nil || lease != nil {
		t.Fatalf("legacy database config = %v, lease=%v", err, lease)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	lease, err = server.AcquireDataLease(ctx, data)
	if err != nil {
		t.Fatalf("failed startup leaked data lease: %v", err)
	}
	defer lease.Close()
}

func TestStandaloneServerConfigHoldsLifetimeDataLease(t *testing.T) {
	data := setupServerGuardConfig(t)
	cfg, lease, err := loadServerConfig(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Close()
	if cfg.ConfigDir != data {
		t.Fatalf("wrong config data path %q", cfg.ConfigDir)
	}
	if _, err := os.Stat(filepath.Join(data, "certs")); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	if other, err := server.AcquireDataLease(ctx, data); !errors.Is(err, context.DeadlineExceeded) {
		_ = other.Close()
		t.Fatalf("standalone server did not retain lease: %v", err)
	}
}

func assertServerGuardNoConfigWrites(t *testing.T, data string) {
	t.Helper()
	info, err := os.Stat(data)
	if err != nil || info.Mode().Perm() != 0755 {
		t.Fatalf("rejected startup changed data permissions: %v", err)
	}
	for _, name := range []string{"certs", "p2pstream.db", "server-installation-id"} {
		if _, err := os.Stat(filepath.Join(data, name)); !os.IsNotExist(err) {
			t.Fatalf("rejected startup created %s: %v", name, err)
		}
	}
}
