//go:build linux

package server

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

// TestHTTP2UploadCapacity is an opt-in protocol-level Go HTTP/2 diagnostic
// matching the public listener's HTTP/2 settings. The companion runner puts
// this process in a disposable server namespace and places a client behind an
// isolated two-leg router with 40 ms netem on each leg. It is not a full
// public-proxy test and does not emulate TCP latency in userspace.
func TestHTTP2UploadCapacity(t *testing.T) {
	clientPID := os.Getenv("P2PSTREAM_HTTP2_CAPACITY_CLIENT_PID")
	if clientPID == "" {
		t.Skip("run scripts/benchmark-http2-upload.sh for the kernel WAN diagnostic")
	}
	if _, err := strconv.Atoi(clientPID); err != nil {
		t.Fatal(err)
	}

	payload := bytes.Repeat([]byte("http2-capacity-test-data-0123456789abcdef"), 1<<20)
	wantHash := sha256.Sum256(payload)
	uploadFile := filepath.Join(t.TempDir(), "upload")
	if err := os.WriteFile(uploadFile, payload, 0600); err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		name         string
		proto        string
		streamWindow int
		connWindow   int
	}{
		{name: "http1.1", proto: "--http1.1"},
		{name: "http2-stream1MiB-default", proto: "--http2", streamWindow: 1 << 20},
		{name: "http2-stream3MiB-conn1MiB", proto: "--http2", streamWindow: 3 << 20, connWindow: 1 << 20},
		{name: "http2-stream3MiB-conn3MiB", proto: "--http2", streamWindow: 3 << 20, connWindow: 3 << 20},
		{name: "http2-stream16MiB-conn16MiB", proto: "--http2", streamWindow: 16 << 20, connWindow: 16 << 20},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := newHTTP2CapacityServer(t, wantHash, tc.streamWindow, tc.connWindow)
			defer server.Close()
			_, port, err := net.SplitHostPort(server.Listener.Addr().String())
			if err != nil {
				t.Fatal(err)
			}
			var speeds []float64
			for run := 0; run < 3; run++ {
				output := filepath.Join(t.TempDir(), fmt.Sprintf("response-%d", run))
				args := []string{
					"curl", "--silent", "--show-error", "--fail", "--insecure", "--noproxy", "*",
					"--max-time", "30", tc.proto, "--resolve", "capacity.test:" + port + ":10.211.0.1",
					"--output", output, "--write-out", "%{http_code} %{http_version} %{size_upload} %{speed_upload}",
					"--data-binary", "@" + uploadFile, "https://capacity.test:" + port + "/upload",
				}
				args = append([]string{"nsenter", "-t", clientPID, "-n"}, args...)
				ctx, cancel := context.WithTimeout(t.Context(), 45*time.Second)
				cmd := exec.CommandContext(ctx, args[0], args[1:]...)
				result, err := cmd.CombinedOutput()
				cancel()
				if err != nil {
					t.Fatalf("run %d: %v: %s", run, err, result)
				}
				fields := strings.Fields(string(result))
				if len(fields) != 4 || fields[0] != "200" {
					t.Fatalf("run %d metrics: %s", run, result)
				}
				if tc.proto == "--http2" && fields[1] != "2" {
					t.Fatalf("HTTP/2 was not negotiated: %s", result)
				}
				uploaded, err := strconv.ParseInt(fields[2], 10, 64)
				if err != nil {
					t.Fatalf("run %d uploaded bytes %q: %v", run, fields[2], err)
				}
				if uploaded != int64(len(payload)) {
					t.Fatalf("run %d uploaded %d bytes, want %d", run, uploaded, len(payload))
				}
				body, err := os.ReadFile(output)
				if err != nil {
					t.Fatal(err)
				}
				if !bytes.Equal(body, wantHash[:]) {
					t.Fatalf("run %d response hash mismatch", run)
				}
				speed, err := strconv.ParseFloat(fields[3], 64)
				if err != nil {
					t.Fatal(err)
				}
				speeds = append(speeds, speed)
				t.Logf("run %d: payload=%d bytes, protocol=%s, %.2f Mbit/s", run+1, uploaded, fields[1], speed*8/1e6)
			}
			t.Logf("stream window=%d, connection window=%d, median upload=%.2f Mbit/s", tc.streamWindow, tc.connWindow, medianHTTP2Capacity(speeds)*8/1e6)
		})
	}
}

func newHTTP2CapacityServer(t *testing.T, wantHash [sha256.Size]byte, streamWindow, connWindow int) *httptest.Server {
	t.Helper()
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		hash := sha256.New()
		if _, err := io.Copy(hash, r.Body); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if !bytes.Equal(hash.Sum(nil), wantHash[:]) {
			http.Error(w, "request hash mismatch", http.StatusBadRequest)
			return
		}
		_, _ = w.Write(wantHash[:])
	}))
	if err := server.Listener.Close(); err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("tcp", "10.211.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server.Listener = ln
	server.EnableHTTP2 = streamWindow > 0
	if streamWindow > 0 {
		server.Config.HTTP2 = &http.HTTP2Config{
			MaxReceiveBufferPerConnection: connWindow,
			MaxReceiveBufferPerStream:     streamWindow,
		}
	}
	server.StartTLS()
	return server
}

func medianHTTP2Capacity(values []float64) float64 {
	if len(values) == 0 {
		return 0
	}
	ordered := append([]float64(nil), values...)
	slices.Sort(ordered)
	return ordered[len(ordered)/2]
}
