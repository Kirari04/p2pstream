package server

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"io"
	"math"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"strconv"
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
	rules := []publicTrafficShaperRuleConfig{later, first}
	sortPublicTrafficShaperRules(rules)
	decision, ok := shaper.evaluate(rules, listener, req, time.Unix(1, 0))
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

func TestPublicTrafficShaperProtocolScopes(t *testing.T) {
	shaper := newPublicTrafficShaper()
	listener := publicListenerConfig{ID: 1, Protocol: publicListenerProtocolHTTP}
	normalRequest := testRateLimitRequest("GET", "http://example.com/chat", "198.51.100.10:1234")
	webSocketRequest := testRateLimitRequest("GET", "http://example.com/chat", "198.51.100.10:1234")
	webSocketRequest.Header.Set("Connection", "keep-alive, Upgrade")
	webSocketRequest.Header.Set("Upgrade", "websocket")
	webSocketRequest.Header.Set("Sec-WebSocket-Version", "13")
	webSocketRequest.Header.Set("Sec-WebSocket-Key", "dGhlIHNhbXBsZSBub25jZQ==")
	spoofedUpgradeRequest := testRateLimitRequest("GET", "http://example.com/chat", "198.51.100.10:1234")
	spoofedUpgradeRequest.Header.Set("Connection", "Upgrade")
	spoofedUpgradeRequest.Header.Set("Upgrade", "websocket")

	tests := []struct {
		name          string
		protocolScope string
		request       *http.Request
		wantSelected  bool
	}{
		{name: "all selects HTTP", protocolScope: publicTrafficShaperProtocolScopeAll, request: normalRequest, wantSelected: true},
		{name: "all selects WebSocket", protocolScope: publicTrafficShaperProtocolScopeAll, request: webSocketRequest, wantSelected: true},
		{name: "WebSocket only skips HTTP", protocolScope: publicTrafficShaperProtocolScopeWebSocketOnly, request: normalRequest, wantSelected: false},
		{name: "WebSocket only selects WebSocket", protocolScope: publicTrafficShaperProtocolScopeWebSocketOnly, request: webSocketRequest, wantSelected: true},
		{name: "WebSocket excluded selects HTTP", protocolScope: publicTrafficShaperProtocolScopeWebSocketExcluded, request: normalRequest, wantSelected: true},
		{name: "WebSocket excluded skips WebSocket", protocolScope: publicTrafficShaperProtocolScopeWebSocketExcluded, request: webSocketRequest, wantSelected: false},
		{name: "incomplete attacker headers remain HTTP", protocolScope: publicTrafficShaperProtocolScopeWebSocketExcluded, request: spoofedUpgradeRequest, wantSelected: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rule := testTrafficShaperRule(1, "protocol", 100, publicTrafficShaperBudgetScopePerRequest, 1024, 1024)
			rule.ProtocolScope = tt.protocolScope
			rule.Fingerprint = publicTrafficShaperRuleFingerprint(rule)
			_, selected := shaper.evaluate([]publicTrafficShaperRuleConfig{rule}, listener, tt.request, time.Unix(1, 0))
			if selected != tt.wantSelected {
				t.Fatalf("selected = %t, want %t", selected, tt.wantSelected)
			}
		})
	}
}

