package serverupdate

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func runtimeTestOwner() RuntimeOwner {
	return RuntimeOwner{API: RuntimeProtocol, InstanceID: "2cdd2454-3a41-421e-ab22-a1346a32a629", Generation: "b44a12d5-9fb9-4433-b5db-b6f1503e5d27", Allowed: true, Version: "v1.2.3", Commit: strings.Repeat("a", 40)}
}

func runtimeTestAdmission(owner RuntimeOwner) RuntimeAdmission {
	return RuntimeAdmission{Protocol: RuntimeProtocol, InstanceID: owner.InstanceID, Generation: owner.Generation, Version: owner.Version, Commit: owner.Commit, Socket: RuntimeControlSocket, GateFile: RuntimeGateFile, TokenFile: RuntimeTokenFile}
}

func TestRuntimeAdmissionRejectsBlockedOrStaleStarts(t *testing.T) {
	for _, name := range []string{"valid", "blocked", "instance", "generation", "version", "commit", "socket", "gate", "token-file", "legacy", "missing-owner", "standalone", "old-enrollment"} {
		t.Run(name, func(t *testing.T) {
			value := runtimeTestOwner()
			owner := &value
			admission := runtimeTestAdmission(value)
			switch name {
			case "blocked":
				owner.Allowed = false
			case "instance":
				admission.InstanceID = "a2c32454-3a41-421e-ab22-a1346a32a629"
			case "generation":
				admission.Generation = "c44a12d5-9fb9-4433-b5db-b6f1503e5d27"
			case "version":
				admission.Version = "v1.2.2"
			case "commit":
				admission.Commit = strings.Repeat("b", 40)
			case "socket":
				admission.Socket = "/stale/control.sock"
			case "gate":
				admission.GateFile = "/stale/maintenance"
			case "token-file":
				admission.TokenFile = ""
			case "legacy":
				admission.Protocol = 0
			case "missing-owner":
				owner = nil
			case "standalone":
				owner = nil
				admission = RuntimeAdmission{}
			case "old-enrollment":
				owner = nil
				admission.Protocol = 0
				admission.Generation = ""
			}
			err := ValidateRuntimeAdmission(owner, admission)
			allowed := name == "valid" || name == "standalone" || name == "old-enrollment"
			if (err == nil) != allowed {
				t.Fatalf("admission error = %v, allowed = %v", err, allowed)
			}
		})
	}
}

