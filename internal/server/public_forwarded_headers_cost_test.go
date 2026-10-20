package server

import (
	"bufio"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"reflect"
	"strings"
	"testing"
)

// Independent historical input set: removing a production key must not also
// remove its spoofed test input or the retained oracle's deletion.
var publicForwardedIdentityHeaderNamesBeforeFixedDelete = [...]string{
	"Cf-Connecting-Ip", "Cf-Connecting-Ipv6", "Cloudfront-Viewer-Address",
	"Fastly-Client-Ip", "Fly-Client-Ip", "Forwarded", "True-Client-Ip",
	"X-Appengine-User-Ip", "X-Azure-Clientip", "X-Client-Ip", "X-Cluster-Client-Ip",
	"X-Envoy-External-Address", "X-Forwarded-Client-Ip", "X-Forwarded-For",
	"X-Forwarded-Host", "X-Forwarded-Proto", "X-Forwarded-Port",
	"X-Original-Forwarded-For", "X-Real-Ip",
}

func TestPublicForwardedIdentityHeaderNamesAreCanonical(t *testing.T) {
	seen := make(map[string]bool)
	for _, name := range publicForwardedIdentityHeaderNames {
		if canonical := http.CanonicalHeaderKey(name); canonical != name {
			t.Errorf("fixed deletion key %q is not canonical; want %q", name, canonical)
		}
		if seen[name] {
			t.Errorf("duplicate fixed deletion key %q", name)
		}
		seen[name] = true
	}
}

func TestPublicForwardedHeadersMatchBeforeFixedDelete(t *testing.T) {
	for _, tc := range []struct {
		name       string
		nilOut     bool
		nilIn      bool
		nilHeader  bool
		identity   *ClientIdentity
		configured []string
		manualKeys bool
	}{
		{name: "nil-out", nilOut: true},
		{name: "nil-in", nilIn: true},
		{name: "nil-in-and-header", nilIn: true, nilHeader: true},
		{name: "nil-header", nilHeader: true},
		{name: "direct-spoofed-claims"},
		{name: "trusted-ipv6", identity: &ClientIdentity{Resolved: netip.MustParseAddr("2001:db8::42"), Source: "trusted"}},
		{name: "unknown-identity", identity: &ClientIdentity{Unknown: true, Source: "trusted"}},
		{name: "custom-header-casing", configured: []string{"x-cUsTom-cLieNt-iP", "X-FoRwArDeD-FoR"}},
		{name: "trusted-custom-header", identity: &ClientIdentity{Resolved: netip.MustParseAddr("203.0.113.42"), Source: "trusted"}, configured: []string{"x-cUsTom-cLieNt-iP"}},
		{name: "manually-noncanonical-map-keys", manualKeys: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			in := httptest.NewRequest(http.MethodPost, "https://EXAMPLE.test:8443/resource", strings.NewReader("body-owned-by-inbound-request"))
			in.RemoteAddr = "198.51.100.42:12345"
			in.Header.Set("Content-Type", "application/octet-stream")
			in.Header.Set("X-Custom-Client-IP", "forged-custom")
			for _, name := range publicForwardedIdentityHeaderNamesBeforeFixedDelete {
				in.Header.Set(name, "forged-fixed")
			}
			if tc.manualKeys {
				in.Header["x-forwarded-for"] = []string{"manual-noncanonical"}
				in.Header["CF-CONNECTING-IP"] = []string{"manual-noncanonical"}
			}
			ctx := in.Context()
			if tc.identity != nil {
				ctx = WithClientIdentity(ctx, *tc.identity)
			}
			if tc.configured != nil {
				ctx = withClientIdentityHeaderNames(ctx, tc.configured)
			}
			in = in.WithContext(ctx)
			before := in.Header.Clone()
			actual, expected := in.Clone(ctx), in.Clone(ctx)
			body, contentLength := actual.Body, actual.ContentLength
			if tc.nilHeader {
				actual.Header, expected.Header = nil, nil
			}
			if tc.nilOut {
				actual, expected = nil, nil
			}
			incoming := in
			if tc.nilIn {
				incoming = nil
			}
			listener := publicListenerConfig{Protocol: publicListenerProtocolHTTPS, Port: 8443}
			applyTrustedForwardedHeadersBeforeFixedDelete(expected, incoming, listener)
			applyTrustedForwardedHeaders(actual, incoming, listener)
			if actual != nil {
				if !reflect.DeepEqual(actual.Header, expected.Header) {
					t.Fatalf("forwarded headers differ from retained baseline:\ngot %v\nwant %v", actual.Header, expected.Header)
				}
				if actual.Body != body || actual.ContentLength != contentLength || actual.Context() != ctx {
					t.Fatal("header sanitization changed body or request context ownership")
				}
				if tc.manualKeys && (actual.Header["x-forwarded-for"][0] != "manual-noncanonical" || actual.Header["CF-CONNECTING-IP"][0] != "manual-noncanonical") {
					t.Fatal("manually noncanonical map-key behavior changed")
				}
			}
			if !reflect.DeepEqual(in.Header, before) {
				t.Fatal("outbound sanitization mutated the inbound header map")
			}
		})
	}
}

