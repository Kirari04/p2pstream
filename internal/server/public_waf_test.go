package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"connectrpc.com/connect"

	p2pstreamv1 "p2pstream/gen/proto/p2pstream/v1"
	"p2pstream/internal/db"
)

func TestPublicWafCaptchaPageUsesCloudflareInspiredLayout(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "http://app.example.test/private?x=1", nil)
	resp := httptest.NewRecorder()
	writeCaptchaChallenge(resp, req, testWafCaptchaPageDecision(publicWafCaptchaProviderTurnstile))

	if resp.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want %d", resp.Code, http.StatusForbidden)
	}
	if contentType := resp.Header().Get("Content-Type"); !strings.Contains(contentType, "text/html") {
		t.Fatalf("Content-Type = %q, want text/html", contentType)
	}
	if cacheControl := resp.Header().Get("Cache-Control"); cacheControl != "no-store" {
		t.Fatalf("Cache-Control = %q, want no-store", cacheControl)
	}
	body := resp.Body.String()
	for _, want := range []string{
		"needs to review the security of your connection",
		"Browser",
		"p2pstream",
		"Destination",
		"Security by p2pstream",
		"cf-turnstile",
		`name="rule_id"`,
		`name="return_to"`,
		`name="captcha_challenge"`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("captcha page missing %q\n%s", want, body)
		}
	}
	if strings.Contains(body, "Cloudflare") {
		t.Fatalf("captcha page must not claim Cloudflare generated it\n%s", body)
	}
}

func TestPublicWafCaptchaPageEscapesHostAndReturnTo(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "http://example.test/", nil)
	req.Host = `bad.example"><script>alert(1)</script>`
	req.URL.RawQuery = `next="><script>alert(2)</script>`
	resp := httptest.NewRecorder()
	writeCaptchaChallenge(resp, req, testWafCaptchaPageDecision(publicWafCaptchaProviderTurnstile))

	body := resp.Body.String()
	if !strings.Contains(body, `bad.example&#34;&gt;&lt;script&gt;alert(1)&lt;/script&gt;`) {
		t.Fatalf("captcha page did not escape hostile host\n%s", body)
	}
	if !strings.Contains(body, `next=&#34;&gt;&lt;script&gt;alert(2)&lt;/script&gt;`) {
		t.Fatalf("captcha page did not escape hostile return URL\n%s", body)
	}
	if strings.Contains(body, `<script>alert(1)</script>`) || strings.Contains(body, `<script>alert(2)</script>`) {
		t.Fatalf("captcha page rendered raw injected script text\n%s", body)
	}
}

func TestPublicWafCaptchaPageSupportsAllProviders(t *testing.T) {
	cases := []struct {
		name        string
		provider    string
		scriptURL   string
		widgetClass string
		label       string
	}{
		{
			name:        "turnstile",
			provider:    publicWafCaptchaProviderTurnstile,
			scriptURL:   "https://challenges.cloudflare.com/turnstile/v0/api.js",
			widgetClass: "cf-turnstile",
			label:       "Turnstile",
		},
		{
			name:        "hcaptcha",
			provider:    publicWafCaptchaProviderHCaptcha,
			scriptURL:   "https://js.hcaptcha.com/1/api.js",
			widgetClass: "h-captcha",
			label:       "hCaptcha",
		},
		{
			name:        "recaptcha",
			provider:    publicWafCaptchaProviderRecaptcha,
			scriptURL:   "https://www.google.com/recaptcha/api.js",
			widgetClass: "g-recaptcha",
			label:       "reCAPTCHA",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "http://app.example.test/private", nil)
			resp := httptest.NewRecorder()
			writeCaptchaChallenge(resp, req, testWafCaptchaPageDecision(tc.provider))

			body := resp.Body.String()
			for _, want := range []string{tc.scriptURL, tc.widgetClass, "JavaScript is required for " + tc.label} {
				if !strings.Contains(body, want) {
					t.Fatalf("captcha page for %s missing %q\n%s", tc.name, want, body)
				}
			}
		})
	}
}

func TestPublicWafCaptchaPageUsesConfiguredTemplate(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "http://app.example.test/private", nil)
	decision := testWafCaptchaPageDecision(publicWafCaptchaProviderTurnstile)
	decision.Rule.CaptchaPageTemplateBody = `<!doctype html><title>{{ .page_title }}</title><main data-rule="{{ .rule_name }}">{{ .host }} {{ .captcha_element_html }}</main>`
	resp := httptest.NewRecorder()

	writeCaptchaChallenge(resp, req, decision)

	body := resp.Body.String()
	for _, want := range []string{
		`<main data-rule="waf">app.example.test`,
		`class="cf-widget cf-turnstile"`,
		`name="captcha_challenge"`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("captcha template missing %q\n%s", want, body)
		}
	}
	if strings.Contains(body, "Security by p2pstream") {
		t.Fatalf("captcha template fell back to built-in page\n%s", body)
	}
}

