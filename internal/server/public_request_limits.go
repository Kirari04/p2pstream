package server

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sync"
	"sync/atomic"
	"time"
)

const (
	defaultPublicMaxConcurrentRequests          = int64(2048)
	defaultPublicMaxConcurrentRequestsPerTarget = int64(256)
	defaultPublicMaxRequestBodyBytes            = int64(1 << 30)
	defaultPublicRequestBodyIdleTimeout         = 30 * time.Second
)

var errPublicRequestBodyIdleTimeout = errors.New("public request body idle timeout")

type publicRequestBodyFailure uint32

const (
	publicRequestBodyFailureNone publicRequestBodyFailure = iota
	publicRequestBodyFailureTooLarge
	publicRequestBodyFailureIdleTimeout
)

type publicRequestBodyState struct {
	failure     atomic.Uint32
	complete    atomic.Bool
	remaining   atomic.Int64
	knownLength bool
}

type publicRequestBodyStateContextKey struct{}

type requestCapacityLimiter struct {
	slots chan struct{}
}

func newRequestCapacityLimiter(limit, fallback int64) *requestCapacityLimiter {
	if limit <= 0 {
		limit = fallback
	}
	return &requestCapacityLimiter{slots: make(chan struct{}, int(limit))}
}

func (l *requestCapacityLimiter) tryAcquire() (func(), bool) {
	if l == nil || l.slots == nil {
		return func() {}, true
	}
	select {
	case l.slots <- struct{}{}:
		var once sync.Once
		return func() { once.Do(func() { <-l.slots }) }, true
	default:
		return nil, false
	}
}

type keyedRequestCapacityLimiter struct {
	mu       sync.Mutex
	capacity int64
	entries  map[int64]*requestCapacityLimiter
}

func newKeyedRequestCapacityLimiter(capacity, fallback int64) *keyedRequestCapacityLimiter {
	if capacity <= 0 {
		capacity = fallback
	}
	return &keyedRequestCapacityLimiter{
		capacity: capacity,
		entries:  make(map[int64]*requestCapacityLimiter),
	}
}

func (l *keyedRequestCapacityLimiter) tryAcquire(key int64) (func(), bool) {
	if l == nil || key <= 0 {
		return func() {}, true
	}
	l.mu.Lock()
	limiter := l.entries[key]
	if limiter == nil {
		limiter = newRequestCapacityLimiter(l.capacity, l.capacity)
		l.entries[key] = limiter
	}
	l.mu.Unlock()
	return limiter.tryAcquire()
}

type idleRequestBody struct {
	body       io.ReadCloser
	controller *http.ResponseController
	timeout    time.Duration
}

type classifiedRequestBody struct {
	io.ReadCloser
	state *publicRequestBodyState
}

func (b *classifiedRequestBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	if b.state != nil {
		if b.state.knownLength && n > 0 && b.state.remaining.Add(-int64(n)) <= 0 {
			b.state.complete.Store(true)
		}
		if errors.Is(err, io.EOF) {
			b.state.complete.Store(true)
		}
	}
	if err != nil && b.state != nil {
		var maxBytesErr *http.MaxBytesError
		switch {
		case errors.As(err, &maxBytesErr):
			b.state.failure.CompareAndSwap(uint32(publicRequestBodyFailureNone), uint32(publicRequestBodyFailureTooLarge))
		case errors.Is(err, errPublicRequestBodyIdleTimeout):
			b.state.failure.CompareAndSwap(uint32(publicRequestBodyFailureNone), uint32(publicRequestBodyFailureIdleTimeout))
		}
	}
	return n, err
}

func (b *idleRequestBody) Read(p []byte) (int, error) {
	if b == nil || b.body == nil {
		return 0, io.EOF
	}
	if b.controller != nil && b.timeout > 0 {
		if err := b.controller.SetReadDeadline(time.Now().Add(b.timeout)); err != nil && !errors.Is(err, http.ErrNotSupported) {
			return 0, fmt.Errorf("set public request body read deadline: %w", err)
		}
	}
	n, err := b.body.Read(p)
	if err != nil && isTimeoutError(err) {
		return n, fmt.Errorf("%w: %v", errPublicRequestBodyIdleTimeout, err)
	}
	return n, err
}

func (b *idleRequestBody) Close() error {
	if b == nil || b.body == nil {
		return nil
	}
	if b.controller != nil {
		_ = b.controller.SetReadDeadline(time.Time{})
	}
	return b.body.Close()
}

func applyPublicRequestBodyLimits(cfgMaxBytes int64, cfgIdleMillis int64, w http.ResponseWriter, r *http.Request) *http.Request {
	if r == nil || r.Body == nil || r.Body == http.NoBody {
		return r
	}
	maxBytes := cfgMaxBytes
	if maxBytes <= 0 {
		maxBytes = defaultPublicMaxRequestBodyBytes
	}
	idleTimeout := time.Duration(cfgIdleMillis) * time.Millisecond
	if idleTimeout <= 0 {
		idleTimeout = defaultPublicRequestBodyIdleTimeout
	}
	body := io.ReadCloser(&idleRequestBody{
		body:       r.Body,
		controller: http.NewResponseController(w),
		timeout:    idleTimeout,
	})
	state := &publicRequestBodyState{knownLength: r.ContentLength >= 0}
	if r.ContentLength == 0 {
		state.complete.Store(true)
	} else if r.ContentLength > 0 {
		state.remaining.Store(r.ContentLength)
	}
	r.Body = &classifiedRequestBody{
		ReadCloser: http.MaxBytesReader(w, body, maxBytes),
		state:      state,
	}
	return r.WithContext(context.WithValue(r.Context(), publicRequestBodyStateContextKey{}, state))
}

func publicRequestBodyLimit(app *App) int64 {
	if app == nil || app.Config == nil || app.Config.PublicMaxRequestBodyBytes <= 0 {
		return defaultPublicMaxRequestBodyBytes
	}
	return app.Config.PublicMaxRequestBodyBytes
}

func publicRequestBodyError(err error) (status int, kind string, message string, ok bool) {
	var maxBytesErr *http.MaxBytesError
	switch {
	case errors.As(err, &maxBytesErr):
		return http.StatusRequestEntityTooLarge, "request_body_too_large", "Request body too large", true
	case errors.Is(err, errPublicRequestBodyIdleTimeout):
		return http.StatusRequestTimeout, "request_body_idle_timeout", "Request body timed out", true
	default:
		return 0, "", "", false
	}
}

func publicRequestBodyErrorForRequest(r *http.Request, err error) (status int, kind string, message string, ok bool) {
	if status, kind, message, ok := publicRequestBodyError(err); ok {
		return status, kind, message, true
	}
	if r == nil {
		return 0, "", "", false
	}
	state, _ := r.Context().Value(publicRequestBodyStateContextKey{}).(*publicRequestBodyState)
	if state == nil {
		return 0, "", "", false
	}
	switch publicRequestBodyFailure(state.failure.Load()) {
	case publicRequestBodyFailureTooLarge:
		return http.StatusRequestEntityTooLarge, "request_body_too_large", "Request body too large", true
	case publicRequestBodyFailureIdleTimeout:
		return http.StatusRequestTimeout, "request_body_idle_timeout", "Request body timed out", true
	default:
		return 0, "", "", false
	}
}
