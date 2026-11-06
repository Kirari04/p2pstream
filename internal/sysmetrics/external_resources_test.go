package sysmetrics

import (
	"p2pstream/internal/tunnel"
	"testing"
)

func TestSocketBufferRangeProducesUsableResourceConfiguration(t *testing.T) {
	for _, buffer := range []int64{16 << 10, 128 << 10, 16 << 20} {
		for _, window := range []int64{256 << 10, 2 << 20, 64 << 20} {
			charge, err := tunnel.StreamMemoryCharge(window, buffer)
			if err != nil {
				t.Fatal(err)
			}
			cfg := DefaultAdaptiveMemoryConfig()
			cfg.EstimatedBytesPerAdmission = charge
			if err := cfg.Validate(); err != nil {
				t.Fatalf("valid buffer/window %d/%d cannot start: %v", buffer, window, err)
			}
			if charge < min(window, tunnel.DefaultAdaptiveReceiveWindowBytes)+4*buffer+256*1024 {
				t.Fatal("socket buffers under-accounted")
			}
		}
	}
}

func TestExternalResourcesDoNotConsumeUnrelatedDimensions(t *testing.T) {
	for _, test := range []struct {
		name               string
		memory, fds        int
		bytes, descriptors int64
		want               int
	}{
		{"bytes do not consume descriptors", 1000, 50, 100 << 20, 0, 50},
		{"descriptors do not consume memory", 50, 1000, 0, 200, 50},
		{"both dimensions", 1000, 1000, 100 << 20, 400, 800},
		{"exact exhaustion", 1, 1000, 1 << 20, 0, 0},
		{"overdraw with no live streams", 1, 1000, (1 << 20) + 1, 0, -1},
	} {
		t.Run(test.name, func(t *testing.T) {
			s := AdaptiveMemorySnapshot{MemoryAdmissionLimit: test.memory, FDAdmissionLimit: test.fds, StreamChargeByte: 1 << 20}
			if got := s.AdmissionLimitWithExternal(test.bytes, test.descriptors); got != test.want {
				t.Fatalf("allowance=%d, want %d", got, test.want)
			}
		})
	}
}

func TestExternalBytesAreNotBoundByPhysicalStreamGuard(t *testing.T) {
	cfg := DefaultAdaptiveMemoryConfig()
	controller := MustNewAdaptiveMemoryController(cfg, &mutableMemorySampler{usage: MemoryUsage{UsedBytes: 64 << 20, LimitBytes: 16 << 30, Source: "test"}})
	s := controller.ForceRefresh(64, 0)
	if got := s.AdmissionLimitWithExternal(128<<20, 0); got != 64 {
		t.Fatalf("byte owners consumed the explicit 64-stream ceiling: %d", got)
	}
}
