package cmd

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"p2pstream/internal/buildinfo"
	"p2pstream/internal/config"
	"p2pstream/internal/server"
	"p2pstream/internal/serverupdate"
)

func TestServerEnrollmentPreflightCommandLocalReadOnly(t *testing.T) {
	command, _, err := rootCmd.Find([]string{"server-enrollment-preflight"})
	if err != nil || command.Name() != "server-enrollment-preflight" || command.Args(command, []string{"prepare"}) == nil {
		t.Fatalf("preflight command must accept no arguments: %v", err)
	}
	// Merely discovering/reporting capabilities must not load configuration or
	// open/create the application database, even in a read-only data directory.
	dir := t.TempDir()
	t.Setenv("CONFIG_DIR", filepath.Join(dir, "must-not-create"))
	capabilityCommand, _, err := rootCmd.Find([]string{"server-enrollment-capabilities"})
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	capabilityCommand.SetOut(&out)
	t.Cleanup(func() { capabilityCommand.SetOut(nil) })
	if err := capabilityCommand.RunE(capabilityCommand, nil); err != nil {
		t.Fatal(err)
	}
	var capability serverupdate.DeploymentCapabilities
	if json.Unmarshal(out.Bytes(), &capability) != nil || capability.RuntimeGuard != 2 || capability.EnrollmentPreflight != 1 || capability.CanonicalCompose != 1 {
		t.Fatalf("capabilities = %s", out.Bytes())
	}
	if entries, err := os.ReadDir(dir); err != nil || len(entries) != 0 {
		t.Fatalf("capability command touched application data: %v %v", entries, err)
	}
}

func TestDeploymentCapabilityResponseAcceptedByPinnedExecutor(t *testing.T) {
	command, _, err := rootCmd.Find([]string{"server-deployment-capabilities"})
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	command.SetOut(&out)
	t.Cleanup(func() { command.SetOut(nil) })
	if err := command.RunE(command, nil); err != nil {
		t.Fatal(err)
	}
	// This is the exact strict reader shape in the already published protocol-2
	// executor. Test old-reader/new-response, not just the reverse direction.
	var legacy struct {
		RuntimeGuard int    `json:"runtime_guard"`
		Version      string `json:"version"`
		Commit       string `json:"commit"`
	}
	decoder := json.NewDecoder(&out)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&legacy); err != nil || legacy.RuntimeGuard != 2 {
		t.Fatalf("pinned executor refused new capability response: %v, %+v", err, legacy)
	}
}

func TestPrintServerEnrollmentPreflightStrictBoundedResponse(t *testing.T) {
	id := "11111111-1111-4111-8111-111111111111"
	ready := `{"api":1,"installation_id":"` + id + `","version":"v1.2.3","commit":"` + strings.Repeat("a", 40) + `","ready":true,"blockers":[]}`
	blocked, _ := json.Marshal(serverupdate.NewEnrollmentPreflight(id, serverupdate.RuntimeStatus{Ready: true, Version: "dev", Blocked: []string{"An enabled public listener is not running"}}))
	unknown := strings.Replace(string(blocked), "public_listener_unavailable", "secret-attacker-message", 1)
	for _, test := range []struct {
		name, body string
		status     int
		valid      bool
	}{
		{"ready", ready, 200, true},
		{"blocked also prints JSON", string(blocked), 200, true},
		{"unknown blocker", unknown, 200, false},
		{"unknown top-level field", strings.Replace(ready, `"api":1`, `"password":"secret","api":1`, 1), 200, false},
		{"duplicate field", strings.Replace(ready, `"api":1`, `"api":1,"api":1`, 1), 200, false},
		{"missing ready", strings.Replace(ready, `"ready":true,`, "", 1), 200, false},
		{"null ready", strings.Replace(ready, `"ready":true`, `"ready":null`, 1), 200, false},
		{"missing installation binding", strings.Replace(ready, id, "", 1), 200, false},
		{"non-UUID installation", strings.Replace(ready, id, "attacker", 1), 200, false},
		{"different installation", strings.Replace(ready, id, "22222222-2222-4222-8222-222222222222", 1), 200, false},
		{"unsupported api", strings.Replace(ready, `"api":1`, `"api":2`, 1), 200, false},
		{"false without blocker", strings.Replace(ready, `"ready":true`, `"ready":false`, 1), 200, false},
		{"true with blocker", strings.Replace(string(blocked), `"ready":false`, `"ready":true`, 1), 200, false},
		{"forged operator text", strings.Replace(string(blocked), "An enabled public listener is not running", "secret-attacker-message", 1), 200, false},
		{"forged operator action", strings.Replace(string(blocked), "restore_public_listeners", "run_secret_command", 1), 200, false},
		{"unknown blocker field", strings.Replace(string(blocked), `"code":`, `"credentials":"secret","code":`, 1), 200, false},
		{"duplicate blocker field", strings.Replace(string(blocked), `"code":`, `"code":"public_listener_unavailable","code":`, 1), 200, false},
		{"null blockers", strings.Replace(ready, `"blockers":[]`, `"blockers":null`, 1), 200, false},
		{"bad commit", strings.Replace(ready, strings.Repeat("a", 40), "credential", 1), 200, false},
		{"bad version", strings.Replace(ready, "v1.2.3", "v1.2.3\\nsecret", 1), 200, false},
		{"oversize", ready + strings.Repeat(" ", serverupdate.EnrollmentPreflightMaxBytes), 200, false},
		{"trailing JSON", ready + `{}`, 200, false},
		{"malformed", `{"api":1`, 200, false},
		{"unavailable", "secret", 503, false},
		{"redirect refused", "secret", 302, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			socket := filepath.Join(t.TempDir(), "runtime.sock")
			listener, err := net.Listen("unix", socket)
			if err != nil {
				t.Fatal(err)
			}
			srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodPost || r.URL.Path != "/enrollment-preflight" || r.ContentLength != 0 {
					t.Errorf("unexpected request: %s %s length %d", r.Method, r.URL.Path, r.ContentLength)
				}
				if test.status == 302 {
					w.Header().Set("Location", "http://external/prepare")
				}
				w.WriteHeader(test.status)
				_, _ = w.Write([]byte(test.body))
			})}
			go func() { _ = srv.Serve(listener) }()
			t.Cleanup(func() { _ = srv.Close() })
			var out bytes.Buffer
			// HTTP/schema fixtures explicitly trust this test listener. The public
			// CLI never accepts a caller-selected verifier or expected PID.
			err = printServerEnrollmentPreflightWithPeer(context.Background(), &out, socket, id, func(net.Conn) error { return nil })
			if test.valid {
				if err != nil || !json.Valid(out.Bytes()) {
					t.Fatalf("valid response rejected: %v %q", err, out.Bytes())
				}
			} else if err == nil || out.Len() != 0 || strings.Contains(err.Error(), "secret") {
				t.Fatalf("untrusted response escaped: output=%q error=%v", out.Bytes(), err)
			}
		})
	}
}

