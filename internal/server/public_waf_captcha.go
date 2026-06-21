package server

import (
	"bytes"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	htmltemplate "html/template"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

type publicWafCookiePayload struct {
	RuleID          int64  `json:"rule_id"`
	ListenerID      int64  `json:"listener_id"`
	RuleFingerprint string `json:"rule_fingerprint"`
	Host            string `json:"host"`
	Kind            string `json:"kind"`
	SessionID       string `json:"session_id,omitempty"`
	ExpiresUnixMs   int64  `json:"expires_unix_ms"`
}

type publicWafCaptchaChallengePayload struct {
	RuleID          int64  `json:"rule_id"`
	ListenerID      int64  `json:"listener_id"`
	RuleFingerprint string `json:"rule_fingerprint"`
	Host            string `json:"host"`
	Method          string `json:"method"`
	ReturnTo        string `json:"return_to"`
	ExpiresUnixMs   int64  `json:"expires_unix_ms"`
}

const (
	publicWafCaptchaVerifyMaxFormBytes       = 16 * 1024
	publicWafCaptchaChallengeTTL             = 10 * time.Minute
	publicWafCaptchaVerifyWindow             = time.Minute
	publicWafCaptchaVerifyIPLimit            = 10
	publicWafCaptchaVerifyRuleLimit          = 120
	publicWafCaptchaVerifyMaxKeys            = 20000
	publicWafCaptchaVerifyIdleTTL            = 15 * time.Minute
	publicWafCaptchaVerifyPruneInterval      = time.Minute
	publicWafCaptchaVerifyMaxConcurrentCalls = 32
	publicWafReservedEndpointWindow          = time.Minute
	publicWafReservedEndpointIPLimit         = 60
	publicWafReservedEndpointPathLimit       = 1200
	publicWafReservedEndpointMaxKeys         = publicWafCaptchaVerifyMaxKeys
	publicWafReservedEndpointIdleTTL         = publicWafCaptchaVerifyIdleTTL
	publicWafReservedEndpointPruneInterval   = publicWafCaptchaVerifyPruneInterval
)

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
	if decision, limited := a.rateLimitPublicWAFReservedEndpoint(w, r, listenerID); limited {
		return decision, true
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

func (a *App) rateLimitPublicWAFReservedEndpoint(w http.ResponseWriter, r *http.Request, listenerID int64) (publicWafDecision, bool) {
	if a == nil || a.PublicWAF == nil || a.PublicWAF.reservedEndpointLimiter == nil {
		return publicWafDecision{}, false
	}
	path := ""
	if r != nil && r.URL != nil {
		path = r.URL.Path
	}
	retryAfter, allowed := a.PublicWAF.reservedEndpointLimiter.allow(listenerID, path, remoteIPForRateLimit(r), time.Now())
	if allowed {
		return publicWafDecision{}, false
	}
	decision := publicWafReservedRateLimitDecision(listenerID, path, retryAfter)
	w.Header().Set("Retry-After", captchaRetryAfterSeconds(retryAfter))
	http.Error(w, "WAF reserved endpoint rate limit exceeded", http.StatusTooManyRequests)
	return decision, true
}

func publicWafReservedRateLimitDecision(listenerID int64, path string, retryAfter time.Duration) publicWafDecision {
	decision := publicWafDecision{
		Listener:   publicListenerConfig{ID: listenerID},
		StatusCode: http.StatusTooManyRequests,
		ErrorKind:  "waf_reserved_rate_limited",
		RetryAfter: retryAfter,
	}
	switch path {
	case publicWafCaptchaVerifyPath:
		decision.Action = publicWafActionCaptcha
		decision.ChallengeKind = publicWafActionCaptcha
	case publicWafWaitingRoomPath, publicWafWaitingRoomStatusPath:
		decision.Action = publicWafActionWaitingRoom
		decision.ChallengeKind = publicWafActionWaitingRoom
	}
	return decision
}

func (a *App) servePublicWAFCaptchaVerify(w http.ResponseWriter, r *http.Request, listenerID int64) publicWafDecision {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return publicWafDecision{Action: publicWafActionCaptcha, StatusCode: http.StatusMethodNotAllowed, ErrorKind: "waf_captcha_method_not_allowed", ChallengeKind: publicWafActionCaptcha}
	}
	if a == nil || a.PublicWAF == nil {
		http.Error(w, "captcha rule unavailable", http.StatusServiceUnavailable)
		return publicWafDecision{Action: publicWafActionCaptcha, StatusCode: http.StatusServiceUnavailable, ErrorKind: "waf_captcha_provider_unavailable", ChallengeKind: publicWafActionCaptcha}
	}
	r.Body = http.MaxBytesReader(w, r.Body, publicWafCaptchaVerifyMaxFormBytes)
	if err := r.ParseForm(); err != nil {
		var maxBytesErr *http.MaxBytesError
		if errors.As(err, &maxBytesErr) {
			http.Error(w, "captcha submission too large", http.StatusRequestEntityTooLarge)
			return publicWafDecision{Action: publicWafActionCaptcha, StatusCode: http.StatusRequestEntityTooLarge, ErrorKind: "waf_captcha_form_too_large", ChallengeKind: publicWafActionCaptcha}
		}
		http.Error(w, "invalid captcha submission", http.StatusBadRequest)
		return publicWafDecision{Action: publicWafActionCaptcha, StatusCode: http.StatusBadRequest, ErrorKind: "waf_captcha_invalid_form", ChallengeKind: publicWafActionCaptcha}
	}
	ruleID, _ := strconv.ParseInt(r.Form.Get("rule_id"), 10, 64)
	returnTo := sanitizeWAFReturnTo(r.Form.Get("return_to"))
	now := time.Now()
	challenge, ok := a.PublicWAF.verifyCaptchaChallenge(strings.TrimSpace(r.Form.Get("captcha_challenge")), now)
	if !ok || ruleID <= 0 || ruleID != challenge.RuleID || listenerID != challenge.ListenerID || returnTo != challenge.ReturnTo || publicWafChallengeRequestHost(r) != challenge.Host {
		http.Error(w, "invalid captcha challenge", http.StatusBadRequest)
		return publicWafDecision{Action: publicWafActionCaptcha, StatusCode: http.StatusBadRequest, ErrorKind: "waf_captcha_invalid_challenge", ChallengeKind: publicWafActionCaptcha}
	}
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
	if rule.Fingerprint != challenge.RuleFingerprint {
		http.Error(w, "invalid captcha challenge", http.StatusBadRequest)
		decision.StatusCode = http.StatusBadRequest
		decision.ErrorKind = "waf_captcha_invalid_challenge"
		return decision
	}
	decision.Action = publicWafActionCaptcha
	decision.StatusCode = http.StatusForbidden
	decision.ErrorKind = "waf_captcha_failed"
	decision.ChallengeKind = provider.ProviderType
	decision.CaptchaProvider = provider
	decision.CaptchaReturnTo = returnTo
	if token == "" {
		decision.ErrorKind = "waf_captcha_required"
		decision.CaptchaChallengeToken = a.PublicWAF.signCaptchaChallenge(rule, decision.Listener, r, returnTo, now)
		writeCaptchaChallenge(w, r, decision)
		return decision
	}
	remoteIP := remoteIPForRateLimit(r)
	if a.PublicWAF.captchaVerifyLimiter != nil {
		if retryAfter, allowed := a.PublicWAF.captchaVerifyLimiter.allow(listenerID, rule.ID, remoteIP, now); !allowed {
			w.Header().Set("Retry-After", captchaRetryAfterSeconds(retryAfter))
			http.Error(w, "captcha verification rate limit exceeded", http.StatusTooManyRequests)
			decision.StatusCode = http.StatusTooManyRequests
			decision.ErrorKind = "waf_captcha_rate_limited"
			return decision
		}
	}
	release, acquired := a.PublicWAF.tryAcquireCaptchaVerifySlot()
	if !acquired {
		w.Header().Set("Retry-After", "1")
		http.Error(w, "captcha verification busy", http.StatusTooManyRequests)
		decision.StatusCode = http.StatusTooManyRequests
		decision.ErrorKind = "waf_captcha_busy"
		return decision
	}
	defer release()
	if err := a.PublicWAF.verifyCaptcha(provider, token, remoteIP); err != nil {
		decision.CaptchaChallengeToken = a.PublicWAF.signCaptchaChallenge(rule, decision.Listener, r, returnTo, now)
		writeCaptchaChallenge(w, r, decision)
		return decision
	}
	cookie := a.PublicWAF.signedRuleCookie(rule, decision.Listener, r, publicWafCaptchaCookieKind, "", rule.CaptchaPassTTL, now)
	http.SetCookie(w, cookie)
	redirectTo := sanitizeWAFReturnTo(returnTo)
	redirectTo = strings.ReplaceAll(redirectTo, `\`, "/")
	redirectURL, err := url.Parse(redirectTo)
	if err == nil && redirectURL.Hostname() == "" {
		http.Redirect(w, r, redirectURL.String(), http.StatusSeeOther)
	} else {
		http.Redirect(w, r, "/", http.StatusSeeOther)
	}
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

func (w *publicWAF) tryAcquireCaptchaVerifySlot() (func(), bool) {
	if w == nil || w.captchaVerifySlots == nil {
		return func() {}, true
	}
	select {
	case w.captchaVerifySlots <- struct{}{}:
		return func() {
			<-w.captchaVerifySlots
		}, true
	default:
		return nil, false
	}
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
		writeWaitingRoomPage(w, r, decision)
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
	returnToValue := decision.CaptchaReturnTo
	if returnToValue == "" {
		returnToValue = sanitizeWAFReturnTo(r.URL.RequestURI())
	}
	returnTo := html.EscapeString(sanitizeWAFReturnTo(returnToValue))
	challengeToken := html.EscapeString(decision.CaptchaChallengeToken)
	script, widgetClass, responseName := captchaWidgetParts(provider.ProviderType)
	primaryHTML := fmt.Sprintf(`<script src="%s" async defer></script>
      <form class="cf-challenge-form" method="post" action="%s">
        <input type="hidden" name="rule_id" value="%d">
        <input type="hidden" name="return_to" value="%s">
        <input type="hidden" name="captcha_challenge" value="%s">
        <div class="cf-widget %s" data-sitekey="%s"></div>
        <noscript><p class="cf-noscript">JavaScript is required for %s.</p></noscript>
        <button class="cf-button" type="submit">Continue</button>
      </form>`,
		html.EscapeString(script),
		html.EscapeString(publicWafCaptchaVerifyPath),
		decision.Rule.ID,
		returnTo,
		challengeToken,
		html.EscapeString(widgetClass),
		html.EscapeString(provider.SiteKey),
		html.EscapeString(responseName),
	)
	host := publicWafRequestHost(r)
	title := "Just a moment..."
	pageBody := "Complete the verification below to continue."
	referenceID := publicWafReferenceID(decision.Rule.ID)
	if decision.Rule.CaptchaPageTemplateBody != "" {
		var rendered bytes.Buffer
		err := renderPublicWafHTMLTemplate(&rendered, decision.Rule.CaptchaPageTemplateBody, map[string]any{
			"captcha_element_html": htmltemplate.HTML(primaryHTML),
			"host":                 host,
			"rule_name":            decision.Rule.Name,
			"reference_id":         referenceID,
			"page_title":           title,
			"page_body":            pageBody,
			"status_url":           publicWafCaptchaVerifyPath,
		})
		if err == nil {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.Header().Set("Cache-Control", "no-store")
			w.WriteHeader(status)
			_, _ = w.Write(rendered.Bytes())
			return
		}
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	writePublicWafInterstitialPage(w, publicWafPageModel{
		Title:       title,
		Heading:     host + " needs to review the security of your connection before proceeding.",
		Lead:        pageBody,
		Host:        host,
		ReferenceID: referenceID,
		FooterLabel: "Security by p2pstream",
		Diagnostics: []publicWafPageDiagnostic{
			{Label: "Browser", Status: "Working", Tone: "ok"},
			{Label: "p2pstream", Status: "Security check", Tone: "warn"},
			{Label: "Destination", Status: "Protected", Tone: "muted"},
		},
		PrimaryHTML:   primaryHTML,
		SecondaryHTML: `<p>This check helps protect the site from automated traffic. Request bodies are not replayed after verification.</p>`,
	})
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
	if err != nil || parsed.IsAbs() || parsed.Host != "" || parsed.Opaque != "" {
		return "/"
	}
	if !isSafeLocalRedirectTarget(parsed.Path) {
		return "/"
	}
	if escapedPath := parsed.EscapedPath(); escapedPath != "" {
		unescapedPath, err := url.PathUnescape(escapedPath)
		if err != nil || !isSafeLocalRedirectTarget(unescapedPath) {
			return "/"
		}
	}
	if isPublicWAFReservedPath(parsed.Path) {
		return "/"
	}
	return parsed.RequestURI()
}

func publicWafChallengeRequestHost(r *http.Request) string {
	if r == nil {
		return ""
	}
	host := normalizeRequestHost(r.Host)
	if host == "" && r.URL != nil {
		host = normalizeRequestHost(r.URL.Host)
	}
	return host
}

func (w *publicWAF) signCaptchaChallenge(rule publicWafRuleConfig, listener publicListenerConfig, r *http.Request, returnTo string, now time.Time) string {
	secret := w.loadCookieSecret()
	if len(secret) == 0 {
		return ""
	}
	method := ""
	if r != nil {
		method = strings.ToUpper(strings.TrimSpace(r.Method))
	}
	payload := publicWafCaptchaChallengePayload{
		RuleID:          rule.ID,
		ListenerID:      listener.ID,
		RuleFingerprint: rule.Fingerprint,
		Host:            publicWafChallengeRequestHost(r),
		Method:          method,
		ReturnTo:        sanitizeWAFReturnTo(returnTo),
		ExpiresUnixMs:   now.Add(publicWafCaptchaChallengeTTL).UnixMilli(),
	}
	body, _ := json.Marshal(payload)
	encoded := base64.RawURLEncoding.EncodeToString(body)
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(encoded))
	signature := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	return encoded + "." + signature
}

func (w *publicWAF) verifyCaptchaChallenge(value string, now time.Time) (publicWafCaptchaChallengePayload, bool) {
	encoded, signature, ok := strings.Cut(value, ".")
	secret := w.loadCookieSecret()
	if !ok || encoded == "" || signature == "" || len(secret) == 0 {
		return publicWafCaptchaChallengePayload{}, false
	}
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(encoded))
	expected := mac.Sum(nil)
	actual, err := base64.RawURLEncoding.DecodeString(signature)
	if err != nil || !hmac.Equal(expected, actual) {
		return publicWafCaptchaChallengePayload{}, false
	}
	body, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		return publicWafCaptchaChallengePayload{}, false
	}
	var payload publicWafCaptchaChallengePayload
	if err := json.Unmarshal(body, &payload); err != nil {
		return publicWafCaptchaChallengePayload{}, false
	}
	if payload.RuleID <= 0 || payload.ListenerID <= 0 || payload.RuleFingerprint == "" || payload.Method == "" || payload.ExpiresUnixMs <= now.UnixMilli() {
		return publicWafCaptchaChallengePayload{}, false
	}
	if payload.ReturnTo == "" || payload.ReturnTo != sanitizeWAFReturnTo(payload.ReturnTo) {
		return publicWafCaptchaChallengePayload{}, false
	}
	return payload, true
}

func captchaRetryAfterSeconds(d time.Duration) string {
	if d <= time.Second {
		return "1"
	}
	return strconv.FormatInt(int64((d+time.Second-time.Nanosecond)/time.Second), 10)
}

func (w *publicWAF) validRuleCookieLocked(r *http.Request, rule publicWafRuleConfig, listener publicListenerConfig, kind string, now time.Time) bool {
	name := wafCookieName(rule.ID, kind)
	cookie, err := r.Cookie(name)
	if err != nil {
		return false
	}
	payload, ok := w.verifyCookieValueLocked(cookie.Value, now)
	return ok && publicWafCookieMatchesRequest(payload, r, rule, listener, kind)
}

func publicWafCookieMatchesRequest(payload publicWafCookiePayload, r *http.Request, rule publicWafRuleConfig, listener publicListenerConfig, kind string) bool {
	return rule.ID > 0 &&
		listener.ID > 0 &&
		rule.Fingerprint != "" &&
		payload.RuleID == rule.ID &&
		payload.ListenerID == listener.ID &&
		payload.RuleFingerprint == rule.Fingerprint &&
		payload.Host == publicWafChallengeRequestHost(r) &&
		payload.Kind == kind
}

func publicWafCookieSecure(listener publicListenerConfig, r *http.Request) bool {
	if listener.Protocol == publicListenerProtocolHTTPS {
		return true
	}
	return r != nil && r.TLS != nil
}

func (w *publicWAF) signedRuleCookie(rule publicWafRuleConfig, listener publicListenerConfig, r *http.Request, kind string, sessionID string, ttl time.Duration, now time.Time) *http.Cookie {
	if ttl <= 0 {
		ttl = defaultWafCaptchaPassTTL
	}
	payload := publicWafCookiePayload{
		RuleID:          rule.ID,
		ListenerID:      listener.ID,
		RuleFingerprint: rule.Fingerprint,
		Host:            publicWafChallengeRequestHost(r),
		Kind:            kind,
		SessionID:       sessionID,
		ExpiresUnixMs:   now.Add(ttl).UnixMilli(),
	}
	return &http.Cookie{
		Name:     wafCookieName(rule.ID, kind),
		Value:    w.signCookieValue(payload),
		Path:     "/",
		Expires:  now.Add(ttl),
		MaxAge:   int(ttl.Seconds()),
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   publicWafCookieSecure(listener, r),
	}
}

func (w *publicWAF) signCookieValue(payload publicWafCookiePayload) string {
	secret := w.loadCookieSecret()
	if len(secret) == 0 {
		return ""
	}
	body, _ := json.Marshal(payload)
	encoded := base64.RawURLEncoding.EncodeToString(body)
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(encoded))
	signature := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	return encoded + "." + signature
}

func (w *publicWAF) verifyCookieValueLocked(value string, now time.Time) (publicWafCookiePayload, bool) {
	encoded, signature, ok := strings.Cut(value, ".")
	secret := w.loadCookieSecret()
	if !ok || encoded == "" || signature == "" || len(secret) == 0 {
		return publicWafCookiePayload{}, false
	}
	mac := hmac.New(sha256.New, secret)
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

func (w *publicWAF) queueCookiePayloadLocked(r *http.Request, rule publicWafRuleConfig, listener publicListenerConfig, now time.Time) (publicWafCookiePayload, bool) {
	cookie, err := r.Cookie(wafCookieName(rule.ID, publicWafQueueCookieKind))
	if err != nil {
		return publicWafCookiePayload{}, false
	}
	payload, ok := w.verifyCookieValueLocked(cookie.Value, now)
	if !ok || !publicWafCookieMatchesRequest(payload, r, rule, listener, publicWafQueueCookieKind) || payload.SessionID == "" {
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
