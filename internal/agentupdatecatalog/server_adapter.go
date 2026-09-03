package agentupdatecatalog

import (
	"context"
	"encoding/base64"
	"errors"
	"math"

	p2pstreamv1 "p2pstream/gen/proto/p2pstream/v1"
	"p2pstream/internal/agentupdate"
)

// AgentUpdateBootstrapConfig implements server.AgentUpdateBootstrapProvider.
// It returns canonical public trust metadata and a repository identifier, not
// a URL. Enrollment tokens bind their hashes and version server-side.
func (c *Catalog) AgentUpdateBootstrapConfig(context.Context) (string, string, error) {
	rootJSON, err := agentupdate.CanonicalRoot(c.options.Root)
	if err != nil {
		return "", "", err
	}
	return base64.StdEncoding.EncodeToString(rootJSON), c.options.Repository, nil
}

// ListTrustedAgentUpdateTargets implements server.TrustedAgentUpdateCatalog.
// Today the immutable stable catalog intentionally exposes one target: the
// latest signed release. Older releases remain usable only for the local
// activator's automatic rollback, never as a new management campaign.
func (c *Catalog) ListTrustedAgentUpdateTargets(ctx context.Context) ([]*p2pstreamv1.AgentUpdateTarget, error) {
	verified, err := c.Latest(ctx)
	if err != nil {
		return nil, err
	}
	target, err := targetFromVerified(verified)
	if err != nil {
		return nil, err
	}
	return []*p2pstreamv1.AgentUpdateTarget{target}, nil
}

// ResolveTrustedAgentUpdateTarget returns metadata only when the requested
// digest exactly matches the currently authenticated manifest.
func (c *Catalog) ResolveTrustedAgentUpdateTarget(ctx context.Context, manifestSHA256 string) (*p2pstreamv1.AgentUpdateTarget, error) {
	verified, err := c.Latest(ctx)
	if err != nil {
		return nil, err
	}
	if manifestSHA256 == "" || manifestSHA256 != verified.ManifestSHA256 {
		return nil, errors.New("agent update manifest is not in the trusted catalog")
	}
	return targetFromVerified(verified)
}

func targetFromVerified(verified *agentupdate.VerifiedCatalog) (*p2pstreamv1.AgentUpdateTarget, error) {
	if verified == nil || verified.Manifest.Sequence > math.MaxInt64 || verified.Manifest.SecurityEpoch > math.MaxInt64 || verified.Manifest.RootVersion > math.MaxInt64 {
		return nil, errors.New("verified agent update target exceeds protocol limits")
	}
	manifest := verified.Manifest
	target := &p2pstreamv1.AgentUpdateTarget{
		Version:               manifest.Version,
		Commit:                manifest.Commit,
		ManifestSha256:        verified.ManifestSHA256,
		ReleaseSequence:       int64(manifest.Sequence),
		MinimumTunnelProtocol: int64(manifest.Compatibility.Protocol.Min),
		MaximumTunnelProtocol: int64(manifest.Compatibility.Protocol.Max),
		SecurityEpoch:         int64(manifest.SecurityEpoch),
		MinimumUpdaterVersion: manifest.Compatibility.Updater.Min,
		RootVersion:           int64(manifest.RootVersion),
		Artifacts:             make([]*p2pstreamv1.AgentUpdateArtifact, 0, len(manifest.Artifacts)),
	}
	for _, artifact := range manifest.Artifacts {
		if artifact.Size > math.MaxInt64 {
			return nil, errors.New("verified agent update artifact exceeds protocol limits")
		}
		target.Artifacts = append(target.Artifacts, &p2pstreamv1.AgentUpdateArtifact{
			Os: artifact.OS, Arch: artifact.Arch, Name: artifact.Name,
			Sha256: artifact.SHA256, SizeBytes: int64(artifact.Size),
		})
	}
	return target, nil
}
