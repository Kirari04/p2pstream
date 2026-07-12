package server

import (
	"context"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"net/netip"
	"reflect"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"

	p2pstreamv1 "p2pstream/gen/proto/p2pstream/v1"
	"p2pstream/internal/db"
)

type testPublicGeoConfigRefresher struct {
	geoInfo         PublicGeoIPDatabaseInfo
	geoErr          error
	providerCIDRs   map[TrustedProxyProvider][]netip.Prefix
	providerErr     error
	geoRefreshCalls int
	geoRefreshCheck func(context.Context, string, string) error
}

type testInfoOnlyGeoRefresher struct {
	info GeoIPCountryDatabaseInfo
}

func (r *testInfoOnlyGeoRefresher) RefreshGeoIPDatabase(context.Context, string, string) (PublicGeoIPDatabaseInfo, error) {
	return PublicGeoIPDatabaseInfo{DatabaseType: r.info.DatabaseType, BuildAt: r.info.BuildTime}, nil
}

func (r *testInfoOnlyGeoRefresher) RefreshTrustedProxySource(context.Context, TrustedProxyProvider) ([]netip.Prefix, error) {
	return nil, errors.New("not implemented")
}

func (r *testInfoOnlyGeoRefresher) GeoIPInfo() (GeoIPCountryDatabaseInfo, bool) {
	return r.info, true
}

func (r *testPublicGeoConfigRefresher) RefreshGeoIPDatabase(ctx context.Context, accountID, licenseKey string) (PublicGeoIPDatabaseInfo, error) {
	r.geoRefreshCalls++
	if r.geoRefreshCheck != nil {
		if err := r.geoRefreshCheck(ctx, accountID, licenseKey); err != nil {
			return PublicGeoIPDatabaseInfo{}, err
		}
	}
	return r.geoInfo, r.geoErr
}

func (r *testPublicGeoConfigRefresher) RefreshTrustedProxySource(_ context.Context, provider TrustedProxyProvider) ([]netip.Prefix, error) {
	if r.providerErr != nil {
		return nil, r.providerErr
	}
	return append([]netip.Prefix(nil), r.providerCIDRs[provider]...), nil
}

func (r *testPublicGeoConfigRefresher) GeoIPInfo() (GeoIPCountryDatabaseInfo, bool) {
	if r == nil || r.geoErr != nil || r.geoInfo.DatabaseType == "" || r.geoInfo.BuildAt.IsZero() {
		return GeoIPCountryDatabaseInfo{}, false
	}
	return GeoIPCountryDatabaseInfo{DatabaseType: r.geoInfo.DatabaseType, BuildTime: r.geoInfo.BuildAt}, true
}

func (r *testPublicGeoConfigRefresher) LookupCountry(netip.Addr) (string, bool, error) {
	if _, ready := r.GeoIPInfo(); !ready {
		return "", false, ErrGeoIPCountryDatabaseUnavailable
	}
	return "CH", true, nil
}

