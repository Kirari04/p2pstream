package server

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"sort"
	"strings"
	"time"

	"connectrpc.com/connect"
	"github.com/rs/zerolog/log"

	p2pstreamv1 "p2pstream/gen/proto/p2pstream/v1"
	"p2pstream/internal/db"
)

func (a *App) publicProxyConfigResponse(ctx context.Context) (*p2pstreamv1.GetPublicProxyConfigResponse, error) {
	rows, snap, err := a.cachedOrLoadPublicConfig(ctx)
	if err != nil {
		return nil, err
	}
	cacheStorageStats, err := a.publicCacheStorageStats(ctx)
	if err != nil {
		log.Warn().Err(err).Msg("Failed to load public cache storage stats")
	}
	routeTargetUpstreamHeaders := publicRouteTargetUpstreamHeadersByTarget(rows.RouteTargetUpstreamHeaders)
	routeTargetResponseHeaders := publicRouteTargetResponseHeadersByTarget(rows.RouteTargetResponseHeaders)

	return &p2pstreamv1.GetPublicProxyConfigResponse{
		AccessProviders:     publicAccessProvidersToProto(rows.AccessProviders),
		AccessUsers:         publicAccessUsersToProto(rows.AccessUsers),
		AccessPolicies:      publicAccessPoliciesToProto(rows.AccessPolicies),
		Listeners:           publicListenersToProto(rows.Listeners),
		Sites:               publicSitesToProto(rows.Sites, rows.SiteHosts, rows.SiteListenerBindings, rows.Listeners, rows.TLSCertificates, rows.Routes, rows.RouteTargets),
		Routes:              publicRoutesToProto(rows.Routes, rows.RouteTargets, routeTargetUpstreamHeaders, routeTargetResponseHeaders, a.TargetHealth),
		RouteTargets:        publicRouteTargetsToProto(rows.RouteTargets, routeTargetUpstreamHeaders, routeTargetResponseHeaders, a.TargetHealth),
		TlsCertificates:     publicTLSCertificatesToProto(rows.TLSCertificates),
		Proxy:               a.proxyStatus(),
		Agents:              a.publicAgentsToProto(ctx, rows.Agents, false),
		RateLimitRules:      publicRateLimitRulesToProto(rows.RateLimitRules),
		TrafficShaperRules:  publicTrafficShaperRulesToProto(rows.TrafficShaperRules),
		WafCaptchaProviders: publicWafCaptchaProvidersToProto(rows.WafCaptchaProviders, false),
		WafRules:            publicWafRulesToProto(rows.WafRules),
		GeoIpSettings:       a.publicGeoIPSettingsProto(rows.GeoIPSettings),
		TrustedProxySources: publicTrustedProxySourcesToProto(rows.TrustedProxySources),
		CacheSettings:       publicCacheSettingsConfigToProto(snap.CacheSettings),
		CacheStorageStats:   cacheStorageStats,
		CacheRules:          publicCacheRulesToProto(rows.CacheRules),
		RetryRules:          publicRetryRulesToProto(rows.RetryRules),
		TlsDnsCredentials:   publicTLSDNSCredentialsToProto(rows.TLSDNSCredentials),
		ResponseTemplates:   publicResponseTemplatesToProto(rows.ResponseTemplates),
	}, nil
}

func (a *App) refreshPublicProxySnapshot(ctx context.Context) error {
	a.publicConfigRefreshMu.Lock()
	defer a.publicConfigRefreshMu.Unlock()
	return a.refreshPublicProxySnapshotAlreadyLocked(ctx)
}

func (a *App) refreshPublicProxySnapshotAlreadyLocked(ctx context.Context) error {
	snap, err := a.loadPublicProxySnapshotLocked(ctx)
	if err != nil {
		return err
	}
	a.applyPublicProxySnapshot(snap)
	return nil
}

func (a *App) currentPublicSnapshot() *publicProxySnapshot {
	if a == nil {
		return nil
	}
	return a.publicSnapshotPtr.Load()
}

func (a *App) setPublicSnapshotLocked(snap *publicProxySnapshot) {
	a.publicSnapshot = snap
	a.publicSnapshotPtr.Store(snap)
	a.publicSnapshotGeneration++
}

