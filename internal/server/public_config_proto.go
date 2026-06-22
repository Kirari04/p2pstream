package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"
	"time"

	p2pstreamv1 "p2pstream/gen/proto/p2pstream/v1"
	"p2pstream/internal/db"
)

func protoRouteActionFromString(action string) p2pstreamv1.PublicRouteAction {
	switch normalizePublicRouteAction(action) {
	case publicRouteActionRedirect:
		return p2pstreamv1.PublicRouteAction_PUBLIC_ROUTE_ACTION_REDIRECT
	case publicRouteActionForward:
		return p2pstreamv1.PublicRouteAction_PUBLIC_ROUTE_ACTION_FORWARD
	default:
		return p2pstreamv1.PublicRouteAction_PUBLIC_ROUTE_ACTION_UNSPECIFIED
	}
}

func protoRouteRedirectTargetModeFromString(mode string) p2pstreamv1.PublicRouteRedirectTargetMode {
	switch normalizePublicRouteRedirectTargetMode(mode) {
	case publicRouteRedirectTargetModeSameHostPath:
		return p2pstreamv1.PublicRouteRedirectTargetMode_PUBLIC_ROUTE_REDIRECT_TARGET_MODE_SAME_HOST_PATH
	case publicRouteRedirectTargetModeExternalOriginKeepPath:
		return p2pstreamv1.PublicRouteRedirectTargetMode_PUBLIC_ROUTE_REDIRECT_TARGET_MODE_EXTERNAL_ORIGIN_KEEP_PATH
	case publicRouteRedirectTargetModeAbsoluteURL:
		return p2pstreamv1.PublicRouteRedirectTargetMode_PUBLIC_ROUTE_REDIRECT_TARGET_MODE_ABSOLUTE_URL
	default:
		return p2pstreamv1.PublicRouteRedirectTargetMode_PUBLIC_ROUTE_REDIRECT_TARGET_MODE_UNSPECIFIED
	}
}

func protoRoutePathSecurityModeFromString(mode string) p2pstreamv1.PublicRoutePathSecurityMode {
	switch normalizePublicRoutePathSecurityMode(mode) {
	case publicRoutePathSecurityModeAllowEncodedSeparators:
		return p2pstreamv1.PublicRoutePathSecurityMode_PUBLIC_ROUTE_PATH_SECURITY_MODE_ALLOW_ENCODED_SEPARATORS
	case publicRoutePathSecurityModeStrict:
		return p2pstreamv1.PublicRoutePathSecurityMode_PUBLIC_ROUTE_PATH_SECURITY_MODE_STRICT
	default:
		return p2pstreamv1.PublicRoutePathSecurityMode_PUBLIC_ROUTE_PATH_SECURITY_MODE_UNSPECIFIED
	}
}

func protoLoadBalancingFromString(loadBalancing string) p2pstreamv1.PublicRouteTargetLoadBalancing {
	switch normalizePublicRouteTargetLoadBalancing(loadBalancing) {
	case publicRouteTargetLoadBalancingWeightedRoundRobin:
		return p2pstreamv1.PublicRouteTargetLoadBalancing_PUBLIC_ROUTE_TARGET_LOAD_BALANCING_WEIGHTED_ROUND_ROBIN
	case publicRouteTargetLoadBalancingRandom:
		return p2pstreamv1.PublicRouteTargetLoadBalancing_PUBLIC_ROUTE_TARGET_LOAD_BALANCING_RANDOM
	case publicRouteTargetLoadBalancingWeightedRandom:
		return p2pstreamv1.PublicRouteTargetLoadBalancing_PUBLIC_ROUTE_TARGET_LOAD_BALANCING_WEIGHTED_RANDOM
	case publicRouteTargetLoadBalancingLeastActiveRequests:
		return p2pstreamv1.PublicRouteTargetLoadBalancing_PUBLIC_ROUTE_TARGET_LOAD_BALANCING_LEAST_ACTIVE_REQUESTS
	case publicRouteTargetLoadBalancingWeightedLeastActiveRequests:
		return p2pstreamv1.PublicRouteTargetLoadBalancing_PUBLIC_ROUTE_TARGET_LOAD_BALANCING_WEIGHTED_LEAST_ACTIVE_REQUESTS
	case publicRouteTargetLoadBalancingRoundRobin:
		return p2pstreamv1.PublicRouteTargetLoadBalancing_PUBLIC_ROUTE_TARGET_LOAD_BALANCING_ROUND_ROBIN
	default:
		return p2pstreamv1.PublicRouteTargetLoadBalancing_PUBLIC_ROUTE_TARGET_LOAD_BALANCING_UNSPECIFIED
	}
}

