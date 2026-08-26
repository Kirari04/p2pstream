package server

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/rs/zerolog/log"

	p2pstreamv1 "p2pstream/gen/proto/p2pstream/v1"
)

const (
	maxPublicForwardAuthBodyBytes   = 64 << 10
	maxPublicForwardAuthHeaderBytes = 64 << 10
)

type publicAccessPrincipalContextKey struct{}

type publicAccessPrincipal struct {
	ProviderID         int64
	Subject            string
	Username           string
	Email              string
	Groups             []string
	HeaderNames        []string
	ForwardedHeader    http.Header
	StripAuthorization bool
}

type publicForwardAuthResult struct {
	Principal  publicAccessPrincipal
	Response   *http.Response
	StatusCode int
	Body       []byte
}

func publicAccessControlStage(ctx *publicProxyContext) publicProxyStageResult {
	if ctx == nil || !ctx.HasRouteMatch || ctx.RouteMatch.Err != nil {
		return publicProxyStageContinue
	}
	policyID := ctx.RouteMatch.Route.AccessPolicyID
	if ctx.Snapshot == nil {
		if policyID <= 0 {
			return publicProxyStageContinue
		}
		return rejectPublicAccessConfiguration(ctx, policyID, "")
	}
	trustedHeaderNames := publicAccessConfiguredHeaderNames(ctx.Snapshot)
	if policyID <= 0 {
		if len(trustedHeaderNames) > 0 {
			ctx.Request = ctx.Request.WithContext(context.WithValue(
				ctx.Request.Context(), publicAccessPrincipalContextKey{}, publicAccessPrincipal{HeaderNames: trustedHeaderNames},
			))
		}
		return publicProxyStageContinue
	}
	policy, ok := ctx.Snapshot.AccessPolicies[policyID]
	if !ok || !policy.Enabled {
		return rejectPublicAccessConfiguration(ctx, policyID, "")
	}
	provider, ok := ctx.Snapshot.AccessProviders[policy.ProviderID]
	if !ok || !provider.Enabled {
		return rejectPublicAccessConfiguration(ctx, policy.ID, policy.Name)
	}

	var principal publicAccessPrincipal
	switch provider.ProviderType {
	case publicAccessProviderTypeForwardAuth:
		if provider.client == nil || provider.ParsedURL == nil {
			return rejectPublicAccessConfiguration(ctx, policy.ID, policy.Name)
		}
		result, err := checkPublicForwardAuth(ctx.Request.Context(), provider, ctx.RouteMatch.Listener, ctx.Request)
		if err != nil {
			return rejectPublicAccessProviderFailure(ctx, policy, provider, err)
		}
		copyPublicForwardAuthClientHeaders(ctx.ResponseWriter.Header(), result.Response.Header)
		if result.StatusCode < 200 || result.StatusCode >= 300 {
			return writePublicForwardAuthResponse(ctx, policy, provider, result)
		}
		principal = result.Principal
	case publicAccessProviderTypeLocal:
		var err error
		var stage publicProxyStageResult
		principal, stage, err = handlePublicLocalAccess(ctx, policy, provider)
		if err != nil {
			return rejectPublicAccessProviderFailure(ctx, policy, provider, err)
		}
		if stage == publicProxyStageDone {
			return stage
		}
	default:
		return rejectPublicAccessConfiguration(ctx, policy.ID, policy.Name)
	}
	if !publicAccessPolicyAllowsGroups(policy, principal.Groups) {
		return rejectPublicAccessDenied(ctx, policy, provider, http.StatusForbidden, "access_denied")
	}
	principal.HeaderNames = trustedHeaderNames
	ctx.Request = ctx.Request.WithContext(context.WithValue(
		ctx.Request.Context(), publicAccessPrincipalContextKey{}, principal,
	))
	emitPublicAccessTrace(ctx, p2pstreamv1.TrafficTraceStage_TRAFFIC_TRACE_STAGE_ACCESS_GRANTED, policy, provider, 0, "")
	return publicProxyStageContinue
}

func publicAccessConfiguredHeaderNames(snap *publicProxySnapshot) []string {
	if snap == nil {
		return nil
	}
	if len(snap.AccessHeaderNames) > 0 {
		return snap.AccessHeaderNames
	}
	seen := make(map[string]string)
	for _, provider := range snap.AccessProviders {
		for _, name := range provider.ForwardedHeaders {
			canonical := http.CanonicalHeaderKey(name)
			if canonical != "" {
				seen[strings.ToLower(canonical)] = canonical
			}
		}
	}
	result := make([]string, 0, len(seen))
	for _, name := range seen {
		result = append(result, name)
	}
	sort.Slice(result, func(i, j int) bool { return strings.ToLower(result[i]) < strings.ToLower(result[j]) })
	return result
}

