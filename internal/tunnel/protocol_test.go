package tunnel

import (
	"errors"
	"testing"
	"time"
)

func TestOpenRequestValidate(t *testing.T) {
	tests := []struct {
		name string
		req  OpenRequest
		want error
	}{
		{
			name: "valid",
			req:  NewOpenRequest("req-1", "tcp", "example.test:443"),
		},
		{
			name: "unsupported version",
			req:  OpenRequest{Version: 2, Network: "tcp", Address: "example.test:443"},
			want: ErrUnsupportedVersion,
		},
		{
			name: "invalid network",
			req:  OpenRequest{Version: ProtocolVersion, Network: "udp", Address: "example.test:443"},
			want: ErrInvalidNetwork,
		},
		{
			name: "missing port",
			req:  OpenRequest{Version: ProtocolVersion, Network: "tcp", Address: "example.test"},
			want: ErrInvalidAddress,
		},
		{
			name: "empty host",
			req:  OpenRequest{Version: ProtocolVersion, Network: "tcp", Address: ":443"},
			want: ErrInvalidAddress,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.req.Validate()
			if tt.want == nil {
				if err != nil {
					t.Fatalf("Validate() error = %v, want nil", err)
				}
				return
			}
			if !errors.Is(err, tt.want) {
				t.Fatalf("Validate() error = %v, want %v", err, tt.want)
			}
		})
	}
}

func TestDefaultYamuxConfig(t *testing.T) {
	cfg := DefaultYamuxConfig(nil)
	if cfg.AcceptBacklog != 256 {
		t.Fatalf("AcceptBacklog = %d, want 256", cfg.AcceptBacklog)
	}
	if !cfg.EnableKeepAlive {
		t.Fatal("EnableKeepAlive = false, want true")
	}
	if cfg.KeepAliveInterval != 20*time.Second {
		t.Fatalf("KeepAliveInterval = %s, want 20s", cfg.KeepAliveInterval)
	}
	if cfg.ConnectionWriteTimeout != 10*time.Second {
		t.Fatalf("ConnectionWriteTimeout = %s, want 10s", cfg.ConnectionWriteTimeout)
	}
	if cfg.MaxStreamWindowSize != uint32(DefaultMaxStreamWindowSizeBytes) {
		t.Fatalf("MaxStreamWindowSize = %d, want %d", cfg.MaxStreamWindowSize, DefaultMaxStreamWindowSizeBytes)
	}
	if cfg.StreamOpenTimeout != 10*time.Second {
		t.Fatalf("StreamOpenTimeout = %s, want 10s", cfg.StreamOpenTimeout)
	}
	if cfg.StreamCloseTimeout != 30*time.Second {
		t.Fatalf("StreamCloseTimeout = %s, want 30s", cfg.StreamCloseTimeout)
	}
}

func TestNewYamuxConfigMaxStreamWindowSizeOverride(t *testing.T) {
	cfg, err := NewYamuxConfig(nil, 16*1024*1024)
	if err != nil {
		t.Fatalf("NewYamuxConfig() error = %v", err)
	}
	if cfg.MaxStreamWindowSize != 16*1024*1024 {
		t.Fatalf("MaxStreamWindowSize = %d, want 16777216", cfg.MaxStreamWindowSize)
	}
}

func TestNormalizeMaxStreamWindowSizeBytesRejectsUnsafeBounds(t *testing.T) {
	for _, value := range []int64{-1, 1, MaxStreamWindowSizeBytesLimit + 1} {
		if _, err := NormalizeMaxStreamWindowSizeBytes(value); err == nil {
			t.Fatalf("NormalizeMaxStreamWindowSizeBytes(%d) error = nil, want error", value)
		}
	}
}

func TestValidateAggregateStreamWindowBudget(t *testing.T) {
	if err := ValidateAggregateStreamWindowBudget(0, 0); err != nil {
		t.Fatalf("default aggregate stream window budget: %v", err)
	}
	if err := ValidateAggregateStreamWindowBudget(64*1024*1024, 8); err != nil {
		t.Fatalf("boundary aggregate stream window budget: %v", err)
	}
	if err := ValidateAggregateStreamWindowBudget(64*1024*1024, 9); err == nil {
		t.Fatal("aggregate stream window budget above limit was accepted")
	}
	for _, requests := range []int64{-1, MaxConcurrentAgentRequestsLimit + 1} {
		if err := ValidateAggregateStreamWindowBudget(DefaultMaxStreamWindowSizeBytes, requests); err == nil {
			t.Fatalf("concurrent request limit %d was accepted", requests)
		}
	}
}
