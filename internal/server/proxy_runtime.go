package server

import (
	"context"

	p2pstreamv1 "p2pstream/gen/proto/p2pstream/v1"
)

type proxyRuntime struct {
	app *App
}

func newProxyRuntime(app *App) *proxyRuntime {
	return &proxyRuntime{app: app}
}

func (a *App) proxyRuntimeService() *proxyRuntime {
	if a == nil {
		return newProxyRuntime(nil)
	}
	if a.proxyRuntime != nil {
		return a.proxyRuntime
	}
	a.proxyRuntime = newProxyRuntime(a)
	return a.proxyRuntime
}

func (r *proxyRuntime) start(ctx context.Context) (*p2pstreamv1.ProxyStatus, error) {
	return r.app.startProxy(ctx)
}

func (r *proxyRuntime) stop(ctx context.Context) (*p2pstreamv1.ProxyStatus, error) {
	return r.app.stopProxy(ctx)
}
