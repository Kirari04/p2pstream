package updater

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"

	"p2pstream/internal/agentupdateauth"
)

func TestSlotRetentionAcrossMultipleUpgradesAndRollback(t *testing.T) {
	f := newFixture(t)
	activateRetentionFixture(t, f)
	assertRetainedSlots(t, f.paths, slotName(f.bootstrap.Target), "v1.1.0")

	setRetentionFixtureRelease(t, &f, "v1.2.0", 'b', 3, 2, 42)
	activateRetentionFixture(t, f)
	assertRetainedSlots(t, f.paths, "v1.1.0", "v1.2.0")

	setRetentionFixtureRelease(t, &f, "v1.3.0", 'c', 4, 3, 43)
	activateRetentionFixture(t, f)
	assertRetainedSlots(t, f.paths, "v1.2.0", "v1.3.0")

	rollbackAssignment := Assignment{
		AgentPublicID: f.assignment.AgentPublicID,
		AssignmentID:  44,
		Generation:    10,
		Nonce:         bytes.Repeat([]byte{0x64}, 32),
	}
	rollbackAuthorization := signedFixtureAuthorization(
		t, f.authorityPrivate, f.authorization.Authorization.AuthorityKeyID,
		rollbackAssignment, f.release, agentupdateauth.AssignmentActionRollback, 4,
	)
	if err := RequestRollback(f.paths, rollbackAuthorization); err != nil {
		t.Fatal(err)
	}
	if err := Rollback(context.Background(), f.paths, &fakeService{}); err != nil {
		t.Fatal(err)
	}
	if target, err := currentTarget(f.paths); err != nil || target != "slots/v1.2.0/p2pstream" {
		t.Fatalf("current after rollback = %q, %v", target, err)
	}
	// The rolled-away slot remains protected by the durable latest-activation
	// state, so an interrupted receipt/report handoff remains recoverable.
	assertRetainedSlots(t, f.paths, "v1.2.0", "v1.3.0")

	setRetentionFixtureRelease(t, &f, "v1.4.0", 'e', 5, 5, 45)
	activateRetentionFixture(t, f)
	assertRetainedSlots(t, f.paths, "v1.2.0", "v1.4.0")
}

func TestSlotRetentionProtectsActivationAndRollbackJournals(t *testing.T) {
	f := newFixture(t)
	for _, version := range []string{"v1.2.0", "v1.3.0", "v1.4.0", "v1.5.0", "v1.6.0"} {
		writeRetentionTestSlot(t, f.paths, version)
	}
	activation := activationJournal{
		Phase:           journalPrepared,
		PreviousTarget:  "slots/v1.2.0/p2pstream",
		CandidateTarget: "slots/v1.3.0/p2pstream",
		PreviousSlot:    f.bootstrap,
	}
	if err := writeJournal(f.paths, activation); err != nil {
		t.Fatal(err)
	}
	rollback := rollbackJournal{
		Phase:            rollbackPrepared,
		AuthorizationSHA: strings.Repeat("a", 64),
		FromSlot:         retentionTestMetadata("v1.4.0"),
		ToSlot:           retentionTestMetadata("v1.5.0"),
	}
	if err := writeRollbackJournal(f.paths, rollback); err != nil {
		t.Fatal(err)
	}

	if err := pruneObsoleteSlots(f.paths); err != nil {
		t.Fatal(err)
	}
	assertRetainedSlots(t, f.paths,
		slotName(f.bootstrap.Target), "v1.2.0", "v1.3.0", "v1.4.0", "v1.5.0")
	if _, err := os.Stat(filepath.Join(f.paths.slotsDir(), "v1.6.0")); !os.IsNotExist(err) {
		t.Fatalf("obsolete unreferenced slot remains: %v", err)
	}
}

