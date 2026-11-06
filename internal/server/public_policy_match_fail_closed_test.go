package server

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

const publicPolicyMatchCostTriggerValues = 5000

func TestPublicPolicyMatchCostLimitReturnsEvaluationError(t *testing.T) {
	tests := []struct {
		name       string
		expression string
		request    func() *http.Request
	}{
		{
			name:       "repeated header",
			expression: `headers["x"].exists(v, v == "match")`,
			request: func() *http.Request {
				req := httptest.NewRequest(http.MethodGet, "http://example.test/", nil)
				req.Header["X"] = policyMatchCostValues()
				return req
			},
		},
		{
			name:       "repeated query",
			expression: `query["q"].exists(v, v == "match")`,
			request: func() *http.Request {
				req := httptest.NewRequest(http.MethodGet, "http://example.test/", nil)
				values := make(url.Values)
				values["q"] = policyMatchCostValues()
				req.URL.RawQuery = values.Encode()
				return req
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			match := mustPublicPolicyMatchCEL(t, tt.expression)
			request := tt.request()
			assertPolicyMatchRequestWithinDefaultHeaderBudget(t, request)
			matched, err := match.evaluate(publicListenerConfig{Protocol: publicListenerProtocolHTTP}, request)
			if err == nil || matched {
				t.Fatalf("cost-limited evaluation = matched %t, err %v; want false with error", matched, err)
			}
			if !strings.Contains(err.Error(), "cost limit") {
				t.Fatalf("evaluation error = %v, want cost limit", err)
			}

			matchingControl := tt.request()
			setPolicyMatchControlValues(matchingControl, true)
			if matched, err := match.evaluate(publicListenerConfig{Protocol: publicListenerProtocolHTTP}, matchingControl); err != nil || !matched {
				t.Fatalf("ordinary matching control = matched %t, err %v; want true, nil", matched, err)
			}
			nonMatchingControl := tt.request()
			setPolicyMatchControlValues(nonMatchingControl, false)
			if matched, err := match.evaluate(publicListenerConfig{Protocol: publicListenerProtocolHTTP}, nonMatchingControl); err != nil || matched {
				t.Fatalf("ordinary non-matching control = matched %t, err %v; want false, nil", matched, err)
			}
		})
	}
}

