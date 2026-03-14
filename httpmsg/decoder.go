package httpmsg

import (
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"p2pstream/msg"
)

// MessageStream is an interface for reading the next message from a stream.
type MessageStream interface {
	Next() (*msg.Request, error)
}

// ChannelStream adapts a <-chan *msg.Request to the MessageStream interface.
type ChannelStream struct {
	Ch <-chan *msg.Request
}

func (c ChannelStream) Next() (*msg.Request, error) {
	m, ok := <-c.Ch
	if !ok {
		return nil, io.EOF
	}
	return m, nil
}

type bodyReader struct {
	stream        MessageStream
	currentBody   io.Reader
	expectedChunk uint32
}

func (b *bodyReader) Read(p []byte) (int, error) {
	for {
		if b.currentBody != nil {
			n, err := b.currentBody.Read(p)
			if n > 0 {
				return n, nil
			}
			if err == io.EOF {
				b.currentBody = nil
				// fall through to fetch the next chunk
			} else if err != nil {
				return n, err
			}
		}

		m, err := b.stream.Next()
		if err != nil {
			return 0, err // Often io.EOF when stream is fully consumed
		}

		if m.Type != msg.RequestTypeBody {
			return 0, fmt.Errorf("expected RequestTypeBody, got %v", m.Type)
		}

		if m.ChunkNr != b.expectedChunk {
			return 0, fmt.Errorf("unexpected chunk nr: expected %d, got %d", b.expectedChunk, m.ChunkNr)
		}
		b.expectedChunk++

		if m.Body != nil {
			b.currentBody = m.Body
		}
	}
}

func (b *bodyReader) Close() error {
	return nil
}

// DecodeRequest reconstructs an http.Request from the first msg.Request.
// It uses stream to fetch subsequent chunks lazily if the body is chunked.
func DecodeRequest(m *msg.Request, stream MessageStream) (*http.Request, error) {
	if m.Type != msg.RequestTypeHeader && m.Type != msg.RequestTypeHeaderAndBody {
		return nil, fmt.Errorf("first message must be Header or HeaderAndBody")
	}

	method := m.Headers[":method"]
	path := m.Headers[":path"]
	host := m.Headers[":host"]

	reqURL, err := url.Parse(path)
	if err != nil {
		return nil, err
	}

	var body io.ReadCloser
	if m.Type == msg.RequestTypeHeaderAndBody {
		if m.Body != nil {
			body = io.NopCloser(m.Body)
		} else {
			body = http.NoBody
		}
	} else {
		if stream != nil {
			body = &bodyReader{stream: stream}
		} else {
			body = http.NoBody
		}
	}

	req := &http.Request{
		Method: method,
		URL:    reqURL,
		Host:   host,
		Header: make(http.Header),
		Body:   body,
	}

	for k, v := range m.Headers {
		if strings.HasPrefix(k, ":") {
			continue
		}
		for _, part := range strings.Split(v, ",") {
			req.Header.Add(k, part)
		}
	}

	return req, nil
}

// DecodeResponse reconstructs an http.Response from the first msg.Request.
// It uses stream to fetch subsequent chunks lazily if the body is chunked.
func DecodeResponse(m *msg.Request, stream MessageStream) (*http.Response, error) {
	if m.Type != msg.RequestTypeHeader && m.Type != msg.RequestTypeHeaderAndBody {
		return nil, fmt.Errorf("first message must be Header or HeaderAndBody")
	}

	statusStr := m.Headers[":status"]
	statusCode, _ := strconv.Atoi(statusStr)

	var body io.ReadCloser
	if m.Type == msg.RequestTypeHeaderAndBody {
		if m.Body != nil {
			body = io.NopCloser(m.Body)
		} else {
			body = http.NoBody
		}
	} else {
		if stream != nil {
			body = &bodyReader{stream: stream}
		} else {
			body = http.NoBody
		}
	}

	resp := &http.Response{
		StatusCode: statusCode,
		Status:     http.StatusText(statusCode),
		Header:     make(http.Header),
		Body:       body,
	}

	for k, v := range m.Headers {
		if strings.HasPrefix(k, ":") {
			continue
		}
		for _, part := range strings.Split(v, ",") {
			resp.Header.Add(k, part)
		}
	}

	return resp, nil
}
