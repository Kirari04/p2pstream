package server

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestPublicProxyDoesNotForwardManagementSessionCookie(t *testing.T) {
	for _, transport := range []string{publicRouteTargetTransportDirect, publicRouteTargetTransportAgent} {
		t.Run(transport, func(t *testing.T) {
			captured := make(chan []*http.Cookie, 1)
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				captured <- r.Cookies()
				w.WriteHeader(http.StatusNoContent)
			}))
			t.Cleanup(upstream.Close)

			origin, err := url.Parse(upstream.URL)
			if err != nil {
				t.Fatalf("parse upstream URL: %v", err)
			}
			target := publicRouteTargetConfig{
				ID: 20, Name: "cookie-boundary", Enabled: true,
				TargetType: publicRouteTargetTypeProxy, Transport: transport, ParsedURL: origin,
			}
			app := NewApp(nil, nil)
			var agent *AgentConn
			if transport == publicRouteTargetTransportAgent {
				agent, _ = newFakeYamuxAgent(t, 7, "cookie-agent")
				if err := app.AgentHub.connect(agent); err != nil {
					t.Fatalf("connect fake agent: %v", err)
				}
				t.Cleanup(func() { app.AgentHub.disconnect(agent) })
				app.agentStreamCapacity = mustNewDefaultAgentStreamCapacityManager(8)
			}

			req := httptest.NewRequest(http.MethodGet, "http://public.example/resource", nil)
			req.Header["Cookie"] = []string{
				"app=one; " + sessionCookieName + "=secret; P2pstream_session=application; duplicate=first; duplicate=second",
				"bad cookie=value; discarded=whole-line",
				"other=two; " + sessionCookieName + "=second-secret",
			}
			originalCookies := append([]string(nil), req.Header.Values("Cookie")...)
			recorder := httptest.NewRecorder()
			app.proxyAgentTargetRequest(recorder, req, publicRouteResolution{Target: target, Agent: agent}, nil, nil, nil, proxyRequestObservability{})
			if recorder.Code != http.StatusNoContent {
				t.Fatalf("proxy status = %d body %q", recorder.Code, recorder.Body.String())
			}
			if got := req.Header.Values("Cookie"); !stringSlicesEqual(got, originalCookies) {
				t.Fatalf("ingress cookies mutated = %#v, want %#v", got, originalCookies)
			}
			select {
			case cookies := <-captured:
				assertCookieValues(t, cookies, sessionCookieName, nil)
				assertCookieValues(t, cookies, "app", []string{"one"})
				assertCookieValues(t, cookies, "duplicate", []string{"first", "second"})
				assertCookieValues(t, cookies, "other", []string{"two"})
				assertCookieValues(t, cookies, "P2pstream_session", []string{"application"})
				assertCookieValues(t, cookies, "discarded", nil)
			case <-time.After(time.Second):
				t.Fatal("upstream request was not captured")
			}
		})
	}
}

func TestPublicTargetHealthSameOriginUsesEffectiveAuthority(t *testing.T) {
	tests := []struct {
		name       string
		initial    string
		redirected string
		want       bool
	}{
		{name: "http default port", initial: "http://EXAMPLE.test/health", redirected: "http://example.test:80/ready", want: true},
		{name: "https default port", initial: "https://example.test:443/health", redirected: "https://EXAMPLE.test/ready", want: true},
		{name: "hostname change", initial: "http://one.example/health", redirected: "http://two.example/ready"},
		{name: "port change", initial: "http://example.test:8080/health", redirected: "http://example.test:8081/ready"},
		{name: "scheme change", initial: "http://example.test:80/health", redirected: "https://example.test:80/ready"},
		{name: "same IPv6 zone", initial: "http://[fe80::1%25ethA]/health", redirected: "http://[FE80::1%25ethA]:80/ready", want: true},
		{name: "IPv6 zone change", initial: "http://[fe80::1%25ethA]/health", redirected: "http://[fe80::1%25etha]:80/ready"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			initial, err := url.Parse(test.initial)
			if err != nil {
				t.Fatalf("parse initial URL: %v", err)
			}
			redirected, err := url.Parse(test.redirected)
			if err != nil {
				t.Fatalf("parse redirected URL: %v", err)
			}
			if got := publicTargetHealthSameOrigin(initial, redirected); got != test.want {
				t.Fatalf("same-origin result = %v, want %v", got, test.want)
			}
		})
	}
}

func TestPublicProxyAppliesTrustedConfiguredCookieAfterIngressFiltering(t *testing.T) {
	captured := make(chan []*http.Cookie, 1)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		captured <- r.Cookies()
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(upstream.Close)
	origin, err := url.Parse(upstream.URL)
	if err != nil {
		t.Fatalf("parse upstream URL: %v", err)
	}
	target := publicRouteTargetConfig{
		ID: 21, Name: "configured-cookie", Enabled: true,
		TargetType: publicRouteTargetTypeProxy, Transport: publicRouteTargetTransportDirect, ParsedURL: origin,
		UpstreamRequestHeaders: []publicRequestHeader{{Name: "Cookie", Value: "operator=configured"}},
	}
	req := httptest.NewRequest(http.MethodGet, "http://public.example/resource", nil)
	req.Header.Set("Cookie", "app=client; "+sessionCookieName+"=secret")
	recorder := httptest.NewRecorder()
	NewApp(nil, nil).proxyAgentTargetRequest(recorder, req, publicRouteResolution{Target: target}, nil, nil, nil, proxyRequestObservability{})
	if recorder.Code != http.StatusNoContent {
		t.Fatalf("proxy status = %d body %q", recorder.Code, recorder.Body.String())
	}
	select {
	case cookies := <-captured:
		assertCookieValues(t, cookies, "operator", []string{"configured"})
		assertCookieValues(t, cookies, "app", nil)
		assertCookieValues(t, cookies, sessionCookieName, nil)
	case <-time.After(time.Second):
		t.Fatal("upstream request was not captured")
	}
}

