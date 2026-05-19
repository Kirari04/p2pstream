package server

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"connectrpc.com/connect"

	p2pstreamv1 "p2pstream/gen/proto/p2pstream/v1"
	"p2pstream/internal/db"
)

const (
	defaultTrafficShaperName                 = "traffic-shaper"
	publicTrafficShaperBudgetScopePerKey     = "per_key"
	publicTrafficShaperBudgetScopePerRequest = "per_request"
	maxTrafficShaperBytesPerSecond           = int64(1 << 40)
	maxTrafficShaperBurstBytes               = int64(1 << 30)
	maxTrafficShaperExemptBytes              = int64(1 << 30)
	maxTrafficShaperBucketsPerRule           = 10000
	maxShapingReadChunkBytes                 = 32 * 1024
	trafficShaperIdleStateTTL                = 15 * time.Minute
	trafficShaperPruneInterval               = time.Minute
)

type publicTrafficShaperRuleConfig struct {
	ID                     int64
	Name                   string
	Priority               int64
	Enabled                bool
	BudgetScope            string
	UploadBytesPerSecond   int64
	DownloadBytesPerSecond int64
	BurstBytes             int64
	RequestExemptBytes     int64
	ResponseExemptBytes    int64
	Match                  publicPolicyMatchConfig
	KeyParts               []publicRateLimitKeyPartConfig
	CreatedAt              time.Time
	UpdatedAt              time.Time
	Fingerprint            string
}

type publicTrafficShaperRuleMutationInput struct {
	Name                   string
	Priority               int64
	Enabled                int64
	BudgetScope            string
	UploadBytesPerSecond   int64
	DownloadBytesPerSecond int64
	BurstBytes             int64
	RequestExemptBytes     int64
	ResponseExemptBytes    int64
	MatchJSON              string
	KeyPartsJSON           string
}

type publicTrafficShaper struct {
	mu        sync.Mutex
	rules     map[int64]*publicTrafficShaperRuleRuntime
	lastPrune time.Time
}

type publicTrafficShaperRuleRuntime struct {
	fingerprint     string
	uploadBuckets   map[string]*byteTokenBucket
	downloadBuckets map[string]*byteTokenBucket
}

type publicTrafficShaperDecision struct {
	Rule           publicTrafficShaperRuleConfig
	Listener       publicListenerConfig
	UploadBucket   *byteTokenBucket
	DownloadBucket *byteTokenBucket
}

type byteTokenBucket struct {
	mu                 sync.Mutex
	rateBytesPerSecond float64
	burstBytes         float64
	tokens             float64
	lastRefill         time.Time
	lastUsed           time.Time
	now                func() time.Time
	sleep              func(context.Context, time.Duration) error
}

type shapingReadCloser struct {
	ctx             context.Context
	body            io.ReadCloser
	bucket          *byteTokenBucket
	exemptRemaining int64
}

func newPublicTrafficShaper() *publicTrafficShaper {
	return &publicTrafficShaper{rules: make(map[int64]*publicTrafficShaperRuleRuntime)}
}

