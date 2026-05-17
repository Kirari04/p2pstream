package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	p2pstreamv1 "p2pstream/gen/proto/p2pstream/v1"
)

func TestPublicPolicyMatchCELRequestFields(t *testing.T) {
	match := mustPublicPolicyMatchCEL(t, `method == "POST" &&
		protocol == "https" &&
		host_match(host, "*.example.com") &&
		path_prefix(path, "/admin") &&
		path.matches("^/admin/[a-z]+$") &&
		cidr(remote_ip, "198.51.100.0/24") &&
		headers["x-plan"].exists(v, v in ["free", "pro"]) &&
		cookies["session"].startsWith("abc") &&
		query["role"].exists(v, v == "ops") &&
		!(query["debug"].exists(v, v == "0"))`)
	listener := publicListenerConfig{Protocol: publicListenerProtocolHTTPS}
	req := httptest.NewRequest(http.MethodPost, "https://api.example.com/admin/users?role=admin&role=ops&debug=1", nil)
	req.RemoteAddr = "198.51.100.23:4567"
	req.Header.Add("X-Plan", "trial")
	req.Header.Add("X-Plan", "free")
	req.AddCookie(&http.Cookie{Name: "session", Value: "abc-123"})

	if !match.matches(listener, req) {
		t.Fatal("expected request-only CEL expression to match")
	}

	req.RemoteAddr = "203.0.113.23:4567"
	if match.matches(listener, req) {
		t.Fatal("CIDR helper matched remote IP outside the range")
	}
}

func TestPublicPolicyMatchEmptyAndLegacyJSON(t *testing.T) {
	var empty publicRateLimitMatchConfig
	if !empty.matches(publicListenerConfig{}, httptest.NewRequest(http.MethodGet, "http://example.test/", nil)) {
		t.Fatal("empty policy match should match every request")
	}

	legacy := publicRateLimitMatchConfig{
		Methods:      []string{http.MethodGet},
		HostPatterns: []string{"assets.example.test"},
		PathPrefixes: []string{"/assets"},
		Headers: []publicRateLimitValueMatcherConfig{{
			Name:     "X-Asset-Class",
			Operator: publicRateLimitMatchOperatorEquals,
			Value:    "public",
		}},
	}
	raw, err := json.Marshal(legacy)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := decodePublicPolicyMatchJSON(string(raw))
	if err != nil {
		t.Fatalf("decode legacy match JSON: %v", err)
	}
	if decoded.CELExpression == "" || decoded.Builder == nil {
		t.Fatal("legacy match JSON was not upgraded to a CEL rule shape")
	}

	req := httptest.NewRequest(http.MethodGet, "https://assets.example.test/assets/app.css", nil)
	req.Header.Set("X-Asset-Class", "public")
	if !decoded.matches(publicListenerConfig{Protocol: publicListenerProtocolHTTPS}, req) {
		t.Fatal("decoded legacy match did not preserve existing behavior")
	}
	confusing := httptest.NewRequest(http.MethodGet, "https://assets.example.test/assets-v2/app.css", nil)
	confusing.Header.Set("X-Asset-Class", "public")
	if decoded.matches(publicListenerConfig{Protocol: publicListenerProtocolHTTPS}, confusing) {
		t.Fatal("decoded legacy path prefix matched a sibling path segment")
	}
}

