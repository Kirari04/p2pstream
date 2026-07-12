package server

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/netip"
	"net/url"
	"sort"
	"strings"
	"time"

	"connectrpc.com/connect"

	p2pstreamv1 "p2pstream/gen/proto/p2pstream/v1"
	"p2pstream/internal/db"
)

const (
	maxPublicTrustedProxyCIDRs = 4096
	maxPublicRefreshErrorBytes = 2048
)

// PublicGeoIPDatabaseInfo is the validated metadata returned after atomically
// installing a GeoLite2 Country database.
type PublicGeoIPDatabaseInfo struct {
	DatabaseType string
	BuildAt      time.Time
}

// PublicGeoConfigRefresher keeps network and filesystem work out of the
// management data layer. Implementations must retain their last-known-good
// database or prefix set when a refresh fails.
type PublicGeoConfigRefresher interface {
	RefreshGeoIPDatabase(ctx context.Context, accountID, licenseKey string) (PublicGeoIPDatabaseInfo, error)
	RefreshTrustedProxySource(ctx context.Context, provider TrustedProxyProvider) ([]netip.Prefix, error)
}

type publicGeoIPRuntimeInfoProvider interface {
	GeoIPInfo() (GeoIPCountryDatabaseInfo, bool)
}

func (a *App) ensurePublicGeoIPSettings(ctx context.Context) (db.PublicGeoIpSetting, error) {
	row, err := a.DB.GetPublicGeoIpSettings(ctx)
	if err == nil {
		return row, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return db.PublicGeoIpSetting{}, connect.NewError(connect.CodeInternal, err)
	}
	row, err = a.DB.UpsertPublicGeoIpSettingsDefaults(ctx)
	if err != nil {
		return db.PublicGeoIpSetting{}, connect.NewError(connect.CodeInternal, err)
	}
	return row, nil
}

func publicGeoIPSettingsToProto(row db.PublicGeoIpSetting) *p2pstreamv1.PublicGeoIpSettings {
	return &p2pstreamv1.PublicGeoIpSettings{
		Enabled:              row.Enabled != 0,
		MaxmindAccountId:     row.MaxmindAccountID,
		MaxmindLicenseKeySet: strings.TrimSpace(row.MaxmindLicenseKey) != "",
		CreatedAtUnixMillis:  row.CreatedAt.UnixMilli(),
		UpdatedAtUnixMillis:  row.UpdatedAt.UnixMilli(),
		DatabaseStatus: &p2pstreamv1.PublicGeoIpDatabaseStatus{
			Ready:                         row.DatabaseType != "" && row.DatabaseBuildAt.Valid && row.LastUpdateSuccessAt.Valid,
			DatabaseType:                  row.DatabaseType,
			BuildAtUnixMillis:             nullTimeUnixMillis(row.DatabaseBuildAt),
			LastUpdateAttemptAtUnixMillis: nullTimeUnixMillis(row.LastUpdateAttemptAt),
			LastUpdateSuccessAtUnixMillis: nullTimeUnixMillis(row.LastUpdateSuccessAt),
			LastUpdateError:               row.LastUpdateError,
		},
	}
}

func (a *App) publicGeoIPSettingsProto(row db.PublicGeoIpSetting) *p2pstreamv1.PublicGeoIpSettings {
	settings := publicGeoIPSettingsToProto(row)
	settings.DatabaseStatus.Ready = settings.DatabaseStatus.Ready && a.publicGeoIPRuntimeMatches(row)
	return settings
}

func (a *App) publicGeoIPRuntimeInfo() (GeoIPCountryDatabaseInfo, bool) {
	if a == nil {
		return GeoIPCountryDatabaseInfo{}, false
	}
	runtime, ok := a.GeoConfigRefresher.(publicGeoIPRuntimeInfoProvider)
	if !ok || runtime == nil {
		return GeoIPCountryDatabaseInfo{}, false
	}
	lookup, ok := a.GeoConfigRefresher.(publicGeoCountryLookup)
	if !ok || lookup == nil {
		return GeoIPCountryDatabaseInfo{}, false
	}
	info, ready := runtime.GeoIPInfo()
	if !ready || strings.TrimSpace(info.DatabaseType) == "" || info.BuildTime.IsZero() {
		return GeoIPCountryDatabaseInfo{}, false
	}
	return info, true
}

func (a *App) publicGeoIPRuntimeMatches(row db.PublicGeoIpSetting) bool {
	info, ready := a.publicGeoIPRuntimeInfo()
	return ready && row.DatabaseBuildAt.Valid &&
		strings.TrimSpace(info.DatabaseType) == strings.TrimSpace(row.DatabaseType) &&
		info.BuildTime.Unix() == row.DatabaseBuildAt.Time.Unix()
}

