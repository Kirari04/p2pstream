package config

import (
	"testing"

	"p2pstream/internal/tunnel"
)

func TestResolveServerTunnelCapacityUsesAdaptiveImplementationGuard(t *testing.T) {
	tests := []struct {
		name        string
		memory      int64
		wantStreams int64
	}{
		{name: "unknown", memory: 0, wantStreams: tunnel.MaxServerConcurrentStreamsLimit},
		{name: "half gibibyte", memory: 512 << 20, wantStreams: tunnel.MaxServerConcurrentStreamsLimit},
		{name: "one gibibyte", memory: 1 << 30, wantStreams: tunnel.MaxServerConcurrentStreamsLimit},
		{name: "two gibibytes", memory: 2 << 30, wantStreams: tunnel.MaxServerConcurrentStreamsLimit},
		{name: "eight gibibytes", memory: 8 << 30, wantStreams: tunnel.MaxServerConcurrentStreamsLimit},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &Config{
				TunnelMaxStreamWindowBytes: tunnel.DefaultMaxStreamWindowSizeBytes,
			}
			if err := resolveServerTunnelCapacity(cfg, tt.memory); err != nil {
				t.Fatalf("resolve capacity: %v", err)
			}
			if cfg.ServerTunnelMaxConcurrentStreams != tt.wantStreams {
				t.Fatalf("streams = %d, want %d", cfg.ServerTunnelMaxConcurrentStreams, tt.wantStreams)
			}
		})
	}
}

func TestResolveServerTunnelCapacityHonorsExplicitOverride(t *testing.T) {
	cfg := &Config{
		TunnelMaxStreamWindowBytes:       tunnel.DefaultMaxStreamWindowSizeBytes,
		ServerTunnelMaxConcurrentStreams: 4096,
	}
	if err := resolveServerTunnelCapacity(cfg, 1<<30); err != nil {
		t.Fatalf("resolve capacity: %v", err)
	}
	if cfg.ServerTunnelMaxConcurrentStreams != 4096 {
		t.Fatalf("explicit streams = %d, want 4096", cfg.ServerTunnelMaxConcurrentStreams)
	}
}
