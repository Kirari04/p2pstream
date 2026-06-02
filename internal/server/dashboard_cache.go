package server

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/rs/zerolog/log"
	"google.golang.org/protobuf/proto"

	p2pstreamv1 "p2pstream/gen/proto/p2pstream/v1"
	"p2pstream/internal/db"
)

const (
	dashboardCacheRefreshInterval = 5 * time.Second
	dashboardCacheRefreshTimeout  = 4 * time.Second
	dashboardCacheFailureBackoff  = 15 * time.Second
)

type dashboardResponseCache struct {
	started atomic.Bool

	mu            sync.RWMutex
	response      *p2pstreamv1.GetDashboardResponse
	lastFailureAt time.Time

	refreshMu sync.Mutex
}

func newDashboardResponseCache() *dashboardResponseCache {
	return &dashboardResponseCache{}
}

func (a *App) StartDashboardCache(ctx context.Context) {
	if a == nil || a.DB == nil || a.Config == nil || a.Config.ManagementUIDisabled {
		return
	}
	if a.DashboardCache == nil {
		a.DashboardCache = newDashboardResponseCache()
	}
	if !a.DashboardCache.started.CompareAndSwap(false, true) {
		return
	}

	go func() {
		a.refreshDashboardCache(ctx)

		ticker := time.NewTicker(dashboardCacheRefreshInterval)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				a.refreshDashboardCache(ctx)
			}
		}
	}()
}

func (a *App) dashboardCacheActive() bool {
	return a != nil && a.DashboardCache != nil && a.DashboardCache.started.Load()
}

func (a *App) dashboardResponseFromCache(now time.Time) *p2pstreamv1.GetDashboardResponse {
	if resp, ok := a.DashboardCache.clone(); ok {
		a.overlayDashboardLive(resp)
		return resp
	}
	resp := a.emptyDashboardResponse(now)
	a.overlayDashboardLive(resp)
	return resp
}

func (c *dashboardResponseCache) clone() (*p2pstreamv1.GetDashboardResponse, bool) {
	if c == nil {
		return nil, false
	}
	c.mu.RLock()
	resp := c.response
	c.mu.RUnlock()
	if resp == nil {
		return nil, false
	}
	cloned, ok := proto.Clone(resp).(*p2pstreamv1.GetDashboardResponse)
	return cloned, ok
}

func (c *dashboardResponseCache) store(resp *p2pstreamv1.GetDashboardResponse) {
	c.mu.Lock()
	c.response = resp
	c.lastFailureAt = time.Time{}
	c.mu.Unlock()
}

func (c *dashboardResponseCache) recordFailure(now time.Time) {
	c.mu.Lock()
	c.lastFailureAt = now
	c.mu.Unlock()
}

func (c *dashboardResponseCache) shouldSkipFailureBackoff(now time.Time) bool {
	c.mu.RLock()
	lastFailureAt := c.lastFailureAt
	c.mu.RUnlock()
	return !lastFailureAt.IsZero() && now.Sub(lastFailureAt) < dashboardCacheFailureBackoff
}

func (a *App) refreshDashboardCache(ctx context.Context) {
	cache := a.DashboardCache
	if cache == nil {
		return
	}
	now := time.Now().UTC()
	if cache.shouldSkipFailureBackoff(now) {
		return
	}
	if !cache.refreshMu.TryLock() {
		return
	}
	defer cache.refreshMu.Unlock()

	refreshCtx, cancel := context.WithTimeout(ctx, dashboardCacheRefreshTimeout)
	defer cancel()

	resp, err := a.buildDashboardCacheSnapshot(refreshCtx, time.Now().UTC())
	if err != nil {
		cache.recordFailure(time.Now().UTC())
		log.Warn().Err(err).Msg("Failed to refresh dashboard cache")
		return
	}
	cache.store(resp)
}

