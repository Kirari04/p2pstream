package serverupdate

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"time"

	"github.com/google/uuid"
)

type state struct {
	API           int        `json:"api"`
	InstanceID    string     `json:"instance_id"`
	Channel       string     `json:"channel"`
	Floor         Floor      `json:"floor"`
	ObservedFloor Floor      `json:"observed_floor"`
	Plan          *Plan      `json:"plan,omitempty"`
	Operation     *Operation `json:"operation,omitempty"`
}

type Engine struct {
	mu          sync.Mutex
	driver      Driver
	source      Source
	path        string
	state       state
	wake        chan struct{}
	lastCheck   time.Time
	cached      *Release
	lastWarning string
}

func NewEngine(directory, instanceID, channel string, driver Driver, source Source, bootstrap Floor) (*Engine, error) {
	if _, err := uuid.Parse(instanceID); err != nil {
		return nil, errors.New("invalid deployment instance ID")
	}
	if driver == nil || source == nil || (channel != "stable" && channel != "staging") {
		return nil, errors.New("invalid updater configuration")
	}
	if err := os.MkdirAll(directory, 0700); err != nil {
		return nil, err
	}
	info, err := os.Lstat(directory)
	if err != nil || !info.IsDir() || info.Mode().Perm()&0077 != 0 {
		return nil, errors.New("updater state directory must be private")
	}
	e := &Engine{driver: driver, source: source, path: filepath.Join(directory, "state.json"), wake: make(chan struct{}, 1), state: state{API: API, InstanceID: instanceID, Channel: channel, Floor: bootstrap, ObservedFloor: bootstrap}}
	data, err := readProtected(e.path, 2<<20)
	if err == nil {
		if err := decode(data, &e.state); err != nil {
			return nil, err
		}
		if e.state.API != API || e.state.InstanceID != instanceID || e.state.Channel != channel {
			return nil, errors.New("updater state belongs to a different deployment or channel")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	return e, nil
}

func (e *Engine) save(next state) error {
	data, err := json.Marshal(next)
	if err != nil {
		return err
	}
	if err := AtomicWrite(e.path, data, 0600); err != nil {
		return err
	}
	e.state = next
	return nil
}

func (e *Engine) Overview(ctx context.Context) (Overview, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	o := Overview{InstanceID: e.state.InstanceID, Channel: e.state.Channel, Operation: e.state.Operation}
	if err := ctx.Err(); err != nil {
		return o, err
	}
	if o.Operation != nil && !o.Operation.Terminal() {
		return clone(o), nil
	}
	current, _, _, err := e.driver.Inspect(ctx)
	if ctxErr := ctx.Err(); ctxErr != nil {
		return o, ctxErr
	}
	if err != nil {
		o.Warning = "Cannot inspect the managed server: " + err.Error()
		return clone(o), nil
	}
	o.Current = current
	if current.InstanceID != e.state.InstanceID {
		return o, errors.New("managed server instance identity changed")
	}
	if e.lastCheck.IsZero() || time.Since(e.lastCheck) >= time.Hour {
		r, err := e.source.Latest(ctx, current, e.verificationFloor())
		if ctxErr := ctx.Err(); ctxErr != nil {
			return o, ctxErr
		}
		e.lastCheck = time.Now()
		e.lastWarning = ""
		e.cached = nil
		if err != nil {
			e.lastWarning = err.Error()
		} else {
			if r != nil {
				if err := e.observe(*r); err != nil {
					return o, err
				}
			}
			e.cached = r
		}
	}
	if e.cached != nil && time.Now().Before(e.cached.ExpiresAt) && e.cached.Version != current.Version {
		o.Target = e.cached
	}
	o.Warning = e.lastWarning
	return clone(o), nil
}

func (e *Engine) Preview(ctx context.Context, version string) (Plan, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.state.Operation != nil && !e.state.Operation.Terminal() {
		return Plan{}, errors.New("a server update is already in progress")
	}
	current, image, deployment, err := e.driver.Inspect(ctx)
	if err != nil {
		return Plan{}, err
	}
	if current.InstanceID != e.state.InstanceID || !current.Ready || len(current.Blocked) != 0 {
		return Plan{}, errors.New("server is not ready or another rollout is in progress")
	}
	r, err := e.source.Resolve(ctx, version, current, e.verificationFloor())
	if err != nil {
		return Plan{}, err
	}
	if err := e.observe(r); err != nil {
		return Plan{}, err
	}
	p := Plan{InstanceID: e.state.InstanceID, CurrentVersion: current.Version, CurrentImage: image, DeploymentSHA256: deployment, Release: r, ExpiresAt: time.Now().UTC().Add(10 * time.Minute), ID: uuid.NewString()}
	next := e.state
	next.Plan = &p
	if err := e.save(next); err != nil {
		return Plan{}, err
	}
	return p, nil
}

func (e *Engine) Start(ctx context.Context, request StartRequest) (Operation, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if _, err := uuid.Parse(request.OperationID); err != nil || len(request.Actor) == 0 || len(request.Actor) > 256 {
		return Operation{}, errors.New("invalid operation ID or actor")
	}
	if old := e.state.Operation; old != nil {
		if old.ID == request.OperationID {
			if !reflect.DeepEqual(old.Plan, request.Plan) || old.Actor != request.Actor {
				return Operation{}, errors.New("idempotency key reused for a different request")
			}
			return clone(*old), nil
		}
		if !old.Terminal() {
			return Operation{}, errors.New("a server update is already in progress")
		}
	}
	if e.state.Plan == nil || !reflect.DeepEqual(*e.state.Plan, request.Plan) || request.Plan.InstanceID != e.state.InstanceID || !time.Now().Before(request.Plan.ExpiresAt) {
		return Operation{}, errors.New("preview changed or expired; preview again")
	}
	current, image, deployment, err := e.driver.Inspect(ctx)
	if err != nil {
		return Operation{}, err
	}
	if current.InstanceID != e.state.InstanceID || current.Version != request.Plan.CurrentVersion || image != request.Plan.CurrentImage || deployment != request.Plan.DeploymentSHA256 {
		return Operation{}, errors.New("server or deployment changed since preview")
	}
	op := Operation{ID: request.OperationID, Plan: request.Plan, Actor: request.Actor, Phase: "accepted", StartedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(), Baseline: current}
	next := e.state
	next.Operation = &op
	next.Plan = nil
	if err := e.save(next); err != nil {
		return Operation{}, err
	}
	select {
	case e.wake <- struct{}{}:
	default:
	}
	return clone(op), nil
}

func (e *Engine) Operation(id string) (*Operation, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.state.Operation == nil || (id != "" && e.state.Operation.ID != id) {
		return nil, errors.New("server update operation not found")
	}
	o := clone(*e.state.Operation)
	return &o, nil
}

// Run is owned by the supervisor lifetime, never an HTTP request. Recovery uses
// the accepted journal even when GitHub or the management service is offline.
func (e *Engine) Run(ctx context.Context) error {
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-e.wake:
			if err := e.execute(ctx); err != nil {
				if ctx.Err() != nil {
					return ctx.Err()
				}
				if err := e.requireRecovery(err); err != nil {
					return err
				}
			}
		}
	}
}

