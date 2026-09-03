// Package agentupdate defines and verifies the signed metadata used to update
// p2pstream agents. It deliberately does not resolve or accept download URLs;
// callers construct a release URL from their own pinned repository and the
// verified version and artifact name.
package agentupdate

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"slices"
	"strings"
	"time"

	"p2pstream/internal/releaseversion"
)

const (
	SchemaVersion = 1

	MaxManifestBytes          = 64 << 10
	MaxRootMetadataBytes      = 32 << 10
	MaxSignatureEnvelopeBytes = 16 << 10
	MaxArtifacts              = 32
	MaxOCIImages              = 1
	MaxReleaseAssets          = 64
	MaxRootKeys               = 16
	MaxSignatures             = 16
	MaxArtifactSize           = 512 << 20
)

const signatureDomain = "p2pstream-agent-update-manifest-v1\x00"

var (
	commitRE    = regexp.MustCompile(`^[0-9a-f]{40}$`)
	tokenRE     = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,31}$`)
	assetNameRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)
	keyIDRE     = regexp.MustCompile(`^[0-9a-f]{64}$`)
	digestRE    = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

// RootMetadata is an out-of-band trust anchor. Version lets callers enforce a
// persistent root floor; Threshold permits offline keys and multi-party
// signing without changing the manifest format.
type RootMetadata struct {
	SchemaVersion uint64    `json:"schema_version"`
	Version       uint64    `json:"version"`
	ExpiresAt     string    `json:"expires_at"`
	Threshold     uint32    `json:"threshold"`
	Keys          []RootKey `json:"keys"`
}

type RootKey struct {
	ID        string `json:"id"`
	PublicKey string `json:"public_key"`
}

type Manifest struct {
	SchemaVersion      uint64         `json:"schema_version"`
	Channel            string         `json:"channel"`
	RootVersion        uint64         `json:"root_version"`
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
// Repository deliberately excludes a tag: release tooling may create friendly
// tags only after the signed digest set has met the offline threshold.
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

// ReleaseAsset authenticates a stable release attachment that is not the
// manifest itself or its signature envelope. Executable artifacts and the OCI
// index are deliberately listed here too so the release's complete public
// asset inventory can be verified through one exact set comparison.
type ReleaseAsset struct {
	Name   string `json:"name"`
	Size   uint64 `json:"size"`
	SHA256 string `json:"sha256"`
}

type SignatureEnvelope struct {
	SchemaVersion uint64      `json:"schema_version"`
	Signatures    []Signature `json:"signatures"`
}

type Signature struct {
	KeyID     string `json:"key_id"`
	Signature string `json:"signature"`
}

func ParseRoot(data []byte) (RootMetadata, error) {
	var root RootMetadata
	if err := parseCanonical(data, MaxRootMetadataBytes, &root); err != nil {
		return RootMetadata{}, fmt.Errorf("parse root metadata: %w", err)
	}
	if err := validateRoot(root); err != nil {
		return RootMetadata{}, fmt.Errorf("validate root metadata: %w", err)
	}
	return root, nil
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

func ParseSignatures(data []byte) (SignatureEnvelope, error) {
	var envelope SignatureEnvelope
	if err := parseCanonical(data, MaxSignatureEnvelopeBytes, &envelope); err != nil {
		return SignatureEnvelope{}, fmt.Errorf("parse signatures: %w", err)
	}
	if err := validateSignatures(envelope); err != nil {
		return SignatureEnvelope{}, fmt.Errorf("validate signatures: %w", err)
	}
	return envelope, nil
}

func CanonicalRoot(root RootMetadata) ([]byte, error) {
	if err := validateRoot(root); err != nil {
		return nil, err
	}
	return json.Marshal(root)
}

func CanonicalManifest(manifest Manifest) ([]byte, error) {
	if err := validateManifest(manifest); err != nil {
		return nil, err
	}
	return json.Marshal(manifest)
}

func CanonicalSignatures(envelope SignatureEnvelope) ([]byte, error) {
	if err := validateSignatures(envelope); err != nil {
		return nil, err
	}
	return json.Marshal(envelope)
}

// NewRootMetadata derives canonical key IDs from Ed25519 public keys.
func NewRootMetadata(version uint64, expiresAt string, threshold uint32, publicKeys []ed25519.PublicKey) (RootMetadata, error) {
	root := RootMetadata{
		SchemaVersion: SchemaVersion,
		Version:       version,
		ExpiresAt:     expiresAt,
		Threshold:     threshold,
		Keys:          make([]RootKey, 0, len(publicKeys)),
	}
	for _, key := range publicKeys {
		if len(key) != ed25519.PublicKeySize {
			return RootMetadata{}, errors.New("invalid Ed25519 public key length")
		}
		root.Keys = append(root.Keys, RootKey{
			ID:        KeyID(key),
			PublicKey: base64.StdEncoding.EncodeToString(key),
		})
	}
	slices.SortFunc(root.Keys, func(a, b RootKey) int { return strings.Compare(a.ID, b.ID) })
	if err := validateRoot(root); err != nil {
		return RootMetadata{}, err
	}
	return root, nil
}

func KeyID(publicKey ed25519.PublicKey) string {
	digest := sha256.Sum256(publicKey)
	return hex.EncodeToString(digest[:])
}

// SignManifest signs already-canonical manifest bytes. Each private key must
// be present in root; the returned signatures are sorted by key ID.
func SignManifest(manifestJSON []byte, root RootMetadata, privateKeys []ed25519.PrivateKey) (SignatureEnvelope, error) {
	envelope, err := signManifest(manifestJSON, root, privateKeys)
	if err != nil {
		return SignatureEnvelope{}, err
	}
	if uint32(len(envelope.Signatures)) < root.Threshold {
		return SignatureEnvelope{}, errors.New("not enough signing keys to satisfy root threshold")
	}
	return envelope, nil
}

// SignManifestPartial creates a canonical signature contribution from one or
// more independently held root keys. It deliberately does not require the root
// threshold: release tooling must combine independently produced contributions
// with MergeManifestSignatures before publication. This keeps a threshold key
// quorum out of any single process or CI trust domain.
func SignManifestPartial(manifestJSON []byte, root RootMetadata, privateKeys []ed25519.PrivateKey) (SignatureEnvelope, error) {
	return signManifest(manifestJSON, root, privateKeys)
}

func signManifest(manifestJSON []byte, root RootMetadata, privateKeys []ed25519.PrivateKey) (SignatureEnvelope, error) {
	manifest, err := ParseManifest(manifestJSON)
	if err != nil {
		return SignatureEnvelope{}, err
	}
	if err := validateRoot(root); err != nil {
		return SignatureEnvelope{}, err
	}
	if manifest.RootVersion != root.Version {
		return SignatureEnvelope{}, errors.New("manifest root version does not match root metadata")
	}
	manifestExpiry, _ := parseTimestamp(manifest.ExpiresAt)
	rootExpiry, _ := parseTimestamp(root.ExpiresAt)
	if manifestExpiry.After(rootExpiry) {
		return SignatureEnvelope{}, errors.New("manifest expires after its root metadata")
	}
	allowed := make(map[string]struct{}, len(root.Keys))
	for _, key := range root.Keys {
		allowed[key.ID] = struct{}{}
	}
	payload := signedPayload(manifestJSON)
	envelope := SignatureEnvelope{SchemaVersion: SchemaVersion}
	seen := make(map[string]struct{}, len(privateKeys))
	for _, privateKey := range privateKeys {
		if len(privateKey) != ed25519.PrivateKeySize {
			return SignatureEnvelope{}, errors.New("invalid Ed25519 private key length")
		}
		if _, err := EncodePrivateKey(privateKey); err != nil {
			return SignatureEnvelope{}, err
		}
		publicKey := privateKey.Public().(ed25519.PublicKey)
		id := KeyID(publicKey)
		if _, ok := allowed[id]; !ok {
			return SignatureEnvelope{}, fmt.Errorf("signing key %s is not in root metadata", id)
		}
		if _, ok := seen[id]; ok {
			return SignatureEnvelope{}, fmt.Errorf("duplicate signing key %s", id)
		}
		seen[id] = struct{}{}
		envelope.Signatures = append(envelope.Signatures, Signature{
			KeyID:     id,
			Signature: base64.StdEncoding.EncodeToString(ed25519.Sign(privateKey, payload)),
		})
	}
	slices.SortFunc(envelope.Signatures, func(a, b Signature) int { return strings.Compare(a.KeyID, b.KeyID) })
	if err := validateSignatures(envelope); err != nil {
		return SignatureEnvelope{}, err
	}
	return envelope, nil
}

// MergeManifestSignatures verifies, de-duplicates, and canonicalizes signature
// contributions for the exact manifest. It succeeds only after the pinned root
// threshold is met, so the resulting envelope is safe to publish directly.
func MergeManifestSignatures(manifestJSON []byte, root RootMetadata, contributions ...SignatureEnvelope) (SignatureEnvelope, error) {
	manifest, err := ParseManifest(manifestJSON)
	if err != nil {
		return SignatureEnvelope{}, err
	}
	if err := validateRoot(root); err != nil {
		return SignatureEnvelope{}, err
	}
	if manifest.RootVersion != root.Version {
		return SignatureEnvelope{}, errors.New("manifest root version does not match root metadata")
	}
	manifestExpiry, _ := parseTimestamp(manifest.ExpiresAt)
	rootExpiry, _ := parseTimestamp(root.ExpiresAt)
	if manifestExpiry.After(rootExpiry) {
		return SignatureEnvelope{}, errors.New("manifest expires after its root metadata")
	}
	if len(contributions) == 0 || len(contributions) > MaxSignatures {
		return SignatureEnvelope{}, fmt.Errorf("expected 1..%d signature contributions", MaxSignatures)
	}
	allowed := make(map[string]ed25519.PublicKey, len(root.Keys))
	for _, key := range root.Keys {
		decoded, _ := decodeCanonicalBase64(key.PublicKey, ed25519.PublicKeySize)
		allowed[key.ID] = ed25519.PublicKey(decoded)
	}
	payload := signedPayload(manifestJSON)
	merged := SignatureEnvelope{SchemaVersion: SchemaVersion}
	seen := make(map[string]struct{}, len(contributions))
	for _, contribution := range contributions {
		if err := validateSignatures(contribution); err != nil {
			return SignatureEnvelope{}, fmt.Errorf("invalid signature contribution: %w", err)
		}
		for _, signature := range contribution.Signatures {
			publicKey, ok := allowed[signature.KeyID]
			if !ok {
				return SignatureEnvelope{}, fmt.Errorf("signature key %s is not in trusted root", signature.KeyID)
			}
			if _, duplicate := seen[signature.KeyID]; duplicate {
				return SignatureEnvelope{}, fmt.Errorf("duplicate signature key %s", signature.KeyID)
			}
			decoded, _ := decodeCanonicalBase64(signature.Signature, ed25519.SignatureSize)
			if !ed25519.Verify(publicKey, payload, decoded) {
				return SignatureEnvelope{}, fmt.Errorf("invalid signature from key %s", signature.KeyID)
			}
			seen[signature.KeyID] = struct{}{}
			merged.Signatures = append(merged.Signatures, signature)
		}
	}
	slices.SortFunc(merged.Signatures, func(a, b Signature) int { return strings.Compare(a.KeyID, b.KeyID) })
	if uint32(len(merged.Signatures)) < root.Threshold {
		return SignatureEnvelope{}, fmt.Errorf("valid signature threshold not met: got %d, need %d", len(merged.Signatures), root.Threshold)
	}
	if err := validateSignatures(merged); err != nil {
		return SignatureEnvelope{}, err
	}
	return merged, nil
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

// OCIImageFor returns the signed OCI index for an exact repository.
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
	return ReleaseAsset{}, fmt.Errorf("no signed release asset named %q", name)
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

func validateRoot(root RootMetadata) error {
	if root.SchemaVersion != SchemaVersion {
		return errors.New("unsupported root schema version")
	}
	if root.Version == 0 {
		return errors.New("root version must be positive")
	}
	if _, err := parseTimestamp(root.ExpiresAt); err != nil {
		return fmt.Errorf("invalid root expiry: %w", err)
	}
	if len(root.Keys) == 0 || len(root.Keys) > MaxRootKeys {
		return fmt.Errorf("root must contain 1..%d keys", MaxRootKeys)
	}
	if root.Threshold == 0 || int(root.Threshold) > len(root.Keys) {
		return errors.New("root threshold is outside key set")
	}
	previous := ""
	for _, key := range root.Keys {
		if !keyIDRE.MatchString(key.ID) {
			return errors.New("invalid root key ID")
		}
		if previous != "" && key.ID <= previous {
			return errors.New("root keys are not uniquely sorted by ID")
		}
		previous = key.ID
		publicKey, err := decodeCanonicalBase64(key.PublicKey, ed25519.PublicKeySize)
		if err != nil {
			return fmt.Errorf("invalid root public key: %w", err)
		}
		if KeyID(ed25519.PublicKey(publicKey)) != key.ID {
			return errors.New("root key ID does not match public key")
		}
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
	if manifest.RootVersion == 0 {
		return errors.New("root version must be positive")
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

func validateSignatures(envelope SignatureEnvelope) error {
	if envelope.SchemaVersion != SchemaVersion {
		return errors.New("unsupported signature schema version")
	}
	if len(envelope.Signatures) == 0 || len(envelope.Signatures) > MaxSignatures {
		return fmt.Errorf("signature envelope must contain 1..%d signatures", MaxSignatures)
	}
	previous := ""
	for _, signature := range envelope.Signatures {
		if !keyIDRE.MatchString(signature.KeyID) {
			return errors.New("invalid signature key ID")
		}
		if previous != "" && signature.KeyID <= previous {
			return errors.New("signatures are not uniquely sorted by key ID")
		}
		previous = signature.KeyID
		if _, err := decodeCanonicalBase64(signature.Signature, ed25519.SignatureSize); err != nil {
			return fmt.Errorf("invalid signature: %w", err)
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

func decodeCanonicalBase64(value string, expectedBytes int) ([]byte, error) {
	decoded, err := base64.StdEncoding.DecodeString(value)
	if err != nil {
		return nil, err
	}
	if len(decoded) != expectedBytes || base64.StdEncoding.EncodeToString(decoded) != value {
		return nil, errors.New("invalid length or non-canonical base64")
	}
	return decoded, nil
}

func signedPayload(manifestJSON []byte) []byte {
	payload := make([]byte, 0, len(signatureDomain)+len(manifestJSON))
	payload = append(payload, signatureDomain...)
	payload = append(payload, manifestJSON...)
	return payload
}
