package updater

import (
	"context"
	"errors"
	"os"
	"path/filepath"

	p2pstreamv1 "p2pstream/gen/proto/p2pstream/v1"
	"p2pstream/internal/agentupdateauth"
)

type failureRecord struct {
	AssignmentID int64  `json:"assignment_id"`
	Generation   int64  `json:"generation"`
	Code         string `json:"code"`
	Detail       string `json:"detail"`
}

func (p Paths) failurePath() string { return filepath.Join(p.stagingDir(), "failure.json") }
func (p Paths) lastActivatorErrorPath() string {
	return filepath.Join(p.rootStateDir(), "last-error.json")
}

// QuarantineActivationFailure removes path-unit edges after any activator
// failure, preventing a tight root-service retry loop. Verified candidate bytes
// remain staged for diagnosis; the worker reports the bounded failure.
func QuarantineActivationFailure(paths Paths, cause error, action agentupdateauth.AssignmentAction, commandPath string) error {
	record := failureRecord{Code: "activation_failed", Detail: boundedFailure(cause)}
	if action == agentupdateauth.AssignmentActionActivate {
		if data, err := readRegularNoFollow(commandPath, 64<<10); err == nil {
			var ready readyRecord
			if strictJSON(data, &ready) == nil {
				record.AssignmentID, record.Generation = ready.AssignmentID, ready.Generation
			}
		}
	} else if action == agentupdateauth.AssignmentActionRollback {
		record.Code = "rollback_failed"
		if data, err := readRegularNoFollow(commandPath, 128<<10); err == nil {
			var rollback rollbackRequest
			if strictJSON(data, &rollback) == nil {
				record.AssignmentID = rollback.Authorization.Authorization.AssignmentID
				record.Generation = rollback.Authorization.Authorization.Generation
			}
		} else if data, journalErr := readRegularNoFollow(paths.rollbackJournalPath(), 256<<10); journalErr == nil {
			var journal rollbackJournal
			if strictJSON(data, &journal) == nil {
				record.AssignmentID = journal.Authorization.Authorization.AssignmentID
				record.Generation = journal.Authorization.Authorization.Generation
			}
		}
	}
	if err := atomicJSON(paths.lastActivatorErrorPath(), record, 0600); err != nil {
		return err
	}
	recoveryJournal := paths.journalPath()
	if action == agentupdateauth.AssignmentActionRollback {
		recoveryJournal = paths.rollbackJournalPath()
	}
	recoverable, err := regularPathExists(recoveryJournal)
	if err != nil {
		return err
	}
	// A durable root journal is still authoritative and watched by the bounded
	// recovery service. Publishing a lower-privilege FAILED report now would
	// move the server assignment to blocked before the recovered root receipt
	// can reconcile. Keep only root-local diagnostics until recovery either
	// succeeds or the server's independent root-action deadline expires.
	if !recoverable {
		if record.AssignmentID > 0 && record.Generation > 0 {
			if err := atomicJSON(paths.failurePath(), record, 0644); err != nil {
				return err
			}
		} else if err := removeAndSync(paths.failurePath()); err != nil {
			return err
		}
	}
	// Remove only the root-owned command claim selected for this invocation.
	// A newer rollback/activation edge published concurrently remains intact.
	if err := removeAndSync(commandPath); err != nil {
		return err
	}
	return nil
}

func (w *Worker) reportFailure(ctx context.Context) (bool, error) {
	data, err := readRegularNoFollow(w.Paths.failurePath(), 64<<10)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return true, err
	}
	var failure failureRecord
	if err := strictJSON(data, &failure); err != nil {
		return true, err
	}
	if failure.AssignmentID <= 0 || failure.Generation <= 0 {
		return true, errors.New("activation failure has no reportable assignment context")
	}
	_, err = w.Control.Report(ctx, agentupdateauth.Report{
		AgentPublicID: w.Config.AgentPublicID, AssignmentID: failure.AssignmentID, Generation: failure.Generation,
		State:       int32(p2pstreamv1.AgentUpdaterReportState_AGENT_UPDATER_REPORT_STATE_FAILED),
		FailureCode: failure.Code, FailureDetail: failure.Detail,
	})
	if err != nil {
		return true, err
	}
	return true, removeAndSync(w.Paths.failurePath())
}
