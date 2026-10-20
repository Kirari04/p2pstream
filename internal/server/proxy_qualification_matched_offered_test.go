//go:build linux

package server

import (
	"crypto/tls"
	"fmt"
	"math"
	"strconv"
	"strings"
)

// This finite qualification-only mode never changes duration workloads. Its
// requests are first-at-zero at one fixed rate for 20 seconds, with latency
// measured from the existing loadJob.scheduled instant, including queue delay.
func qualificationMatchedOfferedMode(getenv func(string) string, scenario, protocol string, concurrency, requests int, offeredRPS, durationSeconds, warmupSeconds float64) (bool, error) {
	if scenario != "matched-h1-offered-20s" && scenario != "matched-h2-offered-20s" {
		if strings.HasPrefix(scenario, "matched-") {
			return false, fmt.Errorf("unknown matched qualification scenario %q", scenario)
		}
		return false, nil
	}
	fail := func() (bool, error) {
		return false, fmt.Errorf("matched offered mode requires exact 20-second integer schedule, TLS, protocol and topology contract")
	}
	if getenv(qualificationMatchedTLSPrefix+"MATCHED_TLS") != "1" || scenario != "matched-"+protocol+"-offered-20s" || (protocol != "h1" && protocol != "h2") {
		return fail()
	}
	if (concurrency != 16 && concurrency != 128) || math.IsNaN(offeredRPS) || math.IsInf(offeredRPS, 0) || offeredRPS < 1 || offeredRPS > 100000 || math.Trunc(offeredRPS) != offeredRPS {
		return fail()
	}
	rate := int(offeredRPS)
	if requests != 20*rate || concurrency > requests || durationSeconds != 0 || warmupSeconds != 0 {
		return fail()
	}
	for name, expected := range map[string]string{"OFFERED_RPS": strconv.Itoa(rate), "CONCURRENCY": strconv.Itoa(concurrency), "REQUESTS": strconv.Itoa(requests), "DURATION_SECONDS": "0", "WARMUP_SECONDS": "0", "BALLAST_MIB": "0"} {
		if getenv(qualificationMatchedTLSPrefix+name) != expected {
			return fail()
		}
	}
	prewarm := ""
	if protocol == "h2" {
		prewarm = "1"
	}
	if getenv(qualificationMatchedTLSPrefix+"H2_PREWARM") != prewarm {
		return fail()
	}
	for _, name := range []string{"NETWORK_TIMEOUT_SECONDS", "GROWTH_PAYLOAD_BYTES", "CAPACITY_ESTABLISHED_H2", "ORIGIN_OBSERVER_URL", "UPLOAD_OBSERVATION_TOKEN"} {
		if getenv(qualificationMatchedTLSPrefix+name) != "" {
			return fail()
		}
	}
	if getenv("P2PSTREAM_COLD_H2_DIAGNOSTIC_MAX_CONNS") != "" {
		return fail()
	}
	admin := getenv(qualificationMatchedTLSPrefix+"ADMIN_URL") != ""
	if admin != strings.HasPrefix(getenv(qualificationMatchedTLSPrefix+"RESOURCE_ENVELOPE"), "p2pstream") {
		return fail()
	}
	path := "direct-control"
	if admin {
		path = "direct"
	}
	if getenv(qualificationMatchedTLSPrefix+"PATH") != path {
		return fail()
	}
	return true, nil
}

// TLS1.3 cipher selection is controlled by Go, so reject a nonmatching suite
// during verification rather than pretend tls.Config.CipherSuites pins it.
// This wraps the existing standard chain/SAN/SNI/DER/ALPN/X25519 verifier.
func qualificationConfigureMatchedOfferedTLS(matched *qualificationMatchedTLS) {
	verify := matched.config.VerifyConnection
	matched.config.VerifyConnection = func(state tls.ConnectionState) error {
		if err := verify(state); err != nil {
			return err
		}
		if state.CipherSuite != tls.TLS_AES_128_GCM_SHA256 {
			return fmt.Errorf("matched offered TLS requires AES128-GCM-SHA256")
		}
		return nil
	}
}

// Contract failures are checked after writing the raw result. Offered delivery,
// wall-time and comparative latency gates are external plan/validator decisions;
// a slow but correct row must remain available and must not trigger rate tuning.
func qualificationMatchedOfferedResult(result *qualificationResult, candidate bool) error {
	bad := func() error { return fmt.Errorf("matched offered terminal payload/event/TLS/topology contract failed") }
	n := result.Attempted
	if n < 1 || result.UsefulCompleted != int64(n) || result.Errors != 0 || result.CanceledErrors != 0 || result.ResponseBytes != int64(n)*256 || len(result.StatusCodes) != 1 || result.StatusCodes["200"] != n || len(result.StatusClasses) != 1 || result.StatusClasses["2xx"] != n {
		return bad()
	}
	if result.OfferedSchedule == nil || result.WarmupRequests != 0 || result.WarmupSeconds != 0 || result.DurationSeconds != 0 || result.OfferedRPS <= 0 || float64(n) != 20*result.OfferedRPS || result.OfferedSchedule.PlannedSeconds != float64(n-1)/result.OfferedRPS {
		return bad()
	}
	receipt := result.MatchedTLS
	if receipt == nil || receipt.ConnectionAttempts < 1 || receipt.ConnectionAttempts != receipt.VerifiedConnectionCount || receipt.VerificationFailureCount != 0 || receipt.ActiveHandshakes != 0 || receipt.ServerName != "public.test" || receipt.ObservedServerName != "public.test" || receipt.ExpectedLeafDERSHA256 == "" || receipt.ExpectedLeafDERSHA256 != receipt.ObservedLeafDERSHA256 || receipt.TLSVersion != tls.VersionTLS13 || receipt.CipherSuite != tls.TLS_AES_128_GCM_SHA256 || receipt.KeyExchangeGroup != uint16(tls.X25519) || receipt.DidResume || receipt.VerifiedChainCount != 1 {
		return bad()
	}
	if result.Protocol == "h2" {
		if result.ObservedHTTPMajor != 2 || result.ClientConnections != 1 || receipt.VerifiedConnectionCount != 1 || result.H2PrewarmRequests != 1 || !result.H2PrewarmConnectionReused || result.H2PrewarmAdditionalConnection || !receipt.H2PrewarmerVerifiedSameLeaf || receipt.ExpectedALPN != "h2" || receipt.ALPN != "h2" {
			return bad()
		}
	} else if result.Protocol != "h1" || result.ObservedHTTPMajor != 1 || result.ClientConnections != receipt.VerifiedConnectionCount || result.ClientConnections > int64(result.Concurrency) || result.H2PrewarmRequests != 0 || result.H2PrewarmEvents != 0 || receipt.H2PrewarmerVerifiedSameLeaf || receipt.ExpectedALPN != "http/1.1" || receipt.ALPN != "http/1.1" {
		return bad()
	}
	if candidate {
		prewarm := int64(result.H2PrewarmRequests)
		if !result.EventStatsCaptured || result.PersistedEventDelta != int64(n) || result.PersistedAfterDrain < int64(n)+prewarm || result.H2PrewarmEvents != prewarm || result.DroppedEventDelta != 0 || result.EventQueueAfterDrain != 0 || result.EventDrainError != "" || result.EventStatsError != "" {
			return bad()
		}
	}
	return nil
}
