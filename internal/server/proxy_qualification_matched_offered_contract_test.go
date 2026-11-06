//go:build linux

package server

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"math"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
)

func qualificationMatchedOfferedContractEnv(protocol string, concurrency, rate int) map[string]string {
	prewarm := ""
	if protocol == "h2" {
		prewarm = "1"
	}
	return map[string]string{"MATCHED_TLS": "1", "OFFERED_RPS": strconv.Itoa(rate), "CONCURRENCY": strconv.Itoa(concurrency), "REQUESTS": strconv.Itoa(20 * rate), "DURATION_SECONDS": "0", "WARMUP_SECONDS": "0", "H2_PREWARM": prewarm, "BALLAST_MIB": "0", "PATH": "direct-control"}
}

func TestQualificationMatchedOfferedMode(t *testing.T) {
	for _, protocol := range []string{"h1", "h2"} {
		for _, concurrency := range []int{16, 128} {
			for _, rate := range []int{10, 100000} {
				env := qualificationMatchedOfferedContractEnv(protocol, concurrency, rate)
				enabled, err := qualificationMatchedOfferedMode(qualificationMatchedTLSContractGetenv(env), "matched-"+protocol+"-offered-20s", protocol, concurrency, 20*rate, float64(rate), 0, 0)
				if err != nil || !enabled {
					t.Fatalf("protocol=%s concurrency=%d rate=%d enabled=%v error=%v", protocol, concurrency, rate, enabled, err)
				}
			}
		}
	}
	for _, scenario := range []string{"h1-keepalive-duration", "h2-multiplex-duration", "growth-download", "h1-keepalive"} {
		if enabled, err := qualificationMatchedOfferedMode(func(string) string { return "" }, scenario, "h1", 1, 1, 0, 1, 1); enabled || err != nil {
			t.Fatalf("historical scenario=%s enabled=%v error=%v", scenario, enabled, err)
		}
	}
}

func TestQualificationMatchedOfferedRejectsInvalidMode(t *testing.T) {
	type invalid struct {
		name, key, value, scenario, protocol string
		concurrency, requests                int
		offered, duration, warmup            float64
	}
	cases := []invalid{
		{name: "zero-rate", offered: 0}, {name: "negative-rate", offered: -1}, {name: "fractional-rate", offered: 10.5},
		{name: "nan-rate", offered: math.NaN()}, {name: "inf-rate", offered: math.Inf(1)}, {name: "minus-inf-rate", offered: math.Inf(-1)}, {name: "over-bound-rate", offered: 100001},
	}
	// Specify defaults explicitly: zero is itself an important invalid input.
	for _, value := range []float64{-1, 1, math.NaN(), math.Inf(1)} {
		cases = append(cases, invalid{name: "duration-" + strconv.FormatFloat(value, 'g', -1, 64), offered: 10, duration: value}, invalid{name: "warmup-" + strconv.FormatFloat(value, 'g', -1, 64), offered: 10, warmup: value})
	}
	for _, tc := range []invalid{
		{name: "count", requests: 201}, {name: "concurrency", concurrency: 17}, {name: "protocol", protocol: "h3"}, {name: "scenario-protocol", protocol: "h1"}, {name: "unknown-mode", scenario: "matched-h2-offered"},
		{name: "missing-tls", key: "MATCHED_TLS", value: ""}, {name: "zero-tls", key: "MATCHED_TLS", value: "0"},
		{name: "missing-prewarm", key: "H2_PREWARM", value: ""}, {name: "malformed-prewarm", key: "H2_PREWARM", value: "true"},
		{name: "nan-text", key: "OFFERED_RPS", value: "NaN"}, {name: "junk-rate", key: "OFFERED_RPS", value: "junk"}, {name: "exponent-rate", key: "OFFERED_RPS", value: "1e1"}, {name: "decimal-rate", key: "OFFERED_RPS", value: "10.0"},
		{name: "missing-duration", key: "DURATION_SECONDS", value: ""}, {name: "junk-duration", key: "DURATION_SECONDS", value: "junk"}, {name: "nan-warmup-text", key: "WARMUP_SECONDS", value: "NaN"},
		{name: "agent-path", key: "PATH", value: "agent-external"}, {name: "ballast", key: "BALLAST_MIB", value: "1"},
		{name: "timeout", key: "NETWORK_TIMEOUT_SECONDS", value: "120"}, {name: "growth", key: "GROWTH_PAYLOAD_BYTES", value: "256"}, {name: "capacity", key: "CAPACITY_ESTABLISHED_H2", value: "1"},
		{name: "observer", key: "ORIGIN_OBSERVER_URL", value: "http://127.0.0.1"}, {name: "upload-token", key: "UPLOAD_OBSERVATION_TOKEN", value: "fixture"},
		{name: "suppressed-event-label", key: "ADMIN_URL", value: "http://127.0.0.1"}, {name: "missing-admin", key: "RESOURCE_ENVELOPE", value: "p2pstream-native"},
	} {
		tc.offered = 10
		cases = append(cases, tc)
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env := qualificationMatchedOfferedContractEnv("h2", 16, 10)
			if tc.key != "" {
				env[tc.key] = tc.value
			}
			scenario, protocol, concurrency, requests := tc.scenario, tc.protocol, tc.concurrency, tc.requests
			if scenario == "" {
				scenario = "matched-h2-offered-20s"
			}
			if protocol == "" {
				protocol = "h2"
			}
			if concurrency == 0 {
				concurrency = 16
			}
			if requests == 0 {
				requests = 200
			}
			if enabled, err := qualificationMatchedOfferedMode(qualificationMatchedTLSContractGetenv(env), scenario, protocol, concurrency, requests, tc.offered, tc.duration, tc.warmup); enabled || err == nil {
				t.Fatal("invalid offered contract accepted")
			}
		})
	}
	env := qualificationMatchedOfferedContractEnv("h1", 16, 10)
	env["H2_PREWARM"] = "1"
	if enabled, err := qualificationMatchedOfferedMode(qualificationMatchedTLSContractGetenv(env), "matched-h1-offered-20s", "h1", 16, 200, 10, 0, 0); enabled || err == nil {
		t.Fatal("H1 prewarm accepted")
	}
	getenv := qualificationMatchedTLSContractGetenv(qualificationMatchedOfferedContractEnv("h2", 16, 10))
	if enabled, err := qualificationMatchedOfferedMode(func(key string) string {
		if key == "P2PSTREAM_COLD_H2_DIAGNOSTIC_MAX_CONNS" {
			return "64"
		}
		return getenv(key)
	}, "matched-h2-offered-20s", "h2", 16, 200, 10, 0, 0); enabled || err == nil {
		t.Fatal("cold H2 diagnostic accepted")
	}
}

