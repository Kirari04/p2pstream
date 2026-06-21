package server

import (
	p2pstreamv1 "p2pstream/gen/proto/p2pstream/v1"
	"p2pstream/internal/db"
)

type publicConfigRuntime interface {
	proxyStatus() *p2pstreamv1.ProxyStatus
}

type appPublicConfigRuntime struct {
	app *App
}

func (r appPublicConfigRuntime) proxyStatus() *p2pstreamv1.ProxyStatus {
	if r.app == nil {
		return &p2pstreamv1.ProxyStatus{State: p2pstreamv1.ProxyState_PROXY_STATE_STOPPED}
	}
	return r.app.proxyStatus()
}

type publicConfigService struct {
	app          *App
	db           *db.DB
	targetHealth *publicRouteTargetHealthMonitor
	acme         *publicACMEManager
	runtime      publicConfigRuntime
}

func newPublicConfigService(app *App, database *db.DB, targetHealth *publicRouteTargetHealthMonitor, acme *publicACMEManager, runtime publicConfigRuntime) *publicConfigService {
	return &publicConfigService{
		app:          app,
		db:           database,
		targetHealth: targetHealth,
		acme:         acme,
		runtime:      runtime,
	}
}

func (a *App) publicConfigService() *publicConfigService {
	if a == nil {
		return newPublicConfigService(nil, nil, nil, nil, nil)
	}
	if a.publicConfig != nil {
		return a.publicConfig
	}
	a.publicConfig = newPublicConfigService(a, a.DB, a.TargetHealth, a.PublicACME, appPublicConfigRuntime{app: a})
	return a.publicConfig
}

func publicConfigProxyStatus(runtime publicConfigRuntime) *p2pstreamv1.ProxyStatus {
	if runtime == nil {
		return &p2pstreamv1.ProxyStatus{State: p2pstreamv1.ProxyState_PROXY_STATE_STOPPED}
	}
	return runtime.proxyStatus()
}
