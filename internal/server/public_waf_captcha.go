package server

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

type publicWafCookiePayload struct {
	RuleID          int64  `json:"rule_id"`
	Kind            string `json:"kind"`
	SessionID       string `json:"session_id,omitempty"`
	ExpiresUnixMs   int64  `json:"expires_unix_ms"`
	OriginalPathKey string `json:"original_path_key,omitempty"`
}

var publicWafCaptchaVerifyEndpoints = map[string]string{
	publicWafCaptchaProviderTurnstile: "https://challenges.cloudflare.com/turnstile/v0/siteverify",
	publicWafCaptchaProviderHCaptcha:  "https://hcaptcha.com/siteverify",
	publicWafCaptchaProviderRecaptcha: "https://www.google.com/recaptcha/api/siteverify",
}

func isPublicWAFReservedPath(path string) bool {
	switch path {
	case publicWafCaptchaVerifyPath, publicWafWaitingRoomPath, publicWafWaitingRoomStatusPath:
		return true
	default:
		return false
	}
}

func (a *App) servePublicWAFReserved(w http.ResponseWriter, r *http.Request, listenerID int64) (publicWafDecision, bool) {
	if !isPublicWAFReservedPath(r.URL.Path) {
		return publicWafDecision{}, false
	}
	switch r.URL.Path {
	case publicWafCaptchaVerifyPath:
		return a.servePublicWAFCaptchaVerify(w, r, listenerID), true
	case publicWafWaitingRoomPath, publicWafWaitingRoomStatusPath:
		return a.servePublicWAFWaitingRoomStatus(w, r, listenerID), true
	default:
		http.NotFound(w, r)
		return publicWafDecision{StatusCode: http.StatusNotFound, ErrorKind: "waf_reserved_not_found"}, true
	}
}

func (a *App) servePublicWAFCaptchaVerify(w http.ResponseWriter, r *http.Request, listenerID int64) publicWafDecision {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return publicWafDecision{Action: publicWafActionCaptcha, StatusCode: http.StatusMethodNotAllowed, ErrorKind: "waf_captcha_method_not_allowed", ChallengeKind: publicWafActionCaptcha}
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid captcha submission", http.StatusBadRequest)
		return publicWafDecision{Action: publicWafActionCaptcha, StatusCode: http.StatusBadRequest, ErrorKind: "waf_captcha_invalid_form", ChallengeKind: publicWafActionCaptcha}
	}
	ruleID, _ := strconv.ParseInt(r.Form.Get("rule_id"), 10, 64)
	returnTo := sanitizeWAFReturnTo(r.Form.Get("return_to"))
	token := strings.TrimSpace(r.Form.Get("cf-turnstile-response"))
	if token == "" {
		token = strings.TrimSpace(r.Form.Get("h-captcha-response"))
	}
	if token == "" {
		token = strings.TrimSpace(r.Form.Get("g-recaptcha-response"))
	}
	decision, rule, provider, ok := a.wafRuleAndProvider(ruleID, listenerID)
	if !ok {
		http.Error(w, "captcha rule unavailable", http.StatusServiceUnavailable)
		return decision
	}
	decision.Action = publicWafActionCaptcha
	decision.StatusCode = http.StatusForbidden
	decision.ErrorKind = "waf_captcha_failed"
	decision.ChallengeKind = provider.ProviderType
	decision.CaptchaProvider = provider
	if token == "" {
		writeCaptchaChallenge(w, r, publicWafDecision{Rule: rule, Listener: decision.Listener, Action: publicWafActionCaptcha, StatusCode: http.StatusForbidden, ErrorKind: "waf_captcha_required", ChallengeKind: provider.ProviderType, CaptchaProvider: provider})
		return decision
	}
	if err := a.PublicWAF.verifyCaptcha(provider, token, remoteIPForRateLimit(r)); err != nil {
		writeCaptchaChallenge(w, r, publicWafDecision{Rule: rule, Listener: decision.Listener, Action: publicWafActionCaptcha, StatusCode: http.StatusForbidden, ErrorKind: "waf_captcha_failed", ChallengeKind: provider.ProviderType, CaptchaProvider: provider})
		return decision
	}
	cookie := a.PublicWAF.signedRuleCookie(rule.ID, publicWafCaptchaCookieKind, "", rule.CaptchaPassTTL, time.Now())
	http.SetCookie(w, cookie)
	http.Redirect(w, r, returnTo, http.StatusSeeOther)
	decision.StatusCode = http.StatusSeeOther
	decision.ErrorKind = ""
	return decision
}

