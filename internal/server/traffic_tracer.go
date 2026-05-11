package server

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"connectrpc.com/connect"

	p2pstreamv1 "p2pstream/gen/proto/p2pstream/v1"
)

const (
	trafficTraceRingSize         = 500
	trafficTraceSubscriberBuffer = 256
	trafficTraceMaxHeaders       = 32
	trafficTraceMaxHeaderValue   = 256
	trafficTraceRedactedValue    = "[redacted]"
)

type trafficTracer struct {
	enabled         atomic.Bool
	level           atomic.Int32
	updatedAtMillis atomic.Int64
	sequence        atomic.Uint64
	emittedEvents   atomic.Uint64
	droppedEvents   atomic.Uint64

	mu               sync.Mutex
	nextSubscriberID uint64
	subscribers      map[uint64]*trafficTraceSubscriber
	ring             []*p2pstreamv1.StreamTrafficTraceEventsResponse
}

type trafficTraceSubscriber struct {
	id      uint64
	ch      chan *p2pstreamv1.StreamTrafficTraceEventsResponse
	dropped atomic.Uint64
}

func newTrafficTracer() *trafficTracer {
	tracer := &trafficTracer{
		subscribers: make(map[uint64]*trafficTraceSubscriber),
	}
	tracer.level.Store(int32(p2pstreamv1.TrafficTraceLevel_TRAFFIC_TRACE_LEVEL_BASIC))
	tracer.updatedAtMillis.Store(time.Now().UnixMilli())
	return tracer
}

func (a *App) GetTrafficTraceSettings(
	ctx context.Context,
	req *connect.Request[p2pstreamv1.GetTrafficTraceSettingsRequest],
) (*connect.Response[p2pstreamv1.GetTrafficTraceSettingsResponse], error) {
	if _, err := a.requireAdmin(ctx, req.Header()); err != nil {
		return nil, err
	}
	return connect.NewResponse(&p2pstreamv1.GetTrafficTraceSettingsResponse{
		Settings: a.TrafficTracer.settings(),
	}), nil
}

func (a *App) SetTrafficTraceSettings(
	ctx context.Context,
	req *connect.Request[p2pstreamv1.SetTrafficTraceSettingsRequest],
) (*connect.Response[p2pstreamv1.SetTrafficTraceSettingsResponse], error) {
	if _, err := a.requireAdmin(ctx, req.Header()); err != nil {
		return nil, err
	}
	settings, err := a.TrafficTracer.set(req.Msg.Enabled, req.Msg.Level)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&p2pstreamv1.SetTrafficTraceSettingsResponse{Settings: settings}), nil
}

func (a *App) StreamTrafficTraceEvents(
	ctx context.Context,
	req *connect.Request[p2pstreamv1.StreamTrafficTraceEventsRequest],
	stream *connect.ServerStream[p2pstreamv1.StreamTrafficTraceEventsResponse],
) error {
	if _, err := a.requireAdmin(ctx, req.Header()); err != nil {
		return err
	}

	subscriber, initial, replay := a.TrafficTracer.subscribe(req.Msg.ReplayRecent, req.Msg.AfterSequence)
	defer a.TrafficTracer.unsubscribe(subscriber.id)

	if err := stream.Send(traceResponseForSubscriber(initial, subscriber)); err != nil {
		return err
	}
	for _, resp := range replay {
		if err := stream.Send(traceResponseForSubscriber(resp, subscriber)); err != nil {
			return err
		}
	}

	for {
		select {
		case <-ctx.Done():
			return nil
		case resp, ok := <-subscriber.ch:
			if !ok {
				return nil
			}
			if err := stream.Send(traceResponseForSubscriber(resp, subscriber)); err != nil {
				return err
			}
		}
	}
}

func (t *trafficTracer) enabledLevel() (p2pstreamv1.TrafficTraceLevel, bool) {
	if t == nil || !t.enabled.Load() {
		return p2pstreamv1.TrafficTraceLevel_TRAFFIC_TRACE_LEVEL_UNSPECIFIED, false
	}
	level := p2pstreamv1.TrafficTraceLevel(t.level.Load())
	if !validTrafficTraceLevel(level) {
		level = p2pstreamv1.TrafficTraceLevel_TRAFFIC_TRACE_LEVEL_BASIC
	}
	return level, true
}

