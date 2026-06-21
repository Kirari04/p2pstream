package server

import (
	"net/http"
	"net/http/httptest"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"

	p2pstreamv1 "p2pstream/gen/proto/p2pstream/v1"
	"p2pstream/internal/db"
)

func TestPublicRateLimiterAlgorithms(t *testing.T) {
	t.Run("fixed window", func(t *testing.T) {
		rule := testRateLimitRule(publicRateLimitAlgorithmFixedWindow, 2, 1000, 0)
		state := &publicRateLimitKeyRuntime{}
		if !state.allow(rule, time.UnixMilli(100)).allowed {
			t.Fatal("first request rejected")
		}
		if !state.allow(rule, time.UnixMilli(200)).allowed {
			t.Fatal("second request rejected")
		}
		if state.allow(rule, time.UnixMilli(300)).allowed {
			t.Fatal("third request allowed in same fixed window")
		}
		if !state.allow(rule, time.UnixMilli(1000)).allowed {
			t.Fatal("request rejected after fixed window reset")
		}
	})

	t.Run("sliding window", func(t *testing.T) {
		rule := testRateLimitRule(publicRateLimitAlgorithmSlidingWindow, 2, 1000, 0)
		state := &publicRateLimitKeyRuntime{}
		if !state.allow(rule, time.UnixMilli(0)).allowed {
			t.Fatal("first request rejected")
		}
		if !state.allow(rule, time.UnixMilli(100)).allowed {
			t.Fatal("second request rejected")
		}
		if state.allow(rule, time.UnixMilli(500)).allowed {
			t.Fatal("third request allowed inside sliding window")
		}
		if !state.allow(rule, time.UnixMilli(1001)).allowed {
			t.Fatal("request rejected after old hit aged out")
		}
	})

	t.Run("token bucket", func(t *testing.T) {
		rule := testRateLimitRule(publicRateLimitAlgorithmTokenBucket, 2, 1000, 4)
		state := &publicRateLimitKeyRuntime{}
		for i := 0; i < 4; i++ {
			if !state.allow(rule, time.UnixMilli(0)).allowed {
				t.Fatalf("burst request %d rejected", i+1)
			}
		}
		if state.allow(rule, time.UnixMilli(0)).allowed {
			t.Fatal("request allowed after token bucket burst was exhausted")
		}
		if !state.allow(rule, time.UnixMilli(500)).allowed {
			t.Fatal("request rejected after token refill")
		}
	})

	t.Run("leaky bucket", func(t *testing.T) {
		rule := testRateLimitRule(publicRateLimitAlgorithmLeakyBucket, 2, 1000, 2)
		state := &publicRateLimitKeyRuntime{}
		if !state.allow(rule, time.UnixMilli(0)).allowed {
			t.Fatal("first request rejected")
		}
		if !state.allow(rule, time.UnixMilli(0)).allowed {
			t.Fatal("second request rejected")
		}
		if state.allow(rule, time.UnixMilli(0)).allowed {
			t.Fatal("request allowed over leaky bucket capacity")
		}
		if !state.allow(rule, time.UnixMilli(500)).allowed {
			t.Fatal("request rejected after leak drain")
		}
	})
}

