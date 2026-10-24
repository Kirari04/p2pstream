package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	p2pstreamv1 "p2pstream/gen/proto/p2pstream/v1"
	"p2pstream/internal/buildinfo"
	"p2pstream/internal/config"
	"p2pstream/internal/serverupdate"
)

const preflightInstallationID = "11111111-1111-4111-8111-111111111111"

func newEnrollmentPreflightApp(t *testing.T) *App {
	t.Helper()
	t.Setenv("DATABASE_URL", "")
	app := NewApp(&config.Config{ConfigDir: "/data"}, newServerTestDB(t))
	app.InstallationID = preflightInstallationID
	return app
}

func enrollmentRuntimeClient(t *testing.T, app *App, enrolledRuntime ...bool) (*http.Client, string, context.CancelFunc) {
	t.Helper()
	socket := filepath.Join(t.TempDir(), "runtime.sock")
	ctx, cancel := context.WithCancel(context.Background())
	legacy := len(enrolledRuntime) > 0 && enrolledRuntime[0]
	if err := app.listenServerUpdateRuntime(ctx, socket, !legacy); err != nil {
		cancel()
		t.Fatal(err)
	}
	return enrollmentSocketClient(t, socket, cancel), socket, cancel
}

func enrollmentSocketClient(t *testing.T, socket string, cancel context.CancelFunc) *http.Client {
	t.Helper()
	transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", socket)
	}}
	t.Cleanup(func() {
		transport.CloseIdleConnections()
		cancel()
		deadline := time.Now().Add(time.Second)
		for time.Now().Before(deadline) {
			if _, err := os.Lstat(socket); errors.Is(err, os.ErrNotExist) {
				return
			}
			time.Sleep(time.Millisecond)
		}
		t.Error("runtime socket survived shutdown")
	})
	return &http.Client{Transport: transport, Timeout: time.Second}
}

func runtimeRequest(t *testing.T, client *http.Client, method, path string) (int, []byte) {
	t.Helper()
	req, err := http.NewRequest(method, "http://server"+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	r, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Body.Close()
	body, err := io.ReadAll(r.Body)
	if err != nil {
		t.Fatal(err)
	}
	return r.StatusCode, body
}

func TestStandaloneEnrollmentPreflightReadOnlyAndSocketLifecycle(t *testing.T) {
	app := newEnrollmentPreflightApp(t)
	app.Config.ServerUpdateGateFile = filepath.Join(t.TempDir(), "maintenance")
	// Holding the mutation barrier must not delay observational preflight.
	app.serverUpdateMu.RLock()
	defer app.serverUpdateMu.RUnlock()
	client, socket, cancel := enrollmentRuntimeClient(t, app)
	info, err := os.Lstat(socket)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("private socket mode: %v %v", info, err)
	}
	if owner := info.Sys().(*syscall.Stat_t).Uid; owner != uint32(os.Geteuid()) {
		t.Fatalf("socket owner = %d", owner)
	}
	status, body := runtimeRequest(t, client, http.MethodPost, "/enrollment-preflight")
	p, err := serverupdate.DecodeEnrollmentPreflight(strings.NewReader(string(body)))
	if status != 200 || err != nil || !p.Ready || p.InstallationID != app.InstallationID || p.Version != buildinfo.Version || p.Commit != buildinfo.Commit {
		t.Fatalf("standalone preflight = %s, %v", body, err)
	}
	for _, path := range []string{"/status", "/prepare", "/unknown"} {
		if status, _ := runtimeRequest(t, client, http.MethodPost, path); status != 404 {
			t.Fatalf("standalone %s available: %d", path, status)
		}
	}
	if status, _ := runtimeRequest(t, client, http.MethodGet, "/enrollment-preflight"); status != 404 {
		t.Fatal("preflight accepts GET")
	}
	if _, err := os.Lstat(app.Config.ServerUpdateGateFile); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("read-only query created maintenance gate: %v", err)
	}
	if err := app.listenServerUpdateRuntime(context.Background(), socket, true); err == nil {
		t.Fatal("second server stole a live runtime socket")
	}
	cancel()
}

