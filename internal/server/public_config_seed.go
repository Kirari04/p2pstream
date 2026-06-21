package server

import (
	"context"
	"database/sql"
	_ "embed"
	"errors"

	"connectrpc.com/connect"

	"p2pstream/internal/db"
)

// defaultWelcomeBody is embedded seed content and is not mutated at runtime.
//go:embed templates/default_welcome.html
var defaultWelcomeBody string

func (a *App) ensurePublicProxySeeded(ctx context.Context) error {
	defaultTemplates, err := a.ensureDefaultPublicResponseTemplates(ctx)
	if err != nil {
		return err
	}
	listeners, err := a.DB.CountPublicListeners(ctx)
	if err != nil {
		return connect.NewError(connect.CodeInternal, err)
	}
	if listeners > 0 {
		return nil
	}

	defaultWelcomeTemplate, ok := defaultTemplates["default-welcome"]
	if !ok || defaultWelcomeTemplate.ID <= 0 {
		return connect.NewError(connect.CodeInternal, errors.New("default welcome response template was not seeded"))
	}

	httpListener, err := a.DB.CreatePublicListener(ctx, db.CreatePublicListenerParams{
		Name:        "public-http",
		BindAddress: "",
		Port:        defaultPublicHTTPPort,
		Protocol:    publicListenerProtocolHTTP,
		Enabled:     1,
	})
	if err != nil {
		return publicDBError(err)
	}

	httpsListener, err := a.DB.CreatePublicListener(ctx, db.CreatePublicListenerParams{
		Name:        "public-https",
		BindAddress: "",
		Port:        443,
		Protocol:    publicListenerProtocolHTTPS,
		Enabled:     1,
	})
	if err != nil {
		return publicDBError(err)
	}

	for _, listener := range []db.PublicListener{httpListener, httpsListener} {
		route, err := a.DB.CreatePublicRoute(ctx, db.CreatePublicRouteParams{
			ListenerID:                 listener.ID,
			Priority:                   defaultPublicRoutePriority,
			HostPattern:                "",
			PathPrefix:                 "/",
			TargetLoadBalancing:        publicRouteTargetLoadBalancingRoundRobin,
			IsDefault:                  1,
			Action:                     publicRouteActionForward,
			RedirectTargetMode:         "",
			RedirectTarget:             "",
			RedirectStatusCode:         defaultRedirectStatusCode,
			RedirectPreservePathSuffix: 1,
			RedirectPreserveQuery:      1,
			Enabled:                    1,
		})
		if err != nil {
			return publicDBError(err)
		}
		target, err := a.DB.CreatePublicRouteTarget(ctx, db.CreatePublicRouteTargetParams{
			RouteID:                             route.ID,
			Name:                                "default",
			Position:                            0,
			PriorityGroup:                       0,
			Weight:                              100,
			Enabled:                             1,
			TargetType:                          publicRouteTargetTypeStatic,
			Url:                                 "",
			Transport:                           publicRouteTargetTransportDirect,
			AgentSelectorJson:                   "{}",
			AgentLoadBalancing:                  publicRouteTargetLoadBalancingRoundRobin,
			TlsSkipVerify:                       0,
			UpstreamBasicAuthEnabled:            0,
			UpstreamBasicAuthUsername:           "",
			UpstreamBasicAuthPassword:           "",
			UpstreamResponseHeaderTimeoutMillis: defaultTargetUpstreamResponseHeaderTimeoutMillis,
			HealthCheckEnabled:                  0,
			HealthCheckMethod:                   defaultTargetHealthCheckMethod,
			HealthCheckPath:                     defaultTargetHealthCheckPath,
			HealthCheckIntervalMillis:           defaultTargetHealthCheckIntervalMillis,
			HealthCheckTimeoutMillis:            defaultTargetHealthCheckTimeoutMillis,
			HealthCheckHealthyThreshold:         defaultTargetHealthCheckHealthyThreshold,
			HealthCheckUnhealthyThreshold:       defaultTargetHealthCheckUnhealthyThreshold,
			HealthCheckExpectedStatusMin:        defaultTargetHealthCheckExpectedStatusMin,
			HealthCheckExpectedStatusMax:        defaultTargetHealthCheckExpectedStatusMax,
			StaticStatusCode:                    defaultStaticStatusCode,
			StaticResponseBody:                  defaultWelcomeBody,
			StaticResponseBodyMode:              publicResponseBodyModeTemplate,
			StaticResponseTemplateID:            sql.NullInt64{Int64: defaultWelcomeTemplate.ID, Valid: true},
		})
		if err != nil {
			return publicDBError(err)
		}
		for idx, header := range []publicRouteTargetResponseHeaderInput{
			{Name: "Content-Type", Value: defaultWelcomeContentType},
			{Name: "X-Content-Type-Options", Value: "nosniff"},
			{Name: "Cache-Control", Value: defaultWelcomeCacheControl},
		} {
			if _, err := a.DB.CreatePublicRouteTargetResponseHeader(ctx, db.CreatePublicRouteTargetResponseHeaderParams{
				TargetID: target.ID,
				Position: int64(idx),
				Name:     header.Name,
				Value:    header.Value,
			}); err != nil {
				return publicDBError(err)
			}
		}
	}

	certPEM, keyPEM, err := generateManagedSelfSignedCertificatePEM()
	if err != nil {
		return connect.NewError(connect.CodeInternal, err)
	}
	if _, err := a.createUploadedPublicTLSCertificate(ctx, publicTLSCertificateMutationInput{
		ListenerID:      httpsListener.ID,
		HostnamePattern: defaultSelfSignedTLSHost,
		Enabled:         1,
		Source:          publicTLSCertificateSourceManual,
		Status:          publicTLSCertificateStatusReady,
	}, certPEM, keyPEM); err != nil {
		return publicDBError(err)
	}
	return nil
}
