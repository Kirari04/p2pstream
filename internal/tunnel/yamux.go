package tunnel

import (
	"errors"
	"fmt"
	"io"
	"math"
	"time"

	"github.com/hashicorp/yamux"
)

const (
	// InitialStreamWindowSizeBytes is the receive credit Yamux grants to every
	// stream before it has observed any traffic. Keep this in sync with
	// hashicorp/yamux's initialStreamWindow. The upstream value is not exported.
	InitialStreamWindowSizeBytes = int64(256 * 1024)
	// AdaptivePerStreamOverheadBytes covers bounded TCP socket buffers, relay
	// copy buffers, Yamux stream metadata, and allocator slack beyond receive
	// credit itself. The stream charge is held until the stream lease closes.
	AdaptivePerStreamOverheadBytes = int64(768 * 1024)
	// AdaptiveServerStreamOverheadBytes covers the bounded userspace state the
	// server owns for a logical Yamux stream beyond receive storage: stream and
	// TLS metadata plus relay copy buffers and allocator slack. Origin TCP
	// socket queues belong to the agent and are deliberately excluded.
	AdaptiveServerStreamOverheadBytes = int64(256 * 1024)
	DefaultAdaptiveReceiveWindowBytes = int64(512 * 1024)
	DefaultAdaptiveStreamChargeBytes  = DefaultAdaptiveReceiveWindowBytes + AdaptivePerStreamOverheadBytes
	MinimumAdaptiveStreamChargeBytes  = InitialStreamWindowSizeBytes + AdaptivePerStreamOverheadBytes
	DefaultMaxStreamWindowSizeBytes   = MaxStreamWindowSizeBytesLimit
	// This is a protocol ceiling, not an initial allocation. Production growth
	// must reserve against the shared process memory budget before advertising
	// any extra credit, including with explicit concurrency limits.
	MaxStreamWindowSizeBytesLimit     = int64(1024 * 1024 * 1024)
	DefaultMaxConcurrentAgentRequests = int64(64)
	MaxConcurrentAgentRequestsLimit   = int64(2048)
	DefaultServerMaxConcurrentStreams = int64(256)
	MaxServerConcurrentStreamsLimit   = int64(65536)
	MaxAdaptiveConcurrentStreamsLimit = MaxServerConcurrentStreamsLimit
	DefaultUpstreamSocketBufferBytes  = int64(0) // kernel autotuning
	MaxUpstreamSocketBufferBytes      = int64(16 * 1024 * 1024)
	// AggregateAcceptBacklogStreams includes Yamux's bounded accept queue plus
	// the one stream removed from that queue while synchronous application
	// admission is deciding whether to serve or reset it.
	AggregateAcceptBacklogStreams = 257
	// AggregateSessionMetadataBytes covers the Session, queues, publisher state,
	// slab descriptors and allocator slack outside the payload pool itself.
	AggregateSessionMetadataBytes = int64(256 * 1024)
	// AggregatePendingStreamMetadataBytes is a conservative bound for a stream
	// object, channels, headers and allocator slack before application admission.
	AggregatePendingStreamMetadataBytes = int64(8 * 1024)
	NegotiatedControlStreamLimit        = 4
	// AggregateRejectedStreamLimit bounds graceful capacity-error responses
	// retained until peer FIN. Excess rejected streams are reset immediately.
	AggregateRejectedStreamLimit = 16
	// AgentLegacyAcceptBacklogStreams preserves the historical 256-stream SYN
	// burst accepted by an old peer. One additional transition stream may have
	// been removed from this queue while admission synchronously acquires its
	// lifetime leases. The complete finite envelope is prepaid for legacy
	// compatibility; aggregate-v1 removes this idle per-stream promise cost.
	AgentLegacyAcceptBacklogStreams = 256
	AgentLegacyPendingStreams       = AgentLegacyAcceptBacklogStreams + 1
	AgentLegacyRejectedStreamLimit  = 16
)

// StreamResourceModel describes resources owned by one admitted logical
// stream in a particular process. FileDescriptors is the peak incremental
// descriptor ownership while opening the stream.
type StreamResourceModel struct {
	MemoryBytes     int64
	FileDescriptors int64
}

