package server

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/netip"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"time"

	maxminddb "github.com/oschwald/maxminddb-golang/v2"
)

const (
	MaxMindGeoLite2CountryDownloadURL = "https://download.maxmind.com/geoip/databases/GeoLite2-Country/download?suffix=tar.gz"
	GeoIPCountryDatabaseFilename      = "GeoLite2-Country.mmdb"

	MaxMaxMindArchiveBytes   int64 = 64 << 20
	MaxMaxMindDatabaseBytes  int64 = 64 << 20
	MaxMaxMindExtractedBytes int64 = 128 << 20
	MaxMaxMindArchiveEntries       = 256
)

var (
	ErrGeoIPCountryDatabaseUnavailable = errors.New("GeoIP country database is unavailable")
	ErrGeoIPCountryStoreClosed         = errors.New("GeoIP country store is closed")
)

type MaxMindCredentials struct {
	AccountID  string
	LicenseKey string
}

type GeoIPCountryDatabaseInfo struct {
	Path         string
	DatabaseType string
	BuildTime    time.Time
	SizeBytes    int64
}

// GeoIPCountryStore owns the current MMDB reader. Lookups may run concurrently
// with refreshes; an old mmap is closed only after all of its readers leave.
type GeoIPCountryStore struct {
	refreshMu sync.Mutex
	mu        sync.RWMutex
	path      string
	client    HTTPClient
	reader    *maxminddb.Reader
	info      GeoIPCountryDatabaseInfo
	closed    bool
}

func NewGeoIPCountryStore(databasePath string, client HTTPClient) (*GeoIPCountryStore, error) {
	databasePath = strings.TrimSpace(databasePath)
	if databasePath == "" {
		return nil, errors.New("GeoIP country database path is required")
	}
	if client == nil {
		client = defaultMaxMindHTTPClient()
	}
	return &GeoIPCountryStore{path: databasePath, client: client}, nil
}

func GeoIPCountryDatabasePath(configDir string) string {
	return filepath.Join(configDir, "geoip", GeoIPCountryDatabaseFilename)
}

// Load activates an existing on-disk database. It leaves any active reader in
// place if the candidate is missing, corrupt, or the wrong database type.
func (s *GeoIPCountryStore) Load() error {
	if s == nil {
		return ErrGeoIPCountryDatabaseUnavailable
	}
	s.refreshMu.Lock()
	defer s.refreshMu.Unlock()
	if s.isClosed() {
		return ErrGeoIPCountryStoreClosed
	}
	if err := hardenGeoIPCountryDatabasePath(s.path); err != nil && !os.IsNotExist(err) {
		return err
	}
	reader, info, err := openGeoIPCountryDatabase(s.path)
	if err != nil {
		return err
	}
	return s.install(reader, info)
}

func hardenGeoIPCountryDatabasePath(databasePath string) error {
	directory := filepath.Dir(databasePath)
	if directory != "" && directory != "." {
		if err := os.Chmod(directory, 0o700); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("secure GeoIP country database directory: %w", err)
		}
	}
	if err := os.Chmod(databasePath, 0o600); err != nil {
		return fmt.Errorf("secure GeoIP country database file: %w", err)
	}
	return nil
}

// Refresh downloads the fixed GeoLite2 Country permalink, validates and
// verifies the MMDB, atomically replaces the file, and only then swaps readers.
func (s *GeoIPCountryStore) Refresh(ctx context.Context, credentials MaxMindCredentials) error {
	if s == nil {
		return ErrGeoIPCountryDatabaseUnavailable
	}
	if err := validateMaxMindCredentials(credentials); err != nil {
		return err
	}
	s.refreshMu.Lock()
	defer s.refreshMu.Unlock()
	if s.isClosed() {
		return ErrGeoIPCountryStoreClosed
	}
	database, err := DownloadMaxMindGeoLite2Country(ctx, s.client, credentials)
	if err != nil {
		return err
	}
	if s.isClosed() {
		return ErrGeoIPCountryStoreClosed
	}
	// Open and fully verify the candidate before replacing the last-known-good
	// file. Installing this already-open reader after the rename also avoids a
	// post-commit reopen failure that could be reported as a failed refresh.
	reader, info, err := validateGeoIPCountryDatabase(database, s.path)
	if err != nil {
		return fmt.Errorf("validate GeoIP country database before replacement: %w", err)
	}
	installReader := false
	defer func() {
		if !installReader {
			_ = reader.Close()
		}
	}()
	if current, ready := s.Info(); ready && info.BuildTime.Before(current.BuildTime) {
		return fmt.Errorf("refusing GeoIP country database build rollback from %s to %s", current.BuildTime.Format(time.RFC3339), info.BuildTime.Format(time.RFC3339))
	}
	if err := writeFileAtomically(s.path, database, 0o600); err != nil {
		return fmt.Errorf("replace GeoIP country database: %w", err)
	}
	if err := s.install(reader, info); err != nil {
		return err
	}
	installReader = true
	return nil
}

