package server

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

func TestDataLeaseRejectsUnsafeLockAndPreservesInode(t *testing.T) {
	for _, name := range []string{"regular", "symlink", "hardlink", "directory", "writable", "fifo"} {
		t.Run(name, func(t *testing.T) {
			data := t.TempDir()
			path := filepath.Join(data, dataLockName)
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
			case "fifo":
				if err := syscall.Mkfifo(path, 0600); err != nil {
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
			lease, err := AcquireDataLease(context.Background(), data)
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
			lease, err = AcquireDataLease(context.Background(), data)
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
func TestDataLeaseProcessHelper(t *testing.T) {
	mode := os.Getenv("P2PSTREAM_RUNTIME_GUARD_TEST_MODE")
	if mode == "" {
		return
	}
	data := os.Getenv("P2PSTREAM_RUNTIME_GUARD_TEST_DATA")
	result := os.Getenv("P2PSTREAM_RUNTIME_GUARD_TEST_RESULT")
	ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
	defer cancel()
	lease, err := AcquireDataLease(ctx, data)
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
		time.Sleep(time.Hour) // The parent kills this simulated crashed server.
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
	child := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestDataLeaseProcessHelper$")
	child.Env = append(os.Environ(), "P2PSTREAM_RUNTIME_GUARD_TEST_MODE="+mode, "P2PSTREAM_RUNTIME_GUARD_TEST_DATA="+data, "P2PSTREAM_RUNTIME_GUARD_TEST_RESULT="+result)
	return child
}

func TestDataLeaseCompetingProcessCannotWrite(t *testing.T) {
	data := t.TempDir()
	lease, err := AcquireDataLease(context.Background(), data)
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

func TestDataLeaseReleasedAfterProcessCrash(t *testing.T) {
	data := t.TempDir()
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
	lease, err := AcquireDataLease(ctx, data)
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Close()
}
