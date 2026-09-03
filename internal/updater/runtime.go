package updater

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	p2pstreamv1 "p2pstream/gen/proto/p2pstream/v1"
	"p2pstream/internal/agentupdateauth"
	"p2pstream/internal/buildinfo"
	"p2pstream/internal/tunnel"
)

type Worker struct {
	Paths   Paths
	Config  HostConfig
	Control WorkerControl
}

type activationReportState struct {
	ActivationCounter uint64 `json:"activation_counter"`
	ActivatedReported bool   `json:"activated_reported"`
}

func (p Paths) activationReportStatePath() string {
	return filepath.Join(p.workerStateDir(), "activation-report.json")
}

func NewWorker(paths Paths) (*Worker, error) {
	config, err := LoadHostConfig(paths.ConfigPath)
	if err != nil {
		return nil, err
	}
	if _, err := readRegularNoFollow(paths.enrolledPath(), 64<<10); err != nil {
		return nil, fmt.Errorf("updater enrollment is not finalized: %w", err)
	}
	api, err := NewControlAPI(config)
	if err != nil {
		return nil, err
	}
	return &Worker{
		Paths: paths, Config: config,
		Control: WorkerControl{Paths: paths, API: api, UpdaterVersion: buildinfo.Version},
	}, nil
}

func EnrollDefault(ctx context.Context, paths Paths) error {
	config, err := LoadHostConfig(paths.ConfigPath)
	if err != nil {
		return err
	}
	api, err := NewControlAPI(config)
	if err != nil {
		return err
	}
	control := WorkerControl{Paths: paths, API: api, UpdaterVersion: buildinfo.Version}
	return control.Enroll(ctx, config)
}

func (w *Worker) Run(ctx context.Context) error {
	if err := os.MkdirAll(w.Paths.workerStateDir(), 0700); err != nil {
		return err
	}
	lock, err := acquireLock(filepath.Join(w.Paths.workerStateDir(), "worker.lock"))
	if err != nil {
		return err
	}
	defer lock.Close()
	if handled, err := w.reportFailure(ctx); handled || err != nil {
		return err
	}
	if handled, err := w.reportRollback(ctx); handled || err != nil {
		return err
	}
	if handled, err := w.reportActivation(ctx); handled || err != nil {
		return err
	}
	check, err := w.Control.Check(ctx, w.Config.AgentPublicID)
	if err != nil {
		return fmt.Errorf("signed update check: %w", err)
	}
	switch check.DesiredAction {
	case p2pstreamv1.AgentUpdateDesiredAction_AGENT_UPDATE_DESIRED_ACTION_NONE:
		if check.Authorization != nil {
			return errors.New("management NONE response unexpectedly contains a root-action authorization")
		}
		return nil
	case p2pstreamv1.AgentUpdateDesiredAction_AGENT_UPDATE_DESIRED_ACTION_STAGE:
		if check.Authorization != nil {
			return errors.New("management STAGE response unexpectedly contains a root-action authorization")
		}
		return w.stage(ctx, check)
	case p2pstreamv1.AgentUpdateDesiredAction_AGENT_UPDATE_DESIRED_ACTION_ACTIVATE:
		return w.requestActivation(check)
	case p2pstreamv1.AgentUpdateDesiredAction_AGENT_UPDATE_DESIRED_ACTION_ROLLBACK:
		authorization, _, err := w.authorizationFromCheck(check, agentupdateauth.AssignmentActionRollback)
		if err != nil {
			return err
		}
		return RequestRollback(w.Paths, authorization)
	default:
		return errors.New("management returned an unsupported update action")
	}
}

func (w *Worker) reportRollback(ctx context.Context) (bool, error) {
	data, err := readRegularNoFollow(w.Paths.rollbackResultPath(), 64<<10)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return true, err
	}
	var result rollbackRecord
	if err := strictJSON(data, &result); err != nil {
		return true, err
	}
	if err := verifyRootActionReceiptForWorker(w.Paths, result.Receipt, w.Config.AgentPublicID, agentupdateauth.AssignmentActionRollback); err != nil {
		return true, err
	}
	_, err = w.Control.ReportRootAction(ctx, agentupdateauth.Report{
		AgentPublicID: w.Config.AgentPublicID, AssignmentID: result.Authorization.Authorization.AssignmentID, Generation: result.Authorization.Authorization.Generation,
		State: int32(p2pstreamv1.AgentUpdaterReportState_AGENT_UPDATER_REPORT_STATE_ROLLED_BACK),
	}, result.Receipt)
	if err != nil {
		return true, err
	}
	return true, removeAndSync(w.Paths.rollbackResultPath())
}

