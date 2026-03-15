package msg

import (
	"bytes"
	"io"
	"testing"

	"github.com/google/uuid"
)

func TestRequest_WriteTo_ParseRequest(t *testing.T) {
	id := uuid.NewMD5(uuid.NameSpaceDNS, []byte("test"))
	bodyData := []byte("hello world")
	req := &Request{
		Version: "0.0.0",
		ID:      id,
		Type:    RequestTypeHeaderAndBody,
		Headers: map[string]string{"key": "value", "foo": "bar"},
		Body:    bytes.NewReader(bodyData),
		BodyLen: uint32(len(bodyData)),
	}

	var buf bytes.Buffer
	if _, err := req.WriteTo(&buf); err != nil {
		t.Fatalf("WriteTo failed: %v", err)
	}

	parsed, err := ParseRequest(&buf)
	if err != nil {
		t.Fatalf("ParseRequest failed: %v", err)
	}

	if parsed.Version != req.Version {
		t.Errorf("expected version %s, got %s", req.Version, parsed.Version)
	}

	if parsed.ID != req.ID {
		t.Errorf("expected ID %s, got %s", req.ID, parsed.ID)
	}

	if parsed.Type != req.Type {
		t.Errorf("expected type %v, got %v", req.Type, parsed.Type)
	}

	var parsedBody []byte
	if parsed.Body != nil {
		var err error
		parsedBody, err = io.ReadAll(parsed.Body)
		if err != nil {
			t.Fatalf("failed to read parsed body: %v", err)
		}
	} else {
		parsedBody = []byte{}
	}

	if !bytes.Equal(parsedBody, bodyData) {
		t.Errorf("expected body %s, got %s", bodyData, parsedBody)
	}

	if len(parsed.Headers) != len(req.Headers) {
		t.Errorf("expected %d headers, got %d", len(req.Headers), len(parsed.Headers))
	}

	for k, v := range req.Headers {
		if parsed.Headers[k] != v {
			t.Errorf("expected header %s=%s, got %s", k, v, parsed.Headers[k])
		}
	}
}

func TestRequest_WriteTo_ParseRequest_Empty(t *testing.T) {
	id, _ := uuid.NewV7()
	req := &Request{
		Version: "0.0.0",
		ID:      id,
		Type:    RequestTypeBody,
		Headers: map[string]string{},
		Body:    nil,
		BodyLen: 0,
		ChunkNr: 42,
	}

	var buf bytes.Buffer
	if _, err := req.WriteTo(&buf); err != nil {
		t.Fatalf("WriteTo failed: %v", err)
	}

	parsed, err := ParseRequest(&buf)
	if err != nil {
		t.Fatalf("ParseRequest failed: %v", err)
	}

	if parsed.ChunkNr != 42 {
		t.Errorf("expected chunk nr 42, got %d", parsed.ChunkNr)
	}

	if parsed.Version != req.Version {
		t.Errorf("expected version %s, got %s", req.Version, parsed.Version)
	}

	if parsed.ID != req.ID {
		t.Errorf("expected ID %s, got %s", req.ID, parsed.ID)
	}

	if parsed.Type != req.Type {
		t.Errorf("expected type %v, got %v", req.Type, parsed.Type)
	}

	var parsedBody []byte
	if parsed.Body != nil {
		var err error
		parsedBody, err = io.ReadAll(parsed.Body)
		if err != nil {
			t.Fatalf("failed to read parsed body: %v", err)
		}
	} else {
		parsedBody = []byte{}
	}

	if !bytes.Equal(parsedBody, []byte{}) {
		t.Errorf("expected empty body, got %s", parsedBody)
	}

	if len(parsed.Headers) != 0 {
		t.Errorf("expected 0 headers, got %d", len(parsed.Headers))
	}
}