func (s *publicTrafficShaper) reconcile(snap *publicProxySnapshot) {
	if s == nil {
		return
	}
	keep := make(map[int64]string)
	if snap != nil {
		for _, rule := range snap.TrafficShaperRules {
			if rule.Enabled {
				keep[rule.ID] = rule.Fingerprint
			}
		}
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	for id, runtime := range s.rules {
		if fingerprint, ok := keep[id]; !ok || runtime.fingerprint != fingerprint {
			delete(s.rules, id)
		}
	}
	for id, fingerprint := range keep {
		if _, ok := s.rules[id]; !ok {
			s.rules[id] = &publicTrafficShaperRuleRuntime{
				fingerprint:     fingerprint,
				uploadBuckets:   make(map[string]*byteTokenBucket),
				downloadBuckets: make(map[string]*byteTokenBucket),
			}
		}
	}
}

func (a *App) selectPublicTrafficShaper(listenerID int64, r *http.Request) (publicTrafficShaperDecision, bool) {
	if a == nil || a.TrafficShaper == nil {
		return publicTrafficShaperDecision{}, false
	}
	a.proxyMu.Lock()
	snap := a.publicSnapshot
	a.proxyMu.Unlock()
	if snap == nil || len(snap.TrafficShaperRules) == 0 {
		return publicTrafficShaperDecision{}, false
	}
	listener, ok := snap.Listeners[listenerID]
	if !ok {
		return publicTrafficShaperDecision{}, false
	}
	return a.TrafficShaper.evaluate(snap.TrafficShaperRules, listener, r, time.Now())
}

func (s *publicTrafficShaper) evaluate(rules []publicTrafficShaperRuleConfig, listener publicListenerConfig, r *http.Request, now time.Time) (publicTrafficShaperDecision, bool) {
	if len(rules) == 0 {
		return publicTrafficShaperDecision{}, false
	}
	ordered := append([]publicTrafficShaperRuleConfig(nil), rules...)
	sort.SliceStable(ordered, func(i, j int) bool {
		if ordered[i].Priority == ordered[j].Priority {
			return ordered[i].ID < ordered[j].ID
		}
		return ordered[i].Priority < ordered[j].Priority
	})
	for _, rule := range ordered {
		if !rule.Enabled || !rule.matches(listener, r) {
			continue
		}
		return s.decisionForRule(rule, listener, r, now), true
	}
	return publicTrafficShaperDecision{}, false
}

func (s *publicTrafficShaper) decisionForRule(rule publicTrafficShaperRuleConfig, listener publicListenerConfig, r *http.Request, now time.Time) publicTrafficShaperDecision {
	decision := publicTrafficShaperDecision{Rule: rule, Listener: listener}
	if rule.BudgetScope == publicTrafficShaperBudgetScopePerRequest {
		decision.UploadBucket = newByteTokenBucket(rule.UploadBytesPerSecond, rule.effectiveBurstBytes(rule.UploadBytesPerSecond), now)
		decision.DownloadBucket = newByteTokenBucket(rule.DownloadBytesPerSecond, rule.effectiveBurstBytes(rule.DownloadBytesPerSecond), now)
		return decision
	}

	key := trafficShaperKeyHash(rule.keyValues(listener, r))
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pruneLocked(now)
	runtime := s.rules[rule.ID]
	if runtime == nil || runtime.fingerprint != rule.Fingerprint {
		runtime = &publicTrafficShaperRuleRuntime{
			fingerprint:     rule.Fingerprint,
			uploadBuckets:   make(map[string]*byteTokenBucket),
			downloadBuckets: make(map[string]*byteTokenBucket),
		}
		s.rules[rule.ID] = runtime
	}
	if rule.UploadBytesPerSecond > 0 {
		decision.UploadBucket = runtime.uploadBuckets[key]
		if decision.UploadBucket == nil {
			if len(runtime.uploadBuckets) >= maxTrafficShaperBucketsPerRule {
				evictOldestTrafficShaperBucket(runtime.uploadBuckets)
			}
			decision.UploadBucket = newByteTokenBucket(rule.UploadBytesPerSecond, rule.effectiveBurstBytes(rule.UploadBytesPerSecond), now)
			runtime.uploadBuckets[key] = decision.UploadBucket
		}
	}
	if rule.DownloadBytesPerSecond > 0 {
		decision.DownloadBucket = runtime.downloadBuckets[key]
		if decision.DownloadBucket == nil {
			if len(runtime.downloadBuckets) >= maxTrafficShaperBucketsPerRule {
				evictOldestTrafficShaperBucket(runtime.downloadBuckets)
			}
			decision.DownloadBucket = newByteTokenBucket(rule.DownloadBytesPerSecond, rule.effectiveBurstBytes(rule.DownloadBytesPerSecond), now)
			runtime.downloadBuckets[key] = decision.DownloadBucket
		}
	}
	return decision
}

func (s *publicTrafficShaper) pruneLocked(now time.Time) {
	if !s.lastPrune.IsZero() && now.Sub(s.lastPrune) < trafficShaperPruneInterval {
		return
	}
	s.lastPrune = now
	for _, runtime := range s.rules {
		pruneTrafficShaperBuckets(runtime.uploadBuckets, now)
		pruneTrafficShaperBuckets(runtime.downloadBuckets, now)
	}
}

func pruneTrafficShaperBuckets(buckets map[string]*byteTokenBucket, now time.Time) {
	for key, bucket := range buckets {
		if now.Sub(bucket.lastUsedAt()) > trafficShaperIdleStateTTL {
			delete(buckets, key)
		}
	}
}

func evictOldestTrafficShaperBucket(buckets map[string]*byteTokenBucket) {
	var oldestKey string
	var oldestTime time.Time
	for key, bucket := range buckets {
		if bucket == nil {
			delete(buckets, key)
			return
		}
		lastUsed := bucket.lastUsedAt()
		if oldestKey == "" || lastUsed.Before(oldestTime) {
			oldestKey = key
			oldestTime = lastUsed
		}
	}
	if oldestKey != "" {
		delete(buckets, oldestKey)
	}
}

func (rule publicTrafficShaperRuleConfig) matches(listener publicListenerConfig, r *http.Request) bool {
	return publicRateLimitRuleConfig{Match: rule.Match}.matches(listener, r)
}

func (rule publicTrafficShaperRuleConfig) keyValues(listener publicListenerConfig, r *http.Request) []string {
	return publicRateLimitRuleConfig{KeyParts: rule.KeyParts}.keyValues(listener, r)
}

func (rule publicTrafficShaperRuleConfig) effectiveBurstBytes(rate int64) int64 {
	if rate <= 0 {
		return 0
	}
	if rule.BurstBytes > 0 {
		return rule.BurstBytes
	}
	return rate
}

func trafficShaperKeyHash(values []string) string {
	h := sha256.New()
	h.Write([]byte("traffic-shaper"))
	h.Write([]byte{0})
	for _, value := range values {
		h.Write([]byte(value))
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))
}