func (w *Worker) stage(ctx context.Context, check *p2pstreamv1.CheckAgentUpdateResponse) error {
	expected, err := releaseFromCheck(check)
	if err != nil {
		return err
	}
	reusable, err := reusableStagedRelease(w.Paths, expected, check.ServerVersion)
	if err != nil {
		return err
	}
	if reusable {
		_, err = w.Control.Report(ctx, agentupdateauth.Report{
			AgentPublicID: w.Config.AgentPublicID, AssignmentID: check.AssignmentId, Generation: check.Generation,
			State:          int32(p2pstreamv1.AgentUpdaterReportState_AGENT_UPDATER_REPORT_STATE_STAGED),
			ManifestSHA256: expected.ManifestSHA256, BinarySHA256: artifactHex(expected.Artifact),
		})
		return err
	}
	_, _ = w.Control.Report(ctx, agentupdateauth.Report{
		AgentPublicID: w.Config.AgentPublicID, AssignmentID: check.AssignmentId, Generation: check.Generation,
		State: int32(p2pstreamv1.AgentUpdaterReportState_AGENT_UPDATER_REPORT_STATE_DOWNLOADING),
	})
	source, err := NewGitHubSource(w.Config, expected.Version, nil)
	if err != nil {
		return err
	}
	verifier := exactReleaseVerifier{Verifier: AgentUpdateVerifier{}, expected: expected}
	_, err = Stage(ctx, StageOptions{
		Paths: w.Paths, Source: source, Verifier: verifier,
		Policy: VerifyPolicy{
			ServerVersion: check.ServerVersion, UpdaterVersion: buildinfo.Version,
			ProtocolVersion: uint32(tunnel.ProtocolVersion), RequiredChannel: "stable",
		},
	})
	if err != nil {
		_, _ = w.Control.Report(ctx, agentupdateauth.Report{
			AgentPublicID: w.Config.AgentPublicID, AssignmentID: check.AssignmentId, Generation: check.Generation,
			State:       int32(p2pstreamv1.AgentUpdaterReportState_AGENT_UPDATER_REPORT_STATE_FAILED),
			FailureCode: "stage_failed", FailureDetail: boundedFailure(err),
		})
		return err
	}
	_, err = w.Control.Report(ctx, agentupdateauth.Report{
		AgentPublicID: w.Config.AgentPublicID, AssignmentID: check.AssignmentId, Generation: check.Generation,
		State:          int32(p2pstreamv1.AgentUpdaterReportState_AGENT_UPDATER_REPORT_STATE_STAGED),
		ManifestSHA256: expected.ManifestSHA256, BinarySHA256: artifactHex(expected.Artifact),
	})
	if err != nil {
		return err
	}
	return err
}

func reusableStagedRelease(paths Paths, expected VerifiedRelease, serverVersion string) (bool, error) {
	data, err := readRegularNoFollow(paths.stagedPath(), 64<<10)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	var staged stagedRecord
	if err := strictJSON(data, &staged); err != nil {
		return false, nil
	}
	want := stagedRecord{
		Version: expected.Version, Commit: expected.Commit, ManifestSHA: expected.ManifestSHA256,
		RootVersion: expected.RootVersion, Sequence: expected.Sequence, SecurityEpoch: expected.SecurityEpoch,
		ArtifactName: expected.Artifact.Name, ArtifactSize: expected.Artifact.Size,
		ArtifactSHA: artifactHex(expected.Artifact), ServerVersion: serverVersion,
	}
	if staged != want {
		return false, nil
	}
	artifact, err := openRegularNoFollow(filepath.Join(paths.candidateDir(), "artifact.bin"), expected.Artifact.Size)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		return false, err
	}
	defer artifact.Close()
	if err := (AgentUpdateVerifier{}).VerifyArtifact(artifact, expected.Artifact); err != nil {
		return false, nil
	}
	return true, nil
}