func checkPublicForwardAuth(
	ctx context.Context,
	provider publicAccessProviderConfig,
	listener publicListenerConfig,
	in *http.Request,
) (publicForwardAuthResult, error) {
	if in == nil || provider.client == nil || provider.ParsedURL == nil {
		return publicForwardAuthResult{}, errors.New("forward auth is not configured")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, provider.ParsedURL.String(), nil)
	if err != nil {
		return publicForwardAuthResult{}, err
	}
	for _, name := range []string{"Accept", "Authorization", "Cookie", "User-Agent"} {
		for _, value := range in.Header.Values(name) {
			req.Header.Add(name, value)
		}
	}
	proto := forwardedProtoForPublicListener(listener)
	originalURL := publicAccessOriginalURL(in, listener)
	req.Header.Set("X-Forwarded-Method", in.Method)
	req.Header.Set("X-Forwarded-Uri", publicAccessRequestURI(in))
	req.Header.Set("X-Forwarded-Host", normalizeRequestHost(in.Host))
	req.Header.Set("X-Forwarded-Proto", proto)
	req.Header.Set("X-Forwarded-Port", forwardedPortForPublicListener(listener, proto))
	req.Header.Set("X-Original-Url", originalURL)
	req.Header.Set("X-Auth-Request-Redirect", originalURL)
	if clientIP := publicAccessRequestClientIP(in); clientIP != "" {
		req.Header.Set("X-Forwarded-For", clientIP)
		req.Header.Set("X-Real-Ip", clientIP)
	}

	resp, err := provider.client.Do(req)
	if err != nil {
		return publicForwardAuthResult{}, err
	}
	if resp == nil {
		return publicForwardAuthResult{}, errors.New("forward auth returned no response")
	}
	if publicAccessHeaderBytes(resp.Header) > maxPublicForwardAuthHeaderBytes {
		resp.Body.Close()
		return publicForwardAuthResult{}, errors.New("forward-auth response headers are too large")
	}
	result := publicForwardAuthResult{Response: resp, StatusCode: resp.StatusCode}
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		resp.Body.Close()
		principal, err := publicAccessPrincipalFromResponse(provider, resp.Header)
		if err != nil {
			return publicForwardAuthResult{}, err
		}
		result.Principal = principal
		return result, nil
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxPublicForwardAuthBodyBytes+1))
	resp.Body.Close()
	if err != nil {
		return publicForwardAuthResult{}, err
	}
	if len(body) > maxPublicForwardAuthBodyBytes {
		return publicForwardAuthResult{}, errors.New("forward-auth response body is too large")
	}
	result.Body = body
	return result, nil
}

func publicAccessPrincipalFromResponse(provider publicAccessProviderConfig, header http.Header) (publicAccessPrincipal, error) {
	forwarded := make(http.Header, len(provider.ForwardedHeaders))
	for _, name := range provider.ForwardedHeaders {
		for _, value := range header.Values(name) {
			if len(value) > maxUpstreamHeaderValueBytes {
				return publicAccessPrincipal{}, fmt.Errorf("forward-auth identity header %q is too large", name)
			}
			forwarded.Add(name, value)
		}
	}
	return publicAccessPrincipal{
		ProviderID:      provider.ID,
		Subject:         strings.TrimSpace(header.Get(provider.SubjectHeader)),
		Username:        strings.TrimSpace(header.Get(provider.UserHeader)),
		Email:           strings.TrimSpace(header.Get(provider.EmailHeader)),
		Groups:          publicAccessGroupsFromHeader(header.Values(provider.GroupsHeader)),
		HeaderNames:     append([]string(nil), provider.ForwardedHeaders...),
		ForwardedHeader: forwarded,
	}, nil
}

func publicAccessGroupsFromHeader(values []string) []string {
	seen := make(map[string]struct{})
	var groups []string
	for _, value := range values {
		for _, group := range strings.Split(value, ",") {
			group = strings.TrimSpace(group)
			if group == "" || len(group) > 128 {
				continue
			}
			if _, ok := seen[group]; ok {
				continue
			}
			seen[group] = struct{}{}
			groups = append(groups, group)
		}
	}
	sort.Strings(groups)
	return groups
}

