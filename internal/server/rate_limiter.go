package server

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net"
	"net/http"
	"net/textproto"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"connectrpc.com/connect"

	p2pstreamv1 "p2pstream/gen/proto/p2pstream/v1"
	"p2pstream/internal/db"
)

const (
	defaultRateLimitName                 = "rate-limit"
	defaultRateLimitLimit                = int64(60)
	defaultRateLimitWindowMillis         = int64(60_000)
	defaultRateLimitStatusCode           = http.StatusTooManyRequests
	defaultRateLimitBody                 = "Rate limit exceeded\n"
	defaultRateLimitContentType          = "text/plain; charset=utf-8"
	maxRateLimitWindowMillis             = int64(86_400_000)
	maxRateLimitKeyParts                 = 8
	maxRateLimitMatchers                 = 32
	maxRateLimitKeyValueBytes            = 256
	maxRateLimitResponseBodyBytes        = 64 * 1024
	maxRateLimitResponseHeaders          = 32
	maxRateLimitKeysPerRule              = 10000
	rateLimitIdleStateTTL                = 15 * time.Minute
	rateLimitPruneInterval               = time.Minute
	rateLimitMissingValue                = "<missing>"
	publicRateLimitKeySourceRemoteIP     = "remote_ip"
	publicRateLimitKeySourceHost         = "host"
	publicRateLimitKeySourceMethod       = "method"
	publicRateLimitKeySourcePath         = "path"
	publicRateLimitKeySourceProtocol     = "protocol"
	publicRateLimitKeySourceHeader       = "header"
	publicRateLimitKeySourceCookie       = "cookie"
	publicRateLimitKeySourceQueryParam   = "query_param"
	publicRateLimitMatchOperatorPresent  = "present"
	publicRateLimitMatchOperatorEquals   = "equals"
	publicRateLimitMatchOperatorPrefix   = "prefix"
	publicRateLimitMatchOperatorSuffix   = "suffix"
	publicRateLimitMatchOperatorContains = "contains"
)

type publicRateLimitRuleConfig struct {
	ID                  int64
	Name                string
	Priority            int64
	Enabled             bool
	Algorithm           string
	Limit               int64
	WindowMillis        int64
	Burst               int64
	Match               publicRateLimitMatchConfig
	KeyParts            []publicRateLimitKeyPartConfig
	ResponseStatusCode  int
	ResponseBody        string
	ResponseContentType string
	ResponseHeaders     []publicRateLimitResponseHeaderConfig
	CreatedAt           time.Time
	UpdatedAt           time.Time
	Fingerprint         string
}

type publicRateLimitMatchConfig struct {
	Methods      []string                            `json:"methods,omitempty"`
	Protocols    []string                            `json:"protocols,omitempty"`
	HostPatterns []string                            `json:"host_patterns,omitempty"`
	PathPrefixes []string                            `json:"path_prefixes,omitempty"`
	PathSuffixes []string                            `json:"path_suffixes,omitempty"`
	Headers      []publicRateLimitValueMatcherConfig `json:"headers,omitempty"`
	Cookies      []publicRateLimitValueMatcherConfig `json:"cookies,omitempty"`
	QueryParams  []publicRateLimitValueMatcherConfig `json:"query_params,omitempty"`
}

type publicRateLimitValueMatcherConfig struct {
	Name     string `json:"name"`
	Operator string `json:"operator"`
	Value    string `json:"value,omitempty"`
}

type publicRateLimitKeyPartConfig struct {
	Source string `json:"source"`
	Name   string `json:"name,omitempty"`
}

type publicRateLimitResponseHeaderConfig struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

type publicRateLimitRuleMutationInput struct {
	Name                string
	Priority            int64
	Enabled             int64
	Algorithm           string
	LimitCount          int64
	WindowMillis        int64
	Burst               int64
	MatchJSON           string
	KeyPartsJSON        string
	ResponseStatusCode  int64
	ResponseBody        string
	ResponseContentType string
	ResponseHeadersJSON string
}

type publicRateLimiter struct {
	mu        sync.Mutex
	rules     map[int64]*publicRateLimitRuleRuntime
	lastPrune time.Time
}

type publicRateLimitRuleRuntime struct {
	fingerprint string
	keys        map[string]*publicRateLimitKeyRuntime
}

type publicRateLimitKeyRuntime struct {
	fixedWindowStart int64
	fixedCount       int64
	slidingHits      []int64
	tokens           float64
	tokenLastRefill  int64
	tokenInitialized bool
	leakyLevel       float64
	leakyLastDrain   int64
	leakyInitialized bool
	lastSeenAt       time.Time
}

type publicRateLimitDecision struct {
	Rule       publicRateLimitRuleConfig
	Listener   publicListenerConfig
	StatusCode int
	Body       string
	Headers    http.Header
	RetryAfter time.Duration
	Limit      int64
	Remaining  int64
	ResetAt    time.Time
}

func newPublicRateLimiter() *publicRateLimiter {
	return &publicRateLimiter{rules: make(map[int64]*publicRateLimitRuleRuntime)}
}

func (l *publicRateLimiter) reconcile(snap *publicProxySnapshot) {
	if l == nil {
		return
	}
	keep := make(map[int64]string)
	if snap != nil {
		for _, rule := range snap.RateLimitRules {
			if rule.Enabled {
				keep[rule.ID] = rule.Fingerprint
			}
		}
	}

	l.mu.Lock()
	defer l.mu.Unlock()
	for id, runtime := range l.rules {
		if fingerprint, ok := keep[id]; !ok || runtime.fingerprint != fingerprint {
			delete(l.rules, id)
		}
	}
	for id, fingerprint := range keep {
		if _, ok := l.rules[id]; !ok {
			l.rules[id] = &publicRateLimitRuleRuntime{fingerprint: fingerprint, keys: make(map[string]*publicRateLimitKeyRuntime)}
		}
	}
}

func (a *App) checkPublicRateLimits(listenerID int64, r *http.Request) (publicRateLimitDecision, bool) {
	if a == nil || a.RateLimiter == nil {
		return publicRateLimitDecision{}, true
	}
	a.proxyMu.Lock()
	snap := a.publicSnapshot
	a.proxyMu.Unlock()
	if snap == nil || len(snap.RateLimitRules) == 0 {
		return publicRateLimitDecision{}, true
	}
	listener, ok := snap.Listeners[listenerID]
	if !ok {
		return publicRateLimitDecision{}, true
	}
	return a.RateLimiter.evaluate(snap.RateLimitRules, listener, r, time.Now())
}