func publicTrustedProxySourcesToProto(rows []db.PublicTrustedProxySource) []*p2pstreamv1.PublicTrustedProxySource {
	result := make([]*p2pstreamv1.PublicTrustedProxySource, 0, len(rows))
	for _, row := range rows {
		result = append(result, publicTrustedProxySourceToProto(row))
	}
	return result
}

func publicTrustedProxySourceToProto(row db.PublicTrustedProxySource) *p2pstreamv1.PublicTrustedProxySource {
	cidrs := decodePublicTrustedProxyCIDRs(row.CidrsJson)
	visibleCIDRs := cidrs
	if row.BuiltIn != 0 {
		visibleCIDRs = []string{}
	}
	return &p2pstreamv1.PublicTrustedProxySource{
		Id:                             row.ID,
		Name:                           row.Name,
		Provider:                       protoPublicTrustedProxyProvider(TrustedProxyProvider(row.Provider)),
		BuiltIn:                        row.BuiltIn != 0,
		Enabled:                        row.Enabled != 0,
		Cidrs:                          visibleCIDRs,
		HeaderName:                     row.HeaderName,
		HeaderMode:                     protoPublicTrustedProxyHeaderMode(TrustedProxyHeaderMode(row.HeaderMode)),
		CidrCount:                      int64(len(cidrs)),
		LastRefreshAttemptAtUnixMillis: nullTimeUnixMillis(row.LastRefreshAttemptAt),
		LastRefreshSuccessAtUnixMillis: nullTimeUnixMillis(row.LastRefreshSuccessAt),
		LastRefreshError:               row.LastRefreshError,
		CreatedAtUnixMillis:            row.CreatedAt.UnixMilli(),
		UpdatedAtUnixMillis:            row.UpdatedAt.UnixMilli(),
	}
}

func decodePublicTrustedProxyCIDRs(value string) []string {
	var cidrs []string
	if strings.TrimSpace(value) == "" || json.Unmarshal([]byte(value), &cidrs) != nil {
		return []string{}
	}
	if cidrs == nil {
		return []string{}
	}
	return cidrs
}

func publicTrustedProxyProviderFromProto(provider p2pstreamv1.PublicTrustedProxyProvider) (TrustedProxyProvider, error) {
	switch provider {
	case p2pstreamv1.PublicTrustedProxyProvider_PUBLIC_TRUSTED_PROXY_PROVIDER_CUSTOM:
		return TrustedProxyProviderCustom, nil
	case p2pstreamv1.PublicTrustedProxyProvider_PUBLIC_TRUSTED_PROXY_PROVIDER_CLOUDFLARE:
		return TrustedProxyProviderCloudflare, nil
	case p2pstreamv1.PublicTrustedProxyProvider_PUBLIC_TRUSTED_PROXY_PROVIDER_BUNNY:
		return TrustedProxyProviderBunny, nil
	case p2pstreamv1.PublicTrustedProxyProvider_PUBLIC_TRUSTED_PROXY_PROVIDER_CLOUDFRONT:
		return TrustedProxyProviderCloudFront, nil
	default:
		return "", connect.NewError(connect.CodeInvalidArgument, errors.New("trusted proxy provider is invalid"))
	}
}

func protoPublicTrustedProxyProvider(provider TrustedProxyProvider) p2pstreamv1.PublicTrustedProxyProvider {
	switch provider {
	case TrustedProxyProviderCloudflare:
		return p2pstreamv1.PublicTrustedProxyProvider_PUBLIC_TRUSTED_PROXY_PROVIDER_CLOUDFLARE
	case TrustedProxyProviderBunny:
		return p2pstreamv1.PublicTrustedProxyProvider_PUBLIC_TRUSTED_PROXY_PROVIDER_BUNNY
	case TrustedProxyProviderCloudFront:
		return p2pstreamv1.PublicTrustedProxyProvider_PUBLIC_TRUSTED_PROXY_PROVIDER_CLOUDFRONT
	default:
		return p2pstreamv1.PublicTrustedProxyProvider_PUBLIC_TRUSTED_PROXY_PROVIDER_CUSTOM
	}
}

