package serverupdate

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// reviewStoppedDriver models a host reboot: any candidate started by Deploy
// still has restart=no until Commit, so it will not be running after reboot.
// ComposeDriver.Commit changes only restart policy and never starts it.
type reviewStoppedDriver struct {
	*fakeDriver
	running bool
}

func (d *reviewStoppedDriver) Commit(ctx context.Context) error {
	return d.fakeDriver.Commit(ctx)
}
func (d *reviewStoppedDriver) Healthy(ctx context.Context, r Release, baseline RuntimeStatus) error {
	if !d.running {
		return errors.New("candidate was stopped by host reboot")
	}
	return d.fakeDriver.Healthy(ctx, r, baseline)
}
func (d *reviewStoppedDriver) Deploy(ctx context.Context, image string) error {
	d.running = true
	return d.fakeDriver.Deploy(ctx, image)
}

func TestReviewCommittingHostRebootMustNotReportStoppedCandidateSuccessful(t *testing.T) {
	e, original, source, request := fixtureEngine(t)
	driver := &reviewStoppedDriver{fakeDriver: original, running: true}
	e.driver = driver
	if _, err := e.Start(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	driver.crash = "commit"
	func() { defer func() { _ = recover() }(); _ = e.execute(context.Background()) }()
	op, _ := e.Operation(request.OperationID)
	if op.Phase != "committing" {
		t.Fatalf("fixture phase = %q", op.Phase)
	}
	driver.crash = ""
	driver.running = false
	driver.calls = nil
	fresh, err := NewEngine(filepath.Dir(e.path), request.Plan.InstanceID, "stable", driver, source, Floor{})
	if err != nil {
		t.Fatal(err)
	}
	if err := fresh.Recover(context.Background(), false); err != nil {
		t.Fatal(err)
	}
	result, _ := fresh.Operation(request.OperationID)
	if result.Phase == "succeeded" && !driver.running {
		t.Fatalf("stopped candidate reported succeeded; gate=%v; recovery calls=%v", driver.gated, driver.calls)
	}
}

func TestReviewPartiallyWrittenGateMustNotRemainAfterFailedPreparation(t *testing.T) {
	e, driver, _, request := fixtureEngine(t)
	if _, err := e.Start(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	// AtomicWrite can install the gate by rename then return a directory-fsync
	// error. fakeDriver similarly applies Gate(true) before returning failure.
	driver.fail = "gate"
	if err := e.execute(context.Background()); err != nil {
		if err := e.requireRecovery(err); err != nil {
			t.Fatal(err)
		}
	}
	result, _ := e.Operation(request.OperationID)
	if result.Terminal() && driver.gated {
		t.Fatalf("terminal operation left admission frozen: phase=%q calls=%v", result.Phase, driver.calls)
	}
}

func TestReviewCommittingRecoveryRollsBackWhenCandidateNoLongerHealthy(t *testing.T) {
	e, driver, source, request := fixtureEngine(t)
	if _, err := e.Start(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	driver.crash = "commit"
	func() { defer func() { _ = recover() }(); _ = e.execute(context.Background()) }()
	driver.crash = ""
	driver.fail = "candidate-health"
	fresh, err := NewEngine(filepath.Dir(e.path), request.Plan.InstanceID, "stable", driver, source, Floor{})
	if err != nil {
		t.Fatal(err)
	}
	if err := fresh.Recover(context.Background(), false); err != nil {
		t.Fatal(err)
	}
	op, _ := fresh.Operation(request.OperationID)
	if op.Phase != "rolled_back" || !driver.restored || driver.image != request.Plan.CurrentImage || driver.gated || fresh.state.Floor.Sequence != 0 {
		t.Fatalf("unhealthy committing recovery = %+v, driver=%+v", op, driver)
	}
}

type reviewPartialGateDriver struct {
	*fakeDriver
	failClear bool
}

func (d *reviewPartialGateDriver) Gate(enabled bool) error {
	d.gated = enabled
	d.calls = append(d.calls, "gate")
	if enabled || d.failClear {
		return errors.New("directory fsync failed after maintenance rename/removal")
	}
	return nil
}

func TestReviewPartialGateErrorClearsBeforeOrdinaryFailure(t *testing.T) {
	for _, failClear := range []bool{false, true} {
		t.Run(map[bool]string{false: "cleanup-succeeds", true: "cleanup-ambiguous"}[failClear], func(t *testing.T) {
			e, original, _, request := fixtureEngine(t)
			driver := &reviewPartialGateDriver{fakeDriver: original, failClear: failClear}
			e.driver = driver
			if _, err := e.Start(context.Background(), request); err != nil {
				t.Fatal(err)
			}
			err := e.execute(context.Background())
			if failClear {
				if err == nil {
					t.Fatal("ambiguous cleanup reported as an ordinary completed failure")
				}
				if err := e.requireRecovery(err); err != nil {
					t.Fatal(err)
				}
			} else if err != nil {
				t.Fatal(err)
			}
			op, _ := e.Operation(request.OperationID)
			wantPhase := "failed"
			if failClear {
				wantPhase = "recovery_required"
			}
			if op.Phase != wantPhase || driver.gated {
				t.Fatalf("phase=%s gated=%v", op.Phase, driver.gated)
			}
		})
	}
}

func TestReviewConcurrentStartRetriesHaveOneDurableAcceptance(t *testing.T) {
	e, driver, _, request := fixtureEngine(t)
	const clients = 24
	results := make(chan error, clients)
	for range clients {
		go func() {
			op, err := e.Start(context.Background(), request)
			if err == nil && op.ID != request.OperationID {
				err = errors.New("retry returned another operation")
			}
			results <- err
		}()
	}
	for range clients {
		if err := <-results; err != nil {
			t.Fatal(err)
		}
	}
	if len(driver.calls) != 0 || len(e.wake) != 1 {
		t.Fatalf("acceptance escaped worker boundary: calls=%v wakes=%d", driver.calls, len(e.wake))
	}
	if err := e.execute(context.Background()); err != nil {
		t.Fatal(err)
	}
	backups := 0
	for _, action := range driver.calls {
		if action == "backup" {
			backups++
		}
	}
	if backups != 1 {
		t.Fatalf("ran %d replacements for concurrent retries", backups)
	}
}

func TestReviewEveryAcceptedSnapshotMustBeRestorable(t *testing.T) {
	data := t.TempDir()
	path := filepath.Join(data, "authority\\backup")
	if err := os.WriteFile(path, []byte("last-good"), 0600); err != nil {
		t.Fatal(err)
	}
	backup := filepath.Join(t.TempDir(), "snapshot.tar")
	digest, err := backupData(context.Background(), data, backup)
	if err != nil {
		return
	} // Unsupported filenames may be rejected pre-activation.
	if err := os.WriteFile(path, []byte("candidate"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := restoreData(context.Background(), data, backup, digest); err != nil {
		t.Fatalf("accepted snapshot cannot restore after deleting candidate data: %v", err)
	}
	restored, err := os.ReadFile(path)
	if err != nil || string(restored) != "last-good" {
		t.Fatalf("restore=%q %v", restored, err)
	}
}

func TestReviewInterruptedRestoreCanRepeatFromProtectedSnapshot(t *testing.T) {
	data := t.TempDir()
	path := filepath.Join(data, "authority.key")
	if err := os.WriteFile(path, []byte("last-good"), 0600); err != nil {
		t.Fatal(err)
	}
	archive := filepath.Join(t.TempDir(), "snapshot.tar")
	digest, err := backupData(context.Background(), data, archive)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("candidate"), 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := restoreData(ctx, data, archive, digest); !errors.Is(err, context.Canceled) {
		t.Fatalf("interrupted restore = %v", err)
	}
	if err := restoreData(context.Background(), data, archive, digest); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	if err != nil || string(got) != "last-good" {
		t.Fatalf("last-good restore = %q %v", got, err)
	}
}

func TestReviewMalformedSnapshotDoesNotDeleteCandidateData(t *testing.T) {
	for _, name := range []string{"backslash", "duplicate", "missing-parent", "symlink", "missing-root", "truncated"} {
		t.Run(name, func(t *testing.T) {
			data := t.TempDir()
			keep := filepath.Join(data, "keep")
			if err := os.WriteFile(keep, []byte("candidate-data"), 0600); err != nil {
				t.Fatal(err)
			}
			var buffer bytes.Buffer
			writer := tar.NewWriter(&buffer)
			root := &tar.Header{Name: ".", Typeflag: tar.TypeDir, Mode: 0700}
			if name != "missing-root" {
				if err := writer.WriteHeader(root); err != nil {
					t.Fatal(err)
				}
			}
			entry := &tar.Header{Name: "value", Typeflag: tar.TypeReg, Mode: 0600}
			switch name {
			case "backslash":
				entry.Name = "authority\\backup"
			case "duplicate":
				entry = root
			case "missing-parent":
				entry.Name = "missing/value"
			case "symlink":
				entry.Typeflag = tar.TypeSymlink
				entry.Linkname = "/outside"
			case "truncated":
				entry.Size = 64
			}
			if err := writer.WriteHeader(entry); err != nil {
				t.Fatal(err)
			}
			if name == "truncated" {
				_, _ = writer.Write([]byte("short"))
			} else if err := writer.Close(); err != nil {
				t.Fatal(err)
			}
			archive := filepath.Join(t.TempDir(), "snapshot.tar")
			if err := os.WriteFile(archive, buffer.Bytes(), 0600); err != nil {
				t.Fatal(err)
			}
			if err := restoreData(context.Background(), data, archive, hash(buffer.Bytes())); err == nil {
				t.Fatal("invalid snapshot accepted")
			}
			got, err := os.ReadFile(keep)
			if err != nil || string(got) != "candidate-data" {
				t.Fatalf("candidate removed before structural validation: %q %v", got, err)
			}
		})
	}
}

func TestReviewDataSnapshotAllowsReadersButExcludesEveryOtherWriter(t *testing.T) {
	driver := &ComposeDriver{Config: ComposeConfig{InstanceID: "instance", Project: "project", DataVolume: "data-volume"}}
	for _, tc := range []struct {
		name, fixture, current string
		allowed                bool
	}{
		{"read-only CA reader", `{"Id":"agent","Mounts":[{"Name":"data-volume","RW":false}],"Config":{"Labels":{}}}`, "", true},
		{"ordinary writer", `{"Id":"other","Mounts":[{"Name":"data-volume","RW":true}],"Config":{"Labels":{}}}`, "", false},
		{"read-only and writable aliases", `{"Id":"other","Mounts":[{"Name":"data-volume","RW":false},{"Name":"data-volume","RW":true}],"Config":{"Labels":{}}}`, "", false},
		{"updater writer", `{"Id":"updater","Mounts":[{"Name":"data-volume","RW":true}],"Config":{"Labels":{"p2pstream.server-update.executor":"instance"}}}`, "", true},
		{"foreign updater", `{"Id":"updater","Mounts":[{"Name":"data-volume","RW":true}],"Config":{"Labels":{"p2pstream.server-update.executor":"other-instance"}}}`, "", false},
		{"current server preflight", `{"Id":"current","Mounts":[{"Name":"data-volume","RW":true}],"Config":{"Labels":{"com.docker.compose.project":"project","com.docker.compose.service":"p2pstream","p2pstream.server-update.instance":"instance"}}}`, "current", true},
		{"current server during backup", `{"Id":"current","Mounts":[{"Name":"data-volume","RW":true}],"Config":{"Labels":{"com.docker.compose.project":"project","com.docker.compose.service":"p2pstream","p2pstream.server-update.instance":"instance"}}}`, "", false},
		{"different current server identity", `{"Id":"current","Mounts":[{"Name":"data-volume","RW":true}],"Config":{"Labels":{"com.docker.compose.project":"other","com.docker.compose.service":"p2pstream","p2pstream.server-update.instance":"instance"}}}`, "current", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var info containerInfo
			if err := json.Unmarshal([]byte(tc.fixture), &info); err != nil {
				t.Fatal(err)
			}
			if got := driver.dataAccessAllowed(info, tc.current); got != tc.allowed {
				t.Fatalf("allowed=%v want=%v", got, tc.allowed)
			}
		})
	}
}
