package server

import (
	"fmt"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

type strictProtoJSONCodec struct {
	name string
}

func (c strictProtoJSONCodec) Name() string {
	return c.name
}

func (c strictProtoJSONCodec) Marshal(message any) ([]byte, error) {
	msg, ok := message.(proto.Message)
	if !ok {
		return nil, fmt.Errorf("expected proto message, got %T", message)
	}
	return protojson.MarshalOptions{}.Marshal(msg)
}

func (c strictProtoJSONCodec) MarshalAppend(dst []byte, message any) ([]byte, error) {
	b, err := c.Marshal(message)
	if err != nil {
		return nil, err
	}
	return append(dst, b...), nil
}

func (c strictProtoJSONCodec) MarshalStable(message any) ([]byte, error) {
	return c.Marshal(message)
}

func (c strictProtoJSONCodec) Unmarshal(data []byte, message any) error {
	msg, ok := message.(proto.Message)
	if !ok {
		return fmt.Errorf("expected proto message, got %T", message)
	}
	return protojson.UnmarshalOptions{DiscardUnknown: false}.Unmarshal(data, msg)
}

func (c strictProtoJSONCodec) IsBinary() bool {
	return false
}