func protoPublicRouteTargetTypeFromString(targetType string) p2pstreamv1.PublicRouteTargetType {
	switch strings.TrimSpace(strings.ToLower(targetType)) {
	case publicRouteTargetTypeStatic:
		return p2pstreamv1.PublicRouteTargetType_PUBLIC_ROUTE_TARGET_TYPE_STATIC
	case publicRouteTargetTypeProxy, "":
		return p2pstreamv1.PublicRouteTargetType_PUBLIC_ROUTE_TARGET_TYPE_PROXY
	default:
		return p2pstreamv1.PublicRouteTargetType_PUBLIC_ROUTE_TARGET_TYPE_UNSPECIFIED
	}
}

func protoPublicRouteTargetTransportFromString(transport string) p2pstreamv1.PublicRouteTargetTransport {
	switch strings.TrimSpace(strings.ToLower(transport)) {
	case publicRouteTargetTransportAgent:
		return p2pstreamv1.PublicRouteTargetTransport_PUBLIC_ROUTE_TARGET_TRANSPORT_AGENT
	case publicRouteTargetTransportDirect, "":
		return p2pstreamv1.PublicRouteTargetTransport_PUBLIC_ROUTE_TARGET_TRANSPORT_DIRECT
	default:
		return p2pstreamv1.PublicRouteTargetTransport_PUBLIC_ROUTE_TARGET_TRANSPORT_UNSPECIFIED
	}
}

func protoTLSCertificateSourceFromString(source string) p2pstreamv1.PublicTlsCertificateSource {
	switch normalizePublicTLSCertificateSource(source) {
	case publicTLSCertificateSourceACME:
		return p2pstreamv1.PublicTlsCertificateSource_PUBLIC_TLS_CERTIFICATE_SOURCE_ACME
	case publicTLSCertificateSourceManual:
		return p2pstreamv1.PublicTlsCertificateSource_PUBLIC_TLS_CERTIFICATE_SOURCE_MANUAL
	default:
		return p2pstreamv1.PublicTlsCertificateSource_PUBLIC_TLS_CERTIFICATE_SOURCE_UNSPECIFIED
	}
}

func protoACMEChallengeTypeFromString(challengeType string) p2pstreamv1.PublicAcmeChallengeType {
	switch normalizePublicACMEChallengeType(challengeType) {
	case publicACMEChallengeHTTP01:
		return p2pstreamv1.PublicAcmeChallengeType_PUBLIC_ACME_CHALLENGE_TYPE_HTTP_01
	case publicACMEChallengeTLSALPN01:
		return p2pstreamv1.PublicAcmeChallengeType_PUBLIC_ACME_CHALLENGE_TYPE_TLS_ALPN_01
	case publicACMEChallengeDNS01:
		return p2pstreamv1.PublicAcmeChallengeType_PUBLIC_ACME_CHALLENGE_TYPE_DNS_01
	default:
		return p2pstreamv1.PublicAcmeChallengeType_PUBLIC_ACME_CHALLENGE_TYPE_UNSPECIFIED
	}
}

func protoACMECAFromString(ca string) p2pstreamv1.PublicAcmeCa {
	switch normalizePublicACMECA(ca) {
	case publicACMECAStaging:
		return p2pstreamv1.PublicAcmeCa_PUBLIC_ACME_CA_LETS_ENCRYPT_STAGING
	case publicACMECAProduction:
		return p2pstreamv1.PublicAcmeCa_PUBLIC_ACME_CA_LETS_ENCRYPT_PRODUCTION
	default:
		return p2pstreamv1.PublicAcmeCa_PUBLIC_ACME_CA_UNSPECIFIED
	}
}

func protoDNSProviderFromString(provider string) p2pstreamv1.PublicDnsProvider {
	switch normalizePublicDNSProvider(provider) {
	case publicDNSProviderCloudflare:
		return p2pstreamv1.PublicDnsProvider_PUBLIC_DNS_PROVIDER_CLOUDFLARE
	default:
		return p2pstreamv1.PublicDnsProvider_PUBLIC_DNS_PROVIDER_UNSPECIFIED
	}
}