func (a *App) emptyDashboardResponse(now time.Time) *p2pstreamv1.GetDashboardResponse {
	windows := make([]*p2pstreamv1.DashboardWindowSummary, 0, len(dashboardWindows))
	for _, window := range dashboardWindows {
		windows = append(windows, &p2pstreamv1.DashboardWindowSummary{
			Label:           window.Label,
			SinceUnixMillis: rollupBucketUnixMillis(now.Add(-window.Since)),
		})
	}
	return &p2pstreamv1.GetDashboardResponse{
		Windows:               windows,
		AgentConnections:      &p2pstreamv1.AgentConnectionSummary{},
		RetentionDays:         int64(a.observabilityRetentionDays()),
		GeneratedAtUnixMillis: now.UnixMilli(),
	}
}

func (a *App) overlayDashboardLive(resp *p2pstreamv1.GetDashboardResponse) {
	resp.Status = a.statusResponse()
	resp.ManagementSecurity = a.managementSecurity()
	if resp.AgentConnections == nil {
		resp.AgentConnections = &p2pstreamv1.AgentConnectionSummary{}
	}
	resp.AgentConnections.Connected = a.AgentHub.connectedCount() > 0
}

func (a *App) buildDashboardCacheSnapshot(ctx context.Context, now time.Time) (*p2pstreamv1.GetDashboardResponse, error) {
	if a.DB == nil {
		return nil, errors.New("database is required for dashboard")
	}

	rows, _, err := a.cachedOrLoadPublicConfig(ctx)
	if err != nil {
		return nil, err
	}
	labels := dashboardLabelsFromPublicRows(rows)

	sinceUnixMillis := rollupBucketUnixMillis(now.Add(-dashboardMaxWindow()))
	proxyRows, err := a.DB.ListProxyRequestRollupMinutesSince(ctx, sinceUnixMillis)
	if err != nil {
		return nil, err
	}
	agentRows, err := a.DB.ListAgentStatRollupMinutesSince(ctx, sinceUnixMillis)
	if err != nil {
		return nil, err
	}
	tupleRows, err := a.DB.ListProxyRequestTupleRollupMinutesSince(ctx, sinceUnixMillis)
	if err != nil {
		return nil, err
	}

	agentConnections, err := a.agentConnectionSummary(ctx, now)
	if err != nil {
		return nil, err
	}

	topSinceUnixMillis := rollupBucketUnixMillis(now.Add(-dashboardTopWindow))
	resp := &p2pstreamv1.GetDashboardResponse{
		Status:                a.statusResponse(),
		Windows:               dashboardRollupWindows(now, proxyRows, agentRows),
		AgentConnections:      agentConnections,
		RetentionDays:         int64(a.observabilityRetentionDays()),
		GeneratedAtUnixMillis: now.UnixMilli(),
		TrafficBuckets:        dashboardCacheRollupTrafficBuckets(now, proxyRows),
		ManagementSecurity:    a.managementSecurity(),
	}
	resp.TopListeners, resp.TopBackends, resp.TopRoutes, resp.TopAgents, resp.TopErrorKinds, resp.StatusClasses = dashboardRollupTopDimensions(tupleRows, topSinceUnixMillis, labels)
	return resp, nil
}

func dashboardMaxWindow() time.Duration {
	maxWindow := time.Duration(0)
	for _, window := range dashboardWindows {
		if window.Since > maxWindow {
			maxWindow = window.Since
		}
	}
	return maxWindow
}

type dashboardProxyRollupMetrics struct {
	Requests         int64
	Success          int64
	ClientError      int64
	ServerError      int64
	InternalError    int64
	DurationMsSum    int64
	MaxDurationMs    int64
	SlowRequests     int64
	RequestBytes     int64
	ResponseBytes    int64
	CacheHits        int64
	CacheMisses      int64
	CacheBypasses    int64
	CacheStored      int64
	CacheStoreFailed int64
	CacheHitBytes    int64
	CacheStoredBytes int64
}

func (m *dashboardProxyRollupMetrics) add(row db.ListProxyRequestRollupMinutesSinceRow) {
	m.Requests += row.Requests
	m.Success += row.Success
	m.ClientError += row.ClientError
	m.ServerError += row.ServerError
	m.InternalError += row.InternalError
	m.DurationMsSum += row.DurationMsSum
	if row.MaxDurationMs > m.MaxDurationMs {
		m.MaxDurationMs = row.MaxDurationMs
	}
	m.SlowRequests += row.SlowRequests
	m.RequestBytes += row.RequestBytes
	m.ResponseBytes += row.ResponseBytes
	m.CacheHits += row.CacheHits
	m.CacheMisses += row.CacheMisses
	m.CacheBypasses += row.CacheBypasses
	m.CacheStored += row.CacheStored
	m.CacheStoreFailed += row.CacheStoreFailed
	m.CacheHitBytes += row.CacheHitBytes
	m.CacheStoredBytes += row.CacheStoredBytes
}