func TestPublicRateLimiterMatchingAndCompositeKeys(t *testing.T) {
	rule := testRateLimitRule(publicRateLimitAlgorithmFixedWindow, 1, 1000, 0)
	rule.Match = mustPublicPolicyMatchCEL(t, `method == "GET" &&
		protocol == "http" &&
		host_match(host, "api.example.com") &&
		path_prefix(path, "/api") &&
		headers["x-plan"].exists(v, v == "free")`)
	rule.KeyParts = []publicRateLimitKeyPartConfig{
		{Source: publicRateLimitKeySourceRemoteIP},
		{Source: publicRateLimitKeySourceHeader, Name: "X-User"},
	}
	rule.Fingerprint = publicRateLimitRuleFingerprint(rule)

	limiter := newPublicRateLimiter()
	listener := publicListenerConfig{ID: 1, Protocol: publicListenerProtocolHTTP}
	first := testRateLimitRequest("GET", "http://api.example.com/api/data", "198.51.100.9:1234")
	first.Header.Set("X-Plan", "free")
	first.Header.Set("X-User", "alice")
	if _, allowed := limiter.evaluate([]publicRateLimitRuleConfig{rule}, listener, first, time.UnixMilli(100)); !allowed {
		t.Fatal("first matching request rejected")
	}

	second := testRateLimitRequest("GET", "http://api.example.com/api/data", "198.51.100.9:1234")
	second.Header.Set("X-Plan", "free")
	second.Header.Set("X-User", "alice")
	if decision, allowed := limiter.evaluate([]publicRateLimitRuleConfig{rule}, listener, second, time.UnixMilli(200)); allowed {
		t.Fatal("second request with same composite key allowed")
	} else if decision.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want %d", decision.StatusCode, http.StatusTooManyRequests)
	}

	third := testRateLimitRequest("GET", "http://api.example.com/api/data", "198.51.100.9:1234")
	third.Header.Set("X-Plan", "free")
	third.Header.Set("X-User", "bob")
	if _, allowed := limiter.evaluate([]publicRateLimitRuleConfig{rule}, listener, third, time.UnixMilli(250)); !allowed {
		t.Fatal("different header key was not isolated")
	}

	nonMatching := testRateLimitRequest("GET", "http://api.example.com/public", "198.51.100.9:1234")
	nonMatching.Header.Set("X-Plan", "free")
	nonMatching.Header.Set("X-User", "alice")
	if _, allowed := limiter.evaluate([]publicRateLimitRuleConfig{rule}, listener, nonMatching, time.UnixMilli(300)); !allowed {
		t.Fatal("non-matching path was rate limited")
	}

	confusing := testRateLimitRequest("GET", "http://api.example.com/apiv2/data", "198.51.100.9:1234")
	confusing.Header.Set("X-Plan", "free")
	confusing.Header.Set("X-User", "alice")
	if _, allowed := limiter.evaluate([]publicRateLimitRuleConfig{rule}, listener, confusing, time.UnixMilli(350)); !allowed {
		t.Fatal("path prefix /api matched /apiv2")
	}
}

func TestPathPrefixMatchesSegmentBoundaries(t *testing.T) {
	for _, tc := range []struct {
		path   string
		prefix string
		want   bool
	}{
		{path: "/api", prefix: "/api", want: true},
		{path: "/api/", prefix: "/api", want: true},
		{path: "/api/users", prefix: "/api", want: true},
		{path: "/apiv2", prefix: "/api", want: false},
		{path: "/apiary", prefix: "/api", want: false},
	} {
		if got := pathPrefixMatches(tc.path, tc.prefix); got != tc.want {
			t.Fatalf("pathPrefixMatches(%q, %q) = %v, want %v", tc.path, tc.prefix, got, tc.want)
		}
	}
}

func TestPublicRateLimitValidationRejectsUnsafeInput(t *testing.T) {
	if _, err := validatePublicRateLimitRuleInput(
		"bad-burst",
		100,
		true,
		p2pstreamv1.PublicRateLimitAlgorithm_PUBLIC_RATE_LIMIT_ALGORITHM_TOKEN_BUCKET,
		10,
		1000,
		101,
		nil,
		429,
		"",
		p2pstreamv1.PublicResponseBodyMode_PUBLIC_RESPONSE_BODY_MODE_INLINE,
		0,
		"",
		nil,
		nil,
	); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("expected invalid burst error, got %v", err)
	}

	if _, err := validatePublicRateLimitRuleInput(
		"bad-header",
		100,
		true,
		p2pstreamv1.PublicRateLimitAlgorithm_PUBLIC_RATE_LIMIT_ALGORITHM_FIXED_WINDOW,
		10,
		1000,
		0,
		nil,
		429,
		"",
		p2pstreamv1.PublicResponseBodyMode_PUBLIC_RESPONSE_BODY_MODE_INLINE,
		0,
		"",
		[]*p2pstreamv1.PublicRateLimitResponseHeader{{Name: "Content-Length", Value: "1"}},
		nil,
	); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("expected protected header error, got %v", err)
	}

	if _, err := validatePublicRateLimitRuleInput(
		"missing-template-id",
		100,
		true,
		p2pstreamv1.PublicRateLimitAlgorithm_PUBLIC_RATE_LIMIT_ALGORITHM_FIXED_WINDOW,
		10,
		1000,
		0,
		nil,
		429,
		"",
		p2pstreamv1.PublicResponseBodyMode_PUBLIC_RESPONSE_BODY_MODE_TEMPLATE,
		0,
		"",
		nil,
		nil,
	); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("expected missing template id error, got %v", err)
	}
}