func TestPublicGeoIPSettingsSecretMutationAndRedaction(t *testing.T) {
	ctx := context.Background()
	app := NewApp(nil, newServerTestDB(t))
	adminHeader := createTestAdminSession(t, app)

	update := func(message *p2pstreamv1.UpdatePublicGeoIpSettingsRequest) *p2pstreamv1.PublicGeoIpSettings {
		t.Helper()
		req := connect.NewRequest(message)
		req.Header().Set("Cookie", adminHeader.Get("Cookie"))
		resp, err := app.UpdatePublicGeoIpSettings(ctx, req)
		if err != nil {
			t.Fatalf("update GeoIP settings: %v", err)
		}
		return resp.Msg.Settings
	}

	settings := update(&p2pstreamv1.UpdatePublicGeoIpSettingsRequest{
		MaxmindAccountId:  "12345",
		MaxmindLicenseKey: "first-secret",
	})
	if !settings.MaxmindLicenseKeySet || settings.MaxmindAccountId != "12345" {
		t.Fatalf("unexpected redacted settings: %+v", settings)
	}
	stored, err := app.DB.GetPublicGeoIpSettings(ctx)
	if err != nil || stored.MaxmindLicenseKey != "first-secret" {
		t.Fatalf("stored initial secret = %q, err=%v", stored.MaxmindLicenseKey, err)
	}

	update(&p2pstreamv1.UpdatePublicGeoIpSettingsRequest{MaxmindAccountId: "12345"})
	stored, _ = app.DB.GetPublicGeoIpSettings(ctx)
	if stored.MaxmindLicenseKey != "first-secret" {
		t.Fatalf("blank update did not retain secret: %q", stored.MaxmindLicenseKey)
	}

	settings = update(&p2pstreamv1.UpdatePublicGeoIpSettingsRequest{MaxmindAccountId: "12345", ClearLicenseKey: true})
	stored, _ = app.DB.GetPublicGeoIpSettings(ctx)
	if settings.MaxmindLicenseKeySet || stored.MaxmindLicenseKey != "" {
		t.Fatalf("explicit clear did not remove secret: proto=%+v stored=%q", settings, stored.MaxmindLicenseKey)
	}

	refresher := &testPublicGeoConfigRefresher{geoInfo: PublicGeoIPDatabaseInfo{
		DatabaseType: "GeoLite2-Country",
		BuildAt:      time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC),
	}}
	app.GeoConfigRefresher = refresher
	settings = update(&p2pstreamv1.UpdatePublicGeoIpSettingsRequest{
		Enabled:           true,
		MaxmindAccountId:  "12345",
		MaxmindLicenseKey: "replacement-secret",
	})
	if refresher.geoRefreshCalls != 1 || settings.DatabaseStatus == nil || !settings.DatabaseStatus.Ready {
		t.Fatalf("enabled settings did not refresh a ready database: calls=%d settings=%+v", refresher.geoRefreshCalls, settings)
	}
	stored, _ = app.DB.GetPublicGeoIpSettings(ctx)
	if stored.MaxmindLicenseKey != "replacement-secret" {
		t.Fatalf("replacement secret = %q", stored.MaxmindLicenseKey)
	}
}

func TestPublicGeoIPSettingsPersistBeforeRefresh(t *testing.T) {
	ctx := context.Background()
	app := NewApp(nil, newServerTestDB(t))
	adminHeader := createTestAdminSession(t, app)
	const accountID = "123456789"
	const licenseKey = "persisted-before-refresh"
	app.GeoConfigRefresher = &testPublicGeoConfigRefresher{
		geoInfo: PublicGeoIPDatabaseInfo{
			DatabaseType: "GeoLite2-Country",
			BuildAt:      time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC),
		},
		geoRefreshCheck: func(refreshCtx context.Context, gotAccountID, gotLicenseKey string) error {
			stored, err := app.DB.GetPublicGeoIpSettings(refreshCtx)
			if err != nil {
				return err
			}
			if stored.Enabled != 1 || stored.MaxmindAccountID != accountID || stored.MaxmindLicenseKey != licenseKey {
				return fmt.Errorf("settings at refresh = enabled:%d account:%q key:%q", stored.Enabled, stored.MaxmindAccountID, stored.MaxmindLicenseKey)
			}
			if gotAccountID != accountID || gotLicenseKey != licenseKey {
				return fmt.Errorf("refresh credentials = account:%q key:%q", gotAccountID, gotLicenseKey)
			}
			return nil
		},
	}
	req := connect.NewRequest(&p2pstreamv1.UpdatePublicGeoIpSettingsRequest{
		Enabled:           true,
		MaxmindAccountId:  accountID,
		MaxmindLicenseKey: licenseKey,
	})
	req.Header().Set("Cookie", adminHeader.Get("Cookie"))
	if _, err := app.UpdatePublicGeoIpSettings(ctx, req); err != nil {
		t.Fatalf("enable GeoIP: %v", err)
	}
}

