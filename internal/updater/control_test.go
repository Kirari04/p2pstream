package updater

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/proto"

	p2pstreamv1 "p2pstream/gen/proto/p2pstream/v1"
	"p2pstream/internal/agentupdate"
	"p2pstream/internal/agentupdateauth"
)

type fakeControlAPI struct {
	t                    *testing.T
	updaterPublic        ed25519.PublicKey
	activatorPublic      ed25519.PublicKey
	enrollCalls          int
	checkCounters        []uint64
	reportCounters       []uint64
	failFirstEnroll      bool
	authorityPrivate     ed25519.PrivateKey
	receipt              *p2pstreamv1.AgentUpdaterEnrollmentReceipt
	mutateEnrollment     func(*p2pstreamv1.AgentUpdaterEnrollmentReceipt)
	lastReport           *p2pstreamv1.ReportAgentUpdateRequest
	failFirstReport      bool
	reportResponse       *p2pstreamv1.ReportAgentUpdateResponse
	rootReceiptPayloads  [][]byte
	expectedToken        string
	enrollmentGeneration uint64
	failNextCheck        bool
}

func TestControlTransportIgnoresEnvironmentProxy(t *testing.T) {
	t.Setenv("HTTPS_PROXY", "http://attacker.invalid:8080")
	t.Setenv("HTTP_PROXY", "http://attacker.invalid:8080")
	transport := newControlTransport(x509.NewCertPool())
	if transport.Proxy != nil {
		t.Fatal("control transport inherited an environment proxy")
	}
}

func (f *fakeControlAPI) EnrollAgentUpdater(_ context.Context, request *connect.Request[p2pstreamv1.EnrollAgentUpdaterRequest]) (*connect.Response[p2pstreamv1.EnrollAgentUpdaterResponse], error) {
	f.enrollCalls++
	expectedToken := f.expectedToken
	if expectedToken == "" {
		expectedToken = "single-use"
	}
	if request.Msg.Token != expectedToken || request.Msg.AgentPublicId != "agent-a" ||
		string(request.Msg.UpdaterPublicKey) != string(f.updaterPublic) || string(request.Msg.ActivatorPublicKey) != string(f.activatorPublic) ||
		request.Msg.Os != runtime.GOOS || request.Msg.Arch != runtime.GOARCH {
		f.t.Fatalf("enrollment request = %+v", request.Msg)
	}
	if f.receipt == nil {
		authorityKeyID, _ := agentupdateauth.KeyID(f.authorityPrivate.Public().(ed25519.PublicKey))
		now := time.Now().UTC()
		generation := f.enrollmentGeneration
		if generation == 0 {
			generation = 1
		}
		value := agentupdateauth.EnrollmentReceipt{
			AgentPublicID: request.Msg.AgentPublicId, UpdaterKeyID: keyID(f.updaterPublic), UpdaterPublicKeySHA256: keyID(f.updaterPublic),
			ActivatorKeyID: keyID(f.activatorPublic), ActivatorPublicKeySHA256: keyID(f.activatorPublic),
			OS: request.Msg.Os, Arch: request.Msg.Arch, UpdaterVersion: request.Msg.UpdaterVersion,
			PinnedRepository: "owner/repo", AuthorityKeyID: authorityKeyID, AuthorityEpoch: 1,
			EnrolledAtUnixMillis: now.UnixMilli(), ExpiresAtUnixMillis: now.Add(time.Hour).UnixMilli(), Generation: generation,
		}
		payload, err := agentupdateauth.EnrollmentReceiptPayload(value)
		if err != nil {
			f.t.Fatal(err)
		}
		signature, err := agentupdateauth.SignEnrollmentReceipt(f.authorityPrivate, value)
		if err != nil {
			f.t.Fatal(err)
		}
		f.receipt = enrollmentReceiptToProto(value, payload, signature)
	}
	if f.failFirstEnroll && f.enrollCalls == 1 {
		return nil, connect.NewError(connect.CodeUnavailable, errors.New("response lost after commit"))
	}
	receipt := proto.Clone(f.receipt).(*p2pstreamv1.AgentUpdaterEnrollmentReceipt)
	if f.mutateEnrollment != nil {
		f.mutateEnrollment(receipt)
	}
	return connect.NewResponse(&p2pstreamv1.EnrollAgentUpdaterResponse{
		UpdaterKeyId: keyID(f.updaterPublic), ActivatorKeyId: keyID(f.activatorPublic),
		EnrolledAtUnixMillis: receipt.EnrolledAtUnixMillis, Receipt: receipt,
	}), nil
}

