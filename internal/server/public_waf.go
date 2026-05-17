package server

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"connectrpc.com/connect"

	p2pstreamv1 "p2pstream/gen/proto/p2pstream/v1"
	"p2pstream/internal/db"
	"p2pstream/internal/sysmetrics"
)

const (
	publicWafCaptchaProviderTurnstile = "turnstile"
	publicWafCaptchaProviderHCaptcha  = "hcaptcha"
	publicWafCaptchaProviderRecaptcha = "recaptcha_v2"

	publicWafActionBlock       = "block"
	publicWafActionCaptcha     = "captcha"
	publicWafActionWaitingRoom = "waiting_room"

	publicWafActivationAlways    = "always"
	publicWafActivationAutomatic = "automatic"

	defaultWafRuleName                   = "waf-rule"
	defaultWafCaptchaPassTTL             = 30 * time.Minute
	defaultWafBlockStatusCode            = http.StatusForbidden
	defaultWafBlockBody                  = "Request blocked\n"
	defaultWafBlockContentType           = "text/plain; charset=utf-8"
	defaultWafWaitingRoomMaxAdmitted     = int64(50)
	defaultWafWaitingRoomAdmissionRate   = int64(10)
	defaultWafWaitingRoomAdmissionTTL    = 10 * time.Minute
	defaultWafWaitingRoomPollInterval    = 5 * time.Second
	defaultWafWaitingRoomQueueTimeout    = 30 * time.Minute
	defaultWafWaitingRoomPageTitle       = "Waiting room"
	defaultWafWaitingRoomPageBody        = "Traffic is high. You will be admitted automatically."
	defaultWafTriggerRequestWindow       = 10 * time.Second
	defaultWafTriggerMinimumRequestRate  = int64(50)
	defaultWafTriggerSpikeMultiplier     = 4.0
	defaultWafTriggerProxyActiveRequests = int64(100)
	defaultWafTriggerBackendActive       = int64(100)
	defaultWafTriggerAgentActive         = int64(50)
	defaultWafTriggerServerCPU           = 85.0
	defaultWafTriggerAgentCPU            = 85.0
	defaultWafTriggerMinimumActive       = 30 * time.Second
	defaultWafTriggerQuietPeriod         = 60 * time.Second
	maxWafResponseBodyBytes              = 64 * 1024

	publicWafCaptchaVerifyPath       = "/.p2pstream/waf/captcha/verify"
	publicWafWaitingRoomPath         = "/.p2pstream/waf/waiting-room"
	publicWafWaitingRoomStatusPath   = "/.p2pstream/waf/waiting-room/status"
	publicWafCaptchaCookieKind       = "captcha"
	publicWafAdmissionCookieKind     = "admission"
	publicWafQueueCookieKind         = "queue"
	publicWafChallengeTimeoutSeconds = 3
)

