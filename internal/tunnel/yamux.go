package tunnel

import (
	"errors"
	"fmt"
	"io"
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
	AdaptivePerStreamOverheadBytes     = int64(768 * 1024)
	DefaultAdaptiveStreamChargeBytes   = int64(1280 * 1024)
	MinimumAdaptiveStreamChargeBytes   = InitialStreamWindowSizeBytes + AdaptivePerStreamOverheadBytes
	DefaultMaxStreamWindowSizeBytes    = int64(2 * 1024 * 1024)
	MaxStreamWindowSizeBytesLimit      = int64(64 * 1024 * 1024)
	DefaultMaxConcurrentAgentRequests  = int64(64)
	MaxConcurrentAgentRequestsLimit    = int64(2048)
	DefaultServerMaxConcurrentStreams  = int64(256)
	MaxServerConcurrentStreamsLimit    = int64(65536)
	MaxAdaptiveConcurrentStreamsLimit  = MaxServerConcurrentStreamsLimit
	MaxAggregateStreamWindowBytesLimit = int64(512 * 1024 * 1024)
)

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
	if chargedBytes > MaxStreamWindowSizeBytesLimit {
		return 0, fmt.Errorf("adaptive stream charge must be at most %d bytes", MaxStreamWindowSizeBytesLimit)
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

func ValidateAggregateStreamWindowBudget(windowBytes int64, requests int64) error {
	window, err := NormalizeMaxStreamWindowSizeBytes(windowBytes)
	if err != nil {
		return err
	}
	requests, err = NormalizeMaxConcurrentAgentRequests(requests)
	if err != nil {
		return err
	}
	if int64(window) > MaxAggregateStreamWindowBytesLimit/requests {
		return fmt.Errorf(
			"TUNNEL_MAX_STREAM_WINDOW_BYTES times TUNNEL_MAX_CONCURRENT_REQUESTS must be less than or equal to %d bytes",
			MaxAggregateStreamWindowBytesLimit,
		)
	}
	return nil
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
