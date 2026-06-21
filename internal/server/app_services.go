package server

import "p2pstream/internal/config"

type appServices struct {
	agentHub        *agentHub
	loadBalancers   *loadBalancerRegistry
	targetHealth    *publicRouteTargetHealthMonitor
	trafficTracer   *trafficTracer
	rateLimiter     *publicRateLimiter
	trafficShaper   *publicTrafficShaper
	publicWAF       *publicWAF
	publicCache     *publicProxyCache
	publicACME      *publicACMEManager
	agentTransports *agentTransportPool
	dashboardCache  *dashboardResponseCache
	loginThrottle   *loginThrottle
}

func newAppServices(cfg *config.Config, app *App) appServices {
	services := appServices{
		agentHub:        newAgentHub(),
		loadBalancers:   newLoadBalancerRegistry(),
		targetHealth:    newPublicRouteTargetHealthMonitor(),
		trafficTracer:   newTrafficTracer(),
		rateLimiter:     newPublicRateLimiter(),
		trafficShaper:   newPublicTrafficShaper(),
		publicWAF:       newPublicWAF(),
		publicCache:     newPublicProxyCache(cfg.PublicCacheDir),
		agentTransports: newAgentTransportPool(),
		dashboardCache:  newDashboardResponseCache(),
		loginThrottle:   newLoginThrottle(cfg.LoginThrottleMaxKeys),
	}
	services.agentHub.onDisconnect = func(conn *AgentConn) {
		if app != nil && app.AgentTransports != nil {
			app.AgentTransports.closeAgent(conn.AgentID)
		}
	}
	services.publicACME = newPublicACMEManager(app)
	return services
}

func (a *App) applyServices(services appServices) {
	a.services = services
	a.AgentHub = services.agentHub
	a.LoadBalancers = services.loadBalancers
	a.TargetHealth = services.targetHealth
	a.TrafficTracer = services.trafficTracer
	a.RateLimiter = services.rateLimiter
	a.TrafficShaper = services.trafficShaper
	a.PublicWAF = services.publicWAF
	a.PublicCache = services.publicCache
	a.PublicACME = services.publicACME
	a.AgentTransports = services.agentTransports
	a.DashboardCache = services.dashboardCache
	a.LoginThrottle = services.loginThrottle
}
