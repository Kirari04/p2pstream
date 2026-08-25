package server

import (
	"bytes"
	"crypto/tls"
	"crypto/x509"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptrace"
	"strconv"
	"sync"
	"sync/atomic"

	"github.com/rs/zerolog/log"

	p2pstreamv1 "p2pstream/gen/proto/p2pstream/v1"
)

type retryReplayBudget struct {
	limit int64
	used  atomic.Int64
}

func newRetryReplayBudget(limit int64) *retryReplayBudget {
	if limit <= 0 {
		limit = defaultPublicRetryReplayBudgetBytes
	}
	return &retryReplayBudget{limit: limit}
}

func (b *retryReplayBudget) tryAcquire(size int64) (func(), bool) {
	if b == nil || size <= 0 {
		return func() {}, true
	}
	for {
		used := b.used.Load()
		if size > b.limit-used {
			return nil, false
		}
		if b.used.CompareAndSwap(used, used+size) {
			var released atomic.Bool
			return func() {
				if released.CompareAndSwap(false, true) {
					b.used.Add(-size)
				}
			}, true
		}
	}
}

type publicRetryAttemptResult struct {
	Rule                *publicRetryRuleConfig
	FinalAgent          *AgentConn
	FirstFailedAgent    *AgentConn
	RetryCount          int64
	Outcome             string
	FirstErrorKind      string
	LastErrorKind       string
	ReplaySkippedReason string
}

func (r *publicRetryAttemptResult) applyToResolution(resolution *publicRouteResolution) {
	if r == nil || resolution == nil {
		return
	}
	if r.FinalAgent != nil {
		resolution.Agent = r.FinalAgent
		resolution.AgentID = sql.NullInt64{Int64: r.FinalAgent.AgentID, Valid: true}
	}
	if r.Rule != nil {
		resolution.RetryRuleID = r.Rule.ID
		resolution.RetryRuleName = r.Rule.Name
	}
	resolution.RetryCount = r.RetryCount
	resolution.RetryOutcome = r.Outcome
}

type publicAgentAttemptRoundTripper struct {
	app               *App
	snapshot          *publicProxySnapshot
	resolution        publicRouteResolution
	initial           *AgentConn
	rule              *publicRetryRuleConfig
	trace             *trafficRequestTrace
	shaper            *publicTrafficShaperDecision
	requestID         string
	result            *publicRetryAttemptResult
	transportForAgent func(*AgentConn) http.RoundTripper
}

type publicRetryRequestBody struct {
	original   io.ReadCloser
	buffered   []byte
	replayable bool
	bodyless   bool
	release    func()
}

type nonClosingReadCloser struct {
	io.Reader
}

func (nonClosingReadCloser) Close() error { return nil }

type countedAttemptBody struct {
	io.ReadCloser
	read *atomic.Int64
}

func (b *countedAttemptBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	if b.read != nil && n > 0 {
		b.read.Add(int64(n))
	}
	return n, err
}

type joinedRequestBody struct {
	io.Reader
	closer io.Closer
}

func (b *joinedRequestBody) Close() error {
	if b == nil || b.closer == nil {
		return nil
	}
	return b.closer.Close()
}

// activeAgentResponseBody keeps least-connections accounting active while the
// response is being streamed, not only until its headers arrive.
type activeAgentResponseBody struct {
	io.ReadCloser
	releaseOnce sync.Once
	release     func()
}

type activeAgentReadWriteResponseBody struct {
	*activeAgentResponseBody
	writer io.Writer
}

func (b *activeAgentResponseBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	if err != nil {
		b.releaseActiveRequest()
	}
	return n, err
}

func (b *activeAgentResponseBody) Close() error {
	err := b.ReadCloser.Close()
	b.releaseActiveRequest()
	return err
}

func (b *activeAgentResponseBody) releaseActiveRequest() {
	if b == nil {
		return
	}
	b.releaseOnce.Do(func() {
		if b.release != nil {
			b.release()
		}
	})
}