func (a *App) applyPublicProxySnapshot(snap *publicProxySnapshot) {
	a.proxyMu.Lock()
	previous := a.publicSnapshot
	reconcilePublicAccessProviderTransports(previous, snap)
	a.setPublicSnapshotLocked(snap)
	generation := a.publicSnapshotGeneration
	a.ensureListenerStatesLocked(snap)
	a.proxyStatusLocked()
	active := a.proxyServiceActive
	a.proxyMu.Unlock()
	a.reconcileRouteTargetTransports(previous, snap)
	a.refreshRunningPublicTLSSelectors(snap, generation)
	a.LoadBalancers.reconcile(snap)
	if a.TargetHealth != nil {
		a.TargetHealth.reconcile(a, snap, active)
	}
	if a.RateLimiter != nil {
		a.RateLimiter.reconcile(snap)
	}
	if a.TrafficShaper != nil {
		a.TrafficShaper.reconcile(snap)
	}
	if a.PublicWAF != nil {
		a.PublicWAF.reconcile(snap)
	}
	if a.PublicCache != nil {
		a.PublicCache.reconcile(snap.CacheSettings, snap.CacheRules)
	}
}

type routeTargetTransportSignature struct {
	Transport                   string
	TargetOrigin                string
	TLSSkipVerify               bool
	ResponseHeaderTimeoutMillis int64
}

func (a *App) reconcileRouteTargetTransports(previous *publicProxySnapshot, current *publicProxySnapshot) {
	if previous == nil {
		return
	}
	for targetID, previousTarget := range previous.RouteTargets {
		currentTarget, ok := current.RouteTargets[targetID]
		if ok && routeTargetTransportSignatureFor(previousTarget) == routeTargetTransportSignatureFor(currentTarget) {
			continue
		}
		if a.DirectTransports != nil {
			a.DirectTransports.closeRouteTarget(targetID)
		}
		if a.AgentTransports != nil {
			a.AgentTransports.closeRouteTarget(targetID)
		}
	}
}

func routeTargetTransportSignatureFor(target publicRouteTargetConfig) routeTargetTransportSignature {
	timeout := normalizeUpstreamResponseHeaderTimeout(target.UpstreamResponseHeaderTimeout)
	return routeTargetTransportSignature{
		Transport:                   target.Transport,
		TargetOrigin:                routeTargetTransportOrigin(target),
		TLSSkipVerify:               target.TLSSkipVerify,
		ResponseHeaderTimeoutMillis: int64(timeout / time.Millisecond),
	}
}

func (a *App) loadPublicProxySnapshot(ctx context.Context) (*publicProxySnapshot, error) {
	a.publicConfigRefreshMu.Lock()
	defer a.publicConfigRefreshMu.Unlock()
	return a.loadPublicProxySnapshotLocked(ctx)
}

func (a *App) loadPublicProxySnapshotLocked(ctx context.Context) (*publicProxySnapshot, error) {
	rows, err := a.loadPublicConfigRows(ctx)
	if err != nil {
		return nil, err
	}
	snap, err := snapshotFromPublicRows(rows)
	if err != nil {
		return nil, err
	}
	a.storePublicConfigCache(rows, snap)
	return snap, nil
}
func (a *App) cachedOrLoadPublicConfig(ctx context.Context) (publicConfigRows, *publicProxySnapshot, error) {
	if rows, snap, ok := a.cachedPublicConfig(); ok {
		return rows, snap, nil
	}
	if _, err := a.loadPublicProxySnapshot(ctx); err != nil {
		return publicConfigRows{}, nil, err
	}
	rows, snap, ok := a.cachedPublicConfig()
	if !ok {
		return publicConfigRows{}, nil, connect.NewError(connect.CodeInternal, errors.New("public proxy config cache was not populated"))
	}
	return rows, snap, nil
}

func (a *App) cachedPublicConfig() (publicConfigRows, *publicProxySnapshot, bool) {
	a.publicConfigCacheMu.RLock()
	cached := a.publicConfigCache
	a.publicConfigCacheMu.RUnlock()
	return cached.Rows, cached.Snapshot, cached.Valid && cached.Snapshot != nil
}

func (a *App) storePublicConfigCache(rows publicConfigRows, snap *publicProxySnapshot) {
	a.publicConfigCacheMu.Lock()
	a.publicConfigCache = cachedPublicConfig{
		Rows:     rows,
		Snapshot: snap,
		Valid:    snap != nil,
	}
	a.publicConfigCacheMu.Unlock()
}

func (a *App) loadPublicConfigRows(ctx context.Context) (publicConfigRows, error) {
	if a.DB == nil {
		return publicConfigRows{}, connect.NewError(connect.CodeFailedPrecondition, errors.New("database is required for public proxy config"))
	}
	if err := a.ensurePublicProxySeeded(ctx); err != nil {
		return publicConfigRows{}, err
	}
	if err := a.ensurePublicConfigCandidatePrerequisites(ctx); err != nil {
		return publicConfigRows{}, err
	}
	tx, err := a.DB.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return publicConfigRows{}, connect.NewError(connect.CodeInternal, err)
	}
	defer tx.Rollback()
	txq := a.DB.Queries.WithTx(tx)
	rows, err := loadPublicConfigRowsWithQueries(ctx, txq)
	if err != nil {
		return publicConfigRows{}, err
	}
	if err := tx.Commit(); err != nil {
		return publicConfigRows{}, connect.NewError(connect.CodeInternal, err)
	}
	return rows, nil
}

