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

// RecommendedAgentTunnelConcurrentRequests derives an agent-side capability
// from the memory limit visible to the process. Operators can still override
// it explicitly; this is the no-configuration default advertised to servers.
func RecommendedAgentTunnelConcurrentRequests(windowBytes int64, memoryLimitBytes int64) (int64, error) {
	if _, err := tunnel.NormalizeMaxStreamWindowSizeBytes(windowBytes); err != nil {
		return 0, err
	}
	_ = memoryLimitBytes
	return tunnel.MaxConcurrentAgentRequestsLimit, nil
}

func DetectProcessMemoryLimitBytes() int64 {
	usage, err := sysmetrics.NewSystemMemoryUsageSampler().SampleMemoryUsage()
	if err != nil || !usage.Valid() {
		return 0
	}
	return usage.LimitBytes
}
