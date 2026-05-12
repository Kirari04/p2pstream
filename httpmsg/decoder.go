package httpmsg

import (
	"context"
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
	Ctx context.Context
	Ch  <-chan *msg.Request
}

func (c ChannelStream) Next() (*msg.Request, error) {
	if c.Ctx == nil {
		m, ok := <-c.Ch
		if !ok {
			return nil, io.EOF
		}
		return m, nil
	}
	select {
	case <-c.Ctx.Done():
		return nil, c.Ctx.Err()
	case m, ok := <-c.Ch:
		if !ok {
			return nil, io.EOF
		}
		return m, nil
	}
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

		if m.BodyLen == 0 {
			return 0, io.EOF
		}

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

	method := FirstHeaderValue(m.Headers, ":method")
	path := FirstHeaderValue(m.Headers, ":path")
	host := FirstHeaderValue(m.Headers, ":host")
	scheme := FirstHeaderValue(m.Headers, ":scheme")
	if method == "" {
		return nil, fmt.Errorf("missing :method")
	}
	if path == "" {
		return nil, fmt.Errorf("missing :path")
	}
	if host == "" {
		return nil, fmt.Errorf("missing :host")
	}
	if scheme == "" {
		scheme = "http"
	}
	if scheme != "http" && scheme != "https" {
		return nil, fmt.Errorf("invalid :scheme %q", scheme)
	}

	reqURL, err := url.Parse(path)
	if err != nil {
		return nil, err
	}
	reqURL.Host = host
	reqURL.Scheme = scheme

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
		if len(v) == 0 {
			req.Header.Add(k, "")
			continue
		}
		for _, part := range v {
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

	statusStr := FirstHeaderValue(m.Headers, ":status")
	statusCode, err := strconv.Atoi(statusStr)
	if err != nil || statusCode < 100 || statusCode > 999 {
		return nil, fmt.Errorf("invalid :status %q", statusStr)
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
		if len(v) == 0 {
			resp.Header.Add(k, "")
			continue
		}
		for _, part := range v {
			resp.Header.Add(k, part)
		}
	}

	return resp, nil
}

func FirstHeaderValue(headers map[string][]string, key string) string {
	values := headers[key]
	if len(values) == 0 {
		return ""
	}
	return values[0]
}
