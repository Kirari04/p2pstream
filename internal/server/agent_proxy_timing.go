package server

import (
	"context"
	"crypto/tls"
	"net/http/httptrace"
	"strconv"
	"sync/atomic"
	"time"

	p2pstreamv1 "p2pstream/gen/proto/p2pstream/v1"
)

type agentProxyTiming struct {
	started    time.Time
	getConn    atomic.Int64
	connection atomic.Int64
	tlsStart   atomic.Int64
	tls        atomic.Int64
	firstByte  atomic.Int64
	admission  atomic.Int64
	open       atomic.Int64
	handshake  atomic.Int64
	agentDial  atomic.Int64
	agentDNS   atomic.Int64
	reused     atomic.Bool
	lane       atomic.Int64
}

type agentProxyTimingContextKey struct{}

func agentTimingFromContext(ctx context.Context) *agentProxyTiming {
	timing, _ := ctx.Value(agentProxyTimingContextKey{}).(*agentProxyTiming)
	return timing
}

func (t *trafficRequestTrace) withUpstreamTiming(ctx context.Context) context.Context {
	if t == nil || t.level < p2pstreamv1.TrafficTraceLevel_TRAFFIC_TRACE_LEVEL_DEBUG {
		return ctx
	}
	p := &agentProxyTiming{started: time.Now()}
	t.upstreamTiming.Store(p)
	ctx = context.WithValue(ctx, agentProxyTimingContextKey{}, p)
	return httptrace.WithClientTrace(ctx, &httptrace.ClientTrace{
		GetConn: func(string) { p.getConn.Store(time.Since(p.started).Nanoseconds()) },
		GotConn: func(info httptrace.GotConnInfo) {
			p.connection.Store(time.Since(p.started).Nanoseconds() - p.getConn.Load())
			p.reused.Store(info.Reused)
		},
		TLSHandshakeStart: func() { p.tlsStart.Store(time.Since(p.started).Nanoseconds()) },
		TLSHandshakeDone: func(tls.ConnectionState, error) {
			p.tls.Add(time.Since(p.started).Nanoseconds() - p.tlsStart.Load())
		},
		GotFirstResponseByte: func() { p.firstByte.CompareAndSwap(0, time.Since(p.started).Nanoseconds()) },
	})
}

func (p *agentProxyTiming) attributes(out map[string]string) {
	if p == nil {
		return
	}
	out["connection_reused"] = strconv.FormatBool(p.reused.Load())
	out["tunnel_lane"] = strconv.FormatInt(p.lane.Load(), 10)
	for key, ns := range map[string]int64{
		"connection_acquire_ms": p.connection.Load(), "tls_handshake_ms": p.tls.Load(),
		"upstream_first_byte_ms": p.firstByte.Load(), "tunnel_admission_ms": p.admission.Load(),
		"tunnel_open_ms": p.open.Load(), "tunnel_handshake_ms": p.handshake.Load(),
		"agent_dial_ms": p.agentDial.Load(), "agent_dns_ms": p.agentDNS.Load(),
	} {
		out[key] = strconv.FormatFloat(float64(ns)/float64(time.Millisecond), 'f', 3, 64)
	}
}
