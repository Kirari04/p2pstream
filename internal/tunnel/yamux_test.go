package tunnel

import (
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
		int64(AgentLegacyPendingStreams)*(supplement+AggregatePendingStreamMetadataBytes) +
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
