package server

import (
	"fmt"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/proto"
)

func rejectRemovedProtoFields(message proto.Message, fields map[protowire.Number]string) error {
	if message == nil {
		return nil
	}
	raw := message.ProtoReflect().GetUnknown()
	for len(raw) > 0 {
		number, _, n := protowire.ConsumeField(raw)
		if n < 0 {
			return connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("invalid unknown protobuf fields: %w", protowire.ParseError(n)))
		}
		if name, ok := fields[number]; ok {
			return connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("legacy %s was removed; use match_rule", name))
		}
		raw = raw[n:]
	}
	return nil
}

func rejectRemovedPolicyMatchField(message proto.Message, fieldNumber protowire.Number) error {
	return rejectRemovedProtoFields(message, map[protowire.Number]string{fieldNumber: "match"})
}
