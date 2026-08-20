package server

import (
	"context"
	"crypto/tls"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"connectrpc.com/connect"
	"golang.org/x/net/http/httpguts"

	p2pstreamv1 "p2pstream/gen/proto/p2pstream/v1"
	"p2pstream/internal/db"
)

const (
	publicAccessProviderTypeForwardAuth = "forward_auth"
	publicAccessGroupMatchAny           = "any"
	publicAccessGroupMatchAll           = "all"
	defaultPublicAccessTimeoutMillis    = int64(5000)
	minPublicAccessTimeoutMillis        = int64(100)
	maxPublicAccessTimeoutMillis        = int64(30000)
	maxPublicAccessHeaders              = 16
	maxPublicAccessGroups               = 64
)

var defaultPublicAccessForwardedHeaders = []string{
	"X-Auth-Request-User",
	"X-Auth-Request-Email",
	"X-Auth-Request-Groups",
	"X-Auth-Request-Preferred-Username",
}

type publicAccessProviderConfig struct {
	ID               int64
	Name             string
	ProviderType     string
	Enabled          bool
	ForwardAuthURL   string
	ParsedURL        *url.URL
	Timeout          time.Duration
	TLSSkipVerify    bool
	SubjectHeader    string
	UserHeader       string
	EmailHeader      string
	GroupsHeader     string
	ForwardedHeaders []string
	client           HTTPClient
	transport        idleConnectionsCloser
}

type idleConnectionsCloser interface {
	CloseIdleConnections()
}

type publicAccessPolicyConfig struct {
	ID             int64
	Name           string
	ProviderID     int64
	Enabled        bool
	RequiredGroups []string
	GroupMatch     string
}

func publicAccessProviderRowToConfig(row db.PublicAccessProvider) (publicAccessProviderConfig, error) {
	parsed, err := parsePublicForwardAuthURL(row.ForwardAuthUrl)
	if err != nil {
		return publicAccessProviderConfig{}, err
	}
	headers, err := publicAccessStringListFromJSON(row.ForwardedHeadersJson)
	if err != nil {
		return publicAccessProviderConfig{}, fmt.Errorf("forwarded headers: %w", err)
	}
	headers, err = normalizePublicAccessHeaderList(headers, false)
	if err != nil {
		return publicAccessProviderConfig{}, err
	}
	timeout := row.TimeoutMillis
	if timeout < minPublicAccessTimeoutMillis || timeout > maxPublicAccessTimeoutMillis {
		return publicAccessProviderConfig{}, errors.New("timeout must be between 100 and 30000 milliseconds")
	}
	providerType := normalizePublicAccessProviderType(row.ProviderType)
	if providerType != publicAccessProviderTypeForwardAuth {
		return publicAccessProviderConfig{}, errors.New("unsupported access provider type")
	}
	transport := newDirectPooledHTTPTransport(row.TlsSkipVerify != 0, time.Duration(timeout)*time.Millisecond, 32)
	transport.MaxResponseHeaderBytes = maxPublicForwardAuthHeaderBytes
	if transport.TLSClientConfig == nil {
		transport.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS12}
	} else {
		transport.TLSClientConfig.MinVersion = tls.VersionTLS12
	}
	client := &http.Client{
		Transport: transport,
		Timeout:   time.Duration(timeout) * time.Millisecond,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	return publicAccessProviderConfig{
		ID:               row.ID,
		Name:             row.Name,
		ProviderType:     providerType,
		Enabled:          row.Enabled != 0,
		ForwardAuthURL:   parsed.String(),
		ParsedURL:        parsed,
		Timeout:          time.Duration(timeout) * time.Millisecond,
		TLSSkipVerify:    row.TlsSkipVerify != 0,
		SubjectHeader:    http.CanonicalHeaderKey(row.SubjectHeader),
		UserHeader:       http.CanonicalHeaderKey(row.UserHeader),
		EmailHeader:      http.CanonicalHeaderKey(row.EmailHeader),
		GroupsHeader:     http.CanonicalHeaderKey(row.GroupsHeader),
		ForwardedHeaders: headers,
		client:           client,
		transport:        transport,
	}, nil
}

