package agentupdate

import (
	"crypto/ed25519"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"time"

	"p2pstream/internal/releaseversion"
)

const allowedClockSkew = 5 * time.Minute

// VerifyPolicy contains locally trusted state. CurrentSequence,
// CurrentSecurityEpoch, CurrentMinimumSafeVersion, and MinimumRootVersion must
// be persisted after successful activation. Zero sequence/epoch/root floors are
// permitted only for bootstrap; they never disable the manifest's positive
// value checks. An empty RequiredChannel means stable.
type VerifyPolicy struct {
	Now                       time.Time
	RequiredChannel           string
	CurrentSequence           uint64
	CurrentSecurityEpoch      uint64
	CurrentMinimumSafeVersion string
	MinimumRootVersion        uint64
	CurrentVersion            string
	ServerVersion             string
	UpdaterVersion            string
	ProtocolVersion           uint32
	GOOS                      string
	GOARCH                    string
}

type VerifiedManifest struct {
	Manifest           Manifest
	Artifact           Artifact
	ManifestSHA256     string
	Version            string
	Commit             string
	Sequence           uint64
	SecurityEpoch      uint64
	MinimumSafeVersion string
	Compatibility      Compatibility
	PublishedAt        time.Time
	ExpiresAt          time.Time
}

// CatalogVerifyPolicy authenticates a release for server-side catalog use,
// before any particular agent platform or updater runtime is known. Empty
// RequiredChannel means stable. ServerVersion and ProtocolVersion may be empty
// ProtocolVersion may be zero only when the caller is intentionally cataloging
// protocol compatibility data for later per-agent enforcement. ServerVersion
// is always required and enforced.
type CatalogVerifyPolicy struct {
	Now                       time.Time
	RequiredChannel           string
	MinimumRootVersion        uint64
	CurrentSequence           uint64
	CurrentSecurityEpoch      uint64
	CurrentMinimumSafeVersion string
	ServerVersion             string
	ProtocolVersion           uint32
}

type VerifiedCatalog struct {
	Manifest       Manifest
	ManifestSHA256 string
	PublishedAt    time.Time
	ExpiresAt      time.Time
}

// VerifyCatalog authenticates the complete multi-platform manifest without
// selecting an artifact or requiring an installed agent/updater version.
func VerifyCatalog(manifestJSON, signatureJSON []byte, root RootMetadata, policy CatalogVerifyPolicy) (*VerifiedCatalog, error) {
	authenticated, err := authenticateManifest(manifestJSON, signatureJSON, root, policy.Now, policy.RequiredChannel, policy.MinimumRootVersion)
	if err != nil {
		return nil, err
	}
	manifest := authenticated.Manifest
	if policy.CurrentSequence != 0 && manifest.Sequence < policy.CurrentSequence {
		return nil, errors.New("manifest sequence is below the catalog sequence floor")
	}
	if manifest.SecurityEpoch < policy.CurrentSecurityEpoch {
		return nil, errors.New("manifest security epoch is below the catalog epoch floor")
	}
	if policy.CurrentMinimumSafeVersion != "" {
		if err := validateStableVersion(policy.CurrentMinimumSafeVersion); err != nil {
			return nil, fmt.Errorf("invalid catalog minimum safe version: %w", err)
		}
		if releaseversion.Compare(manifest.MinimumSafeVersion, policy.CurrentMinimumSafeVersion) < 0 {
			return nil, errors.New("manifest lowers the catalog minimum safe version")
		}
	}
	if err := requireVersionInRange("server", policy.ServerVersion, manifest.Compatibility.Server); err != nil {
		return nil, err
	}
	if policy.ProtocolVersion != 0 && (policy.ProtocolVersion < manifest.Compatibility.Protocol.Min || policy.ProtocolVersion > manifest.Compatibility.Protocol.Max) {
		return nil, errors.New("protocol version is outside manifest compatibility range")
	}
	return &VerifiedCatalog{
		Manifest:       manifest,
		ManifestSHA256: authenticated.ManifestSHA256,
		PublishedAt:    authenticated.PublishedAt,
		ExpiresAt:      authenticated.ExpiresAt,
	}, nil
}