func newByteTokenBucket(rateBytesPerSecond int64, burstBytes int64, now time.Time) *byteTokenBucket {
	if rateBytesPerSecond <= 0 {
		return nil
	}
	if burstBytes <= 0 {
		burstBytes = rateBytesPerSecond
	}
	bucket := &byteTokenBucket{
		rateBytesPerSecond: float64(rateBytesPerSecond),
		burstBytes:         float64(burstBytes),
		tokens:             float64(burstBytes),
		lastRefill:         now,
		lastUsed:           now,
		now:                time.Now,
		sleep:              sleepWithContext,
	}
	return bucket
}

func (b *byteTokenBucket) wait(ctx context.Context, bytes int) error {
	if b == nil || bytes <= 0 || b.rateBytesPerSecond <= 0 {
		return nil
	}
	remaining := float64(bytes)
	for remaining > 0 {
		spend := remaining
		if b.burstBytes > 0 && spend > b.burstBytes {
			spend = b.burstBytes
		}
		if err := b.waitForSpend(ctx, spend); err != nil {
			return err
		}
		remaining -= spend
	}
	return nil
}

func (b *byteTokenBucket) waitForSpend(ctx context.Context, bytes float64) error {
	for {
		now := b.now()
		b.mu.Lock()
		b.refillLocked(now)
		if b.tokens >= bytes {
			b.tokens -= bytes
			b.lastUsed = now
			b.mu.Unlock()
			return nil
		}
		missing := bytes - b.tokens
		wait := time.Duration(math.Ceil((missing / b.rateBytesPerSecond) * float64(time.Second)))
		if wait < time.Millisecond {
			wait = time.Millisecond
		}
		b.mu.Unlock()
		if err := b.sleep(ctx, wait); err != nil {
			return err
		}
	}
}

func (b *byteTokenBucket) refillLocked(now time.Time) {
	if now.Before(b.lastRefill) {
		b.lastRefill = now
		return
	}
	elapsed := now.Sub(b.lastRefill).Seconds()
	if elapsed <= 0 {
		return
	}
	b.tokens += elapsed * b.rateBytesPerSecond
	if b.tokens > b.burstBytes {
		b.tokens = b.burstBytes
	}
	b.lastRefill = now
}

func (b *byteTokenBucket) readChunkLimit() int {
	if b == nil || b.burstBytes <= 0 {
		return maxShapingReadChunkBytes
	}
	limit := int(math.Ceil(b.burstBytes))
	if limit < 1 {
		limit = 1
	}
	if limit > maxShapingReadChunkBytes {
		limit = maxShapingReadChunkBytes
	}
	return limit
}