func TestPublicPolicyMatchRuleWinsOverLegacyMatch(t *testing.T) {
	legacy := &p2pstreamv1.PublicRateLimitMatch{Methods: []string{http.MethodPost}}
	rule := &p2pstreamv1.PublicPolicyMatchRule{CelExpression: `method == "GET"`}
	params, err := validatePublicRateLimitRuleInput(
		"new-match",
		10,
		true,
		p2pstreamv1.PublicRateLimitAlgorithm_PUBLIC_RATE_LIMIT_ALGORITHM_FIXED_WINDOW,
		1,
		1000,
		0,
		legacy,
		nil,
		http.StatusTooManyRequests,
		"",
		p2pstreamv1.PublicResponseBodyMode_PUBLIC_RESPONSE_BODY_MODE_INLINE,
		0,
		"",
		nil,
		rule,
	)
	if err != nil {
		t.Fatalf("validate rate limit rule: %v", err)
	}
	decoded, err := decodePublicPolicyMatchJSON(params.MatchJSON)
	if err != nil {
		t.Fatalf("decode match JSON: %v", err)
	}
	if len(decoded.Methods) != 0 {
		t.Fatalf("legacy match methods survived match_rule precedence: %v", decoded.Methods)
	}
	if !decoded.matches(publicListenerConfig{}, httptest.NewRequest(http.MethodGet, "http://example.test/", nil)) {
		t.Fatal("new match_rule did not match GET")
	}
	if decoded.matches(publicListenerConfig{}, httptest.NewRequest(http.MethodPost, "http://example.test/", nil)) {
		t.Fatal("legacy match won over match_rule")
	}
}

func TestPublicPolicyMatchValidationRejectsInvalidCEL(t *testing.T) {
	for _, expr := range []string{
		`unknown_request_field == true`,
		`method`,
		`path.matches("[")`,
		`cidr(remote_ip, "not-a-cidr")`,
		`path_prefix(path, "api")`,
	} {
		if _, err := validatePublicPolicyMatch(nil, &p2pstreamv1.PublicPolicyMatchRule{CelExpression: expr}); err == nil {
			t.Fatalf("expected invalid expression %q to be rejected", expr)
		}
	}
}

func TestPublicPolicyMatchValidationRejectsInvalidBuilder(t *testing.T) {
	_, err := validatePublicPolicyMatch(nil, &p2pstreamv1.PublicPolicyMatchRule{
		Builder: &p2pstreamv1.PublicPolicyMatchBuilder{
			Root: &p2pstreamv1.PublicPolicyMatchGroup{
				Conditions: []*p2pstreamv1.PublicPolicyMatchCondition{{
					Field:    p2pstreamv1.PublicPolicyMatchField_PUBLIC_POLICY_MATCH_FIELD_REMOTE_IP,
					Operator: p2pstreamv1.PublicPolicyMatchConditionOperator_PUBLIC_POLICY_MATCH_CONDITION_OPERATOR_CIDR,
					Values:   []string{"bad-cidr"},
				}},
			},
		},
	})
	if err == nil {
		t.Fatal("expected invalid builder CIDR to be rejected")
	}
}

func TestPublicPolicyMatchValidationAcceptsEquivalentBuilderAndCEL(t *testing.T) {
	config, err := validatePublicPolicyMatch(nil, &p2pstreamv1.PublicPolicyMatchRule{
		CelExpression: `(((method == "GET")))`,
		Builder: &p2pstreamv1.PublicPolicyMatchBuilder{
			Root: &p2pstreamv1.PublicPolicyMatchGroup{
				Conditions: []*p2pstreamv1.PublicPolicyMatchCondition{{
					Field:    p2pstreamv1.PublicPolicyMatchField_PUBLIC_POLICY_MATCH_FIELD_METHOD,
					Operator: p2pstreamv1.PublicPolicyMatchConditionOperator_PUBLIC_POLICY_MATCH_CONDITION_OPERATOR_EQUALS,
					Values:   []string{http.MethodGet},
				}},
			},
		},
	})
	if err != nil {
		t.Fatalf("equivalent builder and CEL should be accepted: %v", err)
	}
	if config.Builder == nil || config.CELExpression != `(((method == "GET")))` {
		t.Fatalf("equivalent builder and CEL were not preserved: %#v", config)
	}
}

