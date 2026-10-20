package server

import (
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"p2pstream/internal/db"
)

func transportOriginSnapshot(t testing.TB, targets ...db.PublicRouteTarget) *publicProxySnapshot {
	t.Helper()
	for i := range targets {
		targets[i].RouteID = 1
		targets[i].TargetType = publicRouteTargetTypeProxy
		targets[i].Enabled = 1
	}
	snapshot, err := snapshotFromPublicRows(publicConfigRows{
		Routes:       []db.PublicRoute{{ID: 1, ListenerID: 1, Enabled: 1, PathPrefix: "/"}},
		RouteTargets: targets,
	})
	if err != nil {
		t.Fatal(err)
	}
	return snapshot
}

func TestTransportOriginSnapshotKeysAndIsolation(t *testing.T) {
	for _, tc := range []struct{ name, raw, want string }{
		{"http", "http://upstream.test", "http://upstream.test"},
		{"scheme-and-host-case", "HTTPS://UPSTREAM.test", "https://UPSTREAM.test"},
		{"explicit-default-port", "https://upstream.test:443/", "https://upstream.test:443"},
		{"nondefault-port", "http://upstream.test:9080", "http://upstream.test:9080"},
		{"ipv6", "https://[2001:db8::1]:8443", "https://[2001:db8::1]:8443"},
		{"trimmed-configuration", " \thttp://upstream.test/\n", "http://upstream.test"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			snapshot := transportOriginSnapshot(t, db.PublicRouteTarget{ID: 70, Url: tc.raw})
			mapped := snapshot.RouteTargets[70]
			routed := snapshot.RoutesByListener[1][0].Targets[0]
			for _, target := range []publicRouteTargetConfig{mapped, routed} {
				if got := routeTargetTransportOrigin(target); got != tc.want {
					t.Fatalf("configured origin = %q, want %q", got, tc.want)
				}
				app := NewApp(nil, nil)
				t.Cleanup(app.DirectTransports.closeAll)
				t.Cleanup(app.AgentTransports.closeAll)
				first := app.directTargetTransport(target)
				if first != app.directTargetTransport(target) {
					t.Fatal("loaded target failed to reuse direct transport")
				}
				changed := target
				changed.ID++
				if first == app.directTargetTransport(changed) {
					t.Fatal("loaded targets with distinct IDs shared transport")
				}
				changed = target
				changed.TLSSkipVerify = !changed.TLSSkipVerify
				if first == app.directTargetTransport(changed) {
					t.Fatal("loaded target TLS policies shared transport")
				}
				changed = target
				changed.UpstreamResponseHeaderTimeout = 2 * time.Second
				if first == app.directTargetTransport(changed) {
					t.Fatal("loaded target header timeouts shared transport")
				}
				agent := &AgentConn{AgentID: 7}
				selector := app.agentTargetTransport(agent, target).(*publicRouteAgentTransport)
				if selector.key.TargetOrigin != tc.want || selector.key.RouteTargetID != target.ID {
					t.Fatalf("agent factory lost configured origin/target: %+v", selector.key)
				}
			}
		})
	}
}