func (s *GeoIPCountryStore) LookupCountry(addr netip.Addr) (string, bool, error) {
	if s == nil {
		return "", false, ErrGeoIPCountryDatabaseUnavailable
	}
	addr = addr.Unmap()
	if !isPublicGeoIPAddress(addr) {
		return "", false, nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.closed {
		return "", false, ErrGeoIPCountryStoreClosed
	}
	if s.reader == nil {
		return "", false, ErrGeoIPCountryDatabaseUnavailable
	}
	result := s.reader.Lookup(addr)
	if err := result.Err(); err != nil {
		return "", false, err
	}
	if !result.Found() {
		return "", false, nil
	}
	var record struct {
		Country struct {
			ISOCode string `maxminddb:"iso_code"`
		} `maxminddb:"country"`
	}
	if err := result.Decode(&record); err != nil {
		return "", false, err
	}
	code := strings.ToUpper(strings.TrimSpace(record.Country.ISOCode))
	if !validISOCountryCode(code) {
		return "", false, nil
	}
	return code, true, nil
}

func (s *GeoIPCountryStore) Info() (GeoIPCountryDatabaseInfo, bool) {
	if s == nil {
		return GeoIPCountryDatabaseInfo{}, false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.info, !s.closed && s.reader != nil
}

func (s *GeoIPCountryStore) Close() error {
	if s == nil {
		return nil
	}
	s.refreshMu.Lock()
	defer s.refreshMu.Unlock()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	s.closed = true
	s.info = GeoIPCountryDatabaseInfo{}
	if s.reader == nil {
		return nil
	}
	err := s.reader.Close()
	s.reader = nil
	return err
}

func (s *GeoIPCountryStore) isClosed() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.closed
}

func (s *GeoIPCountryStore) install(reader *maxminddb.Reader, info GeoIPCountryDatabaseInfo) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		_ = reader.Close()
		return ErrGeoIPCountryStoreClosed
	}
	old := s.reader
	s.reader = reader
	s.info = info
	if old != nil {
		_ = old.Close()
	}
	return nil
}

// DownloadMaxMindGeoLite2Country uses Basic Authentication on MaxMind's fixed
// download permalink. Standard http.Client redirect policy prevents these
// credentials from being forwarded to the cross-origin presigned R2 URL.
func DownloadMaxMindGeoLite2Country(ctx context.Context, client HTTPClient, credentials MaxMindCredentials) ([]byte, error) {
	if err := validateMaxMindCredentials(credentials); err != nil {
		return nil, err
	}
	if client == nil {
		client = defaultMaxMindHTTPClient()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, MaxMindGeoLite2CountryDownloadURL, nil)
	if err != nil {
		return nil, err
	}
	req.SetBasicAuth(credentials.AccountID, credentials.LicenseKey)
	req.Header.Set("Accept", "application/gzip, application/octet-stream")
	req.Header.Set("User-Agent", "p2pstream-geoip-updater")
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("download MaxMind GeoLite2 Country database: %w", err)
	}
	if resp == nil {
		return nil, errors.New("download MaxMind GeoLite2 Country database: HTTP client returned a nil response")
	}
	if resp.Body == nil {
		return nil, errors.New("download MaxMind GeoLite2 Country database: response has no body")
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, fmt.Errorf("download MaxMind GeoLite2 Country database: unexpected HTTP status %d", resp.StatusCode)
	}
	if resp.ContentLength > MaxMaxMindArchiveBytes {
		return nil, errors.New("download MaxMind GeoLite2 Country database: archive exceeds size limit")
	}
	database, err := ExtractMaxMindGeoLite2Country(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("extract MaxMind GeoLite2 Country database: %w", err)
	}
	reader, _, err := validateGeoIPCountryDatabase(database, "")
	if err != nil {
		return nil, fmt.Errorf("validate MaxMind GeoLite2 Country database: %w", err)
	}
	if err := reader.Close(); err != nil {
		return nil, err
	}
	return database, nil
}

