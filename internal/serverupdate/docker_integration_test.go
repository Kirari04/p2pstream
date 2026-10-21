//go:build server_update_docker

package serverupdate

// This suite uses real release-like runtime containers and the production
// ComposeDriver. Only the not-yet-published GitHub catalog and deterministic
// fault checkpoints are fixtures. Run via scripts/test-server-updater.sh.
import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

type dockerReviewSource struct{ release Release }

func (s dockerReviewSource) Latest(context.Context, RuntimeStatus, Floor) (*Release, error) {
	return &s.release, nil
}
func (s dockerReviewSource) Resolve(_ context.Context, version string, _ RuntimeStatus, _ Floor) (Release, error) {
	if version != s.release.Version {
		return Release{}, errors.New("unexpected fixture release")
	}
	return s.release, nil
}

func dockerReviewConfig(t *testing.T) ComposeConfig {
	t.Helper()
	var c ComposeConfig
	b, err := os.ReadFile(os.Getenv("REVIEW_CONFIG"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, &c); err != nil {
		t.Fatal(err)
	}
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
	return c
}

func TestDockerReviewEnroll(t *testing.T) {
	directory := os.Getenv("REVIEW_STATE")
	if directory == "" {
		t.Skip("isolated Docker fixture required")
	}
	model, err := os.ReadFile(filepath.Join(directory, "model.json"))
	if err != nil {
		t.Fatal(err)
	}
	floor := Floor{Sequence: 9000, SecurityEpoch: 1, MinimumSafeVersion: "v0.1.53-staging.9000", ManifestSHA256: strings.Repeat("a", 64)}
	deploymentDirectory := directory + "-deployment"
	if err := os.Mkdir(deploymentDirectory, 0750); err != nil {
		t.Fatal(err)
	}
	if err := EnrollManagedInstallation(directory, deploymentDirectory, model, os.Getenv("REVIEW_BASELINE"), os.Getenv("REVIEW_HELPER"), "v0.1.53-staging.9000", "review/p2pstream", floor, os.Getenv("REVIEW_INSTALLATION")); err != nil {
		t.Fatal(err)
	}
	var c ComposeConfig
	b, _ := os.ReadFile(filepath.Join(directory, "config.json"))
	if err := json.Unmarshal(b, &c); err != nil {
		t.Fatal(err)
	}
	if os.Getenv("REVIEW_SCENARIO") != "success" {
		c.HealthyDwellSeconds, c.HealthTimeoutSeconds = 2, 30
	}
	b, _ = json.Marshal(c)
	if err := AtomicWrite(filepath.Join(directory, "config.json"), b, 0600); err != nil {
		t.Fatal(err)
	}
}

// Adjacent Compose versions can produce the same service hash. Independently
// exercise the receipt verifier against a real native-created runtime so that
// cross-version compatibility does not silently skip fingerprint validation.
func TestDockerReviewRuntimeReceipt(t *testing.T) {
	if os.Getenv("REVIEW_CONFIG") == "" {
		t.Skip("isolated Docker fixture required")
	}
	c := dockerReviewConfig(t)
	d := &ComposeDriver{Config: c}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	model, err := os.ReadFile(c.DeploymentFile)
	if err != nil {
		t.Fatal(err)
	}
	id, err := d.container(ctx)
	if err != nil {
		t.Fatal(err)
	}
	info, err := d.inspectContainer(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	native, err := d.command(ctx, "--config", "/review/native-docker", "compose", "version", "--short")
	if err != nil {
		t.Fatal(err)
	}
	managed, err := d.command(ctx, "compose", "version", "--short")
	if err != nil {
		t.Fatal(err)
	}
	nativeVersion := strings.TrimPrefix(strings.TrimSpace(string(native)), "v")
	managedVersion := strings.TrimPrefix(strings.TrimSpace(string(managed)), "v")
	if nativeVersion == managedVersion || strings.TrimPrefix(info.Config.Labels["com.docker.compose.version"], "v") != nativeVersion {
		t.Fatal("receipt rehearsal requires a runtime created by the distinct native Compose version")
	}
	if err := d.VerifyRuntimeReceipt(ctx, model, id); err != nil {
		t.Fatal("real native runtime differs from the trusted managed receipt", err)
	}
	if _, _, _, err := d.Inspect(ctx); err != nil {
		t.Fatal("native runtime failed enrolled identity or writer checks", err)
	}
	hashes, err := d.compose(ctx, "config", "--hash", "p2pstream")
	fields := strings.Fields(string(hashes))
	if err != nil || len(fields) != 2 {
		t.Fatal("cannot record managed configuration hash", err)
	}
	nativeHash := info.Config.Labels["com.docker.compose.config-hash"]
	result, err := json.Marshal(map[string]any{"native_version": nativeVersion, "managed_version": managedVersion,
		"native_hash": nativeHash, "managed_hash": fields[1], "receipt_fallback_required": nativeHash != fields[1], "runtime_receipt_verified_directly": true})
	if err != nil {
		t.Fatal(err)
	}
	if err := AtomicWrite(filepath.Join(c.StateDir, "native-runtime-receipt.json"), result, 0600); err != nil {
		t.Fatal(err)
	}
}

// Native Compose does not take the host lock. Rehearse its actual competing
// container while the production driver owns offline data, then retain a stale
// generation waiter across the handoff to a freshly allowed generation.
func TestDockerReviewNativeGuard(t *testing.T) {
	if os.Getenv("REVIEW_CONFIG") == "" {
		t.Skip("isolated Docker fixture required")
	}
	c := dockerReviewConfig(t)
	if c.DeploymentProtocol != 2 {
		t.Fatal("native recreation rehearsal requires protocol 2")
	}
	c.HealthyDwellSeconds, c.HealthTimeoutSeconds = 1, 60
	d := &ComposeDriver{Config: c}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	defer d.releaseRuntimeLease()
	baseline, image, _, err := d.Inspect(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := d.Gate(true); err != nil {
		t.Fatal(err)
	}
	if err := d.Stop(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := d.command(ctx, "--config", "/review/native-docker", "compose", "--project-name", c.Project, "--project-directory", filepath.Dir(c.DeploymentFile), "-f", c.DeploymentFile,
		"up", "-d", "--no-deps", "--no-build", "--pull", "never", "--force-recreate", "p2pstream"); err != nil {
		t.Fatal(err)
	}
	id, err := d.container(ctx)
	if err != nil || id == "" {
		t.Fatal("native recreation did not create a waiting server", err)
	}
	if _, err := d.command(ctx, "exec", id, "/app/p2pstream", "server-health"); err == nil {
		t.Fatal("native recreation opened the management listener during offline ownership")
	}
	if err := d.requireOfflineLease(); err != nil {
		t.Fatal("native recreation altered blocked data ownership", err)
	}
	if _, err := d.Backup(ctx, uuid.NewString()); err == nil {
		t.Fatal("offline backup ignored the separately running native container")
	}
	if err := d.Stop(ctx); err != nil {
		t.Fatal(err)
	}
	model, err := os.ReadFile(c.DeploymentFile)
	if err != nil {
		t.Fatal(err)
	}
	service, err := receiptService(model)
	if err != nil {
		t.Fatal(err)
	}
	staleName := c.Project + "-stale-generation"
	args := []string{"run", "-d", "--name", staleName, "--network", "none", "--read-only", "--tmpfs", "/tmp", "--cap-drop", "ALL", "--cap-add", "NET_BIND_SERVICE",
		"--mount", "type=volume,source=" + c.DataVolume + ",target=/data", "--mount", "type=bind,source=" + c.ControlDir + ",target=" + RuntimeControlDir + ",readonly"}
	for key, value := range receiptMap(service["environment"]) {
		text, ok := value.(string)
		if !ok {
			t.Fatal("unexpected fixture environment type")
		}
		args = append(args, "--env", key+"="+text)
	}
	args = append(args, image)
	if _, err := d.command(ctx, args...); err != nil {
		t.Fatal(err)
	}
	defer d.command(context.Background(), "rm", "--force", staleName)
	// Deploy writes a fresh generation while still holding the lease, then
	// allows that generation before releasing it. The old waiter must fail.
	if err := d.Deploy(ctx, image); err != nil {
		t.Fatal(err)
	}
	replacement, err := d.container(ctx)
	if err != nil || replacement == "" {
		t.Fatal("missing newly allowed replacement", err)
	}
	// Either waiting process can win the released lease. Stop the newly
	// allowed candidate without changing admission, so the stale waiter must
	// get a turn and validate the new generation deterministically.
	if _, err := d.command(ctx, "stop", "--time", "30", replacement); err != nil {
		t.Fatal(err)
	}
	for {
		raw, err := d.command(ctx, "inspect", staleName)
		if err != nil {
			t.Fatal(err)
		}
		var states []struct {
			State struct {
				Running  bool
				ExitCode int
			}
		}
		if err := json.Unmarshal(raw, &states); err != nil || len(states) != 1 {
			t.Fatal("invalid stale container inspection", err)
		}
		if !states[0].State.Running {
			// Docker preserves the container's output streams. Startup admission
			// errors are written to stderr, which the production command helper
			// intentionally omits from successful command results.
			logsCommand := exec.CommandContext(ctx, d.Config.Docker, "logs", staleName)
			logsCommand.Env = []string{"PATH=/usr/local/bin:/usr/bin:/bin", "HOME=/root", "DOCKER_HOST=unix:///var/run/docker.sock", "COMPOSE_DISABLE_ENV_FILE=1"}
			logsCommand.WaitDelay = 5 * time.Second
			var logs limitedBuffer
			logsCommand.Stdout, logsCommand.Stderr = &logs, &logs
			if err := logsCommand.Run(); err != nil {
				t.Fatal("cannot read stale runtime admission diagnostics", err)
			}
			if states[0].State.ExitCode == 0 || !strings.Contains(logs.String(), "runtime start is blocked") {
				t.Fatalf("stale generation did not fail at admission: exit=%d logs=%s", states[0].State.ExitCode, logs.String())
			}
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-time.After(100 * time.Millisecond):
		}
	}
	if _, err := d.command(ctx, "start", replacement); err != nil {
		t.Fatal(err)
	}
	release := Release{Version: baseline.Version, Commit: baseline.Commit, Image: image, Metadata: Metadata{TargetSchema: baseline.Schema}}
	if err := d.Healthy(ctx, release, baseline); err != nil {
		t.Fatal(err)
	}
	if err := d.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if err := d.Gate(false); err != nil {
		t.Fatal(err)
	}
	t.Log("Native force-recreate could not write offline data; stale generation was rejected after lease handoff")
}

type dockerReviewDriver struct {
	*ComposeDriver
	scenario  string
	candidate string
}

func (d *dockerReviewDriver) checkpoint(ctx context.Context, name string) error {
	path := filepath.Join(d.Config.StateDir, "checkpoint")
	if _, err := os.Stat(path); err == nil {
		return nil
	}
	if err := AtomicWrite(path, []byte(name), 0600); err != nil {
		return err
	}
	<-ctx.Done()
	return ctx.Err()
}

func (d *dockerReviewDriver) Healthy(ctx context.Context, r Release, baseline RuntimeStatus) error {
	if r.Image == d.candidate {
		if d.scenario == "kill-validating" {
			if err := d.checkpoint(ctx, "validating"); err != nil {
				return err
			}
		}
		if d.scenario == "rollback" || d.scenario == "retry-recovery" {
			db, err := sql.Open("sqlite3", filepath.Join(d.Config.DataDir, "p2pstream.db"))
			if err != nil {
				return err
			}
			_, err = db.Exec("CREATE TABLE review_candidate(value TEXT); INSERT INTO review_candidate VALUES ('candidate changed the schema');")
			db.Close()
			if err != nil {
				return err
			}
			if err := os.WriteFile(filepath.Join(d.Config.DataDir, "review-authority"), []byte("candidate authority"), 0600); err != nil {
				return err
			}
			return errors.New("injected candidate health failure after schema and authority mutation")
		}
	}
	return d.ComposeDriver.Healthy(ctx, r, baseline)
}

func (d *dockerReviewDriver) Commit(ctx context.Context) error {
	if d.scenario == "reboot-committing" {
		if err := d.checkpoint(ctx, "committing"); err != nil {
			return err
		}
	}
	return d.ComposeDriver.Commit(ctx)
}

func (d *dockerReviewDriver) Restore(ctx context.Context, id, digest string) error {
	if d.scenario == "retry-recovery" {
		marker := filepath.Join(d.Config.StateDir, "restore-failed")
		if _, err := os.Stat(marker); errors.Is(err, os.ErrNotExist) {
			if err := AtomicWrite(marker, []byte("one injected restore failure"), 0600); err != nil {
				return err
			}
			return errors.New("injected unavailable backup storage")
		}
	}
	return d.ComposeDriver.Restore(ctx, id, digest)
}

func TestDockerReviewLifecycle(t *testing.T) {
	if os.Getenv("REVIEW_CONFIG") == "" {
		t.Skip("isolated Docker fixture required")
	}
	c := dockerReviewConfig(t)
	if _, err := os.Stat(filepath.Join(c.StateDir, "failed")); err == nil {
		t.Fatal("a prior executor run failed; automatic restart cannot erase test failure")
	}
	defer func() {
		if t.Failed() {
			_ = AtomicWrite(filepath.Join(c.StateDir, "failed"), []byte("executor assertion failed"), 0600)
		}
		if value := recover(); value != nil {
			_ = AtomicWrite(filepath.Join(c.StateDir, "failed"), []byte("executor panicked"), 0600)
			panic(value)
		}
	}()
	scenario := os.Getenv("REVIEW_SCENARIO")
	candidate := os.Getenv("REVIEW_CANDIDATE")
	d := &dockerReviewDriver{ComposeDriver: &ComposeDriver{Config: c}, scenario: scenario, candidate: candidate}
	r := Release{Version: "v0.1.53-staging.9001", Commit: strings.Repeat("b", 40), Channel: "staging", Image: candidate, Sequence: 9001, SecurityEpoch: 1, MinimumSafeVersion: "v0.1.53-staging.9000", ManifestSHA256: strings.Repeat("b", 64), ExpiresAt: time.Now().UTC().Add(time.Hour), Metadata: NewMetadata("v0.1.53-staging.9001", strings.Repeat("b", 40))}
	e, err := NewEngine(c.StateDir, c.InstanceID, c.Channel, d, dockerReviewSource{r}, c.BootstrapFloor)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Minute)
	defer cancel()
	workerCtx, stopWorker := context.WithCancel(ctx)
	defer stopWorker()
	workerDone := make(chan struct{})
	old, _ := e.Operation("")
	if old == nil {
		status, _, _, err := d.Inspect(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if !status.Ready || len(status.Agents) != 1 || len(status.Listeners) < 1 {
			t.Fatalf("fixture must have a live agent and listener: %+v", status)
		}
		if err := os.WriteFile(filepath.Join(c.DataDir, "review-authority"), []byte("original authority"), 0600); err != nil {
			t.Fatal(err)
		}
		p, err := e.Preview(ctx, r.Version)
		if err != nil {
			t.Fatal(err)
		}
		req := StartRequest{OperationID: uuid.NewString(), Plan: p, Actor: "docker-review-admin"}
		accepted, err := e.Start(ctx, req)
		if err != nil {
			t.Fatal(err)
		}
		retried, err := e.Start(ctx, req)
		if err != nil || retried.ID != accepted.ID {
			t.Fatalf("idempotent retry: %+v %v", retried, err)
		}
		go func(engine *Engine) { defer close(workerDone); _ = engine.Run(workerCtx) }(e)
	} else {
		if (scenario != "reboot-committing" || old.Phase != "committing") && (scenario != "kill-validating" || old.Phase != "validating") {
			t.Fatalf("unexpected executor restart in scenario %s, phase %s", scenario, old.Phase)
		}
		if _, err := d.command(ctx, "update", "--restart=no", os.Getenv("REVIEW_EXECUTOR")); err != nil {
			t.Fatal(err)
		}
		t.Logf("Recovering durable phase %s after executor/host restart", old.Phase)
		// Recovery must not require the unpublished fixture source.
		e.source = dockerReviewOfflineSource{}
		if err := e.Recover(ctx, false); err != nil {
			t.Fatal(err)
		}
	}
	last := ""
	for {
		op, err := e.Operation("")
		if err != nil {
			t.Fatal(err)
		}
		if op.Phase != last {
			t.Logf("operation %s: %s (%s)", op.ID, op.Phase, op.Detail)
			last = op.Phase
		}
		if op.Phase == "recovery_required" {
			if scenario != "retry-recovery" {
				t.Fatalf("unexpected host recovery: %+v", op)
			}
			// Reload durable state with the previous worker fully stopped.
			stopWorker()
			<-workerDone
			restarted, err := NewEngine(c.StateDir, c.InstanceID, c.Channel, d, dockerReviewOfflineSource{}, c.BootstrapFloor)
			if err != nil {
				t.Fatal(err)
			}
			if err := restarted.Recover(ctx, false); err != nil {
				t.Fatal(err)
			}
			paused, _ := restarted.Operation("")
			if paused.Phase != "recovery_required" {
				t.Fatal("restart bypassed host recovery")
			}
			if _, err := os.Stat(filepath.Join(c.ControlDir, "maintenance")); err != nil {
				t.Fatal("recovery lost maintenance gate", err)
			}
			if err := restarted.Recover(ctx, true); err != nil {
				t.Fatal(err)
			}
			e = restarted
			continue
		}
		if op.Terminal() {
			expectedPhase, expectedVersion := "succeeded", r.Version
			if scenario == "rollback" || scenario == "retry-recovery" || scenario == "kill-validating" {
				expectedPhase, expectedVersion = "rolled_back", "v0.1.53-staging.9000"
			}
			if op.Phase != expectedPhase {
				t.Fatalf("expected %s, got %+v", expectedPhase, op)
			}
			status, image, _, err := d.Inspect(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if status.Version != expectedVersion || !status.Ready || status.Maintenance || len(status.Agents) != 1 || len(status.Listeners) != len(op.Baseline.Listeners) {
				t.Fatalf("unexpected recovered runtime: %+v image=%s", status, image)
			}
			if op.BackupSHA256 == "" {
				t.Fatal("missing durable snapshot digest")
			}
			if _, err := os.Stat(filepath.Join(c.ControlDir, "maintenance")); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("maintenance was not cleared", err)
			}
			contents, err := os.ReadFile(filepath.Join(c.DataDir, "review-authority"))
			if err != nil || string(contents) != "original authority" {
				t.Fatalf("authority not preserved/restored: %q %v", contents, err)
			}
			db, err := sql.Open("sqlite3", filepath.Join(c.DataDir, "p2pstream.db"))
			if err != nil {
				t.Fatal(err)
			}
			var count int
			err = db.QueryRow("SELECT count(*) FROM sqlite_master WHERE name = 'review_candidate'").Scan(&count)
			db.Close()
			if err != nil || count != 0 {
				t.Fatalf("candidate schema survived rollback: count=%d %v", count, err)
			}
			result, _ := json.MarshalIndent(op, "", "  ")
			if err := AtomicWrite(filepath.Join(c.StateDir, "passed.json"), result, 0600); err != nil {
				t.Fatal(err)
			}
			return
		}
		select {
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-time.After(time.Second):
		}
	}
}

type dockerReviewOfflineSource struct{}

func (dockerReviewOfflineSource) Latest(context.Context, RuntimeStatus, Floor) (*Release, error) {
	return nil, errors.New("catalog offline during recovery")
}
func (dockerReviewOfflineSource) Resolve(context.Context, string, RuntimeStatus, Floor) (Release, error) {
	return Release{}, errors.New("catalog offline during recovery")
}