func (t *trafficTracer) settings() *p2pstreamv1.TrafficTraceSettings {
	if t == nil {
		return &p2pstreamv1.TrafficTraceSettings{
			Level: p2pstreamv1.TrafficTraceLevel_TRAFFIC_TRACE_LEVEL_BASIC,
		}
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.settingsLocked()
}

func (t *trafficTracer) set(enabled bool, level p2pstreamv1.TrafficTraceLevel) (*p2pstreamv1.TrafficTraceSettings, error) {
	if t == nil {
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("traffic tracer is unavailable"))
	}
	if level == p2pstreamv1.TrafficTraceLevel_TRAFFIC_TRACE_LEVEL_UNSPECIFIED {
		if enabled {
			level = p2pstreamv1.TrafficTraceLevel_TRAFFIC_TRACE_LEVEL_BASIC
		} else {
			level = p2pstreamv1.TrafficTraceLevel(t.level.Load())
			if !validTrafficTraceLevel(level) {
				level = p2pstreamv1.TrafficTraceLevel_TRAFFIC_TRACE_LEVEL_BASIC
			}
		}
	}
	if !validTrafficTraceLevel(level) {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("invalid trace level: %s", level.String()))
	}

	t.enabled.Store(enabled)
	t.level.Store(int32(level))
	t.updatedAtMillis.Store(time.Now().UnixMilli())

	t.mu.Lock()
	settings := t.settingsLocked()
	resp := &p2pstreamv1.StreamTrafficTraceEventsResponse{Settings: settings}
	t.broadcastLocked(resp)
	t.mu.Unlock()
	return settings, nil
}

func (t *trafficTracer) subscribe(replayRecent bool, afterSequence uint64) (*trafficTraceSubscriber, *p2pstreamv1.StreamTrafficTraceEventsResponse, []*p2pstreamv1.StreamTrafficTraceEventsResponse) {
	t.mu.Lock()
	defer t.mu.Unlock()

	t.nextSubscriberID++
	subscriber := &trafficTraceSubscriber{
		id: t.nextSubscriberID,
		ch: make(chan *p2pstreamv1.StreamTrafficTraceEventsResponse, trafficTraceSubscriberBuffer),
	}
	t.subscribers[subscriber.id] = subscriber

	initial := &p2pstreamv1.StreamTrafficTraceEventsResponse{Settings: t.settingsLocked()}
	var replay []*p2pstreamv1.StreamTrafficTraceEventsResponse
	if replayRecent {
		for _, resp := range t.ring {
			if resp == nil || resp.Event == nil {
				continue
			}
			if afterSequence > 0 && resp.Event.Sequence <= afterSequence {
				continue
			}
			replay = append(replay, resp)
		}
	}
	return subscriber, initial, replay
}

func (t *trafficTracer) unsubscribe(id uint64) {
	t.mu.Lock()
	defer t.mu.Unlock()
	subscriber, ok := t.subscribers[id]
	if !ok {
		return
	}
	delete(t.subscribers, id)
	close(subscriber.ch)
}

func (t *trafficTracer) publish(event *p2pstreamv1.TrafficTraceEvent) {
	if event == nil {
		return
	}
	level, ok := t.enabledLevel()
	if !ok {
		return
	}

	event = sanitizeTrafficTraceEvent(event, level)
	event.Sequence = t.sequence.Add(1)
	if event.OccurredAtUnixMillis == 0 {
		event.OccurredAtUnixMillis = time.Now().UnixMilli()
	}
	t.emittedEvents.Add(1)

	t.mu.Lock()
	resp := &p2pstreamv1.StreamTrafficTraceEventsResponse{
		Settings: t.settingsLocked(),
		Event:    event,
	}
	t.ring = append(t.ring, resp)
	if len(t.ring) > trafficTraceRingSize {
		copy(t.ring, t.ring[len(t.ring)-trafficTraceRingSize:])
		t.ring = t.ring[:trafficTraceRingSize]
	}
	t.broadcastLocked(resp)
	t.mu.Unlock()
}

func (t *trafficTracer) broadcastLocked(resp *p2pstreamv1.StreamTrafficTraceEventsResponse) {
	for _, subscriber := range t.subscribers {
		select {
		case subscriber.ch <- resp:
		default:
			subscriber.dropped.Add(1)
			t.droppedEvents.Add(1)
		}
	}
}