func (a *App) ensurePublicConfigCandidatePrerequisites(ctx context.Context) error {
	if _, err := a.ensurePublicWafSettings(ctx); err != nil {
		return err
	}
	if _, err := a.ensurePublicGeoIPSettings(ctx); err != nil {
		return err
	}
	if _, err := a.ensurePublicCacheSettings(ctx); err != nil {
		return err
	}
	return nil
}

func loadPublicConfigRowsWithQueries(ctx context.Context, txq *db.Queries) (publicConfigRows, error) {
	accessProviders, err := txq.ListPublicAccessProviders(ctx)
	if err != nil {
		return publicConfigRows{}, connect.NewError(connect.CodeInternal, err)
	}
	accessPolicies, err := txq.ListPublicAccessPolicies(ctx)
	if err != nil {
		return publicConfigRows{}, connect.NewError(connect.CodeInternal, err)
	}
	accessUsers, err := txq.ListPublicAccessUsers(ctx)
	if err != nil {
		return publicConfigRows{}, connect.NewError(connect.CodeInternal, err)
	}
	responseTemplates, err := txq.ListPublicResponseTemplates(ctx)
	if err != nil {
		return publicConfigRows{}, connect.NewError(connect.CodeInternal, err)
	}
	agents, err := txq.ListAgents(ctx)
	if err != nil {
		return publicConfigRows{}, connect.NewError(connect.CodeInternal, err)
	}
	agentLabels, err := txq.ListAgentLabels(ctx)
	if err != nil {
		return publicConfigRows{}, connect.NewError(connect.CodeInternal, err)
	}
	listeners, err := txq.ListPublicListeners(ctx)
	if err != nil {
		return publicConfigRows{}, connect.NewError(connect.CodeInternal, err)
	}
	sites, err := txq.ListPublicSites(ctx)
	if err != nil {
		return publicConfigRows{}, connect.NewError(connect.CodeInternal, err)
	}
	siteHosts, err := txq.ListPublicSiteHosts(ctx)
	if err != nil {
		return publicConfigRows{}, connect.NewError(connect.CodeInternal, err)
	}
	siteListenerBindings, err := txq.ListPublicSiteListenerBindings(ctx)
	if err != nil {
		return publicConfigRows{}, connect.NewError(connect.CodeInternal, err)
	}
	routes, err := txq.ListPublicRoutes(ctx)
	if err != nil {
		return publicConfigRows{}, connect.NewError(connect.CodeInternal, err)
	}
	routeTargets, err := txq.ListPublicRouteTargets(ctx)
	if err != nil {
		return publicConfigRows{}, connect.NewError(connect.CodeInternal, err)
	}
	routeTargetUpstreamHeaders, err := txq.ListPublicRouteTargetUpstreamHeaders(ctx)
	if err != nil {
		return publicConfigRows{}, connect.NewError(connect.CodeInternal, err)
	}
	routeTargetResponseHeaders, err := txq.ListPublicRouteTargetResponseHeaders(ctx)
	if err != nil {
		return publicConfigRows{}, connect.NewError(connect.CodeInternal, err)
	}
	certs, err := txq.ListPublicTlsCertificates(ctx)
	if err != nil {
		return publicConfigRows{}, connect.NewError(connect.CodeInternal, err)
	}
	tlsDNSCredentials, err := txq.ListPublicTlsDnsCredentials(ctx)
	if err != nil {
		return publicConfigRows{}, connect.NewError(connect.CodeInternal, err)
	}
	rateLimitRules, err := txq.ListPublicRateLimitRules(ctx)
	if err != nil {
		return publicConfigRows{}, connect.NewError(connect.CodeInternal, err)
	}
	trafficShaperRules, err := txq.ListPublicTrafficShaperRules(ctx)
	if err != nil {
		return publicConfigRows{}, connect.NewError(connect.CodeInternal, err)
	}
	wafCaptchaProviders, err := txq.ListPublicWafCaptchaProviders(ctx)
	if err != nil {
		return publicConfigRows{}, connect.NewError(connect.CodeInternal, err)
	}
	wafRules, err := txq.ListPublicWafRules(ctx)
	if err != nil {
		return publicConfigRows{}, connect.NewError(connect.CodeInternal, err)
	}
	wafSettings, err := txq.GetPublicWafSettings(ctx)
	if err != nil {
		return publicConfigRows{}, err
	}
	geoIPSettings, err := txq.GetPublicGeoIpSettings(ctx)
	if err != nil {
		return publicConfigRows{}, err
	}
	trustedProxySources, err := txq.ListPublicTrustedProxySources(ctx)
	if err != nil {
		return publicConfigRows{}, connect.NewError(connect.CodeInternal, err)
	}
	cacheSettings, err := txq.GetPublicCacheSettings(ctx)
	if err != nil {
		return publicConfigRows{}, err
	}
	cacheRules, err := txq.ListPublicCacheRules(ctx)
	if err != nil {
		return publicConfigRows{}, connect.NewError(connect.CodeInternal, err)
	}
	retryRules, err := txq.ListPublicRetryRules(ctx)
	if err != nil {
		return publicConfigRows{}, connect.NewError(connect.CodeInternal, err)
	}
	rows := publicConfigRows{
		AccessProviders:            accessProviders,
		AccessUsers:                accessUsers,
		AccessPolicies:             accessPolicies,
		Agents:                     agents,
		AgentLabels:                agentLabels,
		Listeners:                  listeners,
		Sites:                      sites,
		SiteHosts:                  siteHosts,
		SiteListenerBindings:       siteListenerBindings,
		Routes:                     routes,
		RouteTargets:               routeTargets,
		RouteTargetUpstreamHeaders: routeTargetUpstreamHeaders,
		RouteTargetResponseHeaders: routeTargetResponseHeaders,
		TLSCertificates:            certs,
		TLSDNSCredentials:          tlsDNSCredentials,
		RateLimitRules:             rateLimitRules,
		TrafficShaperRules:         trafficShaperRules,
		WafCaptchaProviders:        wafCaptchaProviders,
		WafRules:                   wafRules,
		WafSettings:                wafSettings,
		GeoIPSettings:              geoIPSettings,
		TrustedProxySources:        trustedProxySources,
		CacheSettings:              cacheSettings,
		CacheRules:                 cacheRules,
		RetryRules:                 retryRules,
		ResponseTemplates:          responseTemplates,
	}
	return rows, nil
}