func publicAccessPolicyAllowsGroups(policy publicAccessPolicyConfig, groups []string) bool {
	if len(policy.RequiredGroups) == 0 {
		return true
	}
	present := make(map[string]struct{}, len(groups))
	for _, group := range groups {
		present[group] = struct{}{}
	}
	if policy.GroupMatch == publicAccessGroupMatchAll {
		for _, required := range policy.RequiredGroups {
			if _, ok := present[required]; !ok {
				return false
			}
		}
		return true
	}
	for _, required := range policy.RequiredGroups {
		if _, ok := present[required]; ok {
			return true
		}
	}
	return false
}

func applyTrustedPublicAccessHeaders(outReq, inReq *http.Request) {
	if outReq == nil || inReq == nil {
		return
	}
	principal, ok := inReq.Context().Value(publicAccessPrincipalContextKey{}).(publicAccessPrincipal)
	if !ok {
		return
	}
	for _, name := range principal.HeaderNames {
		outReq.Header.Del(name)
	}
	if principal.StripAuthorization {
		outReq.Header.Del("Authorization")
	}
	for name, values := range principal.ForwardedHeader {
		outReq.Header.Del(name)
		for _, value := range values {
			outReq.Header.Add(name, value)
		}
	}
}

func writePublicForwardAuthResponse(
	ctx *publicProxyContext,
	policy publicAccessPolicyConfig,
	provider publicAccessProviderConfig,
	result publicForwardAuthResult,
) publicProxyStageResult {
	status := result.StatusCode
	errorKind := "access_denied"
	if status >= 500 {
		status = http.StatusServiceUnavailable
		errorKind = "access_provider_error"
		result.Body = []byte("Access provider unavailable\n")
	} else if status >= 300 && status < 400 {
		errorKind = "access_challenge"
	} else if status == http.StatusUnauthorized {
		errorKind = "access_unauthenticated"
	}
	copyPublicForwardAuthResponseHeaders(ctx.ResponseWriter.Header(), result.Response.Header)
	ctx.ResponseWriter.WriteHeader(status)
	if ctx.Request.Method != http.MethodHead && len(result.Body) > 0 {
		_, _ = ctx.ResponseWriter.Write(result.Body)
	}
	emitPublicAccessTrace(ctx, p2pstreamv1.TrafficTraceStage_TRAFFIC_TRACE_STAGE_ACCESS_DENIED, policy, provider, status, errorKind)
	recordPublicAccessTerminal(ctx, status, errorKind)
	return publicProxyStageDone
}

func rejectPublicAccessConfiguration(ctx *publicProxyContext, policyID int64, policyName string) publicProxyStageResult {
	http.Error(ctx.ResponseWriter, "Access policy unavailable", http.StatusServiceUnavailable)
	policy := publicAccessPolicyConfig{ID: policyID, Name: policyName}
	emitPublicAccessTrace(ctx, p2pstreamv1.TrafficTraceStage_TRAFFIC_TRACE_STAGE_ACCESS_DENIED, policy, publicAccessProviderConfig{}, http.StatusServiceUnavailable, "access_configuration_error")
	recordPublicAccessTerminal(ctx, http.StatusServiceUnavailable, "access_configuration_error")
	return publicProxyStageDone
}

func rejectPublicAccessProviderFailure(ctx *publicProxyContext, policy publicAccessPolicyConfig, provider publicAccessProviderConfig, err error) publicProxyStageResult {
	log.Warn().Err(err).
		Int64("access_policy_id", policy.ID).
		Int64("access_provider_id", provider.ID).
		Msg("access provider request failed")
	http.Error(ctx.ResponseWriter, "Access provider unavailable", http.StatusServiceUnavailable)
	emitPublicAccessTrace(ctx, p2pstreamv1.TrafficTraceStage_TRAFFIC_TRACE_STAGE_ACCESS_DENIED, policy, provider, http.StatusServiceUnavailable, "access_provider_error")
	recordPublicAccessTerminal(ctx, http.StatusServiceUnavailable, "access_provider_error")
	return publicProxyStageDone
}

func rejectPublicAccessDenied(ctx *publicProxyContext, policy publicAccessPolicyConfig, provider publicAccessProviderConfig, status int, errorKind string) publicProxyStageResult {
	http.Error(ctx.ResponseWriter, http.StatusText(status), status)
	emitPublicAccessTrace(ctx, p2pstreamv1.TrafficTraceStage_TRAFFIC_TRACE_STAGE_ACCESS_DENIED, policy, provider, status, errorKind)
	recordPublicAccessTerminal(ctx, status, errorKind)
	return publicProxyStageDone
}

