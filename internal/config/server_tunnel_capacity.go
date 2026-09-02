package config

import (
	"bufio"
	"errors"
	"math"
	"os"
	"runtime/debug"
	"strconv"
	"strings"

	"p2pstream/internal/tunnel"
)

const (
	// Automatic sizing must never regress below the production fallback. The
	// Yamux receive window is a credit ceiling, not an eagerly resident
	// allocation per stream; treating the full window as committed memory left
	// small but otherwise capable hosts stuck at the legacy 64/128 boundary.
	minimumServerTunnelConcurrentStreams = tunnel.DefaultServerMaxConcurrentStreams
)

func resolveServerTunnelCapacity(cfg *Config, memoryLimitBytes int64) error {
	if cfg == nil {
		return errors.New("server tunnel capacity configuration is nil")
	}
	if cfg.ServerTunnelMemoryPercent < 1 || cfg.ServerTunnelMemoryPercent > 90 {
		return errors.New("SERVER_TUNNEL_MEMORY_PERCENT must be between 1 and 90")
	}
	if cfg.ServerTunnelMemoryReserveBytes < 0 {
		return errors.New("SERVER_TUNNEL_MEMORY_RESERVE_BYTES must be greater than or equal to 0")
	}
	cfg.ServerTunnelDetectedMemoryBytes = memoryLimitBytes
	if cfg.ServerTunnelMaxConcurrentStreams != 0 {
		cfg.ServerTunnelCapacityAuto = false
		return nil
	}
	cfg.ServerTunnelCapacityAuto = true
	window, err := tunnel.NormalizeMaxStreamWindowSizeBytes(cfg.TunnelMaxStreamWindowBytes)
	if err != nil {
		return err
	}
	if memoryLimitBytes <= 0 {
		cfg.ServerTunnelMaxConcurrentStreams = tunnel.DefaultServerMaxConcurrentStreams
		return nil
	}
	budget := (memoryLimitBytes/100)*cfg.ServerTunnelMemoryPercent +
		(memoryLimitBytes%100)*cfg.ServerTunnelMemoryPercent/100
	if memoryLimitBytes > cfg.ServerTunnelMemoryReserveBytes {
		afterReserve := memoryLimitBytes - cfg.ServerTunnelMemoryReserveBytes
		if afterReserve < budget {
			budget = afterReserve
		}
	}
	streams := budget / int64(window)
	if streams < minimumServerTunnelConcurrentStreams {
		streams = minimumServerTunnelConcurrentStreams
	}
	if streams > tunnel.MaxServerConcurrentStreamsLimit {
		streams = tunnel.MaxServerConcurrentStreamsLimit
	}
	cfg.ServerTunnelMaxConcurrentStreams = streams
	return nil
}

// RecommendedAgentTunnelConcurrentRequests derives an agent-side capability
// from the memory limit visible to the process. Operators can still override
// it explicitly; this is the no-configuration default advertised to servers.
func RecommendedAgentTunnelConcurrentRequests(windowBytes int64, memoryLimitBytes int64) (int64, error) {
	window, err := tunnel.NormalizeMaxStreamWindowSizeBytes(windowBytes)
	if err != nil {
		return 0, err
	}
	if memoryLimitBytes <= 0 {
		return tunnel.DefaultServerMaxConcurrentStreams, nil
	}
	budget := memoryLimitBytes / 2
	const reserve = int64(512 * 1024 * 1024)
	if memoryLimitBytes > reserve && memoryLimitBytes-reserve < budget {
		budget = memoryLimitBytes - reserve
	}
	streams := budget / int64(window)
	if streams < tunnel.DefaultServerMaxConcurrentStreams {
		streams = tunnel.DefaultServerMaxConcurrentStreams
	}
	if streams > tunnel.MaxConcurrentAgentRequestsLimit {
		streams = tunnel.MaxConcurrentAgentRequestsLimit
	}
	return streams, nil
}

func DetectProcessMemoryLimitBytes() int64 {
	limits := make([]int64, 0, 4)
	if value := currentGoMemoryLimitBytes(); value > 0 {
		limits = append(limits, value)
	}
	for _, path := range []string{
		"/sys/fs/cgroup/memory.max",
		"/sys/fs/cgroup/memory/memory.limit_in_bytes",
	} {
		if value := readMemoryLimitFile(path); value > 0 {
			limits = append(limits, value)
		}
	}
	if value := readHostMemoryBytes("/proc/meminfo"); value > 0 {
		limits = append(limits, value)
	}
	var minimum int64
	for _, value := range limits {
		if value <= 0 || value >= math.MaxInt64/2 {
			continue
		}
		if minimum == 0 || value < minimum {
			minimum = value
		}
	}
	return minimum
}

func currentGoMemoryLimitBytes() int64 {
	value := debug.SetMemoryLimit(-1)
	if value <= 0 || value >= math.MaxInt64/2 {
		return 0
	}
	return value
}

func readMemoryLimitFile(path string) int64 {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	raw := strings.TrimSpace(string(data))
	if raw == "" || strings.EqualFold(raw, "max") {
		return 0
	}
	value, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || value <= 0 {
		return 0
	}
	return value
}

func readHostMemoryBytes(path string) int64 {
	file, err := os.Open(path)
	if err != nil {
		return 0
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) < 2 || fields[0] != "MemTotal:" {
			continue
		}
		kilobytes, err := strconv.ParseInt(fields[1], 10, 64)
		if err != nil || kilobytes <= 0 || kilobytes > math.MaxInt64/1024 {
			return 0
		}
		return kilobytes * 1024
	}
	return 0
}