// ServerStreamBaseResourceModel accounts for the bounded metadata, relay and
// TLS state owned by an admitted server-side logical stream. Receive storage
// is supplied either by the aggregate session pool or by a separate legacy
// supplement acquired before Open publishes a stream promise.
func ServerStreamBaseResourceModel() StreamResourceModel {
	return StreamResourceModel{MemoryBytes: AdaptiveServerStreamOverheadBytes}
}

// AgentStreamBaseResourceModel adds the actual origin TCP queues and peak dial
// descriptors owned by the agent to the shared logical-stream base. Receive
// storage is accounted separately by the negotiated session or legacy lease.
func AgentStreamBaseResourceModel(socketBufferBytes int64) (StreamResourceModel, error) {
	model := ServerStreamBaseResourceModel()
	buffer, err := NormalizeUpstreamSocketBufferBytes(socketBufferBytes)
	if err != nil {
		return StreamResourceModel{}, err
	}
	if buffer == 0 {
		buffer = 128 * 1024
	}
	if buffer > (math.MaxInt64-model.MemoryBytes)/4 {
		return StreamResourceModel{}, errors.New("agent origin socket memory exceeds accounting range")
	}
	model.MemoryBytes += 4 * buffer
	model.FileDescriptors = 2
	return model, nil
}

// LegacyReceiveWindowSupplementBytes returns the exact conservative storage
// reservation for one legacy per-stream receive promise.
func LegacyReceiveWindowSupplementBytes(windowBytes int64) (int64, error) {
	window, err := InitialReceiveWindow(windowBytes)
	if err != nil {
		return 0, err
	}
	receive := yamux.ReceiveWindowMemory(uint32(window))
	if receive > math.MaxInt64 {
		return 0, errors.New("legacy stream receive memory exceeds accounting range")
	}
	return int64(receive), nil
}

// ServerStreamResourceModel accounts only for resources created by a logical
// Yamux stream in the server process. Admission does not create a physical
// tunnel or public socket; charging either here would invent socket memory and
// descriptors for multiplexed streams. Their baseline remains visible to the
// process sampler, and accepted public sockets use separate lifetime owners.
func ServerStreamResourceModel(windowBytes int64) (StreamResourceModel, error) {
	window, err := InitialReceiveWindow(windowBytes)
	if err != nil {
		return StreamResourceModel{}, err
	}
	receiveBytes := yamux.ReceiveWindowMemory(uint32(window))
	if receiveBytes > uint64(math.MaxInt64-AdaptiveServerStreamOverheadBytes) {
		return StreamResourceModel{}, errors.New("server stream receive memory exceeds accounting range")
	}
	return StreamResourceModel{
		MemoryBytes: int64(receiveBytes) + AdaptiveServerStreamOverheadBytes,
	}, nil
}

// AgentStreamResourceModel adds the origin TCP queues and dial descriptors
// that the agent actually owns. Two descriptors cover the temporary peak of
// a dual-stack Happy Eyeballs dial; the settled origin connection owns one.
// ReceiveWindowMemory includes chunk rounding and slack, so the default agent
// charge is deliberately higher than the legacy 1.25 MiB estimate.
func AgentStreamResourceModel(windowBytes, socketBufferBytes int64) (StreamResourceModel, error) {
	model, err := AgentStreamBaseResourceModel(socketBufferBytes)
	if err != nil {
		return StreamResourceModel{}, err
	}
	receive, err := LegacyReceiveWindowSupplementBytes(windowBytes)
	if err != nil {
		return StreamResourceModel{}, err
	}
	if receive > math.MaxInt64-model.MemoryBytes {
		return StreamResourceModel{}, errors.New("agent stream receive memory exceeds accounting range")
	}
	model.MemoryBytes += receive
	return model, nil
}

// StreamMemoryCharge covers initial receive credit and socket/relay overhead.
// Linux auto TCP queues use the included baseline while actual growth is
// observed by the resource controller. GrowingConn reserves additional receive
// credit only for sustained traffic.
func StreamMemoryCharge(windowBytes, socketBufferBytes int64) (int64, error) {
	window, err := NormalizeMaxStreamWindowSizeBytes(windowBytes)
	if err != nil {
		return 0, err
	}
	buffer, err := NormalizeUpstreamSocketBufferBytes(socketBufferBytes)
	if err != nil {
		return 0, err
	}
	if buffer == 0 {
		buffer = 128 * 1024 // four-times accounting yields the 512 KiB auto baseline
	}
	// Linux may double both requested socket buffers. The remaining 256 KiB
	// covers relay buffers, stream metadata, TLS state and allocator slack.
	charge := min(int64(window), DefaultAdaptiveReceiveWindowBytes) + 4*buffer + 256*1024
	return max(charge, MinimumAdaptiveStreamChargeBytes), nil
}

