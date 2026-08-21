package db

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"
)

func TestPublicGeoSchemaDefaultsAndRoundTrips(t *testing.T) {
	database, err := Open(filepath.Join(t.TempDir(), "public-geo.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer func() { _ = database.Close() }()

	for _, table := range []string{"public_geo_ip_settings", "public_trusted_proxy_sources"} {
		if !tableExists(t, database, table) {
			t.Fatalf("expected table %s", table)
		}
	}
	wafColumns := tableColumns(t, database, "public_waf_rules")
	for _, column := range []string{"geo_mode", "geo_country_codes_json", "geo_unknown_behavior"} {
		if !containsString(wafColumns, column) {
			t.Fatalf("public_waf_rules missing %s in %v", column, wafColumns)
		}
	}

	ctx := context.Background()
	settings, err := database.GetPublicGeoIpSettings(ctx)
	if err != nil {
		t.Fatalf("get GeoIP settings: %v", err)
	}
	if settings.Enabled != 0 || settings.MaxmindAccountID != "" || settings.MaxmindLicenseKey != "" || settings.LastUpdateSuccessAt.Valid {
		t.Fatalf("unexpected GeoIP defaults: %+v", settings)
	}
	settings, err = database.UpdatePublicGeoIpSettings(ctx, UpdatePublicGeoIpSettingsParams{
		Enabled:           1,
		MaxmindAccountID:  "12345",
		MaxmindLicenseKey: "license-secret",
	})
	if err != nil {
		t.Fatalf("update GeoIP settings: %v", err)
	}
	if settings.Enabled != 1 || settings.MaxmindAccountID != "12345" || settings.MaxmindLicenseKey != "license-secret" {
		t.Fatalf("unexpected updated GeoIP settings: %+v", settings)
	}
	now := time.Now().UTC().Truncate(time.Second)
	settings, err = database.SetPublicGeoIpUpdateSuccess(ctx, SetPublicGeoIpUpdateSuccessParams{
		DatabaseType:        "GeoLite2-Country",
		DatabaseBuildAt:     sql.NullTime{Time: now.Add(-time.Hour), Valid: true},
		LastUpdateAttemptAt: sql.NullTime{Time: now, Valid: true},
		LastUpdateSuccessAt: sql.NullTime{Time: now, Valid: true},
	})
	if err != nil {
		t.Fatalf("set GeoIP update success: %v", err)
	}
	if settings.DatabaseType != "GeoLite2-Country" || !settings.DatabaseBuildAt.Valid || !settings.LastUpdateSuccessAt.Valid {
		t.Fatalf("unexpected GeoIP status: %+v", settings)
	}

	sources, err := database.ListPublicTrustedProxySources(ctx)
	if err != nil {
		t.Fatalf("list trusted proxy sources: %v", err)
	}
	if len(sources) != 3 {
		t.Fatalf("built-in source count = %d, want 3: %+v", len(sources), sources)
	}
	wantBuiltIns := map[string]struct {
		header string
		mode   string
	}{
		"cloudflare": {header: "CF-Connecting-IP", mode: "single_ip"},
		"bunny":      {header: "X-Real-IP", mode: "single_ip"},
		"cloudfront": {header: "X-Forwarded-For", mode: "trusted_chain"},
	}
	for _, source := range sources {
		want, ok := wantBuiltIns[source.Provider]
		if !ok || source.BuiltIn != 1 || source.Enabled != 0 || source.CidrsJson != "[]" || source.HeaderName != want.header || source.HeaderMode != want.mode {
			t.Fatalf("unexpected built-in source: %+v", source)
		}
	}
	custom, err := database.CreatePublicTrustedProxySource(ctx, CreatePublicTrustedProxySourceParams{
		Name:       "edge-proxy",
		Enabled:    1,
		CidrsJson:  `["192.0.2.0/24"]`,
		HeaderName: "X-Client-IP",
		HeaderMode: "single_ip",
	})
	if err != nil {
		t.Fatalf("create custom trusted proxy source: %v", err)
	}
	if custom.Provider != "custom" || custom.BuiltIn != 0 || custom.Enabled != 1 {
		t.Fatalf("unexpected custom source: %+v", custom)
	}

	if _, err := database.ExecContext(ctx, `
		INSERT INTO public_waf_rules (name, geo_mode, geo_country_codes_json, geo_unknown_behavior)
		VALUES ('geo-test', 'selected_countries', '["CH","XK"]', 'bypass_rule')
	`); err != nil {
		t.Fatalf("insert geo WAF rule: %v", err)
	}
	var ruleID int64
	if err := database.QueryRowContext(ctx, `SELECT id FROM public_waf_rules WHERE name = 'geo-test'`).Scan(&ruleID); err != nil {
		t.Fatalf("get geo WAF rule id: %v", err)
	}
	rule, err := database.GetPublicWafRule(ctx, ruleID)
	if err != nil {
		t.Fatalf("get geo WAF rule: %v", err)
	}
	if rule.GeoMode != "selected_countries" || rule.GeoCountryCodesJson != `["CH","XK"]` || rule.GeoUnknownBehavior != "bypass_rule" {
		t.Fatalf("unexpected stored WAF geo restriction: %+v", rule)
	}
}

func TestGeoMigrationUpgradesExistingWafRules(t *testing.T) {
	raw, err := sql.Open("sqlite3", filepath.Join(t.TempDir(), "legacy-geo.db"))
	if err != nil {
		t.Fatalf("open raw db: %v", err)
	}
	defer func() { _ = raw.Close() }()
	if _, err := raw.Exec(`
		CREATE TABLE public_waf_rules (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			name TEXT NOT NULL UNIQUE
		);
		INSERT INTO public_waf_rules (name) VALUES ('legacy-waf');
	`); err != nil {
		t.Fatalf("create legacy WAF schema: %v", err)
	}
	if err := runEmbeddedMigrations(raw); err != nil {
		t.Fatalf("run embedded migrations: %v", err)
	}
	var mode, countryCodes, unknownBehavior string
	if err := raw.QueryRow(`SELECT geo_mode, geo_country_codes_json, geo_unknown_behavior FROM public_waf_rules WHERE name = 'legacy-waf'`).Scan(&mode, &countryCodes, &unknownBehavior); err != nil {
		t.Fatalf("read migrated WAF row: %v", err)
	}
	if mode != "disabled" || countryCodes != "[]" || unknownBehavior != "apply_rule" {
		t.Fatalf("migrated geo defaults = %q/%q/%q", mode, countryCodes, unknownBehavior)
	}
	for _, table := range []string{"public_geo_ip_settings", "public_trusted_proxy_sources"} {
		if !tableExists(t, &DB{DB: raw}, table) {
			t.Fatalf("migration did not create %s", table)
		}
	}
}