func enrollmentReceiptToProto(value agentupdateauth.EnrollmentReceipt, payload, signature []byte) *p2pstreamv1.AgentUpdaterEnrollmentReceipt {
	return &p2pstreamv1.AgentUpdaterEnrollmentReceipt{
		AgentPublicId: value.AgentPublicID, UpdaterKeyId: value.UpdaterKeyID, UpdaterPublicKeySha256: value.UpdaterPublicKeySHA256,
		ActivatorKeyId: value.ActivatorKeyID, ActivatorPublicKeySha256: value.ActivatorPublicKeySHA256,
		Os: value.OS, Arch: value.Arch, UpdaterVersion: value.UpdaterVersion,
		PinnedRepository: value.PinnedRepository, AuthorityKeyId: value.AuthorityKeyID, AuthorityEpoch: value.AuthorityEpoch,
		EnrolledAtUnixMillis: value.EnrolledAtUnixMillis, ExpiresAtUnixMillis: value.ExpiresAtUnixMillis,
		Generation: value.Generation, CanonicalPayload: payload, Signature: signature,
	}
}

func prepareEnrollmentTrust(t *testing.T, paths Paths) ed25519.PrivateKey {
	t.Helper()
	authorityPublic, authorityPrivate, _ := ed25519.GenerateKey(rand.Reader)
	authorityKeyID, _ := agentupdateauth.KeyID(authorityPublic)
	if err := atomicJSON(paths.authorityPath(), pinnedManagementAuthority{KeyID: authorityKeyID, Epoch: 1, PublicKey: mustEncodePublicKey(t, authorityPublic)}, 0640); err != nil {
		t.Fatal(err)
	}
	return authorityPrivate
}

func TestEnrollmentRetriesSameIdentityAfterCommittedResponseLoss(t *testing.T) {
	root := t.TempDir()
	paths := Paths{
		ConfigPath: filepath.Join(root, "etc", "updater.json"),
		StateDir:   filepath.Join(root, "state"), InstallRoot: filepath.Join(root, "install"), CommandPath: filepath.Join(root, "bin", "p2pstream"),
	}
	for _, dir := range []string{filepath.Dir(paths.ConfigPath), paths.workerStateDir(), paths.rootStateDir()} {
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	updaterPublic, updaterPrivate, _ := ed25519.GenerateKey(rand.Reader)
	activatorPublic, activatorPrivate, _ := ed25519.GenerateKey(rand.Reader)
	writeTestPrivateKey(t, paths.workerPrivateKeyPath(), updaterPrivate)
	writeTestPrivateKey(t, paths.activatorPrivateKeyPath(), activatorPrivate)
	if err := os.WriteFile(paths.enrollmentTokenPath(), []byte("single-use\n"), 0640); err != nil {
		t.Fatal(err)
	}
	authorityPrivate := prepareEnrollmentTrust(t, paths)
	api := &fakeControlAPI{t: t, updaterPublic: updaterPublic, activatorPublic: activatorPublic, failFirstEnroll: true, authorityPrivate: authorityPrivate}
	control := WorkerControl{Paths: paths, API: api, UpdaterVersion: "v1.0.0"}
	config := HostConfig{Repository: "owner/repo", ManagementOrigin: "https://management.example", AgentPublicID: "agent-a", Channel: "stable"}
	if err := control.Enroll(context.Background(), config); err == nil {
		t.Fatal("committed enrollment response loss was not surfaced")
	}
	if _, err := os.Stat(paths.enrollmentReceiptPath()); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("host persisted an enrollment receipt without receiving the server receipt")
	}
	if err := control.Enroll(context.Background(), config); err != nil {
		t.Fatal(err)
	}
	if api.enrollCalls != 2 || len(api.checkCounters) != 1 || api.checkCounters[0] != 1 {
		t.Fatalf("enroll/check calls = %d/%v", api.enrollCalls, api.checkCounters)
	}
}