func InitialReceiveWindow(configured int64) (int64, error) {
	window, err := NormalizeMaxStreamWindowSizeBytes(configured)
	return min(int64(window), DefaultAdaptiveReceiveWindowBytes), err
}

func NormalizeUpstreamSocketBufferBytes(size int64) (int64, error) {
	if size == 0 {
		return 0, nil
	}
	if size < 16*1024 || size > MaxUpstreamSocketBufferBytes {
		return 0, fmt.Errorf("TUNNEL_UPSTREAM_SOCKET_BUFFER_BYTES must be zero (automatic) or between %d and %d", 16*1024, MaxUpstreamSocketBufferBytes)
	}
	return size, nil
}

// AdaptiveMaxStreamWindowSizeBytes returns the largest receive window that is
// fully covered by the adaptive controller's lifetime charge for one stream.
// A stream may grow its Yamux receive buffer up to MaxStreamWindowSize, so an
// adaptive session must never grant more credit than it reserves.
func AdaptiveMaxStreamWindowSizeBytes(configuredBytes, chargedBytes int64) (int64, error) {
	configured, err := NormalizeMaxStreamWindowSizeBytes(configuredBytes)
	if err != nil {
		return 0, err
	}
	if chargedBytes == 0 {
		chargedBytes = DefaultAdaptiveStreamChargeBytes
	}
	if chargedBytes < MinimumAdaptiveStreamChargeBytes {
		return 0, fmt.Errorf(
			"adaptive stream charge must be at least %d bytes (%d-byte Yamux initial window plus %d bytes of per-stream overhead)",
			MinimumAdaptiveStreamChargeBytes,
			InitialStreamWindowSizeBytes,
			AdaptivePerStreamOverheadBytes,
		)
	}
	if chargedBytes > MaxStreamWindowSizeBytesLimit+4*MaxUpstreamSocketBufferBytes+AdaptivePerStreamOverheadBytes {
		return 0, fmt.Errorf("adaptive stream charge exceeds the maximum accounted window and socket buffers")
	}
	coveredWindow := chargedBytes - AdaptivePerStreamOverheadBytes
	if coveredWindow < int64(configured) {
		return coveredWindow, nil
	}
	return int64(configured), nil
}

func DefaultYamuxConfig(logger yamux.Logger) *yamux.Config {
	cfg, _ := NewYamuxConfig(logger, 0)
	return cfg
}

func NormalizeMaxConcurrentAgentRequests(requests int64) (int64, error) {
	if requests == 0 {
		requests = DefaultMaxConcurrentAgentRequests
	}
	if requests < 1 {
		return 0, errors.New("TUNNEL_MAX_CONCURRENT_REQUESTS must be at least 1")
	}
	if requests > MaxConcurrentAgentRequestsLimit {
		return 0, fmt.Errorf("TUNNEL_MAX_CONCURRENT_REQUESTS must be less than or equal to %d", MaxConcurrentAgentRequestsLimit)
	}
	return requests, nil
}

func NewYamuxConfig(logger yamux.Logger, maxStreamWindowSizeBytes int64) (*yamux.Config, error) {
	cfg := yamux.DefaultConfig()
	cfg.AcceptBacklog = 256
	cfg.EnableKeepAlive = true
	cfg.KeepAliveInterval = 20 * time.Second
	cfg.ConnectionWriteTimeout = 10 * time.Second
	maxStreamWindowSize, err := NormalizeMaxStreamWindowSizeBytes(maxStreamWindowSizeBytes)
	if err != nil {
		return nil, err
	}
	cfg.MaxStreamWindowSize = maxStreamWindowSize
	cfg.StreamOpenTimeout = 10 * time.Second
	cfg.StreamCloseTimeout = 30 * time.Second
	if logger != nil {
		cfg.Logger = logger
		cfg.LogOutput = nil
	} else {
		cfg.Logger = nil
		cfg.LogOutput = io.Discard
	}
	return cfg, nil
}

