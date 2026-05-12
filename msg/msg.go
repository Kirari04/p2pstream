package msg

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
	"math"

	"github.com/google/uuid"
)

const (
	Version = "0.1.0"

	MaxHeaderEntries  = 256
	MaxHeaderKeyLen   = 1024
	MaxHeaderValueLen = 32 * 1024
	MaxBodyChunkSize  = 64 * 1024
	MaxFrameSize      = 128 * 1024
)

type Request struct {
	Version string
	ID      uuid.UUID
	Type    RequestType
	Headers map[string][]string
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

func NewRequest(id uuid.UUID, t RequestType, headers map[string][]string, body io.Reader, bodyLen uint32) *Request {
	return &Request{
		Version: Version,
		ID:      id,
		Type:    t,
		Headers: headers,
		Body:    body,
		BodyLen: bodyLen,
	}
}

func (r *Request) WriteTo(w io.Writer) (int64, error) {
	if r == nil {
		return 0, fmt.Errorf("request is nil")
	}
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
	if !validRequestType(r.Type) {
		return 0, fmt.Errorf("unsupported request type: %d", r.Type)
	}
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
		headerEntries, err := flattenHeaders(r.Headers)
		if err != nil {
			return 0, err
		}
		// next 2 bytes are headers count (uint16)
		if err := binary.Write(buffer, binary.BigEndian, uint16(len(headerEntries))); err != nil {
			return 0, err
		}

		// for each header:
		for _, entry := range headerEntries {
			// key length (2 bytes) + key
			if err := binary.Write(buffer, binary.BigEndian, uint16(len(entry.key))); err != nil {
				return 0, err
			}
			buffer.WriteString(entry.key)
			// value length (4 bytes) + value
			if err := binary.Write(buffer, binary.BigEndian, uint32(len(entry.value))); err != nil {
				return 0, err
			}
			buffer.WriteString(entry.value)
			if buffer.Len() > MaxFrameSize {
				return 0, fmt.Errorf("frame exceeds %d bytes", MaxFrameSize)
			}
		}
	}

	if r.Type == RequestTypeBody {
		// chunk nr (4 bytes)
		if err := binary.Write(buffer, binary.BigEndian, r.ChunkNr); err != nil {
			return 0, err
		}
	}

	if r.Type == RequestTypeBody || r.Type == RequestTypeHeaderAndBody {
		if r.BodyLen > MaxBodyChunkSize {
			return 0, fmt.Errorf("body chunk exceeds %d bytes", MaxBodyChunkSize)
		}
		// body length (4 bytes)
		if err := binary.Write(buffer, binary.BigEndian, r.BodyLen); err != nil {
			return 0, err
		}
	}

	if buffer.Len()+int(r.BodyLen) > MaxFrameSize {
		return 0, fmt.Errorf("frame exceeds %d bytes", MaxFrameSize)
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
	bytesRead := 0

	// version
	versionBuf := make([]byte, 5)
	if _, err := io.ReadFull(reader, versionBuf); err != nil {
		return nil, fmt.Errorf("reading version: %v", err)
	}
	bytesRead += len(versionBuf)
	request.Version = string(versionBuf)
	if request.Version != Version {
		return nil, fmt.Errorf("unsupported protocol version: expected %s, got %s", Version, request.Version)
	}

	// uuid
	idBuf := make([]byte, 16)
	if _, err := io.ReadFull(reader, idBuf); err != nil {
		return nil, fmt.Errorf("reading uuid: %v", err)
	}
	bytesRead += len(idBuf)
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
	bytesRead += len(reqType)
	request.Type = RequestType(reqType[0])
	if !validRequestType(request.Type) {
		return nil, fmt.Errorf("unsupported request type: %d", request.Type)
	}

	if request.Type == RequestTypeHeader || request.Type == RequestTypeHeaderAndBody {
		// headers count (uint16)
		var headerCount uint16
		if err := binary.Read(reader, binary.BigEndian, &headerCount); err != nil {
			return nil, fmt.Errorf("reading headers count: %v", err)
		}
		bytesRead += 2
		if headerCount > MaxHeaderEntries {
			return nil, fmt.Errorf("too many headers: %d", headerCount)
		}
		request.Headers = make(map[string][]string)
		for i := 0; i < int(headerCount); i++ {
			// key length (uint16)
			var kLen uint16
			if err := binary.Read(reader, binary.BigEndian, &kLen); err != nil {
				return nil, fmt.Errorf("reading header key len: %v", err)
			}
			bytesRead += 2
			if kLen == 0 || kLen > MaxHeaderKeyLen {
				return nil, fmt.Errorf("invalid header key length: %d", kLen)
			}
			// key
			kBuf := make([]byte, kLen)
			if _, err := io.ReadFull(reader, kBuf); err != nil {
				return nil, fmt.Errorf("reading header key: %v", err)
			}
			bytesRead += int(kLen)
			// value length (uint32)
			var vLen uint32
			if err := binary.Read(reader, binary.BigEndian, &vLen); err != nil {
				return nil, fmt.Errorf("reading header val len: %v", err)
			}
			bytesRead += 4
			if vLen > MaxHeaderValueLen {
				return nil, fmt.Errorf("header value exceeds %d bytes", MaxHeaderValueLen)
			}
			if bytesRead+int(vLen) > MaxFrameSize {
				return nil, fmt.Errorf("frame exceeds %d bytes", MaxFrameSize)
			}
			// value
			vBuf := make([]byte, vLen)
			if _, err := io.ReadFull(reader, vBuf); err != nil {
				return nil, fmt.Errorf("reading header val: %v", err)
			}
			bytesRead += int(vLen)
			request.Headers[string(kBuf)] = append(request.Headers[string(kBuf)], string(vBuf))
		}
	} else {
		request.Headers = make(map[string][]string)
	}

	if request.Type == RequestTypeBody {
		if err := binary.Read(reader, binary.BigEndian, &request.ChunkNr); err != nil {
			return nil, fmt.Errorf("reading chunk nr: %v", err)
		}
		bytesRead += 4
	}

	if request.Type == RequestTypeBody || request.Type == RequestTypeHeaderAndBody {
		// body length
		if err := binary.Read(reader, binary.BigEndian, &request.BodyLen); err != nil {
			return nil, fmt.Errorf("reading body len: %v", err)
		}
		bytesRead += 4
		if request.BodyLen > MaxBodyChunkSize {
			return nil, fmt.Errorf("body chunk exceeds %d bytes", MaxBodyChunkSize)
		}
		if bytesRead+int(request.BodyLen) > MaxFrameSize {
			return nil, fmt.Errorf("frame exceeds %d bytes", MaxFrameSize)
		}
		// Eagerly read the entire body chunk into memory so the underlying reader is fully consumed.
		if request.BodyLen > 0 {
			bodyData := make([]byte, request.BodyLen)
			if _, err := io.ReadFull(reader, bodyData); err != nil {
				return nil, fmt.Errorf("reading body: %v", err)
			}
			request.Body = bytes.NewReader(bodyData)
		}
	}

	return request, nil
}

type headerEntry struct {
	key   string
	value string
}

func flattenHeaders(headers map[string][]string) ([]headerEntry, error) {
	if len(headers) == 0 {
		return nil, nil
	}
	entries := make([]headerEntry, 0, len(headers))
	for k, values := range headers {
		if len(k) == 0 || len(k) > MaxHeaderKeyLen || len(k) > math.MaxUint16 {
			return nil, fmt.Errorf("invalid header key length for %q", k)
		}
		if len(values) == 0 {
			values = []string{""}
		}
		for _, v := range values {
			if len(entries) >= MaxHeaderEntries {
				return nil, fmt.Errorf("too many headers")
			}
			if len(v) > MaxHeaderValueLen {
				return nil, fmt.Errorf("header %q value exceeds %d bytes", k, MaxHeaderValueLen)
			}
			entries = append(entries, headerEntry{key: k, value: v})
		}
	}
	return entries, nil
}

func validRequestType(t RequestType) bool {
	return t == RequestTypeHeader || t == RequestTypeBody || t == RequestTypeHeaderAndBody
}