func (b *activeAgentReadWriteResponseBody) Write(p []byte) (int, error) {
	n, err := b.writer.Write(p)
	if err != nil {
		b.releaseActiveRequest()
	}
	return n, err
}

func wrapActiveAgentResponseBody(body io.ReadCloser, release func()) io.ReadCloser {
	active := &activeAgentResponseBody{ReadCloser: body, release: release}
	if readWriteBody, ok := body.(io.ReadWriteCloser); ok {
		return &activeAgentReadWriteResponseBody{activeAgentResponseBody: active, writer: readWriteBody}
	}
	return active
}

func preparePublicRetryRequestBody(app *App, req *http.Request, rule *publicRetryRuleConfig) (*publicRetryRequestBody, error) {
	source := &publicRetryRequestBody{bodyless: true, replayable: true, release: func() {}}
	if req == nil || req.Body == nil || req.Body == http.NoBody {
		return source, nil
	}
	source.bodyless = false
	source.replayable = false
	source.original = req.Body
	if rule == nil || rule.BodyMode != publicRetryBodyModeBuffered || rule.MaxReplayBodyBytes <= 0 {
		return source, nil
	}
	if req.ContentLength > rule.MaxReplayBodyBytes {
		return source, nil
	}
	if app == nil || app.retryReplayBudget == nil {
		return source, nil
	}
	reservationBytes := rule.MaxReplayBodyBytes
	if req.ContentLength > 0 {
		reservationBytes = req.ContentLength
	}
	release, ok := app.retryReplayBudget.tryAcquire(reservationBytes)
	if !ok {
		return source, nil
	}
	source.release = release
	limit := rule.MaxReplayBodyBytes + 1
	prefix, err := io.ReadAll(io.LimitReader(req.Body, limit))
	if err != nil {
		release()
		source.release = func() {}
		return nil, err
	}
	if int64(len(prefix)) > rule.MaxReplayBodyBytes {
		source.original = &joinedRequestBody{
			Reader: io.MultiReader(bytes.NewReader(prefix), req.Body),
			closer: req.Body,
		}
		release()
		source.release = func() {}
		return source, nil
	}
	if err := req.Body.Close(); err != nil {
		release()
		source.release = func() {}
		return nil, err
	}
	source.original = nil
	source.buffered = prefix
	source.replayable = true
	return source, nil
}

func (b *publicRetryRequestBody) close() {
	if b == nil {
		return
	}
	if b.original != nil {
		_ = b.original.Close()
		b.original = nil
	}
	if b.release != nil {
		b.release()
		b.release = nil
	}
}

func (b *publicRetryRequestBody) next() (io.ReadCloser, bool) {
	if b == nil || b.bodyless {
		return http.NoBody, true
	}
	if b.replayable {
		return io.NopCloser(bytes.NewReader(b.buffered)), true
	}
	if b.original == nil {
		return nil, false
	}
	return nonClosingReadCloser{Reader: b.original}, true
}

