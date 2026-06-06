package tunnel

import (
	"errors"
	"fmt"
	"net"
	"strings"
)

const (
	ProtocolVersion = 1

	BootstrapPath       = "/agent/tunnel"
	UpgradeToken        = "p2pstream-yamux"
	TunnelVersionHeader = "X-P2PStream-Tunnel-Version"

	MaxControlFrameBytes = 16 * 1024
)

var (
	ErrUnsupportedVersion = errors.New("unsupported tunnel protocol version")
	ErrInvalidNetwork     = errors.New("invalid tunnel network")
	ErrInvalidAddress     = errors.New("invalid tunnel address")
)

type OpenRequest struct {
	Version   int    `json:"version"`
	RequestID string `json:"request_id"`
	Network   string `json:"network"`
	Address   string `json:"address"`
}

type OpenResponse struct {
	OK        bool   `json:"ok"`
	ErrorKind string `json:"error_kind,omitempty"`
	Error     string `json:"error,omitempty"`
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
