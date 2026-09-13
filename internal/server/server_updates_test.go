package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	p2pstreamv1 "p2pstream/gen/proto/p2pstream/v1"
	"p2pstream/internal/config"
	"p2pstream/internal/db"
	"p2pstream/internal/serverupdate"
)

func TestServerUpdateMethodsRequireAdminAndExactInstance(t *testing.T) {
	app := NewApp(&config.Config{ServerUpdateInstanceID: "11111111-1111-4111-8111-111111111111"}, newServerTestDB(t))
	admin := createTestAdminSession(t, app)
	viewer, err := app.DB.CreateUser(context.Background(), db.CreateUserParams{Username: "viewer", PasswordHash: "unused", Role: "user"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = app.DB.CreateSession(context.Background(), db.CreateSessionParams{UserID: viewer.ID, TokenHash: hashSessionToken("viewer"), ExpiresAt: time.Now().Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	app.RegisterManagementRoutes(mux)
	methods := []string{"GetServerUpdateOverview", "PreviewServerUpdate", "StartServerUpdate", "GetServerUpdateOperation"}
	for _, method := range methods {
		for _, test := range []struct {
			cookie string
			code   int
		}{{"", 401}, {sessionCookieName + "=viewer", 403}} {
			r := httptest.NewRequest(http.MethodPost, "/p2pstream.v1.AgentManagementService/"+method, strings.NewReader("{}"))
			r.Header.Set("Content-Type", "application/json")
			r.Header.Set("Cookie", test.cookie)
			w := httptest.NewRecorder()
			mux.ServeHTTP(w, r)
			if w.Code != test.code {
				t.Fatalf("%s: %d %s", method, w.Code, w.Body.String())
			}
		}
	}
	request := connect.NewRequest(&p2pstreamv1.PreviewServerUpdateRequest{InstanceId: "22222222-2222-4222-8222-222222222222", TargetVersion: "v1.0.1"})
	request.Header().Set("Cookie", admin.Get("Cookie"))
	if _, err := app.PreviewServerUpdate(context.Background(), request); connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Fatalf("wrong instance: %v", err)
	}
	overview := connect.NewRequest(&p2pstreamv1.GetServerUpdateOverviewRequest{})
	overview.Header().Set("Cookie", admin.Get("Cookie"))
	result, err := app.GetServerUpdateOverview(context.Background(), overview)
	if err != nil || result.Msg.ExecutorConfigured {
		t.Fatalf("unconfigured overview: %v %v", result, err)
	}
}

func TestServerUpdateMaintenanceAdmissionAndOrigins(t *testing.T) {
	path := filepath.Join(t.TempDir(), "maintenance")
	if err := os.WriteFile(path, []byte("active"), 0644); err != nil {
		t.Fatal(err)
	}
	app := &App{Config: &config.Config{ServerUpdateGateFile: path}}
	h := app.serverUpdateAdmission(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) }))
	for _, test := range []struct {
		method, origin, token string
		code                  int
	}{
		{"CreateAgent", "", "", 503}, {"CheckAgentUpdate", "", "", 503},
		{"GetServerUpdateOverview", "", "", 204}, {"StartServerUpdate", "http://example.com", "", 204},
		{"StartServerUpdate", "https://attacker.test", "", 403}, {"PreviewServerUpdate", "null", "", 403},
		{"StartServerUpdate", "https://attacker.test", "Basic forged", 403},
		{"StartServerUpdate", "https://parent.test", "Bearer p2pat_remote-token", 204},
	} {
		r := httptest.NewRequest(http.MethodPost, "http://example.com/p2pstream.v1.AgentManagementService/"+test.method, nil)
		r.Header.Set("Origin", test.origin)
		r.Header.Set("Authorization", test.token)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != test.code {
			t.Fatalf("%s %s: %d", test.method, test.origin, w.Code)
		}
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest(http.MethodPost, "/CreateAgent", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 204 {
		t.Fatal("gate remained closed")
	}
}

func TestServerMetadataSchemaTracksCurrentDatabase(t *testing.T) {
	database := newServerTestDB(t)
	var schema int
	if err := database.QueryRow("SELECT MAX(version_id) FROM goose_db_version WHERE is_applied=1").Scan(&schema); err != nil {
		t.Fatal(err)
	}
	if schema != serverupdate.Schema {
		t.Fatalf("release metadata schema %d differs from migrated database %d; update compatibility metadata", serverupdate.Schema, schema)
	}
}

func TestTrafficTraceSubscriptionDoesNotBlockUpdatePreparation(t *testing.T) {
	app := &App{}
	entered := make(chan struct{})
	finished := make(chan struct{})
	h := app.serverUpdateAdmission(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { close(entered); <-finished }))
	done := make(chan struct{})
	go func() {
		defer close(done)
		h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/p2pstream.v1.AgentManagementService/StreamTrafficTraceEvents", nil))
	}()
	<-entered
	acquired := app.serverUpdateMu.TryLock()
	if acquired {
		app.serverUpdateMu.Unlock()
	}
	close(finished)
	<-done
	if !acquired {
		t.Fatal("read-only subscription held the update barrier")
	}
}