func (e *Engine) change(op Operation, phase, detail string) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	op.Phase = phase
	op.Detail = detail
	op.UpdatedAt = time.Now().UTC()
	next := e.state
	next.Operation = &op
	return e.save(next)
}

func (e *Engine) execute(ctx context.Context) error {
	opPtr, err := e.Operation("")
	if err != nil {
		return err
	}
	op := *opPtr
	if op.Terminal() || op.Phase == "recovery_required" {
		return nil
	}
	if err := e.change(op, "pulling", ""); err != nil {
		return err
	}
	// Re-fetch from the executor's trusted origin just before staging. A browser
	// preview cannot extend metadata lifetime or substitute an image.
	e.mu.Lock()
	floor := e.verificationFloor()
	e.mu.Unlock()
	r, err := e.source.Resolve(ctx, op.Plan.Release.Version, op.Baseline, floor)
	if err == nil && !reflect.DeepEqual(r, op.Plan.Release) {
		err = errors.New("release changed since preview")
	}
	if err == nil {
		err = e.driver.Pull(ctx, op.Plan.CurrentImage)
	}
	if err == nil {
		err = e.driver.Pull(ctx, op.Plan.Release.Image)
	}
	if err != nil {
		return e.change(op, "failed", err.Error())
	}
	// Host operators may have edited or replaced the deployment while images
	// were downloading. Check the preview binding again before stopping it.
	current, image, deployment, err := e.driver.Inspect(ctx)
	if err == nil && (current.InstanceID != op.Plan.InstanceID || current.Version != op.Plan.CurrentVersion || image != op.Plan.CurrentImage || deployment != op.Plan.DeploymentSHA256) {
		err = errors.New("deployment changed while downloading; preview again")
	}
	if err != nil {
		return e.change(op, "failed", err.Error())
	}
	if err := e.change(op, "preparing", ""); err != nil {
		return err
	}
	if err := e.driver.Gate(true); err != nil {
		// A failed fsync can follow a successful rename, so an error does not
		// prove the gate was never installed. Clear it durably before reporting
		// an ordinary failure; an ambiguous cleanup requires host recovery.
		return e.failPreparation(op, err)
	}
	baseline, err := e.driver.Prepare(ctx)
	if err == nil && (baseline.InstanceID != op.Plan.InstanceID || baseline.Version != op.Plan.CurrentVersion || !baseline.Ready || len(baseline.Blocked) > 0) {
		err = errors.New("server readiness or rollout state changed")
	}
	if err != nil {
		return e.failPreparation(op, err)
	}
	op.Baseline = baseline
	if err := e.change(op, "stopping", ""); err != nil {
		return err
	}
	if err := e.driver.Stop(ctx); err != nil {
		return e.rollback(ctx, op, err)
	}
	if err := e.change(op, "backing_up", ""); err != nil {
		return err
	}
	op.BackupSHA256, err = e.driver.Backup(ctx, op.ID)
	if err != nil {
		return e.rollback(ctx, op, err)
	}
	return e.activateCandidate(ctx, op)
}