func publicTrustedProxyHeaderModeFromProto(mode p2pstreamv1.PublicTrustedProxyHeaderMode) (TrustedProxyHeaderMode, error) {
	switch mode {
	case p2pstreamv1.PublicTrustedProxyHeaderMode_PUBLIC_TRUSTED_PROXY_HEADER_MODE_UNSPECIFIED,
		p2pstreamv1.PublicTrustedProxyHeaderMode_PUBLIC_TRUSTED_PROXY_HEADER_MODE_SINGLE_IP:
		return TrustedProxyHeaderSingleIP, nil
	case p2pstreamv1.PublicTrustedProxyHeaderMode_PUBLIC_TRUSTED_PROXY_HEADER_MODE_TRUSTED_CHAIN:
		return TrustedProxyHeaderTrustedChain, nil
	default:
		return "", connect.NewError(connect.CodeInvalidArgument, errors.New("trusted proxy header mode is invalid"))
	}
}

func protoPublicTrustedProxyHeaderMode(mode TrustedProxyHeaderMode) p2pstreamv1.PublicTrustedProxyHeaderMode {
	if mode == TrustedProxyHeaderTrustedChain {
		return p2pstreamv1.PublicTrustedProxyHeaderMode_PUBLIC_TRUSTED_PROXY_HEADER_MODE_TRUSTED_CHAIN
	}
	return p2pstreamv1.PublicTrustedProxyHeaderMode_PUBLIC_TRUSTED_PROXY_HEADER_MODE_SINGLE_IP
}

func validatePublicTrustedProxySourceInput(name string, enabled bool, rawCIDRs []string, headerName string, protoMode p2pstreamv1.PublicTrustedProxyHeaderMode) (db.CreatePublicTrustedProxySourceParams, error) {
	name = strings.TrimSpace(name)
	if !publicNamePattern.MatchString(name) {
		return db.CreatePublicTrustedProxySourceParams{}, connect.NewError(connect.CodeInvalidArgument, errors.New("trusted proxy source name must be 1-64 alphanumeric, dot, dash, or underscore characters"))
	}
	headerName = strings.TrimSpace(headerName)
	if len(headerName) > 256 {
		return db.CreatePublicTrustedProxySourceParams{}, connect.NewError(connect.CodeInvalidArgument, errors.New("trusted proxy source header name is too long"))
	}
	if !validHTTPFieldName(headerName) {
		return db.CreatePublicTrustedProxySourceParams{}, connect.NewError(connect.CodeInvalidArgument, errors.New("trusted proxy source header name is invalid"))
	}
	mode, err := publicTrustedProxyHeaderModeFromProto(protoMode)
	if err != nil {
		return db.CreatePublicTrustedProxySourceParams{}, err
	}
	if len(rawCIDRs) > maxPublicTrustedProxyCIDRs {
		return db.CreatePublicTrustedProxySourceParams{}, connect.NewError(connect.CodeInvalidArgument, errors.New("trusted proxy source has too many CIDRs"))
	}
	prefixes := make([]netip.Prefix, 0, len(rawCIDRs))
	for _, rawCIDR := range rawCIDRs {
		if len(rawCIDR) > 64 {
			return db.CreatePublicTrustedProxySourceParams{}, connect.NewError(connect.CodeInvalidArgument, errors.New("trusted proxy CIDR is too long"))
		}
		prefix, err := netip.ParsePrefix(strings.TrimSpace(rawCIDR))
		if err != nil || prefix.Addr().Zone() != "" {
			return db.CreatePublicTrustedProxySourceParams{}, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("trusted proxy CIDR %q is invalid", rawCIDR))
		}
		prefixes = append(prefixes, prefix)
	}
	prefixes, err = normalizeTrustedProxyPrefixes(prefixes)
	if err != nil {
		return db.CreatePublicTrustedProxySourceParams{}, connect.NewError(connect.CodeInvalidArgument, err)
	}
	if enabled && len(prefixes) == 0 {
		return db.CreatePublicTrustedProxySourceParams{}, connect.NewError(connect.CodeInvalidArgument, errors.New("enabled trusted proxy sources require at least one CIDR"))
	}
	for _, prefix := range prefixes {
		if prefix.Bits() == 0 {
			return db.CreatePublicTrustedProxySourceParams{}, connect.NewError(connect.CodeInvalidArgument, errors.New("trusted proxy source CIDRs cannot include a default route"))
		}
	}
	if trustedProxyPrefixesCoverFullAddressFamily(prefixes) {
		return db.CreatePublicTrustedProxySourceParams{}, connect.NewError(connect.CodeInvalidArgument, errors.New("trusted proxy source CIDRs cannot collectively cover an entire address family"))
	}
	cidrs := make([]string, 0, len(prefixes))
	for _, prefix := range prefixes {
		cidrs = append(cidrs, prefix.String())
	}
	cidrsJSON, _ := json.Marshal(cidrs)
	return db.CreatePublicTrustedProxySourceParams{
		Name:       name,
		Enabled:    boolInt(enabled),
		CidrsJson:  string(cidrsJSON),
		HeaderName: http.CanonicalHeaderKey(headerName),
		HeaderMode: string(mode),
	}, nil
}

