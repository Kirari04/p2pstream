package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httputil"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/google/uuid"
	"p2pstream/httpmsg"
	"p2pstream/msg"
)

var (
	// incomingRequests maps a request ID to a channel where incoming request chunks will be streamed
	incomingRequests sync.Map // map[uuid.UUID]chan *msg.Request
	
	// writeCh queues messages to be sent back to the server
	writeCh chan *msg.Request
)

func main() {
	serverURL := "ws://localhost:8080/ws"
	
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
			if _, err := req.WriteTo(cw); err != nil {
				log.Printf("msg WriteTo error: %v", err)
				cw.Close()
				return
			}
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
	// Ensure we cleanup the channel mapping when done
	defer incomingRequests.Delete(id)
	
	stream := &httpmsg.ChannelStream{Ch: reqCh}
	firstMsg, err := stream.Next()
	if err != nil {
		log.Printf("[req %s] Failed to read first chunk: %v", id, err)
		return
	}

	req, err := httpmsg.DecodeRequest(firstMsg, stream)
	if err != nil {
		log.Printf("[req %s] Failed to decode HTTP request: %v", id, err)
		return
	}

	log.Printf("[req %s] Executing: %s %s", id, req.Method, req.URL.Path)

	// For the demo, we'll just echo the reconstructed request back as text
	dump, err := httputil.DumpRequest(req, true)
	if err != nil {
		log.Printf("[req %s] Failed to dump request: %v", id, err)
		dump = []byte(fmt.Sprintf("Failed to read body: %v", err))
	}

	bodyText := fmt.Sprintf("=== Hello from the Agent! ===\nYour request successfully round-tripped through the WebSocket!\n\n%s", string(dump))

	// Create an HTTP Response
	resp := &http.Response{
		StatusCode:    http.StatusOK,
		Status:        http.StatusText(http.StatusOK),
		Header:        make(http.Header),
		Body:          io.NopCloser(bytes.NewReader([]byte(bodyText))),
		ContentLength: int64(len(bodyText)),
	}
	resp.Header.Set("Content-Type", "text/plain")

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