func (l *publicRateLimiter) evaluate(rules []publicRateLimitRuleConfig, listener publicListenerConfig, r *http.Request, now time.Time) (publicRateLimitDecision, bool) {
	if len(rules) == 0 {
		return publicRateLimitDecision{}, true
	}
	ordered := append([]publicRateLimitRuleConfig(nil), rules...)
	sort.SliceStable(ordered, func(i, j int) bool {
		if ordered[i].Priority == ordered[j].Priority {
			return ordered[i].ID < ordered[j].ID
		}
		return ordered[i].Priority < ordered[j].Priority
	})

	l.mu.Lock()
	defer l.mu.Unlock()
	l.pruneLocked(now)
	for _, rule := range ordered {
		if !rule.Enabled || !rule.matches(listener, r) {
			continue
		}
		runtime := l.rules[rule.ID]
		if runtime == nil || runtime.fingerprint != rule.Fingerprint {
			runtime = &publicRateLimitRuleRuntime{fingerprint: rule.Fingerprint, keys: make(map[string]*publicRateLimitKeyRuntime)}
			l.rules[rule.ID] = runtime
		}
		key := rateLimitKeyHash(rule.keyValues(listener, r))
		keyState := runtime.keys[key]
		if keyState == nil {
			if len(runtime.keys) >= maxRateLimitKeysPerRule {
				evictOldestRateLimitKey(runtime.keys)
			}
			keyState = &publicRateLimitKeyRuntime{}
			runtime.keys[key] = keyState
		}
		keyState.lastSeenAt = now
		result := keyState.allow(rule, now)
		if !result.allowed {
			return publicRateLimitDecision{
				Rule:       rule,
				Listener:   listener,
				StatusCode: rule.ResponseStatusCode,
				Body:       rule.ResponseBody,
				Headers:    rateLimitGeneratedHeaders(rule, result, now),
				RetryAfter: result.retryAfter,
				Limit:      rule.Limit,
				Remaining:  result.remaining,
				ResetAt:    now.Add(result.retryAfter),
			}, false
		}
	}
	return publicRateLimitDecision{}, true
}

func (l *publicRateLimiter) pruneLocked(now time.Time) {
	if !l.lastPrune.IsZero() && now.Sub(l.lastPrune) < rateLimitPruneInterval {
		return
	}
	l.lastPrune = now
	for _, runtime := range l.rules {
		for key, keyState := range runtime.keys {
			if keyState.lastSeenAt.IsZero() || now.Sub(keyState.lastSeenAt) > rateLimitIdleStateTTL {
				delete(runtime.keys, key)
			}
		}
	}
}

func evictOldestRateLimitKey(keys map[string]*publicRateLimitKeyRuntime) {
	var oldestKey string
	var oldestTime time.Time
	for key, keyState := range keys {
		if keyState == nil {
			delete(keys, key)
			return
		}
		if oldestKey == "" || keyState.lastSeenAt.Before(oldestTime) {
			oldestKey = key
			oldestTime = keyState.lastSeenAt
		}
	}
	if oldestKey != "" {
		delete(keys, oldestKey)
	}
}

type publicRateLimitAllowResult struct {
	allowed    bool
	remaining  int64
	retryAfter time.Duration
}

func (s *publicRateLimitKeyRuntime) allow(rule publicRateLimitRuleConfig, now time.Time) publicRateLimitAllowResult {
	nowMs := now.UnixMilli()
	switch rule.Algorithm {
	case publicRateLimitAlgorithmSlidingWindow:
		return s.allowSlidingWindow(rule, nowMs)
	case publicRateLimitAlgorithmTokenBucket:
		return s.allowTokenBucket(rule, nowMs)
	case publicRateLimitAlgorithmLeakyBucket:
		return s.allowLeakyBucket(rule, nowMs)
	default:
		return s.allowFixedWindow(rule, nowMs)
	}
}

func (s *publicRateLimitKeyRuntime) allowFixedWindow(rule publicRateLimitRuleConfig, nowMs int64) publicRateLimitAllowResult {
	window := rule.WindowMillis
	windowStart := (nowMs / window) * window
	if s.fixedWindowStart != windowStart {
		s.fixedWindowStart = windowStart
		s.fixedCount = 0
	}
	if s.fixedCount >= rule.Limit {
		return publicRateLimitAllowResult{remaining: 0, retryAfter: millisDuration(windowStart + window - nowMs)}
	}
	s.fixedCount++
	return publicRateLimitAllowResult{allowed: true, remaining: maxInt64(0, rule.Limit-s.fixedCount), retryAfter: millisDuration(windowStart + window - nowMs)}
}

func (s *publicRateLimitKeyRuntime) allowSlidingWindow(rule publicRateLimitRuleConfig, nowMs int64) publicRateLimitAllowResult {
	cutoff := nowMs - rule.WindowMillis
	keepAt := 0
	for keepAt < len(s.slidingHits) && s.slidingHits[keepAt] <= cutoff {
		keepAt++
	}
	if keepAt > 0 {
		copy(s.slidingHits, s.slidingHits[keepAt:])
		s.slidingHits = s.slidingHits[:len(s.slidingHits)-keepAt]
	}
	if int64(len(s.slidingHits)) >= rule.Limit {
		retryMs := s.slidingHits[0] + rule.WindowMillis - nowMs
		return publicRateLimitAllowResult{remaining: 0, retryAfter: millisDuration(retryMs)}
	}
	s.slidingHits = append(s.slidingHits, nowMs)
	return publicRateLimitAllowResult{allowed: true, remaining: maxInt64(0, rule.Limit-int64(len(s.slidingHits))), retryAfter: millisDuration(rule.WindowMillis)}
}

func (s *publicRateLimitKeyRuntime) allowTokenBucket(rule publicRateLimitRuleConfig, nowMs int64) publicRateLimitAllowResult {
	capacity := effectiveRateLimitBurst(rule)
	if !s.tokenInitialized {
		s.tokens = float64(capacity)
		s.tokenLastRefill = nowMs
		s.tokenInitialized = true
	}
	elapsed := maxInt64(0, nowMs-s.tokenLastRefill)
	if elapsed > 0 {
		s.tokens = math.Min(float64(capacity), s.tokens+float64(elapsed)*float64(rule.Limit)/float64(rule.WindowMillis))
		s.tokenLastRefill = nowMs
	}
	if s.tokens < 1 {
		needed := 1 - s.tokens
		retryMs := int64(math.Ceil(needed * float64(rule.WindowMillis) / float64(rule.Limit)))
		return publicRateLimitAllowResult{remaining: 0, retryAfter: millisDuration(retryMs)}
	}
	s.tokens--
	return publicRateLimitAllowResult{allowed: true, remaining: int64(math.Floor(s.tokens)), retryAfter: millisDuration(rule.WindowMillis)}
}

