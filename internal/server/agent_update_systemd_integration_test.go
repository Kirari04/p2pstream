package server

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/hex"
	"encoding/pem"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/proto"

	p2pstreamv1 "p2pstream/gen/proto/p2pstream/v1"
	"p2pstream/internal/agentupdate"
	"p2pstream/internal/buildinfo"
)

// This test provisions the production paths and users. It is deliberately
// opt-in and requires the disposable-VM marker made by the companion script.
// Network distribution is a VM-local TLS mirror; installer, binaries, release
// verification, management handlers, signatures, tunnels and systemd are real.
func TestManagedUpdatesSystemdLifecycle(t *testing.T) {
	if os.Getenv("P2PSTREAM_SYSTEMD_INTEGRATION") != "1" {
		t.Skip("run scripts/test-managed-updates-systemd.sh in a disposable Multipass VM")
	}
	marker, err := os.ReadFile("/run/p2pstream-systemd-test-vm")
	hostname, _ := os.Hostname()
	if err != nil || string(marker) != "disposable-p2pstream-update-test\n" || os.Geteuid() != 0 || !strings.HasPrefix(hostname, "p2pstream-update-review") {
		t.Fatal("refusing to provision system paths outside the disposable test VM")
	}
	for _, path := range []string{"/etc/p2pstream", "/etc/p2pstream-updater", "/opt/p2pstream-agent", "/var/lib/p2pstream-updater", "/var/lib/p2pstream-agent"} {
		if _, err := os.Lstat(path); !os.IsNotExist(err) {
			t.Fatalf("VM must have no existing agent installation: %s", path)
		}
	}
	root := os.Getenv("P2PSTREAM_SYSTEMD_FIXTURE_DIR")
	if !filepath.IsAbs(root) {
		t.Fatal("absolute P2PSTREAM_SYSTEMD_FIXTURE_DIR is required")
	}
	legacy := filepath.Join(root, "p2pstream-legacy-worker")
	if _, err := os.Stat(legacy); err != nil {
		t.Fatalf("real legacy worker fixture is required: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	defer cancel()
	defer func() {
		cmd := exec.Command("journalctl", "-u", "p2pstream-agent", "-u", "p2pstream-updater", "-u", "p2pstream-updater-activate", "--no-pager", "-n", "240")
		if output, err := cmd.CombinedOutput(); err == nil {
			_ = os.WriteFile(filepath.Join(root, "systemd-journal.log"), output, 0600)
			if t.Failed() {
				t.Logf("systemd journal:\n%s", output)
			}
		}
		_ = exec.Command("systemctl", "stop", "p2pstream-updater.timer", "p2pstream-updater-activate.path", "p2pstream-updater.service", "p2pstream-updater-activate.service", "p2pstream-agent.service").Run()
	}()

	database := newServerTestDB(t)
	app := newAgentUpdateTestApp(t, database)
	buildinfo.Version = "v1.5.0"
	app.agentUpdateDrainReady = nil // Exercise actual tunnel/request draining.
	app.AgentUpdateBootstrap = agentUpdateTestBootstrapProvider{repository: "test/systemd"}
	agent := createAgentUpdateTestAgent(t, database, "agent-systemd-review")
	header := createTestAdminSession(t, app)
	assets := make(map[string][]byte)
	targets := make(map[string]*p2pstreamv1.AgentUpdateTarget)
	for i, version := range []string{"v1.1.0", "v1.2.0"} {
		body := systemdRead(t, filepath.Join(root, "p2pstream-"+version))
		digest := sha256.Sum256(body)
		artifact := agentupdate.Artifact{OS: "linux", Arch: "amd64", Name: "p2pstream_" + version + "_linux_amd64", Size: uint64(len(body)), SHA256: hex.EncodeToString(digest[:])}
		manifest := agentupdate.Manifest{
			SchemaVersion: 1, Channel: "stable", Version: version, Commit: strings.Repeat(string(rune('a'+i)), 40), Sequence: uint64(i + 2),
			PublishedAt: time.Now().UTC().Add(-time.Minute).Format(time.RFC3339), ExpiresAt: time.Now().UTC().Add(time.Hour).Format(time.RFC3339),
			MinimumSafeVersion: "v1.0.0", SecurityEpoch: 1,
			Compatibility: agentupdate.Compatibility{Server: agentupdate.VersionRange{Min: "v1.0.0", Max: "v2.0.0"}, Updater: agentupdate.VersionRange{Min: "v1.0.0", Max: "v2.0.0"}, Protocol: agentupdate.ProtocolRange{Min: 1, Max: 1}},
			Artifacts:     []agentupdate.Artifact{artifact},
		}
		data, err := agentupdate.CanonicalManifest(manifest)
		if err != nil {
			t.Fatal(err)
		}
		manifestDigest := sha256.Sum256(data)
		target := &p2pstreamv1.AgentUpdateTarget{Version: version, Commit: manifest.Commit, ManifestSha256: hex.EncodeToString(manifestDigest[:]), ReleaseSequence: int64(manifest.Sequence), SecurityEpoch: 1, MinimumUpdaterVersion: "v1.0.0", MinimumTunnelProtocol: 1, MaximumTunnelProtocol: 1,
			Artifacts: []*p2pstreamv1.AgentUpdateArtifact{{Os: artifact.OS, Arch: artifact.Arch, Name: artifact.Name, SizeBytes: int64(artifact.Size), Sha256: artifact.SHA256}}}
		targets[version] = target
		base := "/test/systemd/releases/download/" + version + "/"
		assets[base+artifact.Name], assets[base+"p2pstream_agent_update_manifest.json"] = body, data
	}
	app.TrustedAgentUpdates = systemdTestCatalog{targets: targets}
	mux := http.NewServeMux()
	app.RegisterManagementRoutes(mux)
	certificate, certPEM := systemdTestCertificate(t)
	var loseRollbackResponse atomic.Bool
	var droppedSuccessfulRollbackResponse atomic.Bool
	var rollbackReportsMu sync.Mutex
	var rollbackReports []*p2pstreamv1.ReportAgentUpdateRequest
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Host == "github.com" {
			if body, ok := assets[r.URL.Path]; ok {
				w.Header().Set("Content-Type", "application/octet-stream")
				w.Header().Set("Content-Length", fmt.Sprint(len(body)))
				_, _ = w.Write(body)
				return
			}
			http.NotFound(w, r)
			return
		}
		if strings.HasSuffix(r.URL.Path, "/ReportAgentUpdate") {
			body, err := io.ReadAll(io.LimitReader(r.Body, 256<<10))
			if err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			r.Body = io.NopCloser(bytes.NewReader(body))
			var report p2pstreamv1.ReportAgentUpdateRequest
			if proto.Unmarshal(body, &report) == nil && report.State == p2pstreamv1.AgentUpdaterReportState_AGENT_UPDATER_REPORT_STATE_ROLLED_BACK {
				rollbackReportsMu.Lock()
				rollbackReports = append(rollbackReports, proto.Clone(&report).(*p2pstreamv1.ReportAgentUpdateRequest))
				rollbackReportsMu.Unlock()
				if loseRollbackResponse.CompareAndSwap(true, false) {
					recorder := httptest.NewRecorder()
					mux.ServeHTTP(recorder, r)
					if recorder.Code == http.StatusOK {
						// Commit in real management, then lose its successful response.
						droppedSuccessfulRollbackResponse.Store(true)
						http.Error(w, "injected lost rollback response", http.StatusServiceUnavailable)
						return
					}
					for key, values := range recorder.Header() {
						w.Header()[key] = values
					}
					w.WriteHeader(recorder.Code)
					_, _ = w.Write(recorder.Body.Bytes())
					return
				}
			}
		}
		mux.ServeHTTP(w, r)
	}))
	_ = server.Listener.Close()
	server.Listener, err = net.Listen("tcp", "127.0.0.1:443")
	if err != nil {
		t.Fatal(err)
	}
	server.TLS = &tls.Config{Certificates: []tls.Certificate{certificate}, MinVersion: tls.VersionTLS12}
	server.EnableHTTP2 = true
	server.StartTLS()
	defer server.Close()
	hosts := systemdRead(t, "/etc/hosts")
	systemdWrite(t, "/etc/hosts", append(hosts, []byte("\n127.0.0.1 management.test github.com\n")...), 0644)
	defer systemdWrite(t, "/etc/hosts", hosts, 0644)
	systemdWrite(t, "/usr/local/share/ca-certificates/p2pstream-review.crt", certPEM, 0644)
	systemdCommand(t, ctx, "update-ca-certificates")

	token, _, err := app.createAgentUpdaterEnrollmentToken(ctx, agent.ID, 10*time.Minute, agentUpdateBootstrap{PinnedRepository: "test/systemd"})
	if err != nil {
		t.Fatal(err)
	}
	authority := app.AgentUpdateAuthority.Identity()
	install := exec.CommandContext(ctx, "bash", filepath.Join(root, "install-agent.sh"))
	install.Env = append(os.Environ(),
		"P2PSTREAM_VERSION=v1.0.0", "P2PSTREAM_AGENT_BINARY_FILE="+filepath.Join(root, "p2pstream-v1.0.0"),
		"P2PSTREAM_REPOSITORY=test/systemd", "MANAGEMENT_URL=https://management.test", "AGENT_ID="+agent.PublicID, "AGENT_TOKEN=test-token-"+agent.PublicID,
		"AGENT_ALLOW_TARGETS=127.0.0.1:9000", "P2PSTREAM_ENABLE_MANAGED_UPDATES=true", "P2PSTREAM_AGENT_UPDATE_CHANNEL=stable",
		"P2PSTREAM_UPDATER_ENROLLMENT_TOKEN="+token, "P2PSTREAM_AGENT_UPDATE_AUTHORITY_PUBLIC_KEY_BASE64="+base64.StdEncoding.EncodeToString(authority.PublicKey),
		"P2PSTREAM_AGENT_UPDATE_AUTHORITY_KEY_ID="+authority.KeyID, "P2PSTREAM_AGENT_UPDATE_AUTHORITY_EPOCH=1",
		"MANAGEMENT_CA_PEM_BASE64="+base64.StdEncoding.EncodeToString(certPEM))
	if output, err := install.CombinedOutput(); err != nil {
		t.Fatalf("real installer failed: %v\n%s", err, output)
	}
	// Drive the worker explicitly to keep the test bounded; the production
	// activation path and service hardening remain unchanged.
	systemdCommand(t, ctx, "systemctl", "stop", "p2pstream-updater.timer")
	waitForAgentHubConnection(t, app, agent.ID, true)
	originalEnv := systemdRead(t, "/etc/p2pstream/agent.env")

	startCampaign := func(version string) int64 {
		t.Helper()
		req := connect.NewRequest(&p2pstreamv1.CreateAgentUpdateCampaignRequest{Name: "systemd " + version, AgentIds: []int64{agent.ID}, Target: &p2pstreamv1.AgentUpdateTarget{ManifestSha256: targets[version].ManifestSha256}, Policy: &p2pstreamv1.AgentUpdatePolicy{MaxUnavailable: 1, MinimumEligibleAgentsPerRoute: 1, CanaryCount: 1, WaveSize: 1, HealthyDwellMillis: 10000}})
		req.Header().Set("Cookie", header.Get("Cookie"))
		response, err := app.CreateAgentUpdateCampaign(ctx, req)
		if err != nil {
			t.Fatalf("create %s campaign: %v", version, err)
		}
		return response.Msg.Campaign.Id
	}
	var lastWorkerStart time.Time
	waitCampaign := func(campaignID int64, want string) {
		t.Helper()
		deadline := time.Now().Add(90 * time.Second)
		last := ""
		for time.Now().Before(deadline) && ctx.Err() == nil {
			var state, action, failure string
			var cordoned int
			if err := database.QueryRow(`SELECT state,desired_action,failure_code,cordoned FROM agent_update_assignments WHERE campaign_id=?`, campaignID).Scan(&state, &action, &failure, &cordoned); err != nil {
				t.Fatal(err)
			}
			current := state + "/" + action + "/" + failure
			if current != last {
				t.Logf("campaign %d: %s", campaignID, current)
				last = current
			}
			if state == want && (want != "failed" || (action == "none" && cordoned == 0)) {
				return
			}
			if state == "blocked" {
				t.Fatalf("campaign blocked: %s", current)
			}
			if time.Since(lastWorkerStart) >= 5*time.Second {
				lastWorkerStart = time.Now()
				if output, err := exec.CommandContext(ctx, "systemctl", "start", "p2pstream-updater.service").CombinedOutput(); err != nil {
					if want != "failed" {
						t.Fatalf("real worker failed: %v\n%s", err, output)
					}
					// A single injected lost response should be recovered by the next
					// invocation. Persistent errors still fail the bounded phase below.
					t.Logf("rollback worker retry after failed invocation: %v", err)
				}
			}
			app.reconcileAgentUpdateMaintenance(ctx, time.Now().UTC())
			time.Sleep(250 * time.Millisecond)
		}
		t.Fatalf("campaign %d did not reach %s; last %s", campaignID, want, last)
	}
	assertLive := func(version string) {
		t.Helper()
		conn := app.AgentHub.connectedByID(agent.ID)
		if conn == nil || conn.BuildVersion != version || app.isAgentUpdateCordoned(agent.ID) {
			t.Fatalf("live tunnel did not prove %s or remained cordoned: %+v", version, conn)
		}
		pid := strings.TrimSpace(systemdCommand(t, ctx, "systemctl", "show", "p2pstream-agent", "--property=MainPID", "--value"))
		exe, err := os.Readlink("/proc/" + pid + "/exe")
		if err != nil || !strings.Contains(exe, "/slots/"+version+"/p2pstream") {
			t.Fatalf("running service executable = %s, %v", exe, err)
		}
		if string(originalEnv) != string(systemdRead(t, "/etc/p2pstream/agent.env")) {
			t.Fatal("rollout changed the existing agent token or network permissions")
		}
		systemdCommand(t, ctx, "runuser", "-u", "p2pstream-updater", "--", "test", "-r", "/var/lib/p2pstream-updater/floor.json")
		if result := strings.TrimSpace(systemdCommand(t, ctx, "systemctl", "show", "p2pstream-updater-activate.service", "--property=Result", "--value")); result != "success" {
			t.Fatalf("privileged helper did not finish successfully: %s", result)
		}
		t.Logf("verified real agent process %s, fresh tunnel, readable floor, unchanged configuration", version)
	}

	first := startCampaign("v1.1.0")
	waitCampaign(first, "succeeded")
	assertLive("v1.1.0")
	second := startCampaign("v1.2.0")
	waitCampaign(second, "healthy_dwell")
	// Exercise the installed .84 worker's actual report serialization against
	// current management while retaining the fixed root activator.
	if err := os.MkdirAll("/etc/systemd/system/p2pstream-updater.service.d", 0755); err != nil {
		t.Fatal(err)
	}
	systemdWrite(t, "/etc/systemd/system/p2pstream-updater.service.d/review.conf", []byte("[Service]\nExecStart=\nExecStart="+legacy+" updater stage\n"), 0644)
	systemdCommand(t, ctx, "systemctl", "daemon-reload")
	campaign, err := app.getAgentUpdateCampaignProto(ctx, second)
	if err != nil {
		t.Fatal(err)
	}
	cancelRequest := connect.NewRequest(&p2pstreamv1.ChangeAgentUpdateCampaignStateRequest{CampaignId: second, ExpectedGeneration: campaign.Generation})
	cancelRequest.Header().Set("Cookie", header.Get("Cookie"))
	loseRollbackResponse.Store(true)
	if _, err := app.CancelAgentUpdateCampaign(ctx, cancelRequest); err != nil {
		t.Fatal(err)
	}
	waitCampaign(second, "failed")
	systemdCommand(t, ctx, "systemctl", "start", "p2pstream-updater.service")
	rollbackReportsMu.Lock()
	if !droppedSuccessfulRollbackResponse.Load() {
		t.Error("lost-response injection did not replace a committed successful rollback response")
	}
	if len(rollbackReports) < 2 || rollbackReports[0].RootActionReceipt == nil || rollbackReports[1].RootActionReceipt == nil || !bytes.Equal(rollbackReports[0].RootActionReceipt.CanonicalPayload, rollbackReports[1].RootActionReceipt.CanonicalPayload) || rollbackReports[0].Counter >= rollbackReports[1].Counter {
		t.Errorf("lost response did not retry the exact root proof with a fresh worker counter (%d reports)", len(rollbackReports))
	}
	if len(rollbackReports) > 0 {
		legacyReport := rollbackReports[0]
		if legacyReport.BinarySha256 != "" || legacyReport.ManifestSha256 != "" || legacyReport.RunningVersion != "" || legacyReport.RunningCommit != "" || legacyReport.RootActionReceipt == nil || len(legacyReport.RootActionReceipt.CanonicalPayload) == 0 {
			t.Error("legacy worker fixture did not exercise the empty rollback envelope with signed proof")
		}
	}
	rollbackReportsMu.Unlock()
	if _, err := os.Stat("/var/lib/p2pstream-updater/staging/rollback-result.json"); !os.IsNotExist(err) {
		t.Fatalf("acknowledged result still blocks polling: %v", err)
	}
	assertLive("v1.1.0")
	if err := os.Remove("/etc/systemd/system/p2pstream-updater.service.d/review.conf"); err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	systemdCommand(t, ctx, "systemctl", "daemon-reload")
	// Reproduce a previously exhausted crash budget. Only the fault injection
	// resets counters here; recovery below must be performed by the installer.
	systemdCommand(t, ctx, "systemctl", "stop", "p2pstream-updater-activate.path", "p2pstream-updater-activate.service")
	failureDropIn := "/etc/systemd/system/p2pstream-updater-activate.service.d/review-failure.conf"
	if err := os.MkdirAll(filepath.Dir(failureDropIn), 0755); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = os.Remove(failureDropIn)
		_ = exec.Command("systemctl", "daemon-reload").Run()
	}()
	failureCount := "/var/lib/p2pstream-updater/root/review-failure-count"
	failingHelper := filepath.Join(root, "failing-helper.sh")
	systemdWrite(t, failingHelper, []byte("#!/bin/sh\nprintf x >>"+failureCount+"\nexit 1\n"), 0755)
	systemdWrite(t, failureDropIn, []byte("[Service]\nExecStart=\nExecStart=/bin/sh "+failingHelper+"\nExecStartPost=\nExecStopPost=\nRestart=no\n"), 0644)
	systemdCommand(t, ctx, "systemctl", "daemon-reload")
	systemdCommand(t, ctx, "systemctl", "reset-failed", "p2pstream-updater-activate.service")
	for i := 0; i < 6; i++ {
		if err := exec.CommandContext(ctx, "systemctl", "start", "p2pstream-updater-activate.service").Run(); err == nil {
			t.Fatal("injected failing helper unexpectedly succeeded")
		}
	}
	// systemd may retain Result=exit-code after refusing a subsequent start.
	// Count actual executions to prove the sixth attempt was rate-limited.
	if executions := len(systemdRead(t, failureCount)); executions != 5 {
		t.Fatalf("crash throttle allowed %d executions for six failed starts; want five", executions)
	}
	if err := os.Remove(failureDropIn); err != nil {
		t.Fatal(err)
	}
	systemdCommand(t, ctx, "systemctl", "daemon-reload")
	// Repair the pinned rescue runner without supplying/rotating the existing
	// agent token or replacing its recovered live binary and destination policy.
	repairToken, _, err := app.createAgentUpdaterEnrollmentToken(ctx, agent.ID, 10*time.Minute, agentUpdateBootstrap{PinnedRepository: "test/systemd"})
	if err != nil {
		t.Fatal(err)
	}
	repair := exec.CommandContext(ctx, "bash", filepath.Join(root, "install-agent.sh"))
	for _, value := range install.Env {
		if strings.HasPrefix(value, "AGENT_TOKEN=") || strings.HasPrefix(value, "P2PSTREAM_VERSION=") || strings.HasPrefix(value, "P2PSTREAM_AGENT_BINARY_FILE=") || strings.HasPrefix(value, "P2PSTREAM_UPDATER_ENROLLMENT_TOKEN=") {
			continue
		}
		repair.Env = append(repair.Env, value)
	}
	repair.Env = append(repair.Env, "P2PSTREAM_VERSION=v1.1.0", "P2PSTREAM_AGENT_BINARY_FILE="+filepath.Join(root, "p2pstream-v1.1.0"), "P2PSTREAM_UPDATER_ENROLLMENT_TOKEN="+repairToken)
	if output, err := repair.CombinedOutput(); err != nil {
		t.Fatalf("real pinned-updater repair failed: %v\n%s", err, output)
	}
	if sha256.Sum256(systemdRead(t, "/opt/p2pstream-agent/updater/p2pstream")) != sha256.Sum256(systemdRead(t, filepath.Join(root, "p2pstream-v1.1.0"))) {
		t.Fatal("repair did not replace the pinned updater with the selected verified binary")
	}
	var enrolledUpdaterVersion string
	if err := database.QueryRow(`SELECT updater_version FROM agent_updater_identities WHERE agent_id=?`, agent.ID).Scan(&enrolledUpdaterVersion); err != nil || enrolledUpdaterVersion != "v1.1.0" {
		t.Fatalf("repair did not promote enrolled updater version: %q, %v", enrolledUpdaterVersion, err)
	}
	systemdCommand(t, ctx, "systemctl", "stop", "p2pstream-updater.timer")
	waitForAgentHubConnection(t, app, agent.ID, true)
	assertLive("v1.1.0")
	third := startCampaign("v1.2.0")
	waitCampaign(third, "succeeded")
	assertLive("v1.2.0")
	t.Log("PASS: real install/enrollment, two upgrades, signed cancellation rollback, legacy worker report/lost-response retry, crash throttle, pinned-updater repair, and exact-target retry")
}

