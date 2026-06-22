package server

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"sort"
	"time"

	"connectrpc.com/connect"

	p2pstreamv1 "p2pstream/gen/proto/p2pstream/v1"
	secretspkg "p2pstream/internal/secrets"
)

func (a *App) publicProxyConfigResponse(ctx context.Context) (*p2pstreamv1.GetPublicProxyConfigResponse, error) {
	rows, snap, err := a.cachedOrLoadPublicConfig(ctx)
	if err != nil {
		return nil, err
	}
	routeTargetUpstreamHeaders := publicRouteTargetUpstreamHeadersByTarget(rows.RouteTargetUpstreamHeaders)
	routeTargetResponseHeaders := publicRouteTargetResponseHeadersByTarget(rows.RouteTargetResponseHeaders)

	return &p2pstreamv1.GetPublicProxyConfigResponse{
		Listeners:           publicListenersToProto(rows.Listeners),
		Routes:              publicRoutesToProto(rows.Routes, rows.RouteTargets, routeTargetUpstreamHeaders, routeTargetResponseHeaders, a.TargetHealth),
		RouteTargets:        publicRouteTargetsToProto(rows.RouteTargets, routeTargetUpstreamHeaders, routeTargetResponseHeaders, a.TargetHealth),
		TlsCertificates:     publicTLSCertificatesToProto(rows.TLSCertificates),
		Proxy:               a.proxyStatus(),
		Agents:              a.publicAgentsToProto(ctx, rows.Agents, false),
		RateLimitRules:      publicRateLimitRulesToProto(rows.RateLimitRules),
		TrafficShaperRules:  publicTrafficShaperRulesToProto(rows.TrafficShaperRules),
		WafCaptchaProviders: publicWafCaptchaProvidersToProto(rows.WafCaptchaProviders, false),
		WafRules:            publicWafRulesToProto(rows.WafRules),
		CacheSettings:       publicCacheSettingsConfigToProto(snap.CacheSettings),
		CacheRules:          publicCacheRulesToProto(rows.CacheRules),
		TlsDnsCredentials:   publicTLSDNSCredentialsToProto(rows.TLSDNSCredentials),
		ResponseTemplates:   publicResponseTemplatesToProto(rows.ResponseTemplates),
	}, nil
}

func (a *App) refreshPublicProxySnapshot(ctx context.Context) error {
	snap, err := a.loadPublicProxySnapshot(ctx)
	if err != nil {
		return err
	}
	a.applyPublicProxySnapshot(snap)
	return nil
}

func (a *App) applyPublicProxySnapshot(snap *publicProxySnapshot) {
	a.proxyMu.Lock()
	a.publicSnapshot = snap
	a.ensureListenerStatesLocked(snap)
	a.proxyStatusLocked()
	active := a.proxyServiceActive
	a.proxyMu.Unlock()
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
		a.PublicCache.reconcile(snap.CacheSettings)
	}
}