// NewAggregateYamuxConfig enables the versioned bounded session payload pool.
// maxStreamWindowSizeBytes validates the caller's byte-window policy, while
// the Yamux session starts at InitialReceiveWindow and the measured wrapper
// publishes later byte growth. Allocation-unit ceilings are a distinct wire
// contract. maxIncomingStreams is -1 to reject all peer SYNs, or a positive
// hard bound enforced before stream allocation.
func NewAggregateYamuxConfig(logger yamux.Logger, maxStreamWindowSizeBytes int64, maxIncomingStreams int) (*yamux.Config, error) {
	// Aggregate unit credit starts at the versioned stream allowance. Keep byte
	// credit at the same bounded initial level and let the measured grower
	// publish it toward the independently configured operator maximum.
	if _, err := NormalizeMaxStreamWindowSizeBytes(maxStreamWindowSizeBytes); err != nil {
		return nil, err
	}
	initialWindow, err := InitialReceiveWindow(maxStreamWindowSizeBytes)
	if err != nil {
		return nil, err
	}
	cfg, err := NewYamuxConfig(logger, initialWindow)
	if err != nil {
		return nil, err
	}
	wire := DefaultAggregateFlowControlConfig()
	cfg.MaxIncomingStreams = maxIncomingStreams
	cfg.InitialSessionWindowSize = wire.InitialSessionWindow
	cfg.MaxSessionWindowSize = wire.MaxSessionWindow
	cfg.MaxDataFrameSize = wire.MaxDataFrame
	cfg.InitialStreamAllocationWindowSize = wire.InitialStreamWindow
	cfg.MaxStreamAllocationWindowSize = wire.MaxStreamWindow
	if err := yamux.VerifyConfig(cfg); err != nil {
		return nil, fmt.Errorf("invalid aggregate Yamux configuration: %w", err)
	}
	return cfg, nil
}

// NewNegotiatedControlYamuxConfig preserves legacy per-stream DATA flow
// control on the dedicated lane while enforcing its negotiated hard incoming
// stream bound before the reader starts.
func NewNegotiatedControlYamuxConfig(logger yamux.Logger, maxStreamWindowSizeBytes int64, maxIncomingStreams int) (*yamux.Config, error) {
	cfg, err := NewYamuxConfig(logger, maxStreamWindowSizeBytes)
	if err != nil {
		return nil, err
	}
	cfg.MaxIncomingStreams = maxIncomingStreams
	if err := yamux.VerifyConfig(cfg); err != nil {
		return nil, fmt.Errorf("invalid negotiated control Yamux configuration: %w", err)
	}
	return cfg, nil
}

// NewAgentLegacyYamuxConfig bounds all peer-created legacy stream objects.
// admittedStreams covers streams with application base+receive leases;
// pending and graceful-rejection residents are covered by the session envelope
// returned from AgentLegacySessionResourceBytes.
func NewAgentLegacyYamuxConfig(logger yamux.Logger, maxStreamWindowSizeBytes int64, admittedStreams int) (*yamux.Config, error) {
	if admittedStreams < 1 || admittedStreams > int(MaxAdaptiveConcurrentStreamsLimit) {
		return nil, errors.New("legacy admitted stream limit is out of range")
	}
	resident := int64(admittedStreams) + AgentLegacyPendingStreams + AgentLegacyRejectedStreamLimit
	if resident > math.MaxInt32 {
		return nil, errors.New("legacy incoming stream bound exceeds protocol range")
	}
	cfg, err := NewYamuxConfig(logger, maxStreamWindowSizeBytes)
	if err != nil {
		return nil, err
	}
	cfg.AcceptBacklog = AgentLegacyAcceptBacklogStreams
	cfg.MaxIncomingStreams = int(resident)
	if err := yamux.VerifyConfig(cfg); err != nil {
		return nil, fmt.Errorf("invalid agent legacy Yamux configuration: %w", err)
	}
	return cfg, nil
}

