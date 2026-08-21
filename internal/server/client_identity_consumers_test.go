package server

import (
	"net/http"
	"net/http/httptest"
	"net/netip"
	"testing"
)

func TestPublicProxyContextUsesOneSnapshotForIdentityAndPolicy(t *testing.T) {
	trusted := mustClientIdentityResolver(t, []TrustedProxySource{{
		Name:       "edge-a",
		Enabled:    true,
		Prefixes:   []netip.Prefix{netip.MustParsePrefix("192.0.2.0/24")},
		HeaderName: "X-Visitor-IP",
		HeaderMode: TrustedProxyHeaderSingleIP,
	}}, ClientIdentityResolverOptions{})
	direct := mustClientIdentityResolver(t, nil, ClientIdentityResolverOptions{})
	snapshotA := &publicProxySnapshot{ClientIdentity: trusted}
	snapshotB := &publicProxySnapshot{ClientIdentity: direct}
	app := &App{}
	app.publicSnapshotPtr.Store(snapshotA)
	req := httptest.NewRequest(http.MethodGet, "https://app.example.test/path", nil)
	req.RemoteAddr = "192.0.2.10:443"
	req.Header.Set("X-Visitor-IP", "81.2.69.142")

	proxyContext := newPublicProxyContext(app, 7, httptest.NewRecorder(), req)
	app.publicSnapshotPtr.Store(snapshotB)

	if proxyContext.Snapshot != snapshotA {
		t.Fatalf("request snapshot = %p, want captured snapshot %p", proxyContext.Snapshot, snapshotA)
	}
	resolved, ok := ClientIdentityResolved(proxyContext.Request.Context())
	if !ok || resolved != netip.MustParseAddr("81.2.69.142") {
		t.Fatalf("resolved identity = %s, %v; want snapshot A result", resolved, ok)
	}
	if got := ClientIdentityHeaderNames(proxyContext.Request.Context()); len(got) != 1 || got[0] != "X-Visitor-Ip" {
		t.Fatalf("identity headers = %v, want snapshot A headers", got)
	}
}

func TestResolvedClientIdentityFeedsPolicyAndRateLimit(t *testing.T) {
	req := httptest.NewRequest("GET", "https://app.example.test/path", nil)
	req.RemoteAddr = "192.0.2.10:443"
	req = req.WithContext(WithClientIdentity(req.Context(), ClientIdentity{
		Peer:     netip.MustParseAddr("192.0.2.10"),
		Resolved: netip.MustParseAddr("203.0.113.9"),
		Source:   "edge",
	}))

	if got := remoteIPForRateLimit(req); got != "203.0.113.9" {
		t.Fatalf("rate-limit remote IP = %q, want resolved visitor", got)
	}
	activation := publicPolicyMatchActivation(publicListenerConfig{Protocol: publicListenerProtocolHTTPS}, req)
	if got := activation["remote_ip"]; got != "203.0.113.9" {
		t.Fatalf("CEL remote_ip = %#v, want resolved visitor", got)
	}
	if got := peerIPForRequest(req); got != "192.0.2.10" {
		t.Fatalf("peer IP = %q, want transport peer", got)
	}
}

func TestUnknownTrustedIdentityDoesNotFallBackToProxyPeer(t *testing.T) {
	req := httptest.NewRequest("GET", "https://app.example.test/path", nil)
	req.RemoteAddr = "192.0.2.10:443"
	req = req.WithContext(WithClientIdentity(req.Context(), ClientIdentity{
		Peer:          netip.MustParseAddr("192.0.2.10"),
		Unknown:       true,
		Source:        "edge",
		UnknownReason: ClientIdentityUnknownMalformedHeader,
	}))

	if got := remoteIPForRateLimit(req); got != rateLimitMissingValue {
		t.Fatalf("rate-limit remote IP = %q, want missing identity", got)
	}
	activation := publicPolicyMatchActivation(publicListenerConfig{Protocol: publicListenerProtocolHTTPS}, req)
	if got := activation["remote_ip"]; got != "" {
		t.Fatalf("CEL remote_ip = %#v, want empty identity", got)
	}

	out := httptest.NewRequest("GET", "http://upstream.test/", nil)
	applyTrustedForwardedHeaders(out, req, publicListenerConfig{Protocol: publicListenerProtocolHTTPS})
	if got := out.Header.Get("X-Forwarded-For"); got != "" {
		t.Fatalf("X-Forwarded-For = %q, want omitted for unresolved visitor", got)
	}
	if got := out.Header.Get("X-Real-IP"); got != "" {
		t.Fatalf("X-Real-IP = %q, want omitted for unresolved visitor", got)
	}
}