type dashboardAgentRollupMetrics struct {
	Samples          int64
	ReqSuccess       int64
	ReqClientError   int64
	ReqServerError   int64
	ReqInternalError int64
	BytesRx          int64
	BytesTx          int64
	MemoryMbSum      int64
	MaxMemoryMb      int64
	GoroutinesSum    int64
	MaxGoroutines    int64
	CpuPercentSum    float64
	MaxCpuPercent    float64
}

func (m *dashboardAgentRollupMetrics) add(row db.ListAgentStatRollupMinutesSinceRow) {
	m.Samples += row.Samples
	m.ReqSuccess += row.ReqSuccess
	m.ReqClientError += row.ReqClientError
	m.ReqServerError += row.ReqServerError
	m.ReqInternalError += row.ReqInternalError
	m.BytesRx += row.BytesRx
	m.BytesTx += row.BytesTx
	m.MemoryMbSum += row.MemoryMbSum
	if row.MaxMemoryMb > m.MaxMemoryMb {
		m.MaxMemoryMb = row.MaxMemoryMb
	}
	m.GoroutinesSum += row.GoroutinesSum
	if row.MaxGoroutines > m.MaxGoroutines {
		m.MaxGoroutines = row.MaxGoroutines
	}
	m.CpuPercentSum += row.CpuPercentSum
	if row.MaxCpuPercent > m.MaxCpuPercent {
		m.MaxCpuPercent = row.MaxCpuPercent
	}
}

func dashboardRollupWindows(now time.Time, proxyRows []db.ListProxyRequestRollupMinutesSinceRow, agentRows []db.ListAgentStatRollupMinutesSinceRow) []*p2pstreamv1.DashboardWindowSummary {
	windows := make([]*p2pstreamv1.DashboardWindowSummary, 0, len(dashboardWindows))
	for _, window := range dashboardWindows {
		since := rollupBucketUnixMillis(now.Add(-window.Since))
		var proxy dashboardProxyRollupMetrics
		for _, row := range proxyRows {
			if row.BucketUnixMillis >= since {
				proxy.add(row)
			}
		}
		var agent dashboardAgentRollupMetrics
		for _, row := range agentRows {
			if row.BucketUnixMillis >= since {
				agent.add(row)
			}
		}
		windows = append(windows, dashboardWindowSummaryFromRollups(window.Label, since, proxy, agent))
	}
	return windows
}

