package serverupdate

import (
	"context"
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

type fakeSource struct {
	release     Release
	err         error
	calls       int
	latestCalls int
	latest      func(context.Context, RuntimeStatus, Floor) (*Release, error)
}

func (s *fakeSource) Latest(ctx context.Context, current RuntimeStatus, floor Floor) (*Release, error) {
	s.latestCalls++
	if s.latest != nil {
		return s.latest(ctx, current, floor)
	}
	return &s.release, s.err
}
func (s *fakeSource) Resolve(context.Context, string, RuntimeStatus, Floor) (Release, error) {
	s.calls++
	return s.release, s.err
}

type fakeDriver struct {
	current     RuntimeStatus
	image       string
	calls       []string
	fail, crash string
	gated       bool
	restored    bool
}

func (d *fakeDriver) action(name string) error {
	d.calls = append(d.calls, name)
	if name == d.crash {
		panic("power lost")
	}
	if name == d.fail {
		return errors.New("injected " + name + " failure")
	}
	return nil
}
func (d *fakeDriver) Inspect(context.Context) (RuntimeStatus, string, string, error) {
	return d.current, d.image, strings.Repeat("d", 64), nil
}
func (d *fakeDriver) Pull(_ context.Context, image string) error { return d.action("pull") }
func (d *fakeDriver) Gate(value bool) error                      { d.gated = value; return d.action("gate") }
func (d *fakeDriver) Prepare(context.Context) (RuntimeStatus, error) {
	return d.current, d.action("prepare")
}
func (d *fakeDriver) Stop(context.Context) error { return d.action("stop") }
func (d *fakeDriver) Backup(context.Context, string) (string, error) {
	err := d.action("backup")
	if err != nil {
		return "", err
	}
	return strings.Repeat("b", 64), nil
}
func (d *fakeDriver) Restore(context.Context, string, string) error {
	d.restored = true
	return d.action("restore")
}
func (d *fakeDriver) Deploy(_ context.Context, image string) error {
	d.image = image
	return d.action("deploy")
}
func (d *fakeDriver) Healthy(_ context.Context, r Release, _ RuntimeStatus) error {
	if r.Version == "v1.0.1" {
		return d.action("candidate-health")
	}
	return d.action("previous-health")
}
func (d *fakeDriver) Commit(context.Context) error { return d.action("commit") }

func fixtureEngine(t *testing.T) (*Engine, *fakeDriver, *fakeSource, StartRequest) {
	t.Helper()
	id := uuid.NewString()
	d := &fakeDriver{current: RuntimeStatus{API: API, InstanceID: id, Version: "v1.0.0", Commit: strings.Repeat("a", 40), Schema: Schema, Ready: true}, image: "ghcr.io/test/repo@sha256:" + strings.Repeat("a", 64)}
	s := &fakeSource{release: Release{Version: "v1.0.1", Commit: strings.Repeat("b", 40), Channel: "stable", ManifestSHA256: strings.Repeat("c", 64), Image: "ghcr.io/test/repo@sha256:" + strings.Repeat("b", 64), Sequence: 2, SecurityEpoch: 1, MinimumSafeVersion: "v1.0.0", ExpiresAt: time.Now().UTC().Add(time.Hour), Metadata: NewMetadata("v1.0.1", strings.Repeat("b", 40))}}
	directory := t.TempDir()
	if err := os.Chmod(directory, 0700); err != nil {
		t.Fatal(err)
	}
	e, err := NewEngine(directory, id, "stable", d, s, Floor{})
	if err != nil {
		t.Fatal(err)
	}
	p, err := e.Preview(context.Background(), "v1.0.1")
	if err != nil {
		t.Fatal(err)
	}
	return e, d, s, StartRequest{OperationID: uuid.NewString(), Plan: p, Actor: "admin:test"}
}

func TestOverviewCallerCancellationDoesNotPoisonCheckCache(t *testing.T) {
	e, _, source, _ := fixtureEngine(t)
	previousCheck := time.Now().Add(-2 * time.Hour)
	previousRelease := clone(source.release)
	previousRelease.Version = "v1.0.2"
	previousWarning := "previous release check warning"
	previousObservedFloor := e.state.ObservedFloor
	e.lastCheck = previousCheck
	e.cached = &previousRelease
	e.lastWarning = previousWarning

	ctx, cancel := context.WithCancel(context.Background())
	canceledRelease := clone(source.release)
	canceledRelease.Sequence++
	source.latest = func(ctx context.Context, _ RuntimeStatus, _ Floor) (*Release, error) {
		cancel()
		return &canceledRelease, nil
	}
	if _, err := e.Overview(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("Overview() error = %v, want context.Canceled", err)
	}
	if !e.lastCheck.Equal(previousCheck) {
		t.Fatalf("lastCheck = %v, want unchanged %v", e.lastCheck, previousCheck)
	}
	if !reflect.DeepEqual(e.cached, &previousRelease) {
		t.Fatalf("cached release = %+v, want unchanged %+v", e.cached, previousRelease)
	}
	if e.lastWarning != previousWarning {
		t.Fatalf("lastWarning = %q, want %q", e.lastWarning, previousWarning)
	}
	if e.state.ObservedFloor != previousObservedFloor {
		t.Fatalf("observed floor = %+v, want unchanged %+v", e.state.ObservedFloor, previousObservedFloor)
	}

	source.latest = nil
	overview, err := e.Overview(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if overview.Target == nil || overview.Target.Version != source.release.Version {
		t.Fatalf("immediate retry target = %+v, want %q", overview.Target, source.release.Version)
	}
	if !e.lastCheck.After(previousCheck) || e.lastWarning != "" {
		t.Fatalf("successful retry did not refresh cache: lastCheck=%v warning=%q", e.lastCheck, e.lastWarning)
	}
	if source.latestCalls != 2 {
		t.Fatalf("Latest() calls = %d, want cancellation plus immediate retry", source.latestCalls)
	}
}

func TestOverviewCachesSourceTimeoutWhenCallerIsActive(t *testing.T) {
	e, _, source, _ := fixtureEngine(t)
	source.err = context.DeadlineExceeded

	overview, err := e.Overview(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if overview.Warning != context.DeadlineExceeded.Error() {
		t.Fatalf("warning = %q, want %q", overview.Warning, context.DeadlineExceeded)
	}
	if e.lastCheck.IsZero() {
		t.Fatal("source timeout did not advance lastCheck")
	}
	if e.cached != nil {
		t.Fatalf("cached release = %+v, want nil", e.cached)
	}

	source.err = nil
	overview, err = e.Overview(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if overview.Warning != context.DeadlineExceeded.Error() || source.latestCalls != 1 {
		t.Fatalf("cached timeout = warning %q after %d calls, want %q after one call", overview.Warning, source.latestCalls, context.DeadlineExceeded)
	}
}

func TestEngineAcceptsExactlyOneBoundRequest(t *testing.T) {
	e, d, _, request := fixtureEngine(t)
	for _, mutate := range []func(*StartRequest){
		func(r *StartRequest) { r.Plan.InstanceID = uuid.NewString() },
		func(r *StartRequest) { r.Plan.Release.Image = "ghcr.io/attacker/image:latest" },
		func(r *StartRequest) { r.Plan.CurrentImage = "other" },
		func(r *StartRequest) { r.Plan.ExpiresAt = time.Now().Add(time.Hour) },
	} {
		r := clone(request)
		mutate(&r)
		if _, err := e.Start(context.Background(), r); err == nil {
			t.Fatal("accepted altered preview")
		}
	}
	d.current.Version = "v1.0.2"
	if _, err := e.Start(context.Background(), request); err == nil {
		t.Fatal("accepted stale server version")
	}
	d.current.Version = "v1.0.0"
	first, err := e.Start(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	second, err := e.Start(context.Background(), request)
	if err != nil || !reflect.DeepEqual(first, second) {
		t.Fatalf("retry = %+v, %v", second, err)
	}
	other := request
	other.OperationID = uuid.NewString()
	if _, err := e.Start(context.Background(), other); err == nil {
		t.Fatal("accepted concurrent update")
	}
	if len(d.calls) != 0 {
		t.Fatal("HTTP acceptance executed host actions")
	}
	if err := e.execute(context.Background()); err != nil {
		t.Fatal(err)
	}
	op, _ := e.Operation(first.ID)
	if op.Phase != "succeeded" || d.gated || e.state.Floor.Sequence != 2 {
		t.Fatalf("not committed: %+v", op)
	}
	count := len(d.calls)
	if _, err := e.Start(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	if len(d.calls) != count {
		t.Fatal("retry repeated host actions")
	}
}

func TestEngineRejectsReleaseChangedAfterAcceptance(t *testing.T) {
	e, d, s, r := fixtureEngine(t)
	if _, err := e.Start(context.Background(), r); err != nil {
		t.Fatal(err)
	}
	s.release.Image = "ghcr.io/test/repo@sha256:" + strings.Repeat("f", 64)
	if err := e.execute(context.Background()); err != nil {
		t.Fatal(err)
	}
	o, _ := e.Operation(r.OperationID)
	if o.Phase != "failed" || len(d.calls) != 0 {
		t.Fatalf("unsafe changed release: %+v %v", o, d.calls)
	}
}

func TestEngineRestoresSnapshotBeforePreviousImage(t *testing.T) {
	e, d, _, r := fixtureEngine(t)
	_, err := e.Start(context.Background(), r)
	if err != nil {
		t.Fatal(err)
	}
	d.fail = "candidate-health"
	if err := e.execute(context.Background()); err != nil {
		t.Fatal(err)
	}
	o, _ := e.Operation(r.OperationID)
	if o.Phase != "rolled_back" || !d.restored || d.image != r.Plan.CurrentImage || d.gated || e.state.Floor.Sequence != 0 {
		t.Fatalf("recovery = %+v, driver %+v", o, d)
	}
	want := []string{"pull", "pull", "gate", "prepare", "stop", "backup", "deploy", "candidate-health", "stop", "restore", "deploy", "previous-health", "commit", "gate"}
	if !reflect.DeepEqual(want, d.calls) {
		t.Fatalf("actions = %v", d.calls)
	}
}

func TestEngineRecoveryAfterEveryInterruptedHostTransition(t *testing.T) {
	for _, crash := range []string{"pull", "gate", "prepare", "stop", "backup", "deploy", "candidate-health", "commit"} {
		t.Run(crash, func(t *testing.T) {
			e, d, s, r := fixtureEngine(t)
			if _, err := e.Start(context.Background(), r); err != nil {
				t.Fatal(err)
			}
			d.crash = crash
			func() {
				defer func() {
					if recover() == nil {
						t.Error("expected simulated power loss")
					}
				}()
				_ = e.execute(context.Background())
			}()
			d.crash = ""
			s.err = errors.New("network unavailable")
			calls := s.calls
			fresh, err := NewEngine(strings.TrimSuffix(e.path, "/state.json"), r.Plan.InstanceID, "stable", d, s, Floor{})
			if err != nil {
				t.Fatal(err)
			}
			if err := fresh.recover(context.Background()); err != nil {
				t.Fatal(err)
			}
			o, _ := fresh.Operation(r.OperationID)
			if !o.Terminal() || d.gated || s.calls != calls {
				t.Fatalf("failed offline recovery: %+v", o)
			}
			if crash == "deploy" || crash == "candidate-health" {
				if !d.restored {
					t.Fatal("candidate data not restored")
				}
			}
			if crash == "commit" && o.Phase != "succeeded" {
				t.Fatal("healthy commit rolled back")
			}
		})
	}
}

func TestEngineFailedBackupDoesNotActivateCandidate(t *testing.T) {
	e, d, _, r := fixtureEngine(t)
	_, _ = e.Start(context.Background(), r)
	d.fail = "backup"
	if err := e.execute(context.Background()); err != nil {
		t.Fatal(err)
	}
	if d.restored || d.image != r.Plan.CurrentImage {
		t.Fatal("failed backup caused data restoration or candidate activation")
	}
	for _, call := range d.calls {
		if call == "candidate-health" {
			t.Fatal("candidate started without a backup")
		}
	}
}

func TestFailedRecoveryPausesAcrossRestartsUntilHostRetries(t *testing.T) {
	e, d, s, r := fixtureEngine(t)
	_, _ = e.Start(context.Background(), r)
	d.crash = "candidate-health"
	func() { defer func() { _ = recover() }(); _ = e.execute(context.Background()) }()
	d.crash = ""
	d.fail = "restore"
	if err := e.Recover(context.Background(), false); err != nil {
		t.Fatal(err)
	}
	op, _ := e.Operation(r.OperationID)
	if op.Phase != "recovery_required" || op.RecoveryPhase != "rolling_back" || !d.gated {
		t.Fatalf("unsafe recovery: %+v", op)
	}
	calls := len(d.calls)
	fresh, err := NewEngine(strings.TrimSuffix(e.path, "/state.json"), r.Plan.InstanceID, "stable", d, s, Floor{})
	if err != nil {
		t.Fatal(err)
	}
	if err := fresh.Recover(context.Background(), false); err != nil {
		t.Fatal(err)
	}
	if len(d.calls) != calls {
		t.Fatal("automatically retried paused recovery")
	}
	if _, err := fresh.Preview(context.Background(), r.Plan.Release.Version); err == nil {
		t.Fatal("allowed update during failed recovery")
	}
	if fresh.state.ObservedFloor.Sequence != r.Plan.Release.Sequence || fresh.state.Floor.Sequence != 0 {
		t.Fatal("lost verification floor or advanced activation floor")
	}
	d.fail = ""
	if err := fresh.Recover(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	op, _ = fresh.Operation(r.OperationID)
	if op.Phase != "rolled_back" || d.gated {
		t.Fatalf("host retry failed: %+v", op)
	}
}