// ExtractMaxMindGeoLite2Country performs a bounded in-memory extraction. It
// rejects traversal, absolute paths, links, duplicate MMDBs, oversized entries,
// and trailing archive entries with unsafe names.
func ExtractMaxMindGeoLite2Country(archive io.Reader) ([]byte, error) {
	if archive == nil {
		return nil, errors.New("archive reader is nil")
	}
	compressed := &io.LimitedReader{R: archive, N: MaxMaxMindArchiveBytes + 1}
	gzipReader, err := gzip.NewReader(compressed)
	if err != nil {
		return nil, err
	}
	decompressed := &io.LimitedReader{R: gzipReader, N: MaxMaxMindExtractedBytes + 1}
	tarReader := tar.NewReader(decompressed)
	var database []byte
	entries := 0
	var declaredBytes int64
	for {
		header, err := tarReader.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			_ = gzipReader.Close()
			return nil, err
		}
		entries++
		if entries > MaxMaxMindArchiveEntries {
			_ = gzipReader.Close()
			return nil, errors.New("archive contains too many entries")
		}
		if !safeTarPath(header.Name) {
			_ = gzipReader.Close()
			return nil, errors.New("archive contains an unsafe path")
		}
		if header.Size < 0 || header.Size > MaxMaxMindExtractedBytes-declaredBytes {
			_ = gzipReader.Close()
			return nil, errors.New("archive exceeds extracted size limit")
		}
		declaredBytes += header.Size
		cleanName := path.Clean(strings.TrimSuffix(header.Name, "/"))
		isDatabase := path.Base(cleanName) == GeoIPCountryDatabaseFilename
		switch header.Typeflag {
		case tar.TypeDir:
			if isDatabase || header.Size != 0 {
				_ = gzipReader.Close()
				return nil, errors.New("archive has an invalid directory entry")
			}
		case tar.TypeReg, tar.TypeRegA:
			if !isDatabase {
				continue
			}
			if database != nil {
				_ = gzipReader.Close()
				return nil, errors.New("archive contains multiple country databases")
			}
			if header.Size < 1 || header.Size > MaxMaxMindDatabaseBytes {
				_ = gzipReader.Close()
				return nil, errors.New("country database exceeds size limit")
			}
			database = make([]byte, header.Size)
			if _, err := io.ReadFull(tarReader, database); err != nil {
				_ = gzipReader.Close()
				return nil, err
			}
		default:
			_ = gzipReader.Close()
			return nil, errors.New("archive contains a link or unsupported entry type")
		}
	}
	// Consume any concatenated gzip streams and the compressed input so both
	// compressed and decompressed limits apply even after the tar end marker.
	_, drainErr := io.Copy(io.Discard, decompressed)
	closeErr := gzipReader.Close()
	_, compressedDrainErr := io.Copy(io.Discard, compressed)
	if drainErr != nil {
		return nil, drainErr
	}
	if closeErr != nil {
		return nil, closeErr
	}
	if compressedDrainErr != nil {
		return nil, compressedDrainErr
	}
	if decompressed.N <= 0 {
		return nil, errors.New("archive exceeds extracted size limit")
	}
	if compressed.N <= 0 {
		return nil, errors.New("archive exceeds compressed size limit")
	}
	if database == nil {
		return nil, errors.New("archive does not contain GeoLite2-Country.mmdb")
	}
	return database, nil
}

func openGeoIPCountryDatabase(databasePath string) (*maxminddb.Reader, GeoIPCountryDatabaseInfo, error) {
	stat, err := os.Stat(databasePath)
	if err != nil {
		return nil, GeoIPCountryDatabaseInfo{}, err
	}
	if !stat.Mode().IsRegular() {
		return nil, GeoIPCountryDatabaseInfo{}, errors.New("GeoIP country database is not a regular file")
	}
	if stat.Size() < 1 || stat.Size() > MaxMaxMindDatabaseBytes {
		return nil, GeoIPCountryDatabaseInfo{}, errors.New("GeoIP country database has an invalid size")
	}
	reader, err := maxminddb.Open(databasePath)
	if err != nil {
		return nil, GeoIPCountryDatabaseInfo{}, err
	}
	info, err := verifyGeoIPCountryReader(reader, databasePath, stat.Size())
	if err != nil {
		_ = reader.Close()
		return nil, GeoIPCountryDatabaseInfo{}, err
	}
	return reader, info, nil
}

func validateGeoIPCountryDatabase(database []byte, databasePath string) (*maxminddb.Reader, GeoIPCountryDatabaseInfo, error) {
	if len(database) < 1 || int64(len(database)) > MaxMaxMindDatabaseBytes {
		return nil, GeoIPCountryDatabaseInfo{}, errors.New("GeoIP country database has an invalid size")
	}
	reader, err := maxminddb.OpenBytes(database)
	if err != nil {
		return nil, GeoIPCountryDatabaseInfo{}, err
	}
	info, err := verifyGeoIPCountryReader(reader, databasePath, int64(len(database)))
	if err != nil {
		_ = reader.Close()
		return nil, GeoIPCountryDatabaseInfo{}, err
	}
	return reader, info, nil
}

