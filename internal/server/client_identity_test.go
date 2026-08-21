package server

import (
	"context"
	"net/http/httptest"
	"net/netip"
	"reflect"
	"testing"
)

func TestClientIdentityResolverDirectPeerIgnoresSpoofedHeaders(t *testing.T) {
	resolver, err := NewClientIdentityResolver(nil, ClientIdentityResolverOptions{})
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest("GET", "http://example.test/", nil)
	req.RemoteAddr = "198.51.100.7:44321"
	req.Header.Set("X-Forwarded-For", "203.0.113.90")
	req.Header.Set("X-Real-IP", "203.0.113.91")
	req.Header.Set("CF-Connecting-IP", "203.0.113.92")

	identity := resolver.Resolve(req)
	if identity.Unknown || !identity.Direct || identity.Source != ClientIdentitySourceDirect {
		t.Fatalf("identity = %#v, want known direct peer", identity)
	}
	if got, want := identity.Resolved, netip.MustParseAddr("198.51.100.7"); got != want {
		t.Fatalf("resolved = %s, want %s", got, want)
	}
}

func TestClientIdentityResolverDisabledSourceDoesNotTrustPeer(t *testing.T) {
	resolver := mustClientIdentityResolver(t, []TrustedProxySource{{
		Name:       "disabled",
		Enabled:    false,
		Prefixes:   []netip.Prefix{netip.MustParsePrefix("192.0.2.0/24")},
		HeaderName: "X-Real-IP",
		HeaderMode: TrustedProxyHeaderSingleIP,
	}}, ClientIdentityResolverOptions{})
	req := httptest.NewRequest("GET", "http://example.test/", nil)
	req.RemoteAddr = "192.0.2.10:443"
	req.Header.Set("X-Real-IP", "203.0.113.1")

	identity := resolver.Resolve(req)
	if !identity.Direct || identity.Resolved != netip.MustParseAddr("192.0.2.10") {
		t.Fatalf("identity = %#v, disabled source must not be trusted", identity)
	}
}

func TestClientIdentityResolverSingleIPIsStrict(t *testing.T) {
	resolver := mustClientIdentityResolver(t, []TrustedProxySource{{
		Name:       "edge",
		Enabled:    true,
		Prefixes:   []netip.Prefix{netip.MustParsePrefix("192.0.2.0/24")},
		HeaderName: "X-Real-IP",
		HeaderMode: TrustedProxyHeaderSingleIP,
	}}, ClientIdentityResolverOptions{})

	tests := []struct {
		name   string
		values []string
		want   netip.Addr
		reason ClientIdentityUnknownReason
	}{
		{name: "valid ipv4", values: []string{" 203.0.113.4 "}, want: netip.MustParseAddr("203.0.113.4")},
		{name: "valid ipv6", values: []string{"2001:db8::4"}, want: netip.MustParseAddr("2001:db8::4")},
		{name: "missing", reason: ClientIdentityUnknownMissingHeader},
		{name: "comma list", values: []string{"203.0.113.4, 203.0.113.5"}, reason: ClientIdentityUnknownMalformedHeader},
		{name: "repeated", values: []string{"203.0.113.4", "203.0.113.4"}, reason: ClientIdentityUnknownMalformedHeader},
		{name: "ip with port", values: []string{"203.0.113.4:80"}, reason: ClientIdentityUnknownMalformedHeader},
		{name: "bracketed ipv6", values: []string{"[2001:db8::4]"}, reason: ClientIdentityUnknownMalformedHeader},
		{name: "zone", values: []string{"fe80::1%eth0"}, reason: ClientIdentityUnknownMalformedHeader},
		{name: "empty", values: []string{" "}, reason: ClientIdentityUnknownMalformedHeader},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest("GET", "http://example.test/", nil)
			req.RemoteAddr = "192.0.2.10:443"
			for _, value := range tc.values {
				req.Header.Add("X-Real-IP", value)
			}
			identity := resolver.Resolve(req)
			if tc.reason != "" {
				if !identity.Unknown || identity.Resolved.IsValid() || identity.UnknownReason != tc.reason {
					t.Fatalf("identity = %#v, want unresolved %q", identity, tc.reason)
				}
				return
			}
			if identity.Unknown || identity.Direct || identity.Resolved != tc.want || identity.Source != "edge" {
				t.Fatalf("identity = %#v, want resolved %s via edge", identity, tc.want)
			}
		})
	}
}