func trustedProxyPrefixesCoverFullAddressFamily(prefixes []netip.Prefix) bool {
	for _, ipv4 := range []bool{true, false} {
		set := make(map[netip.Prefix]struct{}, len(prefixes))
		queue := make([]netip.Prefix, 0, len(prefixes))
		for _, prefix := range prefixes {
			prefix = prefix.Masked()
			if prefix.Addr().Is4() != ipv4 {
				continue
			}
			if prefix.Bits() == 0 {
				return true
			}
			if _, exists := set[prefix]; exists {
				continue
			}
			set[prefix] = struct{}{}
			queue = append(queue, prefix)
		}
		for len(queue) != 0 {
			prefix := queue[len(queue)-1]
			queue = queue[:len(queue)-1]
			if _, exists := set[prefix]; !exists || prefix.Bits() == 0 {
				continue
			}
			sibling := trustedProxyPrefixSibling(prefix)
			if _, exists := set[sibling]; !exists {
				continue
			}
			delete(set, prefix)
			delete(set, sibling)
			parent := netip.PrefixFrom(prefix.Addr(), prefix.Bits()-1).Masked()
			if parent.Bits() == 0 {
				return true
			}
			if _, exists := set[parent]; !exists {
				set[parent] = struct{}{}
				queue = append(queue, parent)
			}
		}
	}
	return false
}

func trustedProxyPrefixSibling(prefix netip.Prefix) netip.Prefix {
	prefix = prefix.Masked()
	bit := prefix.Bits() - 1
	if prefix.Addr().Is4() {
		address := prefix.Addr().As4()
		address[bit/8] ^= byte(1 << (7 - bit%8))
		return netip.PrefixFrom(netip.AddrFrom4(address), prefix.Bits()).Masked()
	}
	address := prefix.Addr().As16()
	address[bit/8] ^= byte(1 << (7 - bit%8))
	return netip.PrefixFrom(netip.AddrFrom16(address), prefix.Bits()).Masked()
}

func validatePublicGeoIPSettingsMutation(current db.PublicGeoIpSetting, req *p2pstreamv1.UpdatePublicGeoIpSettingsRequest) (db.UpdatePublicGeoIpSettingsParams, bool, error) {
	accountID := strings.TrimSpace(req.MaxmindAccountId)
	if len(accountID) > 64 {
		return db.UpdatePublicGeoIpSettingsParams{}, false, connect.NewError(connect.CodeInvalidArgument, errors.New("MaxMind account ID is too long"))
	}
	for _, char := range accountID {
		if char < '0' || char > '9' {
			return db.UpdatePublicGeoIpSettingsParams{}, false, connect.NewError(connect.CodeInvalidArgument, errors.New("MaxMind account ID must be numeric"))
		}
	}
	licenseKey := current.MaxmindLicenseKey
	providedLicenseKey := req.MaxmindLicenseKey
	if strings.TrimSpace(providedLicenseKey) == "" {
		providedLicenseKey = ""
	}
	if req.ClearLicenseKey && providedLicenseKey != "" {
		return db.UpdatePublicGeoIpSettingsParams{}, false, connect.NewError(connect.CodeInvalidArgument, errors.New("MaxMind license key cannot be replaced and cleared in the same request"))
	}
	if len(providedLicenseKey) > 256 {
		return db.UpdatePublicGeoIpSettingsParams{}, false, connect.NewError(connect.CodeInvalidArgument, errors.New("MaxMind license key is too long"))
	}
	if req.ClearLicenseKey {
		licenseKey = ""
	} else if providedLicenseKey != "" {
		licenseKey = providedLicenseKey
	}
	if len(licenseKey) > 256 {
		return db.UpdatePublicGeoIpSettingsParams{}, false, connect.NewError(connect.CodeInvalidArgument, errors.New("MaxMind license key is too long"))
	}
	for i := 0; i < len(licenseKey); i++ {
		if licenseKey[i] < 0x21 || licenseKey[i] > 0x7e {
			return db.UpdatePublicGeoIpSettingsParams{}, false, connect.NewError(connect.CodeInvalidArgument, errors.New("MaxMind license key must contain only visible ASCII characters"))
		}
	}
	if req.Enabled && (accountID == "" || licenseKey == "") {
		return db.UpdatePublicGeoIpSettingsParams{}, false, connect.NewError(connect.CodeInvalidArgument, errors.New("enabled GeoIP requires a MaxMind account ID and license key"))
	}
	credentialsChanged := accountID != current.MaxmindAccountID || licenseKey != current.MaxmindLicenseKey
	databaseReady := current.DatabaseType != "" && current.DatabaseBuildAt.Valid && current.LastUpdateSuccessAt.Valid
	needsRefresh := req.Enabled && (current.Enabled == 0 || credentialsChanged || !databaseReady)
	return db.UpdatePublicGeoIpSettingsParams{
		Enabled:           boolInt(req.Enabled),
		MaxmindAccountID:  accountID,
		MaxmindLicenseKey: licenseKey,
	}, needsRefresh, nil
}

