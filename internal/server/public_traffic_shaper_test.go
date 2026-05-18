package server

import (
	"context"
	"io"
	"math"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"

	p2pstreamv1 "p2pstream/gen/proto/p2pstream/v1"
	"p2pstream/internal/db"
)

func TestPublicTrafficShaperSelectsFirstMatchingRule(t *testing.T) {
	shaper := newPublicTrafficShaper()
	listener := publicListenerConfig{ID: 1, Protocol: publicListenerProtocolHTTP}
	req := testRateLimitRequest("GET", "http://example.com/assets/app.js", "198.51.100.10:1234")

	later := testTrafficShaperRule(1, "later", 100, publicTrafficShaperBudgetScopePerKey, 0, 1024)
	first := testTrafficShaperRule(2, "first", 10, publicTrafficShaperBudgetScopePerKey, 0, 512)
	decision, ok := shaper.evaluate([]publicTrafficShaperRuleConfig{later, first}, listener, req, time.Unix(1, 0))
	if !ok {
		t.Fatal("expected matching shaper")
	}
	if decision.Rule.ID != first.ID {
		t.Fatalf("selected rule id = %d, want %d", decision.Rule.ID, first.ID)
	}
}

func TestPublicTrafficShaperSkipsNonMatchingRules(t *testing.T) {
	shaper := newPublicTrafficShaper()
	listener := publicListenerConfig{ID: 1, Protocol: publicListenerProtocolHTTP}
	rule := testTrafficShaperRule(1, "post-only", 100, publicTrafficShaperBudgetScopePerKey, 0, 1024)
	rule.Match = mustPublicPolicyMatchCEL(t, `method == "POST"`)
	rule.Fingerprint = publicTrafficShaperRuleFingerprint(rule)

	req := testRateLimitRequest("GET", "http://example.com/assets/app.js", "198.51.100.10:1234")
	if _, ok := shaper.evaluate([]publicTrafficShaperRuleConfig{rule}, listener, req, time.Unix(1, 0)); ok {
		t.Fatal("non-matching request selected a shaper")
	}
}

func TestPublicTrafficShaperPathPrefixUsesSegmentBoundaries(t *testing.T) {
	shaper := newPublicTrafficShaper()
	listener := publicListenerConfig{ID: 1, Protocol: publicListenerProtocolHTTP}
	rule := testTrafficShaperRule(1, "api", 100, publicTrafficShaperBudgetScopePerKey, 0, 1024)
	rule.Match = mustPublicPolicyMatchCEL(t, `path_prefix(path, "/api")`)
	rule.Fingerprint = publicTrafficShaperRuleFingerprint(rule)

	matching := testRateLimitRequest("GET", "http://example.com/api/data", "198.51.100.10:1234")
	if _, ok := shaper.evaluate([]publicTrafficShaperRuleConfig{rule}, listener, matching, time.Unix(1, 0)); !ok {
		t.Fatal("matching /api path did not select shaper")
	}
	confusing := testRateLimitRequest("GET", "http://example.com/apiv2/data", "198.51.100.10:1234")
	if _, ok := shaper.evaluate([]publicTrafficShaperRuleConfig{rule}, listener, confusing, time.Unix(2, 0)); ok {
		t.Fatal("path prefix /api matched /apiv2")
	}
}

func TestPublicTrafficShaperPerKeyAndPerRequestBuckets(t *testing.T) {
	shaper := newPublicTrafficShaper()
	listener := publicListenerConfig{ID: 1, Protocol: publicListenerProtocolHTTP}
	reqA := testRateLimitRequest("GET", "http://example.com/download", "198.51.100.10:1234")
	reqB := testRateLimitRequest("GET", "http://example.com/download", "198.51.100.10:5678")

	perKey := testTrafficShaperRule(1, "per-key", 100, publicTrafficShaperBudgetScopePerKey, 0, 1024)
	first, ok := shaper.evaluate([]publicTrafficShaperRuleConfig{perKey}, listener, reqA, time.Unix(1, 0))
	if !ok {
		t.Fatal("expected first per-key decision")
	}
	second, ok := shaper.evaluate([]publicTrafficShaperRuleConfig{perKey}, listener, reqB, time.Unix(2, 0))
	if !ok {
		t.Fatal("expected second per-key decision")
	}
	if first.DownloadBucket == nil || first.DownloadBucket != second.DownloadBucket {
		t.Fatal("per-key requests from same remote IP should share download bucket")
	}

	perRequest := testTrafficShaperRule(2, "per-request", 100, publicTrafficShaperBudgetScopePerRequest, 0, 1024)
	third, ok := shaper.evaluate([]publicTrafficShaperRuleConfig{perRequest}, listener, reqA, time.Unix(3, 0))
	if !ok {
		t.Fatal("expected first per-request decision")
	}
	fourth, ok := shaper.evaluate([]publicTrafficShaperRuleConfig{perRequest}, listener, reqA, time.Unix(4, 0))
	if !ok {
		t.Fatal("expected second per-request decision")
	}
	if third.DownloadBucket == nil || fourth.DownloadBucket == nil || third.DownloadBucket == fourth.DownloadBucket {
		t.Fatal("per-request decisions should use independent buckets")
	}
}

