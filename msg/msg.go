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
	Body    io.Reader
	BodyLen uint32
	ChunkNr uint32
}

type RequestType int

const (
	RequestTypeHeader RequestType = iota
	RequestTypeBody
	RequestTypeHeaderAndBody
)

func NewRequest(t RequestType, headers map[string]string, body io.Reader, bodyLen uint32) (*Request, error) {
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
		BodyLen: bodyLen,
	}, nil
}

func (r *Request) WriteTo(w io.Writer) (int64, error) {
	buffer := new(bytes.Buffer)
	// first 5 bytes are the version
	if len(r.Version) != 5 {
		return 0, fmt.Errorf("version must be 5 bytes: %s", r.Version)
	}
	buffer.WriteString(r.Version)

	// next 16 bytes are the uuid
	idBinary, err := r.ID.MarshalBinary()
	if err != nil {
		return 0, err
	}
	buffer.Write(idBinary)

	// next 1 byte is the type
	buffer.WriteByte(byte(r.Type))

	if r.Type == RequestTypeHeader {
		if r.BodyLen > 0 || r.Body != nil {
			return 0, fmt.Errorf("body not allowed for RequestTypeHeader")
		}
	} else if r.Type == RequestTypeBody {
		if len(r.Headers) > 0 {
			return 0, fmt.Errorf("headers not allowed for RequestTypeBody")
		}
	}

	if r.Type == RequestTypeHeader || r.Type == RequestTypeHeaderAndBody {
		// next 2 bytes are headers count (uint16)
		if err := binary.Write(buffer, binary.BigEndian, uint16(len(r.Headers))); err != nil {
			return 0, err
		}

		// for each header:
		for k, v := range r.Headers {
			// key length (2 bytes) + key
			if err := binary.Write(buffer, binary.BigEndian, uint16(len(k))); err != nil {
				return 0, err
			}
			buffer.WriteString(k)
			// value length (4 bytes) + value
			if err := binary.Write(buffer, binary.BigEndian, uint32(len(v))); err != nil {
				return 0, err
			}
			buffer.WriteString(v)
		}
	}

	if r.Type == RequestTypeBody {
		// chunk nr (4 bytes)
		if err := binary.Write(buffer, binary.BigEndian, r.ChunkNr); err != nil {
			return 0, err
		}
	}

	if r.Type == RequestTypeBody || r.Type == RequestTypeHeaderAndBody {
		// body length (4 bytes)
		if err := binary.Write(buffer, binary.BigEndian, r.BodyLen); err != nil {
			return 0, err
		}
	}

	// Set a reasonable 64KB limit for non-chunked transfers
	if r.Type == RequestTypeHeaderAndBody && (buffer.Len()+int(r.BodyLen)) > 65536 {
		return 0, fmt.Errorf("RequestTypeHeaderAndBody payload exceeds 64KB (65536 bytes), use chunked transfer")
	}

	var totalWritten int64

	// write headers/metadata to output
	n, err := w.Write(buffer.Bytes())
	totalWritten += int64(n)
	if err != nil {
		return totalWritten, err
	}

	// stream body to output
	if r.Type == RequestTypeBody || r.Type == RequestTypeHeaderAndBody {
		if r.Body != nil && r.BodyLen > 0 {
			written, err := io.Copy(w, r.Body)
			totalWritten += written
			if err != nil {
				return totalWritten, err
			}
			if written != int64(r.BodyLen) {
				return totalWritten, fmt.Errorf("expected to write %d bytes, wrote %d", r.BodyLen, written)
			}
		}
	}

	return totalWritten, nil
}

func ParseRequest(reader io.Reader) (*Request, error) {
	request := &Request{}

	// version
	versionBuf := make([]byte, 5)
	if _, err := io.ReadFull(reader, versionBuf); err != nil {
		return nil, fmt.Errorf("reading version: %v", err)
	}
	request.Version = string(versionBuf)
	if request.Version != Version {
		return nil, fmt.Errorf("unsupported protocol version: expected %s, got %s", Version, request.Version)
	}

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
	reqType := make([]byte, 1)
	if _, err := io.ReadFull(reader, reqType); err != nil {
		return nil, fmt.Errorf("reading type: %v", err)
	}
	request.Type = RequestType(reqType[0])

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
		if err := binary.Read(reader, binary.BigEndian, &request.BodyLen); err != nil {
			return nil, fmt.Errorf("reading body len: %v", err)
		}
		// body reader
		request.Body = io.LimitReader(reader, int64(request.BodyLen))
	}

	return request, nil
}
