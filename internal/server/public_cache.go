package server

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/textproto"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"connectrpc.com/connect"
	"github.com/rs/zerolog/log"

	p2pstreamv1 "p2pstream/gen/proto/p2pstream/v1"
	"p2pstream/internal/config"
	"p2pstream/internal/db"
)

const (
	publicCacheTTLModeFixed  = "fixed"
	publicCacheTTLModeOrigin = "origin"

	publicCacheQueryModeFull      = "full"
	publicCacheQueryModeIgnore    = "ignore"
	publicCacheQueryModeAllowlist = "allowlist"
	publicCacheQueryModeDenylist  = "denylist"

	publicCacheScopeSelectedBackend = "selected_backend"
	publicCacheScopeRoute           = "route"

	publicCacheStatusHit         = "hit"
	publicCacheStatusMiss        = "miss"
	publicCacheStatusBypass      = "bypass"
	publicCacheStatusExpired     = "expired"
	publicCacheStatusStored      = "stored"
	publicCacheStatusStoreFailed = "store_failed"

	defaultPublicCacheTTLMillis             = int64(3600000)
	defaultPublicCacheMaxObjectBytes        = int64(104857600)
	defaultPublicCacheMaxDiskBytes          = int64(1073741824)
	defaultPublicCacheMaxMemoryBytes        = int64(134217728)
	defaultPublicCacheMemoryHotObjectBytes  = int64(262144)
	defaultPublicCacheMaxEntries            = int64(100000)
	defaultPublicCacheCleanupIntervalMillis = int64(60000)
	minPublicCacheTTLMillis                 = int64(1000)
	maxPublicCacheTTLMillis                 = int64(31536000000)
	minPublicCacheCleanupIntervalMillis     = int64(1000)
	maxPublicCacheCleanupIntervalMillis     = int64(3600000)
	maxPublicCacheHeaderBytes               = 256 * 1024
	maxPublicCacheListItems                 = 64
)

var defaultPublicCacheStatusCodes = []int64{200, 203, 204, 301, 308}
var defaultPublicCacheVaryHeaders = []string{"Accept-Encoding"}

type publicCacheSettingsConfig struct {
	Enabled                 bool
	MaxDiskBytes            int64
	MaxMemoryBytes          int64
	MemoryHotObjectMaxBytes int64
	MaxEntries              int64
	CleanupInterval         time.Duration
	CreatedAt               time.Time
	UpdatedAt               time.Time
}

type publicCacheRuleConfig struct {
	ID                   int64
	Name                 string
	Priority             int64
	Enabled              bool
	Match                publicPolicyMatchConfig
	RouteIDs             []int64
	TargetIDs            []int64
	Scope                string
	TTLMode              string
	TTL                  time.Duration
	QueryMode            string
	QueryParams          []string
	VaryHeaders          []string
	CacheStatusCodes     []int64
	MaxObjectBytes       int64
	AddCacheStatusHeader bool
	AllowCookieRequests  bool
	CreatedAt            time.Time
	UpdatedAt            time.Time
	Fingerprint          string
}

type publicCacheRuleMutationInput struct {
	Name                 string
	Priority             int64
	Enabled              int64
	MatchJSON            string
	RouteIDsJSON         string
	TargetIDsJSON        string
	Scope                string
	TTLMode              string
	TTLMillis            int64
	QueryMode            string
	QueryParamsJSON      string
	VaryHeadersJSON      string
	CacheStatusCodesJSON string
	MaxObjectBytes       int64
	AddCacheStatusHeader int64
	AllowCookieRequests  int64
}

type publicCacheDecision struct {
	Rule           publicCacheRuleConfig
	Status         string
	KeyDigest      string
	QueryKey       string
	Host           string
	Path           string
	RouteID        sql.NullInt64
	RouteTargetID  sql.NullInt64
	Entry          *db.PublicCacheEntry
	BypassReason   string
	CookieRequest  bool
	Cacheable      bool
	StoredBytes    int64
	LookupDuration time.Duration
}

type publicProxyCache struct {
	dir string

	mu              sync.Mutex
	settings        publicCacheSettingsConfig
	lastCleanup     time.Time
	memoryEntries   map[string]*publicCacheMemoryEntry
	memoryBytes     int64
	lastFingerprint string
}

type publicCacheMemoryEntry struct {
	body         []byte
	lastAccessed time.Time
}

func newPublicProxyCache(cacheDir string) *publicProxyCache {
	if strings.TrimSpace(cacheDir) == "" {
		cacheDir = filepath.Join(config.DefaultConfigDir, "cache", "public")
	}
	return &publicProxyCache{
		dir:           cacheDir,
		settings:      defaultPublicCacheSettings(),
		memoryEntries: make(map[string]*publicCacheMemoryEntry),
	}
}

func defaultPublicCacheSettings() publicCacheSettingsConfig {
	now := time.Now()
	return publicCacheSettingsConfig{
		Enabled:                 true,
		MaxDiskBytes:            defaultPublicCacheMaxDiskBytes,
		MaxMemoryBytes:          defaultPublicCacheMaxMemoryBytes,
		MemoryHotObjectMaxBytes: defaultPublicCacheMemoryHotObjectBytes,
		MaxEntries:              defaultPublicCacheMaxEntries,
		CleanupInterval:         time.Duration(defaultPublicCacheCleanupIntervalMillis) * time.Millisecond,
		CreatedAt:               now,
		UpdatedAt:               now,
	}
}

func (c *publicProxyCache) reconcile(settings publicCacheSettingsConfig) {
	if c == nil {
		return
	}
	c.mu.Lock()
	c.settings = settings
	if !settings.Enabled || settings.MaxMemoryBytes <= 0 {
		c.memoryEntries = make(map[string]*publicCacheMemoryEntry)
		c.memoryBytes = 0
	} else {
		c.pruneMemoryLocked()
	}
	c.mu.Unlock()
}