type publicWafCaptchaProviderConfig struct {
	ID           int64
	Name         string
	ProviderType string
	SiteKey      string
	SecretKey    string
	Enabled      bool
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

type publicWafTriggerConfig struct {
	RequestWindowMillis    int64
	MinimumRequestRate     int64
	TrafficSpikeMultiplier float64
	ProxyActiveRequests    int64
	BackendActiveRequests  int64
	AgentActiveRequests    int64
	ServerCPUPercent       float64
	AgentCPUPercent        float64
	MinimumActiveMillis    int64
	QuietPeriodMillis      int64
}

type publicWafWaitingRoomConfig struct {
	MaxAdmittedSessions       int64
	AdmissionRatePerSecond    int64
	AdmissionSessionTTLMillis int64
	QueuePollIntervalMillis   int64
	QueueTimeoutMillis        int64
	PageTitle                 string
	PageBody                  string
}

type publicWafRuleConfig struct {
	ID                          int64
	Name                        string
	Priority                    int64
	Enabled                     bool
	Action                      string
	ActivationMode              string
	Match                       publicPolicyMatchConfig
	KeyParts                    []publicRateLimitKeyPartConfig
	CaptchaProviderID           int64
	CaptchaPassTTL              time.Duration
	WaitingRoom                 publicWafWaitingRoomConfig
	Triggers                    publicWafTriggerConfig
	BlockResponseStatusCode     int
	BlockResponseBody           string
	BlockResponseBodyMode       string
	BlockResponseTemplateID     int64
	CaptchaPageTemplateID       int64
	CaptchaPageTemplateBody     string
	WaitingRoomPageTemplateID   int64
	WaitingRoomPageTemplateBody string
	BlockResponseContentType    string
	BlockResponseHeaders        []publicRateLimitResponseHeaderConfig
	CreatedAt                   time.Time
	UpdatedAt                   time.Time
	Fingerprint                 string
}

type publicWafRuleMutationInput struct {
	Name                                 string
	Priority                             int64
	Enabled                              int64
	Action                               string
	ActivationMode                       string
	MatchJSON                            string
	KeyPartsJSON                         string
	CaptchaProviderID                    sql.NullInt64
	CaptchaPassTTLMillis                 int64
	WaitingRoomMaxAdmittedSessions       int64
	WaitingRoomAdmissionRatePerSecond    int64
	WaitingRoomAdmissionSessionTtlMillis int64
	WaitingRoomQueuePollIntervalMillis   int64
	WaitingRoomQueueTimeoutMillis        int64
	WaitingRoomPageTitle                 string
	WaitingRoomPageBody                  string
	TriggerRequestWindowMillis           int64
	TriggerMinimumRequestRate            int64
	TriggerTrafficSpikeMultiplier        float64
	TriggerProxyActiveRequests           int64
	TriggerBackendActiveRequests         int64
	TriggerAgentActiveRequests           int64
	TriggerServerCpuPercent              float64
	TriggerAgentCpuPercent               float64
	TriggerMinimumActiveMillis           int64
	TriggerQuietPeriodMillis             int64
	BlockResponseStatusCode              int64
	BlockResponseBody                    string
	BlockResponseBodyMode                string
	BlockResponseTemplateID              sql.NullInt64
	CaptchaPageTemplateID                sql.NullInt64
	WaitingRoomPageTemplateID            sql.NullInt64
	BlockResponseContentType             string
	BlockResponseHeadersJSON             string
}

type publicWAF struct {
	mu                   sync.Mutex
	rules                map[int64]*publicWafRuleRuntime
	cookieSecret         atomic.Value // stores immutable []byte
	proxyActiveRequests  atomic.Int64
	captchaHTTPClient    *http.Client
	captchaVerifyLimiter *publicWafCaptchaVerifyLimiter
	captchaVerifySlots   chan struct{}
	cpuSampler           *sysmetrics.ProcessCPUSampler
	lastCPUSampleAt      time.Time
	lastCPUPercent       float64
}

type publicWafRuleRuntime struct {
	fingerprint       string
	requestHits       []time.Time
	baselineRPS       float64
	pressureStartedAt time.Time
	quietStartedAt    time.Time
	automaticActive   bool
	waitingRoom       *publicWaitingRoomRuntime
}

type publicWafDecision struct {
	Rule                  publicWafRuleConfig
	Listener              publicListenerConfig
	Action                string
	StatusCode            int
	ErrorKind             string
	Body                  string
	ContentType           string
	Headers               http.Header
	RetryAfter            time.Duration
	Cookies               []*http.Cookie
	RedirectLocation      string
	AutomaticActive       bool
	ChallengeKind         string
	QueuePosition         int64
	CaptchaProvider       publicWafCaptchaProviderConfig
	CaptchaChallengeToken string
	CaptchaReturnTo       string
}

func newPublicWAF() *publicWAF {
	return &publicWAF{
		rules:                make(map[int64]*publicWafRuleRuntime),
		captchaHTTPClient:    &http.Client{Timeout: publicWafChallengeTimeoutSeconds * time.Second},
		captchaVerifyLimiter: newPublicWafCaptchaVerifyLimiter(),
		captchaVerifySlots:   make(chan struct{}, publicWafCaptchaVerifyMaxConcurrentCalls),
		cpuSampler:           sysmetrics.NewProcessCPUSampler(),
	}
}

func (w *publicWAF) storeCookieSecret(secret []byte) {
	if w == nil || len(secret) == 0 {
		return
	}
	w.cookieSecret.Store(append([]byte(nil), secret...))
}

func (w *publicWAF) loadCookieSecret() []byte {
	if w == nil {
		return nil
	}
	secret, _ := w.cookieSecret.Load().([]byte)
	return secret
}

func (w *publicWAF) beginProxyRequest() func() {
	if w == nil {
		return func() {}
	}
	w.proxyActiveRequests.Add(1)
	return func() {
		w.proxyActiveRequests.Add(-1)
	}
}

func (w *publicWAF) reconcile(snap *publicProxySnapshot) {
	if w == nil {
		return
	}
	keep := make(map[int64]string)
	if snap != nil {
		for _, rule := range snap.WafRules {
			if rule.Enabled {
				keep[rule.ID] = rule.Fingerprint
			}
		}
	}

	w.mu.Lock()
	defer w.mu.Unlock()
	if snap != nil && len(snap.WafCookieSecret) > 0 {
		w.storeCookieSecret(snap.WafCookieSecret)
	}
	for id, runtime := range w.rules {
		if fingerprint, ok := keep[id]; !ok || runtime.fingerprint != fingerprint {
			delete(w.rules, id)
		}
	}
	for id, fingerprint := range keep {
		if _, ok := w.rules[id]; !ok {
			w.rules[id] = &publicWafRuleRuntime{
				fingerprint: fingerprint,
				waitingRoom: newPublicWaitingRoomRuntime(),
			}
		}
	}
}

func (a *App) checkPublicWAF(listenerID int64, r *http.Request) (publicWafDecision, bool) {
	if a == nil || a.PublicWAF == nil {
		return publicWafDecision{}, true
	}
	a.proxyMu.Lock()
	snap := a.publicSnapshot
	a.proxyMu.Unlock()
	if snap == nil || len(snap.WafRules) == 0 {
		return publicWafDecision{}, true
	}
	listener, ok := snap.Listeners[listenerID]
	if !ok {
		return publicWafDecision{}, true
	}
	return a.PublicWAF.evaluate(snap, listener, r, time.Now(), a)
}

func (w *publicWAF) evaluate(snap *publicProxySnapshot, listener publicListenerConfig, r *http.Request, now time.Time, app *App) (publicWafDecision, bool) {
	ordered := append([]publicWafRuleConfig(nil), snap.WafRules...)
	sort.SliceStable(ordered, func(i, j int) bool {
		if ordered[i].Priority == ordered[j].Priority {
			return ordered[i].ID < ordered[j].ID
		}
		return ordered[i].Priority < ordered[j].Priority
	})

	w.mu.Lock()
	defer w.mu.Unlock()
	for _, rule := range ordered {
		if !rule.Enabled || !rule.matches(listener, r) {
			continue
		}
		runtime := w.runtimeLocked(rule)
		automaticActive := false
		if rule.ActivationMode == publicWafActivationAutomatic {
			automaticActive = w.updateAutomaticActivationLocked(runtime, rule, snap, app, now)
			if !automaticActive {
				continue
			}
		}
		switch rule.Action {
		case publicWafActionCaptcha:
			if w.validRuleCookieLocked(r, rule.ID, publicWafCaptchaCookieKind, now) {
				return publicWafDecision{}, true
			}
			provider, ok := snap.WafCaptchaProviders[rule.CaptchaProviderID]
			if !ok || !provider.Enabled {
				return publicWafDecision{
					Rule:            rule,
					Listener:        listener,
					Action:          rule.Action,
					StatusCode:      http.StatusServiceUnavailable,
					ErrorKind:       "waf_captcha_provider_unavailable",
					Body:            "Captcha provider unavailable\n",
					ContentType:     defaultWafBlockContentType,
					AutomaticActive: automaticActive,
					ChallengeKind:   publicWafActionCaptcha,
				}, false
			}
			returnTo := sanitizeWAFReturnTo(r.URL.RequestURI())
			return publicWafDecision{
				Rule:                  rule,
				Listener:              listener,
				Action:                rule.Action,
				StatusCode:            http.StatusForbidden,
				ErrorKind:             "waf_captcha_required",
				AutomaticActive:       automaticActive,
				ChallengeKind:         provider.ProviderType,
				CaptchaProvider:       provider,
				CaptchaChallengeToken: w.signCaptchaChallenge(rule, listener, r, returnTo, now),
				CaptchaReturnTo:       returnTo,
			}, false
		case publicWafActionWaitingRoom:
			decision, allowed := runtime.waitingRoom.evaluateLocked(w, rule, listener, r, now, automaticActive)
			return decision, allowed
		default:
			return publicWafDecision{
				Rule:            rule,
				Listener:        listener,
				Action:          publicWafActionBlock,
				StatusCode:      rule.BlockResponseStatusCode,
				ErrorKind:       "waf_blocked",
				Body:            rule.BlockResponseBody,
				ContentType:     rule.BlockResponseContentType,
				Headers:         wafResponseHeaders(rule.BlockResponseHeaders),
				AutomaticActive: automaticActive,
				ChallengeKind:   publicWafActionBlock,
			}, false
		}
	}
	return publicWafDecision{}, true
}

func (w *publicWAF) runtimeLocked(rule publicWafRuleConfig) *publicWafRuleRuntime {
	runtime := w.rules[rule.ID]
	if runtime == nil || runtime.fingerprint != rule.Fingerprint {
		runtime = &publicWafRuleRuntime{
			fingerprint: rule.Fingerprint,
			waitingRoom: newPublicWaitingRoomRuntime(),
		}
		w.rules[rule.ID] = runtime
	}
	if runtime.waitingRoom == nil {
		runtime.waitingRoom = newPublicWaitingRoomRuntime()
	}
	return runtime
}

func (w *publicWAF) updateAutomaticActivationLocked(runtime *publicWafRuleRuntime, rule publicWafRuleConfig, snap *publicProxySnapshot, app *App, now time.Time) bool {
	rate := runtime.recordAndRate(rule, now)
	pressure := false
	if rule.Triggers.MinimumRequestRate > 0 && rate >= float64(rule.Triggers.MinimumRequestRate) {
		pressure = true
	}
	if rule.Triggers.TrafficSpikeMultiplier > 0 && runtime.baselineRPS > 0 && rate >= runtime.baselineRPS*rule.Triggers.TrafficSpikeMultiplier {
		pressure = true
	}
	if rule.Triggers.ProxyActiveRequests > 0 && w.proxyActiveRequests.Load() >= rule.Triggers.ProxyActiveRequests {
		pressure = true
	}
	if rule.Triggers.BackendActiveRequests > 0 && app.maxPublicBackendActiveRequests(snap) >= rule.Triggers.BackendActiveRequests {
		pressure = true
	}
	if rule.Triggers.AgentActiveRequests > 0 && app.maxAgentActiveRequests() >= rule.Triggers.AgentActiveRequests {
		pressure = true
	}
	if rule.Triggers.ServerCPUPercent > 0 && w.serverCPUPercentLocked(now) >= rule.Triggers.ServerCPUPercent {
		pressure = true
	}
	if rule.Triggers.AgentCPUPercent > 0 && app.maxAgentCPUPercent() >= rule.Triggers.AgentCPUPercent {
		pressure = true
	}

	if !runtime.automaticActive && rate > 0 {
		if runtime.baselineRPS == 0 {
			runtime.baselineRPS = rate
		} else {
			runtime.baselineRPS = runtime.baselineRPS*0.9 + rate*0.1
		}
	}

	minActive := time.Duration(rule.Triggers.MinimumActiveMillis) * time.Millisecond
	quiet := time.Duration(rule.Triggers.QuietPeriodMillis) * time.Millisecond
	if pressure {
		runtime.quietStartedAt = time.Time{}
		if runtime.pressureStartedAt.IsZero() {
			runtime.pressureStartedAt = now
		}
		if minActive <= 0 || now.Sub(runtime.pressureStartedAt) >= minActive {
			runtime.automaticActive = true
		}
		return runtime.automaticActive
	}

	runtime.pressureStartedAt = time.Time{}
	if runtime.automaticActive {
		if runtime.quietStartedAt.IsZero() {
			runtime.quietStartedAt = now
		}
		if quiet <= 0 || now.Sub(runtime.quietStartedAt) >= quiet {
			runtime.automaticActive = false
			runtime.quietStartedAt = time.Time{}
		}
	}
	return runtime.automaticActive
}

func (runtime *publicWafRuleRuntime) recordAndRate(rule publicWafRuleConfig, now time.Time) float64 {
	window := time.Duration(rule.Triggers.RequestWindowMillis) * time.Millisecond
	if window <= 0 {
		window = defaultWafTriggerRequestWindow
	}
	cutoff := now.Add(-window)
	keepAt := 0
	for keepAt < len(runtime.requestHits) && runtime.requestHits[keepAt].Before(cutoff) {
		keepAt++
	}
	if keepAt > 0 {
		copy(runtime.requestHits, runtime.requestHits[keepAt:])
		runtime.requestHits = runtime.requestHits[:len(runtime.requestHits)-keepAt]
	}
	runtime.requestHits = append(runtime.requestHits, now)
	return float64(len(runtime.requestHits)) / window.Seconds()
}

func (w *publicWAF) serverCPUPercentLocked(now time.Time) float64 {
	if w.cpuSampler == nil {
		return 0
	}
	if !w.lastCPUSampleAt.IsZero() && now.Sub(w.lastCPUSampleAt) < 5*time.Second {
		return w.lastCPUPercent
	}
	percent, ok, err := w.cpuSampler.Sample()
	w.lastCPUSampleAt = now
	if err != nil || !ok {
		w.lastCPUPercent = 0
		return 0
	}
	w.lastCPUPercent = percent
	return percent
}

func (a *App) maxPublicBackendActiveRequests(snap *publicProxySnapshot) int64 {
	if a == nil || a.BackendHealth == nil || snap == nil {
		return 0
	}
	var maxActive int64
	for id := range snap.Backends {
		if active := a.BackendHealth.activeRequests(id); active > maxActive {
			maxActive = active
		}
	}
	return maxActive
}

func (a *App) maxAgentActiveRequests() int64 {
	if a == nil || a.AgentHub == nil {
		return 0
	}
	var maxActive int64
	for _, conn := range a.AgentHub.connectedIDs() {
		if conn == nil {
			continue
		}
		if active := conn.ActiveRequests.Load(); active > maxActive {
			maxActive = active
		}
	}
	if latest := a.LatestAgentStats.Load(); latest != nil && int64(latest.ActiveRequests) > maxActive {
		maxActive = int64(latest.ActiveRequests)
	}
	return maxActive
}

func (a *App) maxAgentCPUPercent() float64 {
	if a == nil {
		return 0
	}
	if latest := a.LatestAgentStats.Load(); latest != nil {
		return latest.CPUPercent
	}
	return 0
}

func (rule publicWafRuleConfig) matches(listener publicListenerConfig, r *http.Request) bool {
	return publicRateLimitRuleConfig{Match: rule.Match, KeyParts: rule.KeyParts}.matches(listener, r)
}

func (rule publicWafRuleConfig) keyValues(listener publicListenerConfig, r *http.Request) []string {
	return publicRateLimitRuleConfig{Match: rule.Match, KeyParts: rule.KeyParts}.keyValues(listener, r)
}

func wafResponseHeaders(headers []publicRateLimitResponseHeaderConfig) http.Header {
	resp := make(http.Header)
	for _, header := range headers {
		resp.Add(header.Name, header.Value)
	}
	return resp
}

func (a *App) ensurePublicWafSettings(ctx context.Context) (db.PublicWafSetting, error) {
	row, err := a.DB.GetPublicWafSettings(ctx)
	if err == nil && strings.TrimSpace(row.CookieSigningSecret) != "" {
		return row, nil
	}
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return db.PublicWafSetting{}, connect.NewError(connect.CodeInternal, err)
	}
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		return db.PublicWafSetting{}, connect.NewError(connect.CodeInternal, err)
	}
	row, err = a.DB.UpsertPublicWafSettings(ctx, base64.RawURLEncoding.EncodeToString(secret))
	if err != nil {
		return db.PublicWafSetting{}, connect.NewError(connect.CodeInternal, err)
	}
	return row, nil
}

