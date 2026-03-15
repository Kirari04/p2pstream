package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"runtime"
	"sync"
	"sync/atomic"
	"time"

	"p2pstream/httpmsg"
	"p2pstream/msg"
	"p2pstream/stats"

	"github.com/coder/websocket"
	"github.com/google/uuid"
)

var (
	// incomingRequests maps a request ID to a channel where incoming request chunks will be streamed
	incomingRequests sync.Map // map[uuid.UUID]chan *msg.Request

	// writeCh queues messages to be sent back to the server
	writeCh chan *msg.Request

	// Metrics trackers
	activeRequests   atomic.Int32
	reqSuccess       atomic.Int32
	reqClientError   atomic.Int32
	reqServerError   atomic.Int32
	reqInternalError atomic.Int32
	bytesReceived    atomic.Uint64
	bytesSent        atomic.Uint64
)

func main() {
	serverURL := os.Getenv("SERVER_URL")
	if serverURL == "" {
		serverURL = "ws://localhost:8080/ws"
	}

	apiStatsURL := os.Getenv("SERVER_STATS_URL")
	if apiStatsURL == "" {
		apiStatsURL = "http://localhost:8080/api/agent/stats"
	}

	go startStatsReporter(apiStatsURL)

	// Simple reconnect loop
	for {
		log.Printf("Attempting to connect to server at %s...", serverURL)

		err := runAgent(serverURL)
		if err != nil {
			log.Printf("Disconnected: %v", err)
		}

		time.Sleep(2 * time.Second)
	}
}

func runAgent(serverURL string) error {
	ctx := context.Background()
	c, _, err := websocket.Dial(ctx, serverURL, nil)
	if err != nil {
		return fmt.Errorf("failed to dial: %w", err)
	}
	defer c.Close(websocket.StatusInternalError, "agent shutting down")

	// Increase read limit to comfortably fit our 64KB chunking max size
	c.SetReadLimit(128 * 1024)

	log.Println("Connected successfully!")

	writeCh = make(chan *msg.Request, 100)

	// Writer goroutine
	go func() {
		for req := range writeCh {
			cw, err := c.Writer(ctx, websocket.MessageBinary)
			if err != nil {
				log.Printf("ws write error: %v", err)
				return // End writer goroutine on error
			}
			n, err := req.WriteTo(cw)
			if err != nil {
				log.Printf("msg WriteTo error: %v", err)
				cw.Close()
				return
			}
			bytesSent.Add(uint64(n))
			cw.Close()
		}
	}()

	// Reader loop
	for {
		_, cr, err := c.Reader(ctx)
		if err != nil {
			return fmt.Errorf("ws read loop ended: %w", err)
		}

		msgBytes, err := io.ReadAll(cr)
		if err != nil {
			log.Printf("ws ReadAll error: %v", err)
			continue
		}
		bytesReceived.Add(uint64(len(msgBytes)))

		m, err := msg.ParseRequest(bytes.NewReader(msgBytes))
		if err != nil {
			log.Printf("msg ParseRequest error: %v", err)
			continue
		}

		if m.Type == msg.RequestTypeHeader || m.Type == msg.RequestTypeHeaderAndBody {
			// This is a brand new incoming HTTP request
			reqCh := make(chan *msg.Request, 100)
			incomingRequests.Store(m.ID, reqCh)
			reqCh <- m

			// Spin up a dedicated goroutine to process this HTTP request concurrently
			go handleRequest(m.ID, reqCh)
		} else {
			// This is a body chunk for an already executing HTTP request
			if ch, ok := incomingRequests.Load(m.ID); ok {
				ch.(chan *msg.Request) <- m
			} else {
				log.Printf("Received body chunk for unknown request ID: %s", m.ID)
			}
		}
	}
}

// handleRequest reconstructs the HTTP request, processes it, and streams the HTTP response back
func handleRequest(id uuid.UUID, reqCh chan *msg.Request) {
	activeRequests.Add(1)
	defer activeRequests.Add(-1)
	// Ensure we cleanup the channel mapping when done
	defer incomingRequests.Delete(id)

	stream := &httpmsg.ChannelStream{Ch: reqCh}
	firstMsg, err := stream.Next()
	if err != nil {
		log.Printf("[req %s] Failed to read first chunk: %v", id, err)
		reqInternalError.Add(1)
		return
	}

	req, err := httpmsg.DecodeRequest(firstMsg, stream)
	if err != nil {
		log.Printf("[req %s] Failed to decode HTTP request: %v", id, err)
		reqInternalError.Add(1)
		return
	}

	// Go's http.Client disallows RequestURI being set for outbound requests.
	req.RequestURI = ""

	log.Printf("[req %s] Forwarding: %s %s", id, req.Method, req.URL.String())

	// Execute the HTTP request to the target origin
	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		log.Printf("[req %s] Proxy request failed: %v", id, err)
		reqServerError.Add(1)

		// Return a 502 Bad Gateway to the server
		resp = &http.Response{
			StatusCode:    http.StatusBadGateway,
			Status:        http.StatusText(http.StatusBadGateway),
			Header:        make(http.Header),
			Body:          io.NopCloser(bytes.NewReader([]byte("Bad Gateway: " + err.Error()))),
			ContentLength: int64(len("Bad Gateway: " + err.Error())),
		}
	} else {
		// Track successful request status metrics
		if resp.StatusCode >= 200 && resp.StatusCode < 400 {
			reqSuccess.Add(1)
		} else if resp.StatusCode >= 400 && resp.StatusCode < 500 {
			reqClientError.Add(1)
		} else {
			reqServerError.Add(1)
		}
	}

	// Encode response into msg.Request chunks and queue them to be sent
	enc := httpmsg.NewResponseEncoder(id, resp)
	for {
		m, err := enc.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			log.Printf("[req %s] Failed to encode response: %v", id, err)
			return
		}
		writeCh <- m
	}

	log.Printf("[req %s] Finished successfully.", id)
}

func startStatsReporter(url string) {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	for range ticker.C {
		var mem runtime.MemStats
		runtime.ReadMemStats(&mem)

		s := stats.AgentStats{
			Timestamp:        time.Now(),
			NumGoroutine:     runtime.NumGoroutine(),
			AllocAllocated:   mem.Alloc / 1024 / 1024,
			ActiveRequests:   activeRequests.Load(),
			ReqSuccess:       reqSuccess.Swap(0),
			ReqClientError:   reqClientError.Swap(0),
			ReqServerError:   reqServerError.Swap(0),
			ReqInternalError: reqInternalError.Swap(0),
			BytesReceived:    bytesReceived.Swap(0),
			BytesSent:        bytesSent.Swap(0),
		}

		payload, err := json.Marshal(s)
		if err != nil {
			log.Printf("Failed to marshal stats: %v", err)
			continue
		}

		resp, err := http.Post(url, "application/json", bytes.NewReader(payload))
		if err != nil {
			log.Printf("Failed to report stats: %v", err)
			continue
		}
		resp.Body.Close()
	}
}
