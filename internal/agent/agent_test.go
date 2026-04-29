package agent

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/google/uuid"

	"p2pstream/httpmsg"
	"p2pstream/msg"
)

func TestHandleRequestTLSMetadataControlsVerification(t *testing.T) {
	target := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get(httpmsg.MetadataTLSSkipVerify) != "" {
			t.Fatalf("internal TLS metadata was forwarded upstream")
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	}))
	defer target.Close()

	status, _ := performAgentRequest(t, target.URL, false)
	if status != http.StatusBadGateway {
		t.Fatalf("expected self-signed upstream to fail without tls skip verify, got %d", status)
	}

	status, body := performAgentRequest(t, target.URL, true)
	if status != http.StatusOK || body != "ok" {
		t.Fatalf("expected tls skip verify request to succeed, got status=%d body=%q", status, body)
	}
}

func performAgentRequest(t *testing.T, targetURL string, tlsSkipVerify bool) (int, string) {
	t.Helper()

	id, err := uuid.NewV7()
	if err != nil {
		t.Fatalf("new request id: %v", err)
	}
	req, err := http.NewRequest(http.MethodGet, targetURL, nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	enc := httpmsg.NewRequestEncoderWithMetadata(id, req, map[string]string{
		httpmsg.MetadataTLSSkipVerify: strconv.FormatBool(tlsSkipVerify),
	})
	firstReq, err := enc.Next()
	if err != nil {
		t.Fatalf("encode request: %v", err)
	}

	reqCh := make(chan *msg.Request, 10)
	reqCh <- firstReq
	writeCh = make(chan *msg.Request, 10)

	done := make(chan struct{})
	go func() {
		defer close(done)
		handleRequest(id, reqCh)
	}()

	var firstResp *msg.Request
	select {
	case firstResp = <-writeCh:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for agent response")
	}

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for agent request handler")
	}

	resp, err := httpmsg.DecodeResponse(firstResp, &httpmsg.ChannelStream{Ch: writeCh})
	if err != nil {
		t.Fatalf("decode response: %v", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read response body: %v", err)
	}
	return resp.StatusCode, string(body)
}