type systemdTestCatalog struct {
	targets map[string]*p2pstreamv1.AgentUpdateTarget
}

func (c systemdTestCatalog) ListTrustedAgentUpdateTargets(context.Context) ([]*p2pstreamv1.AgentUpdateTarget, error) {
	var targets []*p2pstreamv1.AgentUpdateTarget
	for _, target := range c.targets {
		targets = append(targets, proto.Clone(target).(*p2pstreamv1.AgentUpdateTarget))
	}
	return targets, nil
}

func (c systemdTestCatalog) ResolveTrustedAgentUpdateTarget(_ context.Context, digest string) (*p2pstreamv1.AgentUpdateTarget, error) {
	for _, target := range c.targets {
		if target.ManifestSha256 == digest {
			return proto.Clone(target).(*p2pstreamv1.AgentUpdateTarget), nil
		}
	}
	return nil, fmt.Errorf("unknown test manifest %s", digest)
}

func systemdTestCertificate(t *testing.T) (tls.Certificate, []byte) {
	t.Helper()
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	cert := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "disposable update test"}, DNSNames: []string{"management.test", "github.com"}, IPAddresses: []net.IP{net.ParseIP("127.0.0.1")}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	der, err := x509.CreateCertificate(rand.Reader, cert, cert, public, private)
	if err != nil {
		t.Fatal(err)
	}
	key, err := x509.MarshalPKCS8PrivateKey(private)
	if err != nil {
		t.Fatal(err)
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	pair, err := tls.X509KeyPair(certPEM, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: key}))
	if err != nil {
		t.Fatal(err)
	}
	return pair, certPEM
}

func systemdRead(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func systemdWrite(t *testing.T, path string, data []byte, mode os.FileMode) {
	t.Helper()
	if err := os.WriteFile(path, data, mode); err != nil {
		t.Fatal(err)
	}
}

func systemdCommand(t *testing.T, ctx context.Context, name string, args ...string) string {
	t.Helper()
	output, err := exec.CommandContext(ctx, name, args...).CombinedOutput()
	if err != nil {
		t.Fatalf("%s %v: %v\n%s", name, args, err, output)
	}
	return string(output)
}