func protoTLSCertificateStatusFromString(status string) p2pstreamv1.PublicTlsCertificateStatus {
	switch normalizePublicTLSCertificateStatus(status) {
	case publicTLSCertificateStatusPending:
		return p2pstreamv1.PublicTlsCertificateStatus_PUBLIC_TLS_CERTIFICATE_STATUS_PENDING
	case publicTLSCertificateStatusRenewing:
		return p2pstreamv1.PublicTlsCertificateStatus_PUBLIC_TLS_CERTIFICATE_STATUS_RENEWING
	case publicTLSCertificateStatusError:
		return p2pstreamv1.PublicTlsCertificateStatus_PUBLIC_TLS_CERTIFICATE_STATUS_ERROR
	case publicTLSCertificateStatusReady:
		return p2pstreamv1.PublicTlsCertificateStatus_PUBLIC_TLS_CERTIFICATE_STATUS_READY
	default:
		return p2pstreamv1.PublicTlsCertificateStatus_PUBLIC_TLS_CERTIFICATE_STATUS_UNSPECIFIED
	}
}

func protoProtocolFromString(protocol string) p2pstreamv1.PublicListenerProtocol {
	switch protocol {
	case publicListenerProtocolHTTP:
		return p2pstreamv1.PublicListenerProtocol_PUBLIC_LISTENER_PROTOCOL_HTTP
	case publicListenerProtocolHTTPS:
		return p2pstreamv1.PublicListenerProtocol_PUBLIC_LISTENER_PROTOCOL_HTTPS
	default:
		return p2pstreamv1.PublicListenerProtocol_PUBLIC_LISTENER_PROTOCOL_UNSPECIFIED
	}
}

func publicRouteTargetUpstreamHeadersToConfig(headers []db.PublicRouteTargetUpstreamHeader) []publicRequestHeader {
	resp := make([]publicRequestHeader, 0, len(headers))
	for _, header := range headers {
		resp = append(resp, publicRequestHeader{Name: header.Name, Value: header.Value, Sensitive: header.Sensitive != 0})
	}
	return resp
}

func publicRouteTargetResponseHeadersToConfig(headers []db.PublicRouteTargetResponseHeader) []publicResponseHeader {
	resp := make([]publicResponseHeader, 0, len(headers))
	for _, header := range headers {
		resp = append(resp, publicResponseHeader{Name: header.Name, Value: header.Value})
	}
	return resp
}

func publicRouteTargetHealthCheckRowToConfig(target db.PublicRouteTarget) publicRouteTargetHealthCheckConfig {
	return publicRouteTargetHealthCheckConfig{
		Enabled:            target.HealthCheckEnabled != 0,
		Method:             target.HealthCheckMethod,
		Path:               target.HealthCheckPath,
		Interval:           time.Duration(target.HealthCheckIntervalMillis) * time.Millisecond,
		Timeout:            time.Duration(target.HealthCheckTimeoutMillis) * time.Millisecond,
		HealthyThreshold:   target.HealthCheckHealthyThreshold,
		UnhealthyThreshold: target.HealthCheckUnhealthyThreshold,
		ExpectedStatusMin:  target.HealthCheckExpectedStatusMin,
		ExpectedStatusMax:  target.HealthCheckExpectedStatusMax,
	}
}

func publicAgentLabelsByAgent(labels []db.PublicAgentLabel) map[int64]map[string]string {
	resp := make(map[int64]map[string]string)
	for _, label := range labels {
		if resp[label.AgentID] == nil {
			resp[label.AgentID] = make(map[string]string)
		}
		resp[label.AgentID][label.Key] = label.Value
	}
	return resp
}

func publicAgentEnabledByID(agents []db.Agent) map[int64]bool {
	resp := make(map[int64]bool, len(agents))
	for _, agent := range agents {
		resp[agent.ID] = agent.Enabled != 0
	}
	return resp
}

func publicRouteTargetsByRoute(targets []db.PublicRouteTarget) map[int64][]db.PublicRouteTarget {
	resp := make(map[int64][]db.PublicRouteTarget)
	for _, target := range targets {
		resp[target.RouteID] = append(resp[target.RouteID], target)
	}
	return resp
}