func (t *trafficTracer) settingsLocked() *p2pstreamv1.TrafficTraceSettings {
	level := p2pstreamv1.TrafficTraceLevel(t.level.Load())
	if !validTrafficTraceLevel(level) {
		level = p2pstreamv1.TrafficTraceLevel_TRAFFIC_TRACE_LEVEL_BASIC
	}
	return &p2pstreamv1.TrafficTraceSettings{
		Enabled:             t.enabled.Load(),
		Level:               level,
		UpdatedAtUnixMillis: t.updatedAtMillis.Load(),
		EmittedEvents:       t.emittedEvents.Load(),
		DroppedEvents:       t.droppedEvents.Load(),
		SubscriberCount:     int64(len(t.subscribers)),
	}
}

func traceResponseForSubscriber(resp *p2pstreamv1.StreamTrafficTraceEventsResponse, subscriber *trafficTraceSubscriber) *p2pstreamv1.StreamTrafficTraceEventsResponse {
	if resp == nil {
		return &p2pstreamv1.StreamTrafficTraceEventsResponse{}
	}
	copyResp := *resp
	if subscriber != nil {
		copyResp.SubscriberDroppedEvents = subscriber.dropped.Load()
	}
	return &copyResp
}

func validTrafficTraceLevel(level p2pstreamv1.TrafficTraceLevel) bool {
	switch level {
	case p2pstreamv1.TrafficTraceLevel_TRAFFIC_TRACE_LEVEL_BASIC,
		p2pstreamv1.TrafficTraceLevel_TRAFFIC_TRACE_LEVEL_DETAILED,
		p2pstreamv1.TrafficTraceLevel_TRAFFIC_TRACE_LEVEL_HEADERS,
		p2pstreamv1.TrafficTraceLevel_TRAFFIC_TRACE_LEVEL_DEBUG:
		return true
	default:
		return false
	}
}

func sanitizeTrafficTraceEvent(event *p2pstreamv1.TrafficTraceEvent, level p2pstreamv1.TrafficTraceLevel) *p2pstreamv1.TrafficTraceEvent {
	copyEvent := *event
	if level < p2pstreamv1.TrafficTraceLevel_TRAFFIC_TRACE_LEVEL_DETAILED {
		copyEvent.Host = ""
		copyEvent.Query = ""
		copyEvent.TargetOrigin = ""
		copyEvent.BackendType = p2pstreamv1.PublicBackendType_PUBLIC_BACKEND_TYPE_UNSPECIFIED
		copyEvent.ForwardMode = p2pstreamv1.PublicBackendForwardMode_PUBLIC_BACKEND_FORWARD_MODE_UNSPECIFIED
		copyEvent.ErrorKind = ""
	}
	if level < p2pstreamv1.TrafficTraceLevel_TRAFFIC_TRACE_LEVEL_HEADERS {
		copyEvent.RequestHeaders = nil
		copyEvent.ResponseHeaders = nil
	}
	if level < p2pstreamv1.TrafficTraceLevel_TRAFFIC_TRACE_LEVEL_DEBUG {
		copyEvent.RequestBytes = 0
		copyEvent.ResponseBytes = 0
		copyEvent.DebugAttributes = nil
	}
	return &copyEvent
}

func sanitizedHeaderMap(header http.Header) map[string]string {
	if len(header) == 0 {
		return nil
	}
	keys := make([]string, 0, len(header))
	for key := range header {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	if len(keys) > trafficTraceMaxHeaders {
		keys = keys[:trafficTraceMaxHeaders]
	}

	resp := make(map[string]string, len(keys))
	for _, key := range keys {
		if traceHeaderIsSensitive(key) {
			resp[key] = trafficTraceRedactedValue
			continue
		}
		value := strings.Join(header.Values(key), ", ")
		if len(value) > trafficTraceMaxHeaderValue {
			value = value[:trafficTraceMaxHeaderValue] + "..."
		}
		resp[key] = value
	}
	return resp
}

func traceHeaderIsSensitive(name string) bool {
	lower := strings.ToLower(name)
	switch lower {
	case "authorization", "proxy-authorization", "cookie", "set-cookie":
		return true
	}
	return strings.Contains(lower, "token") ||
		strings.Contains(lower, "secret") ||
		strings.Contains(lower, "password") ||
		strings.Contains(lower, "key")
}