func TestEnrollmentRejectsForgedOrNonCanonicalReceipt(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*p2pstreamv1.AgentUpdaterEnrollmentReceipt)
	}{
		{name: "signature", mutate: func(receipt *p2pstreamv1.AgentUpdaterEnrollmentReceipt) { receipt.Signature[0] ^= 0xff }},
		{name: "canonical-payload", mutate: func(receipt *p2pstreamv1.AgentUpdaterEnrollmentReceipt) {
			receipt.CanonicalPayload = append(receipt.CanonicalPayload, 0)
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			paths := Paths{ConfigPath: filepath.Join(root, "etc", "updater.json"), StateDir: filepath.Join(root, "state"), InstallRoot: filepath.Join(root, "install"), CommandPath: filepath.Join(root, "bin", "p2pstream")}
			for _, dir := range []string{filepath.Dir(paths.ConfigPath), paths.workerStateDir(), paths.rootStateDir()} {
				if err := os.MkdirAll(dir, 0700); err != nil {
					t.Fatal(err)
				}
			}
			updaterPublic, updaterPrivate, _ := ed25519.GenerateKey(rand.Reader)
			activatorPublic, activatorPrivate, _ := ed25519.GenerateKey(rand.Reader)
			writeTestPrivateKey(t, paths.workerPrivateKeyPath(), updaterPrivate)
			writeTestPrivateKey(t, paths.activatorPrivateKeyPath(), activatorPrivate)
			if err := os.WriteFile(paths.enrollmentTokenPath(), []byte("single-use\n"), 0640); err != nil {
				t.Fatal(err)
			}
			authorityPrivate := prepareEnrollmentTrust(t, paths)
			api := &fakeControlAPI{t: t, updaterPublic: updaterPublic, activatorPublic: activatorPublic, authorityPrivate: authorityPrivate, mutateEnrollment: test.mutate}
			control := WorkerControl{Paths: paths, API: api, UpdaterVersion: "v1.0.0"}
			config := HostConfig{Repository: "owner/repo", ManagementOrigin: "https://management.example", AgentPublicID: "agent-a", Channel: "stable"}
			if err := control.Enroll(context.Background(), config); err == nil {
				t.Fatal("forged/non-canonical signed enrollment receipt was accepted")
			}
			if _, err := os.Stat(paths.enrollmentReceiptPath()); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("rejected enrollment receipt was persisted")
			}
		})
	}
}

func TestControlHTTPClientNeverFollowsManagementRedirects(t *testing.T) {
	destinationCalls := 0
	destination := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		destinationCalls++
		_, _ = io.Copy(io.Discard, r.Body)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer destination.Close()
	source := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Location", destination.URL)
		w.WriteHeader(http.StatusTemporaryRedirect)
	}))
	defer source.Close()
	client := newControlHTTPClient(source.Client().Transport)
	response, err := client.Post(source.URL, "application/proto", bytes.NewBufferString("single-use-enrollment-token"))
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusTemporaryRedirect || destinationCalls != 0 {
		t.Fatalf("status/destination calls = %d/%d", response.StatusCode, destinationCalls)
	}
}

func (f *fakeControlAPI) CheckAgentUpdate(_ context.Context, request *connect.Request[p2pstreamv1.CheckAgentUpdateRequest]) (*connect.Response[p2pstreamv1.CheckAgentUpdateResponse], error) {
	if !ed25519.Verify(f.updaterPublic, agentupdateauth.CheckPayload(request.Msg.AgentPublicId, request.Msg.Counter), request.Msg.Signature) {
		f.t.Fatal("invalid signed check")
	}
	f.checkCounters = append(f.checkCounters, request.Msg.Counter)
	if f.failNextCheck {
		f.failNextCheck = false
		return nil, connect.NewError(connect.CodeUnavailable, errors.New("check response lost"))
	}
	return connect.NewResponse(&p2pstreamv1.CheckAgentUpdateResponse{
		DesiredAction: p2pstreamv1.AgentUpdateDesiredAction_AGENT_UPDATE_DESIRED_ACTION_NONE,
	}), nil
}