func TestPublicTargetHealthRedirectFence(t *testing.T) {
	for _, transport := range []string{publicRouteTargetTransportDirect, publicRouteTargetTransportAgent} {
		for _, variant := range []string{"hostname", "port", "scheme"} {
			t.Run(transport+"/"+variant, func(t *testing.T) {
				var connections atomic.Int64
				var requests atomic.Int64
				var foreignRequests atomic.Int64
				foreign := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					foreignRequests.Add(1)
					w.WriteHeader(http.StatusOK)
				}))
				t.Cleanup(foreign.Close)
				var redirectLocation string
				initial := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					requests.Add(1)
					w.Header().Set("Location", redirectLocation)
					w.WriteHeader(http.StatusFound)
				}))
				initial.Config.ConnState = func(_ net.Conn, state http.ConnState) {
					if state == http.StateNew {
						connections.Add(1)
					}
				}
				initial.Start()
				t.Cleanup(initial.Close)
				initialURL, err := url.Parse(initial.URL)
				if err != nil {
					t.Fatalf("parse initial URL: %v", err)
				}
				switch variant {
				case "hostname":
					redirectLocation = "http://localhost:" + initialURL.Port() + "/ready"
				case "port":
					redirectLocation = foreign.URL + "/ready"
				case "scheme":
					redirectLocation = "https://" + initialURL.Host + "/ready"
				}

				backend := testHealthTarget(t, 501, transport, initial.URL)
				attempt := runHealthCheckForTransport(t, transport, backend)
				if attempt.Err != nil || attempt.StatusCode != http.StatusFound {
					t.Fatalf("redirect-fenced health attempt = status %d kind %q err %v, want returned 302", attempt.StatusCode, attempt.ErrorKind, attempt.Err)
				}
				if got := requests.Load(); got != 1 {
					t.Fatalf("initial authority received %d HTTP requests, want 1", got)
				}
				if got := connections.Load(); got != 1 {
					t.Fatalf("initial authority received %d connections, want 1", got)
				}
				if got := foreignRequests.Load(); got != 0 {
					t.Fatalf("foreign authority received %d requests, want 0", got)
				}
			})
		}
	}
}

func TestPublicTargetHealthAllowsSameOriginRedirect(t *testing.T) {
	for _, transport := range []string{publicRouteTargetTransportDirect, publicRouteTargetTransportAgent} {
		t.Run(transport, func(t *testing.T) {
			var requests atomic.Int64
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				if r.URL.Path == "/health" {
					w.Header().Set("Location", "/ready?probe=1")
					w.WriteHeader(http.StatusFound)
					return
				}
				if r.URL.Path != "/ready" || r.URL.Query().Get("probe") != "1" {
					t.Errorf("same-origin redirect target = %s", r.URL.RequestURI())
				}
				w.WriteHeader(http.StatusOK)
			}))
			t.Cleanup(server.Close)
			backend := testHealthTarget(t, 502, transport, server.URL)
			attempt := runHealthCheckForTransport(t, transport, backend)
			if attempt.Err != nil || attempt.StatusCode != http.StatusOK || attempt.ErrorKind != "success" {
				t.Fatalf("same-origin health attempt = status %d kind %q err %v", attempt.StatusCode, attempt.ErrorKind, attempt.Err)
			}
			if got := requests.Load(); got != 2 {
				t.Fatalf("same-origin request count = %d, want 2", got)
			}
		})
	}
}

func TestPublicTargetHealthPreservesTenRedirectLimit(t *testing.T) {
	var requests atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		w.Header().Set("Location", "/loop")
		w.WriteHeader(http.StatusFound)
	}))
	t.Cleanup(server.Close)
	backend := testHealthTarget(t, 503, publicRouteTargetTransportDirect, server.URL)
	attempt := runPublicRouteTargetHealthCheck(context.Background(), backend)
	if attempt.Err == nil || !strings.Contains(attempt.Err.Error(), "stopped after 10 redirects") {
		t.Fatalf("redirect-loop health error = %v, want 10-redirect limit", attempt.Err)
	}
	if got := requests.Load(); got != 10 {
		t.Fatalf("redirect-loop request count = %d, want 10", got)
	}
}

func runHealthCheckForTransport(t *testing.T, transport string, backend publicRouteTargetHealthConfig) publicRouteTargetHealthCheckAttempt {
	t.Helper()
	if transport == publicRouteTargetTransportDirect {
		return runPublicRouteTargetHealthCheck(context.Background(), backend)
	}
	app := NewApp(nil, nil)
	app.agentStreamCapacity = mustNewDefaultAgentStreamCapacityManager(16)
	agent, _ := newFakeYamuxAgent(t, 8, "health-redirect-agent")
	if err := app.AgentHub.connect(agent); err != nil {
		t.Fatalf("connect fake health agent: %v", err)
	}
	t.Cleanup(func() { app.AgentHub.disconnect(agent) })
	return app.runPublicRouteTargetHealthCheckViaAgent(context.Background(), backend, agent)
}

func assertCookieValues(t *testing.T, cookies []*http.Cookie, name string, want []string) {
	t.Helper()
	var got []string
	for _, cookie := range cookies {
		if cookie.Name == name {
			got = append(got, cookie.Value)
		}
	}
	if !stringSlicesEqual(got, want) {
		t.Fatalf("cookie %q values = %#v, want %#v", name, got, want)
	}
}

func stringSlicesEqual(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}
	return true
}