func TestPublicTrafficShaperDirectionBuckets(t *testing.T) {
	shaper := newPublicTrafficShaper()
	listener := publicListenerConfig{ID: 1, Protocol: publicListenerProtocolHTTP}
	req := testRateLimitRequest("POST", "http://example.com/upload", "198.51.100.10:1234")

	uploadOnly := testTrafficShaperRule(1, "upload", 100, publicTrafficShaperBudgetScopePerKey, 2048, 0)
	decision, ok := shaper.evaluate([]publicTrafficShaperRuleConfig{uploadOnly}, listener, req, time.Unix(1, 0))
	if !ok {
		t.Fatal("expected upload-only decision")
	}
	if decision.UploadBucket == nil {
		t.Fatal("upload bucket is nil")
	}
	if decision.DownloadBucket != nil {
		t.Fatal("download bucket should be nil for upload-only rule")
	}
}

func TestByteTokenBucketWaitMathIsDeterministic(t *testing.T) {
	now := time.Unix(10, 0)
	var slept time.Duration
	bucket := newByteTokenBucket(100, 100, now)
	bucket.now = func() time.Time { return now }
	bucket.sleep = func(_ context.Context, d time.Duration) error {
		slept += d
		now = now.Add(d)
		return nil
	}

	if err := bucket.wait(context.Background(), 50); err != nil {
		t.Fatalf("wait 50: %v", err)
	}
	if slept != 0 {
		t.Fatalf("first wait slept %s, want 0", slept)
	}
	if err := bucket.wait(context.Background(), 100); err != nil {
		t.Fatalf("wait 100: %v", err)
	}
	if slept != 500*time.Millisecond {
		t.Fatalf("slept %s, want 500ms", slept)
	}
}

func TestShapingReadCloserExemptsBytesWithoutDebit(t *testing.T) {
	bucket := newByteTokenBucket(10, 10, time.Unix(1, 0))
	reader := newShapingReadCloser(context.Background(), io.NopCloser(strings.NewReader("abcdefghij")), bucket, 5)
	body, err := io.ReadAll(reader)
	if err != nil {
		t.Fatalf("read shaped body: %v", err)
	}
	if string(body) != "abcdefghij" {
		t.Fatalf("body = %q", body)
	}

	bucket.mu.Lock()
	tokens := bucket.tokens
	bucket.mu.Unlock()
	if math.Abs(tokens-5) > 0.001 {
		t.Fatalf("tokens = %.3f, want 5 after exempting half the body", tokens)
	}
}