func (a *App) UpdatePublicGeoIpSettings(ctx context.Context, req *connect.Request[p2pstreamv1.UpdatePublicGeoIpSettingsRequest]) (*connect.Response[p2pstreamv1.UpdatePublicGeoIpSettingsResponse], error) {
	if _, err := a.requireAdmin(ctx, req.Header()); err != nil {
		return nil, err
	}
	a.publicGeoConfigMu.Lock()
	defer a.publicGeoConfigMu.Unlock()
	current, err := a.ensurePublicGeoIPSettings(ctx)
	if err != nil {
		return nil, err
	}
	params, needsRefresh, err := validatePublicGeoIPSettingsMutation(current, req.Msg)
	if err != nil {
		return nil, err
	}
	if current.Enabled != 0 && params.Enabled == 0 {
		if err := a.validatePublicGeoIPCanDisable(ctx); err != nil {
			return nil, err
		}
	}
	row, err := a.DB.UpdatePublicGeoIpSettings(ctx, params)
	if err != nil {
		return nil, publicDBError(err)
	}
	var refreshInfo PublicGeoIPDatabaseInfo
	if needsRefresh {
		refreshInfo, err = a.performPublicGeoIPRefresh(ctx, params.MaxmindAccountID, params.MaxmindLicenseKey)
		if err != nil {
			return nil, err
		}
		now := time.Now().UTC()
		row, err = a.DB.SetPublicGeoIpUpdateSuccess(ctx, db.SetPublicGeoIpUpdateSuccessParams{
			DatabaseType:        refreshInfo.DatabaseType,
			DatabaseBuildAt:     sql.NullTime{Time: refreshInfo.BuildAt, Valid: true},
			LastUpdateAttemptAt: sql.NullTime{Time: now, Valid: true},
			LastUpdateSuccessAt: sql.NullTime{Time: now, Valid: true},
		})
		if err != nil {
			return nil, publicDBError(err)
		}
	}
	if err := a.refreshPublicProxySnapshot(ctx); err != nil {
		return nil, err
	}
	return connect.NewResponse(&p2pstreamv1.UpdatePublicGeoIpSettingsResponse{Settings: a.publicGeoIPSettingsProto(row)}), nil
}

func (a *App) validatePublicGeoIPCanDisable(ctx context.Context) error {
	rules, err := a.DB.ListPublicWafRules(ctx)
	if err != nil {
		return publicDBError(err)
	}
	for _, rule := range rules {
		if rule.Enabled != 0 && normalizePublicWafGeoMode(rule.GeoMode) != publicWafGeoModeDisabled {
			return connect.NewError(connect.CodeFailedPrecondition, errors.New("GeoIP cannot be disabled while enabled geo-restricted WAF rules exist"))
		}
	}
	return nil
}

func (a *App) performPublicGeoIPRefresh(ctx context.Context, accountID, licenseKey string) (PublicGeoIPDatabaseInfo, error) {
	if a.GeoConfigRefresher == nil {
		return PublicGeoIPDatabaseInfo{}, connect.NewError(connect.CodeFailedPrecondition, errors.New("GeoIP refresh service is unavailable"))
	}
	attemptedAt := time.Now().UTC()
	if _, err := a.DB.SetPublicGeoIpUpdateAttempt(ctx, sql.NullTime{Time: attemptedAt, Valid: true}); err != nil {
		return PublicGeoIPDatabaseInfo{}, publicDBError(err)
	}
	info, err := a.GeoConfigRefresher.RefreshGeoIPDatabase(ctx, accountID, licenseKey)
	if err == nil && (strings.TrimSpace(info.DatabaseType) == "" || info.BuildAt.IsZero()) {
		err = errors.New("GeoIP refresh returned incomplete database metadata")
	}
	if err != nil {
		safeErr := redactPublicGeoRefreshError(err, accountID, licenseKey)
		_, dbErr := a.DB.SetPublicGeoIpUpdateError(ctx, db.SetPublicGeoIpUpdateErrorParams{
			LastUpdateAttemptAt: sql.NullTime{Time: attemptedAt, Valid: true},
			LastUpdateError:     boundedPublicRefreshError(safeErr),
		})
		if dbErr == nil {
			_ = a.refreshPublicProxySnapshot(ctx)
		}
		return PublicGeoIPDatabaseInfo{}, connect.NewError(connect.CodeUnavailable, fmt.Errorf("refresh GeoIP database: %w", safeErr))
	}
	info.DatabaseType = strings.TrimSpace(info.DatabaseType)
	info.BuildAt = info.BuildAt.UTC()
	return info, nil
}

