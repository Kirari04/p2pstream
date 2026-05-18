package httpmsg

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"strconv"

	"github.com/google/uuid"
	"p2pstream/msg"
)

const (
	MaxBodyChunkSize                    = msg.MaxBodyChunkSize
	MetadataTLSSkipVerify               = ":p2pstream-tls-skip-verify"
	MetadataHealthCheck                 = ":p2pstream-health-check"
	MetadataDiscoverCertificate         = ":p2pstream-discover-certificate"
	MetadataTrustedCertificatePEM       = ":p2pstream-trusted-cert-pem"
	MetadataTrustedCertificateSHA256    = ":p2pstream-trusted-cert-sha256"
	MetadataResponseHeaderTimeoutMillis = ":p2pstream-response-header-timeout-ms"
)

// Encoder takes an HTTP request or response and yields a sequence of msg.Request chunks.
type Encoder struct {
	id         uuid.UUID
	headers    map[string][]string
	bodyReader io.Reader
	bodyLen    int64
	chunkNr    uint32
	isFirst    bool
	isDone     bool
	eofSent    bool
}

// NewRequestEncoder creates a new encoder from an http.Request.
func NewRequestEncoder(id uuid.UUID, req *http.Request) *Encoder {
	return NewRequestEncoderWithMetadata(id, req, nil)
}

// NewRequestEncoderWithMetadata creates a request encoder with internal
// colon-prefixed metadata that is consumed by the agent and not forwarded.
func NewRequestEncoderWithMetadata(id uuid.UUID, req *http.Request, metadata map[string]string) *Encoder {
	headers := make(map[string][]string)
	headers[":method"] = []string{req.Method}
	if req.URL != nil {
		headers[":path"] = []string{req.URL.RequestURI()}
		if req.URL.Scheme != "" {
			headers[":scheme"] = []string{req.URL.Scheme}
		} else {
			// Fallback to http if not set
			headers[":scheme"] = []string{"http"}
		}
	} else {
		headers[":path"] = []string{"/"}
		headers[":scheme"] = []string{"http"}
	}
	host := req.Host
	if host == "" && req.URL != nil {
		host = req.URL.Host
	}
	headers[":host"] = []string{host}

	for k, vv := range req.Header {
		headers[k] = append([]string(nil), vv...)
	}
	for k, v := range metadata {
		if len(k) > 0 && k[0] == ':' {
			headers[k] = []string{v}
		}
	}

	return &Encoder{
		id:         id,
		headers:    headers,
		bodyReader: req.Body,
		bodyLen:    req.ContentLength,
		isFirst:    true,
	}
}

// NewResponseEncoder creates a new encoder from an http.Response.
func NewResponseEncoder(id uuid.UUID, resp *http.Response) *Encoder {
	headers := make(map[string][]string)
	headers[":status"] = []string{strconv.Itoa(resp.StatusCode)}

	for k, vv := range resp.Header {
		headers[k] = append([]string(nil), vv...)
	}

	return &Encoder{
		id:         id,
		headers:    headers,
		bodyReader: resp.Body,
		bodyLen:    resp.ContentLength,
		isFirst:    true,
	}
}

// Next yields the next msg.Request in the chunked sequence.
// Returns io.EOF when the body is fully consumed.
func (e *Encoder) Next() (*msg.Request, error) {
	if e.eofSent {
		return nil, io.EOF
	}

	if e.isFirst {
		e.isFirst = false

		// If we know the content length is small enough, we can send it all in one go
		if e.bodyLen >= 0 && e.bodyLen <= MaxBodyChunkSize {
			var bodyBuf bytes.Buffer
			if e.bodyReader != nil {
				_, err := io.Copy(&bodyBuf, io.LimitReader(e.bodyReader, MaxBodyChunkSize+1))
				if err != nil {
					return nil, err
				}
				if bodyBuf.Len() > MaxBodyChunkSize {
					return nil, fmt.Errorf("body exceeds %d bytes", MaxBodyChunkSize)
				}
			}
			e.isDone = true
			e.eofSent = true
			req := msg.NewRequest(e.id, msg.RequestTypeHeaderAndBody, e.headers, bytes.NewReader(bodyBuf.Bytes()), uint32(bodyBuf.Len()))
			return req, nil
		}

		// Otherwise, send the header first
		req := msg.NewRequest(e.id, msg.RequestTypeHeader, e.headers, nil, 0)
		if e.bodyReader == nil {
			e.isDone = true
		}
		return req, nil
	}

	if e.isDone {
		e.eofSent = true
		req := msg.NewRequest(e.id, msg.RequestTypeBody, map[string][]string{}, nil, 0)
		req.ChunkNr = e.chunkNr
		return req, nil
	}

	// Read next chunk
	chunk := make([]byte, MaxBodyChunkSize)
	n, err := e.bodyReader.Read(chunk)
	if err != nil && err != io.EOF {
		return nil, err
	}

	if n == 0 && err == io.EOF {
		e.isDone = true
		e.eofSent = true
		req := msg.NewRequest(e.id, msg.RequestTypeBody, map[string][]string{}, nil, 0)
		req.ChunkNr = e.chunkNr
		return req, nil
	}

	req := msg.NewRequest(e.id, msg.RequestTypeBody, map[string][]string{}, bytes.NewReader(chunk[:n]), uint32(n))
	req.ChunkNr = e.chunkNr
	e.chunkNr++

	if err == io.EOF {
		e.isDone = true
	}

	return req, nil
}