func publicWafCaptchaProviderRowToConfig(row db.PublicWafCaptchaProvider, includeSecret bool) publicWafCaptchaProviderConfig {
	secret := ""
	if includeSecret {
		secret = row.SecretKey
	}
	return publicWafCaptchaProviderConfig{
		ID:           row.ID,
		Name:         row.Name,
		ProviderType: normalizePublicWafCaptchaProvider(row.ProviderType),
		SiteKey:      row.SiteKey,
		SecretKey:    secret,
		Enabled:      row.Enabled != 0,
		CreatedAt:    row.CreatedAt,
		UpdatedAt:    row.UpdatedAt,
	}
}

func publicWafRuleRowToConfig(row db.PublicWafRule) (publicWafRuleConfig, error) {
	match, err := decodePublicPolicyMatchJSON(row.MatchJson)
	if err != nil {
		return publicWafRuleConfig{}, err
	}
	var keyParts []publicRateLimitKeyPartConfig
	if strings.TrimSpace(row.KeyPartsJson) != "" {
		if err := json.Unmarshal([]byte(row.KeyPartsJson), &keyParts); err != nil {
			return publicWafRuleConfig{}, err
		}
	}
	var blockHeaders []publicRateLimitResponseHeaderConfig
	if strings.TrimSpace(row.BlockResponseHeadersJson) != "" {
		if err := json.Unmarshal([]byte(row.BlockResponseHeadersJson), &blockHeaders); err != nil {
			return publicWafRuleConfig{}, err
		}
	}
	rule := publicWafRuleConfig{
		ID:                row.ID,
		Name:              row.Name,
		Priority:          row.Priority,
		Enabled:           row.Enabled != 0,
		Action:            normalizePublicWafAction(row.Action),
		ActivationMode:    normalizePublicWafActivationMode(row.ActivationMode),
		Match:             match,
		KeyParts:          keyParts,
		CaptchaProviderID: nullInt64Value(row.CaptchaProviderID),
		CaptchaPassTTL:    time.Duration(row.CaptchaPassTtlMillis) * time.Millisecond,
		WaitingRoom: publicWafWaitingRoomConfig{
			MaxAdmittedSessions:       row.WaitingRoomMaxAdmittedSessions,
			AdmissionRatePerSecond:    row.WaitingRoomAdmissionRatePerSecond,
			AdmissionSessionTTLMillis: row.WaitingRoomAdmissionSessionTtlMillis,
			QueuePollIntervalMillis:   row.WaitingRoomQueuePollIntervalMillis,
			QueueTimeoutMillis:        row.WaitingRoomQueueTimeoutMillis,
			PageTitle:                 row.WaitingRoomPageTitle,
			PageBody:                  row.WaitingRoomPageBody,
		},
		Triggers: publicWafTriggerConfig{
			RequestWindowMillis:    row.TriggerRequestWindowMillis,
			MinimumRequestRate:     row.TriggerMinimumRequestRate,
			TrafficSpikeMultiplier: row.TriggerTrafficSpikeMultiplier,
			ProxyActiveRequests:    row.TriggerProxyActiveRequests,
			BackendActiveRequests:  row.TriggerBackendActiveRequests,
			AgentActiveRequests:    row.TriggerAgentActiveRequests,
			ServerCPUPercent:       row.TriggerServerCpuPercent,
			AgentCPUPercent:        row.TriggerAgentCpuPercent,
			MinimumActiveMillis:    row.TriggerMinimumActiveMillis,
			QuietPeriodMillis:      row.TriggerQuietPeriodMillis,
		},
		BlockResponseStatusCode:   int(row.BlockResponseStatusCode),
		BlockResponseBody:         row.BlockResponseBody,
		BlockResponseBodyMode:     normalizePublicResponseBodyMode(row.BlockResponseBodyMode),
		BlockResponseTemplateID:   nullInt64Value(row.BlockResponseTemplateID),
		CaptchaPageTemplateID:     nullInt64Value(row.CaptchaPageTemplateID),
		WaitingRoomPageTemplateID: nullInt64Value(row.WaitingRoomPageTemplateID),
		BlockResponseContentType:  row.BlockResponseContentType,
		BlockResponseHeaders:      blockHeaders,
		CreatedAt:                 row.CreatedAt,
		UpdatedAt:                 row.UpdatedAt,
	}
	applyWafRuleDefaults(&rule)
	rule.Fingerprint = publicWafRuleFingerprint(rule)
	return rule, nil
}

func applyWafRuleDefaults(rule *publicWafRuleConfig) {
	if rule.Action == "" {
		rule.Action = publicWafActionBlock
	}
	if rule.ActivationMode == "" {
		rule.ActivationMode = publicWafActivationAlways
	}
	if len(rule.KeyParts) == 0 {
		rule.KeyParts = []publicRateLimitKeyPartConfig{{Source: publicRateLimitKeySourceRemoteIP}}
	}
	if rule.CaptchaPassTTL <= 0 {
		rule.CaptchaPassTTL = defaultWafCaptchaPassTTL
	}
	if rule.WaitingRoom.MaxAdmittedSessions <= 0 {
		rule.WaitingRoom.MaxAdmittedSessions = defaultWafWaitingRoomMaxAdmitted
	}
	if rule.WaitingRoom.AdmissionRatePerSecond <= 0 {
		rule.WaitingRoom.AdmissionRatePerSecond = defaultWafWaitingRoomAdmissionRate
	}
	if rule.WaitingRoom.AdmissionSessionTTLMillis <= 0 {
		rule.WaitingRoom.AdmissionSessionTTLMillis = int64(defaultWafWaitingRoomAdmissionTTL / time.Millisecond)
	}
	if rule.WaitingRoom.QueuePollIntervalMillis <= 0 {
		rule.WaitingRoom.QueuePollIntervalMillis = int64(defaultWafWaitingRoomPollInterval / time.Millisecond)
	}
	if rule.WaitingRoom.QueueTimeoutMillis <= 0 {
		rule.WaitingRoom.QueueTimeoutMillis = int64(defaultWafWaitingRoomQueueTimeout / time.Millisecond)
	}
	if rule.WaitingRoom.PageTitle == "" {
		rule.WaitingRoom.PageTitle = defaultWafWaitingRoomPageTitle
	}
	if rule.WaitingRoom.PageBody == "" {
		rule.WaitingRoom.PageBody = defaultWafWaitingRoomPageBody
	}
	if rule.Triggers.RequestWindowMillis <= 0 {
		rule.Triggers.RequestWindowMillis = int64(defaultWafTriggerRequestWindow / time.Millisecond)
	}
	if rule.Triggers.MinimumRequestRate < 0 {
		rule.Triggers.MinimumRequestRate = 0
	}
	if rule.Triggers.TrafficSpikeMultiplier < 0 {
		rule.Triggers.TrafficSpikeMultiplier = 0
	}
	if rule.Triggers.MinimumActiveMillis <= 0 {
		rule.Triggers.MinimumActiveMillis = int64(defaultWafTriggerMinimumActive / time.Millisecond)
	}
	if rule.Triggers.QuietPeriodMillis <= 0 {
		rule.Triggers.QuietPeriodMillis = int64(defaultWafTriggerQuietPeriod / time.Millisecond)
	}
	if rule.BlockResponseStatusCode == 0 {
		rule.BlockResponseStatusCode = defaultWafBlockStatusCode
	}
	if rule.BlockResponseBody == "" {
		rule.BlockResponseBody = defaultWafBlockBody
	}
	if rule.BlockResponseContentType == "" {
		rule.BlockResponseContentType = defaultWafBlockContentType
	}
}