func TestIndependentStandaloneEnrollmentSockets(t *testing.T) {
	first, second := newEnrollmentPreflightApp(t), newEnrollmentPreflightApp(t)
	first.InstallationID, second.InstallationID = uuid.NewString(), uuid.NewString()
	var clients []*http.Client
	var sockets []string
	var cancelFirst context.CancelFunc
	for _, app := range []*App{first, second} {
		ctx, cancel := context.WithCancel(context.Background())
		t.Cleanup(cancel)
		if err := app.StartServerUpdateRuntime(ctx); err != nil {
			t.Fatal(err)
		}
		socket, err := serverupdate.EnrollmentPreflightSocket(app.InstallationID)
		if err != nil {
			t.Fatal(err)
		}
		clients = append(clients, enrollmentSocketClient(t, socket, cancel))
		sockets = append(sockets, socket)
		if cancelFirst == nil {
			cancelFirst = cancel
		}
	}
	for i, app := range []*App{first, second} {
		code, body := runtimeRequest(t, clients[i], http.MethodPost, "/enrollment-preflight")
		p, err := serverupdate.DecodeEnrollmentPreflight(strings.NewReader(string(body)))
		if code != 200 || err != nil || !p.Ready || p.InstallationID != app.InstallationID {
			t.Fatalf("independent server %d: %s %v", i, body, err)
		}
	}
	before, err := os.Lstat(sockets[1])
	if err != nil {
		t.Fatal(err)
	}
	cancelFirst()
	deadline := time.Now().Add(time.Second)
	for {
		if _, err := os.Lstat(sockets[0]); errors.Is(err, os.ErrNotExist) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("first standalone did not release its socket")
		}
		time.Sleep(time.Millisecond)
	}
	after, err := os.Lstat(sockets[1])
	if err != nil || !os.SameFile(before, after) {
		t.Fatalf("first standalone removed second socket: %v", err)
	}
	if code, _ := runtimeRequest(t, clients[1], http.MethodPost, "/enrollment-preflight"); code != 200 {
		t.Fatal("second standalone stopped serving")
	}
}

func TestStandaloneInvalidIdentityKeepsServingWithoutPreflight(t *testing.T) {
	for _, id := range []string{"", "not-an-id", uuid.Nil.String()} {
		app := newEnrollmentPreflightApp(t)
		app.InstallationID = id
		if err := app.StartServerUpdateRuntime(context.Background()); err != nil {
			t.Fatalf("invalid identity stopped standalone server: %v", err)
		}
		assertPreflightCodes(t, app.serverEnrollmentPreflight(context.Background()), "installation_identity_unavailable")
	}
}

func TestEnrolledRuntimeStatusAndPrepareCompatibility(t *testing.T) {
	app := newEnrollmentPreflightApp(t)
	app.Config.ServerUpdateSocket = "/private/control.sock"
	app.Config.ServerUpdateInstanceID = "22222222-2222-4222-8222-222222222222"
	app.Config.ServerUpdateGateFile = filepath.Join(t.TempDir(), "maintenance")
	if err := os.WriteFile(app.Config.ServerUpdateGateFile, []byte("active"), 0600); err != nil {
		t.Fatal(err)
	}
	before, _ := os.Stat(app.Config.ServerUpdateGateFile)
	client, _, _ := enrollmentRuntimeClient(t, app, true)
	want := app.serverUpdateRuntimeStatus(context.Background())
	for _, path := range []string{"/status", "/prepare"} {
		code, body := runtimeRequest(t, client, http.MethodPost, path)
		var got serverupdate.RuntimeStatus
		if code != 200 || json.Unmarshal(body, &got) != nil || !reflect.DeepEqual(got, want) {
			t.Fatalf("%s status changed: %s", path, body)
		}
	}
	if code, _ := runtimeRequest(t, client, http.MethodPost, "/enrollment-preflight"); code != http.StatusNotFound {
		t.Fatal("legacy runtime socket exposed enrollment preflight")
	}
	preflightClient, _, _ := enrollmentRuntimeClient(t, app)
	for _, path := range []string{"/status", "/prepare"} {
		if code, _ := runtimeRequest(t, preflightClient, http.MethodPost, path); code != http.StatusNotFound {
			t.Fatalf("enrolled preflight socket exposed %s", path)
		}
	}
	_, body := runtimeRequest(t, preflightClient, http.MethodPost, "/enrollment-preflight")
	p, err := serverupdate.DecodeEnrollmentPreflight(strings.NewReader(string(body)))
	if err != nil || p.Ready || p.InstallationID != app.InstallationID || p.InstallationID == app.Config.ServerUpdateInstanceID {
		t.Fatalf("installation binding lost: %s %v", body, err)
	}
	assertPreflightCodes(t, p, "maintenance_active")
	after, _ := os.Stat(app.Config.ServerUpdateGateFile)
	if !os.SameFile(before, after) || !before.ModTime().Equal(after.ModTime()) {
		t.Fatal("preflight changed existing gate")
	}
	if !app.serverUpdateMu.TryLock() {
		t.Fatal("prepare left barrier held")
	}
	app.serverUpdateMu.Unlock()
}

