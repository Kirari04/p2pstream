package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"reflect"
	"sync"
	"testing"
	"time"
)

func identityContextFixture(t testing.TB, sources []TrustedProxySource) *ClientIdentityResolver {
	t.Helper()
	r, err := NewClientIdentityResolver(sources, ClientIdentityResolverOptions{})
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func identityContextSource(name, header string, enabled bool, mode TrustedProxyHeaderMode) TrustedProxySource {
	return TrustedProxySource{Name: name, Enabled: enabled, HeaderName: header, HeaderMode: mode, Prefixes: []netip.Prefix{netip.MustParsePrefix("192.0.2.0/24")}}
}

func TestResolvedIdentityContextOwnershipAndPrecedence(t *testing.T) {
	sources := []TrustedProxySource{identityContextSource("trusted", "X-Visitor", true, TrustedProxyHeaderSingleIP)}
	resolver := identityContextFixture(t, sources)
	sources[0].Name = "changed"
	sources[0].HeaderName = "X-Changed"
	sources[0].Prefixes[0] = netip.MustParsePrefix("198.51.100.0/24")
	request := httptest.NewRequest(http.MethodGet, "http://identity.example/path", nil)
	request.RemoteAddr = "192.0.2.10:1234"
	request.Header.Set("X-Visitor", "198.51.100.20")
	prior := ClientIdentity{Unknown: true, Source: "prior", MatchedSources: []string{"prior"}}
	request = request.WithContext(withClientIdentityHeaderNames(WithClientIdentity(request.Context(), prior), []string{"X-Prior"}))
	originalContext := request.Context()
	resolved := resolver.ResolveRequest(request)
	if resolved == request || request.Context() != originalContext {
		t.Fatal("ResolveRequest did not preserve its input request/context")
	}
	expected := resolver.Resolve(request)
	got, ok := ClientIdentityFromContext(resolved.Context())
	if !ok || !reflect.DeepEqual(got, expected) {
		t.Fatalf("resolved identity = %+v/%v, want %+v", got, ok, expected)
	}
	names := ClientIdentityHeaderNames(resolved.Context())
	if !reflect.DeepEqual(names, []string{"X-Visitor"}) {
		t.Fatalf("headers = %v", names)
	}
	got.MatchedSources[0] = "mutated-return"
	names[0] = "X-Mutated-Return"
	configured := resolver.ConfiguredHeaderNames()
	configured[0] = "X-Mutated-Configured"
	again, _ := ClientIdentityFromContext(resolved.Context())
	if !reflect.DeepEqual(again, expected) || !reflect.DeepEqual(ClientIdentityHeaderNames(resolved.Context()), []string{"X-Visitor"}) {
		t.Fatal("returned slices mutated retained request identity or headers")
	}
	override := ClientIdentity{Unknown: true, Source: "override", MatchedSources: []string{"override"}}
	ctx := WithClientIdentity(resolved.Context(), override)
	override.MatchedSources[0] = "changed-after-insert"
	got, ok = ClientIdentityFromContext(ctx)
	if !ok || got.Source != "override" || !reflect.DeepEqual(got.MatchedSources, []string{"override"}) {
		t.Fatalf("identity override = %+v/%v", got, ok)
	}
	if !reflect.DeepEqual(ClientIdentityHeaderNames(ctx), []string{"X-Visitor"}) {
		t.Fatal("identity override changed header names")
	}
	replacement := []string{"X-Replacement"}
	ctx = withClientIdentityHeaderNames(resolved.Context(), replacement)
	replacement[0] = "X-Changed-After-Insert"
	if !reflect.DeepEqual(ClientIdentityHeaderNames(ctx), []string{"X-Replacement"}) {
		t.Fatal("header override failed or retained input slice")
	}
	got, ok = ClientIdentityFromContext(ctx)
	if !ok || !reflect.DeepEqual(got, expected) {
		t.Fatal("header override changed identity")
	}
	// A descendant with an invalid private value masks its ancestor, as with
	// context.WithValue. Readers must not silently fall back to the ancestor.
	invalid := context.WithValue(resolved.Context(), clientIdentityContextKey{}, "invalid")
	if got, ok = ClientIdentityFromContext(invalid); ok || !reflect.DeepEqual(got, ClientIdentity{}) {
		t.Fatalf("invalid identity = %+v/%v", got, ok)
	}
	if got := ClientIdentityHeaderNames(context.WithValue(resolved.Context(), clientIdentityHeaderNamesContextKey{}, "invalid")); len(got) != 0 {
		t.Fatalf("invalid headers = %v", got)
	}
	if got, ok = ClientIdentityFromContext(nil); ok || !reflect.DeepEqual(got, ClientIdentity{}) {
		t.Fatal("nil identity context changed")
	}
	if len(ClientIdentityHeaderNames(nil)) != 0 {
		t.Fatal("nil header context changed")
	}
	if resolver.ResolveRequest(nil) != nil {
		t.Fatal("nil request changed")
	}
}

func TestResolvedIdentityContextPreservesParentCancellationAndLifetime(t *testing.T) {
	type parentKey struct{}
	deadline := time.Now().Add(time.Minute)
	parent, cancel := context.WithDeadline(context.WithValue(context.Background(), parentKey{}, "parent-value"), deadline)
	defer cancel()
	request := httptest.NewRequest(http.MethodGet, "http://identity.example/", nil).WithContext(parent)
	request.RemoteAddr = "198.51.100.7:1234"
	resolver := identityContextFixture(t, nil)
	resolved := resolver.ResolveRequest(request)
	ctx := resolved.Context()
	if ctx.Value(parentKey{}) != "parent-value" || ctx.Done() != parent.Done() || ctx.Err() != nil {
		t.Fatal("parent value or cancellation channel changed")
	}
	if got, ok := ctx.Deadline(); !ok || !got.Equal(deadline) {
		t.Fatalf("deadline = %v/%v", got, ok)
	}
	child, childCancel := context.WithCancel(ctx)
	defer childCancel()
	cancel()
	select {
	case <-child.Done():
	case <-time.After(time.Second):
		t.Fatal("parent cancellation did not reach child")
	}
	if ctx.Err() != context.Canceled || child.Err() != context.Canceled {
		t.Fatal("cancellation error changed")
	}
	want, _ := ClientIdentityFromContext(ctx)
	for range 5 {
		other := httptest.NewRequest(http.MethodGet, "http://other.example/", nil)
		other.RemoteAddr = "203.0.113.8:99"
		_ = resolver.ResolveRequest(other)
	}
	got, ok := ClientIdentityFromContext(ctx)
	if !ok || !reflect.DeepEqual(got, want) {
		t.Fatal("later requests changed retained identity")
	}
	var nilResolver *ClientIdentityResolver
	if got, ok := ClientIdentityFromContext(nilResolver.ResolveRequest(request).Context()); !ok || !reflect.DeepEqual(got, resolver.Resolve(request)) {
		t.Fatalf("nil resolver changed direct identity: %+v/%v", got, ok)
	}
}

func TestResolvedIdentityContextConcurrentReadersAreIsolated(t *testing.T) {
	resolver := identityContextFixture(t, []TrustedProxySource{identityContextSource("trusted", "X-Visitor", true, TrustedProxyHeaderSingleIP)})
	request := httptest.NewRequest(http.MethodGet, "http://identity.example/path", nil)
	request.RemoteAddr = "192.0.2.10:1234"
	request.Header.Set("X-Visitor", "198.51.100.20")
	resolved := resolver.ResolveRequest(request)
	expected := resolver.Resolve(request)
	var wg sync.WaitGroup
	for range 16 {
		wg.Go(func() {
			for range 32 {
				got, ok := ClientIdentityFromContext(resolved.Context())
				if !ok || !reflect.DeepEqual(got, expected) {
					t.Error("concurrent reader changed identity")
					return
				}
				headers := ClientIdentityHeaderNames(resolved.Context())
				if !reflect.DeepEqual(headers, []string{"X-Visitor"}) {
					t.Error("concurrent reader changed headers")
					return
				}
				got.MatchedSources[0] = "reader-local"
				headers[0] = "X-Reader-Local"
				other := resolver.ResolveRequest(request)
				next, _ := ClientIdentityFromContext(other.Context())
				if !reflect.DeepEqual(next, expected) {
					t.Error("concurrent resolver changed identity")
					return
				}
			}
		})
	}
	wg.Wait()
}

var benchmarkResolvedIdentityRequest *http.Request
var benchmarkResolvedIdentity ClientIdentity
var benchmarkResolvedHeaderNames []string

func BenchmarkResolvedIdentityContext(b *testing.B) {
	cases := []struct {
		name    string
		sources []TrustedProxySource
		peer    string
		headers http.Header
	}{
		{name: "direct", peer: "198.51.100.10:443"},
		{name: "invalid-peer", peer: "invalid-peer", headers: http.Header{"X-Forwarded-For": []string{"192.0.2.1"}}},
		{name: "configured-untrusted", sources: []TrustedProxySource{identityContextSource("trusted", "X-Visitor", true, TrustedProxyHeaderSingleIP)}, peer: "198.51.100.10:443", headers: http.Header{"X-Visitor": []string{"192.0.2.15"}}},
		{name: "trusted-single", sources: []TrustedProxySource{identityContextSource("trusted", "X-Visitor", true, TrustedProxyHeaderSingleIP)}, peer: "192.0.2.10:443", headers: http.Header{"X-Visitor": []string{"198.51.100.15"}}},
		{name: "trusted-chain", sources: []TrustedProxySource{identityContextSource("trusted", "X-Forwarded-For", true, TrustedProxyHeaderTrustedChain)}, peer: "192.0.2.10:443", headers: http.Header{"X-Forwarded-For": []string{"198.51.100.15, 192.0.2.12"}}},
		{name: "conflicting-sources", sources: []TrustedProxySource{identityContextSource("first", "X-First", true, TrustedProxyHeaderSingleIP), identityContextSource("second", "X-Second", true, TrustedProxyHeaderSingleIP)}, peer: "192.0.2.10:443", headers: http.Header{"X-First": []string{"198.51.100.15"}, "X-Second": []string{"203.0.113.15"}}},
		{name: "four-disabled-headers", sources: []TrustedProxySource{identityContextSource("first", "X-First", false, TrustedProxyHeaderSingleIP), identityContextSource("second", "X-Second", false, TrustedProxyHeaderSingleIP), identityContextSource("third", "X-Third", false, TrustedProxyHeaderSingleIP), identityContextSource("fourth", "X-Fourth", false, TrustedProxyHeaderSingleIP)}, peer: "192.0.2.10:443"},
	}
	for _, tc := range cases {
		b.Run(tc.name, func(b *testing.B) {
			resolver := identityContextFixture(b, tc.sources)
			for _, parentKind := range []string{"background", "deadline"} {
				b.Run(parentKind, func(b *testing.B) {
					parent := context.Background()
					if parentKind == "deadline" {
						var cancel context.CancelFunc
						parent, cancel = context.WithDeadline(parent, time.Now().Add(time.Hour))
						defer cancel()
					}
					request := httptest.NewRequest(http.MethodGet, "http://identity.example/path", nil).WithContext(parent)
					request.RemoteAddr = tc.peer
					request.Header = tc.headers
					b.Run("resolve", func(b *testing.B) {
						b.ReportAllocs()
						b.ResetTimer()
						for range b.N {
							benchmarkResolvedIdentityRequest = resolver.ResolveRequest(request)
						}
					})
					b.Run("resolve-and-read", func(b *testing.B) {
						b.ReportAllocs()
						b.ResetTimer()
						for range b.N {
							r := resolver.ResolveRequest(request)
							benchmarkResolvedIdentityRequest = r
							benchmarkResolvedIdentity, _ = ClientIdentityFromContext(r.Context())
							benchmarkResolvedHeaderNames = ClientIdentityHeaderNames(r.Context())
						}
					})
				})
			}
		})
	}
}