func publicRouteTargetUpstreamHeadersByTarget(headers []db.PublicRouteTargetUpstreamHeader) map[int64][]db.PublicRouteTargetUpstreamHeader {
	resp := make(map[int64][]db.PublicRouteTargetUpstreamHeader)
	for _, header := range headers {
		resp[header.TargetID] = append(resp[header.TargetID], header)
	}
	return resp
}

func publicRouteTargetResponseHeadersByTarget(headers []db.PublicRouteTargetResponseHeader) map[int64][]db.PublicRouteTargetResponseHeader {
	resp := make(map[int64][]db.PublicRouteTargetResponseHeader)
	for _, header := range headers {
		resp[header.TargetID] = append(resp[header.TargetID], header)
	}
	return resp
}

func (a *App) publicRouteTargetHeaderMaps(ctx context.Context) (map[int64][]db.PublicRouteTargetUpstreamHeader, map[int64][]db.PublicRouteTargetResponseHeader) {
	upstreamHeaders, err := a.DB.ListPublicRouteTargetUpstreamHeaders(ctx)
	if err != nil {
		upstreamHeaders = nil
	}
	responseHeaders, err := a.DB.ListPublicRouteTargetResponseHeaders(ctx)
	if err != nil {
		responseHeaders = nil
	}
	return publicRouteTargetUpstreamHeadersByTarget(upstreamHeaders), publicRouteTargetResponseHeadersByTarget(responseHeaders)
}

func publicRouteTargetsToProto(targets []db.PublicRouteTarget, upstreamHeaders map[int64][]db.PublicRouteTargetUpstreamHeader, responseHeaders map[int64][]db.PublicRouteTargetResponseHeader, monitor *publicRouteTargetHealthMonitor) []*p2pstreamv1.PublicRouteTarget {
	resp := make([]*p2pstreamv1.PublicRouteTarget, 0, len(targets))
	for _, target := range targets {
		resp = append(resp, publicRouteTargetToProto(target, upstreamHeaders[target.ID], responseHeaders[target.ID], monitor))
	}
	return resp
}

func publicRouteTargetToProto(target db.PublicRouteTarget, upstreamHeaders []db.PublicRouteTargetUpstreamHeader, responseHeaders []db.PublicRouteTargetResponseHeader, monitor *publicRouteTargetHealthMonitor) *p2pstreamv1.PublicRouteTarget {
	return &p2pstreamv1.PublicRouteTarget{
		Id:                                  target.ID,
		RouteId:                             target.RouteID,
		Name:                                target.Name,
		Position:                            target.Position,
		PriorityGroup:                       target.PriorityGroup,
		Weight:                              target.Weight,
		Enabled:                             target.Enabled != 0,
		TargetType:                          protoPublicRouteTargetTypeFromString(target.TargetType),
		Url:                                 target.Url,
		Transport:                           protoPublicRouteTargetTransportFromString(target.Transport),
		AgentSelector:                       publicAgentSelectorFromJSON(target.AgentSelectorJson),
		AgentLoadBalancing:                  protoLoadBalancingFromString(target.AgentLoadBalancing),
		TlsSkipVerify:                       target.TlsSkipVerify != 0,
		UpstreamResponseHeaderTimeoutMillis: target.UpstreamResponseHeaderTimeoutMillis,
		UpstreamRequestHeaders:              publicRouteTargetUpstreamHeadersToProto(upstreamHeaders),
		UpstreamBasicAuth: &p2pstreamv1.PublicRouteTargetBasicAuth{
			Enabled:     target.UpstreamBasicAuthEnabled != 0,
			Username:    target.UpstreamBasicAuthUsername,
			PasswordSet: target.UpstreamBasicAuthPassword != "",
		},
		HealthCheck: &p2pstreamv1.PublicRouteTargetHealthCheck{
			Enabled:            target.HealthCheckEnabled != 0,
			Method:             target.HealthCheckMethod,
			Path:               target.HealthCheckPath,
			IntervalMillis:     target.HealthCheckIntervalMillis,
			TimeoutMillis:      target.HealthCheckTimeoutMillis,
			HealthyThreshold:   target.HealthCheckHealthyThreshold,
			UnhealthyThreshold: target.HealthCheckUnhealthyThreshold,
			ExpectedStatusMin:  target.HealthCheckExpectedStatusMin,
			ExpectedStatusMax:  target.HealthCheckExpectedStatusMax,
		},
		StaticStatusCode:         target.StaticStatusCode,
		StaticResponseHeaders:    publicRouteTargetResponseHeadersToProto(responseHeaders),
		StaticResponseBody:       target.StaticResponseBody,
		StaticResponseBodyMode:   protoPublicResponseBodyMode(target.StaticResponseBodyMode),
		StaticResponseTemplateId: nullInt64Value(target.StaticResponseTemplateID),
		Health:                   publicRouteTargetHealthToProto(target, monitor),
	}
}

