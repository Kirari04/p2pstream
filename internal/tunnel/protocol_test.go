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
	if cfg.StreamOpenTimeout != 10*time.Second {
		t.Fatalf("StreamOpenTimeout = %s, want 10s", cfg.StreamOpenTimeout)
	}
	if cfg.StreamCloseTimeout != 30*time.Second {
		t.Fatalf("StreamCloseTimeout = %s, want 30s", cfg.StreamCloseTimeout)
	}
}
