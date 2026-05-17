package managementui

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestHandlerServesDistIndex(t *testing.T) {
	distDir := t.TempDir()
	writeFile(t, filepath.Join(distDir, "index.html"), "<html>app shell</html>")

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()

	NewHandler("", distDir).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "app shell") {
		t.Fatalf("expected index body, got %q", rec.Body.String())
	}
}

func TestHandlerServesDistAsset(t *testing.T) {
	distDir := t.TempDir()
	writeFile(t, filepath.Join(distDir, "index.html"), "<html>app shell</html>")
	writeFile(t, filepath.Join(distDir, "asset.js"), "console.log('asset')")

	req := httptest.NewRequest(http.MethodGet, "/asset.js", nil)
	rec := httptest.NewRecorder()

	NewHandler("", distDir).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "asset") {
		t.Fatalf("expected asset body, got %q", rec.Body.String())
	}
}

func TestHandlerFallsBackToIndexForSPARoute(t *testing.T) {
	distDir := t.TempDir()
	writeFile(t, filepath.Join(distDir, "index.html"), "<html>app shell</html>")

	req := httptest.NewRequest(http.MethodGet, "/settings", nil)
	rec := httptest.NewRecorder()

	NewHandler("", distDir).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "app shell") {
		t.Fatalf("expected index fallback, got %q", rec.Body.String())
	}
}

func TestHandlerDoesNotServeTraversalOutsideDist(t *testing.T) {
	rootDir := t.TempDir()
	distDir := filepath.Join(rootDir, "dist")
	if err := os.Mkdir(distDir, 0755); err != nil {
		t.Fatalf("mkdir dist: %v", err)
	}
	writeFile(t, filepath.Join(rootDir, "secret.txt"), "outside-secret")
	writeFile(t, filepath.Join(distDir, "index.html"), "<html>app shell</html>")
	writeFile(t, filepath.Join(distDir, "asset.js"), "console.log('asset')")

	for _, target := range []string{
		"/../secret.txt",
		"/%2e%2e/secret.txt",
		"/assets/../../secret.txt",
		"/assets/%2e%2e/%2e%2e/secret.txt",
	} {
		t.Run(target, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, target, nil)
			rec := httptest.NewRecorder()

			NewHandler("", distDir).ServeHTTP(rec, req)

			if rec.Code != http.StatusOK {
				t.Fatalf("expected status 200, got %d", rec.Code)
			}
			body := rec.Body.String()
			if strings.Contains(body, "outside-secret") {
				t.Fatalf("served file outside dist for %q: %q", target, body)
			}
			if !strings.Contains(body, "app shell") {
				t.Fatalf("expected index fallback for %q, got %q", target, body)
			}
		})
	}
}

func TestHandlerReturnsUnavailableWhenDistMissing(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()

	NewHandler("", filepath.Join(t.TempDir(), "missing")).ServeHTTP(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected status 503, got %d", rec.Code)
	}
}

func TestHandlerProxiesToDevServer(t *testing.T) {
	devServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/dev-path" {
			t.Fatalf("expected proxied path /dev-path, got %s", r.URL.Path)
		}
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte("vite response"))
	}))
	defer devServer.Close()

	req := httptest.NewRequest(http.MethodGet, "/dev-path", nil)
	rec := httptest.NewRecorder()

	NewHandler(devServer.URL, "").ServeHTTP(rec, req)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("expected status 202, got %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "vite response") {
		t.Fatalf("expected dev proxy body, got %q", rec.Body.String())
	}
}

func writeFile(t *testing.T, name string, data string) {
	t.Helper()
	if err := os.WriteFile(name, []byte(data), 0644); err != nil {
		t.Fatalf("write file %s: %v", name, err)
	}
}
