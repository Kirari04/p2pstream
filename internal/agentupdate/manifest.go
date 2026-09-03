// Package agentupdate defines and verifies the release metadata used to update
// p2pstream agents. GitHub is the trusted distribution boundary. The manifest
// still binds every artifact by exact name, size, and SHA-256 digest, and it
// deliberately cannot introduce download URLs or commands.
package agentupdate

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"
	"time"

	"p2pstream/internal/releaseversion"
)

const (
	SchemaVersion = 1

	MaxManifestBytes = 64 << 10
	MaxArtifacts     = 32
	MaxOCIImages     = 1
	MaxReleaseAssets = 64
	MaxArtifactSize  = 512 << 20
)

var (
	commitRE    = regexp.MustCompile(`^[0-9a-f]{40}$`)
	tokenRE     = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,31}$`)
	assetNameRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)
	digestRE    = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

type Manifest struct {
	SchemaVersion      uint64         `json:"schema_version"`
	Channel            string         `json:"channel"`
	Version            string         `json:"version"`
	Commit             string         `json:"commit"`
	Sequence           uint64         `json:"sequence"`
	PublishedAt        string         `json:"published_at"`
	ExpiresAt          string         `json:"expires_at"`
	MinimumSafeVersion string         `json:"minimum_safe_version"`
	SecurityEpoch      uint64         `json:"security_epoch"`
	Compatibility      Compatibility  `json:"compatibility"`
	Artifacts          []Artifact     `json:"artifacts"`
	OCIImages          []OCIImage     `json:"oci_images"`
	ReleaseAssets      []ReleaseAsset `json:"release_assets"`
}

type Compatibility struct {
	Server   VersionRange  `json:"server"`
	Protocol ProtocolRange `json:"protocol"`
	Updater  VersionRange  `json:"updater"`
}

type VersionRange struct {
	Min string `json:"min"`
	Max string `json:"max"`
}

type ProtocolRange struct {
	Min uint32 `json:"min"`
	Max uint32 `json:"max"`
}

type Artifact struct {
	OS     string `json:"os"`
	Arch   string `json:"arch"`
	Name   string `json:"name"`
	Size   uint64 `json:"size"`
	SHA256 string `json:"sha256"`
}

// OCIImage binds a release to an exact content-addressed multi-platform index.
// Repository deliberately excludes a tag.
type OCIImage struct {
	Repository string        `json:"repository"`
	Digest     string        `json:"digest"`
	MediaType  string        `json:"media_type"`
	Size       uint64        `json:"size"`
	Platforms  []OCIPlatform `json:"platforms"`
}

type OCIPlatform struct {
	OS        string `json:"os"`
	Arch      string `json:"arch"`
	Digest    string `json:"digest"`
	MediaType string `json:"media_type"`
	Size      uint64 `json:"size"`
}

// ReleaseAsset describes a release attachment other than the manifest itself.
// Executable artifacts and the OCI index are deliberately listed here too so
// the release's complete public asset inventory can be verified exactly.
type ReleaseAsset struct {
	Name   string `json:"name"`
	Size   uint64 `json:"size"`
	SHA256 string `json:"sha256"`
}

func ParseManifest(data []byte) (Manifest, error) {
	var manifest Manifest
	if err := parseCanonical(data, MaxManifestBytes, &manifest); err != nil {
		return Manifest{}, fmt.Errorf("parse manifest: %w", err)
	}
	if err := validateManifest(manifest); err != nil {
		return Manifest{}, fmt.Errorf("validate manifest: %w", err)
	}
	return manifest, nil
}

func CanonicalManifest(manifest Manifest) ([]byte, error) {
	if err := validateManifest(manifest); err != nil {
		return nil, err
	}
	return json.Marshal(manifest)
}

// ArtifactFor returns the single artifact for an exact GOOS/GOARCH pair.
func (m Manifest) ArtifactFor(goos, goarch string) (Artifact, error) {
	if !tokenRE.MatchString(goos) || !tokenRE.MatchString(goarch) {
		return Artifact{}, errors.New("invalid OS or architecture")
	}
	for _, artifact := range m.Artifacts {
		if artifact.OS == goos && artifact.Arch == goarch {
			return artifact, nil
		}
	}
	return Artifact{}, fmt.Errorf("no artifact for %s/%s", goos, goarch)
}

// OCIImageFor returns the OCI index for an exact repository.
func (m Manifest) OCIImageFor(repository string) (OCIImage, error) {
	for _, image := range m.OCIImages {
		if image.Repository == repository {
			return image, nil
		}
	}
	return OCIImage{}, fmt.Errorf("no OCI image for repository %q", repository)
}

func (m Manifest) ReleaseAssetFor(name string) (ReleaseAsset, error) {
	for _, asset := range m.ReleaseAssets {
		if asset.Name == name {
			return asset, nil
		}
	}
	return ReleaseAsset{}, fmt.Errorf("no release asset named %q", name)
}

func parseCanonical(data []byte, maxBytes int, dst any) error {
	if len(data) == 0 {
		return errors.New("empty input")
	}
	if len(data) > maxBytes {
		return fmt.Errorf("input exceeds %d-byte limit", maxBytes)
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(dst); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("multiple JSON values")
		}
		return err
	}
	canonical, err := json.Marshal(dst)
	if err != nil {
		return err
	}
	if !bytes.Equal(data, canonical) {
		return errors.New("input is not canonical JSON")
	}
	return nil
}

func validateManifest(manifest Manifest) error {
	if manifest.SchemaVersion != SchemaVersion {
		return errors.New("unsupported manifest schema version")
	}
	if manifest.Channel != "stable" && manifest.Channel != "staging" {
		return errors.New("unsupported manifest channel")
	}
	if err := validateChannelVersion(manifest.Version, manifest.Channel); err != nil {
		return fmt.Errorf("invalid version: %w", err)
	}
	if !commitRE.MatchString(manifest.Commit) {
		return errors.New("commit must be a lowercase 40-character SHA-1 hexadecimal object ID")
	}
	if manifest.Sequence == 0 {
		return errors.New("sequence must be positive")
	}
	publishedAt, err := parseTimestamp(manifest.PublishedAt)
	if err != nil {
		return fmt.Errorf("invalid publication time: %w", err)
	}
	expiresAt, err := parseTimestamp(manifest.ExpiresAt)
	if err != nil {
		return fmt.Errorf("invalid expiry: %w", err)
	}
	if !expiresAt.After(publishedAt) {
		return errors.New("expiry must be after publication time")
	}
	if err := validateStableVersion(manifest.MinimumSafeVersion); err != nil {
		return fmt.Errorf("invalid minimum safe version: %w", err)
	}
	if releaseversion.Compare(manifest.Version, manifest.MinimumSafeVersion) < 0 {
		return errors.New("version is below minimum safe version")
	}
	if manifest.SecurityEpoch == 0 {
		return errors.New("security epoch must be positive")
	}
	if err := validateVersionRange(manifest.Compatibility.Server); err != nil {
		return fmt.Errorf("invalid server compatibility: %w", err)
	}
	if err := validateVersionRange(manifest.Compatibility.Updater); err != nil {
		return fmt.Errorf("invalid updater compatibility: %w", err)
	}
	if manifest.Compatibility.Protocol.Min == 0 || manifest.Compatibility.Protocol.Max < manifest.Compatibility.Protocol.Min {
		return errors.New("invalid protocol compatibility")
	}
	if len(manifest.Artifacts) == 0 || len(manifest.Artifacts) > MaxArtifacts {
		return fmt.Errorf("manifest must contain 1..%d artifacts", MaxArtifacts)
	}
	previous := ""
	for _, artifact := range manifest.Artifacts {
		if !tokenRE.MatchString(artifact.OS) || !tokenRE.MatchString(artifact.Arch) {
			return errors.New("invalid artifact OS or architecture")
		}
		identity := artifact.OS + "/" + artifact.Arch
		if previous != "" && identity <= previous {
			return errors.New("artifacts are not uniquely sorted by OS/architecture")
		}
		previous = identity
		if !assetNameRE.MatchString(artifact.Name) || artifact.Name == "." || artifact.Name == ".." || strings.Contains(artifact.Name, "..") {
			return errors.New("invalid artifact name")
		}
		if strings.ContainsAny(artifact.Name, "/\\:") {
			return errors.New("artifact name must not contain a path or URL")
		}
		if artifact.Size == 0 || artifact.Size > MaxArtifactSize {
			return fmt.Errorf("artifact size must be 1..%d bytes", MaxArtifactSize)
		}
		if !digestRE.MatchString(artifact.SHA256) {
			return errors.New("artifact SHA-256 must be lowercase hexadecimal")
		}
	}
	if len(manifest.OCIImages) > MaxOCIImages {
		return fmt.Errorf("manifest must contain at most %d OCI images", MaxOCIImages)
	}
	previous = ""
	for _, image := range manifest.OCIImages {
		if previous != "" && image.Repository <= previous {
			return errors.New("OCI images are not uniquely sorted by repository")
		}
		previous = image.Repository
		if err := validateOCIImage(image); err != nil {
			return err
		}
	}
	if len(manifest.ReleaseAssets) > MaxReleaseAssets {
		return fmt.Errorf("manifest must contain at most %d release assets", MaxReleaseAssets)
	}
	previous = ""
	for _, asset := range manifest.ReleaseAssets {
		if previous != "" && asset.Name <= previous {
			return errors.New("release assets are not uniquely sorted by name")
		}
		previous = asset.Name
		if !assetNameRE.MatchString(asset.Name) || asset.Name == "." || asset.Name == ".." || strings.Contains(asset.Name, "..") || strings.ContainsAny(asset.Name, "/\\:") {
			return errors.New("invalid release asset name")
		}
		if asset.Size == 0 || asset.Size > MaxArtifactSize {
			return fmt.Errorf("release asset size must be 1..%d bytes", MaxArtifactSize)
		}
		if !digestRE.MatchString(asset.SHA256) {
			return errors.New("release asset SHA-256 must be lowercase hexadecimal")
		}
	}
	return nil
}

func validOCIDigest(digest string) bool {
	return strings.HasPrefix(digest, "sha256:") && digestRE.MatchString(strings.TrimPrefix(digest, "sha256:"))
}

func validateOCIRepository(repository string) error {
	if repository == "" || len(repository) > 255 || repository != strings.ToLower(repository) {
		return errors.New("OCI image repository must be a non-empty lowercase name")
	}
	if strings.ContainsAny(repository, "@?#\\") || strings.Contains(repository, "://") {
		return errors.New("OCI image repository must not contain a tag, digest, path escape, or URL")
	}
	parts := strings.Split(repository, "/")
	if len(parts) < 2 {
		return errors.New("OCI image repository must include a registry and path")
	}
	for _, part := range parts {
		if part == "" || part == "." || part == ".." || strings.HasPrefix(part, ".") || strings.HasSuffix(part, ".") {
			return errors.New("invalid OCI image repository component")
		}
		for _, character := range part {
			if (character < 'a' || character > 'z') && (character < '0' || character > '9') && character != '.' && character != '_' && character != '-' {
				return errors.New("invalid OCI image repository character")
			}
		}
	}
	return nil
}

func validateVersionRange(versionRange VersionRange) error {
	if err := validateStableVersion(versionRange.Min); err != nil {
		return err
	}
	if err := validateStableVersion(versionRange.Max); err != nil {
		return err
	}
	if releaseversion.Compare(versionRange.Min, versionRange.Max) > 0 {
		return errors.New("minimum version exceeds maximum version")
	}
	return nil
}

func validateStableVersion(version string) error {
	if !releaseversion.Stable(version) {
		return errors.New("version must be canonical vX.Y.Z")
	}
	return nil
}

func validateChannelVersion(version, channel string) error {
	if releaseversion.ValidForChannel(version, channel) {
		return nil
	}
	if channel == releaseversion.ChannelStaging {
		return errors.New("staging version must be a canonical SemVer prerelease")
	}
	return errors.New("stable version must be canonical vX.Y.Z")
}

func parseTimestamp(value string) (time.Time, error) {
	if len(value) != len("2006-01-02T15:04:05Z") || !strings.HasSuffix(value, "Z") {
		return time.Time{}, errors.New("timestamp must be UTC RFC3339 with second precision")
	}
	parsed, err := time.Parse(time.RFC3339, value)
	if err != nil {
		return time.Time{}, err
	}
	if parsed.UTC().Format(time.RFC3339) != value {
		return time.Time{}, errors.New("timestamp is not canonical")
	}
	return parsed, nil
}