func dashboardWindowSummaryFromRollups(label string, sinceUnixMillis int64, proxy dashboardProxyRollupMetrics, agent dashboardAgentRollupMetrics) *p2pstreamv1.DashboardWindowSummary {
	resp := &p2pstreamv1.DashboardWindowSummary{
		Label:                 label,
		SinceUnixMillis:       sinceUnixMillis,
		ProxyRequests:         proxy.Requests,
		ProxySuccess:          proxy.Success,
		ProxyClientError:      proxy.ClientError,
		ProxyServerError:      proxy.ServerError,
		ProxyInternalError:    proxy.InternalError,
		ProxyRequestBytes:     uint64FromInt64(proxy.RequestBytes),
		ProxyResponseBytes:    uint64FromInt64(proxy.ResponseBytes),
		ProxyTotalBytes:       uint64FromInt64(proxy.RequestBytes + proxy.ResponseBytes),
		ProxyMaxDurationMs:    proxy.MaxDurationMs,
		ProxySlowRequests:     proxy.SlowRequests,
		ProxyCacheHits:        proxy.CacheHits,
		ProxyCacheMisses:      proxy.CacheMisses,
		ProxyCacheBypasses:    proxy.CacheBypasses,
		ProxyCacheStored:      proxy.CacheStored,
		ProxyCacheStoreFailed: proxy.CacheStoreFailed,
		ProxyCacheHitBytes:    uint64FromInt64(proxy.CacheHitBytes),
		ProxyCacheStoredBytes: uint64FromInt64(proxy.CacheStoredBytes),
		AgentSamples:          agent.Samples,
		AgentReqSuccess:       agent.ReqSuccess,
		AgentReqClientError:   agent.ReqClientError,
		AgentReqServerError:   agent.ReqServerError,
		AgentReqInternalError: agent.ReqInternalError,
		AgentBytesReceived:    uint64FromInt64(agent.BytesRx),
		AgentBytesSent:        uint64FromInt64(agent.BytesTx),
		AgentMaxMemoryMb:      agent.MaxMemoryMb,
		AgentMaxGoroutines:    agent.MaxGoroutines,
		AgentMaxCpuPercent:    agent.MaxCpuPercent,
	}
	if proxy.Requests > 0 {
		resp.ProxyAvgDurationMs = proxy.DurationMsSum / proxy.Requests
		resp.ProxyAvgRequestBytes = uint64FromInt64(proxy.RequestBytes / proxy.Requests)
		resp.ProxyAvgResponseBytes = uint64FromInt64(proxy.ResponseBytes / proxy.Requests)
	}
	if agent.Samples > 0 {
		resp.AgentAvgMemoryMb = agent.MemoryMbSum / agent.Samples
		resp.AgentAvgGoroutines = agent.GoroutinesSum / agent.Samples
		resp.AgentAvgCpuPercent = agent.CpuPercentSum / float64(agent.Samples)
	}
	return resp
}

func dashboardCacheRollupTrafficBuckets(now time.Time, rows []db.ListProxyRequestRollupMinutesSinceRow) []*p2pstreamv1.DashboardTrafficBucket {
	since := rollupBucketUnixMillis(now.Add(-dashboardTrafficBucketWindow))
	widthMillis := dashboardTrafficBucketSeconds * int64(time.Second/time.Millisecond)
	buckets := make(map[int64]dashboardProxyRollupMetrics)
	for _, row := range rows {
		if row.BucketUnixMillis < since {
			continue
		}
		bucketUnixMillis := (row.BucketUnixMillis / widthMillis) * widthMillis
		metric := buckets[bucketUnixMillis]
		metric.add(row)
		buckets[bucketUnixMillis] = metric
	}

	keys := make([]int64, 0, len(buckets))
	for key := range buckets {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i] < keys[j] })

	resp := make([]*p2pstreamv1.DashboardTrafficBucket, 0, len(keys))
	for _, key := range keys {
		metric := buckets[key]
		item := &p2pstreamv1.DashboardTrafficBucket{
			BucketUnixMillis: key,
			Requests:         metric.Requests,
			Success:          metric.Success,
			ClientError:      metric.ClientError,
			ServerError:      metric.ServerError,
			InternalError:    metric.InternalError,
			RequestBytes:     uint64FromInt64(metric.RequestBytes),
			ResponseBytes:    uint64FromInt64(metric.ResponseBytes),
		}
		if metric.Requests > 0 {
			item.AvgDurationMs = metric.DurationMsSum / metric.Requests
		}
		resp = append(resp, item)
	}
	return resp
}

type dashboardDimensionRollupMetrics struct {
	Requests      int64
	Success       int64
	ClientError   int64
	ServerError   int64
	InternalError int64
	DurationMsSum int64
	RequestBytes  int64
	ResponseBytes int64
}

func (m *dashboardDimensionRollupMetrics) add(row db.ListProxyRequestTupleRollupMinutesSinceRow) {
	m.Requests += row.Requests
	m.Success += row.Success
	m.ClientError += row.ClientError
	m.ServerError += row.ServerError
	m.InternalError += row.InternalError
	m.DurationMsSum += row.DurationMsSum
	m.RequestBytes += row.RequestBytes
	m.ResponseBytes += row.ResponseBytes
}

type dashboardDimensionAggregate struct {
	ID      int64
	Label   string
	Metrics dashboardDimensionRollupMetrics
}