func (a *App) RefreshPublicGeoIpDatabase(ctx context.Context, req *connect.Request[p2pstreamv1.RefreshPublicGeoIpDatabaseRequest]) (*connect.Response[p2pstreamv1.RefreshPublicGeoIpDatabaseResponse], error) {
	if _, err := a.requireAdmin(ctx, req.Header()); err != nil {
		return nil, err
	}
	a.publicGeoConfigMu.Lock()
	defer a.publicGeoConfigMu.Unlock()
	current, err := a.ensurePublicGeoIPSettings(ctx)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(current.MaxmindAccountID) == "" || strings.TrimSpace(current.MaxmindLicenseKey) == "" {
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("MaxMind credentials are required before refreshing GeoIP"))
	}
	info, err := a.performPublicGeoIPRefresh(ctx, current.MaxmindAccountID, current.MaxmindLicenseKey)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	row, err := a.DB.SetPublicGeoIpUpdateSuccess(ctx, db.SetPublicGeoIpUpdateSuccessParams{
		DatabaseType:        info.DatabaseType,
		DatabaseBuildAt:     sql.NullTime{Time: info.BuildAt, Valid: true},
		LastUpdateAttemptAt: sql.NullTime{Time: now, Valid: true},
		LastUpdateSuccessAt: sql.NullTime{Time: now, Valid: true},
	})
	if err != nil {
		return nil, publicDBError(err)
	}
	if err := a.refreshPublicProxySnapshot(ctx); err != nil {
		return nil, err
	}
	return connect.NewResponse(&p2pstreamv1.RefreshPublicGeoIpDatabaseResponse{Settings: a.publicGeoIPSettingsProto(row)}), nil
}

func (a *App) CreatePublicTrustedProxySource(ctx context.Context, req *connect.Request[p2pstreamv1.CreatePublicTrustedProxySourceRequest]) (*connect.Response[p2pstreamv1.CreatePublicTrustedProxySourceResponse], error) {
	if _, err := a.requireAdmin(ctx, req.Header()); err != nil {
		return nil, err
	}
	a.publicGeoConfigMu.Lock()
	defer a.publicGeoConfigMu.Unlock()
	params, err := validatePublicTrustedProxySourceInput(req.Msg.Name, req.Msg.Enabled, req.Msg.Cidrs, req.Msg.HeaderName, req.Msg.HeaderMode)
	if err != nil {
		return nil, err
	}
	row, err := a.DB.CreatePublicTrustedProxySource(ctx, params)
	if err != nil {
		return nil, publicDBError(err)
	}
	if err := a.refreshPublicProxySnapshot(ctx); err != nil {
		return nil, err
	}
	return connect.NewResponse(&p2pstreamv1.CreatePublicTrustedProxySourceResponse{Source: publicTrustedProxySourceToProto(row)}), nil
}

