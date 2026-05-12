package msg

import (
	"bytes"
	"encoding/binary"
	"io"
	"testing"

	"github.com/google/uuid"
)

func TestRequest_WriteTo_ParseRequest(t *testing.T) {
	id := uuid.NewMD5(uuid.NameSpaceDNS, []byte("test"))
	bodyData := []byte("hello world")
	req := &Request{
		Version: Version,
		ID:      id,
		Type:    RequestTypeHeaderAndBody,
		Headers: map[string][]string{"key": {"value"}, "foo": {"bar"}},
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
		if !equalStringSlices(parsed.Headers[k], v) {
			t.Errorf("expected header %s=%v, got %v", k, v, parsed.Headers[k])
		}
	}
}

func TestRequest_WriteTo_ParseRequest_RepeatedHeaders(t *testing.T) {
	id := uuid.NewMD5(uuid.NameSpaceDNS, []byte("repeated"))
	req := &Request{
		Version: Version,
		ID:      id,
		Type:    RequestTypeHeader,
		Headers: map[string][]string{
			"Set-Cookie": {"a=1; Path=/", "b=2; Path=/"},
		},
	}

	var buf bytes.Buffer
	if _, err := req.WriteTo(&buf); err != nil {
		t.Fatalf("WriteTo failed: %v", err)
	}
	parsed, err := ParseRequest(&buf)
	if err != nil {
		t.Fatalf("ParseRequest failed: %v", err)
	}
	if !equalStringSlices(parsed.Headers["Set-Cookie"], req.Headers["Set-Cookie"]) {
		t.Fatalf("Set-Cookie headers = %v, want %v", parsed.Headers["Set-Cookie"], req.Headers["Set-Cookie"])
	}
}

func TestRequest_WriteTo_ParseRequest_Empty(t *testing.T) {
	id, _ := uuid.NewV7()
	req := &Request{
		Version: Version,
		ID:      id,
		Type:    RequestTypeBody,
		Headers: map[string][]string{},
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
		Version: Version,
		ID:      id,
		Type:    RequestTypeHeader,
		Headers: map[string][]string{largeKey: {largeValue}},
	}

	var buf bytes.Buffer
	if _, err := req.WriteTo(&buf); err != nil {
		t.Fatalf("WriteTo failed: %v", err)
	}

	parsed, err := ParseRequest(&buf)
	if err != nil {
		t.Fatalf("ParseRequest failed: %v", err)
	}

	if got := parsed.Headers[largeKey]; len(got) != 1 || got[0] != largeValue {
		t.Errorf("expected large header value to match")
	}
}

func TestRequest_RejectsOversizedBodyChunk(t *testing.T) {
	id, _ := uuid.NewV7()

	bodySize := MaxBodyChunkSize + 1
	largeBodyData := bytes.Repeat([]byte("b"), bodySize)

	req := &Request{
		Version: Version,
		ID:      id,
		Type:    RequestTypeBody,
		Headers: map[string][]string{},
		Body:    bytes.NewReader(largeBodyData),
		BodyLen: uint32(bodySize),
		ChunkNr: 1,
	}

	var buf bytes.Buffer
	if _, err := req.WriteTo(&buf); err == nil {
		t.Fatalf("expected oversized body chunk to fail")
	}
}

func TestRequest_WriteTo_Errors(t *testing.T) {
	id, _ := uuid.NewV7()

	// Test header with body error
	req1 := &Request{
		Version: Version,
		ID:      id,
		Type:    RequestTypeHeader,
		Headers: map[string][]string{"k": {"v"}},
		Body:    bytes.NewReader([]byte("should fail")),
		BodyLen: 11,
	}
	var buf1 bytes.Buffer
	if _, err := req1.WriteTo(&buf1); err == nil {
		t.Errorf("expected error for RequestTypeHeader with body")
	}

	// Test body with header error
	req2 := &Request{
		Version: Version,
		ID:      id,
		Type:    RequestTypeBody,
		Headers: map[string][]string{"k": {"v"}},
		Body:    bytes.NewReader([]byte("body content")),
		BodyLen: 12,
	}
	var buf2 bytes.Buffer
	if _, err := req2.WriteTo(&buf2); err == nil {
		t.Errorf("expected error for RequestTypeBody with header")
	}

	// Test large RequestTypeHeaderAndBody payload error (64KB limit)
	req3 := &Request{
		Version: Version,
		ID:      id,
		Type:    RequestTypeHeaderAndBody,
		Headers: map[string][]string{},
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
		Headers: map[string][]string{},
	}
	var buf4 bytes.Buffer
	if _, err := req4.WriteTo(&buf4); err != nil {
		t.Fatalf("WriteTo failed: %v", err)
	}
	if _, err := ParseRequest(&buf4); err == nil {
		t.Errorf("expected error for unsupported protocol version")
	}
}

