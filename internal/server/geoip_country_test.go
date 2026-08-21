package server

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"net/http"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/maxmind/mmdbwriter"
	"github.com/maxmind/mmdbwriter/mmdbtype"
)

func TestGeoIPCountryStoreLoadLookupAndSpecialAddresses(t *testing.T) {
	database := buildCountryMMDB(t, "GeoLite2-Country", map[string]string{
		"81.2.69.0/24": "GB",
		"10.0.0.0/8":   "DE",
	})
	databasePath := filepath.Join(t.TempDir(), GeoIPCountryDatabaseFilename)
	if err := os.WriteFile(databasePath, database, 0o600); err != nil {
		t.Fatal(err)
	}
	store, err := NewGeoIPCountryStore(databasePath, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Load(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })

	country, found, err := store.LookupCountry(netip.MustParseAddr("81.2.69.142"))
	if err != nil || !found || country != "GB" {
		t.Fatalf("lookup = %q, %v, %v; want GB", country, found, err)
	}
	if _, found, err := store.LookupCountry(netip.MustParseAddr("8.8.8.8")); err != nil || found {
		t.Fatalf("missing lookup found = %v, err = %v", found, err)
	}

	for _, address := range []string{
		"10.1.2.3",
		"100.64.0.1",
		"127.0.0.1",
		"169.254.1.1",
		"192.0.2.1",
		"198.18.0.1",
		"203.0.113.1",
		"224.0.0.1",
		"240.0.0.1",
		"::1",
		"2001:db8::1",
		"fc00::1",
		"fe80::1",
		"ff02::1",
	} {
		t.Run("unknown_"+strings.ReplaceAll(address, ":", "_"), func(t *testing.T) {
			country, found, err := store.LookupCountry(netip.MustParseAddr(address))
			if err != nil || found || country != "" {
				t.Fatalf("lookup(%s) = %q, %v, %v; want unknown", address, country, found, err)
			}
		})
	}

	info, ready := store.Info()
	if !ready || info.DatabaseType != "GeoLite2-Country" || info.Path != databasePath || info.SizeBytes != int64(len(database)) {
		t.Fatalf("info = %#v, ready = %v", info, ready)
	}
}

