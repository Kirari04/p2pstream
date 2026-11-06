package server

import (
	"context"
	"database/sql"
	_ "embed"
	"errors"
	"os"

	"connectrpc.com/connect"

	"p2pstream/internal/config"
	"p2pstream/internal/db"
)

// defaultWelcomeBody is embedded seed content and is not mutated at runtime.
//
//go:embed templates/default_welcome.html
var defaultWelcomeBody string

// defaultLocalAccessLoginBody is the editable seed for local access providers.
//
//go:embed templates/default_local_access_login.html
var defaultLocalAccessLoginBody string

func (a *App) ensurePublicProxySeeded(ctx context.Context) error {
	return a.ensurePublicProxySeededWithHook(ctx, nil)
}

func (a *App) ensurePublicProxySeededWithHook(ctx context.Context, beforeCommit func() error) error {
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
	certPEM, keyPEM, err := generateManagedSelfSignedCertificatePEM()
	if err != nil {
		return connect.NewError(connect.CodeInternal, err)
	}

	tx, err := a.DB.BeginTx(ctx, nil)
	if err != nil {
		return connect.NewError(connect.CodeInternal, err)
	}
	defer tx.Rollback()
	q := a.DB.WithTx(tx)
	listeners, err = q.CountPublicListeners(ctx)
	if err != nil {
		return connect.NewError(connect.CodeInternal, err)
	}
	if listeners > 0 {
		return nil
	}

	httpListener, err := q.CreatePublicListener(ctx, db.CreatePublicListenerParams{
		Name:        "public-http",
		BindAddress: "",
		Port:        defaultPublicHTTPPort,
		Protocol:    publicListenerProtocolHTTP,
		Enabled:     1,
	})
	if err != nil {
		return publicDBError(err)
	}

	httpsListener, err := q.CreatePublicListener(ctx, db.CreatePublicListenerParams{
		Name:        "public-https",
		BindAddress: "",
		Port:        443,
		Protocol:    publicListenerProtocolHTTPS,
		Enabled:     1,
	})
	if err != nil {
		return publicDBError(err)
	}

	welcomeSite, err := q.CreatePublicSite(ctx, db.CreatePublicSiteParams{
		Name: "welcome", Enabled: 1, Published: 1, DefaultSite: 1, CanonicalHostname: "",
	})
	if err != nil {
		return publicDBError(err)
	}
	for _, listener := range []db.PublicListener{httpListener, httpsListener} {
		if err := q.MarkPublicListenerSiteMigrated(ctx, listener.ID); err != nil {
			return publicDBError(err)
		}
		if _, err := q.CreatePublicSiteListenerBinding(ctx, db.CreatePublicSiteListenerBindingParams{
			SiteID: welcomeSite.ID, ListenerID: listener.ID, Behavior: publicSiteListenerBehaviorServe,
		}); err != nil {
			return publicDBError(err)
		}
	}
	route, err := q.CreatePublicRoute(ctx, db.CreatePublicRouteParams{
		ListenerID:                 0,
		SiteID:                     sql.NullInt64{Int64: welcomeSite.ID, Valid: true},
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
		PathSecurityMode:           publicRoutePathSecurityModeStrict,
		Enabled:                    1,
	})
	if err != nil {
		return publicDBError(err)
	}
	target, err := q.CreatePublicRouteTarget(ctx, db.CreatePublicRouteTargetParams{
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
		if _, err := q.CreatePublicRouteTargetResponseHeader(ctx, db.CreatePublicRouteTargetResponseHeaderParams{
			TargetID: target.ID,
			Position: int64(idx),
			Name:     header.Name,
			Value:    header.Value,
		}); err != nil {
			return publicDBError(err)
		}
	}

	certParams := publicTLSCertificateInputWithPEMValidity(publicTLSCertificateMutationInput{
		ListenerID:      httpsListener.ID,
		HostnamePattern: defaultSelfSignedTLSHost,
		Enabled:         1,
		Source:          publicTLSCertificateSourceManual,
		Status:          publicTLSCertificateStatusReady,
	}, certPEM)
	cert, err := q.CreatePublicTlsCertificate(ctx, db.CreatePublicTlsCertificateParams{
		ListenerID:           certParams.ListenerID,
		HostnamePattern:      certParams.HostnamePattern,
		CertPath:             "",
		KeyPath:              "",
		Enabled:              certParams.Enabled,
		Source:               publicTLSCertificateSourceManual,
		AcmeChallengeType:    "",
		AcmeCa:               "",
		AcmeEmail:            "",
		DnsCredentialID:      sql.NullInt64{},
		Status:               publicTLSCertificateStatusReady,
		LastError:            "",
		IssuedAt:             sql.NullTime{},
		ExpiresAt:            sql.NullTime{},
		NextRenewalAt:        sql.NullTime{},
		LastRenewalAttemptAt: sql.NullTime{},
	})
	if err != nil {
		return publicDBError(err)
	}
	certificateConfig := a.Config
	if certificateConfig == nil {
		certificateConfig = &config.Config{}
	}
	certPath, keyPath := certificateConfig.PublicTLSCertificatePaths(httpsListener.ID, cert.ID)
	cleanupCertificateFiles := true
	defer func() {
		if cleanupCertificateFiles {
			_ = os.Remove(certPath)
			_ = os.Remove(keyPath)
		}
	}()
	if _, _, err := a.writePublicTLSCertificateFiles(httpsListener.ID, cert.ID, certPEM, keyPEM); err != nil {
		return publicDBError(err)
	}
	certParams.ID = cert.ID
	certParams.CertPath = certPath
	certParams.KeyPath = keyPath
	if _, err := q.UpdatePublicTlsCertificate(ctx, publicTLSCertificateUpdateParams(certParams)); err != nil {
		return publicDBError(err)
	}
	if beforeCommit != nil {
		if err := beforeCommit(); err != nil {
			return connect.NewError(connect.CodeInternal, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return connect.NewError(connect.CodeInternal, err)
	}
	cleanupCertificateFiles = false
	return nil
}