func TestRequest_WriteTo_RejectsTooManyHeaders(t *testing.T) {
	id, _ := uuid.NewV7()
	headers := make(map[string][]string)
	for i := 0; i < MaxHeaderEntries+1; i++ {
		headers[string(rune('a'+(i%26)))+string(rune('A'+(i/26)))] = []string{"v"}
	}
	req := &Request{
		Version: Version,
		ID:      id,
		Type:    RequestTypeHeader,
		Headers: headers,
	}
	var buf bytes.Buffer
	if _, err := req.WriteTo(&buf); err == nil {
		t.Fatal("expected too many headers to fail")
	}
}

func TestRequest_WriteTo_RejectsOversizedHeaderKeyAndValue(t *testing.T) {
	id, _ := uuid.NewV7()
	for _, tc := range []struct {
		name    string
		headers map[string][]string
	}{
		{
			name:    "key",
			headers: map[string][]string{string(bytes.Repeat([]byte("k"), MaxHeaderKeyLen+1)): {"v"}},
		},
		{
			name:    "value",
			headers: map[string][]string{"k": {string(bytes.Repeat([]byte("v"), MaxHeaderValueLen+1))}},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := &Request{
				Version: Version,
				ID:      id,
				Type:    RequestTypeHeader,
				Headers: tc.headers,
			}
			var buf bytes.Buffer
			if _, err := req.WriteTo(&buf); err == nil {
				t.Fatal("expected oversized header to fail")
			}
		})
	}
}

func TestParseRequestRejectsUnknownType(t *testing.T) {
	var buf bytes.Buffer
	buf.WriteString(Version)
	id, _ := uuid.NewV7()
	idBytes, _ := id.MarshalBinary()
	buf.Write(idBytes)
	buf.WriteByte(99)
	if _, err := ParseRequest(&buf); err == nil {
		t.Fatal("expected unknown type to fail")
	}
}

func TestParseRequestRejectsMalformedEOF(t *testing.T) {
	var buf bytes.Buffer
	writeParseTestPrefix(t, &buf, RequestTypeHeader)
	if err := binary.Write(&buf, binary.BigEndian, uint16(1)); err != nil {
		t.Fatalf("write header count: %v", err)
	}
	if err := binary.Write(&buf, binary.BigEndian, uint16(3)); err != nil {
		t.Fatalf("write key len: %v", err)
	}
	buf.WriteString("ab")
	if _, err := ParseRequest(&buf); err == nil {
		t.Fatal("expected malformed EOF to fail")
	}
}

func TestParseRequestRejectsOversizedHeaderAndBody(t *testing.T) {
	t.Run("too many headers", func(t *testing.T) {
		var buf bytes.Buffer
		writeParseTestPrefix(t, &buf, RequestTypeHeader)
		if err := binary.Write(&buf, binary.BigEndian, uint16(MaxHeaderEntries+1)); err != nil {
			t.Fatalf("write header count: %v", err)
		}
		if _, err := ParseRequest(&buf); err == nil {
			t.Fatal("expected too many headers to fail")
		}
	})

	t.Run("large value", func(t *testing.T) {
		var buf bytes.Buffer
		writeParseTestPrefix(t, &buf, RequestTypeHeader)
		if err := binary.Write(&buf, binary.BigEndian, uint16(1)); err != nil {
			t.Fatalf("write header count: %v", err)
		}
		if err := binary.Write(&buf, binary.BigEndian, uint16(1)); err != nil {
			t.Fatalf("write key len: %v", err)
		}
		buf.WriteByte('k')
		if err := binary.Write(&buf, binary.BigEndian, uint32(MaxHeaderValueLen+1)); err != nil {
			t.Fatalf("write value len: %v", err)
		}
		if _, err := ParseRequest(&buf); err == nil {
			t.Fatal("expected oversized value to fail")
		}
	})

	t.Run("large body", func(t *testing.T) {
		var buf bytes.Buffer
		writeParseTestPrefix(t, &buf, RequestTypeBody)
		if err := binary.Write(&buf, binary.BigEndian, uint32(0)); err != nil {
			t.Fatalf("write chunk nr: %v", err)
		}
		if err := binary.Write(&buf, binary.BigEndian, uint32(MaxBodyChunkSize+1)); err != nil {
			t.Fatalf("write body len: %v", err)
		}
		if _, err := ParseRequest(&buf); err == nil {
			t.Fatal("expected oversized body to fail")
		}
	})
}

func writeParseTestPrefix(t *testing.T, buf *bytes.Buffer, typ RequestType) {
	t.Helper()
	buf.WriteString(Version)
	id, _ := uuid.NewV7()
	idBytes, err := id.MarshalBinary()
	if err != nil {
		t.Fatalf("marshal id: %v", err)
	}
	buf.Write(idBytes)
	buf.WriteByte(byte(typ))
}

func equalStringSlices(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
