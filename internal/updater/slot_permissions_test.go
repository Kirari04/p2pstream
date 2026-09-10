package updater

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"golang.org/x/sys/unix"
)

func TestInstallSlotWithActivatorUmask(t *testing.T) {
	// Umask is process-wide. Match the root activator's UMask=0077 in a
	// subprocess so this test cannot affect other filesystem tests.
	if os.Getenv("P2PSTREAM_TEST_SLOT_UMASK") != "1" {
		cmd := exec.Command(os.Args[0], "-test.run=^TestInstallSlotWithActivatorUmask$")
		cmd.Env = append(os.Environ(), "P2PSTREAM_TEST_SLOT_UMASK=1")
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("activator umask regression: %v\n%s", err, output)
		}
		return
	}
	previousMask := unix.Umask(0077)
	defer unix.Umask(previousMask)
	for _, reuse := range []bool{false, true} {
		name := "new_slot"
		if reuse {
			name = "retry_legacy_slot"
		}
		t.Run(name, func(t *testing.T) {
			f := newFixture(t)
			stageFixture(t, f)
			slotDir := filepath.Join(f.paths.slotsDir(), f.release.Version)
			slotPath := filepath.Join(slotDir, "p2pstream")
			if reuse {
				if err := os.Mkdir(slotDir, 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(slotPath, f.body, 0700); err != nil {
					t.Fatal(err)
				}
			}
			artifact, err := os.Open(filepath.Join(f.paths.candidateDir(), "artifact.bin"))
			if err != nil {
				t.Fatal(err)
			}
			defer artifact.Close()
			if _, err := installSlot(f.paths, f.release, artifact); err != nil {
				t.Fatal(err)
			}
			// The service runs as p2pstream, not the root activator. Both the
			// directory and executable must allow that separate user access.
			for _, path := range []string{slotDir, slotPath} {
				info, err := os.Stat(path)
				if err != nil {
					t.Fatal(err)
				}
				if info.Mode().Perm() != 0755 {
					t.Errorf("%s permissions = %04o, want 0755 for the unprivileged agent service", filepath.Base(path), info.Mode().Perm())
				}
			}
			if err := verifyFile(slotPath, f.release.Artifact); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestInstallSlotRejectsUnsafeExistingSlotBeforeChangingPermissions(t *testing.T) {
	for _, kind := range []string{"corrupt", "writable_file", "writable_directory", "symlink_directory", "symlink_file", "hardlink_file"} {
		t.Run(kind, func(t *testing.T) {
			f := newFixture(t)
			stageFixture(t, f)
			slotDir := filepath.Join(f.paths.slotsDir(), f.release.Version)
			realDir := slotDir
			if kind == "symlink_directory" {
				realDir = filepath.Join(f.paths.slotsDir(), "redirected")
			}
			if err := os.Mkdir(realDir, 0700); err != nil {
				t.Fatal(err)
			}
			if realDir != slotDir {
				if err := os.Symlink(realDir, slotDir); err != nil {
					t.Fatal(err)
				}
			}
			binaryPath := filepath.Join(realDir, "p2pstream")
			data := f.body
			if kind == "corrupt" {
				data = bytes.Repeat([]byte("!"), len(f.body))
			}
			if err := os.WriteFile(binaryPath, data, 0700); err != nil {
				t.Fatal(err)
			}
			dirMode, fileMode := os.FileMode(0700), os.FileMode(0700)
			switch kind {
			case "writable_file":
				fileMode = 0777
				if err := os.Chmod(binaryPath, fileMode); err != nil {
					t.Fatal(err)
				}
			case "writable_directory":
				dirMode = 0777
				if err := os.Chmod(realDir, dirMode); err != nil {
					t.Fatal(err)
				}
			case "symlink_file", "hardlink_file":
				otherPath := filepath.Join(realDir, "other")
				if err := os.Rename(binaryPath, otherPath); err != nil {
					t.Fatal(err)
				}
				link := os.Link
				if kind == "symlink_file" {
					link = os.Symlink
				}
				if err := link(otherPath, binaryPath); err != nil {
					t.Fatal(err)
				}
			}
			artifact, err := os.Open(filepath.Join(f.paths.candidateDir(), "artifact.bin"))
			if err != nil {
				t.Fatal(err)
			}
			defer artifact.Close()
			if _, err := installSlot(f.paths, f.release, artifact); err == nil {
				t.Fatal("unsafe existing slot was accepted")
			}
			for path, mode := range map[string]os.FileMode{realDir: dirMode, binaryPath: fileMode} {
				info, err := os.Stat(path)
				if err != nil {
					t.Fatal(err)
				}
				if info.Mode().Perm() != mode {
					t.Errorf("%s permissions changed to %04o", path, info.Mode().Perm())
				}
			}
			current, err := currentTarget(f.paths)
			if err != nil || current != f.bootstrap.Target {
				t.Fatalf("live slot changed: %q, %v", current, err)
			}
		})
	}
}
