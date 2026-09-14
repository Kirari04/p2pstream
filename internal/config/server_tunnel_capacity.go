package config

import (
	"errors"

	"p2pstream/internal/sysmetrics"
	"p2pstream/internal/tunnel"
)

func resolveServerTunnelCapacity(cfg *Config, memoryLimitBytes int64) error {
	if cfg == nil {
		return errors.New("server tunnel capacity configuration is nil")
	}
	cfg.ServerTunnelDetectedMemoryBytes = memoryLimitBytes
	if cfg.ServerTunnelMaxConcurrentStreams != 0 {
		cfg.ServerTunnelCapacityAuto = false
		return nil
	}
	cfg.ServerTunnelCapacityAuto = true
	if _, err := tunnel.NormalizeMaxStreamWindowSizeBytes(cfg.TunnelMaxStreamWindowBytes); err != nil {
		return err
	}
	// Automatic mode is intentionally not sized from MaxStreamWindowSize. The
	// Yamux window is lazy flow-control credit, not committed resident memory.
	// Actual cgroup/host/Go pressure dynamically gates admission at runtime; this
	// value is only the unreachable server implementation guard.
	cfg.ServerTunnelMaxConcurrentStreams = tunnel.MaxServerConcurrentStreamsLimit
	return nil
}

func DetectProcessMemoryLimitBytes() int64 {
	usage, err := sysmetrics.NewSystemMemoryUsageSampler().SampleMemoryUsage()
	if err != nil || !usage.Valid() {
		return 0
	}
	return usage.LimitBytes
}