func (e *Engine) failPreparation(op Operation, cause error) error {
	if err := e.driver.Gate(false); err != nil {
		return fmt.Errorf("could not durably clear maintenance: %w", errors.Join(cause, err))
	}
	return e.change(op, "failed", cause.Error())
}

// activateCandidate is also used when a reboot interrupted committing. The
// candidate still had restart=no at that point and may now be stopped. Keep
// recovery durable before starting it and require a fresh health dwell before
// enabling its normal restart policy or advancing the activation floor.
func (e *Engine) activateCandidate(ctx context.Context, op Operation) error {
	if err := e.change(op, "deploying", ""); err != nil {
		return err
	}
	if err := e.driver.Deploy(ctx, op.Plan.Release.Image); err != nil {
		return e.rollback(ctx, op, err)
	}
	if err := e.change(op, "validating", ""); err != nil {
		return err
	}
	if err := e.driver.Healthy(ctx, op.Plan.Release, op.Baseline); err != nil {
		return e.rollback(ctx, op, err)
	}
	if err := e.change(op, "committing", ""); err != nil {
		return err
	}
	return e.commit(ctx, op)
}

func (e *Engine) commit(ctx context.Context, op Operation) error {
	if err := e.driver.Commit(ctx); err != nil {
		return err
	}
	e.mu.Lock()
	next := e.state
	r := op.Plan.Release
	next.Floor = r.VerificationFloor()
	op.Phase = "succeeded"
	op.UpdatedAt = time.Now().UTC()
	next.Operation = &op
	err := e.save(next)
	e.lastCheck = time.Time{}
	e.mu.Unlock()
	if err != nil {
		return err
	}
	return e.driver.Gate(false)
}

