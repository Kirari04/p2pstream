//go:build linux

package server

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"sync/atomic"
	"testing"
	"time"
)

// qualificationNetworkRequestTimeout keeps all existing fixture invocations at
// exactly 30 seconds. The private network cohort explicitly opts into one longer
// deadline for a single H2 growth request, without changing endpoint policy.
func qualificationNetworkRequestTimeout(raw, scenario, protocol string, concurrency, requests int, offeredRPS, durationSeconds, warmupSeconds float64, prewarm bool) (time.Duration, int, error) {
	if raw == "" {
		return 30 * time.Second, 0, nil
	}
	if raw != "120" || protocol != "h2" || (scenario != "growth-download" && scenario != "growth-upload") ||
		concurrency != 1 || requests != 1 || offeredRPS != 0 || durationSeconds != 0 || warmupSeconds != 0 || prewarm {
		return 0, 0, fmt.Errorf("network timeout override requires 120 seconds and one unpaced H2 growth download/upload without prewarm")
	}
	return 120 * time.Second, 120, nil
}

func TestQualificationNetworkRequestTimeout(t *testing.T) {
	for _, scenario := range []string{"growth-download", "growth-upload", "h1-keepalive", "growth-upload-cancel"} {
		t.Run("default-"+scenario, func(t *testing.T) {
			timeout, reported, err := qualificationNetworkRequestTimeout("", scenario, "h1", 2, 4, 1, 2, 3, true)
			if err != nil || timeout != 30*time.Second || reported != 0 {
				t.Fatalf("default timeout=%v reported=%d error=%v", timeout, reported, err)
			}
		})
	}
	for _, scenario := range []string{"growth-download", "growth-upload"} {
		t.Run("enabled-"+scenario, func(t *testing.T) {
			timeout, reported, err := qualificationNetworkRequestTimeout("120", scenario, "h2", 1, 1, 0, 0, 0, false)
			if err != nil || timeout != 120*time.Second || reported != 120 {
				t.Fatalf("opt-in timeout=%v reported=%d error=%v", timeout, reported, err)
			}
		})
	}
	type invalidCase struct {
		name, raw, scenario, protocol string
		concurrency, requests         int
		offered, duration, warmup     float64
		prewarm                       bool
	}
	cases := []invalidCase{
		{name: "h1", protocol: "h1"}, {name: "missing-protocol", protocol: "missing"},
		{name: "tiny", scenario: "h2-keepalive"}, {name: "cancel", scenario: "growth-upload-cancel"},
		{name: "concurrent", concurrency: 2}, {name: "multiple", requests: 2},
		{name: "offered", offered: 1}, {name: "duration", duration: 1},
		{name: "warmup", warmup: 1}, {name: "prewarm", prewarm: true},
	}
	for _, raw := range []string{"0", "30", "119", "121", "-1", "120s", " 120", "120 ", "1e2", "invalid"} {
		cases = append(cases, invalidCase{name: "value-" + strconv.Quote(raw), raw: raw})
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			raw, scenario, protocol, concurrency, requests := tc.raw, tc.scenario, tc.protocol, tc.concurrency, tc.requests
			if raw == "" {
				raw = "120"
			}
			if scenario == "" {
				scenario = "growth-download"
			}
			if protocol == "" {
				protocol = "h2"
			}
			if concurrency == 0 {
				concurrency = 1
			}
			if requests == 0 {
				requests = 1
			}
			_, _, err := qualificationNetworkRequestTimeout(raw, scenario, protocol, concurrency, requests, tc.offered, tc.duration, tc.warmup, tc.prewarm)
			if err == nil {
				t.Fatal("invalid override accepted")
			}
		})
	}
}

func TestQualificationNetworkTimeoutResultOmission(t *testing.T) {
	for _, seconds := range []int{0, 120} {
		body, err := json.Marshal(qualificationResult{NetworkRequestTimeoutSeconds: seconds})
		if err != nil {
			t.Fatal(err)
		}
		var value map[string]json.RawMessage
		if err := json.Unmarshal(body, &value); err != nil {
			t.Fatal(err)
		}
		raw, present := value["network_request_timeout_seconds"]
		if (seconds == 0 && present) || (seconds == 120 && (!present || string(raw) != "120")) {
			t.Fatalf("timeout=%d report=%s", seconds, body)
		}
	}
}