func assertPreflightCodes(t *testing.T, p serverupdate.EnrollmentPreflight, want ...string) {
	t.Helper()
	got := map[string]bool{}
	for _, b := range p.Blockers {
		if got[b.Code] || b != serverupdate.EnrollmentBlockerForCode(b.Code) {
			t.Fatalf("unbounded or unknown blocker: %+v", b)
		}
		got[b.Code] = true
	}
	if p.Ready {
		t.Fatal("blocked preflight claims ready")
	}
	for _, code := range want {
		if !got[code] {
			t.Errorf("missing %s in %+v", code, p.Blockers)
		}
	}
}

func TestEnrollmentPreflightAllRuntimeBlockers(t *testing.T) {
	app := newEnrollmentPreflightApp(t)
	app.Config.ConfigDir = "/custom-data"
	app.Config.PublicCacheDir = "/custom-cache/secret"
	app.ManagementTLS = &ManagementTLSRuntime{state: managementTLSRotationDiskState{Phase: "rotating"}}
	app.publicSnapshot = &publicProxySnapshot{Listeners: map[int64]publicListenerConfig{1: {ID: 1, Enabled: true}, 2: {ID: 2, Enabled: true}, 3: {ID: 3}}}
	if _, err := app.DB.Exec("DROP TABLE goose_db_version"); err != nil {
		t.Fatal(err)
	}
	agent := createAgentUpdateTestAgent(t, app.DB, "private-agent-id")
	insertAgentUpdateTestCampaign(t, app.DB, agent.ID, "pending", "stage", false)
	p := app.serverEnrollmentPreflight(context.Background())
	assertPreflightCodes(t, p, "database_schema_unavailable", "agent_updates_active", "certificate_rotation_active", "public_listener_unavailable", "custom_database_path", "custom_state_path", "runtime_unavailable")
	if len(p.Blockers) != 7 {
		t.Fatalf("unexpected blockers: %+v", p.Blockers)
	}
	encoded, _ := json.Marshal(p)
	if strings.Contains(string(encoded), "private-agent-id") || strings.Contains(string(encoded), "/custom-cache/secret") {
		t.Fatalf("preflight leaked details: %s", encoded)
	}
}