func TestUnsafeForwardingKeyPartHeader(t *testing.T) {
	tests := []struct {
		name string
		want bool
	}{
		{name: "Forwarded", want: true},
		{name: "forwarded", want: true},
		{name: "X-Forwarded-For", want: true},
		{name: "x-forwarded-for", want: true},
		{name: "X-Real-IP", want: true},
		{name: "x-real-ip", want: true},
		{name: "X-FoRwArDeD-PoRt", want: true},
		{name: "CF-Connecting-IP", want: true},
		{name: "X-Plan", want: false},
		{name: "X-User", want: false},
	}
	for _, tt := range tests {
		if got := unsafeForwardingKeyPartHeader(tt.name); got != tt.want {
			t.Fatalf("unsafeForwardingKeyPartHeader(%q) = %v, want %v", tt.name, got, tt.want)
		}
	}
}

func TestPublicRateLimitValidationRejectsUnsafeForwardingHeaderKeyParts(t *testing.T) {
	for _, header := range []string{
		"Forwarded",
		"X-Forwarded-For",
		"X-Forwarded-Host",
		"X-Forwarded-Proto",
		"X-Forwarded-Port",
		"x-real-ip",
		"Client-IP",
		"True-Client-IP",
		"CF-Connecting-IP",
		"Fastly-Client-IP",
		"X-Client-IP",
		"X-Cluster-Client-IP",
	} {
		_, err := validatePublicRateLimitRuleInput(
			"unsafe-key",
			100,
			true,
			p2pstreamv1.PublicRateLimitAlgorithm_PUBLIC_RATE_LIMIT_ALGORITHM_FIXED_WINDOW,
			10,
			1000,
			0,
			[]*p2pstreamv1.PublicRateLimitKeyPart{{
				Source: p2pstreamv1.PublicRateLimitKeySource_PUBLIC_RATE_LIMIT_KEY_SOURCE_HEADER,
				Name:   header,
			}},
			429,
			"",
			p2pstreamv1.PublicResponseBodyMode_PUBLIC_RESPONSE_BODY_MODE_INLINE,
			0,
			"",
			nil,
			nil,
		)
		if connect.CodeOf(err) != connect.CodeInvalidArgument {
			t.Fatalf("header %q: expected invalid argument, got %v", header, err)
		}
		if !strings.Contains(err.Error(), "rate limit header key part must not use forwarding or client IP headers; use REMOTE_IP") {
			t.Fatalf("header %q: unexpected error %v", header, err)
		}
	}
}

func TestPublicRateLimitValidationAllowsApplicationHeaderKeyPart(t *testing.T) {
	params, err := validatePublicRateLimitRuleInput(
		"safe-key",
		100,
		true,
		p2pstreamv1.PublicRateLimitAlgorithm_PUBLIC_RATE_LIMIT_ALGORITHM_FIXED_WINDOW,
		10,
		1000,
		0,
		[]*p2pstreamv1.PublicRateLimitKeyPart{{
			Source: p2pstreamv1.PublicRateLimitKeySource_PUBLIC_RATE_LIMIT_KEY_SOURCE_HEADER,
			Name:   "x-plan",
		}},
		429,
		"",
		p2pstreamv1.PublicResponseBodyMode_PUBLIC_RESPONSE_BODY_MODE_INLINE,
		0,
		"",
		nil,
		nil,
	)
	if err != nil {
		t.Fatalf("validate application header key part: %v", err)
	}
	if !strings.Contains(params.KeyPartsJSON, `"name":"X-Plan"`) {
		t.Fatalf("key parts json = %q, want canonical X-Plan", params.KeyPartsJSON)
	}

	params, err = validatePublicRateLimitRuleInput(
		"default-key",
		100,
		true,
		p2pstreamv1.PublicRateLimitAlgorithm_PUBLIC_RATE_LIMIT_ALGORITHM_FIXED_WINDOW,
		10,
		1000,
		0,
		nil,
		429,
		"",
		p2pstreamv1.PublicResponseBodyMode_PUBLIC_RESPONSE_BODY_MODE_INLINE,
		0,
		"",
		nil,
		nil,
	)
	if err != nil {
		t.Fatalf("validate default remote IP key part: %v", err)
	}
	if !strings.Contains(params.KeyPartsJSON, `"source":"remote_ip"`) {
		t.Fatalf("key parts json = %q, want remote_ip", params.KeyPartsJSON)
	}
}