// preparePublicConfigCandidateTx builds the exact runtime candidate represented
// by an open write transaction. Callers must hold publicConfigRefreshMu until
// the transaction commits and the prepared snapshot is applied.
func preparePublicConfigCandidateTx(ctx context.Context, q *db.Queries) (publicConfigRows, *publicProxySnapshot, error) {
	rows, err := loadPublicConfigRowsWithQueries(ctx, q)
	if err != nil {
		return publicConfigRows{}, nil, err
	}
	snap, err := snapshotFromPublicRows(rows)
	if err != nil {
		return publicConfigRows{}, nil, err
	}
	return rows, snap, nil
}

func (a *App) applyPreparedPublicConfigCandidate(rows publicConfigRows, snap *publicProxySnapshot) {
	a.storePublicConfigCache(rows, snap)
	a.applyPublicProxySnapshot(snap)
}

func snapshotFromPublicRows(rows publicConfigRows) (*publicProxySnapshot, error) {
	clientIdentity, err := trustedProxyResolverFromRows(rows.TrustedProxySources)
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	snap := &publicProxySnapshot{
		AccessProviders:        make(map[int64]publicAccessProviderConfig),
		AccessPolicies:         make(map[int64]publicAccessPolicyConfig),
		RouteTargets:           make(map[int64]publicRouteTargetConfig),
		Agents:                 make(map[int64]publicAgentConfig),
		Listeners:              make(map[int64]publicListenerConfig),
		Sites:                  make(map[int64]publicSiteConfig),
		SiteHostsByListener:    make(map[int64][]publicSiteHostConfig),
		SiteBindingsByListener: make(map[int64][]publicSiteListenerBindingConfig),
		DefaultSiteByListener:  make(map[int64]publicSiteListenerBindingConfig),
		RoutesByListener:       make(map[int64][]publicRouteConfig),
		RoutesBySite:           make(map[int64][]publicRouteConfig),
		CertsByListener:        make(map[int64][]publicTLSCertificateConfig),
		WafCaptchaProviders:    make(map[int64]publicWafCaptchaProviderConfig),
		WafCookieSecret:        []byte(rows.WafSettings.CookieSigningSecret),
		CacheSettings:          publicCacheSettingsRowToConfig(rows.CacheSettings),
		ResponseTemplates:      publicResponseTemplatesToConfig(rows.ResponseTemplates),
		ClientIdentity:         clientIdentity,
	}
	for _, row := range rows.AccessProviders {
		provider, err := publicAccessProviderRowToConfig(row)
		if err != nil {
			return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("access provider %q is invalid: %w", row.Name, err))
		}
		if err := configurePublicAccessLocalLoginTemplate(&provider, snap.ResponseTemplates); err != nil {
			return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("access provider %q is invalid: %w", row.Name, err))
		}
		snap.AccessProviders[provider.ID] = provider
	}
	for _, row := range rows.AccessUsers {
		user, err := publicAccessUserRowToConfig(row)
		if err != nil {
			return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("access user %q is invalid: %w", row.Username, err))
		}
		provider, ok := snap.AccessProviders[user.ProviderID]
		if !ok || provider.ProviderType != publicAccessProviderTypeLocal {
			return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("access user %q references a non-local provider", row.Username))
		}
		if _, exists := provider.LocalUsers[user.Username]; exists {
			return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("access provider %q has duplicate user %q", provider.Name, user.Username))
		}
		provider.LocalUsers[user.Username] = user
		snap.AccessProviders[user.ProviderID] = provider
	}
	snap.AccessHeaderNames = publicAccessConfiguredHeaderNames(snap)
	for _, row := range rows.AccessPolicies {
		policy, err := publicAccessPolicyRowToConfig(row)
		if err != nil {
			return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("access policy %q is invalid: %w", row.Name, err))
		}
		if _, ok := snap.AccessProviders[policy.ProviderID]; !ok {
			return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("access policy %q references a missing provider", row.Name))
		}
		snap.AccessPolicies[policy.ID] = policy
	}

	routeTargetsByRoute := make(map[int64][]publicRouteTargetConfig)
	targetUpstreamHeadersByTarget := publicRouteTargetUpstreamHeadersByTarget(rows.RouteTargetUpstreamHeaders)
	targetResponseHeadersByTarget := publicRouteTargetResponseHeadersByTarget(rows.RouteTargetResponseHeaders)
	agentLabelsByAgent := publicAgentLabelsByAgent(rows.AgentLabels)
	for _, agent := range rows.Agents {
		snap.Agents[agent.ID] = publicAgentConfig{
			ID:       agent.ID,
			PublicID: agent.PublicID,
			Name:     agent.Name,
			Enabled:  agent.Enabled != 0,
			Labels:   cloneStringMap(agentLabelsByAgent[agent.ID]),
		}
	}
	for _, target := range rows.RouteTargets {
		targetType := normalizePublicRouteTargetType(target.TargetType)
		transport := normalizePublicRouteTargetTransport(target.Transport)
		var parsed *url.URL
		if targetType == publicRouteTargetTypeProxy {
			var err error
			parsed, err = parsePublicTargetOrigin(target.Url)
			if err != nil {
				return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("route target %q has invalid URL: %w", target.Name, err))
			}
		}
		staticResponseBody, err := effectiveGenericResponseBody(target.StaticResponseBodyMode, target.StaticResponseTemplateID, target.StaticResponseBody, snap.ResponseTemplates)
		if err != nil {
			return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("route target %q has invalid static response template: %w", target.Name, err))
		}
		selector, err := publicAgentSelectorConfigFromJSON(target.AgentSelectorJson)
		if err != nil {
			return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("route target %q has invalid agent selector: %w", target.Name, err))
		}
		config := publicRouteTargetConfig{
			ID:                            target.ID,
			RouteID:                       target.RouteID,
			Name:                          target.Name,
			Position:                      target.Position,
			PriorityGroup:                 target.PriorityGroup,
			Weight:                        target.Weight,
			Enabled:                       target.Enabled != 0,
			TargetType:                    targetType,
			URL:                           target.Url,
			Transport:                     transport,
			AgentSelector:                 selector,
			AgentLoadBalancing:            normalizePublicRouteTargetLoadBalancing(target.AgentLoadBalancing),
			TLSSkipVerify:                 target.TlsSkipVerify != 0,
			UpstreamResponseHeaderTimeout: time.Duration(normalizePublicRouteTargetUpstreamResponseHeaderTimeoutMillis(target.UpstreamResponseHeaderTimeoutMillis)) * time.Millisecond,
			UpstreamRequestHeaders:        publicRouteTargetUpstreamHeadersToConfig(targetUpstreamHeadersByTarget[target.ID]),
			UpstreamBasicAuth: publicRouteTargetBasicAuthConfig{
				Enabled:  target.UpstreamBasicAuthEnabled != 0,
				Username: target.UpstreamBasicAuthUsername,
				Password: target.UpstreamBasicAuthPassword,
			},
			HealthCheck:              publicRouteTargetHealthCheckRowToConfig(target),
			StaticStatusCode:         int(target.StaticStatusCode),
			StaticResponseHeaders:    publicRouteTargetResponseHeadersToConfig(targetResponseHeadersByTarget[target.ID]),
			StaticResponseBody:       staticResponseBody,
			StaticResponseBodyMode:   normalizePublicResponseBodyMode(target.StaticResponseBodyMode),
			StaticResponseTemplateID: nullInt64Value(target.StaticResponseTemplateID),
			ParsedURL:                parsed,
		}
		snap.RouteTargets[target.ID] = config
		routeTargetsByRoute[target.RouteID] = append(routeTargetsByRoute[target.RouteID], config)
	}
	for _, listener := range rows.Listeners {
		snap.Listeners[listener.ID] = publicListenerConfig{
			ID:          listener.ID,
			Name:        listener.Name,
			BindAddress: listener.BindAddress,
			Port:        listener.Port,
			Protocol:    listener.Protocol,
			Enabled:     listener.Enabled != 0,
		}
	}
	for _, site := range rows.Sites {
		snap.Sites[site.ID] = publicSiteConfig{
			ID: site.ID, Name: site.Name, Enabled: site.Enabled != 0, Published: site.Published != 0,
			DefaultSite: site.DefaultSite != 0, CanonicalHostname: site.CanonicalHostname,
			PrimaryHostname: site.CanonicalHostname,
		}
	}
	siteHostsBySite := make(map[int64][]publicSiteHostConfig)
	for _, host := range rows.SiteHosts {
		if _, ok := snap.Sites[host.SiteID]; !ok {
			return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("site host %q has no owner", host.HostnamePattern))
		}
		normalized, normalizeErr := normalizePublicSiteHostnamePattern(host.HostnamePattern)
		if normalizeErr != nil || normalized != host.HostnamePattern {
			return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("site host %q is not stored canonically", host.HostnamePattern))
		}
		if host.Role != publicSiteHostRolePrimary && host.Role != publicSiteHostRoleAlias {
			return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("site host %q has invalid role", host.HostnamePattern))
		}
		if host.Behavior != publicSiteHostBehaviorServe && host.Behavior != publicSiteHostBehaviorRedirect {
			return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("site host %q has invalid behavior", host.HostnamePattern))
		}
		if host.Role == publicSiteHostRolePrimary && (host.Behavior != publicSiteHostBehaviorServe || strings.HasPrefix(host.HostnamePattern, "*.")) {
			return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("site host %q has invalid primary binding", host.HostnamePattern))
		}
		binding := publicSiteHostConfig{
			ID: host.ID, SiteID: host.SiteID,
			HostnamePattern: host.HostnamePattern, Primary: host.Role == "primary", Behavior: host.Behavior,
		}
		siteHostsBySite[host.SiteID] = append(siteHostsBySite[host.SiteID], binding)
	}
	ownedHostPatterns := make(map[int64]map[string]int64)
	for _, row := range rows.SiteListenerBindings {
		site, ok := snap.Sites[row.SiteID]
		if !ok {
			return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("site listener binding has no Site %d", row.SiteID))
		}
		if _, ok := snap.Listeners[row.ListenerID]; !ok {
			return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("site %q references missing listener %d", site.Name, row.ListenerID))
		}
		binding := publicSiteListenerBindingConfig{
			SiteID: row.SiteID, ListenerID: row.ListenerID, Behavior: row.Behavior,
			RedirectListenerID: nullInt64Value(row.RedirectListenerID), RedirectHostname: row.RedirectHostname,
		}
		if site.ListenerID == 0 || row.ListenerID < site.ListenerID {
			site.ListenerID = row.ListenerID
			snap.Sites[row.SiteID] = site
		}
		if !site.Published {
			continue
		}
		if row.Behavior != publicSiteListenerBehaviorServe && row.Behavior != publicSiteListenerBehaviorRedirectHTTPS {
			return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("site %q has invalid listener behavior %q", site.Name, row.Behavior))
		}
		snap.SiteBindingsByListener[row.ListenerID] = append(snap.SiteBindingsByListener[row.ListenerID], binding)
		if site.DefaultSite {
			if previous, exists := snap.DefaultSiteByListener[row.ListenerID]; exists && previous.SiteID != row.SiteID {
				return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("listener %d has multiple published Default Sites", row.ListenerID))
			}
			snap.DefaultSiteByListener[row.ListenerID] = binding
			continue
		}
		hosts := siteHostsBySite[row.SiteID]
		if len(hosts) == 0 || len(hosts) > maxPublicSiteHosts {
			return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("published Site %q requires 1-%d hostnames", site.Name, maxPublicSiteHosts))
		}
		if ownedHostPatterns[row.ListenerID] == nil {
			ownedHostPatterns[row.ListenerID] = make(map[string]int64)
		}
		for _, host := range hosts {
			if owner, exists := ownedHostPatterns[row.ListenerID][host.HostnamePattern]; exists && owner != row.SiteID {
				return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("listener %d hostname %q is owned by multiple published Sites", row.ListenerID, host.HostnamePattern))
			}
			ownedHostPatterns[row.ListenerID][host.HostnamePattern] = row.SiteID
			snap.SiteHostsByListener[row.ListenerID] = append(snap.SiteHostsByListener[row.ListenerID], host)
		}
	}
	for _, site := range snap.Sites {
		if !site.Published {
			continue
		}
		if site.DefaultSite && len(siteHostsBySite[site.ID]) != 0 {
			return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("Default Site %q cannot have hostnames", site.Name))
		}
	}
	for routeID, targets := range routeTargetsByRoute {
		sort.SliceStable(targets, func(i, j int) bool {
			if targets[i].PriorityGroup == targets[j].PriorityGroup {
				if targets[i].Position == targets[j].Position {
					return targets[i].ID < targets[j].ID
				}
				return targets[i].Position < targets[j].Position
			}
			return targets[i].PriorityGroup < targets[j].PriorityGroup
		})
		routeTargetsByRoute[routeID] = targets
	}
	for _, route := range rows.Routes {
		config := publicRouteConfig{
			ID:                         route.ID,
			ListenerID:                 route.ListenerID,
			SiteID:                     nullInt64Value(route.SiteID),
			Priority:                   route.Priority,
			HostPattern:                normalizeHostPattern(route.HostPattern),
			PathPrefix:                 route.PathPrefix,
			TargetLoadBalancing:        normalizePublicRouteTargetLoadBalancing(route.TargetLoadBalancing),
			IsDefault:                  route.IsDefault != 0,
			Targets:                    routeTargetsByRoute[route.ID],
			Action:                     normalizePublicRouteAction(route.Action),
			RedirectTargetMode:         normalizePublicRouteRedirectTargetMode(route.RedirectTargetMode),
			RedirectTarget:             route.RedirectTarget,
			RedirectStatusCode:         route.RedirectStatusCode,
			RedirectPreservePathSuffix: route.RedirectPreservePathSuffix != 0,
			RedirectPreserveQuery:      route.RedirectPreserveQuery != 0,
			PathSecurityMode:           normalizePublicRoutePathSecurityMode(route.PathSecurityMode),
			AccessPolicyID:             nullInt64Value(route.AccessPolicyID),
			Enabled:                    route.Enabled != 0,
		}
		if config.SiteID != 0 {
			snap.RoutesBySite[config.SiteID] = append(snap.RoutesBySite[config.SiteID], config)
		} else {
			snap.RoutesByListener[route.ListenerID] = append(snap.RoutesByListener[route.ListenerID], config)
		}
	}
	for listenerID, routes := range snap.RoutesByListener {
		sortPublicRoutes(routes)
		snap.RoutesByListener[listenerID] = routes
	}
	for siteID, routes := range snap.RoutesBySite {
		sortPublicRoutes(routes)
		snap.RoutesBySite[siteID] = routes
	}
	for _, cert := range rows.TLSCertificates {
		snap.CertsByListener[cert.ListenerID] = append(snap.CertsByListener[cert.ListenerID], publicTLSCertificateConfig{
			ID:                cert.ID,
			ListenerID:        cert.ListenerID,
			HostnamePattern:   normalizeHostPattern(cert.HostnamePattern),
			CertPath:          cert.CertPath,
			KeyPath:           cert.KeyPath,
			Enabled:           cert.Enabled != 0,
			Source:            normalizePublicTLSCertificateSource(cert.Source),
			ACMEChallengeType: normalizePublicACMEChallengeType(cert.AcmeChallengeType),
			Status:            normalizePublicTLSCertificateStatus(cert.Status),
		})
	}
	for _, row := range rows.RateLimitRules {
		rule, err := publicRateLimitRuleRowToConfig(row)
		if err != nil {
			return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("rate limit rule %q is invalid: %w", row.Name, err))
		}
		responseBody, err := effectiveGenericResponseBody(row.ResponseBodyMode, row.ResponseBodyTemplateID, rule.ResponseBody, snap.ResponseTemplates)
		if err != nil {
			return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("rate limit rule %q has invalid response template: %w", row.Name, err))
		}
		rule.ResponseBody = responseBody
		rule.Fingerprint = publicRateLimitRuleFingerprint(rule)
		snap.RateLimitRules = append(snap.RateLimitRules, rule)
	}
	sortPublicRateLimitRules(snap.RateLimitRules)
	for _, row := range rows.TrafficShaperRules {
		rule, err := publicTrafficShaperRuleRowToConfig(row)
		if err != nil {
			return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("traffic shaper rule %q is invalid: %w", row.Name, err))
		}
		snap.TrafficShaperRules = append(snap.TrafficShaperRules, rule)
	}
	sortPublicTrafficShaperRules(snap.TrafficShaperRules)
	for _, row := range rows.WafCaptchaProviders {
		provider := publicWafCaptchaProviderRowToConfig(row, true)
		snap.WafCaptchaProviders[provider.ID] = provider
	}
	for _, row := range rows.WafRules {
		rule, err := publicWafRuleRowToConfig(row)
		if err != nil {
			return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("WAF rule %q is invalid: %w", row.Name, err))
		}
		blockBody, err := effectiveGenericResponseBody(row.BlockResponseBodyMode, row.BlockResponseTemplateID, rule.BlockResponseBody, snap.ResponseTemplates)
		if err != nil {
			return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("WAF rule %q has invalid block response template: %w", row.Name, err))
		}
		captchaTemplate, err := optionalWafPageTemplate(row.CaptchaPageTemplateID, publicResponseTemplateKindWafCaptchaPage, snap.ResponseTemplates)
		if err != nil {
			return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("WAF rule %q has invalid captcha page template: %w", row.Name, err))
		}
		waitingRoomTemplate, err := optionalWafPageTemplate(row.WaitingRoomPageTemplateID, publicResponseTemplateKindWafWaitingRoomPage, snap.ResponseTemplates)
		if err != nil {
			return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("WAF rule %q has invalid waiting-room page template: %w", row.Name, err))
		}
		rule.BlockResponseBody = blockBody
		rule.CaptchaPageTemplateBody = captchaTemplate
		rule.WaitingRoomPageTemplateBody = waitingRoomTemplate
		rule.Fingerprint = publicWafRuleFingerprint(rule)
		snap.WafRules = append(snap.WafRules, rule)
	}
	sortPublicWafRules(snap.WafRules)
	for _, row := range rows.CacheRules {
		rule, err := publicCacheRuleRowToConfig(row)
		if err != nil {
			return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("cache rule %q is invalid: %w", row.Name, err))
		}
		snap.CacheRules = append(snap.CacheRules, rule)
	}
	sortPublicCacheRules(snap.CacheRules)
	for _, row := range rows.RetryRules {
		rule, err := publicRetryRuleRowToConfig(row)
		if err != nil {
			return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("retry rule %q is invalid: %w", row.Name, err))
		}
		snap.RetryRules = append(snap.RetryRules, rule)
	}
	sortPublicRetryRules(snap.RetryRules)
	snap.CacheFingerprint = publicCacheRuntimeFingerprint(snap.CacheSettings, snap.CacheRules)
	return snap, nil
}