func (s *publicRateLimitKeyRuntime) allowLeakyBucket(rule publicRateLimitRuleConfig, nowMs int64) publicRateLimitAllowResult {
	capacity := effectiveRateLimitBurst(rule)
	if !s.leakyInitialized {
		s.leakyLastDrain = nowMs
		s.leakyInitialized = true
	}
	elapsed := maxInt64(0, nowMs-s.leakyLastDrain)
	if elapsed > 0 {
		s.leakyLevel = math.Max(0, s.leakyLevel-float64(elapsed)*float64(rule.Limit)/float64(rule.WindowMillis))
		s.leakyLastDrain = nowMs
	}
	if s.leakyLevel+1 > float64(capacity) {
		retryMs := int64(math.Ceil((s.leakyLevel + 1 - float64(capacity)) * float64(rule.WindowMillis) / float64(rule.Limit)))
		return publicRateLimitAllowResult{remaining: 0, retryAfter: millisDuration(retryMs)}
	}
	s.leakyLevel++
	return publicRateLimitAllowResult{allowed: true, remaining: maxInt64(0, capacity-int64(math.Ceil(s.leakyLevel))), retryAfter: millisDuration(rule.WindowMillis)}
}

func (rule publicRateLimitRuleConfig) matches(listener publicListenerConfig, r *http.Request) bool {
	if len(rule.Match.Methods) > 0 && !stringInSlice(strings.ToUpper(r.Method), rule.Match.Methods) {
		return false
	}
	if len(rule.Match.Protocols) > 0 && !stringInSlice(listener.Protocol, rule.Match.Protocols) {
		return false
	}
	host := normalizeRequestHost(r.Host)
	if len(rule.Match.HostPatterns) > 0 {
		matched := false
		for _, pattern := range rule.Match.HostPatterns {
			if hostMatchesPattern(host, pattern) {
				matched = true
				break
			}
		}
		if !matched {
			return false
		}
	}
	if len(rule.Match.PathPrefixes) > 0 {
		matched := false
		for _, prefix := range rule.Match.PathPrefixes {
			if pathPrefixMatches(r.URL.Path, prefix) {
				matched = true
				break
			}
		}
		if !matched {
			return false
		}
	}
	if len(rule.Match.PathSuffixes) > 0 {
		matched := false
		for _, suffix := range rule.Match.PathSuffixes {
			if strings.HasSuffix(r.URL.Path, suffix) {
				matched = true
				break
			}
		}
		if !matched {
			return false
		}
	}
	for _, matcher := range rule.Match.Headers {
		_, present := r.Header[textproto.CanonicalMIMEHeaderKey(matcher.Name)]
		if !rateLimitValueMatches(r.Header.Get(matcher.Name), present, matcher) {
			return false
		}
	}
	for _, matcher := range rule.Match.Cookies {
		cookie, err := r.Cookie(matcher.Name)
		value := ""
		present := err == nil
		if err == nil {
			value = cookie.Value
		}
		if !rateLimitValueMatches(value, present, matcher) {
			return false
		}
	}
	query := r.URL.Query()
	for _, matcher := range rule.Match.QueryParams {
		values, present := query[matcher.Name]
		value := ""
		if len(values) > 0 {
			value = values[0]
		}
		if !rateLimitValueMatches(value, present, matcher) {
			return false
		}
	}
	return true
}

func rateLimitValueMatches(actual string, present bool, matcher publicRateLimitValueMatcherConfig) bool {
	switch matcher.Operator {
	case publicRateLimitMatchOperatorPresent:
		return present
	case publicRateLimitMatchOperatorEquals:
		return actual == matcher.Value
	case publicRateLimitMatchOperatorPrefix:
		return strings.HasPrefix(actual, matcher.Value)
	case publicRateLimitMatchOperatorSuffix:
		return strings.HasSuffix(actual, matcher.Value)
	case publicRateLimitMatchOperatorContains:
		return strings.Contains(actual, matcher.Value)
	default:
		return false
	}
}

func (rule publicRateLimitRuleConfig) keyValues(listener publicListenerConfig, r *http.Request) []string {
	parts := rule.KeyParts
	if len(parts) == 0 {
		parts = []publicRateLimitKeyPartConfig{{Source: publicRateLimitKeySourceRemoteIP}}
	}
	values := make([]string, 0, len(parts)*2)
	for _, part := range parts {
		value := rateLimitMissingValue
		switch part.Source {
		case publicRateLimitKeySourceRemoteIP:
			value = remoteIPForRateLimit(r)
		case publicRateLimitKeySourceHost:
			value = normalizeRequestHost(r.Host)
		case publicRateLimitKeySourceMethod:
			value = strings.ToUpper(r.Method)
		case publicRateLimitKeySourcePath:
			value = r.URL.Path
		case publicRateLimitKeySourceProtocol:
			value = listener.Protocol
		case publicRateLimitKeySourceHeader:
			value = r.Header.Get(part.Name)
		case publicRateLimitKeySourceCookie:
			if cookie, err := r.Cookie(part.Name); err == nil {
				value = cookie.Value
			}
		case publicRateLimitKeySourceQueryParam:
			value = r.URL.Query().Get(part.Name)
		}
		if value == "" {
			value = rateLimitMissingValue
		}
		if len(value) > maxRateLimitKeyValueBytes {
			value = value[:maxRateLimitKeyValueBytes]
		}
		values = append(values, part.Source+":"+strings.ToLower(part.Name), value)
	}
	return values
}

func remoteIPForRateLimit(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err == nil && host != "" {
		return host
	}
	if r.RemoteAddr != "" {
		return r.RemoteAddr
	}
	return rateLimitMissingValue
}

func rateLimitKeyHash(values []string) string {
	h := sha256.New()
	for _, value := range values {
		h.Write([]byte(value))
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))
}

func rateLimitGeneratedHeaders(rule publicRateLimitRuleConfig, result publicRateLimitAllowResult, now time.Time) http.Header {
	retryAfter := int(math.Ceil(result.retryAfter.Seconds()))
	if retryAfter < 1 {
		retryAfter = 1
	}
	resetUnix := now.Add(time.Duration(retryAfter) * time.Second).Unix()
	headers := make(http.Header)
	headers.Set("Retry-After", strconv.Itoa(retryAfter))
	headers.Set("RateLimit-Limit", strconv.FormatInt(rule.Limit, 10))
	headers.Set("RateLimit-Remaining", strconv.FormatInt(maxInt64(0, result.remaining), 10))
	headers.Set("RateLimit-Reset", strconv.Itoa(retryAfter))
	headers.Set("X-RateLimit-Limit", strconv.FormatInt(rule.Limit, 10))
	headers.Set("X-RateLimit-Remaining", strconv.FormatInt(maxInt64(0, result.remaining), 10))
	headers.Set("X-RateLimit-Reset", strconv.FormatInt(resetUnix, 10))
	return headers
}

