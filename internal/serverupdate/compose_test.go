package serverupdate

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestPrepareRejectsBackupProblemsBeforeContactingRuntime(t *testing.T) {
	for _, name := range []string{"valid", "missing-source", "missing-state", "regular-source", "regular-state", "symlink-child", "canceled"} {
		t.Run(name, func(t *testing.T) {
			source, state := t.TempDir(), t.TempDir()
			ctx := context.Background()
			switch name {
			case "missing-source", "regular-source":
				if err := os.Remove(source); err != nil {
					t.Fatal(err)
				}
				if name == "regular-source" {
					if err := os.WriteFile(source, nil, 0600); err != nil {
						t.Fatal(err)
					}
				}
			case "missing-state", "regular-state":
				if err := os.Remove(state); err != nil {
					t.Fatal(err)
				}
				if name == "regular-state" {
					if err := os.WriteFile(state, nil, 0600); err != nil {
						t.Fatal(err)
					}
				}
			case "symlink-child":
				if err := os.Symlink(t.TempDir(), filepath.Join(source, "unsupported")); err != nil {
					t.Fatal(err)
				}
			case "canceled":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			}
			docker := filepath.Join(t.TempDir(), "docker")
			if err := os.WriteFile(docker, []byte("#!/bin/sh\ntouch \"$0.called\"\nexit 1\n"), 0700); err != nil {
				t.Fatal(err)
			}
			driver := &ComposeDriver{Config: ComposeConfig{DataDir: source, StateDir: state, Docker: docker}}
			_, err := driver.Prepare(ctx)
			if err == nil {
				t.Fatal("expected preflight or fixture runtime error")
			}
			_, markerErr := os.Stat(docker + ".called")
			if name == "valid" {
				if markerErr != nil {
					t.Fatalf("valid preflight did not reach runtime: %v (%v)", markerErr, err)
				}
			} else if !os.IsNotExist(markerErr) {
				t.Fatalf("runtime contacted after failed preflight: %v", markerErr)
			}
			if name == "canceled" && !errors.Is(err, context.Canceled) {
				t.Fatalf("canceled preparation = %v", err)
			}
		})
	}
}

func TestCleanupBackupsPreservesActiveSnapshotAndHonorsCancellation(t *testing.T) {
	state := t.TempDir()
	var names []string
	for i := range 5 {
		name := fmt.Sprintf("backup-00000000-0000-4000-8000-%012d.tar", i)
		names = append(names, name)
		path := filepath.Join(state, name)
		if err := os.WriteFile(path, nil, 0600); err != nil {
			t.Fatal(err)
		}
		stamp := time.Unix(int64(i), 0)
		if err := os.Chtimes(path, stamp, stamp); err != nil {
			t.Fatal(err)
		}
	}
	driver := &ComposeDriver{Config: ComposeConfig{StateDir: state}}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := driver.CleanupBackups(ctx, "00000000-0000-4000-8000-000000000000"); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled cleanup = %v", err)
	}
	for _, name := range names {
		if _, err := os.Stat(filepath.Join(state, name)); err != nil {
			t.Fatalf("canceled cleanup removed %s: %v", name, err)
		}
	}
	if err := driver.CleanupBackups(context.Background(), "00000000-0000-4000-8000-000000000000"); err != nil {
		t.Fatal(err)
	}
	for i, name := range names {
		_, err := os.Stat(filepath.Join(state, name))
		if i == 1 {
			if !os.IsNotExist(err) {
				t.Fatalf("old snapshot was not pruned: %v", err)
			}
		} else if err != nil {
			t.Fatalf("retained snapshot %s missing: %v", name, err)
		}
	}
}

func TestRuntimeCannotForgeInstalledIdentityToAuthorizeDowngrade(t *testing.T) {
	driver := &ComposeDriver{Config: ComposeConfig{Channel: "stable", Repository: "test/repo"}}
	labels := map[string]string{"org.opencontainers.image.version": "v1.2.0", "org.opencontainers.image.revision": strings.Repeat("a", 40), "org.opencontainers.image.source": "https://github.com/test/repo"}
	status := RuntimeStatus{Version: "v1.2.0", Commit: labels["org.opencontainers.image.revision"]}
	if err := driver.validateRuntimeIdentity(status, labels); err != nil {
		t.Fatal(err)
	}
	status.Version = "v1.0.0"
	if err := driver.validateRuntimeIdentity(status, labels); err == nil {
		t.Fatal("runtime could lie about its version to select an older image")
	}
	status.Version = "v1.2.0"
	status.Commit = strings.Repeat("b", 40)
	if err := driver.validateRuntimeIdentity(status, labels); err == nil {
		t.Fatal("runtime could lie about installed commit")
	}
	status.Commit = labels["org.opencontainers.image.revision"]
	labels["org.opencontainers.image.source"] = "https://github.com/attacker/repo"
	if err := driver.validateRuntimeIdentity(status, labels); err == nil {
		t.Fatal("image from an unrelated repository accepted")
	}
}