func TestPublicWafWaitingRoomPageUsesCloudflareInspiredLayout(t *testing.T) {
	rule := testWafRule(9, publicWafActionWaitingRoom)
	rule.WaitingRoom.PageTitle = "Queue for access"
	rule.WaitingRoom.PageBody = "Traffic is high. Please wait."
	decision := publicWafDecision{
		Rule:          rule,
		Action:        publicWafActionWaitingRoom,
		StatusCode:    http.StatusServiceUnavailable,
		RetryAfter:    5 * time.Second,
		ChallengeKind: publicWafActionWaitingRoom,
		QueuePosition: 12,
	}
	req := httptest.NewRequest(http.MethodGet, "http://app.example.test/private", nil)
	resp := httptest.NewRecorder()
	writePublicWafResponse(resp, req, decision)

	if resp.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d", resp.Code, http.StatusServiceUnavailable)
	}
	if retryAfter := resp.Header().Get("Retry-After"); retryAfter != "5" {
		t.Fatalf("Retry-After = %q, want 5", retryAfter)
	}
	body := resp.Body.String()
	for _, want := range []string{
		"Queue for access",
		"Traffic is high. Please wait.",
		"Queue position",
		"Next check",
		"Browser",
		"p2pstream",
		"Destination",
		"Waiting room by p2pstream",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("waiting-room page missing %q\n%s", want, body)
		}
	}
	if strings.Contains(body, "Cloudflare") {
		t.Fatalf("waiting-room page must not claim Cloudflare generated it\n%s", body)
	}
}

func TestPublicWafWaitingRoomPageEscapesConfiguredCopy(t *testing.T) {
	rule := testWafRule(10, publicWafActionWaitingRoom)
	rule.WaitingRoom.PageTitle = `Wait"><script>alert(1)</script>`
	rule.WaitingRoom.PageBody = `Body"><script>alert(2)</script>`
	req := httptest.NewRequest(http.MethodGet, "http://app.example.test/", nil)
	resp := httptest.NewRecorder()
	writeWaitingRoomPage(resp, req, publicWafDecision{
		Rule:          rule,
		Action:        publicWafActionWaitingRoom,
		StatusCode:    http.StatusServiceUnavailable,
		RetryAfter:    5 * time.Second,
		QueuePosition: 3,
	})

	body := resp.Body.String()
	for _, want := range []string{
		`Wait&#34;&gt;&lt;script&gt;alert(1)&lt;/script&gt;`,
		`Body&#34;&gt;&lt;script&gt;alert(2)&lt;/script&gt;`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("waiting-room page missing escaped copy %q\n%s", want, body)
		}
	}
	if strings.Contains(body, `<script>alert(1)</script>`) || strings.Contains(body, `<script>alert(2)</script>`) {
		t.Fatalf("waiting-room page rendered raw configured HTML\n%s", body)
	}
}

func TestPublicWafWaitingRoomPageKeepsRefreshInterval(t *testing.T) {
	rule := testWafRule(11, publicWafActionWaitingRoom)
	req := httptest.NewRequest(http.MethodGet, "http://app.example.test/", nil)
	resp := httptest.NewRecorder()
	writeWaitingRoomPage(resp, req, publicWafDecision{
		Rule:          rule,
		Action:        publicWafActionWaitingRoom,
		StatusCode:    http.StatusServiceUnavailable,
		RetryAfter:    5 * time.Second,
		QueuePosition: 1,
	})

	if body := resp.Body.String(); !strings.Contains(body, `<meta http-equiv="refresh" content="5">`) {
		t.Fatalf("waiting-room page did not keep meta refresh interval\n%s", body)
	}
}

func TestPublicWafWaitingRoomPageUsesConfiguredTemplate(t *testing.T) {
	rule := testWafRule(12, publicWafActionWaitingRoom)
	rule.WaitingRoomPageTemplateBody = `<!doctype html><main>{{ .host }} #{{ .queue_position }} retry={{ .retry_after_seconds }} status={{ .status_url }}</main>`
	req := httptest.NewRequest(http.MethodGet, "http://app.example.test/private", nil)
	resp := httptest.NewRecorder()

	writeWaitingRoomPage(resp, req, publicWafDecision{
		Rule:          rule,
		Action:        publicWafActionWaitingRoom,
		StatusCode:    http.StatusServiceUnavailable,
		RetryAfter:    5 * time.Second,
		QueuePosition: 7,
	})

	body := resp.Body.String()
	for _, want := range []string{
		"app.example.test #7 retry=5",
		"/.p2pstream/waf/waiting-room/status",
		"rule_id=12",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("waiting-room template missing %q\n%s", want, body)
		}
	}
	if strings.Contains(body, "Waiting room by p2pstream") {
		t.Fatalf("waiting-room template fell back to built-in page\n%s", body)
	}
}

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

