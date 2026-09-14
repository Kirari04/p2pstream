package server

import (
	"errors"
	"net/http"
	"net/http/httptrace"
	"sync/atomic"
)

// Only a local admission failure before any request bytes were sent may hand
// off to the reserved one-shot capacity. A management mutation is never
// replayed after an upstream failure, even if its body happens to be rewindable.
func roundTripEnvironmentAgent(req *http.Request, app *App, agent *AgentConn, pooled http.RoundTripper, oneShot func() http.RoundTripper) (*http.Response, error) {
	body, err := preparePublicRetryRequestBody(app, req, nil)
	if err != nil {
		return nil, err
	}
	responseOwnsBody := false
	defer func() {
		if !responseOwnsBody {
			body.close()
		}
	}()
	var read atomic.Int64
	var wrote atomic.Bool
	ctx := httptrace.WithClientTrace(req.Context(), &httptrace.ClientTrace{
		WroteHeaders: func() { wrote.Store(true) },
	})
	makeRequest := func() *http.Request {
		clone := req.Clone(ctx)
		clone.GetBody = nil
		next, _ := body.next()
		clone.Body = &countedAttemptBody{ReadCloser: next, read: &read}
		return clone
	}
	resp, err := pooled.RoundTrip(makeRequest())
	if agentDialErrorHasKind(err, "server_pooled_capacity") && agentStreamCapacityAllowsPooledHandoff(err) &&
		ctx.Err() == nil && !wrote.Load() && read.Load() == 0 {
		if agentStreamCapacityRequiresIdleReclaim(err) {
			app.reclaimIdleAgentTransportFor(agent, agentStreamCapacityAllowsCrossSessionReclaim(err))
		}
		resp, err = oneShot().RoundTrip(makeRequest())
		app.AgentTransports.recordFallbackResult(err == nil && resp != nil)
	}
	if err != nil {
		return resp, err
	}
	if resp == nil {
		return nil, errors.New("agent transport returned no response")
	}
	if resp.Body == nil {
		resp.Body = http.NoBody
	}
	resp.Body = wrapActiveAgentResponseBody(resp.Body, body.close)
	responseOwnsBody = true
	return resp, nil
}
