package httpmsg

import (
	"bytes"
	"io"
	"net/http"
	"testing"

	"github.com/google/uuid"
	"p2pstream/msg"
)

// ArrayStream is a simple MessageStream that yields pre-recorded messages.
type ArrayStream struct {
	messages []*msg.Request
	pos      int
}

func (s *ArrayStream) Next() (*msg.Request, error) {
	if s.pos >= len(s.messages) {
		return nil, io.EOF
	}
	m := s.messages[s.pos]
	s.pos++
	return m, nil
}

func TestEncoderDecoder_SmallRequest(t *testing.T) {
	id, _ := uuid.NewV7()
	bodyData := []byte("hello world, small payload")

	req, _ := http.NewRequest("POST", "http://example.com/upload", bytes.NewReader(bodyData))
	req.Header.Set("Content-Type", "text/plain")
	req.Header.Set("X-Custom", "abc")

	enc := NewRequestEncoder(id, req)

	// Consume encoder
	var msgs []*msg.Request
	for {
		m, err := enc.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("encoder error: %v", err)
		}
		msgs = append(msgs, m)
	}

	if len(msgs) != 1 {
		t.Fatalf("expected 1 message for small payload, got %d", len(msgs))
	}
	if msgs[0].Type != msg.RequestTypeHeaderAndBody {
		t.Fatalf("expected HeaderAndBody type, got %v", msgs[0].Type)
	}

	// Decode back
	stream := &ArrayStream{messages: msgs[1:]} // remaining messages
	decReq, err := DecodeRequest(msgs[0], stream)
	if err != nil {
		t.Fatalf("DecodeRequest failed: %v", err)
	}

	if decReq.Method != "POST" {
		t.Errorf("expected POST, got %s", decReq.Method)
	}
	if decReq.Host != "example.com" {
		t.Errorf("expected example.com, got %s", decReq.Host)
	}
	if decReq.URL.Path != "/upload" {
		t.Errorf("expected /upload, got %s", decReq.URL.Path)
	}
	if decReq.Header.Get("Content-Type") != "text/plain" {
		t.Errorf("expected text/plain, got %s", decReq.Header.Get("Content-Type"))
	}
	if decReq.Header.Get("X-Custom") != "abc" {
		t.Errorf("expected abc, got %s", decReq.Header.Get("X-Custom"))
	}

	decBody, _ := io.ReadAll(decReq.Body)
	if !bytes.Equal(decBody, bodyData) {
		t.Errorf("expected body %q, got %q", string(bodyData), string(decBody))
	}
}

func TestEncoderDecoder_LargeRequest(t *testing.T) {
	id, _ := uuid.NewV7()

	// Create a large body (~150KB) which requires chunking
	bodySize := 150 * 1024
	largeBodyData := bytes.Repeat([]byte("a"), bodySize)

	req, _ := http.NewRequest("PUT", "http://example.com/large", bytes.NewReader(largeBodyData))
	req.Header.Set("Content-Type", "application/octet-stream")

	enc := NewRequestEncoder(id, req)

	// Consume encoder
	var msgs []*msg.Request
	for {
		m, err := enc.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("encoder error: %v", err)
		}
		msgs = append(msgs, m)
	}

	if len(msgs) < 3 {
		t.Fatalf("expected multiple messages for large payload, got %d", len(msgs))
	}
	if msgs[0].Type != msg.RequestTypeHeader {
		t.Fatalf("expected Header type for first message, got %v", msgs[0].Type)
	}
	for i := 1; i < len(msgs); i++ {
		if msgs[i].Type != msg.RequestTypeBody {
			t.Fatalf("expected Body type for chunk %d, got %v", i, msgs[i].Type)
		}
	}

	// Decode back
	stream := &ArrayStream{messages: msgs[1:]} // remaining messages
	decReq, err := DecodeRequest(msgs[0], stream)
	if err != nil {
		t.Fatalf("DecodeRequest failed: %v", err)
	}

	decBody, err := io.ReadAll(decReq.Body)
	if err != nil {
		t.Fatalf("read body failed: %v", err)
	}

	if !bytes.Equal(decBody, largeBodyData) {
		t.Errorf("expected body to match large body data")
	}
}

func TestEncoderDecoder_PreservesRepeatedCommaHeaders(t *testing.T) {
	id, _ := uuid.NewV7()
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header: http.Header{
			"Set-Cookie": {
				"session=abc; Expires=Wed, 21 Oct 2030 07:28:00 GMT; Path=/",
				"theme=dark; Path=/",
			},
		},
		Body:          io.NopCloser(bytes.NewReader(nil)),
		ContentLength: 0,
	}

	enc := NewResponseEncoder(id, resp)
	first, err := enc.Next()
	if err != nil {
		t.Fatalf("encode response: %v", err)
	}
	decoded, err := DecodeResponse(first, &ArrayStream{})
	if err != nil {
		t.Fatalf("decode response: %v", err)
	}
	got := decoded.Header.Values("Set-Cookie")
	want := resp.Header.Values("Set-Cookie")
	if len(got) != len(want) {
		t.Fatalf("Set-Cookie values = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("Set-Cookie[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestDecodeRequestValidatesPseudoHeaders(t *testing.T) {
	id, _ := uuid.NewV7()
	baseHeaders := map[string][]string{
		":method": {"GET"},
		":path":   {"/"},
		":scheme": {"http"},
		":host":   {"example.com"},
	}
	for _, tc := range []struct {
		name    string
		mutate  func(map[string][]string)
		wantErr bool
	}{
		{name: "valid"},
		{name: "missing method", mutate: func(h map[string][]string) { delete(h, ":method") }, wantErr: true},
		{name: "missing host", mutate: func(h map[string][]string) { delete(h, ":host") }, wantErr: true},
		{name: "invalid scheme", mutate: func(h map[string][]string) { h[":scheme"] = []string{"ftp"} }, wantErr: true},
		{name: "bad path", mutate: func(h map[string][]string) { h[":path"] = []string{"http://[::1"} }, wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			headers := make(map[string][]string, len(baseHeaders))
			for k, v := range baseHeaders {
				headers[k] = append([]string(nil), v...)
			}
			if tc.mutate != nil {
				tc.mutate(headers)
			}
			_, err := DecodeRequest(msg.NewRequest(id, msg.RequestTypeHeaderAndBody, headers, nil, 0), &ArrayStream{})
			if tc.wantErr && err == nil {
				t.Fatal("expected error")
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}

func TestDecodeResponseValidatesStatus(t *testing.T) {
	id, _ := uuid.NewV7()
	for _, status := range []string{"", "99", "1000", "abc"} {
		t.Run(status, func(t *testing.T) {
			headers := map[string][]string{":status": {status}}
			_, err := DecodeResponse(msg.NewRequest(id, msg.RequestTypeHeaderAndBody, headers, nil, 0), &ArrayStream{})
			if err == nil {
				t.Fatal("expected invalid status to fail")
			}
		})
	}
}

func TestEncoderRejectsSmallBodyContentLengthLies(t *testing.T) {
	id, _ := uuid.NewV7()
	body := bytes.Repeat([]byte("x"), MaxBodyChunkSize+1)
	req, err := http.NewRequest(http.MethodPost, "http://example.com/upload", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.ContentLength = 1

	enc := NewRequestEncoder(id, req)
	if _, err := enc.Next(); err == nil {
		t.Fatal("expected oversized body read through small content length to fail")
	}
}