func publicRouteTargetHealthToProto(target db.PublicRouteTarget, monitor *publicRouteTargetHealthMonitor) *p2pstreamv1.PublicRouteTargetHealth {
	if target.Enabled == 0 {
		return &p2pstreamv1.PublicRouteTargetHealth{
			Status:    p2pstreamv1.PublicRouteTargetHealthStatus_PUBLIC_ROUTE_TARGET_HEALTH_STATUS_DISABLED,
			Available: false,
		}
	}
	if target.TargetType == publicRouteTargetTypeStatic {
		return &p2pstreamv1.PublicRouteTargetHealth{
			Status:    p2pstreamv1.PublicRouteTargetHealthStatus_PUBLIC_ROUTE_TARGET_HEALTH_STATUS_HEALTHY,
			Available: true,
			Connected: true,
		}
	}
	if monitor == nil {
		return &p2pstreamv1.PublicRouteTargetHealth{
			Status:    p2pstreamv1.PublicRouteTargetHealthStatus_PUBLIC_ROUTE_TARGET_HEALTH_STATUS_UNKNOWN,
			Available: true,
		}
	}
	snapshot := monitor.snapshot(publicRouteTargetHealthDBAdapter{id: target.ID, enabled: target.Enabled != 0})
	if snapshot == nil {
		return &p2pstreamv1.PublicRouteTargetHealth{
			Status:    p2pstreamv1.PublicRouteTargetHealthStatus_PUBLIC_ROUTE_TARGET_HEALTH_STATUS_UNKNOWN,
			Available: true,
		}
	}
	return &p2pstreamv1.PublicRouteTargetHealth{
		Status:                          snapshot.Status,
		Available:                       snapshot.Status != p2pstreamv1.PublicRouteTargetHealthStatus_PUBLIC_ROUTE_TARGET_HEALTH_STATUS_UNHEALTHY && snapshot.Status != p2pstreamv1.PublicRouteTargetHealthStatus_PUBLIC_ROUTE_TARGET_HEALTH_STATUS_DISABLED && snapshot.Status != p2pstreamv1.PublicRouteTargetHealthStatus_PUBLIC_ROUTE_TARGET_HEALTH_STATUS_DISCONNECTED,
		Connected:                       snapshot.Status != p2pstreamv1.PublicRouteTargetHealthStatus_PUBLIC_ROUTE_TARGET_HEALTH_STATUS_DISCONNECTED,
		LastCheckedAtUnixMillis:         snapshot.LastCheckedAtUnixMillis,
		LastError:                       snapshot.LastError,
		PassiveUnhealthyUntilUnixMillis: snapshot.PassiveUnhealthyUntilUnixMillis,
		ActiveRequests:                  monitor.activeRequests(target.ID),
	}
}

func publicRouteTargetTransportFromConfig(transport string) p2pstreamv1.PublicRouteTargetTransport {
	if transport == publicRouteTargetTransportAgent {
		return p2pstreamv1.PublicRouteTargetTransport_PUBLIC_ROUTE_TARGET_TRANSPORT_AGENT
	}
	return p2pstreamv1.PublicRouteTargetTransport_PUBLIC_ROUTE_TARGET_TRANSPORT_DIRECT
}

func publicRouteTargetUpstreamHeadersToProto(headers []db.PublicRouteTargetUpstreamHeader) []*p2pstreamv1.PublicRouteTargetUpstreamHeader {
	resp := make([]*p2pstreamv1.PublicRouteTargetUpstreamHeader, 0, len(headers))
	for _, header := range headers {
		value := header.Value
		sensitive := header.Sensitive != 0 || isForcedSensitiveUpstreamHeader(header.Name)
		if sensitive {
			value = ""
		}
		resp = append(resp, &p2pstreamv1.PublicRouteTargetUpstreamHeader{
			Id:        header.ID,
			TargetId:  header.TargetID,
			Name:      header.Name,
			Value:     value,
			Sensitive: sensitive,
			ValueSet:  !sensitive,
			Position:  header.Position,
		})
	}
	return resp
}