func TestPublicGeoIPSettingsRefreshFailureKeepsLastGoodMetadata(t *testing.T) {
	ctx := context.Background()
	app := NewApp(nil, newServerTestDB(t))
	adminHeader := createTestAdminSession(t, app)
	accountID := "987654321"
	licenseKey := "super-secret-KEY_987"
	good := &testPublicGeoConfigRefresher{geoInfo: PublicGeoIPDatabaseInfo{DatabaseType: "GeoLite2-Country", BuildAt: time.Now().Add(-time.Hour)}}
	app.GeoConfigRefresher = good
	req := connect.NewRequest(&p2pstreamv1.UpdatePublicGeoIpSettingsRequest{Enabled: true, MaxmindAccountId: accountID, MaxmindLicenseKey: licenseKey})
	req.Header().Set("Cookie", adminHeader.Get("Cookie"))
	if _, err := app.UpdatePublicGeoIpSettings(ctx, req); err != nil {
		t.Fatalf("enable GeoIP: %v", err)
	}
	before, _ := app.DB.GetPublicGeoIpSettings(ctx)

	basicToken := base64.StdEncoding.EncodeToString([]byte(accountID + ":" + licenseKey))
	app.GeoConfigRefresher = &testPublicGeoConfigRefresher{geoErr: fmt.Errorf("offline account=%s key=%s Authorization=Basic %s", accountID, licenseKey, basicToken)}
	refreshReq := connect.NewRequest(&p2pstreamv1.RefreshPublicGeoIpDatabaseRequest{})
	refreshReq.Header().Set("Cookie", adminHeader.Get("Cookie"))
	_, refreshErr := app.RefreshPublicGeoIpDatabase(ctx, refreshReq)
	if connect.CodeOf(refreshErr) != connect.CodeUnavailable {
		t.Fatalf("refresh failure code = %s, want unavailable: %v", connect.CodeOf(refreshErr), refreshErr)
	}
	for _, secret := range []string{accountID, licenseKey, basicToken} {
		if strings.Contains(refreshErr.Error(), secret) {
			t.Fatalf("refresh error leaked credential %q: %v", secret, refreshErr)
		}
	}
	after, _ := app.DB.GetPublicGeoIpSettings(ctx)
	if after.DatabaseType != before.DatabaseType || !after.DatabaseBuildAt.Time.Equal(before.DatabaseBuildAt.Time) || !after.LastUpdateSuccessAt.Time.Equal(before.LastUpdateSuccessAt.Time) {
		t.Fatalf("failed refresh replaced last-good metadata: before=%+v after=%+v", before, after)
	}
	if after.LastUpdateError == "" {
		t.Fatal("failed refresh did not persist an error")
	}
	config, err := app.publicProxyConfigResponse(ctx)
	if err != nil {
		t.Fatalf("get public config after refresh error: %v", err)
	}
	for _, secret := range []string{accountID, licenseKey, basicToken} {
		if strings.Contains(after.LastUpdateError, secret) || strings.Contains(config.GeoIpSettings.DatabaseStatus.LastUpdateError, secret) {
			t.Fatalf("persisted/config refresh status leaked credential %q: db=%q proto=%q", secret, after.LastUpdateError, config.GeoIpSettings.DatabaseStatus.LastUpdateError)
		}
	}
}