type publicAccessProviderTransportSignature struct {
	ForwardAuthURL string
	Timeout        time.Duration
	TLSSkipVerify  bool
}

func reconcilePublicAccessProviderTransports(previous, current *publicProxySnapshot) {
	if previous == nil || previous == current {
		return
	}
	if current == nil {
		closePublicAccessProviderIdleConnections(previous)
		return
	}
	for providerID, previousProvider := range previous.AccessProviders {
		currentProvider, ok := current.AccessProviders[providerID]
		if ok &&
			previousProvider.client != nil &&
			previousProvider.transport != nil &&
			currentProvider.transport != nil &&
			publicAccessProviderTransportSignatureFor(previousProvider) == publicAccessProviderTransportSignatureFor(currentProvider) {
			currentProvider.transport.CloseIdleConnections()
			currentProvider.client = previousProvider.client
			currentProvider.transport = previousProvider.transport
			current.AccessProviders[providerID] = currentProvider
			continue
		}
		if previousProvider.transport != nil {
			previousProvider.transport.CloseIdleConnections()
		}
	}
}

func closePublicAccessProviderIdleConnections(snap *publicProxySnapshot) {
	if snap == nil {
		return
	}
	for _, provider := range snap.AccessProviders {
		if provider.transport != nil {
			provider.transport.CloseIdleConnections()
		}
	}
}

func publicAccessProviderTransportSignatureFor(provider publicAccessProviderConfig) publicAccessProviderTransportSignature {
	return publicAccessProviderTransportSignature{
		ForwardAuthURL: provider.ForwardAuthURL,
		Timeout:        provider.Timeout,
		TLSSkipVerify:  provider.TLSSkipVerify,
	}
}

func publicAccessPolicyRowToConfig(row db.PublicAccessPolicy) (publicAccessPolicyConfig, error) {
	groups, err := publicAccessStringListFromJSON(row.RequiredGroupsJson)
	if err != nil {
		return publicAccessPolicyConfig{}, fmt.Errorf("required groups: %w", err)
	}
	groups, err = normalizePublicAccessGroups(groups)
	if err != nil {
		return publicAccessPolicyConfig{}, err
	}
	groupMatch := normalizePublicAccessGroupMatch(row.GroupMatch)
	if groupMatch != publicAccessGroupMatchAny && groupMatch != publicAccessGroupMatchAll {
		return publicAccessPolicyConfig{}, errors.New("unsupported group match mode")
	}
	return publicAccessPolicyConfig{
		ID:             row.ID,
		Name:           row.Name,
		ProviderID:     row.ProviderID,
		Enabled:        row.Enabled != 0,
		RequiredGroups: groups,
		GroupMatch:     groupMatch,
	}, nil
}