func TestPublicRateLimitStoredRuleRejectsUnsafeForwardingHeaderKeyPart(t *testing.T) {
	_, err := publicRateLimitRuleRowToConfig(db.PublicRateLimitRule{
		Name:         "stored-unsafe",
		KeyPartsJson: `[{"source":"header","name":"x-forwarded-for"}]`,
	})
	if err == nil {
		t.Fatal("expected stored unsafe forwarding header key part to be rejected")
	}
	if !strings.Contains(err.Error(), "rate limit header key part must not use forwarding or client IP headers; use REMOTE_IP") {
		t.Fatalf("unexpected stored-row error: %v", err)
	}
}

func TestPublicRateLimitRemoteIPKeyUsesRemoteAddr(t *testing.T) {
	rule := testRateLimitRule(publicRateLimitAlgorithmFixedWindow, 10, 1000, 0)
	rule.KeyParts = []publicRateLimitKeyPartConfig{{Source: publicRateLimitKeySourceRemoteIP}}
	listener := publicListenerConfig{ID: 1, Protocol: publicListenerProtocolHTTP}
	req := testRateLimitRequest("GET", "http://example.com/api", "198.51.100.9:1234")
	req.Header.Set("X-Forwarded-For", "203.0.113.9")

	values := rule.keyValues(listener, req)
	if !rateLimitTestValuesContain(values, "198.51.100.9") {
		t.Fatalf("key values = %#v, want remote addr IP", values)
	}
	if rateLimitTestValuesContain(values, "203.0.113.9") {
		t.Fatalf("key values = %#v, used spoofed X-Forwarded-For", values)
	}
}

func TestPublicRateLimitResponseUsesGeneratedHeaders(t *testing.T) {
	rule := testRateLimitRule(publicRateLimitAlgorithmFixedWindow, 1, 1000, 0)
	rule.ResponseHeaders = []publicRateLimitResponseHeaderConfig{
		{Name: "X-Custom", Value: "limited"},
	}
	decision := publicRateLimitDecision{
		Rule:       rule,
		StatusCode: http.StatusTooManyRequests,
		Body:       "blocked\n",
		Headers:    rateLimitGeneratedHeaders(rule, publicRateLimitAllowResult{retryAfter: time.Second}, time.Unix(100, 0)),
	}
	recorder := httptest.NewRecorder()
	writeRateLimitResponse(recorder, decision)
	if recorder.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusTooManyRequests)
	}
	if got := recorder.Header().Get("X-Custom"); got != "limited" {
		t.Fatalf("custom header = %q", got)
	}
	if got := recorder.Header().Get("RateLimit-Limit"); got != "1" {
		t.Fatalf("RateLimit-Limit = %q, want 1", got)
	}
	if got := recorder.Body.String(); got != "blocked\n" {
		t.Fatalf("body = %q", got)
	}
}

