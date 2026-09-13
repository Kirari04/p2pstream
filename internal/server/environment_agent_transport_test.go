package server

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type trackedEnvironmentBody struct {
	io.Reader
	closed bool
}

func (b *trackedEnvironmentBody) Close() error { b.closed = true; return nil }

func TestEnvironmentAgentAdmissionHandoffPreservesUnconsumedMutation(t *testing.T) {
	app := NewApp(nil, nil)
	body := &trackedEnvironmentBody{Reader: strings.NewReader("mutation")}
	req := httptest.NewRequest(http.MethodPost, "https://management.test/mutate", body)
	pooled := retryRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		_ = req.Body.Close() // net/http closes the body even on dial failure.
		return nil, agentStreamCapacityDialError(req.Context(), agentStreamCapacityPublicPooled, errAgentStreamCapacityPooledBudget)
	})
	called := 0
	resp, err := roundTripEnvironmentAgent(req, app, &AgentConn{}, pooled, func() http.RoundTripper {
		called++
		return retryRoundTripFunc(func(req *http.Request) (*http.Response, error) {
			if body.closed || req.GetBody != nil {
				t.Fatal("mutation body was closed or made replayable")
			}
			payload, err := io.ReadAll(req.Body)
			if err != nil || string(payload) != "mutation" {
				t.Fatalf("handoff body = %q, %v", payload, err)
			}
			return &http.Response{StatusCode: 200, Body: http.NoBody}, nil
		})
	})
	if err != nil || called != 1 {
		t.Fatalf("admission handoff: calls=%d err=%v", called, err)
	}
	_ = resp.Body.Close()
	if !body.closed {
		t.Fatal("request body ownership was leaked")
	}
}

func TestEnvironmentAgentDoesNotReplayAfterConsumptionOrUpstreamFailure(t *testing.T) {
	for _, consumed := range []bool{false, true} {
		t.Run(map[bool]string{false: "upstream failure", true: "consumed body"}[consumed], func(t *testing.T) {
			body := &trackedEnvironmentBody{Reader: strings.NewReader("mutation")}
			req := httptest.NewRequest(http.MethodPost, "https://management.test/mutate", body)
			pooled := retryRoundTripFunc(func(req *http.Request) (*http.Response, error) {
				if consumed {
					var p [1]byte
					_, _ = req.Body.Read(p[:])
					return nil, agentStreamCapacityDialError(req.Context(), agentStreamCapacityPublicPooled, errAgentStreamCapacityPooledBudget)
				}
				return nil, errors.New("upstream disconnected")
			})
			_, err := roundTripEnvironmentAgent(req, NewApp(nil, nil), &AgentConn{}, pooled, func() http.RoundTripper {
				t.Fatal("mutation was replayed")
				return nil
			})
			if err == nil || !body.closed {
				t.Fatal("failed mutation must report its error and close the body")
			}
		})
	}
}