func writeRateLimitResponse(w http.ResponseWriter, decision publicRateLimitDecision) {
	for _, header := range decision.Rule.ResponseHeaders {
		w.Header().Set(header.Name, header.Value)
	}
	if decision.Rule.ResponseContentType != "" {
		w.Header().Set("Content-Type", decision.Rule.ResponseContentType)
	}
	for name, values := range decision.Headers {
		w.Header().Del(name)
		for _, value := range values {
			w.Header().Add(name, value)
		}
	}
	w.WriteHeader(decision.StatusCode)
	_, _ = w.Write([]byte(decision.Body))
}

func publicRateLimitRuleRowToConfig(row db.PublicRateLimitRule) (publicRateLimitRuleConfig, error) {
	var match publicRateLimitMatchConfig
	if strings.TrimSpace(row.MatchJson) != "" {
		if err := json.Unmarshal([]byte(row.MatchJson), &match); err != nil {
			return publicRateLimitRuleConfig{}, err
		}
	}
	var keyParts []publicRateLimitKeyPartConfig
	if strings.TrimSpace(row.KeyPartsJson) != "" {
		if err := json.Unmarshal([]byte(row.KeyPartsJson), &keyParts); err != nil {
			return publicRateLimitRuleConfig{}, err
		}
	}
	var responseHeaders []publicRateLimitResponseHeaderConfig
	if strings.TrimSpace(row.ResponseHeadersJson) != "" {
		if err := json.Unmarshal([]byte(row.ResponseHeadersJson), &responseHeaders); err != nil {
			return publicRateLimitRuleConfig{}, err
		}
	}
	rule := publicRateLimitRuleConfig{
		ID:                  row.ID,
		Name:                row.Name,
		Priority:            row.Priority,
		Enabled:             row.Enabled != 0,
		Algorithm:           normalizePublicRateLimitAlgorithm(row.Algorithm),
		Limit:               row.LimitCount,
		WindowMillis:        row.WindowMillis,
		Burst:               row.Burst,
		Match:               match,
		KeyParts:            keyParts,
		ResponseStatusCode:  int(row.ResponseStatusCode),
		ResponseBody:        row.ResponseBody,
		ResponseContentType: row.ResponseContentType,
		ResponseHeaders:     responseHeaders,
		CreatedAt:           row.CreatedAt,
		UpdatedAt:           row.UpdatedAt,
	}
	if rule.Algorithm == "" {
		rule.Algorithm = publicRateLimitAlgorithmFixedWindow
	}
	if rule.ResponseStatusCode == 0 {
		rule.ResponseStatusCode = defaultRateLimitStatusCode
	}
	if rule.ResponseBody == "" {
		rule.ResponseBody = defaultRateLimitBody
	}
	if rule.ResponseContentType == "" {
		rule.ResponseContentType = defaultRateLimitContentType
	}
	if len(rule.KeyParts) == 0 {
		rule.KeyParts = []publicRateLimitKeyPartConfig{{Source: publicRateLimitKeySourceRemoteIP}}
	}
	rule.Fingerprint = publicRateLimitRuleFingerprint(rule)
	return rule, nil
}

func publicRateLimitRuleFingerprint(rule publicRateLimitRuleConfig) string {
	type fingerprint struct {
		Algorithm           string
		Limit               int64
		WindowMillis        int64
		Burst               int64
		Match               publicRateLimitMatchConfig
		KeyParts            []publicRateLimitKeyPartConfig
		ResponseStatusCode  int
		ResponseBody        string
		ResponseContentType string
		ResponseHeaders     []publicRateLimitResponseHeaderConfig
		UpdatedAt           int64
	}
	payload, _ := json.Marshal(fingerprint{
		Algorithm:           rule.Algorithm,
		Limit:               rule.Limit,
		WindowMillis:        rule.WindowMillis,
		Burst:               rule.Burst,
		Match:               rule.Match,
		KeyParts:            rule.KeyParts,
		ResponseStatusCode:  rule.ResponseStatusCode,
		ResponseBody:        rule.ResponseBody,
		ResponseContentType: rule.ResponseContentType,
		ResponseHeaders:     rule.ResponseHeaders,
		UpdatedAt:           rule.UpdatedAt.UnixNano(),
	})
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:])
}

func validatePublicRateLimitRuleInput(
	name string,
	priority int64,
	enabled bool,
	algorithm p2pstreamv1.PublicRateLimitAlgorithm,
	limit int64,
	windowMillis int64,
	burst int64,
	match *p2pstreamv1.PublicRateLimitMatch,
	keyParts []*p2pstreamv1.PublicRateLimitKeyPart,
	responseStatusCode int64,
	responseBody string,
	responseContentType string,
	responseHeaders []*p2pstreamv1.PublicRateLimitResponseHeader,
) (publicRateLimitRuleMutationInput, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		name = defaultRateLimitName
	}
	if !publicNamePattern.MatchString(name) {
		return publicRateLimitRuleMutationInput{}, connect.NewError(connect.CodeInvalidArgument, errors.New("rate limit rule name must be 1-64 alphanumeric, dot, dash, or underscore characters"))
	}
	algorithmString, err := rateLimitAlgorithmStringFromProto(algorithm)
	if err != nil {
		return publicRateLimitRuleMutationInput{}, err
	}
	if limit == 0 {
		limit = defaultRateLimitLimit
	}
	if limit < 1 {
		return publicRateLimitRuleMutationInput{}, connect.NewError(connect.CodeInvalidArgument, errors.New("rate limit must be at least 1"))
	}
	if windowMillis == 0 {
		windowMillis = defaultRateLimitWindowMillis
	}
	if windowMillis < 1000 || windowMillis > maxRateLimitWindowMillis {
		return publicRateLimitRuleMutationInput{}, connect.NewError(connect.CodeInvalidArgument, errors.New("rate limit window must be between 1 second and 1 day"))
	}
	if burst < 0 {
		return publicRateLimitRuleMutationInput{}, connect.NewError(connect.CodeInvalidArgument, errors.New("rate limit burst must be non-negative"))
	}
	if burst > 0 && burst > 10*limit {
		return publicRateLimitRuleMutationInput{}, connect.NewError(connect.CodeInvalidArgument, errors.New("rate limit burst must not exceed 10x limit"))
	}
	matchConfig, err := validateRateLimitMatch(match)
	if err != nil {
		return publicRateLimitRuleMutationInput{}, err
	}
	keyPartConfig, err := validateRateLimitKeyParts(keyParts)
	if err != nil {
		return publicRateLimitRuleMutationInput{}, err
	}
	if responseStatusCode == 0 {
		responseStatusCode = defaultRateLimitStatusCode
	}
	if responseStatusCode < 400 || responseStatusCode > 599 {
		return publicRateLimitRuleMutationInput{}, connect.NewError(connect.CodeInvalidArgument, errors.New("rate limit response status must be between 400 and 599"))
	}
	if responseBody == "" {
		responseBody = defaultRateLimitBody
	}
	if len(responseBody) > maxRateLimitResponseBodyBytes {
		return publicRateLimitRuleMutationInput{}, connect.NewError(connect.CodeInvalidArgument, errors.New("rate limit response body is too large"))
	}
	if responseContentType == "" {
		responseContentType = defaultRateLimitContentType
	}
	headerConfig, err := validateRateLimitResponseHeaders(responseHeaders)
	if err != nil {
		return publicRateLimitRuleMutationInput{}, err
	}
	matchJSON, err := json.Marshal(matchConfig)
	if err != nil {
		return publicRateLimitRuleMutationInput{}, connect.NewError(connect.CodeInternal, err)
	}
	keyPartsJSON, err := json.Marshal(keyPartConfig)
	if err != nil {
		return publicRateLimitRuleMutationInput{}, connect.NewError(connect.CodeInternal, err)
	}
	responseHeadersJSON, err := json.Marshal(headerConfig)
	if err != nil {
		return publicRateLimitRuleMutationInput{}, connect.NewError(connect.CodeInternal, err)
	}
	return publicRateLimitRuleMutationInput{
		Name:                name,
		Priority:            priority,
		Enabled:             boolInt(enabled),
		Algorithm:           algorithmString,
		LimitCount:          limit,
		WindowMillis:        windowMillis,
		Burst:               burst,
		MatchJSON:           string(matchJSON),
		KeyPartsJSON:        string(keyPartsJSON),
		ResponseStatusCode:  responseStatusCode,
		ResponseBody:        responseBody,
		ResponseContentType: responseContentType,
		ResponseHeadersJSON: string(responseHeadersJSON),
	}, nil
}

