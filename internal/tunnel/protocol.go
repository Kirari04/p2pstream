package tunnel

import (
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
)

const (
	ProtocolVersion = 1

	BootstrapPath                    = "/agent/tunnel"
	UpgradeToken                     = "p2pstream-yamux"
	TunnelVersionHeader              = "X-P2PStream-Tunnel-Version"
	TunnelMaxConcurrentStreamsHeader = "X-P2PStream-Tunnel-Max-Concurrent-Streams"
	TunnelCapacityModeHeader         = "X-P2PStream-Tunnel-Capacity-Mode"
	TunnelAgentVersionHeader         = "X-P2PStream-Agent-Version"
	TunnelAgentCommitHeader          = "X-P2PStream-Agent-Commit"
	TunnelGroupHeader                = "X-P2PStream-Tunnel-Group"
	TunnelLaneHeader                 = "X-P2PStream-Tunnel-Lane"
	DefaultParallelTunnels           = 4
	MaxParallelTunnels               = 32
	TunnelCapacityModeAdaptive       = "adaptive"
	TunnelCapacityModeFixed          = "fixed"

	MaxControlFrameBytes = 16 * 1024
)

// The extension is acknowledged before an agent starts additional connections.
// Old peers therefore retain their single authenticated tunnel unchanged.
func ParseTunnelLane(group, rawLane string) (string, int, error) {
	if group == "" && rawLane == "" {
		return "", 0, nil
	}
	if len(group) < 16 || len(group) > 64 {
		return "", 0, errors.New("invalid tunnel group")
	}
	for _, ch := range group {
		if !(ch >= 'a' && ch <= 'z' || ch >= 'A' && ch <= 'Z' || ch >= '0' && ch <= '9' || ch == '-') {
			return "", 0, errors.New("invalid tunnel group")
		}
	}
	lane, err := strconv.Atoi(rawLane)
	if err != nil || lane < 0 || lane >= MaxParallelTunnels {
		return "", 0, errors.New("invalid tunnel lane")
	}
	return group, lane, nil
}

func ParseCapacityMode(value string) (string, error) {
	value = strings.ToLower(strings.TrimSpace(value))
	switch value {
	case TunnelCapacityModeAdaptive, TunnelCapacityModeFixed:
		return value, nil
	default:
		return "", fmt.Errorf("tunnel capacity mode is required and must be fixed or adaptive: %q", value)
	}
}

// ParseMaxConcurrentStreams validates the required HTTP upgrade capacity header.
func ParseMaxConcurrentStreams(value string, maximum int64) (int64, error) {
	value = strings.TrimSpace(value)
	streams, err := strconv.ParseInt(value, 10, 64)
	if err != nil || streams < 1 || streams > maximum {
		return 0, fmt.Errorf("tunnel max concurrent streams is required and must be between 1 and %d: %q", maximum, value)
	}
	return streams, nil
}

var (
	ErrUnsupportedVersion = errors.New("unsupported tunnel protocol version")
	ErrInvalidNetwork     = errors.New("invalid tunnel network")
	ErrInvalidAddress     = errors.New("invalid tunnel address")
)

type OpenRequest struct {
	Version      int    `json:"version"`
	RequestID    string `json:"request_id"`
	Network      string `json:"network"`
	Address      string `json:"address"`
	TraceTimings bool   `json:"trace_timings,omitempty"`
}

type OpenResponse struct {
	OK                bool   `json:"ok"`
	ErrorKind         string `json:"error_kind,omitempty"`
	Error             string `json:"error,omitempty"`
	DialDurationNanos int64  `json:"dial_duration_nanos,omitempty"`
	DNSDurationNanos  int64  `json:"dns_duration_nanos,omitempty"`
}

func NewOpenRequest(requestID string, network string, address string) OpenRequest {
	return OpenRequest{
		Version:   ProtocolVersion,
		RequestID: strings.TrimSpace(requestID),
		Network:   strings.TrimSpace(strings.ToLower(network)),
		Address:   strings.TrimSpace(address),
	}
}

func (r OpenRequest) Validate() error {
	if r.Version != ProtocolVersion {
		return fmt.Errorf("%w: %d", ErrUnsupportedVersion, r.Version)
	}
	if strings.TrimSpace(strings.ToLower(r.Network)) != "tcp" {
		return fmt.Errorf("%w: %q", ErrInvalidNetwork, r.Network)
	}
	host, port, err := net.SplitHostPort(strings.TrimSpace(r.Address))
	if err != nil {
		return fmt.Errorf("%w: %w", ErrInvalidAddress, err)
	}
	if strings.TrimSpace(host) == "" || strings.TrimSpace(port) == "" {
		return ErrInvalidAddress
	}
	return nil
}
