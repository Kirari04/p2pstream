package updater

import (
	"context"
	"io"
	"time"
)

const (
	DefaultConfigPath     = "/etc/p2pstream-updater/updater.json"
	DefaultStateDir       = "/var/lib/p2pstream-updater"
	DefaultInstallRoot    = "/opt/p2pstream-agent"
	DefaultCommandPath    = "/usr/local/bin/p2pstream"
	DefaultAgentUnit      = "p2pstream-agent.service"
	DefaultUpdaterUser    = "p2pstream-updater"
	DefaultUpdaterGroup   = "p2pstream-updater"
	defaultMaxMetadata    = 1 << 20
	defaultMaxArtifact    = 512 << 20
	defaultDiskHeadroom   = 32 << 20
	defaultHealthTimeout  = 45 * time.Second
	defaultRequestTimeout = 2 * time.Minute
)

// Paths are fixed in production. Tests construct a private tree explicitly;
// the updater CLI does not accept path flags or positional path arguments.
type Paths struct {
	ConfigPath  string
	StateDir    string
	InstallRoot string
	CommandPath string
}

func DefaultPaths() Paths {
	return Paths{
		ConfigPath:  DefaultConfigPath,
		StateDir:    DefaultStateDir,
		InstallRoot: DefaultInstallRoot,
		CommandPath: DefaultCommandPath,
	}
}

type VerifyPolicy struct {
	Now                       time.Time
	CurrentSequence           uint64
	CurrentSecurityEpoch      uint64
	CurrentMinimumSafeVersion string
	CurrentVersion            string
	ServerVersion             string
	UpdaterVersion            string
	ProtocolVersion           uint32
	RequiredChannel           string
}

type Artifact struct {
	Name   string
	Size   int64
	SHA256 [32]byte
}

type VerifiedRelease struct {
	Version            string
	Commit             string
	ManifestSHA256     string
	Sequence           uint64
	SecurityEpoch      uint64
	MinimumSafeVersion string
	Artifact           Artifact
}

type Assignment struct {
	AgentPublicID string
	AssignmentID  int64
	Generation    int64
	Nonce         []byte
}

// Verifier is deliberately metadata-only. Artifact integrity is bound by the
// manifest size and SHA-256 and checked by both Stage and Activate.
type Verifier interface {
	Verify(manifestJSON []byte, policy VerifyPolicy) (VerifiedRelease, error)
	VerifyArtifact(io.Reader, Artifact) error
}

// Source is the narrow backend boundary. The server does not choose local
// paths, commands, or activation arguments.
type Source interface {
	FetchMetadata(context.Context) (manifestJSON []byte, err error)
	FetchArtifact(context.Context, Artifact) (io.ReadCloser, error)
}

type ServiceController interface {
	Restart(context.Context) error
	Healthy(context.Context) error
}

type Floor struct {
	Sequence           uint64 `json:"sequence"`
	SecurityEpoch      uint64 `json:"security_epoch"`
	MinimumSafeVersion string `json:"minimum_safe_version"`
	Version            string `json:"version"`
}

type Result struct {
	Version       string
	Sequence      uint64
	SecurityEpoch uint64
	Changed       bool
}
