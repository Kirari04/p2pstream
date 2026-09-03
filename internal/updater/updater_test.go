package updater

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	p2pstreamv1 "p2pstream/gen/proto/p2pstream/v1"
	"p2pstream/internal/agentupdate"
	"p2pstream/internal/agentupdateauth"
)

type fakeSource struct {
	manifest []byte
	body     []byte
	artifact Artifact
	fetches  atomic.Int32
}

func (s *fakeSource) FetchMetadata(context.Context) ([]byte, error) {
	return append([]byte(nil), s.manifest...), nil
}

func (s *fakeSource) FetchArtifact(_ context.Context, artifact Artifact) (io.ReadCloser, error) {
	if artifact != s.artifact {
		return nil, errors.New("unexpected artifact")
	}
	s.fetches.Add(1)
	return io.NopCloser(bytes.NewReader(s.body)), nil
}

type fakeVerifier struct {
	release           VerifiedRelease
	wantServerVersion string
	calls             atomic.Int32
}

func (v *fakeVerifier) Verify(manifest []byte, policy VerifyPolicy) (VerifiedRelease, error) {
	v.calls.Add(1)
	if string(manifest) != "manifest" {
		return VerifiedRelease{}, errors.New("metadata verification failed")
	}
	if policy.CurrentSequence >= v.release.Sequence {
		return VerifiedRelease{}, errors.New("sequence does not advance floor")
	}
	if policy.CurrentSecurityEpoch > v.release.SecurityEpoch {
		return VerifiedRelease{}, errors.New("security epoch is below floor")
	}
	if v.wantServerVersion != "" && policy.ServerVersion != v.wantServerVersion {
		return VerifiedRelease{}, errors.New("management server version was not bound into verification")
	}
	return v.release, nil
}

func (v *fakeVerifier) VerifyArtifact(reader io.Reader, artifact Artifact) error {
	data, err := io.ReadAll(io.LimitReader(reader, artifact.Size+1))
	if err != nil {
		return err
	}
	digest := sha256.Sum256(data)
	if int64(len(data)) != artifact.Size || digest != artifact.SHA256 {
		return errors.New("artifact mismatch")
	}
	return nil
}

type fakeService struct {
	mu            sync.Mutex
	restarts      int
	healthCalls   int
	failHealth    bool
	healthBlock   <-chan struct{}
	healthEntered chan<- struct{}
}

func (s *fakeService) Restart(context.Context) error {
	s.mu.Lock()
	s.restarts++
	s.mu.Unlock()
	return nil
}

func (s *fakeService) Healthy(ctx context.Context) error {
	s.mu.Lock()
	s.healthCalls++
	fail := s.failHealth
	block := s.healthBlock
	entered := s.healthEntered
	s.mu.Unlock()
	if entered != nil {
		select {
		case entered <- struct{}{}:
		default:
		}
	}
	if block != nil {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-block:
		}
	}
	if fail {
		s.mu.Lock()
		s.failHealth = false // rollback health succeeds
		s.mu.Unlock()
		return errors.New("unhealthy candidate")
	}
	return nil
}

type alwaysFailService struct {
	mu       sync.Mutex
	restarts int
}

func (s *alwaysFailService) Restart(context.Context) error {
	s.mu.Lock()
	s.restarts++
	s.mu.Unlock()
	return nil
}

func (*alwaysFailService) Healthy(context.Context) error {
	return errors.New("injected unhealthy service")
}

type fixture struct {
	paths            Paths
	body             []byte
	release          VerifiedRelease
	source           *fakeSource
	verifier         *fakeVerifier
	assignment       Assignment
	authorization    assignmentAuthorizationRecord
	authorityPrivate ed25519.PrivateKey
	activatorPublic  ed25519.PublicKey
	bootstrap        slotMetadata
}

