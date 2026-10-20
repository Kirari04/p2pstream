//go:build linux

package server

import (
	"bufio"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type headerBurstGate struct {
	release chan struct{}
	once    sync.Once
	active  atomic.Int64
	peak    atomic.Int64
}

func (g *headerBurstGate) unblock() { g.once.Do(func() { close(g.release) }) }

type headerBurstResult struct {
	PublicOwnersReleased          bool              `json:"public_owners_released"`
	TLSAttempted                  int               `json:"tls_attempted"`
	TLSUnattempted                int               `json:"tls_unattempted"`
	TLSSetupSeconds               float64           `json:"tls_setup_seconds"`
	ConnectionStatsCaptured       bool              `json:"connection_stats_captured"`
	ProxyCPUSet                   string            `json:"proxy_cpu_set"`
	DriverCPUSet                  string            `json:"driver_cpu_set"`
	GoVersion                     string            `json:"go_version"`
	ServerBallastBytes            int64             `json:"server_ballast_bytes"`
	EventDrainError               string            `json:"event_drain_error,omitempty"`
	SourceCommit                  string            `json:"source_commit"`
	BinarySHA256                  string            `json:"binary_sha256"`
	HarnessSHA256                 string            `json:"harness_sha256"`
	RequestedConnections          int               `json:"requested_connections"`
	EstablishedTLSConnections     int               `json:"established_tls_connections"`
	TLSFailures                   int               `json:"tls_failures"`
	DistinctHeaderKeys            int               `json:"distinct_header_keys"`
	WireRequestBytes              int               `json:"wire_request_bytes"`
	StatusCodes                   map[string]int    `json:"status_codes"`
	ClientErrors                  map[string]int    `json:"client_errors"`
	OriginPeakInflight            int64             `json:"origin_peak_inflight"`
	HeaderPhaseInitialCgroupBytes int64             `json:"header_phase_initial_cgroup_bytes"`
	HeaderPhasePeakCgroupBytes    int64             `json:"header_phase_peak_cgroup_bytes"`
	HeaderPhasePeakHeapBytes      uint64            `json:"header_phase_peak_heap_bytes"`
	HeaderPhasePeakRSSBytes       int64             `json:"header_phase_peak_rss_bytes"`
	CgroupMemoryLimitBytes        int64             `json:"cgroup_memory_limit_bytes"`
	CgroupLifetimePeakBytes       int64             `json:"cgroup_lifetime_peak_bytes"`
	PeakSocketBytes               int64             `json:"peak_socket_bytes"`
	OwnerPeakBytes                map[string]int64  `json:"owner_peak_bytes"`
	OwnerPeakReservations         map[string]int64  `json:"owner_peak_reservations"`
	AdmissionMisses               map[string]uint64 `json:"admission_misses"`
	FailureKindsCumulative        map[string]int64  `json:"failure_kinds_cumulative"`
	FailureKindsError             string            `json:"failure_kinds_error,omitempty"`
	RecoveredRequests             int               `json:"recovered_requests"`
	FinalOwnerBytes               map[string]int64  `json:"final_owner_bytes"`
	FinalCgroupBytes              int64             `json:"final_cgroup_bytes"`
	OOMEvents                     int64             `json:"oom_events"`
	OOMKillEvents                 int64             `json:"oom_kill_events"`
	OOMKilled                     bool              `json:"oom_killed"`
	ContainerRunning              bool              `json:"container_running"`
}

// The driver and origin run outside the constrained proxy cgroup. Each step
// establishes TLS before releasing one header burst, then holds admitted origin
// work for three seconds. The sampled header phase includes native parsing AND
// admitted decoded request maps; it is not isolated parser retained memory.
func TestPublicHeaderBurstQualification(t *testing.T) {
	if os.Getenv("P2PSTREAM_HEADER_BURST") != "driver" {
		t.Skip("run scripts/qualify-proxy-headers.sh for the finite 4 GiB header burst")
	}
	dir := requiredQualificationEnv(t, "P2PSTREAM_HEADER_BURST_DIR")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	certPath, keyPath, roots := headerBurstCertificate(t, dir)
	var gates sync.Map
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if value, ok := gates.Load(r.URL.Query().Get("burst")); ok {
			gate := value.(*headerBurstGate)
			active := gate.active.Add(1)
			defer gate.active.Add(-1)
			for previous := gate.peak.Load(); active > previous; previous = gate.peak.Load() {
				if gate.peak.CompareAndSwap(previous, active) {
					break
				}
			}
			select {
			case <-gate.release:
			case <-r.Context().Done():
				return
			}
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer origin.Close()
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	containerName := fmt.Sprintf("p2pstream-header-burst-%d-%d", os.Getpid(), time.Now().UnixNano())
	readyPath, stopPath := filepath.Join(dir, "proxy.ready.json"), filepath.Join(dir, "proxy.stop")
	log, err := os.Create(filepath.Join(dir, "proxy.log"))
	if err != nil {
		t.Fatal(err)
	}
	defer log.Close()
	args := []string{"run", "--name", containerName, "--network", "host", "--user", fmt.Sprintf("%d:%d", os.Getuid(), os.Getgid()),
		"--memory", "4g", "--memory-swap", "4g", "--pids-limit", "8192", "--read-only", "--cap-drop", "ALL",
		"--security-opt", "no-new-privileges", "--tmpfs", "/tmp:rw,nosuid,nodev,size=256m",
		"--mount", "type=bind,source=" + binary + ",target=/server.test,readonly",
		"--mount", "type=bind,source=" + dir + ",target=/qualification",
		"--env", "GOMAXPROCS=4", "--env", "P2PSTREAM_HEADER_BURST=proxy",
		"--env", "P2PSTREAM_QUALIFICATION_ORIGIN_URL=" + origin.URL,
		"--env", "P2PSTREAM_QUALIFICATION_CERT=/qualification/" + filepath.Base(certPath),
		"--env", "P2PSTREAM_QUALIFICATION_KEY=/qualification/" + filepath.Base(keyPath),
		"--env", "P2PSTREAM_QUALIFICATION_READY=/qualification/proxy.ready.json",
		"--env", "P2PSTREAM_QUALIFICATION_STOP=/qualification/proxy.stop"}
	if cpus := os.Getenv("P2PSTREAM_HEADER_BURST_PROXY_CPUS"); cpus != "" {
		args = append(args, "--cpuset-cpus", cpus)
	}
	args = append(args, "debian:trixie-slim", "/server.test", "-test.run", "^TestPublicHeaderBurstProxy$", "-test.v", "-test.timeout", "6m")
	command := exec.Command("docker", args...)
	command.Stdout, command.Stderr = log, log
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	exited := make(chan error, 1)
	go func() { exited <- command.Wait() }()
	defer func() {
		_ = os.WriteFile(stopPath, nil, 0600)
		select {
		case <-exited:
		case <-time.After(3 * time.Second):
		}
		_ = exec.Command("docker", "rm", "-f", containerName).Run()
	}()
	var ready qualificationReady
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		if data, err := os.ReadFile(readyPath); err == nil && json.Unmarshal(data, &ready) == nil && ready.URL != "" {
			break
		}
		select {
		case err := <-exited:
			t.Fatalf("header proxy exited during startup: %v; see %s", err, log.Name())
		default:
		}
		time.Sleep(25 * time.Millisecond)
	}
	if ready.URL == "" {
		t.Fatalf("header proxy did not start; see %s", log.Name())
	}
	pidData, err := exec.Command("docker", "inspect", "--format", "{{.State.Pid}}", containerName).Output()
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(pidData)))
	if err != nil || pid <= 0 {
		t.Fatalf("invalid proxy host PID %q: %v", pidData, err)
	}
	resultFile, err := os.Create(filepath.Join(dir, "results.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer resultFile.Close()
	for _, count := range []int{128, 256, 512, 1024, 1536} {
		gate := &headerBurstGate{release: make(chan struct{})}
		token := strconv.Itoa(count)
		gates.Store(token, gate)
		result := runHeaderBurstStep(t, ready, pid, roots, count, token, gate)
		result.OOMEvents, result.OOMKillEvents = headerBurstOOMEvents(pid)
		stateData, inspectErr := exec.Command("docker", "inspect", "--format", "{{json .State}}", containerName).Output()
		var state struct{ Running, OOMKilled bool }
		if inspectErr == nil {
			inspectErr = json.Unmarshal(stateData, &state)
		}
		result.OOMKilled, result.ContainerRunning = state.OOMKilled, state.Running
		if !state.OOMKilled && state.Running {
			result.RecoveredRequests = recoverHeaderBurstRequests(ready.URL, roots)
			probe := newQualificationSampler(strconv.Itoa(pid), ready.AdminURL, true)
			ownerDeadline := time.Now().Add(2 * time.Second)
			for time.Now().Before(ownerDeadline) {
				if stats, ok := probe.readRuntime(qualificationPoll); ok && stats.OwnerStats {
					result.FinalOwnerBytes = stats.OwnerBytes
					result.PublicOwnersReleased = true
					for _, owner := range []string{"public_connection", "public_handshake", "public_request", "public_response"} {
						if bytes, present := stats.OwnerBytes[owner]; !present || bytes != 0 {
							result.PublicOwnersReleased = false
						}
					}
					if result.PublicOwnersReleased {
						break
					}
				}
				time.Sleep(20 * time.Millisecond)
			}
			result.FinalCgroupBytes = probe.readCgroupValue("memory.current")
		}
		if err := json.NewEncoder(resultFile).Encode(result); err != nil {
			t.Fatal(err)
		}
		_ = resultFile.Sync()
		t.Logf("headers=%d connections=%d/%d statuses=%v errors=%v peak=%d/%d origin=%d recovery=%d OOM=%v", result.DistinctHeaderKeys, result.EstablishedTLSConnections, count, result.StatusCodes, result.ClientErrors, result.HeaderPhasePeakCgroupBytes, result.CgroupMemoryLimitBytes, result.OriginPeakInflight, result.RecoveredRequests, result.OOMKilled)
		if inspectErr != nil || !state.Running || state.OOMKilled || result.OOMEvents != 0 || result.OOMKillEvents != 0 {
			t.Fatalf("stopping staircase after container failure: inspect=%v state=%+v; evidence=%s", inspectErr, state, resultFile.Name())
		}
		if result.RecoveredRequests != 32 || !result.PublicOwnersReleased {
			t.Fatalf("header burst did not recover; evidence=%s", resultFile.Name())
		}
	}
}

func runHeaderBurstStep(t *testing.T, ready qualificationReady, pid int, roots *x509.CertPool, count int, token string, gate *headerBurstGate) headerBurstResult {
	t.Helper()
	defer gate.unblock()
	endpoint, err := url.Parse(ready.URL)
	if err != nil {
		t.Fatal(err)
	}
	config := &tls.Config{RootCAs: roots, ServerName: "public.test", NextProtos: []string{"http/1.1"}}
	beforeProbe := newQualificationSampler(strconv.Itoa(pid), ready.AdminURL, true)
	beforeTLS, beforeTLSStatsOK := beforeProbe.readRuntime(qualificationPoll)
	setupStarted := time.Now()
	dialCtx, cancelDial := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancelDial()
	dialer := tls.Dialer{NetDialer: &net.Dialer{}, Config: config}
	connections := make([]*tls.Conn, count)
	var next atomic.Int64
	var attempted atomic.Int64
	var workers sync.WaitGroup
	for range min(count, 64) {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for index := int(next.Add(1) - 1); index < count; index = int(next.Add(1) - 1) {
				if dialCtx.Err() != nil {
					return
				}
				attempted.Add(1)
				conn, err := dialer.DialContext(dialCtx, "tcp", endpoint.Host)
				if err == nil {
					connections[index] = conn.(*tls.Conn)
				}
			}
		}()
	}
	workers.Wait()
	setupSeconds := time.Since(setupStarted).Seconds()
	defer func() {
		for _, conn := range connections {
			if conn != nil {
				_ = conn.Close()
			}
		}
	}()
	var request strings.Builder
	fmt.Fprintf(&request, "GET /direct/held?burst=%s HTTP/1.1\r\nHost: public.test\r\nConnection: close\r\n", token)
	const keys = 8000
	for index := range keys {
		key := strconv.FormatInt(int64(index), 36)
		request.WriteString("H" + strings.Repeat("0", 3-len(key)) + key + ": \r\n")
	}
	request.WriteString("\r\n")
	payload := []byte(request.String())
	if len(payload) >= defaultPublicMaxHeaderBytes {
		t.Fatalf("header fixture exceeds policy: %d >= %d", len(payload), defaultPublicMaxHeaderBytes)
	}
	result := headerBurstResult{SourceCommit: os.Getenv("P2PSTREAM_HEADER_BURST_SOURCE"), BinarySHA256: os.Getenv("P2PSTREAM_HEADER_BURST_BINARY_SHA256"), HarnessSHA256: os.Getenv("P2PSTREAM_HEADER_BURST_HARNESS_SHA256"),
		RequestedConnections: count, DistinctHeaderKeys: keys, WireRequestBytes: len(payload), StatusCodes: make(map[string]int), ClientErrors: make(map[string]int)}
	result.TLSAttempted = int(attempted.Load())
	result.TLSUnattempted = count - result.TLSAttempted
	result.TLSSetupSeconds = setupSeconds
	sampler := newQualificationSampler(strconv.Itoa(pid), ready.AdminURL, true)
	sampler.start()
	start := make(chan struct{})
	results := make(chan string, count)
	for _, conn := range connections {
		if conn == nil {
			continue
		}
		result.EstablishedTLSConnections++
		go func(conn *tls.Conn) {
			defer conn.Close()
			<-start
			_ = conn.SetDeadline(time.Now().Add(12 * time.Second))
			if _, err := conn.Write(payload); err != nil {
				results <- "write_error"
				return
			}
			response, err := http.ReadResponse(bufio.NewReader(conn), nil)
			if err != nil {
				results <- "read_response_error"
				return
			}
			_, readErr := io.Copy(io.Discard, io.LimitReader(response.Body, 16<<10))
			closeErr := response.Body.Close()
			if readErr != nil || closeErr != nil {
				results <- "read_body_error"
				return
			}
			results <- strconv.Itoa(response.StatusCode)
		}(conn)
	}
	result.TLSFailures = result.TLSAttempted - result.EstablishedTLSConnections
	close(start)
	timer := time.AfterFunc(3*time.Second, gate.unblock)
	defer timer.Stop()
	for range result.EstablishedTLSConnections {
		outcome := <-results
		if _, err := strconv.Atoi(outcome); err == nil {
			result.StatusCodes[outcome]++
		} else {
			result.ClientErrors[outcome]++
		}
	}
	gate.unblock()
	sampler.stop()
	result.ProxyCPUSet = os.Getenv("P2PSTREAM_HEADER_BURST_PROXY_CPUS")
	result.DriverCPUSet = os.Getenv("P2PSTREAM_HEADER_BURST_DRIVER_CPUS")
	result.GoVersion = runtime.Version()
	result.ServerBallastBytes = 1 << 30
	result.EventDrainError = sampler.eventDrainError
	result.OriginPeakInflight = gate.peak.Load()
	result.HeaderPhaseInitialCgroupBytes, result.HeaderPhasePeakCgroupBytes = sampler.initialCgroup, sampler.cgroupPeak
	result.HeaderPhasePeakHeapBytes, result.HeaderPhasePeakRSSBytes = sampler.peakHeap, sampler.peakRSS
	result.CgroupMemoryLimitBytes, result.CgroupLifetimePeakBytes = sampler.cgroupMax, sampler.readCgroupValue("memory.peak")
	result.PeakSocketBytes = sampler.peakCgroupSock
	result.OwnerPeakBytes, result.OwnerPeakReservations = sampler.ownerPeakBytes, sampler.ownerPeakReservations
	result.AdmissionMisses = sampler.admissionRejectionDelta()
	result.ConnectionStatsCaptured = beforeTLSStatsOK && beforeTLS.OwnerStats && sampler.finalOwnerStats.OwnerStats
	if result.ConnectionStatsCaptured {
		result.AdmissionMisses["public_connection_resource"] = counterDelta(beforeTLS.PublicConnectionResourceRejected, sampler.finalOwnerStats.PublicConnectionResourceRejected)
		result.AdmissionMisses["public_connection_limit"] = counterDelta(beforeTLS.PublicConnectionLimitRejected, sampler.finalOwnerStats.PublicConnectionLimitRejected)
	}
	client := http.Client{Timeout: time.Second}
	if response, err := client.Get(ready.AdminURL + "/failure-kinds"); err != nil {
		result.FailureKindsError = err.Error()
	} else {
		defer response.Body.Close()
		if err := json.NewDecoder(io.LimitReader(response.Body, 16<<10)).Decode(&result.FailureKindsCumulative); err != nil {
			result.FailureKindsError = err.Error()
		}
	}
	return result
}