func TestRequest_LargeHeader(t *testing.T) {
	id, _ := uuid.NewV7()
	largeKey := ""
	for i := 0; i < 300; i++ {
		largeKey += "k"
	}
	largeValue := ""
	for i := 0; i < 1000; i++ {
		largeValue += "v"
	}

	req := &Request{
		Version: "0.0.0",
		ID:      id,
		Type:    RequestTypeHeader,
		Headers: map[string]string{largeKey: largeValue},
	}

	var buf bytes.Buffer
	if _, err := req.WriteTo(&buf); err != nil {
		t.Fatalf("WriteTo failed: %v", err)
	}

	parsed, err := ParseRequest(&buf)
	if err != nil {
		t.Fatalf("ParseRequest failed: %v", err)
	}

	if parsed.Headers[largeKey] != largeValue {
		t.Errorf("expected large header value to match")
	}
}

func TestRequest_LargeBody(t *testing.T) {
	id, _ := uuid.NewV7()
	
	// Create a large body (e.g., 5MB)
	bodySize := 5 * 1024 * 1024
	largeBodyData := bytes.Repeat([]byte("b"), bodySize)

	req := &Request{
		Version: "0.0.0",
		ID:      id,
		Type:    RequestTypeBody,
		Headers: map[string]string{},
		Body:    bytes.NewReader(largeBodyData),
		BodyLen: uint32(bodySize),
		ChunkNr: 1,
	}

	var buf bytes.Buffer
	if _, err := req.WriteTo(&buf); err != nil {
		t.Fatalf("WriteTo failed: %v", err)
	}

	parsed, err := ParseRequest(&buf)
	if err != nil {
		t.Fatalf("ParseRequest failed: %v", err)
	}

	if parsed.ChunkNr != 1 {
		t.Errorf("expected chunk nr 1, got %d", parsed.ChunkNr)
	}

	var parsedBody []byte
	if parsed.Body != nil {
		var err error
		parsedBody, err = io.ReadAll(parsed.Body)
		if err != nil {
			t.Fatalf("failed to read parsed body: %v", err)
		}
	} else {
		parsedBody = []byte{}
	}

	if !bytes.Equal(parsedBody, largeBodyData) {
		t.Errorf("expected body to match large body data")
	}
}

func TestRequest_WriteTo_Errors(t *testing.T) {
	id, _ := uuid.NewV7()
	
	// Test header with body error
	req1 := &Request{
		Version: "0.0.0",
		ID:      id,
		Type:    RequestTypeHeader,
		Headers: map[string]string{"k": "v"},
		Body:    bytes.NewReader([]byte("should fail")),
		BodyLen: 11,
	}
	var buf1 bytes.Buffer
	if _, err := req1.WriteTo(&buf1); err == nil {
		t.Errorf("expected error for RequestTypeHeader with body")
	}

	// Test body with header error
	req2 := &Request{
		Version: "0.0.0",
		ID:      id,
		Type:    RequestTypeBody,
		Headers: map[string]string{"k": "v"},
		Body:    bytes.NewReader([]byte("body content")),
		BodyLen: 12,
	}
	var buf2 bytes.Buffer
	if _, err := req2.WriteTo(&buf2); err == nil {
		t.Errorf("expected error for RequestTypeBody with header")
	}

	// Test large RequestTypeHeaderAndBody payload error (64KB limit)
	req3 := &Request{
		Version: "0.0.0",
		ID:      id,
		Type:    RequestTypeHeaderAndBody,
		Headers: map[string]string{},
		Body:    nil, // doesn't matter for the limit check, it will trigger before reading body
		BodyLen: 65537,
	}
	var buf3 bytes.Buffer
	if _, err := req3.WriteTo(&buf3); err == nil {
		t.Errorf("expected error for RequestTypeHeaderAndBody exceeding 64KB")
	}

	// Test invalid version during parsing
	req4 := &Request{
		Version: "1.0.0", // Invalid version
		ID:      id,
		Type:    RequestTypeHeader,
		Headers: map[string]string{},
	}
	var buf4 bytes.Buffer
	if _, err := req4.WriteTo(&buf4); err != nil {
		t.Fatalf("WriteTo failed: %v", err)
	}
	if _, err := ParseRequest(&buf4); err == nil {
		t.Errorf("expected error for unsupported protocol version")
	}
}