func (b *byteTokenBucket) lastUsedAt() time.Time {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.lastUsed
}

func sleepWithContext(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (d publicTrafficShaperDecision) wrapUploadBody(ctx context.Context, body io.ReadCloser) io.ReadCloser {
	return newShapingReadCloser(ctx, body, d.UploadBucket, d.Rule.RequestExemptBytes)
}

func (d publicTrafficShaperDecision) wrapDownloadBody(ctx context.Context, body io.ReadCloser) io.ReadCloser {
	return newShapingReadCloser(ctx, body, d.DownloadBucket, d.Rule.ResponseExemptBytes)
}

func newShapingReadCloser(ctx context.Context, body io.ReadCloser, bucket *byteTokenBucket, exemptBytes int64) io.ReadCloser {
	if body == nil || bucket == nil {
		return body
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if exemptBytes < 0 {
		exemptBytes = 0
	}
	return &shapingReadCloser{ctx: ctx, body: body, bucket: bucket, exemptRemaining: exemptBytes}
}

func (r *shapingReadCloser) Read(p []byte) (int, error) {
	if len(p) > r.bucket.readChunkLimit() {
		p = p[:r.bucket.readChunkLimit()]
	}
	n, err := r.body.Read(p)
	if n > 0 {
		charge := n
		if r.exemptRemaining > 0 {
			exempted := int64(n)
			if exempted > r.exemptRemaining {
				exempted = r.exemptRemaining
			}
			r.exemptRemaining -= exempted
			charge -= int(exempted)
		}
		if charge > 0 {
			if waitErr := r.bucket.wait(r.ctx, charge); waitErr != nil && err == nil {
				err = waitErr
			}
		}
	}
	return n, err
}

func (r *shapingReadCloser) Close() error {
	return r.body.Close()
}

func validatePublicTrafficShaperRuleInput(
	name string,
	priority int64,
	enabled bool,
	budgetScope p2pstreamv1.PublicTrafficShaperBudgetScope,
	uploadBytesPerSecond int64,
	downloadBytesPerSecond int64,
	burstBytes int64,
	requestExemptBytes int64,
	responseExemptBytes int64,
	keyParts []*p2pstreamv1.PublicRateLimitKeyPart,
	matchRule *p2pstreamv1.PublicPolicyMatchRule,
) (publicTrafficShaperRuleMutationInput, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		name = defaultTrafficShaperName
	}
	if !publicNamePattern.MatchString(name) {
		return publicTrafficShaperRuleMutationInput{}, connect.NewError(connect.CodeInvalidArgument, errors.New("traffic shaper rule name must be 1-64 alphanumeric, dot, dash, or underscore characters"))
	}
	scope, err := trafficShaperBudgetScopeStringFromProto(budgetScope)
	if err != nil {
		return publicTrafficShaperRuleMutationInput{}, err
	}
	if uploadBytesPerSecond < 0 || uploadBytesPerSecond > maxTrafficShaperBytesPerSecond {
		return publicTrafficShaperRuleMutationInput{}, connect.NewError(connect.CodeInvalidArgument, errors.New("traffic shaper upload bandwidth must be between 0 and 1 TiB/s"))
	}
	if downloadBytesPerSecond < 0 || downloadBytesPerSecond > maxTrafficShaperBytesPerSecond {
		return publicTrafficShaperRuleMutationInput{}, connect.NewError(connect.CodeInvalidArgument, errors.New("traffic shaper download bandwidth must be between 0 and 1 TiB/s"))
	}
	if uploadBytesPerSecond == 0 && downloadBytesPerSecond == 0 {
		return publicTrafficShaperRuleMutationInput{}, connect.NewError(connect.CodeInvalidArgument, errors.New("traffic shaper requires upload or download bandwidth"))
	}
	if burstBytes < 0 || burstBytes > maxTrafficShaperBurstBytes {
		return publicTrafficShaperRuleMutationInput{}, connect.NewError(connect.CodeInvalidArgument, errors.New("traffic shaper burst must be between 0 and 1 GiB"))
	}
	if requestExemptBytes < 0 || requestExemptBytes > maxTrafficShaperExemptBytes {
		return publicTrafficShaperRuleMutationInput{}, connect.NewError(connect.CodeInvalidArgument, errors.New("traffic shaper request exemption must be between 0 and 1 GiB"))
	}
	if responseExemptBytes < 0 || responseExemptBytes > maxTrafficShaperExemptBytes {
		return publicTrafficShaperRuleMutationInput{}, connect.NewError(connect.CodeInvalidArgument, errors.New("traffic shaper response exemption must be between 0 and 1 GiB"))
	}
	matchConfig, err := validatePublicPolicyMatch(matchRule)
	if err != nil {
		return publicTrafficShaperRuleMutationInput{}, trafficShaperValidationError(err)
	}
	keyPartConfig := []publicRateLimitKeyPartConfig(nil)
	if scope == publicTrafficShaperBudgetScopePerKey {
		keyPartConfig, err = validateRateLimitKeyParts(keyParts)
		if err != nil {
			return publicTrafficShaperRuleMutationInput{}, trafficShaperValidationError(err)
		}
	} else {
		keyPartConfig = []publicRateLimitKeyPartConfig{}
	}
	matchJSON, err := json.Marshal(matchConfig)
	if err != nil {
		return publicTrafficShaperRuleMutationInput{}, connect.NewError(connect.CodeInternal, err)
	}
	keyPartsJSON, err := json.Marshal(keyPartConfig)
	if err != nil {
		return publicTrafficShaperRuleMutationInput{}, connect.NewError(connect.CodeInternal, err)
	}
	return publicTrafficShaperRuleMutationInput{
		Name:                   name,
		Priority:               priority,
		Enabled:                boolInt(enabled),
		BudgetScope:            scope,
		UploadBytesPerSecond:   uploadBytesPerSecond,
		DownloadBytesPerSecond: downloadBytesPerSecond,
		BurstBytes:             burstBytes,
		RequestExemptBytes:     requestExemptBytes,
		ResponseExemptBytes:    responseExemptBytes,
		MatchJSON:              string(matchJSON),
		KeyPartsJSON:           string(keyPartsJSON),
	}, nil
}

func trafficShaperValidationError(err error) error {
	if err == nil {
		return nil
	}
	msg := strings.ReplaceAll(err.Error(), "rate limit", "traffic shaper")
	if code := connect.CodeOf(err); code != connect.CodeUnknown {
		return connect.NewError(code, errors.New(msg))
	}
	return err
}

func publicTrafficShaperRuleRowToConfig(row db.PublicTrafficShaperRule) (publicTrafficShaperRuleConfig, error) {
	rule := publicTrafficShaperRuleConfig{
		ID:                     row.ID,
		Name:                   row.Name,
		Priority:               row.Priority,
		Enabled:                row.Enabled != 0,
		BudgetScope:            normalizePublicTrafficShaperBudgetScope(row.BudgetScope),
		UploadBytesPerSecond:   row.UploadBytesPerSecond,
		DownloadBytesPerSecond: row.DownloadBytesPerSecond,
		BurstBytes:             row.BurstBytes,
		RequestExemptBytes:     row.RequestExemptBytes,
		ResponseExemptBytes:    row.ResponseExemptBytes,
		CreatedAt:              row.CreatedAt,
		UpdatedAt:              row.UpdatedAt,
	}
	if row.MatchJson != "" {
		match, err := decodePublicPolicyMatchJSON(row.MatchJson)
		if err != nil {
			return publicTrafficShaperRuleConfig{}, fmt.Errorf("decode match: %w", err)
		}
		rule.Match = match
	}
	if row.KeyPartsJson != "" {
		if err := json.Unmarshal([]byte(row.KeyPartsJson), &rule.KeyParts); err != nil {
			return publicTrafficShaperRuleConfig{}, fmt.Errorf("decode key parts: %w", err)
		}
	}
	if rule.BudgetScope == publicTrafficShaperBudgetScopePerRequest {
		rule.KeyParts = nil
	} else if len(rule.KeyParts) == 0 {
		rule.KeyParts = []publicRateLimitKeyPartConfig{{Source: publicRateLimitKeySourceRemoteIP}}
	}
	rule.Fingerprint = publicTrafficShaperRuleFingerprint(rule)
	return rule, nil
}

func publicTrafficShaperRuleFingerprint(rule publicTrafficShaperRuleConfig) string {
	type fingerprint struct {
		BudgetScope            string
		UploadBytesPerSecond   int64
		DownloadBytesPerSecond int64
		BurstBytes             int64
		RequestExemptBytes     int64
		ResponseExemptBytes    int64
		Match                  publicPolicyMatchConfig
		KeyParts               []publicRateLimitKeyPartConfig
		UpdatedAt              int64
	}
	payload, _ := json.Marshal(fingerprint{
		BudgetScope:            rule.BudgetScope,
		UploadBytesPerSecond:   rule.UploadBytesPerSecond,
		DownloadBytesPerSecond: rule.DownloadBytesPerSecond,
		BurstBytes:             rule.BurstBytes,
		RequestExemptBytes:     rule.RequestExemptBytes,
		ResponseExemptBytes:    rule.ResponseExemptBytes,
		Match:                  rule.Match,
		KeyParts:               rule.KeyParts,
		UpdatedAt:              rule.UpdatedAt.UnixNano(),
	})
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:])
}

