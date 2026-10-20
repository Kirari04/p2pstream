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
}