func TestPublicTrafficShaperValidationAndDBRoundTrip(t *testing.T) {
	if _, err := validatePublicTrafficShaperRuleInput(
		"invalid",
		100,
		true,
		p2pstreamv1.PublicTrafficShaperBudgetScope_PUBLIC_TRAFFIC_SHAPER_BUDGET_SCOPE_PER_KEY,
		0,
		0,
		0,
		0,
		0,
		nil,
		nil,
	); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("expected both-directions-unlimited validation error, got %v", err)
	}

	params, err := validatePublicTrafficShaperRuleInput(
		"per-request",
		100,
		true,
		p2pstreamv1.PublicTrafficShaperBudgetScope_PUBLIC_TRAFFIC_SHAPER_BUDGET_SCOPE_PER_REQUEST,
		1024,
		0,
		0,
		128,
		256,
		[]*p2pstreamv1.PublicRateLimitKeyPart{{Source: p2pstreamv1.PublicRateLimitKeySource_PUBLIC_RATE_LIMIT_KEY_SOURCE_HEADER, Name: "X-User"}},
		nil,
	)
	if err != nil {
		t.Fatalf("validate per-request shaper: %v", err)
	}
	if params.KeyPartsJSON != "[]" {
		t.Fatalf("per-request key parts json = %q, want []", params.KeyPartsJSON)
	}

	params, err = validatePublicTrafficShaperRuleInput(
		"round-trip",
		50,
		true,
		p2pstreamv1.PublicTrafficShaperBudgetScope_PUBLIC_TRAFFIC_SHAPER_BUDGET_SCOPE_PER_KEY,
		1024,
		2048,
		4096,
		128,
		256,
		nil,
		nil,
	)
	if err != nil {
		t.Fatalf("validate per-key shaper: %v", err)
	}
	database := newAgentRegistryTestDB(t)
	row, err := database.CreatePublicTrafficShaperRule(context.Background(), db.CreatePublicTrafficShaperRuleParams{
		Name:                   params.Name,
		Priority:               params.Priority,
		Enabled:                params.Enabled,
		BudgetScope:            params.BudgetScope,
		UploadBytesPerSecond:   params.UploadBytesPerSecond,
		DownloadBytesPerSecond: params.DownloadBytesPerSecond,
		BurstBytes:             params.BurstBytes,
		RequestExemptBytes:     params.RequestExemptBytes,
		ResponseExemptBytes:    params.ResponseExemptBytes,
		MatchJson:              params.MatchJSON,
		KeyPartsJson:           params.KeyPartsJSON,
	})
	if err != nil {
		t.Fatalf("create shaper row: %v", err)
	}
	rule, err := publicTrafficShaperRuleRowToConfig(row)
	if err != nil {
		t.Fatalf("row to config: %v", err)
	}
	if rule.BudgetScope != publicTrafficShaperBudgetScopePerKey {
		t.Fatalf("scope = %q", rule.BudgetScope)
	}
	if len(rule.KeyParts) != 1 || rule.KeyParts[0].Source != publicRateLimitKeySourceRemoteIP {
		t.Fatalf("default key parts = %+v, want remote IP", rule.KeyParts)
	}
	listed, err := database.ListPublicTrafficShaperRules(context.Background())
	if err != nil {
		t.Fatalf("list shaper rows: %v", err)
	}
	if len(listed) != 1 || listed[0].Name != "round-trip" {
		t.Fatalf("listed rows = %+v", listed)
	}
}

func TestPublicTrafficShaperPrunesIdlePerKeyBuckets(t *testing.T) {
	shaper := newPublicTrafficShaper()
	listener := publicListenerConfig{ID: 1, Protocol: publicListenerProtocolHTTP}
	req := testRateLimitRequest("GET", "http://example.com/download", "198.51.100.10:1234")
	rule := testTrafficShaperRule(1, "per-key", 100, publicTrafficShaperBudgetScopePerKey, 0, 1024)

	if _, ok := shaper.evaluate([]publicTrafficShaperRuleConfig{rule}, listener, req, time.Unix(1, 0)); !ok {
		t.Fatal("expected initial shaper decision")
	}
	shaper.mu.Lock()
	runtime := shaper.rules[rule.ID]
	if runtime == nil || len(runtime.downloadBuckets) != 1 {
		t.Fatalf("download buckets after initial decision = %+v", runtime)
	}
	shaper.mu.Unlock()

	if _, ok := shaper.evaluate([]publicTrafficShaperRuleConfig{rule}, listener, req, time.Unix(1, 0).Add(trafficShaperIdleStateTTL+trafficShaperPruneInterval+time.Second)); !ok {
		t.Fatal("expected shaper decision after prune")
	}
	shaper.mu.Lock()
	defer shaper.mu.Unlock()
	runtime = shaper.rules[rule.ID]
	if runtime == nil || len(runtime.downloadBuckets) != 1 {
		t.Fatalf("expected old bucket pruned and new bucket created, got %+v", runtime)
	}
}

func testTrafficShaperRule(id int64, name string, priority int64, scope string, uploadBPS int64, downloadBPS int64) publicTrafficShaperRuleConfig {
	rule := publicTrafficShaperRuleConfig{
		ID:                     id,
		Name:                   name,
		Priority:               priority,
		Enabled:                true,
		BudgetScope:            scope,
		UploadBytesPerSecond:   uploadBPS,
		DownloadBytesPerSecond: downloadBPS,
		KeyParts:               []publicRateLimitKeyPartConfig{{Source: publicRateLimitKeySourceRemoteIP}},
		UpdatedAt:              time.Unix(1, 0),
	}
	rule.Fingerprint = publicTrafficShaperRuleFingerprint(rule)
	return rule
}
