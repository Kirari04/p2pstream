package agentupdateauth

import (
	"encoding/hex"
	"testing"
)

func TestSigningPayloadVectors(t *testing.T) {
	tests := []struct {
		name string
		got  []byte
		want string
	}{
		{"check", CheckPayload("agent-a", 7), "70327073747265616d2d6167656e742d7570646174652d636f6e74726f6c2d7631000000000d757064617465722d636865636b000000076167656e742d610000000137"},
		{"activation", ActivationPayload(Activation{AgentPublicID: "a", AssignmentID: 2, Generation: 3, Counter: 4, Nonce: []byte{0xaa}, RootVersion: 7, ManifestSHA256: "m", TargetVersion: "v", TargetCommit: "c", ReleaseSequence: 5, SecurityEpoch: 6, OS: "linux", Arch: "amd64", ArtifactName: "agent", ArtifactSize: 8, ArtifactSHA256: "b"}), "70327073747265616d2d6167656e742d7570646174652d636f6e74726f6c2d7631000000000f726f6f742d61637469766174696f6e00000001610000000132000000013300000001340000000261610000000137000000016d0000000176000000016300000001350000000136000000056c696e757800000005616d643634000000056167656e7400000001380000000162"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := hex.EncodeToString(test.got); got != test.want {
				t.Fatalf("payload = %s, want %s", got, test.want)
			}
		})
	}
}
