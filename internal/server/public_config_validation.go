package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"

	"connectrpc.com/connect"

	p2pstreamv1 "p2pstream/gen/proto/p2pstream/v1"
	"p2pstream/internal/db"
)

func validatePublicRouteTargetHealthCheck(input *p2pstreamv1.PublicRouteTargetHealthCheck) (publicRouteTargetHealthCheckConfig, error) {
	cfg := publicRouteTargetHealthCheckConfig{
		Enabled:            false,
		Method:             defaultTargetHealthCheckMethod,
		Path:               defaultTargetHealthCheckPath,
		Interval:           time.Duration(defaultTargetHealthCheckIntervalMillis) * time.Millisecond,
		Timeout:            time.Duration(defaultTargetHealthCheckTimeoutMillis) * time.Millisecond,
		HealthyThreshold:   defaultTargetHealthCheckHealthyThreshold,
		UnhealthyThreshold: defaultTargetHealthCheckUnhealthyThreshold,
		ExpectedStatusMin:  defaultTargetHealthCheckExpectedStatusMin,
		ExpectedStatusMax:  defaultTargetHealthCheckExpectedStatusMax,
	}
	if input == nil {
		return cfg, nil
	}
	cfg.Enabled = input.Enabled
	method := strings.ToUpper(strings.TrimSpace(input.Method))
	if method == "" {
		method = defaultTargetHealthCheckMethod
	}
	if method != http.MethodGet && method != http.MethodHead {
		return publicRouteTargetHealthCheckConfig{}, connect.NewError(connect.CodeInvalidArgument, errors.New("health check method must be GET or HEAD"))
	}
	path := strings.TrimSpace(input.Path)
	if path == "" {
		path = defaultTargetHealthCheckPath
	}
	if !strings.HasPrefix(path, "/") {
		return publicRouteTargetHealthCheckConfig{}, connect.NewError(connect.CodeInvalidArgument, errors.New("health check path must start with /"))
	}
	intervalMillis := input.IntervalMillis
	if intervalMillis == 0 {
		intervalMillis = defaultTargetHealthCheckIntervalMillis
	}
	if intervalMillis < 1000 || intervalMillis > 3600000 {
		return publicRouteTargetHealthCheckConfig{}, connect.NewError(connect.CodeInvalidArgument, errors.New("health check interval must be between 1000 and 3600000 milliseconds"))
	}
	timeoutMillis := input.TimeoutMillis
	if timeoutMillis == 0 {
		timeoutMillis = defaultTargetHealthCheckTimeoutMillis
	}
	if timeoutMillis < 100 || timeoutMillis > 30000 {
		return publicRouteTargetHealthCheckConfig{}, connect.NewError(connect.CodeInvalidArgument, errors.New("health check timeout must be between 100 and 30000 milliseconds"))
	}
	if timeoutMillis > intervalMillis {
		return publicRouteTargetHealthCheckConfig{}, connect.NewError(connect.CodeInvalidArgument, errors.New("health check timeout must not exceed interval"))
	}
	healthyThreshold := input.HealthyThreshold
	if healthyThreshold == 0 {
		healthyThreshold = defaultTargetHealthCheckHealthyThreshold
	}
	unhealthyThreshold := input.UnhealthyThreshold
	if unhealthyThreshold == 0 {
		unhealthyThreshold = defaultTargetHealthCheckUnhealthyThreshold
	}
	if healthyThreshold < 1 || healthyThreshold > 10 || unhealthyThreshold < 1 || unhealthyThreshold > 10 {
		return publicRouteTargetHealthCheckConfig{}, connect.NewError(connect.CodeInvalidArgument, errors.New("health check thresholds must be between 1 and 10"))
	}
	statusMin := input.ExpectedStatusMin
	if statusMin == 0 {
		statusMin = defaultTargetHealthCheckExpectedStatusMin
	}
	statusMax := input.ExpectedStatusMax
	if statusMax == 0 {
		statusMax = defaultTargetHealthCheckExpectedStatusMax
	}
	if statusMin < 100 || statusMax > 599 || statusMin > statusMax {
		return publicRouteTargetHealthCheckConfig{}, connect.NewError(connect.CodeInvalidArgument, errors.New("health check expected status range must be between 100 and 599"))
	}
	cfg.Method = method
	cfg.Path = path
	cfg.Interval = time.Duration(intervalMillis) * time.Millisecond
	cfg.Timeout = time.Duration(timeoutMillis) * time.Millisecond
	cfg.HealthyThreshold = healthyThreshold
	cfg.UnhealthyThreshold = unhealthyThreshold
	cfg.ExpectedStatusMin = statusMin
	cfg.ExpectedStatusMax = statusMax
	return cfg, nil
}

func validatePublicRouteTargetUpstreamResponseHeaderTimeout(timeoutMillis int64) (int64, error) {
	if timeoutMillis == 0 {
		return defaultTargetUpstreamResponseHeaderTimeoutMillis, nil
	}
	if timeoutMillis < minTargetUpstreamResponseHeaderTimeoutMillis || timeoutMillis > maxTargetUpstreamResponseHeaderTimeoutMillis {
		return 0, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf(
			"upstream response header timeout must be between %d and %d milliseconds",
			minTargetUpstreamResponseHeaderTimeoutMillis,
			maxTargetUpstreamResponseHeaderTimeoutMillis,
		))
	}
	return timeoutMillis, nil
}

func normalizePublicRouteTargetUpstreamResponseHeaderTimeoutMillis(timeoutMillis int64) int64 {
	if timeoutMillis < minTargetUpstreamResponseHeaderTimeoutMillis || timeoutMillis > maxTargetUpstreamResponseHeaderTimeoutMillis {
		return defaultTargetUpstreamResponseHeaderTimeoutMillis
	}
	return timeoutMillis
}