func TestServerEnrollmentPreflightUnavailableDoesNotOpenData(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CONFIG_DIR", filepath.Join(dir, "data"))
	var out bytes.Buffer
	err := printServerEnrollmentPreflight(context.Background(), &out, filepath.Join(dir, "absent.sock"), "11111111-1111-4111-8111-111111111111")
	if err == nil || out.Len() != 0 {
		t.Fatal("missing preflight did not fail closed")
	}
	if entries, err := os.ReadDir(dir); err != nil || len(entries) != 0 {
		t.Fatalf("preflight touched application filesystem: %v %v", entries, err)
	}
}

func TestServerEnrollmentPreflightCommandReadsExistingIdentityOnly(t *testing.T) {
	for _, test := range []struct {
		name, identity string
		defaultDir     bool
		serve, blocked bool
	}{
		{name: "missing data directory"},
		{name: "missing identity", identity: "missing"},
		{name: "invalid identity", identity: "invalid"},
		{name: "zero identity", identity: uuid.Nil.String()},
		{name: "noncanonical identity", identity: "AAAAAAAA-AAAA-4AAA-8AAA-AAAAAAAAAAAA"},
		{name: "unavailable socket", identity: "valid"},
		{name: "foreign ready configured directory", identity: "valid", serve: true},
		{name: "foreign blocked configured directory", identity: "valid", serve: true, blocked: true},
		{name: "foreign ready default directory", identity: "valid", serve: true, defaultDir: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			if test.serve && os.Getpid() == 1 {
				t.Skip("foreign listener fixture must not itself run as PID 1")
			}
			root := t.TempDir()
			dir := filepath.Join(root, "data")
			if test.defaultDir {
				t.Chdir(root)
				dir = filepath.Join(root, "p2pstream-data")
				t.Setenv("CONFIG_DIR", "")
			} else {
				t.Setenv("CONFIG_DIR", dir)
			}
			id := test.identity
			if id == "valid" {
				id = uuid.NewString()
			}
			var original []byte
			if id != "" {
				if err := os.Mkdir(dir, 0700); err != nil {
					t.Fatal(err)
				}
				if id != "missing" {
					original = []byte(id + "\n")
					if err := os.WriteFile(filepath.Join(dir, serverupdate.IdentityFile), original, 0600); err != nil {
						t.Fatal(err)
					}
				}
			}
			if test.serve {
				socket, err := serverupdate.EnrollmentPreflightSocket(id)
				if err != nil {
					t.Fatal(err)
				}
				listener, err := net.Listen("unix", socket)
				if err != nil {
					t.Fatal(err)
				}
				status := serverupdate.RuntimeStatus{Version: "dev", Ready: !test.blocked}
				p := serverupdate.NewEnrollmentPreflight(id, status)
				srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.URL.Path != "/enrollment-preflight" || r.Method != http.MethodPost {
						t.Errorf("unexpected command request: %s %s", r.Method, r.URL.Path)
					}
					_ = json.NewEncoder(w).Encode(p)
				})}
				go func() { _ = srv.Serve(listener) }()
				t.Cleanup(func() { _ = srv.Close(); _ = listener.Close() })
			}
			command, _, err := rootCmd.Find([]string{"server-enrollment-preflight"})
			if err != nil {
				t.Fatal(err)
			}
			var out bytes.Buffer
			command.SetOut(&out)
			command.SetContext(context.Background())
			t.Cleanup(func() { command.SetOut(nil); command.SetContext(nil) })
			err = command.RunE(command, nil)
			if err == nil || out.Len() != 0 {
				t.Fatalf("unavailable preflight failed open: output=%q error=%v", out.Bytes(), err)
			}
			entries, readErr := os.ReadDir(dir)
			if id == "" {
				if !os.IsNotExist(readErr) {
					t.Fatalf("command created missing directory: %v", readErr)
				}
			} else if id == "missing" {
				if readErr != nil || len(entries) != 0 {
					t.Fatalf("command created application data: %v %v", entries, readErr)
				}
			} else {
				got, err := os.ReadFile(filepath.Join(dir, serverupdate.IdentityFile))
				if readErr != nil || len(entries) != 1 || err != nil || !bytes.Equal(got, original) {
					t.Fatalf("command changed application data: %v %v %v", entries, readErr, err)
				}
			}
		})
	}
}

