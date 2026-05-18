package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/proto"

	p2pstreamv1 "p2pstream/gen/proto/p2pstream/v1"
	"p2pstream/gen/proto/p2pstream/v1/p2pstreamv1connect"
	"p2pstream/internal/config"
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

func TestPublicPolicyMatchEmptyJSON(t *testing.T) {
	var empty publicPolicyMatchConfig
	if !empty.matches(publicListenerConfig{}, httptest.NewRequest(http.MethodGet, "http://example.test/", nil)) {
		t.Fatal("empty policy match should match every request")
	}

	decoded, err := decodePublicPolicyMatchJSON("{}")
	if err != nil {
		t.Fatalf("decode empty match JSON: %v", err)
	}
	if !decoded.matches(publicListenerConfig{}, httptest.NewRequest(http.MethodGet, "http://example.test/", nil)) {
		t.Fatal("decoded empty policy match should match every request")
	}
}

func TestPublicPolicyMatchBuilderOnlyRule(t *testing.T) {
	config, err := validatePublicPolicyMatch(&p2pstreamv1.PublicPolicyMatchRule{
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
		t.Fatalf("validate builder-only rule: %v", err)
	}
	if config.CELExpression == "" || config.Builder == nil {
		t.Fatalf("builder-only rule was not normalized: %#v", config)
	}
	if !config.matches(publicListenerConfig{}, httptest.NewRequest(http.MethodGet, "http://example.test/", nil)) {
		t.Fatal("builder-only rule did not match GET")
	}
	if config.matches(publicListenerConfig{}, httptest.NewRequest(http.MethodPost, "http://example.test/", nil)) {
		t.Fatal("builder-only rule matched POST")
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
		if _, err := validatePublicPolicyMatch(&p2pstreamv1.PublicPolicyMatchRule{CelExpression: expr}); err == nil {
			t.Fatalf("expected invalid expression %q to be rejected", expr)
		}
	}
}

func TestPublicPolicyMatchValidationRejectsInvalidBuilder(t *testing.T) {
	_, err := validatePublicPolicyMatch(&p2pstreamv1.PublicPolicyMatchRule{
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

func TestPublicPolicyMatchValidationRejectsInvalidProtoBuilderOperator(t *testing.T) {
	_, err := validatePublicPolicyMatch(&p2pstreamv1.PublicPolicyMatchRule{
		Builder: &p2pstreamv1.PublicPolicyMatchBuilder{
			Root: &p2pstreamv1.PublicPolicyMatchGroup{
				Conditions: []*p2pstreamv1.PublicPolicyMatchCondition{{
					Field:    p2pstreamv1.PublicPolicyMatchField_PUBLIC_POLICY_MATCH_FIELD_METHOD,
					Operator: p2pstreamv1.PublicPolicyMatchConditionOperator(999),
					Values:   []string{http.MethodGet},
				}},
			},
		},
	})
	if connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("expected invalid proto builder operator to be rejected, got %v", err)
	}
}

func TestPublicPolicyMatchValidationRejectsInvalidStoredBuilderOperator(t *testing.T) {
	_, err := decodePublicPolicyMatchJSON(`{
		"builder": {
			"root": {
				"conditions": [
					{"field": "method", "operator": "bogus", "values": ["GET"]}
				]
			}
		}
	}`)
	if connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("expected invalid stored builder operator to be rejected, got %v", err)
	}
}

func TestPublicPolicyMatchValidationRejectsMismatchedStoredBuilderAndCEL(t *testing.T) {
	_, err := decodePublicPolicyMatchJSON(`{
		"cel_expression": "method == \"POST\"",
		"builder": {
			"root": {
				"conditions": [
					{"field": "method", "operator": "equals", "values": ["GET"]}
				]
			}
		}
	}`)
	if err == nil {
		t.Fatal("expected mismatched stored builder and CEL to be rejected")
	}
}

func TestPublicPolicyMatchValidationAcceptsEquivalentBuilderAndCEL(t *testing.T) {
	config, err := validatePublicPolicyMatch(&p2pstreamv1.PublicPolicyMatchRule{
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
	_, err := validatePublicPolicyMatch(&p2pstreamv1.PublicPolicyMatchRule{
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

func TestRemovedLegacyMatchUnknownFieldsRejected(t *testing.T) {
	tests := []struct {
		name   string
		msg    proto.Message
		number protowire.Number
	}{
		{name: "create rate limit", msg: &p2pstreamv1.CreatePublicRateLimitRuleRequest{}, number: 8},
		{name: "update rate limit", msg: &p2pstreamv1.UpdatePublicRateLimitRuleRequest{}, number: 9},
		{name: "create shaper", msg: &p2pstreamv1.CreatePublicTrafficShaperRuleRequest{}, number: 10},
		{name: "update shaper", msg: &p2pstreamv1.UpdatePublicTrafficShaperRuleRequest{}, number: 11},
		{name: "create waf", msg: &p2pstreamv1.CreatePublicWafRuleRequest{}, number: 6},
		{name: "update waf", msg: &p2pstreamv1.UpdatePublicWafRuleRequest{}, number: 7},
		{name: "create cache", msg: &p2pstreamv1.CreatePublicCacheRuleRequest{}, number: 4},
		{name: "update cache", msg: &p2pstreamv1.UpdatePublicCacheRuleRequest{}, number: 5},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			raw := protowire.AppendTag(nil, tt.number, protowire.BytesType)
			raw = protowire.AppendBytes(raw, nil)
			tt.msg.ProtoReflect().SetUnknown(raw)
			if err := rejectRemovedPolicyMatchField(tt.msg, tt.number); err == nil {
				t.Fatal("expected removed match field to be rejected")
			}
		})
	}
}

func TestManagementJSONRejectsRemovedLegacyMatchField(t *testing.T) {
	app := &App{Config: &config.Config{ManagementUIDisabled: true}}
	mux := http.NewServeMux()
	app.RegisterManagementRoutes(mux)

	req := httptest.NewRequest(
		http.MethodPost,
		p2pstreamv1connect.AgentManagementServiceCreatePublicRateLimitRuleProcedure,
		strings.NewReader(`{"name":"legacy","limit":1,"windowMillis":"1000","match":{"methods":["GET"]}}`),
	)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Connect-Protocol-Version", "1")
	rec := httptest.NewRecorder()

	mux.ServeHTTP(rec, req)

	body := strings.ToLower(rec.Body.String())
	if rec.Code < http.StatusBadRequest || !strings.Contains(body, "unknown field") || !strings.Contains(body, "match") {
		t.Fatalf("legacy JSON match field response = status %d body %s", rec.Code, rec.Body.String())
	}
}

func TestPublicPolicyMatchLegacyFirstValueStoredBuilder(t *testing.T) {
	match, err := decodePublicPolicyMatchJSON(`{
		"builder": {
			"root": {
				"conditions": [
					{"field": "header", "name": "x-plan", "operator": "equals", "values": ["free"], "legacy_first_value": true},
					{"field": "query_param", "name": "plan", "operator": "equals", "values": ["free"], "legacy_first_value": true}
				]
			}
		}
	}`)
	if err != nil {
		t.Fatalf("decode legacy first-value builder: %v", err)
	}
	for _, want := range []string{`headers["x-plan"][0] == "free"`, `query["plan"][0] == "free"`} {
		if !strings.Contains(match.CELExpression, want) {
			t.Fatalf("legacy first-value expression %q missing %q", match.CELExpression, want)
		}
	}

	listener := publicListenerConfig{Protocol: publicListenerProtocolHTTP}
	previouslySkipped := httptest.NewRequest(http.MethodGet, "http://example.test/?plan=paid&plan=free", nil)
	previouslySkipped.Header.Add("X-Plan", "paid")
	previouslySkipped.Header.Add("X-Plan", "free")
	if match.matches(listener, previouslySkipped) {
		t.Fatal("legacy first-value match used a later duplicate value")
	}

	previouslyMatched := httptest.NewRequest(http.MethodGet, "http://example.test/?plan=free&plan=paid", nil)
	previouslyMatched.Header.Add("X-Plan", "free")
	previouslyMatched.Header.Add("X-Plan", "paid")
	if !match.matches(listener, previouslyMatched) {
		t.Fatal("legacy first-value match did not use the first duplicate value")
	}

	protoRule := publicPolicyMatchRuleToProto(match)
	if protoRule == nil || protoRule.Builder != nil {
		t.Fatalf("legacy first-value builder should be returned as CEL-only, got %#v", protoRule)
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

func mustPublicPolicyMatchCEL(t *testing.T, expr string) publicPolicyMatchConfig {
	t.Helper()
	match := publicPolicyMatchConfig{CELExpression: strings.TrimSpace(expr)}
	if err := compilePublicPolicyMatch(&match); err != nil {
		t.Fatalf("compile policy match %q: %v", expr, err)
	}
	return match
}