// The small H2 round trip drives the actual load fixture, including its payload
// hashes and event-drain result, with a local recorder stub. It does not claim
// to reproduce a real proxy/agent path or exercise a 120-second wait.
func TestQualificationNetworkTimeoutGrowthRoundTrip(t *testing.T) {
	for _, override := range []string{"", "120"} {
		for _, scenario := range []string{"growth-download", "growth-upload"} {
			t.Run("timeout-"+override+"-"+scenario, func(t *testing.T) {
				const payload = 128<<10 + 17
				var persisted atomic.Int64
				var requestCount atomic.Int64
				fixture := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					requestCount.Add(1)
					if r.ProtoMajor != 2 {
						http.Error(w, "H2 required", http.StatusBadRequest)
						return
					}
					w.Header().Set("X-Qualification-Origin-HTTP-Major", "1")
					if scenario == "growth-download" {
						if r.Method != http.MethodGet || r.URL.Query().Get("bytes") != strconv.Itoa(payload) {
							http.Error(w, "incorrect download", http.StatusBadRequest)
							return
						}
						persisted.Add(1)
						w.Header().Set("Content-Length", strconv.Itoa(payload))
						for remaining := payload; remaining > 0; {
							n := min(remaining, len(qualificationGrowthBlock))
							if _, err := w.Write(qualificationGrowthBlock[:n]); err != nil {
								return
							}
							remaining -= n
						}
						return
					}
					if r.Method != http.MethodPost || r.ContentLength != payload {
						http.Error(w, "incorrect upload", http.StatusBadRequest)
						return
					}
					hash := sha256.New()
					n, err := io.Copy(hash, io.LimitReader(r.Body, payload+1))
					if err != nil || n != payload {
						http.Error(w, "inexact upload", http.StatusBadRequest)
						return
					}
					persisted.Add(1)
					_, _ = w.Write(hash.Sum(nil))
				}))
				fixture.EnableHTTP2 = true
				fixture.StartTLS()
				t.Cleanup(fixture.Close)
				admin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					_ = json.NewEncoder(w).Encode(qualificationRuntime{EventStats: true, PersistedEvents: persisted.Load()})
				}))
				t.Cleanup(admin.Close)
				resultPath := filepath.Join(t.TempDir(), "result.json")
				for name, value := range map[string]string{
					"ROLE": "load", "URL": fixture.URL + "/bulk", "SCENARIO": scenario,
					"PROTOCOL": "h2", "CONCURRENCY": "1", "REQUESTS": "1", "OFFERED_RPS": "0",
					"DURATION_SECONDS": "0", "WARMUP_SECONDS": "0", "GROWTH_PAYLOAD_BYTES": strconv.Itoa(payload),
					"NETWORK_TIMEOUT_SECONDS": override, "ADMIN_URL": admin.URL, "RESULT": resultPath,
					"RESOURCE_ENVELOPE": "p2pstream-local-recorder-stub",
					"PIDS":              "", "SERVER_PROCESS": "", "AGENT_PROCESSES": "", "H2_PREWARM": "",
				} {
					t.Setenv("P2PSTREAM_QUALIFICATION_"+name, value)
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
				wantTimeout := 0
				if override != "" {
					wantTimeout = 120
				}
				wantResponse := int64(payload)
				if scenario == "growth-upload" {
					wantResponse = sha256.Size
				}
				if requestCount.Load() != 1 || result.Attempted != 1 || result.UsefulCompleted != 1 || result.Errors != 0 ||
					result.StatusCodes["200"] != 1 || result.ObservedHTTPMajor != 2 || result.ResponseBytes != wantResponse ||
					result.NetworkRequestTimeoutSeconds != wantTimeout || !result.EventStatsCaptured ||
					result.PersistedEventDelta != 1 || result.PersistedAfterDrain != 1 || result.DroppedEventDelta != 0 ||
					result.EventQueueAfterDrain != 0 || result.EventDrainError != "" {
					t.Fatalf("inexact fixture round trip: requests=%d result=%+v", requestCount.Load(), result)
				}
			})
		}
	}
}