func validatePublicAccessProviderInput(
	name string,
	providerType p2pstreamv1.PublicAccessProviderType,
	enabled bool,
	forwardAuthURL string,
	timeoutMillis int64,
	tlsSkipVerify bool,
	subjectHeader string,
	userHeader string,
	emailHeader string,
	groupsHeader string,
	forwardedHeaders []string,
) (db.UpdatePublicAccessProviderParams, error) {
	name, err := normalizePublicName(name)
	if err != nil {
		return db.UpdatePublicAccessProviderParams{}, err
	}
	typeString, err := publicAccessProviderTypeStringFromProto(providerType)
	if err != nil {
		return db.UpdatePublicAccessProviderParams{}, err
	}
	parsed, err := parsePublicForwardAuthURL(forwardAuthURL)
	if err != nil {
		return db.UpdatePublicAccessProviderParams{}, connect.NewError(connect.CodeInvalidArgument, err)
	}
	if timeoutMillis == 0 {
		timeoutMillis = defaultPublicAccessTimeoutMillis
	}
	if timeoutMillis < minPublicAccessTimeoutMillis || timeoutMillis > maxPublicAccessTimeoutMillis {
		return db.UpdatePublicAccessProviderParams{}, connect.NewError(connect.CodeInvalidArgument, errors.New("forward-auth timeout must be between 100 and 30000 milliseconds"))
	}
	subjectHeader, err = normalizePublicAccessHeader(subjectHeader, "X-Auth-Request-Preferred-Username")
	if err != nil {
		return db.UpdatePublicAccessProviderParams{}, err
	}
	userHeader, err = normalizePublicAccessHeader(userHeader, "X-Auth-Request-User")
	if err != nil {
		return db.UpdatePublicAccessProviderParams{}, err
	}
	emailHeader, err = normalizePublicAccessHeader(emailHeader, "X-Auth-Request-Email")
	if err != nil {
		return db.UpdatePublicAccessProviderParams{}, err
	}
	groupsHeader, err = normalizePublicAccessHeader(groupsHeader, "X-Auth-Request-Groups")
	if err != nil {
		return db.UpdatePublicAccessProviderParams{}, err
	}
	if len(forwardedHeaders) == 0 {
		forwardedHeaders = append([]string(nil), defaultPublicAccessForwardedHeaders...)
	}
	forwardedHeaders, err = normalizePublicAccessHeaderList(forwardedHeaders, true)
	if err != nil {
		return db.UpdatePublicAccessProviderParams{}, err
	}
	forwardedJSON, err := json.Marshal(forwardedHeaders)
	if err != nil {
		return db.UpdatePublicAccessProviderParams{}, connect.NewError(connect.CodeInternal, err)
	}
	return db.UpdatePublicAccessProviderParams{
		Name:                 name,
		ProviderType:         typeString,
		Enabled:              boolInt(enabled),
		ForwardAuthUrl:       parsed.String(),
		TimeoutMillis:        timeoutMillis,
		TlsSkipVerify:        boolInt(tlsSkipVerify),
		SubjectHeader:        subjectHeader,
		UserHeader:           userHeader,
		EmailHeader:          emailHeader,
		GroupsHeader:         groupsHeader,
		ForwardedHeadersJson: string(forwardedJSON),
	}, nil
}

func validatePublicAccessPolicyInput(
	ctx context.Context,
	database *db.DB,
	name string,
	providerID int64,
	enabled bool,
	requiredGroups []string,
	groupMatch p2pstreamv1.PublicAccessGroupMatch,
) (db.UpdatePublicAccessPolicyParams, error) {
	name, err := normalizePublicName(name)
	if err != nil {
		return db.UpdatePublicAccessPolicyParams{}, err
	}
	if providerID <= 0 {
		return db.UpdatePublicAccessPolicyParams{}, connect.NewError(connect.CodeInvalidArgument, errors.New("access provider is required"))
	}
	if _, err := database.GetPublicAccessProvider(ctx, providerID); err != nil {
		return db.UpdatePublicAccessPolicyParams{}, publicDBError(err)
	}
	groups, err := normalizePublicAccessGroups(requiredGroups)
	if err != nil {
		return db.UpdatePublicAccessPolicyParams{}, connect.NewError(connect.CodeInvalidArgument, err)
	}
	matchString, err := publicAccessGroupMatchStringFromProto(groupMatch)
	if err != nil {
		return db.UpdatePublicAccessPolicyParams{}, err
	}
	groupsJSON, err := json.Marshal(groups)
	if err != nil {
		return db.UpdatePublicAccessPolicyParams{}, connect.NewError(connect.CodeInternal, err)
	}
	return db.UpdatePublicAccessPolicyParams{
		Name:               name,
		ProviderID:         providerID,
		Enabled:            boolInt(enabled),
		RequiredGroupsJson: string(groupsJSON),
		GroupMatch:         matchString,
	}, nil
}

func parsePublicForwardAuthURL(raw string) (*url.URL, error) {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed == nil {
		return nil, errors.New("forward-auth URL is invalid")
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return nil, errors.New("forward-auth URL must use http or https")
	}
	if parsed.Host == "" {
		return nil, errors.New("forward-auth URL requires a host")
	}
	if parsed.User != nil {
		return nil, errors.New("forward-auth URL must not contain credentials")
	}
	if parsed.Fragment != "" {
		return nil, errors.New("forward-auth URL must not contain a fragment")
	}
	return parsed, nil
}