func TestClientIdentityResolverTrustedChainWalksRightToLeft(t *testing.T) {
	resolver := mustClientIdentityResolver(t, []TrustedProxySource{
		{
			Name:       "front",
			Enabled:    true,
			Prefixes:   []netip.Prefix{netip.MustParsePrefix("192.0.2.0/24")},
			HeaderName: "X-Forwarded-For",
			HeaderMode: TrustedProxyHeaderTrustedChain,
		},
		{
			Name:       "inner",
			Enabled:    true,
			Prefixes:   []netip.Prefix{netip.MustParsePrefix("198.51.100.0/24")},
			HeaderName: "X-Inner-Client-IP",
			HeaderMode: TrustedProxyHeaderSingleIP,
		},
	}, ClientIdentityResolverOptions{})
	req := httptest.NewRequest("GET", "http://example.test/", nil)
	req.RemoteAddr = "192.0.2.10:443"
	req.Header.Add("X-Forwarded-For", "203.0.113.9, 198.18.0.5")
	req.Header.Add("X-Forwarded-For", "198.51.100.7, 192.0.2.8")

	identity := resolver.Resolve(req)
	if identity.Unknown || identity.Resolved != netip.MustParseAddr("198.51.100.7") {
		t.Fatalf("identity = %#v, want single-IP source excluded from XFF trust domain", identity)
	}
}

func TestClientIdentityResolverTrustedChainSharesOnlySameHeaderChainSources(t *testing.T) {
	resolver := mustClientIdentityResolver(t, []TrustedProxySource{
		{
			Name:       "front",
			Enabled:    true,
			Prefixes:   []netip.Prefix{netip.MustParsePrefix("192.0.2.0/24")},
			HeaderName: "X-Forwarded-For",
			HeaderMode: TrustedProxyHeaderTrustedChain,
		},
		{
			Name:       "inner",
			Enabled:    true,
			Prefixes:   []netip.Prefix{netip.MustParsePrefix("198.51.100.0/24")},
			HeaderName: "X-Forwarded-For",
			HeaderMode: TrustedProxyHeaderTrustedChain,
		},
		{
			Name:       "different-chain",
			Enabled:    true,
			Prefixes:   []netip.Prefix{netip.MustParsePrefix("198.18.0.0/15")},
			HeaderName: "X-Other-Forwarded-For",
			HeaderMode: TrustedProxyHeaderTrustedChain,
		},
	}, ClientIdentityResolverOptions{})
	req := httptest.NewRequest("GET", "http://example.test/", nil)
	req.RemoteAddr = "192.0.2.10:443"
	req.Header.Set("X-Forwarded-For", "203.0.113.9, 198.18.0.5, 198.51.100.7, 192.0.2.8")

	identity := resolver.Resolve(req)
	if identity.Unknown || identity.Resolved != netip.MustParseAddr("198.18.0.5") {
		t.Fatalf("identity = %#v, want first hop outside same-header trusted-chain domain", identity)
	}
}

func TestClientIdentityResolverCloudFrontDoesNotTrustCloudflareSingleIPAsXFFHop(t *testing.T) {
	resolver := mustClientIdentityResolver(t, []TrustedProxySource{
		{
			Name:       "cloudfront",
			Enabled:    true,
			Prefixes:   []netip.Prefix{netip.MustParsePrefix("54.182.0.0/16")},
			HeaderName: "X-Forwarded-For",
			HeaderMode: TrustedProxyHeaderTrustedChain,
		},
		{
			Name:       "cloudflare",
			Enabled:    true,
			Prefixes:   []netip.Prefix{netip.MustParsePrefix("104.16.0.0/13")},
			HeaderName: "CF-Connecting-IP",
			HeaderMode: TrustedProxyHeaderSingleIP,
		},
	}, ClientIdentityResolverOptions{})
	req := httptest.NewRequest("GET", "http://example.test/", nil)
	req.RemoteAddr = "54.182.0.10:443"
	req.Header.Set("X-Forwarded-For", "1.2.3.4, 104.16.1.1")

	identity := resolver.Resolve(req)
	if identity.Unknown || identity.Resolved != netip.MustParseAddr("104.16.1.1") {
		t.Fatalf("identity = %#v, want real rightmost viewer instead of attacker-supplied left hop", identity)
	}
}

