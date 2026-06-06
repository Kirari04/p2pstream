package tunnel

import (
	"bytes"
	"encoding/binary"
	"strings"
	"testing"
)

func TestOpenRequestFrameRoundTrip(t *testing.T) {
	want := NewOpenRequest("req-1", "tcp", "127.0.0.1:8080")
	var buf bytes.Buffer
	if err := WriteOpenRequest(&buf, want); err != nil {
		t.Fatalf("WriteOpenRequest() error = %v", err)
	}
	got, err := ReadOpenRequest(&buf)
	if err != nil {
		t.Fatalf("ReadOpenRequest() error = %v", err)
	}
	if got != want {
		t.Fatalf("ReadOpenRequest() = %+v, want %+v", got, want)
	}
}

func TestOpenResponseFrameRoundTrip(t *testing.T) {
	want := OpenResponse{OK: false, ErrorKind: "dial_failed", Error: "connection refused"}
	var buf bytes.Buffer
	if err := WriteOpenResponse(&buf, want); err != nil {
		t.Fatalf("WriteOpenResponse() error = %v", err)
	}
	got, err := ReadOpenResponse(&buf)
	if err != nil {
		t.Fatalf("ReadOpenResponse() error = %v", err)
	}
	if got != want {
		t.Fatalf("ReadOpenResponse() = %+v, want %+v", got, want)
	}
}

func TestReadFrameRejectsOversizedFrame(t *testing.T) {
	var buf bytes.Buffer
	var header [4]byte
	binary.BigEndian.PutUint32(header[:], MaxControlFrameBytes+1)
	buf.Write(header[:])
	if err := ReadFrame(&buf, &OpenResponse{}); err == nil {
		t.Fatal("ReadFrame() error = nil, want oversized frame error")
	}
}

func TestReadFrameRejectsMalformedJSON(t *testing.T) {
	var buf bytes.Buffer
	data := []byte("{")
	var header [4]byte
	binary.BigEndian.PutUint32(header[:], uint32(len(data)))
	buf.Write(header[:])
	buf.Write(data)
	if err := ReadFrame(&buf, &OpenResponse{}); err == nil {
		t.Fatal("ReadFrame() error = nil, want malformed JSON error")
	}
}

func TestWriteFrameRejectsOversizedPayload(t *testing.T) {
	payload := OpenResponse{OK: false, Error: strings.Repeat("x", MaxControlFrameBytes)}
	if err := WriteFrame(&bytes.Buffer{}, payload); err == nil {
		t.Fatal("WriteFrame() error = nil, want oversized payload error")
	}
}