func normalizePublicAccessHeader(value, fallback string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		value = fallback
	}
	if len(value) > 128 || !httpguts.ValidHeaderFieldName(value) {
		return "", connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("invalid access identity header %q", value))
	}
	return http.CanonicalHeaderKey(value), nil
}

func normalizePublicAccessHeaderList(values []string, connectErrors bool) ([]string, error) {
	if len(values) > maxPublicAccessHeaders {
		return nil, publicAccessValidationError(connectErrors, "at most 16 identity headers may be forwarded")
	}
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if len(value) > 128 || !httpguts.ValidHeaderFieldName(value) {
			return nil, publicAccessValidationError(connectErrors, fmt.Sprintf("invalid forwarded identity header %q", value))
		}
		canonical := http.CanonicalHeaderKey(value)
		if publicAccessForbiddenForwardedHeader(canonical) {
			return nil, publicAccessValidationError(connectErrors, fmt.Sprintf("identity header %q cannot be forwarded", canonical))
		}
		key := strings.ToLower(canonical)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		result = append(result, canonical)
	}
	sort.SliceStable(result, func(i, j int) bool { return strings.ToLower(result[i]) < strings.ToLower(result[j]) })
	return result, nil
}

func publicAccessForbiddenForwardedHeader(name string) bool {
	lower := strings.ToLower(strings.TrimSpace(name))
	if strings.HasPrefix(lower, "x-forwarded-") {
		return true
	}
	switch lower {
	case "authorization", "cookie", "set-cookie", "forwarded", "host", "connection", "content-length", "expect", "keep-alive", "proxy-authenticate", "proxy-authorization", "proxy-connection", "te", "trailer", "transfer-encoding", "upgrade", "x-real-ip":
		return true
	default:
		return false
	}
}

func normalizePublicAccessGroups(values []string) ([]string, error) {
	if len(values) > maxPublicAccessGroups {
		return nil, errors.New("at most 64 required groups may be configured")
	}
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" || len(value) > 128 || strings.ContainsAny(value, "\r\n,") {
			return nil, fmt.Errorf("invalid required group %q", value)
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	sort.Strings(result)
	return result, nil
}

func publicAccessValidationError(asConnect bool, message string) error {
	err := errors.New(message)
	if asConnect {
		return connect.NewError(connect.CodeInvalidArgument, err)
	}
	return err
}

func publicAccessStringListFromJSON(raw string) ([]string, error) {
	var values []string
	if err := json.Unmarshal([]byte(raw), &values); err != nil {
		return nil, err
	}
	return values, nil
}

func normalizePublicAccessProviderType(value string) string {
	return strings.TrimSpace(strings.ToLower(value))
}

func normalizePublicAccessGroupMatch(value string) string {
	value = strings.TrimSpace(strings.ToLower(value))
	if value == "" {
		return publicAccessGroupMatchAny
	}
	return value
}

func publicAccessProviderTypeStringFromProto(value p2pstreamv1.PublicAccessProviderType) (string, error) {
	switch value {
	case p2pstreamv1.PublicAccessProviderType_PUBLIC_ACCESS_PROVIDER_TYPE_FORWARD_AUTH,
		p2pstreamv1.PublicAccessProviderType_PUBLIC_ACCESS_PROVIDER_TYPE_UNSPECIFIED:
		return publicAccessProviderTypeForwardAuth, nil
	default:
		return "", connect.NewError(connect.CodeInvalidArgument, errors.New("unsupported access provider type"))
	}
}