func TestEnrollmentPreflightCancelledCampaignAndRollback(t *testing.T) {
	for _, test := range []struct {
		name, state, action string
		blocked             bool
	}{
		{"active", "pending", "stage", true},
		{"cancelled campaign blocked assignment", "blocked", "none", true},
		{"cancelled campaign blocked rollback pending", "blocked", "rollback", true},
		{"cancelled campaign rollback pending", "failed", "rollback", true},
		{"terminal cancelled assignment ignores obsolete rollback", "cancelled", "rollback", false},
		{"terminal succeeded assignment ignores obsolete rollback", "succeeded", "rollback", false},
		{"completed rollback", "failed", "none", false},
		{"completed cancelled assignment", "cancelled", "none", false},
		{"completed update", "succeeded", "none", false},
	} {
		t.Run(test.name, func(t *testing.T) {
			app := newEnrollmentPreflightApp(t)
			agent := createAgentUpdateTestAgent(t, app.DB, "agent")
			campaignID, assignmentID := insertAgentUpdateTestCampaign(t, app.DB, agent.ID, test.state, test.action, test.blocked)
			if _, err := app.DB.Exec("UPDATE agent_update_campaigns SET state='cancelled' WHERE id=?", campaignID); err != nil {
				t.Fatal(err)
			}
			assignment, _, err := app.activeAgentUpdateAssignment(context.Background(), agent.ID)
			if test.blocked && (err != nil || assignment.ID != assignmentID) || !test.blocked && !errors.Is(err, sql.ErrNoRows) {
				t.Fatalf("engine active assignment disagrees: %+v %v", assignment, err)
			}
			admin := createTestAdminSession(t, app)
			req := connect.NewRequest(&p2pstreamv1.GetAgentUpdateOverviewRequest{})
			req.Header().Set("Cookie", admin.Get("Cookie"))
			overview, err := app.GetAgentUpdateOverview(context.Background(), req)
			if err != nil || len(overview.Msg.Agents) != 1 || (overview.Msg.Agents[0].ActiveAssignmentId != 0) != test.blocked {
				t.Fatalf("agent overview disagrees: %v %v", overview, err)
			}
			p := app.serverEnrollmentPreflight(context.Background())
			if p.Ready == test.blocked {
				t.Fatalf("readiness = %v, blockers %+v", p.Ready, p.Blockers)
			}
			if test.blocked {
				assertPreflightCodes(t, p, "agent_updates_active")
				if !strings.Contains(p.Blockers[0].Message, "pending agent rollback recovery") {
					t.Fatal("operator action omits rollback recovery")
				}
			}
			var state, action string
			if err := app.DB.QueryRow("SELECT state,desired_action FROM agent_update_assignments WHERE id=?", assignmentID).Scan(&state, &action); err != nil || state != test.state || action != test.action {
				t.Fatalf("preflight mutated assignment: %s %s %v", state, action, err)
			}
		})
	}
}

func TestEnrollmentPreflightAfterCommittedRollbackCompletion(t *testing.T) {
	app := newEnrollmentPreflightApp(t)
	agent := createAgentUpdateTestAgent(t, app.DB, "rollback-agent")
	campaignID, assignmentID := insertAgentUpdateTestCampaign(t, app.DB, agent.ID, "awaiting_tunnel", "none", true)
	completedAt := time.Now().UTC().Add(-time.Second).Truncate(time.Millisecond)
	commit := strings.Repeat("d", 40)
	// Seed the persisted receipt edge, then exercise the real fresh-tunnel
	// reconciliation and its committed failed/none/uncordoned transition.
	if _, err := app.DB.Exec(`UPDATE agent_update_assignments SET authorization_action='rollback',root_action_completed_at=?,root_result_kind='release',root_result_version='v1.0.0',root_result_commit=? WHERE id=?`, completedAt, commit, assignmentID); err != nil {
		t.Fatal(err)
	}
	if _, err := app.DB.Exec("UPDATE agent_update_campaigns SET state='cancelled' WHERE id=?", campaignID); err != nil {
		t.Fatal(err)
	}
	app.setAgentUpdateCordon(agent.ID)
	assertPreflightCodes(t, app.serverEnrollmentPreflight(context.Background()), "agent_updates_active")
	conn := &AgentConn{AgentID: agent.ID, PublicID: agent.PublicID, Done: make(chan struct{}), ConnectedAt: completedAt.Add(time.Millisecond), BuildVersion: "v1.0.0", BuildCommit: commit}
	if err := app.AgentHub.connect(conn); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { app.AgentHub.disconnect(conn) })
	app.recordAgentUpdateFreshTunnel(conn)
	var state, action string
	var cordoned int
	if err := app.DB.QueryRow("SELECT state,desired_action,cordoned FROM agent_update_assignments WHERE id=?", assignmentID).Scan(&state, &action, &cordoned); err != nil || state != "failed" || action != "none" || cordoned != 0 {
		t.Fatalf("rollback did not commit terminal state: %s %s %d %v", state, action, cordoned, err)
	}
	if p := app.serverEnrollmentPreflight(context.Background()); !p.Ready || len(p.Blockers) != 0 {
		t.Fatalf("completed rollback still blocks enrollment: %+v", p)
	}
}