func (a *App) validatePublicRouteTargets(
	ctx context.Context,
	targets []*p2pstreamv1.PublicRouteTarget,
	existingSecrets existingPublicRouteTargetSecrets,
) ([]publicRouteTargetMutationInput, error) {
	if len(targets) == 0 {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("forwarding route requires at least one target"))
	}
	resp := make([]publicRouteTargetMutationInput, 0, len(targets))
	enabledTargets := 0
	for idx, target := range targets {
		if target == nil {
			continue
		}
		targetType, err := publicRouteTargetTypeStringFromProto(target.TargetType)
		if err != nil {
			return nil, err
		}
		transport, err := publicRouteTargetTransportStringFromProto(target.Transport)
		if err != nil {
			return nil, err
		}
		agentLoadBalancing, err := loadBalancingStringFromProto(target.AgentLoadBalancing)
		if err != nil {
			return nil, err
		}
		weight := target.Weight
		if weight == 0 {
			weight = 100
		}
		if weight < 1 || weight > 1_000_000 {
			return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("route target weight must be between 1 and 1000000"))
		}
		name := strings.TrimSpace(target.Name)
		if name == "" {
			name = fmt.Sprintf("target-%d", idx+1)
		}
		if len(name) > 128 || strings.ContainsAny(name, "\r\n") {
			return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("route target name must be at most 128 characters without CR or LF"))
		}

		params := db.CreatePublicRouteTargetParams{
			Name:                                name,
			Position:                            int64(idx),
			PriorityGroup:                       target.PriorityGroup,
			Weight:                              weight,
			Enabled:                             boolInt(target.Enabled),
			TargetType:                          targetType,
			Transport:                           transport,
			AgentLoadBalancing:                  agentLoadBalancing,
			TlsSkipVerify:                       boolInt(target.TlsSkipVerify),
			UpstreamResponseHeaderTimeoutMillis: defaultTargetUpstreamResponseHeaderTimeoutMillis,
			HealthCheckMethod:                   defaultTargetHealthCheckMethod,
			HealthCheckPath:                     defaultTargetHealthCheckPath,
			HealthCheckIntervalMillis:           defaultTargetHealthCheckIntervalMillis,
			HealthCheckTimeoutMillis:            defaultTargetHealthCheckTimeoutMillis,
			HealthCheckHealthyThreshold:         defaultTargetHealthCheckHealthyThreshold,
			HealthCheckUnhealthyThreshold:       defaultTargetHealthCheckUnhealthyThreshold,
			HealthCheckExpectedStatusMin:        defaultTargetHealthCheckExpectedStatusMin,
			HealthCheckExpectedStatusMax:        defaultTargetHealthCheckExpectedStatusMax,
			StaticStatusCode:                    defaultStaticStatusCode,
			StaticResponseBodyMode:              publicResponseBodyModeInline,
		}
		var upstreamHeaders []publicRouteTargetUpstreamHeaderInput
		var responseHeaders []publicRouteTargetResponseHeaderInput
		if targetType == publicRouteTargetTypeProxy {
			targetURL := strings.TrimSpace(target.Url)
			parsed, err := parsePublicTargetOrigin(targetURL)
			if err != nil {
				return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("proxy route target URL must be an http or https origin"))
			}
			params.Url = strings.TrimRight(parsed.String(), "/")
			if transport == publicRouteTargetTransportAgent {
				selectorJSON, err := validatePublicAgentSelector(target.AgentSelector)
				if err != nil {
					return nil, err
				}
				params.AgentSelectorJson = selectorJSON
			} else {
				params.AgentSelectorJson = "{}"
			}
			upstreamHeaders, err = validatePublicRouteTargetUpstreamHeaders(target.Id, target.UpstreamRequestHeaders, target.UpstreamBasicAuth != nil && target.UpstreamBasicAuth.Enabled, existingSecrets)
			if err != nil {
				return nil, err
			}
			authEnabled, authUsername, authPassword, err := validatePublicRouteTargetBasicAuth(target.Id, target.UpstreamBasicAuth, existingSecrets)
			if err != nil {
				return nil, err
			}
			params.UpstreamBasicAuthEnabled = authEnabled
			params.UpstreamBasicAuthUsername = authUsername
			params.UpstreamBasicAuthPassword = authPassword
			health, err := validatePublicRouteTargetHealthCheck(target.HealthCheck)
			if err != nil {
				return nil, err
			}
			params.HealthCheckEnabled = boolInt(health.Enabled)
			params.HealthCheckMethod = health.Method
			params.HealthCheckPath = health.Path
			params.HealthCheckIntervalMillis = health.Interval.Milliseconds()
			params.HealthCheckTimeoutMillis = health.Timeout.Milliseconds()
			params.HealthCheckHealthyThreshold = health.HealthyThreshold
			params.HealthCheckUnhealthyThreshold = health.UnhealthyThreshold
			params.HealthCheckExpectedStatusMin = health.ExpectedStatusMin
			params.HealthCheckExpectedStatusMax = health.ExpectedStatusMax
			timeoutMillis, err := validatePublicRouteTargetUpstreamResponseHeaderTimeout(target.UpstreamResponseHeaderTimeoutMillis)
			if err != nil {
				return nil, err
			}
			params.UpstreamResponseHeaderTimeoutMillis = timeoutMillis
		} else {
			params.Transport = publicRouteTargetTransportDirect
			params.AgentSelectorJson = "{}"
			status := target.StaticStatusCode
			if status == 0 {
				status = defaultStaticStatusCode
			}
			if status < 100 || status > 599 {
				return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("static target status code must be between 100 and 599"))
			}
			bodyMode, templateRef, err := a.validateGenericResponseTemplateReference(ctx, target.StaticResponseBodyMode, target.StaticResponseTemplateId)
			if err != nil {
				return nil, err
			}
			responseHeaders, err = validatePublicStaticHeaders(target.StaticResponseHeaders)
			if err != nil {
				return nil, err
			}
			params.StaticStatusCode = status
			params.StaticResponseBody = target.StaticResponseBody
			params.StaticResponseBodyMode = bodyMode
			params.StaticResponseTemplateID = templateRef
		}
		if target.Enabled {
			enabledTargets++
		}
		resp = append(resp, publicRouteTargetMutationInput{
			Params:          params,
			UpstreamHeaders: upstreamHeaders,
			ResponseHeaders: responseHeaders,
		})
	}
	if len(resp) == 0 || enabledTargets == 0 {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("forwarding route requires at least one enabled target"))
	}
	return resp, nil
}