func validateRateLimitMatch(match *p2pstreamv1.PublicRateLimitMatch) (publicRateLimitMatchConfig, error) {
	if match == nil {
		return publicRateLimitMatchConfig{}, nil
	}
	if len(match.Headers)+len(match.Cookies)+len(match.QueryParams) > maxRateLimitMatchers {
		return publicRateLimitMatchConfig{}, connect.NewError(connect.CodeInvalidArgument, errors.New("rate limit rule has too many matchers"))
	}
	resp := publicRateLimitMatchConfig{}
	for _, method := range match.Methods {
		method = strings.ToUpper(strings.TrimSpace(method))
		if method == "" {
			continue
		}
		if !validHTTPToken(method) {
			return publicRateLimitMatchConfig{}, connect.NewError(connect.CodeInvalidArgument, errors.New("rate limit method matcher is invalid"))
		}
		resp.Methods = append(resp.Methods, method)
	}
	for _, protocol := range match.Protocols {
		value, err := protocolStringFromProto(protocol)
		if err != nil {
			return publicRateLimitMatchConfig{}, err
		}
		resp.Protocols = append(resp.Protocols, value)
	}
	for _, pattern := range match.HostPatterns {
		pattern = normalizeHostPattern(pattern)
		if pattern != "" {
			resp.HostPatterns = append(resp.HostPatterns, pattern)
		}
	}
	for _, prefix := range match.PathPrefixes {
		prefix = strings.TrimSpace(prefix)
		if prefix == "" {
			continue
		}
		if !strings.HasPrefix(prefix, "/") {
			return publicRateLimitMatchConfig{}, connect.NewError(connect.CodeInvalidArgument, errors.New("rate limit path prefix must start with /"))
		}
		resp.PathPrefixes = append(resp.PathPrefixes, prefix)
	}
	for _, suffix := range match.PathSuffixes {
		suffix = strings.TrimSpace(suffix)
		if suffix == "" {
			continue
		}
		if strings.ContainsAny(suffix, "/\\") {
			return publicRateLimitMatchConfig{}, connect.NewError(connect.CodeInvalidArgument, errors.New("rate limit path suffix must not contain a slash"))
		}
		resp.PathSuffixes = append(resp.PathSuffixes, suffix)
	}
	var err error
	if resp.Headers, err = validateRateLimitValueMatchers(match.Headers, true); err != nil {
		return publicRateLimitMatchConfig{}, err
	}
	if resp.Cookies, err = validateRateLimitValueMatchers(match.Cookies, false); err != nil {
		return publicRateLimitMatchConfig{}, err
	}
	if resp.QueryParams, err = validateRateLimitValueMatchers(match.QueryParams, false); err != nil {
		return publicRateLimitMatchConfig{}, err
	}
	return resp, nil
}

func validateRateLimitValueMatchers(matchers []*p2pstreamv1.PublicRateLimitValueMatcher, header bool) ([]publicRateLimitValueMatcherConfig, error) {
	resp := make([]publicRateLimitValueMatcherConfig, 0, len(matchers))
	for _, matcher := range matchers {
		if matcher == nil {
			continue
		}
		name := strings.TrimSpace(matcher.Name)
		if name == "" {
			return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("rate limit matcher name is required"))
		}
		if header {
			name = textproto.CanonicalMIMEHeaderKey(name)
			if !validHTTPToken(name) {
				return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("rate limit header matcher name is invalid"))
			}
		}
		operator, err := rateLimitMatchOperatorStringFromProto(matcher.Operator)
		if err != nil {
			return nil, err
		}
		resp = append(resp, publicRateLimitValueMatcherConfig{Name: name, Operator: operator, Value: matcher.Value})
	}
	return resp, nil
}

func validateRateLimitKeyParts(parts []*p2pstreamv1.PublicRateLimitKeyPart) ([]publicRateLimitKeyPartConfig, error) {
	if len(parts) == 0 {
		return []publicRateLimitKeyPartConfig{{Source: publicRateLimitKeySourceRemoteIP}}, nil
	}
	if len(parts) > maxRateLimitKeyParts {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("rate limit rule has too many key parts"))
	}
	resp := make([]publicRateLimitKeyPartConfig, 0, len(parts))
	for _, part := range parts {
		if part == nil {
			continue
		}
		source, err := rateLimitKeySourceStringFromProto(part.Source)
		if err != nil {
			return nil, err
		}
		name := strings.TrimSpace(part.Name)
		switch source {
		case publicRateLimitKeySourceHeader:
			if name == "" {
				return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("rate limit header key part requires a name"))
			}
			name = textproto.CanonicalMIMEHeaderKey(name)
			if !validHTTPToken(name) {
				return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("rate limit header key part name is invalid"))
			}
		case publicRateLimitKeySourceCookie, publicRateLimitKeySourceQueryParam:
			if name == "" {
				return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("rate limit cookie and query key parts require a name"))
			}
		default:
			name = ""
		}
		resp = append(resp, publicRateLimitKeyPartConfig{Source: source, Name: name})
	}
	if len(resp) == 0 {
		resp = append(resp, publicRateLimitKeyPartConfig{Source: publicRateLimitKeySourceRemoteIP})
	}
	return resp, nil
}