// Verify authenticates canonical manifest bytes, enforces root and release
// freshness, applies all compatibility and downgrade floors, and selects the
// exact platform artifact. It never resolves a network location.
func Verify(manifestJSON, signatureJSON []byte, root RootMetadata, policy VerifyPolicy) (*VerifiedManifest, error) {
	authenticated, err := authenticateManifest(manifestJSON, signatureJSON, root, policy.Now, policy.RequiredChannel, policy.MinimumRootVersion)
	if err != nil {
		return nil, err
	}
	manifest := authenticated.Manifest
	if manifest.Sequence <= policy.CurrentSequence {
		return nil, errors.New("manifest sequence does not advance the persisted sequence")
	}
	if manifest.SecurityEpoch < policy.CurrentSecurityEpoch {
		return nil, errors.New("manifest security epoch is below the persisted epoch")
	}
	if policy.CurrentMinimumSafeVersion != "" {
		if err := validateStableVersion(policy.CurrentMinimumSafeVersion); err != nil {
			return nil, fmt.Errorf("invalid persisted minimum safe version: %w", err)
		}
		if releaseversion.Compare(manifest.MinimumSafeVersion, policy.CurrentMinimumSafeVersion) < 0 {
			return nil, errors.New("manifest lowers the persisted minimum safe version")
		}
	}
	if !releaseversion.Valid(policy.CurrentVersion) {
		return nil, errors.New("invalid current version: version must be canonical SemVer")
	}
	if releaseversion.Compare(manifest.Version, policy.CurrentVersion) <= 0 {
		return nil, errors.New("manifest version does not advance the installed version")
	}
	if err := requireVersionInRange("server", policy.ServerVersion, manifest.Compatibility.Server); err != nil {
		return nil, err
	}
	if err := requireVersionInRange("updater", policy.UpdaterVersion, manifest.Compatibility.Updater); err != nil {
		return nil, err
	}
	if policy.ProtocolVersion < manifest.Compatibility.Protocol.Min || policy.ProtocolVersion > manifest.Compatibility.Protocol.Max {
		return nil, errors.New("protocol version is outside manifest compatibility range")
	}
	artifact, err := manifest.ArtifactFor(policy.GOOS, policy.GOARCH)
	if err != nil {
		return nil, err
	}
	return &VerifiedManifest{
		Manifest:           manifest,
		Artifact:           artifact,
		ManifestSHA256:     authenticated.ManifestSHA256,
		Version:            manifest.Version,
		Commit:             manifest.Commit,
		Sequence:           manifest.Sequence,
		SecurityEpoch:      manifest.SecurityEpoch,
		MinimumSafeVersion: manifest.MinimumSafeVersion,
		Compatibility:      manifest.Compatibility,
		PublishedAt:        authenticated.PublishedAt,
		ExpiresAt:          authenticated.ExpiresAt,
	}, nil
}

func authenticateManifest(manifestJSON, signatureJSON []byte, root RootMetadata, now time.Time, requiredChannel string, minimumRootVersion uint64) (*VerifiedCatalog, error) {
	if err := validateRoot(root); err != nil {
		return nil, fmt.Errorf("invalid trusted root: %w", err)
	}
	if now.IsZero() {
		return nil, errors.New("verification time is required")
	}
	if requiredChannel == "" {
		requiredChannel = "stable"
	}
	if requiredChannel != "stable" && requiredChannel != "staging" {
		return nil, errors.New("unsupported required channel")
	}
	if root.Version < minimumRootVersion {
		return nil, errors.New("trusted root is below the persisted root version floor")
	}
	rootExpiry, _ := parseTimestamp(root.ExpiresAt)
	if !now.Before(rootExpiry) {
		return nil, errors.New("trusted root metadata is expired")
	}
	manifest, err := ParseManifest(manifestJSON)
	if err != nil {
		return nil, err
	}
	envelope, err := ParseSignatures(signatureJSON)
	if err != nil {
		return nil, err
	}
	if manifest.RootVersion != root.Version {
		return nil, errors.New("manifest root version does not match trusted root")
	}
	if manifest.Channel != requiredChannel {
		return nil, fmt.Errorf("manifest channel %q is not trusted for %q updates", manifest.Channel, requiredChannel)
	}
	if err := verifyThreshold(signedPayload(manifestJSON), envelope, root); err != nil {
		return nil, err
	}
	publishedAt, _ := parseTimestamp(manifest.PublishedAt)
	expiresAt, _ := parseTimestamp(manifest.ExpiresAt)
	if publishedAt.After(now.Add(allowedClockSkew)) {
		return nil, errors.New("manifest publication time is in the future")
	}
	if !now.Before(expiresAt) {
		return nil, errors.New("manifest is expired")
	}
	if expiresAt.After(rootExpiry) {
		return nil, errors.New("manifest expires after its trusted root")
	}
	manifestDigest := sha256.Sum256(manifestJSON)
	return &VerifiedCatalog{
		Manifest:       manifest,
		ManifestSHA256: hex.EncodeToString(manifestDigest[:]),
		PublishedAt:    publishedAt,
		ExpiresAt:      expiresAt,
	}, nil
}

