package server

import (
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
	if !strings.Contains(event.TargetOrigin, "example.test") || !strings.Contains(event.TargetOrigin, "debug=true") {
		t.Fatalf("target origin lost non-sensitive parts: %q", event.TargetOrigin)
	}
	if event.RouteTargetId != 42 {
		t.Fatalf("route target id = %d, want 42", event.RouteTargetId)
	}
}