func TestPublicTrustedProxyManagementAndManagedCIDRRedaction(t *testing.T) {
	ctx := context.Background()
	app := NewApp(nil, newServerTestDB(t))
	adminHeader := createTestAdminSession(t, app)
	app.GeoConfigRefresher = &testPublicGeoConfigRefresher{providerCIDRs: map[TrustedProxyProvider][]netip.Prefix{
		TrustedProxyProviderCloudflare: {netip.MustParsePrefix("104.16.0.0/13")},
	}}

	unauthorized := connect.NewRequest(&p2pstreamv1.CreatePublicTrustedProxySourceRequest{
		Name: "unauthorized", Cidrs: []string{"192.0.2.0/24"}, HeaderName: "X-Client-IP",
	})
	if _, err := app.CreatePublicTrustedProxySource(ctx, unauthorized); connect.CodeOf(err) != connect.CodeUnauthenticated {
		t.Fatalf("unauthorized create code = %s, want unauthenticated", connect.CodeOf(err))
	}

	createReq := connect.NewRequest(&p2pstreamv1.CreatePublicTrustedProxySourceRequest{
		Name:       "custom-edge",
		Enabled:    true,
		Cidrs:      []string{"192.0.2.99/24", "192.0.2.0/24"},
		HeaderName: "x-client-ip",
		HeaderMode: p2pstreamv1.PublicTrustedProxyHeaderMode_PUBLIC_TRUSTED_PROXY_HEADER_MODE_SINGLE_IP,
	})
	createReq.Header().Set("Cookie", adminHeader.Get("Cookie"))
	created, err := app.CreatePublicTrustedProxySource(ctx, createReq)
	if err != nil {
		t.Fatalf("create custom source: %v", err)
	}
	if created.Msg.Source.BuiltIn || created.Msg.Source.Provider != p2pstreamv1.PublicTrustedProxyProvider_PUBLIC_TRUSTED_PROXY_PROVIDER_CUSTOM ||
		!reflect.DeepEqual(created.Msg.Source.Cidrs, []string{"192.0.2.0/24"}) || created.Msg.Source.HeaderName != "X-Client-Ip" {
		t.Fatalf("unexpected custom source: %+v", created.Msg.Source)
	}

	rows, err := app.DB.ListPublicTrustedProxySources(ctx)
	if err != nil {
		t.Fatalf("list sources: %v", err)
	}
	var cloudflareID int64
	for _, row := range rows {
		if row.Provider == string(TrustedProxyProviderCloudflare) {
			cloudflareID = row.ID
		}
	}
	if cloudflareID == 0 {
		t.Fatal("Cloudflare built-in source was not seeded")
	}
	refreshReq := connect.NewRequest(&p2pstreamv1.RefreshPublicTrustedProxySourceRequest{Id: cloudflareID})
	refreshReq.Header().Set("Cookie", adminHeader.Get("Cookie"))
	app.GeoConfigRefresher = &testPublicGeoConfigRefresher{providerCIDRs: map[TrustedProxyProvider][]netip.Prefix{
		TrustedProxyProviderCloudflare: {netip.MustParsePrefix("0.0.0.0/0")},
	}}
	if _, err := app.RefreshPublicTrustedProxySource(ctx, refreshReq); connect.CodeOf(err) != connect.CodeUnavailable {
		t.Fatalf("unsafe provider refresh code = %s, want unavailable: %v", connect.CodeOf(err), err)
	}
	stored, err := app.DB.GetPublicTrustedProxySource(ctx, cloudflareID)
	if err != nil || stored.CidrsJson != `[]` {
		t.Fatalf("unsafe provider refresh poisoned stored CIDRs: row=%+v err=%v", stored, err)
	}
	app.GeoConfigRefresher = &testPublicGeoConfigRefresher{providerCIDRs: map[TrustedProxyProvider][]netip.Prefix{
		TrustedProxyProviderCloudflare: {netip.MustParsePrefix("104.16.0.0/13")},
	}}
	refreshed, err := app.RefreshPublicTrustedProxySource(ctx, refreshReq)
	if err != nil {
		t.Fatalf("refresh Cloudflare source: %v", err)
	}
	if refreshed.Msg.Source.CidrCount != 1 || len(refreshed.Msg.Source.Cidrs) != 0 || refreshed.Msg.Source.LastRefreshSuccessAtUnixMillis == 0 {
		t.Fatalf("managed CIDRs were not counted/redacted: %+v", refreshed.Msg.Source)
	}
	stored, err = app.DB.GetPublicTrustedProxySource(ctx, cloudflareID)
	if err != nil || stored.CidrsJson != `["104.16.0.0/13"]` {
		t.Fatalf("managed CIDRs were not stored: row=%+v err=%v", stored, err)
	}
	staleAt := time.Now().UTC().Add(-30 * 24 * time.Hour)
	if _, err := app.DB.SetPublicTrustedProxySourceRefreshSuccess(ctx, db.SetPublicTrustedProxySourceRefreshSuccessParams{
		CidrsJson:            `["104.16.0.0/13"]`,
		LastRefreshAttemptAt: sql.NullTime{Time: staleAt, Valid: true},
		LastRefreshSuccessAt: sql.NullTime{Time: staleAt, Valid: true},
		ID:                   cloudflareID,
	}); err != nil {
		t.Fatalf("stage stale built-in ranges: %v", err)
	}
	app.GeoConfigRefresher = &testPublicGeoConfigRefresher{providerCIDRs: map[TrustedProxyProvider][]netip.Prefix{
		TrustedProxyProviderCloudflare: {netip.MustParsePrefix("172.64.0.0/13")},
	}}
	enableReq := connect.NewRequest(&p2pstreamv1.UpdatePublicTrustedProxySourceRequest{Id: cloudflareID, Enabled: true})
	enableReq.Header().Set("Cookie", adminHeader.Get("Cookie"))
	if _, err := app.UpdatePublicTrustedProxySource(ctx, enableReq); err != nil {
		t.Fatalf("enable stale built-in source: %v", err)
	}
	stored, _ = app.DB.GetPublicTrustedProxySource(ctx, cloudflareID)
	if stored.Enabled != 1 || stored.CidrsJson != `["172.64.0.0/13"]` || !stored.LastRefreshSuccessAt.Valid || !stored.LastRefreshSuccessAt.Time.After(staleAt) {
		t.Fatalf("enabling built-in source did not refresh stale ranges: %+v", stored)
	}

	deleteReq := connect.NewRequest(&p2pstreamv1.DeletePublicTrustedProxySourceRequest{Id: cloudflareID})
	deleteReq.Header().Set("Cookie", adminHeader.Get("Cookie"))
	if _, err := app.DeletePublicTrustedProxySource(ctx, deleteReq); connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Fatalf("delete built-in code = %s, want failed_precondition: %v", connect.CodeOf(err), err)
	}
}