func TestPublicPolicyMatchCostFailureFailsClosedForEnforcementConsumers(t *testing.T) {
	listener := publicListenerConfig{ID: 1, Protocol: publicListenerProtocolHTTP}

	t.Run("WAF", func(t *testing.T) {
		rule := testWafRule(1, publicWafActionBlock)
		rule.Match = mustPublicPolicyMatchCEL(t, `headers["x"].exists(v, v == "match")`)
		snap := testWafSnapshot(rule, nil)
		req := httptest.NewRequest(http.MethodGet, "http://example.test/", nil)
		req.Header["X"] = policyMatchCostValues()
		assertPolicyMatchRequestWithinDefaultHeaderBudget(t, req)
		app := &App{PublicWAF: newPublicWAF()}

		decision, allowed := app.PublicWAF.evaluate(snap, snap.Listeners[1], req, time.Unix(1, 0), app)
		if allowed || decision.MatchError == nil || decision.StatusCode != http.StatusServiceUnavailable || decision.ErrorKind != publicPolicyMatchFailureErrorKind {
			t.Fatalf("WAF decision = allowed %t, %+v; want local policy failure", allowed, decision)
		}
		recorder := httptest.NewRecorder()
		writePublicWafResponse(recorder, req, decision)
		if recorder.Code != http.StatusServiceUnavailable || recorder.Body.String() != publicPolicyMatchFailureBody {
			t.Fatalf("WAF response = %d %q, want 503 %q", recorder.Code, recorder.Body.String(), publicPolicyMatchFailureBody)
		}
	})

	t.Run("rate limiter", func(t *testing.T) {
		rule := testRateLimitRule(publicRateLimitAlgorithmFixedWindow, 100, 1000, 0)
		rule.Match = mustPublicPolicyMatchCEL(t, `query["q"].exists(v, v == "match")`)
		rule.Fingerprint = publicRateLimitRuleFingerprint(rule)
		req := httptest.NewRequest(http.MethodGet, "http://example.test/", nil)
		values := make(url.Values)
		values["q"] = policyMatchCostValues()
		req.URL.RawQuery = values.Encode()
		assertPolicyMatchRequestWithinDefaultHeaderBudget(t, req)

		decision, allowed := newPublicRateLimiter().evaluate([]publicRateLimitRuleConfig{rule}, listener, req, time.Unix(1, 0))
		if allowed || decision.MatchError == nil || decision.StatusCode != http.StatusServiceUnavailable || decision.ErrorKind != publicPolicyMatchFailureErrorKind {
			t.Fatalf("rate-limit decision = allowed %t, %+v; want local policy failure", allowed, decision)
		}
		recorder := httptest.NewRecorder()
		writeRateLimitResponse(recorder, decision)
		if recorder.Code != http.StatusServiceUnavailable || recorder.Body.String() != publicPolicyMatchFailureBody {
			t.Fatalf("rate-limit response = %d %q, want 503 %q", recorder.Code, recorder.Body.String(), publicPolicyMatchFailureBody)
		}
	})

	t.Run("traffic shaper", func(t *testing.T) {
		rule := testTrafficShaperRule(1, "cost", 10, publicTrafficShaperBudgetScopePerRequest, 1024, 1024)
		rule.Match = mustPublicPolicyMatchCEL(t, `headers["x"].exists(v, v == "match")`)
		rule.Fingerprint = publicTrafficShaperRuleFingerprint(rule)
		req := httptest.NewRequest(http.MethodGet, "http://example.test/", nil)
		req.Header["X"] = policyMatchCostValues()

		decision, selected := newPublicTrafficShaper().evaluate([]publicTrafficShaperRuleConfig{rule}, listener, req, time.Unix(1, 0))
		if !selected || decision.MatchError == nil || decision.UploadBucket != nil || decision.DownloadBucket != nil {
			t.Fatalf("traffic-shaper decision = selected %t, %+v; want policy failure without buckets", selected, decision)
		}
	})
}

func TestPublicPolicyMatchCostFailureStopsOptionalRuleFallback(t *testing.T) {
	listener := publicListenerConfig{ID: 1, Protocol: publicListenerProtocolHTTP}
	req := httptest.NewRequest(http.MethodGet, "http://example.test/", nil)
	req.Header["X"] = policyMatchCostValues()
	costMatch := mustPublicPolicyMatchCEL(t, `headers["x"].exists(v, v == "match")`)

	cacheRules := []publicCacheRuleConfig{
		{ID: 1, Enabled: true, Match: costMatch},
		{ID: 2, Enabled: true},
	}
	if selected, ok, err := selectPublicCacheRule(cacheRules, listener, req, publicRouteResolution{}); err == nil || ok || selected.ID != 0 {
		t.Fatalf("cache selection = rule %d, ok %t, err %v; want evaluation error without catch-all fallback", selected.ID, ok, err)
	}

	retryRules := []publicRetryRuleConfig{
		{ID: 1, Enabled: true, AllMethods: true, Match: costMatch},
		{ID: 2, Enabled: true, AllMethods: true},
	}
	resolution := publicRouteResolution{Listener: listener, Target: publicRouteTargetConfig{Transport: publicRouteTargetTransportAgent}}
	if selected := selectPublicRetryRule(&publicProxySnapshot{RetryRules: retryRules}, req, resolution); selected != nil {
		t.Fatalf("retry selection = rule %d, want no retry after evaluation error", selected.ID)
	}
}