func (a *App) wafRuleAndProvider(ruleID int64, listenerID int64) (publicWafDecision, publicWafRuleConfig, publicWafCaptchaProviderConfig, bool) {
	a.proxyMu.Lock()
	snap := a.publicSnapshot
	a.proxyMu.Unlock()
	decision := publicWafDecision{
		Listener:   publicListenerConfig{ID: listenerID},
		Action:     publicWafActionCaptcha,
		StatusCode: http.StatusServiceUnavailable,
		ErrorKind:  "waf_captcha_provider_unavailable",
	}
	if snap == nil {
		return decision, publicWafRuleConfig{}, publicWafCaptchaProviderConfig{}, false
	}
	if listener, ok := snap.Listeners[listenerID]; ok {
		decision.Listener = listener
	}
	for _, rule := range snap.WafRules {
		if rule.ID != ruleID || rule.Action != publicWafActionCaptcha || !rule.Enabled {
			continue
		}
		provider, ok := snap.WafCaptchaProviders[rule.CaptchaProviderID]
		if !ok || !provider.Enabled {
			decision.Rule = rule
			return decision, rule, publicWafCaptchaProviderConfig{}, false
		}
		decision.Rule = rule
		return decision, rule, provider, true
	}
	return decision, publicWafRuleConfig{}, publicWafCaptchaProviderConfig{}, false
}

