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
		{"/files/a%00b", publicPathIssueSet{NUL: true}},
		{"/files/a%2500b", publicPathIssueSet{NUL: true, NestedEncoding: true}},
		{"/files/..;session=x/admin", publicPathIssueSet{DecodedDotSegment: true, PathParameter: true}},
		{"/files/%2e%3Bsession=x/admin", publicPathIssueSet{DecodedDotSegment: true, PathParameter: true}},
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

func TestPublicRequestPathNULDecodeLayers(t *testing.T) {
	for layer := 0; layer <= maxPublicPathDecodeLayers; layer++ {
		bad, control := "/files/a\x00b", "/files/a\x01b"
		if layer > 0 {
			prefix := "/files/a%" + strings.Repeat("25", layer-1)
			bad, control = prefix+"00b", prefix+"01b"
		}
		var issues publicPathIssueSet
		inspectPublicPathLayers(bad, &issues)
		if !issues.NUL || issues.DecodeLimit {
			t.Fatalf("layer %d: issues=%+v, want NUL found within inspection bound", layer, issues)
		}
		issues = publicPathIssueSet{}
		inspectPublicPathLayers(control, &issues)
		if issues.NUL || issues.DecodeLimit {
			t.Fatalf("layer %d valid byte control: issues=%+v", layer, issues)
		}
	}
}