func (w *Worker) requestActivation(check *p2pstreamv1.CheckAgentUpdateResponse) error {
	authorization, expected, err := w.authorizationFromCheck(check, agentupdateauth.AssignmentActionActivate)
	if err != nil {
		return err
	}
	return RequestActivation(w.Paths, authorization, expected, check.ServerVersion)
}

func (w *Worker) authorizationFromCheck(check *p2pstreamv1.CheckAgentUpdateResponse, action agentupdateauth.AssignmentAction) (assignmentAuthorizationRecord, VerifiedRelease, error) {
	expected, err := releaseFromCheck(check)
	if err != nil {
		return assignmentAuthorizationRecord{}, VerifiedRelease{}, err
	}
	record, err := assignmentAuthorizationFromProto(check.Authorization)
	if err != nil {
		return assignmentAuthorizationRecord{}, VerifiedRelease{}, err
	}
	if err := verifyAssignmentAuthorizationRecord(w.Paths, record, action, time.Now().UTC(), 0); err != nil {
		return assignmentAuthorizationRecord{}, VerifiedRelease{}, err
	}
	a := record.Authorization
	if a.AgentPublicID != w.Config.AgentPublicID || a.AssignmentID != check.AssignmentId || a.CampaignID != check.CampaignId ||
		a.Generation != check.Generation || (action == agentupdateauth.AssignmentActionActivate && check.DesiredAction != p2pstreamv1.AgentUpdateDesiredAction_AGENT_UPDATE_DESIRED_ACTION_ACTIVATE) ||
		(action == agentupdateauth.AssignmentActionRollback && check.DesiredAction != p2pstreamv1.AgentUpdateDesiredAction_AGENT_UPDATE_DESIRED_ACTION_ROLLBACK) {
		return assignmentAuthorizationRecord{}, VerifiedRelease{}, errors.New("signed root-action authorization does not match management check context")
	}
	if err := authorizationMatchesRelease(a, expected, check.ServerVersion); err != nil {
		return assignmentAuthorizationRecord{}, VerifiedRelease{}, err
	}
	return record, expected, nil
}

func releaseFromCheck(check *p2pstreamv1.CheckAgentUpdateResponse) (VerifiedRelease, error) {
	if check == nil || check.AssignmentId <= 0 || check.Generation <= 0 || check.Target == nil || check.Artifact == nil {
		return VerifiedRelease{}, errors.New("management update assignment is incomplete")
	}
	if !validVersion(check.ServerVersion) {
		return VerifiedRelease{}, errors.New("management update response has an invalid server version")
	}
	if check.Target.ReleaseSequence <= 0 || check.Target.SecurityEpoch <= 0 || check.Target.RootVersion <= 0 ||
		check.Artifact.SizeBytes <= 0 || check.Artifact.Os != runtime.GOOS || check.Artifact.Arch != runtime.GOARCH {
		return VerifiedRelease{}, errors.New("management update target has invalid floor or platform data")
	}
	digest, err := hex.DecodeString(check.Artifact.Sha256)
	if err != nil || len(digest) != 32 {
		return VerifiedRelease{}, errors.New("management update artifact digest is invalid")
	}
	var sha [32]byte
	copy(sha[:], digest)
	release := VerifiedRelease{
		Version: check.Target.Version, Commit: check.Target.Commit, ManifestSHA256: check.Target.ManifestSha256,
		Sequence: uint64(check.Target.ReleaseSequence), SecurityEpoch: uint64(check.Target.SecurityEpoch),
		RootVersion: uint64(check.Target.RootVersion),
		Artifact:    Artifact{Name: check.Artifact.Name, Size: check.Artifact.SizeBytes, SHA256: sha},
	}
	if err := validateRelease(release); err != nil {
		return VerifiedRelease{}, err
	}
	return release, nil
}

type exactReleaseVerifier struct {
	Verifier
	expected VerifiedRelease
}

