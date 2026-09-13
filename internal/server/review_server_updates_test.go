package server

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"p2pstream/internal/config"
)

func TestServerUpdateMaintenanceAllowsSessionRecovery(t *testing.T) {
	gate := filepath.Join(t.TempDir(), "maintenance")
	if err := os.WriteFile(gate, []byte("active"), 0644); err != nil {
		t.Fatal(err)
	}
	app := NewApp(&config.Config{ServerUpdateGateFile: gate}, newServerTestDB(t))
	admin := createTestAdminSession(t, app)
	mux := http.NewServeMux()
	app.RegisterManagementRoutes(mux)
	for _, test := range []struct {
		method, body string
		status       int
	}{
		{"GetSetupState", `{}`, http.StatusOK},
		{"GetCurrentUser", `{}`, http.StatusOK},
		{"ListEnvironments", `{}`, http.StatusOK},
		{"Login", `{"username":"admin","password":"very-good-test-password"}`, http.StatusOK},
		{"Login", `{"username":"admin","password":"wrong"}`, http.StatusUnauthorized},
		{"GetServerUpdateOverview", `{}`, http.StatusOK},
		{"SetupAdmin", `{}`, http.StatusServiceUnavailable},
		{"CreateAgent", `{}`, http.StatusServiceUnavailable},
		{"Logout", `{}`, http.StatusOK},
		{"GetCurrentUser", `{}`, http.StatusUnauthorized},
	} {
		r := httptest.NewRequest(http.MethodPost, "/p2pstream.v1.AgentManagementService/"+test.method, strings.NewReader(test.body))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Cookie", admin.Get("Cookie"))
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		if w.Code != test.status {
			t.Errorf("%s: got %d, want %d: %s", test.method, w.Code, test.status, w.Body.String())
		}
	}
}

func TestCanceledServerUpdatePreparationLeavesManagementResponsive(t *testing.T) {
	app := &App{}
	// Simulate an already running certificate request awaiting its CA.
	app.serverUpdateMu.RLock()
	defer app.serverUpdateMu.RUnlock()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	finished := make(chan error, 1)
	go func() { finished <- app.lockServerUpdatePreparation(ctx) }()
	select {
	case err := <-finished:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("preparation result: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("preparation ignored its deadline")
	}
	if !app.serverUpdateMu.TryRLock() {
		t.Fatal("aborted preparation still blocks management reads")
	}
	app.serverUpdateMu.RUnlock()
}

func TestServerUpdatePreparationDrainsExistingWork(t *testing.T) {
	app := &App{}
	app.serverUpdateMu.RLock()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	finished := make(chan error, 1)
	go func() { finished <- app.lockServerUpdatePreparation(ctx) }()
	select {
	case err := <-finished:
		app.serverUpdateMu.RUnlock()
		t.Fatalf("preparation did not wait for in-flight work: %v", err)
	case <-time.After(20 * time.Millisecond):
	}
	app.serverUpdateMu.RUnlock()
	if err := <-finished; err != nil {
		t.Fatal(err)
	}
	if app.serverUpdateMu.TryRLock() {
		app.serverUpdateMu.RUnlock()
		t.Fatal("preparation did not own the exclusive barrier")
	}
	app.serverUpdateMu.Unlock()
}
