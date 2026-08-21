package server

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	p2pstreamv1 "p2pstream/gen/proto/p2pstream/v1"
)

func TestFillTrafficTraceResolutionRedactsTargetOrigin(t *testing.T) {
	event := &p2pstreamv1.TrafficTraceEvent{}
	fillTrafficTraceResolution(event, publicRouteResolution{
		Target: publicRouteTargetConfig{
			ID:         42,
			Name:       "sensitive",
			TargetType: publicRouteTargetTypeProxy,
			Transport:  publicRouteTargetTransportDirect,
			URL:        "https://user:pass@example.test/path?token=secret&debug=true",
		},
	})

	if strings.Contains(event.TargetOrigin, "pass") || strings.Contains(event.TargetOrigin, "secret") {
		t.Fatalf("target origin was not redacted: %q", event.TargetOrigin)
	}
	parsed, err := url.Parse(event.TargetOrigin)
	if err != nil {
		t.Fatalf("parse target origin: %v", err)
	}
	if parsed.Host != "example.test" || parsed.User == nil || parsed.User.Username() != trafficTraceRedactedValue {
		t.Fatalf("target origin lost non-sensitive parts: %q", event.TargetOrigin)
	}
	for name, values := range parsed.Query() {
		if len(values) != 1 || values[0] != trafficTraceRedactedValue {
			t.Fatalf("target origin query %s = %q, want redacted", name, values)
		}
	}
	if event.RouteTargetId != 42 {
		t.Fatalf("route target id = %d, want 42", event.RouteTargetId)
	}
}

func TestTrafficTraceRedactsAllQueryValuesAndNonAllowlistedHeaders(t *testing.T) {
	query := redactSensitiveQuery("debug=true&sig=abc123&email=user%40example.test")
	for _, secret := range []string{"true", "abc123", "user%40example.test"} {
		if strings.Contains(query, secret) {
			t.Fatalf("redacted query %q retained %q", query, secret)
		}
	}

	headers := sanitizedHeaderMap(map[string][]string{
		"Content-Type":     {"application/json"},
		"X-Signature":      {"secret-signature"},
		"X-Application-Id": {"private-tenant-id"},
		"Authorization":    {"Bearer token"},
	})
	if headers["Content-Type"] != "application/json" {
		t.Fatalf("safe Content-Type header = %q", headers["Content-Type"])
	}
	for _, name := range []string{"X-Signature", "X-Application-Id", "Authorization"} {
		if headers[name] != trafficTraceRedactedValue {
			t.Fatalf("header %s = %q, want redacted", name, headers[name])
		}
	}
}

func TestTrafficTraceURLRedactionFailsClosed(t *testing.T) {
	if got := redactSensitiveTraceURL("https://example.test/%zz?secret=value"); got != trafficTraceRedactedValue {
		t.Fatalf("invalid trace URL = %q, want fully redacted", got)
	}
}

func TestPublicTracePathPreservesRequestRedactionForDefaultRoute(t *testing.T) {
	app := NewApp(nil, nil)
	snapshot := &publicProxySnapshot{
		Listeners: map[int64]publicListenerConfig{1: {
			ID:      1,
			Enabled: true,
		}},
		RoutesByListener: map[int64][]publicRouteConfig{1: {{
			ID:         10,
			Enabled:    true,
			IsDefault:  true,
			PathPrefix: "/api",
		}}},
	}
	req := httptest.NewRequest(http.MethodGet, "http://example.test/private/value", nil)

	if got := publicTracePathForRequest(app, snapshot, 1, req); got != "/..." {
		t.Fatalf("default route trace path = %q, want request-derived redaction", got)
	}

	snapshot.RoutesByListener[1] = []publicRouteConfig{{
		ID:         11,
		Enabled:    true,
		PathPrefix: "/private",
	}}
	if got := publicTracePathForRequest(app, snapshot, 1, req); got != "/private/..." {
		t.Fatalf("matched route trace path = %q, want /private/...", got)
	}
}