func (c *publicProxyCache) memoryHotObjectMaxBytesSnapshot() int64 {
	if c == nil {
		return 0
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.settings.Enabled || c.settings.MaxMemoryBytes <= 0 || c.settings.MemoryHotObjectMaxBytes <= 0 {
		return 0
	}
	return c.settings.MemoryHotObjectMaxBytes
}

func (c *publicProxyCache) cacheDir() string {
	if c == nil || strings.TrimSpace(c.dir) == "" {
		return filepath.Join(config.DefaultConfigDir, "cache", "public")
	}
	return c.dir
}

func (c *publicProxyCache) bodyPath(keyDigest string) string {
	shardA := "00"
	shardB := "00"
	if len(keyDigest) >= 4 {
		shardA = keyDigest[:2]
		shardB = keyDigest[2:4]
	}
	return filepath.Join(c.cacheDir(), shardA, shardB, keyDigest+".body")
}

func (c *publicProxyCache) tempDir() string {
	return filepath.Join(c.cacheDir(), "tmp")
}

func (c *publicProxyCache) getMemory(key string) []byte {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	entry := c.memoryEntries[key]
	if entry == nil {
		return nil
	}
	entry.lastAccessed = time.Now()
	return append([]byte(nil), entry.body...)
}

func (c *publicProxyCache) putMemory(key string, body []byte) {
	if c == nil || key == "" || len(body) == 0 {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.settings.Enabled || c.settings.MaxMemoryBytes <= 0 || int64(len(body)) > c.settings.MemoryHotObjectMaxBytes {
		return
	}
	if existing := c.memoryEntries[key]; existing != nil {
		c.memoryBytes -= int64(len(existing.body))
	}
	c.memoryEntries[key] = &publicCacheMemoryEntry{body: append([]byte(nil), body...), lastAccessed: time.Now()}
	c.memoryBytes += int64(len(body))
	c.pruneMemoryLocked()
}

func (c *publicProxyCache) deleteMemory(key string) {
	if c == nil || key == "" {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if existing := c.memoryEntries[key]; existing != nil {
		c.memoryBytes -= int64(len(existing.body))
		delete(c.memoryEntries, key)
	}
}

func (c *publicProxyCache) pruneMemoryLocked() {
	if c.settings.MaxMemoryBytes <= 0 {
		c.memoryEntries = make(map[string]*publicCacheMemoryEntry)
		c.memoryBytes = 0
		return
	}
	if c.memoryBytes <= c.settings.MaxMemoryBytes {
		return
	}
	type item struct {
		key string
		at  time.Time
	}
	items := make([]item, 0, len(c.memoryEntries))
	for key, entry := range c.memoryEntries {
		items = append(items, item{key: key, at: entry.lastAccessed})
	}
	sort.Slice(items, func(i, j int) bool { return items[i].at.Before(items[j].at) })
	for _, item := range items {
		if c.memoryBytes <= c.settings.MaxMemoryBytes {
			break
		}
		entry := c.memoryEntries[item.key]
		if entry == nil {
			continue
		}
		c.memoryBytes -= int64(len(entry.body))
		delete(c.memoryEntries, item.key)
	}
}

func (c *publicProxyCache) maybeCleanup(ctx context.Context, q *db.DB) {
	if c == nil || q == nil {
		return
	}
	now := time.Now()
	c.mu.Lock()
	interval := c.settings.CleanupInterval
	if interval <= 0 {
		interval = time.Duration(defaultPublicCacheCleanupIntervalMillis) * time.Millisecond
	}
	if !c.lastCleanup.IsZero() && now.Sub(c.lastCleanup) < interval {
		c.mu.Unlock()
		return
	}
	c.lastCleanup = now
	settings := c.settings
	c.mu.Unlock()

	rows, err := q.DeleteExpiredPublicCacheEntries(ctx, now)
	if err == nil {
		c.removeCacheBodies(rows)
	}

	sum, err := q.SumPublicCacheBytes(ctx)
	if err != nil {
		return
	}
	totalBytes := sum.TotalBytes
	entryCount := sum.EntryCount
	for (settings.MaxDiskBytes > 0 && totalBytes > settings.MaxDiskBytes) || (settings.MaxEntries > 0 && entryCount > settings.MaxEntries) {
		rows, err := q.ListPublicCacheEntriesForCleanup(ctx, 100)
		if err != nil || len(rows) == 0 {
			return
		}
		for _, row := range rows {
			if err := q.DeletePublicCacheEntry(ctx, row.KeyDigest); err != nil {
				continue
			}
			c.deleteMemory(row.KeyDigest)
			_ = os.Remove(row.BodyPath)
			totalBytes -= row.SizeBytes
			entryCount--
			if (settings.MaxDiskBytes <= 0 || totalBytes <= settings.MaxDiskBytes) && (settings.MaxEntries <= 0 || entryCount <= settings.MaxEntries) {
				break
			}
		}
	}
}

func (c *publicProxyCache) removeCacheBodies(rows []db.DeleteExpiredPublicCacheEntriesRow) {
	for _, row := range rows {
		c.deleteMemory(row.KeyDigest)
		_ = os.Remove(row.BodyPath)
	}
}

func (a *App) checkPublicCache(r *http.Request, resolution publicRouteResolution) publicCacheDecision {
	startedAt := time.Now()
	if a == nil || a.PublicCache == nil || a.DB == nil {
		return publicCacheDecision{Status: publicCacheStatusBypass, BypassReason: "cache_unavailable"}
	}
	if resolution.Target.TargetType != publicRouteTargetTypeProxy {
		return publicCacheDecision{Status: publicCacheStatusBypass, BypassReason: "target_not_proxy"}
	}
	a.proxyMu.Lock()
	snap := a.publicSnapshot
	a.proxyMu.Unlock()
	if snap == nil || !snap.CacheSettings.Enabled || len(snap.CacheRules) == 0 {
		return publicCacheDecision{Status: publicCacheStatusBypass, BypassReason: "cache_disabled"}
	}
	decision := publicCacheBaseDecision(r, resolution)
	if reason := publicCacheRequestBypassReason(r); reason != "" {
		decision.Status = publicCacheStatusBypass
		decision.BypassReason = reason
		return decision
	}
	if publicRouteAllowsEncodedPathSeparators(resolution.Route) && publicRequestHasEncodedPathSeparator(r) {
		decision.Status = publicCacheStatusBypass
		decision.BypassReason = "encoded_path"
		decision.LookupDuration = time.Since(startedAt)
		return decision
	}

	rule, ok := selectPublicCacheRule(snap.CacheRules, resolution.Listener, r, resolution)
	if !ok {
		decision.Status = publicCacheStatusBypass
		decision.BypassReason = "no_rule"
		return decision
	}
	decision.Rule = rule
	if reason := publicCacheRuleBypassReason(rule, r); reason != "" {
		decision.Status = publicCacheStatusBypass
		decision.BypassReason = reason
		return decision
	}
	if rule.Scope == publicCacheScopeRoute {
		decision.RouteTargetID = sql.NullInt64{}
	}
	decision.Cacheable = true
	queryKey, err := publicCacheQueryKey(r.URL, rule)
	if err != nil {
		decision.Status = publicCacheStatusBypass
		decision.BypassReason = "invalid_query"
		return decision
	}
	decision.QueryKey = queryKey

	a.PublicCache.maybeCleanup(context.Background(), a.DB)
	candidates, err := a.DB.ListPublicCacheEntryCandidates(context.Background(), db.ListPublicCacheEntryCandidatesParams{
		RuleID:           rule.ID,
		ListenerProtocol: resolution.Listener.Protocol,
		Host:             decision.Host,
		Path:             decision.Path,
		QueryKey:         queryKey,
		RouteID:          sql.NullInt64{Int64: nullInt64Value(decision.RouteID), Valid: true},
		RouteTargetID:    sql.NullInt64{Int64: nullInt64Value(decision.RouteTargetID), Valid: true},
		ExpiresAt:        time.Now(),
	})
	if err != nil {
		decision.Status = publicCacheStatusMiss
		decision.LookupDuration = time.Since(startedAt)
		return decision
	}
	for _, entry := range candidates {
		varyHeaders := publicCacheStringListFromJSON(entry.VaryHeadersJson)
		keyDigest := publicCacheKeyDigest(r, resolution, rule, queryKey, varyHeaders)
		if keyDigest != entry.KeyDigest {
			continue
		}
		if _, err := os.Stat(entry.BodyPath); err != nil {
			_ = a.DB.DeletePublicCacheEntry(context.Background(), entry.KeyDigest)
			a.PublicCache.deleteMemory(entry.KeyDigest)
			continue
		}
		decision.Status = publicCacheStatusHit
		decision.KeyDigest = entry.KeyDigest
		entryCopy := entry
		decision.Entry = &entryCopy
		decision.LookupDuration = time.Since(startedAt)
		_ = a.DB.TouchPublicCacheEntry(context.Background(), entry.KeyDigest)
		return decision
	}
	decision.Status = publicCacheStatusMiss
	decision.KeyDigest = publicCacheKeyDigest(r, resolution, rule, queryKey, rule.VaryHeaders)
	decision.LookupDuration = time.Since(startedAt)
	return decision
}

func publicCacheBaseDecision(r *http.Request, resolution publicRouteResolution) publicCacheDecision {
	path := r.URL.EscapedPath()
	if path == "" {
		path = "/"
	}
	routeID := resolution.RouteID
	if !routeID.Valid && resolution.Route.ID != 0 {
		routeID = sql.NullInt64{Int64: resolution.Route.ID, Valid: true}
	}
	routeTargetID := resolution.RouteTargetID
	if !routeTargetID.Valid && resolution.Target.ID != 0 {
		routeTargetID = sql.NullInt64{Int64: resolution.Target.ID, Valid: true}
	}
	return publicCacheDecision{
		Host:          normalizeRequestHost(r.Host),
		Path:          path,
		RouteID:       routeID,
		RouteTargetID: routeTargetID,
		CookieRequest: r.Header.Get("Cookie") != "",
	}
}

func publicCacheRequestBypassReason(r *http.Request) string {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		return "method"
	}
	if r.Header.Get("Authorization") != "" {
		return "authorization"
	}
	if r.Header.Get("Range") != "" {
		return "range"
	}
	if strings.EqualFold(r.Header.Get("Connection"), "upgrade") || r.Header.Get("Upgrade") != "" {
		return "upgrade"
	}
	if len(r.TransferEncoding) > 0 || (r.Body != nil && r.Body != http.NoBody && r.ContentLength != 0) {
		return "request_body"
	}
	return ""
}

func publicCacheRuleBypassReason(rule publicCacheRuleConfig, r *http.Request) string {
	if r.Header.Get("Cookie") != "" && !rule.AllowCookieRequests {
		return "cookie"
	}
	return ""
}

func selectPublicCacheRule(rules []publicCacheRuleConfig, listener publicListenerConfig, r *http.Request, resolution publicRouteResolution) (publicCacheRuleConfig, bool) {
	if len(rules) == 0 {
		return publicCacheRuleConfig{}, false
	}
	ordered := append([]publicCacheRuleConfig(nil), rules...)
	sort.SliceStable(ordered, func(i, j int) bool {
		if ordered[i].Priority == ordered[j].Priority {
			return ordered[i].ID < ordered[j].ID
		}
		return ordered[i].Priority < ordered[j].Priority
	})
	for _, rule := range ordered {
		if !rule.Enabled || !rule.matches(listener, r, resolution) {
			continue
		}
		return rule, true
	}
	return publicCacheRuleConfig{}, false
}

func (rule publicCacheRuleConfig) matches(listener publicListenerConfig, r *http.Request, resolution publicRouteResolution) bool {
	if !(publicRateLimitRuleConfig{Match: rule.Match}).matches(listener, r) {
		return false
	}
	if len(rule.RouteIDs) > 0 {
		routeID := resolution.Route.ID
		if routeID == 0 && resolution.RouteID.Valid {
			routeID = resolution.RouteID.Int64
		}
		if !int64InSlice(routeID, rule.RouteIDs) {
			return false
		}
	}
	if len(rule.TargetIDs) > 0 {
		targetID := resolution.Target.ID
		if targetID == 0 && resolution.RouteTargetID.Valid {
			targetID = resolution.RouteTargetID.Int64
		}
		if !int64InSlice(targetID, rule.TargetIDs) {
			return false
		}
	}
	return true
}

func int64InSlice(value int64, values []int64) bool {
	for _, item := range values {
		if item == value {
			return true
		}
	}
	return false
}

func publicCacheQueryKey(u *url.URL, rule publicCacheRuleConfig) (string, error) {
	if u == nil {
		return "", nil
	}
	switch rule.QueryMode {
	case publicCacheQueryModeIgnore:
		return "", nil
	case publicCacheQueryModeAllowlist:
		return filteredQuery(u.Query(), rule.QueryParams, true), nil
	case publicCacheQueryModeDenylist:
		return filteredQuery(u.Query(), rule.QueryParams, false), nil
	default:
		values, err := url.ParseQuery(u.RawQuery)
		if err != nil {
			return "", err
		}
		return values.Encode(), nil
	}
}

func filteredQuery(values url.Values, params []string, allow bool) string {
	filter := make(map[string]struct{}, len(params))
	for _, param := range params {
		filter[param] = struct{}{}
	}
	out := url.Values{}
	for key, vals := range values {
		_, listed := filter[key]
		if (allow && !listed) || (!allow && listed) {
			continue
		}
		out[key] = append([]string(nil), vals...)
	}
	return out.Encode()
}

func publicCacheKeyDigest(r *http.Request, resolution publicRouteResolution, rule publicCacheRuleConfig, queryKey string, varyHeaders []string) string {
	routeID := int64(0)
	routeTargetID := int64(0)
	if resolution.Route.ID != 0 {
		routeID = resolution.Route.ID
	} else if resolution.RouteID.Valid {
		routeID = resolution.RouteID.Int64
	}
	if rule.Scope == publicCacheScopeSelectedBackend {
		if resolution.Target.ID != 0 {
			routeTargetID = resolution.Target.ID
		} else if resolution.RouteTargetID.Valid {
			routeTargetID = resolution.RouteTargetID.Int64
		}
	}
	parts := []string{
		"v2",
		resolution.Listener.Protocol,
		normalizeRequestHost(r.Host),
		r.URL.EscapedPath(),
		queryKey,
		rule.Scope,
		strconv.FormatInt(routeID, 10),
		strconv.FormatInt(routeTargetID, 10),
	}
	for _, header := range normalizePublicCacheHeaderList(varyHeaders) {
		parts = append(parts, textproto.CanonicalMIMEHeaderKey(header), r.Header.Get(header))
	}
	sum := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return hex.EncodeToString(sum[:])
}

func (a *App) servePublicCacheHit(w http.ResponseWriter, r *http.Request, resolution publicRouteResolution, trace *trafficRequestTrace, shaper *publicTrafficShaperDecision, decision publicCacheDecision, observability proxyRequestObservability) {
	startedAt := time.Now()
	statusCode := http.StatusOK
	errorKind := ""
	defer func() {
		if trace != nil {
			attrs := publicCacheTraceAttributes(decision)
			attrs["handler"] = "cache"
			trace.emit(p2pstreamv1.TrafficTraceStage_TRAFFIC_TRACE_STAGE_RESPONSE_SENT, &resolution, nil, statusCode, errorKind, w.Header(), attrs)
		}
		a.recordProxyRequestEventWithRouteTargetCacheAndContext(
			context.Background(),
			statusCode,
			time.Since(startedAt),
			errorKind,
			resolution.ListenerID,
			resolution.RouteID,
			resolution.RouteTargetID,
			sql.NullInt64{},
			"",
			sql.NullInt64{},
			sql.NullInt64{Int64: decision.Rule.ID, Valid: decision.Rule.ID != 0},
			publicCacheStatusHit,
			uint64FromInt64(decision.Entry.SizeBytes),
			observability.requestBytesValue(),
			observability.responseBytesValue(),
			proxyRequestContextFromHTTP(r),
		)
	}()
	if decision.Entry == nil {
		statusCode = http.StatusInternalServerError
		errorKind = "cache_entry_missing"
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}
	headers := publicCacheHeadersFromJSON(decision.Entry.ResponseHeadersJson)
	for key, values := range headers {
		for _, value := range values {
			w.Header().Add(key, value)
		}
	}
	age := int64(time.Since(decision.Entry.StoredAt).Seconds())
	if age < 0 {
		age = 0
	}
	w.Header().Set("Age", strconv.FormatInt(age, 10))
	if decision.Rule.AddCacheStatusHeader {
		w.Header().Set("X-p2pstream-Cache", "HIT")
	}
	statusCode = int(decision.Entry.StatusCode)
	w.WriteHeader(statusCode)
	if r.Method == http.MethodHead || decision.Entry.SizeBytes == 0 {
		return
	}
	if body := a.PublicCache.getMemory(decision.Entry.KeyDigest); len(body) > 0 {
		reader := io.NopCloser(bytes.NewReader(body))
		if shaper != nil {
			reader = shaper.wrapDownloadBody(r.Context(), reader)
		}
		defer reader.Close()
		_, _ = io.Copy(w, reader)
		return
	}
	file, err := os.Open(decision.Entry.BodyPath)
	if err != nil {
		statusCode = http.StatusInternalServerError
		errorKind = "cache_body_missing"
		return
	}
	var body io.ReadCloser = file
	if shaper != nil {
		body = shaper.wrapDownloadBody(r.Context(), body)
	}
	defer body.Close()
	_, _ = io.Copy(w, body)
}

func (a *App) capturePublicCacheResponseBody(ctx context.Context, r *http.Request, resolution publicRouteResolution, decision *publicCacheDecision, resp *http.Response, trace *trafficRequestTrace) io.ReadCloser {
	if a == nil || a.PublicCache == nil || a.DB == nil || decision == nil || !decision.Cacheable || resp == nil || resp.Body == nil || r.Method != http.MethodGet {
		if resp != nil {
			return resp.Body
		}
		return nil
	}
	ttl, varyHeaders, ok := publicCacheResponseEligibility(decision.Rule, resp)
	if !ok {
		return resp.Body
	}
	keyDigest := publicCacheKeyDigest(r, resolution, decision.Rule, decision.QueryKey, varyHeaders)
	bodyPath := a.PublicCache.bodyPath(keyDigest)
	if err := os.MkdirAll(filepath.Dir(bodyPath), 0700); err != nil {
		return resp.Body
	}
	if err := os.MkdirAll(a.PublicCache.tempDir(), 0700); err != nil {
		return resp.Body
	}
	tmp, err := os.CreateTemp(a.PublicCache.tempDir(), "cache-*.tmp")
	if err != nil {
		return resp.Body
	}
	headersJSON, err := publicCacheHeadersJSON(resp.Header)
	if err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmp.Name())
		return resp.Body
	}
	varyJSON := publicCacheStringListJSON(varyHeaders)
	memoryHotObjectMaxBytes := a.PublicCache.memoryHotObjectMaxBytesSnapshot()
	var memoryBuffer *bytes.Buffer
	if memoryHotObjectMaxBytes > 0 {
		memoryBuffer = &bytes.Buffer{}
	}
	return &publicCacheStoreReadCloser{
		ctx:                     ctx,
		source:                  resp.Body,
		tmp:                     tmp,
		tmpPath:                 tmp.Name(),
		finalPath:               bodyPath,
		app:                     a,
		trace:                   trace,
		decision:                decision,
		rule:                    decision.Rule,
		keyDigest:               keyDigest,
		resolution:              resolution,
		requestHost:             decision.Host,
		requestPath:             decision.Path,
		queryKey:                decision.QueryKey,
		statusCode:              int64(resp.StatusCode),
		headersJSON:             headersJSON,
		varyJSON:                varyJSON,
		maxBytes:                decision.Rule.MaxObjectBytes,
		expiresAt:               time.Now().Add(ttl),
		memoryBuffer:            memoryBuffer,
		memoryHotObjectMaxBytes: memoryHotObjectMaxBytes,
	}
}

