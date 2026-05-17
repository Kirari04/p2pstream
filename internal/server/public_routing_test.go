package server

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestRedirectPathSuffixNormalizesLocalTargets(t *testing.T) {
	for _, tc := range []struct {
		name   string
		target string
		prefix string
		want   string
	}{
		{name: "matched suffix", target: "http://example.com/api/users", prefix: "/api", want: "/users"},
		{name: "segment boundary mismatch", target: "http://example.com/apiv2", prefix: "/api", want: ""},
		{name: "scheme relative request path", target: "http://example.com//evil.example/path", prefix: "", want: "/evil.example/path"},
		{name: "backslash request path", target: `http://example.com/\evil.example/path`, prefix: "", want: "/evil.example/path"},
		{name: "scheme relative suffix", target: "http://example.com/app//evil.example/path", prefix: "/app", want: "/evil.example/path"},
		{name: "backslash suffix", target: `http://example.com/app/\evil.example/path`, prefix: "/app", want: "/evil.example/path"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, tc.target, nil)

			if got := redirectPathSuffix(req, tc.prefix); got != tc.want {
				t.Fatalf("redirectPathSuffix(%q, %q) = %q, want %q", tc.target, tc.prefix, got, tc.want)
			}
		})
	}
}

func TestJoinRedirectPathNormalizesUnsafeSuffixes(t *testing.T) {
	for _, tc := range []struct {
		name   string
		base   string
		suffix string
		want   string
	}{
		{name: "root with scheme relative suffix", base: "/", suffix: "//evil.example/path", want: "/evil.example/path"},
		{name: "root with backslash suffix", base: "/", suffix: `/\evil.example/path`, want: "/evil.example/path"},
		{name: "base with safe suffix", base: "/base", suffix: "/users", want: "/base/users"},
		{name: "empty base with relative suffix", base: "", suffix: "users", want: "/users"},
		{name: "unsafe base is normalized", base: "//configured.example", suffix: "users", want: "/configured.example/users"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := joinRedirectPath(tc.base, tc.suffix); got != tc.want {
				t.Fatalf("joinRedirectPath(%q, %q) = %q, want %q", tc.base, tc.suffix, got, tc.want)
			}
			if got := joinRedirectPath("/", tc.suffix); got != "" && !isSafeLocalRedirectTarget(got) {
				t.Fatalf("joinRedirectPath returned unsafe local redirect target %q", got)
			}
		})
	}
}

func TestRedirectLocationPreservesSafePathSuffix(t *testing.T) {
	route := publicRouteConfig{
		PathPrefix:                 "/api",
		RedirectTargetMode:         publicRouteRedirectTargetModeSameHostPath,
		RedirectTarget:             "/target",
		RedirectPreservePathSuffix: true,
	}
	req := httptest.NewRequest(http.MethodGet, "http://example.com/api/users?debug=1", nil)

	location, err := redirectLocationForRequest(req, route)
	if err != nil {
		t.Fatalf("redirectLocationForRequest returned error: %v", err)
	}
	if location != "/target/users" {
		t.Fatalf("location = %q, want %q", location, "/target/users")
	}
}