func TestTransportOriginManualFallback(t *testing.T) {
	for _, tc := range []struct {
		name   string
		target publicRouteTargetConfig
		want   string
	}{
		{"parsed-precedence", publicRouteTargetConfig{URL: "http://ignored.test", ParsedURL: &url.URL{Scheme: "https", Host: "EXAMPLE.test:443", Path: "/ignored"}}, "https://EXAMPLE.test:443"},
		{"unparsed-userinfo-omitted", publicRouteTargetConfig{URL: "http://user:secret@upstream.test:8080/path?q=value"}, "http://upstream.test:8080"},
		{"unparsed-ipv6", publicRouteTargetConfig{URL: "https://[2001:db8::2]:8443/path"}, "https://[2001:db8::2]:8443"},
		{"partial-parsed", publicRouteTargetConfig{URL: "http://fallback.test:80", ParsedURL: &url.URL{Host: "incomplete.test"}}, "http://fallback.test:80"},
		{"relative", publicRouteTargetConfig{URL: "/relative"}, "/relative"},
		{"invalid-escape", publicRouteTargetConfig{URL: "http://upstream.test/%zz"}, "http://upstream.test/%zz"},
		{"zero", publicRouteTargetConfig{}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := routeTargetTransportOrigin(tc.target); got != tc.want {
				t.Fatalf("manual origin = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestTransportOriginSnapshotReloadRetiresOldOrigin(t *testing.T) {
	closed := make(chan struct{}, 1)
	oldOrigin := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "old")
	}))
	oldOrigin.Config.ConnState = func(_ net.Conn, state http.ConnState) {
		if state == http.StateClosed {
			select {
			case closed <- struct{}{}:
			default:
			}
		}
	}
	oldOrigin.Start()
	t.Cleanup(oldOrigin.Close)
	newOrigin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "new")
	}))
	t.Cleanup(newOrigin.Close)
	app := NewApp(nil, nil)
	t.Cleanup(app.DirectTransports.closeAll)
	t.Cleanup(app.AgentTransports.closeAll)
	previous := transportOriginSnapshot(t,
		db.PublicRouteTarget{ID: 70, Url: oldOrigin.URL},
		db.PublicRouteTarget{ID: 71, Url: oldOrigin.URL, Transport: publicRouteTargetTransportAgent},
		db.PublicRouteTarget{ID: 72, Url: oldOrigin.URL},
	)
	oldTransport := app.directTargetTransport(previous.RouteTargets[70])
	keptTransport := app.directTargetTransport(previous.RouteTargets[72])
	agent := &AgentConn{AgentID: 7}
	oldAgentTransport := seedAgentRouteTransportForTest(app, agent, previous.RouteTargets[71])
	request := func(target publicRouteTargetConfig, want string) {
		t.Helper()
		response := httptest.NewRecorder()
		input := httptest.NewRequest(http.MethodGet, "http://attacker.invalid/resource", nil)
		input.Header.Set("X-Forwarded-Host", "another-attacker.invalid")
		proxyDirectTargetForTest(app, response, input, target)
		if response.Code != http.StatusOK || response.Body.String() != want {
			t.Fatalf("configured-origin request = %d %q, want 200 %q", response.Code, response.Body.String(), want)
		}
	}
	request(previous.RouteTargets[70], "old")
	current := transportOriginSnapshot(t,
		db.PublicRouteTarget{ID: 70, Url: newOrigin.URL},
		db.PublicRouteTarget{ID: 71, Url: newOrigin.URL, Transport: publicRouteTargetTransportAgent},
		db.PublicRouteTarget{ID: 72, Url: oldOrigin.URL},
	)
	select {
	case <-closed:
		t.Fatal("old origin connection closed before reconciliation")
	default:
	}
	app.reconcileRouteTargetTransports(previous, current)
	if app.DirectTransports.len() != 1 || app.AgentTransports.len() != 0 {
		t.Fatal("reload failed to retire changed origins while retaining unchanged target")
	}
	select {
	case <-closed:
	case <-time.After(5 * time.Second):
		t.Fatal("old origin's idle connection did not close after reload")
	}
	if got := app.directTargetTransport(current.RouteTargets[72]); got != keptTransport {
		t.Fatal("unchanged origin lost its pooled transport")
	}
	if got := app.directTargetTransport(current.RouteTargets[70]); got == oldTransport {
		t.Fatal("changed origin reused old direct transport")
	}
	if got := seedAgentRouteTransportForTest(app, agent, current.RouteTargets[71]); got == oldAgentTransport {
		t.Fatal("changed origin reused old agent transport")
	}
	if got := routeTargetTransportOrigin(previous.RouteTargets[70]); got != oldOrigin.URL {
		t.Fatal("new snapshot mutated the prior snapshot's origin")
	}
	request(current.RouteTargets[70], "new")
	if app.DirectTransports.len() != 2 || app.AgentTransports.len() != 1 {
		t.Fatal("unexpected transport entries after replacement")
	}
}
