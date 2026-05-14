package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"connectrpc.com/connect"

	p2pstreamv1 "p2pstream/gen/proto/p2pstream/v1"
	"p2pstream/internal/db"
)

func TestPublicWafCaptchaVerificationProviders(t *testing.T) {
	providers := []string{
		publicWafCaptchaProviderTurnstile,
		publicWafCaptchaProviderHCaptcha,
		publicWafCaptchaProviderRecaptcha,
	}
	for _, providerType := range providers {
		t.Run(providerType, func(t *testing.T) {
			var received url.Values
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if err := r.ParseForm(); err != nil {
					t.Errorf("parse form: %v", err)
				}
				received = r.Form
				if r.Form.Get("secret") == "secret" && r.Form.Get("response") == "token" && r.Form.Get("remoteip") == "203.0.113.7" {
					_, _ = w.Write([]byte(`{"success":true}`))
					return
				}
				_, _ = w.Write([]byte(`{"success":false}`))
			}))
			defer server.Close()

			oldEndpoint := publicWafCaptchaVerifyEndpoints[providerType]
			publicWafCaptchaVerifyEndpoints[providerType] = server.URL
			t.Cleanup(func() {
				publicWafCaptchaVerifyEndpoints[providerType] = oldEndpoint
			})

			waf := newPublicWAF()
			provider := publicWafCaptchaProviderConfig{ProviderType: providerType, SecretKey: "secret"}
			if err := waf.verifyCaptcha(provider, "token", "203.0.113.7"); err != nil {
				t.Fatalf("verify captcha: %v", err)
			}
			if received.Get("secret") != "secret" || received.Get("response") != "token" || received.Get("remoteip") != "203.0.113.7" {
				t.Fatalf("verification form = %v", received)
			}
			if err := waf.verifyCaptcha(provider, "bad-token", "203.0.113.7"); err == nil {
				t.Fatal("expected failed captcha verification")
			}
		})
	}
}

func TestPublicWafCaptchaVerificationTimeout(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(50 * time.Millisecond)
		_, _ = w.Write([]byte(`{"success":true}`))
	}))
	defer server.Close()

	oldEndpoint := publicWafCaptchaVerifyEndpoints[publicWafCaptchaProviderTurnstile]
	publicWafCaptchaVerifyEndpoints[publicWafCaptchaProviderTurnstile] = server.URL
	t.Cleanup(func() {
		publicWafCaptchaVerifyEndpoints[publicWafCaptchaProviderTurnstile] = oldEndpoint
	})

	waf := newPublicWAF()
	waf.captchaHTTPClient = &http.Client{Timeout: 5 * time.Millisecond}
	err := waf.verifyCaptcha(publicWafCaptchaProviderConfig{ProviderType: publicWafCaptchaProviderTurnstile, SecretKey: "secret"}, "token", "203.0.113.7")
	if err == nil {
		t.Fatal("expected timeout error")
	}
}

func TestPublicWafCaptchaPassCookieAllowsRequest(t *testing.T) {
	waf := newPublicWAF()
	snap := testWafSnapshot(testWafRule(1, publicWafActionCaptcha), nil)
	snap.WafCaptchaProviders[1] = publicWafCaptchaProviderConfig{
		ID:           1,
		Name:         "turnstile",
		ProviderType: publicWafCaptchaProviderTurnstile,
		SiteKey:      "site",
		SecretKey:    "secret",
		Enabled:      true,
	}
	waf.reconcile(snap)
	now := time.Unix(100, 0)
	req := httptest.NewRequest(http.MethodGet, "http://example.com/", nil)
	decision, allowed := waf.evaluate(snap, snap.Listeners[1], req, now, nil)
	if allowed {
		t.Fatal("request without captcha cookie was allowed")
	}
	if decision.Action != publicWafActionCaptcha || decision.ChallengeKind != publicWafCaptchaProviderTurnstile {
		t.Fatalf("decision = %#v, want captcha challenge", decision)
	}

	req = httptest.NewRequest(http.MethodGet, "http://example.com/", nil)
	req.AddCookie(waf.signedRuleCookie(1, publicWafCaptchaCookieKind, "", time.Minute, now))
	if _, allowed := waf.evaluate(snap, snap.Listeners[1], req, now.Add(time.Second), nil); !allowed {
		t.Fatal("request with valid captcha cookie was not allowed")
	}
}

