package server

import (
	"bytes"
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

// Record the fixture owner's calls without ever repeating Close on the real H2
// body: Close-after-first has undefined behavior under the io.Closer contract.
type saturationLifecycleRecordingBody struct {
	io.ReadCloser
	closes atomic.Int32
}

func (b *saturationLifecycleRecordingBody) Close() error {
	if calls := b.closes.Add(1); calls != 1 {
		return fmt.Errorf("underlying body close called %d times", calls)
	}
	return b.ReadCloser.Close()
}

func TestSaturationResponseBodyOwnershipLifecycle(t *testing.T) {
	for _, name := range []string{"complete", "partial-cancel", "failure-cleanup", "copied-owner"} {
		t.Run(name, func(t *testing.T) {
			payload := bytes.Repeat([]byte{0x5a}, 128<<10)
			release, stopped := make(chan struct{}), make(chan struct{})
			var calls atomic.Int32
			var canceled atomic.Bool
			origin := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				defer close(stopped)
				w.Header().Set("Content-Length", fmt.Sprint(len(payload)+1))
				_, _ = w.Write(payload)
				w.(http.Flusher).Flush()
				select {
				case <-r.Context().Done():
					canceled.Store(true)
				case <-release:
					_, _ = io.WriteString(w, "y")
				}
			}))
			origin.EnableHTTP2 = true
			origin.StartTLS()
			defer origin.Close()
			transport := &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, ForceAttemptHTTP2: true} // fixture certificate
			defer transport.CloseIdleConnections()
			client := &http.Client{Transport: transport}
			ctx, cancel := context.WithTimeout(t.Context(), mixedSaturationWait)
			defer cancel()
			req, err := http.NewRequestWithContext(ctx, http.MethodGet, origin.URL, nil)
			if err != nil {
				t.Fatal(err)
			}
			response, err := client.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			recorded := &saturationLifecycleRecordingBody{ReadCloser: response.Body}
			response.Body = recorded
			owner := sustainedBody{response: response, cancel: cancel, close: ownedSaturationBodyClose(recorded)}
			consumed := 0
			func() {
				// Match fixture failure cleanup, including a by-value body owner.
				defer owner.cancel()
				defer owner.close()
				if response.ProtoMajor != 2 || response.StatusCode != 200 || response.ContentLength != int64(len(payload)+1) {
					t.Fatalf("response=%s/%d/%d", response.Proto, response.StatusCode, response.ContentLength)
				}
				if name == "failure-cleanup" {
					return // Failure before the explicit close path still owns cleanup.
				}
				if name == "complete" {
					close(release)
					body, err := io.ReadAll(io.LimitReader(response.Body, int64(len(payload)+2)))
					if err != nil || !bytes.Equal(body, append(append([]byte(nil), payload...), 'y')) {
						t.Fatalf("completed body bytes=%d err=%v", len(body), err)
					}
					consumed = len(body)
					owner.close()
					return
				}
				prefix := make([]byte, mixedSaturationPrefix)
				if _, err := io.ReadFull(response.Body, prefix); err != nil || !bytes.Equal(prefix, payload[:len(prefix)]) {
					t.Fatalf("partial prefix err=%v", err)
				}
				consumed = len(prefix)
				if name == "copied-owner" {
					copyOfOwner := owner
					copyOfOwner.cancel()
					copyOfOwner.close()
					return
				}
				owner.cancel()
				owner.close()
			}()
			mixedSaturationAwaitChannel(t, "lifecycle origin joined", stopped)
			if recorded.closes.Load() != 1 || calls.Load() != 1 || canceled.Load() != (name != "complete") {
				t.Fatalf("close/origin calls=%d/%d origin_canceled=%v", recorded.closes.Load(), calls.Load(), canceled.Load())
			}
			t.Logf("saturation_body_lifecycle case=%s consumed=%d underlying_closes=1 origin_joined=true copied_owner=%t", name, consumed, name == "copied-owner")
		})
	}
}
