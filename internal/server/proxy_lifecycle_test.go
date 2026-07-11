package server

import (
	"context"
	"net"
	"net/http"
	"testing"
	"time"
)

func TestShutdownPublicHTTPServerForcesCloseAfterTimeout(t *testing.T) {
	server, handlerDone, serveErr := startBlockingPublicHTTPServer(t)

	started := time.Now()
	if err := shutdownPublicHTTPServer(context.Background(), server, 25*time.Millisecond); err != nil {
		t.Fatalf("shutdownPublicHTTPServer() error = %v", err)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("shutdown took %s, want forced close within one second", elapsed)
	}
	assertBlockingPublicHTTPServerClosed(t, handlerDone, serveErr)
}

func TestShutdownPublicHTTPServerForcesCloseWhenParentCanceled(t *testing.T) {
	server, handlerDone, serveErr := startBlockingPublicHTTPServer(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if err := shutdownPublicHTTPServer(ctx, server, time.Hour); err != nil {
		t.Fatalf("shutdownPublicHTTPServer() error = %v", err)
	}
	assertBlockingPublicHTTPServerClosed(t, handlerDone, serveErr)
}

func TestPublicListenerShutdownContextUsesEarlierDeadline(t *testing.T) {
	t.Run("configured timeout is earlier", func(t *testing.T) {
		parent, cancelParent := context.WithTimeout(context.Background(), time.Hour)
		defer cancelParent()
		started := time.Now()
		ctx, cancel := publicListenerShutdownContext(parent, 50*time.Millisecond)
		defer cancel()
		deadline, ok := ctx.Deadline()
		if !ok {
			t.Fatal("shutdown context has no deadline")
		}
		if remaining := deadline.Sub(started); remaining < 25*time.Millisecond || remaining > 250*time.Millisecond {
			t.Fatalf("configured shutdown deadline remaining = %s, want about 50ms", remaining)
		}
	})

	t.Run("parent deadline is earlier", func(t *testing.T) {
		parentDeadline := time.Now().Add(50 * time.Millisecond)
		parent, cancelParent := context.WithDeadline(context.Background(), parentDeadline)
		defer cancelParent()
		ctx, cancel := publicListenerShutdownContext(parent, time.Hour)
		defer cancel()
		deadline, ok := ctx.Deadline()
		if !ok {
			t.Fatal("shutdown context has no deadline")
		}
		if !deadline.Equal(parentDeadline) {
			t.Fatalf("shutdown deadline = %s, want parent deadline %s", deadline, parentDeadline)
		}
	})
}

func startBlockingPublicHTTPServer(t *testing.T) (*http.Server, <-chan struct{}, <-chan error) {
	t.Helper()
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
	t.Cleanup(func() { _ = listener.Close() })
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
	t.Cleanup(func() { _ = resp.Body.Close() })
	select {
	case <-requestStarted:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for active request")
	}
	return server, handlerDone, serveErr
}

func assertBlockingPublicHTTPServerClosed(t *testing.T, handlerDone <-chan struct{}, serveErr <-chan error) {
	t.Helper()
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