func validatePublicAgentSelector(selector *p2pstreamv1.PublicAgentSelector) (string, error) {
	if selector == nil || len(selector.MatchLabels) == 0 {
		return "", connect.NewError(connect.CodeInvalidArgument, errors.New("agent route target requires at least one selector label"))
	}
	matchLabels := make(map[string]string, len(selector.MatchLabels))
	for key, value := range selector.MatchLabels {
		key, value, err := validateAgentLabel(key, value)
		if err != nil {
			return "", err
		}
		matchLabels[key] = value
	}
	payload, err := json.Marshal(map[string]map[string]string{"match_labels": matchLabels})
	if err != nil {
		return "", connect.NewError(connect.CodeInternal, err)
	}
	return string(payload), nil
}

func (a *App) validatePublicListenerInput(
	name string,
	bindAddress string,
	port int64,
	protocol p2pstreamv1.PublicListenerProtocol,
	enabled bool,
	allowPortZero bool,
) (db.UpdatePublicListenerParams, error) {
	name, err := normalizePublicName(name)
	if err != nil {
		return db.UpdatePublicListenerParams{}, err
	}
	bindAddress, err = normalizeBindAddress(bindAddress)
	if err != nil {
		return db.UpdatePublicListenerParams{}, err
	}
	if port < 1 || port > 65535 {
		if !(allowPortZero && port == 0) {
			return db.UpdatePublicListenerParams{}, connect.NewError(connect.CodeInvalidArgument, errors.New("port must be between 1 and 65535"))
		}
	}
	protocolString, err := protocolStringFromProto(protocol)
	if err != nil {
		return db.UpdatePublicListenerParams{}, err
	}
	return db.UpdatePublicListenerParams{
		Name:        name,
		BindAddress: bindAddress,
		Port:        port,
		Protocol:    protocolString,
		Enabled:     boolInt(enabled),
	}, nil
}

func (a *App) validatePublicRouteInput(
	ctx context.Context,
	listenerID int64,
	priority int64,
	hostPattern string,
	pathPrefix string,
	targetLoadBalancing p2pstreamv1.PublicRouteTargetLoadBalancing,
	isDefault bool,
	targets []*p2pstreamv1.PublicRouteTarget,
	enabled bool,
	action p2pstreamv1.PublicRouteAction,
	redirectTargetMode p2pstreamv1.PublicRouteRedirectTargetMode,
	redirectTarget string,
	redirectStatusCode int64,
	redirectPreservePathSuffix bool,
	redirectPreserveQuery bool,
	pathSecurityMode p2pstreamv1.PublicRoutePathSecurityMode,
	existingSecrets existingPublicRouteTargetSecrets,
) (db.UpdatePublicRouteParams, []publicRouteTargetMutationInput, error) {
	if _, err := a.DB.GetPublicListener(ctx, listenerID); err != nil {
		return db.UpdatePublicRouteParams{}, nil, publicDBError(err)
	}
	hostPattern = normalizeHostPattern(hostPattern)
	pathPrefix = strings.TrimSpace(pathPrefix)
	if !isDefault && hostPattern == "" && pathPrefix == "" {
		return db.UpdatePublicRouteParams{}, nil, connect.NewError(connect.CodeInvalidArgument, errors.New("route requires a host pattern or path prefix"))
	}
	if hostPattern != "" {
		if err := validateHostPattern(hostPattern); err != nil {
			return db.UpdatePublicRouteParams{}, nil, err
		}
	}
	if pathPrefix != "" && !strings.HasPrefix(pathPrefix, "/") {
		return db.UpdatePublicRouteParams{}, nil, connect.NewError(connect.CodeInvalidArgument, errors.New("path prefix must start with /"))
	}
	actionString, err := routeActionStringFromProto(action)
	if err != nil {
		return db.UpdatePublicRouteParams{}, nil, err
	}
	pathSecurityModeString, err := routePathSecurityModeStringFromProto(pathSecurityMode)
	if err != nil {
		return db.UpdatePublicRouteParams{}, nil, err
	}
	targetLoadBalancingString := publicRouteTargetLoadBalancingRoundRobin
	var routeTargets []publicRouteTargetMutationInput
	redirectMode := ""
	redirectTarget = strings.TrimSpace(redirectTarget)
	if redirectStatusCode == 0 {
		redirectStatusCode = defaultRedirectStatusCode
	}
	if actionString == publicRouteActionForward {
		targetLoadBalancingString, err = loadBalancingStringFromProto(targetLoadBalancing)
		if err != nil {
			return db.UpdatePublicRouteParams{}, nil, err
		}
		routeTargets, err = a.validatePublicRouteTargets(ctx, targets, existingSecrets)
		if err != nil {
			return db.UpdatePublicRouteParams{}, nil, err
		}
		redirectStatusCode = defaultRedirectStatusCode
		redirectPreservePathSuffix = true
		redirectPreserveQuery = true
	} else {
		redirectMode, err = routeRedirectTargetModeStringFromProto(redirectTargetMode)
		if err != nil {
			return db.UpdatePublicRouteParams{}, nil, err
		}
		if err := validateRouteRedirectTarget(redirectMode, redirectTarget); err != nil {
			return db.UpdatePublicRouteParams{}, nil, err
		}
		if !validRedirectStatusCode(redirectStatusCode) {
			return db.UpdatePublicRouteParams{}, nil, connect.NewError(connect.CodeInvalidArgument, errors.New("redirect status must be 301, 302, 307, or 308"))
		}
		if redirectMode == publicRouteRedirectTargetModeExternalOriginKeepPath {
			redirectPreservePathSuffix = false
		}
	}
	return db.UpdatePublicRouteParams{
		ListenerID:                 listenerID,
		Priority:                   priority,
		HostPattern:                hostPattern,
		PathPrefix:                 pathPrefix,
		TargetLoadBalancing:        targetLoadBalancingString,
		IsDefault:                  boolInt(isDefault),
		Action:                     actionString,
		RedirectTargetMode:         redirectMode,
		RedirectTarget:             redirectTarget,
		RedirectStatusCode:         redirectStatusCode,
		RedirectPreservePathSuffix: boolInt(redirectPreservePathSuffix),
		RedirectPreserveQuery:      boolInt(redirectPreserveQuery),
		PathSecurityMode:           pathSecurityModeString,
		Enabled:                    boolInt(enabled),
	}, routeTargets, nil
}