func publicWafRuleFingerprint(rule publicWafRuleConfig) string {
	type fingerprint struct {
		Action                      string
		ActivationMode              string
		Match                       publicPolicyMatchConfig
		KeyParts                    []publicRateLimitKeyPartConfig
		CaptchaProviderID           int64
		CaptchaPassTTL              time.Duration
		WaitingRoom                 publicWafWaitingRoomConfig
		Triggers                    publicWafTriggerConfig
		BlockResponseStatusCode     int
		BlockResponseBody           string
		BlockResponseBodyMode       string
		BlockResponseTemplateID     int64
		CaptchaPageTemplateID       int64
		CaptchaPageTemplateBody     string
		WaitingRoomPageTemplateID   int64
		WaitingRoomPageTemplateBody string
		BlockResponseContentType    string
		BlockResponseHeaders        []publicRateLimitResponseHeaderConfig
		UpdatedAt                   int64
	}
	payload, _ := json.Marshal(fingerprint{
		Action:                      rule.Action,
		ActivationMode:              rule.ActivationMode,
		Match:                       rule.Match,
		KeyParts:                    rule.KeyParts,
		CaptchaProviderID:           rule.CaptchaProviderID,
		CaptchaPassTTL:              rule.CaptchaPassTTL,
		WaitingRoom:                 rule.WaitingRoom,
		Triggers:                    rule.Triggers,
		BlockResponseStatusCode:     rule.BlockResponseStatusCode,
		BlockResponseBody:           rule.BlockResponseBody,
		BlockResponseBodyMode:       rule.BlockResponseBodyMode,
		BlockResponseTemplateID:     rule.BlockResponseTemplateID,
		CaptchaPageTemplateID:       rule.CaptchaPageTemplateID,
		CaptchaPageTemplateBody:     rule.CaptchaPageTemplateBody,
		WaitingRoomPageTemplateID:   rule.WaitingRoomPageTemplateID,
		WaitingRoomPageTemplateBody: rule.WaitingRoomPageTemplateBody,
		BlockResponseContentType:    rule.BlockResponseContentType,
		BlockResponseHeaders:        rule.BlockResponseHeaders,
		UpdatedAt:                   rule.UpdatedAt.UnixNano(),
	})
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:])
}

func publicWafCaptchaProvidersToProto(rows []db.PublicWafCaptchaProvider, includeSecret bool) []*p2pstreamv1.PublicWafCaptchaProvider {
	resp := make([]*p2pstreamv1.PublicWafCaptchaProvider, 0, len(rows))
	for _, row := range rows {
		resp = append(resp, publicWafCaptchaProviderConfigToProto(publicWafCaptchaProviderRowToConfig(row, includeSecret), row.SecretKey != ""))
	}
	return resp
}

func publicWafCaptchaProviderConfigToProto(provider publicWafCaptchaProviderConfig, secretSet bool) *p2pstreamv1.PublicWafCaptchaProvider {
	return &p2pstreamv1.PublicWafCaptchaProvider{
		Id:                  provider.ID,
		Name:                provider.Name,
		ProviderType:        protoWafCaptchaProviderFromString(provider.ProviderType),
		SiteKey:             provider.SiteKey,
		SecretKey:           provider.SecretKey,
		SecretKeySet:        secretSet || provider.SecretKey != "",
		Enabled:             provider.Enabled,
		CreatedAtUnixMillis: provider.CreatedAt.UnixMilli(),
		UpdatedAtUnixMillis: provider.UpdatedAt.UnixMilli(),
	}
}

func publicWafRulesToProto(rows []db.PublicWafRule) []*p2pstreamv1.PublicWafRule {
	resp := make([]*p2pstreamv1.PublicWafRule, 0, len(rows))
	for _, row := range rows {
		rule, err := publicWafRuleRowToConfig(row)
		if err != nil {
			continue
		}
		resp = append(resp, publicWafRuleConfigToProto(rule))
	}
	return resp
}

func publicWafRuleConfigToProto(rule publicWafRuleConfig) *p2pstreamv1.PublicWafRule {
	return &p2pstreamv1.PublicWafRule{
		Id:                        rule.ID,
		Name:                      rule.Name,
		Priority:                  rule.Priority,
		Enabled:                   rule.Enabled,
		Action:                    protoWafRuleActionFromString(rule.Action),
		ActivationMode:            protoWafActivationModeFromString(rule.ActivationMode),
		KeyParts:                  rateLimitKeyPartsToProto(rule.KeyParts),
		CaptchaProviderId:         rule.CaptchaProviderID,
		CaptchaPassTtlMillis:      int64(rule.CaptchaPassTTL / time.Millisecond),
		WaitingRoom:               publicWafWaitingRoomToProto(rule.WaitingRoom),
		Triggers:                  publicWafTriggersToProto(rule.Triggers),
		BlockResponseStatusCode:   int64(rule.BlockResponseStatusCode),
		BlockResponseBody:         rule.BlockResponseBody,
		BlockResponseBodyMode:     protoPublicResponseBodyMode(rule.BlockResponseBodyMode),
		BlockResponseTemplateId:   rule.BlockResponseTemplateID,
		CaptchaPageTemplateId:     rule.CaptchaPageTemplateID,
		WaitingRoomPageTemplateId: rule.WaitingRoomPageTemplateID,
		BlockResponseContentType:  rule.BlockResponseContentType,
		BlockResponseHeaders:      rateLimitResponseHeadersToProto(rule.BlockResponseHeaders),
		CreatedAtUnixMillis:       rule.CreatedAt.UnixMilli(),
		UpdatedAtUnixMillis:       rule.UpdatedAt.UnixMilli(),
		MatchRule:                 publicPolicyMatchRuleToProto(rule.Match),
	}
}

func publicWafWaitingRoomToProto(cfg publicWafWaitingRoomConfig) *p2pstreamv1.PublicWafWaitingRoomConfig {
	return &p2pstreamv1.PublicWafWaitingRoomConfig{
		MaxAdmittedSessions:       cfg.MaxAdmittedSessions,
		AdmissionRatePerSecond:    cfg.AdmissionRatePerSecond,
		AdmissionSessionTtlMillis: cfg.AdmissionSessionTTLMillis,
		QueuePollIntervalMillis:   cfg.QueuePollIntervalMillis,
		QueueTimeoutMillis:        cfg.QueueTimeoutMillis,
		PageTitle:                 cfg.PageTitle,
		PageBody:                  cfg.PageBody,
	}
}

func publicWafTriggersToProto(cfg publicWafTriggerConfig) *p2pstreamv1.PublicWafTriggerConfig {
	return &p2pstreamv1.PublicWafTriggerConfig{
		RequestWindowMillis:    cfg.RequestWindowMillis,
		MinimumRequestRate:     cfg.MinimumRequestRate,
		TrafficSpikeMultiplier: cfg.TrafficSpikeMultiplier,
		ProxyActiveRequests:    cfg.ProxyActiveRequests,
		BackendActiveRequests:  cfg.BackendActiveRequests,
		AgentActiveRequests:    cfg.AgentActiveRequests,
		ServerCpuPercent:       cfg.ServerCPUPercent,
		AgentCpuPercent:        cfg.AgentCPUPercent,
		MinimumActiveMillis:    cfg.MinimumActiveMillis,
		QuietPeriodMillis:      cfg.QuietPeriodMillis,
	}
}

func validatePublicWafCaptchaProviderInput(name string, providerType p2pstreamv1.PublicWafCaptchaProviderType, siteKey string, secretKey string, enabled bool, existing *db.PublicWafCaptchaProvider, secretSet bool) (db.CreatePublicWafCaptchaProviderParams, string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		name = "captcha-provider"
	}
	if !publicNamePattern.MatchString(name) {
		return db.CreatePublicWafCaptchaProviderParams{}, "", connect.NewError(connect.CodeInvalidArgument, errors.New("captcha provider name must be 1-64 alphanumeric, dot, dash, or underscore characters"))
	}
	providerTypeString, err := wafCaptchaProviderStringFromProto(providerType)
	if err != nil {
		return db.CreatePublicWafCaptchaProviderParams{}, "", err
	}
	siteKey = strings.TrimSpace(siteKey)
	if siteKey == "" {
		return db.CreatePublicWafCaptchaProviderParams{}, "", connect.NewError(connect.CodeInvalidArgument, errors.New("captcha site key is required"))
	}
	if existing != nil && !secretSet && secretKey == "" {
		secretKey = existing.SecretKey
	}
	if strings.TrimSpace(secretKey) == "" {
		return db.CreatePublicWafCaptchaProviderParams{}, "", connect.NewError(connect.CodeInvalidArgument, errors.New("captcha secret key is required"))
	}
	return db.CreatePublicWafCaptchaProviderParams{
		Name:         name,
		ProviderType: providerTypeString,
		SiteKey:      siteKey,
		SecretKey:    secretKey,
		Enabled:      boolInt(enabled),
	}, secretKey, nil
}

