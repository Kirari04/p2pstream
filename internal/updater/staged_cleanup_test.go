package updater

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestCompletedActivationCleanupPreservesUnarmedStaging(t *testing.T) {
	for _, kind := range []string{"different target", "same target new generation", "worker publishing", "missing worker lock"} {
		t.Run(kind, func(t *testing.T) {
			f := newFixture(t)
			stageAndRequestActivation(t, f)
			ready, err := os.ReadFile(f.paths.readyPath())
			if err != nil {
				t.Fatal(err)
			}
			staged, err := os.ReadFile(f.paths.stagedPath())
			if err != nil {
				t.Fatal(err)
			}
			if _, err := Activate(context.Background(), ActivateOptions{Paths: f.paths, Verifier: f.verifier, Service: &fakeService{}, DiskPreflight: allowDisk}); err != nil {
				t.Fatal(err)
			}
			// A later Stage owns the shared cache before management authorizes its
			// ready edge. It can legitimately contain the same target metadata.
			if kind == "different target" {
				var newer stagedRecord
				if err := strictJSON(staged, &newer); err != nil {
					t.Fatal(err)
				}
				newer.Version = "v1.2.0"
				if err := atomicJSON(f.paths.stagedPath(), newer, 0600); err != nil {
					t.Fatal(err)
				}
				staged, err = os.ReadFile(f.paths.stagedPath())
				if err != nil {
					t.Fatal(err)
				}
			} else if err := atomicWrite(f.paths.stagedPath(), staged, 0600); err != nil {
				t.Fatal(err)
			}
			candidate := filepath.Join(f.paths.candidateDir(), "manifest.json")
			if err := atomicWrite(candidate, []byte("new campaign candidate"), 0600); err != nil {
				t.Fatal(err)
			}
			if err := atomicWrite(f.paths.activationClaimPath(), ready, 0600); err != nil {
				t.Fatal(err)
			}
			lockPath := filepath.Join(f.paths.workerStateDir(), "worker.lock")
			if kind == "worker publishing" {
				lock, err := acquireLock(lockPath)
				if err != nil {
					t.Fatal(err)
				}
				defer lock.Close()
			} else if kind == "missing worker lock" {
				if err := os.Remove(lockPath); err != nil {
					t.Fatal(err)
				}
			}
			service := &fakeService{}
			if _, err := Activate(context.Background(), ActivateOptions{Paths: f.paths, ReadyPath: f.paths.activationClaimPath(), Verifier: f.verifier, Service: service, DiskPreflight: allowDisk}); err != nil {
				t.Fatal(err)
			}
			if service.restarts != 0 {
				t.Fatal("completed command replay restarted service")
			}
			if _, err := os.Stat(f.paths.activationClaimPath()); !os.IsNotExist(err) {
				t.Fatalf("old root claim was not consumed: %v", err)
			}
			got, err := os.ReadFile(f.paths.stagedPath())
			if err != nil || string(got) != string(staged) {
				t.Fatalf("old completed command destroyed newer unarmed staged record: %v", err)
			}
			got, err = os.ReadFile(candidate)
			if err != nil || string(got) != "new campaign candidate" {
				t.Fatalf("old completed command destroyed newer candidate: %v", err)
			}
			if kind == "missing worker lock" {
				if _, err := os.Lstat(lockPath); !os.IsNotExist(err) {
					t.Fatalf("root created a replacement worker lock: %v", err)
				}
			}
		})
	}
}