func recoverHeaderBurstRequests(endpoint string, roots *x509.CertPool) int {
	transport := &http.Transport{TLSClientConfig: &tls.Config{RootCAs: roots, ServerName: "public.test", NextProtos: []string{"http/1.1"}}, DisableKeepAlives: true}
	defer transport.CloseIdleConnections()
	client := http.Client{Transport: transport, Timeout: 2 * time.Second}
	completed := 0
	deadline := time.Now().Add(10 * time.Second)
	for completed < 32 && time.Now().Before(deadline) {
		request, err := http.NewRequest(http.MethodGet, endpoint+"/direct/recovery", nil)
		if err != nil {
			return completed
		}
		request.Host = "public.test"
		response, err := client.Do(request)
		if err != nil {
			time.Sleep(100 * time.Millisecond)
			continue
		}
		_, readErr := io.Copy(io.Discard, io.LimitReader(response.Body, 16<<10))
		_ = response.Body.Close()
		if response.StatusCode == http.StatusNoContent && readErr == nil {
			completed++
		} else {
			time.Sleep(100 * time.Millisecond)
		}
	}
	return completed
}

func headerBurstOOMEvents(pid int) (oom, killed int64) {
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/root/sys/fs/cgroup/memory.events", pid))
	if err != nil {
		return -1, -1
	}
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 {
			continue
		}
		value, _ := strconv.ParseInt(fields[1], 10, 64)
		switch fields[0] {
		case "oom":
			oom = value
		case "oom_kill":
			killed = value
		}
	}
	return
}