func (v exactReleaseVerifier) Verify(manifest, signatures, root []byte, policy VerifyPolicy) (VerifiedRelease, error) {
	release, err := v.Verifier.Verify(manifest, signatures, root, policy)
	if err != nil {
		return VerifiedRelease{}, err
	}
	if release.Version != v.expected.Version || release.Commit != v.expected.Commit ||
		release.ManifestSHA256 != v.expected.ManifestSHA256 || release.Sequence != v.expected.Sequence ||
		release.SecurityEpoch != v.expected.SecurityEpoch || release.RootVersion != v.expected.RootVersion ||
		release.Artifact != v.expected.Artifact {
		return VerifiedRelease{}, errors.New("signed GitHub manifest does not exactly match management assignment")
	}
	return release, nil
}

func (w *Worker) reportActivation(ctx context.Context) (bool, error) {
	receipt, err := LoadRootActionReceipt(w.Paths)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return true, err
	}
	if err := verifyRootActionReceiptForWorker(w.Paths, receipt, w.Config.AgentPublicID, agentupdateauth.AssignmentActionActivate); err != nil {
		return true, err
	}
	activation := receipt.Receipt
	if activation.Action != agentupdateauth.AssignmentActionActivate {
		return true, errors.New("root activation receipt records a different action")
	}
	state := activationReportState{}
	if data, err := readRegularNoFollow(w.Paths.activationReportStatePath(), 64<<10); err == nil {
		if err := strictJSON(data, &state); err != nil {
			return true, err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return true, err
	}
	if state.ActivationCounter != activation.RootActionCounter {
		state = activationReportState{ActivationCounter: activation.RootActionCounter}
	}
	report := agentupdateauth.Report{
		AgentPublicID: activation.AgentPublicID, AssignmentID: activation.AssignmentID,
		Generation: activation.Generation, ManifestSHA256: activation.ResultManifestSHA256,
		BinarySHA256: activation.ResultArtifactSHA256, RunningVersion: activation.ResultVersion,
		RunningCommit: activation.ResultCommit,
	}
	if !state.ActivatedReported {
		report.State = int32(p2pstreamv1.AgentUpdaterReportState_AGENT_UPDATER_REPORT_STATE_ACTIVATED)
		response, err := w.Control.ReportRootAction(ctx, report, receipt)
		if err != nil {
			return true, err
		}
		if response.Generation < activation.Generation {
			return true, errors.New("management acknowledged activation with a different assignment generation")
		}
		if response.Generation > activation.Generation ||
			response.State == p2pstreamv1.AgentUpdateAssignmentState_AGENT_UPDATE_ASSIGNMENT_STATE_BLOCKED ||
			response.State == p2pstreamv1.AgentUpdateAssignmentState_AGENT_UPDATE_ASSIGNMENT_STATE_FAILED ||
			response.State == p2pstreamv1.AgentUpdateAssignmentState_AGENT_UPDATE_ASSIGNMENT_STATE_CANCELLED ||
			response.State == p2pstreamv1.AgentUpdateAssignmentState_AGENT_UPDATE_ASSIGNMENT_STATE_SUCCEEDED {
			if err := removeAndSync(w.Paths.rootActionReceiptPath()); err != nil {
				return true, err
			}
			if err := removeAndSync(w.Paths.activationReportStatePath()); err != nil {
				return true, err
			}
			return true, nil
		}
		if response.State != p2pstreamv1.AgentUpdateAssignmentState_AGENT_UPDATE_ASSIGNMENT_STATE_AWAITING_TUNNEL &&
			response.State != p2pstreamv1.AgentUpdateAssignmentState_AGENT_UPDATE_ASSIGNMENT_STATE_HEALTHY_DWELL {
			return true, errors.New("management acknowledged activation in an unsafe state")
		}
		state.ActivatedReported = true
		if err := atomicJSON(w.Paths.activationReportStatePath(), state, 0600); err != nil {
			return true, err
		}
		return true, nil
	}
	report.State = int32(p2pstreamv1.AgentUpdaterReportState_AGENT_UPDATER_REPORT_STATE_HEALTHY)
	if _, err := w.Control.Report(ctx, report); err != nil {
		return true, err
	}
	if err := removeAndSync(w.Paths.rootActionReceiptPath()); err != nil {
		return true, err
	}
	if err := removeAndSync(w.Paths.activationReportStatePath()); err != nil {
		return true, err
	}
	return true, nil
}

