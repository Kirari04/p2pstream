package tunnel

import (
	"errors"
	"fmt"
	"net"
	"net/http"
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
	TunnelFlowControlHeader          = "X-P2PStream-Tunnel-Flow-Control"
	TunnelLaneRoleHeader             = "X-P2PStream-Tunnel-Lane-Role"
	TunnelSessionWindowHeader        = "X-P2PStream-Tunnel-Session-Window"
	TunnelSessionWindowMaxHeader     = "X-P2PStream-Tunnel-Session-Window-Max"
	TunnelAllocationWindowHeader     = "X-P2PStream-Tunnel-Allocation-Window"
	TunnelAllocationWindowMaxHeader  = "X-P2PStream-Tunnel-Allocation-Window-Max"
	TunnelDataFrameMaxHeader         = "X-P2PStream-Tunnel-Data-Frame-Max"
	DefaultParallelTunnels           = 4
	MaxParallelTunnels               = 32
	// MaxTunnelLaneSlots includes lane zero, which becomes the dedicated
	// control lane when aggregate flow control is negotiated. Aggregate bulk
	// lane IDs are 1..MaxParallelTunnels; legacy IDs remain 0..N-1.
	MaxTunnelLaneSlots           = MaxParallelTunnels + 1
	TunnelCapacityModeAdaptive   = "adaptive"
	TunnelCapacityModeFixed      = "fixed"
	TunnelFlowControlAggregateV1 = "aggregate-v1"
	TunnelLaneRoleControl        = "control"
	TunnelLaneRoleBulk           = "bulk"

	AggregateInitialSessionWindowBytes = uint32(2 * 1024 * 1024)
	AggregateMaxSessionWindowBytes     = uint32(1024 * 1024 * 1024)
	AggregateInitialStreamWindowBytes  = uint32(512 * 1024)
	AggregateMaxStreamWindowBytes      = uint32(256 * 1024 * 1024)
	AggregateMaxDataFrameBytes         = uint32(32 * 1024)

	MaxControlFrameBytes = 16 * 1024
)

// The extension is acknowledged before an agent starts additional connections.
// Old peers therefore retain their single authenticated tunnel unchanged.
func ParseTunnelLane(group, rawLane string) (string, int, error) {
	return parseTunnelLane(group, rawLane, false)
}

// ParseAggregateTunnelLane accepts the dedicated control lane zero and bulk
// lanes 1..MaxParallelTunnels. The separate parser keeps the legacy lane
// boundary unchanged for mixed-version peers.
func ParseAggregateTunnelLane(group, rawLane string) (string, int, error) {
	return parseTunnelLane(group, rawLane, true)
}

func parseTunnelLane(group, rawLane string, aggregate bool) (string, int, error) {
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
	maximum := MaxParallelTunnels - 1
	if aggregate {
		maximum = MaxParallelTunnels
	}
	if err != nil || lane < 0 || lane > maximum {
		return "", 0, errors.New("invalid tunnel lane")
	}
	return group, lane, nil
}

// AggregateFlowControlConfig is a versioned, symmetric wire contract. Local
// resource policy may decline or delay growth below these maxima, but peers
// must agree on the same ceilings before either side starts Yamux.
type AggregateFlowControlConfig struct {
	InitialSessionWindow uint32
	MaxSessionWindow     uint32
	InitialStreamWindow  uint32
	MaxStreamWindow      uint32
	MaxDataFrame         uint32
}

func DefaultAggregateFlowControlConfig() AggregateFlowControlConfig {
	return AggregateFlowControlConfig{
		InitialSessionWindow: AggregateInitialSessionWindowBytes,
		MaxSessionWindow:     AggregateMaxSessionWindowBytes,
		InitialStreamWindow:  AggregateInitialStreamWindowBytes,
		MaxStreamWindow:      AggregateMaxStreamWindowBytes,
		MaxDataFrame:         AggregateMaxDataFrameBytes,
	}
}

func (c AggregateFlowControlConfig) Equal(other AggregateFlowControlConfig) bool {
	return c == other
}