func (a *App) loadPublicProxySnapshot(ctx context.Context) (*publicProxySnapshot, error) {
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
	snap, err := a.loadPublicProxySnapshot(ctx)
	if err != nil {
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
	responseTemplates, err := a.DB.ListPublicResponseTemplates(ctx)
	if err != nil {
		return publicConfigRows{}, connect.NewError(connect.CodeInternal, err)
	}
	agents, err := a.DB.ListAgents(ctx)
	if err != nil {
		return publicConfigRows{}, connect.NewError(connect.CodeInternal, err)
	}
	agentLabels, err := a.DB.ListAgentLabels(ctx)
	if err != nil {
		return publicConfigRows{}, connect.NewError(connect.CodeInternal, err)
	}
	listeners, err := a.DB.ListPublicListeners(ctx)
	if err != nil {
		return publicConfigRows{}, connect.NewError(connect.CodeInternal, err)
	}
	routes, err := a.DB.ListPublicRoutes(ctx)
	if err != nil {
		return publicConfigRows{}, connect.NewError(connect.CodeInternal, err)
	}
	routeTargets, err := a.DB.ListPublicRouteTargets(ctx)
	if err != nil {
		return publicConfigRows{}, connect.NewError(connect.CodeInternal, err)
	}
	routeTargetUpstreamHeaders, err := a.DB.ListPublicRouteTargetUpstreamHeaders(ctx)
	if err != nil {
		return publicConfigRows{}, connect.NewError(connect.CodeInternal, err)
	}
	routeTargetResponseHeaders, err := a.DB.ListPublicRouteTargetResponseHeaders(ctx)
	if err != nil {
		return publicConfigRows{}, connect.NewError(connect.CodeInternal, err)
	}
	certs, err := a.DB.ListPublicTlsCertificates(ctx)
	if err != nil {
		return publicConfigRows{}, connect.NewError(connect.CodeInternal, err)
	}
	tlsDNSCredentials, err := a.DB.ListPublicTlsDnsCredentials(ctx)
	if err != nil {
		return publicConfigRows{}, connect.NewError(connect.CodeInternal, err)
	}
	rateLimitRules, err := a.DB.ListPublicRateLimitRules(ctx)
	if err != nil {
		return publicConfigRows{}, connect.NewError(connect.CodeInternal, err)
	}
	trafficShaperRules, err := a.DB.ListPublicTrafficShaperRules(ctx)
	if err != nil {
		return publicConfigRows{}, connect.NewError(connect.CodeInternal, err)
	}
	wafCaptchaProviders, err := a.DB.ListPublicWafCaptchaProviders(ctx)
	if err != nil {
		return publicConfigRows{}, connect.NewError(connect.CodeInternal, err)
	}
	wafRules, err := a.DB.ListPublicWafRules(ctx)
	if err != nil {
		return publicConfigRows{}, connect.NewError(connect.CodeInternal, err)
	}
	wafSettings, err := a.ensurePublicWafSettings(ctx)
	if err != nil {
		return publicConfigRows{}, err
	}
	cacheSettings, err := a.ensurePublicCacheSettings(ctx)
	if err != nil {
		return publicConfigRows{}, err
	}
	cacheRules, err := a.DB.ListPublicCacheRules(ctx)
	if err != nil {
		return publicConfigRows{}, connect.NewError(connect.CodeInternal, err)
	}
	rows := publicConfigRows{
		Agents:                     agents,
		AgentLabels:                agentLabels,
		Listeners:                  listeners,
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
		CacheSettings:              cacheSettings,
		CacheRules:                 cacheRules,
		ResponseTemplates:          responseTemplates,
	}
	if err := a.decryptPublicConfigRows(&rows); err != nil {
		return publicConfigRows{}, err
	}
	return rows, nil
}

func (a *App) decryptPublicConfigRows(rows *publicConfigRows) error {
	if rows == nil {
		return nil
	}
	for idx := range rows.RouteTargets {
		target := &rows.RouteTargets[idx]
		if target.UpstreamBasicAuthPassword == "" {
			continue
		}
		password, _, err := a.decryptSecret(secretspkg.PurposePublicRouteTargetBasicAuthPassword, target.ID, target.UpstreamBasicAuthPassword)
		if err != nil {
			return connect.NewError(connect.CodeInternal, fmt.Errorf("decrypt route target basic auth password: %w", err))
		}
		target.UpstreamBasicAuthPassword = password
	}
	for idx := range rows.RouteTargetUpstreamHeaders {
		header := &rows.RouteTargetUpstreamHeaders[idx]
		if header.Value == "" || (header.Sensitive == 0 && !isForcedSensitiveUpstreamHeader(header.Name)) {
			continue
		}
		value, _, err := a.decryptSecret(secretspkg.PurposePublicRouteTargetSensitiveHeader, header.ID, header.Value)
		if err != nil {
			return connect.NewError(connect.CodeInternal, fmt.Errorf("decrypt route target upstream header: %w", err))
		}
		header.Value = value
	}
	for idx := range rows.TLSDNSCredentials {
		credential := &rows.TLSDNSCredentials[idx]
		if credential.ApiToken == "" {
			continue
		}
		apiToken, _, err := a.decryptSecret(secretspkg.PurposePublicTLSDNSCredentialAPIToken, credential.ID, credential.ApiToken)
		if err != nil {
			return connect.NewError(connect.CodeInternal, fmt.Errorf("decrypt TLS DNS credential token: %w", err))
		}
		credential.ApiToken = apiToken
	}
	for idx := range rows.WafCaptchaProviders {
		provider := &rows.WafCaptchaProviders[idx]
		if provider.SecretKey == "" {
			continue
		}
		secretKey, _, err := a.decryptSecret(secretspkg.PurposePublicWAFCaptchaProviderSecretKey, provider.ID, provider.SecretKey)
		if err != nil {
			return connect.NewError(connect.CodeInternal, fmt.Errorf("decrypt WAF captcha provider secret: %w", err))
		}
		provider.SecretKey = secretKey
	}
	if rows.WafSettings.CookieSigningSecret != "" {
		secret, _, err := a.decryptSecret(secretspkg.PurposePublicWAFCookieSigningSecret, publicWAFSettingsSingletonID, rows.WafSettings.CookieSigningSecret)
		if err != nil {
			return connect.NewError(connect.CodeInternal, fmt.Errorf("decrypt WAF cookie signing secret: %w", err))
		}
		rows.WafSettings.CookieSigningSecret = secret
	}
	return nil
}

func snapshotFromPublicRows(rows publicConfigRows) (*publicProxySnapshot, error) {
	snap := &publicProxySnapshot{
		RouteTargets:        make(map[int64]publicRouteTargetConfig),
		Agents:              make(map[int64]publicAgentConfig),
		Listeners:           make(map[int64]publicListenerConfig),
		RoutesByListener:    make(map[int64][]publicRouteConfig),
		CertsByListener:     make(map[int64][]publicTLSCertificateConfig),
		WafCaptchaProviders: make(map[int64]publicWafCaptchaProviderConfig),
		WafCookieSecret:     []byte(rows.WafSettings.CookieSigningSecret),
		CacheSettings:       publicCacheSettingsRowToConfig(rows.CacheSettings),
		ResponseTemplates:   publicResponseTemplatesToConfig(rows.ResponseTemplates),
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
		snap.RoutesByListener[route.ListenerID] = append(snap.RoutesByListener[route.ListenerID], publicRouteConfig{
			ID:                         route.ID,
			ListenerID:                 route.ListenerID,
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
			Enabled:                    route.Enabled != 0,
		})
	}
	for listenerID, routes := range snap.RoutesByListener {
		sortPublicRoutes(routes)
		snap.RoutesByListener[listenerID] = routes
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
	for _, row := range rows.TrafficShaperRules {
		rule, err := publicTrafficShaperRuleRowToConfig(row)
		if err != nil {
			return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("traffic shaper rule %q is invalid: %w", row.Name, err))
		}
		snap.TrafficShaperRules = append(snap.TrafficShaperRules, rule)
	}
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
	for _, row := range rows.CacheRules {
		rule, err := publicCacheRuleRowToConfig(row)
		if err != nil {
			return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("cache rule %q is invalid: %w", row.Name, err))
		}
		snap.CacheRules = append(snap.CacheRules, rule)
	}
	return snap, nil
}

func (a *App) reconcilePublicListenerAfterMutation(ctx context.Context, listenerID int64) (*p2pstreamv1.PublicListenerStatus, error) {
	if err := a.refreshPublicProxySnapshot(ctx); err != nil {
		return nil, err
	}
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

func (a *App) restartTLSListenerIfActive(ctx context.Context, listenerID int64) (*p2pstreamv1.PublicListenerStatus, error) {
	a.proxyMu.Lock()
	runtime := a.publicListenerState[listenerID]
	running := runtime != nil && runtime.Server != nil
	a.proxyMu.Unlock()
	if !running {
		return a.getPublicListenerStatus(listenerID), nil
	}
	return a.restartPublicListenerRuntime(ctx, listenerID)
}
