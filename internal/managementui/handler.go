package managementui

import (
	"io"
	"io/fs"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"path"
	"strings"
)

// NewHandler serves the management UI. In development it proxies to Vite; in
// normal operation it serves the built dist directory from disk.
func NewHandler(devProxyURL, distDir string) http.Handler {
	if devProxyURL != "" {
		return newDevProxy(devProxyURL)
	}

	distFS := os.DirFS(distDir)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !managementUIFileExists(distFS, "index.html") {
			http.Error(w, "management UI not built", http.StatusServiceUnavailable)
			return
		}

		if relPath := managementUIAssetPath(r.URL.Path); relPath != "" {
			if serveManagementUIFile(w, r, distFS, relPath) {
				return
			}
			if !shouldServeManagementUIIndex(r, relPath) {
				http.NotFound(w, r)
				return
			}
		} else if !shouldServeManagementUIIndex(r, "") {
			http.NotFound(w, r)
			return
		}
		if serveManagementUIFile(w, r, distFS, "index.html") {
			return
		}

		http.Error(w, "management UI not built", http.StatusServiceUnavailable)
	})
}

func managementUIFileExists(distFS fs.FS, name string) bool {
	file, _, ok := openManagementUIFile(distFS, name)
	if ok {
		_ = file.Close()
	}
	return ok
}

func managementUIAssetPath(requestPath string) string {
	cleanPath := path.Clean("/" + requestPath)
	relPath := strings.TrimPrefix(cleanPath, "/")
	if relPath == "" || !fs.ValidPath(relPath) || strings.Contains(relPath, `\`) {
		return ""
	}
	return relPath
}

func shouldServeManagementUIIndex(r *http.Request, relPath string) bool {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		return false
	}
	if looksLikeManagementUIAssetPath(relPath) {
		return false
	}
	return managementUIAcceptsHTML(r.Header.Get("Accept"))
}

func looksLikeManagementUIAssetPath(relPath string) bool {
	if relPath == "" {
		return false
	}
	return path.Ext(relPath) != "" || relPath == "assets" || strings.HasPrefix(relPath, "assets/")
}

func managementUIAcceptsHTML(accept string) bool {
	if strings.TrimSpace(accept) == "" {
		return true
	}
	for _, part := range strings.Split(accept, ",") {
		mediaType := strings.TrimSpace(part)
		if semi := strings.IndexByte(mediaType, ';'); semi >= 0 {
			mediaType = strings.TrimSpace(mediaType[:semi])
		}
		switch mediaType {
		case "text/html", "application/xhtml+xml", "*/*":
			return true
		}
	}
	return false
}

func serveManagementUIFile(w http.ResponseWriter, r *http.Request, distFS fs.FS, name string) bool {
	file, info, ok := openManagementUIFile(distFS, name)
	if !ok {
		return false
	}
	defer func() {
		// Best-effort close; response may already be in-flight.
		_ = file.Close()
	}()
	seeker, ok := file.(io.ReadSeeker)
	if !ok {
		return false
	}
	http.ServeContent(w, r, info.Name(), info.ModTime(), seeker)
	return true
}

func openManagementUIFile(distFS fs.FS, name string) (fs.File, fs.FileInfo, bool) {
	if name == "" || !fs.ValidPath(name) || strings.Contains(name, `\`) {
		return nil, nil, false
	}
	file, err := distFS.Open(name)
	if err != nil {
		return nil, nil, false
	}
	info, err := file.Stat()
	if err != nil || info.IsDir() {
		_ = file.Close()
		return nil, nil, false
	}
	return file, info, true
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