func (f *fakeControlAPI) ReportAgentUpdate(_ context.Context, request *connect.Request[p2pstreamv1.ReportAgentUpdateRequest]) (*connect.Response[p2pstreamv1.ReportAgentUpdateResponse], error) {
	payload := agentupdateauth.ReportPayload(agentupdateauth.Report{
		AgentPublicID: request.Msg.AgentPublicId, Counter: request.Msg.Counter,
		AssignmentID: request.Msg.AssignmentId, Generation: request.Msg.Generation, State: int32(request.Msg.State),
		ManifestSHA256: request.Msg.ManifestSha256, BinarySHA256: request.Msg.BinarySha256,
		RunningVersion: request.Msg.RunningVersion, RunningCommit: request.Msg.RunningCommit,
		FailureCode: request.Msg.FailureCode, FailureDetail: request.Msg.FailureDetail,
		ActivationCounter: request.Msg.ActivationCounter, ActivationNonce: request.Msg.ActivationNonce,
		ActivatorSignature: request.Msg.ActivatorSignature,
	})
	if !ed25519.Verify(f.updaterPublic, payload, request.Msg.Signature) {
		f.t.Fatal("invalid signed report")
	}
	f.reportCounters = append(f.reportCounters, request.Msg.Counter)
	f.lastReport = request.Msg
	if request.Msg.RootActionReceipt != nil {
		f.rootReceiptPayloads = append(f.rootReceiptPayloads, append([]byte(nil), request.Msg.RootActionReceipt.CanonicalPayload...))
	}
	if f.failFirstReport && len(f.reportCounters) == 1 {
		return nil, connect.NewError(connect.CodeUnavailable, errors.New("report response lost after commit"))
	}
	if f.reportResponse != nil {
		return connect.NewResponse(f.reportResponse), nil
	}
	return connect.NewResponse(&p2pstreamv1.ReportAgentUpdateResponse{}), nil
}