func TestPublicTrustedProxyValidationRejectsDefaultRoutes(t *testing.T) {
	for name, cidrs := range map[string][]string{
		"IPv4 default":       {"0.0.0.0/0"},
		"IPv6 default":       {"::/0"},
		"split IPv4 default": {"0.0.0.0/1", "128.0.0.0/1"},
		"split IPv6 default": {"::/1", "8000::/1"},
	} {
		_, err := validatePublicTrustedProxySourceInput("unsafe", true, cidrs, "X-Client-IP", p2pstreamv1.PublicTrustedProxyHeaderMode_PUBLIC_TRUSTED_PROXY_HEADER_MODE_SINGLE_IP)
		if connect.CodeOf(err) != connect.CodeInvalidArgument {
			t.Fatalf("%s validation code = %s, want invalid_argument: %v", name, connect.CodeOf(err), err)
		}
	}
	if _, err := validatePublicTrustedProxySourceInput("too-long", false, nil, strings.Repeat("A", 257), p2pstreamv1.PublicTrustedProxyHeaderMode_PUBLIC_TRUSTED_PROXY_HEADER_MODE_SINGLE_IP); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("long header validation code = %s, want invalid_argument: %v", connect.CodeOf(err), err)
	}
	if _, err := validatePublicTrustedProxySourceInput("long-cidr", false, []string{strings.Repeat("1", 65)}, "X-Client-IP", p2pstreamv1.PublicTrustedProxyHeaderMode_PUBLIC_TRUSTED_PROXY_HEADER_MODE_SINGLE_IP); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("long CIDR validation code = %s, want invalid_argument: %v", connect.CodeOf(err), err)
	}
}