func TestResolvedClientIdentityFeedsGeneratedForwardingHeaders(t *testing.T) {
	in := httptest.NewRequest("GET", "https://app.example.test/path", nil)
	in.RemoteAddr = "192.0.2.10:443"
	in = in.WithContext(WithClientIdentity(in.Context(), ClientIdentity{
		Peer:     netip.MustParseAddr("192.0.2.10"),
		Resolved: netip.MustParseAddr("2001:db8::9"),
		Source:   "edge",
	}))
	out := httptest.NewRequest("GET", "http://upstream.test/", nil)

	applyTrustedForwardedHeaders(out, in, publicListenerConfig{Protocol: publicListenerProtocolHTTPS})
	if got := out.Header.Get("X-Forwarded-For"); got != "2001:db8::9" {
		t.Fatalf("X-Forwarded-For = %q, want resolved visitor", got)
	}
	if got := out.Header.Get("X-Real-IP"); got != "2001:db8::9" {
		t.Fatalf("X-Real-IP = %q, want resolved visitor", got)
	}
}

func TestForwardingHeadersStripConfiguredAndCommonClientIPClaims(t *testing.T) {
	commonClaims := []string{
		"CF-Connecting-IP",
		"CF-Connecting-IPv6",
		"CloudFront-Viewer-Address",
		"Fastly-Client-IP",
		"Fly-Client-IP",
		"Forwarded",
		"True-Client-IP",
		"X-AppEngine-User-IP",
		"X-Azure-ClientIP",
		"X-Client-IP",
		"X-Cluster-Client-IP",
		"X-Envoy-External-Address",
		"X-Forwarded-Client-IP",
		"X-Original-Forwarded-For",
	}
	tests := []struct {
		name         string
		source       TrustedProxySource
		remoteAddr   string
		customValue  string
		wantResolved string
	}{
		{
			name: "direct request with disabled source",
			source: TrustedProxySource{
				Name:       "disabled-custom",
				HeaderName: "X-Custom-Client-IP",
			},
			remoteAddr:   "198.51.100.7:443",
			customValue:  "203.0.113.90",
			wantResolved: "198.51.100.7",
		},
		{
			name: "unresolved trusted request",
			source: TrustedProxySource{
				Name:       "custom",
				Enabled:    true,
				Prefixes:   []netip.Prefix{netip.MustParsePrefix("192.0.2.0/24")},
				HeaderName: "X-Custom-Client-IP",
				HeaderMode: TrustedProxyHeaderSingleIP,
			},
			remoteAddr:  "192.0.2.10:443",
			customValue: "malformed attacker value",
		},
		{
			name: "resolved trusted request",
			source: TrustedProxySource{
				Name:       "custom",
				Enabled:    true,
				Prefixes:   []netip.Prefix{netip.MustParsePrefix("192.0.2.0/24")},
				HeaderName: "X-Custom-Client-IP",
				HeaderMode: TrustedProxyHeaderSingleIP,
			},
			remoteAddr:   "192.0.2.10:443",
			customValue:  "81.2.69.142",
			wantResolved: "81.2.69.142",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			resolver := mustClientIdentityResolver(t, []TrustedProxySource{tc.source}, ClientIdentityResolverOptions{})
			in := httptest.NewRequest(http.MethodGet, "https://app.example.test/path", nil)
			in.RemoteAddr = tc.remoteAddr
			in.Header.Set("X-Custom-Client-IP", tc.customValue)
			in.Header.Set("X-Forwarded-For", "attacker-xff")
			in.Header.Set("X-Real-IP", "attacker-real-ip")
			for _, name := range commonClaims {
				in.Header.Set(name, "attacker-claim")
			}
			in = resolver.ResolveRequest(in)
			out := httptest.NewRequest(http.MethodGet, "http://upstream.test/", nil)
			out.Header = in.Header.Clone()

			applyTrustedForwardedHeaders(out, in, publicListenerConfig{Protocol: publicListenerProtocolHTTPS})

			if got := out.Header.Get("X-Custom-Client-IP"); got != "" {
				t.Fatalf("configured client-IP header leaked upstream: %q", got)
			}
			for _, name := range commonClaims {
				if got := out.Header.Get(name); got != "" {
					t.Fatalf("common client-IP header %s leaked upstream: %q", name, got)
				}
			}
			if got := out.Header.Get("X-Forwarded-For"); got != tc.wantResolved {
				t.Fatalf("X-Forwarded-For = %q, want %q", got, tc.wantResolved)
			}
			if got := out.Header.Get("X-Real-IP"); got != tc.wantResolved {
				t.Fatalf("X-Real-IP = %q, want %q", got, tc.wantResolved)
			}
		})
	}
}
