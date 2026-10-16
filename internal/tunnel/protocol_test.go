package tunnel

import (
	"errors"
	"net/http"
	"strconv"
	"testing"
	"time"

	"github.com/hashicorp/yamux"
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

func TestParseMaxConcurrentStreams(t *testing.T) {
	if value, err := ParseMaxConcurrentStreams(" 512 ", MaxConcurrentAgentRequestsLimit); err != nil || value != 512 {
		t.Fatalf("valid capacity = %d, %v", value, err)
	}
	for _, raw := range []string{"", " ", "0", "-1", "2049", "invalid"} {
		if _, err := ParseMaxConcurrentStreams(raw, MaxConcurrentAgentRequestsLimit); err == nil {
			t.Fatalf("capacity %q must be rejected", raw)
		}
	}
}

func TestParseCapacityMode(t *testing.T) {
	for _, raw := range []string{" Adaptive ", "fixed"} {
		if _, err := ParseCapacityMode(raw); err != nil {
			t.Fatal(err)
		}
	}
	for _, raw := range []string{"", " ", "unlimited"} {
		if _, err := ParseCapacityMode(raw); err == nil {
			t.Fatalf("mode %q must be rejected", raw)
		}
	}
}

func TestAggregateTunnelLaneBoundaryKeepsLegacyUnchanged(t *testing.T) {
	group := "0123456789abcdef"
	if _, lane, err := ParseAggregateTunnelLane(group, strconv.Itoa(MaxParallelTunnels)); err != nil || lane != MaxParallelTunnels {
		t.Fatalf("aggregate maximum lane = %d, %v", lane, err)
	}
	if _, _, err := ParseAggregateTunnelLane(group, strconv.Itoa(MaxParallelTunnels+1)); err == nil {
		t.Fatal("aggregate lane beyond maximum accepted")
	}
	if _, _, err := ParseTunnelLane(group, strconv.Itoa(MaxParallelTunnels)); err == nil {
		t.Fatal("legacy parser accepted aggregate-only lane")
	}
	if _, lane, err := ParseTunnelLane(group, strconv.Itoa(MaxParallelTunnels-1)); err != nil || lane != MaxParallelTunnels-1 {
		t.Fatalf("legacy maximum lane = %d, %v", lane, err)
	}
}

func TestAggregateFlowControlHeadersRoundTripAndRejectMalformed(t *testing.T) {
	config := DefaultAggregateFlowControlConfig()
	headers := make(http.Header)
	SetAggregateFlowControlHeaders(headers, TunnelLaneRoleBulk, config)
	got, role, enabled, err := ParseAggregateFlowControlHeaders(headers)
	if err != nil || !enabled || role != TunnelLaneRoleBulk || !got.Equal(config) {
		t.Fatalf("parse round trip = %#v, %q, %t, %v", got, role, enabled, err)
	}

	tests := map[string]func(http.Header){
		"duplicate": func(h http.Header) { h.Add(TunnelFlowControlHeader, TunnelFlowControlAggregateV1) },
		"partial":   func(h http.Header) { h.Del(TunnelDataFrameMaxHeader) },
		"role":      func(h http.Header) { h.Set(TunnelLaneRoleHeader, "public") },
		"version":   func(h http.Header) { h.Set(TunnelFlowControlHeader, "aggregate-v2") },
		"value":     func(h http.Header) { h.Set(TunnelSessionWindowHeader, "02097152") },
		"mismatch":  func(h http.Header) { h.Set(TunnelAllocationWindowHeader, "262144") },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			h := headers.Clone()
			mutate(h)
			if _, _, _, err := ParseAggregateFlowControlHeaders(h); err == nil {
				t.Fatal("malformed aggregate offer accepted")
			}
		})
	}

	if _, _, enabled, err := ParseAggregateFlowControlHeaders(make(http.Header)); err != nil || enabled {
		t.Fatalf("absent extension = enabled %t, err %v", enabled, err)
	}
	partial := make(http.Header)
	partial.Set(TunnelLaneRoleHeader, TunnelLaneRoleControl)
	if _, _, _, err := ParseAggregateFlowControlHeaders(partial); err == nil {
		t.Fatal("orphan extension header accepted as legacy")
	}
}