func verifyThreshold(payload []byte, envelope SignatureEnvelope, root RootMetadata) error {
	rootKeys := make(map[string]ed25519.PublicKey, len(root.Keys))
	for _, key := range root.Keys {
		decoded, _ := decodeCanonicalBase64(key.PublicKey, ed25519.PublicKeySize)
		rootKeys[key.ID] = ed25519.PublicKey(decoded)
	}
	validSignatures := uint32(0)
	for _, signature := range envelope.Signatures {
		publicKey, ok := rootKeys[signature.KeyID]
		if !ok {
			return fmt.Errorf("signature key %s is not in trusted root", signature.KeyID)
		}
		decoded, _ := decodeCanonicalBase64(signature.Signature, ed25519.SignatureSize)
		if !ed25519.Verify(publicKey, payload, decoded) {
			return fmt.Errorf("invalid signature from key %s", signature.KeyID)
		}
		validSignatures++
	}
	if validSignatures < root.Threshold {
		return fmt.Errorf("valid signature threshold not met: got %d, need %d", validSignatures, root.Threshold)
	}
	return nil
}

func requireVersionInRange(name, version string, versionRange VersionRange) error {
	if !releaseversion.Valid(version) {
		return fmt.Errorf("invalid local %s version: version must be canonical SemVer", name)
	}
	if releaseversion.Compare(version, versionRange.Min) < 0 || releaseversion.Compare(version, versionRange.Max) > 0 {
		return fmt.Errorf("%s version is outside manifest compatibility range", name)
	}
	return nil
}

// VerifyArtifact streams a downloaded raw binary through SHA-256 and requires
// both its byte count and digest to exactly match the verified manifest entry.
func VerifyArtifact(reader io.Reader, artifact Artifact) error {
	if artifact.Size == 0 || artifact.Size > MaxArtifactSize || !digestRE.MatchString(artifact.SHA256) {
		return errors.New("invalid artifact metadata")
	}
	hasher := sha256.New()
	written, err := io.CopyN(hasher, reader, int64(artifact.Size)+1)
	if err != nil && !errors.Is(err, io.EOF) {
		return fmt.Errorf("read artifact: %w", err)
	}
	if written != int64(artifact.Size) {
		return fmt.Errorf("artifact size mismatch: got %d, want %d", written, artifact.Size)
	}
	want, _ := hex.DecodeString(artifact.SHA256)
	if subtle.ConstantTimeCompare(hasher.Sum(nil), want) != 1 {
		return errors.New("artifact SHA-256 mismatch")
	}
	return nil
}

// VerifyReleaseAsset streams one stable release attachment through SHA-256.
func VerifyReleaseAsset(reader io.Reader, asset ReleaseAsset) error {
	if asset.Size == 0 || asset.Size > MaxArtifactSize || !digestRE.MatchString(asset.SHA256) {
		return errors.New("invalid release asset metadata")
	}
	hasher := sha256.New()
	written, err := io.CopyN(hasher, reader, int64(asset.Size)+1)
	if err != nil && !errors.Is(err, io.EOF) {
		return fmt.Errorf("read release asset: %w", err)
	}
	if written != int64(asset.Size) {
		return fmt.Errorf("release asset size mismatch: got %d, want %d", written, asset.Size)
	}
	want, _ := hex.DecodeString(asset.SHA256)
	if subtle.ConstantTimeCompare(hasher.Sum(nil), want) != 1 {
		return errors.New("release asset SHA-256 mismatch")
	}
	return nil
}
