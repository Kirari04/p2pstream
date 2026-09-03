package server

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"math"
	"mime"
	"net/http"
	"net/http/httptrace"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

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

func (a *App) tryAcquireRetryReplayBudget(size int64) (func(), bool) {
	if a == nil || a.retryReplayBudget == nil {
		return nil, false
	}
	budgetRelease, ok := a.retryReplayBudget.tryAcquire(size)
	if !ok {
		return nil, false
	}
	// io.ReadAll's growing chunks and final exact copy can coexist briefly.
	// Charge three bytes of the shared adaptive resource ledger for every
	// buffered payload byte so the peak allocation cannot spend stream
	// headroom that has already been granted elsewhere.
	if size > math.MaxInt64/3 {
		budgetRelease()
		return nil, false
	}
	resourceRelease, resourceOK, constrained := a.agentStreamCapacity.tryReserveAdaptiveExternal(size*3, 0)
	if constrained && !resourceOK {
		budgetRelease()
		return nil, false
	}
	if resourceRelease == nil {
		resourceRelease = func() {}
	}
	var once sync.Once
	return func() {
		once.Do(func() {
			resourceRelease()
			budgetRelease()
		})
	}, true
}

type publicRetryAttemptResult struct {
	Rule                *publicRetryRuleConfig
	FinalAgent          *AgentConn
	FirstFailedAgent    *AgentConn
	RetryCount          int64
	Outcome             string
	FirstErrorKind      string
	LastErrorKind       string
	TerminalErrorKind   string
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

type publicRetryBufferedResponseBody struct {
	reader      *bytes.Reader
	releaseOnce sync.Once
	release     func()
}

func (b *publicRetryBufferedResponseBody) Read(p []byte) (int, error) {
	if b == nil || b.reader == nil {
		return 0, io.EOF
	}
	return b.reader.Read(p)
}

func (b *publicRetryBufferedResponseBody) Close() error {
	if b == nil {
		return nil
	}
	b.releaseOnce.Do(func() {
		if b.release != nil {
			b.release()
		}
	})
	return nil
}

type publicRetryPrefixedResponseBody struct {
	reader    io.Reader
	source    io.ReadCloser
	closeOnce sync.Once
	release   func()
	closeErr  error
}

type publicRetryResponseErrorReader struct {
	err error
}

func (r publicRetryResponseErrorReader) Read([]byte) (int, error) {
	return 0, r.err
}

func (b *publicRetryPrefixedResponseBody) Read(p []byte) (int, error) {
	if b == nil || b.reader == nil {
		return 0, io.EOF
	}
	return b.reader.Read(p)
}

func (b *publicRetryPrefixedResponseBody) Close() error {
	if b == nil {
		return nil
	}
	b.closeOnce.Do(func() {
		if b.source != nil {
			b.closeErr = b.source.Close()
		}
		if b.release != nil {
			b.release()
		}
	})
	return b.closeErr
}

type publicRetryObservedResponseBody struct {
	io.ReadCloser
	once    sync.Once
	onError func(error)
}

func (b *publicRetryObservedResponseBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	if err != nil && err != io.EOF && !errors.Is(err, context.Canceled) {
		b.once.Do(func() {
			if b.onError != nil {
				b.onError(err)
			}
		})
	}
	return n, err
}

type publicRetryPreparedResponseBody struct {
	body       io.ReadCloser
	complete   bool
	skipReason string
}

const publicRetryResponsePumpChunkBytes = 32 << 10

type publicRetryResponseChunk struct {
	data []byte
	err  error
}

// publicRetryResponsePump keeps a single reader on the upstream body while
// allowing the pre-commit buffering window to expire. Once buffering falls
// back, the same pump becomes the downstream response body, so no bytes are
// lost and the upstream request is not restarted merely because it is slow.
type publicRetryResponsePump struct {
	source     io.ReadCloser
	chunks     chan publicRetryResponseChunk
	done       chan struct{}
	closeOnce  sync.Once
	closeErr   error
	current    []byte
	currentErr error
	finished   bool
}

func newPublicRetryResponsePump(source io.ReadCloser) *publicRetryResponsePump {
	pump := &publicRetryResponsePump{
		source: source,
		chunks: make(chan publicRetryResponseChunk, 1),
		done:   make(chan struct{}),
	}
	go pump.run()
	return pump
}

func (p *publicRetryResponsePump) run() {
	emptyReads := 0
	for {
		buffer := make([]byte, publicRetryResponsePumpChunkBytes)
		n, err := p.source.Read(buffer)
		if n < 0 || n > len(buffer) {
			n = 0
			err = errors.New("upstream response body returned an invalid read count")
		}
		if n == 0 && err == nil {
			emptyReads++
			if emptyReads >= 100 {
				err = io.ErrNoProgress
			}
		} else {
			emptyReads = 0
		}
		if n > 0 || err != nil {
			chunk := publicRetryResponseChunk{err: err}
			if n > 0 {
				chunk.data = append([]byte(nil), buffer[:n]...)
			}
			select {
			case p.chunks <- chunk:
			case <-p.done:
				return
			}
		}
		if err != nil {
			return
		}
	}
}

func (p *publicRetryResponsePump) Read(dst []byte) (int, error) {
	if p == nil || len(dst) == 0 {
		return 0, nil
	}
	for {
		if len(p.current) > 0 {
			n := copy(dst, p.current)
			p.current = p.current[n:]
			if len(p.current) == 0 && p.currentErr != nil {
				err := p.currentErr
				p.currentErr = nil
				p.finished = true
				return n, err
			}
			return n, nil
		}
		if p.currentErr != nil {
			err := p.currentErr
			p.currentErr = nil
			p.finished = true
			return 0, err
		}
		if p.finished {
			return 0, io.EOF
		}
		select {
		case chunk := <-p.chunks:
			p.current = chunk.data
			p.currentErr = chunk.err
		case <-p.done:
			p.finished = true
			return 0, io.ErrClosedPipe
		}
	}
}

func (p *publicRetryResponsePump) Close() error {
	if p == nil {
		return nil
	}
	p.closeOnce.Do(func() {
		close(p.done)
		if p.source != nil {
			p.closeErr = p.source.Close()
		}
	})
	return p.closeErr
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
	release, ok := app.tryAcquireRetryReplayBudget(reservationBytes)
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

func preparePublicRetryResponseBody(app *App, req *http.Request, resp *http.Response, rule *publicRetryRuleConfig) (publicRetryPreparedResponseBody, error) {
	if resp == nil || resp.Body == nil || resp.Body == http.NoBody {
		return publicRetryPreparedResponseBody{body: http.NoBody, skipReason: "response_body_absent"}, nil
	}
	reason := publicRetryResponseBufferExclusion(req, resp, rule)
	if reason != "" {
		return publicRetryPreparedResponseBody{body: resp.Body, skipReason: reason}, nil
	}
	limit := rule.MaxBufferedResponseBodyBytes
	if resp.ContentLength > limit {
		return publicRetryPreparedResponseBody{body: resp.Body, skipReason: "response_body_too_large"}, nil
	}
	if app == nil || app.retryReplayBudget == nil {
		return publicRetryPreparedResponseBody{body: resp.Body, skipReason: "response_buffer_unavailable"}, nil
	}
	reservationBytes := limit
	if resp.ContentLength >= 0 {
		reservationBytes = resp.ContentLength
	}
	release, ok := app.tryAcquireRetryReplayBudget(reservationBytes)
	if !ok {
		return publicRetryPreparedResponseBody{body: resp.Body, skipReason: "response_buffer_budget_exhausted"}, nil
	}
	wait := rule.MaxBufferedResponseWait
	if wait <= 0 {
		wait = time.Duration(defaultPublicRetryResponseWaitMillis) * time.Millisecond
	}
	prefix, complete, tail, pump, skipReason, err := readPublicRetryResponseBodyBeforeDeadline(req.Context(), resp.Body, resp.ContentLength, limit, wait)
	if err != nil {
		release()
		return publicRetryPreparedResponseBody{}, err
	}
	if !complete {
		readers := []io.Reader{bytes.NewReader(prefix), tail}
		return publicRetryPreparedResponseBody{
			body: &publicRetryPrefixedResponseBody{
				reader:  io.MultiReader(readers...),
				source:  pump,
				release: release,
			},
			skipReason: skipReason,
		}, nil
	}
	return publicRetryPreparedResponseBody{
		body: &publicRetryBufferedResponseBody{
			reader:  bytes.NewReader(prefix),
			release: release,
		},
		complete: true,
	}, nil
}

func readPublicRetryResponseBodyBeforeDeadline(
	ctx context.Context,
	body io.ReadCloser,
	contentLength int64,
	limit int64,
	wait time.Duration,
) (prefix []byte, complete bool, tail io.Reader, pump *publicRetryResponsePump, skipReason string, err error) {
	if body == nil || body == http.NoBody || limit <= 0 {
		if body != nil {
			_ = body.Close()
		}
		return nil, true, nil, nil, "", nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if wait <= 0 {
		wait = time.Duration(defaultPublicRetryResponseWaitMillis) * time.Millisecond
	}
	capacity := limit
	if contentLength >= 0 && contentLength < capacity {
		capacity = contentLength
	}
	prefix = make([]byte, 0, int(capacity))
	pump = newPublicRetryResponsePump(body)
	timer := time.NewTimer(wait)
	defer timer.Stop()

	for {
		var chunk publicRetryResponseChunk
		select {
		case <-ctx.Done():
			_ = pump.Close()
			return nil, false, nil, nil, "", ctx.Err()
		case <-timer.C:
			// If completion raced the deadline, prefer the already available
			// terminal chunk over an unnecessary streaming fallback.
			select {
			case chunk = <-pump.chunks:
			default:
				return prefix, false, pump, pump, "response_buffer_wait_exceeded", nil
			}
		case chunk = <-pump.chunks:
		}
		if len(chunk.data) > 0 {
			if contentLength >= 0 && int64(len(prefix)+len(chunk.data)) > contentLength {
				_ = pump.Close()
				return nil, false, nil, nil, "", errors.New("upstream response body exceeded Content-Length")
			}
			remaining := limit - int64(len(prefix))
			if int64(len(chunk.data)) > remaining {
				prefix = append(prefix, chunk.data[:int(remaining)]...)
				readers := []io.Reader{bytes.NewReader(chunk.data[int(remaining):])}
				switch {
				case chunk.err == nil:
					readers = append(readers, pump)
				case !errors.Is(chunk.err, io.EOF) || errors.Is(chunk.err, errAgentDisconnected):
					readers = append(readers, publicRetryResponseErrorReader{err: chunk.err})
				}
				return prefix, false, io.MultiReader(readers...), pump, "response_body_too_large", nil
			}
			prefix = append(prefix, chunk.data...)
		}
		if chunk.err == nil {
			continue
		}
		if errors.Is(chunk.err, io.EOF) && !errors.Is(chunk.err, errAgentDisconnected) {
			if contentLength >= 0 && int64(len(prefix)) != contentLength {
				_ = pump.Close()
				return nil, false, nil, nil, "", io.ErrUnexpectedEOF
			}
			_ = pump.Close()
			return prefix, true, nil, nil, "", nil
		}
		_ = pump.Close()
		return nil, false, nil, nil, "", chunk.err
	}
}

// readPublicRetryResponseBody bounds allocations by the acquired replay
// reservation. Known-length responses are read to their HTTP framing length;
// unknown-length responses use a one-byte probe to distinguish a complete
// body at the configured limit from a body that must fall back to streaming.
func readPublicRetryResponseBody(body io.Reader, contentLength, limit int64) (prefix []byte, complete bool, overflow []byte, overflowErr error, err error) {
	if body == nil || limit <= 0 {
		return nil, true, nil, nil, nil
	}
	if contentLength >= 0 {
		prefix = make([]byte, int(contentLength))
		if _, err := io.ReadFull(body, prefix); err != nil {
			if errors.Is(err, io.EOF) {
				err = io.ErrUnexpectedEOF
			}
			return nil, false, nil, nil, err
		}
		return prefix, true, nil, nil, nil
	}

	prefix = make([]byte, int(limit))
	read := 0
	emptyReads := 0
	for read < len(prefix) {
		n, readErr := body.Read(prefix[read:])
		if n < 0 || n > len(prefix)-read {
			return nil, false, nil, nil, errors.New("upstream response body returned an invalid read count")
		}
		read += n
		if readErr == io.EOF {
			return prefix[:read], true, nil, nil, nil
		}
		if readErr != nil {
			return nil, false, nil, nil, readErr
		}
		if n == 0 {
			emptyReads++
			if emptyReads >= 100 {
				return nil, false, nil, nil, io.ErrNoProgress
			}
		} else {
			emptyReads = 0
		}
	}

	var probe [1]byte
	for emptyReads = 0; ; emptyReads++ {
		n, readErr := body.Read(probe[:])
		if n < 0 || n > len(probe) {
			return nil, false, nil, nil, errors.New("upstream response body returned an invalid read count")
		}
		if n > 0 {
			return prefix, false, append([]byte(nil), probe[:n]...), readErr, nil
		}
		if readErr == io.EOF {
			return prefix, true, nil, nil, nil
		}
		if readErr != nil {
			return nil, false, nil, nil, readErr
		}
		if emptyReads >= 99 {
			return nil, false, nil, nil, io.ErrNoProgress
		}
	}
}

func publicRetryResponseBufferExclusion(req *http.Request, resp *http.Response, rule *publicRetryRuleConfig) string {
	if rule == nil || normalizePublicRetryResponseBodyMode(rule.ResponseBodyMode) != publicRetryResponseBodyModeBuffered || rule.MaxBufferedResponseBodyBytes <= 0 {
		return "response_body_streamed"
	}
	if req == nil || req.Method == http.MethodHead || resp == nil || !responseStatusAllowsBody(resp.StatusCode) || resp.ContentLength == 0 {
		return "response_body_absent"
	}
	if req.Header.Get("Range") != "" || resp.StatusCode == http.StatusPartialContent || resp.Header.Get("Content-Range") != "" {
		return "response_range_streamed"
	}
	if strings.EqualFold(strings.TrimSpace(resp.Header.Get("X-Accel-Buffering")), "no") {
		return "response_buffering_disabled_by_upstream"
	}
	mediaType, _, err := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	if err != nil {
		mediaType = strings.TrimSpace(strings.Split(resp.Header.Get("Content-Type"), ";")[0])
	}
	mediaType = strings.ToLower(mediaType)
	if mediaType == "text/event-stream" || mediaType == "multipart/x-mixed-replace" || strings.HasPrefix(mediaType, "application/grpc") {
		return "streaming_response_type"
	}
	return ""
}

func responseStatusAllowsBody(status int) bool {
	return status >= http.StatusOK && status != http.StatusNoContent && status != http.StatusResetContent && status != http.StatusNotModified
}

func publicRetryResponseBodyErrorKind(err error) string {
	if err == nil {
		return ""
	}
	if errors.Is(err, errAgentDisconnected) {
		return "agent_disconnected_during_response"
	}
	if errors.Is(err, io.ErrUnexpectedEOF) {
		return "upstream_response_body_truncated"
	}
	if isTimeoutError(err) {
		return "upstream_response_body_timeout"
	}
	return "upstream_response_body_failed"
}

func (rt *publicAgentAttemptRoundTripper) recordResponseBodyFailure(req *http.Request, agent *AgentConn, attempt int64, err error, outcome, skipReason string) string {
	errorKind := publicRetryResponseBodyErrorKind(err)
	rt.result.LastErrorKind = errorKind
	rt.result.TerminalErrorKind = errorKind
	if rt.result.FirstErrorKind == "" {
		rt.result.FirstErrorKind = errorKind
		rt.result.FirstFailedAgent = agent
	}
	rt.result.Outcome = outcome
	rt.result.RetryCount = attempt - 1
	if skipReason != "" {
		rt.result.ReplaySkippedReason = skipReason
	}
	if req != nil && shouldMarkAgentPassiveFailure(req.Context(), err) {
		rt.app.markPublicRouteTargetAgentPassiveFailure(rt.resolution.Target.ID, agent.AgentID, err)
	}
	logger := log.Warn().Err(err).Str("req_id", rt.requestID).Int64("attempt", attempt).Str("error_kind", errorKind)
	if agent != nil {
		logger = logger.Str("agent", agent.PublicID)
	}
	logger.Msg("Agent response body failed")
	return errorKind
}

func (rt *publicAgentAttemptRoundTripper) observeStreamingResponseBody(req *http.Request, resp *http.Response, agent *AgentConn, attempt int64, skipReason string) {
	if rt == nil || rt.rule == nil || resp == nil || resp.Body == nil || resp.Body == http.NoBody {
		return
	}
	resp.Body = &publicRetryObservedResponseBody{
		ReadCloser: resp.Body,
		onError: func(err error) {
			if req != nil && requestContextCanceled(req.Context(), err) {
				return
			}
			rt.recordResponseBodyFailure(req, agent, attempt, err, publicRetryOutcomeSkipped, skipReason)
		},
	}
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
	finishBufferedResponse := func(resp *http.Response, releaseActiveRequest func(), attempt int64, outcome string) *http.Response {
		releaseActiveRequest()
		body.close()
		responseOwnsRequestBody = true
		rt.result.Outcome = outcome
		rt.result.RetryCount = attempt - 1
		return resp
	}

	maxAttempts := int64(1)
	if rt.rule != nil {
		maxAttempts += rt.rule.MaxRetries
	} else {
		// Agent resource pressure is an admission decision made before request
		// bytes are accepted. A small built-in failover budget prevents one hot
		// agent from producing avoidable 503s without enabling general retries or
		// weakening the no-duplicate-body guarantees below.
		maxAttempts = 4
	}
	attemptedAgents := make(map[int64]struct{}, maxAttempts)
	agent := rt.initial
	for attempt := int64(1); attempt <= maxAttempts; attempt++ {
		if agent == nil {
			break
		}
		attemptedAgents[agent.AgentID] = struct{}{}
		rt.result.FinalAgent = agent
		releaseActiveRequest, admitted := rt.app.beginAgentUpdateProtectedRequest(agent)
		if !admitted {
			next := rt.app.selectTargetAgentExcludingFromSnapshot(rt.snapshot, rt.resolution.Target, attemptedAgents)
			if next == nil {
				return nil, errAgentUpdateCordoned
			}
			agent = next
			continue
		}

		attemptBody, ok := body.next()
		if !ok {
			releaseActiveRequest()
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

		transport := rt.app.agentTargetTransport(agent, rt.resolution.Target)
		if rt.transportForAgent != nil {
			transport = rt.transportForAgent(agent)
		}
		resp, attemptErr := transport.RoundTrip(attemptReq)
		fallbackAttempted := false
		if rt.transportForAgent == nil &&
			agentDialErrorHasKind(attemptErr, "server_pooled_capacity") &&
			agentStreamCapacityAllowsPooledHandoff(attemptErr) &&
			attemptCtx.Err() == nil && !wroteRequest.Load() && bodyRead.Load() == 0 {
			fallbackAttempted = true
			// The pooled class could not admit a new connection before any request
			// bytes or body bytes were consumed. Retire one known-idle shard, then
			// rebuild the body wrapper because net/http may close Request.Body on
			// any error. nonClosingReadCloser keeps a non-replayable original safe
			// for this exact pre-consumption handoff. This is local admission
			// recovery on the same agent, not a policy retry.
			if agentStreamCapacityRequiresIdleReclaim(attemptErr) {
				rt.app.reclaimIdleAgentTransportFor(agent, agentStreamCapacityAllowsCrossSessionReclaim(attemptErr))
			}
			_ = attemptReq.Body.Close()
			fallbackBody, fallbackOK := body.next()
			if fallbackOK {
				if rt.shaper != nil {
					shaper := *rt.shaper
					if attempt > 1 {
						shaper.Rule.RequestExemptBytes = 0
					}
					fallbackBody = shaper.wrapUploadBody(req.Context(), fallbackBody)
				}
				fallbackBody = &countedAttemptBody{ReadCloser: fallbackBody, read: &bodyRead}
				attemptReq = req.Clone(attemptCtx)
				attemptReq.Body = fallbackBody
				attemptReq.GetBody = nil
				resp, attemptErr = rt.app.agentTargetOneShotTransport(agent, rt.resolution.Target).RoundTrip(attemptReq)
			}
		}
		if attemptErr != nil {
			if resp != nil && resp.Body != nil {
				_ = resp.Body.Close()
			}
			releaseActiveRequest()
		} else if resp == nil {
			releaseActiveRequest()
			attemptErr = errors.New("agent transport returned no response")
		}
		if fallbackAttempted && rt.app.AgentTransports != nil {
			rt.app.AgentTransports.recordFallbackResult(attemptErr == nil && resp != nil)
		}
		if attemptErr == nil {
			outcome := ""
			if attempt > 1 {
				outcome = publicRetryOutcomeRecovered
			}
			if rt.rule != nil && rt.rule.retriesStatus(resp.StatusCode) {
				errorKind := fmt.Sprintf("upstream_status_%d", resp.StatusCode)
				rt.result.LastErrorKind = errorKind
				if rt.result.FirstErrorKind == "" {
					rt.result.FirstErrorKind = errorKind
					rt.result.FirstFailedAgent = agent
				}
				switch {
				case attempt >= maxAttempts:
					outcome = publicRetryOutcomeExhausted
				case !body.replayable && !body.bodyless && bodyRead.Load() > 0:
					outcome = publicRetryOutcomeSkipped
					rt.result.ReplaySkippedReason = "request_body_not_replayable"
				default:
					next := rt.app.selectTargetAgentExcludingFromSnapshot(rt.snapshot, rt.resolution.Target, attemptedAgents)
					if next == nil {
						outcome = publicRetryOutcomeExhausted
					} else {
						if resp.Body != nil {
							_ = resp.Body.Close()
						}
						releaseActiveRequest()
						rt.result.RetryCount = attempt
						rt.emitRetry(attemptResolution, agent, next, attempt, errorKind)
						agent = next
						continue
					}
				}
			}

			prepared, responseBodyErr := preparePublicRetryResponseBody(rt.app, req, resp, rt.rule)
			if responseBodyErr != nil {
				releaseActiveRequest()
				if requestContextCanceled(req.Context(), responseBodyErr) {
					rt.result.Outcome = publicRetryOutcomeSkipped
					rt.result.ReplaySkippedReason = "client_cancelled"
					rt.result.RetryCount = attempt - 1
					return nil, responseBodyErr
				}
				errorKind := rt.recordResponseBodyFailure(req, agent, attempt, responseBodyErr, publicRetryOutcomeExhausted, "")
				if attempt >= maxAttempts || rt.rule == nil {
					return nil, responseBodyErr
				}
				if !body.replayable && !body.bodyless && bodyRead.Load() > 0 {
					rt.result.Outcome = publicRetryOutcomeSkipped
					rt.result.ReplaySkippedReason = "request_body_not_replayable"
					return nil, responseBodyErr
				}
				next := rt.app.selectTargetAgentExcludingFromSnapshot(rt.snapshot, rt.resolution.Target, attemptedAgents)
				if next == nil {
					return nil, responseBodyErr
				}
				rt.result.TerminalErrorKind = ""
				rt.result.RetryCount = attempt
				rt.emitRetry(attemptResolution, agent, next, attempt, errorKind)
				agent = next
				continue
			}
			resp.Body = prepared.body
			if prepared.complete {
				return finishBufferedResponse(resp, releaseActiveRequest, attempt, outcome), nil
			}
			if rt.result.ReplaySkippedReason == "" && rt.rule != nil && normalizePublicRetryResponseBodyMode(rt.rule.ResponseBodyMode) == publicRetryResponseBodyModeBuffered && prepared.skipReason != "" && prepared.skipReason != "response_body_absent" {
				rt.result.ReplaySkippedReason = prepared.skipReason
				if outcome == "" {
					outcome = publicRetryOutcomeSkipped
				}
			}
			rt.observeStreamingResponseBody(req, resp, agent, attempt, prepared.skipReason)
			return finishResponse(resp, releaseActiveRequest, attempt, outcome), nil
		}

		errorKind := agentProxyErrorKind(attemptErr)
		var localCapacityErr agentDialError
		if errors.As(attemptErr, &localCapacityErr) && agentDialErrorIsLocalCapacity(localCapacityErr) {
			rt.app.logTerminalAgentStreamCapacityFailure(attemptErr, agent, rt.resolution.Target, rt.requestID)
			if rt.app.AgentTransports != nil {
				rt.app.AgentTransports.recordTerminalCapacityFailure()
			}
		}
		rt.result.LastErrorKind = errorKind
		if rt.result.FirstErrorKind == "" {
			rt.result.FirstErrorKind = errorKind
			if agentProxyFailureAttributedToAgent(attemptErr) {
				rt.result.FirstFailedAgent = agent
			}
		}
		if shouldMarkAgentPassiveFailure(req.Context(), attemptErr) {
			rt.app.markPublicRouteTargetAgentPassiveFailure(rt.resolution.Target.ID, agent.AgentID, attemptErr)
		}
		automaticPressureFailover := rt.rule == nil &&
			agentDialErrorHasKind(attemptErr, "agent_resource_pressure") &&
			attemptCtx.Err() == nil && !wroteRequest.Load() && bodyRead.Load() == 0
		if automaticPressureFailover {
			rt.app.markAgentRoutingResourcePressure(agent.AgentID, time.Now())
		}
		if attempt >= maxAttempts || (rt.rule == nil && !automaticPressureFailover) {
			if attempt > 1 {
				rt.result.Outcome = publicRetryOutcomeExhausted
			}
			rt.result.RetryCount = attempt - 1
			return nil, attemptErr
		}
		if !automaticPressureFailover && !publicRetryAttemptErrorAllowed(req, attemptErr, rt.rule, wroteRequest.Load(), bodyRead.Load(), body.replayable || body.bodyless) {
			rt.result.Outcome = publicRetryOutcomeSkipped
			rt.result.ReplaySkippedReason = publicRetrySkipReason(req, attemptErr, wroteRequest.Load(), bodyRead.Load(), body.replayable || body.bodyless)
			rt.result.RetryCount = attempt - 1
			return nil, attemptErr
		}
		var next *AgentConn
		if automaticPressureFailover {
			next = rt.app.selectUnpressuredTargetAgentExcludingFromSnapshot(rt.snapshot, rt.resolution.Target, attemptedAgents)
		} else {
			next = rt.app.selectTargetAgentExcludingFromSnapshot(rt.snapshot, rt.resolution.Target, attemptedAgents)
		}
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
		case "dial_failed", "dial_timeout", "agent_capacity", "agent_resource_pressure":
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

func agentDialErrorHasKind(err error, kind string) bool {
	if err == nil {
		return false
	}
	var dialErr agentDialError
	return errors.As(err, &dialErr) && dialErr.Kind == kind
}

func agentProxyFailureAttributedToAgent(err error) bool {
	if err == nil {
		return false
	}
	var dialErr agentDialError
	return !errors.As(err, &dialErr) || !agentDialErrorIsLocalCapacity(dialErr)
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
	if errors.Is(err, errAgentUpdateCordoned) {
		return "agent_update_cordoned"
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
		case "agent_resource_pressure":
			return "agent_resource_pressure"
		case "server_capacity":
			return "agent_server_capacity"
		case "server_health_capacity":
			return "agent_server_health_capacity"
		case "server_pooled_capacity":
			return "agent_server_pooled_capacity"
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
	case "agent_capacity", "agent_resource_pressure", "agent_server_capacity", "agent_server_health_capacity", "agent_server_pooled_capacity", "agent_update_cordoned":
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