func TestClientIdentityResolverTrustedChainRejectsAllTrustedAndCapsInput(t *testing.T) {
	baseSource := TrustedProxySource{
		Name:       "front",
		Enabled:    true,
		Prefixes:   []netip.Prefix{netip.MustParsePrefix("192.0.2.0/24")},
		HeaderName: "X-Forwarded-For",
		HeaderMode: TrustedProxyHeaderTrustedChain,
	}
	tests := []struct {
		name    string
		options ClientIdentityResolverOptions
		header  string
		reason  ClientIdentityUnknownReason
	}{
		{name: "all trusted", header: "192.0.2.1, 192.0.2.2", reason: ClientIdentityUnknownNoUntrustedHop},
		{name: "too many hops", options: ClientIdentityResolverOptions{MaxHops: 2}, header: "203.0.113.1, 203.0.113.2, 203.0.113.3", reason: ClientIdentityUnknownMalformedHeader},
		{name: "too many bytes", options: ClientIdentityResolverOptions{MaxHeaderBytes: 10}, header: "203.0.113.1", reason: ClientIdentityUnknownMalformedHeader},
		{name: "empty hop", header: "203.0.113.1,,192.0.2.2", reason: ClientIdentityUnknownMalformedHeader},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			resolver := mustClientIdentityResolver(t, []TrustedProxySource{baseSource}, tc.options)
			req := httptest.NewRequest("GET", "http://example.test/", nil)
			req.RemoteAddr = "192.0.2.10:443"
			req.Header.Set("X-Forwarded-For", tc.header)
			identity := resolver.Resolve(req)
			if !identity.Unknown || identity.UnknownReason != tc.reason {
				t.Fatalf("identity = %#v, want unresolved %q", identity, tc.reason)
			}
		})
	}
}

func TestClientIdentityResolverOverlappingSourcesRequireAgreement(t *testing.T) {
	sources := []TrustedProxySource{
		{Name: "zeta", Enabled: true, Prefixes: []netip.Prefix{netip.MustParsePrefix("192.0.2.0/24")}, HeaderName: "X-Real-IP", HeaderMode: TrustedProxyHeaderSingleIP},
		{Name: "alpha", Enabled: true, Prefixes: []netip.Prefix{netip.MustParsePrefix("192.0.2.0/25")}, HeaderName: "CF-Connecting-IP", HeaderMode: TrustedProxyHeaderSingleIP},
	}
	resolver := mustClientIdentityResolver(t, sources, ClientIdentityResolverOptions{})

	t.Run("agree", func(t *testing.T) {
		req := httptest.NewRequest("GET", "http://example.test/", nil)
		req.RemoteAddr = "192.0.2.10:443"
		req.Header.Set("X-Real-IP", "203.0.113.8")
		req.Header.Set("CF-Connecting-IP", "203.0.113.8")
		identity := resolver.Resolve(req)
		if identity.Unknown || identity.Resolved != netip.MustParseAddr("203.0.113.8") || identity.Source != "alpha+zeta" {
			t.Fatalf("identity = %#v", identity)
		}
		if want := []string{"alpha", "zeta"}; !reflect.DeepEqual(identity.MatchedSources, want) {
			t.Fatalf("matched sources = %v, want %v", identity.MatchedSources, want)
		}
	})

	t.Run("conflict", func(t *testing.T) {
		req := httptest.NewRequest("GET", "http://example.test/", nil)
		req.RemoteAddr = "192.0.2.10:443"
		req.Header.Set("X-Real-IP", "203.0.113.8")
		req.Header.Set("CF-Connecting-IP", "203.0.113.9")
		identity := resolver.Resolve(req)
		if !identity.Unknown || identity.UnknownReason != ClientIdentityUnknownConflictingSource || identity.Resolved.IsValid() {
			t.Fatalf("identity = %#v, want conflict", identity)
		}
	})

	t.Run("one source missing", func(t *testing.T) {
		req := httptest.NewRequest("GET", "http://example.test/", nil)
		req.RemoteAddr = "192.0.2.10:443"
		req.Header.Set("X-Real-IP", "203.0.113.8")
		identity := resolver.Resolve(req)
		if !identity.Unknown || identity.UnknownReason != ClientIdentityUnknownMissingHeader || identity.Resolved.IsValid() {
			t.Fatalf("identity = %#v, want unresolved discrepancy", identity)
		}
	})
}

