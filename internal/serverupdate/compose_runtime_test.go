package serverupdate

import (
	"context"
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

func managedRuntimeFixture(t *testing.T) (*ComposeDriver, RuntimeOwner) {
	t.Helper()
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("Docker command fixture requires python3")
	}
	dir := t.TempDir()
	state, data, deployment := filepath.Join(dir, "state"), filepath.Join(dir, "data"), filepath.Join(dir, "deployment")
	for _, path := range []string{state, data, deployment} {
		if err := os.Mkdir(path, 0750); err != nil {
			t.Fatal(err)
		}
	}
	input, _ := json.Marshal(enrollmentFixture())
	instance := uuid.NewString()
	image := "ghcr.io/test/repo@sha256:" + strings.Repeat("a", 64)
	if err := EnrollManagedInstallation(state, deployment, input, image, "sha256:"+strings.Repeat("b", 64), "v1.0.0", "test/repo", Floor{Sequence: 1, SecurityEpoch: 1, MinimumSafeVersion: "v1.0.0", ManifestSHA256: strings.Repeat("c", 64)}, instance); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(filepath.Join(state, "config.json"))
	var cfg ComposeConfig
	if err := json.Unmarshal(raw, &cfg); err != nil {
		t.Fatal(err)
	}
	cfg.DataDir = data
	cfg.Docker = filepath.Join(dir, "docker")
	// Model Docker's stopped creation independently of the owner marker. The
	// stop assertion observes the durable guard at the external side effect.
	script := `#!/usr/bin/env python3
import json,pathlib,sys,time
root=pathlib.Path(__file__).parent
args=sys.argv[1:]
cfg=json.loads((root/'state'/'config.json').read_text())
model=json.loads((root/'deployment'/'compose.json').read_text())
service=model['services']['p2pstream']
owner=root/'data'/'.p2pstream-runtime-owner.json'
def identity(image):
    candidate=image.endswith('d'*64)
    return {'runtime_guard':2,'version':'v1.1.0' if candidate else 'v1.0.0','commit':('d' if candidate else 'a')*40}
def container():
    image=service['image']
    return {'Id':'managed-container','Image':'sha256:'+image.split(':')[-1], 'State':{'Running':(root/'running').exists()},'Config':{'Image':image,'Labels':{'com.docker.compose.project':'fixture','com.docker.compose.service':'p2pstream','p2pstream.server-update.instance':cfg['instance_id']}},'Mounts':[]}
if args[:2]==['image','inspect']:
    cap=identity(args[-1])
    print(json.dumps([{'Id':'sha256:'+args[-1].split(':')[-1],'Config':{'Labels':{'org.opencontainers.image.version':cap['version'],'org.opencontainers.image.revision':cap['commit'],'org.opencontainers.image.source':'https://github.com/test/repo'}}}]))
elif args[0]=='run':
    assert args[-1]=='server-deployment-capabilities'
    if (root/'unsupported').exists():
        print('private-probe-output-must-not-escape',file=sys.stderr)
        sys.exit(1)
    if (root/'pause-probe').exists():
        (root/'probe-started').touch()
        time.sleep(60)
    print(json.dumps(identity(args[-2])))
elif args[0]=='compose':
    if 'ps' in args: print('managed-container')
    elif 'up' in args:
        assert '--no-start' in args
        assert json.loads(owner.read_text())['allowed']==False
        (root/'running').unlink(missing_ok=True)
    else: sys.exit(3)
elif args[0]=='inspect': print(json.dumps([container()]))
elif args[0]=='update': pass
elif args[0]=='stop':
    assert json.loads(owner.read_text())['allowed']==False
    (root/'running').unlink(missing_ok=True)
elif args[0]=='start':
    current=json.loads(owner.read_text())
    assert current['allowed'] and current['generation']==service['environment']['SERVER_UPDATE_GENERATION']
    (root/'running').touch()
else: sys.exit(4)
`
	if err := os.WriteFile(cfg.Docker, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	driver := &ComposeDriver{Config: cfg}
	owner, err := driver.modelRuntimeOwner(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	owner.Allowed = true
	if err := WriteRuntimeOwner(data, owner); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "running"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(driver.releaseRuntimeLease)
	return driver, owner
}

func TestManagedEnrollmentPublishesNoControlSecret(t *testing.T) {
	driver, _ := managedRuntimeFixture(t)
	raw, err := os.ReadFile(driver.Config.DeploymentFile)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), driver.Config.Token) || strings.Contains(string(raw), `"SERVER_UPDATE_TOKEN":`) {
		t.Fatal("shared canonical model exposes private control token")
	}
	info, err := os.Stat(driver.Config.DeploymentFile)
	if err != nil || info.Mode().Perm() != 0640 {
		t.Fatalf("canonical model mode: %v %v", info, err)
	}
	token, err := os.ReadFile(filepath.Join(driver.Config.ControlDir, "token"))
	if err != nil || strings.TrimSpace(string(token)) != driver.Config.Token {
		t.Fatal("token file missing or changed")
	}
	input, _ := json.Marshal(enrollmentFixture())
	configured, err := ConfigureModel(driver.Config, input, raw)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(configured), `"SERVER_UPDATE_TOKEN":`) {
		t.Fatal("configuration reintroduced legacy secret")
	}
}

