package updater

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"os"
	"testing"

	"connectrpc.com/connect"

	p2pstreamv1 "p2pstream/gen/proto/p2pstream/v1"
	"p2pstream/internal/agentupdateauth"
)

func TestWorkerReportsRollbackReceiptResultsAndResumesPolling(t *testing.T) {
	f := newFixture(t)
	stageAndRequestActivation(t, f)
	assignment := f.assignment
	assignment.Generation++
	authorization := signedFixtureAuthorization(t, f.authorityPrivate, f.authorization.Authorization.AuthorityKeyID, assignment, f.release, agentupdateauth.AssignmentActionRollback, 2)
	if err := RequestRollback(f.paths, authorization); err != nil {
		t.Fatal(err)
	}
	if err := Rollback(context.Background(), f.paths, &fakeService{}); err != nil {
		t.Fatal(err)
	}
	durable, err := os.ReadFile(f.paths.rollbackResultPath())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(f.paths.workerStateDir(), 0700); err != nil {
		t.Fatal(err)
	}
	public, private, _ := ed25519.GenerateKey(rand.Reader)
	writeTestPrivateKey(t, f.paths.workerPrivateKeyPath(), private)
	api := &fakeControlAPI{t: t, updaterPublic: public, failFirstReport: true}
	worker := Worker{
		Paths: f.paths, Config: HostConfig{AgentPublicID: assignment.AgentPublicID},
		Control: WorkerControl{Paths: f.paths, API: api},
	}
	if err := worker.Run(context.Background()); connect.CodeOf(err) != connect.CodeUnavailable {
		t.Fatalf("lost response = %v", err)
	}
	report := api.lastReport
	if report == nil || report.RootActionReceipt == nil || report.State != p2pstreamv1.AgentUpdaterReportState_AGENT_UPDATER_REPORT_STATE_ROLLED_BACK {
		t.Fatalf("rollback report = %+v", report)
	}
	receipt := report.RootActionReceipt
	if report.ManifestSha256 != receipt.ResultManifestSha256 || report.BinarySha256 != receipt.ResultArtifactSha256 ||
		report.RunningVersion != receipt.ResultVersion || report.RunningCommit != receipt.ResultCommit {
		t.Fatalf("rollback report results differ from signed receipt: report version=%q digest=%q; receipt version=%q digest=%q",
			report.RunningVersion, report.BinarySha256, receipt.ResultVersion, receipt.ResultArtifactSha256)
	}
	if data, err := os.ReadFile(f.paths.rollbackResultPath()); err != nil || !bytes.Equal(data, durable) {
		t.Fatalf("lost response did not preserve the exact durable result: %v", err)
	}
	if err := worker.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(f.paths.rollbackResultPath()); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("acknowledged rollback result was retained: %v", err)
	}
	if len(api.reportCounters) != 2 || api.reportCounters[1] <= api.reportCounters[0] ||
		len(api.rootReceiptPayloads) != 2 || !bytes.Equal(api.rootReceiptPayloads[0], api.rootReceiptPayloads[1]) {
		t.Fatalf("rollback retry changed the root proof or reused a worker counter: %v", api.reportCounters)
	}
	if err := worker.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(api.checkCounters) != 1 || api.checkCounters[0] <= api.reportCounters[1] {
		t.Fatalf("worker did not resume polling after receipt acknowledgement: %v", api.checkCounters)
	}
}