func TestServerEnrollmentForeignResponderFixture(t *testing.T) {
	path := os.Getenv("P2PSTREAM_TEST_PREFLIGHT_FOREIGN_SOCKET")
	if path == "" {
		t.Skip("subprocess fixture")
	}
	id := os.Getenv("P2PSTREAM_TEST_PREFLIGHT_FOREIGN_ID")
	listener, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	forged := serverupdate.NewEnrollmentPreflight(id, serverupdate.RuntimeStatus{Ready: true, Version: buildinfo.Version, Commit: buildinfo.Commit})
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(forged)
	})}
	go func() { _, _ = io.Copy(io.Discard, os.Stdin); _ = srv.Close() }()
	fmt.Fprintln(os.Stdout, "foreign listener ready")
	_ = srv.Serve(listener)
}

func startForeignEnrollmentResponder(t *testing.T, socket, id string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	child := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestServerEnrollmentForeignResponderFixture$")
	child.Env = append(os.Environ(), "P2PSTREAM_TEST_PREFLIGHT_FOREIGN_SOCKET="+socket, "P2PSTREAM_TEST_PREFLIGHT_FOREIGN_ID="+id)
	child.Stderr = os.Stderr
	input, err := child.StdinPipe()
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	output, err := child.StdoutPipe()
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	if err := child.Start(); err != nil {
		cancel()
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = input.Close(); _ = child.Wait(); cancel() })
	line, err := bufio.NewReader(output).ReadString('\n')
	if err != nil || line != "foreign listener ready\n" {
		t.Fatalf("foreign fixture failed: %q %v", line, err)
	}
}

func TestServerEnrollmentPreflightCommandRejectsForeignReadyResponder(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CONFIG_DIR", dir)
	id, err := serverupdate.InstallationIdentity(dir, true)
	if err != nil {
		t.Fatal(err)
	}
	path, err := serverupdate.EnrollmentPreflightSocket(id)
	if err != nil {
		t.Fatal(err)
	}
	// First observe the actual application's missing-database blockers over its
	// own socket. The forged responder below uses this same release and identity.
	app := &server.App{Config: &config.Config{ConfigDir: "/data"}, InstallationID: id}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	if err := app.StartServerUpdateRuntime(ctx); err != nil {
		t.Fatal(err)
	}
	transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", path)
	}}
	client := &http.Client{Transport: transport, Timeout: time.Second}
	response, err := client.Post("http://server/enrollment-preflight", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	actual, decodeErr := serverupdate.DecodeEnrollmentPreflight(response.Body)
	_ = response.Body.Close()
	transport.CloseIdleConnections()
	if decodeErr != nil || actual.Ready {
		t.Fatalf("actual app unexpectedly ready: %+v %v", actual, decodeErr)
	}
	missingDB := false
	for _, blocker := range actual.Blockers {
		missingDB = missingDB || blocker.Code == "database_schema_unavailable"
	}
	if !missingDB {
		t.Fatalf("actual app lacks missing-DB blocker: %+v", actual)
	}
	cancel()
	deadline := time.Now().Add(time.Second)
	for {
		if _, err := os.Lstat(path); os.IsNotExist(err) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("actual app socket did not close")
		}
		time.Sleep(time.Millisecond)
	}
	startForeignEnrollmentResponder(t, path, id)
	ctx, cancelForeignStartup := context.WithCancel(context.Background())
	defer cancelForeignStartup()
	if err := app.StartServerUpdateRuntime(ctx); err != nil {
		t.Fatalf("standalone startup should tolerate occupied optional preflight: %v", err)
	}
	command, _, err := rootCmd.Find([]string{"server-enrollment-preflight"})
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	command.SetOut(&out)
	command.SetContext(context.Background())
	t.Cleanup(func() { command.SetOut(nil); command.SetContext(nil) })
	err = command.RunE(command, nil)
	if err == nil || out.Len() != 0 {
		t.Fatalf("public CLI accepted a foreign ready responder while actual app lacks its DB: output=%s error=%v", out.Bytes(), err)
	}
}
