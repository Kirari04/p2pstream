package tunnel

import (
	"math"
	"testing"

	"github.com/hashicorp/yamux"
)

func TestOwnedStreamResourceModelsSeparateLogicalAndPhysicalSockets(t *testing.T) {
	server, err := ServerStreamResourceModel(0)
	if err != nil {
		t.Fatal(err)
	}
	agent, err := AgentStreamResourceModel(0, 0)
	if err != nil {
		t.Fatal(err)
	}
	wantReceiveFloor := int64(yamux.ReceiveWindowMemory(uint32(DefaultAdaptiveReceiveWindowBytes)))
	if server.FileDescriptors != 0 || server.MemoryBytes < wantReceiveFloor+AdaptiveServerStreamOverheadBytes {
		t.Fatalf("server model = %+v, receive floor = %d", server, wantReceiveFloor)
	}
	if agent.FileDescriptors != 2 || agent.MemoryBytes != server.MemoryBytes+512*1024 {
		t.Fatalf("agent model = %+v, server = %+v", agent, server)
	}
	serverBase := ServerStreamBaseResourceModel()
	agentBase, err := AgentStreamBaseResourceModel(0)
	if err != nil {
		t.Fatal(err)
	}
	if serverBase.MemoryBytes != AdaptiveServerStreamOverheadBytes || serverBase.FileDescriptors != 0 {
		t.Fatalf("server aggregate base = %+v", serverBase)
	}
	if agentBase.MemoryBytes != serverBase.MemoryBytes+512*1024 || agentBase.FileDescriptors != 2 {
		t.Fatalf("agent aggregate base = %+v, server base = %+v", agentBase, serverBase)
	}
	supplement, err := LegacyReceiveWindowSupplementBytes(0)
	if err != nil {
		t.Fatal(err)
	}
	if serverBase.MemoryBytes+supplement != server.MemoryBytes || agentBase.MemoryBytes+supplement != agent.MemoryBytes {
		t.Fatalf("base+supplement mismatch: server=%d agent=%d supplement=%d", serverBase.MemoryBytes, agentBase.MemoryBytes, supplement)
	}
}

func TestAgentLegacySessionEnvelopeAndIncomingBound(t *testing.T) {
	supplement, err := LegacyReceiveWindowSupplementBytes(DefaultAdaptiveReceiveWindowBytes)
	if err != nil {
		t.Fatal(err)
	}
	got, err := AgentLegacySessionResourceBytes(DefaultAdaptiveReceiveWindowBytes)
	if err != nil {
		t.Fatal(err)
	}
	want := AggregateSessionMetadataBytes +
		int64(AgentLegacyAcceptBacklogStreams)*(int64(yamux.ReceiveWindowMemory(uint32(InitialStreamWindowSizeBytes)))+AggregatePendingStreamMetadataBytes) +
		(supplement + AggregatePendingStreamMetadataBytes) +
		int64(AgentLegacyRejectedStreamLimit)*(supplement+AggregatePendingStreamMetadataBytes)
	if got != want {
		t.Fatalf("legacy session envelope=%d want=%d", got, want)
	}
	cfg, err := NewAgentLegacyYamuxConfig(nil, DefaultAdaptiveReceiveWindowBytes, 2048)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.AcceptBacklog != AgentLegacyAcceptBacklogStreams || cfg.MaxIncomingStreams != 2048+AgentLegacyPendingStreams+AgentLegacyRejectedStreamLimit {
		t.Fatalf("legacy bounds: backlog=%d incoming=%d", cfg.AcceptBacklog, cfg.MaxIncomingStreams)
	}
}

func TestLegacyEnvelopeConstructorSharesInitialCeiling(t *testing.T) {
	for _, configured := range []int64{256 << 10, 512 << 10, 2 << 20, 0, MaxStreamWindowSizeBytesLimit} {
		cfg, err := NewAgentLegacyYamuxConfig(nil, configured, 64)
		if err != nil {
			t.Fatal(err)
		}
		envelope, err := AgentLegacySessionResourceBytes(configured)
		if err != nil {
			t.Fatal(err)
		}
		if cfg.MaxStreamWindowSize > 512<<10 || cfg.AcceptBacklog != 64 || cfg.MaxIncomingStreams != 145 {
			t.Fatalf("unsafe constructor: %+v", cfg)
		}
		want := int64(37268096)
		if configured == 256<<10 {
			want = 32807296
		}
		if envelope != want {
			t.Fatalf("configured%d envelope=%d want%d", configured, envelope, want)
		}
	}
	for _, bad := range []int64{-1, 1, math.MaxInt64} {
		if _, err := AgentLegacySessionResourceBytes(bad); err == nil {
			t.Fatal("invalid resource ceiling accepted", bad)
		}
		if _, err := NewAgentLegacyYamuxConfig(nil, bad, 64); err == nil {
			t.Fatal("invalid constructor ceiling accepted", bad)
		}
	}
	for _, bad := range []int{0, -1, int(MaxAdaptiveConcurrentStreamsLimit) + 1} {
		if _, err := NewAgentLegacyYamuxConfig(nil, 512<<10, bad); err == nil {
			t.Fatal("invalid resident cap accepted", bad)
		}
	}
}