// AggregateSessionResourceBytes is the lifetime reservation required before
// advertising an aggregate receive pool. It includes the exact conservative
// pool allocation model plus fixed session state and a caller-selected bound
// for peer-created streams awaiting synchronous application admission.
func AggregateSessionResourceBytes(window uint32, pendingStreams int) (int64, error) {
	if pendingStreams < 0 {
		return 0, errors.New("aggregate pending stream count must not be negative")
	}
	pool := yamux.AggregateReceiveWindowMemory(window)
	base := uint64(AggregateSessionMetadataBytes)
	perStream := uint64(AggregatePendingStreamMetadataBytes)
	if uint64(pendingStreams) > (math.MaxUint64-base)/perStream {
		return 0, errors.New("aggregate pending stream reservation exceeds accounting range")
	}
	metadata := base + uint64(pendingStreams)*perStream
	if metadata > uint64(math.MaxInt64) || pool > uint64(math.MaxInt64)-metadata {
		return 0, errors.New("aggregate session resource reservation exceeds accounting range")
	}
	return int64(pool + metadata), nil
}

// NegotiatedControlSessionResourceBytes reserves the four full legacy receive
// promises before a negotiated control session starts. The hard Yamux incoming
// cap makes this session-lifetime envelope complete for unaccepted streams.
func NegotiatedControlSessionResourceBytes(windowBytes int64) (int64, error) {
	return NegotiatedControlSessionResourceBytesForStreams(windowBytes, NegotiatedControlStreamLimit)
}

// NegotiatedControlSessionResourceBytesForStreams returns the receive and
// metadata promise for a negotiated number of control streams. The agent uses
// the configured limit before the upgrade, then releases any excess promise
// when the server negotiates a smaller total request limit. The separate agent
// stream-base promises cover working buffers, origin TCP queues and dial FDs.
func NegotiatedControlSessionResourceBytesForStreams(windowBytes int64, streams int) (int64, error) {
	if streams < 1 || streams > NegotiatedControlStreamLimit {
		return 0, errors.New("negotiated control stream count is out of range")
	}
	window, err := InitialReceiveWindow(windowBytes)
	if err != nil {
		return 0, err
	}
	receive := yamux.ReceiveWindowMemory(uint32(window))
	perStream := receive + uint64(AggregatePendingStreamMetadataBytes)
	total := uint64(AggregateSessionMetadataBytes) + uint64(streams)*perStream
	if total > math.MaxInt64 {
		return 0, errors.New("control session resource reservation exceeds accounting range")
	}
	return int64(total), nil
}

// AgentLegacySessionResourceBytes reserves every receive promise and metadata
// object that can exist before per-stream application admission, including the
// bounded graceful-rejection set used when pressure prevents acquiring a new
// supplement. The fixed envelope remains for the session lifetime because the
// accept queue can refill immediately while an accepted stream transfers to its
// own supplement.
func AgentLegacySessionResourceBytes(windowBytes int64) (int64, error) {
	receive, err := LegacyReceiveWindowSupplementBytes(windowBytes)
	if err != nil {
		return 0, err
	}
	perPending := receive + AggregatePendingStreamMetadataBytes
	pending := int64(AgentLegacyPendingStreams)
	rejected := int64(AgentLegacyRejectedStreamLimit)
	if perPending > (math.MaxInt64-AggregateSessionMetadataBytes)/pending {
		return 0, errors.New("legacy session pending reservation exceeds accounting range")
	}
	total := AggregateSessionMetadataBytes + pending*perPending
	if perPending > (math.MaxInt64-total)/rejected {
		return 0, errors.New("legacy session rejection reservation exceeds accounting range")
	}
	return total + rejected*perPending, nil
}

func NormalizeMaxStreamWindowSizeBytes(sizeBytes int64) (uint32, error) {
	if sizeBytes == 0 {
		sizeBytes = DefaultMaxStreamWindowSizeBytes
	}
	min := int64(yamux.DefaultConfig().MaxStreamWindowSize)
	if sizeBytes < min {
		return 0, fmt.Errorf("TUNNEL_MAX_STREAM_WINDOW_BYTES must be at least %d bytes", min)
	}
	if sizeBytes > MaxStreamWindowSizeBytesLimit {
		return 0, fmt.Errorf("TUNNEL_MAX_STREAM_WINDOW_BYTES must be less than or equal to %d bytes", MaxStreamWindowSizeBytesLimit)
	}
	return uint32(sizeBytes), nil
}
