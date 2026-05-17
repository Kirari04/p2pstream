package server

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"connectrpc.com/connect"

	p2pstreamv1 "p2pstream/gen/proto/p2pstream/v1"
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
	rule.Match = publicRateLimitMatchConfig{
		Methods:      []string{"GET"},
		Protocols:    []string{publicListenerProtocolHTTP},
		HostPatterns: []string{"api.example.com"},
		PathPrefixes: []string{"/api"},
		Headers: []publicRateLimitValueMatcherConfig{{
			Name:     "X-Plan",
			Operator: publicRateLimitMatchOperatorEquals,
			Value:    "free",
		}},
	}
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
		nil,
		429,
		"",
		p2pstreamv1.PublicResponseBodyMode_PUBLIC_RESPONSE_BODY_MODE_INLINE,
		0,
		"",
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
		nil,
		429,
		"",
		p2pstreamv1.PublicResponseBodyMode_PUBLIC_RESPONSE_BODY_MODE_INLINE,
		0,
		"",
		[]*p2pstreamv1.PublicRateLimitResponseHeader{{Name: "Content-Length", Value: "1"}},
	); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("expected protected header error, got %v", err)
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
