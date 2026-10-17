package server

import "testing"

var benchmarkNormalizedRequestHost string

func BenchmarkNormalizeRequestHost(b *testing.B) {
	for _, tc := range []struct {
		name string
		host string
	}{
		{name: "DNS", host: "example.com"},
		{name: "DNSTrailingDot", host: "example.com."},
		{name: "DNSPort", host: "example.com:443"},
		{name: "IPv6Port", host: "[2001:db8::1]:443"},
		{name: "MalformedColons", host: "example.com:bad:tail"},
	} {
		b.Run(tc.name, func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				benchmarkNormalizedRequestHost = normalizeRequestHost(tc.host)
			}
		})
	}
}