func TestGeoIPCountryStoreLoadHardensExistingPermissions(t *testing.T) {
	database := buildCountryMMDB(t, "GeoLite2-Country", map[string]string{"81.2.69.0/24": "GB"})
	directory := filepath.Join(t.TempDir(), "geoip")
	if err := os.Mkdir(directory, 0o755); err != nil {
		t.Fatal(err)
	}
	databasePath := filepath.Join(directory, GeoIPCountryDatabaseFilename)
	if err := os.WriteFile(databasePath, database, 0o644); err != nil {
		t.Fatal(err)
	}
	store, err := NewGeoIPCountryStore(databasePath, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Load(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if stat, err := os.Stat(directory); err != nil || stat.Mode().Perm() != 0o700 {
		t.Fatalf("GeoIP directory mode = %v, err = %v; want 0700", statMode(stat), err)
	}
	if stat, err := os.Stat(databasePath); err != nil || stat.Mode().Perm() != 0o600 {
		t.Fatalf("GeoIP database mode = %v, err = %v; want 0600", statMode(stat), err)
	}
}

func TestGeoIPCountryStoreUsesCountryNotRegisteredCountry(t *testing.T) {
	database := buildMMDBWithRecords(t, "GeoLite2-Country", map[string]mmdbtype.Map{
		"81.2.69.0/24": {
			mmdbtype.String("registered_country"): mmdbtype.Map{
				mmdbtype.String("iso_code"): mmdbtype.String("US"),
			},
		},
	})
	databasePath := filepath.Join(t.TempDir(), GeoIPCountryDatabaseFilename)
	if err := os.WriteFile(databasePath, database, 0o600); err != nil {
		t.Fatal(err)
	}
	store, err := NewGeoIPCountryStore(databasePath, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Load(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })

	if country, found, err := store.LookupCountry(netip.MustParseAddr("81.2.69.142")); err != nil || found || country != "" {
		t.Fatalf("lookup = %q, %v, %v; registered_country must not be substituted", country, found, err)
	}
}

func TestGeoIPCountryStoreRefreshIsAtomicAndRetainsLastGood(t *testing.T) {
	initial := buildCountryMMDB(t, "GeoLite2-Country", map[string]string{"81.2.69.0/24": "GB"})
	replacement := buildCountryMMDB(t, "GeoLite2-Country", map[string]string{"81.2.69.0/24": "US"})
	archive := buildGeoIPTarGzip(t, []geoIPTarEntry{
		{name: "GeoLite2-Country_20260711/", typeflag: tar.TypeDir},
		{name: "GeoLite2-Country_20260711/LICENSE.txt", data: []byte("test license")},
		{name: "GeoLite2-Country_20260711/GeoLite2-Country.mmdb", data: replacement},
	})
	databasePath := filepath.Join(t.TempDir(), "geoip", GeoIPCountryDatabaseFilename)
	if err := os.MkdirAll(filepath.Dir(databasePath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(databasePath, initial, 0o600); err != nil {
		t.Fatal(err)
	}

	requests := 0
	client := geoIPHTTPClientFunc(func(req *http.Request) (*http.Response, error) {
		requests++
		if req.URL.String() != MaxMindGeoLite2CountryDownloadURL {
			t.Fatalf("URL = %q", req.URL)
		}
		username, password, ok := req.BasicAuth()
		if !ok || username != "12345" || password != "test-license-secret" {
			t.Fatalf("unexpected Basic authentication: %q, %q, %v", username, password, ok)
		}
		if requests == 1 {
			return geoIPHTTPResponse(http.StatusOK, archive), nil
		}
		return geoIPHTTPResponse(http.StatusServiceUnavailable, []byte("do not reflect this body")), nil
	})
	store, err := NewGeoIPCountryStore(databasePath, client)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Load(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	credentials := MaxMindCredentials{AccountID: "12345", LicenseKey: "test-license-secret"}

	if err := store.Refresh(context.Background(), credentials); err != nil {
		t.Fatal(err)
	}
	assertCountryLookup(t, store, "81.2.69.142", "US")
	onDisk, err := os.ReadFile(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(onDisk, replacement) {
		t.Fatal("on-disk database is not the validated replacement")
	}
	stat, err := os.Stat(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	if got := stat.Mode().Perm(); got != 0o600 {
		t.Fatalf("database mode = %o, want 600", got)
	}

	err = store.Refresh(context.Background(), credentials)
	if err == nil {
		t.Fatal("expected failed second refresh")
	}
	if strings.Contains(err.Error(), credentials.LicenseKey) || strings.Contains(err.Error(), "do not reflect") {
		t.Fatalf("refresh error leaks secret or response body: %v", err)
	}
	assertCountryLookup(t, store, "81.2.69.142", "US")
	afterFailure, err := os.ReadFile(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(afterFailure, replacement) {
		t.Fatal("failed refresh changed the last-known-good database")
	}
}

func TestGeoIPCountryStoreFailedLoadRetainsReader(t *testing.T) {
	good := buildCountryMMDB(t, "GeoLite2-Country", map[string]string{"81.2.69.0/24": "GB"})
	directory := t.TempDir()
	databasePath := filepath.Join(directory, GeoIPCountryDatabaseFilename)
	if err := os.WriteFile(databasePath, good, 0o600); err != nil {
		t.Fatal(err)
	}
	store, err := NewGeoIPCountryStore(databasePath, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Load(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	assertCountryLookup(t, store, "81.2.69.142", "GB")

	corruptPath := filepath.Join(directory, "corrupt.mmdb")
	if err := os.WriteFile(corruptPath, []byte("not an mmdb"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(corruptPath, databasePath); err != nil {
		t.Fatal(err)
	}
	if err := store.Load(); err == nil {
		t.Fatal("expected corrupt candidate error")
	}
	assertCountryLookup(t, store, "81.2.69.142", "GB")
}

func TestGeoIPCountryStoreRejectsDatabaseBuildRollback(t *testing.T) {
	newerEpoch := time.Date(2026, time.July, 11, 0, 0, 0, 0, time.UTC)
	olderEpoch := newerEpoch.Add(-24 * time.Hour)
	newer := buildCountryMMDBAtEpoch(t, "GeoLite2-Country", map[string]string{"81.2.69.0/24": "GB"}, newerEpoch)
	older := buildCountryMMDBAtEpoch(t, "GeoLite2-Country", map[string]string{"81.2.69.0/24": "US"}, olderEpoch)
	archive := buildGeoIPTarGzip(t, []geoIPTarEntry{{name: "release/GeoLite2-Country.mmdb", data: older}})
	databasePath := filepath.Join(t.TempDir(), GeoIPCountryDatabaseFilename)
	if err := os.WriteFile(databasePath, newer, 0o600); err != nil {
		t.Fatal(err)
	}
	store, err := NewGeoIPCountryStore(databasePath, geoIPHTTPClientFunc(func(*http.Request) (*http.Response, error) {
		return geoIPHTTPResponse(http.StatusOK, archive), nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Load(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })

	err = store.Refresh(context.Background(), MaxMindCredentials{AccountID: "12345", LicenseKey: "secret-value"})
	if err == nil || !strings.Contains(err.Error(), "build rollback") {
		t.Fatalf("refresh error = %v, want build rollback rejection", err)
	}
	assertCountryLookup(t, store, "81.2.69.142", "GB")
	onDisk, err := os.ReadFile(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(onDisk, newer) {
		t.Fatal("rejected rollback changed the on-disk database")
	}
}

func TestGeoIPCountryStoreClose(t *testing.T) {
	database := buildCountryMMDB(t, "GeoLite2-Country", map[string]string{"81.2.69.0/24": "GB"})
	databasePath := filepath.Join(t.TempDir(), GeoIPCountryDatabaseFilename)
	if err := os.WriteFile(databasePath, database, 0o600); err != nil {
		t.Fatal(err)
	}
	store, err := NewGeoIPCountryStore(databasePath, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Load(); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if _, ready := store.Info(); ready {
		t.Fatal("closed store reported ready")
	}
	if _, _, err := store.LookupCountry(netip.MustParseAddr("81.2.69.142")); !errors.Is(err, ErrGeoIPCountryStoreClosed) {
		t.Fatalf("lookup error = %v, want closed", err)
	}
	if err := store.Load(); !errors.Is(err, ErrGeoIPCountryStoreClosed) {
		t.Fatalf("load error = %v, want closed", err)
	}
}

func TestDownloadMaxMindGeoLite2CountryRejectsWrongDatabaseType(t *testing.T) {
	database := buildCountryMMDB(t, "GeoIP2-Country", map[string]string{"81.2.69.0/24": "GB"})
	archive := buildGeoIPTarGzip(t, []geoIPTarEntry{{name: "release/GeoLite2-Country.mmdb", data: database}})
	client := geoIPHTTPClientFunc(func(*http.Request) (*http.Response, error) {
		return geoIPHTTPResponse(http.StatusOK, archive), nil
	})
	credentials := MaxMindCredentials{AccountID: "12345", LicenseKey: "secret-value"}
	_, err := DownloadMaxMindGeoLite2Country(context.Background(), client, credentials)
	if err == nil || !strings.Contains(err.Error(), "unexpected MaxMind database type") {
		t.Fatalf("error = %v", err)
	}
	if strings.Contains(err.Error(), credentials.LicenseKey) {
		t.Fatalf("error leaks license key: %v", err)
	}
}

func TestDownloadMaxMindGeoLite2CountryRejectsMissingBuildTimestamp(t *testing.T) {
	database := buildCountryMMDB(t, "GeoLite2-Country", map[string]string{"81.2.69.0/24": "GB"})
	epoch := time.Date(2026, time.July, 11, 0, 0, 0, 0, time.UTC).Unix()
	encodedEpoch := make([]byte, 8)
	binary.BigEndian.PutUint64(encodedEpoch, uint64(epoch))
	// mmdbwriter emits the smallest unsigned integer encoding, so locate the
	// four-byte suffix and retain its encoded width while changing its value.
	encodedEpoch = encodedEpoch[4:]
	index := bytes.LastIndex(database, encodedEpoch)
	if index < 0 {
		t.Fatal("test MMDB build timestamp encoding was not found")
	}
	copy(database[index:index+len(encodedEpoch)], make([]byte, len(encodedEpoch)))
	archive := buildGeoIPTarGzip(t, []geoIPTarEntry{{name: "release/GeoLite2-Country.mmdb", data: database}})
	client := geoIPHTTPClientFunc(func(*http.Request) (*http.Response, error) {
		return geoIPHTTPResponse(http.StatusOK, archive), nil
	})

	_, err := DownloadMaxMindGeoLite2Country(context.Background(), client, MaxMindCredentials{AccountID: "12345", LicenseKey: "secret-value"})
	if err == nil || !strings.Contains(err.Error(), "no build timestamp") {
		t.Fatalf("error = %v, want missing build timestamp rejection", err)
	}
}

func TestExtractMaxMindGeoLite2CountryRejectsUnsafeArchives(t *testing.T) {
	database := buildCountryMMDB(t, "GeoLite2-Country", map[string]string{"81.2.69.0/24": "GB"})
	tests := []struct {
		name    string
		entries []geoIPTarEntry
	}{
		{name: "traversal", entries: []geoIPTarEntry{{name: "../GeoLite2-Country.mmdb", data: database}}},
		{name: "absolute", entries: []geoIPTarEntry{{name: "/GeoLite2-Country.mmdb", data: database}}},
		{name: "backslash", entries: []geoIPTarEntry{{name: `release\GeoLite2-Country.mmdb`, data: database}}},
		{name: "symlink", entries: []geoIPTarEntry{{name: "release/GeoLite2-Country.mmdb", typeflag: tar.TypeSymlink, linkname: "/etc/passwd"}}},
		{name: "duplicate", entries: []geoIPTarEntry{
			{name: "one/GeoLite2-Country.mmdb", data: database},
			{name: "two/GeoLite2-Country.mmdb", data: database},
		}},
		{name: "unsafe after valid", entries: []geoIPTarEntry{
			{name: "release/GeoLite2-Country.mmdb", data: database},
			{name: "../late", data: []byte("late")},
		}},
		{name: "missing", entries: []geoIPTarEntry{{name: "release/LICENSE.txt", data: []byte("license")}}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			archive := buildGeoIPTarGzip(t, tc.entries)
			if _, err := ExtractMaxMindGeoLite2Country(bytes.NewReader(archive)); err == nil {
				t.Fatal("expected extraction error")
			}
		})
	}
	if _, err := ExtractMaxMindGeoLite2Country(strings.NewReader("not gzip")); err == nil {
		t.Fatal("expected corrupt gzip error")
	}
}

func TestMaxMindCredentialValidationDoesNotCallHTTP(t *testing.T) {
	client := geoIPHTTPClientFunc(func(*http.Request) (*http.Response, error) {
		t.Fatal("HTTP client called with invalid credentials")
		return nil, nil
	})
	for _, credentials := range []MaxMindCredentials{
		{},
		{AccountID: "not-numeric", LicenseKey: "secret"},
		{AccountID: "123", LicenseKey: " secret"},
	} {
		if _, err := DownloadMaxMindGeoLite2Country(context.Background(), client, credentials); err == nil {
			t.Fatalf("credentials %#v unexpectedly accepted", credentials)
		}
	}
}

func buildCountryMMDB(t *testing.T, databaseType string, networks map[string]string) []byte {
	return buildCountryMMDBAtEpoch(t, databaseType, networks, time.Date(2026, time.July, 11, 0, 0, 0, 0, time.UTC))
}

func buildCountryMMDBAtEpoch(t *testing.T, databaseType string, networks map[string]string, buildTime time.Time) []byte {
	t.Helper()
	records := make(map[string]mmdbtype.Map, len(networks))
	for network, country := range networks {
		records[network] = mmdbtype.Map{
			mmdbtype.String("country"): mmdbtype.Map{
				mmdbtype.String("iso_code"): mmdbtype.String(country),
			},
		}
	}
	return buildMMDBWithRecordsAtEpoch(t, databaseType, records, buildTime)
}

func buildMMDBWithRecords(t *testing.T, databaseType string, records map[string]mmdbtype.Map) []byte {
	return buildMMDBWithRecordsAtEpoch(t, databaseType, records, time.Date(2026, time.July, 11, 0, 0, 0, 0, time.UTC))
}

func buildMMDBWithRecordsAtEpoch(t *testing.T, databaseType string, records map[string]mmdbtype.Map, buildTime time.Time) []byte {
	t.Helper()
	tree, err := mmdbwriter.New(mmdbwriter.Options{
		BuildEpoch:              buildTime.Unix(),
		DatabaseType:            databaseType,
		Description:             map[string]string{"en": "p2pstream test country database"},
		IPVersion:               6,
		IncludeReservedNetworks: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	for rawNetwork, record := range records {
		_, network, err := net.ParseCIDR(rawNetwork)
		if err != nil {
			t.Fatal(err)
		}
		if err := tree.Insert(network, record); err != nil {
			t.Fatal(err)
		}
	}
	var database bytes.Buffer
	if _, err := tree.WriteTo(&database); err != nil {
		t.Fatal(err)
	}
	return database.Bytes()
}

type geoIPTarEntry struct {
	name     string
	typeflag byte
	linkname string
	data     []byte
}

func buildGeoIPTarGzip(t *testing.T, entries []geoIPTarEntry) []byte {
	t.Helper()
	var archive bytes.Buffer
	gzipWriter := gzip.NewWriter(&archive)
	tarWriter := tar.NewWriter(gzipWriter)
	for _, entry := range entries {
		typeflag := entry.typeflag
		if typeflag == 0 {
			typeflag = tar.TypeReg
		}
		size := int64(len(entry.data))
		if typeflag != tar.TypeReg && typeflag != tar.TypeRegA {
			size = 0
		}
		header := &tar.Header{
			Name:     entry.name,
			Mode:     0o600,
			Size:     size,
			Typeflag: typeflag,
			Linkname: entry.linkname,
		}
		if typeflag == tar.TypeDir {
			header.Mode = 0o700
		}
		if err := tarWriter.WriteHeader(header); err != nil {
			t.Fatal(err)
		}
		if size > 0 {
			if _, err := tarWriter.Write(entry.data); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := tarWriter.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gzipWriter.Close(); err != nil {
		t.Fatal(err)
	}
	return archive.Bytes()
}

func assertCountryLookup(t *testing.T, store *GeoIPCountryStore, address, want string) {
	t.Helper()
	country, found, err := store.LookupCountry(netip.MustParseAddr(address))
	if err != nil || !found || country != want {
		t.Fatalf("lookup(%s) = %q, %v, %v; want %s", address, country, found, err, want)
	}
}

type geoIPHTTPClientFunc func(*http.Request) (*http.Response, error)

func (fn geoIPHTTPClientFunc) Do(req *http.Request) (*http.Response, error) {
	return fn(req)
}

func geoIPHTTPResponse(status int, body []byte) *http.Response {
	return &http.Response{
		StatusCode:    status,
		Body:          io.NopCloser(bytes.NewReader(body)),
		ContentLength: int64(len(body)),
		Header:        make(http.Header),
	}
}

func statMode(info os.FileInfo) os.FileMode {
	if info == nil {
		return 0
	}
	return info.Mode().Perm()
}