func (w *publicWAF) verifyCaptcha(provider publicWafCaptchaProviderConfig, token string, remoteIP string) error {
	if w == nil {
		return errors.New("WAF is unavailable")
	}
	endpoint := publicWafCaptchaVerifyEndpoints[provider.ProviderType]
	if endpoint == "" {
		return fmt.Errorf("unsupported captcha provider %q", provider.ProviderType)
	}
	form := url.Values{}
	form.Set("secret", provider.SecretKey)
	form.Set("response", token)
	if remoteIP != "" && remoteIP != rateLimitMissingValue {
		form.Set("remoteip", remoteIP)
	}
	req, err := http.NewRequest(http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	client := w.captchaHTTPClient
	if client == nil {
		client = &http.Client{Timeout: publicWafChallengeTimeoutSeconds * time.Second}
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
	if err != nil {
		return err
	}
	var payload struct {
		Success bool `json:"success"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return err
	}
	if !payload.Success {
		return errors.New("captcha verification failed")
	}
	return nil
}

func writePublicWafResponse(w http.ResponseWriter, r *http.Request, decision publicWafDecision) {
	for _, cookie := range decision.Cookies {
		http.SetCookie(w, cookie)
	}
	for name, values := range decision.Headers {
		for _, value := range values {
			w.Header().Add(name, value)
		}
	}
	if decision.RetryAfter > 0 {
		w.Header().Set("Retry-After", strconv.Itoa(maxInt(1, int(decision.RetryAfter.Seconds()))))
	}
	if decision.RedirectLocation != "" {
		http.Redirect(w, r, decision.RedirectLocation, http.StatusSeeOther)
		return
	}
	switch decision.Action {
	case publicWafActionCaptcha:
		writeCaptchaChallenge(w, r, decision)
	case publicWafActionWaitingRoom:
		writeWaitingRoomPage(w, decision)
	default:
		if decision.ContentType != "" {
			w.Header().Set("Content-Type", decision.ContentType)
		}
		status := decision.StatusCode
		if status == 0 {
			status = defaultWafBlockStatusCode
		}
		w.WriteHeader(status)
		_, _ = w.Write([]byte(decision.Body))
	}
}

func writeCaptchaChallenge(w http.ResponseWriter, r *http.Request, decision publicWafDecision) {
	provider := decision.CaptchaProvider
	status := decision.StatusCode
	if status == 0 {
		status = http.StatusForbidden
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	returnTo := html.EscapeString(sanitizeWAFReturnTo(r.URL.RequestURI()))
	script, widgetClass, responseName := captchaWidgetParts(provider.ProviderType)
	_, _ = fmt.Fprintf(w, `<!doctype html>
<html lang="en">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <title>Security check</title>
  <script src="%s" async defer></script>
  <style>
    :root { color-scheme: light dark; --bg: #0b0f14; --panel: #151b23; --line: #2b3642; --text: #f5f7fb; --muted: #9aa7b4; --accent: #2dd4bf; }
    * { box-sizing: border-box; }
    body { margin: 0; min-height: 100vh; display: grid; place-items: center; background: var(--bg); color: var(--text); font-family: system-ui, -apple-system, BlinkMacSystemFont, "Segoe UI", sans-serif; padding: 24px; }
    main { width: min(520px, 100%%); border: 1px solid var(--line); background: var(--panel); padding: 32px; border-radius: 8px; }
    h1 { margin: 0 0 12px; font-size: 1.65rem; letter-spacing: 0; }
    p { margin: 0 0 24px; color: var(--muted); line-height: 1.55; }
    button { margin-top: 20px; border: 0; background: var(--accent); color: #06110f; font-weight: 700; padding: 11px 16px; border-radius: 6px; cursor: pointer; }
  </style>
</head>
<body>
  <main>
    <h1>Security check</h1>
    <p>Complete the challenge to continue. The original request body will not be replayed.</p>
    <form method="post" action="%s">
      <input type="hidden" name="rule_id" value="%d">
      <input type="hidden" name="return_to" value="%s">
      <div class="%s" data-sitekey="%s"></div>
      <noscript><p>JavaScript is required for %s.</p></noscript>
      <button type="submit">Continue</button>
    </form>
  </main>
</body>
</html>`, html.EscapeString(script), publicWafCaptchaVerifyPath, decision.Rule.ID, returnTo, html.EscapeString(widgetClass), html.EscapeString(provider.SiteKey), html.EscapeString(responseName))
}

func captchaWidgetParts(providerType string) (script string, widgetClass string, responseName string) {
	switch providerType {
	case publicWafCaptchaProviderHCaptcha:
		return "https://js.hcaptcha.com/1/api.js", "h-captcha", "hCaptcha"
	case publicWafCaptchaProviderRecaptcha:
		return "https://www.google.com/recaptcha/api.js", "g-recaptcha", "reCAPTCHA"
	default:
		return "https://challenges.cloudflare.com/turnstile/v0/api.js", "cf-turnstile", "Turnstile"
	}
}

func sanitizeWAFReturnTo(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "/"
	}
	parsed, err := url.Parse(value)
	if err != nil || parsed.IsAbs() || parsed.Host != "" || !strings.HasPrefix(parsed.Path, "/") {
		return "/"
	}
	if isPublicWAFReservedPath(parsed.Path) {
		return "/"
	}
	return parsed.RequestURI()
}

func (w *publicWAF) validRuleCookieLocked(r *http.Request, ruleID int64, kind string, now time.Time) bool {
	name := wafCookieName(ruleID, kind)
	cookie, err := r.Cookie(name)
	if err != nil {
		return false
	}
	payload, ok := w.verifyCookieValueLocked(cookie.Value, now)
	return ok && payload.RuleID == ruleID && payload.Kind == kind
}

func (w *publicWAF) signedRuleCookie(ruleID int64, kind string, sessionID string, ttl time.Duration, now time.Time) *http.Cookie {
	if ttl <= 0 {
		ttl = defaultWafCaptchaPassTTL
	}
	payload := publicWafCookiePayload{
		RuleID:        ruleID,
		Kind:          kind,
		SessionID:     sessionID,
		ExpiresUnixMs: now.Add(ttl).UnixMilli(),
	}
	return &http.Cookie{
		Name:     wafCookieName(ruleID, kind),
		Value:    w.signCookieValue(payload),
		Path:     "/",
		Expires:  now.Add(ttl),
		MaxAge:   int(ttl.Seconds()),
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   false,
	}
}

func (w *publicWAF) signCookieValue(payload publicWafCookiePayload) string {
	body, _ := json.Marshal(payload)
	encoded := base64.RawURLEncoding.EncodeToString(body)
	mac := hmac.New(sha256.New, w.cookieSecret)
	mac.Write([]byte(encoded))
	signature := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	return encoded + "." + signature
}

func (w *publicWAF) verifyCookieValueLocked(value string, now time.Time) (publicWafCookiePayload, bool) {
	encoded, signature, ok := strings.Cut(value, ".")
	if !ok || encoded == "" || signature == "" || len(w.cookieSecret) == 0 {
		return publicWafCookiePayload{}, false
	}
	mac := hmac.New(sha256.New, w.cookieSecret)
	mac.Write([]byte(encoded))
	expected := mac.Sum(nil)
	actual, err := base64.RawURLEncoding.DecodeString(signature)
	if err != nil || !hmac.Equal(expected, actual) {
		return publicWafCookiePayload{}, false
	}
	body, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		return publicWafCookiePayload{}, false
	}
	var payload publicWafCookiePayload
	if err := json.Unmarshal(body, &payload); err != nil {
		return publicWafCookiePayload{}, false
	}
	if payload.ExpiresUnixMs <= now.UnixMilli() {
		return publicWafCookiePayload{}, false
	}
	return payload, true
}

func (w *publicWAF) queueCookiePayloadLocked(r *http.Request, ruleID int64, now time.Time) (publicWafCookiePayload, bool) {
	cookie, err := r.Cookie(wafCookieName(ruleID, publicWafQueueCookieKind))
	if err != nil {
		return publicWafCookiePayload{}, false
	}
	payload, ok := w.verifyCookieValueLocked(cookie.Value, now)
	if !ok || payload.RuleID != ruleID || payload.Kind != publicWafQueueCookieKind || payload.SessionID == "" {
		return publicWafCookiePayload{}, false
	}
	return payload, true
}

func wafCookieName(ruleID int64, kind string) string {
	switch kind {
	case publicWafCaptchaCookieKind:
		return "p2pstream_waf_" + strconv.FormatInt(ruleID, 10)
	case publicWafAdmissionCookieKind:
		return "p2pstream_waf_" + strconv.FormatInt(ruleID, 10) + "_admission"
	case publicWafQueueCookieKind:
		return "p2pstream_waf_" + strconv.FormatInt(ruleID, 10) + "_queue"
	default:
		return "p2pstream_waf_" + strconv.FormatInt(ruleID, 10) + "_" + kind
	}
}

func randomWAFSessionID() string {
	buf := make([]byte, 18)
	if _, err := rand.Read(buf); err != nil {
		return strconv.FormatInt(time.Now().UnixNano(), 36)
	}
	return base64.RawURLEncoding.EncodeToString(buf)
}