func (a *App) validatePublicWafRuleInput(
	ctx context.Context,
	name string,
	priority int64,
	enabled bool,
	action p2pstreamv1.PublicWafRuleAction,
	activationMode p2pstreamv1.PublicWafActivationMode,
	keyParts []*p2pstreamv1.PublicRateLimitKeyPart,
	captchaProviderID int64,
	captchaPassTTLMillis int64,
	waitingRoom *p2pstreamv1.PublicWafWaitingRoomConfig,
	triggers *p2pstreamv1.PublicWafTriggerConfig,
	blockStatusCode int64,
	blockBody string,
	blockResponseBodyMode p2pstreamv1.PublicResponseBodyMode,
	blockResponseTemplateID int64,
	captchaPageTemplateID int64,
	waitingRoomPageTemplateID int64,
	blockContentType string,
	blockHeaders []*p2pstreamv1.PublicRateLimitResponseHeader,
	matchRule *p2pstreamv1.PublicPolicyMatchRule,
) (publicWafRuleMutationInput, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		name = defaultWafRuleName
	}
	if !publicNamePattern.MatchString(name) {
		return publicWafRuleMutationInput{}, connect.NewError(connect.CodeInvalidArgument, errors.New("WAF rule name must be 1-64 alphanumeric, dot, dash, or underscore characters"))
	}
	actionString, err := wafRuleActionStringFromProto(action)
	if err != nil {
		return publicWafRuleMutationInput{}, err
	}
	activationModeString, err := wafActivationModeStringFromProto(activationMode)
	if err != nil {
		return publicWafRuleMutationInput{}, err
	}
	matchConfig, err := validatePublicPolicyMatch(matchRule)
	if err != nil {
		return publicWafRuleMutationInput{}, err
	}
	keyPartConfig, err := validateRateLimitKeyParts(keyParts)
	if err != nil {
		return publicWafRuleMutationInput{}, err
	}
	captchaProviderNull := sql.NullInt64{}
	if actionString == publicWafActionCaptcha {
		if captchaProviderID <= 0 {
			return publicWafRuleMutationInput{}, connect.NewError(connect.CodeInvalidArgument, errors.New("captcha WAF rules require a captcha provider"))
		}
		provider, err := a.DB.GetPublicWafCaptchaProvider(ctx, captchaProviderID)
		if err != nil {
			return publicWafRuleMutationInput{}, publicDBError(err)
		}
		if provider.Enabled == 0 {
			return publicWafRuleMutationInput{}, connect.NewError(connect.CodeInvalidArgument, errors.New("captcha WAF rules require an enabled captcha provider"))
		}
		captchaProviderNull = sql.NullInt64{Int64: captchaProviderID, Valid: true}
	}
	if captchaPassTTLMillis == 0 {
		captchaPassTTLMillis = int64(defaultWafCaptchaPassTTL / time.Millisecond)
	}
	if captchaPassTTLMillis < int64(time.Minute/time.Millisecond) || captchaPassTTLMillis > int64((24*time.Hour)/time.Millisecond) {
		return publicWafRuleMutationInput{}, connect.NewError(connect.CodeInvalidArgument, errors.New("captcha pass TTL must be between 1 minute and 24 hours"))
	}
	waitingRoomConfig, err := validatePublicWafWaitingRoom(waitingRoom)
	if err != nil {
		return publicWafRuleMutationInput{}, err
	}
	triggerConfig, err := validatePublicWafTriggers(triggers)
	if err != nil {
		return publicWafRuleMutationInput{}, err
	}
	if blockStatusCode == 0 {
		blockStatusCode = defaultWafBlockStatusCode
	}
	if blockStatusCode < 400 || blockStatusCode > 599 {
		return publicWafRuleMutationInput{}, connect.NewError(connect.CodeInvalidArgument, errors.New("WAF block response status must be between 400 and 599"))
	}
	if blockBody == "" {
		blockBody = defaultWafBlockBody
	}
	if len(blockBody) > maxWafResponseBodyBytes {
		return publicWafRuleMutationInput{}, connect.NewError(connect.CodeInvalidArgument, errors.New("WAF block response body is too large"))
	}
	bodyMode, err := publicResponseBodyModeStringFromProto(blockResponseBodyMode)
	if err != nil {
		return publicWafRuleMutationInput{}, err
	}
	blockTemplateRef := sql.NullInt64{}
	if bodyMode == publicResponseBodyModeTemplate {
		if blockResponseTemplateID <= 0 {
			return publicWafRuleMutationInput{}, connect.NewError(connect.CodeInvalidArgument, errors.New("WAF block response template id is required"))
		}
		blockTemplateRef, err = a.validatePublicResponseTemplateReference(ctx, blockResponseTemplateID, publicResponseTemplateKindGenericBody)
		if err != nil {
			return publicWafRuleMutationInput{}, err
		}
	}
	captchaTemplateRef := sql.NullInt64{}
	if captchaPageTemplateID > 0 {
		if actionString != publicWafActionCaptcha {
			return publicWafRuleMutationInput{}, connect.NewError(connect.CodeInvalidArgument, errors.New("captcha page templates can only be selected for captcha WAF rules"))
		}
		captchaTemplateRef, err = a.validatePublicResponseTemplateReference(ctx, captchaPageTemplateID, publicResponseTemplateKindWafCaptchaPage)
		if err != nil {
			return publicWafRuleMutationInput{}, err
		}
	}
	waitingRoomTemplateRef := sql.NullInt64{}
	if waitingRoomPageTemplateID > 0 {
		if actionString != publicWafActionWaitingRoom {
			return publicWafRuleMutationInput{}, connect.NewError(connect.CodeInvalidArgument, errors.New("waiting-room page templates can only be selected for waiting-room WAF rules"))
		}
		waitingRoomTemplateRef, err = a.validatePublicResponseTemplateReference(ctx, waitingRoomPageTemplateID, publicResponseTemplateKindWafWaitingRoomPage)
		if err != nil {
			return publicWafRuleMutationInput{}, err
		}
	}
	if blockContentType == "" {
		blockContentType = defaultWafBlockContentType
	}
	headerConfig, err := validateRateLimitResponseHeaders(blockHeaders)
	if err != nil {
		return publicWafRuleMutationInput{}, err
	}
	matchJSON, _ := json.Marshal(matchConfig)
	keyPartsJSON, _ := json.Marshal(keyPartConfig)
	headersJSON, _ := json.Marshal(headerConfig)
	return publicWafRuleMutationInput{
		Name:                                 name,
		Priority:                             priority,
		Enabled:                              boolInt(enabled),
		Action:                               actionString,
		ActivationMode:                       activationModeString,
		MatchJSON:                            string(matchJSON),
		KeyPartsJSON:                         string(keyPartsJSON),
		CaptchaProviderID:                    captchaProviderNull,
		CaptchaPassTTLMillis:                 captchaPassTTLMillis,
		WaitingRoomMaxAdmittedSessions:       waitingRoomConfig.MaxAdmittedSessions,
		WaitingRoomAdmissionRatePerSecond:    waitingRoomConfig.AdmissionRatePerSecond,
		WaitingRoomAdmissionSessionTtlMillis: waitingRoomConfig.AdmissionSessionTTLMillis,
		WaitingRoomQueuePollIntervalMillis:   waitingRoomConfig.QueuePollIntervalMillis,
		WaitingRoomQueueTimeoutMillis:        waitingRoomConfig.QueueTimeoutMillis,
		WaitingRoomPageTitle:                 waitingRoomConfig.PageTitle,
		WaitingRoomPageBody:                  waitingRoomConfig.PageBody,
		TriggerRequestWindowMillis:           triggerConfig.RequestWindowMillis,
		TriggerMinimumRequestRate:            triggerConfig.MinimumRequestRate,
		TriggerTrafficSpikeMultiplier:        triggerConfig.TrafficSpikeMultiplier,
		TriggerProxyActiveRequests:           triggerConfig.ProxyActiveRequests,
		TriggerBackendActiveRequests:         triggerConfig.BackendActiveRequests,
		TriggerAgentActiveRequests:           triggerConfig.AgentActiveRequests,
		TriggerServerCpuPercent:              triggerConfig.ServerCPUPercent,
		TriggerAgentCpuPercent:               triggerConfig.AgentCPUPercent,
		TriggerMinimumActiveMillis:           triggerConfig.MinimumActiveMillis,
		TriggerQuietPeriodMillis:             triggerConfig.QuietPeriodMillis,
		BlockResponseStatusCode:              blockStatusCode,
		BlockResponseBody:                    blockBody,
		BlockResponseBodyMode:                bodyMode,
		BlockResponseTemplateID:              blockTemplateRef,
		CaptchaPageTemplateID:                captchaTemplateRef,
		WaitingRoomPageTemplateID:            waitingRoomTemplateRef,
		BlockResponseContentType:             blockContentType,
		BlockResponseHeadersJSON:             string(headersJSON),
	}, nil
}