func (rt *publicAgentAttemptRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	if rt == nil || rt.app == nil || rt.initial == nil {
		return nil, errAgentDisconnected
	}
	if rt.result == nil {
		rt.result = &publicRetryAttemptResult{}
	}
	rt.result.Rule = rt.rule
	body, err := preparePublicRetryRequestBody(rt.app, req, rt.rule)
	if err != nil {
		return nil, err
	}
	responseOwnsRequestBody := false
	defer func() {
		if !responseOwnsRequestBody {
			body.close()
		}
	}()
	finishResponse := func(resp *http.Response, releaseActiveRequest func(), attempt int64, outcome string) *http.Response {
		if resp.Body == nil {
			resp.Body = http.NoBody
		}
		resp.Body = wrapActiveAgentResponseBody(resp.Body, func() {
			releaseActiveRequest()
			body.close()
		})
		responseOwnsRequestBody = true
		rt.result.Outcome = outcome
		rt.result.RetryCount = attempt - 1
		return resp
	}

	maxAttempts := int64(1)
	if rt.rule != nil {
		maxAttempts += rt.rule.MaxRetries
	}
	attemptedAgents := make(map[int64]struct{}, maxAttempts)
	agent := rt.initial
	for attempt := int64(1); attempt <= maxAttempts; attempt++ {
		if agent == nil {
			break
		}
		attemptedAgents[agent.AgentID] = struct{}{}
		rt.result.FinalAgent = agent

		attemptBody, ok := body.next()
		if !ok {
			rt.result.Outcome = publicRetryOutcomeSkipped
			rt.result.ReplaySkippedReason = "request_body_not_replayable"
			break
		}
		var bodyRead atomic.Int64
		if rt.shaper != nil {
			shaper := *rt.shaper
			if attempt > 1 {
				shaper.Rule.RequestExemptBytes = 0
			}
			attemptBody = shaper.wrapUploadBody(req.Context(), attemptBody)
		}
		attemptBody = &countedAttemptBody{ReadCloser: attemptBody, read: &bodyRead}

		var wroteRequest atomic.Bool
		attemptCtx := httptrace.WithClientTrace(req.Context(), &httptrace.ClientTrace{
			WroteRequest: func(info httptrace.WroteRequestInfo) {
				if info.Err == nil {
					wroteRequest.Store(true)
				}
			},
		})
		attemptCtx = withAgentDialRequestID(attemptCtx, rt.requestID)
		attemptReq := req.Clone(attemptCtx)
		attemptReq.Body = attemptBody
		attemptReq.GetBody = nil
		attemptResolution := rt.resolution
		attemptResolution.Agent = agent
		attemptResolution.AgentID = sql.NullInt64{Int64: agent.AgentID, Valid: true}
		attemptResolution.RetryCount = attempt - 1
		if rt.rule != nil {
			attemptResolution.RetryRuleID = rt.rule.ID
			attemptResolution.RetryRuleName = rt.rule.Name
		}
		rt.emitAttemptStarted(attemptResolution, agent, attempt, maxAttempts)
		log.Info().
			Str("req_id", rt.requestID).
			Int64("attempt", attempt).
			Int64("max_attempts", maxAttempts).
			Str("method", req.Method).
			Str("path", req.URL.Path).
			Str("agent", agent.PublicID).
			Msg("Proxying request through agent target")

		agent.ActiveRequests.Add(1)
		releaseActiveRequest := func() { agent.ActiveRequests.Add(-1) }
		transport := rt.app.agentTargetTransport(agent, rt.resolution.Target)
		if rt.transportForAgent != nil {
			transport = rt.transportForAgent(agent)
		}
		resp, attemptErr := transport.RoundTrip(attemptReq)
		if attemptErr != nil {
			if resp != nil && resp.Body != nil {
				_ = resp.Body.Close()
			}
			releaseActiveRequest()
		} else if resp == nil {
			releaseActiveRequest()
			attemptErr = errors.New("agent transport returned no response")
		}
		if attemptErr == nil {
			if rt.rule == nil || !rt.rule.retriesStatus(resp.StatusCode) {
				outcome := ""
				if attempt > 1 {
					outcome = publicRetryOutcomeRecovered
				}
				return finishResponse(resp, releaseActiveRequest, attempt, outcome), nil
			}

			errorKind := fmt.Sprintf("upstream_status_%d", resp.StatusCode)
			rt.result.LastErrorKind = errorKind
			if rt.result.FirstErrorKind == "" {
				rt.result.FirstErrorKind = errorKind
				rt.result.FirstFailedAgent = agent
			}
			if attempt >= maxAttempts {
				return finishResponse(resp, releaseActiveRequest, attempt, publicRetryOutcomeExhausted), nil
			}
			if !body.replayable && !body.bodyless && bodyRead.Load() > 0 {
				rt.result.ReplaySkippedReason = "request_body_not_replayable"
				return finishResponse(resp, releaseActiveRequest, attempt, publicRetryOutcomeSkipped), nil
			}
			next := rt.app.selectTargetAgentExcludingFromSnapshot(rt.snapshot, rt.resolution.Target, attemptedAgents)
			if next == nil {
				return finishResponse(resp, releaseActiveRequest, attempt, publicRetryOutcomeExhausted), nil
			}
			if resp.Body != nil {
				_ = resp.Body.Close()
			}
			releaseActiveRequest()
			rt.result.RetryCount = attempt
			rt.emitRetry(attemptResolution, agent, next, attempt, errorKind)
			agent = next
			continue
		}

		errorKind := agentProxyErrorKind(attemptErr)
		rt.result.LastErrorKind = errorKind
		if rt.result.FirstErrorKind == "" {
			rt.result.FirstErrorKind = errorKind
			rt.result.FirstFailedAgent = agent
		}
		if shouldMarkAgentPassiveFailure(req.Context(), attemptErr) {
			rt.app.markPublicRouteTargetAgentPassiveFailure(rt.resolution.Target.ID, agent.AgentID, attemptErr)
		}
		if attempt >= maxAttempts || rt.rule == nil {
			if attempt > 1 {
				rt.result.Outcome = publicRetryOutcomeExhausted
			}
			rt.result.RetryCount = attempt - 1
			return nil, attemptErr
		}
		if !publicRetryAttemptErrorAllowed(req, attemptErr, rt.rule, wroteRequest.Load(), bodyRead.Load(), body.replayable || body.bodyless) {
			rt.result.Outcome = publicRetryOutcomeSkipped
			rt.result.ReplaySkippedReason = publicRetrySkipReason(req, attemptErr, wroteRequest.Load(), bodyRead.Load(), body.replayable || body.bodyless)
			rt.result.RetryCount = attempt - 1
			return nil, attemptErr
		}
		next := rt.app.selectTargetAgentExcludingFromSnapshot(rt.snapshot, rt.resolution.Target, attemptedAgents)
		if next == nil {
			rt.result.Outcome = publicRetryOutcomeExhausted
			rt.result.RetryCount = attempt - 1
			return nil, attemptErr
		}
		rt.result.RetryCount = attempt
		rt.emitRetry(attemptResolution, agent, next, attempt, errorKind)
		agent = next
	}
	return nil, fmt.Errorf("no untried agent is available")
}

