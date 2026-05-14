package server

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"p2pstream/httpmsg"
	"p2pstream/msg"
)

func TestDirectProxyResponseHeaderTimeoutReturnsGatewayTimeout(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(150 * time.Millisecond)
		_, _ = w.Write([]byte("too late"))
	}))
	defer upstream.Close()

	origin, err := url.Parse(upstream.URL)
	if err != nil {
		t.Fatalf("parse upstream URL: %v", err)
	}
	app := NewApp(nil, nil)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "http://public.test/slow", nil)
	app.proxyDirectRequest(rec, req, publicRouteResolution{
		Backend: publicBackendConfig{
			ID:                            1,
			Name:                          "slow-direct",
			Enabled:                       true,
			BackendType:                   publicBackendTypeProxyForward,
			ForwardMode:                   publicBackendForwardModeDirect,
			ParsedOrigin:                  origin,
			UpstreamResponseHeaderTimeout: 25 * time.Millisecond,
		},
	}, nil, nil, nil, proxyRequestObservability{})

	if rec.Code != http.StatusGatewayTimeout {
		t.Fatalf("direct timeout status = %d body=%q, want 504", rec.Code, rec.Body.String())
	}
}

func TestAgentProxySendsResponseHeaderTimeoutMetadata(t *testing.T) {
	origin, err := url.Parse("http://127.0.0.1:8888")
	if err != nil {
		t.Fatalf("parse upstream URL: %v", err)
	}
	app := NewApp(nil, nil)
	agent := testAgentConn(7, "agent-7")
	if err := app.AgentHub.connect(agent); err != nil {
		t.Fatalf("connect agent: %v", err)
	}
	defer app.AgentHub.disconnect(agent)

	backend := publicBackendConfig{
		ID:                            1,
		Name:                          "agent-timeout",
		Enabled:                       true,
		BackendType:                   publicBackendTypeProxyForward,
		ForwardMode:                   publicBackendForwardModeAgentPool,
		ParsedOrigin:                  origin,
		UpstreamResponseHeaderTimeout: 45 * time.Second,
		AgentAssignments: []publicBackendAgentConfig{{
			BackendID: 1,
			AgentID:   7,
			Position:  0,
			Weight:    100,
			Enabled:   true,
		}},
	}
	snap := &publicProxySnapshot{
		Backends: map[int64]publicBackendConfig{backend.ID: backend},
		Agents:   map[int64]publicAgentConfig{7: {ID: 7, PublicID: "agent-7", Enabled: true}},
	}
	app.proxyMu.Lock()
	app.publicSnapshot = snap
	app.proxyMu.Unlock()
	app.BackendHealth.reconcile(app, snap, false)

	served := make(chan struct{})
	go func() {
		defer close(served)
		var first *msg.Request
		select {
		case first = <-agent.WriteCh:
		case <-time.After(2 * time.Second):
			t.Errorf("timed out waiting for agent request")
			return
		}
		if got := httpmsg.FirstHeaderValue(first.Headers, httpmsg.MetadataResponseHeaderTimeoutMillis); got != "45000" {
			t.Errorf("response header timeout metadata = %q, want 45000", got)
		}
		req, err := httpmsg.DecodeRequest(first, &httpmsg.ChannelStream{Ctx: context.Background(), Ch: agent.WriteCh})
		if err != nil {
			t.Errorf("decode request: %v", err)
			return
		}
		if req.Header.Get(httpmsg.MetadataResponseHeaderTimeoutMillis) != "" {
			t.Errorf("internal timeout metadata was forwarded upstream")
		}
		pendingValue, ok := app.PendingRequests.Load(first.ID)
		if !ok {
			t.Errorf("pending request %s not registered", first.ID.String())
			return
		}
		resp := &http.Response{
			StatusCode:    http.StatusOK,
			Status:        http.StatusText(http.StatusOK),
			Header:        make(http.Header),
			Body:          io.NopCloser(strings.NewReader("ok")),
			ContentLength: 2,
		}
		enc := httpmsg.NewResponseEncoder(first.ID, resp)
		pending := pendingValue.(*pendingAgentRequest)
		for {
			chunk, err := enc.Next()
			if err == io.EOF {
				return
			}
			if err != nil {
				t.Errorf("encode response: %v", err)
				return
			}
			pending.ResponseCh <- chunk
		}
	}()

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "http://public.test/agent", nil)
	app.proxyAgentRequest(rec, req, publicRouteResolution{Backend: backend}, nil, nil, nil, proxyRequestObservability{})
	if rec.Code != http.StatusOK || rec.Body.String() != "ok" {
		t.Fatalf("agent proxy response = status %d body %q, want 200 ok", rec.Code, rec.Body.String())
	}
	select {
	case <-served:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for fake agent")
	}
}