func TestPublicCachePolicyCostFailureBypassesRequest(t *testing.T) {
	app, resolution, closeDB := newTestPublicCacheApp(t)
	defer closeDB()
	req := httptest.NewRequest(http.MethodGet, "http://assets.example.test/assets/app.txt", nil)
	req.Header["X"] = policyMatchCostValues()
	costRule := app.currentPublicSnapshot().CacheRules[0]
	costRule.ID = 100
	costRule.Priority = 1
	costRule.Match = mustPublicPolicyMatchCEL(t, `headers["x"].exists(v, v == "match")`)
	catchAll := costRule
	catchAll.ID = 101
	catchAll.Priority = 2
	catchAll.Match = publicPolicyMatchConfig{}
	snapshot := *app.currentPublicSnapshot()
	snapshot.CacheRules = []publicCacheRuleConfig{costRule, catchAll}

	decision := app.checkPublicCacheWithSnapshot(&snapshot, req, resolution)
	if decision.Status != publicCacheStatusBypass || decision.BypassReason != publicPolicyMatchFailureErrorKind || decision.Cacheable {
		t.Fatalf("cache decision = %+v, want policy-match bypass without catch-all fallback", decision)
	}
	assertPublicCacheStorageStats(t, app, 0, 0, 0)
}

func TestPublicTrafficShaperProtocolScopeSkipsIrrelevantPolicyEvaluation(t *testing.T) {
	listener := publicListenerConfig{ID: 1, Protocol: publicListenerProtocolHTTP}
	req := httptest.NewRequest(http.MethodGet, "http://example.test/", nil)
	req.Header["X"] = policyMatchCostValues()
	match := mustPublicPolicyMatchCEL(t, `headers["x"].exists(v, v == "match")`)

	disabled := testTrafficShaperRule(1, "disabled", 10, publicTrafficShaperBudgetScopePerRequest, 1024, 1024)
	disabled.Enabled = false
	disabled.Match = match
	webSocketOnly := testTrafficShaperRule(2, "websocket", 20, publicTrafficShaperBudgetScopePerRequest, 1024, 1024)
	webSocketOnly.ProtocolScope = publicTrafficShaperProtocolScopeWebSocketOnly
	webSocketOnly.Match = match

	if decision, selected := newPublicTrafficShaper().evaluate([]publicTrafficShaperRuleConfig{disabled, webSocketOnly}, listener, req, time.Unix(1, 0)); selected || decision.MatchError != nil {
		t.Fatalf("irrelevant shapers selected=%t decision=%+v, want clean skip", selected, decision)
	}
}