func (rt *publicAgentAttemptRoundTripper) emitAttemptStarted(resolution publicRouteResolution, agent *AgentConn, attempt, maxAttempts int64) {
	if rt.trace == nil {
		return
	}
	attributes := map[string]string{
		"load_balancer": rt.resolution.Target.AgentLoadBalancing,
		"attempt":       strconv.FormatInt(attempt, 10),
		"max_attempts":  strconv.FormatInt(maxAttempts, 10),
	}
	if rt.rule != nil {
		attributes["retry_rule"] = rt.rule.Name
	}
	rt.trace.emit(p2pstreamv1.TrafficTraceStage_TRAFFIC_TRACE_STAGE_AGENT_SELECTED, &resolution, agent, 0, "", nil, attributes)
	attributes = map[string]string{
		"handler":      "agent_target",
		"upstream":     redactSensitiveTraceURL(rt.resolution.Target.ParsedURL.String()),
		"agent":        agent.PublicID,
		"attempt":      strconv.FormatInt(attempt, 10),
		"max_attempts": strconv.FormatInt(maxAttempts, 10),
	}
	rt.trace.emit(p2pstreamv1.TrafficTraceStage_TRAFFIC_TRACE_STAGE_UPSTREAM_STARTED, &resolution, agent, 0, "", nil, attributes)
}