func (a *App) UpdatePublicTrustedProxySource(ctx context.Context, req *connect.Request[p2pstreamv1.UpdatePublicTrustedProxySourceRequest]) (*connect.Response[p2pstreamv1.UpdatePublicTrustedProxySourceResponse], error) {
	if _, err := a.requireAdmin(ctx, req.Header()); err != nil {
		return nil, err
	}
	a.publicGeoConfigMu.Lock()
	defer a.publicGeoConfigMu.Unlock()
	current, err := a.DB.GetPublicTrustedProxySource(ctx, req.Msg.Id)
	if err != nil {
		return nil, publicDBError(err)
	}
	if current.BuiltIn != 0 {
		if err := validatePublicBuiltInTrustedProxyMutation(current, req.Msg); err != nil {
			return nil, err
		}
		if req.Msg.Enabled && current.Enabled == 0 {
			current, err = a.refreshPublicTrustedProxySource(ctx, current)
			if err != nil {
				return nil, err
			}
		}
		row, err := a.DB.SetPublicTrustedProxySourceEnabled(ctx, db.SetPublicTrustedProxySourceEnabledParams{Enabled: boolInt(req.Msg.Enabled), ID: current.ID})
		if err != nil {
			return nil, publicDBError(err)
		}
		if err := a.refreshPublicProxySnapshot(ctx); err != nil {
			return nil, err
		}
		return connect.NewResponse(&p2pstreamv1.UpdatePublicTrustedProxySourceResponse{Source: publicTrustedProxySourceToProto(row)}), nil
	}
	params, err := validatePublicTrustedProxySourceInput(req.Msg.Name, req.Msg.Enabled, req.Msg.Cidrs, req.Msg.HeaderName, req.Msg.HeaderMode)
	if err != nil {
		return nil, err
	}
	row, err := a.DB.UpdatePublicTrustedProxySource(ctx, db.UpdatePublicTrustedProxySourceParams{
		Name:       params.Name,
		Enabled:    params.Enabled,
		CidrsJson:  params.CidrsJson,
		HeaderName: params.HeaderName,
		HeaderMode: params.HeaderMode,
		ID:         current.ID,
	})
	if err != nil {
		return nil, publicDBError(err)
	}
	if err := a.refreshPublicProxySnapshot(ctx); err != nil {
		return nil, err
	}
	return connect.NewResponse(&p2pstreamv1.UpdatePublicTrustedProxySourceResponse{Source: publicTrustedProxySourceToProto(row)}), nil
}

func validatePublicBuiltInTrustedProxyMutation(current db.PublicTrustedProxySource, req *p2pstreamv1.UpdatePublicTrustedProxySourceRequest) error {
	if name := strings.TrimSpace(req.Name); name != "" && name != current.Name {
		return connect.NewError(connect.CodeInvalidArgument, errors.New("built-in trusted proxy source name is immutable"))
	}
	if header := strings.TrimSpace(req.HeaderName); header != "" && !strings.EqualFold(header, current.HeaderName) {
		return connect.NewError(connect.CodeInvalidArgument, errors.New("built-in trusted proxy source header is immutable"))
	}
	if req.HeaderMode != p2pstreamv1.PublicTrustedProxyHeaderMode_PUBLIC_TRUSTED_PROXY_HEADER_MODE_UNSPECIFIED &&
		publicTrustedProxyHeaderModeFromProtoMust(req.HeaderMode) != TrustedProxyHeaderMode(current.HeaderMode) {
		return connect.NewError(connect.CodeInvalidArgument, errors.New("built-in trusted proxy source header mode is immutable"))
	}
	if len(req.Cidrs) != 0 {
		return connect.NewError(connect.CodeInvalidArgument, errors.New("built-in trusted proxy source CIDRs are managed automatically"))
	}
	return nil
}

func publicTrustedProxyHeaderModeFromProtoMust(mode p2pstreamv1.PublicTrustedProxyHeaderMode) TrustedProxyHeaderMode {
	parsed, _ := publicTrustedProxyHeaderModeFromProto(mode)
	return parsed
}

func (a *App) DeletePublicTrustedProxySource(ctx context.Context, req *connect.Request[p2pstreamv1.DeletePublicTrustedProxySourceRequest]) (*connect.Response[p2pstreamv1.DeletePublicTrustedProxySourceResponse], error) {
	if _, err := a.requireAdmin(ctx, req.Header()); err != nil {
		return nil, err
	}
	a.publicGeoConfigMu.Lock()
	defer a.publicGeoConfigMu.Unlock()
	current, err := a.DB.GetPublicTrustedProxySource(ctx, req.Msg.Id)
	if err != nil {
		return nil, publicDBError(err)
	}
	if current.BuiltIn != 0 {
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("built-in trusted proxy sources cannot be deleted"))
	}
	if err := a.DB.DeletePublicTrustedProxySource(ctx, current.ID); err != nil {
		return nil, publicDBError(err)
	}
	if err := a.refreshPublicProxySnapshot(ctx); err != nil {
		return nil, err
	}
	return connect.NewResponse(&p2pstreamv1.DeletePublicTrustedProxySourceResponse{}), nil
}

func (a *App) RefreshPublicTrustedProxySource(ctx context.Context, req *connect.Request[p2pstreamv1.RefreshPublicTrustedProxySourceRequest]) (*connect.Response[p2pstreamv1.RefreshPublicTrustedProxySourceResponse], error) {
	if _, err := a.requireAdmin(ctx, req.Header()); err != nil {
		return nil, err
	}
	a.publicGeoConfigMu.Lock()
	defer a.publicGeoConfigMu.Unlock()
	current, err := a.DB.GetPublicTrustedProxySource(ctx, req.Msg.Id)
	if err != nil {
		return nil, publicDBError(err)
	}
	row, err := a.refreshPublicTrustedProxySource(ctx, current)
	if err != nil {
		return nil, err
	}
	if err := a.refreshPublicProxySnapshot(ctx); err != nil {
		return nil, err
	}
	return connect.NewResponse(&p2pstreamv1.RefreshPublicTrustedProxySourceResponse{Source: publicTrustedProxySourceToProto(row)}), nil
}