func TestPublicWafCaptchaVerifyRequiresSignedChallenge(t *testing.T) {
	_, handler, rule, calls := newTestCaptchaVerifyApp(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"success":false}`))
	})
	form := captchaVerifyForm(rule.ID, "/private", "", "token")
	resp := httptest.NewRecorder()
	handler(resp, newCaptchaVerifyRequest("example.com", form))

	if resp.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", resp.Code, http.StatusBadRequest)
	}
	if got := calls.Load(); got != 0 {
		t.Fatalf("provider calls = %d, want 0", got)
	}
}

func TestPublicWafCaptchaVerifyRejectsOversizedFormBeforeProviderCall(t *testing.T) {
	_, handler, rule, calls := newTestCaptchaVerifyApp(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"success":false}`))
	})
	form := captchaVerifyForm(rule.ID, "/private", "", "token")
	form.Set("padding", strings.Repeat("x", publicWafCaptchaVerifyMaxFormBytes))
	resp := httptest.NewRecorder()
	handler(resp, newCaptchaVerifyRequest("example.com", form))

	if resp.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want %d", resp.Code, http.StatusRequestEntityTooLarge)
	}
	if got := calls.Load(); got != 0 {
		t.Fatalf("provider calls = %d, want 0", got)
	}
}

func TestPublicWafCaptchaVerifyRejectsTamperedChallenge(t *testing.T) {
	app, handler, rule, calls := newTestCaptchaVerifyApp(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"success":false}`))
	})
	challenge := testCaptchaChallenge(t, app, rule, "example.com", "/private")
	form := captchaVerifyForm(rule.ID, "/private", challenge+"x", "token")
	resp := httptest.NewRecorder()
	handler(resp, newCaptchaVerifyRequest("example.com", form))

	if resp.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", resp.Code, http.StatusBadRequest)
	}
	if got := calls.Load(); got != 0 {
		t.Fatalf("provider calls = %d, want 0", got)
	}
}

func TestPublicWafCaptchaVerifyRejectsMismatchedHost(t *testing.T) {
	app, handler, rule, calls := newTestCaptchaVerifyApp(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"success":false}`))
	})
	challenge := testCaptchaChallenge(t, app, rule, "example.com", "/private")
	form := captchaVerifyForm(rule.ID, "/private", challenge, "token")
	resp := httptest.NewRecorder()
	handler(resp, newCaptchaVerifyRequest("evil.example", form))

	if resp.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", resp.Code, http.StatusBadRequest)
	}
	if got := calls.Load(); got != 0 {
		t.Fatalf("provider calls = %d, want 0", got)
	}
}

func TestPublicWafCaptchaVerifyRejectsMismatchedReturnTo(t *testing.T) {
	app, handler, rule, calls := newTestCaptchaVerifyApp(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"success":false}`))
	})
	challenge := testCaptchaChallenge(t, app, rule, "example.com", "/private")
	form := captchaVerifyForm(rule.ID, "/other", challenge, "token")
	resp := httptest.NewRecorder()
	handler(resp, newCaptchaVerifyRequest("example.com", form))

	if resp.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", resp.Code, http.StatusBadRequest)
	}
	if got := calls.Load(); got != 0 {
		t.Fatalf("provider calls = %d, want 0", got)
	}
}

func TestPublicWafCaptchaVerifyRejectsChangedRuleFingerprint(t *testing.T) {
	app, handler, rule, calls := newTestCaptchaVerifyApp(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"success":false}`))
	})
	challenge := testCaptchaChallenge(t, app, rule, "example.com", "/private")

	changedRule := rule
	changedRule.CaptchaPassTTL = time.Hour
	changedRule.Fingerprint = publicWafRuleFingerprint(changedRule)
	app.proxyMu.Lock()
	snap := app.publicSnapshot
	snap.WafRules = []publicWafRuleConfig{changedRule}
	app.proxyMu.Unlock()
	app.PublicWAF.reconcile(snap)

	form := captchaVerifyForm(rule.ID, "/private", challenge, "token")
	resp := httptest.NewRecorder()
	handler(resp, newCaptchaVerifyRequest("example.com", form))

	if resp.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", resp.Code, http.StatusBadRequest)
	}
	if got := calls.Load(); got != 0 {
		t.Fatalf("provider calls = %d, want 0", got)
	}
}

