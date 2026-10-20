package server

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"testing"
)

var benchmarkPublicForwardedHeaderRequest *http.Request

// Only existing production symbols are used, so this file can be copied
// unchanged into the a7e8382f baseline. The clone models ReverseProxy's owned
// outbound header map; request parsing and fixture setup are outside the timer.
func BenchmarkPublicForwardedHeaders(b *testing.B) {
	for _, shape := range []string{"small-direct", "browser-direct", "browser-trusted-custom", "large-128-spoofed"} {
		b.Run(shape, func(b *testing.B) {
			in := httptest.NewRequest(http.MethodGet, "https://public.example:8443/asset?q=value", nil)
			in.RemoteAddr = "198.51.100.42:12345"
			in.Header = http.Header{
				"User-Agent":      {"Mozilla/5.0 benchmark-client"},
				"Accept":          {"text/html,application/xhtml+xml,application/json;q=0.8,*/*;q=0.5"},
				"Accept-Encoding": {"gzip, br"},
			}
			if shape != "small-direct" {
				for name, value := range map[string]string{
					"Accept-Language": "en-GB,en;q=0.9", "Authorization": "Bearer application-token",
					"Cache-Control": "max-age=0", "Cookie": "session=opaque-cookie; settings=light",
					"Dnt": "1", "If-None-Match": `"resource-v1"`, "Origin": "https://public.example",
					"Referer": "https://public.example/page", "Sec-Fetch-Dest": "document",
					"Sec-Fetch-Mode": "navigate", "Sec-Fetch-Site": "same-origin",
				} {
					in.Header.Set(name, value)
				}
			}
			if shape == "browser-trusted-custom" {
				in.Header.Set("X-Custom-Client-IP", "203.0.113.42")
				ctx := WithClientIdentity(in.Context(), ClientIdentity{
					Peer: netip.MustParseAddr("198.51.100.42"), Resolved: netip.MustParseAddr("203.0.113.42"), Source: "trusted",
				})
				in = in.WithContext(withClientIdentityHeaderNames(ctx, []string{"X-Custom-Client-Ip"}))
			} else {
				in = directPublicClientIdentityResolver.ResolveRequest(in)
			}
			if shape == "large-128-spoofed" {
				for _, name := range []string{
					"Cf-Connecting-Ip", "Cf-Connecting-Ipv6", "Cloudfront-Viewer-Address",
					"Fastly-Client-Ip", "Fly-Client-Ip", "Forwarded", "True-Client-Ip",
					"X-Appengine-User-Ip", "X-Azure-Clientip", "X-Client-Ip", "X-Cluster-Client-Ip",
					"X-Envoy-External-Address", "X-Forwarded-Client-Ip", "X-Forwarded-For",
					"X-Forwarded-Host", "X-Forwarded-Proto", "X-Forwarded-Port",
					"X-Original-Forwarded-For", "X-Real-Ip",
				} {
					in.Header.Set(name, "forged-client-identity")
				}
				for index := 0; len(in.Header) < 128; index++ {
					in.Header.Set(fmt.Sprintf("X-Application-Field-%03d", index), "application-value-with-realistic-width")
				}
			}
			listener := publicListenerConfig{Protocol: publicListenerProtocolHTTPS, Port: 8443}
			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				out := in.Clone(in.Context())
				applyTrustedForwardedHeaders(out, in, listener)
				benchmarkPublicForwardedHeaderRequest = out
			}
		})
	}
}
