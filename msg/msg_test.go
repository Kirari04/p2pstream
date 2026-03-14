package msg

import (
	"bytes"
	"testing"

	"github.com/google/uuid"
)

func TestRequest_ToBytes_ParseRequest(t *testing.T) {
	id := uuid.NewMD5(uuid.NameSpaceDNS, []byte("test"))
	req := &Request{
		Version: "0.0.0",
		ID:      id,
		Type:    RequestTypeHeaderAndBody,
		Headers: map[string]string{"key": "value", "foo": "bar"},
		Body:    []byte("hello world"),
	}

	data, err := req.ToBytes()
	if err != nil {
		t.Fatalf("ToBytes failed: %v", err)
	}

	parsed, err := ParseRequest(data)
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

	if !bytes.Equal(parsed.Body, req.Body) {
		t.Errorf("expected body %s, got %s", req.Body, parsed.Body)
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

func TestRequest_ToBytes_ParseRequest_Empty(t *testing.T) {
	id, _ := uuid.NewV7()
	req := &Request{
		Version: "0.0.0",
		ID:      id,
		Type:    RequestTypeBody,
		Headers: map[string]string{},
		Body:    []byte{},
		ChunkNr: 42,
	}

	data, err := req.ToBytes()
	if err != nil {
		t.Fatalf("ToBytes failed: %v", err)
	}

	parsed, err := ParseRequest(data)
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

	if !bytes.Equal(parsed.Body, req.Body) {
		t.Errorf("expected body %s, got %s", req.Body, parsed.Body)
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
		// No body for RequestTypeHeader
	}

	data, err := req.ToBytes()
	if err != nil {
		t.Fatalf("ToBytes failed: %v", err)
	}

	parsed, err := ParseRequest(data)
	if err != nil {
		t.Fatalf("ParseRequest failed: %v", err)
	}

	if parsed.Headers[largeKey] != largeValue {
		t.Errorf("expected large header value to match")
	}
}

func TestRequest_ToBytes_Errors(t *testing.T) {
	id, _ := uuid.NewV7()
	
	// Test header with body error
	req1 := &Request{
		Version: "0.0.0",
		ID:      id,
		Type:    RequestTypeHeader,
		Headers: map[string]string{"k": "v"},
		Body:    []byte("should fail"),
	}
	if _, err := req1.ToBytes(); err == nil {
		t.Errorf("expected error for RequestTypeHeader with body")
	}

	// Test body with header error
	req2 := &Request{
		Version: "0.0.0",
		ID:      id,
		Type:    RequestTypeBody,
		Headers: map[string]string{"k": "v"},
		Body:    []byte("body content"),
	}
	if _, err := req2.ToBytes(); err == nil {
		t.Errorf("expected error for RequestTypeBody with header")
	}

	// Test large RequestTypeHeaderAndBody payload error (64KB limit)
	largeBody := bytes.Repeat([]byte("a"), 65537)
	req3 := &Request{
		Version: "0.0.0",
		ID:      id,
		Type:    RequestTypeHeaderAndBody,
		Headers: map[string]string{},
		Body:    largeBody,
	}
	if _, err := req3.ToBytes(); err == nil {
		t.Errorf("expected error for RequestTypeHeaderAndBody exceeding 64KB")
	}
}