func dashboardRollupTopDimensions(
	rows []db.ListProxyRequestTupleRollupMinutesSinceRow,
	sinceUnixMillis int64,
	labels dashboardLabelMaps,
) (
	[]*p2pstreamv1.DashboardProxyDimensionSummary,
	[]*p2pstreamv1.DashboardProxyDimensionSummary,
	[]*p2pstreamv1.DashboardProxyDimensionSummary,
	[]*p2pstreamv1.DashboardProxyDimensionSummary,
	[]*p2pstreamv1.DashboardProxyDimensionSummary,
	[]*p2pstreamv1.DashboardProxyDimensionSummary,
) {
	listeners := make(map[int64]*dashboardDimensionAggregate)
	backends := make(map[int64]*dashboardDimensionAggregate)
	routes := make(map[int64]*dashboardDimensionAggregate)
	agents := make(map[int64]*dashboardDimensionAggregate)
	errorsByKind := make(map[string]*dashboardDimensionAggregate)
	statusClasses := make(map[int64]*dashboardDimensionAggregate)

	for _, row := range rows {
		if row.BucketUnixMillis < sinceUnixMillis {
			continue
		}
		addIDDimension(listeners, row.ListenerID, labels.listener(row.ListenerID), row)
		addIDDimension(backends, row.BackendID, labels.backend(row.BackendID), row)
		addIDDimension(routes, row.RouteID, labels.route(row.RouteID), row)
		if row.AgentID != 0 {
			addIDDimension(agents, row.AgentID, labels.agent(row.AgentID), row)
		}
		if row.ErrorKind != "" {
			addStringDimension(errorsByKind, row.ErrorKind, row)
		}
		if row.StatusClass >= 2 && row.StatusClass < 6 {
			addIDDimension(statusClasses, row.StatusClass, fmt.Sprintf("%dxx", row.StatusClass), row)
		}
	}

	return idDimensionSummaries(p2pstreamv1.DashboardProxyDimension_DASHBOARD_PROXY_DIMENSION_LISTENER, listeners, 5, true),
		idDimensionSummaries(p2pstreamv1.DashboardProxyDimension_DASHBOARD_PROXY_DIMENSION_BACKEND, backends, 5, true),
		idDimensionSummaries(p2pstreamv1.DashboardProxyDimension_DASHBOARD_PROXY_DIMENSION_ROUTE, routes, 5, true),
		idDimensionSummaries(p2pstreamv1.DashboardProxyDimension_DASHBOARD_PROXY_DIMENSION_AGENT, agents, 5, true),
		stringDimensionSummaries(p2pstreamv1.DashboardProxyDimension_DASHBOARD_PROXY_DIMENSION_ERROR_KIND, errorsByKind, 5),
		idDimensionSummaries(p2pstreamv1.DashboardProxyDimension_DASHBOARD_PROXY_DIMENSION_STATUS_CLASS, statusClasses, 0, false)
}

func addIDDimension(items map[int64]*dashboardDimensionAggregate, id int64, label string, row db.ListProxyRequestTupleRollupMinutesSinceRow) {
	item := items[id]
	if item == nil {
		item = &dashboardDimensionAggregate{ID: id, Label: label}
		items[id] = item
	}
	item.Metrics.add(row)
}

func addStringDimension(items map[string]*dashboardDimensionAggregate, label string, row db.ListProxyRequestTupleRollupMinutesSinceRow) {
	item := items[label]
	if item == nil {
		item = &dashboardDimensionAggregate{Label: label}
		items[label] = item
	}
	item.Metrics.add(row)
}

func idDimensionSummaries(
	dimension p2pstreamv1.DashboardProxyDimension,
	items map[int64]*dashboardDimensionAggregate,
	limit int,
	sortByRequests bool,
) []*p2pstreamv1.DashboardProxyDimensionSummary {
	values := make([]*dashboardDimensionAggregate, 0, len(items))
	for _, item := range items {
		values = append(values, item)
	}
	sort.Slice(values, func(i, j int) bool {
		if sortByRequests && values[i].Metrics.Requests != values[j].Metrics.Requests {
			return values[i].Metrics.Requests > values[j].Metrics.Requests
		}
		return values[i].ID < values[j].ID
	})
	if limit > 0 && len(values) > limit {
		values = values[:limit]
	}
	return dimensionAggregateSummaries(dimension, values)
}