func verifyGeoIPCountryReader(reader *maxminddb.Reader, databasePath string, size int64) (GeoIPCountryDatabaseInfo, error) {
	if reader.Metadata.DatabaseType != "GeoLite2-Country" {
		return GeoIPCountryDatabaseInfo{}, fmt.Errorf("unexpected MaxMind database type %q", reader.Metadata.DatabaseType)
	}
	if reader.Metadata.BuildEpoch == 0 {
		return GeoIPCountryDatabaseInfo{}, errors.New("GeoIP country database has no build timestamp")
	}
	if err := reader.Verify(); err != nil {
		return GeoIPCountryDatabaseInfo{}, err
	}
	return GeoIPCountryDatabaseInfo{
		Path:         databasePath,
		DatabaseType: reader.Metadata.DatabaseType,
		BuildTime:    reader.Metadata.BuildTime().UTC(),
		SizeBytes:    size,
	}, nil
}

func validateMaxMindCredentials(credentials MaxMindCredentials) error {
	if credentials.AccountID == "" || strings.TrimSpace(credentials.AccountID) != credentials.AccountID {
		return errors.New("MaxMind account ID is required")
	}
	for _, char := range credentials.AccountID {
		if char < '0' || char > '9' {
			return errors.New("MaxMind account ID must be numeric")
		}
	}
	if credentials.LicenseKey == "" || strings.TrimSpace(credentials.LicenseKey) != credentials.LicenseKey {
		return errors.New("MaxMind license key is required")
	}
	return nil
}

func safeTarPath(name string) bool {
	if name == "" || strings.Contains(name, "\\") || strings.ContainsRune(name, '\x00') || path.IsAbs(name) {
		return false
	}
	withoutSlash := strings.TrimSuffix(name, "/")
	clean := path.Clean(withoutSlash)
	if clean == "." || clean == ".." || strings.HasPrefix(clean, "../") {
		return false
	}
	return clean == withoutSlash
}

func writeFileAtomically(filename string, content []byte, mode os.FileMode) (err error) {
	directory := filepath.Dir(filename)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return err
	}
	if err := os.Chmod(directory, 0o700); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(directory, ".geoip-country-*.tmp")
	if err != nil {
		return err
	}
	temporaryName := temporary.Name()
	defer func() {
		if temporary != nil {
			_ = temporary.Close()
		}
		if err != nil {
			_ = os.Remove(temporaryName)
		}
	}()
	if err = temporary.Chmod(mode); err != nil {
		return err
	}
	if _, err = temporary.Write(content); err != nil {
		return err
	}
	if err = temporary.Sync(); err != nil {
		return err
	}
	if err = temporary.Close(); err != nil {
		return err
	}
	temporary = nil
	directoryHandle, err := os.Open(directory)
	if err != nil {
		return err
	}
	defer directoryHandle.Close()
	if err = os.Rename(temporaryName, filename); err != nil {
		return err
	}
	// At this point the fully-written, prevalidated candidate is committed.
	// Directory sync is best-effort because returning an error after rename
	// would falsely claim the last-known-good file was retained.
	_ = directoryHandle.Sync()
	return nil
}

func validISOCountryCode(code string) bool {
	return len(code) == 2 && code != "XX" && code[0] >= 'A' && code[0] <= 'Z' && code[1] >= 'A' && code[1] <= 'Z'
}

var geoIPSpecialUsePrefixes = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"),
	netip.MustParsePrefix("10.0.0.0/8"),
	netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("127.0.0.0/8"),
	netip.MustParsePrefix("169.254.0.0/16"),
	netip.MustParsePrefix("172.16.0.0/12"),
	netip.MustParsePrefix("192.0.0.0/24"),
	netip.MustParsePrefix("192.0.2.0/24"),
	netip.MustParsePrefix("192.88.99.0/24"),
	netip.MustParsePrefix("192.168.0.0/16"),
	netip.MustParsePrefix("198.18.0.0/15"),
	netip.MustParsePrefix("198.51.100.0/24"),
	netip.MustParsePrefix("203.0.113.0/24"),
	netip.MustParsePrefix("224.0.0.0/4"),
	netip.MustParsePrefix("240.0.0.0/4"),
	netip.MustParsePrefix("::/128"),
	netip.MustParsePrefix("::1/128"),
	netip.MustParsePrefix("64:ff9b::/96"),
	netip.MustParsePrefix("64:ff9b:1::/48"),
	netip.MustParsePrefix("100::/64"),
	netip.MustParsePrefix("2001::/23"),
	netip.MustParsePrefix("2002::/16"),
	netip.MustParsePrefix("fc00::/7"),
	netip.MustParsePrefix("fe80::/10"),
	netip.MustParsePrefix("ff00::/8"),
}

func isPublicGeoIPAddress(addr netip.Addr) bool {
	if !addr.IsValid() || addr.Zone() != "" {
		return false
	}
	addr = addr.Unmap()
	if !addr.IsGlobalUnicast() || addr.IsPrivate() {
		return false
	}
	return !prefixesContain(geoIPSpecialUsePrefixes, addr)
}
