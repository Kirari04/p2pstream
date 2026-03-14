package e2e_test

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/google/uuid"
	"p2pstream/httpmsg"
	"p2pstream/msg"
)

// --- MOCK SERVER LOGIC ---
// We replicate the core proxy logic from cmd/server here for the test.

type agentConn struct {
	writeCh chan *msg.Request
}

var (
	activeAgent     atomic.Pointer[agentConn]
	pendingRequests sync.Map // map[uuid.UUID]chan *msg.Request
)

func wsHandler(w http.ResponseWriter, r *http.Request) {
	c, err := websocket.Accept(w, r, nil)
	if err != nil {
		return
	}
	defer c.Close(websocket.StatusInternalError, "internal server error")

	ctx := context.Background()
	agent := &agentConn{writeCh: make(chan *msg.Request, 100)}
	activeAgent.Store(agent)
	defer activeAgent.CompareAndSwap(agent, nil)

	go func() {
		for req := range agent.writeCh {
			cw, err := c.Writer(ctx, websocket.MessageBinary)
			if err != nil {
				return
			}
			if _, err := req.WriteTo(cw); err != nil {
				cw.Close()
				return
			}
			cw.Close()
		}
	}()

	for {
		_, cr, err := c.Reader(ctx)
		if err != nil {
				break
		}

		msgBytes, err := io.ReadAll(cr)
		if err != nil {
				continue
		}

		m, err := msg.ParseRequest(bytes.NewReader(msgBytes))
		if err != nil {
				continue
		}

		if ch, ok := pendingRequests.Load(m.ID); ok {
			ch.(chan *msg.Request) <- m
		} else {
			}
	}
}

func proxyHandler(w http.ResponseWriter, r *http.Request) {
	agent := activeAgent.Load()
	if agent == nil {
		http.Error(w, "No agent", http.StatusServiceUnavailable)
		return
	}

	id, _ := uuid.NewV7()
	respCh := make(chan *msg.Request, 100)
	pendingRequests.Store(id, respCh)
	defer pendingRequests.Delete(id)

	enc := httpmsg.NewRequestEncoder(id, r)
	for {
		m, err := enc.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			http.Error(w, "Encode err", 500)
			return
		}
		agent.writeCh <- m
	}

	stream := &httpmsg.ChannelStream{Ch: respCh}
	firstMsg, err := stream.Next()
	if err != nil {
		http.Error(w, "Read resp headers err", 502)
		return
	}

	resp, err := httpmsg.DecodeResponse(firstMsg, stream)
	if err != nil {
		http.Error(w, "Decode resp err", 500)
		return
	}

	for k, vv := range resp.Header {
		for _, v := range vv {
			w.Header().Add(k, v)
		}
	}
	w.WriteHeader(resp.StatusCode)
	if resp.Body != nil {
		defer resp.Body.Close()
		io.Copy(w, resp.Body)
	}
}

// --- MOCK AGENT LOGIC ---

var (
	incomingRequests sync.Map
	agentWriteCh     chan *msg.Request
)

func runAgent(ctx context.Context, wsURL string) error {
	c, _, err := websocket.Dial(ctx, wsURL, nil)
	if err != nil {
		return err
	}
	defer c.Close(websocket.StatusInternalError, "agent stopping")

	agentWriteCh = make(chan *msg.Request, 100)

	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case req := <-agentWriteCh:
				cw, err := c.Writer(ctx, websocket.MessageBinary)
				if err != nil {
					return
				}
				req.WriteTo(cw)
				cw.Close()
			}
		}
	}()

	for {
		select {
		case <-ctx.Done():
			return nil
		default:
			_, cr, err := c.Reader(ctx)
			if err != nil {
				return err
			}

			msgBytes, err := io.ReadAll(cr)
			if err != nil {
				continue
			}

			m, err := msg.ParseRequest(bytes.NewReader(msgBytes))
			if err != nil {
				continue
			}

			if m.Type == msg.RequestTypeHeader || m.Type == msg.RequestTypeHeaderAndBody {
				reqCh := make(chan *msg.Request, 100)
				incomingRequests.Store(m.ID, reqCh)
				reqCh <- m
				go handleAgentRequest(m.ID, reqCh)
			} else {
				if ch, ok := incomingRequests.Load(m.ID); ok {
					ch.(chan *msg.Request) <- m
				} else {
				}
			}
		}
	}
}

func handleAgentRequest(id uuid.UUID, reqCh chan *msg.Request) {
	defer incomingRequests.Delete(id)
	
	stream := &httpmsg.ChannelStream{Ch: reqCh}
	firstMsg, err := stream.Next()
	if err != nil {
		return
	}

	req, err := httpmsg.DecodeRequest(firstMsg, stream)
	if err != nil {
		return
	}

	dump, _ := httputil.DumpRequest(req, true)
	bodyText := fmt.Sprintf("AGENT_ECHO\n%s", string(dump))

	resp := &http.Response{
		StatusCode:    200,
		Header:        make(http.Header),
		Body:          io.NopCloser(bytes.NewReader([]byte(bodyText))),
		ContentLength: int64(len(bodyText)),
	}

	enc := httpmsg.NewResponseEncoder(id, resp)
	for {
		m, err := enc.Next()
		if err == io.EOF {
			break
		}
		if err == nil {
			agentWriteCh <- m
		}
	}
}

// --- ACTUAL E2E TEST ---

func TestE2E_RoundTrip(t *testing.T) {
	// 1. Setup Server
	mux := http.NewServeMux()
	mux.HandleFunc("/ws", wsHandler)
	mux.HandleFunc("/", proxyHandler)
	testSrv := httptest.NewServer(mux)
	defer testSrv.Close()

	// 2. Setup Agent
	wsURL := "ws" + testSrv.URL[4:] + "/ws"
	
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	agentDone := make(chan struct{})
	go func() {
		_ = runAgent(ctx, wsURL)
		close(agentDone)
	}()

	// Wait for agent connection
	time.Sleep(200 * time.Millisecond)

	// 3. Make HTTP request through Proxy
	bodyData := bytes.Repeat([]byte("test_e2e_data_"), 1000) // ~14KB, large enough to test handling but fast
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, testSrv.URL+"/test-e2e-path", bytes.NewReader(bodyData))
	req.Header.Set("X-E2E-Custom", "Hello")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("HTTP request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("Expected status 200, got %d", resp.StatusCode)
	}

	respBody, _ := io.ReadAll(resp.Body)
	
	// Verify response body contains the echoed request from the Agent
	if !bytes.Contains(respBody, []byte("AGENT_ECHO")) {
		t.Errorf("Expected AGENT_ECHO in response, got %s", string(respBody))
	}
	if !bytes.Contains(respBody, []byte("/test-e2e-path")) {
		t.Errorf("Expected path /test-e2e-path in echoed response")
	}
	if !bytes.Contains(respBody, []byte("test_e2e_data_")) {
		t.Errorf("Expected body payload in echoed response")
	}

	// 4. Teardown
	cancel() // signal agent to stop
	<-agentDone
}