func validatePublicWafWaitingRoom(cfg *p2pstreamv1.PublicWafWaitingRoomConfig) (publicWafWaitingRoomConfig, error) {
	resp := publicWafWaitingRoomConfig{
		MaxAdmittedSessions:       defaultWafWaitingRoomMaxAdmitted,
		AdmissionRatePerSecond:    defaultWafWaitingRoomAdmissionRate,
		AdmissionSessionTTLMillis: int64(defaultWafWaitingRoomAdmissionTTL / time.Millisecond),
		QueuePollIntervalMillis:   int64(defaultWafWaitingRoomPollInterval / time.Millisecond),
		QueueTimeoutMillis:        int64(defaultWafWaitingRoomQueueTimeout / time.Millisecond),
		PageTitle:                 defaultWafWaitingRoomPageTitle,
		PageBody:                  defaultWafWaitingRoomPageBody,
	}
	if cfg != nil {
		if cfg.MaxAdmittedSessions != 0 {
			resp.MaxAdmittedSessions = cfg.MaxAdmittedSessions
		}
		if cfg.AdmissionRatePerSecond != 0 {
			resp.AdmissionRatePerSecond = cfg.AdmissionRatePerSecond
		}
		if cfg.AdmissionSessionTtlMillis != 0 {
			resp.AdmissionSessionTTLMillis = cfg.AdmissionSessionTtlMillis
		}
		if cfg.QueuePollIntervalMillis != 0 {
			resp.QueuePollIntervalMillis = cfg.QueuePollIntervalMillis
		}
		if cfg.QueueTimeoutMillis != 0 {
			resp.QueueTimeoutMillis = cfg.QueueTimeoutMillis
		}
		if strings.TrimSpace(cfg.PageTitle) != "" {
			resp.PageTitle = strings.TrimSpace(cfg.PageTitle)
		}
		if strings.TrimSpace(cfg.PageBody) != "" {
			resp.PageBody = strings.TrimSpace(cfg.PageBody)
		}
	}
	if resp.MaxAdmittedSessions < 1 || resp.MaxAdmittedSessions > 1_000_000 {
		return publicWafWaitingRoomConfig{}, connect.NewError(connect.CodeInvalidArgument, errors.New("waiting room max admitted sessions must be between 1 and 1000000"))
	}
	if resp.AdmissionRatePerSecond < 1 || resp.AdmissionRatePerSecond > 100_000 {
		return publicWafWaitingRoomConfig{}, connect.NewError(connect.CodeInvalidArgument, errors.New("waiting room admission rate must be between 1 and 100000 per second"))
	}
	if resp.AdmissionSessionTTLMillis < int64(time.Minute/time.Millisecond) || resp.AdmissionSessionTTLMillis > int64((24*time.Hour)/time.Millisecond) {
		return publicWafWaitingRoomConfig{}, connect.NewError(connect.CodeInvalidArgument, errors.New("waiting room admission TTL must be between 1 minute and 24 hours"))
	}
	if resp.QueuePollIntervalMillis < 1000 || resp.QueuePollIntervalMillis > 60000 {
		return publicWafWaitingRoomConfig{}, connect.NewError(connect.CodeInvalidArgument, errors.New("waiting room poll interval must be between 1 and 60 seconds"))
	}
	if resp.QueueTimeoutMillis < int64(time.Minute/time.Millisecond) || resp.QueueTimeoutMillis > int64((24*time.Hour)/time.Millisecond) {
		return publicWafWaitingRoomConfig{}, connect.NewError(connect.CodeInvalidArgument, errors.New("waiting room queue timeout must be between 1 minute and 24 hours"))
	}
	return resp, nil
}

func validatePublicWafTriggers(cfg *p2pstreamv1.PublicWafTriggerConfig) (publicWafTriggerConfig, error) {
	resp := publicWafTriggerConfig{
		RequestWindowMillis:    int64(defaultWafTriggerRequestWindow / time.Millisecond),
		MinimumRequestRate:     defaultWafTriggerMinimumRequestRate,
		TrafficSpikeMultiplier: defaultWafTriggerSpikeMultiplier,
		ProxyActiveRequests:    defaultWafTriggerProxyActiveRequests,
		BackendActiveRequests:  defaultWafTriggerBackendActive,
		AgentActiveRequests:    defaultWafTriggerAgentActive,
		ServerCPUPercent:       defaultWafTriggerServerCPU,
		AgentCPUPercent:        defaultWafTriggerAgentCPU,
		MinimumActiveMillis:    int64(defaultWafTriggerMinimumActive / time.Millisecond),
		QuietPeriodMillis:      int64(defaultWafTriggerQuietPeriod / time.Millisecond),
	}
	if cfg != nil {
		resp.RequestWindowMillis = valueOrDefault(cfg.RequestWindowMillis, resp.RequestWindowMillis)
		resp.MinimumRequestRate = cfg.MinimumRequestRate
		resp.TrafficSpikeMultiplier = cfg.TrafficSpikeMultiplier
		resp.ProxyActiveRequests = cfg.ProxyActiveRequests
		resp.BackendActiveRequests = cfg.BackendActiveRequests
		resp.AgentActiveRequests = cfg.AgentActiveRequests
		resp.ServerCPUPercent = cfg.ServerCpuPercent
		resp.AgentCPUPercent = cfg.AgentCpuPercent
		resp.MinimumActiveMillis = valueOrDefault(cfg.MinimumActiveMillis, resp.MinimumActiveMillis)
		resp.QuietPeriodMillis = valueOrDefault(cfg.QuietPeriodMillis, resp.QuietPeriodMillis)
	}
	if resp.RequestWindowMillis < 1000 || resp.RequestWindowMillis > 300000 {
		return publicWafTriggerConfig{}, connect.NewError(connect.CodeInvalidArgument, errors.New("WAF trigger request window must be between 1 second and 5 minutes"))
	}
	if resp.MinimumRequestRate < 0 || resp.ProxyActiveRequests < 0 || resp.BackendActiveRequests < 0 || resp.AgentActiveRequests < 0 {
		return publicWafTriggerConfig{}, connect.NewError(connect.CodeInvalidArgument, errors.New("WAF trigger thresholds must be non-negative"))
	}
	if resp.TrafficSpikeMultiplier < 0 || resp.TrafficSpikeMultiplier > 100 {
		return publicWafTriggerConfig{}, connect.NewError(connect.CodeInvalidArgument, errors.New("WAF traffic spike multiplier must be between 0 and 100"))
	}
	if resp.ServerCPUPercent < 0 || resp.ServerCPUPercent > 100 || resp.AgentCPUPercent < 0 || resp.AgentCPUPercent > 100 {
		return publicWafTriggerConfig{}, connect.NewError(connect.CodeInvalidArgument, errors.New("WAF CPU triggers must be between 0 and 100 percent"))
	}
	if resp.MinimumActiveMillis < 0 || resp.MinimumActiveMillis > int64((24*time.Hour)/time.Millisecond) {
		return publicWafTriggerConfig{}, connect.NewError(connect.CodeInvalidArgument, errors.New("WAF minimum active duration must be between 0 and 24 hours"))
	}
	if resp.QuietPeriodMillis < 0 || resp.QuietPeriodMillis > int64((24*time.Hour)/time.Millisecond) {
		return publicWafTriggerConfig{}, connect.NewError(connect.CodeInvalidArgument, errors.New("WAF quiet period must be between 0 and 24 hours"))
	}
	return resp, nil
}

func valueOrDefault(value int64, fallback int64) int64 {
	if value == 0 {
		return fallback
	}
	return value
}

func normalizePublicWafCaptchaProvider(value string) string {
	switch strings.TrimSpace(value) {
	case publicWafCaptchaProviderHCaptcha:
		return publicWafCaptchaProviderHCaptcha
	case publicWafCaptchaProviderRecaptcha:
		return publicWafCaptchaProviderRecaptcha
	default:
		return publicWafCaptchaProviderTurnstile
	}
}

func normalizePublicWafAction(value string) string {
	switch strings.TrimSpace(value) {
	case publicWafActionCaptcha:
		return publicWafActionCaptcha
	case publicWafActionWaitingRoom:
		return publicWafActionWaitingRoom
	default:
		return publicWafActionBlock
	}
}

func normalizePublicWafActivationMode(value string) string {
	switch strings.TrimSpace(value) {
	case publicWafActivationAutomatic:
		return publicWafActivationAutomatic
	default:
		return publicWafActivationAlways
	}
}

func wafCaptchaProviderStringFromProto(provider p2pstreamv1.PublicWafCaptchaProviderType) (string, error) {
	switch provider {
	case p2pstreamv1.PublicWafCaptchaProviderType_PUBLIC_WAF_CAPTCHA_PROVIDER_TYPE_UNSPECIFIED,
		p2pstreamv1.PublicWafCaptchaProviderType_PUBLIC_WAF_CAPTCHA_PROVIDER_TYPE_TURNSTILE:
		return publicWafCaptchaProviderTurnstile, nil
	case p2pstreamv1.PublicWafCaptchaProviderType_PUBLIC_WAF_CAPTCHA_PROVIDER_TYPE_HCAPTCHA:
		return publicWafCaptchaProviderHCaptcha, nil
	case p2pstreamv1.PublicWafCaptchaProviderType_PUBLIC_WAF_CAPTCHA_PROVIDER_TYPE_RECAPTCHA_V2:
		return publicWafCaptchaProviderRecaptcha, nil
	default:
		return "", connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("unsupported captcha provider: %s", provider.String()))
	}
}