func stringDimensionSummaries(
	dimension p2pstreamv1.DashboardProxyDimension,
	items map[string]*dashboardDimensionAggregate,
	limit int,
) []*p2pstreamv1.DashboardProxyDimensionSummary {
	values := make([]*dashboardDimensionAggregate, 0, len(items))
	for _, item := range items {
		values = append(values, item)
	}
	sort.Slice(values, func(i, j int) bool {
		if values[i].Metrics.Requests != values[j].Metrics.Requests {
			return values[i].Metrics.Requests > values[j].Metrics.Requests
		}
		return values[i].Label < values[j].Label
	})
	if limit > 0 && len(values) > limit {
		values = values[:limit]
	}
	return dimensionAggregateSummaries(dimension, values)
}

func dimensionAggregateSummaries(dimension p2pstreamv1.DashboardProxyDimension, values []*dashboardDimensionAggregate) []*p2pstreamv1.DashboardProxyDimensionSummary {
	resp := make([]*p2pstreamv1.DashboardProxyDimensionSummary, 0, len(values))
	for _, item := range values {
		avgDurationMs := int64(0)
		if item.Metrics.Requests > 0 {
			avgDurationMs = item.Metrics.DurationMsSum / item.Metrics.Requests
		}
		resp = append(resp, dashboardDimensionSummary(
			dimension,
			item.ID,
			item.Label,
			item.Metrics.Requests,
			item.Metrics.Success,
			item.Metrics.ClientError,
			item.Metrics.ServerError,
			item.Metrics.InternalError,
			avgDurationMs,
			item.Metrics.RequestBytes,
			item.Metrics.ResponseBytes,
		))
	}
	return resp
}

type dashboardLabelMaps struct {
	listeners map[int64]string
	backends  map[int64]string
	routes    map[int64]string
	agents    map[int64]string
}

func dashboardLabelsFromPublicRows(rows publicConfigRows) dashboardLabelMaps {
	labels := dashboardLabelMaps{
		listeners: make(map[int64]string, len(rows.Listeners)),
		backends:  make(map[int64]string, len(rows.Backends)),
		routes:    make(map[int64]string, len(rows.Routes)),
		agents:    make(map[int64]string, len(rows.Agents)),
	}
	for _, listener := range rows.Listeners {
		labels.listeners[listener.ID] = listener.Name
	}
	for _, backend := range rows.Backends {
		labels.backends[backend.ID] = backend.Name
	}
	for _, route := range rows.Routes {
		labels.routes[route.ID] = dashboardRouteLabel(route)
	}
	for _, agent := range rows.Agents {
		labels.agents[agent.ID] = agent.Name
	}
	return labels
}

func (m dashboardLabelMaps) listener(id int64) string {
	if id == 0 {
		return "unknown listener"
	}
	if label, ok := m.listeners[id]; ok && label != "" {
		return label
	}
	return fmt.Sprintf("listener #%d", id)
}

func (m dashboardLabelMaps) backend(id int64) string {
	if id == 0 {
		return "unknown backend"
	}
	if label, ok := m.backends[id]; ok && label != "" {
		return label
	}
	return fmt.Sprintf("backend #%d", id)
}

func (m dashboardLabelMaps) route(id int64) string {
	if id == 0 {
		return "Default route"
	}
	if label, ok := m.routes[id]; ok && label != "" {
		return label
	}
	return fmt.Sprintf("route #%d", id)
}

func (m dashboardLabelMaps) agent(id int64) string {
	if label, ok := m.agents[id]; ok && label != "" {
		return label
	}
	return fmt.Sprintf("agent #%d", id)
}

func dashboardRouteLabel(route db.PublicRoute) string {
	if route.HostPattern != "" && route.PathPrefix != "" {
		return route.HostPattern + " " + route.PathPrefix
	}
	if route.HostPattern != "" {
		return route.HostPattern
	}
	if route.PathPrefix != "" {
		return route.PathPrefix
	}
	return fmt.Sprintf("route #%d", route.ID)
}