func trafficShaperBudgetScopeStringFromProto(scope p2pstreamv1.PublicTrafficShaperBudgetScope) (string, error) {
	switch scope {
	case p2pstreamv1.PublicTrafficShaperBudgetScope_PUBLIC_TRAFFIC_SHAPER_BUDGET_SCOPE_UNSPECIFIED,
		p2pstreamv1.PublicTrafficShaperBudgetScope_PUBLIC_TRAFFIC_SHAPER_BUDGET_SCOPE_PER_KEY:
		return publicTrafficShaperBudgetScopePerKey, nil
	case p2pstreamv1.PublicTrafficShaperBudgetScope_PUBLIC_TRAFFIC_SHAPER_BUDGET_SCOPE_PER_REQUEST:
		return publicTrafficShaperBudgetScopePerRequest, nil
	default:
		return "", connect.NewError(connect.CodeInvalidArgument, errors.New("traffic shaper budget scope is invalid"))
	}
}

func normalizePublicTrafficShaperBudgetScope(scope string) string {
	if scope == publicTrafficShaperBudgetScopePerRequest {
		return publicTrafficShaperBudgetScopePerRequest
	}
	return publicTrafficShaperBudgetScopePerKey
}

func protoTrafficShaperBudgetScopeFromString(scope string) p2pstreamv1.PublicTrafficShaperBudgetScope {
	if normalizePublicTrafficShaperBudgetScope(scope) == publicTrafficShaperBudgetScopePerRequest {
		return p2pstreamv1.PublicTrafficShaperBudgetScope_PUBLIC_TRAFFIC_SHAPER_BUDGET_SCOPE_PER_REQUEST
	}
	return p2pstreamv1.PublicTrafficShaperBudgetScope_PUBLIC_TRAFFIC_SHAPER_BUDGET_SCOPE_PER_KEY
}

