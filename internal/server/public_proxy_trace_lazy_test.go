package server

import (
	"net/http/httptest"
	"testing"
)

func TestPublicProxyTrafficTraceSkipsPathWhenDisabled(t *testing.T) {
	app := &App{TrafficTracer: newTrafficTracer()}
	request := httptest.NewRequest("GET", "http://public.test/private/value", nil)
	pathCalls := 0
	trace := newPublicProxyTrafficTrace(app, request, nil, func() string {
		pathCalls++
		return "/private/..."
	})
	if trace != nil || pathCalls != 0 {
		t.Fatalf("disabled trace = %v path calls = %d, want nil/0", trace, pathCalls)
	}

	app.TrafficTracer.enabled.Store(true)
	trace = newPublicProxyTrafficTrace(app, request, nil, func() string {
		pathCalls++
		return "/private/..."
	})
	if trace == nil || trace.path != "/private/..." || pathCalls != 1 {
		t.Fatalf("enabled trace = %v path calls = %d, want safe path once", trace, pathCalls)
	}
}