func newFixture(t testing.TB) fixture {
	t.Helper()
	root := t.TempDir()
	paths := Paths{
		ConfigPath:  filepath.Join(root, "etc", "updater.json"),
		StateDir:    filepath.Join(root, "state"),
		InstallRoot: filepath.Join(root, "install"),
		CommandPath: filepath.Join(root, "bin", "p2pstream"),
	}
	for _, dir := range []string{filepath.Dir(paths.ConfigPath), paths.stagingDir(), paths.rootStateDir(), paths.slotsDir(), filepath.Dir(paths.CommandPath)} {
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	body := []byte("raw p2pstream executable\n")
	digest := sha256.Sum256(body)
	release := VerifiedRelease{
		Version: "v1.1.0", Commit: strings.Repeat("a", 40),
		ManifestSHA256: fmt.Sprintf("%x", sha256.Sum256([]byte("manifest"))),
		Sequence:       2, SecurityEpoch: 4,
		MinimumSafeVersion: "v1.0.1",
		Artifact:           Artifact{Name: runtimeArtifactName("v1.1.0"), Size: int64(len(body)), SHA256: digest},
	}
	source := &fakeSource{manifest: []byte("manifest"), body: body, artifact: release.Artifact}
	verifier := &fakeVerifier{release: release}
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	encodedPrivate, err := agentupdate.EncodePrivateKey(private)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(paths.activatorPrivateKeyPath(), []byte(encodedPrivate+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(paths.activatorPublicKeyPath(), []byte(mustEncodePublicKey(t, public)+"\n"), 0644); err != nil {
		t.Fatal(err)
	}
	authorityPublic, authorityPrivate, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	authorityKeyID, _ := agentupdateauth.KeyID(authorityPublic)
	if err := atomicJSON(paths.authorityPath(), pinnedManagementAuthority{KeyID: authorityKeyID, Epoch: 1, PublicKey: mustEncodePublicKey(t, authorityPublic)}, 0640); err != nil {
		t.Fatal(err)
	}
	config := HostConfig{Repository: "owner/repo", ManagementOrigin: "https://management.example", AgentPublicID: "agent-public-a", Channel: "stable"}
	if err := atomicJSON(paths.ConfigPath, config, 0640); err != nil {
		t.Fatal(err)
	}
	bootstrapBody := []byte("old executable\n")
	bootstrapDigest := sha256.Sum256(bootstrapBody)
	bootstrapVersion := "bootstrap-" + fmt.Sprintf("%x", bootstrapDigest)[:16]
	bootstrapSlot(t, paths, bootstrapVersion, bootstrapBody)
	bootstrap := slotMetadata{
		Target: "slots/" + bootstrapVersion + "/p2pstream", ResultKind: agentupdateauth.RootActionResultBootstrap,
		Version: bootstrapVersion, BuildVersion: "v1.0.0", BuildCommit: strings.Repeat("d", 40), OS: runtime.GOOS, Arch: runtime.GOARCH,
		ArtifactName: "p2pstream_bootstrap_" + runtime.GOOS + "_" + runtime.GOARCH,
		ArtifactSize: int64(len(bootstrapBody)), ArtifactSHA256: fmt.Sprintf("%x", bootstrapDigest),
	}
	if err := atomicJSON(paths.currentSlotMetadataPath(), bootstrap, 0600); err != nil {
		t.Fatal(err)
	}
	if err := atomicJSON(paths.floorPath(), Floor{Version: "v1.0.0", Sequence: 1, SecurityEpoch: 3, MinimumSafeVersion: "v1.0.0"}, 0640); err != nil {
		t.Fatal(err)
	}
	assignment := Assignment{AgentPublicID: "agent-public-a", AssignmentID: 41, Generation: 7, Nonce: bytes.Repeat([]byte{0x42}, 32)}
	authorization := signedFixtureAuthorization(t, authorityPrivate, authorityKeyID, assignment, release, agentupdateauth.AssignmentActionActivate, 1)
	return fixture{
		paths: paths, body: body, release: release, source: source, verifier: verifier,
		assignment: assignment, authorization: authorization, authorityPrivate: authorityPrivate, activatorPublic: public, bootstrap: bootstrap,
	}
}

func bootstrapSlot(t testing.TB, paths Paths, version string, body []byte) {
	t.Helper()
	dir := filepath.Join(paths.slotsDir(), version)
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "p2pstream"), body, 0755); err != nil {
		t.Fatal(err)
	}
	_ = os.Remove(paths.currentPath())
	if err := os.Symlink(filepath.ToSlash(filepath.Join("slots", version, "p2pstream")), paths.currentPath()); err != nil {
		t.Fatal(err)
	}
}

func allowDisk(string, int64) error { return nil }

func stageFixture(t testing.TB, f fixture) {
	t.Helper()
	result, err := Stage(context.Background(), StageOptions{
		Paths: f.paths, Source: f.source, Verifier: f.verifier,
		Policy: VerifyPolicy{CurrentVersion: "v1.0.0", ServerVersion: "v1.5.0"}, DiskPreflight: allowDisk,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Changed || result.Version != f.release.Version {
		t.Fatalf("stage result = %+v", result)
	}
}

func requestFixtureActivation(t testing.TB, f fixture) {
	t.Helper()
	if err := RequestActivation(f.paths, f.authorization, f.release, "v1.5.0"); err != nil {
		t.Fatal(err)
	}
}

func mustEncodePublicKey(t testing.TB, key ed25519.PublicKey) string {
	t.Helper()
	encoded, err := agentupdate.EncodePublicKey(key)
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}

func signedFixtureAuthorization(t testing.TB, private ed25519.PrivateKey, keyID string, assignment Assignment, release VerifiedRelease, action agentupdateauth.AssignmentAction, sequence uint64) assignmentAuthorizationRecord {
	t.Helper()
	now := time.Now().UTC()
	authorization := agentupdateauth.AssignmentAuthorization{
		AgentPublicID: assignment.AgentPublicID, AssignmentID: assignment.AssignmentID, CampaignID: 10,
		Generation: assignment.Generation, Action: action, CommandSequence: sequence, Nonce: append([]byte(nil), assignment.Nonce...),
		IssuedAtUnixMillis: now.Add(-time.Minute).UnixMilli(), ExpiresAtUnixMillis: now.Add(time.Hour).UnixMilli(),
		AuthorityKeyID: keyID, AuthorityEpoch: 1, ServerVersion: "v1.5.0",
		ManifestSHA256: release.ManifestSHA256, TargetVersion: release.Version, TargetCommit: release.Commit,
		ReleaseSequence: release.Sequence, SecurityEpoch: release.SecurityEpoch, OS: runtime.GOOS, Arch: runtime.GOARCH,
		ArtifactName: release.Artifact.Name, ArtifactSize: release.Artifact.Size, ArtifactSHA256: artifactHex(release.Artifact),
	}
	payload, err := agentupdateauth.AssignmentAuthorizationPayload(authorization)
	if err != nil {
		t.Fatal(err)
	}
	signature, err := agentupdateauth.SignAssignmentAuthorization(private, authorization)
	if err != nil {
		t.Fatal(err)
	}
	return assignmentAuthorizationRecord{Authorization: authorization, CanonicalPayload: payload, Signature: signature}
}

func fixtureAuthorizationProto(t testing.TB, record assignmentAuthorizationRecord) *p2pstreamv1.AgentUpdateAssignmentAuthorization {
	t.Helper()
	a := record.Authorization
	action, err := authorizationActionToProto(a.Action)
	if err != nil {
		t.Fatal(err)
	}
	return &p2pstreamv1.AgentUpdateAssignmentAuthorization{
		AgentPublicId: a.AgentPublicID, AssignmentId: a.AssignmentID, CampaignId: a.CampaignID,
		Generation: a.Generation, Action: action, CommandSequence: a.CommandSequence,
		Nonce: append([]byte(nil), a.Nonce...), IssuedAtUnixMillis: a.IssuedAtUnixMillis,
		ExpiresAtUnixMillis: a.ExpiresAtUnixMillis, AuthorityKeyId: a.AuthorityKeyID,
		AuthorityEpoch: a.AuthorityEpoch, ServerVersion: a.ServerVersion,
		ManifestSha256: a.ManifestSHA256, TargetVersion: a.TargetVersion, TargetCommit: a.TargetCommit,
		ReleaseSequence: a.ReleaseSequence, SecurityEpoch: a.SecurityEpoch, Os: a.OS, Arch: a.Arch,
		ArtifactName: a.ArtifactName, ArtifactSize: a.ArtifactSize, ArtifactSha256: a.ArtifactSHA256,
		CanonicalPayload: append([]byte(nil), record.CanonicalPayload...), Signature: append([]byte(nil), record.Signature...),
	}
}

func fixtureCheckResponse(t testing.TB, f fixture) *p2pstreamv1.CheckAgentUpdateResponse {
	t.Helper()
	return &p2pstreamv1.CheckAgentUpdateResponse{
		AssignmentId: f.authorization.Authorization.AssignmentID, CampaignId: f.authorization.Authorization.CampaignID,
		Generation:    f.authorization.Authorization.Generation,
		DesiredAction: p2pstreamv1.AgentUpdateDesiredAction_AGENT_UPDATE_DESIRED_ACTION_ACTIVATE,
		ServerVersion: f.authorization.Authorization.ServerVersion,
		Target: &p2pstreamv1.AgentUpdateTarget{
			Version: f.release.Version, Commit: f.release.Commit, ManifestSha256: f.release.ManifestSHA256,
			ReleaseSequence: int64(f.release.Sequence), SecurityEpoch: int64(f.release.SecurityEpoch),
		},
		Artifact: &p2pstreamv1.AgentUpdateArtifact{
			Os: runtime.GOOS, Arch: runtime.GOARCH, Name: f.release.Artifact.Name,
			SizeBytes: f.release.Artifact.Size, Sha256: artifactHex(f.release.Artifact),
		},
		Authorization: fixtureAuthorizationProto(t, f.authorization),
	}
}

func stageAndRequestActivation(t testing.TB, f fixture) {
	t.Helper()
	stageFixture(t, f)
	requestFixtureActivation(t, f)
}

func TestStagePublishesReadyLastAndVerifiesTwice(t *testing.T) {
	f := newFixture(t)
	stageFixture(t, f)
	if f.verifier.calls.Load() != 2 {
		t.Fatalf("verification calls = %d, want 2", f.verifier.calls.Load())
	}
	if f.source.fetches.Load() != 1 {
		t.Fatalf("artifact fetches = %d, want 1", f.source.fetches.Load())
	}
	got, err := os.ReadFile(filepath.Join(f.paths.candidateDir(), "artifact.bin"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, f.body) {
		t.Fatal("staged artifact changed")
	}
	if _, err := os.Stat(f.paths.stagedPath()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(f.paths.readyPath()); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("staging must not publish the activation edge")
	}
}

func TestWorkerRetriesDurableStagedReportWithoutDownloadingAgain(t *testing.T) {
	f := newFixture(t)
	stageFixture(t, f)
	updaterPublic, updaterPrivate, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(f.paths.workerStateDir(), 0700); err != nil {
		t.Fatal(err)
	}
	writeTestPrivateKey(t, f.paths.workerPrivateKeyPath(), updaterPrivate)
	api := &fakeControlAPI{t: t, updaterPublic: updaterPublic}
	worker := Worker{
		Paths:  f.paths,
		Config: HostConfig{AgentPublicID: f.assignment.AgentPublicID},
		Control: WorkerControl{
			Paths: f.paths, API: api, UpdaterVersion: "v1.0.0",
		},
	}
	check := fixtureCheckResponse(t, f)
	check.DesiredAction = p2pstreamv1.AgentUpdateDesiredAction_AGENT_UPDATE_DESIRED_ACTION_STAGE
	if err := worker.stage(context.Background(), check); err != nil {
		t.Fatal(err)
	}
	if got := f.source.fetches.Load(); got != 1 {
		t.Fatalf("artifact fetches = %d, want original single fetch", got)
	}
	if api.lastReport == nil || api.lastReport.State != p2pstreamv1.AgentUpdaterReportState_AGENT_UPDATER_REPORT_STATE_STAGED {
		t.Fatalf("durable staged retry report = %+v", api.lastReport)
	}
}

func TestWorkerArchivesSupersededActivationReceiptAndCanPollNewGeneration(t *testing.T) {
	f := newFixture(t)
	stageAndRequestActivation(t, f)
	if _, err := Activate(context.Background(), ActivateOptions{
		Paths: f.paths, Verifier: f.verifier, Service: &fakeService{},
		Policy: VerifyPolicy{CurrentVersion: "v1.0.0", ServerVersion: "v1.5.0"}, DiskPreflight: allowDisk,
	}); err != nil {
		t.Fatal(err)
	}
	updaterPublic, updaterPrivate, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(f.paths.workerStateDir(), 0700); err != nil {
		t.Fatal(err)
	}
	writeTestPrivateKey(t, f.paths.workerPrivateKeyPath(), updaterPrivate)
	api := &fakeControlAPI{
		t: t, updaterPublic: updaterPublic,
		reportResponse: &p2pstreamv1.ReportAgentUpdateResponse{
			Generation: f.assignment.Generation + 1,
			State:      p2pstreamv1.AgentUpdateAssignmentState_AGENT_UPDATE_ASSIGNMENT_STATE_BLOCKED,
		},
	}
	worker := Worker{
		Paths: f.paths, Config: HostConfig{AgentPublicID: f.assignment.AgentPublicID},
		Control: WorkerControl{Paths: f.paths, API: api, UpdaterVersion: "v1.0.0"},
	}
	handled, err := worker.reportActivation(context.Background())
	if err != nil || !handled {
		t.Fatalf("superseded activation handling = handled:%v err:%v", handled, err)
	}
	for _, path := range []string{f.paths.rootActionReceiptPath(), f.paths.activationReportStatePath()} {
		if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("superseded durable result was retained at %s: %v", path, err)
		}
	}
}

func TestWorkerArchivesIdempotentlyAcknowledgedFailureAndCanPollAgain(t *testing.T) {
	f := newFixture(t)
	updaterPublic, updaterPrivate, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(f.paths.workerStateDir(), 0700); err != nil {
		t.Fatal(err)
	}
	writeTestPrivateKey(t, f.paths.workerPrivateKeyPath(), updaterPrivate)
	failure := failureRecord{AssignmentID: f.assignment.AssignmentID, Generation: f.assignment.Generation, Code: "activation_failed", Detail: "candidate did not start"}
	if err := atomicJSON(f.paths.failurePath(), failure, 0644); err != nil {
		t.Fatal(err)
	}
	api := &fakeControlAPI{
		t: t, updaterPublic: updaterPublic,
		reportResponse: &p2pstreamv1.ReportAgentUpdateResponse{
			Generation: f.assignment.Generation,
			State:      p2pstreamv1.AgentUpdateAssignmentState_AGENT_UPDATE_ASSIGNMENT_STATE_BLOCKED,
		},
	}
	worker := Worker{
		Paths: f.paths, Config: HostConfig{AgentPublicID: f.assignment.AgentPublicID},
		Control: WorkerControl{Paths: f.paths, API: api, UpdaterVersion: "v1.0.0"},
	}
	handled, err := worker.reportFailure(context.Background())
	if err != nil || !handled {
		t.Fatalf("idempotent failure acknowledgment = handled:%v err:%v", handled, err)
	}
	if _, err := os.Lstat(f.paths.failurePath()); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("acknowledged durable failure was retained: %v", err)
	}
}

func TestStageAndActivationBindManagementServerVersion(t *testing.T) {
	f := newFixture(t)
	f.verifier.wantServerVersion = "v1.5.0"
	stageFixture(t, f)
	if err := RequestActivation(f.paths, f.authorization, f.release, "v1.6.0"); err == nil || !strings.Contains(err.Error(), "exactly match") {
		t.Fatalf("changed server-version assignment accepted: %v", err)
	}
	requestFixtureActivation(t, f)
	if _, err := Activate(context.Background(), ActivateOptions{
		Paths: f.paths, Verifier: f.verifier, Service: &fakeService{},
		Policy: VerifyPolicy{CurrentVersion: "v1.0.0", ServerVersion: "v1.5.0"}, DiskPreflight: allowDisk,
	}); err != nil {
		t.Fatal(err)
	}
}

func TestWorkerBindsProductionCheckAuthorizationWire(t *testing.T) {
	f := newFixture(t)
	worker := Worker{Paths: f.paths, Config: HostConfig{AgentPublicID: f.assignment.AgentPublicID}}
	check := fixtureCheckResponse(t, f)
	record, release, err := worker.authorizationFromCheck(check, agentupdateauth.AssignmentActionActivate)
	if err != nil {
		t.Fatal(err)
	}
	if record.Authorization.CommandSequence != f.authorization.Authorization.CommandSequence ||
		release.Version != f.release.Version || release.Commit != f.release.Commit ||
		release.ManifestSHA256 != f.release.ManifestSHA256 || release.Sequence != f.release.Sequence ||
		release.SecurityEpoch != f.release.SecurityEpoch ||
		release.Artifact != f.release.Artifact {
		t.Fatalf("wire authorization/release mismatch: record=%+v release=%+v", record, release)
	}

	tampered := fixtureCheckResponse(t, f)
	tampered.Authorization.CanonicalPayload[0] ^= 0xff
	if _, _, err := worker.authorizationFromCheck(tampered, agentupdateauth.AssignmentActionActivate); err == nil || !strings.Contains(err.Error(), "canonical payload mismatch") {
		t.Fatalf("tampered canonical authorization accepted: %v", err)
	}

	confused := fixtureCheckResponse(t, f)
	confused.DesiredAction = p2pstreamv1.AgentUpdateDesiredAction_AGENT_UPDATE_DESIRED_ACTION_ROLLBACK
	if _, _, err := worker.authorizationFromCheck(confused, agentupdateauth.AssignmentActionActivate); err == nil || !strings.Contains(err.Error(), "check context") {
		t.Fatalf("action-confused check accepted: %v", err)
	}
}

func TestRootRejectsWorkerForgedAssignmentAuthorization(t *testing.T) {
	f := newFixture(t)
	stageAndRequestActivation(t, f)
	data, err := readRegularNoFollow(f.paths.readyPath(), 128<<10)
	if err != nil {
		t.Fatal(err)
	}
	var ready readyRecord
	if err := strictJSON(data, &ready); err != nil {
		t.Fatal(err)
	}
	ready.AssignmentID++
	ready.Authorization.Authorization.AssignmentID = ready.AssignmentID
	if err := atomicJSON(f.paths.readyPath(), ready, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Activate(context.Background(), ActivateOptions{
		Paths: f.paths, Verifier: f.verifier, Service: &fakeService{}, DiskPreflight: allowDisk,
	}); err == nil || !strings.Contains(err.Error(), "canonical payload") {
		t.Fatalf("root accepted worker-forged authorization: %v", err)
	}
}

func TestRootRejectsExpiredAndActionConfusedAuthorization(t *testing.T) {
	f := newFixture(t)
	stageFixture(t, f)
	rollbackAuthorization := signedFixtureAuthorization(t, f.authorityPrivate, f.authorization.Authorization.AuthorityKeyID, f.assignment, f.release, agentupdateauth.AssignmentActionRollback, 1)
	if err := RequestActivation(f.paths, rollbackAuthorization, f.release, "v1.5.0"); err == nil || !strings.Contains(err.Error(), "different action") {
		t.Fatalf("activation accepted rollback authorization: %v", err)
	}
	requestFixtureActivation(t, f)
	data, err := readRegularNoFollow(f.paths.readyPath(), 128<<10)
	if err != nil {
		t.Fatal(err)
	}
	var ready readyRecord
	if err := strictJSON(data, &ready); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	ready.Authorization.Authorization.IssuedAtUnixMillis = now.Add(-2 * time.Hour).UnixMilli()
	ready.Authorization.Authorization.ExpiresAtUnixMillis = now.Add(-time.Hour).UnixMilli()
	ready.Authorization.CanonicalPayload, err = agentupdateauth.AssignmentAuthorizationPayload(ready.Authorization.Authorization)
	if err != nil {
		t.Fatal(err)
	}
	ready.Authorization.Signature, err = agentupdateauth.SignAssignmentAuthorization(f.authorityPrivate, ready.Authorization.Authorization)
	if err != nil {
		t.Fatal(err)
	}
	if err := atomicJSON(f.paths.readyPath(), ready, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Activate(context.Background(), ActivateOptions{
		Paths: f.paths, Verifier: f.verifier, Service: &fakeService{}, DiskPreflight: allowDisk,
	}); err == nil || !strings.Contains(err.Error(), "not currently valid") {
		t.Fatalf("root accepted expired management authorization: %v", err)
	}
}

func TestStageRejectsWrongArtifactAndLeavesNoReadyEdge(t *testing.T) {
	f := newFixture(t)
	f.source.body = append([]byte(nil), f.body...)
	f.source.body[0] ^= 0xff
	_, err := Stage(context.Background(), StageOptions{
		Paths: f.paths, Source: f.source, Verifier: f.verifier,
		Policy: VerifyPolicy{CurrentVersion: "v1.0.0", ServerVersion: "v1.5.0"}, DiskPreflight: allowDisk,
	})
	if err == nil || !strings.Contains(err.Error(), "SHA-256") {
		t.Fatalf("error = %v, want digest rejection", err)
	}
	if _, err := os.Stat(f.paths.readyPath()); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("ready marker exists after failed download: %v", err)
	}
}

func TestStageFailsDiskPreflightBeforeNetworkBody(t *testing.T) {
	f := newFixture(t)
	want := errors.New("disk full")
	_, err := Stage(context.Background(), StageOptions{
		Paths: f.paths, Source: f.source, Verifier: f.verifier,
		Policy:        VerifyPolicy{CurrentVersion: "v1.0.0", ServerVersion: "v1.5.0"},
		DiskPreflight: func(string, int64) error { return want },
	})
	if !errors.Is(err, want) || f.source.fetches.Load() != 0 {
		t.Fatalf("error/fetches = %v/%d", err, f.source.fetches.Load())
	}
}

func TestActivateReverifiesPromotesPersistsFloorAndCleansStage(t *testing.T) {
	f := newFixture(t)
	stageAndRequestActivation(t, f)
	service := &fakeService{}
	result, err := Activate(context.Background(), ActivateOptions{
		Paths: f.paths, Verifier: f.verifier, Service: service,
		Policy: VerifyPolicy{CurrentVersion: "v1.0.0"}, DiskPreflight: allowDisk,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Changed || result.Version != "v1.1.0" {
		t.Fatalf("activation result = %+v", result)
	}
	target, err := os.Readlink(f.paths.currentPath())
	if err != nil || target != "slots/v1.1.0/p2pstream" {
		t.Fatalf("current = %q, %v", target, err)
	}
	floor, err := loadFloor(f.paths.floorPath())
	if err != nil {
		t.Fatal(err)
	}
	if floor.Version != "v1.1.0" || floor.Sequence != 2 || floor.SecurityEpoch != 4 || floor.MinimumSafeVersion != "v1.0.1" {
		t.Fatalf("floor = %+v", floor)
	}
	receipt, err := LoadRootActionReceipt(f.paths)
	if err != nil {
		t.Fatal(err)
	}
	activation := receipt.Receipt
	if activation.AgentPublicID != f.assignment.AgentPublicID || activation.AssignmentID != f.assignment.AssignmentID ||
		activation.RootActionCounter != 1 || !ed25519.Verify(f.activatorPublic, receipt.CanonicalPayload, receipt.Signature) {
		t.Fatalf("invalid activation receipt: %+v", activation)
	}
	if _, err := os.Stat(f.paths.readyPath()); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("ready marker was not consumed")
	}
	service.mu.Lock()
	defer service.mu.Unlock()
	if service.restarts != 1 || service.healthCalls != 1 {
		t.Fatalf("service calls = restart %d health %d", service.restarts, service.healthCalls)
	}
}

func TestActivateHealthFailureRollsBackWithoutRaisingFloor(t *testing.T) {
	f := newFixture(t)
	stageAndRequestActivation(t, f)
	service := &fakeService{failHealth: true}
	_, err := Activate(context.Background(), ActivateOptions{
		Paths: f.paths, Verifier: f.verifier, Service: service,
		Policy: VerifyPolicy{CurrentVersion: "v1.0.0"}, DiskPreflight: allowDisk,
	})
	if err == nil || !strings.Contains(err.Error(), "rolled back") {
		t.Fatalf("error = %v", err)
	}
	target, _ := os.Readlink(f.paths.currentPath())
	if target != f.bootstrap.Target {
		t.Fatalf("current after rollback = %q", target)
	}
	floor, err := loadFloor(f.paths.floorPath())
	if err != nil || floor.Version != "v1.0.0" || floor.Sequence != 1 {
		t.Fatalf("floor after rollback = %+v, %v", floor, err)
	}
	if _, err := os.Stat(f.paths.readyPath()); err != nil {
		t.Fatal("failed candidate should remain staged for diagnosis/retry")
	}
}

func TestActivateRecoversSwitchedJournalBeforeNewWork(t *testing.T) {
	f := newFixture(t)
	bootstrapSlot(t, f.paths, "v1.1.0", f.body)
	journal := activationJournal{
		Phase: journalSwitched, PreviousTarget: f.bootstrap.Target, PreviousSlot: f.bootstrap,
		CandidateTarget: "slots/v1.1.0/p2pstream", Version: "v1.1.0", Sequence: 2,
		SecurityEpoch: 4, MinimumSafeVersion: "v1.0.1",
	}
	if err := writeJournal(f.paths, journal); err != nil {
		t.Fatal(err)
	}
	service := &fakeService{}
	result, err := Activate(context.Background(), ActivateOptions{
		Paths: f.paths, Verifier: f.verifier, Service: service, DiskPreflight: allowDisk,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Changed {
		t.Fatalf("recovery result = %+v", result)
	}
	target, _ := os.Readlink(f.paths.currentPath())
	if target != f.bootstrap.Target {
		t.Fatalf("current after crash recovery = %q", target)
	}
	if _, err := os.Stat(f.paths.journalPath()); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("journal was not cleared after recovery")
	}
}

func TestActivateCompletesHealthyJournalCrash(t *testing.T) {
	f := newFixture(t)
	bootstrapSlot(t, f.paths, "v1.1.0", f.body)
	journal := activationJournal{
		Phase: journalHealthy, PreviousTarget: f.bootstrap.Target, PreviousSlot: f.bootstrap, Authorization: f.authorization,
		CandidateTarget: "slots/v1.1.0/p2pstream", Version: "v1.1.0", Sequence: 2,
		SecurityEpoch: 4, MinimumSafeVersion: "v1.0.1",
	}
	authorizationDigest, err := agentupdateauth.AssignmentAuthorizationDigest(f.authorization.Authorization)
	if err != nil {
		t.Fatal(err)
	}
	journal.AuthorizationSHA = fmt.Sprintf("%x", authorizationDigest)
	receipt, err := createRootActionReceipt(f.paths, f.authorization, journal.AuthorizationSHA, releaseSlotMetadata(f.release))
	if err != nil {
		t.Fatal(err)
	}
	journal.Receipt = &receipt
	if err := writeJournal(f.paths, journal); err != nil {
		t.Fatal(err)
	}
	result, err := Activate(context.Background(), ActivateOptions{
		Paths: f.paths, Verifier: f.verifier, Service: &fakeService{}, DiskPreflight: allowDisk,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Version != "v1.1.0" || result.Sequence != 2 {
		t.Fatalf("result = %+v", result)
	}
	floor, _ := loadFloor(f.paths.floorPath())
	if floor.Version != "v1.1.0" {
		t.Fatalf("floor = %+v", floor)
	}
}

func TestHealthyJournalRecoveryCannotConsumeNewerActivationCommand(t *testing.T) {
	f := newFixture(t)
	stageFixture(t, f)
	newAssignment := Assignment{AgentPublicID: f.assignment.AgentPublicID, AssignmentID: 301, Generation: 9, Nonce: bytes.Repeat([]byte{0x74}, 32)}
	newAuthorization := signedFixtureAuthorization(t, f.authorityPrivate, f.authorization.Authorization.AuthorityKeyID, newAssignment, f.release, agentupdateauth.AssignmentActionActivate, 2)
	if err := RequestActivation(f.paths, newAuthorization, f.release, "v1.5.0"); err != nil {
		t.Fatal(err)
	}
	readyBefore, err := readRegularNoFollow(f.paths.readyPath(), 64<<10)
	if err != nil {
		t.Fatal(err)
	}

	journal := activationJournal{
		Phase: journalHealthy, PreviousTarget: f.bootstrap.Target, PreviousSlot: f.bootstrap, Authorization: f.authorization,
		CandidateTarget: "slots/v1.1.0/p2pstream", Version: "v1.1.0", Sequence: 2,
		SecurityEpoch: 4, MinimumSafeVersion: "v1.0.1",
	}
	authorizationDigest, err := agentupdateauth.AssignmentAuthorizationDigest(f.authorization.Authorization)
	if err != nil {
		t.Fatal(err)
	}
	journal.AuthorizationSHA = fmt.Sprintf("%x", authorizationDigest)
	receipt, err := createRootActionReceipt(f.paths, f.authorization, journal.AuthorizationSHA, releaseSlotMetadata(f.release))
	if err != nil {
		t.Fatal(err)
	}
	journal.Receipt = &receipt
	if err := writeJournal(f.paths, journal); err != nil {
		t.Fatal(err)
	}
	if err := recoverActivation(context.Background(), ActivateOptions{Paths: f.paths, Verifier: f.verifier, Service: &fakeService{}, DiskPreflight: allowDisk}); err != nil {
		t.Fatal(err)
	}
	readyAfter, err := readRegularNoFollow(f.paths.readyPath(), 64<<10)
	if err != nil {
		t.Fatalf("stale healthy journal removed newer command: %v", err)
	}
	if !bytes.Equal(readyBefore, readyAfter) {
		t.Fatal("stale healthy journal rewrote newer activation command")
	}
	if _, err := os.Stat(filepath.Join(f.paths.candidateDir(), "artifact.bin")); err != nil {
		t.Fatalf("stale healthy journal removed newer candidate: %v", err)
	}
}

func TestActivateRejectsSymlinkedWorkerArtifact(t *testing.T) {
	f := newFixture(t)
	stageAndRequestActivation(t, f)
	artifact := filepath.Join(f.paths.candidateDir(), "artifact.bin")
	if err := os.Remove(artifact); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/proc/self/exe", artifact); err != nil {
		t.Fatal(err)
	}
	_, err := Activate(context.Background(), ActivateOptions{
		Paths: f.paths, Verifier: f.verifier, Service: &fakeService{},
		Policy: VerifyPolicy{CurrentVersion: "v1.0.0"}, DiskPreflight: allowDisk,
	})
	if err == nil {
		t.Fatal("symlinked artifact was accepted")
	}
}

func TestActivationLockRejectsConcurrentActivator(t *testing.T) {
	f := newFixture(t)
	stageAndRequestActivation(t, f)
	block := make(chan struct{})
	service := &fakeService{healthBlock: block}
	firstDone := make(chan error, 1)
	go func() {
		_, err := Activate(context.Background(), ActivateOptions{
			Paths: f.paths, Verifier: f.verifier, Service: service,
			Policy: VerifyPolicy{CurrentVersion: "v1.0.0"}, DiskPreflight: allowDisk,
		})
		firstDone <- err
	}()
	deadline := time.Now().Add(2 * time.Second)
	for {
		service.mu.Lock()
		started := service.healthCalls > 0
		service.mu.Unlock()
		if started {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("first activation did not enter health check")
		}
		time.Sleep(time.Millisecond)
	}
	_, err := Activate(context.Background(), ActivateOptions{
		Paths: f.paths, Verifier: f.verifier, Service: &fakeService{},
		Policy: VerifyPolicy{CurrentVersion: "v1.0.0"}, DiskPreflight: allowDisk,
	})
	if err == nil || !strings.Contains(err.Error(), "another activation") {
		t.Fatalf("concurrent activation error = %v", err)
	}
	close(block)
	if err := <-firstDone; err != nil {
		t.Fatal(err)
	}
}

func TestReleaseRejectsTraversalAndWrongPlatformName(t *testing.T) {
	f := newFixture(t)
	for _, name := range []string{"../p2pstream", "p2pstream_v1.1.0_linux_arm32", "p2pstream_v1.1.0_linux_amd64.tar.gz"} {
		t.Run(fmt.Sprintf("%x", sha256.Sum256([]byte(name)))[:8], func(t *testing.T) {
			release := f.release
			release.Artifact.Name = name
			if err := validateRelease(release); err == nil {
				t.Fatalf("artifact name %q accepted", name)
			}
		})
	}
}

func TestActivationCounterAdvancesExactlyAcrossSuccessiveUpdates(t *testing.T) {
	f := newFixture(t)
	stageAndRequestActivation(t, f)
	if _, err := Activate(context.Background(), ActivateOptions{
		Paths: f.paths, Verifier: f.verifier, Service: &fakeService{},
		Policy: VerifyPolicy{CurrentVersion: "v1.0.0"}, DiskPreflight: allowDisk,
	}); err != nil {
		t.Fatal(err)
	}

	body := []byte("second signed executable\n")
	digest := sha256.Sum256(body)
	f.release = VerifiedRelease{
		Version: "v1.2.0", Commit: strings.Repeat("c", 40),
		ManifestSHA256: strings.Repeat("d", 64), Sequence: 3, SecurityEpoch: 4,
		MinimumSafeVersion: "v1.0.1",
		Artifact:           Artifact{Name: runtimeArtifactName("v1.2.0"), Size: int64(len(body)), SHA256: digest},
	}
	f.source = &fakeSource{manifest: []byte("manifest"), body: body, artifact: f.release.Artifact}
	f.verifier = &fakeVerifier{release: f.release}
	f.assignment = Assignment{AgentPublicID: "agent-public-a", AssignmentID: 42, Generation: 8, Nonce: bytes.Repeat([]byte{0x43}, 32)}
	f.authorization = signedFixtureAuthorization(t, f.authorityPrivate, f.authorization.Authorization.AuthorityKeyID, f.assignment, f.release, agentupdateauth.AssignmentActionActivate, 2)
	stageAndRequestActivation(t, f)
	if _, err := Activate(context.Background(), ActivateOptions{
		Paths: f.paths, Verifier: f.verifier, Service: &fakeService{},
		Policy: VerifyPolicy{CurrentVersion: "v1.1.0"}, DiskPreflight: allowDisk,
	}); err != nil {
		t.Fatal(err)
	}
	receipt, err := LoadRootActionReceipt(f.paths)
	if err != nil {
		t.Fatal(err)
	}
	activation := receipt.Receipt
	if activation.RootActionCounter != 2 || activation.AssignmentID != 42 ||
		!ed25519.Verify(f.activatorPublic, receipt.CanonicalPayload, receipt.Signature) {
		t.Fatalf("second activation receipt = %+v", activation)
	}
	counter, err := loadRootActionCounter(f.paths.rootActionCounterPath())
	if err != nil || counter != 2 {
		t.Fatalf("persisted activation counter = %d, %v", counter, err)
	}
}

func TestRootRejectsReplayedManagementCommandSequence(t *testing.T) {
	f := newFixture(t)
	stageAndRequestActivation(t, f)
	if _, err := Activate(context.Background(), ActivateOptions{
		Paths: f.paths, Verifier: f.verifier, Service: &fakeService{}, DiskPreflight: allowDisk,
	}); err != nil {
		t.Fatal(err)
	}
	body := []byte("second signed executable\n")
	digest := sha256.Sum256(body)
	f.release = VerifiedRelease{
		Version: "v1.2.0", Commit: strings.Repeat("c", 40), ManifestSHA256: strings.Repeat("d", 64),
		Sequence: 3, SecurityEpoch: 4, MinimumSafeVersion: "v1.0.1",
		Artifact: Artifact{Name: runtimeArtifactName("v1.2.0"), Size: int64(len(body)), SHA256: digest},
	}
	f.source = &fakeSource{manifest: []byte("manifest"), body: body, artifact: f.release.Artifact}
	f.verifier = &fakeVerifier{release: f.release}
	f.assignment = Assignment{AgentPublicID: "agent-public-a", AssignmentID: 42, Generation: 8, Nonce: bytes.Repeat([]byte{0x43}, 32)}
	f.authorization = signedFixtureAuthorization(t, f.authorityPrivate, f.authorization.Authorization.AuthorityKeyID, f.assignment, f.release, agentupdateauth.AssignmentActionActivate, 1)
	stageAndRequestActivation(t, f)
	if _, err := Activate(context.Background(), ActivateOptions{
		Paths: f.paths, Verifier: f.verifier, Service: &fakeService{}, DiskPreflight: allowDisk,
	}); err == nil || !strings.Contains(err.Error(), "replayed") {
		t.Fatalf("root accepted replayed management command sequence: %v", err)
	}
}

func TestExplicitRollbackUsesPersistedPreviousSignedSlot(t *testing.T) {
	f := newFixture(t)
	stageAndRequestActivation(t, f)
	if _, err := Activate(context.Background(), ActivateOptions{
		Paths: f.paths, Verifier: f.verifier, Service: &fakeService{},
		Policy: VerifyPolicy{CurrentVersion: "v1.0.0"}, DiskPreflight: allowDisk,
	}); err != nil {
		t.Fatal(err)
	}
	floor, err := loadFloor(f.paths.floorPath())
	if err != nil {
		t.Fatal(err)
	}
	floor.MinimumSafeVersion = "v1.0.0"
	if err := atomicJSON(f.paths.floorPath(), floor, 0640); err != nil {
		t.Fatal(err)
	}
	rollbackAssignment := Assignment{AgentPublicID: f.assignment.AgentPublicID, AssignmentID: 99, Generation: 8, Nonce: bytes.Repeat([]byte{0x55}, 32)}
	rollbackAuthorization := signedFixtureAuthorization(t, f.authorityPrivate, f.authorization.Authorization.AuthorityKeyID, rollbackAssignment, f.release, agentupdateauth.AssignmentActionRollback, 2)
	if err := RequestRollback(f.paths, rollbackAuthorization); err != nil {
		t.Fatal(err)
	}
	service := &fakeService{}
	if err := Rollback(context.Background(), f.paths, service); err != nil {
		t.Fatal(err)
	}
	target, err := os.Readlink(f.paths.currentPath())
	if err != nil || target != f.bootstrap.Target {
		t.Fatalf("rollback current target = %q, %v", target, err)
	}
	data, err := readRegularNoFollow(f.paths.rollbackResultPath(), 64<<10)
	if err != nil {
		t.Fatal(err)
	}
	var result rollbackRecord
	if err := strictJSON(data, &result); err != nil || result.Authorization.Authorization.AssignmentID != rollbackAssignment.AssignmentID || result.Receipt.Receipt.ResultKind != agentupdateauth.RootActionResultBootstrap {
		t.Fatalf("rollback result = %+v, %v", result, err)
	}
	// Security floors remain monotonic even though the running slot rolled back.
	floor, err = loadFloor(f.paths.floorPath())
	if err != nil || floor.Version != "v1.1.0" || floor.Sequence != 2 {
		t.Fatalf("rollback lowered security floor: %+v, %v", floor, err)
	}
}

func TestActivationFailureCannotDeleteConcurrentlyPublishedRollback(t *testing.T) {
	f := newFixture(t)
	stageAndRequestActivation(t, f)
	entered := make(chan struct{}, 1)
	releaseHealth := make(chan struct{})
	service := &fakeService{failHealth: true, healthBlock: releaseHealth, healthEntered: entered}
	if claimed, err := claimRootActionCommand(f.paths.readyPath(), f.paths.activationClaimPath()); err != nil || !claimed {
		t.Fatalf("claim activation command = %t, %v", claimed, err)
	}
	result := make(chan error, 1)
	go func() {
		_, err := Activate(context.Background(), ActivateOptions{Paths: f.paths, ReadyPath: f.paths.activationClaimPath(), Verifier: f.verifier, Service: service, DiskPreflight: allowDisk})
		if err != nil {
			err = errors.Join(err, QuarantineActivationFailure(f.paths, err, agentupdateauth.AssignmentActionActivate, f.paths.activationClaimPath()))
		}
		result <- err
	}()
	select {
	case <-entered:
	case err := <-result:
		t.Fatalf("activation failed before candidate health check: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("activation did not enter candidate health check")
	}
	rollbackAssignment := Assignment{AgentPublicID: f.assignment.AgentPublicID, AssignmentID: 199, Generation: 9, Nonce: bytes.Repeat([]byte{0x71}, 32)}
	rollbackAuthorization := signedFixtureAuthorization(t, f.authorityPrivate, f.authorization.Authorization.AuthorityKeyID, rollbackAssignment, f.release, agentupdateauth.AssignmentActionRollback, 2)
	if err := RequestRollback(f.paths, rollbackAuthorization); err != nil {
		t.Fatal(err)
	}
	close(releaseHealth)
	if err := <-result; err == nil || !strings.Contains(err.Error(), "candidate failed health check") {
		t.Fatalf("activation failure = %v", err)
	}
	if _, err := os.Stat(f.paths.rollbackPath()); err != nil {
		t.Fatalf("concurrently published rollback was consumed by activation cleanup: %v", err)
	}
	if claimed, err := claimRootActionCommand(f.paths.rollbackPath(), f.paths.rollbackClaimPath()); err != nil || !claimed {
		t.Fatalf("claim preserved rollback = %t, %v", claimed, err)
	}
	if err := rollbackFromPath(context.Background(), f.paths, &fakeService{}, f.paths.rollbackClaimPath()); err != nil {
		t.Fatalf("preserved rollback did not execute on the next activator run: %v", err)
	}
	target, err := os.Readlink(f.paths.currentPath())
	if err != nil || target != f.bootstrap.Target {
		t.Fatalf("preserved rollback target = %q, %v", target, err)
	}
}

func TestRollbackFailureRestartsRecoveredCurrentSlot(t *testing.T) {
	f := newFixture(t)
	stageAndRequestActivation(t, f)
	if _, err := Activate(context.Background(), ActivateOptions{Paths: f.paths, Verifier: f.verifier, Service: &fakeService{}, DiskPreflight: allowDisk}); err != nil {
		t.Fatal(err)
	}
	rollbackAssignment := Assignment{AgentPublicID: f.assignment.AgentPublicID, AssignmentID: 200, Generation: 9, Nonce: bytes.Repeat([]byte{0x72}, 32)}
	rollbackAuthorization := signedFixtureAuthorization(t, f.authorityPrivate, f.authorization.Authorization.AuthorityKeyID, rollbackAssignment, f.release, agentupdateauth.AssignmentActionRollback, 2)
	if err := RequestRollback(f.paths, rollbackAuthorization); err != nil {
		t.Fatal(err)
	}
	service := &fakeService{failHealth: true}
	if err := Rollback(context.Background(), f.paths, service); err == nil || !strings.Contains(err.Error(), "current agent recovered") {
		t.Fatalf("rollback destination failure = %v", err)
	}
	service.mu.Lock()
	restarts := service.restarts
	healthCalls := service.healthCalls
	service.mu.Unlock()
	if restarts != 2 || healthCalls != 2 {
		t.Fatalf("rollback recovery service calls = restarts:%d health:%d, want 2/2", restarts, healthCalls)
	}
	target, err := os.Readlink(f.paths.currentPath())
	if err != nil || target != "slots/v1.1.0/p2pstream" {
		t.Fatalf("failed rollback did not restore active source slot: %q, %v", target, err)
	}
	if _, err := os.Stat(f.paths.rollbackJournalPath()); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("recovered source left a stale rollback journal: %v", err)
	}
}

func TestRollbackDoubleFailurePreservesJournalForRecovery(t *testing.T) {
	f := newFixture(t)
	stageAndRequestActivation(t, f)
	if _, err := Activate(context.Background(), ActivateOptions{Paths: f.paths, Verifier: f.verifier, Service: &fakeService{}, DiskPreflight: allowDisk}); err != nil {
		t.Fatal(err)
	}
	rollbackAssignment := Assignment{AgentPublicID: f.assignment.AgentPublicID, AssignmentID: 201, Generation: 9, Nonce: bytes.Repeat([]byte{0x73}, 32)}
	rollbackAuthorization := signedFixtureAuthorization(t, f.authorityPrivate, f.authorization.Authorization.AuthorityKeyID, rollbackAssignment, f.release, agentupdateauth.AssignmentActionRollback, 2)
	if err := RequestRollback(f.paths, rollbackAuthorization); err != nil {
		t.Fatal(err)
	}
	if err := Rollback(context.Background(), f.paths, &alwaysFailService{}); err == nil || !strings.Contains(err.Error(), "restored current agent failed") {
		t.Fatalf("double rollback failure = %v", err)
	}
	if _, err := os.Stat(f.paths.rollbackJournalPath()); err != nil {
		t.Fatalf("double failure discarded recovery journal: %v", err)
	}
	if err := rollbackFromPath(context.Background(), f.paths, &fakeService{}, f.paths.rollbackPath()); err != nil {
		t.Fatalf("durable rollback journal did not recover: %v", err)
	}
	if _, err := os.Stat(f.paths.rollbackJournalPath()); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("successful recovery retained rollback journal: %v", err)
	}
}

func TestRecoverableRootJournalDoesNotPublishPrematureFailure(t *testing.T) {
	f := newFixture(t)
	rollbackAssignment := Assignment{AgentPublicID: f.assignment.AgentPublicID, AssignmentID: 202, Generation: 9, Nonce: bytes.Repeat([]byte{0x75}, 32)}
	rollbackAuthorization := signedFixtureAuthorization(t, f.authorityPrivate, f.authorization.Authorization.AuthorityKeyID, rollbackAssignment, f.release, agentupdateauth.AssignmentActionRollback, 2)
	digest, err := agentupdateauth.AssignmentAuthorizationDigest(rollbackAuthorization.Authorization)
	if err != nil {
		t.Fatal(err)
	}
	journal := rollbackJournal{
		Phase: rollbackSwitched, Authorization: rollbackAuthorization, AuthorizationSHA: fmt.Sprintf("%x", digest),
		FromSlot: f.bootstrap, ToSlot: f.bootstrap,
	}
	// The exact journal contents are validated by Rollback; quarantine only
	// needs the protected durable edge to avoid racing recovery with FAILED.
	if err := writeRollbackJournal(f.paths, journal); err != nil {
		t.Fatal(err)
	}
	if err := atomicJSON(f.paths.rollbackClaimPath(), rollbackRequest{Authorization: rollbackAuthorization}, 0600); err != nil {
		t.Fatal(err)
	}
	if err := QuarantineActivationFailure(f.paths, errors.New("transient double failure"), agentupdateauth.AssignmentActionRollback, f.paths.rollbackClaimPath()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(f.paths.failurePath()); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("recoverable journal published a premature worker failure: %v", err)
	}
	if _, err := os.Stat(f.paths.rollbackJournalPath()); err != nil {
		t.Fatalf("quarantine discarded recoverable journal: %v", err)
	}
	if _, err := os.Stat(f.paths.rollbackClaimPath()); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("failed invocation claim remained armed: %v", err)
	}
}

func TestRollbackAuthorizationAfterEscapedActivationIsSafeBeforeActivation(t *testing.T) {
	f := newFixture(t)
	stageAndRequestActivation(t, f) // ACTIVATE authorization escaped, but root has not consumed it.
	rollbackAssignment := Assignment{AgentPublicID: f.assignment.AgentPublicID, AssignmentID: 102, Generation: 9, Nonce: bytes.Repeat([]byte{0x78}, 32)}
	rollbackAuthorization := signedFixtureAuthorization(t, f.authorityPrivate, f.authorization.Authorization.AuthorityKeyID, rollbackAssignment, f.release, agentupdateauth.AssignmentActionRollback, 2)
	if err := RequestRollback(f.paths, rollbackAuthorization); err != nil {
		t.Fatal(err)
	}
	if err := Rollback(context.Background(), f.paths, &fakeService{}); err != nil {
		t.Fatal(err)
	}
	target, err := os.Readlink(f.paths.currentPath())
	if err != nil || target != f.bootstrap.Target {
		t.Fatalf("pre-activation rollback changed bootstrap slot: %q, %v", target, err)
	}
	if _, err := os.Stat(f.paths.readyPath()); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("successful signed rollback left the escaped activation edge armed")
	}
	data, err := readRegularNoFollow(f.paths.rollbackResultPath(), 128<<10)
	if err != nil {
		t.Fatal(err)
	}
	var result rollbackRecord
	if err := strictJSON(data, &result); err != nil || result.Receipt.Receipt.ResultKind != agentupdateauth.RootActionResultBootstrap || result.Receipt.Receipt.CommandSequence != 2 {
		t.Fatalf("pre-activation rollback receipt = %+v, %v", result, err)
	}
}

func TestRollbackRecoversCompletedRootReceiptExactlyOnce(t *testing.T) {
	f := newFixture(t)
	stageAndRequestActivation(t, f)
	if _, err := Activate(context.Background(), ActivateOptions{
		Paths: f.paths, Verifier: f.verifier, Service: &fakeService{}, DiskPreflight: allowDisk,
	}); err != nil {
		t.Fatal(err)
	}
	rollbackAssignment := Assignment{AgentPublicID: f.assignment.AgentPublicID, AssignmentID: 101, Generation: 9, Nonce: bytes.Repeat([]byte{0x77}, 32)}
	authorization := signedFixtureAuthorization(t, f.authorityPrivate, f.authorization.Authorization.AuthorityKeyID, rollbackAssignment, f.release, agentupdateauth.AssignmentActionRollback, 2)
	if err := RequestRollback(f.paths, authorization); err != nil {
		t.Fatal(err)
	}
	digest, err := agentupdateauth.AssignmentAuthorizationDigest(authorization.Authorization)
	if err != nil {
		t.Fatal(err)
	}
	authorizationSHA := fmt.Sprintf("%x", digest)
	if _, err := consumeAssignmentAuthorization(f.paths, authorization, agentupdateauth.AssignmentActionRollback, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if err := switchCurrent(f.paths, f.bootstrap.Target); err != nil {
		t.Fatal(err)
	}
	receipt, err := createRootActionReceipt(f.paths, authorization, authorizationSHA, f.bootstrap)
	if err != nil {
		t.Fatal(err)
	}
	journal := rollbackJournal{Phase: rollbackCompleted, Authorization: authorization, AuthorizationSHA: authorizationSHA,
		FromSlot: releaseSlotMetadata(f.release), ToSlot: f.bootstrap, Receipt: &receipt}
	if err := writeRollbackJournal(f.paths, journal); err != nil {
		t.Fatal(err)
	}
	if err := Rollback(context.Background(), f.paths, &fakeService{}); err != nil {
		t.Fatal(err)
	}
	counter, err := loadRootActionCounter(f.paths.rootActionCounterPath())
	if err != nil || counter != 2 {
		t.Fatalf("recovered root action counter = %d, %v", counter, err)
	}
	if _, err := os.Stat(f.paths.rollbackJournalPath()); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("completed rollback journal was not cleared")
	}
	data, err := readRegularNoFollow(f.paths.rollbackResultPath(), 128<<10)
	if err != nil {
		t.Fatal(err)
	}
	var result rollbackRecord
	if err := strictJSON(data, &result); err != nil || result.Receipt.Receipt.RootActionCounter != 2 {
		t.Fatalf("recovered rollback result = %+v, %v", result, err)
	}
}

func TestRollbackRejectsForgeryAndMakesStaleTargetANoOp(t *testing.T) {
	f := newFixture(t)
	stageAndRequestActivation(t, f)
	if _, err := Activate(context.Background(), ActivateOptions{
		Paths: f.paths, Verifier: f.verifier, Service: &fakeService{},
		Policy: VerifyPolicy{CurrentVersion: "v1.0.0"}, DiskPreflight: allowDisk,
	}); err != nil {
		t.Fatal(err)
	}
	rollbackAssignment := Assignment{AgentPublicID: f.assignment.AgentPublicID, AssignmentID: 100, Generation: 9, Nonce: bytes.Repeat([]byte{0x66}, 32)}
	forged := signedFixtureAuthorization(t, f.authorityPrivate, f.authorization.Authorization.AuthorityKeyID, rollbackAssignment, f.release, agentupdateauth.AssignmentActionRollback, 2)
	forged.Signature[0] ^= 0xff
	if err := RequestRollback(f.paths, forged); err == nil || !strings.Contains(err.Error(), "signature") {
		t.Fatalf("forged rollback authorization accepted: %v", err)
	}
	wrongRelease := f.release
	wrongRelease.Version = "v1.2.0"
	wrongRelease.Artifact.Name = runtimeArtifactName(wrongRelease.Version)
	confused := signedFixtureAuthorization(t, f.authorityPrivate, f.authorization.Authorization.AuthorityKeyID, rollbackAssignment, wrongRelease, agentupdateauth.AssignmentActionRollback, 2)
	if err := RequestRollback(f.paths, confused); err != nil {
		t.Fatal(err)
	}
	if err := Rollback(context.Background(), f.paths, &fakeService{}); err != nil {
		t.Fatalf("stale signed rollback should attest the unchanged current slot: %v", err)
	}
	target, err := os.Readlink(f.paths.currentPath())
	if err != nil || target != "slots/v1.1.0/p2pstream" {
		t.Fatalf("no-op rollback changed current target: %q, %v", target, err)
	}
	data, err := readRegularNoFollow(f.paths.rollbackResultPath(), 128<<10)
	if err != nil {
		t.Fatal(err)
	}
	var result rollbackRecord
	if err := strictJSON(data, &result); err != nil || result.Receipt.Receipt.ResultVersion != "v1.1.0" {
		t.Fatalf("no-op rollback receipt = %+v, %v", result, err)
	}
}