func TestPublicGeoIPCredentialValidationBoundsInputs(t *testing.T) {
	current := db.PublicGeoIpSetting{}
	for name, req := range map[string]*p2pstreamv1.UpdatePublicGeoIpSettingsRequest{
		"non-numeric account": {MaxmindAccountId: "account"},
		"long account":        {MaxmindAccountId: strings.Repeat("1", 65)},
		"long license":        {MaxmindLicenseKey: strings.Repeat("a", 257)},
		"control in license":  {MaxmindLicenseKey: "abc\ndef"},
		"space in license":    {MaxmindLicenseKey: " abc"},
	} {
		if _, _, err := validatePublicGeoIPSettingsMutation(current, req); connect.CodeOf(err) != connect.CodeInvalidArgument {
			t.Fatalf("%s validation code = %s, want invalid_argument: %v", name, connect.CodeOf(err), err)
		}
	}
}

func TestPublicWafGeoRestrictionValidationNormalizesCountryCodes(t *testing.T) {
	config, err := validatePublicWafGeoRestriction(&p2pstreamv1.PublicWafGeoRestriction{
		Mode:            p2pstreamv1.PublicWafGeoRestrictionMode_PUBLIC_WAF_GEO_RESTRICTION_MODE_SELECTED_COUNTRIES,
		CountryCodes:    []string{" xk ", "CH", "ch"},
		UnknownBehavior: p2pstreamv1.PublicWafGeoUnknownBehavior_PUBLIC_WAF_GEO_UNKNOWN_BEHAVIOR_BYPASS_RULE,
	})
	if err != nil {
		t.Fatalf("validate WAF geo restriction: %v", err)
	}
	if config.Mode != publicWafGeoModeSelected || config.UnknownBehavior != publicWafGeoUnknownBypassRule || !reflect.DeepEqual(config.CountryCodes, []string{"CH", "XK"}) {
		t.Fatalf("unexpected normalized geo restriction: %+v", config)
	}
	if _, err := validatePublicWafGeoRestriction(&p2pstreamv1.PublicWafGeoRestriction{
		Mode:         p2pstreamv1.PublicWafGeoRestrictionMode_PUBLIC_WAF_GEO_RESTRICTION_MODE_SELECTED_COUNTRIES,
		CountryCodes: []string{"CHE"},
	}); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("invalid country validation code = %s, want invalid_argument: %v", connect.CodeOf(err), err)
	}
	if _, err := validatePublicWafGeoRestriction(&p2pstreamv1.PublicWafGeoRestriction{
		Mode:         p2pstreamv1.PublicWafGeoRestrictionMode_PUBLIC_WAF_GEO_RESTRICTION_MODE_SELECTED_COUNTRIES,
		CountryCodes: []string{"XX"},
	}); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("unknown sentinel validation code = %s, want invalid_argument: %v", connect.CodeOf(err), err)
	}
}

