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

	wsReq := httptest.NewRequest(http.MethodGet, "/ws", nil)
	wsRec := httptest.NewRecorder()
	mux.ServeHTTP(wsRec, wsReq)
	if wsRec.Code != http.StatusUnauthorized {
		t.Fatalf("websocket route status = %d, want 401 from agent auth", wsRec.Code)
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
