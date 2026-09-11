package updater

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"

	"p2pstream/internal/agentupdate"
	"p2pstream/internal/agentupdateauth"
	"p2pstream/internal/buildinfo"
)

// This test must also run in the isolated root-owned host test environment:
// ordinary same-user filesystem tests cannot detect the worker losing access
// to files atomically replaced by the root activator.
func TestActivationKeepsFloorReadableByWorker(t *testing.T) {
	if floorPath := os.Getenv("P2PSTREAM_DEEP_REVIEW_READ_FLOOR"); floorPath != "" {
		if _, err := loadFloor(floorPath); err != nil {
			t.Fatal(err)
		}
		return
	}
	if os.Geteuid() != 0 {
		t.Skip("requires an isolated root test environment to model separate activator and worker users")
	}
	f := newFixture(t)
	const workerID = 23456
	fixtureRoot := filepath.Dir(f.paths.StateDir)
	for _, dir := range []string{filepath.Dir(fixtureRoot), fixtureRoot} {
		if err := os.Chmod(dir, 0755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Chown(f.paths.StateDir, 0, workerID); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(f.paths.StateDir, 0750); err != nil {
		t.Fatal(err)
	}
	// Model bootstrap's root:p2pstream-updater 0640 floor exactly.
	if err := os.Chown(f.paths.floorPath(), 0, workerID); err != nil {
		t.Fatal(err)
	}
	readAsWorker := func() ([]byte, error) {
		cmd := exec.Command(os.Args[0], "-test.run=^TestActivationKeepsFloorReadableByWorker$")
		cmd.Env = append(os.Environ(), "P2PSTREAM_DEEP_REVIEW_READ_FLOOR="+f.paths.floorPath())
		cmd.SysProcAttr = &syscall.SysProcAttr{Credential: &syscall.Credential{Uid: workerID, Gid: workerID}}
		return cmd.CombinedOutput()
	}
	if output, err := readAsWorker(); err != nil {
		t.Fatalf("bootstrap floor is not readable by worker: %v\n%s", err, output)
	}
	stageAndRequestActivation(t, f)
	if _, err := Activate(context.Background(), ActivateOptions{
		Paths: f.paths, Verifier: f.verifier, Service: &fakeService{}, DiskPreflight: allowDisk,
	}); err != nil {
		t.Fatal(err)
	}
	if output, err := readAsWorker(); err != nil {
		info, _ := os.Stat(f.paths.floorPath())
		t.Fatalf("activation made security floor unreadable to the worker (mode/owner=%+v): %v\n%s", info.Sys(), err, output)
	}
}

func TestRetrySameTargetAfterManagedRollback(t *testing.T) {
	f := productionVerifierFixture(t)
	policy := VerifyPolicy{ServerVersion: "v1.5.0", UpdaterVersion: "v1.0.0", ProtocolVersion: 1}
	if _, err := Stage(context.Background(), StageOptions{
		Paths: f.paths, Source: f.source, Verifier: AgentUpdateVerifier{}, Policy: policy, DiskPreflight: allowDisk,
	}); err != nil {
		t.Fatal(err)
	}
	requestFixtureActivation(t, f)
	if _, err := Activate(context.Background(), ActivateOptions{
		Paths: f.paths, Verifier: AgentUpdateVerifier{}, Service: &fakeService{}, Policy: policy, DiskPreflight: allowDisk,
	}); err != nil {
		t.Fatal(err)
	}
	assignment := f.assignment
	assignment.Generation++
	rollbackAuthorization := signedFixtureAuthorization(t, f.authorityPrivate, f.authorization.Authorization.AuthorityKeyID,
		assignment, f.release, agentupdateauth.AssignmentActionRollback, 2)
	if err := RequestRollback(f.paths, rollbackAuthorization); err != nil {
		t.Fatal(err)
	}
	if err := Rollback(context.Background(), f.paths, &fakeService{}); err != nil {
		t.Fatal(err)
	}
	current, err := currentTarget(f.paths)
	if err != nil || current != f.bootstrap.Target {
		t.Fatalf("managed rollback did not restore bootstrap slot: %q, %v", current, err)
	}
	// Retry uses a new management command, but the immutable target remains the
	// same. A rollback must not make that permitted retry impossible to stage.
	result, err := Stage(context.Background(), StageOptions{
		Paths: f.paths, Source: f.source, Verifier: AgentUpdateVerifier{},
		Policy: policy, DiskPreflight: allowDisk,
	})
	if err != nil {
		t.Fatalf("same-target retry after managed rollback cannot stage: %v", err)
	}
	if !result.Changed {
		t.Fatal("same-target retry did not restage the candidate removed by completed activation")
	}
	requestFixtureActivation(t, f)
	if _, err := Activate(context.Background(), ActivateOptions{
		Paths: f.paths, Verifier: AgentUpdateVerifier{}, Service: &fakeService{}, Policy: policy, DiskPreflight: allowDisk,
	}); err == nil || !strings.Contains(err.Error(), "replayed") {
		t.Fatalf("same-target retry accepted the consumed authorization: %v", err)
	}
	assignment.Generation++
	f.assignment = assignment
	f.authorization = signedFixtureAuthorization(t, f.authorityPrivate, f.authorization.Authorization.AuthorityKeyID,
		assignment, f.release, agentupdateauth.AssignmentActionActivate, 3)
	requestFixtureActivation(t, f)
	if _, err := Activate(context.Background(), ActivateOptions{
		Paths: f.paths, Verifier: AgentUpdateVerifier{}, Service: &fakeService{}, Policy: policy, DiskPreflight: allowDisk,
	}); err != nil {
		t.Fatalf("same-target retry after managed rollback cannot activate: %v", err)
	}
}

func productionVerifierFixture(t *testing.T) fixture {
	t.Helper()
	f := newFixture(t)
	now := time.Now().UTC()
	manifest := agentupdate.Manifest{
		SchemaVersion: agentupdate.SchemaVersion, Channel: "stable", Version: f.release.Version,
		Commit: f.release.Commit, Sequence: f.release.Sequence, SecurityEpoch: f.release.SecurityEpoch,
		MinimumSafeVersion: f.release.MinimumSafeVersion,
		PublishedAt:        now.Add(-time.Hour).Format(time.RFC3339), ExpiresAt: now.Add(24 * time.Hour).Format(time.RFC3339),
		Compatibility: agentupdate.Compatibility{
			Server:   agentupdate.VersionRange{Min: "v1.0.0", Max: "v2.0.0"},
			Updater:  agentupdate.VersionRange{Min: "v1.0.0", Max: "v2.0.0"},
			Protocol: agentupdate.ProtocolRange{Min: 1, Max: 2},
		},
		Artifacts: []agentupdate.Artifact{{OS: runtime.GOOS, Arch: runtime.GOARCH, Name: f.release.Artifact.Name,
			Size: uint64(f.release.Artifact.Size), SHA256: artifactHex(f.release.Artifact)}},
	}
	data, err := agentupdate.CanonicalManifest(manifest)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(data)
	f.release.ManifestSHA256 = hex.EncodeToString(digest[:])
	f.source.manifest = data
	f.verifier.release = f.release
	f.authorization = signedFixtureAuthorization(t, f.authorityPrivate, f.authorization.Authorization.AuthorityKeyID,
		f.assignment, f.release, agentupdateauth.AssignmentActionActivate, 1)
	return f
}

func TestRepeatRollbackUsesFreshAuthorizationAndSameKnownGoodSlot(t *testing.T) {
	f := newFixture(t)
	stageAndRequestActivation(t, f)
	staleReady, err := os.ReadFile(f.paths.readyPath())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Activate(context.Background(), ActivateOptions{
		Paths: f.paths, Verifier: f.verifier, Service: &fakeService{}, DiskPreflight: allowDisk,
	}); err != nil {
		t.Fatal(err)
	}
	for sequence := uint64(2); sequence <= 3; sequence++ {
		assignment := f.assignment
		assignment.Generation += int64(sequence)
		authorization := signedFixtureAuthorization(t, f.authorityPrivate, f.authorization.Authorization.AuthorityKeyID,
			assignment, f.release, agentupdateauth.AssignmentActionRollback, sequence)
		if sequence == 3 {
			if err := os.WriteFile(f.paths.readyPath(), staleReady, 0600); err != nil {
				t.Fatal(err)
			}
		}
		if err := RequestRollback(f.paths, authorization); err != nil {
			t.Fatal(err)
		}
		if err := Rollback(context.Background(), f.paths, &fakeService{}); err != nil {
			t.Fatal(err)
		}
		current, err := currentTarget(f.paths)
		if err != nil || current != f.bootstrap.Target {
			t.Fatalf("rollback %d changed known-good slot: %q, %v", sequence, current, err)
		}
		data, err := os.ReadFile(f.paths.rollbackResultPath())
		if err != nil {
			t.Fatal(err)
		}
		var result rollbackRecord
		if err := strictJSON(data, &result); err != nil {
			t.Fatal(err)
		}
		if result.Receipt.Receipt.RootActionCounter != sequence || result.Receipt.Receipt.Generation != assignment.Generation ||
			result.Receipt.Receipt.ResultArtifactSHA256 != f.bootstrap.ArtifactSHA256 || result.Receipt.Receipt.ResultVersion != f.bootstrap.BuildVersion {
			t.Fatalf("rollback %d receipt does not bind fresh authorization and bootstrap: %+v", sequence, result.Receipt.Receipt)
		}
	}
	if _, err := os.Stat(f.paths.readyPath()); !os.IsNotExist(err) {
		t.Fatalf("repeat rollback retained superseded activation edge: %v", err)
	}
}

func TestRollbackDoesNotExecuteSameCompletedAuthorizationTwice(t *testing.T) {
	f := newFixture(t)
	authorization := signedFixtureAuthorization(t, f.authorityPrivate, f.authorization.Authorization.AuthorityKeyID,
		f.assignment, f.release, agentupdateauth.AssignmentActionRollback, 1)
	if err := RequestRollback(f.paths, authorization); err != nil {
		t.Fatal(err)
	}
	service := &fakeService{}
	if err := Rollback(context.Background(), f.paths, service); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(f.paths.rollbackResultPath())
	if err != nil {
		t.Fatal(err)
	}
	// A second worker check may race with the helper completing the first one.
	if err := RequestRollback(f.paths, authorization); err != nil {
		t.Fatal(err)
	}
	if err := Rollback(context.Background(), f.paths, service); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(f.paths.rollbackResultPath())
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatal("a replayed completed rollback authorization executed again and replaced its signed receipt")
	}
	if service.restarts != 1 {
		t.Fatalf("completed rollback replay restarted the agent %d times", service.restarts)
	}
}

func TestReinstallExactActiveTargetPreservesDistinctRollbackSlot(t *testing.T) {
	for _, failReinstall := range []bool{false, true} {
		name := "managed rollback"
		if failReinstall {
			name = "local health rollback"
		}
		t.Run(name, func(t *testing.T) {
			f := productionVerifierFixture(t)
			policy := VerifyPolicy{ServerVersion: "v1.5.0", UpdaterVersion: "v1.0.0", ProtocolVersion: 1}
			for sequence := uint64(1); sequence <= 2; sequence++ {
				f.assignment.Generation++
				f.authorization = signedFixtureAuthorization(t, f.authorityPrivate, f.authorization.Authorization.AuthorityKeyID,
					f.assignment, f.release, agentupdateauth.AssignmentActionActivate, sequence)
				if _, err := Stage(context.Background(), StageOptions{
					Paths: f.paths, Source: f.source, Verifier: AgentUpdateVerifier{}, Policy: policy, DiskPreflight: allowDisk,
				}); err != nil {
					t.Fatal(err)
				}
				requestFixtureActivation(t, f)
				_, err := Activate(context.Background(), ActivateOptions{
					Paths: f.paths, Verifier: AgentUpdateVerifier{}, Service: &fakeService{failHealth: failReinstall && sequence == 2},
					Policy: policy, DiskPreflight: allowDisk,
				})
				if failReinstall && sequence == 2 {
					if err == nil || !strings.Contains(err.Error(), "rolled back") {
						t.Fatalf("reinstall health failure did not roll back: %v", err)
					}
				} else if err != nil {
					t.Fatal(err)
				}
			}
			f.assignment.Generation++
			authorization := signedFixtureAuthorization(t, f.authorityPrivate, f.authorization.Authorization.AuthorityKeyID,
				f.assignment, f.release, agentupdateauth.AssignmentActionRollback, 3)
			if err := RequestRollback(f.paths, authorization); err != nil {
				t.Fatal(err)
			}
			if err := Rollback(context.Background(), f.paths, &fakeService{}); err != nil {
				t.Fatal(err)
			}
			current, err := currentTarget(f.paths)
			if err != nil || current != f.bootstrap.Target {
				t.Fatalf("reinstall discarded original known-good slot: %q, %v", current, err)
			}
			metadata, err := loadCurrentSlotMetadata(f.paths, current)
			if err != nil || metadata != f.bootstrap {
				t.Fatalf("rollback current metadata does not describe the recovered slot: %+v, %v", metadata, err)
			}
		})
	}
}

func TestLegacyFloorManifestMigrationRequiresMatchingAuthenticatedReceipt(t *testing.T) {
	for _, kind := range []string{"matching activation", "after rollback", "missing receipt", "newer floor", "tampered root receipt", "tampered authorization", "ahead root counter", "wrong signed result"} {
		t.Run(kind, func(t *testing.T) {
			f := newFixture(t)
			stageAndRequestActivation(t, f)
			if _, err := Activate(context.Background(), ActivateOptions{
				Paths: f.paths, Verifier: f.verifier, Service: &fakeService{}, DiskPreflight: allowDisk,
			}); err != nil {
				t.Fatal(err)
			}
			floor, err := loadFloor(f.paths.floorPath())
			if err != nil {
				t.Fatal(err)
			}
			floor.ManifestSHA256 = ""
			if kind == "newer floor" {
				floor.Sequence++
				floor.Version = "v1.2.0"
			}
			if err := atomicJSON(f.paths.floorPath(), floor, 0640); err != nil {
				t.Fatal(err)
			}
			wantError := false
			switch kind {
			case "after rollback":
				assignment := f.assignment
				assignment.Generation++
				authorization := signedFixtureAuthorization(t, f.authorityPrivate, f.authorization.Authorization.AuthorityKeyID,
					assignment, f.release, agentupdateauth.AssignmentActionRollback, 2)
				if err := RequestRollback(f.paths, authorization); err != nil {
					t.Fatal(err)
				}
				if err := Rollback(context.Background(), f.paths, &fakeService{}); err != nil {
					t.Fatal(err)
				}
			case "missing receipt":
				if err := os.Remove(f.paths.lastActivationPath()); err != nil {
					t.Fatal(err)
				}
			case "ahead root counter":
				wantError = true
				if err := atomicJSON(f.paths.rootActionCounterPath(), rootActionCounter{}, 0600); err != nil {
					t.Fatal(err)
				}
			case "tampered root receipt", "tampered authorization", "wrong signed result":
				wantError = true
				data, err := os.ReadFile(f.paths.lastActivationPath())
				if err != nil {
					t.Fatal(err)
				}
				var record completedActivation
				if err := strictJSON(data, &record); err != nil {
					t.Fatal(err)
				}
				if kind == "tampered root receipt" {
					record.Receipt.Signature[0] ^= 1
				} else if kind == "tampered authorization" {
					record.Authorization.Signature[0] ^= 1
				} else {
					record.Receipt.Receipt.ResultArtifactSHA256 = strings.Repeat("f", 64)
					private, err := loadActivatorPrivateKey(f.paths.activatorPrivateKeyPath())
					if err != nil {
						t.Fatal(err)
					}
					record.Receipt.CanonicalPayload, err = agentupdateauth.RootActionReceiptPayload(record.Receipt.Receipt)
					if err != nil {
						t.Fatal(err)
					}
					record.Receipt.Signature, err = agentupdateauth.SignRootActionReceipt(private, record.Receipt.Receipt)
					if err != nil {
						t.Fatal(err)
					}
				}
				if err := atomicJSON(f.paths.lastActivationPath(), record, 0600); err != nil {
					t.Fatal(err)
				}
			}
			err = restoreFloorManifestPin(f.paths)
			if (err != nil) != wantError {
				t.Fatalf("floor migration error = %v, wantError=%v", err, wantError)
			}
			got, err := loadFloor(f.paths.floorPath())
			if err != nil {
				t.Fatal(err)
			}
			want := floor
			if kind == "matching activation" || kind == "after rollback" {
				want.ManifestSHA256 = f.release.ManifestSHA256
			}
			if got != want {
				t.Fatalf("migration modified the floor without matching evidence: got %+v, want %+v", got, want)
			}
		})
	}
}

func TestCompletedRollbackReplayKeepsProofAndRejectsSubstitution(t *testing.T) {
	for _, kind := range []string{"lost worker publication", "legacy fallback", "forged legacy fallback", "different authorization", "superseded command"} {
		t.Run(kind, func(t *testing.T) {
			f := newFixture(t)
			authorization := signedFixtureAuthorization(t, f.authorityPrivate, f.authorization.Authorization.AuthorityKeyID,
				f.assignment, f.release, agentupdateauth.AssignmentActionRollback, 1)
			if err := RequestRollback(f.paths, authorization); err != nil {
				t.Fatal(err)
			}
			if err := Rollback(context.Background(), f.paths, &fakeService{}); err != nil {
				t.Fatal(err)
			}
			original, err := os.ReadFile(f.paths.rollbackResultPath())
			if err != nil {
				t.Fatal(err)
			}
			wantError, wantCounter := false, uint64(1)
			switch kind {
			case "lost worker publication":
				if err := os.Remove(f.paths.rollbackResultPath()); err != nil {
					t.Fatal(err)
				}
			case "legacy fallback", "forged legacy fallback":
				if err := os.Remove(f.paths.lastRollbackPath()); err != nil {
					t.Fatal(err)
				}
				if kind == "forged legacy fallback" {
					wantError = true
					var result rollbackRecord
					if err := strictJSON(original, &result); err != nil {
						t.Fatal(err)
					}
					result.Receipt.Signature[0] ^= 1
					if err := atomicJSON(f.paths.rollbackResultPath(), result, 0644); err != nil {
						t.Fatal(err)
					}
				}
			case "different authorization":
				wantError = true
				assignment := f.assignment
				assignment.Generation++
				authorization = signedFixtureAuthorization(t, f.authorityPrivate, f.authorization.Authorization.AuthorityKeyID,
					assignment, f.release, agentupdateauth.AssignmentActionRollback, 1)
			case "superseded command":
				wantError, wantCounter = true, 2
				assignment := f.assignment
				assignment.Generation++
				newer := signedFixtureAuthorization(t, f.authorityPrivate, f.authorization.Authorization.AuthorityKeyID,
					assignment, f.release, agentupdateauth.AssignmentActionRollback, 2)
				if err := RequestRollback(f.paths, newer); err != nil {
					t.Fatal(err)
				}
				if err := Rollback(context.Background(), f.paths, &fakeService{}); err != nil {
					t.Fatal(err)
				}
			}
			if err := RequestRollback(f.paths, authorization); err != nil {
				t.Fatal(err)
			}
			service := &fakeService{}
			err = Rollback(context.Background(), f.paths, service)
			if (err != nil) != wantError {
				t.Fatalf("replay error = %v, wantError=%v", err, wantError)
			}
			counter, err := loadRootActionCounter(f.paths.rootActionCounterPath())
			if err != nil || counter != wantCounter || service.restarts != 0 {
				t.Fatalf("replay changed counter or restarted: counter=%d, restarts=%d, err=%v", counter, service.restarts, err)
			}
			if !wantError {
				replayed, err := os.ReadFile(f.paths.rollbackResultPath())
				if err != nil || string(replayed) != string(original) {
					t.Fatalf("replay did not restore the original signed result: %v", err)
				}
			}
		})
	}
}

func TestNewlyEnrolledOldTunnelCanUpgradeToRescueRelease(t *testing.T) {
	f := productionVerifierFixture(t)
	if err := pinBootstrapState(f.paths, bootstrapVersionFloor(f.release.Version, f.bootstrap.BuildVersion), true, os.Geteuid(), os.Getegid()); err != nil {
		t.Fatal(err)
	}
	if _, err := Stage(context.Background(), StageOptions{
		Paths: f.paths, Source: f.source, Verifier: AgentUpdateVerifier{},
		Policy: VerifyPolicy{ServerVersion: "v1.5.0", UpdaterVersion: f.release.Version, ProtocolVersion: 1}, DiskPreflight: allowDisk,
	}); err != nil {
		t.Fatalf("new rescue runner prevents preserved older tunnel upgrading to current release: %v", err)
	}
}

func TestActivationRefreshesStagedManagementVersionOnlyAfterReverification(t *testing.T) {
	previousVersion := buildinfo.Version
	buildinfo.Version = "v1.0.0"
	t.Cleanup(func() { buildinfo.Version = previousVersion })
	for _, kind := range []string{"compatible server", "incompatible server", "forged authorization", "changed staged manifest"} {
		t.Run(kind, func(t *testing.T) {
			f := productionVerifierFixture(t)
			policy := VerifyPolicy{ServerVersion: "v1.5.0", UpdaterVersion: "v1.0.0", ProtocolVersion: 1}
			if _, err := Stage(context.Background(), StageOptions{
				Paths: f.paths, Source: f.source, Verifier: AgentUpdateVerifier{}, Policy: policy, DiskPreflight: allowDisk,
			}); err != nil {
				t.Fatal(err)
			}
			before, err := os.ReadFile(f.paths.stagedPath())
			if err != nil {
				t.Fatal(err)
			}
			serverVersion := "v1.5.1"
			if kind == "incompatible server" {
				serverVersion = "v2.1.0"
			}
			f.authorization.Authorization.ServerVersion = serverVersion
			f.authorization.CanonicalPayload, err = agentupdateauth.AssignmentAuthorizationPayload(f.authorization.Authorization)
			if err != nil {
				t.Fatal(err)
			}
			f.authorization.Signature, err = agentupdateauth.SignAssignmentAuthorization(f.authorityPrivate, f.authorization.Authorization)
			if err != nil {
				t.Fatal(err)
			}
			if kind == "forged authorization" {
				f.authorization.Signature[0] ^= 1
			}
			if kind == "changed staged manifest" {
				manifest, err := agentupdate.ParseManifest(f.source.manifest)
				if err != nil {
					t.Fatal(err)
				}
				manifest.Commit = strings.Repeat("c", 40)
				data, err := agentupdate.CanonicalManifest(manifest)
				if err != nil {
					t.Fatal(err)
				}
				if err := atomicWrite(filepath.Join(f.paths.candidateDir(), "manifest.json"), data, 0600); err != nil {
					t.Fatal(err)
				}
			}
			err = RequestActivation(f.paths, f.authorization, f.release, serverVersion)
			if kind != "compatible server" {
				if err == nil {
					t.Fatal("unsafe management context change armed activation")
				}
				after, readErr := os.ReadFile(f.paths.stagedPath())
				if readErr != nil || string(before) != string(after) {
					t.Fatalf("rejected management context changed staged state: %v", readErr)
				}
				if _, statErr := os.Stat(f.paths.readyPath()); !os.IsNotExist(statErr) {
					t.Fatalf("rejected management context published ready marker: %v", statErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			data, err := os.ReadFile(f.paths.stagedPath())
			if err != nil {
				t.Fatal(err)
			}
			var staged stagedRecord
			if err := strictJSON(data, &staged); err != nil || staged.ServerVersion != serverVersion {
				t.Fatalf("management context was not refreshed: %+v, %v", staged, err)
			}
			if _, err := Activate(context.Background(), ActivateOptions{
				Paths: f.paths, Verifier: AgentUpdateVerifier{}, Service: &fakeService{}, Policy: policy, DiskPreflight: allowDisk,
			}); err != nil {
				t.Fatalf("root could not activate after independent management context verification: %v", err)
			}
		})
	}
}

func TestCompletedActivationReplayDoesNotRestartOrDestroyNewerStaging(t *testing.T) {
	for _, kind := range []string{"duplicate edge", "consumed worker receipt", "newer staged edge", "expired completed command", "different authorization", "forged root proof", "wrong current slot"} {
		t.Run(kind, func(t *testing.T) {
			f := newFixture(t)
			stageAndRequestActivation(t, f)
			data, err := os.ReadFile(f.paths.readyPath())
			if err != nil {
				t.Fatal(err)
			}
			var ready readyRecord
			if err := strictJSON(data, &ready); err != nil {
				t.Fatal(err)
			}
			if _, err := Activate(context.Background(), ActivateOptions{
				Paths: f.paths, Verifier: f.verifier, Service: &fakeService{}, DiskPreflight: allowDisk,
			}); err != nil {
				t.Fatal(err)
			}
			expected, err := os.ReadFile(f.paths.rootActionReceiptPath())
			if err != nil {
				t.Fatal(err)
			}
			readyPath := f.paths.readyPath()
			wantError := false
			switch kind {
			case "consumed worker receipt":
				if err := os.Remove(f.paths.rootActionReceiptPath()); err != nil {
					t.Fatal(err)
				}
			case "newer staged edge":
				readyPath = f.paths.activationClaimPath()
				newer := ready
				assignment := f.assignment
				assignment.Generation++
				newer.Authorization = signedFixtureAuthorization(t, f.authorityPrivate, f.authorization.Authorization.AuthorityKeyID,
					assignment, f.release, agentupdateauth.AssignmentActionActivate, 2)
				newer.Generation = assignment.Generation
				if err := atomicJSON(f.paths.readyPath(), newer, 0600); err != nil {
					t.Fatal(err)
				}
				if err := atomicWrite(filepath.Join(f.paths.candidateDir(), "manifest.json"), []byte("later candidate bytes"), 0600); err != nil {
					t.Fatal(err)
				}
			case "expired completed command", "forged root proof":
				data, err := os.ReadFile(f.paths.lastActivationPath())
				if err != nil {
					t.Fatal(err)
				}
				var activation completedActivation
				if err := strictJSON(data, &activation); err != nil {
					t.Fatal(err)
				}
				if kind == "forged root proof" {
					wantError = true
					activation.Receipt.Signature[0] ^= 1
				} else {
					makeCompletedActionHistorical(t, f, &activation.Authorization, &activation.Receipt)
					ready.Authorization = activation.Authorization
					if err := atomicJSON(f.paths.rootActionReceiptPath(), activation.Receipt, 0644); err != nil {
						t.Fatal(err)
					}
					expected, err = os.ReadFile(f.paths.rootActionReceiptPath())
					if err != nil {
						t.Fatal(err)
					}
				}
				if err := atomicJSON(f.paths.lastActivationPath(), activation, 0600); err != nil {
					t.Fatal(err)
				}
			case "different authorization":
				wantError = true
				assignment := f.assignment
				assignment.Generation++
				ready.Authorization = signedFixtureAuthorization(t, f.authorityPrivate, f.authorization.Authorization.AuthorityKeyID,
					assignment, f.release, agentupdateauth.AssignmentActionActivate, 1)
				ready.Generation = assignment.Generation
			case "wrong current slot":
				wantError = true
				if err := switchCurrent(f.paths, f.bootstrap.Target); err != nil {
					t.Fatal(err)
				}
			}
			if err := atomicJSON(readyPath, ready, 0600); err != nil {
				t.Fatal(err)
			}
			service := &fakeService{}
			_, err = Activate(context.Background(), ActivateOptions{
				Paths: f.paths, ReadyPath: readyPath, Verifier: f.verifier, Service: service, DiskPreflight: allowDisk,
			})
			if (err != nil) != wantError {
				t.Fatalf("completed activation replay error=%v, wantError=%v", err, wantError)
			}
			counter, counterErr := loadRootActionCounter(f.paths.rootActionCounterPath())
			if service.restarts != 0 || counterErr != nil || counter != 1 {
				t.Fatalf("replay restarted or changed root counter: restarts=%d counter=%d err=%v", service.restarts, counter, counterErr)
			}
			if !wantError {
				got, err := os.ReadFile(f.paths.rootActionReceiptPath())
				if err != nil || string(got) != string(expected) {
					t.Fatalf("replay changed completed root proof: %v", err)
				}
				if _, err := os.Stat(readyPath); !os.IsNotExist(err) {
					t.Fatalf("replay left its old activation edge: %v", err)
				}
			}
			if kind == "newer staged edge" {
				if _, err := os.Stat(f.paths.readyPath()); err != nil {
					t.Fatalf("old replay consumed newer ready edge: %v", err)
				}
				data, err := os.ReadFile(filepath.Join(f.paths.candidateDir(), "manifest.json"))
				if err != nil || string(data) != "later candidate bytes" {
					t.Fatalf("old replay removed newer candidate: %v", err)
				}
			}
		})
	}
}

func makeCompletedActionHistorical(t *testing.T, f fixture, authorization *assignmentAuthorizationRecord, receipt *rootActionReceiptRecord) {
	t.Helper()
	authorization.Authorization.IssuedAtUnixMillis = time.Now().Add(-2 * time.Hour).UnixMilli()
	authorization.Authorization.ExpiresAtUnixMillis = time.Now().Add(-time.Hour).UnixMilli()
	var err error
	authorization.CanonicalPayload, err = agentupdateauth.AssignmentAuthorizationPayload(authorization.Authorization)
	if err != nil {
		t.Fatal(err)
	}
	authorization.Signature, err = agentupdateauth.SignAssignmentAuthorization(f.authorityPrivate, authorization.Authorization)
	if err != nil {
		t.Fatal(err)
	}
	digest, err := agentupdateauth.AssignmentAuthorizationDigest(authorization.Authorization)
	if err != nil {
		t.Fatal(err)
	}
	receipt.Receipt.AuthorizationSHA256 = hex.EncodeToString(digest[:])
	receipt.Receipt.CompletedAtUnixMillis = authorization.Authorization.ExpiresAtUnixMillis - 1000
	receipt.CanonicalPayload, err = agentupdateauth.RootActionReceiptPayload(receipt.Receipt)
	if err != nil {
		t.Fatal(err)
	}
	private, err := loadActivatorPrivateKey(f.paths.activatorPrivateKeyPath())
	if err != nil {
		t.Fatal(err)
	}
	receipt.Signature, err = agentupdateauth.SignRootActionReceipt(private, receipt.Receipt)
	if err != nil {
		t.Fatal(err)
	}
	if err := atomicJSON(f.paths.rootCommandFloorPath(), rootCommandFloor{
		Sequence: authorization.Authorization.CommandSequence, AuthorizationSHA256: receipt.Receipt.AuthorizationSHA256,
	}, 0600); err != nil {
		t.Fatal(err)
	}
}

func TestEnrollmentDoesNotRaceActiveWorker(t *testing.T) {
	f := newFixture(t)
	if err := os.MkdirAll(f.paths.workerStateDir(), 0700); err != nil {
		t.Fatal(err)
	}
	lock, err := acquireLock(filepath.Join(f.paths.workerStateDir(), "worker.lock"))
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	api := &fakeControlAPI{t: t}
	control := WorkerControl{Paths: f.paths, API: api}
	if err := control.Enroll(context.Background(), HostConfig{}); err == nil || !strings.Contains(err.Error(), "lock updater enrollment state") {
		t.Fatalf("enrollment raced a live worker: %v", err)
	}
	if api.enrollCalls != 0 {
		t.Fatal("enrollment consumed a token while a worker held its state lock")
	}
}

func TestBootstrapDoesNotRaceRootAction(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("requires an isolated root host with the updater account")
	}
	if _, err := user.Lookup(DefaultUpdaterUser); err != nil {
		t.Skip("isolated host has no updater account")
	}
	f := newFixture(t)
	lock, err := acquireLock(f.paths.lockPath())
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	oldVersion := buildinfo.Version
	buildinfo.Version = f.release.Version
	t.Cleanup(func() { buildinfo.Version = oldVersion })
	config, err := LoadHostConfig(f.paths.ConfigPath)
	if err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(f.paths.floorPath())
	if err != nil {
		t.Fatal(err)
	}
	_, err = BootstrapHost(BootstrapOptions{
		Paths: f.paths, UpdaterUser: DefaultUpdaterUser, Config: config, EnrollmentToken: "one-use",
		CurrentVersion: f.release.Version,
	})
	if err == nil || !strings.Contains(err.Error(), "lock updater bootstrap state") {
		t.Fatalf("bootstrap raced active root action: %v", err)
	}
	after, err := os.ReadFile(f.paths.floorPath())
	if err != nil || string(before) != string(after) {
		t.Fatalf("bootstrap modified security floor during root action: %v", err)
	}
}

func TestExpiredCompletedRollbackOnlyAcknowledgesPastExecution(t *testing.T) {
	f := newFixture(t)
	authorization := signedFixtureAuthorization(t, f.authorityPrivate, f.authorization.Authorization.AuthorityKeyID,
		f.assignment, f.release, agentupdateauth.AssignmentActionRollback, 1)
	if err := RequestRollback(f.paths, authorization); err != nil {
		t.Fatal(err)
	}
	if err := Rollback(context.Background(), f.paths, &fakeService{}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(f.paths.lastRollbackPath())
	if err != nil {
		t.Fatal(err)
	}
	var result rollbackRecord
	if err := strictJSON(data, &result); err != nil {
		t.Fatal(err)
	}
	makeCompletedActionHistorical(t, f, &result.Authorization, &result.Receipt)
	if err := atomicJSON(f.paths.lastRollbackPath(), result, 0600); err != nil {
		t.Fatal(err)
	}
	if err := atomicJSON(f.paths.rollbackResultPath(), result, 0644); err != nil {
		t.Fatal(err)
	}
	expected, err := os.ReadFile(f.paths.rollbackResultPath())
	if err != nil {
		t.Fatal(err)
	}
	// This command was already queued when it was valid, then processed late.
	if err := atomicJSON(f.paths.rollbackPath(), rollbackRequest{Authorization: result.Authorization}, 0600); err != nil {
		t.Fatal(err)
	}
	service := &fakeService{}
	if err := Rollback(context.Background(), f.paths, service); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(f.paths.rollbackResultPath())
	if err != nil || string(got) != string(expected) || service.restarts != 0 {
		t.Fatalf("expired completed rollback was not acknowledged exactly: restarts=%d err=%v", service.restarts, err)
	}
	fresh := result.Authorization
	fresh.Authorization.CommandSequence++
	fresh.CanonicalPayload, err = agentupdateauth.AssignmentAuthorizationPayload(fresh.Authorization)
	if err != nil {
		t.Fatal(err)
	}
	fresh.Signature, err = agentupdateauth.SignAssignmentAuthorization(f.authorityPrivate, fresh.Authorization)
	if err != nil {
		t.Fatal(err)
	}
	if err := RequestRollback(f.paths, fresh); err == nil {
		t.Fatal("worker accepted a fresh expired rollback command")
	}
	if err := atomicJSON(f.paths.rollbackPath(), rollbackRequest{Authorization: fresh}, 0600); err != nil {
		t.Fatal(err)
	}
	if err := Rollback(context.Background(), f.paths, service); err == nil || !strings.Contains(err.Error(), "not currently valid") {
		t.Fatalf("root accepted a fresh expired rollback command: %v", err)
	}
	if service.restarts != 0 {
		t.Fatal("expired rollback command restarted the agent")
	}
}