func publicTrafficShaperRulesToProto(rows []db.PublicTrafficShaperRule) []*p2pstreamv1.PublicTrafficShaperRule {
	resp := make([]*p2pstreamv1.PublicTrafficShaperRule, 0, len(rows))
	for _, row := range rows {
		rule, err := publicTrafficShaperRuleRowToConfig(row)
		if err != nil {
			continue
		}
		resp = append(resp, publicTrafficShaperConfigToProto(rule))
	}
	return resp
}

func publicTrafficShaperConfigToProto(rule publicTrafficShaperRuleConfig) *p2pstreamv1.PublicTrafficShaperRule {
	return &p2pstreamv1.PublicTrafficShaperRule{
		Id:                     rule.ID,
		Name:                   rule.Name,
		Priority:               rule.Priority,
		Enabled:                rule.Enabled,
		BudgetScope:            protoTrafficShaperBudgetScopeFromString(rule.BudgetScope),
		UploadBytesPerSecond:   rule.UploadBytesPerSecond,
		DownloadBytesPerSecond: rule.DownloadBytesPerSecond,
		BurstBytes:             rule.BurstBytes,
		RequestExemptBytes:     rule.RequestExemptBytes,
		ResponseExemptBytes:    rule.ResponseExemptBytes,
		KeyParts:               rateLimitKeyPartsToProto(rule.KeyParts),
		CreatedAtUnixMillis:    rule.CreatedAt.UnixMilli(),
		UpdatedAtUnixMillis:    rule.UpdatedAt.UnixMilli(),
		MatchRule:              publicPolicyMatchRuleToProto(rule.Match),
	}
}

