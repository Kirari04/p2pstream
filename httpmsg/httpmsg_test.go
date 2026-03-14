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
