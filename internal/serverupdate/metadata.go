// Package serverupdate implements the independently supervised server updater.
// Its inputs select release identities, never host commands or deployment paths.
package serverupdate

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"time"

	"p2pstream/internal/agentupdate"
	"p2pstream/internal/releaseversion"
)

const (
	API           = 1
	Schema        = 20
	MetadataAsset = "p2pstream_server_update.json"
	RuntimeSocket = "/tmp/p2pstream-server-status.sock"
)

var commitPattern = regexp.MustCompile(`^[0-9a-f]{40}$`)
var digestPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)
var stagingPattern = regexp.MustCompile(`^v[0-9]+\.[0-9]+\.[0-9]+-staging\.[1-9][0-9]*$`)

// Metadata is a separate attachment, hashed by the existing agent manifest.
// Keeping it separate preserves strict, older agent manifest readers.
type Metadata struct {
	API              int    `json:"api"`
	Version          string `json:"version"`
	Commit           string `json:"commit"`
	SourceSchemaMin  int    `json:"source_schema_min"`
	SourceSchemaMax  int    `json:"source_schema_max"`
	TargetSchema     int    `json:"target_schema"`
	RuntimeAPIMin    int    `json:"runtime_api_min"`
	AgentProtocolMin int    `json:"agent_protocol_min"`
	AgentProtocolMax int    `json:"agent_protocol_max"`
	Rollback         string `json:"rollback"`
}

func NewMetadata(version, commit string) Metadata {
	return Metadata{API: API, Version: version, Commit: commit, SourceSchemaMin: 17,
		SourceSchemaMax: Schema, TargetSchema: Schema, RuntimeAPIMin: API, AgentProtocolMin: 1, AgentProtocolMax: 1, Rollback: "snapshot"}
}

func (m Metadata) Validate() error {
	if m.API != API || !releaseversion.Valid(m.Version) || !commitPattern.MatchString(m.Commit) ||
		m.SourceSchemaMin < 1 || m.SourceSchemaMax < m.SourceSchemaMin || m.TargetSchema < m.SourceSchemaMax ||
		m.RuntimeAPIMin != API || m.AgentProtocolMin != 1 || m.AgentProtocolMax != 1 || m.Rollback != "snapshot" {
		return errors.New("unsupported server update metadata")
	}
	return nil
}

type Release struct {
	Version            string    `json:"version"`
	Commit             string    `json:"commit"`
	Channel            string    `json:"channel"`
	ManifestSHA256     string    `json:"manifest_sha256"`
	Image              string    `json:"image"`
	Sequence           uint64    `json:"sequence"`
	SecurityEpoch      uint64    `json:"security_epoch"`
	MinimumSafeVersion string    `json:"minimum_safe_version"`
	ExpiresAt          time.Time `json:"expires_at"`
	Metadata           Metadata  `json:"metadata"`
}

type Floor struct {
	Sequence           uint64 `json:"sequence"`
	SecurityEpoch      uint64 `json:"security_epoch"`
	MinimumSafeVersion string `json:"minimum_safe_version"`
	ManifestSHA256     string `json:"manifest_sha256"`
}

func VerifyRelease(manifest, metadata []byte, repository, channel, version, arch string, current RuntimeStatus, floor Floor, now time.Time) (Release, error) {
	return verifyRelease(manifest, metadata, repository, channel, version, arch, current, floor, now, false)
}

func verifyRelease(manifest, metadata []byte, repository, channel, version, arch string, current RuntimeStatus, floor Floor, now time.Time, installed bool) (Release, error) {
	if !validVersion(version, channel) {
		return Release{}, errors.New("invalid release channel or version")
	}
	v, err := agentupdate.VerifyCatalog(manifest, agentupdate.CatalogVerifyPolicy{
		Now: now, RequiredChannel: channel, ServerVersion: current.Version, ProtocolVersion: 1,
		CurrentSequence: floor.Sequence, CurrentSecurityEpoch: floor.SecurityEpoch, CurrentMinimumSafeVersion: floor.MinimumSafeVersion,
	})
	if err != nil {
		return Release{}, err
	}
	if v.Manifest.Version != version || (floor.Sequence == v.Manifest.Sequence && floor.ManifestSHA256 != "" && floor.ManifestSHA256 != v.ManifestSHA256) {
		return Release{}, errors.New("release identity changed")
	}
	asset, err := v.Manifest.ReleaseAssetFor(MetadataAsset)
	if err != nil {
		return Release{}, errors.New("release does not support managed server updates")
	}
	if uint64(len(metadata)) != asset.Size || hash(metadata) != asset.SHA256 {
		return Release{}, errors.New("server metadata digest mismatch")
	}
	var meta Metadata
	if err := decode(metadata, &meta); err != nil {
		return Release{}, err
	}
	canonical, _ := json.Marshal(meta)
	if !bytes.Equal(canonical, metadata) {
		return Release{}, errors.New("server metadata is not canonical")
	}
	if err := meta.Validate(); err != nil {
		return Release{}, err
	}
	if meta.Version != version || meta.Commit != v.Manifest.Commit {
		return Release{}, errors.New("server metadata identity mismatch")
	}
	if current.API < meta.RuntimeAPIMin || current.Schema < meta.SourceSchemaMin || current.Schema > meta.SourceSchemaMax {
		return Release{}, errors.New("server API or database schema is outside the supported upgrade range")
	}
	if comparison := releaseversion.Compare(version, current.Version); comparison < 0 || comparison == 0 && !installed {
		return Release{}, errors.New("target must advance the installed server version")
	}
	// A one-click operation must have a permitted last-good recovery target.
	if releaseversion.Compare(current.Version, v.Manifest.MinimumSafeVersion) < 0 {
		return Release{}, errors.New("previous server is below the release recovery floor; a manual upgrade is required")
	}
	image, err := v.Manifest.OCIImageFor(repository)
	if err != nil {
		return Release{}, err
	}
	found := false
	for _, platform := range image.Platforms {
		if platform.OS == "linux" && platform.Arch == arch {
			found = true
		}
	}
	if !found {
		return Release{}, fmt.Errorf("release has no linux/%s image", arch)
	}
	return Release{Version: version, Commit: v.Manifest.Commit, Channel: channel, ManifestSHA256: v.ManifestSHA256,
		Image: image.Repository + "@" + image.Digest, Sequence: v.Manifest.Sequence, SecurityEpoch: v.Manifest.SecurityEpoch,
		MinimumSafeVersion: v.Manifest.MinimumSafeVersion, ExpiresAt: v.ExpiresAt, Metadata: meta}, nil
}

func validVersion(version, channel string) bool {
	return releaseversion.ValidForChannel(version, channel) && (channel != "staging" || stagingPattern.MatchString(version))
}

func hash(data []byte) string { sum := sha256.Sum256(data); return hex.EncodeToString(sum[:]) }

func decode(data []byte, target any) error {
	if len(data) > 2<<20 {
		return errors.New("update message exceeds size limit")
	}
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if err := d.Decode(target); err != nil {
		return err
	}
	if err := d.Decode(new(any)); err != io.EOF {
		return errors.New("trailing update message data")
	}
	return nil
}

func (r Release) VerificationFloor() Floor {
	return Floor{Sequence: r.Sequence, SecurityEpoch: r.SecurityEpoch, MinimumSafeVersion: r.MinimumSafeVersion, ManifestSHA256: r.ManifestSHA256}
}
