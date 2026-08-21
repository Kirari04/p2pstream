package server

import (
	"net/http"
	"testing"
)

func TestEnvironmentProxyMethodPolicyIsAllowlistBased(t *testing.T) {
	if _, ok := allowedEnvironmentProxyMethods["GetStatus"]; !ok {
		t.Fatal("expected operational GetStatus RPC to remain proxyable")
	}
	for _, method := range []string{"Login", "SetupAdmin", "CreateEnvironment", "FutureSensitiveMethod"} {
		if _, ok := allowedEnvironmentProxyMethods[method]; ok {
			t.Fatalf("sensitive or unknown method %q is proxyable", method)
		}
	}
}

func TestEnvironmentProxyResponseStripsBrowserStateHeaders(t *testing.T) {
	src := http.Header{
		"Content-Type":            {"application/json"},
		"Set-Cookie":              {"p2pstream_session=remote; Path=/"},
		"Clear-Site-Data":         {`"cookies", "storage"`},
		"Location":                {"https://attacker.example/redirect"},
		"Content-Security-Policy": {"script-src *"},
		"Refresh":                 {"0;url=https://attacker.example/"},
		"Connection":              {"close, X-Internal-Hop"},
		"X-Internal-Hop":          {"private-hop-value"},
	}
	dst := make(http.Header)
	copyEnvironmentProxyHeader(dst, src)
	if got := dst.Get("Content-Type"); got != "application/json" {
		t.Fatalf("Content-Type = %q, want application/json", got)
	}
	for _, name := range []string{"Set-Cookie", "Clear-Site-Data", "Location", "Content-Security-Policy", "Refresh", "Connection", "X-Internal-Hop"} {
		if values := dst.Values(name); len(values) != 0 {
			t.Fatalf("%s leaked from environment response: %v", name, values)
		}
	}
}

func TestEnvironmentProxyOnlyPassesConnectResponseTypes(t *testing.T) {
	for _, contentType := range []string{
		"application/json",
		"application/connect+json; charset=utf-8",
		"application/connect+proto",
		"application/proto",
	} {
		if !isEnvironmentProxyResponseContentTypeAllowed(contentType) {
			t.Fatalf("allowed response Content-Type %q was rejected", contentType)
		}
	}
	for _, contentType := range []string{"", "text/html", "text/javascript", "image/svg+xml", "application/octet-stream"} {
		if isEnvironmentProxyResponseContentTypeAllowed(contentType) {
			t.Fatalf("unsafe response Content-Type %q was allowed", contentType)
		}
	}
}

func TestEnvironmentProxyRequestStripsCredentialsAndConnectionHeaders(t *testing.T) {
	src := http.Header{
		"Content-Type":   {"application/json"},
		"Cookie":         {"p2pstream_session=local"},
		"Authorization":  {"Bearer local-token"},
		"Connection":     {"X-Internal-Hop"},
		"X-Internal-Hop": {"private-hop-value"},
	}
	got := cloneEnvironmentProxyHeader(src)
	if got.Get("Content-Type") != "application/json" {
		t.Fatalf("Content-Type = %q", got.Get("Content-Type"))
	}
	for _, name := range []string{"Cookie", "Authorization", "Connection", "X-Internal-Hop"} {
		if values := got.Values(name); len(values) != 0 {
			t.Fatalf("%s leaked to environment request: %v", name, values)
		}
	}
}