func TestRuntimeLeaseRejectsUnsafeLockAndPreservesInode(t *testing.T) {
	for _, name := range []string{"regular", "symlink", "hardlink", "directory", "writable"} {
		t.Run(name, func(t *testing.T) {
			data := t.TempDir()
			path := filepath.Join(data, RuntimeLockName)
			switch name {
			case "symlink", "hardlink":
				other := filepath.Join(t.TempDir(), "other")
				if err := os.WriteFile(other, []byte("untouched"), 0600); err != nil {
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
			case "directory":
				if err := os.Mkdir(path, 0700); err != nil {
					t.Fatal(err)
				}
			case "writable":
				if err := os.WriteFile(path, nil, 0600); err != nil {
					t.Fatal(err)
				}
				if err := os.Chmod(path, 0666); err != nil {
					t.Fatal(err)
				}
			}
			lease, err := AcquireRuntimeLease(context.Background(), data)
			if name != "regular" {
				if err == nil {
					_ = lease.Close()
					t.Fatal("unsafe lock accepted")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			before, err := os.Lstat(path)
			if err != nil {
				t.Fatal(err)
			}
			if err := lease.Close(); err != nil {
				t.Fatal(err)
			}
			lease, err = AcquireRuntimeLease(context.Background(), data)
			if err != nil {
				t.Fatal(err)
			}
			defer lease.Close()
			after, err := os.Lstat(path)
			if err != nil || !os.SameFile(before, after) {
				t.Fatalf("lock inode changed: %v", err)
			}
		})
	}
}

// The child uses a distinct process so flock exclusion is checked across the
// same boundary as two independently launched server containers/processes.
func TestRuntimeLeaseProcessHelper(t *testing.T) {
	mode := os.Getenv("P2PSTREAM_RUNTIME_GUARD_TEST_MODE")
	if mode == "" {
		return
	}
	data := os.Getenv("P2PSTREAM_RUNTIME_GUARD_TEST_DATA")
	result := os.Getenv("P2PSTREAM_RUNTIME_GUARD_TEST_RESULT")
	ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
	defer cancel()
	lease, err := AcquireRuntimeLease(ctx, data)
	if err != nil {
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatal(err)
		}
		if err := os.WriteFile(result, []byte("blocked"), 0600); err != nil {
			t.Fatal(err)
		}
		return
	}
	defer lease.Close()
	if mode == "hold" {
		if err := os.WriteFile(result, []byte("holding"), 0600); err != nil {
			t.Fatal(err)
		}
		time.Sleep(time.Hour) // The parent kills this simulated crashed updater.
		return
	}
	if err := os.WriteFile(filepath.Join(data, "writer-side-effect"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(result, []byte("wrote"), 0600); err != nil {
		t.Fatal(err)
	}
}

func runtimeGuardChild(t *testing.T, mode, data, result string) *exec.Cmd {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)
	child := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestRuntimeLeaseProcessHelper$")
	child.Env = append(os.Environ(), "P2PSTREAM_RUNTIME_GUARD_TEST_MODE="+mode, "P2PSTREAM_RUNTIME_GUARD_TEST_DATA="+data, "P2PSTREAM_RUNTIME_GUARD_TEST_RESULT="+result)
	return child
}

func TestRuntimeLeaseCompetingProcessCannotWrite(t *testing.T) {
	data := t.TempDir()
	lease, err := AcquireRuntimeLease(context.Background(), data)
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Close()
	result := filepath.Join(t.TempDir(), "result")
	if output, err := runtimeGuardChild(t, "write", data, result).CombinedOutput(); err != nil {
		t.Fatalf("child: %v: %s", err, output)
	}
	got, err := os.ReadFile(result)
	if err != nil || string(got) != "blocked" {
		t.Fatalf("competing child result = %q, %v", got, err)
	}
	if _, err := os.Stat(filepath.Join(data, "writer-side-effect")); !os.IsNotExist(err) {
		t.Fatalf("competing writer changed data: %v", err)
	}
}

func TestRuntimeBlockedOwnerSurvivesLeaseHolderCrash(t *testing.T) {
	data := t.TempDir()
	owner := runtimeTestOwner()
	owner.Allowed = false
	if err := WriteRuntimeOwner(data, owner); err != nil {
		t.Fatal(err)
	}
	result := filepath.Join(t.TempDir(), "result")
	child := runtimeGuardChild(t, "hold", data, result)
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if child.ProcessState == nil {
			_ = child.Process.Kill()
			_ = child.Wait()
		}
	})
	deadline := time.Now().Add(3 * time.Second)
	for {
		if got, err := os.ReadFile(result); err == nil && string(got) == "holding" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("lease holder did not become ready")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err := child.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = child.Wait()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	lease, err := AcquireRuntimeLease(ctx, data)
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Close()
	persisted, err := ReadRuntimeOwner(data)
	if err != nil || persisted == nil || persisted.Allowed {
		t.Fatalf("blocked owner lost after crash: %+v, %v", persisted, err)
	}
	if err := ValidateRuntimeAdmission(persisted, runtimeTestAdmission(owner)); err == nil {
		t.Fatal("crash permitted a blocked server start")
	}
}

func TestRuntimeOwnerRejectsMalformedOrLinkedMarkers(t *testing.T) {
	for _, name := range []string{"missing", "valid", "unknown-api", "malformed", "symlink", "hardlink", "writable"} {
		t.Run(name, func(t *testing.T) {
			data := t.TempDir()
			path := filepath.Join(data, RuntimeOwnerName)
			if name != "missing" {
				owner := runtimeTestOwner()
				if err := WriteRuntimeOwner(data, owner); err != nil {
					t.Fatal(err)
				}
			}
			switch name {
			case "unknown-api", "malformed":
				contents := []byte(`{"api":99}`)
				if name == "malformed" {
					contents = []byte(`{`)
				}
				if err := os.WriteFile(path, contents, 0600); err != nil {
					t.Fatal(err)
				}
			case "symlink", "hardlink":
				other := filepath.Join(t.TempDir(), "owner")
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
			}
			owner, err := ReadRuntimeOwner(data)
			if name == "missing" {
				if err != nil || owner != nil {
					t.Fatalf("absent owner = %+v %v", owner, err)
				}
				return
			}
			if name == "valid" {
				if err != nil || owner == nil {
					t.Fatalf("valid owner = %+v %v", owner, err)
				}
				return
			}
			if err == nil {
				t.Fatal("unsafe owner marker accepted")
			}
		})
	}
}
