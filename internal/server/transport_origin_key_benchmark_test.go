package server

import (
	"net/http"
	"net/url"
	"strings"
	"testing"

	"p2pstream/internal/db"
)

var benchmarkTransportOriginLookup http.RoundTripper

// Both arms receive this exact file and the same fixture helper. Snapshot
// construction and initial pool fill are outside the timer; every timed lookup
// includes origin-key construction, pool locking and last-used timestamp update.
// No request or socket is opened by this benchmark.
func BenchmarkTransportOriginWarmDirectLookup(b *testing.B) {
	for _, shape := range []string{"snapshot-host", "snapshot-ipv6", "snapshot-long-host", "manual-parsed", "manual-unparsed"} {
		b.Run(shape, func(b *testing.B) {
			origin := "https://UPSTREAM.example:8443"
			if shape == "snapshot-ipv6" {
				origin = "https://[2001:db8::1]:8443"
			}
			if shape == "snapshot-long-host" {
				origin = "https://" + strings.Repeat("a", 63) + "." + strings.Repeat("b", 63) + "." + strings.Repeat("c", 63) + ".example:8443"
			}
			target := transportOriginSnapshot(b, db.PublicRouteTarget{ID: 70, Url: origin}).RouteTargets[70]
			if shape == "manual-parsed" {
				parsed, err := url.Parse(origin)
				if err != nil {
					b.Fatal(err)
				}
				target = publicRouteTargetConfig{ID: 70, URL: origin, ParsedURL: parsed}
			}
			if shape == "manual-unparsed" {
				target = publicRouteTargetConfig{ID: 70, URL: origin}
			}
			pool := newDirectTransportPool(17)
			b.Cleanup(pool.closeAll)
			first := pool.publicRouteTargetTransport(target)
			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				benchmarkTransportOriginLookup = pool.publicRouteTargetTransport(target)
			}
			b.StopTimer()
			if pool.len() != 1 || benchmarkTransportOriginLookup != first {
				b.Fatal("warm lookup did not preserve exact single-entry transport reuse")
			}
		})
	}
}
