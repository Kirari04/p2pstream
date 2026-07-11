package server

import (
	"context"
	"net/netip"
)

// PublicGeoRuntime is the production adapter shared by management refreshes and
// request-time country lookups. Persistence of provider snapshots and refresh
// status remains in the management/DB layer.
type PublicGeoRuntime struct {
	country *GeoIPCountryStore
	client  HTTPClient
}

var _ PublicGeoConfigRefresher = (*PublicGeoRuntime)(nil)
var _ publicGeoCountryLookup = (*PublicGeoRuntime)(nil)

func NewPublicGeoRuntime(databasePath string, client HTTPClient) (*PublicGeoRuntime, error) {
	store, err := NewGeoIPCountryStore(databasePath, client)
	if err != nil {
		return nil, err
	}
	if client == nil {
		client = defaultProviderRangeHTTPClient()
	}
	return &PublicGeoRuntime{country: store, client: client}, nil
}

// Load activates the last database persisted under CONFIG_DIR. A missing file
// is returned as os.ErrNotExist so startup can distinguish a first run from a
// corrupt existing database.
func (r *PublicGeoRuntime) Load() error {
	if r == nil || r.country == nil {
		return ErrGeoIPCountryDatabaseUnavailable
	}
	return r.country.Load()
}

func (r *PublicGeoRuntime) RefreshGeoIPDatabase(ctx context.Context, accountID, licenseKey string) (PublicGeoIPDatabaseInfo, error) {
	if r == nil || r.country == nil {
		return PublicGeoIPDatabaseInfo{}, ErrGeoIPCountryDatabaseUnavailable
	}
	if err := r.country.Refresh(ctx, MaxMindCredentials{AccountID: accountID, LicenseKey: licenseKey}); err != nil {
		return PublicGeoIPDatabaseInfo{}, err
	}
	info, ready := r.country.Info()
	if !ready {
		return PublicGeoIPDatabaseInfo{}, ErrGeoIPCountryDatabaseUnavailable
	}
	return PublicGeoIPDatabaseInfo{DatabaseType: info.DatabaseType, BuildAt: info.BuildTime}, nil
}

func (r *PublicGeoRuntime) RefreshTrustedProxySource(ctx context.Context, provider TrustedProxyProvider) ([]netip.Prefix, error) {
	if r == nil {
		return nil, ErrGeoIPCountryDatabaseUnavailable
	}
	return FetchTrustedProxyProviderPrefixes(ctx, r.client, provider)
}

func (r *PublicGeoRuntime) LookupCountry(addr netip.Addr) (string, bool, error) {
	if r == nil || r.country == nil {
		return "", false, ErrGeoIPCountryDatabaseUnavailable
	}
	return r.country.LookupCountry(addr)
}

func (r *PublicGeoRuntime) GeoIPInfo() (GeoIPCountryDatabaseInfo, bool) {
	if r == nil || r.country == nil {
		return GeoIPCountryDatabaseInfo{}, false
	}
	return r.country.Info()
}

func (r *PublicGeoRuntime) Close() error {
	if r == nil || r.country == nil {
		return nil
	}
	return r.country.Close()
}