func (e *Engine) rollback(ctx context.Context, op Operation, cause error) error {
	if err := e.change(op, "rolling_back", cause.Error()); err != nil {
		return err
	}
	if err := e.driver.Stop(ctx); err != nil {
		return fmt.Errorf("recovery could not stop the server: %w", err)
	}
	if op.BackupSHA256 != "" {
		if err := e.driver.Restore(ctx, op.ID, op.BackupSHA256); err != nil {
			return fmt.Errorf("recovery requires host attention: %w", err)
		}
	}
	if err := e.driver.Deploy(ctx, op.Plan.CurrentImage); err != nil {
		return err
	}
	previous := Release{Version: op.Baseline.Version, Commit: op.Baseline.Commit, Image: op.Plan.CurrentImage, Metadata: Metadata{TargetSchema: op.Baseline.Schema}}
	if err := e.driver.Healthy(ctx, previous, op.Baseline); err != nil {
		return fmt.Errorf("previous server did not recover: %w", err)
	}
	if err := e.driver.Commit(ctx); err != nil {
		return err
	}
	if err := e.change(op, "rolled_back", cause.Error()); err != nil {
		return err
	}
	return e.driver.Gate(false)
}

func (e *Engine) recover(ctx context.Context) error {
	op, err := e.Operation("")
	if err != nil {
		return nil
	}
	switch op.Phase {
	case "recovery_required":
		return nil
	case "succeeded", "rolled_back", "failed":
		return e.driver.Gate(false)
	case "accepted", "pulling", "preparing":
		if err := e.driver.Gate(false); err != nil {
			return err
		}
		return e.change(*op, "failed", "Updater restarted before replacement; preview and retry the update")
	case "committing":
		if err := e.driver.Gate(true); err != nil {
			return err
		}
		return e.activateCandidate(ctx, *op)
	case "stopping", "backing_up", "deploying", "validating", "rolling_back":
		return e.rollback(ctx, *op, errors.New("recovered interrupted server update"))
	default:
		return errors.New("unknown recovery phase; host attention required")
	}
}

// Recover must complete before accepting HTTP requests. Failed recovery is
// durably paused; only the host-only recover command may explicitly retry it.
func (e *Engine) Recover(ctx context.Context, retry bool) error {
	if op, err := e.Operation(""); err == nil && op.Phase == "recovery_required" {
		if !retry {
			return nil
		}
		if err := e.change(*op, op.RecoveryPhase, op.Detail); err != nil {
			return err
		}
	}
	if err := e.recover(ctx); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if saved := e.requireRecovery(err); saved != nil {
			return saved
		}
		if retry {
			return err
		}
	}
	return nil
}

func (e *Engine) requireRecovery(cause error) error {
	op, err := e.Operation("")
	if err != nil {
		return cause
	}
	if op.Phase != "recovery_required" {
		op.RecoveryPhase = op.Phase
	}
	return e.change(*op, "recovery_required", "Host recovery required: "+cause.Error())
}

// The verification floor records trusted observations even when activation
// fails. Restoring last-good data never erases this executor-owned state.
func (e *Engine) verificationFloor() Floor {
	if e.state.ObservedFloor.Sequence != 0 {
		return e.state.ObservedFloor
	}
	return e.state.Floor
}
func (e *Engine) observe(r Release) error {
	next := e.state
	next.ObservedFloor = r.VerificationFloor()
	return e.save(next)
}

func clone[T any](value T) T {
	data, _ := json.Marshal(value)
	var out T
	_ = json.Unmarshal(data, &out)
	return out
}