func publicRouteTargetResponseHeadersToProto(headers []db.PublicRouteTargetResponseHeader) []*p2pstreamv1.PublicHeader {
	resp := make([]*p2pstreamv1.PublicHeader, 0, len(headers))
	for _, header := range headers {
		resp = append(resp, &p2pstreamv1.PublicHeader{Name: header.Name, Value: header.Value})
	}
	return resp
}

func publicAgentSelectorFromJSON(payload string) *p2pstreamv1.PublicAgentSelector {
	var decoded struct {
		MatchLabels map[string]string `json:"match_labels"`
	}
	_ = json.Unmarshal([]byte(payload), &decoded)
	if decoded.MatchLabels == nil {
		decoded.MatchLabels = map[string]string{}
	}
	return &p2pstreamv1.PublicAgentSelector{MatchLabels: decoded.MatchLabels}
}

func publicAgentSelectorConfigFromJSON(payload string) (publicAgentSelectorConfig, error) {
	payload = strings.TrimSpace(payload)
	if payload == "" {
		payload = "{}"
	}
	var decoded struct {
		MatchLabels map[string]string `json:"match_labels"`
	}
	if err := json.Unmarshal([]byte(payload), &decoded); err != nil {
		return publicAgentSelectorConfig{}, err
	}
	return publicAgentSelectorConfig{MatchLabels: cloneStringMap(decoded.MatchLabels)}, nil
}

func (a *App) publicAgentsToProto(ctx context.Context, agents []db.Agent, useDBFallback bool) []*p2pstreamv1.Agent {
	resp := make([]*p2pstreamv1.Agent, 0, len(agents))
	for _, agent := range agents {
		resp = append(resp, a.agentToProtoWithLatestStats(ctx, agent, useDBFallback))
	}
	return resp
}

func publicListenerToProto(listener db.PublicListener) *p2pstreamv1.PublicListener {
	return &p2pstreamv1.PublicListener{
		Id:                  listener.ID,
		Name:                listener.Name,
		BindAddress:         listener.BindAddress,
		Port:                listener.Port,
		Protocol:            protoProtocolFromString(listener.Protocol),
		Enabled:             listener.Enabled != 0,
		CreatedAtUnixMillis: listener.CreatedAt.UnixMilli(),
		UpdatedAtUnixMillis: listener.UpdatedAt.UnixMilli(),
	}
}

func publicListenersToProto(listeners []db.PublicListener) []*p2pstreamv1.PublicListener {
	resp := make([]*p2pstreamv1.PublicListener, 0, len(listeners))
	for _, listener := range listeners {
		resp = append(resp, publicListenerToProto(listener))
	}
	return resp
}

func publicRouteToProto(route db.PublicRoute, targets []db.PublicRouteTarget, upstreamHeaders map[int64][]db.PublicRouteTargetUpstreamHeader, responseHeaders map[int64][]db.PublicRouteTargetResponseHeader, monitor *publicRouteTargetHealthMonitor) *p2pstreamv1.PublicRoute {
	return &p2pstreamv1.PublicRoute{
		Id:                         route.ID,
		ListenerId:                 route.ListenerID,
		Priority:                   route.Priority,
		HostPattern:                route.HostPattern,
		PathPrefix:                 route.PathPrefix,
		TargetLoadBalancing:        protoLoadBalancingFromString(route.TargetLoadBalancing),
		IsDefault:                  route.IsDefault != 0,
		Targets:                    publicRouteTargetsToProto(targets, upstreamHeaders, responseHeaders, monitor),
		Enabled:                    route.Enabled != 0,
		CreatedAtUnixMillis:        route.CreatedAt.UnixMilli(),
		UpdatedAtUnixMillis:        route.UpdatedAt.UnixMilli(),
		Action:                     protoRouteActionFromString(route.Action),
		RedirectTargetMode:         protoRouteRedirectTargetModeFromString(route.RedirectTargetMode),
		RedirectTarget:             route.RedirectTarget,
		RedirectStatusCode:         route.RedirectStatusCode,
		RedirectPreservePathSuffix: route.RedirectPreservePathSuffix != 0,
		RedirectPreserveQuery:      route.RedirectPreserveQuery != 0,
		PathSecurityMode:           protoRoutePathSecurityModeFromString(route.PathSecurityMode),
	}
}