func normalizePublicName(name string) (string, error) {
	name = strings.TrimSpace(name)
	if !publicNamePattern.MatchString(name) {
		return "", connect.NewError(connect.CodeInvalidArgument, errors.New("name must be 1-64 letters, numbers, dots, underscores, or hyphens and start with a letter or number"))
	}
	return name, nil
}

func normalizeBindAddress(bindAddress string) (string, error) {
	bindAddress = strings.TrimSpace(bindAddress)
	if bindAddress == "" {
		return "", nil
	}
	if strings.Contains(bindAddress, "/") {
		return "", connect.NewError(connect.CodeInvalidArgument, errors.New("bind address must not include a path"))
	}
	if strings.Contains(bindAddress, ":") {
		if ip := net.ParseIP(bindAddress); ip == nil {
			return "", connect.NewError(connect.CodeInvalidArgument, errors.New("bind address must not include a port"))
		}
	}
	return bindAddress, nil
}

func validatePublicStaticHeaders(headers []*p2pstreamv1.PublicHeader) ([]publicRouteTargetResponseHeaderInput, error) {
	resp := make([]publicRouteTargetResponseHeaderInput, 0, len(headers))
	for _, header := range headers {
		if header == nil {
			continue
		}
		name := strings.TrimSpace(header.Name)
		if name == "" {
			return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("static response header name is required"))
		}
		if !isHTTPToken(name) {
			return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("invalid static response header name %q", name))
		}
		if isBlockedStaticHeader(name) {
			return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("static response header %q is controlled by the proxy", name))
		}
		if strings.ContainsAny(header.Value, "\r\n") {
			return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("static response header %q must not contain CR or LF", name))
		}
		if !utf8.ValidString(header.Value) {
			return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("static response header %q must be valid UTF-8", name))
		}
		resp = append(resp, publicRouteTargetResponseHeaderInput{Name: name, Value: header.Value})
	}
	return resp, nil
}

func validatePublicRouteTargetUpstreamHeaders(
	targetID int64,
	headers []*p2pstreamv1.PublicRouteTargetUpstreamHeader,
	basicAuthEnabled bool,
	existingSecrets existingPublicRouteTargetSecrets,
) ([]publicRouteTargetUpstreamHeaderInput, error) {
	resp := make([]publicRouteTargetUpstreamHeaderInput, 0, len(headers))
	seen := make(map[string]struct{}, len(headers))
	for _, header := range headers {
		if header == nil {
			continue
		}
		name := strings.TrimSpace(header.Name)
		if name == "" {
			return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("upstream request header name is required"))
		}
		if !isHTTPToken(name) {
			return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("invalid upstream request header name %q", name))
		}
		if isBlockedUpstreamRequestHeader(name) {
			return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("upstream request header %q is controlled by the proxy", name))
		}
		lowerName := strings.ToLower(name)
		if _, ok := seen[lowerName]; ok {
			return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("upstream request header %q is duplicated", name))
		}
		seen[lowerName] = struct{}{}
		if basicAuthEnabled && lowerName == "authorization" {
			return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("authorization header cannot be configured when upstream basic auth is enabled"))
		}

		sensitive := header.Sensitive || isForcedSensitiveUpstreamHeader(name)
		value := header.Value
		if sensitive && value == "" {
			if existingValue, ok := existingSensitiveUpstreamHeaderValueForUpdate(existingSecrets, header.Id, targetID, name); ok {
				value = existingValue
			} else {
				return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("sensitive upstream request header %q requires a value", name))
			}
		}
		if err := validateUpstreamHeaderValue(name, value); err != nil {
			return nil, err
		}
		resp = append(resp, publicRouteTargetUpstreamHeaderInput{
			ID:        header.Id,
			Name:      name,
			Value:     value,
			Sensitive: boolInt(sensitive),
		})
	}
	return resp, nil
}