func TestPublicWafSignedCookiesRejectExpiredAndForgedValues(t *testing.T) {
	waf := newPublicWAF()
	waf.cookieSecret = []byte("test-secret")
	now := time.Unix(100, 0)
	cookie := waf.signedRuleCookie(7, publicWafCaptchaCookieKind, "", time.Minute, now)

	req := httptest.NewRequest(http.MethodGet, "http://example.com/", nil)
	req.AddCookie(cookie)
	if !waf.validRuleCookieLocked(req, 7, publicWafCaptchaCookieKind, now.Add(time.Second)) {
		t.Fatal("valid signed cookie was rejected")
	}
	if waf.validRuleCookieLocked(req, 7, publicWafCaptchaCookieKind, now.Add(2*time.Minute)) {
		t.Fatal("expired signed cookie was accepted")
	}

	forged := *cookie
	forged.Value += "x"
	forgedReq := httptest.NewRequest(http.MethodGet, "http://example.com/", nil)
	forgedReq.AddCookie(&forged)
	if waf.validRuleCookieLocked(forgedReq, 7, publicWafCaptchaCookieKind, now.Add(time.Second)) {
		t.Fatal("forged signed cookie was accepted")
	}
	if waf.validRuleCookieLocked(req, 8, publicWafCaptchaCookieKind, now.Add(time.Second)) {
		t.Fatal("cookie signed for another rule was accepted")
	}
}

func TestPublicWafWaitingRoomFIFOAdmission(t *testing.T) {
	waf := newPublicWAF()
	rule := testWafRule(1, publicWafActionWaitingRoom)
	rule.WaitingRoom.MaxAdmittedSessions = 1
	rule.WaitingRoom.AdmissionRatePerSecond = 1
	rule.WaitingRoom.AdmissionSessionTTLMillis = 1000
	rule.WaitingRoom.QueuePollIntervalMillis = 1000
	rule.WaitingRoom.QueueTimeoutMillis = 60000
	rule.Fingerprint = publicWafRuleFingerprint(rule)
	snap := testWafSnapshot(rule, nil)
	waf.reconcile(snap)
	now := time.Unix(100, 0)

	firstReq := httptest.NewRequest(http.MethodGet, "http://example.com/", nil)
	firstDecision, firstAllowed := waf.evaluate(snap, snap.Listeners[1], firstReq, now, nil)
	if firstAllowed || firstDecision.StatusCode != http.StatusSeeOther {
		t.Fatalf("first decision = %#v allowed=%v, want immediate admission", firstDecision, firstAllowed)
	}

	secondReq := httptest.NewRequest(http.MethodGet, "http://example.com/", nil)
	secondDecision, secondAllowed := waf.evaluate(snap, snap.Listeners[1], secondReq, now.Add(100*time.Millisecond), nil)
	if secondAllowed || secondDecision.StatusCode != http.StatusServiceUnavailable || secondDecision.QueuePosition != 1 {
		t.Fatalf("second decision = %#v allowed=%v, want queued position 1", secondDecision, secondAllowed)
	}

	queuedReq := httptest.NewRequest(http.MethodGet, "http://example.com/", nil)
	for _, cookie := range secondDecision.Cookies {
		queuedReq.AddCookie(cookie)
	}
	admitDecision, admitAllowed := waf.evaluate(snap, snap.Listeners[1], queuedReq, now.Add(1200*time.Millisecond), nil)
	if admitAllowed || admitDecision.StatusCode != http.StatusSeeOther {
		t.Fatalf("queued decision = %#v allowed=%v, want admission after first TTL", admitDecision, admitAllowed)
	}
}

func TestPublicWafAutomaticActivationUsesPressureSignals(t *testing.T) {
	app := &App{PublicWAF: newPublicWAF()}
	rule := testWafRule(1, publicWafActionBlock)
	rule.ActivationMode = publicWafActivationAutomatic
	rule.Triggers.MinimumRequestRate = 0
	rule.Triggers.TrafficSpikeMultiplier = 0
	rule.Triggers.ProxyActiveRequests = 1
	rule.Triggers.BackendActiveRequests = 0
	rule.Triggers.AgentActiveRequests = 0
	rule.Triggers.ServerCPUPercent = 0
	rule.Triggers.AgentCPUPercent = 0
	rule.Triggers.MinimumActiveMillis = 0
	rule.Triggers.QuietPeriodMillis = 0
	rule.Fingerprint = publicWafRuleFingerprint(rule)
	snap := testWafSnapshot(rule, nil)
	app.PublicWAF.reconcile(snap)
	done := app.PublicWAF.beginProxyRequest()
	defer done()

	req := httptest.NewRequest(http.MethodGet, "http://example.com/", nil)
	decision, allowed := app.PublicWAF.evaluate(snap, snap.Listeners[1], req, time.Unix(100, 0), app)
	if allowed {
		t.Fatal("automatic WAF rule was not activated by proxy active pressure")
	}
	if !decision.AutomaticActive || decision.Action != publicWafActionBlock {
		t.Fatalf("decision = %#v, want automatic block", decision)
	}
}