func TestPublicRateLimiterPrunesIdleKeys(t *testing.T) {
	rule := testRateLimitRule(publicRateLimitAlgorithmFixedWindow, 10, 1000, 0)
	rule.KeyParts = []publicRateLimitKeyPartConfig{{Source: publicRateLimitKeySourceHeader, Name: "X-User"}}
	rule.Fingerprint = publicRateLimitRuleFingerprint(rule)
	limiter := newPublicRateLimiter()
	listener := publicListenerConfig{ID: 1, Protocol: publicListenerProtocolHTTP}

	first := testRateLimitRequest("GET", "http://example.com/api", "198.51.100.9:1234")
	first.Header.Set("X-User", "old")
	if _, allowed := limiter.evaluate([]publicRateLimitRuleConfig{rule}, listener, first, time.Unix(1, 0)); !allowed {
		t.Fatal("first request rejected")
	}
	second := testRateLimitRequest("GET", "http://example.com/api", "198.51.100.9:1234")
	second.Header.Set("X-User", "new")
	if _, allowed := limiter.evaluate([]publicRateLimitRuleConfig{rule}, listener, second, time.Unix(1, 0).Add(rateLimitIdleStateTTL+rateLimitPruneInterval+time.Second)); !allowed {
		t.Fatal("second request rejected")
	}
	runtime := limiter.rules[rule.ID]
	if got := len(runtime.keys); got != 1 {
		t.Fatalf("runtime keys = %d, want 1 after pruning idle key", got)
	}
}

func TestPublicRateLimitHostKeyCanonicalizesDottedHostWithPort(t *testing.T) {
	rule := testRateLimitRule(publicRateLimitAlgorithmFixedWindow, 10, 1000, 0)
	rule.KeyParts = []publicRateLimitKeyPartConfig{{Source: publicRateLimitKeySourceHost}}
	listener := publicListenerConfig{ID: 1, Protocol: publicListenerProtocolHTTP}

	plain := testRateLimitRequest("GET", "http://example.com/api", "198.51.100.9:1234")
	plain.Host = "example.com:443"
	dotted := testRateLimitRequest("GET", "http://example.com/api", "198.51.100.9:1234")
	dotted.Host = "example.com.:443"

	if got, want := rule.keyValues(listener, dotted), rule.keyValues(listener, plain); !reflect.DeepEqual(got, want) {
		t.Fatalf("dotted host key values = %#v, want %#v", got, want)
	}
}

func TestPublicRateLimiterCapsPerRuleKeys(t *testing.T) {
	rule := testRateLimitRule(publicRateLimitAlgorithmFixedWindow, 10, 1000, 0)
	rule.KeyParts = []publicRateLimitKeyPartConfig{{Source: publicRateLimitKeySourceHeader, Name: "X-User"}}
	rule.Fingerprint = publicRateLimitRuleFingerprint(rule)
	limiter := newPublicRateLimiter()
	listener := publicListenerConfig{ID: 1, Protocol: publicListenerProtocolHTTP}
	now := time.Unix(1, 0)

	for i := 0; i < maxRateLimitKeysPerRule+1; i++ {
		req := testRateLimitRequest("GET", "http://example.com/api", "198.51.100.9:1234")
		req.Header.Set("X-User", strconv.Itoa(i))
		if _, allowed := limiter.evaluate([]publicRateLimitRuleConfig{rule}, listener, req, now.Add(time.Duration(i)*time.Millisecond)); !allowed {
			t.Fatalf("request %d rejected", i)
		}
	}
	runtime := limiter.rules[rule.ID]
	if got := len(runtime.keys); got != maxRateLimitKeysPerRule {
		t.Fatalf("runtime keys = %d, want capped at %d", got, maxRateLimitKeysPerRule)
	}
}

func testRateLimitRule(algorithm string, limit int64, windowMillis int64, burst int64) publicRateLimitRuleConfig {
	rule := publicRateLimitRuleConfig{
		ID:                  1,
		Name:                "test",
		Priority:            100,
		Enabled:             true,
		Algorithm:           algorithm,
		Limit:               limit,
		WindowMillis:        windowMillis,
		Burst:               burst,
		KeyParts:            []publicRateLimitKeyPartConfig{{Source: publicRateLimitKeySourceRemoteIP}},
		ResponseStatusCode:  http.StatusTooManyRequests,
		ResponseBody:        defaultRateLimitBody,
		ResponseContentType: defaultRateLimitContentType,
		UpdatedAt:           time.Unix(1, 0),
	}
	rule.Fingerprint = publicRateLimitRuleFingerprint(rule)
	return rule
}

func testRateLimitRequest(method string, target string, remoteAddr string) *http.Request {
	req := httptest.NewRequest(method, target, nil)
	req.RemoteAddr = remoteAddr
	return req
}

func rateLimitTestValuesContain(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