type publicCacheStoreReadCloser struct {
	ctx                     context.Context
	source                  io.ReadCloser
	tmp                     *os.File
	tmpPath                 string
	finalPath               string
	app                     *App
	trace                   *trafficRequestTrace
	decision                *publicCacheDecision
	rule                    publicCacheRuleConfig
	keyDigest               string
	resolution              publicRouteResolution
	requestHost             string
	requestPath             string
	queryKey                string
	statusCode              int64
	headersJSON             string
	varyJSON                string
	maxBytes                int64
	expiresAt               time.Time
	memoryBuffer            *bytes.Buffer
	memoryHotObjectMaxBytes int64
	bytesWritten            int64
	tooLarge                bool
	committed               bool
	closed                  bool
}

func (r *publicCacheStoreReadCloser) Read(p []byte) (int, error) {
	n, err := r.source.Read(p)
	if n > 0 && !r.tooLarge {
		chunk := p[:n]
		r.bytesWritten += int64(n)
		if r.maxBytes > 0 && r.bytesWritten > r.maxBytes {
			r.tooLarge = true
		} else {
			if _, writeErr := r.tmp.Write(chunk); writeErr != nil {
				r.tooLarge = true
			}
			if r.memoryBuffer != nil && int64(r.memoryBuffer.Len()+n) <= r.memoryHotObjectMaxBytes {
				_, _ = r.memoryBuffer.Write(chunk)
			} else {
				r.memoryBuffer = nil
			}
		}
	}
	if err == io.EOF {
		r.commit()
	}
	return n, err
}