func existingSensitiveUpstreamHeaderValueForUpdate(
	existingSecrets existingPublicRouteTargetSecrets,
	headerID int64,
	targetID int64,
	name string,
) (string, bool) {
	if headerID <= 0 || targetID <= 0 || existingSecrets.UpstreamHeaders == nil {
		return "", false
	}
	existing, ok := existingSecrets.UpstreamHeaders[headerID]
	if !ok || existing.TargetID != targetID || !strings.EqualFold(existing.Name, name) {
		return "", false
	}
	return existing.Value, true
}

func validatePublicRouteTargetBasicAuth(
	targetID int64,
	auth *p2pstreamv1.PublicRouteTargetBasicAuth,
	existingSecrets existingPublicRouteTargetSecrets,
) (int64, string, string, error) {
	if auth == nil || !auth.Enabled {
		return 0, "", "", nil
	}
	username := strings.TrimSpace(auth.Username)
	if username == "" {
		return 0, "", "", connect.NewError(connect.CodeInvalidArgument, errors.New("upstream basic auth username is required"))
	}
	if strings.Contains(username, ":") {
		return 0, "", "", connect.NewError(connect.CodeInvalidArgument, errors.New("upstream basic auth username must not contain ':'"))
	}
	if strings.ContainsAny(username, "\r\n") || !utf8.ValidString(username) {
		return 0, "", "", connect.NewError(connect.CodeInvalidArgument, errors.New("upstream basic auth username must be valid UTF-8 without CR or LF"))
	}

	password := auth.Password
	if password == "" {
		if existingPassword, ok := existingSecrets.BasicAuthPasswords[targetID]; ok && targetID > 0 {
			password = existingPassword
		} else {
			return 0, "", "", connect.NewError(connect.CodeInvalidArgument, errors.New("upstream basic auth password is required"))
		}
	}
	if strings.ContainsAny(password, "\r\n") || !utf8.ValidString(password) {
		return 0, "", "", connect.NewError(connect.CodeInvalidArgument, errors.New("upstream basic auth password must be valid UTF-8 without CR or LF"))
	}
	return 1, username, password, nil
}

func validateUpstreamHeaderValue(name string, value string) error {
	if strings.ContainsAny(value, "\r\n") {
		return connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("upstream request header %q must not contain CR or LF", name))
	}
	if !utf8.ValidString(value) {
		return connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("upstream request header %q must be valid UTF-8", name))
	}
	if len([]byte(value)) > maxUpstreamHeaderValueBytes {
		return connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("upstream request header %q must be at most %d bytes", name, maxUpstreamHeaderValueBytes))
	}
	return nil
}

func isBlockedStaticHeader(name string) bool {
	switch strings.ToLower(name) {
	case "connection", "transfer-encoding", "content-length", "upgrade", "keep-alive", "te", "trailer":
		return true
	default:
		return false
	}
}

func isBlockedUpstreamRequestHeader(name string) bool {
	switch strings.ToLower(name) {
	case "host", "connection", "transfer-encoding", "content-length", "upgrade", "keep-alive", "te", "trailer":
		return true
	default:
		return false
	}
}

func isForcedSensitiveUpstreamHeader(name string) bool {
	switch strings.ToLower(name) {
	case "authorization", "proxy-authorization", "cookie":
		return true
	default:
		return false
	}
}

