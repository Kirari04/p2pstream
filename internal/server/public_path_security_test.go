package server

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"path"
	"strings"
	"sync/atomic"
	"testing"
)

func TestPublicRequestPathNestedRepresentations(t *testing.T) {
	for _, tc := range []struct {
		path string
		want publicPathIssueSet
	}{
		{"/files/%252e%252E/admin", publicPathIssueSet{DecodedDotSegment: true, NestedEncoding: true}},
		{"/files/a%252Fb", publicPathIssueSet{EncodedSlash: true, NestedEncoding: true}},
		{"/files/a%255Cb", publicPathIssueSet{EncodedBackslash: true, NestedEncoding: true}},
		{"/files/a%5C..%5Cb", publicPathIssueSet{DecodedDotSegment: true, EncodedBackslash: true}},
		{"/files//a", publicPathIssueSet{DuplicateSlash: true}},
		{"/files/a%2F%2Fb", publicPathIssueSet{DuplicateSlash: true, EncodedSlash: true}},
		{"/files/a%253Bb", publicPathIssueSet{PathParameter: true, NestedEncoding: true}},
		{"/%2561dmin", publicPathIssueSet{NestedEncoding: true}},
		{"/files/%25/%252e%252e/a", publicPathIssueSet{DecodedDotSegment: true, NestedEncoding: true}},
		{"/files/v1%2e2/readme", publicPathIssueSet{}},
		{"/files/100%25.txt", publicPathIssueSet{}},
	} {
		t.Run(tc.path, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, tc.path, nil)
			if got := classifyPublicRequestPath(req); got != tc.want {
				t.Fatalf("issues = %+v, want %+v", got, tc.want)
			}
		})
	}
	deep := "/files/%" + strings.Repeat("25", 32768) + "2fadmin"
	if issues := classifyPublicRequestPath(httptest.NewRequest(http.MethodGet, deep, nil)); !issues.DecodeLimit {
		t.Fatal("deep encoding must hit the fixed inspection limit")
	}
}

func TestPublicProxyAmbiguousPathsCannotBypassProtectedRoute(t *testing.T) {
	for _, mode := range []string{publicRoutePathSecurityModeStrict, publicRoutePathSecurityModeAllowEncodedSeparators} {
		t.Run(mode, func(t *testing.T) {
			var hits atomic.Int64
			// Model an origin that decodes again, collapses slashes, treats
			// backslashes as separators, and strips servlet path parameters.
			origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				hits.Add(1)
				decoded := r.URL.Path
				for range 8 {
					next, err := url.PathUnescape(decoded)
					if err != nil || next == decoded {
						break
					}
					decoded = next
				}
				segments := strings.Split(strings.ReplaceAll(decoded, `\`, "/"), "/")
				for i := range segments {
					segments[i], _, _ = strings.Cut(segments[i], ";")
				}
				_, _ = w.Write([]byte(path.Clean(strings.Join(segments, "/"))))
			}))
			t.Cleanup(origin.Close)
			target := publicRouteTargetConfig{ID: 20, RouteID: 10, Enabled: true,
				TargetType: publicRouteTargetTypeProxy, Transport: publicRouteTargetTransportDirect,
				ParsedURL: mustParsePublicAccessTestURL(t, origin.URL)}
			fallback := publicRouteConfig{ID: 10, Enabled: true, PathPrefix: "/", Action: publicRouteActionForward,
				PathSecurityMode: mode, Targets: []publicRouteTargetConfig{target}}
			protected := fallback
			protected.ID, protected.PathPrefix, protected.AccessPolicyID = 11, "/admin", 99
			protected.PathSecurityMode = publicRoutePathSecurityModeStrict
			app := NewApp(nil, nil)
			setPublicSnapshotForTest(t, app, &publicProxySnapshot{
				Listeners:        map[int64]publicListenerConfig{1: {ID: 1, Protocol: publicListenerProtocolHTTP, Enabled: true}},
				RoutesByListener: map[int64][]publicRouteConfig{1: {protected, fallback}},
				RouteTargets:     map[int64]publicRouteTargetConfig{target.ID: target},
			})
			handler := app.publicProxyHandler(1)
			request := func(target string) *httptest.ResponseRecorder {
				t.Helper()
				rec := httptest.NewRecorder()
				handler(rec, httptest.NewRequest(http.MethodGet, target, nil))
				return rec
			}
			if rec := request("/admin"); rec.Code != http.StatusServiceUnavailable {
				t.Fatalf("protected path = %d, want fail-closed access policy", rec.Code)
			}
			for _, target := range []string{
				"//admin", "/public//../admin", "/admin;jsessionid=x", "/admin%3bjsessionid=x", "/admin%253Bfoo",
				"/public%2F%2Fadmin", "/public%5C%5Cadmin", "/admin%2fpanel",
				"/public/%252e%252e/admin", "/public/%252E%2e/admin", "/public/%25252e%25252e/admin",
				"/public%252f..%252fadmin", "/public%255c..%255Cadmin", "/public%5c..%5cadmin",
				"/public/%25/%252e%252e/admin", "/public/..;x/admin", "/public/%252e%252e%253bx/admin",
				"/%2561dmin", "/%252561dmin", "/public%252fadmin", "/public%255cadmin",
				"/public/%25" + strings.Repeat("25", 32) + "2e/admin",
			} {
				t.Run(target, func(t *testing.T) {
					if rec := request(target); rec.Code != http.StatusBadRequest {
						t.Errorf("status = %d body=%q, want 400", rec.Code, rec.Body.String())
					}
					if got := hits.Load(); got != 0 {
						t.Fatalf("origin hits = %d, want 0", got)
					}
				})
			}
			if rec := request("/public/readme.txt"); rec.Code != http.StatusOK || rec.Body.String() != "/public/readme.txt" {
				t.Fatalf("ordinary fallback = %d %q", rec.Code, rec.Body.String())
			}
		})
	}
}

func TestPublicProxySafePathsKeepOriginalEscapes(t *testing.T) {
	for _, mode := range []string{publicRoutePathSecurityModeStrict, publicRoutePathSecurityModeAllowEncodedSeparators} {
		t.Run(mode, func(t *testing.T) {
			_, handler, _, lastPath := newTestPublicPathProxyWithMode(t, "/files", nil, mode)
			paths := []string{"/files/v1%2e2/readme", "/files/a%20b.txt", "/files/a+b.txt", "/files/100%25.txt", "/files/%25.txt"}
			if mode == publicRoutePathSecurityModeAllowEncodedSeparators {
				paths = append(paths, "/files/group%2Fproject", "/files/group%5cproject")
			}
			for _, target := range paths {
				rec := httptest.NewRecorder()
				handler(rec, httptest.NewRequest(http.MethodGet, target, nil))
				if rec.Code != http.StatusOK {
					t.Fatalf("%s: status = %d body=%q", target, rec.Code, rec.Body.String())
				}
				if got := lastPath.Load(); got != target {
					t.Fatalf("origin escaped path = %q, want %q", got, target)
				}
			}
		})
	}
}
