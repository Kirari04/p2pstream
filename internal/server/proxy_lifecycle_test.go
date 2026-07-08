package server

import (
	"context"
	"net"
	"net/http"
	"testing"
	"time"
)

func TestShutdownPublicHTTPServerForcesCloseAfterTimeout(t *testing.T) {
	requestStarted := make(chan struct{})
	handlerDone := make(chan struct{})
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(requestStarted)
		_, _ = w.Write([]byte("started"))
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
		<-r.Context().Done()
		close(handlerDone)
	})}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	serveErr := make(chan error, 1)
	go func() {
		err := server.Serve(listener)
		if err == http.ErrServerClosed {
			err = nil
		}
		serveErr <- err
	}()

	resp, err := http.Get("http://" + listener.Addr().String())
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	select {
	case <-requestStarted:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for active request")
	}

	started := time.Now()
	if err := shutdownPublicHTTPServer(context.Background(), server, 25*time.Millisecond); err != nil {
		t.Fatalf("shutdownPublicHTTPServer() error = %v", err)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("shutdown took %s, want forced close within one second", elapsed)
	}
	select {
	case <-handlerDone:
	case <-time.After(time.Second):
		t.Fatal("handler was not closed after shutdown timeout")
	}
	select {
	case err := <-serveErr:
		if err != nil {
			t.Fatalf("Serve() error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for Serve to exit")
	}
}