func TestPublicPolicyMatchValidationRejectsMismatchedBuilderAndCEL(t *testing.T) {
	_, err := validatePublicPolicyMatch(nil, &p2pstreamv1.PublicPolicyMatchRule{
		CelExpression: `method == "POST"`,
		Builder: &p2pstreamv1.PublicPolicyMatchBuilder{
			Root: &p2pstreamv1.PublicPolicyMatchGroup{
				Conditions: []*p2pstreamv1.PublicPolicyMatchCondition{{
					Field:    p2pstreamv1.PublicPolicyMatchField_PUBLIC_POLICY_MATCH_FIELD_METHOD,
					Operator: p2pstreamv1.PublicPolicyMatchConditionOperator_PUBLIC_POLICY_MATCH_CONDITION_OPERATOR_EQUALS,
					Values:   []string{http.MethodGet},
				}},
			},
		},
	})
	if err == nil {
		t.Fatal("expected mismatched builder and CEL to be rejected")
	}
}

func TestPublicPolicyMatchRuntimeErrorsFailClosed(t *testing.T) {
	match := mustPublicPolicyMatchCEL(t, `headers["missing"][0] == "x"`)
	if match.matches(publicListenerConfig{}, httptest.NewRequest(http.MethodGet, "http://example.test/", nil)) {
		t.Fatal("runtime CEL error matched instead of failing closed")
	}
}

func TestPublicPolicyMatchSharedEvaluatorAcrossPolicies(t *testing.T) {
	match := mustPublicPolicyMatchCEL(t, `path_prefix(path, "/assets") && query["v"].exists(v, v == "1")`)
	listener := publicListenerConfig{Protocol: publicListenerProtocolHTTP}
	req := httptest.NewRequest(http.MethodGet, "http://example.test/assets/app.js?v=1", nil)

	rateRule := testRateLimitRule(publicRateLimitAlgorithmFixedWindow, 10, 1000, 0)
	rateRule.Match = match
	if !rateRule.matches(listener, req) {
		t.Fatal("rate limit rule did not use shared policy evaluator")
	}

	shaperRule := testTrafficShaperRule(1, "assets", 10, publicTrafficShaperBudgetScopePerKey, 0, 1024)
	shaperRule.Match = match
	if !shaperRule.matches(listener, req) {
		t.Fatal("traffic shaper rule did not use shared policy evaluator")
	}

	wafRule := testWafRule(1, publicWafActionBlock)
	wafRule.Match = match
	if !wafRule.matches(listener, req) {
		t.Fatal("WAF rule did not use shared policy evaluator")
	}

	cacheRule := publicCacheRuleConfig{Enabled: true, Match: match}
	if !cacheRule.matches(listener, req, publicRouteResolution{}) {
		t.Fatal("cache rule did not use shared policy evaluator")
	}
}

func TestPublicCacheRouteAndBackendFiltersRemainOutsideCEL(t *testing.T) {
	rule := publicCacheRuleConfig{
		Enabled:    true,
		Match:      mustPublicPolicyMatchCEL(t, `path_prefix(path, "/assets")`),
		RouteIDs:   []int64{10},
		BackendIDs: []int64{20},
	}
	listener := publicListenerConfig{Protocol: publicListenerProtocolHTTP}
	req := httptest.NewRequest(http.MethodGet, "http://example.test/assets/app.js", nil)
	matching := publicRouteResolution{Route: publicRouteConfig{ID: 10}, Backend: publicBackendConfig{ID: 20}}
	if !rule.matches(listener, req, matching) {
		t.Fatal("cache rule should match when CEL and route/backend filters match")
	}
	if rule.matches(listener, req, publicRouteResolution{Route: publicRouteConfig{ID: 11}, Backend: publicBackendConfig{ID: 20}}) {
		t.Fatal("cache route filter was ignored")
	}
	if rule.matches(listener, req, publicRouteResolution{Route: publicRouteConfig{ID: 10}, Backend: publicBackendConfig{ID: 21}}) {
		t.Fatal("cache backend filter was ignored")
	}
}

func mustPublicPolicyMatchCEL(t *testing.T, expr string) publicRateLimitMatchConfig {
	t.Helper()
	match := publicRateLimitMatchConfig{CELExpression: strings.TrimSpace(expr)}
	if err := compilePublicPolicyMatch(&match); err != nil {
		t.Fatalf("compile policy match %q: %v", expr, err)
	}
	return match
}
