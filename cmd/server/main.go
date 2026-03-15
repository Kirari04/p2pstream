package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"sync"
	"sync/atomic"

	"github.com/coder/websocket"
	"github.com/google/uuid"
	"p2pstream/httpmsg"
	"p2pstream/msg"
	"p2pstream/stats"
)

var (
	// activeAgent stores the single agent connection (for this iteration)
	activeAgent atomic.Pointer[agentConn]
	
	// pendingRequests maps a request ID to a channel where the response chunks will be sent
	pendingRequests sync.Map // map[uuid.UUID]chan *msg.Request
	
	// latestAgentStats stores the most recently reported stats
	latestAgentStats atomic.Pointer[stats.AgentStats]

	// targetOrigin is the upstream server we forward traffic to
	targetOrigin *url.URL
)

type agentConn struct {
	writeCh chan *msg.Request
}

func main() {
	originStr := os.Getenv("TARGET_ORIGIN")
	if originStr == "" {
		originStr = "https://httpbin.org" // Default for testing
	}
	
	var err error
	targetOrigin, err = url.Parse(originStr)
	if err != nil {
		log.Fatalf("Invalid TARGET_ORIGIN %q: %v", originStr, err)
	}

	http.HandleFunc("/ws", wsHandler)
	http.HandleFunc("/api/agent/stats", statsHandler)
	http.HandleFunc("/", proxyHandler)

	port := ":8080"
	log.Printf("Proxy server listening on http://localhost%s", port)
	log.Printf("Forwarding traffic to: %s", targetOrigin.String())
	log.Printf("Agent WebSocket endpoint at ws://localhost%s/ws", port)
	if err := http.ListenAndServe(port, nil); err != nil {
		log.Fatalf("Server failed: %v", err)
	}
}

// wsHandler handles the incoming WebSocket connection from the agent
func wsHandler(w http.ResponseWriter, r *http.Request) {
	c, err := websocket.Accept(w, r, nil)
	if err != nil {
		log.Printf("websocket accept error: %v", err)
		return
	}
	defer c.Close(websocket.StatusInternalError, "internal server error")

	// Increase read limit to comfortably fit our 64KB chunking max size
	c.SetReadLimit(128 * 1024)

	ctx := context.Background()

	// 100-chunk buffer allows asynchronous flow without immediately blocking
	agent := &agentConn{
		writeCh: make(chan *msg.Request, 100),
	}
	
	// Store the new agent. In a real system, you'd manage multiple agents here.
	activeAgent.Store(agent)
	log.Println("Agent connected successfully")

	// Writer goroutine: reads from writeCh and sends over WebSocket
	go func() {
		for req := range agent.writeCh {
			cw, err := c.Writer(ctx, websocket.MessageBinary)
			if err != nil {
				log.Printf("ws writer error: %v", err)
				return
			}
			if _, err := req.WriteTo(cw); err != nil {
				log.Printf("msg WriteTo error: %v", err)
				cw.Close()
				return
			}
			cw.Close()
		}
	}()

	// Reader loop: reads chunks from WebSocket and routes them to the correct HTTP handler
	for {
		_, cr, err := c.Reader(ctx)
		if err != nil {
			log.Printf("ws read loop ended: %v", err)
			break
		}

		msgBytes, err := io.ReadAll(cr)
		if err != nil {
			log.Printf("ws ReadAll error: %v", err)
			continue
		}

		m, err := msg.ParseRequest(bytes.NewReader(msgBytes))
		if err != nil {
			log.Printf("msg ParseRequest error: %v", err)
			continue
		}

		// Route the chunk to the HTTP request handler waiting for it
		if ch, ok := pendingRequests.Load(m.ID); ok {
			ch.(chan *msg.Request) <- m
		} else {
			log.Printf("Received chunk for unknown request ID: %s", m.ID)
		}
	}

	// Cleanup when agent disconnects
	activeAgent.CompareAndSwap(agent, nil)
	log.Println("Agent disconnected")
}

// proxyHandler handles incoming external HTTP requests, forwards them to the agent, and streams the response
func proxyHandler(w http.ResponseWriter, r *http.Request) {
	agent := activeAgent.Load()
	if agent == nil {
		http.Error(w, "No internal agent connected", http.StatusServiceUnavailable)
		return
	}

	id, err := uuid.NewV7()
	if err != nil {
		http.Error(w, "Failed to generate request ID", http.StatusInternalServerError)
		return
	}

	// Register channel to receive the response chunks from the agent
	respCh := make(chan *msg.Request, 100)
	pendingRequests.Store(id, respCh)
	defer pendingRequests.Delete(id)

	// Rewrite request to target origin
	r.URL.Scheme = targetOrigin.Scheme
	r.URL.Host = targetOrigin.Host
	r.Host = targetOrigin.Host

	// Encode the HTTP request into chunks and send to agent
	enc := httpmsg.NewRequestEncoder(id, r)
	for {
		m, err := enc.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			http.Error(w, "Error encoding request", http.StatusInternalServerError)
			return
		}
		agent.writeCh <- m
	}

	// Create a stream from the response channel
	stream := &httpmsg.ChannelStream{Ch: respCh}

	// The first message from the agent should be the Header
	firstMsg, err := stream.Next()
	if err != nil {
		http.Error(w, "Failed to receive response headers from agent", http.StatusBadGateway)
		return
	}

	// Decode the response
	resp, err := httpmsg.DecodeResponse(firstMsg, stream)
	if err != nil {
		http.Error(w, "Failed to decode agent response", http.StatusInternalServerError)
		return
	}

	// Copy headers to the client
	for k, vv := range resp.Header {
		for _, v := range vv {
			w.Header().Add(k, v)
		}
	}
	
	// Write status code
	w.WriteHeader(resp.StatusCode)

	// Stream the body dynamically to the client
	if resp.Body != nil {
		defer resp.Body.Close()
		io.Copy(w, resp.Body)
	}
}

func statsHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var s stats.AgentStats
	if err := json.NewDecoder(r.Body).Decode(&s); err != nil {
		http.Error(w, "Bad request", http.StatusBadRequest)
		return
	}

	// Store the latest globally
	latestAgentStats.Store(&s)

	// Log out a human readable summary
	log.Printf("Agent Health [Mem: %dMB | Goroutines: %d | Active Req: %d]", 
		s.AllocAllocated, s.NumGoroutine, s.ActiveRequests)
	
	totalReq := s.ReqSuccess + s.ReqClientError + s.ReqServerError + s.ReqInternalError
	if totalReq > 0 || s.BytesReceived > 0 || s.BytesSent > 0 {
		log.Printf("  Traffic (last 5s) -> Req: %d [2xx/3xx: %d, 4xx: %d, 5xx: %d, Err: %d] | RX: %.2fKB | TX: %.2fKB",
			totalReq, s.ReqSuccess, s.ReqClientError, s.ReqServerError, s.ReqInternalError,
			float64(s.BytesReceived)/1024.0, float64(s.BytesSent)/1024.0)
	}

	w.WriteHeader(http.StatusOK)
}
