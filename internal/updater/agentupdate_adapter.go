package updater

import (
	"encoding/hex"
	"fmt"
	"io"
	"runtime"

	"p2pstream/internal/agentupdate"
)

// AgentUpdateVerifier is the production adapter to the release metadata
// package. It is stateless so the worker and activator can independently
// validate the metadata and artifact digest received through GitHub.
type AgentUpdateVerifier struct{}

func (AgentUpdateVerifier) Verify(manifestJSON []byte, policy VerifyPolicy) (VerifiedRelease, error) {
	verified, err := agentupdate.Verify(manifestJSON, agentupdate.VerifyPolicy{
		Now:                       policy.Now,
		RequiredChannel:           policy.RequiredChannel,
		CurrentSequence:           policy.CurrentSequence,
		CurrentSecurityEpoch:      policy.CurrentSecurityEpoch,
		CurrentMinimumSafeVersion: policy.CurrentMinimumSafeVersion,
		CurrentVersion:            policy.CurrentVersion,
		ServerVersion:             policy.ServerVersion,
		UpdaterVersion:            policy.UpdaterVersion,
		ProtocolVersion:           policy.ProtocolVersion,
		GOOS:                      runtime.GOOS,
		GOARCH:                    runtime.GOARCH,
	})
	if err != nil {
		return VerifiedRelease{}, err
	}
	digest, err := hex.DecodeString(verified.Artifact.SHA256)
	if err != nil || len(digest) != 32 {
		return VerifiedRelease{}, fmt.Errorf("decode verified artifact digest")
	}
	var sha [32]byte
	copy(sha[:], digest)
	if verified.Artifact.Size > uint64(^uint64(0)>>1) {
		return VerifiedRelease{}, fmt.Errorf("verified artifact is too large")
	}
	return VerifiedRelease{
		Version: verified.Version, Commit: verified.Commit, ManifestSHA256: verified.ManifestSHA256,
		Sequence: verified.Sequence, SecurityEpoch: verified.SecurityEpoch,
		MinimumSafeVersion: verified.MinimumSafeVersion,
		Artifact:           Artifact{Name: verified.Artifact.Name, Size: int64(verified.Artifact.Size), SHA256: sha},
	}, nil
}

func (AgentUpdateVerifier) VerifyArtifact(reader io.Reader, artifact Artifact) error {
	return agentupdate.VerifyArtifact(reader, agentupdate.Artifact{
		OS: runtime.GOOS, Arch: runtime.GOARCH, Name: artifact.Name,
		Size: uint64(artifact.Size), SHA256: artifactHex(artifact),
	})
}