func TestQualificationMatchedOfferedTLSCipherAndIdentity(t *testing.T) {
	cert, leaf := qualificationMatchedTLSContractLeaf(t, "public.test")
	env := qualificationMatchedTLSContractEnv(t, leaf, "127.0.0.1:4443")
	matched, err := qualificationMatchedTLSFromEnvironment(qualificationMatchedTLSContractGetenv(env), "https://public.test:4443/direct/small", "h2")
	if err != nil {
		t.Fatal(err)
	}
	qualificationConfigureMatchedOfferedTLS(matched)
	other, _ := qualificationMatchedTLSContractLeaf(t, "public.test")
	base := tls.ConnectionState{ServerName: "public.test", Version: tls.VersionTLS13, CipherSuite: tls.TLS_AES_128_GCM_SHA256, CurveID: tls.X25519, NegotiatedProtocol: "h2", PeerCertificates: []*x509.Certificate{cert.Leaf}, VerifiedChains: [][]*x509.Certificate{{cert.Leaf}}}
	if err := matched.config.VerifyConnection(base); err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*tls.ConnectionState){
		func(s *tls.ConnectionState) { s.CipherSuite = tls.TLS_AES_256_GCM_SHA384 },
		func(s *tls.ConnectionState) { s.ServerName = "other.test" },
		func(s *tls.ConnectionState) { s.NegotiatedProtocol = "http/1.1" },
		func(s *tls.ConnectionState) { s.DidResume = true },
		func(s *tls.ConnectionState) { s.Version = tls.VersionTLS12 },
		func(s *tls.ConnectionState) { s.CurveID = tls.CurveP256 },
		func(s *tls.ConnectionState) { s.VerifiedChains = nil },
		func(s *tls.ConnectionState) { s.PeerCertificates = nil },
		func(s *tls.ConnectionState) {
			s.PeerCertificates = []*x509.Certificate{other.Leaf}
			s.VerifiedChains = [][]*x509.Certificate{{other.Leaf}}
		},
	} {
		state := base
		mutate(&state)
		if err := matched.config.VerifyConnection(state); err == nil {
			t.Fatal("TLS mismatch accepted")
		}
	}
}