func (r *publicCacheStoreReadCloser) Close() error {
	if r.closed {
		return nil
	}
	r.closed = true
	sourceErr := r.source.Close()
	if !r.committed {
		r.discard()
	}
	return sourceErr
}

func (r *publicCacheStoreReadCloser) commit() {
	if r.committed {
		return
	}
	r.committed = true
	if r.tooLarge {
		r.markStoreFailed()
		r.discard()
		return
	}
	if err := r.tmp.Close(); err != nil {
		r.markStoreFailed()
		r.discard()
		return
	}
	if err := os.Rename(r.tmpPath, r.finalPath); err != nil {
		r.markStoreFailed()
		r.discard()
		return
	}
	routeID := r.resolution.RouteID
	if !routeID.Valid && r.resolution.Route.ID != 0 {
		routeID = sql.NullInt64{Int64: r.resolution.Route.ID, Valid: true}
	}
	routeTargetID := sql.NullInt64{}
	if r.rule.Scope == publicCacheScopeSelectedBackend {
		routeTargetID = r.resolution.RouteTargetID
		if !routeTargetID.Valid && r.resolution.Target.ID != 0 {
			routeTargetID = sql.NullInt64{Int64: r.resolution.Target.ID, Valid: true}
		}
	}
	_, err := r.app.DB.UpsertPublicCacheEntry(context.Background(), db.UpsertPublicCacheEntryParams{
		KeyDigest:           r.keyDigest,
		RuleID:              r.rule.ID,
		Scope:               r.rule.Scope,
		ListenerProtocol:    r.resolution.Listener.Protocol,
		Host:                r.requestHost,
		Path:                r.requestPath,
		QueryKey:            r.queryKey,
		RouteID:             routeID,
		RouteTargetID:       routeTargetID,
		Method:              http.MethodGet,
		VaryHeadersJson:     r.varyJSON,
		ResponseHeadersJson: r.headersJSON,
		StatusCode:          r.statusCode,
		BodyPath:            r.finalPath,
		SizeBytes:           r.bytesWritten,
		ExpiresAt:           r.expiresAt,
	})
	if err != nil {
		_ = os.Remove(r.finalPath)
		r.markStoreFailed()
		log.Warn().Err(err).Str("cache_key", r.keyDigest).Msg("Failed to store public cache entry")
		return
	}
	if r.decision != nil {
		r.decision.Status = publicCacheStatusStored
		r.decision.KeyDigest = r.keyDigest
		r.decision.StoredBytes = r.bytesWritten
	}
	if r.memoryBuffer != nil {
		r.app.PublicCache.putMemory(r.keyDigest, r.memoryBuffer.Bytes())
	}
	if r.trace != nil {
		resolution := r.resolution
		applyCacheResolutionFields(&resolution, publicCacheDecision{
			Rule:      r.rule,
			Status:    publicCacheStatusStored,
			KeyDigest: r.keyDigest,
		})
		r.trace.emit(
			p2pstreamv1.TrafficTraceStage_TRAFFIC_TRACE_STAGE_CACHE_STORED,
			&resolution,
			nil,
			int(r.statusCode),
			"",
			nil,
			map[string]string{"cache_bytes": strconv.FormatInt(r.bytesWritten, 10)},
		)
	}
}

func (r *publicCacheStoreReadCloser) markStoreFailed() {
	if r.decision != nil && r.decision.Status == publicCacheStatusMiss {
		r.decision.Status = publicCacheStatusStoreFailed
	}
}

func (r *publicCacheStoreReadCloser) discard() {
	if r.tmp != nil {
		_ = r.tmp.Close()
	}
	if r.tmpPath != "" {
		_ = os.Remove(r.tmpPath)
	}
}

func publicCacheResponseEligibility(rule publicCacheRuleConfig, resp *http.Response) (time.Duration, []string, bool) {
	if !int64InSlice(int64(resp.StatusCode), rule.CacheStatusCodes) {
		return 0, nil, false
	}
	if len(resp.Header.Values("Set-Cookie")) > 0 {
		return 0, nil, false
	}
	for _, directive := range publicCacheControlDirectives(resp.Header) {
		switch directive.Name {
		case "no-store", "private", "no-cache":
			return 0, nil, false
		}
	}
	if strings.Contains(strings.ToLower(resp.Header.Get("Content-Type")), "text/event-stream") {
		return 0, nil, false
	}
	varyHeaders, ok := publicCacheResponseVaryHeaders(rule.VaryHeaders, resp.Header.Values("Vary"))
	if !ok {
		return 0, nil, false
	}
	ttl := rule.TTL
	if ttl <= 0 {
		ttl = time.Duration(defaultPublicCacheTTLMillis) * time.Millisecond
	}
	if rule.TTLMode == publicCacheTTLModeOrigin {
		if originTTL, ok := publicCacheOriginTTL(resp.Header); ok {
			ttl = originTTL
		}
	}
	if ttl <= 0 {
		return 0, nil, false
	}
	return ttl, varyHeaders, true
}

func publicCacheOriginTTL(header http.Header) (time.Duration, bool) {
	var maxAge time.Duration
	var hasMaxAge bool
	for _, directive := range publicCacheControlDirectives(header) {
		seconds, err := strconv.ParseInt(strings.Trim(strings.TrimSpace(directive.Value), `"`), 10, 64)
		if err != nil {
			continue
		}
		ttl := time.Duration(seconds) * time.Second
		switch directive.Name {
		case "s-maxage":
			return ttl, true
		case "max-age":
			if !hasMaxAge {
				maxAge = ttl
				hasMaxAge = true
			}
		}
	}
	if hasMaxAge {
		return maxAge, true
	}
	if expires := header.Get("Expires"); expires != "" {
		if t, err := http.ParseTime(expires); err == nil {
			ttl := time.Until(t)
			if ttl > 0 {
				return ttl, true
			}
		}
	}
	return 0, false
}

type publicCacheControlDirective struct {
	Name  string
	Value string
}

func publicCacheControlDirectives(header http.Header) []publicCacheControlDirective {
	var directives []publicCacheControlDirective
	for _, value := range header.Values("Cache-Control") {
		for _, part := range strings.Split(value, ",") {
			part = strings.TrimSpace(part)
			if part == "" {
				continue
			}
			name, directiveValue, _ := strings.Cut(part, "=")
			name = strings.ToLower(strings.TrimSpace(name))
			if name == "" {
				continue
			}
			directives = append(directives, publicCacheControlDirective{
				Name:  name,
				Value: strings.TrimSpace(directiveValue),
			})
		}
	}
	return directives
}

func publicCacheResponseVaryHeaders(ruleHeaders []string, varyValues []string) ([]string, bool) {
	headers := normalizePublicCacheHeaderList(ruleHeaders)
	for _, header := range headers {
		if publicCacheSensitiveVaryHeader(header) {
			return nil, false
		}
	}
	for _, value := range varyValues {
		for _, part := range strings.Split(value, ",") {
			part = strings.TrimSpace(part)
			if part == "" {
				continue
			}
			if part == "*" {
				return nil, false
			}
			canonical := textproto.CanonicalMIMEHeaderKey(part)
			if publicCacheSensitiveVaryHeader(canonical) {
				return nil, false
			}
			headers = append(headers, canonical)
		}
	}
	return normalizePublicCacheHeaderList(headers), true
}

func publicCacheSensitiveVaryHeader(header string) bool {
	switch strings.ToLower(textproto.CanonicalMIMEHeaderKey(strings.TrimSpace(header))) {
	case "cookie", "authorization", "set-cookie":
		return true
	default:
		return false
	}
}