// SetAggregateFlowControlHeaders writes the complete extension contract. A
// partial offer is never interpreted as a downgrade because that could make
// the two endpoints account different receive promises.
func SetAggregateFlowControlHeaders(headers http.Header, role string, config AggregateFlowControlConfig) {
	headers.Set(TunnelFlowControlHeader, TunnelFlowControlAggregateV1)
	headers.Set(TunnelLaneRoleHeader, role)
	headers.Set(TunnelSessionWindowHeader, strconv.FormatUint(uint64(config.InitialSessionWindow), 10))
	headers.Set(TunnelSessionWindowMaxHeader, strconv.FormatUint(uint64(config.MaxSessionWindow), 10))
	headers.Set(TunnelAllocationWindowHeader, strconv.FormatUint(uint64(config.InitialStreamWindow), 10))
	headers.Set(TunnelAllocationWindowMaxHeader, strconv.FormatUint(uint64(config.MaxStreamWindow), 10))
	headers.Set(TunnelDataFrameMaxHeader, strconv.FormatUint(uint64(config.MaxDataFrame), 10))
}

// ParseAggregateFlowControlHeaders returns enabled=false only when every
// extension header is absent. Duplicate, partial and non-canonical offers are
// rejected because all public upgrade headers are attacker-controlled.
func ParseAggregateFlowControlHeaders(headers http.Header) (config AggregateFlowControlConfig, role string, enabled bool, err error) {
	names := [...]string{
		TunnelFlowControlHeader,
		TunnelLaneRoleHeader,
		TunnelSessionWindowHeader,
		TunnelSessionWindowMaxHeader,
		TunnelAllocationWindowHeader,
		TunnelAllocationWindowMaxHeader,
		TunnelDataFrameMaxHeader,
	}
	values := make([]string, len(names))
	present := false
	for i, name := range names {
		var ok bool
		values[i], ok, err = singleTunnelHeader(headers, name)
		if err != nil {
			return AggregateFlowControlConfig{}, "", false, err
		}
		present = present || ok
	}
	if !present {
		return AggregateFlowControlConfig{}, "", false, nil
	}
	for i, value := range values {
		if value == "" {
			return AggregateFlowControlConfig{}, "", false, fmt.Errorf("incomplete aggregate flow-control header %s", names[i])
		}
	}
	if values[0] != TunnelFlowControlAggregateV1 {
		return AggregateFlowControlConfig{}, "", false, fmt.Errorf("unsupported tunnel flow control %q", values[0])
	}
	role = values[1]
	if role != TunnelLaneRoleControl && role != TunnelLaneRoleBulk {
		return AggregateFlowControlConfig{}, "", false, fmt.Errorf("invalid tunnel lane role %q", role)
	}
	parsed := make([]uint32, 5)
	for i, raw := range values[2:] {
		value, parseErr := strconv.ParseUint(raw, 10, 32)
		if parseErr != nil || value == 0 || strconv.FormatUint(value, 10) != raw {
			return AggregateFlowControlConfig{}, "", false, fmt.Errorf("invalid aggregate flow-control header %s", names[i+2])
		}
		parsed[i] = uint32(value)
	}
	config = AggregateFlowControlConfig{
		InitialSessionWindow: parsed[0],
		MaxSessionWindow:     parsed[1],
		InitialStreamWindow:  parsed[2],
		MaxStreamWindow:      parsed[3],
		MaxDataFrame:         parsed[4],
	}
	if !config.Equal(DefaultAggregateFlowControlConfig()) {
		return AggregateFlowControlConfig{}, "", false, errors.New("unsupported aggregate flow-control values")
	}
	return config, role, true, nil
}

// ParseTunnelLaneHeaders applies duplicate checks before selecting the lane
// boundary for the negotiated protocol mode.
func ParseTunnelLaneHeaders(headers http.Header, aggregate bool) (string, int, error) {
	group, _, err := singleTunnelHeader(headers, TunnelGroupHeader)
	if err != nil {
		return "", 0, err
	}
	lane, _, err := singleTunnelHeader(headers, TunnelLaneHeader)
	if err != nil {
		return "", 0, err
	}
	return parseTunnelLane(group, lane, aggregate)
}

func singleTunnelHeader(headers http.Header, name string) (string, bool, error) {
	values := headers.Values(name)
	if len(values) == 0 {
		return "", false, nil
	}
	if len(values) != 1 {
		return "", false, fmt.Errorf("duplicate tunnel header %s", name)
	}
	value := strings.TrimSpace(values[0])
	if value == "" {
		return "", true, nil
	}
	return value, true, nil
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