func protoWafCaptchaProviderFromString(provider string) p2pstreamv1.PublicWafCaptchaProviderType {
	switch normalizePublicWafCaptchaProvider(provider) {
	case publicWafCaptchaProviderHCaptcha:
		return p2pstreamv1.PublicWafCaptchaProviderType_PUBLIC_WAF_CAPTCHA_PROVIDER_TYPE_HCAPTCHA
	case publicWafCaptchaProviderRecaptcha:
		return p2pstreamv1.PublicWafCaptchaProviderType_PUBLIC_WAF_CAPTCHA_PROVIDER_TYPE_RECAPTCHA_V2
	default:
		return p2pstreamv1.PublicWafCaptchaProviderType_PUBLIC_WAF_CAPTCHA_PROVIDER_TYPE_TURNSTILE
	}
}

func wafRuleActionStringFromProto(action p2pstreamv1.PublicWafRuleAction) (string, error) {
	switch action {
	case p2pstreamv1.PublicWafRuleAction_PUBLIC_WAF_RULE_ACTION_UNSPECIFIED,
		p2pstreamv1.PublicWafRuleAction_PUBLIC_WAF_RULE_ACTION_BLOCK:
		return publicWafActionBlock, nil
	case p2pstreamv1.PublicWafRuleAction_PUBLIC_WAF_RULE_ACTION_CAPTCHA:
		return publicWafActionCaptcha, nil
	case p2pstreamv1.PublicWafRuleAction_PUBLIC_WAF_RULE_ACTION_WAITING_ROOM:
		return publicWafActionWaitingRoom, nil
	default:
		return "", connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("unsupported WAF action: %s", action.String()))
	}
}

func protoWafRuleActionFromString(action string) p2pstreamv1.PublicWafRuleAction {
	switch normalizePublicWafAction(action) {
	case publicWafActionCaptcha:
		return p2pstreamv1.PublicWafRuleAction_PUBLIC_WAF_RULE_ACTION_CAPTCHA
	case publicWafActionWaitingRoom:
		return p2pstreamv1.PublicWafRuleAction_PUBLIC_WAF_RULE_ACTION_WAITING_ROOM
	default:
		return p2pstreamv1.PublicWafRuleAction_PUBLIC_WAF_RULE_ACTION_BLOCK
	}
}

func wafActivationModeStringFromProto(mode p2pstreamv1.PublicWafActivationMode) (string, error) {
	switch mode {
	case p2pstreamv1.PublicWafActivationMode_PUBLIC_WAF_ACTIVATION_MODE_UNSPECIFIED,
		p2pstreamv1.PublicWafActivationMode_PUBLIC_WAF_ACTIVATION_MODE_ALWAYS:
		return publicWafActivationAlways, nil
	case p2pstreamv1.PublicWafActivationMode_PUBLIC_WAF_ACTIVATION_MODE_AUTOMATIC:
		return publicWafActivationAutomatic, nil
	default:
		return "", connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("unsupported WAF activation mode: %s", mode.String()))
	}
}

func protoWafActivationModeFromString(mode string) p2pstreamv1.PublicWafActivationMode {
	switch normalizePublicWafActivationMode(mode) {
	case publicWafActivationAutomatic:
		return p2pstreamv1.PublicWafActivationMode_PUBLIC_WAF_ACTIVATION_MODE_AUTOMATIC
	default:
		return p2pstreamv1.PublicWafActivationMode_PUBLIC_WAF_ACTIVATION_MODE_ALWAYS
	}
}

func (a *App) CreatePublicWafCaptchaProvider(ctx context.Context, req *connect.Request[p2pstreamv1.CreatePublicWafCaptchaProviderRequest]) (*connect.Response[p2pstreamv1.CreatePublicWafCaptchaProviderResponse], error) {
	if _, err := a.requireAdmin(ctx, req.Header()); err != nil {
		return nil, err
	}
	params, _, err := validatePublicWafCaptchaProviderInput(req.Msg.Name, req.Msg.ProviderType, req.Msg.SiteKey, req.Msg.SecretKey, req.Msg.Enabled, nil, true)
	if err != nil {
		return nil, err
	}
	row, err := a.DB.CreatePublicWafCaptchaProvider(ctx, params)
	if err != nil {
		return nil, publicDBError(err)
	}
	if err := a.refreshPublicProxySnapshot(ctx); err != nil {
		return nil, err
	}
	return connect.NewResponse(&p2pstreamv1.CreatePublicWafCaptchaProviderResponse{Provider: publicWafCaptchaProviderConfigToProto(publicWafCaptchaProviderRowToConfig(row, false), row.SecretKey != "")}), nil
}

func (a *App) UpdatePublicWafCaptchaProvider(ctx context.Context, req *connect.Request[p2pstreamv1.UpdatePublicWafCaptchaProviderRequest]) (*connect.Response[p2pstreamv1.UpdatePublicWafCaptchaProviderResponse], error) {
	if _, err := a.requireAdmin(ctx, req.Header()); err != nil {
		return nil, err
	}
	existing, err := a.DB.GetPublicWafCaptchaProvider(ctx, req.Msg.Id)
	if err != nil {
		return nil, publicDBError(err)
	}
	params, _, err := validatePublicWafCaptchaProviderInput(req.Msg.Name, req.Msg.ProviderType, req.Msg.SiteKey, req.Msg.SecretKey, req.Msg.Enabled, &existing, req.Msg.SecretKeySet || req.Msg.SecretKey != "")
	if err != nil {
		return nil, err
	}
	row, err := a.DB.UpdatePublicWafCaptchaProvider(ctx, db.UpdatePublicWafCaptchaProviderParams{
		ID:           req.Msg.Id,
		Name:         params.Name,
		ProviderType: params.ProviderType,
		SiteKey:      params.SiteKey,
		SecretKey:    params.SecretKey,
		Enabled:      params.Enabled,
	})
	if err != nil {
		return nil, publicDBError(err)
	}
	if err := a.refreshPublicProxySnapshot(ctx); err != nil {
		return nil, err
	}
	return connect.NewResponse(&p2pstreamv1.UpdatePublicWafCaptchaProviderResponse{Provider: publicWafCaptchaProviderConfigToProto(publicWafCaptchaProviderRowToConfig(row, false), row.SecretKey != "")}), nil
}

func (a *App) DeletePublicWafCaptchaProvider(ctx context.Context, req *connect.Request[p2pstreamv1.DeletePublicWafCaptchaProviderRequest]) (*connect.Response[p2pstreamv1.DeletePublicWafCaptchaProviderResponse], error) {
	if _, err := a.requireAdmin(ctx, req.Header()); err != nil {
		return nil, err
	}
	if err := a.DB.DeletePublicWafCaptchaProvider(ctx, req.Msg.Id); err != nil {
		return nil, publicDBError(err)
	}
	if err := a.refreshPublicProxySnapshot(ctx); err != nil {
		return nil, err
	}
	return connect.NewResponse(&p2pstreamv1.DeletePublicWafCaptchaProviderResponse{}), nil
}

func (a *App) CreatePublicWafRule(ctx context.Context, req *connect.Request[p2pstreamv1.CreatePublicWafRuleRequest]) (*connect.Response[p2pstreamv1.CreatePublicWafRuleResponse], error) {
	if _, err := a.requireAdmin(ctx, req.Header()); err != nil {
		return nil, err
	}
	if err := rejectRemovedPolicyMatchField(req.Msg, 6); err != nil {
		return nil, err
	}
	params, err := a.validatePublicWafRuleInput(
		ctx,
		req.Msg.Name,
		req.Msg.Priority,
		req.Msg.Enabled,
		req.Msg.Action,
		req.Msg.ActivationMode,
		req.Msg.KeyParts,
		req.Msg.CaptchaProviderId,
		req.Msg.CaptchaPassTtlMillis,
		req.Msg.WaitingRoom,
		req.Msg.Triggers,
		req.Msg.BlockResponseStatusCode,
		req.Msg.BlockResponseBody,
		req.Msg.BlockResponseBodyMode,
		req.Msg.BlockResponseTemplateId,
		req.Msg.CaptchaPageTemplateId,
		req.Msg.WaitingRoomPageTemplateId,
		req.Msg.BlockResponseContentType,
		req.Msg.BlockResponseHeaders,
		req.Msg.MatchRule,
	)
	if err != nil {
		return nil, err
	}
	row, err := a.DB.CreatePublicWafRule(ctx, wafCreateParams(params))
	if err != nil {
		return nil, publicDBError(err)
	}
	if err := a.refreshPublicProxySnapshot(ctx); err != nil {
		return nil, err
	}
	rule, err := publicWafRuleRowToConfig(row)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	return connect.NewResponse(&p2pstreamv1.CreatePublicWafRuleResponse{Rule: publicWafRuleConfigToProto(rule)}), nil
}