func publicCacheHeadersJSON(header http.Header) (string, error) {
	resp := http.Header{}
	size := 0
	for key, values := range header {
		canonical := textproto.CanonicalMIMEHeaderKey(key)
		if publicCacheHopByHopHeader(canonical) || strings.EqualFold(canonical, "Set-Cookie") || strings.EqualFold(canonical, "Age") || strings.EqualFold(canonical, "X-p2pstream-Cache") {
			continue
		}
		for _, value := range values {
			size += len(canonical) + len(value)
			if size > maxPublicCacheHeaderBytes {
				return "", errors.New("cached response headers are too large")
			}
			resp.Add(canonical, value)
		}
	}
	data, err := json.Marshal(resp)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

func publicCacheHopByHopHeader(name string) bool {
	switch strings.ToLower(name) {
	case "connection", "keep-alive", "proxy-authenticate", "proxy-authorization", "te", "trailer", "transfer-encoding", "upgrade":
		return true
	default:
		return false
	}
}

func publicCacheHeadersFromJSON(raw string) http.Header {
	var header http.Header
	if err := json.Unmarshal([]byte(raw), &header); err != nil || header == nil {
		return http.Header{}
	}
	return header
}

func publicCacheStringListJSON(values []string) string {
	data, err := json.Marshal(values)
	if err != nil {
		return "[]"
	}
	return string(data)
}

func publicCacheStringListFromJSON(raw string) []string {
	var values []string
	if err := json.Unmarshal([]byte(raw), &values); err != nil {
		return nil
	}
	return values
}

func normalizePublicCacheHeaderList(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	resp := make([]string, 0, len(values))
	for _, value := range values {
		value = textproto.CanonicalMIMEHeaderKey(strings.TrimSpace(value))
		if value == "" || !validHTTPToken(value) {
			continue
		}
		if _, ok := seen[strings.ToLower(value)]; ok {
			continue
		}
		seen[strings.ToLower(value)] = struct{}{}
		resp = append(resp, value)
	}
	sort.Strings(resp)
	return resp
}

func publicCacheTraceAttributes(decision publicCacheDecision) map[string]string {
	attrs := map[string]string{
		"cache_status": decision.Status,
	}
	if decision.Rule.ID != 0 {
		attrs["cache_rule_id"] = strconv.FormatInt(decision.Rule.ID, 10)
		attrs["cache_rule_name"] = decision.Rule.Name
	}
	if decision.KeyDigest != "" {
		attrs["cache_key_digest"] = decision.KeyDigest
	}
	if decision.BypassReason != "" {
		attrs["cache_bypass_reason"] = decision.BypassReason
	}
	if decision.CookieRequest {
		attrs["cache_cookie_request"] = "true"
	}
	if decision.LookupDuration > 0 {
		attrs["cache_lookup_ms"] = strconv.FormatInt(decision.LookupDuration.Milliseconds(), 10)
	}
	return attrs
}

func applyCacheResolutionFields(resolution *publicRouteResolution, decision publicCacheDecision) {
	if resolution == nil {
		return
	}
	resolution.CacheRuleID = decision.Rule.ID
	resolution.CacheRuleName = decision.Rule.Name
	resolution.CacheStatus = decision.Status
	resolution.CacheKeyDigest = decision.KeyDigest
}

func cacheRuleID(decision *publicCacheDecision) sql.NullInt64 {
	if decision == nil || decision.Rule.ID == 0 {
		return sql.NullInt64{}
	}
	return sql.NullInt64{Int64: decision.Rule.ID, Valid: true}
}

func cacheStatus(decision *publicCacheDecision) string {
	if decision == nil {
		return ""
	}
	return decision.Status
}

func cacheBytes(decision *publicCacheDecision) uint64 {
	if decision == nil || decision.StoredBytes <= 0 {
		return 0
	}
	return uint64FromInt64(decision.StoredBytes)
}

func publicCacheSettingsRowToConfig(row db.PublicCacheSetting) publicCacheSettingsConfig {
	settings := publicCacheSettingsConfig{
		Enabled:                 row.Enabled != 0,
		MaxDiskBytes:            row.MaxDiskBytes,
		MaxMemoryBytes:          row.MaxMemoryBytes,
		MemoryHotObjectMaxBytes: row.MemoryHotObjectMaxBytes,
		MaxEntries:              row.MaxEntries,
		CleanupInterval:         time.Duration(row.CleanupIntervalMillis) * time.Millisecond,
		CreatedAt:               row.CreatedAt,
		UpdatedAt:               row.UpdatedAt,
	}
	if settings.MaxDiskBytes < 0 {
		settings.MaxDiskBytes = defaultPublicCacheMaxDiskBytes
	}
	if settings.MaxMemoryBytes < 0 {
		settings.MaxMemoryBytes = defaultPublicCacheMaxMemoryBytes
	}
	if settings.MemoryHotObjectMaxBytes < 0 {
		settings.MemoryHotObjectMaxBytes = defaultPublicCacheMemoryHotObjectBytes
	}
	if settings.MaxEntries <= 0 {
		settings.MaxEntries = defaultPublicCacheMaxEntries
	}
	if settings.CleanupInterval <= 0 {
		settings.CleanupInterval = time.Duration(defaultPublicCacheCleanupIntervalMillis) * time.Millisecond
	}
	return settings
}

func publicCacheSettingsConfigToProto(settings publicCacheSettingsConfig) *p2pstreamv1.PublicCacheSettings {
	return &p2pstreamv1.PublicCacheSettings{
		Enabled:                 settings.Enabled,
		MaxDiskBytes:            settings.MaxDiskBytes,
		MaxMemoryBytes:          settings.MaxMemoryBytes,
		MemoryHotObjectMaxBytes: settings.MemoryHotObjectMaxBytes,
		MaxEntries:              settings.MaxEntries,
		CleanupIntervalMillis:   settings.CleanupInterval.Milliseconds(),
		CreatedAtUnixMillis:     settings.CreatedAt.UnixMilli(),
		UpdatedAtUnixMillis:     settings.UpdatedAt.UnixMilli(),
	}
}

func publicCacheRuleRowToConfig(row db.PublicCacheRule) (publicCacheRuleConfig, error) {
	match, err := decodePublicPolicyMatchJSON(row.MatchJson)
	if err != nil {
		return publicCacheRuleConfig{}, err
	}
	routeIDs, err := publicCacheInt64ListFromJSON(row.RouteIdsJson)
	if err != nil {
		return publicCacheRuleConfig{}, err
	}
	targetIDs, err := publicCacheInt64ListFromJSON(row.TargetIdsJson)
	if err != nil {
		return publicCacheRuleConfig{}, err
	}
	queryParams := publicCacheStringListFromJSON(row.QueryParamsJson)
	varyHeaders := normalizePublicCacheHeaderList(publicCacheStringListFromJSON(row.VaryHeadersJson))
	statusCodes, err := publicCacheInt64ListFromJSON(row.CacheStatusCodesJson)
	if err != nil {
		return publicCacheRuleConfig{}, err
	}
	rule := publicCacheRuleConfig{
		ID:                   row.ID,
		Name:                 row.Name,
		Priority:             row.Priority,
		Enabled:              row.Enabled != 0,
		Match:                match,
		RouteIDs:             routeIDs,
		TargetIDs:            targetIDs,
		Scope:                normalizePublicCacheScope(row.Scope),
		TTLMode:              normalizePublicCacheTTLMode(row.TtlMode),
		TTL:                  time.Duration(normalizePublicCacheTTLMillis(row.TtlMillis)) * time.Millisecond,
		QueryMode:            normalizePublicCacheQueryMode(row.QueryMode),
		QueryParams:          normalizePublicCacheQueryParams(queryParams),
		VaryHeaders:          varyHeaders,
		CacheStatusCodes:     normalizePublicCacheStatusCodes(statusCodes),
		MaxObjectBytes:       normalizePublicCacheMaxObjectBytes(row.MaxObjectBytes),
		AddCacheStatusHeader: row.AddCacheStatusHeader != 0,
		AllowCookieRequests:  row.AllowCookieRequests != 0,
		CreatedAt:            row.CreatedAt,
		UpdatedAt:            row.UpdatedAt,
	}
	if len(rule.VaryHeaders) == 0 {
		rule.VaryHeaders = append([]string(nil), defaultPublicCacheVaryHeaders...)
	}
	rule.Fingerprint = publicCacheRuleFingerprint(rule)
	return rule, nil
}

func publicCacheRulesToProto(rows []db.PublicCacheRule) []*p2pstreamv1.PublicCacheRule {
	resp := make([]*p2pstreamv1.PublicCacheRule, 0, len(rows))
	for _, row := range rows {
		rule, err := publicCacheRuleRowToConfig(row)
		if err != nil {
			continue
		}
		resp = append(resp, publicCacheRuleConfigToProto(rule))
	}
	return resp
}

func publicCacheRuleConfigToProto(rule publicCacheRuleConfig) *p2pstreamv1.PublicCacheRule {
	return &p2pstreamv1.PublicCacheRule{
		Id:                   rule.ID,
		Name:                 rule.Name,
		Priority:             rule.Priority,
		Enabled:              rule.Enabled,
		RouteIds:             append([]int64(nil), rule.RouteIDs...),
		TargetIds:            append([]int64(nil), rule.TargetIDs...),
		Scope:                protoPublicCacheScope(rule.Scope),
		TtlMode:              protoPublicCacheTTLMode(rule.TTLMode),
		TtlMillis:            rule.TTL.Milliseconds(),
		QueryMode:            protoPublicCacheQueryMode(rule.QueryMode),
		QueryParams:          append([]string(nil), rule.QueryParams...),
		VaryHeaders:          append([]string(nil), rule.VaryHeaders...),
		CacheStatusCodes:     append([]int64(nil), rule.CacheStatusCodes...),
		MaxObjectBytes:       rule.MaxObjectBytes,
		AddCacheStatusHeader: rule.AddCacheStatusHeader,
		AllowCookieRequests:  rule.AllowCookieRequests,
		CreatedAtUnixMillis:  rule.CreatedAt.UnixMilli(),
		UpdatedAtUnixMillis:  rule.UpdatedAt.UnixMilli(),
		MatchRule:            publicPolicyMatchRuleToProto(rule.Match),
	}
}

func (a *App) ensurePublicCacheSettings(ctx context.Context) (db.PublicCacheSetting, error) {
	row, err := a.DB.GetPublicCacheSettings(ctx)
	if err == nil {
		return row, nil
	}
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return db.PublicCacheSetting{}, connect.NewError(connect.CodeInternal, err)
	}
	row, err = a.DB.UpsertPublicCacheSettingsDefaults(ctx)
	if err != nil {
		return db.PublicCacheSetting{}, connect.NewError(connect.CodeInternal, err)
	}
	return row, nil
}

