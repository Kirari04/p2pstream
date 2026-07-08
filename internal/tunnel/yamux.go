package tunnel

import (
	"fmt"
	"io"
	"time"

	"github.com/hashicorp/yamux"
)

const (
	DefaultMaxStreamWindowSizeBytes = int64(8 * 1024 * 1024)
	MaxStreamWindowSizeBytesLimit   = int64(1024 * 1024 * 1024)
)

func DefaultYamuxConfig(logger yamux.Logger) *yamux.Config {
	cfg, _ := NewYamuxConfig(logger, 0)
	return cfg
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
