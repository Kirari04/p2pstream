package managementui

import (
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// NewHandler serves the management UI. In development it proxies to Vite; in
// normal operation it serves the built dist directory from disk.
func NewHandler(devProxyURL, distDir string) http.Handler {
	if devProxyURL != "" {
		return newDevProxy(devProxyURL)
	}

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		indexPath := filepath.Join(distDir, "index.html")
		if _, err := os.Stat(indexPath); err != nil {
			http.Error(w, "management UI not built", http.StatusServiceUnavailable)
			return
		}

		cleanPath := path.Clean("/" + r.URL.Path)
		relPath := strings.TrimPrefix(cleanPath, "/")
		if relPath == "" {
			http.ServeFile(w, r, indexPath)
			return
		}

		filePath := filepath.Join(distDir, filepath.FromSlash(relPath))
		if stat, err := os.Stat(filePath); err == nil && !stat.IsDir() {
			http.ServeFile(w, r, filePath)
			return
		}

		http.ServeFile(w, r, indexPath)
	})
}

func newDevProxy(rawURL string) http.Handler {
	target, err := url.Parse(rawURL)
	if err != nil {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "invalid management UI dev proxy URL", http.StatusBadGateway)
		})
	}

	proxy := httputil.NewSingleHostReverseProxy(target)
	director := proxy.Director
	proxy.Director = func(r *http.Request) {
		director(r)
		r.Host = target.Host
	}
	proxy.ErrorHandler = func(w http.ResponseWriter, r *http.Request, err error) {
		http.Error(w, "management UI dev proxy unavailable", http.StatusBadGateway)
	}

	return proxy
}