func (a *App) validatePublicCacheRuleInput(ctx context.Context, name string, priority int64, enabled bool, routeIDs []int64, targetIDs []int64, scope p2pstreamv1.PublicCacheScope, ttlMode p2pstreamv1.PublicCacheTtlMode, ttlMillis int64, queryMode p2pstreamv1.PublicCacheQueryMode, queryParams []string, varyHeaders []string, statusCodes []int64, maxObjectBytes int64, addCacheStatusHeader bool, allowCookieRequests bool, allowCookieRequestsAcknowledged bool, matchRule *p2pstreamv1.PublicPolicyMatchRule) (publicCacheRuleMutationInput, error) {
	name, err := normalizePublicName(name)
	if err != nil {
		return publicCacheRuleMutationInput{}, connect.NewError(connect.CodeInvalidArgument, errors.New("cache rule name must be 1-64 alphanumeric, dot, dash, or underscore characters"))
	}
	if allowCookieRequests && !allowCookieRequestsAcknowledged {
		return publicCacheRuleMutationInput{}, connect.NewError(connect.CodeInvalidArgument, errors.New("cache rules that allow Cookie requests require explicit acknowledgement that Cookie is not part of the cache key"))
	}
	matchConfig, err := validatePublicPolicyMatch(matchRule)
	if err != nil {
		return publicCacheRuleMutationInput{}, err
	}
	routeIDs, err = a.validatePublicCacheRouteIDs(ctx, routeIDs)
	if err != nil {
		return publicCacheRuleMutationInput{}, err
	}
	targetIDs, err = a.validatePublicCacheTargetIDs(ctx, targetIDs)
	if err != nil {
		return publicCacheRuleMutationInput{}, err
	}
	scopeString, err := publicCacheScopeStringFromProto(scope)
	if err != nil {
		return publicCacheRuleMutationInput{}, err
	}
	ttlModeString, err := publicCacheTTLModeStringFromProto(ttlMode)
	if err != nil {
		return publicCacheRuleMutationInput{}, err
	}
	ttlMillis = normalizePublicCacheTTLMillis(ttlMillis)
	if ttlMillis < minPublicCacheTTLMillis || ttlMillis > maxPublicCacheTTLMillis {
		return publicCacheRuleMutationInput{}, connect.NewError(connect.CodeInvalidArgument, errors.New("cache TTL must be between 1 second and 365 days"))
	}
	queryModeString, err := publicCacheQueryModeStringFromProto(queryMode)
	if err != nil {
		return publicCacheRuleMutationInput{}, err
	}
	queryParams = normalizePublicCacheQueryParams(queryParams)
	if (queryModeString == publicCacheQueryModeAllowlist || queryModeString == publicCacheQueryModeDenylist) && len(queryParams) == 0 {
		return publicCacheRuleMutationInput{}, connect.NewError(connect.CodeInvalidArgument, errors.New("cache query allowlist and denylist modes require at least one query parameter"))
	}
	if len(queryParams) > maxPublicCacheListItems {
		return publicCacheRuleMutationInput{}, connect.NewError(connect.CodeInvalidArgument, errors.New("cache rule has too many query parameters"))
	}
	varyHeaders = normalizePublicCacheHeaderList(varyHeaders)
	if len(varyHeaders) == 0 {
		varyHeaders = append([]string(nil), defaultPublicCacheVaryHeaders...)
	}
	if len(varyHeaders) > maxPublicCacheListItems {
		return publicCacheRuleMutationInput{}, connect.NewError(connect.CodeInvalidArgument, errors.New("cache rule has too many vary headers"))
	}
	for _, header := range varyHeaders {
		if publicCacheSensitiveVaryHeader(header) {
			return publicCacheRuleMutationInput{}, connect.NewError(connect.CodeInvalidArgument, errors.New("cache vary headers must not include Cookie, Authorization, or Set-Cookie"))
		}
	}
	statusCodes = normalizePublicCacheStatusCodes(statusCodes)
	if len(statusCodes) > maxPublicCacheListItems {
		return publicCacheRuleMutationInput{}, connect.NewError(connect.CodeInvalidArgument, errors.New("cache rule has too many status codes"))
	}
	maxObjectBytes = normalizePublicCacheMaxObjectBytes(maxObjectBytes)
	matchJSON, err := json.Marshal(matchConfig)
	if err != nil {
		return publicCacheRuleMutationInput{}, connect.NewError(connect.CodeInternal, err)
	}
	routeIDsJSON, _ := json.Marshal(routeIDs)
	targetIDsJSON, _ := json.Marshal(targetIDs)
	queryParamsJSON, _ := json.Marshal(queryParams)
	varyHeadersJSON, _ := json.Marshal(varyHeaders)
	statusCodesJSON, _ := json.Marshal(statusCodes)
	return publicCacheRuleMutationInput{
		Name:                 name,
		Priority:             priority,
		Enabled:              boolInt(enabled),
		MatchJSON:            string(matchJSON),
		RouteIDsJSON:         string(routeIDsJSON),
		TargetIDsJSON:        string(targetIDsJSON),
		Scope:                scopeString,
		TTLMode:              ttlModeString,
		TTLMillis:            ttlMillis,
		QueryMode:            queryModeString,
		QueryParamsJSON:      string(queryParamsJSON),
		VaryHeadersJSON:      string(varyHeadersJSON),
		CacheStatusCodesJSON: string(statusCodesJSON),
		MaxObjectBytes:       maxObjectBytes,
		AddCacheStatusHeader: boolInt(addCacheStatusHeader),
		AllowCookieRequests:  boolInt(allowCookieRequests),
	}, nil
}

func (a *App) validatePublicCacheRouteIDs(ctx context.Context, ids []int64) ([]int64, error) {
	ids = normalizePublicCacheInt64List(ids)
	if len(ids) > maxPublicCacheListItems {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("cache rule has too many route filters"))
	}
	for _, id := range ids {
		if _, err := a.DB.GetPublicRoute(ctx, id); err != nil {
			return nil, publicDBError(err)
		}
	}
	return ids, nil
}

func (a *App) validatePublicCacheTargetIDs(ctx context.Context, ids []int64) ([]int64, error) {
	ids = normalizePublicCacheInt64List(ids)
	if len(ids) > maxPublicCacheListItems {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("cache rule has too many target filters"))
	}
	for _, id := range ids {
		if _, err := a.DB.GetPublicRouteTarget(ctx, id); err != nil {
			return nil, publicDBError(err)
		}
	}
	return ids, nil
}

func (a *App) CreatePublicCacheRule(ctx context.Context, req *connect.Request[p2pstreamv1.CreatePublicCacheRuleRequest]) (*connect.Response[p2pstreamv1.CreatePublicCacheRuleResponse], error) {
	if _, err := a.requireAdmin(ctx, req.Header()); err != nil {
		return nil, err
	}
	if err := rejectRemovedPolicyMatchField(req.Msg, 4); err != nil {
		return nil, err
	}
	params, err := a.validatePublicCacheRuleInput(ctx, req.Msg.Name, req.Msg.Priority, req.Msg.Enabled, req.Msg.RouteIds, req.Msg.TargetIds, req.Msg.Scope, req.Msg.TtlMode, req.Msg.TtlMillis, req.Msg.QueryMode, req.Msg.QueryParams, req.Msg.VaryHeaders, req.Msg.CacheStatusCodes, req.Msg.MaxObjectBytes, req.Msg.AddCacheStatusHeader, req.Msg.AllowCookieRequests, req.Msg.AllowCookieRequestsAcknowledged, req.Msg.MatchRule)
	if err != nil {
		return nil, err
	}
	row, err := a.DB.CreatePublicCacheRule(ctx, cacheCreateParams(params))
	if err != nil {
		return nil, publicDBError(err)
	}
	if err := a.refreshPublicProxySnapshot(ctx); err != nil {
		return nil, err
	}
	rule, err := publicCacheRuleRowToConfig(row)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	return connect.NewResponse(&p2pstreamv1.CreatePublicCacheRuleResponse{Rule: publicCacheRuleConfigToProto(rule)}), nil
}