func TestParseTunnelLaneHeadersRejectsDuplicates(t *testing.T) {
	headers := make(http.Header)
	headers.Add(TunnelGroupHeader, "0123456789abcdef")
	headers.Add(TunnelGroupHeader, "fedcba9876543210")
	headers.Set(TunnelLaneHeader, "0")
	if _, _, err := ParseTunnelLaneHeaders(headers, false); err == nil {
		t.Fatal("duplicate tunnel group accepted")
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

func TestNewAggregateYamuxConfigUsesVersionedWireContract(t *testing.T) {
	cfg, err := NewAggregateYamuxConfig(nil, DefaultMaxStreamWindowSizeBytes, AggregateAcceptBacklogStreams)
	if err != nil {
		t.Fatal(err)
	}
	want := DefaultAggregateFlowControlConfig()
	if cfg.InitialSessionWindowSize != want.InitialSessionWindow ||
		cfg.MaxSessionWindowSize != want.MaxSessionWindow ||
		cfg.InitialStreamAllocationWindowSize != want.InitialStreamWindow ||
		cfg.MaxStreamAllocationWindowSize != want.MaxStreamWindow ||
		cfg.MaxDataFrameSize != want.MaxDataFrame ||
		cfg.MaxIncomingStreams != AggregateAcceptBacklogStreams {
		t.Fatalf("aggregate config = %#v", cfg)
	}
	if cfg.MaxStreamWindowSize != uint32(DefaultAdaptiveReceiveWindowBytes) {
		t.Fatalf("initial aggregate byte window = %d, want %d", cfg.MaxStreamWindowSize, DefaultAdaptiveReceiveWindowBytes)
	}
	if cfg.MaxDataFrameSize != yamux.AggregateMaxDataFrameSize {
		t.Fatalf("DATA frame = %d, Yamux protocol = %d", cfg.MaxDataFrameSize, yamux.AggregateMaxDataFrameSize)
	}
}

func TestNewAggregateYamuxConfigHonorsSmallerConfiguredByteCeiling(t *testing.T) {
	cfg, err := NewAggregateYamuxConfig(nil, InitialStreamWindowSizeBytes, AggregateAcceptBacklogStreams)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.MaxStreamWindowSize != uint32(InitialStreamWindowSizeBytes) {
		t.Fatalf("initial aggregate byte window = %d, want configured protocol floor %d", cfg.MaxStreamWindowSize, InitialStreamWindowSizeBytes)
	}
	if cfg.InitialStreamAllocationWindowSize != AggregateInitialStreamWindowBytes {
		t.Fatalf("allocation window = %d, want wire contract %d", cfg.InitialStreamAllocationWindowSize, AggregateInitialStreamWindowBytes)
	}
}

func TestNegotiatedControlYamuxConfigRemainsLegacyAndBounded(t *testing.T) {
	cfg, err := NewNegotiatedControlYamuxConfig(nil, DefaultAdaptiveReceiveWindowBytes, NegotiatedControlStreamLimit)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.InitialSessionWindowSize != 0 || cfg.InitialStreamAllocationWindowSize != 0 {
		t.Fatal("control lane unexpectedly enabled aggregate DATA flow control")
	}
	if cfg.MaxIncomingStreams != NegotiatedControlStreamLimit {
		t.Fatalf("control incoming limit = %d, want %d", cfg.MaxIncomingStreams, NegotiatedControlStreamLimit)
	}
}

func TestNegotiatedSessionResourceModelsCoverPoolsAndPendingObjects(t *testing.T) {
	wire := DefaultAggregateFlowControlConfig()
	got, err := AggregateSessionResourceBytes(wire.InitialSessionWindow, AggregateAcceptBacklogStreams)
	if err != nil {
		t.Fatal(err)
	}
	want := int64(yamux.AggregateReceiveWindowMemory(wire.InitialSessionWindow)) +
		AggregateSessionMetadataBytes + AggregateAcceptBacklogStreams*AggregatePendingStreamMetadataBytes
	if got != want {
		t.Fatalf("aggregate session reservation = %d, want %d", got, want)
	}
	if _, err := AggregateSessionResourceBytes(wire.InitialSessionWindow, -1); err == nil {
		t.Fatal("negative pending stream count accepted")
	}
	if _, err := AggregateSessionResourceBytes(wire.InitialSessionWindow, int(^uint(0)>>1)); err == nil {
		t.Fatal("overflowing pending stream count accepted")
	}
	if strconv.IntSize == 64 {
		justOverInt64 := (uint64(^uint64(0)>>1)-uint64(AggregateSessionMetadataBytes))/uint64(AggregatePendingStreamMetadataBytes) + 1
		if _, err := AggregateSessionResourceBytes(wire.InitialSessionWindow, int(justOverInt64)); err == nil {
			t.Fatal("metadata beyond MaxInt64 accepted")
		}
	}

	control, err := NegotiatedControlSessionResourceBytes(DefaultAdaptiveReceiveWindowBytes)
	if err != nil {
		t.Fatal(err)
	}
	receive := int64(yamux.ReceiveWindowMemory(uint32(DefaultAdaptiveReceiveWindowBytes)))
	wantControl := AggregateSessionMetadataBytes + NegotiatedControlStreamLimit*(receive+AggregatePendingStreamMetadataBytes)
	if control != wantControl {
		t.Fatalf("control session reservation = %d, want %d", control, wantControl)
	}
}

func TestAdaptiveMaxStreamWindowSizeIsCoveredByLifetimeCharge(t *testing.T) {
	got, err := AdaptiveMaxStreamWindowSizeBytes(DefaultMaxStreamWindowSizeBytes, DefaultAdaptiveStreamChargeBytes)
	if err != nil {
		t.Fatal(err)
	}
	want := DefaultAdaptiveStreamChargeBytes - AdaptivePerStreamOverheadBytes
	if got != want {
		t.Fatalf("adaptive stream window = %d, want covered %d", got, want)
	}
	if _, err := AdaptiveMaxStreamWindowSizeBytes(DefaultMaxStreamWindowSizeBytes, MinimumAdaptiveStreamChargeBytes-1); err == nil {
		t.Fatal("adaptive stream window accepted a charge without per-stream overhead")
	}
	got, err = AdaptiveMaxStreamWindowSizeBytes(InitialStreamWindowSizeBytes, DefaultMaxStreamWindowSizeBytes)
	if err != nil || got != InitialStreamWindowSizeBytes {
		t.Fatalf("configured smaller adaptive window = %d, err=%v", got, err)
	}
}

func TestNormalizeMaxStreamWindowSizeBytesRejectsUnsafeBounds(t *testing.T) {
	for _, value := range []int64{-1, 1, MaxStreamWindowSizeBytesLimit + 1} {
		if _, err := NormalizeMaxStreamWindowSizeBytes(value); err == nil {
			t.Fatalf("NormalizeMaxStreamWindowSizeBytes(%d) error = nil, want error", value)
		}
	}
}