func TestPublicWafCaptchaVerifyThrottlesBeforeProviderCall(t *testing.T) {
	app, handler, rule, calls := newTestCaptchaVerifyApp(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"success":false}`))
	})
	challenge := testCaptchaChallenge(t, app, rule, "example.com", "/private")
	form := captchaVerifyForm(rule.ID, "/private", challenge, "bad-token")

	for i := 0; i < publicWafCaptchaVerifyIPLimit; i++ {
		resp := httptest.NewRecorder()
		handler(resp, newCaptchaVerifyRequest("example.com", form))
		if resp.Code != http.StatusForbidden {
			t.Fatalf("attempt %d status = %d, want %d", i+1, resp.Code, http.StatusForbidden)
		}
	}
	if got := calls.Load(); got != publicWafCaptchaVerifyIPLimit {
		t.Fatalf("provider calls = %d, want %d", got, publicWafCaptchaVerifyIPLimit)
	}

	resp := httptest.NewRecorder()
	handler(resp, newCaptchaVerifyRequest("example.com", form))
	if resp.Code != http.StatusTooManyRequests {
		t.Fatalf("throttled status = %d, want %d", resp.Code, http.StatusTooManyRequests)
	}
	if got := calls.Load(); got != publicWafCaptchaVerifyIPLimit {
		t.Fatalf("provider calls after throttle = %d, want %d", got, publicWafCaptchaVerifyIPLimit)
	}
}

func TestPublicWafCaptchaVerifySuccessSetsCookieAndRedirects(t *testing.T) {
	app, handler, rule, _ := newTestCaptchaVerifyApp(t, func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Errorf("parse form: %v", err)
		}
		if r.Form.Get("response") == "token" {
			_, _ = w.Write([]byte(`{"success":true}`))
			return
		}
		_, _ = w.Write([]byte(`{"success":false}`))
	})
	challenge := testCaptchaChallenge(t, app, rule, "example.com", "/private?x=1")
	form := captchaVerifyForm(rule.ID, "/private?x=1", challenge, "token")
	resp := httptest.NewRecorder()
	handler(resp, newCaptchaVerifyRequest("example.com", form))

	if resp.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want %d", resp.Code, http.StatusSeeOther)
	}
	if location := resp.Header().Get("Location"); location != "/private?x=1" {
		t.Fatalf("Location = %q, want %q", location, "/private?x=1")
	}
	var passCookie *http.Cookie
	for _, cookie := range resp.Result().Cookies() {
		if cookie.Name == wafCookieName(rule.ID, publicWafCaptchaCookieKind) {
			passCookie = cookie
			break
		}
	}
	if passCookie == nil {
		t.Fatalf("missing WAF pass cookie %q", wafCookieName(rule.ID, publicWafCaptchaCookieKind))
	}
	assertPublicWafCookieAttributes(t, passCookie, false)
}

func TestPublicWafCaptchaVerifySanitizesUnsafeReturnRedirect(t *testing.T) {
	app, handler, rule, _ := newTestCaptchaVerifyApp(t, func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Errorf("parse form: %v", err)
		}
		if r.Form.Get("response") == "token" {
			_, _ = w.Write([]byte(`{"success":true}`))
			return
		}
		_, _ = w.Write([]byte(`{"success":false}`))
	})
	unsafeReturnTo := "//evil.example/private"
	challenge := testCaptchaChallenge(t, app, rule, "example.com", unsafeReturnTo)
	form := captchaVerifyForm(rule.ID, unsafeReturnTo, challenge, "token")
	resp := httptest.NewRecorder()
	handler(resp, newCaptchaVerifyRequest("example.com", form))

	if resp.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want %d", resp.Code, http.StatusSeeOther)
	}
	if location := resp.Header().Get("Location"); location != "/" {
		t.Fatalf("Location = %q, want /", location)
	}
}

func TestPublicWafCaptchaVerifySuccessSetsSecureCookieForHTTPSListener(t *testing.T) {
	app, handler, rule, _ := newTestCaptchaVerifyApp(t, func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Errorf("parse form: %v", err)
		}
		if r.Form.Get("response") == "token" {
			_, _ = w.Write([]byte(`{"success":true}`))
			return
		}
		_, _ = w.Write([]byte(`{"success":false}`))
	})
	app.proxyMu.Lock()
	snap := app.publicSnapshot
	listener := snap.Listeners[1]
	listener.Protocol = publicListenerProtocolHTTPS
	snap.Listeners[1] = listener
	app.proxyMu.Unlock()
	app.PublicWAF.reconcile(snap)

	challenge := testCaptchaChallenge(t, app, rule, "example.com", "/private")
	form := captchaVerifyForm(rule.ID, "/private", challenge, "token")
	resp := httptest.NewRecorder()
	handler(resp, newCaptchaVerifyRequest("example.com", form))

	if resp.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want %d", resp.Code, http.StatusSeeOther)
	}
	passCookie := requirePublicWafCookie(t, resp.Result().Cookies(), wafCookieName(rule.ID, publicWafCaptchaCookieKind))
	assertPublicWafCookieAttributes(t, passCookie, true)
}

func TestSanitizeWAFReturnTo(t *testing.T) {
	for _, tc := range []struct {
		name  string
		value string
		want  string
	}{
		{name: "empty", value: "", want: "/"},
		{name: "valid path and query", value: "/private?x=1", want: "/private?x=1"},
		{name: "absolute URL", value: "https://evil.example/private", want: "/"},
		{name: "scheme relative URL", value: "//evil.example/private", want: "/"},
		{name: "backslash-prefixed path", value: `/\evil.example/private`, want: "/"},
		{name: "encoded slash prefix", value: "/%2fevil.example/private", want: "/"},
		{name: "encoded backslash prefix", value: "/%5cevil.example/private", want: "/"},
		{name: "relative path", value: "private", want: "/"},
		{name: "reserved captcha path", value: publicWafCaptchaVerifyPath, want: "/"},
		{name: "reserved waiting room path with query", value: publicWafWaitingRoomStatusPath + "?rule_id=1", want: "/"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := sanitizeWAFReturnTo(tc.value); got != tc.want {
				t.Fatalf("sanitizeWAFReturnTo(%q) = %q, want %q", tc.value, got, tc.want)
			}
		})
	}
}

func TestPublicWafWaitingRoomStatusSanitizesUnsafeReturnRedirect(t *testing.T) {
	app := NewApp(nil, nil)
	rule := testWafRule(1, publicWafActionWaitingRoom)
	snap := testWafSnapshot(rule, nil)
	app.proxyMu.Lock()
	app.publicSnapshot = snap
	app.proxyMu.Unlock()
	app.PublicWAF.reconcile(snap)

	req := httptest.NewRequest(http.MethodGet, "http://example.com"+publicWafWaitingRoomStatusPath+"?rule_id=1&return_to=%2F%2Fevil.example%2Fprivate", nil)
	req.AddCookie(app.PublicWAF.signedRuleCookie(rule.ID, publicWafAdmissionCookieKind, "session", time.Minute, time.Now(), snap.Listeners[1], req))
	resp := httptest.NewRecorder()

	decision := app.servePublicWAFWaitingRoomStatus(resp, req, 1)

	if decision.StatusCode != http.StatusSeeOther {
		t.Fatalf("decision status = %d, want %d", decision.StatusCode, http.StatusSeeOther)
	}
	if location := resp.Header().Get("Location"); location != "/" {
		t.Fatalf("Location = %q, want /", location)
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
	req.AddCookie(waf.signedRuleCookie(1, publicWafCaptchaCookieKind, "", time.Minute, now, snap.Listeners[1], req))
	if _, allowed := waf.evaluate(snap, snap.Listeners[1], req, now.Add(time.Second), nil); !allowed {
		t.Fatal("request with valid captcha cookie was not allowed")
	}
}

func TestPublicProxyCaptchaPassStillHitsRateLimit(t *testing.T) {
	app := NewApp(nil, nil)
	wafRule := testWafRule(1, publicWafActionCaptcha)
	wafRule.Fingerprint = publicWafRuleFingerprint(wafRule)
	rateLimitRule := publicRateLimitRuleConfig{
		ID:                  1,
		Name:                "one-request",
		Priority:            100,
		Enabled:             true,
		Algorithm:           publicRateLimitAlgorithmFixedWindow,
		Limit:               1,
		WindowMillis:        60_000,
		KeyParts:            []publicRateLimitKeyPartConfig{{Source: publicRateLimitKeySourceRemoteIP}},
		ResponseStatusCode:  http.StatusTooManyRequests,
		ResponseBody:        "Rate limit exceeded\n",
		ResponseContentType: "text/plain; charset=utf-8",
	}
	rateLimitRule.Fingerprint = publicRateLimitRuleFingerprint(rateLimitRule)
	snap := &publicProxySnapshot{
		Listeners: map[int64]publicListenerConfig{
			1: {ID: 1, Protocol: publicListenerProtocolHTTP, Enabled: true, DefaultBackendID: 1},
		},
		Backends: map[int64]publicBackendConfig{
			1: {
				ID:                 1,
				Name:               "static",
				BackendType:        publicBackendTypeStatic,
				StaticStatusCode:   http.StatusOK,
				StaticResponseBody: "ok\n",
				Enabled:            true,
			},
		},
		RoutesByListener: map[int64][]publicRouteConfig{1: nil},
		WafRules:         []publicWafRuleConfig{wafRule},
		WafCaptchaProviders: map[int64]publicWafCaptchaProviderConfig{
			1: {
				ID:           1,
				Name:         "turnstile",
				ProviderType: publicWafCaptchaProviderTurnstile,
				SiteKey:      "site",
				SecretKey:    "secret",
				Enabled:      true,
			},
		},
		WafCookieSecret: []byte("test-secret"),
		RateLimitRules:  []publicRateLimitRuleConfig{rateLimitRule},
	}
	app.proxyMu.Lock()
	app.publicSnapshot = snap
	app.proxyMu.Unlock()
	app.PublicWAF.reconcile(snap)
	app.RateLimiter.reconcile(snap)
	handler := app.publicProxyHandler(1)

	noPassReq := httptest.NewRequest(http.MethodGet, "http://example.com/", nil)
	noPassResp := httptest.NewRecorder()
	handler(noPassResp, noPassReq)
	if noPassResp.Code != http.StatusForbidden {
		t.Fatalf("request without captcha pass status = %d, want WAF captcha status %d", noPassResp.Code, http.StatusForbidden)
	}

	passCookie := app.PublicWAF.signedRuleCookie(wafRule.ID, publicWafCaptchaCookieKind, "", time.Minute, time.Now(), snap.Listeners[1], nil)
	firstReq := httptest.NewRequest(http.MethodGet, "http://example.com/", nil)
	firstReq.AddCookie(passCookie)
	firstResp := httptest.NewRecorder()
	handler(firstResp, firstReq)
	if firstResp.Code != http.StatusOK {
		t.Fatalf("first request with captcha pass status = %d, want %d", firstResp.Code, http.StatusOK)
	}

	secondReq := httptest.NewRequest(http.MethodGet, "http://example.com/", nil)
	secondReq.AddCookie(passCookie)
	secondResp := httptest.NewRecorder()
	handler(secondResp, secondReq)
	if secondResp.Code != http.StatusTooManyRequests {
		t.Fatalf("second request with captcha pass status = %d, want rate limit status %d", secondResp.Code, http.StatusTooManyRequests)
	}
}

func TestPublicWafSignedCookiesRejectExpiredAndForgedValues(t *testing.T) {
	waf := newPublicWAF()
	waf.storeCookieSecret([]byte("test-secret"))
	now := time.Unix(100, 0)
	cookie := waf.signedRuleCookie(7, publicWafCaptchaCookieKind, "", time.Minute, now, publicListenerConfig{Protocol: publicListenerProtocolHTTP}, nil)

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

func TestPublicWafCookieSecretConcurrentReconcileAndSigning(t *testing.T) {
	waf := newPublicWAF()
	rule := testWafRule(1, publicWafActionCaptcha)
	snap := testWafSnapshot(rule, map[int64]publicWafCaptchaProviderConfig{
		1: {
			ID:           1,
			Name:         "turnstile",
			ProviderType: publicWafCaptchaProviderTurnstile,
			SiteKey:      "site",
			SecretKey:    "secret",
			Enabled:      true,
		},
	})
	waf.reconcile(snap)
	listener := snap.Listeners[1]
	req := httptest.NewRequest(http.MethodGet, "http://example.com/protected?x=1", nil)
	returnTo := req.URL.RequestURI()

	const iterations = 2000
	errCh := make(chan string, 3)
	var wg sync.WaitGroup
	wg.Add(3)

	go func() {
		defer wg.Done()
		for i := 0; i < iterations; i++ {
			next := *snap
			next.WafCookieSecret = []byte("test-secret-" + strconv.Itoa(i))
			waf.reconcile(&next)
		}
	}()

	signAndVerify := func() {
		defer wg.Done()
		for i := 0; i < iterations; i++ {
			now := time.Unix(100, int64(i))
			challenge := waf.signCaptchaChallenge(rule, listener, req, returnTo, now)
			if challenge == "" {
				errCh <- "empty captcha challenge"
				return
			}
			waf.verifyCaptchaChallenge(challenge, now)

			cookie := waf.signedRuleCookie(rule.ID, publicWafCaptchaCookieKind, "", time.Minute, now, listener, req)
			if cookie == nil || cookie.Value == "" {
				errCh <- "empty signed WAF cookie"
				return
			}
			cookieReq := httptest.NewRequest(http.MethodGet, "http://example.com/protected?x=1", nil)
			cookieReq.AddCookie(cookie)
			waf.validRuleCookieLocked(cookieReq, rule.ID, publicWafCaptchaCookieKind, now)
		}
	}
	go signAndVerify()
	go signAndVerify()

	wg.Wait()
	close(errCh)
	for err := range errCh {
		if err != "" {
			t.Fatal(err)
		}
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

func TestPublicWafWaitingRoomCapsQueuedSessions(t *testing.T) {
	previousCap := maxWafWaitingRoomQueuedSessions
	maxWafWaitingRoomQueuedSessions = 2
	t.Cleanup(func() { maxWafWaitingRoomQueuedSessions = previousCap })

	waf := newPublicWAF()
	rule := testWafRule(1, publicWafActionWaitingRoom)
	rule.WaitingRoom.MaxAdmittedSessions = 1
	rule.WaitingRoom.AdmissionRatePerSecond = 1
	rule.WaitingRoom.AdmissionSessionTTLMillis = int64(time.Hour / time.Millisecond)
	rule.WaitingRoom.QueuePollIntervalMillis = 1000
	rule.WaitingRoom.QueueTimeoutMillis = 60000
	rule.Fingerprint = publicWafRuleFingerprint(rule)
	snap := testWafSnapshot(rule, nil)
	waf.reconcile(snap)
	now := time.Unix(100, 0)

	if decision, allowed := waf.evaluate(snap, snap.Listeners[1], httptest.NewRequest(http.MethodGet, "http://example.com/", nil), now, nil); allowed || decision.StatusCode != http.StatusSeeOther {
		t.Fatalf("first decision = %#v allowed=%v, want admission", decision, allowed)
	}
	for i := 0; i < 2; i++ {
		decision, allowed := waf.evaluate(snap, snap.Listeners[1], httptest.NewRequest(http.MethodGet, "http://example.com/", nil), now.Add(time.Duration(i+1)*time.Second), nil)
		if allowed || decision.StatusCode != http.StatusServiceUnavailable || len(decision.Cookies) == 0 {
			t.Fatalf("queued decision %d = %#v allowed=%v, want queued cookie", i, decision, allowed)
		}
	}
	fullDecision, fullAllowed := waf.evaluate(snap, snap.Listeners[1], httptest.NewRequest(http.MethodGet, "http://example.com/", nil), now.Add(3*time.Second), nil)
	if fullAllowed || fullDecision.ErrorKind != "waf_waiting_room_queue_full" || len(fullDecision.Cookies) != 0 {
		t.Fatalf("full decision = %#v allowed=%v, want queue_full without cookie", fullDecision, fullAllowed)
	}
}

func TestPublicWafWaitingRoomCookiesFollowListenerProtocol(t *testing.T) {
	tests := []struct {
		name       string
		protocol   string
		targetURL  string
		wantSecure bool
	}{
		{name: "http-listener", protocol: publicListenerProtocolHTTP, targetURL: "http://example.com/", wantSecure: false},
		{name: "https-listener", protocol: publicListenerProtocolHTTPS, targetURL: "http://example.com/", wantSecure: true},
		{name: "tls-request", protocol: publicListenerProtocolHTTP, targetURL: "https://example.com/", wantSecure: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			waf := newPublicWAF()
			rule := testWafRule(1, publicWafActionWaitingRoom)
			rule.WaitingRoom.MaxAdmittedSessions = 1
			rule.WaitingRoom.AdmissionRatePerSecond = 1
			rule.Fingerprint = publicWafRuleFingerprint(rule)
			snap := testWafSnapshot(rule, nil)
			listener := snap.Listeners[1]
			listener.Protocol = tt.protocol
			snap.Listeners[1] = listener
			waf.reconcile(snap)

			req := httptest.NewRequest(http.MethodGet, tt.targetURL, nil)
			decision, allowed := waf.evaluate(snap, snap.Listeners[1], req, time.Unix(100, 0), nil)
			if allowed || decision.StatusCode != http.StatusSeeOther {
				t.Fatalf("decision = %#v allowed=%v, want immediate admission", decision, allowed)
			}

			queueCookie := requirePublicWafCookie(t, decision.Cookies, wafCookieName(rule.ID, publicWafQueueCookieKind))
			admissionCookie := requirePublicWafCookie(t, decision.Cookies, wafCookieName(rule.ID, publicWafAdmissionCookieKind))
			assertPublicWafCookieAttributes(t, queueCookie, tt.wantSecure)
			assertPublicWafCookieAttributes(t, admissionCookie, tt.wantSecure)
		})
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
		disabled.ID,
		0,
		nil,
		nil,
		0,
		"",
		p2pstreamv1.PublicResponseBodyMode_PUBLIC_RESPONSE_BODY_MODE_INLINE,
		0,
		0,
		0,
		"",
		nil,
		nil,
	)
	if connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("expected invalid provider error, got %v", err)
	}
}

func TestPublicWafTriggerValidationPreservesDisabledSignals(t *testing.T) {
	cfg, err := validatePublicWafTriggers(&p2pstreamv1.PublicWafTriggerConfig{
		RequestWindowMillis:    10000,
		MinimumRequestRate:     0,
		TrafficSpikeMultiplier: 0,
		ProxyActiveRequests:    0,
		BackendActiveRequests:  0,
		AgentActiveRequests:    0,
		ServerCpuPercent:       0,
		AgentCpuPercent:        0,
		MinimumActiveMillis:    30000,
		QuietPeriodMillis:      60000,
	})
	if err != nil {
		t.Fatalf("validate triggers: %v", err)
	}
	if cfg.MinimumRequestRate != 0 ||
		cfg.TrafficSpikeMultiplier != 0 ||
		cfg.ProxyActiveRequests != 0 ||
		cfg.BackendActiveRequests != 0 ||
		cfg.AgentActiveRequests != 0 ||
		cfg.ServerCPUPercent != 0 ||
		cfg.AgentCPUPercent != 0 {
		t.Fatalf("disabled trigger signals were not preserved: %#v", cfg)
	}
}

func newTestCaptchaVerifyApp(t *testing.T, provider http.HandlerFunc) (*App, http.HandlerFunc, publicWafRuleConfig, *atomic.Int64) {
	t.Helper()
	var calls atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		provider(w, r)
	}))
	t.Cleanup(server.Close)

	oldEndpoint := publicWafCaptchaVerifyEndpoints[publicWafCaptchaProviderTurnstile]
	publicWafCaptchaVerifyEndpoints[publicWafCaptchaProviderTurnstile] = server.URL
	t.Cleanup(func() {
		publicWafCaptchaVerifyEndpoints[publicWafCaptchaProviderTurnstile] = oldEndpoint
	})

	app := NewApp(nil, nil)
	rule := testWafRule(1, publicWafActionCaptcha)
	snap := testWafSnapshot(rule, map[int64]publicWafCaptchaProviderConfig{
		1: {
			ID:           1,
			Name:         "turnstile",
			ProviderType: publicWafCaptchaProviderTurnstile,
			SiteKey:      "site",
			SecretKey:    "secret",
			Enabled:      true,
		},
	})
	app.proxyMu.Lock()
	app.publicSnapshot = snap
	app.proxyMu.Unlock()
	app.PublicWAF.reconcile(snap)
	return app, app.publicProxyHandler(1), rule, &calls
}

func captchaVerifyForm(ruleID int64, returnTo string, challenge string, token string) url.Values {
	form := url.Values{}
	form.Set("rule_id", strconv.FormatInt(ruleID, 10))
	form.Set("return_to", returnTo)
	form.Set("captcha_challenge", challenge)
	form.Set("cf-turnstile-response", token)
	return form
}

func newCaptchaVerifyRequest(host string, form url.Values) *http.Request {
	req := httptest.NewRequest(http.MethodPost, "http://"+host+publicWafCaptchaVerifyPath, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.RemoteAddr = "198.51.100.9:12345"
	return req
}

func testCaptchaChallenge(t *testing.T, app *App, rule publicWafRuleConfig, host string, returnTo string) string {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "http://"+host+returnTo, nil)
	challenge := app.PublicWAF.signCaptchaChallenge(rule, publicListenerConfig{ID: 1, Protocol: publicListenerProtocolHTTP}, req, returnTo, time.Now())
	if challenge == "" {
		t.Fatal("empty captcha challenge")
	}
	return challenge
}

func testWafCaptchaPageDecision(providerType string) publicWafDecision {
	rule := testWafRule(7, publicWafActionCaptcha)
	return publicWafDecision{
		Rule:          rule,
		Action:        publicWafActionCaptcha,
		StatusCode:    http.StatusForbidden,
		ChallengeKind: providerType,
		CaptchaProvider: publicWafCaptchaProviderConfig{
			ID:           1,
			Name:         "captcha",
			ProviderType: providerType,
			SiteKey:      `site"><key`,
			SecretKey:    "secret",
			Enabled:      true,
		},
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

func requirePublicWafCookie(t *testing.T, cookies []*http.Cookie, name string) *http.Cookie {
	t.Helper()
	for _, cookie := range cookies {
		if cookie.Name == name {
			return cookie
		}
	}
	t.Fatalf("missing WAF cookie %q", name)
	return nil
}

func assertPublicWafCookieAttributes(t *testing.T, cookie *http.Cookie, wantSecure bool) {
	t.Helper()
	if cookie.Secure != wantSecure {
		t.Fatalf("cookie %q Secure = %v, want %v", cookie.Name, cookie.Secure, wantSecure)
	}
	if !cookie.HttpOnly {
		t.Fatalf("cookie %q HttpOnly = false, want true", cookie.Name)
	}
	if cookie.SameSite != http.SameSiteLaxMode {
		t.Fatalf("cookie %q SameSite = %v, want %v", cookie.Name, cookie.SameSite, http.SameSiteLaxMode)
	}
	if cookie.Path != "/" {
		t.Fatalf("cookie %q Path = %q, want /", cookie.Name, cookie.Path)
	}
}