func TestManagedStopBlocksBeforeDockerAndHoldsWriterThroughDeployment(t *testing.T) {
	driver, old := managedRuntimeFixture(t)
	// A live server owns this lease. Blocking new starts must not wait for it.
	serverLease, err := AcquireRuntimeLease(context.Background(), driver.Config.DataDir)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := driver.BlockRuntime(ctx); err != nil {
		t.Fatal(err)
	}
	blocked, err := ReadRuntimeOwner(driver.Config.DataDir)
	if err != nil || blocked.Allowed {
		t.Fatal("new starts were not durably blocked")
	}
	serverLease.Close()
	if err := driver.Stop(ctx); err != nil {
		t.Fatal(err)
	}
	waitCtx, waitCancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer waitCancel()
	if lease, err := AcquireRuntimeLease(waitCtx, driver.Config.DataDir); !errors.Is(err, context.DeadlineExceeded) {
		if lease != nil {
			lease.Close()
		}
		t.Fatalf("offline writer exclusion failed: %v", err)
	}
	if err := driver.Deploy(context.Background(), "ghcr.io/test/repo@sha256:"+strings.Repeat("d", 64)); err != nil {
		t.Fatal(err)
	}
	lease, err := AcquireRuntimeLease(context.Background(), driver.Config.DataDir)
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Close()
	current, err := ReadRuntimeOwner(driver.Config.DataDir)
	if err != nil || !current.Allowed || current.Generation == old.Generation || current.Version != "v1.1.0" {
		t.Fatalf("candidate admission invalid: %+v %v", current, err)
	}
	stale := RuntimeAdmission{Protocol: 2, InstanceID: old.InstanceID, Generation: old.Generation, Version: old.Version, Commit: old.Commit, Socket: RuntimeControlSocket, GateFile: RuntimeGateFile, TokenFile: RuntimeTokenFile}
	if err := ValidateRuntimeAdmission(current, stale); err == nil {
		t.Fatal("stale native container could write candidate data")
	}
}

func TestManagedCrashAndHostAdmissionReplay(t *testing.T) {
	driver, _ := managedRuntimeFixture(t)
	if _, err := driver.Backup(context.Background(), uuid.NewString()); err == nil {
		t.Fatal("backup without lease accepted")
	}
	if err := driver.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	driver.releaseRuntimeLease() // Equivalent to a supervisor exit/kill.
	owner, err := ReadRuntimeOwner(driver.Config.DataDir)
	if err != nil || owner.Allowed {
		t.Fatal("process exit cleared durable admission block")
	}
	if err := driver.AllowRuntime(context.Background()); err != nil {
		t.Fatal(err)
	}
	// Replaying the helper after its marker was persisted is idempotent.
	if err := driver.AllowRuntime(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := driver.BlockRuntime(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := driver.DetachRuntime(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := driver.DetachRuntime(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(driver.Config.DataDir, RuntimeLockName)); err != nil {
		t.Fatal("detach removed stable lock")
	}
	if err := driver.AllowRuntime(context.Background()); err == nil {
		t.Fatal("allow silently recreated removed enrollment")
	}
}

func TestManagedUnsupportedCandidateLeavesDataBlocked(t *testing.T) {
	driver, _ := managedRuntimeFixture(t)
	if err := driver.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(filepath.Dir(driver.Config.Docker), "unsupported"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	if err := driver.Deploy(context.Background(), "ghcr.io/test/repo@sha256:"+strings.Repeat("d", 64)); err == nil {
		t.Fatal("unsupported runtime accepted")
	}
	owner, err := ReadRuntimeOwner(driver.Config.DataDir)
	if err != nil || owner.Allowed {
		t.Fatal("failed activation unblocked the data volume")
	}
}

func TestGuardedImageProbeFailureRetainsSafeCause(t *testing.T) {
	driver, _ := managedRuntimeFixture(t)
	root := filepath.Dir(driver.Config.Docker)
	if err := os.WriteFile(filepath.Join(root, "unsupported"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	_, err := driver.guardedImage(context.Background(), "ghcr.io/test/repo@sha256:"+strings.Repeat("a", 64))
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 1 || !strings.Contains(err.Error(), "capability probe failed: Docker run failed") {
		t.Fatalf("Docker probe failure lost its safe cause: %v", err)
	}
	if strings.Contains(err.Error(), "lacks coordinated") || strings.Contains(err.Error(), "private-probe-output") {
		t.Fatalf("probe error claims unsupported image or exposes subprocess output: %v", err)
	}
}

func TestGuardedImageCancelledProbePreservesDeployment(t *testing.T) {
	driver, _ := managedRuntimeFixture(t)
	root := filepath.Dir(driver.Config.Docker)
	ownerPath := filepath.Join(driver.Config.DataDir, RuntimeOwnerName)
	owner, err := os.ReadFile(ownerPath)
	if err != nil {
		t.Fatal(err)
	}
	model, err := os.ReadFile(driver.Config.DeploymentFile)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "pause-probe"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	result := make(chan error, 1)
	go func() {
		_, err := driver.guardedImage(ctx, "ghcr.io/test/repo@sha256:"+strings.Repeat("a", 64))
		result <- err
	}()
	for {
		if _, err := os.Stat(filepath.Join(root, "probe-started")); err == nil {
			break
		}
		select {
		case err := <-result:
			t.Fatalf("probe did not reach the capability command: %v", err)
		case <-time.After(10 * time.Millisecond):
		}
	}
	cancel()
	err = <-result
	if !errors.Is(err, context.Canceled) || !strings.Contains(err.Error(), "capability probe interrupted") {
		t.Fatalf("probe cancellation was misreported: %v", err)
	}
	for path, before := range map[string][]byte{ownerPath: owner, driver.Config.DeploymentFile: model} {
		after, err := os.ReadFile(path)
		if err != nil || string(after) != string(before) {
			t.Fatalf("cancelled read-only probe changed deployment state: %s: %v", path, err)
		}
	}
}