func (rt *publicAgentAttemptRoundTripper) emitRetry(resolution publicRouteResolution, failed, next *AgentConn, attempt int64, errorKind string) {
	if rt.trace == nil {
		return
	}
	resolution.RetryCount = attempt
	resolution.RetryOutcome = "retrying"
	attributes := map[string]string{
		"attempt":           strconv.FormatInt(attempt, 10),
		"failed_agent":      failed.PublicID,
		"replacement_agent": next.PublicID,
	}
	rt.trace.emit(p2pstreamv1.TrafficTraceStage_TRAFFIC_TRACE_STAGE_UPSTREAM_RETRY, &resolution, failed, 0, errorKind, nil, attributes)
}

func publicRetryAttemptErrorAllowed(req *http.Request, err error, rule *publicRetryRuleConfig, wroteRequest bool, bodyBytesRead int64, replayable bool) bool {
	if rule == nil || err == nil || requestContextCanceled(req.Context(), err) {
		return false
	}
	if _, _, _, bodyErr := publicRequestBodyErrorForRequest(req, err); bodyErr {
		return false
	}
	if !retryableAgentTransportError(err) {
		return false
	}
	if rule.FailureMode == publicRetryFailureModeConnectionFailures {
		return !wroteRequest && bodyBytesRead == 0
	}
	return replayable || bodyBytesRead == 0
}

func retryableAgentTransportError(err error) bool {
	if err == nil {
		return false
	}
	var dialErr agentDialError
	if errors.As(err, &dialErr) {
		switch dialErr.Kind {
		case "dial_failed", "dial_timeout", "agent_capacity":
			return true
		default:
			return false
		}
	}
	var unknownAuthority x509.UnknownAuthorityError
	var hostnameError x509.HostnameError
	var certificateInvalid x509.CertificateInvalidError
	var recordHeader tls.RecordHeaderError
	if errors.As(err, &unknownAuthority) || errors.As(err, &hostnameError) || errors.As(err, &certificateInvalid) || errors.As(err, &recordHeader) {
		return false
	}
	return true
}

func publicRetrySkipReason(req *http.Request, err error, wroteRequest bool, bodyBytesRead int64, replayable bool) string {
	if req != nil && requestContextCanceled(req.Context(), err) {
		return "client_cancelled"
	}
	if req != nil {
		if _, _, _, bodyErr := publicRequestBodyErrorForRequest(req, err); bodyErr {
			return "request_body_failed"
		}
	}
	if !retryableAgentTransportError(err) {
		return "failure_not_retryable"
	}
	if wroteRequest {
		return "request_already_written"
	}
	if bodyBytesRead > 0 && !replayable {
		return "request_body_not_replayable"
	}
	return "policy_not_satisfied"
}

func agentProxyErrorKind(err error) string {
	if err == nil {
		return ""
	}
	if errors.Is(err, errAgentDisconnected) {
		return "agent_disconnected"
	}
	var dialErr agentDialError
	if errors.As(err, &dialErr) {
		switch dialErr.Kind {
		case "":
			return "agent_dial_failed"
		case "dial_timeout":
			return "agent_dial_timeout"
		case "agent_capacity":
			return "agent_capacity"
		case "server_capacity":
			return "agent_server_capacity"
		default:
			return "agent_" + dialErr.Kind
		}
	}
	if isTimeoutError(err) {
		return "upstream_response_header_timeout"
	}
	return "agent_proxy_failed"
}

func agentProxyHTTPFailure(err error) (int, string, string) {
	kind := agentProxyErrorKind(err)
	switch kind {
	case "agent_dial_timeout", "upstream_response_header_timeout":
		return http.StatusGatewayTimeout, kind, "Gateway Timeout"
	case "agent_capacity", "agent_server_capacity":
		return http.StatusServiceUnavailable, kind, "Service Unavailable"
	default:
		return http.StatusBadGateway, kind, "Bad Gateway"
	}
}

func publicRetryRuleID(result *publicRetryAttemptResult) sql.NullInt64 {
	if result == nil || result.Rule == nil {
		return sql.NullInt64{}
	}
	return sql.NullInt64{Int64: result.Rule.ID, Valid: true}
}

var _ http.RoundTripper = (*publicAgentAttemptRoundTripper)(nil)