func (a *App) UpdatePublicWafRule(ctx context.Context, req *connect.Request[p2pstreamv1.UpdatePublicWafRuleRequest]) (*connect.Response[p2pstreamv1.UpdatePublicWafRuleResponse], error) {
	if _, err := a.requireAdmin(ctx, req.Header()); err != nil {
		return nil, err
	}
	if err := rejectRemovedPolicyMatchField(req.Msg, 7); err != nil {
		return nil, err
	}
	params, err := a.validatePublicWafRuleInput(
		ctx,
		req.Msg.Name,
		req.Msg.Priority,
		req.Msg.Enabled,
		req.Msg.Action,
		req.Msg.ActivationMode,
		req.Msg.KeyParts,
		req.Msg.CaptchaProviderId,
		req.Msg.CaptchaPassTtlMillis,
		req.Msg.WaitingRoom,
		req.Msg.Triggers,
		req.Msg.BlockResponseStatusCode,
		req.Msg.BlockResponseBody,
		req.Msg.BlockResponseBodyMode,
		req.Msg.BlockResponseTemplateId,
		req.Msg.CaptchaPageTemplateId,
		req.Msg.WaitingRoomPageTemplateId,
		req.Msg.BlockResponseContentType,
		req.Msg.BlockResponseHeaders,
		req.Msg.MatchRule,
	)
	if err != nil {
		return nil, err
	}
	row, err := a.DB.UpdatePublicWafRule(ctx, wafUpdateParams(req.Msg.Id, params))
	if err != nil {
		return nil, publicDBError(err)
	}
	if err := a.refreshPublicProxySnapshot(ctx); err != nil {
		return nil, err
	}
	rule, err := publicWafRuleRowToConfig(row)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	return connect.NewResponse(&p2pstreamv1.UpdatePublicWafRuleResponse{Rule: publicWafRuleConfigToProto(rule)}), nil
}

func (a *App) DeletePublicWafRule(ctx context.Context, req *connect.Request[p2pstreamv1.DeletePublicWafRuleRequest]) (*connect.Response[p2pstreamv1.DeletePublicWafRuleResponse], error) {
	if _, err := a.requireAdmin(ctx, req.Header()); err != nil {
		return nil, err
	}
	if err := a.DB.DeletePublicWafRule(ctx, req.Msg.Id); err != nil {
		return nil, publicDBError(err)
	}
	if err := a.refreshPublicProxySnapshot(ctx); err != nil {
		return nil, err
	}
	return connect.NewResponse(&p2pstreamv1.DeletePublicWafRuleResponse{}), nil
}

func wafCreateParams(input publicWafRuleMutationInput) db.CreatePublicWafRuleParams {
	return db.CreatePublicWafRuleParams{
		Name:                                 input.Name,
		Priority:                             input.Priority,
		Enabled:                              input.Enabled,
		Action:                               input.Action,
		ActivationMode:                       input.ActivationMode,
		MatchJson:                            input.MatchJSON,
		KeyPartsJson:                         input.KeyPartsJSON,
		CaptchaProviderID:                    input.CaptchaProviderID,
		CaptchaPassTtlMillis:                 input.CaptchaPassTTLMillis,
		WaitingRoomMaxAdmittedSessions:       input.WaitingRoomMaxAdmittedSessions,
		WaitingRoomAdmissionRatePerSecond:    input.WaitingRoomAdmissionRatePerSecond,
		WaitingRoomAdmissionSessionTtlMillis: input.WaitingRoomAdmissionSessionTtlMillis,
		WaitingRoomQueuePollIntervalMillis:   input.WaitingRoomQueuePollIntervalMillis,
		WaitingRoomQueueTimeoutMillis:        input.WaitingRoomQueueTimeoutMillis,
		WaitingRoomPageTitle:                 input.WaitingRoomPageTitle,
		WaitingRoomPageBody:                  input.WaitingRoomPageBody,
		TriggerRequestWindowMillis:           input.TriggerRequestWindowMillis,
		TriggerMinimumRequestRate:            input.TriggerMinimumRequestRate,
		TriggerTrafficSpikeMultiplier:        input.TriggerTrafficSpikeMultiplier,
		TriggerProxyActiveRequests:           input.TriggerProxyActiveRequests,
		TriggerBackendActiveRequests:         input.TriggerBackendActiveRequests,
		TriggerAgentActiveRequests:           input.TriggerAgentActiveRequests,
		TriggerServerCpuPercent:              input.TriggerServerCpuPercent,
		TriggerAgentCpuPercent:               input.TriggerAgentCpuPercent,
		TriggerMinimumActiveMillis:           input.TriggerMinimumActiveMillis,
		TriggerQuietPeriodMillis:             input.TriggerQuietPeriodMillis,
		BlockResponseStatusCode:              input.BlockResponseStatusCode,
		BlockResponseBody:                    input.BlockResponseBody,
		BlockResponseBodyMode:                input.BlockResponseBodyMode,
		BlockResponseTemplateID:              input.BlockResponseTemplateID,
		CaptchaPageTemplateID:                input.CaptchaPageTemplateID,
		WaitingRoomPageTemplateID:            input.WaitingRoomPageTemplateID,
		BlockResponseContentType:             input.BlockResponseContentType,
		BlockResponseHeadersJson:             input.BlockResponseHeadersJSON,
	}
}

func wafUpdateParams(id int64, input publicWafRuleMutationInput) db.UpdatePublicWafRuleParams {
	return db.UpdatePublicWafRuleParams{
		ID:                                   id,
		Name:                                 input.Name,
		Priority:                             input.Priority,
		Enabled:                              input.Enabled,
		Action:                               input.Action,
		ActivationMode:                       input.ActivationMode,
		MatchJson:                            input.MatchJSON,
		KeyPartsJson:                         input.KeyPartsJSON,
		CaptchaProviderID:                    input.CaptchaProviderID,
		CaptchaPassTtlMillis:                 input.CaptchaPassTTLMillis,
		WaitingRoomMaxAdmittedSessions:       input.WaitingRoomMaxAdmittedSessions,
		WaitingRoomAdmissionRatePerSecond:    input.WaitingRoomAdmissionRatePerSecond,
		WaitingRoomAdmissionSessionTtlMillis: input.WaitingRoomAdmissionSessionTtlMillis,
		WaitingRoomQueuePollIntervalMillis:   input.WaitingRoomQueuePollIntervalMillis,
		WaitingRoomQueueTimeoutMillis:        input.WaitingRoomQueueTimeoutMillis,
		WaitingRoomPageTitle:                 input.WaitingRoomPageTitle,
		WaitingRoomPageBody:                  input.WaitingRoomPageBody,
		TriggerRequestWindowMillis:           input.TriggerRequestWindowMillis,
		TriggerMinimumRequestRate:            input.TriggerMinimumRequestRate,
		TriggerTrafficSpikeMultiplier:        input.TriggerTrafficSpikeMultiplier,
		TriggerProxyActiveRequests:           input.TriggerProxyActiveRequests,
		TriggerBackendActiveRequests:         input.TriggerBackendActiveRequests,
		TriggerAgentActiveRequests:           input.TriggerAgentActiveRequests,
		TriggerServerCpuPercent:              input.TriggerServerCpuPercent,
		TriggerAgentCpuPercent:               input.TriggerAgentCpuPercent,
		TriggerMinimumActiveMillis:           input.TriggerMinimumActiveMillis,
		TriggerQuietPeriodMillis:             input.TriggerQuietPeriodMillis,
		BlockResponseStatusCode:              input.BlockResponseStatusCode,
		BlockResponseBody:                    input.BlockResponseBody,
		BlockResponseBodyMode:                input.BlockResponseBodyMode,
		BlockResponseTemplateID:              input.BlockResponseTemplateID,
		CaptchaPageTemplateID:                input.CaptchaPageTemplateID,
		WaitingRoomPageTemplateID:            input.WaitingRoomPageTemplateID,
		BlockResponseContentType:             input.BlockResponseContentType,
		BlockResponseHeadersJson:             input.BlockResponseHeadersJSON,
	}
}

func traceResolutionFromWafDecision(decision publicWafDecision, listenerID int64) publicRouteResolution {
	return publicRouteResolution{
		Listener:           decision.Listener,
		ListenerID:         sql.NullInt64{Int64: listenerID, Valid: true},
		WafRuleID:          decision.Rule.ID,
		WafRuleName:        decision.Rule.Name,
		WafAction:          decision.Action,
		WafActivationMode:  decision.Rule.ActivationMode,
		WafAutomaticActive: decision.AutomaticActive,
		WafChallengeKind:   decision.ChallengeKind,
	}
}

func wafDebugAttributes(decision publicWafDecision) map[string]string {
	return map[string]string{
		"handler":              "waf",
		"waf_rule_id":          strconv.FormatInt(decision.Rule.ID, 10),
		"waf_rule_name":        decision.Rule.Name,
		"waf_action":           decision.Action,
		"waf_activation_mode":  decision.Rule.ActivationMode,
		"waf_automatic_active": strconv.FormatBool(decision.AutomaticActive),
		"waf_challenge_kind":   decision.ChallengeKind,
	}
}

func wafTraceStage(decision publicWafDecision) p2pstreamv1.TrafficTraceStage {
	if decision.Action == publicWafActionCaptcha {
		if decision.StatusCode == http.StatusSeeOther && decision.ErrorKind == "" {
			return p2pstreamv1.TrafficTraceStage_TRAFFIC_TRACE_STAGE_WAF_CAPTCHA_VERIFIED
		}
		return p2pstreamv1.TrafficTraceStage_TRAFFIC_TRACE_STAGE_WAF_CAPTCHA_CHALLENGED
	}
	if decision.Action == publicWafActionWaitingRoom {
		return p2pstreamv1.TrafficTraceStage_TRAFFIC_TRACE_STAGE_WAF_WAITING_ROOM
	}
	if decision.Action == publicWafActionBlock {
		return p2pstreamv1.TrafficTraceStage_TRAFFIC_TRACE_STAGE_WAF_BLOCKED
	}
	return p2pstreamv1.TrafficTraceStage_TRAFFIC_TRACE_STAGE_WAF_EVALUATED
}
