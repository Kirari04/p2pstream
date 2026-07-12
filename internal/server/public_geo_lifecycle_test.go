package server

import (
	"context"
	"database/sql"
	"errors"
	"net/netip"
	"sync"
	"testing"
	"time"

	"p2pstream/internal/db"
)

func TestRunPublicGeoMaintenanceRefreshesDueDataOnce(t *testing.T) {
	ctx := context.Background()
	now := time.Now().UTC()
	app := NewApp(nil, newServerTestDB(t))
	refresher := &lifecycleGeoRefresher{
		geoInfo: PublicGeoIPDatabaseInfo{
			DatabaseType: "GeoLite2-Country",
			BuildAt:      now.Add(-time.Hour),
		},
		providerCIDRs: map[TrustedProxyProvider][]netip.Prefix{
			TrustedProxyProviderCloudflare: {netip.MustParsePrefix("104.16.0.0/13")},
		},
	}
	app.GeoConfigRefresher = refresher
	if _, err := app.DB.UpdatePublicGeoIpSettings(ctx, db.UpdatePublicGeoIpSettingsParams{
		Enabled:           1,
		MaxmindAccountID:  "12345",
		MaxmindLicenseKey: "secret-value",
	}); err != nil {
		t.Fatal(err)
	}
	stale := now.Add(-25 * time.Hour)
	if _, err := app.DB.SetPublicGeoIpUpdateSuccess(ctx, db.SetPublicGeoIpUpdateSuccessParams{
		DatabaseType:        "GeoLite2-Country",
		DatabaseBuildAt:     sql.NullTime{Time: now.Add(-48 * time.Hour), Valid: true},
		LastUpdateAttemptAt: sql.NullTime{Time: stale, Valid: true},
		LastUpdateSuccessAt: sql.NullTime{Time: stale, Valid: true},
	}); err != nil {
		t.Fatal(err)
	}
	var cloudflare db.PublicTrustedProxySource
	sources, err := app.DB.ListPublicTrustedProxySources(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, source := range sources {
		if source.Provider == string(TrustedProxyProviderCloudflare) {
			cloudflare = source
			break
		}
	}
	if cloudflare.ID == 0 {
		t.Fatal("Cloudflare built-in source was not seeded")
	}
	if _, err := app.DB.SetPublicTrustedProxySourceRefreshSuccess(ctx, db.SetPublicTrustedProxySourceRefreshSuccessParams{
		CidrsJson:            `["104.16.0.0/13"]`,
		LastRefreshAttemptAt: sql.NullTime{Time: stale, Valid: true},
		LastRefreshSuccessAt: sql.NullTime{Time: stale, Valid: true},
		ID:                   cloudflare.ID,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := app.DB.SetPublicTrustedProxySourceEnabled(ctx, db.SetPublicTrustedProxySourceEnabledParams{Enabled: 1, ID: cloudflare.ID}); err != nil {
		t.Fatal(err)
	}

	app.runPublicGeoMaintenance(ctx, now)
	if got := refresher.geoRefreshCount(); got != 1 {
		t.Fatalf("GeoIP refresh calls = %d, want 1", got)
	}
	if got := refresher.providerRefreshCount(TrustedProxyProviderCloudflare); got != 1 {
		t.Fatalf("Cloudflare refresh calls = %d, want 1", got)
	}

	app.runPublicGeoMaintenance(ctx, now.Add(time.Minute))
	if got := refresher.geoRefreshCount(); got != 1 {
		t.Fatalf("fresh GeoIP refresh calls = %d, want unchanged 1", got)
	}
	if got := refresher.providerRefreshCount(TrustedProxyProviderCloudflare); got != 1 {
		t.Fatalf("fresh Cloudflare refresh calls = %d, want unchanged 1", got)
	}
}

func TestStopPublicGeoMaintenanceCancelsAndWaits(t *testing.T) {
	ctx := context.Background()
	app := NewApp(nil, newServerTestDB(t))
	if _, err := app.DB.UpdatePublicGeoIpSettings(ctx, db.UpdatePublicGeoIpSettingsParams{
		Enabled:           1,
		MaxmindAccountID:  "12345",
		MaxmindLicenseKey: "secret-value",
	}); err != nil {
		t.Fatal(err)
	}
	refresher := &lifecycleGeoRefresher{blockGeoRefresh: true, geoRefreshStarted: make(chan struct{})}
	app.GeoConfigRefresher = refresher
	app.StartPublicGeoMaintenance(context.Background())

	select {
	case <-refresher.geoRefreshStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("background GeoIP refresh did not start")
	}
	stopped := make(chan struct{})
	go func() {
		app.StopPublicGeoMaintenance()
		close(stopped)
	}()
	select {
	case <-stopped:
	case <-time.After(2 * time.Second):
		t.Fatal("maintenance stop did not wait for canceled refresh to exit")
	}
	app.StopPublicGeoMaintenance()
}

func TestPublicGeoRefreshDue(t *testing.T) {
	now := time.Now().UTC()
	for _, tc := range []struct {
		name string
		last sql.NullTime
		want bool
	}{
		{name: "never", want: true},
		{name: "zero", last: sql.NullTime{Valid: true}, want: true},
		{name: "fresh", last: sql.NullTime{Time: now.Add(-time.Hour), Valid: true}},
		{name: "due", last: sql.NullTime{Time: now.Add(-publicGeoRefreshAge), Valid: true}, want: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := publicGeoRefreshDue(tc.last, now); got != tc.want {
				t.Fatalf("publicGeoRefreshDue() = %t, want %t", got, tc.want)
			}
		})
	}
}

type lifecycleGeoRefresher struct {
	mu                sync.Mutex
	geoInfo           PublicGeoIPDatabaseInfo
	geoCalls          int
	providerCalls     map[TrustedProxyProvider]int
	providerCIDRs     map[TrustedProxyProvider][]netip.Prefix
	blockGeoRefresh   bool
	geoRefreshStarted chan struct{}
	startOnce         sync.Once
}

func (r *lifecycleGeoRefresher) RefreshGeoIPDatabase(ctx context.Context, _, _ string) (PublicGeoIPDatabaseInfo, error) {
	r.mu.Lock()
	r.geoCalls++
	block := r.blockGeoRefresh
	started := r.geoRefreshStarted
	info := r.geoInfo
	r.mu.Unlock()
	if block {
		r.startOnce.Do(func() { close(started) })
		<-ctx.Done()
		return PublicGeoIPDatabaseInfo{}, ctx.Err()
	}
	return info, nil
}

func (r *lifecycleGeoRefresher) RefreshTrustedProxySource(_ context.Context, provider TrustedProxyProvider) ([]netip.Prefix, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.providerCalls == nil {
		r.providerCalls = make(map[TrustedProxyProvider]int)
	}
	r.providerCalls[provider]++
	prefixes, ok := r.providerCIDRs[provider]
	if !ok {
		return nil, errors.New("provider fixture is missing")
	}
	return append([]netip.Prefix(nil), prefixes...), nil
}

func (r *lifecycleGeoRefresher) GeoIPInfo() (GeoIPCountryDatabaseInfo, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return GeoIPCountryDatabaseInfo{DatabaseType: r.geoInfo.DatabaseType, BuildTime: r.geoInfo.BuildAt}, r.geoInfo.DatabaseType != "" && !r.geoInfo.BuildAt.IsZero()
}

func (r *lifecycleGeoRefresher) LookupCountry(netip.Addr) (string, bool, error) {
	return "", false, nil
}

func (r *lifecycleGeoRefresher) geoRefreshCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.geoCalls
}

func (r *lifecycleGeoRefresher) providerRefreshCount(provider TrustedProxyProvider) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.providerCalls[provider]
}