// This intentionally exercises the complete load role at its true fixed
// schedule: two 20-request/1RPS fixtures, about 38 seconds total, no shortened
// schedule, latency claims or real proxy/agent coverage. The admin is a stub.
// CI opts in explicitly so ordinary package tests keep the fast contracts.
func TestQualificationMatchedOfferedLoadRoundTrip(t *testing.T) {
	if os.Getenv("P2PSTREAM_QUALIFICATION_MATCHED_OFFERED_ROUNDTRIP") != "1" {
		t.Skip("set P2PSTREAM_QUALIFICATION_MATCHED_OFFERED_ROUNDTRIP=1 to run the full offered-load schedule")
	}
	for _, protocol := range []string{"h1", "h2"} {
		t.Run(protocol, func(t *testing.T) {
			for _, entry := range os.Environ() {
				name, _, _ := strings.Cut(entry, "=")
				if strings.HasPrefix(name, qualificationMatchedTLSPrefix) {
					t.Setenv(name, "")
				}
			}
			t.Setenv("P2PSTREAM_COLD_H2_DIAGNOSTIC_MAX_CONNS", "")
			var persisted atomic.Int64
			server, leaf, seenSNI := qualificationMatchedTLSContractServer(t, protocol, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Host != "public.test" || r.URL.Path != "/direct/small" {
					t.Error("unexpected authority/path")
				}
				persisted.Add(1)
				w.Header().Set("X-Qualification-Origin-HTTP-Major", "1")
				_, _ = w.Write(make([]byte, 256))
			}))
			_, port, err := net.SplitHostPort(server.Listener.Addr().String())
			if err != nil {
				t.Fatal(err)
			}
			env := qualificationMatchedTLSContractEnv(t, leaf, net.JoinHostPort("127.0.0.1", port))
			for name, value := range qualificationMatchedOfferedContractEnv(protocol, 16, 1) {
				env[name] = value
			}
			admin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_ = json.NewEncoder(w).Encode(qualificationRuntime{EventStats: true, PersistedEvents: persisted.Load()})
			}))
			t.Cleanup(admin.Close)
			resultPath := filepath.Join(t.TempDir(), "result.json")
			for name, value := range map[string]string{"ROLE": "load", "URL": "https://public.test:" + port + "/direct/small", "PROTOCOL": protocol, "SCENARIO": "matched-" + protocol + "-offered-20s", "PATH": "direct", "ADMIN_URL": admin.URL, "RESOURCE_ENVELOPE": "p2pstream-local-recorder-stub", "RESULT": resultPath} {
				env[name] = value
			}
			for name, value := range env {
				t.Setenv(qualificationMatchedTLSPrefix+name, value)
			}
			TestProxyQualificationLoad(t)
			data, err := os.ReadFile(resultPath)
			if err != nil {
				t.Fatal(err)
			}
			var result qualificationResult
			if err := json.Unmarshal(data, &result); err != nil {
				t.Fatal(err)
			}
			if err := qualificationMatchedOfferedResult(&result, true); err != nil {
				t.Fatal(err)
			}
			prewarm := int64(0)
			if protocol == "h2" {
				prewarm = 1
			}
			if !seenSNI.Load() || persisted.Load() != 20+prewarm || result.PersistedAfterDrain != 20+prewarm || result.Attempted != 20 || result.OfferedSchedule.PlannedSeconds != 19 || result.OfferedSchedule.DispatchCompletedSeconds < 19 || result.WallSeconds < result.OfferedSchedule.DispatchCompletedSeconds || result.P95Milliseconds <= 0 || result.P99Milliseconds <= 0 {
				t.Fatalf("inexact offered fixture result=%+v", result)
			}
			// A reused endpoint may already contain rows. Absolute cumulative
			// conservation is controller-owned; this helper checks timed deltas.
			result.PersistedAfterDrain += 200
			if err := qualificationMatchedOfferedResult(&result, true); err != nil {
				t.Fatal(err)
			}
			for _, mutate := range []func(*qualificationResult){
				func(r *qualificationResult) { r.PersistedEventDelta-- },
				func(r *qualificationResult) { r.DroppedEventDelta++ },
				func(r *qualificationResult) { r.EventQueueAfterDrain++ },
				func(r *qualificationResult) { r.ResponseBytes-- },
				func(r *qualificationResult) { r.Errors++ },
				func(r *qualificationResult) { r.H2PrewarmRequests++ },
			} {
				bad := result
				mutate(&bad)
				if err := qualificationMatchedOfferedResult(&bad, true); err == nil {
					t.Fatal("invalid terminal result accepted")
				}
			}
			// Exercise the old matched duration branch using this same fixture.
			// Its positive duration and absent offered schedule remain unchanged.
			before := persisted.Load()
			scenario := "h1-keepalive-duration"
			if protocol == "h2" {
				scenario = "h2-multiplex-duration"
			}
			legacyPath := filepath.Join(t.TempDir(), "duration.json")
			for name, value := range map[string]string{"SCENARIO": scenario, "OFFERED_RPS": "0", "REQUESTS": "16", "DURATION_SECONDS": "0.02", "RESULT": legacyPath} {
				t.Setenv(qualificationMatchedTLSPrefix+name, value)
			}
			TestProxyQualificationLoad(t)
			legacyData, err := os.ReadFile(legacyPath)
			if err != nil {
				t.Fatal(err)
			}
			var legacy qualificationResult
			if err := json.Unmarshal(legacyData, &legacy); err != nil {
				t.Fatal(err)
			}
			if legacy.OfferedSchedule != nil || legacy.OfferedRPS != 0 || legacy.DurationSeconds != 0.02 || legacy.WarmupRequests != 0 || legacy.Attempted < 1 || legacy.UsefulCompleted != int64(legacy.Attempted) || legacy.Errors != 0 || legacy.ResponseBytes != 256*legacy.UsefulCompleted || !legacy.EventStatsCaptured || legacy.PersistedEventDelta != legacy.UsefulCompleted || legacy.PersistedAfterDrain != before+prewarm+legacy.UsefulCompleted || legacy.H2PrewarmEvents != prewarm || legacy.MatchedTLS == nil {
				t.Fatalf("duration contract changed: %+v", legacy)
			}
		})
	}
}