func (a *App) UpdatePublicCacheRule(ctx context.Context, req *connect.Request[p2pstreamv1.UpdatePublicCacheRuleRequest]) (*connect.Response[p2pstreamv1.UpdatePublicCacheRuleResponse], error) {
	if _, err := a.requireAdmin(ctx, req.Header()); err != nil {
		return nil, err
	}
	if err := rejectRemovedPolicyMatchField(req.Msg, 5); err != nil {
		return nil, err
	}
	params, err := a.validatePublicCacheRuleInput(ctx, req.Msg.Name, req.Msg.Priority, req.Msg.Enabled, req.Msg.RouteIds, req.Msg.TargetIds, req.Msg.Scope, req.Msg.TtlMode, req.Msg.TtlMillis, req.Msg.QueryMode, req.Msg.QueryParams, req.Msg.VaryHeaders, req.Msg.CacheStatusCodes, req.Msg.MaxObjectBytes, req.Msg.AddCacheStatusHeader, req.Msg.AllowCookieRequests, req.Msg.AllowCookieRequestsAcknowledged, req.Msg.MatchRule)
	if err != nil {
		return nil, err
	}
	row, err := a.DB.UpdatePublicCacheRule(ctx, cacheUpdateParams(req.Msg.Id, params))
	if err != nil {
		return nil, publicDBError(err)
	}
	if err := a.refreshPublicProxySnapshot(ctx); err != nil {
		return nil, err
	}
	rule, err := publicCacheRuleRowToConfig(row)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	return connect.NewResponse(&p2pstreamv1.UpdatePublicCacheRuleResponse{Rule: publicCacheRuleConfigToProto(rule)}), nil
}

func (a *App) DeletePublicCacheRule(ctx context.Context, req *connect.Request[p2pstreamv1.DeletePublicCacheRuleRequest]) (*connect.Response[p2pstreamv1.DeletePublicCacheRuleResponse], error) {
	if _, err := a.requireAdmin(ctx, req.Header()); err != nil {
		return nil, err
	}
	if err := a.DB.DeletePublicCacheRule(ctx, req.Msg.Id); err != nil {
		return nil, publicDBError(err)
	}
	if err := a.refreshPublicProxySnapshot(ctx); err != nil {
		return nil, err
	}
	return connect.NewResponse(&p2pstreamv1.DeletePublicCacheRuleResponse{}), nil
}

func (a *App) UpdatePublicCacheSettings(ctx context.Context, req *connect.Request[p2pstreamv1.UpdatePublicCacheSettingsRequest]) (*connect.Response[p2pstreamv1.UpdatePublicCacheSettingsResponse], error) {
	if _, err := a.requireAdmin(ctx, req.Header()); err != nil {
		return nil, err
	}
	params, err := validatePublicCacheSettingsInput(req.Msg.Enabled, req.Msg.MaxDiskBytes, req.Msg.MaxMemoryBytes, req.Msg.MemoryHotObjectMaxBytes, req.Msg.MaxEntries, req.Msg.CleanupIntervalMillis)
	if err != nil {
		return nil, err
	}
	row, err := a.DB.UpdatePublicCacheSettings(ctx, params)
	if err != nil {
		return nil, publicDBError(err)
	}
	if err := a.refreshPublicProxySnapshot(ctx); err != nil {
		return nil, err
	}
	return connect.NewResponse(&p2pstreamv1.UpdatePublicCacheSettingsResponse{Settings: publicCacheSettingsConfigToProto(publicCacheSettingsRowToConfig(row))}), nil
}

func (a *App) PurgePublicCache(ctx context.Context, req *connect.Request[p2pstreamv1.PurgePublicCacheRequest]) (*connect.Response[p2pstreamv1.PurgePublicCacheResponse], error) {
	if _, err := a.requireAdmin(ctx, req.Header()); err != nil {
		return nil, err
	}
	if a.PublicCache == nil {
		return connect.NewResponse(&p2pstreamv1.PurgePublicCacheResponse{}), nil
	}
	var rows []db.PurgeAllPublicCacheEntriesRow
	var purgedEntries int64
	var purgedBytes int64
	if req.Msg.All {
		var err error
		rows, err = a.DB.PurgeAllPublicCacheEntries(ctx)
		if err != nil {
			return nil, publicDBError(err)
		}
		for _, row := range rows {
			a.PublicCache.deleteMemory(row.KeyDigest)
			_ = os.Remove(row.BodyPath)
			purgedEntries++
			purgedBytes += row.SizeBytes
		}
	} else if req.Msg.RuleId > 0 {
		ruleRows, err := a.DB.PurgePublicCacheEntriesByRule(ctx, req.Msg.RuleId)
		if err != nil {
			return nil, publicDBError(err)
		}
		for _, row := range ruleRows {
			a.PublicCache.deleteMemory(row.KeyDigest)
			_ = os.Remove(row.BodyPath)
			purgedEntries++
			purgedBytes += row.SizeBytes
		}
	} else {
		host := normalizeRequestHost(req.Msg.Host)
		pathPrefix := strings.TrimSpace(req.Msg.PathPrefix)
		if host == "" && pathPrefix == "" {
			return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("cache purge requires all=true, a rule ID, host, or path prefix"))
		}
		if pathPrefix != "" && !strings.HasPrefix(pathPrefix, "/") {
			return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("cache purge path prefix must start with /"))
		}
		hostRows, err := a.DB.PurgePublicCacheEntriesByHostPath(ctx, db.PurgePublicCacheEntriesByHostPathParams{
			Column1: host,
			Host:    host,
			Column3: pathPrefix,
			Column4: sql.NullString{String: pathPrefix, Valid: true},
		})
		if err != nil {
			return nil, publicDBError(err)
		}
		for _, row := range hostRows {
			a.PublicCache.deleteMemory(row.KeyDigest)
			_ = os.Remove(row.BodyPath)
			purgedEntries++
			purgedBytes += row.SizeBytes
		}
	}
	return connect.NewResponse(&p2pstreamv1.PurgePublicCacheResponse{PurgedEntries: purgedEntries, PurgedBytes: purgedBytes}), nil
}

func validatePublicCacheSettingsInput(enabled bool, maxDiskBytes int64, maxMemoryBytes int64, hotObjectBytes int64, maxEntries int64, cleanupMillis int64) (db.UpdatePublicCacheSettingsParams, error) {
	if maxDiskBytes == 0 {
		maxDiskBytes = defaultPublicCacheMaxDiskBytes
	}
	if maxMemoryBytes == 0 {
		maxMemoryBytes = defaultPublicCacheMaxMemoryBytes
	}
	if hotObjectBytes == 0 {
		hotObjectBytes = defaultPublicCacheMemoryHotObjectBytes
	}
	if maxEntries == 0 {
		maxEntries = defaultPublicCacheMaxEntries
	}
	if cleanupMillis == 0 {
		cleanupMillis = defaultPublicCacheCleanupIntervalMillis
	}
	if maxDiskBytes < 0 || maxMemoryBytes < 0 || hotObjectBytes < 0 {
		return db.UpdatePublicCacheSettingsParams{}, connect.NewError(connect.CodeInvalidArgument, errors.New("cache byte limits must be zero or positive"))
	}
	if hotObjectBytes > maxMemoryBytes && maxMemoryBytes > 0 {
		return db.UpdatePublicCacheSettingsParams{}, connect.NewError(connect.CodeInvalidArgument, errors.New("cache hot object limit cannot exceed memory budget"))
	}
	if maxEntries < 1 || maxEntries > 10000000 {
		return db.UpdatePublicCacheSettingsParams{}, connect.NewError(connect.CodeInvalidArgument, errors.New("cache max entries must be between 1 and 10000000"))
	}
	if cleanupMillis < minPublicCacheCleanupIntervalMillis || cleanupMillis > maxPublicCacheCleanupIntervalMillis {
		return db.UpdatePublicCacheSettingsParams{}, connect.NewError(connect.CodeInvalidArgument, errors.New("cache cleanup interval must be between 1000 and 3600000ms"))
	}
	return db.UpdatePublicCacheSettingsParams{
		Enabled:                 boolInt(enabled),
		MaxDiskBytes:            maxDiskBytes,
		MaxMemoryBytes:          maxMemoryBytes,
		MemoryHotObjectMaxBytes: hotObjectBytes,
		MaxEntries:              maxEntries,
		CleanupIntervalMillis:   cleanupMillis,
	}, nil
}