func TestClientIdentityResolverRejectsInvalidConfiguration(t *testing.T) {
	valid := TrustedProxySource{
		Name:       "edge",
		Enabled:    true,
		Prefixes:   []netip.Prefix{netip.MustParsePrefix("192.0.2.0/24")},
		HeaderName: "X-Real-IP",
		HeaderMode: TrustedProxyHeaderSingleIP,
	}
	tests := []struct {
		name    string
		sources []TrustedProxySource
		options ClientIdentityResolverOptions
	}{
		{name: "empty name", sources: []TrustedProxySource{func() TrustedProxySource { v := valid; v.Name = ""; return v }()}},
		{name: "duplicate name", sources: []TrustedProxySource{valid, valid}},
		{name: "invalid header", sources: []TrustedProxySource{func() TrustedProxySource { v := valid; v.HeaderName = "Bad Header"; return v }()}},
		{name: "invalid mode", sources: []TrustedProxySource{func() TrustedProxySource { v := valid; v.HeaderMode = "first"; return v }()}},
		{name: "no prefixes", sources: []TrustedProxySource{func() TrustedProxySource { v := valid; v.Prefixes = nil; return v }()}},
		{name: "bad byte cap", sources: []TrustedProxySource{valid}, options: ClientIdentityResolverOptions{MaxHeaderBytes: -1}},
		{name: "bad hop cap", sources: []TrustedProxySource{valid}, options: ClientIdentityResolverOptions{MaxHops: -1}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := NewClientIdentityResolver(tc.sources, tc.options); err == nil {
				t.Fatal("expected validation error")
			}
		})
	}
}

func TestClientIdentityContextHelpers(t *testing.T) {
	identity := ClientIdentity{
		Peer:           netip.MustParseAddr("192.0.2.1"),
		Resolved:       netip.MustParseAddr("203.0.113.1"),
		Source:         "edge",
		MatchedSources: []string{"edge"},
	}
	ctx := WithClientIdentity(context.Background(), identity)
	identity.MatchedSources[0] = "mutated"

	got, ok := ClientIdentityFromContext(ctx)
	if !ok || got.Source != "edge" || got.MatchedSources[0] != "edge" {
		t.Fatalf("identity = %#v, ok = %v", got, ok)
	}
	if peer, ok := ClientIdentityPeer(ctx); !ok || peer != netip.MustParseAddr("192.0.2.1") {
		t.Fatalf("peer = %s, ok = %v", peer, ok)
	}
	if resolved, ok := ClientIdentityResolved(ctx); !ok || resolved != netip.MustParseAddr("203.0.113.1") {
		t.Fatalf("resolved = %s, ok = %v", resolved, ok)
	}
	if ClientIdentityIsUnknown(ctx) || ClientIdentitySource(ctx) != "edge" {
		t.Fatalf("unexpected context helper result")
	}
	if !ClientIdentityIsUnknown(context.Background()) {
		t.Fatal("missing identity must be unknown")
	}
}

func TestClientIdentityResolverAttachesAllConfiguredHeaderNames(t *testing.T) {
	resolver := mustClientIdentityResolver(t, []TrustedProxySource{
		{
			Name:       "enabled",
			Enabled:    true,
			Prefixes:   []netip.Prefix{netip.MustParsePrefix("192.0.2.0/24")},
			HeaderName: "x-client-ip",
			HeaderMode: TrustedProxyHeaderSingleIP,
		},
		{
			Name:       "disabled",
			Enabled:    false,
			HeaderName: "CF-Connecting-IP",
		},
		{
			Name:       "duplicate",
			Enabled:    false,
			HeaderName: "X-Client-IP",
		},
		{
			Name:       "invalid-disabled",
			Enabled:    false,
			HeaderName: "Bad Header",
		},
	}, ClientIdentityResolverOptions{})
	want := []string{"Cf-Connecting-Ip", "X-Client-Ip"}
	configured := resolver.ConfiguredHeaderNames()
	if !reflect.DeepEqual(configured, want) {
		t.Fatalf("configured headers = %v, want %v", configured, want)
	}
	configured[0] = "mutated"
	if got := resolver.ConfiguredHeaderNames(); !reflect.DeepEqual(got, want) {
		t.Fatalf("configured headers mutated through caller: %v", got)
	}

	req := httptest.NewRequest("GET", "http://example.test/", nil)
	req.RemoteAddr = "198.51.100.7:443"
	resolved := resolver.ResolveRequest(req)
	fromContext := ClientIdentityHeaderNames(resolved.Context())
	if !reflect.DeepEqual(fromContext, want) {
		t.Fatalf("context headers = %v, want %v", fromContext, want)
	}
	fromContext[0] = "mutated"
	if got := ClientIdentityHeaderNames(resolved.Context()); !reflect.DeepEqual(got, want) {
		t.Fatalf("context headers mutated through caller: %v", got)
	}
}

func mustClientIdentityResolver(t *testing.T, sources []TrustedProxySource, options ClientIdentityResolverOptions) *ClientIdentityResolver {
	t.Helper()
	resolver, err := NewClientIdentityResolver(sources, options)
	if err != nil {
		t.Fatal(err)
	}
	return resolver
}
