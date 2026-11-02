package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
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

func TestDeploymentMigrationRecipeKeepsVerifiedInstallationBinding(t *testing.T) {
	t.Setenv("DATABASE_URL", "")
	app := &App{Config: &config.Config{ConfigDir: "/data"}, InstallationID: "11111111-1111-4111-8111-111111111111"}
	app.installationChecked = time.Now()
	app.installationExpires = time.Now().Add(time.Hour)
	app.installationCommand = "verified-download-and-checksum\nsudo python3 -I installer.py install --bundle protected --expect-installation '11111111-1111-4111-8111-111111111111' --COMPOSE_OPTIONS--"
	got, unavailable := app.serverDeploymentMigrationCommand(context.Background())
	if unavailable != "" || !strings.Contains(got, "install --migrate-deployment --bundle protected") || strings.Replace(got, " --migrate-deployment", "", 1) != app.installationCommand {
		t.Fatalf("migration changed verified recipe: %q %q", got, unavailable)
	}
	source, _ := serverupdate.NewGitHubSource("test/repo", "stable", "amd64")
	digest := strings.Repeat("a", 64)
	recipe := serverupdate.InstallationRecipe{
		Descriptor:     serverupdate.Installation{API: 1, Version: "v1.0.1", Commit: strings.Repeat("b", 40), Channel: "stable", Bundle: "p2pstream_v1.0.1_docker.tar.gz", UpdaterImages: map[string]string{"linux/amd64": "ghcr.io/test/repo-updater@sha256:" + digest, "linux/arm64": "ghcr.io/test/repo-updater@sha256:" + digest}},
		ManifestSHA256: digest,
	}
	// Reuse the exact generated shape with a manifest-advertised bootstrap.
	recipe.Bundle.Name, recipe.Bundle.SHA256 = recipe.Descriptor.Bundle, digest
	recipe.Setup.Name, recipe.Setup.SHA256, recipe.Setup.Size = serverupdate.SetupAsset, digest, 8192
	var err error
	app.installationCommand, err = source.SetupCommand(recipe, app.InstallationID)
	if err != nil {
		t.Fatal(err)
	}
	got, unavailable = app.serverDeploymentMigrationCommand(context.Background())
	if unavailable != "" || !strings.Contains(got, "'amd64' --migrate-deployment --COMPOSE_OPTIONS--)") || strings.Replace(got, " --migrate-deployment", "", 1) != app.installationCommand {
		t.Fatalf("migration changed standalone binding: %q %q", got, unavailable)
	}
	work := t.TempDir()
	for name, body := range map[string]string{
		"curl": "#!/bin/sh\ncat <<'SETUP'\nmain() { printf '%s\\n' \"$@\"; }\nmain \"$@\"\nSETUP\n",
		"sudo": "#!/bin/sh\nexec \"$@\"\n",
	} {
		if err := os.WriteFile(filepath.Join(work, name), []byte(body), 0700); err != nil {
			t.Fatal(err)
		}
	}
	// Deliberately include shell syntax and a quote in a custom Compose value.
	hostile := "custom ' $(false); *"
	quoted := "'" + strings.ReplaceAll(hostile, "'", "'\\''") + "'"
	cmd := exec.Command("bash", "-c", strings.Replace(got, "--COMPOSE_OPTIONS--", "-- -p "+quoted, 1))
	cmd.Env = append(os.Environ(), "PATH="+work+":"+os.Getenv("PATH"))
	output, err := cmd.CombinedOutput()
	want := strings.Join([]string{"test/repo@v1.0.1", app.InstallationID, digest, "amd64", "--migrate-deployment", "--", "-p", hostile, ""}, "\n")
	if err != nil || string(output) != want {
		t.Fatalf("migration shell changed argument boundaries: %q, %v", output, err)
	}
	for _, malformed := range []string{strings.Replace(app.installationCommand, "--COMPOSE_OPTIONS--", "--COMPOSE_OPTIONS-- --COMPOSE_OPTIONS--", 1), strings.Replace(app.installationCommand, "--COMPOSE_OPTIONS--", "", 1)} {
		app.installationCommand = malformed
		if got, unavailable := app.serverDeploymentMigrationCommand(context.Background()); got != "" || unavailable == "" {
			t.Fatal("malformed standalone recipe was made executable")
		}
	}
	app.installationCommand = "unexpected-recipe"
	if got, unavailable := app.serverDeploymentMigrationCommand(context.Background()); got != "" || unavailable == "" {
		t.Fatal("unrecognized recipe was made executable")
	}
}

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