func validateRateLimitResponseHeaders(headers []*p2pstreamv1.PublicRateLimitResponseHeader) ([]publicRateLimitResponseHeaderConfig, error) {
	if len(headers) > maxRateLimitResponseHeaders {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("rate limit response has too many headers"))
	}
	resp := make([]publicRateLimitResponseHeaderConfig, 0, len(headers))
	for _, header := range headers {
		if header == nil {
			continue
		}
		name := textproto.CanonicalMIMEHeaderKey(strings.TrimSpace(header.Name))
		if name == "" {
			continue
		}
		if !validHTTPToken(name) || protectedRateLimitResponseHeader(name) {
			return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("rate limit response header %q is not allowed", name))
		}
		resp = append(resp, publicRateLimitResponseHeaderConfig{Name: name, Value: header.Value})
	}
	return resp, nil
}

func protectedRateLimitResponseHeader(name string) bool {
	switch strings.ToLower(name) {
	case "connection", "transfer-encoding", "content-length",
		"retry-after", "ratelimit-limit", "ratelimit-remaining", "ratelimit-reset",
		"x-ratelimit-limit", "x-ratelimit-remaining", "x-ratelimit-reset":
		return true
	default:
		return false
	}
}

func validHTTPToken(value string) bool {
	if value == "" {
		return false
	}
	for _, r := range value {
		if r > 127 {
			return false
		}
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
			continue
		}
		switch r {
		case '!', '#', '$', '%', '&', '\'', '*', '+', '-', '.', '^', '_', '`', '|', '~':
			continue
		default:
			return false
		}
	}
	return true
}

func rateLimitAlgorithmStringFromProto(algorithm p2pstreamv1.PublicRateLimitAlgorithm) (string, error) {
	switch algorithm {
	case p2pstreamv1.PublicRateLimitAlgorithm_PUBLIC_RATE_LIMIT_ALGORITHM_UNSPECIFIED,
		p2pstreamv1.PublicRateLimitAlgorithm_PUBLIC_RATE_LIMIT_ALGORITHM_FIXED_WINDOW:
		return publicRateLimitAlgorithmFixedWindow, nil
	case p2pstreamv1.PublicRateLimitAlgorithm_PUBLIC_RATE_LIMIT_ALGORITHM_SLIDING_WINDOW:
		return publicRateLimitAlgorithmSlidingWindow, nil
	case p2pstreamv1.PublicRateLimitAlgorithm_PUBLIC_RATE_LIMIT_ALGORITHM_TOKEN_BUCKET:
		return publicRateLimitAlgorithmTokenBucket, nil
	case p2pstreamv1.PublicRateLimitAlgorithm_PUBLIC_RATE_LIMIT_ALGORITHM_LEAKY_BUCKET:
		return publicRateLimitAlgorithmLeakyBucket, nil
	default:
		return "", connect.NewError(connect.CodeInvalidArgument, errors.New("rate limit algorithm is invalid"))
	}
}

func normalizePublicRateLimitAlgorithm(algorithm string) string {
	switch algorithm {
	case publicRateLimitAlgorithmSlidingWindow, publicRateLimitAlgorithmTokenBucket, publicRateLimitAlgorithmLeakyBucket:
		return algorithm
	default:
		return publicRateLimitAlgorithmFixedWindow
	}
}

func protoRateLimitAlgorithmFromString(algorithm string) p2pstreamv1.PublicRateLimitAlgorithm {
	switch normalizePublicRateLimitAlgorithm(algorithm) {
	case publicRateLimitAlgorithmSlidingWindow:
		return p2pstreamv1.PublicRateLimitAlgorithm_PUBLIC_RATE_LIMIT_ALGORITHM_SLIDING_WINDOW
	case publicRateLimitAlgorithmTokenBucket:
		return p2pstreamv1.PublicRateLimitAlgorithm_PUBLIC_RATE_LIMIT_ALGORITHM_TOKEN_BUCKET
	case publicRateLimitAlgorithmLeakyBucket:
		return p2pstreamv1.PublicRateLimitAlgorithm_PUBLIC_RATE_LIMIT_ALGORITHM_LEAKY_BUCKET
	default:
		return p2pstreamv1.PublicRateLimitAlgorithm_PUBLIC_RATE_LIMIT_ALGORITHM_FIXED_WINDOW
	}
}

func rateLimitKeySourceStringFromProto(source p2pstreamv1.PublicRateLimitKeySource) (string, error) {
	switch source {
	case p2pstreamv1.PublicRateLimitKeySource_PUBLIC_RATE_LIMIT_KEY_SOURCE_UNSPECIFIED,
		p2pstreamv1.PublicRateLimitKeySource_PUBLIC_RATE_LIMIT_KEY_SOURCE_REMOTE_IP:
		return publicRateLimitKeySourceRemoteIP, nil
	case p2pstreamv1.PublicRateLimitKeySource_PUBLIC_RATE_LIMIT_KEY_SOURCE_HOST:
		return publicRateLimitKeySourceHost, nil
	case p2pstreamv1.PublicRateLimitKeySource_PUBLIC_RATE_LIMIT_KEY_SOURCE_METHOD:
		return publicRateLimitKeySourceMethod, nil
	case p2pstreamv1.PublicRateLimitKeySource_PUBLIC_RATE_LIMIT_KEY_SOURCE_PATH:
		return publicRateLimitKeySourcePath, nil
	case p2pstreamv1.PublicRateLimitKeySource_PUBLIC_RATE_LIMIT_KEY_SOURCE_PROTOCOL:
		return publicRateLimitKeySourceProtocol, nil
	case p2pstreamv1.PublicRateLimitKeySource_PUBLIC_RATE_LIMIT_KEY_SOURCE_HEADER:
		return publicRateLimitKeySourceHeader, nil
	case p2pstreamv1.PublicRateLimitKeySource_PUBLIC_RATE_LIMIT_KEY_SOURCE_COOKIE:
		return publicRateLimitKeySourceCookie, nil
	case p2pstreamv1.PublicRateLimitKeySource_PUBLIC_RATE_LIMIT_KEY_SOURCE_QUERY_PARAM:
		return publicRateLimitKeySourceQueryParam, nil
	default:
		return "", connect.NewError(connect.CodeInvalidArgument, errors.New("rate limit key source is invalid"))
	}
}

