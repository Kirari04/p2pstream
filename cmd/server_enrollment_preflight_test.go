package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/uuid"
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
			err = printServerEnrollmentPreflight(context.Background(), &out, socket, id)
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
		{name: "ready configured directory", identity: "valid", serve: true},
		{name: "blocked configured directory exits successfully", identity: "valid", serve: true, blocked: true},
		{name: "ready default directory", identity: "valid", serve: true, defaultDir: true},
	} {
		t.Run(test.name, func(t *testing.T) {
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
			if test.serve {
				p, decodeErr := serverupdate.DecodeEnrollmentPreflight(&out)
				if err != nil || decodeErr != nil || p.InstallationID != id || p.Ready == test.blocked {
					t.Fatalf("command response: %+v error=%v decode=%v", p, err, decodeErr)
				}
			} else if err == nil || out.Len() != 0 {
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