func TestPublicHeaderBurstProxy(t *testing.T) {
	if os.Getenv("P2PSTREAM_HEADER_BURST") != "proxy" {
		t.Skip("header-burst child process only")
	}
	if got := stressCgroupValue(t, "/sys/fs/cgroup/memory.max", ""); got != 4<<30 {
		t.Fatalf("refusing header burst outside 4 GiB cgroup: %d", got)
	}
	ballast := stressTouchedBytes(1 << 30)
	defer runtime.KeepAlive(ballast)
	f := newRealAgentProxyFixture(t, 4, 0, 0)
	originURL := requiredQualificationEnv(t, "P2PSTREAM_QUALIFICATION_ORIGIN_URL")
	parsed, err := url.Parse(originURL)
	if err != nil {
		t.Fatal(err)
	}
	target := publicRouteTargetConfig{ID: 801, RouteID: 701, Name: "header-burst", Enabled: true, Weight: 100,
		TargetType: publicRouteTargetTypeProxy, Transport: publicRouteTargetTransportDirect, URL: originURL, ParsedURL: parsed, UpstreamResponseHeaderTimeout: 10 * time.Second}
	listener := publicListenerConfig{ID: 1, Name: "header-burst", Protocol: publicListenerProtocolHTTPS, BindAddress: "127.0.0.1", Enabled: true}
	snapshot := &publicProxySnapshot{
		Listeners:        map[int64]publicListenerConfig{1: listener},
		RoutesByListener: map[int64][]publicRouteConfig{1: {{ID: 701, ListenerID: 1, Enabled: true, HostPattern: "public.test", PathPrefix: "/direct", PathSecurityMode: publicRoutePathSecurityModeStrict, Targets: []publicRouteTargetConfig{target}}}},
		RouteTargets:     map[int64]publicRouteTargetConfig{target.ID: target},
		CertsByListener:  map[int64][]publicTLSCertificateConfig{1: {{ID: 1, ListenerID: 1, HostnamePattern: "public.test", CertPath: requiredQualificationEnv(t, "P2PSTREAM_QUALIFICATION_CERT"), KeyPath: requiredQualificationEnv(t, "P2PSTREAM_QUALIFICATION_KEY"), Enabled: true, Source: publicTLSCertificateSourceManual, Status: publicTLSCertificateStatusReady}}},
	}
	setPublicSnapshotForTest(t, f.app, snapshot)
	f.app.TargetHealth.reconcile(f.app, snapshot, false)
	if _, err := f.app.startPublicListenerFromSnapshot(listener, snapshot); err != nil {
		t.Fatal(err)
	}
	f.app.proxyMu.Lock()
	address := f.app.publicListenerState[1].BoundAddress
	f.app.proxyMu.Unlock()
	adminListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.Handle("/", qualificationRuntimeHandler(f.app))
	mux.HandleFunc("/failure-kinds", func(w http.ResponseWriter, r *http.Request) {
		rows, err := f.app.DB.QueryContext(r.Context(), "SELECT error_kind, COUNT(*) FROM proxy_request_events GROUP BY error_kind LIMIT 128")
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		defer rows.Close()
		counts := make(map[string]int64)
		for rows.Next() {
			var kind string
			var count int64
			if err := rows.Scan(&kind, &count); err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			counts[kind] = count
		}
		if err := rows.Err(); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		_ = json.NewEncoder(w).Encode(counts)
	})
	admin := &http.Server{Handler: mux}
	go func() { _ = admin.Serve(adminListener) }()
	writeQualificationReady(t, qualificationReady{URL: "https://" + address, AdminURL: "http://" + adminListener.Addr().String(), PID: os.Getpid()})
	waitQualificationStop(t)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_, _ = f.app.stopPublicListenerRuntime(ctx, 1)
	_ = admin.Shutdown(ctx)
}

func headerBurstCertificate(t *testing.T, dir string) (string, string, *x509.CertPool) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{SerialNumber: big.NewInt(1), DNSNames: []string{"public.test"}, NotBefore: time.Now().Add(-time.Minute), NotAfter: time.Now().Add(time.Hour), KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	certificate, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	privateKey, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	certPath, keyPath := filepath.Join(dir, "public.crt.pem"), filepath.Join(dir, "public.key.pem")
	certificatePEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certificate})
	if err := os.WriteFile(certPath, certificatePEM, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: privateKey}), 0600); err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(certificatePEM) {
		t.Fatal("failed to trust generated certificate")
	}
	return certPath, keyPath, roots
}