func TestEnrollmentPreflightFailsClosedMissingConfigurationAndDatabase(t *testing.T) {
	app := &App{}
	p := app.serverEnrollmentPreflight(context.Background())
	assertPreflightCodes(t, p, "configuration_unavailable", "database_schema_unavailable", "agent_updates_active", "installation_identity_unavailable", "runtime_unavailable")
	client, _, _ := enrollmentRuntimeClient(t, app)
	code, body := runtimeRequest(t, client, http.MethodPost, "/enrollment-preflight")
	if code != 200 {
		t.Fatalf("missing config preflight unavailable: %s", body)
	}
	if _, err := serverupdate.DecodeEnrollmentPreflight(strings.NewReader(string(body))); err != nil {
		t.Fatal(err)
	}
	app = newEnrollmentPreflightApp(t)
	_ = app.DB.Close()
	assertPreflightCodes(t, app.serverEnrollmentPreflight(context.Background()), "database_schema_unavailable", "agent_updates_active", "runtime_unavailable")
}

func TestUnenrolledOverviewIncludesPreflightAndPreservesSetup(t *testing.T) {
	app := newEnrollmentPreflightApp(t)
	app.installationChecked, app.installationExpires = time.Now(), time.Now().Add(time.Hour)
	app.installationCommand = "verified-release-setup-command"
	app.ManagementTLS = &ManagementTLSRuntime{state: managementTLSRotationDiskState{Phase: "rotating"}}
	admin := createTestAdminSession(t, app)
	req := connect.NewRequest(&p2pstreamv1.GetServerUpdateOverviewRequest{})
	req.Header().Set("Cookie", admin.Get("Cookie"))
	got, err := app.GetServerUpdateOverview(context.Background(), req)
	if err != nil || got.Msg.ExecutorConfigured || got.Msg.SetupCommand != app.installationCommand || got.Msg.SetupUnavailable != "" {
		t.Fatalf("setup handling changed: %v %v", got, err)
	}
	p := app.serverEnrollmentPreflight(context.Background())
	if len(got.Msg.Blockers) != len(p.Blockers) || got.Msg.Blockers[0] != p.Blockers[0].Message {
		t.Fatalf("overview readiness mismatch: %+v", got.Msg.Blockers)
	}
}