func publicRoutesToProto(routes []db.PublicRoute, targets []db.PublicRouteTarget, upstreamHeaders map[int64][]db.PublicRouteTargetUpstreamHeader, responseHeaders map[int64][]db.PublicRouteTargetResponseHeader, monitor *publicRouteTargetHealthMonitor) []*p2pstreamv1.PublicRoute {
	targetsByRoute := publicRouteTargetsByRoute(targets)
	resp := make([]*p2pstreamv1.PublicRoute, 0, len(routes))
	for _, route := range routes {
		resp = append(resp, publicRouteToProto(route, targetsByRoute[route.ID], upstreamHeaders, responseHeaders, monitor))
	}
	return resp
}

func publicTLSCertificateToProto(cert db.PublicTlsCertificate) *p2pstreamv1.PublicTlsCertificate {
	issuedAt := cert.IssuedAt
	expiresAt := cert.ExpiresAt
	if (!issuedAt.Valid || !expiresAt.Valid) && strings.TrimSpace(cert.CertPath) != "" {
		fileIssuedAt, fileExpiresAt := publicTLSCertificateValidityFromFile(cert.CertPath)
		if !issuedAt.Valid {
			issuedAt = fileIssuedAt
		}
		if !expiresAt.Valid {
			expiresAt = fileExpiresAt
		}
	}
	return &p2pstreamv1.PublicTlsCertificate{
		Id:                             cert.ID,
		ListenerId:                     cert.ListenerID,
		HostnamePattern:                cert.HostnamePattern,
		CertPath:                       cert.CertPath,
		KeyPath:                        cert.KeyPath,
		Enabled:                        cert.Enabled != 0,
		CreatedAtUnixMillis:            cert.CreatedAt.UnixMilli(),
		UpdatedAtUnixMillis:            cert.UpdatedAt.UnixMilli(),
		Source:                         protoTLSCertificateSourceFromString(cert.Source),
		AcmeChallengeType:              protoACMEChallengeTypeFromString(cert.AcmeChallengeType),
		AcmeCa:                         protoACMECAFromString(cert.AcmeCa),
		AcmeEmail:                      cert.AcmeEmail,
		DnsCredentialId:                nullInt64Value(cert.DnsCredentialID),
		Status:                         protoTLSCertificateStatusFromString(cert.Status),
		LastError:                      cert.LastError,
		IssuedAtUnixMillis:             nullTimeUnixMillis(issuedAt),
		ExpiresAtUnixMillis:            nullTimeUnixMillis(expiresAt),
		NextRenewalAtUnixMillis:        nullTimeUnixMillis(cert.NextRenewalAt),
		LastRenewalAttemptAtUnixMillis: nullTimeUnixMillis(cert.LastRenewalAttemptAt),
	}
}

func publicTLSCertificatesToProto(certs []db.PublicTlsCertificate) []*p2pstreamv1.PublicTlsCertificate {
	resp := make([]*p2pstreamv1.PublicTlsCertificate, 0, len(certs))
	for _, cert := range certs {
		resp = append(resp, publicTLSCertificateToProto(cert))
	}
	return resp
}

func publicTLSDNSCredentialToProto(credential db.PublicTlsDnsCredential) *p2pstreamv1.PublicTlsDnsCredential {
	return &p2pstreamv1.PublicTlsDnsCredential{
		Id:                  credential.ID,
		Name:                credential.Name,
		Provider:            protoDNSProviderFromString(credential.Provider),
		CloudflareZoneId:    credential.CloudflareZoneID,
		ApiTokenSet:         credential.ApiToken != "",
		Enabled:             credential.Enabled != 0,
		CreatedAtUnixMillis: credential.CreatedAt.UnixMilli(),
		UpdatedAtUnixMillis: credential.UpdatedAt.UnixMilli(),
	}
}

func publicTLSDNSCredentialsToProto(credentials []db.PublicTlsDnsCredential) []*p2pstreamv1.PublicTlsDnsCredential {
	resp := make([]*p2pstreamv1.PublicTlsDnsCredential, 0, len(credentials))
	for _, credential := range credentials {
		resp = append(resp, publicTLSDNSCredentialToProto(credential))
	}
	return resp
}

func nullInt64Value(value sql.NullInt64) int64 {
	if !value.Valid {
		return 0
	}
	return value.Int64
}