func publicAccessGroupMatchStringFromProto(value p2pstreamv1.PublicAccessGroupMatch) (string, error) {
	switch value {
	case p2pstreamv1.PublicAccessGroupMatch_PUBLIC_ACCESS_GROUP_MATCH_ALL:
		return publicAccessGroupMatchAll, nil
	case p2pstreamv1.PublicAccessGroupMatch_PUBLIC_ACCESS_GROUP_MATCH_ANY,
		p2pstreamv1.PublicAccessGroupMatch_PUBLIC_ACCESS_GROUP_MATCH_UNSPECIFIED:
		return publicAccessGroupMatchAny, nil
	default:
		return "", connect.NewError(connect.CodeInvalidArgument, errors.New("unsupported access group match mode"))
	}
}

func protoPublicAccessProviderType(value string) p2pstreamv1.PublicAccessProviderType {
	if normalizePublicAccessProviderType(value) == publicAccessProviderTypeForwardAuth {
		return p2pstreamv1.PublicAccessProviderType_PUBLIC_ACCESS_PROVIDER_TYPE_FORWARD_AUTH
	}
	return p2pstreamv1.PublicAccessProviderType_PUBLIC_ACCESS_PROVIDER_TYPE_UNSPECIFIED
}

func protoPublicAccessGroupMatch(value string) p2pstreamv1.PublicAccessGroupMatch {
	if normalizePublicAccessGroupMatch(value) == publicAccessGroupMatchAll {
		return p2pstreamv1.PublicAccessGroupMatch_PUBLIC_ACCESS_GROUP_MATCH_ALL
	}
	return p2pstreamv1.PublicAccessGroupMatch_PUBLIC_ACCESS_GROUP_MATCH_ANY
}

func publicAccessProviderToProto(row db.PublicAccessProvider) *p2pstreamv1.PublicAccessProvider {
	headers, _ := publicAccessStringListFromJSON(row.ForwardedHeadersJson)
	return &p2pstreamv1.PublicAccessProvider{
		Id:                  row.ID,
		Name:                row.Name,
		ProviderType:        protoPublicAccessProviderType(row.ProviderType),
		Enabled:             row.Enabled != 0,
		ForwardAuthUrl:      row.ForwardAuthUrl,
		TimeoutMillis:       row.TimeoutMillis,
		TlsSkipVerify:       row.TlsSkipVerify != 0,
		SubjectHeader:       row.SubjectHeader,
		UserHeader:          row.UserHeader,
		EmailHeader:         row.EmailHeader,
		GroupsHeader:        row.GroupsHeader,
		ForwardedHeaders:    headers,
		CreatedAtUnixMillis: row.CreatedAt.UnixMilli(),
		UpdatedAtUnixMillis: row.UpdatedAt.UnixMilli(),
	}
}

func publicAccessProvidersToProto(rows []db.PublicAccessProvider) []*p2pstreamv1.PublicAccessProvider {
	result := make([]*p2pstreamv1.PublicAccessProvider, 0, len(rows))
	for _, row := range rows {
		result = append(result, publicAccessProviderToProto(row))
	}
	return result
}

func publicAccessPolicyToProto(row db.PublicAccessPolicy) *p2pstreamv1.PublicAccessPolicy {
	groups, _ := publicAccessStringListFromJSON(row.RequiredGroupsJson)
	return &p2pstreamv1.PublicAccessPolicy{
		Id:                  row.ID,
		Name:                row.Name,
		ProviderId:          row.ProviderID,
		Enabled:             row.Enabled != 0,
		RequiredGroups:      groups,
		GroupMatch:          protoPublicAccessGroupMatch(row.GroupMatch),
		CreatedAtUnixMillis: row.CreatedAt.UnixMilli(),
		UpdatedAtUnixMillis: row.UpdatedAt.UnixMilli(),
	}
}

func publicAccessPoliciesToProto(rows []db.PublicAccessPolicy) []*p2pstreamv1.PublicAccessPolicy {
	result := make([]*p2pstreamv1.PublicAccessPolicy, 0, len(rows))
	for _, row := range rows {
		result = append(result, publicAccessPolicyToProto(row))
	}
	return result
}

func publicAccessPolicyID(value int64) sql.NullInt64 {
	return sql.NullInt64{Int64: value, Valid: value > 0}
}
