//go:build linux

package server

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

// TestPublicProxyWANThroughput traverses the production public listener, TLS,
// routing and a real agent. scripts/test-proxy-wan.sh supplies an isolated
// kernel network with 80 ms RTT and 100 Mbit/s capacity; it is mandatory in CI.
// Userspace delay readers cannot catch SO_SNDBUF/SO_RCVBUF regressions because
// they do not model TCP ACKs, congestion control or advertised receive windows.
func TestPublicProxyWANThroughput(t *testing.T) {
	clientPID := os.Getenv("P2PSTREAM_WAN_CLIENT_PID")
	if clientPID == "" {
		t.Skip("run scripts/test-proxy-wan.sh for the kernel WAN regression")
	}
	if _, err := strconv.Atoi(clientPID); err != nil {
		t.Fatal(err)
	}
	f := newRealAgentProxyFixture(t, 4, 0, 0)
	payload := bytes.Repeat([]byte("wan-throughput-test-data-01234567"), 1<<20)
	wantHash := sha256.Sum256(payload)
	origin := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			hash := sha256.New()
			if _, err := io.Copy(hash, r.Body); err != nil {
				http.Error(w, err.Error(), 400)
				return
			}
			_, _ = w.Write(hash.Sum(nil))
			return
		}
		w.Header().Set("Content-Length", strconv.Itoa(len(payload)))
		_, _ = w.Write(payload)
	}))
	ln, err := net.Listen("tcp", "10.201.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	_ = origin.Listener.Close()
	origin.Listener = ln
	origin.EnableHTTP2 = true
	origin.StartTLS()
	t.Cleanup(origin.Close)
	// This origin is reachable both directly from the remote client (control)
	// and locally from the proxy/agent (same bytes, same public WAN path).
	target := directTransportPoolTestTarget(t, 71, origin.URL, 10*time.Second)
	target.TLSSkipVerify = true
	target.Weight = 100
	agentTarget := target
	agentTarget.ID = 72
	agentTarget.Transport = publicRouteTargetTransportAgent
	agentTarget.AgentSelector = publicAgentSelectorConfig{MatchLabels: map[string]string{"test": "wan"}}
	snap, _ := testPublicTLSSnapshot(t, 1, "public.test", "wan")
	listener := snap.Listeners[1]
	listener.BindAddress = "10.201.0.1"
	snap.Listeners[1] = listener
	snap.Agents = map[int64]publicAgentConfig{f.agent.AgentID: {Enabled: true, Labels: map[string]string{"test": "wan"}}}
	snap.RouteTargets = map[int64]publicRouteTargetConfig{71: target, 72: agentTarget}
	snap.RoutesByListener[1] = []publicRouteConfig{
		{ID: 1, ListenerID: 1, Enabled: true, PathPrefix: "/direct", Targets: []publicRouteTargetConfig{target}},
		{ID: 2, ListenerID: 1, Enabled: true, PathPrefix: "/agent", Targets: []publicRouteTargetConfig{agentTarget}},
	}
	setPublicSnapshotForTest(t, f.app, snap)
	if _, err := f.app.startPublicListenerFromSnapshot(listener, snap); err != nil {
		t.Fatal(err)
	}
	f.app.proxyMu.Lock()
	runtime := f.app.publicListenerState[1]
	address := runtime.BoundAddress
	f.app.proxyMu.Unlock()
	if address == "" {
		t.Fatalf("public listener failed: %s", runtime.LastError)
	}
	t.Cleanup(func() { _, _ = f.app.stopPublicListenerRuntime(t.Context(), 1); f.app.DirectTransports.closeAll() })
	_, port, err := net.SplitHostPort(address)
	if err != nil {
		t.Fatal(err)
	}
	uploadFile := filepath.Join(t.TempDir(), "upload")
	if err := os.WriteFile(uploadFile, payload, 0600); err != nil {
		t.Fatal(err)
	}
	transfer := func(t *testing.T, url, protocol string, remoteClient, upload bool) float64 {
		t.Helper()
		output := filepath.Join(t.TempDir(), "body")
		metric := "%{http_code} %{http_version} %{speed_download}"
		if upload {
			metric = "%{http_code} %{http_version} %{speed_upload}"
		}
		args := []string{"curl",
			"--silent", "--show-error", "--fail", "--insecure", "--noproxy", "*", "--max-time", "40", protocol,
			"--resolve", "public.test:" + port + ":10.201.0.1", "--output", output,
			"--write-out", metric, url}
		if upload {
			args = append(args, "--data-binary", "@"+uploadFile)
		}
		if remoteClient {
			args = append([]string{"nsenter", "-t", clientPID, "-n"}, args...)
		}
		cmd := exec.CommandContext(t.Context(), args[0], args[1:]...)
		result, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("download %s: %v: %s", url, err, result)
		}
		fields := strings.Fields(string(result))
		if len(fields) != 3 || fields[0] != "200" {
			t.Fatalf("download metrics: %s", result)
		}
		if protocol == "--http2" && fields[1] != "2" {
			t.Fatalf("HTTP/2 was not negotiated: %s", result)
		}
		body, err := os.ReadFile(output)
		if err != nil {
			t.Fatal(err)
		}
		if upload {
			if !bytes.Equal(body, wantHash[:]) {
				t.Fatal("upload body truncated or corrupted")
			}
		} else if len(body) != len(payload) || sha256.Sum256(body) != wantHash {
			t.Fatal("download body truncated or corrupted")
		}
		speed, err := strconv.ParseFloat(fields[2], 64)
		if err != nil {
			t.Fatal(err)
		}
		return speed
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	child := exec.CommandContext(t.Context(), "nsenter", "-t", clientPID, "-n", executable, "-test.run", "^TestPublicWANOriginProcess$", "-test.timeout", "5m")
	child.Env = append(os.Environ(), "P2PSTREAM_WAN_ORIGIN=1")
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = child.Process.Kill(); _ = child.Wait() })
	deadline := time.Now().Add(5 * time.Second)
	for {
		conn, err := net.DialTimeout("tcp", "10.202.0.2:38081", time.Second)
		if err == nil {
			_ = conn.Close()
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("remote origin did not start")
		}
	}
	for _, topology := range []string{"public-wan", "origin-wan"} {
		t.Run(topology, func(t *testing.T) {
			controlURL := origin.URL
			if topology == "origin-wan" {
				controlURL = "http://10.202.0.2:38081"
				remoteURL, err := url.Parse(controlURL)
				if err != nil {
					t.Fatal(err)
				}
				remoteSnapshot := *snap
				remoteSnapshot.RoutesByListener = map[int64][]publicRouteConfig{1: slices.Clone(snap.RoutesByListener[1])}
				remoteSnapshot.RouteTargets = make(map[int64]publicRouteTargetConfig)
				for i := range remoteSnapshot.RoutesByListener[1] {
					route := &remoteSnapshot.RoutesByListener[1][i]
					route.Targets = slices.Clone(route.Targets)
					route.Targets[0].URL = controlURL
					route.Targets[0].ParsedURL = remoteURL
					remoteSnapshot.RouteTargets[route.Targets[0].ID] = route.Targets[0]
				}
				// Publish a new immutable snapshot while no request is running.
				setPublicSnapshotForTest(t, f.app, &remoteSnapshot)
			}
			for _, protocol := range []string{"--http1.1", "--http2"} {
				t.Run(protocol, func(t *testing.T) {
					measure := func(url string, protocol string, upload bool) float64 {
						var speeds []float64
						for range 3 {
							speeds = append(speeds, transfer(t, url, protocol, topology == "public-wan", upload))
						}
						slices.Sort(speeds)
						return speeds[1]
					}
					controlProtocol := protocol
					if topology == "origin-wan" {
						controlProtocol = "--http1.1"
					}
					control := measure(controlURL, controlProtocol, false)
					if control < 8e6 {
						t.Fatalf("WAN control %.2f MB/s is below the 100 Mbit/s fixture floor", control/1e6)
					}
					for _, path := range []string{"direct", "agent"} {
						speed := measure(fmt.Sprintf("https://public.test:%s/%s", port, path), protocol, false)
						t.Logf("%s: %.2f MB/s, control %.2f MB/s (%.0f%%)", path, speed/1e6, control/1e6, speed/control*100)
						if speed < control*0.65 {
							t.Errorf("%s throughput %.2f MB/s is below 65%% of control %.2f MB/s", path, speed/1e6, control/1e6)
						}
					}
					if topology == "public-wan" && protocol == "--http2" {
						uploadControl := measure(controlURL, protocol, true)
						if uploadControl < 8e6 {
							t.Fatalf("upload control too slow: %.2f MB/s", uploadControl/1e6)
						}
						for _, path := range []string{"direct", "agent"} {
							speed := measure(fmt.Sprintf("https://public.test:%s/%s", port, path), protocol, true)
							t.Logf("%s upload: %.2f MB/s, control %.2f MB/s", path, speed/1e6, uploadControl/1e6)
							if speed < uploadControl*0.65 {
								t.Errorf("%s upload is below 65%% of control", path)
							}
						}
					}
				})
			}
		})
	}
}

// Subprocess origin in the far namespace. It runs only under the WAN harness.
func TestPublicWANOriginProcess(t *testing.T) {
	if os.Getenv("P2PSTREAM_WAN_ORIGIN") != "1" {
		t.Skip("WAN subprocess only")
	}
	payload := bytes.Repeat([]byte("wan-throughput-test-data-01234567"), 1<<20)
	server := &http.Server{Addr: "10.202.0.2:38081", Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			hash := sha256.New()
			if _, err := io.Copy(hash, r.Body); err != nil {
				http.Error(w, err.Error(), 400)
				return
			}
			_, _ = w.Write(hash.Sum(nil))
			return
		}
		w.Header().Set("Content-Length", strconv.Itoa(len(payload)))
		_, _ = w.Write(payload)
	})}
	t.Fatal(server.ListenAndServe())
}