func TestWorkerEnrollmentUsesSeparateKeysAndPersistsCounterBeforeRetry(t *testing.T) {
	root := t.TempDir()
	paths := Paths{
		ConfigPath: filepath.Join(root, "etc", "updater.json"),
		StateDir:   filepath.Join(root, "state"), InstallRoot: filepath.Join(root, "install"), CommandPath: filepath.Join(root, "bin", "p2pstream"),
	}
	for _, dir := range []string{filepath.Dir(paths.ConfigPath), paths.workerStateDir(), paths.rootStateDir()} {
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	updaterPublic, updaterPrivate, _ := ed25519.GenerateKey(rand.Reader)
	activatorPublic, activatorPrivate, _ := ed25519.GenerateKey(rand.Reader)
	writeTestPrivateKey(t, paths.workerPrivateKeyPath(), updaterPrivate)
	writeTestPrivateKey(t, paths.activatorPrivateKeyPath(), activatorPrivate)
	if err := os.WriteFile(paths.enrollmentTokenPath(), []byte("single-use\n"), 0640); err != nil {
		t.Fatal(err)
	}
	authorityPrivate := prepareEnrollmentTrust(t, paths)
	api := &fakeControlAPI{t: t, updaterPublic: updaterPublic, activatorPublic: activatorPublic, authorityPrivate: authorityPrivate}
	control := WorkerControl{Paths: paths, API: api, UpdaterVersion: "v1.0.0"}
	config := HostConfig{Repository: "owner/repo", ManagementOrigin: "https://management.example", AgentPublicID: "agent-a", Channel: "stable"}
	if err := control.Enroll(context.Background(), config); err != nil {
		t.Fatal(err)
	}
	if err := control.Enroll(context.Background(), config); err != nil {
		t.Fatal(err)
	}
	if api.enrollCalls != 1 || len(api.checkCounters) != 2 || api.checkCounters[0] != 1 || api.checkCounters[1] != 2 {
		t.Fatalf("enroll/check calls = %d/%v", api.enrollCalls, api.checkCounters)
	}
	stateData, err := os.ReadFile(paths.workerCounterPath())
	if err != nil {
		t.Fatal(err)
	}
	var state workerCounter
	if err := strictJSON(stateData, &state); err != nil || state.Counter != 2 {
		t.Fatalf("counter state = %+v, %v", state, err)
	}
}

func TestWorkerReenrollmentRequiresNewSignedGenerationAndRecoversAfterCheckFailure(t *testing.T) {
	root := t.TempDir()
	paths := Paths{
		ConfigPath: filepath.Join(root, "etc", "updater.json"),
		StateDir:   filepath.Join(root, "state"), InstallRoot: filepath.Join(root, "install"), CommandPath: filepath.Join(root, "bin", "p2pstream"),
	}
	for _, dir := range []string{filepath.Dir(paths.ConfigPath), paths.workerStateDir(), paths.rootStateDir()} {
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	updaterPublic, updaterPrivate, _ := ed25519.GenerateKey(rand.Reader)
	activatorPublic, activatorPrivate, _ := ed25519.GenerateKey(rand.Reader)
	writeTestPrivateKey(t, paths.workerPrivateKeyPath(), updaterPrivate)
	writeTestPrivateKey(t, paths.activatorPrivateKeyPath(), activatorPrivate)
	if err := os.WriteFile(paths.enrollmentTokenPath(), []byte("single-use\n"), 0640); err != nil {
		t.Fatal(err)
	}
	authorityPrivate := prepareEnrollmentTrust(t, paths)
	api := &fakeControlAPI{t: t, updaterPublic: updaterPublic, activatorPublic: activatorPublic, authorityPrivate: authorityPrivate}
	control := WorkerControl{Paths: paths, API: api, UpdaterVersion: "v1.0.0"}
	config := HostConfig{Repository: "owner/repo", ManagementOrigin: "https://management.example", AgentPublicID: "agent-a", Channel: "stable"}
	if err := control.Enroll(context.Background(), config); err != nil {
		t.Fatal(err)
	}
	firstData, err := readRegularNoFollow(paths.enrollmentReceiptPath(), 64<<10)
	if err != nil {
		t.Fatal(err)
	}
	var first enrollmentReceiptRecord
	if err := strictJSON(firstData, &first); err != nil || first.Receipt.Generation != 1 {
		t.Fatalf("first receipt = %+v err=%v", first, err)
	}

	if err := os.WriteFile(paths.enrollmentTokenPath(), []byte("replacement-use\n"), 0640); err != nil {
		t.Fatal(err)
	}
	api.expectedToken = "replacement-use"
	api.enrollmentGeneration = 2
	api.receipt = nil
	api.failNextCheck = true
	if err := control.Enroll(context.Background(), config); err == nil {
		t.Fatal("post-enrollment check failure was not surfaced")
	}
	secondData, err := readRegularNoFollow(paths.enrollmentReceiptPath(), 64<<10)
	if err != nil {
		t.Fatal(err)
	}
	var second enrollmentReceiptRecord
	if err := strictJSON(secondData, &second); err != nil {
		t.Fatal(err)
	}
	if second.Receipt.Generation != 2 || second.EnrollmentTokenSHA256 == first.EnrollmentTokenSHA256 {
		t.Fatalf("replacement receipt = %+v", second)
	}
	if err := control.Enroll(context.Background(), config); err != nil {
		t.Fatalf("idempotent recovery after signed enrollment = %v", err)
	}
	if api.enrollCalls != 2 {
		t.Fatalf("replacement token was redundantly redeemed %d times", api.enrollCalls)
	}

	api.receipt = nil
	api.enrollmentGeneration = 1
	if err := os.WriteFile(paths.enrollmentTokenPath(), []byte("third-use\n"), 0640); err != nil {
		t.Fatal(err)
	}
	api.expectedToken = "third-use"
	if err := control.Enroll(context.Background(), config); err == nil || !strings.Contains(err.Error(), "generation was replayed") {
		t.Fatalf("non-monotonic replacement receipt error = %v", err)
	}
}

func TestWorkerReportUsesNextReservedCounterAndCanonicalPayload(t *testing.T) {
	root := t.TempDir()
	paths := Paths{ConfigPath: filepath.Join(root, "etc", "updater.json"), StateDir: filepath.Join(root, "state"), InstallRoot: filepath.Join(root, "install"), CommandPath: filepath.Join(root, "bin", "p2pstream")}
	if err := os.MkdirAll(paths.workerStateDir(), 0700); err != nil {
		t.Fatal(err)
	}
	public, private, _ := ed25519.GenerateKey(rand.Reader)
	writeTestPrivateKey(t, paths.workerPrivateKeyPath(), private)
	api := &fakeControlAPI{t: t, updaterPublic: public}
	control := WorkerControl{Paths: paths, API: api}
	if _, err := control.Report(context.Background(), agentupdateauth.Report{
		AgentPublicID: "agent-a", AssignmentID: 3, Generation: 4,
		State:          int32(p2pstreamv1.AgentUpdaterReportState_AGENT_UPDATER_REPORT_STATE_STAGED),
		ManifestSHA256: "manifest", BinarySHA256: "binary",
	}); err != nil {
		t.Fatal(err)
	}
	if len(api.reportCounters) != 1 || api.reportCounters[0] != 1 {
		t.Fatalf("report counters = %v", api.reportCounters)
	}
}

func TestRootActionReportUsesSignedReceiptAndLeavesLegacyAttestationEmpty(t *testing.T) {
	f := newFixture(t)
	stageAndRequestActivation(t, f)
	if _, err := Activate(context.Background(), ActivateOptions{Paths: f.paths, Verifier: f.verifier, Service: &fakeService{}, DiskPreflight: allowDisk}); err != nil {
		t.Fatal(err)
	}
	receipt, err := LoadRootActionReceipt(f.paths)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(f.paths.workerStateDir(), 0700); err != nil {
		t.Fatal(err)
	}
	workerPublic, workerPrivate, _ := ed25519.GenerateKey(rand.Reader)
	writeTestPrivateKey(t, f.paths.workerPrivateKeyPath(), workerPrivate)
	api := &fakeControlAPI{t: t, updaterPublic: workerPublic}
	control := WorkerControl{Paths: f.paths, API: api}
	if _, err := control.ReportRootAction(context.Background(), agentupdateauth.Report{
		AgentPublicID: receipt.Receipt.AgentPublicID, AssignmentID: receipt.Receipt.AssignmentID,
		Generation: receipt.Receipt.Generation, State: int32(p2pstreamv1.AgentUpdaterReportState_AGENT_UPDATER_REPORT_STATE_ACTIVATED),
		ManifestSHA256: receipt.Receipt.ResultManifestSHA256, BinarySHA256: receipt.Receipt.ResultArtifactSHA256,
	}, receipt); err != nil {
		t.Fatal(err)
	}
	if api.lastReport == nil || api.lastReport.RootActionReceipt == nil {
		t.Fatal("root action report omitted the signed root receipt")
	}
	if api.lastReport.ActivationCounter != 0 || len(api.lastReport.ActivationNonce) != 0 || len(api.lastReport.ActivatorSignature) != 0 {
		t.Fatal("root action report populated deprecated legacy activation attestation fields")
	}
}

func TestRootActionReportRetriesExactReceiptWithNewWorkerCounter(t *testing.T) {
	f := newFixture(t)
	stageAndRequestActivation(t, f)
	if _, err := Activate(context.Background(), ActivateOptions{Paths: f.paths, Verifier: f.verifier, Service: &fakeService{}, DiskPreflight: allowDisk}); err != nil {
		t.Fatal(err)
	}
	receipt, err := LoadRootActionReceipt(f.paths)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(f.paths.workerStateDir(), 0700); err != nil {
		t.Fatal(err)
	}
	workerPublic, workerPrivate, _ := ed25519.GenerateKey(rand.Reader)
	writeTestPrivateKey(t, f.paths.workerPrivateKeyPath(), workerPrivate)
	api := &fakeControlAPI{t: t, updaterPublic: workerPublic, failFirstReport: true}
	control := WorkerControl{Paths: f.paths, API: api}
	report := agentupdateauth.Report{AgentPublicID: receipt.Receipt.AgentPublicID, AssignmentID: receipt.Receipt.AssignmentID,
		Generation: receipt.Receipt.Generation, State: int32(p2pstreamv1.AgentUpdaterReportState_AGENT_UPDATER_REPORT_STATE_ACTIVATED)}
	if _, err := control.ReportRootAction(context.Background(), report, receipt); err == nil {
		t.Fatal("committed report response loss was not surfaced")
	}
	if _, err := control.ReportRootAction(context.Background(), report, receipt); err != nil {
		t.Fatal(err)
	}
	if len(api.reportCounters) != 2 || api.reportCounters[0] != 1 || api.reportCounters[1] != 2 ||
		len(api.rootReceiptPayloads) != 2 || !bytes.Equal(api.rootReceiptPayloads[0], api.rootReceiptPayloads[1]) ||
		!bytes.Equal(api.rootReceiptPayloads[0], receipt.CanonicalPayload) {
		t.Fatalf("root receipt retry counters/payloads = %v/%d", api.reportCounters, len(api.rootReceiptPayloads))
	}
}

func writeTestPrivateKey(t *testing.T, path string, key ed25519.PrivateKey) {
	t.Helper()
	encoded, err := agentupdate.EncodePrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(encoded+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
}