func TestSlotRetentionProtectsLocallySignedRootReceiptResult(t *testing.T) {
	f := newFixture(t)
	writeRetentionTestSlot(t, f.paths, f.release.Version)
	writeRetentionTestSlot(t, f.paths, "v1.2.0")
	authorizationDigest, err := agentupdateauth.AssignmentAuthorizationDigest(f.authorization.Authorization)
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := createRootActionReceipt(
		f.paths, f.authorization, hex.EncodeToString(authorizationDigest[:]), signedReleaseSlotMetadata(f.release),
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := atomicJSON(f.paths.rootActionReceiptPath(), receipt, 0644); err != nil {
		t.Fatal(err)
	}

	if err := pruneObsoleteSlots(f.paths); err != nil {
		t.Fatal(err)
	}
	assertRetainedSlots(t, f.paths, slotName(f.bootstrap.Target), f.release.Version)
}

func TestSlotRetentionRefusesSymlinkedVersionDirectory(t *testing.T) {
	f := newFixture(t)
	external := t.TempDir()
	sentinel := filepath.Join(external, "p2pstream")
	if err := os.WriteFile(sentinel, []byte("must remain\n"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(external, filepath.Join(f.paths.slotsDir(), "v9.9.9")); err != nil {
		t.Fatal(err)
	}

	err := pruneObsoleteSlots(f.paths)
	if err == nil || !strings.Contains(err.Error(), "without following links") {
		t.Fatalf("symlinked slot error = %v", err)
	}
	if got, err := os.ReadFile(sentinel); err != nil || string(got) != "must remain\n" {
		t.Fatalf("external target changed: %q, %v", got, err)
	}
	if info, err := os.Lstat(filepath.Join(f.paths.slotsDir(), "v9.9.9")); err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("symlinked slot was altered: %v, %v", info, err)
	}
}

func TestSlotRetentionRefusesSymlinkedInstallRoot(t *testing.T) {
	f := newFixture(t)
	symlinkRoot := filepath.Join(filepath.Dir(f.paths.InstallRoot), "install-link")
	if err := os.Symlink(f.paths.InstallRoot, symlinkRoot); err != nil {
		t.Fatal(err)
	}
	paths := f.paths
	paths.InstallRoot = symlinkRoot

	err := pruneObsoleteSlots(paths)
	if err == nil || !strings.Contains(err.Error(), "managed install root") {
		t.Fatalf("symlinked install root error = %v", err)
	}
	if _, err := os.Stat(filepath.Join(f.paths.InstallRoot, filepath.FromSlash(f.bootstrap.Target))); err != nil {
		t.Fatalf("current slot was altered through symlinked install root: %v", err)
	}
}

func setRetentionFixtureRelease(t testing.TB, f *fixture, version string, fill byte, releaseSequence, commandSequence uint64, assignmentID int64) {
	t.Helper()
	body := []byte("signed raw p2pstream executable " + version + "\n")
	digest := sha256.Sum256(body)
	release := VerifiedRelease{
		Version: version, Commit: strings.Repeat(string(fill), 40),
		ManifestSHA256: fmt.Sprintf("%x", sha256.Sum256([]byte("manifest-"+version))),
		Sequence:       releaseSequence, SecurityEpoch: 4,
		MinimumSafeVersion: "v1.0.0", RootVersion: 3,
		Artifact: Artifact{Name: runtimeArtifactName(version), Size: int64(len(body)), SHA256: digest},
	}
	assignment := Assignment{
		AgentPublicID: f.assignment.AgentPublicID,
		AssignmentID:  assignmentID,
		Generation:    assignmentID,
		Nonce:         bytes.Repeat([]byte{byte(assignmentID)}, 32),
	}
	f.body = body
	f.release = release
	f.source = &fakeSource{manifest: []byte("manifest"), signature: []byte("signatures"), body: body, artifact: release.Artifact}
	f.verifier = &fakeVerifier{release: release}
	f.assignment = assignment
	f.authorization = signedFixtureAuthorization(
		t, f.authorityPrivate, f.authorization.Authorization.AuthorityKeyID,
		assignment, release, agentupdateauth.AssignmentActionActivate, commandSequence,
	)
}

func activateRetentionFixture(t testing.TB, f fixture) {
	t.Helper()
	stageAndRequestActivation(t, f)
	if _, err := Activate(context.Background(), ActivateOptions{
		Paths: f.paths, Verifier: f.verifier, Service: &fakeService{},
		Policy: VerifyPolicy{CurrentVersion: "v1.0.0"}, DiskPreflight: allowDisk,
	}); err != nil {
		t.Fatal(err)
	}
}

func writeRetentionTestSlot(t testing.TB, paths Paths, version string) {
	t.Helper()
	dir := filepath.Join(paths.slotsDir(), version)
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "p2pstream"), []byte("test slot "+version+"\n"), 0755); err != nil {
		t.Fatal(err)
	}
}

func retentionTestMetadata(version string) slotMetadata {
	body := []byte("test slot " + version + "\n")
	digest := sha256.Sum256(body)
	return slotMetadata{
		Target: "slots/" + version + "/p2pstream", ResultKind: agentupdateauth.RootActionResultSignedRelease,
		RootVersion: 3, ManifestSHA256: strings.Repeat("f", 64), Version: version,
		Commit: strings.Repeat("c", 40), BuildVersion: version, BuildCommit: strings.Repeat("c", 40),
		ReleaseSequence: 9, SecurityEpoch: 4, OS: runtime.GOOS, Arch: runtime.GOARCH,
		ArtifactName: runtimeArtifactName(version), ArtifactSize: int64(len(body)), ArtifactSHA256: fmt.Sprintf("%x", digest),
	}
}

func slotName(target string) string {
	return strings.Split(target, "/")[1]
}

func assertRetainedSlots(t testing.TB, paths Paths, want ...string) {
	t.Helper()
	entries, err := os.ReadDir(paths.slotsDir())
	if err != nil {
		t.Fatal(err)
	}
	got := make([]string, 0, len(entries))
	for _, entry := range entries {
		got = append(got, entry.Name())
	}
	sort.Strings(got)
	sort.Strings(want)
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("slots = %v, want %v", got, want)
	}
}