func TestPublicForwardedHeadersStripParserCanonicalizedSpoofs(t *testing.T) {
	var wire strings.Builder
	wire.WriteString("GET / HTTP/1.1\r\nHost: public.example\r\n")
	for _, name := range publicForwardedIdentityHeaderNamesBeforeFixedDelete {
		wire.WriteString(strings.ToLower(name) + ": forged-lower\r\n")
		wire.WriteString(strings.ToUpper(name) + ": forged-upper\r\n")
	}
	wire.WriteString("x-CuStOm-cLiEnT-iP: forged-custom\r\nX-Keep: retained\r\n\r\n")
	in, err := http.ReadRequest(bufio.NewReader(strings.NewReader(wire.String())))
	if err != nil {
		t.Fatal(err)
	}
	in.RemoteAddr = "198.51.100.42:12345"
	in = in.WithContext(withClientIdentityHeaderNames(in.Context(), []string{"x-cUsTom-cLiEnT-iP"}))
	before := in.Header.Clone()
	out := in.Clone(in.Context())
	applyTrustedForwardedHeaders(out, in, publicListenerConfig{Protocol: publicListenerProtocolHTTPS})
	for _, name := range publicForwardedIdentityHeaderNamesBeforeFixedDelete {
		for _, value := range out.Header.Values(name) {
			if strings.Contains(value, "forged") {
				t.Errorf("attacker-controlled claim %q survived: %q", name, value)
			}
		}
	}
	if out.Header.Get("X-Custom-Client-IP") != "" || out.Header.Get("X-Keep") != "retained" {
		t.Fatal("configured claim was retained or unrelated header was removed")
	}
	if out.Header.Get("X-Forwarded-For") != "198.51.100.42" || out.Header.Get("X-Real-IP") != "198.51.100.42" {
		t.Fatal("direct peer identity did not replace spoofed claims")
	}
	if !reflect.DeepEqual(in.Header, before) {
		t.Fatal("parser-owned inbound headers were mutated")
	}
}

// This is the a7e8382f implementation, retained as a differential oracle. In
// particular Header.Del canonicalizes its argument, not existing map keys:
// manually inserted noncanonical keys remain untouched by both versions.
func applyTrustedForwardedHeadersBeforeFixedDelete(outReq, inReq *http.Request, listener publicListenerConfig) {
	if outReq == nil {
		return
	}
	if outReq.Header == nil {
		outReq.Header = make(http.Header)
	}
	for _, name := range publicForwardedIdentityHeaderNamesBeforeFixedDelete {
		outReq.Header.Del(name)
	}
	if inReq == nil {
		return
	}
	for _, name := range ClientIdentityHeaderNames(inReq.Context()) {
		outReq.Header.Del(name)
	}
	clientIP := ""
	if _, attached := ClientIdentityFromContext(inReq.Context()); attached {
		if addr, ok := ClientIdentityResolved(inReq.Context()); ok {
			clientIP = addr.String()
		}
	} else {
		clientIP = remoteAddrIP(inReq.RemoteAddr)
	}
	if clientIP != "" {
		outReq.Header.Set("X-Forwarded-For", clientIP)
		outReq.Header.Set("X-Real-IP", clientIP)
	}
	if host := normalizeRequestHost(inReq.Host); host != "" {
		outReq.Header.Set("X-Forwarded-Host", host)
	}
	proto := forwardedProtoForPublicListener(listener)
	outReq.Header.Set("X-Forwarded-Proto", proto)
	outReq.Header.Set("X-Forwarded-Port", forwardedPortForPublicListener(listener, proto))
}
