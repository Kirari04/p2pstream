package server

import (
	"fmt"
	"strings"
	"testing"
)

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

func BenchmarkNormalizeRequestHostComparison(b *testing.B) {
	for _, tc := range []struct {
		name string
		host string
	}{
		{name: "DNS", host: "example.com"},
		{name: "DNSTrailingDot", host: "example.com."},
		{name: "DNSPort", host: "example.com:443"},
		{name: "BracketIPv6", host: "[2001:db8::1]:443"},
		{name: "BareIPv6", host: "2001:db8::1"},
		{name: "Malformed", host: "example.com:bad:tail"},
		{name: "LongDNSPort", host: strings.Repeat("a", 4096) + ".example:443"},
		{name: "LongColonTail", host: strings.Repeat("a", 4096) + ":bad:tail"},
	} {
		b.Run(tc.name, func(b *testing.B) {
			for block := 0; block < 5; block++ {
				for slot, implementation := range []struct {
					name      string
					normalize func(string) string
				}{
					{name: "Legacy", normalize: legacyNormalizeRequestHostForComparison},
					{name: "Current", normalize: normalizeRequestHost},
					{name: "Current", normalize: normalizeRequestHost},
					{name: "Legacy", normalize: legacyNormalizeRequestHostForComparison},
				} {
					b.Run(fmt.Sprintf("Block%02d/%s%d", block, implementation.name, slot), func(b *testing.B) {
						b.ReportAllocs()
						for b.Loop() {
							benchmarkNormalizedRequestHost = implementation.normalize(tc.host)
						}
					})
				}
			}
		})
	}
}