func emitPublicAccessTrace(ctx *publicProxyContext, stage p2pstreamv1.TrafficTraceStage, policy publicAccessPolicyConfig, provider publicAccessProviderConfig, status int, errorKind string) {
	if ctx == nil || ctx.Trace == nil {
		return
	}
	resolution := publicRouteResolution{
		Listener:   ctx.RouteMatch.Listener,
		Route:      ctx.RouteMatch.Route,
		ListenerID: sql.NullInt64{Int64: ctx.ListenerID, Valid: true},
		RouteID:    sql.NullInt64{Int64: ctx.RouteMatch.Route.ID, Valid: ctx.RouteMatch.Route.ID != 0},
	}
	attrs := map[string]string{
		"handler":          "access_control",
		"access_policy_id": strconv.FormatInt(policy.ID, 10),
		"access_policy":    policy.Name,
		"access_provider":  provider.Name,
	}
	ctx.Trace.emit(stage, &resolution, nil, status, errorKind, ctx.ResponseWriter.Header(), attrs)
}

func recordPublicAccessTerminal(ctx *publicProxyContext, status int, errorKind string) {
	if ctx == nil || ctx.App == nil {
		return
	}
	resolution := publicRouteResolution{
		Listener:     ctx.RouteMatch.Listener,
		Route:        ctx.RouteMatch.Route,
		DefaultRoute: ctx.RouteMatch.DefaultRoute,
		ListenerID:   sql.NullInt64{Int64: ctx.ListenerID, Valid: true},
		RouteID:      sql.NullInt64{Int64: ctx.RouteMatch.Route.ID, Valid: ctx.RouteMatch.Route.ID != 0},
	}
	ctx.App.recordProxyRequestEventWithIDsAndContext(
		context.Background(), status, time.Since(ctx.StartedAt), errorKind,
		resolution.ListenerID, resolution.RouteID, sql.NullInt64{},
		ctx.Observability.requestBytesValue(), ctx.Observability.responseBytesValue(),
		proxyRequestContextFromResolution(ctx.Request, resolution),
	)
}

func copyPublicForwardAuthClientHeaders(dst, src http.Header) {
	for _, value := range src.Values("Set-Cookie") {
		dst.Add("Set-Cookie", value)
	}
}

func copyPublicForwardAuthResponseHeaders(dst, src http.Header) {
	for _, name := range []string{"Cache-Control", "Content-Type", "Location", "Retry-After", "Set-Cookie", "Www-Authenticate"} {
		dst.Del(name)
		for _, value := range src.Values(name) {
			dst.Add(name, value)
		}
	}
}

func publicAccessHeaderBytes(header http.Header) int {
	total := 0
	for name, values := range header {
		for _, value := range values {
			total += len(name) + len(value)
		}
	}
	return total
}

func publicAccessRequestURI(r *http.Request) string {
	if r == nil || r.URL == nil || r.URL.RequestURI() == "" {
		return "/"
	}
	return r.URL.RequestURI()
}

func publicAccessOriginalURL(r *http.Request, listener publicListenerConfig) string {
	host := ""
	if r != nil {
		host = normalizeRequestHost(r.Host)
	}
	scheme := forwardedProtoForPublicListener(listener)
	port := forwardedPortForPublicListener(listener, scheme)
	if host != "" && port != "" && !((scheme == "http" && port == "80") || (scheme == "https" && port == "443")) {
		host = net.JoinHostPort(strings.Trim(host, "[]"), port)
	}
	return (&url.URL{
		Scheme:   scheme,
		Host:     host,
		Path:     publicAccessOriginalPath(r),
		RawPath:  publicAccessOriginalRawPath(r),
		RawQuery: publicAccessOriginalQuery(r),
	}).String()
}

func publicAccessOriginalPath(r *http.Request) string {
	if r == nil || r.URL == nil || r.URL.Path == "" {
		return "/"
	}
	return r.URL.Path
}

func publicAccessOriginalRawPath(r *http.Request) string {
	if r == nil || r.URL == nil {
		return ""
	}
	return r.URL.RawPath
}

func publicAccessOriginalQuery(r *http.Request) string {
	if r == nil || r.URL == nil {
		return ""
	}
	return r.URL.RawQuery
}

func publicAccessRequestClientIP(r *http.Request) string {
	if r == nil {
		return ""
	}
	if _, attached := ClientIdentityFromContext(r.Context()); attached {
		if addr, ok := ClientIdentityResolved(r.Context()); ok {
			return addr.String()
		}
		return ""
	}
	return remoteAddrIP(r.RemoteAddr)
}