func isHTTPToken(value string) bool {
	if value == "" {
		return false
	}
	for _, r := range value {
		if r > 127 {
			return false
		}
		if ('a' <= r && r <= 'z') || ('A' <= r && r <= 'Z') || ('0' <= r && r <= '9') {
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

func parsePublicTargetOrigin(targetOrigin string) (*url.URL, error) {
	parsed, err := url.Parse(strings.TrimSpace(targetOrigin))
	if err != nil {
		return nil, fmt.Errorf("invalid target origin: %w", err)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return nil, errors.New("target origin must use http or https")
	}
	if parsed.Host == "" {
		return nil, errors.New("target origin must include a host")
	}
	return parsed, nil
}

func normalizeHostPattern(pattern string) string {
	pattern = strings.TrimSpace(strings.ToLower(pattern))
	return strings.TrimSuffix(pattern, ".")
}

func validateHostPattern(pattern string) error {
	pattern = normalizeHostPattern(pattern)
	if pattern == "" {
		return connect.NewError(connect.CodeInvalidArgument, errors.New("host pattern is required"))
	}
	if strings.ContainsAny(pattern, "/: ") {
		return connect.NewError(connect.CodeInvalidArgument, errors.New("host pattern must not include scheme, port, path, or spaces"))
	}
	if strings.HasPrefix(pattern, "*.") {
		remainder := strings.TrimPrefix(pattern, "*.")
		if remainder == "" || strings.Contains(remainder, "*") {
			return connect.NewError(connect.CodeInvalidArgument, errors.New("wildcard host pattern must look like *.example.com"))
		}
		return nil
	}
	if strings.Contains(pattern, "*") {
		return connect.NewError(connect.CodeInvalidArgument, errors.New("wildcard host pattern must start with *."))
	}
	return nil
}

func publicRouteTargetTypeStringFromProto(targetType p2pstreamv1.PublicRouteTargetType) (string, error) {
	switch targetType {
	case p2pstreamv1.PublicRouteTargetType_PUBLIC_ROUTE_TARGET_TYPE_UNSPECIFIED,
		p2pstreamv1.PublicRouteTargetType_PUBLIC_ROUTE_TARGET_TYPE_PROXY:
		return publicRouteTargetTypeProxy, nil
	case p2pstreamv1.PublicRouteTargetType_PUBLIC_ROUTE_TARGET_TYPE_STATIC:
		return publicRouteTargetTypeStatic, nil
	default:
		return "", connect.NewError(connect.CodeInvalidArgument, errors.New("route target type must be proxy or static"))
	}
}

func publicRouteTargetTransportStringFromProto(transport p2pstreamv1.PublicRouteTargetTransport) (string, error) {
	switch transport {
	case p2pstreamv1.PublicRouteTargetTransport_PUBLIC_ROUTE_TARGET_TRANSPORT_UNSPECIFIED,
		p2pstreamv1.PublicRouteTargetTransport_PUBLIC_ROUTE_TARGET_TRANSPORT_DIRECT:
		return publicRouteTargetTransportDirect, nil
	case p2pstreamv1.PublicRouteTargetTransport_PUBLIC_ROUTE_TARGET_TRANSPORT_AGENT:
		return publicRouteTargetTransportAgent, nil
	default:
		return "", connect.NewError(connect.CodeInvalidArgument, errors.New("route target transport must be direct or agent"))
	}
}

func normalizePublicRouteTargetType(targetType string) string {
	switch strings.TrimSpace(strings.ToLower(targetType)) {
	case "", publicRouteTargetTypeProxy:
		return publicRouteTargetTypeProxy
	case publicRouteTargetTypeStatic:
		return publicRouteTargetTypeStatic
	default:
		return publicRouteTargetTypeProxy
	}
}

func normalizePublicRouteTargetTransport(transport string) string {
	switch strings.TrimSpace(strings.ToLower(transport)) {
	case "", publicRouteTargetTransportDirect:
		return publicRouteTargetTransportDirect
	case publicRouteTargetTransportAgent:
		return publicRouteTargetTransportAgent
	default:
		return publicRouteTargetTransportDirect
	}
}

func routeActionStringFromProto(action p2pstreamv1.PublicRouteAction) (string, error) {
	switch action {
	case p2pstreamv1.PublicRouteAction_PUBLIC_ROUTE_ACTION_UNSPECIFIED,
		p2pstreamv1.PublicRouteAction_PUBLIC_ROUTE_ACTION_FORWARD:
		return publicRouteActionForward, nil
	case p2pstreamv1.PublicRouteAction_PUBLIC_ROUTE_ACTION_REDIRECT:
		return publicRouteActionRedirect, nil
	default:
		return "", connect.NewError(connect.CodeInvalidArgument, errors.New("route action must be forward or redirect"))
	}
}

func routeRedirectTargetModeStringFromProto(mode p2pstreamv1.PublicRouteRedirectTargetMode) (string, error) {
	switch mode {
	case p2pstreamv1.PublicRouteRedirectTargetMode_PUBLIC_ROUTE_REDIRECT_TARGET_MODE_SAME_HOST_PATH:
		return publicRouteRedirectTargetModeSameHostPath, nil
	case p2pstreamv1.PublicRouteRedirectTargetMode_PUBLIC_ROUTE_REDIRECT_TARGET_MODE_EXTERNAL_ORIGIN_KEEP_PATH:
		return publicRouteRedirectTargetModeExternalOriginKeepPath, nil
	case p2pstreamv1.PublicRouteRedirectTargetMode_PUBLIC_ROUTE_REDIRECT_TARGET_MODE_ABSOLUTE_URL:
		return publicRouteRedirectTargetModeAbsoluteURL, nil
	default:
		return "", connect.NewError(connect.CodeInvalidArgument, errors.New("redirect target mode is required"))
	}
}

func validateRouteRedirectTarget(mode string, target string) error {
	if strings.TrimSpace(target) == "" {
		return connect.NewError(connect.CodeInvalidArgument, errors.New("redirect target is required"))
	}
	parsed, err := url.Parse(target)
	if err != nil {
		return connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("invalid redirect target: %w", err))
	}
	switch mode {
	case publicRouteRedirectTargetModeSameHostPath:
		if parsed.IsAbs() || parsed.Host != "" || !strings.HasPrefix(target, "/") || strings.HasPrefix(target, "//") {
			return connect.NewError(connect.CodeInvalidArgument, errors.New("same-host redirect target must be a root-relative path"))
		}
	case publicRouteRedirectTargetModeExternalOriginKeepPath:
		if !isHTTPURL(parsed) || (parsed.Path != "" && parsed.Path != "/") || parsed.RawQuery != "" || parsed.Fragment != "" {
			return connect.NewError(connect.CodeInvalidArgument, errors.New("external-origin redirect target must be an http or https origin"))
		}
	case publicRouteRedirectTargetModeAbsoluteURL:
		if !isHTTPURL(parsed) {
			return connect.NewError(connect.CodeInvalidArgument, errors.New("absolute redirect target must be an http or https URL"))
		}
	default:
		return connect.NewError(connect.CodeInvalidArgument, errors.New("unsupported redirect target mode"))
	}
	return nil
}

func isHTTPURL(value *url.URL) bool {
	if value == nil || value.Host == "" {
		return false
	}
	return value.Scheme == "http" || value.Scheme == "https"
}

func validRedirectStatusCode(statusCode int64) bool {
	switch statusCode {
	case http.StatusMovedPermanently, http.StatusFound, http.StatusTemporaryRedirect, http.StatusPermanentRedirect:
		return true
	default:
		return false
	}
}

func loadBalancingStringFromProto(loadBalancing p2pstreamv1.PublicRouteTargetLoadBalancing) (string, error) {
	switch loadBalancing {
	case p2pstreamv1.PublicRouteTargetLoadBalancing_PUBLIC_ROUTE_TARGET_LOAD_BALANCING_UNSPECIFIED,
		p2pstreamv1.PublicRouteTargetLoadBalancing_PUBLIC_ROUTE_TARGET_LOAD_BALANCING_ROUND_ROBIN:
		return publicRouteTargetLoadBalancingRoundRobin, nil
	case p2pstreamv1.PublicRouteTargetLoadBalancing_PUBLIC_ROUTE_TARGET_LOAD_BALANCING_WEIGHTED_ROUND_ROBIN:
		return publicRouteTargetLoadBalancingWeightedRoundRobin, nil
	case p2pstreamv1.PublicRouteTargetLoadBalancing_PUBLIC_ROUTE_TARGET_LOAD_BALANCING_RANDOM:
		return publicRouteTargetLoadBalancingRandom, nil
	case p2pstreamv1.PublicRouteTargetLoadBalancing_PUBLIC_ROUTE_TARGET_LOAD_BALANCING_WEIGHTED_RANDOM:
		return publicRouteTargetLoadBalancingWeightedRandom, nil
	case p2pstreamv1.PublicRouteTargetLoadBalancing_PUBLIC_ROUTE_TARGET_LOAD_BALANCING_LEAST_ACTIVE_REQUESTS:
		return publicRouteTargetLoadBalancingLeastActiveRequests, nil
	case p2pstreamv1.PublicRouteTargetLoadBalancing_PUBLIC_ROUTE_TARGET_LOAD_BALANCING_WEIGHTED_LEAST_ACTIVE_REQUESTS:
		return publicRouteTargetLoadBalancingWeightedLeastActiveRequests, nil
	default:
		return "", connect.NewError(connect.CodeInvalidArgument, errors.New("unsupported backend load balancing algorithm"))
	}
}

func normalizePublicTargetType(backendType string) string {
	switch strings.TrimSpace(strings.ToLower(backendType)) {
	case "", publicRouteTargetTypeProxy:
		return publicRouteTargetTypeProxy
	case publicRouteTargetTypeStatic:
		return publicRouteTargetTypeStatic
	default:
		return strings.TrimSpace(strings.ToLower(backendType))
	}
}

func normalizePublicRouteTargetLoadBalancing(loadBalancing string) string {
	switch strings.TrimSpace(strings.ToLower(loadBalancing)) {
	case "", publicRouteTargetLoadBalancingRoundRobin:
		return publicRouteTargetLoadBalancingRoundRobin
	case publicRouteTargetLoadBalancingWeightedRoundRobin:
		return publicRouteTargetLoadBalancingWeightedRoundRobin
	case publicRouteTargetLoadBalancingRandom:
		return publicRouteTargetLoadBalancingRandom
	case publicRouteTargetLoadBalancingWeightedRandom:
		return publicRouteTargetLoadBalancingWeightedRandom
	case publicRouteTargetLoadBalancingLeastActiveRequests:
		return publicRouteTargetLoadBalancingLeastActiveRequests
	case publicRouteTargetLoadBalancingWeightedLeastActiveRequests:
		return publicRouteTargetLoadBalancingWeightedLeastActiveRequests
	default:
		return strings.TrimSpace(strings.ToLower(loadBalancing))
	}
}

func normalizePublicRouteAction(action string) string {
	switch strings.TrimSpace(strings.ToLower(action)) {
	case "", publicRouteActionForward:
		return publicRouteActionForward
	case publicRouteActionRedirect:
		return publicRouteActionRedirect
	default:
		return strings.TrimSpace(strings.ToLower(action))
	}
}

func normalizePublicRouteRedirectTargetMode(mode string) string {
	switch strings.TrimSpace(strings.ToLower(mode)) {
	case publicRouteRedirectTargetModeSameHostPath:
		return publicRouteRedirectTargetModeSameHostPath
	case publicRouteRedirectTargetModeExternalOriginKeepPath:
		return publicRouteRedirectTargetModeExternalOriginKeepPath
	case publicRouteRedirectTargetModeAbsoluteURL:
		return publicRouteRedirectTargetModeAbsoluteURL
	default:
		return strings.TrimSpace(strings.ToLower(mode))
	}
}

func normalizePublicRoutePathSecurityMode(mode string) string {
	switch strings.TrimSpace(strings.ToLower(mode)) {
	case "", publicRoutePathSecurityModeStrict:
		return publicRoutePathSecurityModeStrict
	case publicRoutePathSecurityModeAllowEncodedSeparators:
		return publicRoutePathSecurityModeAllowEncodedSeparators
	default:
		return strings.TrimSpace(strings.ToLower(mode))
	}
}

func routePathSecurityModeStringFromProto(mode p2pstreamv1.PublicRoutePathSecurityMode) (string, error) {
	switch mode {
	case p2pstreamv1.PublicRoutePathSecurityMode_PUBLIC_ROUTE_PATH_SECURITY_MODE_UNSPECIFIED,
		p2pstreamv1.PublicRoutePathSecurityMode_PUBLIC_ROUTE_PATH_SECURITY_MODE_STRICT:
		return publicRoutePathSecurityModeStrict, nil
	case p2pstreamv1.PublicRoutePathSecurityMode_PUBLIC_ROUTE_PATH_SECURITY_MODE_ALLOW_ENCODED_SEPARATORS:
		return publicRoutePathSecurityModeAllowEncodedSeparators, nil
	default:
		return "", connect.NewError(connect.CodeInvalidArgument, errors.New("route path security mode must be strict or allow encoded separators"))
	}
}

func tlsCertificateSourceStringFromProto(source p2pstreamv1.PublicTlsCertificateSource) (string, error) {
	switch source {
	case p2pstreamv1.PublicTlsCertificateSource_PUBLIC_TLS_CERTIFICATE_SOURCE_UNSPECIFIED,
		p2pstreamv1.PublicTlsCertificateSource_PUBLIC_TLS_CERTIFICATE_SOURCE_MANUAL:
		return publicTLSCertificateSourceManual, nil
	case p2pstreamv1.PublicTlsCertificateSource_PUBLIC_TLS_CERTIFICATE_SOURCE_ACME:
		return publicTLSCertificateSourceACME, nil
	default:
		return "", connect.NewError(connect.CodeInvalidArgument, errors.New("TLS certificate source must be manual or ACME"))
	}
}

func acmeChallengeTypeStringFromProto(challengeType p2pstreamv1.PublicAcmeChallengeType) (string, error) {
	switch challengeType {
	case p2pstreamv1.PublicAcmeChallengeType_PUBLIC_ACME_CHALLENGE_TYPE_HTTP_01:
		return publicACMEChallengeHTTP01, nil
	case p2pstreamv1.PublicAcmeChallengeType_PUBLIC_ACME_CHALLENGE_TYPE_TLS_ALPN_01:
		return publicACMEChallengeTLSALPN01, nil
	case p2pstreamv1.PublicAcmeChallengeType_PUBLIC_ACME_CHALLENGE_TYPE_DNS_01:
		return publicACMEChallengeDNS01, nil
	default:
		return "", connect.NewError(connect.CodeInvalidArgument, errors.New("ACME challenge type is required"))
	}
}

func acmeCAStringFromProto(ca p2pstreamv1.PublicAcmeCa) (string, error) {
	switch ca {
	case p2pstreamv1.PublicAcmeCa_PUBLIC_ACME_CA_UNSPECIFIED,
		p2pstreamv1.PublicAcmeCa_PUBLIC_ACME_CA_LETS_ENCRYPT_PRODUCTION:
		return publicACMECAProduction, nil
	case p2pstreamv1.PublicAcmeCa_PUBLIC_ACME_CA_LETS_ENCRYPT_STAGING:
		return publicACMECAStaging, nil
	default:
		return "", connect.NewError(connect.CodeInvalidArgument, errors.New("ACME CA must be Let's Encrypt production or staging"))
	}
}

func dnsProviderStringFromProto(provider p2pstreamv1.PublicDnsProvider) (string, error) {
	switch provider {
	case p2pstreamv1.PublicDnsProvider_PUBLIC_DNS_PROVIDER_UNSPECIFIED,
		p2pstreamv1.PublicDnsProvider_PUBLIC_DNS_PROVIDER_CLOUDFLARE:
		return publicDNSProviderCloudflare, nil
	default:
		return "", connect.NewError(connect.CodeInvalidArgument, errors.New("DNS provider must be Cloudflare"))
	}
}

func normalizePublicTLSCertificateSource(source string) string {
	switch strings.TrimSpace(strings.ToLower(source)) {
	case "", publicTLSCertificateSourceManual:
		return publicTLSCertificateSourceManual
	case publicTLSCertificateSourceACME:
		return publicTLSCertificateSourceACME
	default:
		return strings.TrimSpace(strings.ToLower(source))
	}
}

func normalizePublicACMEChallengeType(challengeType string) string {
	switch strings.TrimSpace(strings.ToLower(challengeType)) {
	case publicACMEChallengeHTTP01, "http-01":
		return publicACMEChallengeHTTP01
	case publicACMEChallengeTLSALPN01, "tls-alpn-01":
		return publicACMEChallengeTLSALPN01
	case publicACMEChallengeDNS01, "dns-01":
		return publicACMEChallengeDNS01
	default:
		return strings.TrimSpace(strings.ToLower(challengeType))
	}
}

func normalizePublicACMECA(ca string) string {
	switch strings.TrimSpace(strings.ToLower(ca)) {
	case "", publicACMECAProduction:
		return publicACMECAProduction
	case publicACMECAStaging:
		return publicACMECAStaging
	default:
		return strings.TrimSpace(strings.ToLower(ca))
	}
}

func normalizePublicDNSProvider(provider string) string {
	switch strings.TrimSpace(strings.ToLower(provider)) {
	case "", publicDNSProviderCloudflare:
		return publicDNSProviderCloudflare
	default:
		return strings.TrimSpace(strings.ToLower(provider))
	}
}

func normalizePublicTLSCertificateStatus(status string) string {
	switch strings.TrimSpace(strings.ToLower(status)) {
	case "", publicTLSCertificateStatusReady:
		return publicTLSCertificateStatusReady
	case publicTLSCertificateStatusPending, publicTLSCertificateStatusRenewing, publicTLSCertificateStatusError:
		return strings.TrimSpace(strings.ToLower(status))
	default:
		return strings.TrimSpace(strings.ToLower(status))
	}
}

func protocolStringFromProto(protocol p2pstreamv1.PublicListenerProtocol) (string, error) {
	switch protocol {
	case p2pstreamv1.PublicListenerProtocol_PUBLIC_LISTENER_PROTOCOL_HTTP:
		return publicListenerProtocolHTTP, nil
	case p2pstreamv1.PublicListenerProtocol_PUBLIC_LISTENER_PROTOCOL_HTTPS:
		return publicListenerProtocolHTTPS, nil
	default:
		return "", connect.NewError(connect.CodeInvalidArgument, errors.New("listener protocol must be http or https"))
	}
}

func boolInt(value bool) int64 {
	if value {
		return 1
	}
	return 0
}

func publicDBError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, sql.ErrNoRows) {
		return connect.NewError(connect.CodeNotFound, err)
	}
	msg := strings.ToLower(err.Error())
	if strings.Contains(msg, "unique constraint failed") {
		return connect.NewError(connect.CodeAlreadyExists, err)
	}
	if strings.Contains(msg, "foreign key constraint failed") {
		return connect.NewError(connect.CodeFailedPrecondition, err)
	}
	return connect.NewError(connect.CodeInternal, err)
}
