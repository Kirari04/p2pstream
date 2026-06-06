package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"connectrpc.com/connect"

	p2pstreamv1 "p2pstream/gen/proto/p2pstream/v1"
	"p2pstream/gen/proto/p2pstream/v1/p2pstreamv1connect"
	"p2pstream/internal/config"
)

func TestRegisterManagementRoutesServesUIByDefault(t *testing.T) {
	distDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(distDir, "index.html"), []byte("<html>management ui</html>"), 0644); err != nil {
		t.Fatalf("write index: %v", err)
	}
	app := NewApp(&config.Config{ManagementUIDistDir: distDir}, newServerTestDB(t))
	mux := http.NewServeMux()
	app.RegisterManagementRoutes(mux)

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "management ui") {
		t.Fatalf("unexpected body: %q", rec.Body.String())
	}
}

func TestRegisterManagementRoutesServesSourceOfferBeforeUI(t *testing.T) {
	distDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(distDir, "index.html"), []byte("<html>management ui</html>"), 0644); err != nil {
		t.Fatalf("write index: %v", err)
	}
	app := NewApp(&config.Config{ManagementUIDistDir: distDir}, newServerTestDB(t))
	mux := http.NewServeMux()
	app.RegisterManagementRoutes(mux)

	req := httptest.NewRequest(http.MethodGet, "/.well-known/p2pstream/source", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("source offer status = %d, want 200", rec.Code)
	}
	if got := rec.Header().Get("Content-Type"); got != "text/plain; charset=utf-8" {
		t.Fatalf("source offer content type = %q, want text/plain; charset=utf-8", got)
	}
	body := rec.Body.String()
	for _, want := range []string{
		"AGPL-3.0-or-later",
		"https://github.com/Kirari04/p2pstream",
		"Corresponding source:",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("source offer body missing %q:\n%s", want, body)
		}
	}
	if strings.Contains(body, "management ui") {
		t.Fatalf("source offer was served by UI fallback:\n%s", body)
	}
}

func TestRegisterManagementRoutesDisablesOnlyFrontendUI(t *testing.T) {
	app := NewApp(&config.Config{ManagementUIDisabled: true}, newServerTestDB(t))
	mux := http.NewServeMux()
	app.RegisterManagementRoutes(mux)

	rootReq := httptest.NewRequest(http.MethodGet, "/", nil)
	rootRec := httptest.NewRecorder()
	mux.ServeHTTP(rootRec, rootReq)
	if rootRec.Code != http.StatusNotFound {
		t.Fatalf("disabled UI root status = %d, want 404", rootRec.Code)
	}

	sourceReq := httptest.NewRequest(http.MethodGet, "/.well-known/p2pstream/source", nil)
	sourceRec := httptest.NewRecorder()
	mux.ServeHTTP(sourceRec, sourceReq)
	if sourceRec.Code != http.StatusOK {
		t.Fatalf("source offer with UI disabled status = %d, want 200", sourceRec.Code)
	}
	if !strings.Contains(sourceRec.Body.String(), "AGPL-3.0-or-later") {
		t.Fatalf("source offer with UI disabled missing license:\n%s", sourceRec.Body.String())
	}

	tunnelReq := httptest.NewRequest(http.MethodGet, "/agent/tunnel", nil)
	tunnelRec := httptest.NewRecorder()
	mux.ServeHTTP(tunnelRec, tunnelReq)
	if tunnelRec.Code != http.StatusUnauthorized {
		t.Fatalf("agent tunnel route status = %d, want 401 from agent auth", tunnelRec.Code)
	}

	server := httptest.NewServer(mux)
	defer server.Close()
	client := p2pstreamv1connect.NewAgentManagementServiceClient(server.Client(), server.URL)
	resp, err := client.GetSetupState(context.Background(), connect.NewRequest(&p2pstreamv1.GetSetupStateRequest{}))
	if err != nil {
		t.Fatalf("GetSetupState with UI disabled: %v", err)
	}
	if !resp.Msg.GetSetupRequired() {
		t.Fatal("expected setup API to remain available")
	}
}