func TestPublicTrafficShaperReclassifiesRejectedWebSocketHandshake(t *testing.T) {
	shaper := newPublicTrafficShaper()
	app := &App{TrafficShaper: shaper}
	listener := publicListenerConfig{ID: 1, Protocol: publicListenerProtocolHTTP}
	webSocketRule := testTrafficShaperRule(1, "websocket-only", 10, publicTrafficShaperBudgetScopePerRequest, 0, 2048)
	webSocketRule.ProtocolScope = publicTrafficShaperProtocolScopeWebSocketOnly
	webSocketRule.Fingerprint = publicTrafficShaperRuleFingerprint(webSocketRule)
	httpRule := testTrafficShaperRule(2, "http-only", 20, publicTrafficShaperBudgetScopePerRequest, 0, 1024)
	httpRule.ProtocolScope = publicTrafficShaperProtocolScopeWebSocketExcluded
	httpRule.Fingerprint = publicTrafficShaperRuleFingerprint(httpRule)
	rules := []publicTrafficShaperRuleConfig{webSocketRule, httpRule}
	sortPublicTrafficShaperRules(rules)
	snapshot := &publicProxySnapshot{
		Listeners:          map[int64]publicListenerConfig{listener.ID: listener},
		TrafficShaperRules: rules,
	}
	request := testRateLimitRequest("GET", "http://example.com/chat", "198.51.100.10:1234")
	request.Header.Set("Connection", "Upgrade")
	request.Header.Set("Upgrade", "websocket")
	request.Header.Set("Sec-WebSocket-Version", "13")
	request.Header.Set("Sec-WebSocket-Key", "dGhlIHNhbXBsZSBub25jZQ==")

	requestDecision, ok := app.selectPublicTrafficShaperWithSnapshot(snapshot, listener.ID, request)
	if !ok || requestDecision.Rule.ID != webSocketRule.ID {
		t.Fatalf("WebSocket handshake selected rule %+v, want %q", requestDecision.Rule, webSocketRule.Name)
	}
	selected := app.publicTrafficShaperForResponse(snapshot, listener.ID, request, http.StatusSwitchingProtocols, &requestDecision)
	if selected == nil || selected.Rule.ID != webSocketRule.ID {
		t.Fatalf("successful WebSocket upgrade selected rule %+v, want %q", selected, webSocketRule.Name)
	}
	selected = app.publicTrafficShaperForResponse(snapshot, listener.ID, request, http.StatusOK, &requestDecision)
	if selected == nil || selected.Rule.ID != httpRule.ID {
		t.Fatalf("rejected WebSocket handshake selected rule %+v, want %q", selected, httpRule.Name)
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

func TestTrafficShaperUpgradeBodyPreservesReadWriteCloser(t *testing.T) {
	body := &testShapingReadWriteCloser{reader: strings.NewReader("download")}
	decision := publicTrafficShaperDecision{
		Rule:           publicTrafficShaperRuleConfig{RequestExemptBytes: 1, ResponseExemptBytes: 2},
		UploadBucket:   newByteTokenBucket(32, 32, time.Unix(1, 0)),
		DownloadBucket: newByteTokenBucket(32, 32, time.Unix(1, 0)),
	}
	wrapped := decision.wrapUpgradeBody(context.Background(), body)
	readWriteBody, ok := wrapped.(io.ReadWriteCloser)
	if !ok {
		t.Fatalf("upgrade body type %T does not implement io.ReadWriteCloser", wrapped)
	}
	download, err := io.ReadAll(readWriteBody)
	if err != nil {
		t.Fatalf("read upgraded body: %v", err)
	}
	if string(download) != "download" {
		t.Fatalf("download = %q", download)
	}
	if _, err := readWriteBody.Write([]byte("upload")); err != nil {
		t.Fatalf("write upgraded body: %v", err)
	}
	if got := body.writes.String(); got != "upload" {
		t.Fatalf("upload = %q", got)
	}
	if err := readWriteBody.Close(); err != nil {
		t.Fatalf("close upgraded body: %v", err)
	}
	if !body.closed {
		t.Fatal("underlying upgraded body was not closed")
	}
}

func TestTrafficShaperUpgradeWriteWaitsBeforeForwarding(t *testing.T) {
	now := time.Unix(1, 0)
	var slept time.Duration
	bucket := newByteTokenBucket(4, 4, now)
	bucket.tokens = 0
	bucket.now = func() time.Time { return now }
	bucket.sleep = func(_ context.Context, d time.Duration) error {
		slept += d
		now = now.Add(d)
		return nil
	}
	body := &testShapingReadWriteCloser{
		reader: strings.NewReader(""),
		beforeWrite: func() {
			if slept != time.Second {
				t.Fatalf("underlying write ran after %s of pacing, want 1s", slept)
			}
		},
	}
	wrapper := &shapingReadWriteCloser{ctx: context.Background(), body: body, writeBucket: bucket}
	if n, err := wrapper.Write([]byte("ping")); err != nil || n != 4 {
		t.Fatalf("write = %d, %v, want 4, nil", n, err)
	}
}

func TestTrafficShaperUpgradeWriteRefundsPartialReservation(t *testing.T) {
	bucket := newByteTokenBucket(4, 4, time.Unix(1, 0))
	body := &testShapingReadWriteCloser{
		reader:   strings.NewReader(""),
		maxWrite: 2,
		writeErr: io.ErrUnexpectedEOF,
	}
	wrapper := &shapingReadWriteCloser{
		ctx:                  context.Background(),
		body:                 body,
		writeBucket:          bucket,
		writeExemptRemaining: 1,
	}
	n, err := wrapper.Write([]byte("ping"))
	if n != 2 || !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("write = %d, %v, want 2, unexpected EOF", n, err)
	}
	bucket.mu.Lock()
	tokens := bucket.tokens
	bucket.mu.Unlock()
	if math.Abs(tokens-3) > 0.001 {
		t.Fatalf("tokens after partial write = %.3f, want 3", tokens)
	}
	if wrapper.writeExemptRemaining != 0 {
		t.Fatalf("exempt bytes after partial write = %d, want 0", wrapper.writeExemptRemaining)
	}
}

func TestTrafficShaperAllowsReverseProxyWebSocketUpgrade(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		conn, rw, err := w.(http.Hijacker).Hijack()
		if err != nil {
			return
		}
		defer conn.Close()
		_, _ = rw.WriteString("HTTP/1.1 101 Switching Protocols\r\nConnection: Upgrade\r\nUpgrade: websocket\r\n\r\n")
		if err := rw.Flush(); err != nil {
			return
		}
		_, _ = io.Copy(conn, rw)
	}))
	defer upstream.Close()

	upstreamURL, err := url.Parse(upstream.URL)
	if err != nil {
		t.Fatalf("parse upstream URL: %v", err)
	}
	decision := publicTrafficShaperDecision{
		Rule:           publicTrafficShaperRuleConfig{ProtocolScope: publicTrafficShaperProtocolScopeAll},
		UploadBucket:   newByteTokenBucket(1024, 1024, time.Now()),
		DownloadBucket: newByteTokenBucket(1024, 1024, time.Now()),
	}
	proxy := &httputil.ReverseProxy{Rewrite: func(r *httputil.ProxyRequest) {
		r.SetURL(upstreamURL)
		r.Out.Body = decision.wrapUploadBody(r.Out.Context(), r.Out.Body)
	}}
	proxy.ModifyResponse = func(resp *http.Response) error {
		resp.Body = decision.wrapUpgradeBody(resp.Request.Context(), resp.Body)
		return nil
	}
	proxyServer := httptest.NewServer(proxy)
	defer proxyServer.Close()

	proxyURL, err := url.Parse(proxyServer.URL)
	if err != nil {
		t.Fatalf("parse proxy URL: %v", err)
	}
	conn, err := net.DialTimeout("tcp", proxyURL.Host, 2*time.Second)
	if err != nil {
		t.Fatalf("dial proxy: %v", err)
	}
	defer conn.Close()
	if err := conn.SetDeadline(time.Now().Add(3 * time.Second)); err != nil {
		t.Fatalf("set connection deadline: %v", err)
	}
	_, err = io.WriteString(conn, "GET /ws HTTP/1.1\r\nHost: example.com\r\nConnection: Upgrade\r\nUpgrade: websocket\r\nSec-WebSocket-Version: 13\r\nSec-WebSocket-Key: dGhlIHNhbXBsZSBub25jZQ==\r\n\r\n")
	if err != nil {
		t.Fatalf("write handshake: %v", err)
	}
	reader := bufio.NewReader(conn)
	resp, err := http.ReadResponse(reader, &http.Request{Method: http.MethodGet})
	if err != nil {
		t.Fatalf("read handshake response: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusSwitchingProtocols {
		t.Fatalf("status = %d, want 101", resp.StatusCode)
	}
	if _, err := io.WriteString(conn, "ping"); err != nil {
		t.Fatalf("write upgraded payload: %v", err)
	}
	echo := make([]byte, 4)
	if _, err := io.ReadFull(reader, echo); err != nil {
		t.Fatalf("read upgraded payload: %v", err)
	}
	if string(echo) != "ping" {
		t.Fatalf("echo = %q", echo)
	}
}

