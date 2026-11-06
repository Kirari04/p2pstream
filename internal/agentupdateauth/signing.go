// Package agentupdateauth defines the stable application-signature protocol
// shared by the management server and the unprivileged/root updater workers.
package agentupdateauth

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"strconv"
)

const Domain = "p2pstream-agent-update-control-v1\x00"

type Report struct {
	AgentPublicID      string
	Counter            uint64
	AssignmentID       int64
	Generation         int64
	State              int32
	ManifestSHA256     string
	BinarySHA256       string
	RunningVersion     string
	RunningCommit      string
	FailureCode        string
	FailureDetail      string
	ActivationCounter  uint64
	ActivationNonce    []byte
	ActivatorSignature []byte
}

type Activation struct {
	AgentPublicID   string
	AssignmentID    int64
	Generation      int64
	Counter         uint64
	Nonce           []byte
	ManifestSHA256  string
	TargetVersion   string
	TargetCommit    string
	ReleaseSequence int64
	SecurityEpoch   int64
	OS              string
	Arch            string
	ArtifactName    string
	ArtifactSize    int64
	ArtifactSHA256  string
}

func CheckPayload(agentPublicID string, counter uint64) []byte {
	return record("updater-check", agentPublicID, strconv.FormatUint(counter, 10))
}

func ReportPayload(value Report) []byte {
	return record("updater-report", value.AgentPublicID, strconv.FormatUint(value.Counter, 10), strconv.FormatInt(value.AssignmentID, 10), strconv.FormatInt(value.Generation, 10), strconv.FormatInt(int64(value.State), 10), value.ManifestSHA256, value.BinarySHA256, value.RunningVersion, value.RunningCommit, value.FailureCode, value.FailureDetail, strconv.FormatUint(value.ActivationCounter, 10), hex.EncodeToString(value.ActivationNonce), hex.EncodeToString(value.ActivatorSignature))
}

func ActivationPayload(value Activation) []byte {
	return record("root-activation", value.AgentPublicID, strconv.FormatInt(value.AssignmentID, 10), strconv.FormatInt(value.Generation, 10), strconv.FormatUint(value.Counter, 10), hex.EncodeToString(value.Nonce), value.ManifestSHA256, value.TargetVersion, value.TargetCommit, strconv.FormatInt(value.ReleaseSequence, 10), strconv.FormatInt(value.SecurityEpoch, 10), value.OS, value.Arch, value.ArtifactName, strconv.FormatInt(value.ArtifactSize, 10), value.ArtifactSHA256)
}

func record(method string, values ...string) []byte {
	var out bytes.Buffer
	out.WriteString(Domain)
	write := func(value string) {
		_ = binary.Write(&out, binary.BigEndian, uint32(len(value)))
		out.WriteString(value)
	}
	write(method)
	for _, value := range values {
		write(value)
	}
	return out.Bytes()
}