func protoRateLimitKeySourceFromString(source string) p2pstreamv1.PublicRateLimitKeySource {
	switch source {
	case publicRateLimitKeySourceHost:
		return p2pstreamv1.PublicRateLimitKeySource_PUBLIC_RATE_LIMIT_KEY_SOURCE_HOST
	case publicRateLimitKeySourceMethod:
		return p2pstreamv1.PublicRateLimitKeySource_PUBLIC_RATE_LIMIT_KEY_SOURCE_METHOD
	case publicRateLimitKeySourcePath:
		return p2pstreamv1.PublicRateLimitKeySource_PUBLIC_RATE_LIMIT_KEY_SOURCE_PATH
	case publicRateLimitKeySourceProtocol:
		return p2pstreamv1.PublicRateLimitKeySource_PUBLIC_RATE_LIMIT_KEY_SOURCE_PROTOCOL
	case publicRateLimitKeySourceHeader:
		return p2pstreamv1.PublicRateLimitKeySource_PUBLIC_RATE_LIMIT_KEY_SOURCE_HEADER
	case publicRateLimitKeySourceCookie:
		return p2pstreamv1.PublicRateLimitKeySource_PUBLIC_RATE_LIMIT_KEY_SOURCE_COOKIE
	case publicRateLimitKeySourceQueryParam:
		return p2pstreamv1.PublicRateLimitKeySource_PUBLIC_RATE_LIMIT_KEY_SOURCE_QUERY_PARAM
	default:
		return p2pstreamv1.PublicRateLimitKeySource_PUBLIC_RATE_LIMIT_KEY_SOURCE_REMOTE_IP
	}
}

func rateLimitMatchOperatorStringFromProto(operator p2pstreamv1.PublicRateLimitMatchOperator) (string, error) {
	switch operator {
	case p2pstreamv1.PublicRateLimitMatchOperator_PUBLIC_RATE_LIMIT_MATCH_OPERATOR_PRESENT:
		return publicRateLimitMatchOperatorPresent, nil
	case p2pstreamv1.PublicRateLimitMatchOperator_PUBLIC_RATE_LIMIT_MATCH_OPERATOR_EQUALS:
		return publicRateLimitMatchOperatorEquals, nil
	case p2pstreamv1.PublicRateLimitMatchOperator_PUBLIC_RATE_LIMIT_MATCH_OPERATOR_PREFIX:
		return publicRateLimitMatchOperatorPrefix, nil
	case p2pstreamv1.PublicRateLimitMatchOperator_PUBLIC_RATE_LIMIT_MATCH_OPERATOR_SUFFIX:
		return publicRateLimitMatchOperatorSuffix, nil
	case p2pstreamv1.PublicRateLimitMatchOperator_PUBLIC_RATE_LIMIT_MATCH_OPERATOR_CONTAINS:
		return publicRateLimitMatchOperatorContains, nil
	default:
		return "", connect.NewError(connect.CodeInvalidArgument, errors.New("rate limit match operator is invalid"))
	}
}

func protoRateLimitMatchOperatorFromString(operator string) p2pstreamv1.PublicRateLimitMatchOperator {
	switch operator {
	case publicRateLimitMatchOperatorPresent:
		return p2pstreamv1.PublicRateLimitMatchOperator_PUBLIC_RATE_LIMIT_MATCH_OPERATOR_PRESENT
	case publicRateLimitMatchOperatorPrefix:
		return p2pstreamv1.PublicRateLimitMatchOperator_PUBLIC_RATE_LIMIT_MATCH_OPERATOR_PREFIX
	case publicRateLimitMatchOperatorSuffix:
		return p2pstreamv1.PublicRateLimitMatchOperator_PUBLIC_RATE_LIMIT_MATCH_OPERATOR_SUFFIX
	case publicRateLimitMatchOperatorContains:
		return p2pstreamv1.PublicRateLimitMatchOperator_PUBLIC_RATE_LIMIT_MATCH_OPERATOR_CONTAINS
	default:
		return p2pstreamv1.PublicRateLimitMatchOperator_PUBLIC_RATE_LIMIT_MATCH_OPERATOR_EQUALS
	}
}

func publicRateLimitConfigsToProto(rules []publicRateLimitRuleConfig) []*p2pstreamv1.PublicRateLimitRule {
	resp := make([]*p2pstreamv1.PublicRateLimitRule, 0, len(rules))
	for _, rule := range rules {
		resp = append(resp, publicRateLimitConfigToProto(rule))
	}
	return resp
}

func publicRateLimitRulesToProto(rows []db.PublicRateLimitRule) []*p2pstreamv1.PublicRateLimitRule {
	resp := make([]*p2pstreamv1.PublicRateLimitRule, 0, len(rows))
	for _, row := range rows {
		rule, err := publicRateLimitRuleRowToConfig(row)
		if err != nil {
			continue
		}
		resp = append(resp, publicRateLimitConfigToProto(rule))
	}
	return resp
}

func publicRateLimitConfigToProto(rule publicRateLimitRuleConfig) *p2pstreamv1.PublicRateLimitRule {
	return &p2pstreamv1.PublicRateLimitRule{
		Id:                  rule.ID,
		Name:                rule.Name,
		Priority:            rule.Priority,
		Enabled:             rule.Enabled,
		Algorithm:           protoRateLimitAlgorithmFromString(rule.Algorithm),
		Limit:               rule.Limit,
		WindowMillis:        rule.WindowMillis,
		Burst:               rule.Burst,
		Match:               rateLimitMatchToProto(rule.Match),
		KeyParts:            rateLimitKeyPartsToProto(rule.KeyParts),
		ResponseStatusCode:  int64(rule.ResponseStatusCode),
		ResponseBody:        rule.ResponseBody,
		ResponseContentType: rule.ResponseContentType,
		ResponseHeaders:     rateLimitResponseHeadersToProto(rule.ResponseHeaders),
		CreatedAtUnixMillis: rule.CreatedAt.UnixMilli(),
		UpdatedAtUnixMillis: rule.UpdatedAt.UnixMilli(),
	}
}

func rateLimitMatchToProto(match publicRateLimitMatchConfig) *p2pstreamv1.PublicRateLimitMatch {
	protocols := make([]p2pstreamv1.PublicListenerProtocol, 0, len(match.Protocols))
	for _, protocol := range match.Protocols {
		protocols = append(protocols, protoProtocolFromString(protocol))
	}
	return &p2pstreamv1.PublicRateLimitMatch{
		Methods:      append([]string(nil), match.Methods...),
		Protocols:    protocols,
		HostPatterns: append([]string(nil), match.HostPatterns...),
		PathPrefixes: append([]string(nil), match.PathPrefixes...),
		PathSuffixes: append([]string(nil), match.PathSuffixes...),
		Headers:      rateLimitValueMatchersToProto(match.Headers),
		Cookies:      rateLimitValueMatchersToProto(match.Cookies),
		QueryParams:  rateLimitValueMatchersToProto(match.QueryParams),
	}
}

