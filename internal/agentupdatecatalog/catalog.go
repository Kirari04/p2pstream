// Package agentupdatecatalog resolves the latest stable agent release from a
// fixed GitHub repository and authenticates it against an out-of-band root.
// Network responses can select only signed metadata; they can never introduce
// a download URL, command, or artifact digest.
package agentupdatecatalog

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"p2pstream/internal/agentupdate"
)

const (
	latestResponseLimit = 256 << 10
	stateSchemaVersion  = 1
)

var (
	repositoryRE = regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$`)
	versionRE    = regexp.MustCompile(`^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$`)
	digestRE     = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

type Options struct {
	Repository      string
	Root            agentupdate.RootMetadata
	StatePath       string
	RefreshInterval time.Duration
	HTTPClient      *http.Client
	ServerVersion   string
	ProtocolVersion uint32
	Now             func() time.Time
	APIBaseURL      string // Tests only. Production must leave this empty.
	DownloadBaseURL string // Tests only. Production must leave this empty.
}

type Snapshot struct {
	Release       *agentupdate.VerifiedCatalog
	RefreshedAt   time.Time
	NextRefreshAt time.Time
	LastError     string
	UsingStale    bool
}

type persistedFloor struct {
	SchemaVersion      uint64 `json:"schema_version"`
	RootVersion        uint64 `json:"root_version"`
	Sequence           uint64 `json:"sequence"`
	SecurityEpoch      uint64 `json:"security_epoch"`
	MinimumSafeVersion string `json:"minimum_safe_version"`
	ManifestSHA256     string `json:"manifest_sha256"`
	Version            string `json:"version"`
}

type Catalog struct {
	options Options

	mu       sync.Mutex
	floor    persistedFloor
	snapshot Snapshot
}

func New(options Options) (*Catalog, error) {
	options.Repository = strings.TrimSpace(options.Repository)
	if !repositoryRE.MatchString(options.Repository) {
		return nil, errors.New("agent update repository must use GitHub owner/repo syntax")
	}
	if options.StatePath == "" {
		return nil, errors.New("agent update catalog state path is required")
	}
	if options.RefreshInterval < 10*time.Second || options.RefreshInterval > time.Hour {
		return nil, errors.New("agent update catalog refresh interval must be between 10 seconds and 1 hour")
	}
	if options.HTTPClient == nil {
		return nil, errors.New("agent update catalog HTTP client is required")
	}
	if options.Now == nil {
		options.Now = time.Now
	}
	if options.APIBaseURL == "" {
		options.APIBaseURL = "https://api.github.com"
	}
	if options.DownloadBaseURL == "" {
		options.DownloadBaseURL = "https://github.com"
	}
	if options.APIBaseURL != "https://api.github.com" || options.DownloadBaseURL != "https://github.com" {
		// Alternate origins are deliberately available only to localhost tests.
		if !loopbackTestOrigin(options.APIBaseURL) || !loopbackTestOrigin(options.DownloadBaseURL) {
			return nil, errors.New("custom update catalog origins must be loopback test servers")
		}
	}

	catalog := &Catalog{options: options}
	floor, err := readFloor(options.StatePath)
	if err != nil {
		return nil, err
	}
	catalog.floor = floor
	return catalog, nil
}

func loopbackTestOrigin(raw string) bool {
	parsed, err := url.Parse(raw)
	return err == nil && parsed.Scheme == "http" && parsed.Hostname() == "127.0.0.1" && parsed.Port() != "" &&
		parsed.User == nil && parsed.Path == "" && parsed.RawQuery == "" && parsed.Fragment == ""
}

// Latest returns a freshly authenticated release when refresh is due. A last
// known good release survives a transient network failure only while its own
// signed expiry is still in the future.
func (c *Catalog) Latest(ctx context.Context) (*agentupdate.VerifiedCatalog, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	now := c.options.Now().UTC()
	if c.snapshot.Release != nil && now.Before(c.snapshot.NextRefreshAt) && now.Before(c.snapshot.Release.ExpiresAt) {
		return cloneVerified(c.snapshot.Release), nil
	}

	verified, err := c.refreshLocked(ctx, now)
	if err == nil {
		return cloneVerified(verified), nil
	}
	c.snapshot.LastError = err.Error()
	if c.snapshot.Release != nil && now.Before(c.snapshot.Release.ExpiresAt) {
		c.snapshot.UsingStale = true
		c.snapshot.NextRefreshAt = now.Add(retryInterval(c.options.RefreshInterval))
		return cloneVerified(c.snapshot.Release), nil
	}
	return nil, err
}

func (c *Catalog) Snapshot() Snapshot {
	c.mu.Lock()
	defer c.mu.Unlock()
	snapshot := c.snapshot
	snapshot.Release = cloneVerified(snapshot.Release)
	return snapshot
}

func (c *Catalog) refreshLocked(ctx context.Context, now time.Time) (*agentupdate.VerifiedCatalog, error) {
	tag, err := c.fetchLatestTag(ctx)
	if err != nil {
		return nil, err
	}
	manifestJSON, err := c.fetchReleaseAsset(ctx, tag, "p2pstream_agent_update_manifest.json", agentupdate.MaxManifestBytes)
	if err != nil {
		return nil, err
	}
	signatureJSON, err := c.fetchReleaseAsset(ctx, tag, "p2pstream_agent_update_manifest.signatures.json", agentupdate.MaxSignatureEnvelopeBytes)
	if err != nil {
		return nil, err
	}
	verified, err := agentupdate.VerifyCatalog(manifestJSON, signatureJSON, c.options.Root, agentupdate.CatalogVerifyPolicy{
		Now:                       now,
		RequiredChannel:           "stable",
		MinimumRootVersion:        c.floor.RootVersion,
		CurrentSequence:           c.floor.Sequence,
		CurrentSecurityEpoch:      c.floor.SecurityEpoch,
		CurrentMinimumSafeVersion: c.floor.MinimumSafeVersion,
		ServerVersion:             c.options.ServerVersion,
		ProtocolVersion:           c.options.ProtocolVersion,
	})
	if err != nil {
		return nil, fmt.Errorf("verify signed agent update manifest: %w", err)
	}
	if verified.Manifest.Version != tag {
		return nil, errors.New("signed manifest version does not match immutable release tag")
	}
	if c.floor.Sequence == verified.Manifest.Sequence && c.floor.ManifestSHA256 != "" && c.floor.ManifestSHA256 != verified.ManifestSHA256 {
		return nil, errors.New("signed manifest equivocates at an already trusted release sequence")
	}

	nextFloor := persistedFloor{
		SchemaVersion:      stateSchemaVersion,
		RootVersion:        c.options.Root.Version,
		Sequence:           verified.Manifest.Sequence,
		SecurityEpoch:      verified.Manifest.SecurityEpoch,
		MinimumSafeVersion: verified.Manifest.MinimumSafeVersion,
		ManifestSHA256:     verified.ManifestSHA256,
		Version:            verified.Manifest.Version,
	}
	if err := writeFloor(c.options.StatePath, nextFloor); err != nil {
		return nil, fmt.Errorf("persist agent update catalog floor: %w", err)
	}
	c.floor = nextFloor
	c.snapshot = Snapshot{
		Release:       cloneVerified(verified),
		RefreshedAt:   now,
		NextRefreshAt: now.Add(c.options.RefreshInterval),
	}
	return verified, nil
}

func (c *Catalog) fetchLatestTag(ctx context.Context) (string, error) {
	url := c.options.APIBaseURL + "/repos/" + c.options.Repository + "/releases/latest"
	data, err := c.fetch(ctx, url, latestResponseLimit, "application/vnd.github+json")
	if err != nil {
		return "", fmt.Errorf("fetch latest stable release: %w", err)
	}
	var response struct {
		TagName string `json:"tag_name"`
	}
	if err := json.Unmarshal(data, &response); err != nil {
		return "", fmt.Errorf("decode latest stable release: %w", err)
	}
	if !versionRE.MatchString(response.TagName) {
		return "", errors.New("latest stable release returned an invalid tag")
	}
	return response.TagName, nil
}

func (c *Catalog) fetchReleaseAsset(ctx context.Context, tag, name string, limit int) ([]byte, error) {
	url := c.options.DownloadBaseURL + "/" + c.options.Repository + "/releases/download/" + tag + "/" + name
	data, err := c.fetch(ctx, url, limit, "application/octet-stream")
	if err != nil {
		return nil, fmt.Errorf("fetch %s for %s: %w", name, tag, err)
	}
	return data, nil
}

func (c *Catalog) fetch(ctx context.Context, url string, limit int, accept string) ([]byte, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("Accept", accept)
	request.Header.Set("User-Agent", "p2pstream-agent-update-catalog/1")
	response, err := c.options.HTTPClient.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected HTTP status %d", response.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, int64(limit)+1))
	if err != nil {
		return nil, err
	}
	if len(data) > limit {
		return nil, fmt.Errorf("response exceeds %d-byte limit", limit)
	}
	return data, nil
}

func retryInterval(refresh time.Duration) time.Duration {
	retry := refresh / 5
	if retry < 10*time.Second {
		return 10 * time.Second
	}
	if retry > time.Minute {
		return time.Minute
	}
	return retry
}

func cloneVerified(source *agentupdate.VerifiedCatalog) *agentupdate.VerifiedCatalog {
	if source == nil {
		return nil
	}
	clone := *source
	clone.Manifest.Artifacts = append([]agentupdate.Artifact(nil), source.Manifest.Artifacts...)
	return &clone
}

func readFloor(path string) (persistedFloor, error) {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return persistedFloor{}, nil
	}
	if err != nil {
		return persistedFloor{}, fmt.Errorf("inspect agent update catalog state: %w", err)
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return persistedFloor{}, errors.New("agent update catalog state must be a regular file")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return persistedFloor{}, fmt.Errorf("read agent update catalog state: %w", err)
	}
	if len(data) > 4096 {
		return persistedFloor{}, errors.New("agent update catalog state exceeds size limit")
	}
	var floor persistedFloor
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&floor); err != nil {
		return persistedFloor{}, fmt.Errorf("decode agent update catalog state: %w", err)
	}
	if err := requireJSONEOF(decoder); err != nil {
		return persistedFloor{}, fmt.Errorf("decode agent update catalog state: %w", err)
	}
	if floor.SchemaVersion != stateSchemaVersion || floor.RootVersion == 0 || floor.Sequence == 0 || floor.SecurityEpoch == 0 || !versionRE.MatchString(floor.Version) || !digestRE.MatchString(floor.ManifestSHA256) {
		return persistedFloor{}, errors.New("agent update catalog state is invalid")
	}
	return floor, nil
}

func requireJSONEOF(decoder *json.Decoder) error {
	var trailing any
	if err := decoder.Decode(&trailing); errors.Is(err, io.EOF) {
		return nil
	} else if err != nil {
		return err
	}
	return errors.New("trailing JSON value")
}

func writeFloor(path string, floor persistedFloor) error {
	data, err := json.Marshal(floor)
	if err != nil {
		return err
	}
	directory := filepath.Dir(path)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(directory, ".agent-update-catalog-state-*")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0o600); err != nil {
		temporary.Close()
		return err
	}
	if _, err := temporary.Write(data); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return err
	}
	dir, err := os.Open(directory)
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}