type testShapingReadWriteCloser struct {
	reader      *strings.Reader
	writes      bytes.Buffer
	beforeWrite func()
	maxWrite    int
	writeErr    error
	closed      bool
}

func (r *testShapingReadWriteCloser) Read(p []byte) (int, error) {
	return r.reader.Read(p)
}

func (r *testShapingReadWriteCloser) Write(p []byte) (int, error) {
	if r.beforeWrite != nil {
		r.beforeWrite()
	}
	if r.maxWrite > 0 && len(p) > r.maxWrite {
		p = p[:r.maxWrite]
	}
	n, _ := r.writes.Write(p)
	return n, r.writeErr
}

func (r *testShapingReadWriteCloser) Close() error {
	r.closed = true
	return nil
}

func TestPublicTrafficShaperValidationAndDBRoundTrip(t *testing.T) {
	if _, err := validatePublicTrafficShaperRuleInput(
		"invalid-protocol",
		100,
		true,
		p2pstreamv1.PublicTrafficShaperBudgetScope_PUBLIC_TRAFFIC_SHAPER_BUDGET_SCOPE_PER_REQUEST,
		p2pstreamv1.PublicTrafficShaperProtocolScope(99),
		1024,
		0,
		0,
		0,
		0,
		nil,
		nil,
	); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("expected invalid protocol scope error, got %v", err)
	}

	if _, err := validatePublicTrafficShaperRuleInput(
		"invalid",
		100,
		true,
		p2pstreamv1.PublicTrafficShaperBudgetScope_PUBLIC_TRAFFIC_SHAPER_BUDGET_SCOPE_PER_KEY,
		p2pstreamv1.PublicTrafficShaperProtocolScope_PUBLIC_TRAFFIC_SHAPER_PROTOCOL_SCOPE_ALL,
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
		p2pstreamv1.PublicTrafficShaperProtocolScope_PUBLIC_TRAFFIC_SHAPER_PROTOCOL_SCOPE_ALL,
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
		p2pstreamv1.PublicTrafficShaperProtocolScope_PUBLIC_TRAFFIC_SHAPER_PROTOCOL_SCOPE_WEBSOCKET_EXCLUDED,
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
		ProtocolScope:          params.ProtocolScope,
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
	if rule.ProtocolScope != publicTrafficShaperProtocolScopeWebSocketExcluded {
		t.Fatalf("protocol scope = %q", rule.ProtocolScope)
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

func TestPublicTrafficShaperValidationRejectsUnsafeForwardingHeaderKeyParts(t *testing.T) {
	for _, header := range []string{"X-Forwarded-For", "x-real-ip"} {
		_, err := validatePublicTrafficShaperRuleInput(
			"unsafe-key",
			100,
			true,
			p2pstreamv1.PublicTrafficShaperBudgetScope_PUBLIC_TRAFFIC_SHAPER_BUDGET_SCOPE_PER_KEY,
			p2pstreamv1.PublicTrafficShaperProtocolScope_PUBLIC_TRAFFIC_SHAPER_PROTOCOL_SCOPE_ALL,
			1024,
			0,
			0,
			0,
			0,
			[]*p2pstreamv1.PublicRateLimitKeyPart{{
				Source: p2pstreamv1.PublicRateLimitKeySource_PUBLIC_RATE_LIMIT_KEY_SOURCE_HEADER,
				Name:   header,
			}},
			nil,
		)
		if connect.CodeOf(err) != connect.CodeInvalidArgument {
			t.Fatalf("header %q: expected invalid argument, got %v", header, err)
		}
		if !strings.Contains(err.Error(), "traffic shaper header key part must not use forwarding or client IP headers; use REMOTE_IP") {
			t.Fatalf("header %q: unexpected error %v", header, err)
		}
	}
}

func TestPublicTrafficShaperValidationAllowsApplicationHeaderKeyPart(t *testing.T) {
	params, err := validatePublicTrafficShaperRuleInput(
		"safe-key",
		100,
		true,
		p2pstreamv1.PublicTrafficShaperBudgetScope_PUBLIC_TRAFFIC_SHAPER_BUDGET_SCOPE_PER_KEY,
		p2pstreamv1.PublicTrafficShaperProtocolScope_PUBLIC_TRAFFIC_SHAPER_PROTOCOL_SCOPE_ALL,
		1024,
		0,
		0,
		0,
		0,
		[]*p2pstreamv1.PublicRateLimitKeyPart{{
			Source: p2pstreamv1.PublicRateLimitKeySource_PUBLIC_RATE_LIMIT_KEY_SOURCE_HEADER,
			Name:   "x-plan",
		}},
		nil,
	)
	if err != nil {
		t.Fatalf("validate application header key part: %v", err)
	}
	if !strings.Contains(params.KeyPartsJSON, `"name":"X-Plan"`) {
		t.Fatalf("key parts json = %q, want canonical X-Plan", params.KeyPartsJSON)
	}
}

func TestPublicTrafficShaperPerRequestIgnoresUnsafeKeyParts(t *testing.T) {
	params, err := validatePublicTrafficShaperRuleInput(
		"per-request",
		100,
		true,
		p2pstreamv1.PublicTrafficShaperBudgetScope_PUBLIC_TRAFFIC_SHAPER_BUDGET_SCOPE_PER_REQUEST,
		p2pstreamv1.PublicTrafficShaperProtocolScope_PUBLIC_TRAFFIC_SHAPER_PROTOCOL_SCOPE_ALL,
		1024,
		0,
		0,
		0,
		0,
		[]*p2pstreamv1.PublicRateLimitKeyPart{{
			Source: p2pstreamv1.PublicRateLimitKeySource_PUBLIC_RATE_LIMIT_KEY_SOURCE_HEADER,
			Name:   "X-Forwarded-For",
		}},
		nil,
	)
	if err != nil {
		t.Fatalf("per-request shaper should ignore unsafe key parts: %v", err)
	}
	if params.KeyPartsJSON != "[]" {
		t.Fatalf("per-request key parts json = %q, want []", params.KeyPartsJSON)
	}
}

func TestPublicTrafficShaperStoredPerKeyRejectsUnsafeForwardingHeaderKeyPart(t *testing.T) {
	_, err := publicTrafficShaperRuleRowToConfig(db.PublicTrafficShaperRule{
		Name:                   "stored-unsafe",
		BudgetScope:            publicTrafficShaperBudgetScopePerKey,
		UploadBytesPerSecond:   1024,
		DownloadBytesPerSecond: 0,
		KeyPartsJson:           `[{"source":"header","name":"x-forwarded-for"}]`,
	})
	if err == nil {
		t.Fatal("expected stored per-key unsafe forwarding header key part to be rejected")
	}
	if !strings.Contains(err.Error(), "traffic shaper header key part must not use forwarding or client IP headers; use REMOTE_IP") {
		t.Fatalf("unexpected stored-row error: %v", err)
	}
}

func TestPublicTrafficShaperStoredPerRequestIgnoresUnsafeForwardingHeaderKeyPart(t *testing.T) {
	rule, err := publicTrafficShaperRuleRowToConfig(db.PublicTrafficShaperRule{
		Name:                   "stored-per-request",
		BudgetScope:            publicTrafficShaperBudgetScopePerRequest,
		UploadBytesPerSecond:   1024,
		DownloadBytesPerSecond: 0,
		KeyPartsJson:           `[{"source":"header","name":"x-forwarded-for"}]`,
	})
	if err != nil {
		t.Fatalf("stored per-request shaper should ignore unsafe key parts: %v", err)
	}
	if rule.KeyParts != nil {
		t.Fatalf("per-request key parts = %#v, want nil", rule.KeyParts)
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

func TestPublicTrafficShaperCapsPerKeyBuckets(t *testing.T) {
	shaper := newPublicTrafficShaper()
	listener := publicListenerConfig{ID: 1, Protocol: publicListenerProtocolHTTP}
	rule := testTrafficShaperRule(1, "per-key", 100, publicTrafficShaperBudgetScopePerKey, 0, 1024)
	rule.KeyParts = []publicRateLimitKeyPartConfig{{Source: publicRateLimitKeySourceHeader, Name: "X-User"}}
	rule.Fingerprint = publicTrafficShaperRuleFingerprint(rule)
	now := time.Unix(1, 0)

	for i := 0; i < maxTrafficShaperBucketsPerRule+1; i++ {
		req := testRateLimitRequest("GET", "http://example.com/download", "198.51.100.10:1234")
		req.Header.Set("X-User", strconv.Itoa(i))
		if _, ok := shaper.evaluate([]publicTrafficShaperRuleConfig{rule}, listener, req, now.Add(time.Duration(i)*time.Millisecond)); !ok {
			t.Fatalf("request %d did not select shaper", i)
		}
	}

	shaper.mu.Lock()
	defer shaper.mu.Unlock()
	runtime := shaper.rules[rule.ID]
	if got := len(runtime.downloadBuckets); got != maxTrafficShaperBucketsPerRule {
		t.Fatalf("download buckets = %d, want capped at %d", got, maxTrafficShaperBucketsPerRule)
	}
}

func testTrafficShaperRule(id int64, name string, priority int64, scope string, uploadBPS int64, downloadBPS int64) publicTrafficShaperRuleConfig {
	rule := publicTrafficShaperRuleConfig{
		ID:                     id,
		Name:                   name,
		Priority:               priority,
		Enabled:                true,
		BudgetScope:            scope,
		ProtocolScope:          publicTrafficShaperProtocolScopeAll,
		UploadBytesPerSecond:   uploadBPS,
		DownloadBytesPerSecond: downloadBPS,
		KeyParts:               []publicRateLimitKeyPartConfig{{Source: publicRateLimitKeySourceRemoteIP}},
		UpdatedAt:              time.Unix(1, 0),
	}
	rule.Fingerprint = publicTrafficShaperRuleFingerprint(rule)
	return rule
}
