package server

import (
	"crypto/tls"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"golang.org/x/net/http2"
)

func TestManagementHTTPServerDefaults(t *testing.T) {
	srv := &http.Server{}
	ConfigureManagementHTTPServer(srv)
	if srv.ReadHeaderTimeout != managementReadHeaderTimeout {
		t.Fatalf("ReadHeaderTimeout = %s, want %s", srv.ReadHeaderTimeout, managementReadHeaderTimeout)
	}
	if srv.ReadTimeout != managementReadTimeout {
		t.Fatalf("ReadTimeout = %s, want %s", srv.ReadTimeout, managementReadTimeout)
	}
	if srv.WriteTimeout != managementWriteTimeout {
		t.Fatalf("WriteTimeout = %s, want %s", srv.WriteTimeout, managementWriteTimeout)
	}
	if srv.IdleTimeout != managementIdleTimeout {
		t.Fatalf("IdleTimeout = %s, want %s", srv.IdleTimeout, managementIdleTimeout)
	}
	if srv.MaxHeaderBytes != defaultManagementMaxHeaderBytes {
		t.Fatalf("MaxHeaderBytes = %d, want %d", srv.MaxHeaderBytes, defaultManagementMaxHeaderBytes)
	}
}

// TestPublicHTTP2LargeReceiveWindowIsAdvertisedOnWire records the Go
// implementation behavior that permits receive windows above the public API
// documentation's stale 4 MiB wording. It is intentionally standalone: the
// production public listener keeps the documented 1 MiB profile until a
// lifecycle-safe accounting design exists for reset-retained request bodies.
func TestPublicHTTP2LargeReceiveWindowIsAdvertisedOnWire(t *testing.T) {
	if os.Getenv("P2PSTREAM_HTTP2_CAPACITY_CLIENT_PID") == "" {
		t.Skip("optional larger-window diagnostic: run scripts/benchmark-http2-upload.sh")
	}
	const wantWindow = 16 << 20
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	srv.EnableHTTP2 = true
	srv.Config.HTTP2 = &http.HTTP2Config{
		MaxReceiveBufferPerConnection: wantWindow,
		MaxReceiveBufferPerStream:     wantWindow,
		MaxReadFrameSize:              16 << 10,
		MaxConcurrentStreams:          250,
	}
	srv.StartTLS()
	t.Cleanup(srv.Close)

	conn, err := tls.Dial("tcp", srv.Listener.Addr().String(), &tls.Config{InsecureSkipVerify: true, NextProtos: []string{http2.NextProtoTLS}}) //nolint:gosec // test server uses a generated certificate.
	if err != nil {
		t.Fatalf("TLS dial: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	if got := conn.ConnectionState().NegotiatedProtocol; got != http2.NextProtoTLS {
		t.Fatalf("negotiated protocol = %q, want %q", got, http2.NextProtoTLS)
	}
	fr := http2.NewFramer(conn, conn)
	if _, err := io.WriteString(conn, http2.ClientPreface); err != nil {
		t.Fatalf("write client preface: %v", err)
	}
	if err := fr.WriteSettings(); err != nil {
		t.Fatalf("write client settings: %v", err)
	}
	if err := conn.SetReadDeadline(time.Now().Add(2 * time.Second)); err != nil {
		t.Fatalf("set read deadline: %v", err)
	}
	var gotStreamWindow, gotConnectionWindow, gotMaxStreams uint32
	for gotStreamWindow == 0 || gotConnectionWindow == 0 || gotMaxStreams == 0 {
		frame, err := fr.ReadFrame()
		if err != nil {
			t.Fatalf("read server frame: %v", err)
		}
		switch frame := frame.(type) {
		case *http2.SettingsFrame:
			if frame.IsAck() {
				continue
			}
			if value, ok := frame.Value(http2.SettingInitialWindowSize); ok {
				gotStreamWindow = value
			}
			if value, ok := frame.Value(http2.SettingMaxConcurrentStreams); ok {
				gotMaxStreams = value
			}
		case *http2.WindowUpdateFrame:
			if frame.StreamID == 0 {
				gotConnectionWindow = frame.Increment
			}
		}
	}
	if gotStreamWindow != wantWindow {
		t.Fatalf("wire SETTINGS_INITIAL_WINDOW_SIZE = %d, want %d", gotStreamWindow, wantWindow)
	}
	wantConnectionIncrement := uint32(wantWindow - (1 << 16) + 1)
	if gotConnectionWindow != wantConnectionIncrement {
		t.Fatalf("wire connection WINDOW_UPDATE = %d, want %d", gotConnectionWindow, wantConnectionIncrement)
	}
	if gotMaxStreams != 250 {
		t.Fatalf("wire SETTINGS_MAX_CONCURRENT_STREAMS = %d, want 250", gotMaxStreams)
	}
}

func TestPublicHTTPServerDefaultsPreserveStreaming(t *testing.T) {
	srv := &http.Server{}
	configurePublicHTTPServer(srv)
	if srv.ReadHeaderTimeout != publicReadHeaderTimeout {
		t.Fatalf("ReadHeaderTimeout = %s, want %s", srv.ReadHeaderTimeout, publicReadHeaderTimeout)
	}
	if srv.ReadTimeout != 0 {
		t.Fatalf("ReadTimeout = %s, want 0 for streaming", srv.ReadTimeout)
	}
	if srv.WriteTimeout != 0 {
		t.Fatalf("WriteTimeout = %s, want 0 for streaming", srv.WriteTimeout)
	}
	if srv.IdleTimeout != publicIdleTimeout {
		t.Fatalf("IdleTimeout = %s, want %s", srv.IdleTimeout, publicIdleTimeout)
	}
	if srv.MaxHeaderBytes != defaultPublicMaxHeaderBytes {
		t.Fatalf("MaxHeaderBytes = %d, want %d", srv.MaxHeaderBytes, defaultPublicMaxHeaderBytes)
	}
	configurePublicHTTPServer(srv, 128<<10)
	if srv.MaxHeaderBytes != 128<<10 {
		t.Fatalf("configured MaxHeaderBytes = %d, want %d", srv.MaxHeaderBytes, 128<<10)
	}
}