func rateLimitValueMatchersToProto(matchers []publicRateLimitValueMatcherConfig) []*p2pstreamv1.PublicRateLimitValueMatcher {
	resp := make([]*p2pstreamv1.PublicRateLimitValueMatcher, 0, len(matchers))
	for _, matcher := range matchers {
		resp = append(resp, &p2pstreamv1.PublicRateLimitValueMatcher{
			Name:     matcher.Name,
			Operator: protoRateLimitMatchOperatorFromString(matcher.Operator),
			Value:    matcher.Value,
		})
	}
	return resp
}

func rateLimitKeyPartsToProto(parts []publicRateLimitKeyPartConfig) []*p2pstreamv1.PublicRateLimitKeyPart {
	resp := make([]*p2pstreamv1.PublicRateLimitKeyPart, 0, len(parts))
	for _, part := range parts {
		resp = append(resp, &p2pstreamv1.PublicRateLimitKeyPart{
			Source: protoRateLimitKeySourceFromString(part.Source),
			Name:   part.Name,
		})
	}
	return resp
}

func rateLimitResponseHeadersToProto(headers []publicRateLimitResponseHeaderConfig) []*p2pstreamv1.PublicRateLimitResponseHeader {
	resp := make([]*p2pstreamv1.PublicRateLimitResponseHeader, 0, len(headers))
	for _, header := range headers {
		resp = append(resp, &p2pstreamv1.PublicRateLimitResponseHeader{Name: header.Name, Value: header.Value})
	}
	return resp
}

func (a *App) CreatePublicRateLimitRule(
	ctx context.Context,
	req *connect.Request[p2pstreamv1.CreatePublicRateLimitRuleRequest],
) (*connect.Response[p2pstreamv1.CreatePublicRateLimitRuleResponse], error) {
	if _, err := a.requireAdmin(ctx, req.Header()); err != nil {
		return nil, err
	}
	params, err := validatePublicRateLimitRuleInput(
		req.Msg.Name,
		req.Msg.Priority,
		req.Msg.Enabled,
		req.Msg.Algorithm,
		req.Msg.Limit,
		req.Msg.WindowMillis,
		req.Msg.Burst,
		req.Msg.Match,
		req.Msg.KeyParts,
		req.Msg.ResponseStatusCode,
		req.Msg.ResponseBody,
		req.Msg.ResponseContentType,
		req.Msg.ResponseHeaders,
	)
	if err != nil {
		return nil, err
	}
	row, err := a.DB.CreatePublicRateLimitRule(ctx, db.CreatePublicRateLimitRuleParams{
		Name:                params.Name,
		Priority:            params.Priority,
		Enabled:             params.Enabled,
		Algorithm:           params.Algorithm,
		LimitCount:          params.LimitCount,
		WindowMillis:        params.WindowMillis,
		Burst:               params.Burst,
		MatchJson:           params.MatchJSON,
		KeyPartsJson:        params.KeyPartsJSON,
		ResponseStatusCode:  params.ResponseStatusCode,
		ResponseBody:        params.ResponseBody,
		ResponseContentType: params.ResponseContentType,
		ResponseHeadersJson: params.ResponseHeadersJSON,
	})
	if err != nil {
		return nil, publicDBError(err)
	}
	if err := a.refreshPublicProxySnapshot(ctx); err != nil {
		return nil, err
	}
	rule, err := publicRateLimitRuleRowToConfig(row)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	return connect.NewResponse(&p2pstreamv1.CreatePublicRateLimitRuleResponse{Rule: publicRateLimitConfigToProto(rule)}), nil
}

func (a *App) UpdatePublicRateLimitRule(
	ctx context.Context,
	req *connect.Request[p2pstreamv1.UpdatePublicRateLimitRuleRequest],
) (*connect.Response[p2pstreamv1.UpdatePublicRateLimitRuleResponse], error) {
	if _, err := a.requireAdmin(ctx, req.Header()); err != nil {
		return nil, err
	}
	params, err := validatePublicRateLimitRuleInput(
		req.Msg.Name,
		req.Msg.Priority,
		req.Msg.Enabled,
		req.Msg.Algorithm,
		req.Msg.Limit,
		req.Msg.WindowMillis,
		req.Msg.Burst,
		req.Msg.Match,
		req.Msg.KeyParts,
		req.Msg.ResponseStatusCode,
		req.Msg.ResponseBody,
		req.Msg.ResponseContentType,
		req.Msg.ResponseHeaders,
	)
	if err != nil {
		return nil, err
	}
	row, err := a.DB.UpdatePublicRateLimitRule(ctx, db.UpdatePublicRateLimitRuleParams{
		ID:                  req.Msg.Id,
		Name:                params.Name,
		Priority:            params.Priority,
		Enabled:             params.Enabled,
		Algorithm:           params.Algorithm,
		LimitCount:          params.LimitCount,
		WindowMillis:        params.WindowMillis,
		Burst:               params.Burst,
		MatchJson:           params.MatchJSON,
		KeyPartsJson:        params.KeyPartsJSON,
		ResponseStatusCode:  params.ResponseStatusCode,
		ResponseBody:        params.ResponseBody,
		ResponseContentType: params.ResponseContentType,
		ResponseHeadersJson: params.ResponseHeadersJSON,
	})
	if err != nil {
		return nil, publicDBError(err)
	}
	if err := a.refreshPublicProxySnapshot(ctx); err != nil {
		return nil, err
	}
	rule, err := publicRateLimitRuleRowToConfig(row)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	return connect.NewResponse(&p2pstreamv1.UpdatePublicRateLimitRuleResponse{Rule: publicRateLimitConfigToProto(rule)}), nil
}

func (a *App) DeletePublicRateLimitRule(
	ctx context.Context,
	req *connect.Request[p2pstreamv1.DeletePublicRateLimitRuleRequest],
) (*connect.Response[p2pstreamv1.DeletePublicRateLimitRuleResponse], error) {
	if _, err := a.requireAdmin(ctx, req.Header()); err != nil {
		return nil, err
	}
	if err := a.DB.DeletePublicRateLimitRule(ctx, req.Msg.Id); err != nil {
		return nil, publicDBError(err)
	}
	if err := a.refreshPublicProxySnapshot(ctx); err != nil {
		return nil, err
	}
	return connect.NewResponse(&p2pstreamv1.DeletePublicRateLimitRuleResponse{}), nil
}

func effectiveRateLimitBurst(rule publicRateLimitRuleConfig) int64 {
	if rule.Burst > 0 {
		return rule.Burst
	}
	return rule.Limit
}

func millisDuration(ms int64) time.Duration {
	if ms < 1 {
		ms = 1
	}
	return time.Duration(ms) * time.Millisecond
}

func stringInSlice(value string, values []string) bool {
	for _, item := range values {
		if item == value {
			return true
		}
	}
	return false
}

func maxInt64(a int64, b int64) int64 {
	if a > b {
		return a
	}
	return b
}