func TestPublicWafCaptchaProviderValidationPreservesSecret(t *testing.T) {
	existing := db.PublicWafCaptchaProvider{
		Name:         "captcha",
		ProviderType: publicWafCaptchaProviderTurnstile,
		SiteKey:      "site",
		SecretKey:    "original-secret",
		Enabled:      1,
	}
	params, _, err := validatePublicWafCaptchaProviderInput(
		"captcha",
		p2pstreamv1.PublicWafCaptchaProviderType_PUBLIC_WAF_CAPTCHA_PROVIDER_TYPE_HCAPTCHA,
		"new-site",
		"",
		true,
		&existing,
		false,
	)
	if err != nil {
		t.Fatalf("validate provider preserving secret: %v", err)
	}
	if params.SecretKey != "original-secret" {
		t.Fatalf("secret = %q, want preserved secret", params.SecretKey)
	}

	params, _, err = validatePublicWafCaptchaProviderInput(
		"captcha",
		p2pstreamv1.PublicWafCaptchaProviderType_PUBLIC_WAF_CAPTCHA_PROVIDER_TYPE_RECAPTCHA_V2,
		"new-site",
		"replacement-secret",
		true,
		&existing,
		true,
	)
	if err != nil {
		t.Fatalf("validate provider replacing secret: %v", err)
	}
	if params.SecretKey != "replacement-secret" {
		t.Fatalf("secret = %q, want replacement secret", params.SecretKey)
	}
}

func TestPublicWafValidationRequiresEnabledCaptchaProvider(t *testing.T) {
	database := newServerTestDB(t)
	app := NewApp(nil, database)
	disabled, err := database.CreatePublicWafCaptchaProvider(context.Background(), db.CreatePublicWafCaptchaProviderParams{
		Name:         "captcha",
		ProviderType: publicWafCaptchaProviderTurnstile,
		SiteKey:      "site",
		SecretKey:    "secret",
		Enabled:      0,
	})
	if err != nil {
		t.Fatalf("create provider: %v", err)
	}
	_, err = app.validatePublicWafRuleInput(
		context.Background(),
		"captcha-rule",
		100,
		true,
		p2pstreamv1.PublicWafRuleAction_PUBLIC_WAF_RULE_ACTION_CAPTCHA,
		p2pstreamv1.PublicWafActivationMode_PUBLIC_WAF_ACTIVATION_MODE_ALWAYS,
		nil,
		nil,
		disabled.ID,
		0,
		nil,
		nil,
		0,
		"",
		"",
		nil,
	)
	if connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("expected invalid provider error, got %v", err)
	}
}

func testWafSnapshot(rule publicWafRuleConfig, providers map[int64]publicWafCaptchaProviderConfig) *publicProxySnapshot {
	snap := &publicProxySnapshot{
		Listeners:           map[int64]publicListenerConfig{1: {ID: 1, Protocol: publicListenerProtocolHTTP}},
		WafRules:            []publicWafRuleConfig{rule},
		WafCaptchaProviders: providers,
		WafCookieSecret:     []byte("test-secret"),
	}
	if snap.WafCaptchaProviders == nil {
		snap.WafCaptchaProviders = map[int64]publicWafCaptchaProviderConfig{}
	}
	return snap
}

func testWafRule(id int64, action string) publicWafRuleConfig {
	rule := publicWafRuleConfig{
		ID:                       id,
		Name:                     "waf",
		Priority:                 100,
		Enabled:                  true,
		Action:                   action,
		ActivationMode:           publicWafActivationAlways,
		KeyParts:                 []publicRateLimitKeyPartConfig{{Source: publicRateLimitKeySourceRemoteIP}},
		CaptchaProviderID:        1,
		CaptchaPassTTL:           defaultWafCaptchaPassTTL,
		WaitingRoom:              publicWafWaitingRoomConfig{MaxAdmittedSessions: 50, AdmissionRatePerSecond: 10, AdmissionSessionTTLMillis: 600000, QueuePollIntervalMillis: 5000, QueueTimeoutMillis: 1800000, PageTitle: "Waiting room", PageBody: "Traffic is high."},
		Triggers:                 publicWafTriggerConfig{RequestWindowMillis: 10000, MinimumRequestRate: 50, TrafficSpikeMultiplier: 4, ProxyActiveRequests: 100, BackendActiveRequests: 100, AgentActiveRequests: 50, ServerCPUPercent: 85, AgentCPUPercent: 85, MinimumActiveMillis: 30000, QuietPeriodMillis: 60000},
		BlockResponseStatusCode:  http.StatusForbidden,
		BlockResponseBody:        "blocked\n",
		BlockResponseContentType: "text/plain; charset=utf-8",
	}
	rule.Fingerprint = publicWafRuleFingerprint(rule)
	return rule
}