func TestPublicWafGeoRestrictionAPIRoundTripAndReadiness(t *testing.T) {
	ctx := context.Background()
	app := NewApp(nil, newServerTestDB(t))
	adminHeader := createTestAdminSession(t, app)
	createRequest := func(name string) *connect.Request[p2pstreamv1.CreatePublicWafRuleRequest] {
		req := connect.NewRequest(&p2pstreamv1.CreatePublicWafRuleRequest{
			Name:     name,
			Priority: 10,
			Enabled:  true,
			Action:   p2pstreamv1.PublicWafRuleAction_PUBLIC_WAF_RULE_ACTION_BLOCK,
			GeoRestriction: &p2pstreamv1.PublicWafGeoRestriction{
				Mode:            p2pstreamv1.PublicWafGeoRestrictionMode_PUBLIC_WAF_GEO_RESTRICTION_MODE_SELECTED_COUNTRIES,
				CountryCodes:    []string{"xk", "CH"},
				UnknownBehavior: p2pstreamv1.PublicWafGeoUnknownBehavior_PUBLIC_WAF_GEO_UNKNOWN_BEHAVIOR_BYPASS_RULE,
			},
		})
		req.Header().Set("Cookie", adminHeader.Get("Cookie"))
		return req
	}

	if _, err := app.CreatePublicWafRule(ctx, createRequest("geo-not-ready")); connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Fatalf("not-ready geo rule code = %s, want failed_precondition: %v", connect.CodeOf(err), err)
	}
	if _, err := app.DB.UpdatePublicGeoIpSettings(ctx, db.UpdatePublicGeoIpSettingsParams{
		Enabled: 1, MaxmindAccountID: "123", MaxmindLicenseKey: "secret",
	}); err != nil {
		t.Fatalf("enable GeoIP row: %v", err)
	}
	now := time.Now().UTC()
	if _, err := app.DB.SetPublicGeoIpUpdateSuccess(ctx, db.SetPublicGeoIpUpdateSuccessParams{
		DatabaseType:        "GeoLite2-Country",
		DatabaseBuildAt:     sql.NullTime{Time: now.Add(-time.Hour), Valid: true},
		LastUpdateAttemptAt: sql.NullTime{Time: now, Valid: true},
		LastUpdateSuccessAt: sql.NullTime{Time: now, Valid: true},
	}); err != nil {
		t.Fatalf("mark GeoIP ready: %v", err)
	}
	app.GeoConfigRefresher = nil
	settingsRow, err := app.DB.GetPublicGeoIpSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if status := app.publicGeoIPSettingsProto(settingsRow).DatabaseStatus; status == nil || status.Ready {
		t.Fatalf("missing runtime status = %+v, want not ready", status)
	}
	if _, err := app.CreatePublicWafRule(ctx, createRequest("geo-runtime-missing")); connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Fatalf("missing runtime geo rule code = %s, want failed_precondition: %v", connect.CodeOf(err), err)
	}
	app.GeoConfigRefresher = &testPublicGeoConfigRefresher{geoErr: errors.New("corrupt database")}
	if _, err := app.CreatePublicWafRule(ctx, createRequest("geo-runtime-corrupt")); connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Fatalf("corrupt runtime geo rule code = %s, want failed_precondition: %v", connect.CodeOf(err), err)
	}
	app.GeoConfigRefresher = &testInfoOnlyGeoRefresher{info: GeoIPCountryDatabaseInfo{
		DatabaseType: "GeoLite2-Country",
		BuildTime:    now.Add(-time.Hour),
	}}
	if _, err := app.CreatePublicWafRule(ctx, createRequest("geo-runtime-no-lookup")); connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Fatalf("no-lookup runtime geo rule code = %s, want failed_precondition: %v", connect.CodeOf(err), err)
	}
	app.GeoConfigRefresher = &testPublicGeoConfigRefresher{geoInfo: PublicGeoIPDatabaseInfo{
		DatabaseType: "GeoLite2-Country",
		BuildAt:      now.Add(-2 * time.Hour),
	}}
	if _, err := app.CreatePublicWafRule(ctx, createRequest("geo-runtime-mismatch")); connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Fatalf("mismatched runtime geo rule code = %s, want failed_precondition: %v", connect.CodeOf(err), err)
	}
	app.GeoConfigRefresher = &testPublicGeoConfigRefresher{geoInfo: PublicGeoIPDatabaseInfo{
		DatabaseType: "GeoLite2-Country",
		BuildAt:      now.Add(-time.Hour),
	}}

	created, err := app.CreatePublicWafRule(ctx, createRequest("geo-ready"))
	if err != nil {
		t.Fatalf("create geo WAF rule: %v", err)
	}
	geo := created.Msg.Rule.GeoRestriction
	if geo == nil || geo.Mode != p2pstreamv1.PublicWafGeoRestrictionMode_PUBLIC_WAF_GEO_RESTRICTION_MODE_SELECTED_COUNTRIES ||
		geo.UnknownBehavior != p2pstreamv1.PublicWafGeoUnknownBehavior_PUBLIC_WAF_GEO_UNKNOWN_BEHAVIOR_BYPASS_RULE ||
		!reflect.DeepEqual(geo.CountryCodes, []string{"CH", "XK"}) {
		t.Fatalf("unexpected WAF geo response: %+v", geo)
	}
	stored, err := app.DB.GetPublicWafRule(ctx, created.Msg.Rule.Id)
	if err != nil {
		t.Fatalf("get stored geo WAF rule: %v", err)
	}
	if stored.GeoMode != publicWafGeoModeSelected || stored.GeoCountryCodesJson != `["CH","XK"]` || stored.GeoUnknownBehavior != publicWafGeoUnknownBypassRule {
		t.Fatalf("unexpected stored geo WAF rule: %+v", stored)
	}
	oldClientUpdate := func(geoRestriction *p2pstreamv1.PublicWafGeoRestriction) (*p2pstreamv1.PublicWafRule, error) {
		req := connect.NewRequest(&p2pstreamv1.UpdatePublicWafRuleRequest{
			Id:             created.Msg.Rule.Id,
			Name:           created.Msg.Rule.Name,
			Priority:       20,
			Enabled:        true,
			Action:         p2pstreamv1.PublicWafRuleAction_PUBLIC_WAF_RULE_ACTION_BLOCK,
			GeoRestriction: geoRestriction,
		})
		req.Header().Set("Cookie", adminHeader.Get("Cookie"))
		resp, err := app.UpdatePublicWafRule(ctx, req)
		if err != nil {
			return nil, err
		}
		return resp.Msg.Rule, nil
	}
	app.GeoConfigRefresher = &testPublicGeoConfigRefresher{geoErr: errors.New("runtime unavailable")}
	if _, err := oldClientUpdate(nil); connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Fatalf("old-client update with unavailable runtime code = %s, want failed_precondition: %v", connect.CodeOf(err), err)
	}
	app.GeoConfigRefresher = &testPublicGeoConfigRefresher{geoInfo: PublicGeoIPDatabaseInfo{
		DatabaseType: "GeoLite2-Country",
		BuildAt:      now.Add(-time.Hour),
	}}
	preserved, err := oldClientUpdate(nil)
	if err != nil {
		t.Fatalf("old-client update: %v", err)
	}
	if preserved.GeoRestriction == nil || preserved.GeoRestriction.Mode != p2pstreamv1.PublicWafGeoRestrictionMode_PUBLIC_WAF_GEO_RESTRICTION_MODE_SELECTED_COUNTRIES ||
		!reflect.DeepEqual(preserved.GeoRestriction.CountryCodes, []string{"CH", "XK"}) {
		t.Fatalf("old-client update stripped geo restriction: %+v", preserved.GeoRestriction)
	}

	disableGeo := connect.NewRequest(&p2pstreamv1.UpdatePublicGeoIpSettingsRequest{Enabled: false, MaxmindAccountId: "123"})
	disableGeo.Header().Set("Cookie", adminHeader.Get("Cookie"))
	if _, err := app.UpdatePublicGeoIpSettings(ctx, disableGeo); connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Fatalf("disable GeoIP with enabled geo rule code = %s, want failed_precondition: %v", connect.CodeOf(err), err)
	}
	cleared, err := oldClientUpdate(&p2pstreamv1.PublicWafGeoRestriction{
		Mode: p2pstreamv1.PublicWafGeoRestrictionMode_PUBLIC_WAF_GEO_RESTRICTION_MODE_DISABLED,
	})
	if err != nil {
		t.Fatalf("explicitly clear WAF geo restriction: %v", err)
	}
	if cleared.GeoRestriction == nil || cleared.GeoRestriction.Mode != p2pstreamv1.PublicWafGeoRestrictionMode_PUBLIC_WAF_GEO_RESTRICTION_MODE_DISABLED {
		t.Fatalf("explicit disabled mode did not clear geo restriction: %+v", cleared.GeoRestriction)
	}
	if _, err := app.UpdatePublicGeoIpSettings(ctx, disableGeo); err != nil {
		t.Fatalf("disable GeoIP after clearing geo rule: %v", err)
	}
}