func (a *App) refreshPublicTrustedProxySource(ctx context.Context, current db.PublicTrustedProxySource) (db.PublicTrustedProxySource, error) {
	if current.BuiltIn == 0 {
		return db.PublicTrustedProxySource{}, connect.NewError(connect.CodeFailedPrecondition, errors.New("custom trusted proxy sources do not support refresh"))
	}
	provider := TrustedProxyProvider(current.Provider)
	switch provider {
	case TrustedProxyProviderCloudflare, TrustedProxyProviderBunny, TrustedProxyProviderCloudFront:
	default:
		return db.PublicTrustedProxySource{}, connect.NewError(connect.CodeFailedPrecondition, errors.New("trusted proxy source provider is invalid"))
	}
	if a.GeoConfigRefresher == nil {
		return db.PublicTrustedProxySource{}, connect.NewError(connect.CodeFailedPrecondition, errors.New("trusted proxy refresh service is unavailable"))
	}
	attemptedAt := time.Now().UTC()
	if _, err := a.DB.SetPublicTrustedProxySourceRefreshAttempt(ctx, db.SetPublicTrustedProxySourceRefreshAttemptParams{
		LastRefreshAttemptAt: sql.NullTime{Time: attemptedAt, Valid: true},
		ID:                   current.ID,
	}); err != nil {
		return db.PublicTrustedProxySource{}, publicDBError(err)
	}
	prefixes, err := a.GeoConfigRefresher.RefreshTrustedProxySource(ctx, provider)
	if err == nil {
		prefixes, err = validateProviderPrefixes(prefixes)
	}
	if err != nil {
		_, dbErr := a.DB.SetPublicTrustedProxySourceRefreshError(ctx, db.SetPublicTrustedProxySourceRefreshErrorParams{
			LastRefreshAttemptAt: sql.NullTime{Time: attemptedAt, Valid: true},
			LastRefreshError:     boundedPublicRefreshError(err),
			ID:                   current.ID,
		})
		if dbErr == nil {
			_ = a.refreshPublicProxySnapshot(ctx)
		}
		return db.PublicTrustedProxySource{}, connect.NewError(connect.CodeUnavailable, fmt.Errorf("refresh trusted proxy source: %w", err))
	}
	cidrs := make([]string, 0, len(prefixes))
	for _, prefix := range prefixes {
		cidrs = append(cidrs, prefix.String())
	}
	cidrsJSON, _ := json.Marshal(cidrs)
	succeededAt := time.Now().UTC()
	row, err := a.DB.SetPublicTrustedProxySourceRefreshSuccess(ctx, db.SetPublicTrustedProxySourceRefreshSuccessParams{
		CidrsJson:            string(cidrsJSON),
		LastRefreshAttemptAt: sql.NullTime{Time: attemptedAt, Valid: true},
		LastRefreshSuccessAt: sql.NullTime{Time: succeededAt, Valid: true},
		ID:                   current.ID,
	})
	if err != nil {
		return db.PublicTrustedProxySource{}, publicDBError(err)
	}
	return row, nil
}

func boundedPublicRefreshError(err error) string {
	if err == nil {
		return ""
	}
	message := strings.ToValidUTF8(strings.TrimSpace(err.Error()), "")
	if len(message) > maxPublicRefreshErrorBytes {
		message = message[:maxPublicRefreshErrorBytes]
		message = strings.ToValidUTF8(message, "")
	}
	return message
}

func redactPublicGeoRefreshError(err error, accountID, licenseKey string) error {
	if err == nil {
		return nil
	}
	message := err.Error()
	basicToken := base64.StdEncoding.EncodeToString([]byte(accountID + ":" + licenseKey))
	values := []string{
		"Basic " + basicToken,
		basicToken,
		url.QueryEscape(licenseKey),
		url.PathEscape(licenseKey),
		licenseKey,
		url.QueryEscape(accountID),
		url.PathEscape(accountID),
		accountID,
	}
	sort.SliceStable(values, func(i, j int) bool { return len(values[i]) > len(values[j]) })
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		if value == "" {
			continue
		}
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		message = strings.ReplaceAll(message, value, "[REDACTED]")
	}
	return errors.New(message)
}