func (a *App) CreatePublicTrafficShaperRule(
	ctx context.Context,
	req *connect.Request[p2pstreamv1.CreatePublicTrafficShaperRuleRequest],
) (*connect.Response[p2pstreamv1.CreatePublicTrafficShaperRuleResponse], error) {
	if _, err := a.requireAdmin(ctx, req.Header()); err != nil {
		return nil, err
	}
	if err := rejectRemovedPolicyMatchField(req.Msg, 10); err != nil {
		return nil, err
	}
	params, err := validatePublicTrafficShaperRuleInput(
		req.Msg.Name,
		req.Msg.Priority,
		req.Msg.Enabled,
		req.Msg.BudgetScope,
		req.Msg.UploadBytesPerSecond,
		req.Msg.DownloadBytesPerSecond,
		req.Msg.BurstBytes,
		req.Msg.RequestExemptBytes,
		req.Msg.ResponseExemptBytes,
		req.Msg.KeyParts,
		req.Msg.MatchRule,
	)
	if err != nil {
		return nil, err
	}
	row, err := a.DB.CreatePublicTrafficShaperRule(ctx, db.CreatePublicTrafficShaperRuleParams{
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
		return nil, publicDBError(err)
	}
	if err := a.refreshPublicProxySnapshot(ctx); err != nil {
		return nil, err
	}
	rule, err := publicTrafficShaperRuleRowToConfig(row)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	return connect.NewResponse(&p2pstreamv1.CreatePublicTrafficShaperRuleResponse{Rule: publicTrafficShaperConfigToProto(rule)}), nil
}

func (a *App) UpdatePublicTrafficShaperRule(
	ctx context.Context,
	req *connect.Request[p2pstreamv1.UpdatePublicTrafficShaperRuleRequest],
) (*connect.Response[p2pstreamv1.UpdatePublicTrafficShaperRuleResponse], error) {
	if _, err := a.requireAdmin(ctx, req.Header()); err != nil {
		return nil, err
	}
	if err := rejectRemovedPolicyMatchField(req.Msg, 11); err != nil {
		return nil, err
	}
	params, err := validatePublicTrafficShaperRuleInput(
		req.Msg.Name,
		req.Msg.Priority,
		req.Msg.Enabled,
		req.Msg.BudgetScope,
		req.Msg.UploadBytesPerSecond,
		req.Msg.DownloadBytesPerSecond,
		req.Msg.BurstBytes,
		req.Msg.RequestExemptBytes,
		req.Msg.ResponseExemptBytes,
		req.Msg.KeyParts,
		req.Msg.MatchRule,
	)
	if err != nil {
		return nil, err
	}
	row, err := a.DB.UpdatePublicTrafficShaperRule(ctx, db.UpdatePublicTrafficShaperRuleParams{
		ID:                     req.Msg.Id,
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
		return nil, publicDBError(err)
	}
	if err := a.refreshPublicProxySnapshot(ctx); err != nil {
		return nil, err
	}
	rule, err := publicTrafficShaperRuleRowToConfig(row)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	return connect.NewResponse(&p2pstreamv1.UpdatePublicTrafficShaperRuleResponse{Rule: publicTrafficShaperConfigToProto(rule)}), nil
}

func (a *App) DeletePublicTrafficShaperRule(
	ctx context.Context,
	req *connect.Request[p2pstreamv1.DeletePublicTrafficShaperRuleRequest],
) (*connect.Response[p2pstreamv1.DeletePublicTrafficShaperRuleResponse], error) {
	if _, err := a.requireAdmin(ctx, req.Header()); err != nil {
		return nil, err
	}
	if err := a.DB.DeletePublicTrafficShaperRule(ctx, req.Msg.Id); err != nil {
		return nil, publicDBError(err)
	}
	if err := a.refreshPublicProxySnapshot(ctx); err != nil {
		return nil, err
	}
	return connect.NewResponse(&p2pstreamv1.DeletePublicTrafficShaperRuleResponse{}), nil
}
