package server

import (
	"net/http"
	"net/url"
	"strings"
	"testing"
)

// Keep the pre-candidate oracle literal: the helper sees decoded URL.Path;
// percent escapes passed directly to this helper are ordinary string bytes.
func publicPathDotScanSplitOracle(path string) bool {
	for _, segment := range strings.Split(path, "/") {
		if segment == "." || segment == ".." {
			return true
		}
	}
	return false
}

type publicPathDotScanCase struct {
	name string
	path string
	want bool
}

func publicPathDotScanCorpus() []publicPathDotScanCase {
	return []publicPathDotScanCase{
		{"empty", "", false},
		{"root", "/", false},
		{"slashes", "////", false},
		{"plain_bare", "asset", false},
		{"plain_short", "/assets/app.js", false},
		{"leading_trailing", "/assets/", false},
		{"repeated_slash", "//assets///app.js//", false},
		{"dot_bare", ".", true},
		{"dotdot_bare", "..", true},
		{"dot_first", "./assets/app.js", true},
		{"dot_middle", "/assets/./app.js", true},
		{"dot_last", "/assets/.", true},
		{"dotdot_first", "../assets/app.js", true},
		{"dotdot_middle", "/assets/../app.js", true},
		{"dotdot_last", "/assets/..", true},
		{"dot_trailing_slash", "/./", true},
		{"dotdot_repeated_slash", "///..///", true},
		{"triple_dot", "/.../asset", false},
		{"embedded_dots", "/a.b/.hidden/end..", false},
		{"literal_percent_dots", "/%2e/%2E%2e/%252e", false},
		{"literal_percent_separators", "/a%2fb/%5C/%2F..", false},
		{"literal_malformed_percent", "/%/%2/%gg", false},
		{"literal_backslash", `/a\..\b`, false},
		{"unicode", "/目录/文件/é", false},
		{"unicode_dot_lookalikes", "/．/。/‥", false},
		{"unicode_real_dot", "/目录/../文件", true},
		{"nul", "/a\x00b/..\x00/\x00.", false},
		{"invalid_utf8", string([]byte{'/', 0xff, '/', 0xc0, 0xaf, '/', '.', 0xfe}), false},
		{"invalid_utf8_dot", string([]byte{'/', 0xff, '/', '.', '.', '/', 0xfe}), true},
		{"long_safe", strings.Repeat("/segment", 2048), false},
		{"long_early_dot", "/." + strings.Repeat("/segment", 2048), true},
		{"long_middle_dot", strings.Repeat("/segment", 1024) + "/../" + strings.Repeat("segment/", 1024), true},
		{"long_late_dot", strings.Repeat("/segment", 2048) + "/..", true},
	}
}

func TestPublicPathDotScanSplitEquivalence(t *testing.T) {
	for _, tc := range publicPathDotScanCorpus() {
		t.Run(tc.name, func(t *testing.T) {
			if got := publicPathDotScanSplitOracle(tc.path); got != tc.want {
				t.Fatalf("old oracle = %v, want %v", got, tc.want)
			}
			if got := containsPublicPathDotSegment(tc.path); got != tc.want {
				t.Fatalf("candidate = %v, want %v", got, tc.want)
			}
			// A manually built request makes this check cover arbitrary decoded
			// bytes without asking URL parsing to accept invalid UTF-8 or NUL.
			r := &http.Request{URL: &url.URL{Path: tc.path}, RequestURI: tc.path}
			if got := classifyPublicRequestPath(r).DecodedDotSegment; got != tc.want {
				t.Fatalf("decoded path classification = %v, want %v", got, tc.want)
			}
		})
	}
}

func FuzzPublicPathDotScanSplitEquivalence(f *testing.F) {
	for _, tc := range publicPathDotScanCorpus() {
		f.Add(tc.path)
	}
	f.Fuzz(func(t *testing.T, path string) {
		// Fixed memory/work bound for adversarial fuzzer-generated strings.
		// The deterministic corpus includes paths up to about 16 KiB.
		if len(path) > 64*1024 {
			return
		}
		want := publicPathDotScanSplitOracle(path)
		if got := containsPublicPathDotSegment(path); got != want {
			t.Fatalf("path = %q: got %v, old oracle %v", path, got, want)
		}
	})
}

var publicPathDotScanBenchmarkSink bool
var publicPathDotClassificationBenchmarkSink publicPathIssueSet

func publicPathDotScanBenchmarkCases() []publicPathDotScanCase {
	return []publicPathDotScanCase{
		{"empty", "", false},
		{"root", "/", false},
		{"safe_one", "/asset", false},
		{"safe_short", "/assets/app.js", false},
		{"safe_unicode", "/目录/文件/é", false},
		{"safe_percent_literal", "/api/%2e/%2F/project", false},
		{"safe_embedded_dot", "/v1.2/.hidden/end..", false},
		{"safe_repeated_slash", "//assets///app.js//", false},
		{"safe_16_segments", strings.Repeat("/segment", 16), false},
		{"safe_128_segments", strings.Repeat("/segment", 128), false},
		{"safe_2048_segments", strings.Repeat("/segment", 2048), false},
		{"dot_early", "/./assets/app.js", true},
		{"dot_middle", "/assets/./app.js", true},
		{"dot_late", "/assets/app.js/.", true},
		{"long_dotdot_early", "/.." + strings.Repeat("/segment", 2048), true},
		{"long_dotdot_middle", strings.Repeat("/segment", 1024) + "/../" + strings.Repeat("segment/", 1024), true},
		{"long_dotdot_late", strings.Repeat("/segment", 2048) + "/..", true},
		{"invalid_utf8_safe", string([]byte{'/', 0xff, '/', '.', 0xfe}), false},
	}
}

// Run the same test file against A (unchanged helper) and B (one-line candidate).
// Inputs and any URL construction happen before the timed loop.
func BenchmarkPublicPathDotScan(b *testing.B) {
	for _, tc := range publicPathDotScanBenchmarkCases() {
		b.Run(tc.name, func(b *testing.B) {
			if got := containsPublicPathDotSegment(tc.path); got != tc.want {
				b.Fatalf("got %v, want %v", got, tc.want)
			}
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				publicPathDotScanBenchmarkSink = containsPublicPathDotSegment(tc.path)
			}
		})
	}
}

func BenchmarkPublicPathClassificationDotScan(b *testing.B) {
	cases := []struct {
		name string
		req  *http.Request
	}{
		{"nil_request", nil},
		{"nil_url", &http.Request{}},
		{"safe_short", &http.Request{URL: &url.URL{Path: "/assets/app.js"}, RequestURI: "/assets/app.js"}},
		{"literal_encoded_dot", &http.Request{URL: &url.URL{Path: "/assets/%2e/app.js"}, RequestURI: "/assets/%252e/app.js"}},
		{"decoded_dot", &http.Request{URL: &url.URL{Path: "/assets/../app.js", RawPath: "/assets/%2e%2e/app.js"}, RequestURI: "/assets/%2e%2e/app.js"}},
		{"encoded_separator", &http.Request{URL: &url.URL{Path: "/a/b", RawPath: "/a%2Fb"}, RequestURI: "/a%2Fb"}},
		{"safe_128_segments", &http.Request{URL: &url.URL{Path: strings.Repeat("/segment", 128)}, RequestURI: strings.Repeat("/segment", 128)}},
	}
	for _, tc := range cases {
		b.Run(tc.name, func(b *testing.B) {
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				publicPathDotClassificationBenchmarkSink = classifyPublicRequestPath(tc.req)
			}
		})
	}
}