func TestPublicProxyProtectedRouteAliasesAndExplicitCompatibilityFallback(t *testing.T) {
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
				"/public//../admin", "/admin%253Bfoo", "/admin%2fpanel", "/admin//panel", "/admin/panel;session=x",
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
			// An explicit compatibility catchall accepts origin parser
			// discrepancies even beside a strict protected route. This is an
			// intentional weaker contract, documented in routing-rules.md.
			for _, target := range []string{"//admin", "/%2Fadmin", "/admin;jsessionid=x", "/admin%3bjsessionid=x", "/public%2F%2Fadmin", "/public%5C%5Cadmin"} {
				before := hits.Load()
				rec := request(target)
				wantStatus, wantHits := http.StatusBadRequest, before
				if mode == publicRoutePathSecurityModeAllowEncodedSeparators {
					wantStatus, wantHits = http.StatusOK, before+1
				}
				if rec.Code != wantStatus || hits.Load() != wantHits {
					t.Fatalf("explicit %s fallback for %s: status=%d hits=%d, want status=%d hits=%d", mode, target, rec.Code, hits.Load(), wantStatus, wantHits)
				}
				if wantStatus == http.StatusOK && strings.Contains(target, "admin") && !strings.HasSuffix(rec.Body.String(), "/admin") {
					t.Fatalf("origin did not exercise admin alias: %q", rec.Body.String())
				}
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
			paths := []string{"/files/v1%2e2/readme", "/files/a%20b.txt", "/files/a+b.txt", "/files/100%25.txt", "/files/%25.txt", "/files/%E7%9B%AE%E5%BD%95/%C3%A9", "/files/00.txt", "/files/a%01b"}
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

func TestPublicProxyParserDiscrepancyCompatibilityControls(t *testing.T) {
	for _, mode := range []string{publicRoutePathSecurityModeStrict, publicRoutePathSecurityModeAllowEncodedSeparators} {
		t.Run(mode, func(t *testing.T) {
			_, handler, hits, lastPath := newTestPublicPathProxyWithMode(t, "/", nil, mode)
			for _, target := range []string{
				"/app;jsessionid=X/page", "/app%3Bjsessionid=X/page", "/app%3bjsessionid=X/page",
				"/app//page", "//app/page", "/%2Fapp/page", "/%2fapp/page", "/%2F%2Fapp/page",
				"/app/%2Fpage", "/app%5C%5Cpage", "/%5Capp/page", "/%5c%5Capp/page",
			} {
				t.Run(target, func(t *testing.T) {
					before := hits.Load()
					rec := httptest.NewRecorder()
					handler(rec, httptest.NewRequest(http.MethodGet, target, nil))
					if mode == publicRoutePathSecurityModeStrict {
						if rec.Code != http.StatusBadRequest || hits.Load() != before {
							t.Fatalf("strict status=%d hits=%d, want 400 and no forwarding", rec.Code, hits.Load())
						}
						return
					}
					if rec.Code != http.StatusOK || hits.Load() != before+1 || lastPath.Load() != target {
						t.Fatalf("compatibility status=%d hits=%d escaped path=%q, want 200 and original %q", rec.Code, hits.Load(), lastPath.Load(), target)
					}
				})
			}
		})
	}
}

func TestPublicProxyNULRejectedAcrossPathRepresentationsBeforePolicy(t *testing.T) {
	cases := []struct {
		name string
		edit func(*http.Request)
	}{
		{"decoded_path_raw", func(r *http.Request) { r.URL.Path = "/public/a\x00b" }},
		{"decoded_path_encoded", func(r *http.Request) { r.URL.Path = "/public/a%00b" }},
		{"raw_path_raw", func(r *http.Request) { r.URL.RawPath = "/public/a\x00b" }},
		{"raw_path_encoded", func(r *http.Request) { r.URL.RawPath = "/public/a%00b" }},
		{"request_uri_raw", func(r *http.Request) { r.RequestURI = "/public/a\x00b" }},
		{"request_uri_encoded", func(r *http.Request) { r.RequestURI = "/public/a%00b" }},
		{"absolute_request_uri", func(r *http.Request) { r.RequestURI = "http://app.example/public/a%00b?ok=1" }},
		{"parsed_encoded", func(r *http.Request) { *r = *httptest.NewRequest(http.MethodGet, "/public/a%00b", nil) }},
		{"nested_encoded", func(r *http.Request) { *r = *httptest.NewRequest(http.MethodGet, "/public/a%2500b", nil) }},
		{"mixed_literal_percent", func(r *http.Request) { *r = *httptest.NewRequest(http.MethodGet, "/public/%25/a%2500b", nil) }},
	}
	for _, mode := range []string{publicRoutePathSecurityModeStrict, publicRoutePathSecurityModeAllowEncodedSeparators} {
		t.Run(mode, func(t *testing.T) {
			app, handler, hits, _ := newTestPublicPathProxyWithMode(t, "/public", []publicWafRuleConfig{testWafRule(1, publicWafActionBlock)}, mode)
			app.PublicWAF.reconcile(app.publicSnapshot)
			for _, tc := range cases {
				t.Run(tc.name, func(t *testing.T) {
					req := httptest.NewRequest(http.MethodGet, "/public/safe", nil)
					tc.edit(req)
					if !classifyPublicRequestPath(req).NUL {
						t.Fatal("missing NUL classification")
					}
					rec := httptest.NewRecorder()
					handler(rec, req)
					if rec.Code != http.StatusBadRequest || hits.Load() != 0 {
						t.Fatalf("status=%d hits=%d, want 400 before policy or forwarding", rec.Code, hits.Load())
					}
				})
			}
			// Nearby bytes, percent filenames, and Unicode remain valid paths;
			// their requests reach the blocking policy instead of path rejection.
			for _, target := range []string{"/public/a%01b", "/public/00", "/public/100%25.txt", "/public/%C3%A9", "/public/safe?q=%00"} {
				rec := httptest.NewRecorder()
				handler(rec, httptest.NewRequest(http.MethodGet, target, nil))
				if rec.Code != http.StatusForbidden {
					t.Fatalf("valid control %q: status=%d, want policy denial", target, rec.Code)
				}
			}
		})
	}
}

func TestPublicProxyManualDecodedPathBytes(t *testing.T) {
	for _, mode := range []string{publicRoutePathSecurityModeStrict, publicRoutePathSecurityModeAllowEncodedSeparators} {
		t.Run(mode, func(t *testing.T) {
			_, handler, hits, lastPath := newTestPublicPathProxyWithMode(t, "/", nil, mode)
			for _, tc := range []struct {
				name string
				path string
				want int
			}{
				{"invalid_utf8_filename", string([]byte{'/', 0xff, '/', '.', 0xfe}), http.StatusOK},
				{"invalid_utf8_traversal", string([]byte{'/', 0xff, '/', '.', '.', '/', 0xfe}), http.StatusBadRequest},
				{"nul_filename", "/a\x00b", http.StatusBadRequest},
				{"nul_adjacent_dots", "/a/..\x00/b", http.StatusBadRequest},
				{"unicode_filename", "/目录/é.txt", http.StatusOK},
			} {
				t.Run(tc.name, func(t *testing.T) {
					req := httptest.NewRequest(http.MethodGet, "/", nil)
					req.URL.Path = tc.path
					req.RequestURI = req.URL.RequestURI()
					before := hits.Load()
					rec := httptest.NewRecorder()
					handler(rec, req)
					if rec.Code != tc.want {
						t.Fatalf("status=%d body=%q, want %d", rec.Code, rec.Body.String(), tc.want)
					}
					if tc.want == http.StatusOK {
						if hits.Load() != before+1 || lastPath.Load() != req.URL.EscapedPath() {
							t.Fatalf("origin path=%q hits=%d, want %q and one new hit", lastPath.Load(), hits.Load(), req.URL.EscapedPath())
						}
					} else if hits.Load() != before {
						t.Fatal("rejected decoded path reached origin")
					}
				})
			}
		})
	}
}