func (a *App) reconcilePublicListenerAfterMutation(ctx context.Context, listenerID int64) (*p2pstreamv1.PublicListenerStatus, error) {
	a.publicListenerLifecycleMu.Lock()
	defer a.publicListenerLifecycleMu.Unlock()
	if err := a.refreshPublicProxySnapshot(ctx); err != nil {
		return nil, err
	}
	return a.reconcilePublicListenerRuntimeFromCurrentSnapshot(ctx, listenerID)
}

func (a *App) reconcilePublicListenerAfterPreparedMutation(ctx context.Context, listenerID int64) (*p2pstreamv1.PublicListenerStatus, error) {
	a.publicListenerLifecycleMu.Lock()
	defer a.publicListenerLifecycleMu.Unlock()
	return a.reconcilePublicListenerRuntimeFromCurrentSnapshot(ctx, listenerID)
}

func (a *App) reconcilePublicListenerRuntimeFromCurrentSnapshot(ctx context.Context, listenerID int64) (*p2pstreamv1.PublicListenerStatus, error) {
	a.proxyMu.Lock()
	snap := a.publicSnapshot
	listener, ok := snap.Listeners[listenerID]
	serviceActive := a.proxyServiceActive
	runtime := a.ensureListenerStateLocked(listenerID)
	isRunning := runtime.Server != nil
	a.proxyMu.Unlock()
	if !ok {
		return nil, connect.NewError(connect.CodeNotFound, errors.New("listener not found"))
	}
	if !listener.Enabled {
		return a.stopPublicListenerRuntime(ctx, listenerID)
	}
	if serviceActive || isRunning {
		return a.restartPublicListenerRuntime(ctx, listenerID)
	}
	return a.getPublicListenerStatus(listenerID), nil
}