func boundedFailure(err error) string {
	text := strings.TrimSpace(err.Error())
	if len(text) > 1024 {
		text = text[:1024]
	}
	return text
}

func RunActivator(ctx context.Context, paths Paths) error {
	return runActivator(ctx, paths, DefaultSystemdService())
}

func runActivator(ctx context.Context, paths Paths, service ServiceController) error {
	// Durable journals always recover before a newly published command. Their
	// cleanup is authorization-specific, so recovery cannot consume a later
	// campaign's staging edge.
	if exists, err := regularPathExists(paths.rollbackJournalPath()); err != nil {
		return err
	} else if exists {
		return runClaimedRollback(ctx, paths, service)
	}
	if exists, err := regularPathExists(paths.journalPath()); err != nil {
		return err
	} else if exists {
		return runClaimedActivation(ctx, paths, service)
	}
	if _, err := claimRootActionCommand(paths.rollbackPath(), paths.rollbackClaimPath()); err != nil {
		return err
	}
	if exists, err := regularPathExists(paths.rollbackClaimPath()); err != nil {
		return err
	} else if exists {
		return runClaimedRollback(ctx, paths, service)
	}
	if _, err := claimRootActionCommand(paths.readyPath(), paths.activationClaimPath()); err != nil {
		return err
	}
	// Cancellation can publish rollback concurrently with claiming activation.
	// Give that newer signed command priority without discarding the activation
	// claim; the rollback path clears only a proven older activation command.
	if _, err := claimRootActionCommand(paths.rollbackPath(), paths.rollbackClaimPath()); err != nil {
		return err
	}
	if exists, err := regularPathExists(paths.rollbackClaimPath()); err != nil {
		return err
	} else if exists {
		return runClaimedRollback(ctx, paths, service)
	}
	if exists, err := regularPathExists(paths.activationClaimPath()); err != nil {
		return err
	} else if !exists {
		return nil
	}
	return runClaimedActivation(ctx, paths, service)
}

func runClaimedRollback(ctx context.Context, paths Paths, service ServiceController) error {
	rollbackErr := rollbackFromPath(ctx, paths, service, paths.rollbackClaimPath())
	if rollbackErr != nil {
		return errors.Join(rollbackErr, QuarantineActivationFailure(paths, rollbackErr, agentupdateauth.AssignmentActionRollback, paths.rollbackClaimPath()))
	}
	return nil
}

func runClaimedActivation(ctx context.Context, paths Paths, service ServiceController) error {
	_, err := Activate(ctx, ActivateOptions{
		Paths: paths, ReadyPath: paths.activationClaimPath(), Verifier: AgentUpdateVerifier{}, Service: service,
		Policy: VerifyPolicy{
			UpdaterVersion: buildinfo.Version, ProtocolVersion: uint32(tunnel.ProtocolVersion), RequiredChannel: "stable",
		},
	})
	if err != nil {
		return errors.Join(err, QuarantineActivationFailure(paths, err, agentupdateauth.AssignmentActionActivate, paths.activationClaimPath()))
	}
	return nil
}

func regularPathExists(path string) (bool, error) {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0022 != 0 {
		return false, errors.New("root action path is not a protected regular file")
	}
	return true, nil
}

func claimRootActionCommand(source, claim string) (bool, error) {
	if exists, err := regularPathExists(claim); err != nil {
		return false, err
	} else if exists {
		return true, nil
	}
	if exists, err := regularPathExists(source); err != nil {
		return false, err
	} else if !exists {
		return false, nil
	}
	if err := os.Rename(source, claim); err != nil {
		return false, err
	}
	if err := syncDir(filepath.Dir(source)); err != nil {
		return false, err
	}
	if filepath.Dir(claim) != filepath.Dir(source) {
		if err := syncDir(filepath.Dir(claim)); err != nil {
			return false, err
		}
	}
	return true, nil
}