func TestEnrollmentRuntimeSocketUnsafeAndStalePaths(t *testing.T) {
	app := newEnrollmentPreflightApp(t)
	path := filepath.Join(t.TempDir(), "runtime.sock")
	if err := os.WriteFile(path, []byte("preserve me"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := app.listenServerUpdateRuntime(context.Background(), path, true); err == nil {
		t.Fatal("accepted regular file at socket path")
	}
	data, _ := os.ReadFile(path)
	if string(data) != "preserve me" {
		t.Fatal("overwrote socket-path file")
	}
	_ = os.Remove(path)
	if err := os.Symlink(filepath.Join(t.TempDir(), "missing"), path); err != nil {
		t.Fatal(err)
	}
	if err := app.listenServerUpdateRuntime(context.Background(), path, true); err == nil {
		t.Fatal("accepted socket symlink")
	}
	_ = os.Remove(path)
	l, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	l.SetUnlinkOnClose(false)
	_ = l.Close()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	if err := app.startServerUpdateRuntime(ctx, path); err != nil {
		t.Fatalf("stale owned socket not recovered: %v", err)
	}
	// Simulate a replacement after this server's socket was externally removed.
	_ = os.Remove(path)
	if err := os.WriteFile(path, []byte("replacement"), 0600); err != nil {
		t.Fatal(err)
	}
	cancel()
	time.Sleep(20 * time.Millisecond)
	if data, err := os.ReadFile(path); err != nil || string(data) != "replacement" {
		t.Fatalf("shutdown removed replacement: %q %v", data, err)
	}
}

func TestStandaloneReadOnlyFilesystemKeepsServingWithoutPreflight(t *testing.T) {
	// /sys is a read-only filesystem on Linux test hosts, modelling a standalone
	// read_only container without /tmp tmpfs. Never alter its mount or permissions.
	path := filepath.Join("/sys", "p2pstream-preflight-test.sock")
	app := newEnrollmentPreflightApp(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	err := app.listenServerUpdateRuntime(ctx, path, true)
	if !errors.Is(err, syscall.EROFS) && !errors.Is(err, os.ErrPermission) {
		t.Skipf("no read-only test filesystem: %v", err)
	}
	if err := app.startServerUpdateRuntime(context.Background(), path); err != nil {
		t.Fatalf("standalone read-only deployment stopped: %v", err)
	}
	app.Config.ServerUpdateSocket = "/private/control.sock"
	if err := app.startServerUpdateRuntime(context.Background(), path); err == nil {
		t.Fatal("enrolled runtime silently lost mandatory socket")
	}
}

// Exercise the public startup entry point used by cmd/server, rather than just
// accepting errors from the private listener helper. An unavailable optional
// observer must never prevent a standalone server from starting.
func TestStandalonePreflightConflictsKeepStartupAvailable(t *testing.T) {
	for _, kind := range []string{"regular file", "symlink", "live listener", "wrong owner"} {
		t.Run(kind, func(t *testing.T) {
			app := newEnrollmentPreflightApp(t)
			app.InstallationID = uuid.NewString()
			path, err := serverupdate.EnrollmentPreflightSocket(app.InstallationID)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = os.Remove(path) })
			var live *net.UnixListener
			switch kind {
			case "regular file":
				err = os.WriteFile(path, []byte("preserve existing file"), 0600)
			case "symlink":
				err = os.Symlink(filepath.Join(t.TempDir(), "missing"), path)
			default:
				live, err = net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
				if err != nil {
					t.Fatal(err)
				}
				live.SetUnlinkOnClose(false)
				t.Cleanup(func() { _ = live.Close() })
				if kind == "wrong owner" {
					if os.Geteuid() != 0 {
						t.Skip("chown requires root; unsafe UID is also covered by the socket metadata fixture")
					}
					err = os.Chown(path, 1, -1)
				}
			}
			if err != nil {
				t.Fatal(err)
			}
			before, err := os.Lstat(path)
			if err != nil {
				t.Fatal(err)
			}
			if err := app.listenServerUpdateRuntime(context.Background(), path, true); err == nil {
				t.Fatal("conflicting socket unexpectedly accepted")
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			startupErr := app.StartServerUpdateRuntime(ctx)
			cancel()
			after, err := os.Lstat(path)
			if err != nil || !os.SameFile(before, after) || before.Mode() != after.Mode() {
				t.Fatalf("startup unlinked or replaced existing path: %v", err)
			}
			if kind == "regular file" {
				data, err := os.ReadFile(path)
				if err != nil || string(data) != "preserve existing file" {
					t.Fatalf("existing file changed: %q %v", data, err)
				}
			}
			if live != nil {
				conn, err := net.DialTimeout("unix", path, time.Second)
				if err != nil {
					t.Fatalf("existing listener stopped: %v", err)
				}
				_ = conn.Close()
			}
			// Enrollment continues to require a usable observation socket.
			app.Config.ServerUpdateSocket = "/private/control.sock"
			if err := app.startServerUpdateRuntime(context.Background(), path); err == nil {
				t.Fatal("enrolled preflight ignored conflicting socket")
			}
			if startupErr != nil {
				t.Fatalf("standalone startup rejected optional preflight conflict: %v", startupErr)
			}
		})
	}
}

func TestStandalonePublicStartupRecoversStaleOwnedPreflightSocket(t *testing.T) {
	app := newEnrollmentPreflightApp(t)
	app.InstallationID = uuid.NewString()
	path, err := serverupdate.EnrollmentPreflightSocket(app.InstallationID)
	if err != nil {
		t.Fatal(err)
	}
	stale, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	stale.SetUnlinkOnClose(false)
	_ = stale.Close()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	if err := app.StartServerUpdateRuntime(ctx); err != nil {
		t.Fatal(err)
	}
	client := enrollmentSocketClient(t, path, cancel)
	code, body := runtimeRequest(t, client, http.MethodPost, "/enrollment-preflight")
	preflight, err := serverupdate.DecodeEnrollmentPreflight(strings.NewReader(string(body)))
	if code != 200 || err != nil || !preflight.Ready || preflight.InstallationID != app.InstallationID {
		t.Fatalf("stale socket recovery: %s %v", body, err)
	}
}

func TestServerUpdateRuntimeStartupPreservesCancellation(t *testing.T) {
	for _, enrolled := range []bool{false, true} {
		app := newEnrollmentPreflightApp(t)
		app.InstallationID = uuid.NewString()
		if enrolled {
			app.Config.ServerUpdateSocket = "/private/control.sock"
		}
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if err := app.StartServerUpdateRuntime(ctx); !errors.Is(err, context.Canceled) {
			t.Fatalf("enrolled=%v startup cancellation = %v", enrolled, err)
		}
	}
}

// The metadata fixture exercises the exact ownership guard on unprivileged
// hosts that cannot chown the public-startup socket fixture above.
type enrollmentSocketInfoFixture struct {
	os.FileInfo
	metadata any
}

func (f enrollmentSocketInfoFixture) Sys() any { return f.metadata }

func TestEnrollmentPreflightSocketOwnershipFixture(t *testing.T) {
	path := filepath.Join(t.TempDir(), "owned.sock")
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := validateServerUpdateRuntimeSocket(info); err != nil {
		t.Fatalf("owned socket rejected: %v", err)
	}
	otherOwner := *info.Sys().(*syscall.Stat_t)
	otherOwner.Uid = uint32(os.Geteuid()) + 1
	for _, metadata := range []any{&otherOwner, nil, (*syscall.Stat_t)(nil)} {
		if err := validateServerUpdateRuntimeSocket(enrollmentSocketInfoFixture{info, metadata}); err == nil {
			t.Fatalf("unsafe socket metadata accepted: %v", metadata)
		}
	}
}

func TestEnrolledPublicStartupRequiresRuntimeSocket(t *testing.T) {
	// The enrolled protocol has a fixed path. Only create an exclusive fixture;
	// never disturb a running local server or another test's existing socket.
	file, err := os.OpenFile(serverupdate.RuntimeSocket, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if errors.Is(err, os.ErrExist) {
		t.Skip("enrolled runtime socket path is already occupied")
	}
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Remove(serverupdate.RuntimeSocket) })
	if _, err := file.WriteString("preserve enrolled path"); err != nil {
		t.Fatal(err)
	}
	_ = file.Close()
	before, err := os.Lstat(serverupdate.RuntimeSocket)
	if err != nil {
		t.Fatal(err)
	}
	app := newEnrollmentPreflightApp(t)
	app.Config.ServerUpdateSocket = "/private/control.sock"
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := app.StartServerUpdateRuntime(ctx); err == nil {
		t.Fatal("enrolled public startup ignored mandatory runtime failure")
	}
	after, err := os.Lstat(serverupdate.RuntimeSocket)
	if err != nil || !os.SameFile(before, after) {
		t.Fatalf("enrolled startup replaced conflicting path: %v", err)
	}
	data, err := os.ReadFile(serverupdate.RuntimeSocket)
	if err != nil || string(data) != "preserve enrolled path" {
		t.Fatalf("enrolled startup changed existing file: %q %v", data, err)
	}
}