func TestPublicTrafficShaperResponseFallbackPolicyFailureStopsResponseBody(t *testing.T) {
	listener := publicListenerConfig{ID: 1, Protocol: publicListenerProtocolHTTP}
	webSocketRule := testTrafficShaperRule(1, "websocket", 10, publicTrafficShaperBudgetScopePerRequest, 0, 1024)
	webSocketRule.ProtocolScope = publicTrafficShaperProtocolScopeWebSocketOnly
	httpRule := testTrafficShaperRule(2, "http", 20, publicTrafficShaperBudgetScopePerRequest, 0, 1024)
	httpRule.ProtocolScope = publicTrafficShaperProtocolScopeWebSocketExcluded
	httpRule.Match = mustPublicPolicyMatchCEL(t, `headers["x"].exists(v, v == "match")`)
	httpRule.Fingerprint = publicTrafficShaperRuleFingerprint(httpRule)
	rules := []publicTrafficShaperRuleConfig{webSocketRule, httpRule}
	sortPublicTrafficShaperRules(rules)
	snapshot := &publicProxySnapshot{
		Listeners:          map[int64]publicListenerConfig{listener.ID: listener},
		TrafficShaperRules: rules,
	}

	newRequest := func() *http.Request {
		req := httptest.NewRequest(http.MethodGet, "http://example.test/chat", nil)
		req.Header.Set("Connection", "Upgrade")
		req.Header.Set("Upgrade", "websocket")
		req.Header.Set("Sec-WebSocket-Version", "13")
		req.Header.Set("Sec-WebSocket-Key", "dGhlIHNhbXBsZSBub25jZQ==")
		req.Header["X"] = policyMatchCostValues()
		assertPolicyMatchRequestWithinDefaultHeaderBudget(t, req)
		return req
	}

	app := NewApp(nil, nil)
	app.TrafficShaper = newPublicTrafficShaper()

	t.Run("static target", func(t *testing.T) {
		req := newRequest()
		initial, selected := app.selectPublicTrafficShaperWithSnapshot(snapshot, listener.ID, req)
		if !selected || initial.MatchError != nil || initial.Rule.ID != webSocketRule.ID {
			t.Fatalf("initial shaper = selected %t, %+v; want WebSocket rule", selected, initial)
		}
		resolution := publicRouteResolution{
			Snapshot: snapshot,
			Listener: listener,
			Target: publicRouteTargetConfig{
				TargetType:         publicRouteTargetTypeStatic,
				StaticStatusCode:   http.StatusOK,
				StaticResponseBody: "unshaped-origin-body",
			},
		}
		recorder := httptest.NewRecorder()
		app.staticTargetResponse(recorder, req, resolution, nil, &initial, proxyRequestObservability{})
		if recorder.Code != http.StatusServiceUnavailable || recorder.Body.String() != publicPolicyMatchFailureBody {
			t.Fatalf("static fallback response = %d %q, want local 503 %q", recorder.Code, recorder.Body.String(), publicPolicyMatchFailureBody)
		}
		if strings.Contains(recorder.Body.String(), "unshaped-origin-body") {
			t.Fatal("static body was forwarded after response policy evaluation failed")
		}
	})

	t.Run("proxy target", func(t *testing.T) {
		upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("unshaped-origin-body"))
		}))
		defer upstream.Close()
		origin, err := url.Parse(upstream.URL)
		if err != nil {
			t.Fatalf("parse upstream URL: %v", err)
		}

		req := newRequest()
		initial, selected := app.selectPublicTrafficShaperWithSnapshot(snapshot, listener.ID, req)
		if !selected || initial.MatchError != nil || initial.Rule.ID != webSocketRule.ID {
			t.Fatalf("initial shaper = selected %t, %+v; want WebSocket rule", selected, initial)
		}
		resolution := publicRouteResolution{
			Snapshot: snapshot,
			Listener: listener,
			Target: publicRouteTargetConfig{
				Name:       "origin",
				TargetType: publicRouteTargetTypeProxy,
				Transport:  publicRouteTargetTransportDirect,
				ParsedURL:  origin,
			},
		}
		recorder := httptest.NewRecorder()
		app.proxyDirectTargetRequest(recorder, req, resolution, nil, &initial, nil, proxyRequestObservability{})
		if recorder.Code != http.StatusServiceUnavailable || recorder.Body.String() != publicPolicyMatchFailureBody {
			t.Fatalf("proxy fallback response = %d %q, want local 503 %q", recorder.Code, recorder.Body.String(), publicPolicyMatchFailureBody)
		}
		if strings.Contains(recorder.Body.String(), "unshaped-origin-body") {
			t.Fatal("upstream body was forwarded after response policy evaluation failed")
		}
	})
}

func repeatedPolicyMatchValues(value string, count int) []string {
	values := make([]string, count)
	for i := range values {
		values[i] = value
	}
	return values
}

func policyMatchCostValues() []string {
	values := repeatedPolicyMatchValues("a", publicPolicyMatchCostTriggerValues)
	values[len(values)-1] = "match"
	return values
}

func setPolicyMatchControlValues(r *http.Request, matching bool) {
	values := []string{"a", "a", "a"}
	if matching {
		values[len(values)-1] = "match"
	}
	if _, ok := r.Header["X"]; ok {
		r.Header["X"] = values
		return
	}
	query := make(url.Values)
	query["q"] = values
	r.URL.RawQuery = query.Encode()
}

func assertPolicyMatchRequestWithinDefaultHeaderBudget(t *testing.T, r *http.Request) {
	t.Helper()
	var serialized bytes.Buffer
	if err := r.Write(&serialized); err != nil {
		t.Fatalf("serialize request: %v", err)
	}
	if serialized.Len() >= defaultPublicMaxHeaderBytes {
		t.Fatalf("serialized request size = %d, must remain below default public limit %d", serialized.Len(), defaultPublicMaxHeaderBytes)
	}
}