func cacheCreateParams(input publicCacheRuleMutationInput) db.CreatePublicCacheRuleParams {
	return db.CreatePublicCacheRuleParams{
		Name:                 input.Name,
		Priority:             input.Priority,
		Enabled:              input.Enabled,
		MatchJson:            input.MatchJSON,
		RouteIdsJson:         input.RouteIDsJSON,
		TargetIdsJson:        input.TargetIDsJSON,
		Scope:                input.Scope,
		TtlMode:              input.TTLMode,
		TtlMillis:            input.TTLMillis,
		QueryMode:            input.QueryMode,
		QueryParamsJson:      input.QueryParamsJSON,
		VaryHeadersJson:      input.VaryHeadersJSON,
		CacheStatusCodesJson: input.CacheStatusCodesJSON,
		MaxObjectBytes:       input.MaxObjectBytes,
		AddCacheStatusHeader: input.AddCacheStatusHeader,
		AllowCookieRequests:  input.AllowCookieRequests,
	}
}

func cacheUpdateParams(id int64, input publicCacheRuleMutationInput) db.UpdatePublicCacheRuleParams {
	return db.UpdatePublicCacheRuleParams{
		ID:                   id,
		Name:                 input.Name,
		Priority:             input.Priority,
		Enabled:              input.Enabled,
		MatchJson:            input.MatchJSON,
		RouteIdsJson:         input.RouteIDsJSON,
		TargetIdsJson:        input.TargetIDsJSON,
		Scope:                input.Scope,
		TtlMode:              input.TTLMode,
		TtlMillis:            input.TTLMillis,
		QueryMode:            input.QueryMode,
		QueryParamsJson:      input.QueryParamsJSON,
		VaryHeadersJson:      input.VaryHeadersJSON,
		CacheStatusCodesJson: input.CacheStatusCodesJSON,
		MaxObjectBytes:       input.MaxObjectBytes,
		AddCacheStatusHeader: input.AddCacheStatusHeader,
		AllowCookieRequests:  input.AllowCookieRequests,
	}
}

func publicCacheRuleFingerprint(rule publicCacheRuleConfig) string {
	payload, _ := json.Marshal(rule)
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:])
}

func normalizePublicCacheScope(value string) string {
	switch value {
	case publicCacheScopeRoute:
		return publicCacheScopeRoute
	default:
		return publicCacheScopeSelectedBackend
	}
}

func normalizePublicCacheTTLMode(value string) string {
	switch value {
	case publicCacheTTLModeOrigin:
		return publicCacheTTLModeOrigin
	default:
		return publicCacheTTLModeFixed
	}
}

func normalizePublicCacheQueryMode(value string) string {
	switch value {
	case publicCacheQueryModeIgnore, publicCacheQueryModeAllowlist, publicCacheQueryModeDenylist:
		return value
	default:
		return publicCacheQueryModeFull
	}
}

func normalizePublicCacheTTLMillis(value int64) int64 {
	if value == 0 {
		return defaultPublicCacheTTLMillis
	}
	return value
}

func normalizePublicCacheMaxObjectBytes(value int64) int64 {
	if value <= 0 {
		return defaultPublicCacheMaxObjectBytes
	}
	return value
}

func normalizePublicCacheStatusCodes(values []int64) []int64 {
	if len(values) == 0 {
		values = defaultPublicCacheStatusCodes
	}
	seen := make(map[int64]struct{}, len(values))
	resp := make([]int64, 0, len(values))
	for _, value := range values {
		if value < 100 || value > 599 {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		resp = append(resp, value)
	}
	if len(resp) == 0 {
		resp = append(resp, defaultPublicCacheStatusCodes...)
	}
	sort.Slice(resp, func(i, j int) bool { return resp[i] < resp[j] })
	return resp
}

func normalizePublicCacheQueryParams(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	resp := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		resp = append(resp, value)
	}
	sort.Strings(resp)
	return resp
}

func normalizePublicCacheInt64List(values []int64) []int64 {
	seen := make(map[int64]struct{}, len(values))
	resp := make([]int64, 0, len(values))
	for _, value := range values {
		if value <= 0 {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		resp = append(resp, value)
	}
	sort.Slice(resp, func(i, j int) bool { return resp[i] < resp[j] })
	return resp
}

func publicCacheInt64ListFromJSON(raw string) ([]int64, error) {
	var values []int64
	if strings.TrimSpace(raw) == "" {
		return nil, nil
	}
	if err := json.Unmarshal([]byte(raw), &values); err != nil {
		return nil, err
	}
	return normalizePublicCacheInt64List(values), nil
}

func publicCacheScopeStringFromProto(value p2pstreamv1.PublicCacheScope) (string, error) {
	switch value {
	case p2pstreamv1.PublicCacheScope_PUBLIC_CACHE_SCOPE_UNSPECIFIED,
		p2pstreamv1.PublicCacheScope_PUBLIC_CACHE_SCOPE_SELECTED_BACKEND:
		return publicCacheScopeSelectedBackend, nil
	case p2pstreamv1.PublicCacheScope_PUBLIC_CACHE_SCOPE_ROUTE:
		return publicCacheScopeRoute, nil
	default:
		return "", connect.NewError(connect.CodeInvalidArgument, errors.New("cache scope is invalid"))
	}
}

func publicCacheTTLModeStringFromProto(value p2pstreamv1.PublicCacheTtlMode) (string, error) {
	switch value {
	case p2pstreamv1.PublicCacheTtlMode_PUBLIC_CACHE_TTL_MODE_UNSPECIFIED,
		p2pstreamv1.PublicCacheTtlMode_PUBLIC_CACHE_TTL_MODE_FIXED:
		return publicCacheTTLModeFixed, nil
	case p2pstreamv1.PublicCacheTtlMode_PUBLIC_CACHE_TTL_MODE_ORIGIN:
		return publicCacheTTLModeOrigin, nil
	default:
		return "", connect.NewError(connect.CodeInvalidArgument, errors.New("cache TTL mode is invalid"))
	}
}

func publicCacheQueryModeStringFromProto(value p2pstreamv1.PublicCacheQueryMode) (string, error) {
	switch value {
	case p2pstreamv1.PublicCacheQueryMode_PUBLIC_CACHE_QUERY_MODE_UNSPECIFIED,
		p2pstreamv1.PublicCacheQueryMode_PUBLIC_CACHE_QUERY_MODE_FULL:
		return publicCacheQueryModeFull, nil
	case p2pstreamv1.PublicCacheQueryMode_PUBLIC_CACHE_QUERY_MODE_IGNORE:
		return publicCacheQueryModeIgnore, nil
	case p2pstreamv1.PublicCacheQueryMode_PUBLIC_CACHE_QUERY_MODE_ALLOWLIST:
		return publicCacheQueryModeAllowlist, nil
	case p2pstreamv1.PublicCacheQueryMode_PUBLIC_CACHE_QUERY_MODE_DENYLIST:
		return publicCacheQueryModeDenylist, nil
	default:
		return "", connect.NewError(connect.CodeInvalidArgument, errors.New("cache query mode is invalid"))
	}
}

func protoPublicCacheScope(value string) p2pstreamv1.PublicCacheScope {
	if value == publicCacheScopeRoute {
		return p2pstreamv1.PublicCacheScope_PUBLIC_CACHE_SCOPE_ROUTE
	}
	return p2pstreamv1.PublicCacheScope_PUBLIC_CACHE_SCOPE_SELECTED_BACKEND
}

func protoPublicCacheTTLMode(value string) p2pstreamv1.PublicCacheTtlMode {
	if value == publicCacheTTLModeOrigin {
		return p2pstreamv1.PublicCacheTtlMode_PUBLIC_CACHE_TTL_MODE_ORIGIN
	}
	return p2pstreamv1.PublicCacheTtlMode_PUBLIC_CACHE_TTL_MODE_FIXED
}

func protoPublicCacheQueryMode(value string) p2pstreamv1.PublicCacheQueryMode {
	switch value {
	case publicCacheQueryModeIgnore:
		return p2pstreamv1.PublicCacheQueryMode_PUBLIC_CACHE_QUERY_MODE_IGNORE
	case publicCacheQueryModeAllowlist:
		return p2pstreamv1.PublicCacheQueryMode_PUBLIC_CACHE_QUERY_MODE_ALLOWLIST
	case publicCacheQueryModeDenylist:
		return p2pstreamv1.PublicCacheQueryMode_PUBLIC_CACHE_QUERY_MODE_DENYLIST
	default:
		return p2pstreamv1.PublicCacheQueryMode_PUBLIC_CACHE_QUERY_MODE_FULL
	}
}
