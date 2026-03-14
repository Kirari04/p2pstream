package msg

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"

	"github.com/google/uuid"
)

const (
	Version = "0.0.0"
)

type Request struct {
	Version string
	ID      uuid.UUID
	Type    RequestType
	Headers map[string]string
	Body    []byte
	ChunkNr uint32
}

type RequestType int

const (
	RequestTypeHeader RequestType = iota
	RequestTypeBody
	RequestTypeHeaderAndBody
)

func NewRequest(t RequestType, headers map[string]string, body []byte) (*Request, error) {
	id, err := uuid.NewV7()
	if err != nil {
		return nil, err
	}
	return &Request{
		Version: Version,
		ID:      id,
		Type:    t,
		Headers: headers,
		Body:    body,
	}, nil
}

func (r *Request) ToBytes() ([]byte, error) {
	buffer := new(bytes.Buffer)
	// first 5 bytes are the version
	if len(r.Version) != 5 {
		return nil, fmt.Errorf("version must be 5 bytes: %s", r.Version)
	}
	buffer.WriteString(r.Version)

	// next 16 bytes are the uuid
	idBinary, err := r.ID.MarshalBinary()
	if err != nil {
		return nil, err
	}
	buffer.Write(idBinary)

	// next 1 byte is the type
	buffer.WriteByte(byte(r.Type))

	if r.Type == RequestTypeHeader {
		if len(r.Body) > 0 {
			return nil, fmt.Errorf("body not allowed for RequestTypeHeader")
		}
	} else if r.Type == RequestTypeBody {
		if len(r.Headers) > 0 {
			return nil, fmt.Errorf("headers not allowed for RequestTypeBody")
		}
	}

	if r.Type == RequestTypeHeader || r.Type == RequestTypeHeaderAndBody {
		// next 2 bytes are headers count (uint16)
		if err := binary.Write(buffer, binary.BigEndian, uint16(len(r.Headers))); err != nil {
			return nil, err
		}

		// for each header:
		for k, v := range r.Headers {
			// key length (2 bytes) + key
			if err := binary.Write(buffer, binary.BigEndian, uint16(len(k))); err != nil {
				return nil, err
			}
			buffer.WriteString(k)
			// value length (4 bytes) + value
			if err := binary.Write(buffer, binary.BigEndian, uint32(len(v))); err != nil {
				return nil, err
			}
			buffer.WriteString(v)
		}
	}

	if r.Type == RequestTypeBody {
		// chunk nr (4 bytes)
		if err := binary.Write(buffer, binary.BigEndian, r.ChunkNr); err != nil {
			return nil, err
		}
	}

	if r.Type == RequestTypeBody || r.Type == RequestTypeHeaderAndBody {
		// body length (4 bytes) + body
		bodyLen := uint32(len(r.Body))
		if err := binary.Write(buffer, binary.BigEndian, bodyLen); err != nil {
			return nil, err
		}
		buffer.Write(r.Body)
	}

	res := buffer.Bytes()
	// Set a reasonable 64KB limit for non-chunked transfers
	if r.Type == RequestTypeHeaderAndBody && len(res) > 65536 {
		return nil, fmt.Errorf("RequestTypeHeaderAndBody payload exceeds 64KB (65536 bytes), use chunked transfer")
	}

	return res, nil
}

func ParseRequest(data []byte) (*Request, error) {
	reader := bytes.NewReader(data)
	request := &Request{}

	// version
	versionBuf := make([]byte, 5)
	if _, err := io.ReadFull(reader, versionBuf); err != nil {
		return nil, fmt.Errorf("reading version: %v", err)
	}
	request.Version = string(versionBuf)

	// uuid
	idBuf := make([]byte, 16)
	if _, err := io.ReadFull(reader, idBuf); err != nil {
		return nil, fmt.Errorf("reading uuid: %v", err)
	}
	id, err := uuid.FromBytes(idBuf)
	if err != nil {
		return nil, fmt.Errorf("parsing uuid: %v", err)
	}
	request.ID = id

	// type
	reqType, err := reader.ReadByte()
	if err != nil {
		return nil, fmt.Errorf("reading type: %v", err)
	}
	request.Type = RequestType(reqType)

	if request.Type == RequestTypeHeader || request.Type == RequestTypeHeaderAndBody {
		// headers count (uint16)
		var headerCount uint16
		if err := binary.Read(reader, binary.BigEndian, &headerCount); err != nil {
			return nil, fmt.Errorf("reading headers count: %v", err)
		}
		request.Headers = make(map[string]string)
		for i := 0; i < int(headerCount); i++ {
			// key length (uint16)
			var kLen uint16
			if err := binary.Read(reader, binary.BigEndian, &kLen); err != nil {
				return nil, fmt.Errorf("reading header key len: %v", err)
			}
			// key
			kBuf := make([]byte, kLen)
			if _, err := io.ReadFull(reader, kBuf); err != nil {
				return nil, fmt.Errorf("reading header key: %v", err)
			}
			// value length (uint32)
			var vLen uint32
			if err := binary.Read(reader, binary.BigEndian, &vLen); err != nil {
				return nil, fmt.Errorf("reading header val len: %v", err)
			}
			// value
			vBuf := make([]byte, vLen)
			if _, err := io.ReadFull(reader, vBuf); err != nil {
				return nil, fmt.Errorf("reading header val: %v", err)
			}
			request.Headers[string(kBuf)] = string(vBuf)
		}
	} else {
		request.Headers = make(map[string]string)
	}

	if request.Type == RequestTypeBody {
		if err := binary.Read(reader, binary.BigEndian, &request.ChunkNr); err != nil {
			return nil, fmt.Errorf("reading chunk nr: %v", err)
		}
	}

	if request.Type == RequestTypeBody || request.Type == RequestTypeHeaderAndBody {
		// body length
		var bodyLen uint32
		if err := binary.Read(reader, binary.BigEndian, &bodyLen); err != nil {
			return nil, fmt.Errorf("reading body len: %v", err)
		}
		// body
		request.Body = make([]byte, bodyLen)
		if _, err := io.ReadFull(reader, request.Body); err != nil {
			return nil, fmt.Errorf("reading body: %v", err)
		}
	}

	return request, nil
}
