package server

import (
	"context"
	"crypto/tls"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/mail"
	"net/url"
	"os"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"connectrpc.com/connect"

	p2pstreamv1 "p2pstream/gen/proto/p2pstream/v1"
	"p2pstream/internal/config"
	"p2pstream/internal/db"
)

const (
	defaultPublicHTTPPort               = int64(80)
	defaultPublicRoutePriority          = int64(1000)
	defaultPublicSelfSignedValidityDays = int64(3650)
	minPublicSelfSignedValidityDays     = int64(1)
	maxPublicSelfSignedValidityDays     = int64(3650)
	defaultSelfSignedTLSHost            = "p2pstream.local"
	defaultWelcomeContentType           = "text/html; charset=utf-8"
	defaultWelcomeCacheControl          = "no-store"
	defaultWelcomeBody                  = `<!doctype html>
<html lang="en">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <title>Welcome to p2pstream proxy</title>
  <style>
    :root {
      color-scheme: dark;
      --bg: #080a0d;
      --panel: #11161c;
      --line: #29313a;
      --text: #f4f7fa;
      --muted: #9aa8b5;
      --accent: #34d399;
    }
    * { box-sizing: border-box; }
    body {
      margin: 0;
      min-height: 100vh;
      display: grid;
      place-items: center;
      background:
        linear-gradient(90deg, rgba(255,255,255,.035) 1px, transparent 1px),
        linear-gradient(rgba(255,255,255,.035) 1px, transparent 1px),
        var(--bg);
      background-size: 44px 44px;
      color: var(--text);
      font-family: "Trebuchet MS", "Lucida Sans Unicode", Verdana, sans-serif;
      padding: 32px;
    }
    main {
      width: min(720px, 100%);
      border: 1px solid var(--line);
      background: rgba(17, 22, 28, .92);
      padding: 56px;
      box-shadow: 0 24px 80px rgba(0,0,0,.42);
    }
    .label {
      display: inline-flex;
      align-items: center;
      gap: 10px;
      color: var(--accent);
      font-size: 12px;
      letter-spacing: 0;
      text-transform: uppercase;
    }
    .label::before {
      content: "";
      width: 9px;
      height: 9px;
      background: var(--accent);
      box-shadow: 0 0 20px var(--accent);
    }
    h1 {
      margin: 22px 0 18px;
      font-family: Georgia, "Times New Roman", serif;
      font-size: 4.75rem;
      line-height: .95;
      letter-spacing: 0;
    }
    p {
      margin: 0;
      max-width: 58ch;
      color: var(--muted);
      font-size: 1.15rem;
      line-height: 1.7;
    }
    code {
      color: var(--text);
      background: #050608;
      border: 1px solid var(--line);
      padding: 2px 6px;
    }
    @media (max-width: 560px) {
      body { padding: 18px; }
      main { padding: 28px; }
      h1 { font-size: 2.65rem; }
      p { font-size: 1rem; }
    }
  </style>
</head>
<body>
  <main>
    <div class="label">p2pstream proxy online</div>
    <h1>Welcome to p2pstream proxy</h1>
    <p>This default static page is served locally by p2pstream. Replace the <code>default</code> backend or add routes when you are ready to publish real traffic.</p>
  </main>
</body>
</html>
`
	publicResponseBodyModeInline                              = "inline"
	publicResponseBodyModeTemplate                            = "template"
	publicResponseTemplateKindGenericBody                     = "generic_body"
	publicResponseTemplateKindWafCaptchaPage                  = "waf_captcha_page"
	publicResponseTemplateKindWafWaitingRoomPage              = "waf_waiting_room_page"
	defaultResponseTemplateContentType                        = "text/html; charset=utf-8"
	publicRouteTargetTypeProxy                                = "proxy"
	publicRouteTargetTypeStatic                               = "static"
	publicRouteTargetTransportDirect                          = "direct"
	publicRouteTargetTransportAgent                           = "agent"
	publicRouteTargetLoadBalancingRoundRobin                  = "round_robin"
	publicRouteTargetLoadBalancingWeightedRoundRobin          = "weighted_round_robin"
	publicRouteTargetLoadBalancingRandom                      = "random"
	publicRouteTargetLoadBalancingWeightedRandom              = "weighted_random"
	publicRouteTargetLoadBalancingLeastActiveRequests         = "least_active_requests"
	publicRouteTargetLoadBalancingWeightedLeastActiveRequests = "weighted_least_active_requests"
	publicRouteActionForward                                  = "forward"
	publicRouteActionRedirect                                 = "redirect"
	publicRouteRedirectTargetModeSameHostPath                 = "same_host_path"
	publicRouteRedirectTargetModeExternalOriginKeepPath       = "external_origin_keep_path"
	publicRouteRedirectTargetModeAbsoluteURL                  = "absolute_url"
	publicRateLimitAlgorithmFixedWindow                       = "fixed_window"
	publicRateLimitAlgorithmSlidingWindow                     = "sliding_window"
	publicRateLimitAlgorithmTokenBucket                       = "token_bucket"
	publicRateLimitAlgorithmLeakyBucket                       = "leaky_bucket"
	publicTLSCertificateSourceManual                          = "manual"
	publicTLSCertificateSourceACME                            = "acme"
	publicACMEChallengeHTTP01                                 = "http_01"
	publicACMEChallengeTLSALPN01                              = "tls_alpn_01"
	publicACMEChallengeDNS01                                  = "dns_01"
	publicACMECAProduction                                    = "letsencrypt_production"
	publicACMECAStaging                                       = "letsencrypt_staging"
	publicDNSProviderCloudflare                               = "cloudflare"
	publicTLSCertificateStatusPending                         = "pending"
	publicTLSCertificateStatusReady                           = "ready"
	publicTLSCertificateStatusRenewing                        = "renewing"
	publicTLSCertificateStatusError                           = "error"
	defaultStaticStatusCode                                   = int64(http.StatusOK)
	defaultRedirectStatusCode                                 = int64(http.StatusFound)
	defaultTargetHealthCheckMethod                            = http.MethodGet
	defaultTargetHealthCheckPath                              = "/"
	defaultTargetHealthCheckIntervalMillis                    = int64(10000)
	defaultTargetHealthCheckTimeoutMillis                     = int64(2000)
	defaultTargetHealthCheckHealthyThreshold                  = int64(2)
	defaultTargetHealthCheckUnhealthyThreshold                = int64(2)
	defaultTargetHealthCheckExpectedStatusMin                 = int64(200)
	defaultTargetHealthCheckExpectedStatusMax                 = int64(399)
	defaultTargetUpstreamResponseHeaderTimeoutMillis          = int64(60000)
	minTargetUpstreamResponseHeaderTimeoutMillis              = int64(1000)
	maxTargetUpstreamResponseHeaderTimeoutMillis              = int64(3600000)
	maxUpstreamHeaderValueBytes                               = 8192
)

var publicNamePattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]{0,63}$`)

type publicConfigRows struct {
	Agents                     []db.Agent
	AgentLabels                []db.PublicAgentLabel
	Listeners                  []db.PublicListener
	Routes                     []db.PublicRoute
	RouteTargets               []db.PublicRouteTarget
	RouteTargetUpstreamHeaders []db.PublicRouteTargetUpstreamHeader
	RouteTargetResponseHeaders []db.PublicRouteTargetResponseHeader
	TLSCertificates            []db.PublicTlsCertificate
	TLSDNSCredentials          []db.PublicTlsDnsCredential
	RateLimitRules             []db.PublicRateLimitRule
	TrafficShaperRules         []db.PublicTrafficShaperRule
	WafCaptchaProviders        []db.PublicWafCaptchaProvider
	WafRules                   []db.PublicWafRule
	WafSettings                db.PublicWafSetting
	CacheSettings              db.PublicCacheSetting
	CacheRules                 []db.PublicCacheRule
	ResponseTemplates          []db.PublicResponseTemplate
}

type cachedPublicConfig struct {
	Rows     publicConfigRows
	Snapshot *publicProxySnapshot
	Valid    bool
}

type publicRouteTargetResponseHeaderInput struct {
	Name  string
	Value string
}

type publicRouteTargetUpstreamHeaderInput struct {
	ID        int64
	Name      string
	Value     string
	Sensitive int64
}

type publicRouteTargetMutationInput struct {
	Params          db.CreatePublicRouteTargetParams
	UpstreamHeaders []publicRouteTargetUpstreamHeaderInput
	ResponseHeaders []publicRouteTargetResponseHeaderInput
}

type publicTLSCertificateMutationInput struct {
	ID                   int64
	ListenerID           int64
	HostnamePattern      string
	CertPath             string
	KeyPath              string
	Enabled              int64
	Source               string
	ACMEChallengeType    string
	ACMECA               string
	ACMEEmail            string
	DNSCredentialID      sql.NullInt64
	Status               string
	LastError            string
	IssuedAt             sql.NullTime
	ExpiresAt            sql.NullTime
	NextRenewalAt        sql.NullTime
	LastRenewalAttemptAt sql.NullTime
}

type publicTLSCertificateMaterial struct {
	Replace bool
	CertPEM []byte
	KeyPEM  []byte
}

func (a *App) GetPublicProxyConfig(
	ctx context.Context,
	req *connect.Request[p2pstreamv1.GetPublicProxyConfigRequest],
) (*connect.Response[p2pstreamv1.GetPublicProxyConfigResponse], error) {
	if _, err := a.requireUser(ctx, req.Header()); err != nil {
		return nil, err
	}
	resp, err := a.publicProxyConfigResponse(ctx)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(resp), nil
}

func (a *App) ListPublicRouteTargetHealthTraces(
	ctx context.Context,
	req *connect.Request[p2pstreamv1.ListPublicRouteTargetHealthTracesRequest],
) (*connect.Response[p2pstreamv1.ListPublicRouteTargetHealthTracesResponse], error) {
	if _, err := a.requireUser(ctx, req.Header()); err != nil {
		return nil, err
	}
	if req.Msg.RouteTargetId <= 0 {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("route target id is required"))
	}
	limit := req.Msg.Limit
	if limit <= 0 || limit > publicRouteTargetHealthTraceLimitPerTarget {
		limit = publicRouteTargetHealthTraceLimitPerTarget
	}
	var traces []*p2pstreamv1.PublicRouteTargetHealthTrace
	var retained int64
	if a.TargetHealth != nil {
		traces, retained = a.TargetHealth.listHealthTraces(req.Msg.RouteTargetId, req.Msg.AgentId, limit, req.Msg.FailuresOnly)
	}
	return connect.NewResponse(&p2pstreamv1.ListPublicRouteTargetHealthTracesResponse{
		Traces:               traces,
		RetainedCount:        retained,
		MaxRetainedPerTarget: publicRouteTargetHealthTraceLimitPerTarget,
	}), nil
}

func (a *App) CreatePublicListener(
	ctx context.Context,
	req *connect.Request[p2pstreamv1.CreatePublicListenerRequest],
) (*connect.Response[p2pstreamv1.CreatePublicListenerResponse], error) {
	if _, err := a.requireAdmin(ctx, req.Header()); err != nil {
		return nil, err
	}
	params, err := a.validatePublicListenerInput(req.Msg.Name, req.Msg.BindAddress, req.Msg.Port, req.Msg.Protocol, req.Msg.Enabled, false)
	if err != nil {
		return nil, err
	}
	listener, err := a.DB.CreatePublicListener(ctx, db.CreatePublicListenerParams{
		Name:        params.Name,
		BindAddress: params.BindAddress,
		Port:        params.Port,
		Protocol:    params.Protocol,
		Enabled:     params.Enabled,
	})
	if err != nil {
		return nil, publicDBError(err)
	}
	status, err := a.reconcilePublicListenerAfterMutation(ctx, listener.ID)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&p2pstreamv1.CreatePublicListenerResponse{
		Listener: publicListenerToProto(listener),
		Status:   status,
		Proxy:    a.proxyStatus(),
	}), nil
}

func (a *App) UpdatePublicListener(
	ctx context.Context,
	req *connect.Request[p2pstreamv1.UpdatePublicListenerRequest],
) (*connect.Response[p2pstreamv1.UpdatePublicListenerResponse], error) {
	if _, err := a.requireAdmin(ctx, req.Header()); err != nil {
		return nil, err
	}
	params, err := a.validatePublicListenerInput(req.Msg.Name, req.Msg.BindAddress, req.Msg.Port, req.Msg.Protocol, req.Msg.Enabled, false)
	if err != nil {
		return nil, err
	}
	params.ID = req.Msg.Id
	listener, err := a.DB.UpdatePublicListener(ctx, params)
	if err != nil {
		return nil, publicDBError(err)
	}
	status, err := a.reconcilePublicListenerAfterMutation(ctx, listener.ID)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&p2pstreamv1.UpdatePublicListenerResponse{
		Listener: publicListenerToProto(listener),
		Status:   status,
		Proxy:    a.proxyStatus(),
	}), nil
}

func (a *App) DeletePublicListener(
	ctx context.Context,
	req *connect.Request[p2pstreamv1.DeletePublicListenerRequest],
) (*connect.Response[p2pstreamv1.DeletePublicListenerResponse], error) {
	if _, err := a.requireAdmin(ctx, req.Header()); err != nil {
		return nil, err
	}
	a.proxyMu.Lock()
	running := false
	if runtime := a.publicListenerState[req.Msg.Id]; runtime != nil {
		running = runtime.Server != nil
	}
	a.proxyMu.Unlock()
	if running {
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("stop or disable listener before deleting it"))
	}
	if err := a.DB.DeletePublicListener(ctx, req.Msg.Id); err != nil {
		return nil, publicDBError(err)
	}
	if err := a.refreshPublicProxySnapshot(ctx); err != nil {
		return nil, err
	}
	return connect.NewResponse(&p2pstreamv1.DeletePublicListenerResponse{}), nil
}

func (a *App) EnablePublicListener(
	ctx context.Context,
	req *connect.Request[p2pstreamv1.EnablePublicListenerRequest],
) (*connect.Response[p2pstreamv1.EnablePublicListenerResponse], error) {
	if _, err := a.requireAdmin(ctx, req.Header()); err != nil {
		return nil, err
	}
	listener, err := a.DB.GetPublicListener(ctx, req.Msg.Id)
	if err != nil {
		return nil, publicDBError(err)
	}
	if _, err := a.validatePublicListenerInput(listener.Name, listener.BindAddress, listener.Port, protoProtocolFromString(listener.Protocol), true, true); err != nil {
		return nil, err
	}
	listener, err = a.DB.SetPublicListenerEnabled(ctx, db.SetPublicListenerEnabledParams{ID: req.Msg.Id, Enabled: 1})
	if err != nil {
		return nil, publicDBError(err)
	}
	status, err := a.reconcilePublicListenerAfterMutation(ctx, listener.ID)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&p2pstreamv1.EnablePublicListenerResponse{
		Listener: publicListenerToProto(listener),
		Status:   status,
		Proxy:    a.proxyStatus(),
	}), nil
}

func (a *App) DisablePublicListener(
	ctx context.Context,
	req *connect.Request[p2pstreamv1.DisablePublicListenerRequest],
) (*connect.Response[p2pstreamv1.DisablePublicListenerResponse], error) {
	if _, err := a.requireAdmin(ctx, req.Header()); err != nil {
		return nil, err
	}
	listener, err := a.DB.SetPublicListenerEnabled(ctx, db.SetPublicListenerEnabledParams{ID: req.Msg.Id, Enabled: 0})
	if err != nil {
		return nil, publicDBError(err)
	}
	if err := a.refreshPublicProxySnapshot(ctx); err != nil {
		return nil, err
	}
	status, err := a.stopPublicListenerRuntime(ctx, listener.ID)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&p2pstreamv1.DisablePublicListenerResponse{
		Listener: publicListenerToProto(listener),
		Status:   status,
		Proxy:    a.proxyStatus(),
	}), nil
}

func (a *App) StartPublicListener(
	ctx context.Context,
	req *connect.Request[p2pstreamv1.StartPublicListenerRequest],
) (*connect.Response[p2pstreamv1.StartPublicListenerResponse], error) {
	if _, err := a.requireAdmin(ctx, req.Header()); err != nil {
		return nil, err
	}
	if err := a.refreshPublicProxySnapshot(ctx); err != nil {
		return nil, err
	}
	status, err := a.startPublicListenerRuntime(ctx, req.Msg.Id, true)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&p2pstreamv1.StartPublicListenerResponse{Status: status, Proxy: a.proxyStatus()}), nil
}

func (a *App) StopPublicListener(
	ctx context.Context,
	req *connect.Request[p2pstreamv1.StopPublicListenerRequest],
) (*connect.Response[p2pstreamv1.StopPublicListenerResponse], error) {
	if _, err := a.requireAdmin(ctx, req.Header()); err != nil {
		return nil, err
	}
	if err := a.refreshPublicProxySnapshot(ctx); err != nil {
		return nil, err
	}
	status, err := a.stopPublicListenerRuntime(ctx, req.Msg.Id)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&p2pstreamv1.StopPublicListenerResponse{Status: status, Proxy: a.proxyStatus()}), nil
}

func (a *App) CreatePublicRoute(
	ctx context.Context,
	req *connect.Request[p2pstreamv1.CreatePublicRouteRequest],
) (*connect.Response[p2pstreamv1.CreatePublicRouteResponse], error) {
	if _, err := a.requireAdmin(ctx, req.Header()); err != nil {
		return nil, err
	}
	params, routeTargets, err := a.validatePublicRouteInput(
		ctx,
		req.Msg.ListenerId,
		req.Msg.Priority,
		req.Msg.HostPattern,
		req.Msg.PathPrefix,
		req.Msg.TargetLoadBalancing,
		req.Msg.IsDefault,
		req.Msg.Targets,
		req.Msg.Enabled,
		req.Msg.Action,
		req.Msg.RedirectTargetMode,
		req.Msg.RedirectTarget,
		req.Msg.RedirectStatusCode,
		req.Msg.RedirectPreservePathSuffix,
		req.Msg.RedirectPreserveQuery,
	)
	if err != nil {
		return nil, err
	}
	route, storedTargets, err := a.createPublicRouteWithTargets(ctx, params, routeTargets)
	if err != nil {
		return nil, publicDBError(err)
	}
	if err := a.refreshPublicProxySnapshot(ctx); err != nil {
		return nil, err
	}
	upstreamHeaders, responseHeaders := a.publicRouteTargetHeaderMaps(ctx)
	return connect.NewResponse(&p2pstreamv1.CreatePublicRouteResponse{Route: publicRouteToProto(route, storedTargets, upstreamHeaders, responseHeaders, a.TargetHealth)}), nil
}

func (a *App) UpdatePublicRoute(
	ctx context.Context,
	req *connect.Request[p2pstreamv1.UpdatePublicRouteRequest],
) (*connect.Response[p2pstreamv1.UpdatePublicRouteResponse], error) {
	if _, err := a.requireAdmin(ctx, req.Header()); err != nil {
		return nil, err
	}
	params, routeTargets, err := a.validatePublicRouteInput(
		ctx,
		req.Msg.ListenerId,
		req.Msg.Priority,
		req.Msg.HostPattern,
		req.Msg.PathPrefix,
		req.Msg.TargetLoadBalancing,
		req.Msg.IsDefault,
		req.Msg.Targets,
		req.Msg.Enabled,
		req.Msg.Action,
		req.Msg.RedirectTargetMode,
		req.Msg.RedirectTarget,
		req.Msg.RedirectStatusCode,
		req.Msg.RedirectPreservePathSuffix,
		req.Msg.RedirectPreserveQuery,
	)
	if err != nil {
		return nil, err
	}
	params.ID = req.Msg.Id
	route, storedTargets, err := a.updatePublicRouteWithTargets(ctx, params, routeTargets)
	if err != nil {
		return nil, publicDBError(err)
	}
	if err := a.refreshPublicProxySnapshot(ctx); err != nil {
		return nil, err
	}
	upstreamHeaders, responseHeaders := a.publicRouteTargetHeaderMaps(ctx)
	return connect.NewResponse(&p2pstreamv1.UpdatePublicRouteResponse{Route: publicRouteToProto(route, storedTargets, upstreamHeaders, responseHeaders, a.TargetHealth)}), nil
}

func (a *App) createPublicRouteWithTargets(
	ctx context.Context,
	params db.UpdatePublicRouteParams,
	targets []publicRouteTargetMutationInput,
) (db.PublicRoute, []db.PublicRouteTarget, error) {
	tx, err := a.DB.BeginTx(ctx, nil)
	if err != nil {
		return db.PublicRoute{}, nil, err
	}
	defer tx.Rollback()

	qtx := a.DB.WithTx(tx)
	route, err := qtx.CreatePublicRoute(ctx, db.CreatePublicRouteParams{
		ListenerID:                 params.ListenerID,
		Priority:                   params.Priority,
		HostPattern:                params.HostPattern,
		PathPrefix:                 params.PathPrefix,
		TargetLoadBalancing:        params.TargetLoadBalancing,
		IsDefault:                  params.IsDefault,
		Action:                     params.Action,
		RedirectTargetMode:         params.RedirectTargetMode,
		RedirectTarget:             params.RedirectTarget,
		RedirectStatusCode:         params.RedirectStatusCode,
		RedirectPreservePathSuffix: params.RedirectPreservePathSuffix,
		RedirectPreserveQuery:      params.RedirectPreserveQuery,
		Enabled:                    params.Enabled,
	})
	if err != nil {
		return db.PublicRoute{}, nil, err
	}
	storedTargets, err := insertPublicRouteTargets(ctx, qtx, route.ID, targets)
	if err != nil {
		return db.PublicRoute{}, nil, err
	}
	if err := tx.Commit(); err != nil {
		return db.PublicRoute{}, nil, err
	}
	return route, storedTargets, nil
}

func (a *App) updatePublicRouteWithTargets(
	ctx context.Context,
	params db.UpdatePublicRouteParams,
	targets []publicRouteTargetMutationInput,
) (db.PublicRoute, []db.PublicRouteTarget, error) {
	tx, err := a.DB.BeginTx(ctx, nil)
	if err != nil {
		return db.PublicRoute{}, nil, err
	}
	defer tx.Rollback()

	qtx := a.DB.WithTx(tx)
	route, err := qtx.UpdatePublicRoute(ctx, params)
	if err != nil {
		return db.PublicRoute{}, nil, err
	}
	if err := qtx.DeletePublicRouteTargets(ctx, params.ID); err != nil {
		return db.PublicRoute{}, nil, err
	}
	storedTargets, err := insertPublicRouteTargets(ctx, qtx, params.ID, targets)
	if err != nil {
		return db.PublicRoute{}, nil, err
	}
	if err := tx.Commit(); err != nil {
		return db.PublicRoute{}, nil, err
	}
	return route, storedTargets, nil
}

func insertPublicRouteTargets(
	ctx context.Context,
	queries *db.Queries,
	routeID int64,
	targets []publicRouteTargetMutationInput,
) ([]db.PublicRouteTarget, error) {
	storedTargets := make([]db.PublicRouteTarget, 0, len(targets))
	for idx, target := range targets {
		params := target.Params
		params.RouteID = routeID
		params.Position = int64(idx)
		stored, err := queries.CreatePublicRouteTarget(ctx, params)
		if err != nil {
			return nil, err
		}
		for headerIdx, header := range target.UpstreamHeaders {
			if _, err := queries.CreatePublicRouteTargetUpstreamHeader(ctx, db.CreatePublicRouteTargetUpstreamHeaderParams{
				TargetID:  stored.ID,
				Position:  int64(headerIdx),
				Name:      header.Name,
				Value:     header.Value,
				Sensitive: header.Sensitive,
			}); err != nil {
				return nil, err
			}
		}
		for headerIdx, header := range target.ResponseHeaders {
			if _, err := queries.CreatePublicRouteTargetResponseHeader(ctx, db.CreatePublicRouteTargetResponseHeaderParams{
				TargetID: stored.ID,
				Position: int64(headerIdx),
				Name:     header.Name,
				Value:    header.Value,
			}); err != nil {
				return nil, err
			}
		}
		storedTargets = append(storedTargets, stored)
	}
	return storedTargets, nil
}

func (a *App) DeletePublicRoute(
	ctx context.Context,
	req *connect.Request[p2pstreamv1.DeletePublicRouteRequest],
) (*connect.Response[p2pstreamv1.DeletePublicRouteResponse], error) {
	if _, err := a.requireAdmin(ctx, req.Header()); err != nil {
		return nil, err
	}
	if err := a.DB.DeletePublicRoute(ctx, req.Msg.Id); err != nil {
		return nil, publicDBError(err)
	}
	if err := a.refreshPublicProxySnapshot(ctx); err != nil {
		return nil, err
	}
	return connect.NewResponse(&p2pstreamv1.DeletePublicRouteResponse{}), nil
}

func (a *App) CreatePublicTlsDnsCredential(
	ctx context.Context,
	req *connect.Request[p2pstreamv1.CreatePublicTlsDnsCredentialRequest],
) (*connect.Response[p2pstreamv1.CreatePublicTlsDnsCredentialResponse], error) {
	if _, err := a.requireAdmin(ctx, req.Header()); err != nil {
		return nil, err
	}
	params, err := a.validatePublicTLSDNSCredentialInput(
		req.Msg.Name,
		req.Msg.Provider,
		req.Msg.CloudflareZoneId,
		req.Msg.ApiToken,
		req.Msg.Enabled,
		nil,
		req.Msg.ApiToken != "",
	)
	if err != nil {
		return nil, err
	}
	credential, err := a.DB.CreatePublicTlsDnsCredential(ctx, db.CreatePublicTlsDnsCredentialParams{
		Name:             params.Name,
		Provider:         params.Provider,
		CloudflareZoneID: params.CloudflareZoneID,
		ApiToken:         params.ApiToken,
		Enabled:          params.Enabled,
	})
	if err != nil {
		return nil, publicDBError(err)
	}
	return connect.NewResponse(&p2pstreamv1.CreatePublicTlsDnsCredentialResponse{Credential: publicTLSDNSCredentialToProto(credential)}), nil
}

func (a *App) UpdatePublicTlsDnsCredential(
	ctx context.Context,
	req *connect.Request[p2pstreamv1.UpdatePublicTlsDnsCredentialRequest],
) (*connect.Response[p2pstreamv1.UpdatePublicTlsDnsCredentialResponse], error) {
	if _, err := a.requireAdmin(ctx, req.Header()); err != nil {
		return nil, err
	}
	existing, err := a.DB.GetPublicTlsDnsCredential(ctx, req.Msg.Id)
	if err != nil {
		return nil, publicDBError(err)
	}
	params, err := a.validatePublicTLSDNSCredentialInput(
		req.Msg.Name,
		req.Msg.Provider,
		req.Msg.CloudflareZoneId,
		req.Msg.ApiToken,
		req.Msg.Enabled,
		&existing,
		req.Msg.ApiTokenSet || req.Msg.ApiToken != "",
	)
	if err != nil {
		return nil, err
	}
	params.ID = req.Msg.Id
	credential, err := a.DB.UpdatePublicTlsDnsCredential(ctx, params)
	if err != nil {
		return nil, publicDBError(err)
	}
	if err := a.refreshPublicProxySnapshot(ctx); err != nil {
		return nil, err
	}
	return connect.NewResponse(&p2pstreamv1.UpdatePublicTlsDnsCredentialResponse{Credential: publicTLSDNSCredentialToProto(credential)}), nil
}

func (a *App) DeletePublicTlsDnsCredential(
	ctx context.Context,
	req *connect.Request[p2pstreamv1.DeletePublicTlsDnsCredentialRequest],
) (*connect.Response[p2pstreamv1.DeletePublicTlsDnsCredentialResponse], error) {
	if _, err := a.requireAdmin(ctx, req.Header()); err != nil {
		return nil, err
	}
	if err := a.DB.DeletePublicTlsDnsCredential(ctx, req.Msg.Id); err != nil {
		return nil, publicDBError(err)
	}
	if err := a.refreshPublicProxySnapshot(ctx); err != nil {
		return nil, err
	}
	return connect.NewResponse(&p2pstreamv1.DeletePublicTlsDnsCredentialResponse{}), nil
}

func (a *App) CreatePublicTlsCertificate(
	ctx context.Context,
	req *connect.Request[p2pstreamv1.CreatePublicTlsCertificateRequest],
) (*connect.Response[p2pstreamv1.CreatePublicTlsCertificateResponse], error) {
	if _, err := a.requireAdmin(ctx, req.Header()); err != nil {
		return nil, err
	}
	params, material, err := a.validatePublicTLSCertificateInput(
		ctx,
		req.Msg.ListenerId,
		req.Msg.HostnamePattern,
		req.Msg.CertPath,
		req.Msg.KeyPath,
		req.Msg.CertPem,
		req.Msg.KeyPem,
		req.Msg.Enabled,
		req.Msg.Source,
		req.Msg.AcmeChallengeType,
		req.Msg.AcmeCa,
		req.Msg.AcmeEmail,
		req.Msg.DnsCredentialId,
		req.Msg.GenerateSelfSigned,
		req.Msg.SelfSignedValidityDays,
		nil,
		false,
	)
	if err != nil {
		return nil, err
	}

	var cert db.PublicTlsCertificate
	if material.Replace {
		cert, err = a.createUploadedPublicTLSCertificate(ctx, params, material.CertPEM, material.KeyPEM)
	} else {
		cert, err = a.DB.CreatePublicTlsCertificate(ctx, publicTLSCertificateCreateParams(params))
	}
	if err != nil {
		return nil, publicDBError(err)
	}
	if err := a.refreshPublicProxySnapshot(ctx); err != nil {
		return nil, err
	}
	_, _ = a.restartTLSListenerIfActive(ctx, cert.ListenerID)
	a.queuePublicACMECertificateIssue(cert)
	return connect.NewResponse(&p2pstreamv1.CreatePublicTlsCertificateResponse{TlsCertificate: publicTLSCertificateToProto(cert)}), nil
}

func (a *App) UpdatePublicTlsCertificate(
	ctx context.Context,
	req *connect.Request[p2pstreamv1.UpdatePublicTlsCertificateRequest],
) (*connect.Response[p2pstreamv1.UpdatePublicTlsCertificateResponse], error) {
	if _, err := a.requireAdmin(ctx, req.Header()); err != nil {
		return nil, err
	}
	existing, err := a.DB.GetPublicTlsCertificate(ctx, req.Msg.Id)
	if err != nil {
		return nil, publicDBError(err)
	}
	params, material, err := a.validatePublicTLSCertificateInput(
		ctx,
		req.Msg.ListenerId,
		req.Msg.HostnamePattern,
		req.Msg.CertPath,
		req.Msg.KeyPath,
		req.Msg.CertPem,
		req.Msg.KeyPem,
		req.Msg.Enabled,
		req.Msg.Source,
		req.Msg.AcmeChallengeType,
		req.Msg.AcmeCa,
		req.Msg.AcmeEmail,
		req.Msg.DnsCredentialId,
		req.Msg.GenerateSelfSigned,
		req.Msg.SelfSignedValidityDays,
		&existing,
		true,
	)
	if err != nil {
		return nil, err
	}
	params.ID = req.Msg.Id
	if params.CertPath == "" && params.KeyPath == "" && params.Source == publicTLSCertificateSourceManual {
		params.CertPath = existing.CertPath
		params.KeyPath = existing.KeyPath
	}
	if params.Source == publicTLSCertificateSourceACME && params.CertPath == "" && params.KeyPath == "" {
		params.CertPath = existing.CertPath
		params.KeyPath = existing.KeyPath
	}

	var cert db.PublicTlsCertificate
	if material.Replace {
		cert, err = a.updateUploadedPublicTLSCertificate(ctx, params, material.CertPEM, material.KeyPEM)
	} else {
		cert, err = a.DB.UpdatePublicTlsCertificate(ctx, publicTLSCertificateUpdateParams(params))
	}
	if err != nil {
		return nil, publicDBError(err)
	}
	if err := a.refreshPublicProxySnapshot(ctx); err != nil {
		return nil, err
	}
	if existing.ListenerID != cert.ListenerID {
		_, _ = a.restartTLSListenerIfActive(ctx, existing.ListenerID)
	}
	_, _ = a.restartTLSListenerIfActive(ctx, cert.ListenerID)
	a.queuePublicACMECertificateIssue(cert)
	return connect.NewResponse(&p2pstreamv1.UpdatePublicTlsCertificateResponse{TlsCertificate: publicTLSCertificateToProto(cert)}), nil
}

func (a *App) DeletePublicTlsCertificate(
	ctx context.Context,
	req *connect.Request[p2pstreamv1.DeletePublicTlsCertificateRequest],
) (*connect.Response[p2pstreamv1.DeletePublicTlsCertificateResponse], error) {
	if _, err := a.requireAdmin(ctx, req.Header()); err != nil {
		return nil, err
	}
	cert, err := a.DB.GetPublicTlsCertificate(ctx, req.Msg.Id)
	if err != nil {
		return nil, publicDBError(err)
	}
	if err := a.DB.DeletePublicTlsCertificate(ctx, req.Msg.Id); err != nil {
		return nil, publicDBError(err)
	}
	if err := a.refreshPublicProxySnapshot(ctx); err != nil {
		return nil, err
	}
	_, _ = a.restartTLSListenerIfActive(ctx, cert.ListenerID)
	return connect.NewResponse(&p2pstreamv1.DeletePublicTlsCertificateResponse{}), nil
}

func (a *App) RenewPublicTlsCertificate(
	ctx context.Context,
	req *connect.Request[p2pstreamv1.RenewPublicTlsCertificateRequest],
) (*connect.Response[p2pstreamv1.RenewPublicTlsCertificateResponse], error) {
	if _, err := a.requireAdmin(ctx, req.Header()); err != nil {
		return nil, err
	}
	cert, err := a.DB.GetPublicTlsCertificate(ctx, req.Msg.Id)
	if err != nil {
		return nil, publicDBError(err)
	}
	if normalizePublicTLSCertificateSource(cert.Source) != publicTLSCertificateSourceACME {
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("only ACME certificates can be renewed"))
	}
	cert, err = a.DB.UpdatePublicTlsCertificateStatus(ctx, db.UpdatePublicTlsCertificateStatusParams{
		ID:                   cert.ID,
		Status:               publicTLSCertificateStatusRenewing,
		LastError:            "",
		LastRenewalAttemptAt: sql.NullTime{Time: time.Now().UTC(), Valid: true},
	})
	if err != nil {
		return nil, publicDBError(err)
	}
	a.queuePublicACMECertificateIssue(cert)
	return connect.NewResponse(&p2pstreamv1.RenewPublicTlsCertificateResponse{TlsCertificate: publicTLSCertificateToProto(cert)}), nil
}

func (a *App) createUploadedPublicTLSCertificate(
	ctx context.Context,
	params publicTLSCertificateMutationInput,
	certPEM []byte,
	keyPEM []byte,
) (db.PublicTlsCertificate, error) {
	params = publicTLSCertificateInputWithPEMValidity(params, certPEM)
	tx, err := a.DB.BeginTx(ctx, nil)
	if err != nil {
		return db.PublicTlsCertificate{}, err
	}
	defer tx.Rollback()

	qtx := a.DB.WithTx(tx)
	cert, err := qtx.CreatePublicTlsCertificate(ctx, db.CreatePublicTlsCertificateParams{
		ListenerID:           params.ListenerID,
		HostnamePattern:      params.HostnamePattern,
		CertPath:             "",
		KeyPath:              "",
		Enabled:              params.Enabled,
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
		return db.PublicTlsCertificate{}, err
	}

	certPath, keyPath, err := a.writePublicTLSCertificateFiles(params.ListenerID, cert.ID, certPEM, keyPEM)
	if err != nil {
		return db.PublicTlsCertificate{}, err
	}
	cleanupOnError := true
	defer func() {
		if cleanupOnError {
			_ = os.Remove(certPath)
			_ = os.Remove(keyPath)
		}
	}()

	params.ID = cert.ID
	params.CertPath = certPath
	params.KeyPath = keyPath
	cert, err = qtx.UpdatePublicTlsCertificate(ctx, publicTLSCertificateUpdateParams(params))
	if err != nil {
		return db.PublicTlsCertificate{}, err
	}
	if err := tx.Commit(); err != nil {
		return db.PublicTlsCertificate{}, err
	}
	cleanupOnError = false
	return cert, nil
}

func (a *App) updateUploadedPublicTLSCertificate(
	ctx context.Context,
	params publicTLSCertificateMutationInput,
	certPEM []byte,
	keyPEM []byte,
) (db.PublicTlsCertificate, error) {
	params = publicTLSCertificateInputWithPEMValidity(params, certPEM)
	certPath, keyPath, err := a.writePublicTLSCertificateFiles(params.ListenerID, params.ID, certPEM, keyPEM)
	if err != nil {
		return db.PublicTlsCertificate{}, err
	}

	params.CertPath = certPath
	params.KeyPath = keyPath
	cert, err := a.DB.UpdatePublicTlsCertificate(ctx, publicTLSCertificateUpdateParams(params))
	if err != nil {
		return db.PublicTlsCertificate{}, err
	}
	return cert, nil
}

func (a *App) writePublicTLSCertificateFiles(listenerID, mappingID int64, certPEM, keyPEM []byte) (string, string, error) {
	cfg := a.Config
	if cfg == nil {
		cfg = &config.Config{}
	}
	return cfg.WritePublicTLSCertificateFiles(listenerID, mappingID, certPEM, keyPEM)
}

func (a *App) publicProxyConfigResponse(ctx context.Context) (*p2pstreamv1.GetPublicProxyConfigResponse, error) {
	rows, snap, err := a.cachedOrLoadPublicConfig(ctx)
	if err != nil {
		return nil, err
	}
	routeTargetUpstreamHeaders := publicRouteTargetUpstreamHeadersByTarget(rows.RouteTargetUpstreamHeaders)
	routeTargetResponseHeaders := publicRouteTargetResponseHeadersByTarget(rows.RouteTargetResponseHeaders)

	return &p2pstreamv1.GetPublicProxyConfigResponse{
		Listeners:           publicListenersToProto(rows.Listeners),
		Routes:              publicRoutesToProto(rows.Routes, rows.RouteTargets, routeTargetUpstreamHeaders, routeTargetResponseHeaders, a.TargetHealth),
		RouteTargets:        publicRouteTargetsToProto(rows.RouteTargets, routeTargetUpstreamHeaders, routeTargetResponseHeaders, a.TargetHealth),
		TlsCertificates:     publicTLSCertificatesToProto(rows.TLSCertificates),
		Proxy:               a.proxyStatus(),
		Agents:              a.publicAgentsToProto(ctx, rows.Agents, false),
		RateLimitRules:      publicRateLimitRulesToProto(rows.RateLimitRules),
		TrafficShaperRules:  publicTrafficShaperRulesToProto(rows.TrafficShaperRules),
		WafCaptchaProviders: publicWafCaptchaProvidersToProto(rows.WafCaptchaProviders, false),
		WafRules:            publicWafRulesToProto(rows.WafRules),
		CacheSettings:       publicCacheSettingsConfigToProto(snap.CacheSettings),
		CacheRules:          publicCacheRulesToProto(rows.CacheRules),
		TlsDnsCredentials:   publicTLSDNSCredentialsToProto(rows.TLSDNSCredentials),
		ResponseTemplates:   publicResponseTemplatesToProto(rows.ResponseTemplates),
	}, nil
}

func (a *App) refreshPublicProxySnapshot(ctx context.Context) error {
	snap, err := a.loadPublicProxySnapshot(ctx)
	if err != nil {
		return err
	}
	a.applyPublicProxySnapshot(snap)
	return nil
}

func (a *App) applyPublicProxySnapshot(snap *publicProxySnapshot) {
	a.proxyMu.Lock()
	a.publicSnapshot = snap
	a.ensureListenerStatesLocked(snap)
	a.proxyStatusLocked()
	active := a.proxyServiceActive
	a.proxyMu.Unlock()
	a.LoadBalancers.reconcile(snap)
	if a.TargetHealth != nil {
		a.TargetHealth.reconcile(a, snap, active)
	}
	if a.RateLimiter != nil {
		a.RateLimiter.reconcile(snap)
	}
	if a.TrafficShaper != nil {
		a.TrafficShaper.reconcile(snap)
	}
	if a.PublicWAF != nil {
		a.PublicWAF.reconcile(snap)
	}
	if a.PublicCache != nil {
		a.PublicCache.reconcile(snap.CacheSettings)
	}
}

func (a *App) loadPublicProxySnapshot(ctx context.Context) (*publicProxySnapshot, error) {
	rows, err := a.loadPublicConfigRows(ctx)
	if err != nil {
		return nil, err
	}
	snap, err := snapshotFromPublicRows(rows)
	if err != nil {
		return nil, err
	}
	a.storePublicConfigCache(rows, snap)
	return snap, nil
}

func (a *App) cachedOrLoadPublicConfig(ctx context.Context) (publicConfigRows, *publicProxySnapshot, error) {
	if rows, snap, ok := a.cachedPublicConfig(); ok {
		return rows, snap, nil
	}
	snap, err := a.loadPublicProxySnapshot(ctx)
	if err != nil {
		return publicConfigRows{}, nil, err
	}
	rows, snap, ok := a.cachedPublicConfig()
	if !ok {
		return publicConfigRows{}, nil, connect.NewError(connect.CodeInternal, errors.New("public proxy config cache was not populated"))
	}
	return rows, snap, nil
}

func (a *App) cachedPublicConfig() (publicConfigRows, *publicProxySnapshot, bool) {
	a.publicConfigCacheMu.RLock()
	cached := a.publicConfigCache
	a.publicConfigCacheMu.RUnlock()
	return cached.Rows, cached.Snapshot, cached.Valid && cached.Snapshot != nil
}

func (a *App) storePublicConfigCache(rows publicConfigRows, snap *publicProxySnapshot) {
	a.publicConfigCacheMu.Lock()
	a.publicConfigCache = cachedPublicConfig{
		Rows:     rows,
		Snapshot: snap,
		Valid:    snap != nil,
	}
	a.publicConfigCacheMu.Unlock()
}

func (a *App) loadPublicConfigRows(ctx context.Context) (publicConfigRows, error) {
	if a.DB == nil {
		return publicConfigRows{}, connect.NewError(connect.CodeFailedPrecondition, errors.New("database is required for public proxy config"))
	}
	if err := a.ensurePublicProxySeeded(ctx); err != nil {
		return publicConfigRows{}, err
	}
	responseTemplates, err := a.DB.ListPublicResponseTemplates(ctx)
	if err != nil {
		return publicConfigRows{}, connect.NewError(connect.CodeInternal, err)
	}
	agents, err := a.DB.ListAgents(ctx)
	if err != nil {
		return publicConfigRows{}, connect.NewError(connect.CodeInternal, err)
	}
	agentLabels, err := a.DB.ListAgentLabels(ctx)
	if err != nil {
		return publicConfigRows{}, connect.NewError(connect.CodeInternal, err)
	}
	listeners, err := a.DB.ListPublicListeners(ctx)
	if err != nil {
		return publicConfigRows{}, connect.NewError(connect.CodeInternal, err)
	}
	routes, err := a.DB.ListPublicRoutes(ctx)
	if err != nil {
		return publicConfigRows{}, connect.NewError(connect.CodeInternal, err)
	}
	routeTargets, err := a.DB.ListPublicRouteTargets(ctx)
	if err != nil {
		return publicConfigRows{}, connect.NewError(connect.CodeInternal, err)
	}
	routeTargetUpstreamHeaders, err := a.DB.ListPublicRouteTargetUpstreamHeaders(ctx)
	if err != nil {
		return publicConfigRows{}, connect.NewError(connect.CodeInternal, err)
	}
	routeTargetResponseHeaders, err := a.DB.ListPublicRouteTargetResponseHeaders(ctx)
	if err != nil {
		return publicConfigRows{}, connect.NewError(connect.CodeInternal, err)
	}
	certs, err := a.DB.ListPublicTlsCertificates(ctx)
	if err != nil {
		return publicConfigRows{}, connect.NewError(connect.CodeInternal, err)
	}
	tlsDNSCredentials, err := a.DB.ListPublicTlsDnsCredentials(ctx)
	if err != nil {
		return publicConfigRows{}, connect.NewError(connect.CodeInternal, err)
	}
	rateLimitRules, err := a.DB.ListPublicRateLimitRules(ctx)
	if err != nil {
		return publicConfigRows{}, connect.NewError(connect.CodeInternal, err)
	}
	trafficShaperRules, err := a.DB.ListPublicTrafficShaperRules(ctx)
	if err != nil {
		return publicConfigRows{}, connect.NewError(connect.CodeInternal, err)
	}
	wafCaptchaProviders, err := a.DB.ListPublicWafCaptchaProviders(ctx)
	if err != nil {
		return publicConfigRows{}, connect.NewError(connect.CodeInternal, err)
	}
	wafRules, err := a.DB.ListPublicWafRules(ctx)
	if err != nil {
		return publicConfigRows{}, connect.NewError(connect.CodeInternal, err)
	}
	wafSettings, err := a.ensurePublicWafSettings(ctx)
	if err != nil {
		return publicConfigRows{}, err
	}
	cacheSettings, err := a.ensurePublicCacheSettings(ctx)
	if err != nil {
		return publicConfigRows{}, err
	}
	cacheRules, err := a.DB.ListPublicCacheRules(ctx)
	if err != nil {
		return publicConfigRows{}, connect.NewError(connect.CodeInternal, err)
	}
	return publicConfigRows{
		Agents:                     agents,
		AgentLabels:                agentLabels,
		Listeners:                  listeners,
		Routes:                     routes,
		RouteTargets:               routeTargets,
		RouteTargetUpstreamHeaders: routeTargetUpstreamHeaders,
		RouteTargetResponseHeaders: routeTargetResponseHeaders,
		TLSCertificates:            certs,
		TLSDNSCredentials:          tlsDNSCredentials,
		RateLimitRules:             rateLimitRules,
		TrafficShaperRules:         trafficShaperRules,
		WafCaptchaProviders:        wafCaptchaProviders,
		WafRules:                   wafRules,
		WafSettings:                wafSettings,
		CacheSettings:              cacheSettings,
		CacheRules:                 cacheRules,
		ResponseTemplates:          responseTemplates,
	}, nil
}

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

func snapshotFromPublicRows(rows publicConfigRows) (*publicProxySnapshot, error) {
	snap := &publicProxySnapshot{
		RouteTargets:        make(map[int64]publicRouteTargetConfig),
		Agents:              make(map[int64]publicAgentConfig),
		Listeners:           make(map[int64]publicListenerConfig),
		RoutesByListener:    make(map[int64][]publicRouteConfig),
		CertsByListener:     make(map[int64][]publicTLSCertificateConfig),
		WafCaptchaProviders: make(map[int64]publicWafCaptchaProviderConfig),
		WafCookieSecret:     []byte(rows.WafSettings.CookieSigningSecret),
		CacheSettings:       publicCacheSettingsRowToConfig(rows.CacheSettings),
		ResponseTemplates:   publicResponseTemplatesToConfig(rows.ResponseTemplates),
	}

	routeTargetsByRoute := make(map[int64][]publicRouteTargetConfig)
	targetUpstreamHeadersByTarget := publicRouteTargetUpstreamHeadersByTarget(rows.RouteTargetUpstreamHeaders)
	targetResponseHeadersByTarget := publicRouteTargetResponseHeadersByTarget(rows.RouteTargetResponseHeaders)
	agentLabelsByAgent := publicAgentLabelsByAgent(rows.AgentLabels)
	for _, agent := range rows.Agents {
		snap.Agents[agent.ID] = publicAgentConfig{
			ID:       agent.ID,
			PublicID: agent.PublicID,
			Name:     agent.Name,
			Enabled:  agent.Enabled != 0,
			Labels:   cloneStringMap(agentLabelsByAgent[agent.ID]),
		}
	}
	for _, target := range rows.RouteTargets {
		targetType := normalizePublicRouteTargetType(target.TargetType)
		transport := normalizePublicRouteTargetTransport(target.Transport)
		var parsed *url.URL
		if targetType == publicRouteTargetTypeProxy {
			var err error
			parsed, err = parsePublicTargetOrigin(target.Url)
			if err != nil {
				return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("route target %q has invalid URL: %w", target.Name, err))
			}
		}
		staticResponseBody, err := effectiveGenericResponseBody(target.StaticResponseBodyMode, target.StaticResponseTemplateID, target.StaticResponseBody, snap.ResponseTemplates)
		if err != nil {
			return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("route target %q has invalid static response template: %w", target.Name, err))
		}
		selector, err := publicAgentSelectorConfigFromJSON(target.AgentSelectorJson)
		if err != nil {
			return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("route target %q has invalid agent selector: %w", target.Name, err))
		}
		config := publicRouteTargetConfig{
			ID:                            target.ID,
			RouteID:                       target.RouteID,
			Name:                          target.Name,
			Position:                      target.Position,
			PriorityGroup:                 target.PriorityGroup,
			Weight:                        target.Weight,
			Enabled:                       target.Enabled != 0,
			TargetType:                    targetType,
			URL:                           target.Url,
			Transport:                     transport,
			AgentSelector:                 selector,
			AgentLoadBalancing:            normalizePublicRouteTargetLoadBalancing(target.AgentLoadBalancing),
			TLSSkipVerify:                 target.TlsSkipVerify != 0,
			UpstreamResponseHeaderTimeout: time.Duration(normalizePublicRouteTargetUpstreamResponseHeaderTimeoutMillis(target.UpstreamResponseHeaderTimeoutMillis)) * time.Millisecond,
			UpstreamRequestHeaders:        publicRouteTargetUpstreamHeadersToConfig(targetUpstreamHeadersByTarget[target.ID]),
			UpstreamBasicAuth: publicRouteTargetBasicAuthConfig{
				Enabled:  target.UpstreamBasicAuthEnabled != 0,
				Username: target.UpstreamBasicAuthUsername,
				Password: target.UpstreamBasicAuthPassword,
			},
			HealthCheck:              publicRouteTargetHealthCheckRowToConfig(target),
			StaticStatusCode:         int(target.StaticStatusCode),
			StaticResponseHeaders:    publicRouteTargetResponseHeadersToConfig(targetResponseHeadersByTarget[target.ID]),
			StaticResponseBody:       staticResponseBody,
			StaticResponseBodyMode:   normalizePublicResponseBodyMode(target.StaticResponseBodyMode),
			StaticResponseTemplateID: nullInt64Value(target.StaticResponseTemplateID),
			ParsedURL:                parsed,
		}
		snap.RouteTargets[target.ID] = config
		routeTargetsByRoute[target.RouteID] = append(routeTargetsByRoute[target.RouteID], config)
	}
	for _, listener := range rows.Listeners {
		snap.Listeners[listener.ID] = publicListenerConfig{
			ID:          listener.ID,
			Name:        listener.Name,
			BindAddress: listener.BindAddress,
			Port:        listener.Port,
			Protocol:    listener.Protocol,
			Enabled:     listener.Enabled != 0,
		}
	}
	for routeID, targets := range routeTargetsByRoute {
		sort.SliceStable(targets, func(i, j int) bool {
			if targets[i].PriorityGroup == targets[j].PriorityGroup {
				if targets[i].Position == targets[j].Position {
					return targets[i].ID < targets[j].ID
				}
				return targets[i].Position < targets[j].Position
			}
			return targets[i].PriorityGroup < targets[j].PriorityGroup
		})
		routeTargetsByRoute[routeID] = targets
	}
	for _, route := range rows.Routes {
		snap.RoutesByListener[route.ListenerID] = append(snap.RoutesByListener[route.ListenerID], publicRouteConfig{
			ID:                         route.ID,
			ListenerID:                 route.ListenerID,
			Priority:                   route.Priority,
			HostPattern:                normalizeHostPattern(route.HostPattern),
			PathPrefix:                 route.PathPrefix,
			TargetLoadBalancing:        normalizePublicRouteTargetLoadBalancing(route.TargetLoadBalancing),
			IsDefault:                  route.IsDefault != 0,
			Targets:                    routeTargetsByRoute[route.ID],
			Action:                     normalizePublicRouteAction(route.Action),
			RedirectTargetMode:         normalizePublicRouteRedirectTargetMode(route.RedirectTargetMode),
			RedirectTarget:             route.RedirectTarget,
			RedirectStatusCode:         route.RedirectStatusCode,
			RedirectPreservePathSuffix: route.RedirectPreservePathSuffix != 0,
			RedirectPreserveQuery:      route.RedirectPreserveQuery != 0,
			Enabled:                    route.Enabled != 0,
		})
	}
	for listenerID, routes := range snap.RoutesByListener {
		sortPublicRoutes(routes)
		snap.RoutesByListener[listenerID] = routes
	}
	for _, cert := range rows.TLSCertificates {
		snap.CertsByListener[cert.ListenerID] = append(snap.CertsByListener[cert.ListenerID], publicTLSCertificateConfig{
			ID:                cert.ID,
			ListenerID:        cert.ListenerID,
			HostnamePattern:   normalizeHostPattern(cert.HostnamePattern),
			CertPath:          cert.CertPath,
			KeyPath:           cert.KeyPath,
			Enabled:           cert.Enabled != 0,
			Source:            normalizePublicTLSCertificateSource(cert.Source),
			ACMEChallengeType: normalizePublicACMEChallengeType(cert.AcmeChallengeType),
			Status:            normalizePublicTLSCertificateStatus(cert.Status),
		})
	}
	for _, row := range rows.RateLimitRules {
		rule, err := publicRateLimitRuleRowToConfig(row)
		if err != nil {
			return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("rate limit rule %q is invalid: %w", row.Name, err))
		}
		responseBody, err := effectiveGenericResponseBody(row.ResponseBodyMode, row.ResponseBodyTemplateID, rule.ResponseBody, snap.ResponseTemplates)
		if err != nil {
			return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("rate limit rule %q has invalid response template: %w", row.Name, err))
		}
		rule.ResponseBody = responseBody
		rule.Fingerprint = publicRateLimitRuleFingerprint(rule)
		snap.RateLimitRules = append(snap.RateLimitRules, rule)
	}
	for _, row := range rows.TrafficShaperRules {
		rule, err := publicTrafficShaperRuleRowToConfig(row)
		if err != nil {
			return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("traffic shaper rule %q is invalid: %w", row.Name, err))
		}
		snap.TrafficShaperRules = append(snap.TrafficShaperRules, rule)
	}
	for _, row := range rows.WafCaptchaProviders {
		provider := publicWafCaptchaProviderRowToConfig(row, true)
		snap.WafCaptchaProviders[provider.ID] = provider
	}
	for _, row := range rows.WafRules {
		rule, err := publicWafRuleRowToConfig(row)
		if err != nil {
			return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("WAF rule %q is invalid: %w", row.Name, err))
		}
		blockBody, err := effectiveGenericResponseBody(row.BlockResponseBodyMode, row.BlockResponseTemplateID, rule.BlockResponseBody, snap.ResponseTemplates)
		if err != nil {
			return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("WAF rule %q has invalid block response template: %w", row.Name, err))
		}
		captchaTemplate, err := optionalWafPageTemplate(row.CaptchaPageTemplateID, publicResponseTemplateKindWafCaptchaPage, snap.ResponseTemplates)
		if err != nil {
			return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("WAF rule %q has invalid captcha page template: %w", row.Name, err))
		}
		waitingRoomTemplate, err := optionalWafPageTemplate(row.WaitingRoomPageTemplateID, publicResponseTemplateKindWafWaitingRoomPage, snap.ResponseTemplates)
		if err != nil {
			return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("WAF rule %q has invalid waiting-room page template: %w", row.Name, err))
		}
		rule.BlockResponseBody = blockBody
		rule.CaptchaPageTemplateBody = captchaTemplate
		rule.WaitingRoomPageTemplateBody = waitingRoomTemplate
		rule.Fingerprint = publicWafRuleFingerprint(rule)
		snap.WafRules = append(snap.WafRules, rule)
	}
	for _, row := range rows.CacheRules {
		rule, err := publicCacheRuleRowToConfig(row)
		if err != nil {
			return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("cache rule %q is invalid: %w", row.Name, err))
		}
		snap.CacheRules = append(snap.CacheRules, rule)
	}
	return snap, nil
}

func (a *App) reconcilePublicListenerAfterMutation(ctx context.Context, listenerID int64) (*p2pstreamv1.PublicListenerStatus, error) {
	if err := a.refreshPublicProxySnapshot(ctx); err != nil {
		return nil, err
	}
	a.proxyMu.Lock()
	snap := a.publicSnapshot
	listener, ok := snap.Listeners[listenerID]
	serviceActive := a.proxyServiceActive
	runtime := a.ensureListenerStateLocked(listenerID)
	isRunning := runtime.Server != nil
	a.proxyMu.Unlock()
	if !ok {
		return nil, connect.NewError(connect.CodeNotFound, errors.New("listener not found"))
	}
	if !listener.Enabled {
		return a.stopPublicListenerRuntime(ctx, listenerID)
	}
	if serviceActive || isRunning {
		return a.restartPublicListenerRuntime(ctx, listenerID)
	}
	return a.getPublicListenerStatus(listenerID), nil
}

func (a *App) restartTLSListenerIfActive(ctx context.Context, listenerID int64) (*p2pstreamv1.PublicListenerStatus, error) {
	a.proxyMu.Lock()
	runtime := a.publicListenerState[listenerID]
	running := runtime != nil && runtime.Server != nil
	a.proxyMu.Unlock()
	if !running {
		return a.getPublicListenerStatus(listenerID), nil
	}
	return a.restartPublicListenerRuntime(ctx, listenerID)
}

func validatePublicRouteTargetHealthCheck(input *p2pstreamv1.PublicRouteTargetHealthCheck) (publicRouteTargetHealthCheckConfig, error) {
	cfg := publicRouteTargetHealthCheckConfig{
		Enabled:            false,
		Method:             defaultTargetHealthCheckMethod,
		Path:               defaultTargetHealthCheckPath,
		Interval:           time.Duration(defaultTargetHealthCheckIntervalMillis) * time.Millisecond,
		Timeout:            time.Duration(defaultTargetHealthCheckTimeoutMillis) * time.Millisecond,
		HealthyThreshold:   defaultTargetHealthCheckHealthyThreshold,
		UnhealthyThreshold: defaultTargetHealthCheckUnhealthyThreshold,
		ExpectedStatusMin:  defaultTargetHealthCheckExpectedStatusMin,
		ExpectedStatusMax:  defaultTargetHealthCheckExpectedStatusMax,
	}
	if input == nil {
		return cfg, nil
	}
	cfg.Enabled = input.Enabled
	method := strings.ToUpper(strings.TrimSpace(input.Method))
	if method == "" {
		method = defaultTargetHealthCheckMethod
	}
	if method != http.MethodGet && method != http.MethodHead {
		return publicRouteTargetHealthCheckConfig{}, connect.NewError(connect.CodeInvalidArgument, errors.New("health check method must be GET or HEAD"))
	}
	path := strings.TrimSpace(input.Path)
	if path == "" {
		path = defaultTargetHealthCheckPath
	}
	if !strings.HasPrefix(path, "/") {
		return publicRouteTargetHealthCheckConfig{}, connect.NewError(connect.CodeInvalidArgument, errors.New("health check path must start with /"))
	}
	intervalMillis := input.IntervalMillis
	if intervalMillis == 0 {
		intervalMillis = defaultTargetHealthCheckIntervalMillis
	}
	if intervalMillis < 1000 || intervalMillis > 3600000 {
		return publicRouteTargetHealthCheckConfig{}, connect.NewError(connect.CodeInvalidArgument, errors.New("health check interval must be between 1000 and 3600000 milliseconds"))
	}
	timeoutMillis := input.TimeoutMillis
	if timeoutMillis == 0 {
		timeoutMillis = defaultTargetHealthCheckTimeoutMillis
	}
	if timeoutMillis < 100 || timeoutMillis > 30000 {
		return publicRouteTargetHealthCheckConfig{}, connect.NewError(connect.CodeInvalidArgument, errors.New("health check timeout must be between 100 and 30000 milliseconds"))
	}
	if timeoutMillis > intervalMillis {
		return publicRouteTargetHealthCheckConfig{}, connect.NewError(connect.CodeInvalidArgument, errors.New("health check timeout must not exceed interval"))
	}
	healthyThreshold := input.HealthyThreshold
	if healthyThreshold == 0 {
		healthyThreshold = defaultTargetHealthCheckHealthyThreshold
	}
	unhealthyThreshold := input.UnhealthyThreshold
	if unhealthyThreshold == 0 {
		unhealthyThreshold = defaultTargetHealthCheckUnhealthyThreshold
	}
	if healthyThreshold < 1 || healthyThreshold > 10 || unhealthyThreshold < 1 || unhealthyThreshold > 10 {
		return publicRouteTargetHealthCheckConfig{}, connect.NewError(connect.CodeInvalidArgument, errors.New("health check thresholds must be between 1 and 10"))
	}
	statusMin := input.ExpectedStatusMin
	if statusMin == 0 {
		statusMin = defaultTargetHealthCheckExpectedStatusMin
	}
	statusMax := input.ExpectedStatusMax
	if statusMax == 0 {
		statusMax = defaultTargetHealthCheckExpectedStatusMax
	}
	if statusMin < 100 || statusMax > 599 || statusMin > statusMax {
		return publicRouteTargetHealthCheckConfig{}, connect.NewError(connect.CodeInvalidArgument, errors.New("health check expected status range must be between 100 and 599"))
	}
	cfg.Method = method
	cfg.Path = path
	cfg.Interval = time.Duration(intervalMillis) * time.Millisecond
	cfg.Timeout = time.Duration(timeoutMillis) * time.Millisecond
	cfg.HealthyThreshold = healthyThreshold
	cfg.UnhealthyThreshold = unhealthyThreshold
	cfg.ExpectedStatusMin = statusMin
	cfg.ExpectedStatusMax = statusMax
	return cfg, nil
}

func validatePublicRouteTargetUpstreamResponseHeaderTimeout(timeoutMillis int64) (int64, error) {
	if timeoutMillis == 0 {
		return defaultTargetUpstreamResponseHeaderTimeoutMillis, nil
	}
	if timeoutMillis < minTargetUpstreamResponseHeaderTimeoutMillis || timeoutMillis > maxTargetUpstreamResponseHeaderTimeoutMillis {
		return 0, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf(
			"upstream response header timeout must be between %d and %d milliseconds",
			minTargetUpstreamResponseHeaderTimeoutMillis,
			maxTargetUpstreamResponseHeaderTimeoutMillis,
		))
	}
	return timeoutMillis, nil
}

func normalizePublicRouteTargetUpstreamResponseHeaderTimeoutMillis(timeoutMillis int64) int64 {
	if timeoutMillis < minTargetUpstreamResponseHeaderTimeoutMillis || timeoutMillis > maxTargetUpstreamResponseHeaderTimeoutMillis {
		return defaultTargetUpstreamResponseHeaderTimeoutMillis
	}
	return timeoutMillis
}

func (a *App) validatePublicRouteTargets(ctx context.Context, targets []*p2pstreamv1.PublicRouteTarget) ([]publicRouteTargetMutationInput, error) {
	if len(targets) == 0 {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("forwarding route requires at least one target"))
	}
	resp := make([]publicRouteTargetMutationInput, 0, len(targets))
	enabledTargets := 0
	for idx, target := range targets {
		if target == nil {
			continue
		}
		targetType, err := publicRouteTargetTypeStringFromProto(target.TargetType)
		if err != nil {
			return nil, err
		}
		transport, err := publicRouteTargetTransportStringFromProto(target.Transport)
		if err != nil {
			return nil, err
		}
		agentLoadBalancing, err := loadBalancingStringFromProto(target.AgentLoadBalancing)
		if err != nil {
			return nil, err
		}
		weight := target.Weight
		if weight == 0 {
			weight = 100
		}
		if weight < 1 || weight > 1_000_000 {
			return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("route target weight must be between 1 and 1000000"))
		}
		name := strings.TrimSpace(target.Name)
		if name == "" {
			name = fmt.Sprintf("target-%d", idx+1)
		}
		if len(name) > 128 || strings.ContainsAny(name, "\r\n") {
			return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("route target name must be at most 128 characters without CR or LF"))
		}

		params := db.CreatePublicRouteTargetParams{
			Name:                                name,
			Position:                            int64(idx),
			PriorityGroup:                       target.PriorityGroup,
			Weight:                              weight,
			Enabled:                             boolInt(target.Enabled),
			TargetType:                          targetType,
			Transport:                           transport,
			AgentLoadBalancing:                  agentLoadBalancing,
			TlsSkipVerify:                       boolInt(target.TlsSkipVerify),
			UpstreamResponseHeaderTimeoutMillis: defaultTargetUpstreamResponseHeaderTimeoutMillis,
			HealthCheckMethod:                   defaultTargetHealthCheckMethod,
			HealthCheckPath:                     defaultTargetHealthCheckPath,
			HealthCheckIntervalMillis:           defaultTargetHealthCheckIntervalMillis,
			HealthCheckTimeoutMillis:            defaultTargetHealthCheckTimeoutMillis,
			HealthCheckHealthyThreshold:         defaultTargetHealthCheckHealthyThreshold,
			HealthCheckUnhealthyThreshold:       defaultTargetHealthCheckUnhealthyThreshold,
			HealthCheckExpectedStatusMin:        defaultTargetHealthCheckExpectedStatusMin,
			HealthCheckExpectedStatusMax:        defaultTargetHealthCheckExpectedStatusMax,
			StaticStatusCode:                    defaultStaticStatusCode,
			StaticResponseBodyMode:              publicResponseBodyModeInline,
		}
		var upstreamHeaders []publicRouteTargetUpstreamHeaderInput
		var responseHeaders []publicRouteTargetResponseHeaderInput
		if targetType == publicRouteTargetTypeProxy {
			targetURL := strings.TrimSpace(target.Url)
			parsed, err := parsePublicTargetOrigin(targetURL)
			if err != nil {
				return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("proxy route target URL must be an http or https origin"))
			}
			params.Url = strings.TrimRight(parsed.String(), "/")
			if transport == publicRouteTargetTransportAgent {
				selectorJSON, err := validatePublicAgentSelector(target.AgentSelector)
				if err != nil {
					return nil, err
				}
				params.AgentSelectorJson = selectorJSON
			} else {
				params.AgentSelectorJson = "{}"
			}
			upstreamHeaders, err = validatePublicRouteTargetUpstreamHeaders(target.UpstreamRequestHeaders, target.UpstreamBasicAuth != nil && target.UpstreamBasicAuth.Enabled)
			if err != nil {
				return nil, err
			}
			authEnabled, authUsername, authPassword, err := validatePublicRouteTargetBasicAuth(target.UpstreamBasicAuth)
			if err != nil {
				return nil, err
			}
			params.UpstreamBasicAuthEnabled = authEnabled
			params.UpstreamBasicAuthUsername = authUsername
			params.UpstreamBasicAuthPassword = authPassword
			health, err := validatePublicRouteTargetHealthCheck(target.HealthCheck)
			if err != nil {
				return nil, err
			}
			params.HealthCheckEnabled = boolInt(health.Enabled)
			params.HealthCheckMethod = health.Method
			params.HealthCheckPath = health.Path
			params.HealthCheckIntervalMillis = health.Interval.Milliseconds()
			params.HealthCheckTimeoutMillis = health.Timeout.Milliseconds()
			params.HealthCheckHealthyThreshold = health.HealthyThreshold
			params.HealthCheckUnhealthyThreshold = health.UnhealthyThreshold
			params.HealthCheckExpectedStatusMin = health.ExpectedStatusMin
			params.HealthCheckExpectedStatusMax = health.ExpectedStatusMax
			timeoutMillis, err := validatePublicRouteTargetUpstreamResponseHeaderTimeout(target.UpstreamResponseHeaderTimeoutMillis)
			if err != nil {
				return nil, err
			}
			params.UpstreamResponseHeaderTimeoutMillis = timeoutMillis
		} else {
			params.Transport = publicRouteTargetTransportDirect
			params.AgentSelectorJson = "{}"
			status := target.StaticStatusCode
			if status == 0 {
				status = defaultStaticStatusCode
			}
			if status < 100 || status > 599 {
				return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("static target status code must be between 100 and 599"))
			}
			bodyMode, templateRef, err := a.validateGenericResponseTemplateReference(ctx, target.StaticResponseBodyMode, target.StaticResponseTemplateId)
			if err != nil {
				return nil, err
			}
			responseHeaders, err = validatePublicStaticHeaders(target.StaticResponseHeaders)
			if err != nil {
				return nil, err
			}
			params.StaticStatusCode = status
			params.StaticResponseBody = target.StaticResponseBody
			params.StaticResponseBodyMode = bodyMode
			params.StaticResponseTemplateID = templateRef
		}
		if target.Enabled {
			enabledTargets++
		}
		resp = append(resp, publicRouteTargetMutationInput{
			Params:          params,
			UpstreamHeaders: upstreamHeaders,
			ResponseHeaders: responseHeaders,
		})
	}
	if len(resp) == 0 || enabledTargets == 0 {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("forwarding route requires at least one enabled target"))
	}
	return resp, nil
}

func validatePublicAgentSelector(selector *p2pstreamv1.PublicAgentSelector) (string, error) {
	if selector == nil || len(selector.MatchLabels) == 0 {
		return "", connect.NewError(connect.CodeInvalidArgument, errors.New("agent route target requires at least one selector label"))
	}
	matchLabels := make(map[string]string, len(selector.MatchLabels))
	for key, value := range selector.MatchLabels {
		key, value, err := validateAgentLabel(key, value)
		if err != nil {
			return "", err
		}
		matchLabels[key] = value
	}
	payload, err := json.Marshal(map[string]map[string]string{"match_labels": matchLabels})
	if err != nil {
		return "", connect.NewError(connect.CodeInternal, err)
	}
	return string(payload), nil
}

func (a *App) validatePublicListenerInput(
	name string,
	bindAddress string,
	port int64,
	protocol p2pstreamv1.PublicListenerProtocol,
	enabled bool,
	allowPortZero bool,
) (db.UpdatePublicListenerParams, error) {
	name, err := normalizePublicName(name)
	if err != nil {
		return db.UpdatePublicListenerParams{}, err
	}
	bindAddress, err = normalizeBindAddress(bindAddress)
	if err != nil {
		return db.UpdatePublicListenerParams{}, err
	}
	if port < 1 || port > 65535 {
		if !(allowPortZero && port == 0) {
			return db.UpdatePublicListenerParams{}, connect.NewError(connect.CodeInvalidArgument, errors.New("port must be between 1 and 65535"))
		}
	}
	protocolString, err := protocolStringFromProto(protocol)
	if err != nil {
		return db.UpdatePublicListenerParams{}, err
	}
	return db.UpdatePublicListenerParams{
		Name:        name,
		BindAddress: bindAddress,
		Port:        port,
		Protocol:    protocolString,
		Enabled:     boolInt(enabled),
	}, nil
}

func (a *App) validatePublicRouteInput(
	ctx context.Context,
	listenerID int64,
	priority int64,
	hostPattern string,
	pathPrefix string,
	targetLoadBalancing p2pstreamv1.PublicRouteTargetLoadBalancing,
	isDefault bool,
	targets []*p2pstreamv1.PublicRouteTarget,
	enabled bool,
	action p2pstreamv1.PublicRouteAction,
	redirectTargetMode p2pstreamv1.PublicRouteRedirectTargetMode,
	redirectTarget string,
	redirectStatusCode int64,
	redirectPreservePathSuffix bool,
	redirectPreserveQuery bool,
) (db.UpdatePublicRouteParams, []publicRouteTargetMutationInput, error) {
	if _, err := a.DB.GetPublicListener(ctx, listenerID); err != nil {
		return db.UpdatePublicRouteParams{}, nil, publicDBError(err)
	}
	hostPattern = normalizeHostPattern(hostPattern)
	pathPrefix = strings.TrimSpace(pathPrefix)
	if !isDefault && hostPattern == "" && pathPrefix == "" {
		return db.UpdatePublicRouteParams{}, nil, connect.NewError(connect.CodeInvalidArgument, errors.New("route requires a host pattern or path prefix"))
	}
	if hostPattern != "" {
		if err := validateHostPattern(hostPattern); err != nil {
			return db.UpdatePublicRouteParams{}, nil, err
		}
	}
	if pathPrefix != "" && !strings.HasPrefix(pathPrefix, "/") {
		return db.UpdatePublicRouteParams{}, nil, connect.NewError(connect.CodeInvalidArgument, errors.New("path prefix must start with /"))
	}
	actionString, err := routeActionStringFromProto(action)
	if err != nil {
		return db.UpdatePublicRouteParams{}, nil, err
	}
	targetLoadBalancingString := publicRouteTargetLoadBalancingRoundRobin
	var routeTargets []publicRouteTargetMutationInput
	redirectMode := ""
	redirectTarget = strings.TrimSpace(redirectTarget)
	if redirectStatusCode == 0 {
		redirectStatusCode = defaultRedirectStatusCode
	}
	if actionString == publicRouteActionForward {
		targetLoadBalancingString, err = loadBalancingStringFromProto(targetLoadBalancing)
		if err != nil {
			return db.UpdatePublicRouteParams{}, nil, err
		}
		routeTargets, err = a.validatePublicRouteTargets(ctx, targets)
		if err != nil {
			return db.UpdatePublicRouteParams{}, nil, err
		}
		redirectStatusCode = defaultRedirectStatusCode
		redirectPreservePathSuffix = true
		redirectPreserveQuery = true
	} else {
		redirectMode, err = routeRedirectTargetModeStringFromProto(redirectTargetMode)
		if err != nil {
			return db.UpdatePublicRouteParams{}, nil, err
		}
		if err := validateRouteRedirectTarget(redirectMode, redirectTarget); err != nil {
			return db.UpdatePublicRouteParams{}, nil, err
		}
		if !validRedirectStatusCode(redirectStatusCode) {
			return db.UpdatePublicRouteParams{}, nil, connect.NewError(connect.CodeInvalidArgument, errors.New("redirect status must be 301, 302, 307, or 308"))
		}
		if redirectMode == publicRouteRedirectTargetModeExternalOriginKeepPath {
			redirectPreservePathSuffix = false
		}
	}
	return db.UpdatePublicRouteParams{
		ListenerID:                 listenerID,
		Priority:                   priority,
		HostPattern:                hostPattern,
		PathPrefix:                 pathPrefix,
		TargetLoadBalancing:        targetLoadBalancingString,
		IsDefault:                  boolInt(isDefault),
		Action:                     actionString,
		RedirectTargetMode:         redirectMode,
		RedirectTarget:             redirectTarget,
		RedirectStatusCode:         redirectStatusCode,
		RedirectPreservePathSuffix: boolInt(redirectPreservePathSuffix),
		RedirectPreserveQuery:      boolInt(redirectPreserveQuery),
		Enabled:                    boolInt(enabled),
	}, routeTargets, nil
}

func (a *App) validatePublicTLSCertificateInput(
	ctx context.Context,
	listenerID int64,
	hostnamePattern string,
	certPath string,
	keyPath string,
	certPEM []byte,
	keyPEM []byte,
	enabled bool,
	sourceProto p2pstreamv1.PublicTlsCertificateSource,
	challengeProto p2pstreamv1.PublicAcmeChallengeType,
	caProto p2pstreamv1.PublicAcmeCa,
	acmeEmail string,
	dnsCredentialID int64,
	generateSelfSigned bool,
	selfSignedValidityDays int64,
	existing *db.PublicTlsCertificate,
	allowMissingMaterial bool,
) (publicTLSCertificateMutationInput, publicTLSCertificateMaterial, error) {
	listener, err := a.DB.GetPublicListener(ctx, listenerID)
	if err != nil {
		return publicTLSCertificateMutationInput{}, publicTLSCertificateMaterial{}, publicDBError(err)
	}
	if listener.Protocol != publicListenerProtocolHTTPS {
		return publicTLSCertificateMutationInput{}, publicTLSCertificateMaterial{}, connect.NewError(connect.CodeFailedPrecondition, errors.New("TLS certificates can only be configured on HTTPS listeners"))
	}
	hostnamePattern = normalizeHostPattern(hostnamePattern)
	if err := validateHostPattern(hostnamePattern); err != nil {
		return publicTLSCertificateMutationInput{}, publicTLSCertificateMaterial{}, err
	}

	source, err := tlsCertificateSourceStringFromProto(sourceProto)
	if err != nil {
		return publicTLSCertificateMutationInput{}, publicTLSCertificateMaterial{}, err
	}
	if sourceProto == p2pstreamv1.PublicTlsCertificateSource_PUBLIC_TLS_CERTIFICATE_SOURCE_UNSPECIFIED && existing != nil {
		source = normalizePublicTLSCertificateSource(existing.Source)
	}

	certPath = strings.TrimSpace(certPath)
	keyPath = strings.TrimSpace(keyPath)
	hasCertUpload := len(certPEM) > 0
	hasKeyUpload := len(keyPEM) > 0

	if source == publicTLSCertificateSourceACME {
		if generateSelfSigned {
			return publicTLSCertificateMutationInput{}, publicTLSCertificateMaterial{}, connect.NewError(connect.CodeInvalidArgument, errors.New("self-signed generation is only available for manual certificates"))
		}
		if hasCertUpload || hasKeyUpload || certPath != "" || keyPath != "" {
			return publicTLSCertificateMutationInput{}, publicTLSCertificateMaterial{}, connect.NewError(connect.CodeInvalidArgument, errors.New("ACME certificates manage certificate material automatically"))
		}
		challengeType, err := acmeChallengeTypeStringFromProto(challengeProto)
		if err != nil {
			return publicTLSCertificateMutationInput{}, publicTLSCertificateMaterial{}, err
		}
		ca, err := acmeCAStringFromProto(caProto)
		if err != nil {
			return publicTLSCertificateMutationInput{}, publicTLSCertificateMaterial{}, err
		}
		acmeEmail = strings.TrimSpace(strings.ToLower(acmeEmail))
		if _, err := mail.ParseAddress(acmeEmail); err != nil {
			return publicTLSCertificateMutationInput{}, publicTLSCertificateMaterial{}, connect.NewError(connect.CodeInvalidArgument, errors.New("ACME email must be a valid email address"))
		}
		if err := validateACMEHostPattern(hostnamePattern, challengeType); err != nil {
			return publicTLSCertificateMutationInput{}, publicTLSCertificateMaterial{}, err
		}
		var credentialID sql.NullInt64
		if challengeType == publicACMEChallengeDNS01 {
			if dnsCredentialID <= 0 {
				return publicTLSCertificateMutationInput{}, publicTLSCertificateMaterial{}, connect.NewError(connect.CodeInvalidArgument, errors.New("DNS-01 requires a DNS credential"))
			}
			credential, err := a.DB.GetPublicTlsDnsCredential(ctx, dnsCredentialID)
			if err != nil {
				return publicTLSCertificateMutationInput{}, publicTLSCertificateMaterial{}, publicDBError(err)
			}
			if credential.Enabled == 0 {
				return publicTLSCertificateMutationInput{}, publicTLSCertificateMaterial{}, connect.NewError(connect.CodeFailedPrecondition, errors.New("DNS-01 requires an enabled DNS credential"))
			}
			if normalizePublicDNSProvider(credential.Provider) != publicDNSProviderCloudflare {
				return publicTLSCertificateMutationInput{}, publicTLSCertificateMaterial{}, connect.NewError(connect.CodeInvalidArgument, errors.New("DNS-01 currently supports Cloudflare credentials only"))
			}
			credentialID = sql.NullInt64{Int64: dnsCredentialID, Valid: true}
		}
		status := publicTLSCertificateStatusPending
		if existing != nil && existing.CertPath != "" && existing.KeyPath != "" && normalizePublicTLSCertificateStatus(existing.Status) == publicTLSCertificateStatusReady {
			status = publicTLSCertificateStatusReady
		}
		return publicTLSCertificateMutationInput{
			ListenerID:           listenerID,
			HostnamePattern:      hostnamePattern,
			Enabled:              boolInt(enabled),
			Source:               publicTLSCertificateSourceACME,
			ACMEChallengeType:    challengeType,
			ACMECA:               ca,
			ACMEEmail:            acmeEmail,
			DNSCredentialID:      credentialID,
			Status:               status,
			LastError:            "",
			IssuedAt:             nullTimeFromExisting(existing, "issued_at"),
			ExpiresAt:            nullTimeFromExisting(existing, "expires_at"),
			NextRenewalAt:        nullTimeFromExisting(existing, "next_renewal_at"),
			LastRenewalAttemptAt: nullTimeFromExisting(existing, "last_renewal_attempt_at"),
		}, publicTLSCertificateMaterial{}, nil
	}

	if generateSelfSigned {
		if hasCertUpload || hasKeyUpload || certPath != "" || keyPath != "" {
			return publicTLSCertificateMutationInput{}, publicTLSCertificateMaterial{}, connect.NewError(connect.CodeInvalidArgument, errors.New("self-signed generation cannot be combined with certificate uploads or file paths"))
		}
		validityDays, err := validatePublicSelfSignedValidityDays(selfSignedValidityDays)
		if err != nil {
			return publicTLSCertificateMutationInput{}, publicTLSCertificateMaterial{}, err
		}
		certPEM, keyPEM, leaf, err := generatePublicSelfSignedCertificatePEM(hostnamePattern, time.Duration(validityDays)*24*time.Hour)
		if err != nil {
			return publicTLSCertificateMutationInput{}, publicTLSCertificateMaterial{}, connect.NewError(connect.CodeInternal, err)
		}
		return publicTLSCertificateMutationInput{
			ListenerID:      listenerID,
			HostnamePattern: hostnamePattern,
			Enabled:         boolInt(enabled),
			Source:          publicTLSCertificateSourceManual,
			Status:          publicTLSCertificateStatusReady,
			IssuedAt:        sql.NullTime{Time: leaf.NotBefore.UTC(), Valid: true},
			ExpiresAt:       sql.NullTime{Time: leaf.NotAfter.UTC(), Valid: true},
		}, publicTLSCertificateMaterial{Replace: true, CertPEM: certPEM, KeyPEM: keyPEM}, nil
	}
	if selfSignedValidityDays != 0 {
		return publicTLSCertificateMutationInput{}, publicTLSCertificateMaterial{}, connect.NewError(connect.CodeInvalidArgument, errors.New("self-signed validity days require self-signed generation"))
	}

	if hasCertUpload || hasKeyUpload {
		if !hasCertUpload || !hasKeyUpload {
			return publicTLSCertificateMutationInput{}, publicTLSCertificateMaterial{}, connect.NewError(connect.CodeInvalidArgument, errors.New("certificate and private key uploads are both required"))
		}
		if _, err := tls.X509KeyPair(certPEM, keyPEM); err != nil {
			return publicTLSCertificateMutationInput{}, publicTLSCertificateMaterial{}, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("certificate and private key must be a valid PEM pair: %w", err))
		}
		issuedAt, expiresAt, err := publicTLSCertificateValidityFromPEM(certPEM)
		if err != nil {
			return publicTLSCertificateMutationInput{}, publicTLSCertificateMaterial{}, connect.NewError(connect.CodeInvalidArgument, err)
		}
		return publicTLSCertificateMutationInput{
			ListenerID:      listenerID,
			HostnamePattern: hostnamePattern,
			Enabled:         boolInt(enabled),
			Source:          publicTLSCertificateSourceManual,
			Status:          publicTLSCertificateStatusReady,
			IssuedAt:        issuedAt,
			ExpiresAt:       expiresAt,
		}, publicTLSCertificateMaterial{Replace: true, CertPEM: certPEM, KeyPEM: keyPEM}, nil
	}

	if certPath == "" && keyPath == "" && allowMissingMaterial {
		if existing == nil || normalizePublicTLSCertificateSource(existing.Source) != publicTLSCertificateSourceManual {
			return publicTLSCertificateMutationInput{}, publicTLSCertificateMaterial{}, connect.NewError(connect.CodeInvalidArgument, errors.New("manual certificates require uploaded files, server file paths, or self-signed generation"))
		}
		return publicTLSCertificateMutationInput{
			ListenerID:           listenerID,
			HostnamePattern:      hostnamePattern,
			Enabled:              boolInt(enabled),
			Source:               publicTLSCertificateSourceManual,
			Status:               publicTLSCertificateStatusReady,
			IssuedAt:             nullTimeFromExisting(existing, "issued_at"),
			ExpiresAt:            nullTimeFromExisting(existing, "expires_at"),
			NextRenewalAt:        nullTimeFromExisting(existing, "next_renewal_at"),
			LastRenewalAttemptAt: nullTimeFromExisting(existing, "last_renewal_attempt_at"),
		}, publicTLSCertificateMaterial{}, nil
	}
	if certPath == "" || keyPath == "" {
		return publicTLSCertificateMutationInput{}, publicTLSCertificateMaterial{}, connect.NewError(connect.CodeInvalidArgument, errors.New("certificate and key paths are required"))
	}
	issuedAt, expiresAt := publicTLSCertificateValidityFromFile(certPath)
	return publicTLSCertificateMutationInput{
		ListenerID:      listenerID,
		HostnamePattern: hostnamePattern,
		CertPath:        certPath,
		KeyPath:         keyPath,
		Enabled:         boolInt(enabled),
		Source:          publicTLSCertificateSourceManual,
		Status:          publicTLSCertificateStatusReady,
		IssuedAt:        issuedAt,
		ExpiresAt:       expiresAt,
	}, publicTLSCertificateMaterial{}, nil
}

func (a *App) validatePublicTLSDNSCredentialInput(
	name string,
	providerProto p2pstreamv1.PublicDnsProvider,
	cloudflareZoneID string,
	apiToken string,
	enabled bool,
	existing *db.PublicTlsDnsCredential,
	replaceToken bool,
) (db.UpdatePublicTlsDnsCredentialParams, error) {
	name, err := normalizePublicName(name)
	if err != nil {
		return db.UpdatePublicTlsDnsCredentialParams{}, err
	}
	provider, err := dnsProviderStringFromProto(providerProto)
	if err != nil {
		return db.UpdatePublicTlsDnsCredentialParams{}, err
	}
	cloudflareZoneID = strings.TrimSpace(cloudflareZoneID)
	if cloudflareZoneID == "" || strings.ContainsAny(cloudflareZoneID, " /\r\n\t") {
		return db.UpdatePublicTlsDnsCredentialParams{}, connect.NewError(connect.CodeInvalidArgument, errors.New("Cloudflare zone ID is required"))
	}
	apiToken = strings.TrimSpace(apiToken)
	if replaceToken {
		if apiToken == "" {
			return db.UpdatePublicTlsDnsCredentialParams{}, connect.NewError(connect.CodeInvalidArgument, errors.New("Cloudflare API token is required"))
		}
		if strings.ContainsAny(apiToken, "\r\n") || !utf8.ValidString(apiToken) {
			return db.UpdatePublicTlsDnsCredentialParams{}, connect.NewError(connect.CodeInvalidArgument, errors.New("Cloudflare API token must be valid UTF-8 without CR or LF"))
		}
	} else if existing != nil {
		apiToken = existing.ApiToken
	} else {
		return db.UpdatePublicTlsDnsCredentialParams{}, connect.NewError(connect.CodeInvalidArgument, errors.New("Cloudflare API token is required"))
	}
	return db.UpdatePublicTlsDnsCredentialParams{
		Name:             name,
		Provider:         provider,
		CloudflareZoneID: cloudflareZoneID,
		ApiToken:         apiToken,
		Enabled:          boolInt(enabled),
	}, nil
}

func publicTLSCertificateCreateParams(input publicTLSCertificateMutationInput) db.CreatePublicTlsCertificateParams {
	return db.CreatePublicTlsCertificateParams{
		ListenerID:           input.ListenerID,
		HostnamePattern:      input.HostnamePattern,
		CertPath:             input.CertPath,
		KeyPath:              input.KeyPath,
		Enabled:              input.Enabled,
		Source:               input.Source,
		AcmeChallengeType:    input.ACMEChallengeType,
		AcmeCa:               input.ACMECA,
		AcmeEmail:            input.ACMEEmail,
		DnsCredentialID:      input.DNSCredentialID,
		Status:               input.Status,
		LastError:            input.LastError,
		IssuedAt:             input.IssuedAt,
		ExpiresAt:            input.ExpiresAt,
		NextRenewalAt:        input.NextRenewalAt,
		LastRenewalAttemptAt: input.LastRenewalAttemptAt,
	}
}

func publicTLSCertificateUpdateParams(input publicTLSCertificateMutationInput) db.UpdatePublicTlsCertificateParams {
	return db.UpdatePublicTlsCertificateParams{
		ID:                   input.ID,
		ListenerID:           input.ListenerID,
		HostnamePattern:      input.HostnamePattern,
		CertPath:             input.CertPath,
		KeyPath:              input.KeyPath,
		Enabled:              input.Enabled,
		Source:               input.Source,
		AcmeChallengeType:    input.ACMEChallengeType,
		AcmeCa:               input.ACMECA,
		AcmeEmail:            input.ACMEEmail,
		DnsCredentialID:      input.DNSCredentialID,
		Status:               input.Status,
		LastError:            input.LastError,
		IssuedAt:             input.IssuedAt,
		ExpiresAt:            input.ExpiresAt,
		NextRenewalAt:        input.NextRenewalAt,
		LastRenewalAttemptAt: input.LastRenewalAttemptAt,
	}
}

func nullTimeFromExisting(existing *db.PublicTlsCertificate, field string) sql.NullTime {
	if existing == nil {
		return sql.NullTime{}
	}
	switch field {
	case "issued_at":
		return existing.IssuedAt
	case "expires_at":
		return existing.ExpiresAt
	case "next_renewal_at":
		return existing.NextRenewalAt
	case "last_renewal_attempt_at":
		return existing.LastRenewalAttemptAt
	default:
		return sql.NullTime{}
	}
}

func validatePublicSelfSignedValidityDays(days int64) (int64, error) {
	if days < minPublicSelfSignedValidityDays || days > maxPublicSelfSignedValidityDays {
		return 0, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("self-signed certificate validity must be between %d and %d days", minPublicSelfSignedValidityDays, maxPublicSelfSignedValidityDays))
	}
	return days, nil
}

func publicTLSCertificateInputWithPEMValidity(input publicTLSCertificateMutationInput, certPEM []byte) publicTLSCertificateMutationInput {
	if input.IssuedAt.Valid && input.ExpiresAt.Valid {
		return input
	}
	issuedAt, expiresAt, err := publicTLSCertificateValidityFromPEM(certPEM)
	if err != nil {
		return input
	}
	if !input.IssuedAt.Valid {
		input.IssuedAt = issuedAt
	}
	if !input.ExpiresAt.Valid {
		input.ExpiresAt = expiresAt
	}
	return input
}

func publicTLSCertificateValidityFromPEM(certPEM []byte) (sql.NullTime, sql.NullTime, error) {
	leaf, err := parseLeafCertificate(certPEM)
	if err != nil {
		return sql.NullTime{}, sql.NullTime{}, fmt.Errorf("certificate PEM must contain a valid leaf certificate: %w", err)
	}
	return sql.NullTime{Time: leaf.NotBefore.UTC(), Valid: true}, sql.NullTime{Time: leaf.NotAfter.UTC(), Valid: true}, nil
}

func publicTLSCertificateValidityFromFile(certPath string) (sql.NullTime, sql.NullTime) {
	certPEM, err := os.ReadFile(strings.TrimSpace(certPath))
	if err != nil {
		return sql.NullTime{}, sql.NullTime{}
	}
	issuedAt, expiresAt, err := publicTLSCertificateValidityFromPEM(certPEM)
	if err != nil {
		return sql.NullTime{}, sql.NullTime{}
	}
	return issuedAt, expiresAt
}

func validateACMEHostPattern(pattern string, challengeType string) error {
	if pattern == defaultSelfSignedTLSHost || pattern == "localhost" {
		return connect.NewError(connect.CodeInvalidArgument, errors.New("ACME certificates require a public DNS hostname"))
	}
	host := strings.TrimPrefix(pattern, "*.")
	if net.ParseIP(host) != nil {
		return connect.NewError(connect.CodeInvalidArgument, errors.New("ACME certificates require a DNS hostname, not an IP address"))
	}
	if !strings.Contains(host, ".") {
		return connect.NewError(connect.CodeInvalidArgument, errors.New("ACME certificates require a fully-qualified DNS hostname"))
	}
	if strings.HasPrefix(pattern, "*.") && challengeType != publicACMEChallengeDNS01 {
		return connect.NewError(connect.CodeInvalidArgument, errors.New("wildcard ACME certificates require DNS-01"))
	}
	return nil
}

func (a *App) queuePublicACMECertificateIssue(cert db.PublicTlsCertificate) {
	if normalizePublicTLSCertificateSource(cert.Source) != publicTLSCertificateSourceACME || cert.Enabled == 0 || a.PublicACME == nil {
		return
	}
	a.PublicACME.QueueIssue(cert.ID)
}

func normalizePublicName(name string) (string, error) {
	name = strings.TrimSpace(name)
	if !publicNamePattern.MatchString(name) {
		return "", connect.NewError(connect.CodeInvalidArgument, errors.New("name must be 1-64 letters, numbers, dots, underscores, or hyphens and start with a letter or number"))
	}
	return name, nil
}

func normalizeBindAddress(bindAddress string) (string, error) {
	bindAddress = strings.TrimSpace(bindAddress)
	if bindAddress == "" {
		return "", nil
	}
	if strings.Contains(bindAddress, "/") {
		return "", connect.NewError(connect.CodeInvalidArgument, errors.New("bind address must not include a path"))
	}
	if strings.Contains(bindAddress, ":") {
		if ip := net.ParseIP(bindAddress); ip == nil {
			return "", connect.NewError(connect.CodeInvalidArgument, errors.New("bind address must not include a port"))
		}
	}
	return bindAddress, nil
}

func validatePublicStaticHeaders(headers []*p2pstreamv1.PublicHeader) ([]publicRouteTargetResponseHeaderInput, error) {
	resp := make([]publicRouteTargetResponseHeaderInput, 0, len(headers))
	for _, header := range headers {
		if header == nil {
			continue
		}
		name := strings.TrimSpace(header.Name)
		if name == "" {
			return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("static response header name is required"))
		}
		if !isHTTPToken(name) {
			return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("invalid static response header name %q", name))
		}
		if isBlockedStaticHeader(name) {
			return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("static response header %q is controlled by the proxy", name))
		}
		if strings.ContainsAny(header.Value, "\r\n") {
			return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("static response header %q must not contain CR or LF", name))
		}
		if !utf8.ValidString(header.Value) {
			return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("static response header %q must be valid UTF-8", name))
		}
		resp = append(resp, publicRouteTargetResponseHeaderInput{Name: name, Value: header.Value})
	}
	return resp, nil
}

func validatePublicRouteTargetUpstreamHeaders(
	headers []*p2pstreamv1.PublicRouteTargetUpstreamHeader,
	basicAuthEnabled bool,
) ([]publicRouteTargetUpstreamHeaderInput, error) {
	resp := make([]publicRouteTargetUpstreamHeaderInput, 0, len(headers))
	seen := make(map[string]struct{}, len(headers))
	for _, header := range headers {
		if header == nil {
			continue
		}
		name := strings.TrimSpace(header.Name)
		if name == "" {
			return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("upstream request header name is required"))
		}
		if !isHTTPToken(name) {
			return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("invalid upstream request header name %q", name))
		}
		if isBlockedUpstreamRequestHeader(name) {
			return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("upstream request header %q is controlled by the proxy", name))
		}
		lowerName := strings.ToLower(name)
		if _, ok := seen[lowerName]; ok {
			return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("upstream request header %q is duplicated", name))
		}
		seen[lowerName] = struct{}{}
		if basicAuthEnabled && lowerName == "authorization" {
			return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("authorization header cannot be configured when upstream basic auth is enabled"))
		}

		sensitive := header.Sensitive || isForcedSensitiveUpstreamHeader(name)
		value := header.Value
		if sensitive && !header.ValueSet {
			return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("sensitive upstream request header %q requires a value", name))
		}
		if err := validateUpstreamHeaderValue(name, value); err != nil {
			return nil, err
		}
		resp = append(resp, publicRouteTargetUpstreamHeaderInput{
			ID:        header.Id,
			Name:      name,
			Value:     value,
			Sensitive: boolInt(sensitive),
		})
	}
	return resp, nil
}

func validatePublicRouteTargetBasicAuth(auth *p2pstreamv1.PublicRouteTargetBasicAuth) (int64, string, string, error) {
	if auth == nil || !auth.Enabled {
		return 0, "", "", nil
	}
	username := strings.TrimSpace(auth.Username)
	if username == "" {
		return 0, "", "", connect.NewError(connect.CodeInvalidArgument, errors.New("upstream basic auth username is required"))
	}
	if strings.Contains(username, ":") {
		return 0, "", "", connect.NewError(connect.CodeInvalidArgument, errors.New("upstream basic auth username must not contain ':'"))
	}
	if strings.ContainsAny(username, "\r\n") || !utf8.ValidString(username) {
		return 0, "", "", connect.NewError(connect.CodeInvalidArgument, errors.New("upstream basic auth username must be valid UTF-8 without CR or LF"))
	}

	password := auth.Password
	if !auth.PasswordSet || password == "" {
		return 0, "", "", connect.NewError(connect.CodeInvalidArgument, errors.New("upstream basic auth password is required"))
	}
	if strings.ContainsAny(password, "\r\n") || !utf8.ValidString(password) {
		return 0, "", "", connect.NewError(connect.CodeInvalidArgument, errors.New("upstream basic auth password must be valid UTF-8 without CR or LF"))
	}
	return 1, username, password, nil
}

func validateUpstreamHeaderValue(name string, value string) error {
	if strings.ContainsAny(value, "\r\n") {
		return connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("upstream request header %q must not contain CR or LF", name))
	}
	if !utf8.ValidString(value) {
		return connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("upstream request header %q must be valid UTF-8", name))
	}
	if len([]byte(value)) > maxUpstreamHeaderValueBytes {
		return connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("upstream request header %q must be at most %d bytes", name, maxUpstreamHeaderValueBytes))
	}
	return nil
}

func isBlockedStaticHeader(name string) bool {
	switch strings.ToLower(name) {
	case "connection", "transfer-encoding", "content-length", "upgrade", "keep-alive", "te", "trailer":
		return true
	default:
		return false
	}
}

func isBlockedUpstreamRequestHeader(name string) bool {
	switch strings.ToLower(name) {
	case "host", "connection", "transfer-encoding", "content-length", "upgrade", "keep-alive", "te", "trailer":
		return true
	default:
		return false
	}
}

func isForcedSensitiveUpstreamHeader(name string) bool {
	switch strings.ToLower(name) {
	case "authorization", "proxy-authorization", "cookie":
		return true
	default:
		return false
	}
}

func isHTTPToken(value string) bool {
	if value == "" {
		return false
	}
	for _, r := range value {
		if r > 127 {
			return false
		}
		if ('a' <= r && r <= 'z') || ('A' <= r && r <= 'Z') || ('0' <= r && r <= '9') {
			continue
		}
		switch r {
		case '!', '#', '$', '%', '&', '\'', '*', '+', '-', '.', '^', '_', '`', '|', '~':
			continue
		default:
			return false
		}
	}
	return true
}

func parsePublicTargetOrigin(targetOrigin string) (*url.URL, error) {
	parsed, err := url.Parse(strings.TrimSpace(targetOrigin))
	if err != nil {
		return nil, fmt.Errorf("invalid target origin: %w", err)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return nil, errors.New("target origin must use http or https")
	}
	if parsed.Host == "" {
		return nil, errors.New("target origin must include a host")
	}
	return parsed, nil
}

func normalizeHostPattern(pattern string) string {
	pattern = strings.TrimSpace(strings.ToLower(pattern))
	return strings.TrimSuffix(pattern, ".")
}

func validateHostPattern(pattern string) error {
	pattern = normalizeHostPattern(pattern)
	if pattern == "" {
		return connect.NewError(connect.CodeInvalidArgument, errors.New("host pattern is required"))
	}
	if strings.ContainsAny(pattern, "/: ") {
		return connect.NewError(connect.CodeInvalidArgument, errors.New("host pattern must not include scheme, port, path, or spaces"))
	}
	if strings.HasPrefix(pattern, "*.") {
		remainder := strings.TrimPrefix(pattern, "*.")
		if remainder == "" || strings.Contains(remainder, "*") {
			return connect.NewError(connect.CodeInvalidArgument, errors.New("wildcard host pattern must look like *.example.com"))
		}
		return nil
	}
	if strings.Contains(pattern, "*") {
		return connect.NewError(connect.CodeInvalidArgument, errors.New("wildcard host pattern must start with *."))
	}
	return nil
}

func publicRouteTargetTypeStringFromProto(targetType p2pstreamv1.PublicRouteTargetType) (string, error) {
	switch targetType {
	case p2pstreamv1.PublicRouteTargetType_PUBLIC_ROUTE_TARGET_TYPE_UNSPECIFIED,
		p2pstreamv1.PublicRouteTargetType_PUBLIC_ROUTE_TARGET_TYPE_PROXY:
		return publicRouteTargetTypeProxy, nil
	case p2pstreamv1.PublicRouteTargetType_PUBLIC_ROUTE_TARGET_TYPE_STATIC:
		return publicRouteTargetTypeStatic, nil
	default:
		return "", connect.NewError(connect.CodeInvalidArgument, errors.New("route target type must be proxy or static"))
	}
}

func publicRouteTargetTransportStringFromProto(transport p2pstreamv1.PublicRouteTargetTransport) (string, error) {
	switch transport {
	case p2pstreamv1.PublicRouteTargetTransport_PUBLIC_ROUTE_TARGET_TRANSPORT_UNSPECIFIED,
		p2pstreamv1.PublicRouteTargetTransport_PUBLIC_ROUTE_TARGET_TRANSPORT_DIRECT:
		return publicRouteTargetTransportDirect, nil
	case p2pstreamv1.PublicRouteTargetTransport_PUBLIC_ROUTE_TARGET_TRANSPORT_AGENT:
		return publicRouteTargetTransportAgent, nil
	default:
		return "", connect.NewError(connect.CodeInvalidArgument, errors.New("route target transport must be direct or agent"))
	}
}

func normalizePublicRouteTargetType(targetType string) string {
	switch strings.TrimSpace(strings.ToLower(targetType)) {
	case "", publicRouteTargetTypeProxy:
		return publicRouteTargetTypeProxy
	case publicRouteTargetTypeStatic:
		return publicRouteTargetTypeStatic
	default:
		return publicRouteTargetTypeProxy
	}
}

func normalizePublicRouteTargetTransport(transport string) string {
	switch strings.TrimSpace(strings.ToLower(transport)) {
	case "", publicRouteTargetTransportDirect:
		return publicRouteTargetTransportDirect
	case publicRouteTargetTransportAgent:
		return publicRouteTargetTransportAgent
	default:
		return publicRouteTargetTransportDirect
	}
}

func routeActionStringFromProto(action p2pstreamv1.PublicRouteAction) (string, error) {
	switch action {
	case p2pstreamv1.PublicRouteAction_PUBLIC_ROUTE_ACTION_UNSPECIFIED,
		p2pstreamv1.PublicRouteAction_PUBLIC_ROUTE_ACTION_FORWARD:
		return publicRouteActionForward, nil
	case p2pstreamv1.PublicRouteAction_PUBLIC_ROUTE_ACTION_REDIRECT:
		return publicRouteActionRedirect, nil
	default:
		return "", connect.NewError(connect.CodeInvalidArgument, errors.New("route action must be forward or redirect"))
	}
}

func routeRedirectTargetModeStringFromProto(mode p2pstreamv1.PublicRouteRedirectTargetMode) (string, error) {
	switch mode {
	case p2pstreamv1.PublicRouteRedirectTargetMode_PUBLIC_ROUTE_REDIRECT_TARGET_MODE_SAME_HOST_PATH:
		return publicRouteRedirectTargetModeSameHostPath, nil
	case p2pstreamv1.PublicRouteRedirectTargetMode_PUBLIC_ROUTE_REDIRECT_TARGET_MODE_EXTERNAL_ORIGIN_KEEP_PATH:
		return publicRouteRedirectTargetModeExternalOriginKeepPath, nil
	case p2pstreamv1.PublicRouteRedirectTargetMode_PUBLIC_ROUTE_REDIRECT_TARGET_MODE_ABSOLUTE_URL:
		return publicRouteRedirectTargetModeAbsoluteURL, nil
	default:
		return "", connect.NewError(connect.CodeInvalidArgument, errors.New("redirect target mode is required"))
	}
}

func validateRouteRedirectTarget(mode string, target string) error {
	if strings.TrimSpace(target) == "" {
		return connect.NewError(connect.CodeInvalidArgument, errors.New("redirect target is required"))
	}
	parsed, err := url.Parse(target)
	if err != nil {
		return connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("invalid redirect target: %w", err))
	}
	switch mode {
	case publicRouteRedirectTargetModeSameHostPath:
		if parsed.IsAbs() || parsed.Host != "" || !strings.HasPrefix(target, "/") || strings.HasPrefix(target, "//") {
			return connect.NewError(connect.CodeInvalidArgument, errors.New("same-host redirect target must be a root-relative path"))
		}
	case publicRouteRedirectTargetModeExternalOriginKeepPath:
		if !isHTTPURL(parsed) || (parsed.Path != "" && parsed.Path != "/") || parsed.RawQuery != "" || parsed.Fragment != "" {
			return connect.NewError(connect.CodeInvalidArgument, errors.New("external-origin redirect target must be an http or https origin"))
		}
	case publicRouteRedirectTargetModeAbsoluteURL:
		if !isHTTPURL(parsed) {
			return connect.NewError(connect.CodeInvalidArgument, errors.New("absolute redirect target must be an http or https URL"))
		}
	default:
		return connect.NewError(connect.CodeInvalidArgument, errors.New("unsupported redirect target mode"))
	}
	return nil
}

func isHTTPURL(value *url.URL) bool {
	if value == nil || value.Host == "" {
		return false
	}
	return value.Scheme == "http" || value.Scheme == "https"
}

func validRedirectStatusCode(statusCode int64) bool {
	switch statusCode {
	case http.StatusMovedPermanently, http.StatusFound, http.StatusTemporaryRedirect, http.StatusPermanentRedirect:
		return true
	default:
		return false
	}
}

func loadBalancingStringFromProto(loadBalancing p2pstreamv1.PublicRouteTargetLoadBalancing) (string, error) {
	switch loadBalancing {
	case p2pstreamv1.PublicRouteTargetLoadBalancing_PUBLIC_ROUTE_TARGET_LOAD_BALANCING_UNSPECIFIED,
		p2pstreamv1.PublicRouteTargetLoadBalancing_PUBLIC_ROUTE_TARGET_LOAD_BALANCING_ROUND_ROBIN:
		return publicRouteTargetLoadBalancingRoundRobin, nil
	case p2pstreamv1.PublicRouteTargetLoadBalancing_PUBLIC_ROUTE_TARGET_LOAD_BALANCING_WEIGHTED_ROUND_ROBIN:
		return publicRouteTargetLoadBalancingWeightedRoundRobin, nil
	case p2pstreamv1.PublicRouteTargetLoadBalancing_PUBLIC_ROUTE_TARGET_LOAD_BALANCING_RANDOM:
		return publicRouteTargetLoadBalancingRandom, nil
	case p2pstreamv1.PublicRouteTargetLoadBalancing_PUBLIC_ROUTE_TARGET_LOAD_BALANCING_WEIGHTED_RANDOM:
		return publicRouteTargetLoadBalancingWeightedRandom, nil
	case p2pstreamv1.PublicRouteTargetLoadBalancing_PUBLIC_ROUTE_TARGET_LOAD_BALANCING_LEAST_ACTIVE_REQUESTS:
		return publicRouteTargetLoadBalancingLeastActiveRequests, nil
	case p2pstreamv1.PublicRouteTargetLoadBalancing_PUBLIC_ROUTE_TARGET_LOAD_BALANCING_WEIGHTED_LEAST_ACTIVE_REQUESTS:
		return publicRouteTargetLoadBalancingWeightedLeastActiveRequests, nil
	default:
		return "", connect.NewError(connect.CodeInvalidArgument, errors.New("unsupported backend load balancing algorithm"))
	}
}

func normalizePublicTargetType(backendType string) string {
	switch strings.TrimSpace(strings.ToLower(backendType)) {
	case "", publicRouteTargetTypeProxy:
		return publicRouteTargetTypeProxy
	case publicRouteTargetTypeStatic:
		return publicRouteTargetTypeStatic
	default:
		return strings.TrimSpace(strings.ToLower(backendType))
	}
}

func normalizePublicRouteTargetLoadBalancing(loadBalancing string) string {
	switch strings.TrimSpace(strings.ToLower(loadBalancing)) {
	case "", publicRouteTargetLoadBalancingRoundRobin:
		return publicRouteTargetLoadBalancingRoundRobin
	case publicRouteTargetLoadBalancingWeightedRoundRobin:
		return publicRouteTargetLoadBalancingWeightedRoundRobin
	case publicRouteTargetLoadBalancingRandom:
		return publicRouteTargetLoadBalancingRandom
	case publicRouteTargetLoadBalancingWeightedRandom:
		return publicRouteTargetLoadBalancingWeightedRandom
	case publicRouteTargetLoadBalancingLeastActiveRequests:
		return publicRouteTargetLoadBalancingLeastActiveRequests
	case publicRouteTargetLoadBalancingWeightedLeastActiveRequests:
		return publicRouteTargetLoadBalancingWeightedLeastActiveRequests
	default:
		return strings.TrimSpace(strings.ToLower(loadBalancing))
	}
}

func normalizePublicRouteAction(action string) string {
	switch strings.TrimSpace(strings.ToLower(action)) {
	case "", publicRouteActionForward:
		return publicRouteActionForward
	case publicRouteActionRedirect:
		return publicRouteActionRedirect
	default:
		return strings.TrimSpace(strings.ToLower(action))
	}
}

func normalizePublicRouteRedirectTargetMode(mode string) string {
	switch strings.TrimSpace(strings.ToLower(mode)) {
	case publicRouteRedirectTargetModeSameHostPath:
		return publicRouteRedirectTargetModeSameHostPath
	case publicRouteRedirectTargetModeExternalOriginKeepPath:
		return publicRouteRedirectTargetModeExternalOriginKeepPath
	case publicRouteRedirectTargetModeAbsoluteURL:
		return publicRouteRedirectTargetModeAbsoluteURL
	default:
		return strings.TrimSpace(strings.ToLower(mode))
	}
}

func protoRouteActionFromString(action string) p2pstreamv1.PublicRouteAction {
	switch normalizePublicRouteAction(action) {
	case publicRouteActionRedirect:
		return p2pstreamv1.PublicRouteAction_PUBLIC_ROUTE_ACTION_REDIRECT
	case publicRouteActionForward:
		return p2pstreamv1.PublicRouteAction_PUBLIC_ROUTE_ACTION_FORWARD
	default:
		return p2pstreamv1.PublicRouteAction_PUBLIC_ROUTE_ACTION_UNSPECIFIED
	}
}

func protoRouteRedirectTargetModeFromString(mode string) p2pstreamv1.PublicRouteRedirectTargetMode {
	switch normalizePublicRouteRedirectTargetMode(mode) {
	case publicRouteRedirectTargetModeSameHostPath:
		return p2pstreamv1.PublicRouteRedirectTargetMode_PUBLIC_ROUTE_REDIRECT_TARGET_MODE_SAME_HOST_PATH
	case publicRouteRedirectTargetModeExternalOriginKeepPath:
		return p2pstreamv1.PublicRouteRedirectTargetMode_PUBLIC_ROUTE_REDIRECT_TARGET_MODE_EXTERNAL_ORIGIN_KEEP_PATH
	case publicRouteRedirectTargetModeAbsoluteURL:
		return p2pstreamv1.PublicRouteRedirectTargetMode_PUBLIC_ROUTE_REDIRECT_TARGET_MODE_ABSOLUTE_URL
	default:
		return p2pstreamv1.PublicRouteRedirectTargetMode_PUBLIC_ROUTE_REDIRECT_TARGET_MODE_UNSPECIFIED
	}
}

func protoLoadBalancingFromString(loadBalancing string) p2pstreamv1.PublicRouteTargetLoadBalancing {
	switch normalizePublicRouteTargetLoadBalancing(loadBalancing) {
	case publicRouteTargetLoadBalancingWeightedRoundRobin:
		return p2pstreamv1.PublicRouteTargetLoadBalancing_PUBLIC_ROUTE_TARGET_LOAD_BALANCING_WEIGHTED_ROUND_ROBIN
	case publicRouteTargetLoadBalancingRandom:
		return p2pstreamv1.PublicRouteTargetLoadBalancing_PUBLIC_ROUTE_TARGET_LOAD_BALANCING_RANDOM
	case publicRouteTargetLoadBalancingWeightedRandom:
		return p2pstreamv1.PublicRouteTargetLoadBalancing_PUBLIC_ROUTE_TARGET_LOAD_BALANCING_WEIGHTED_RANDOM
	case publicRouteTargetLoadBalancingLeastActiveRequests:
		return p2pstreamv1.PublicRouteTargetLoadBalancing_PUBLIC_ROUTE_TARGET_LOAD_BALANCING_LEAST_ACTIVE_REQUESTS
	case publicRouteTargetLoadBalancingWeightedLeastActiveRequests:
		return p2pstreamv1.PublicRouteTargetLoadBalancing_PUBLIC_ROUTE_TARGET_LOAD_BALANCING_WEIGHTED_LEAST_ACTIVE_REQUESTS
	case publicRouteTargetLoadBalancingRoundRobin:
		return p2pstreamv1.PublicRouteTargetLoadBalancing_PUBLIC_ROUTE_TARGET_LOAD_BALANCING_ROUND_ROBIN
	default:
		return p2pstreamv1.PublicRouteTargetLoadBalancing_PUBLIC_ROUTE_TARGET_LOAD_BALANCING_UNSPECIFIED
	}
}

func protoPublicRouteTargetTypeFromString(targetType string) p2pstreamv1.PublicRouteTargetType {
	switch strings.TrimSpace(strings.ToLower(targetType)) {
	case publicRouteTargetTypeStatic:
		return p2pstreamv1.PublicRouteTargetType_PUBLIC_ROUTE_TARGET_TYPE_STATIC
	case publicRouteTargetTypeProxy, "":
		return p2pstreamv1.PublicRouteTargetType_PUBLIC_ROUTE_TARGET_TYPE_PROXY
	default:
		return p2pstreamv1.PublicRouteTargetType_PUBLIC_ROUTE_TARGET_TYPE_UNSPECIFIED
	}
}

func protoPublicRouteTargetTransportFromString(transport string) p2pstreamv1.PublicRouteTargetTransport {
	switch strings.TrimSpace(strings.ToLower(transport)) {
	case publicRouteTargetTransportAgent:
		return p2pstreamv1.PublicRouteTargetTransport_PUBLIC_ROUTE_TARGET_TRANSPORT_AGENT
	case publicRouteTargetTransportDirect, "":
		return p2pstreamv1.PublicRouteTargetTransport_PUBLIC_ROUTE_TARGET_TRANSPORT_DIRECT
	default:
		return p2pstreamv1.PublicRouteTargetTransport_PUBLIC_ROUTE_TARGET_TRANSPORT_UNSPECIFIED
	}
}

func tlsCertificateSourceStringFromProto(source p2pstreamv1.PublicTlsCertificateSource) (string, error) {
	switch source {
	case p2pstreamv1.PublicTlsCertificateSource_PUBLIC_TLS_CERTIFICATE_SOURCE_UNSPECIFIED,
		p2pstreamv1.PublicTlsCertificateSource_PUBLIC_TLS_CERTIFICATE_SOURCE_MANUAL:
		return publicTLSCertificateSourceManual, nil
	case p2pstreamv1.PublicTlsCertificateSource_PUBLIC_TLS_CERTIFICATE_SOURCE_ACME:
		return publicTLSCertificateSourceACME, nil
	default:
		return "", connect.NewError(connect.CodeInvalidArgument, errors.New("TLS certificate source must be manual or ACME"))
	}
}

func acmeChallengeTypeStringFromProto(challengeType p2pstreamv1.PublicAcmeChallengeType) (string, error) {
	switch challengeType {
	case p2pstreamv1.PublicAcmeChallengeType_PUBLIC_ACME_CHALLENGE_TYPE_HTTP_01:
		return publicACMEChallengeHTTP01, nil
	case p2pstreamv1.PublicAcmeChallengeType_PUBLIC_ACME_CHALLENGE_TYPE_TLS_ALPN_01:
		return publicACMEChallengeTLSALPN01, nil
	case p2pstreamv1.PublicAcmeChallengeType_PUBLIC_ACME_CHALLENGE_TYPE_DNS_01:
		return publicACMEChallengeDNS01, nil
	default:
		return "", connect.NewError(connect.CodeInvalidArgument, errors.New("ACME challenge type is required"))
	}
}

func acmeCAStringFromProto(ca p2pstreamv1.PublicAcmeCa) (string, error) {
	switch ca {
	case p2pstreamv1.PublicAcmeCa_PUBLIC_ACME_CA_UNSPECIFIED,
		p2pstreamv1.PublicAcmeCa_PUBLIC_ACME_CA_LETS_ENCRYPT_PRODUCTION:
		return publicACMECAProduction, nil
	case p2pstreamv1.PublicAcmeCa_PUBLIC_ACME_CA_LETS_ENCRYPT_STAGING:
		return publicACMECAStaging, nil
	default:
		return "", connect.NewError(connect.CodeInvalidArgument, errors.New("ACME CA must be Let's Encrypt production or staging"))
	}
}

func dnsProviderStringFromProto(provider p2pstreamv1.PublicDnsProvider) (string, error) {
	switch provider {
	case p2pstreamv1.PublicDnsProvider_PUBLIC_DNS_PROVIDER_UNSPECIFIED,
		p2pstreamv1.PublicDnsProvider_PUBLIC_DNS_PROVIDER_CLOUDFLARE:
		return publicDNSProviderCloudflare, nil
	default:
		return "", connect.NewError(connect.CodeInvalidArgument, errors.New("DNS provider must be Cloudflare"))
	}
}

func normalizePublicTLSCertificateSource(source string) string {
	switch strings.TrimSpace(strings.ToLower(source)) {
	case "", publicTLSCertificateSourceManual:
		return publicTLSCertificateSourceManual
	case publicTLSCertificateSourceACME:
		return publicTLSCertificateSourceACME
	default:
		return strings.TrimSpace(strings.ToLower(source))
	}
}

func normalizePublicACMEChallengeType(challengeType string) string {
	switch strings.TrimSpace(strings.ToLower(challengeType)) {
	case publicACMEChallengeHTTP01, "http-01":
		return publicACMEChallengeHTTP01
	case publicACMEChallengeTLSALPN01, "tls-alpn-01":
		return publicACMEChallengeTLSALPN01
	case publicACMEChallengeDNS01, "dns-01":
		return publicACMEChallengeDNS01
	default:
		return strings.TrimSpace(strings.ToLower(challengeType))
	}
}

func normalizePublicACMECA(ca string) string {
	switch strings.TrimSpace(strings.ToLower(ca)) {
	case "", publicACMECAProduction:
		return publicACMECAProduction
	case publicACMECAStaging:
		return publicACMECAStaging
	default:
		return strings.TrimSpace(strings.ToLower(ca))
	}
}

func normalizePublicDNSProvider(provider string) string {
	switch strings.TrimSpace(strings.ToLower(provider)) {
	case "", publicDNSProviderCloudflare:
		return publicDNSProviderCloudflare
	default:
		return strings.TrimSpace(strings.ToLower(provider))
	}
}

func normalizePublicTLSCertificateStatus(status string) string {
	switch strings.TrimSpace(strings.ToLower(status)) {
	case "", publicTLSCertificateStatusReady:
		return publicTLSCertificateStatusReady
	case publicTLSCertificateStatusPending, publicTLSCertificateStatusRenewing, publicTLSCertificateStatusError:
		return strings.TrimSpace(strings.ToLower(status))
	default:
		return strings.TrimSpace(strings.ToLower(status))
	}
}

func protoTLSCertificateSourceFromString(source string) p2pstreamv1.PublicTlsCertificateSource {
	switch normalizePublicTLSCertificateSource(source) {
	case publicTLSCertificateSourceACME:
		return p2pstreamv1.PublicTlsCertificateSource_PUBLIC_TLS_CERTIFICATE_SOURCE_ACME
	case publicTLSCertificateSourceManual:
		return p2pstreamv1.PublicTlsCertificateSource_PUBLIC_TLS_CERTIFICATE_SOURCE_MANUAL
	default:
		return p2pstreamv1.PublicTlsCertificateSource_PUBLIC_TLS_CERTIFICATE_SOURCE_UNSPECIFIED
	}
}

func protoACMEChallengeTypeFromString(challengeType string) p2pstreamv1.PublicAcmeChallengeType {
	switch normalizePublicACMEChallengeType(challengeType) {
	case publicACMEChallengeHTTP01:
		return p2pstreamv1.PublicAcmeChallengeType_PUBLIC_ACME_CHALLENGE_TYPE_HTTP_01
	case publicACMEChallengeTLSALPN01:
		return p2pstreamv1.PublicAcmeChallengeType_PUBLIC_ACME_CHALLENGE_TYPE_TLS_ALPN_01
	case publicACMEChallengeDNS01:
		return p2pstreamv1.PublicAcmeChallengeType_PUBLIC_ACME_CHALLENGE_TYPE_DNS_01
	default:
		return p2pstreamv1.PublicAcmeChallengeType_PUBLIC_ACME_CHALLENGE_TYPE_UNSPECIFIED
	}
}

func protoACMECAFromString(ca string) p2pstreamv1.PublicAcmeCa {
	switch normalizePublicACMECA(ca) {
	case publicACMECAStaging:
		return p2pstreamv1.PublicAcmeCa_PUBLIC_ACME_CA_LETS_ENCRYPT_STAGING
	case publicACMECAProduction:
		return p2pstreamv1.PublicAcmeCa_PUBLIC_ACME_CA_LETS_ENCRYPT_PRODUCTION
	default:
		return p2pstreamv1.PublicAcmeCa_PUBLIC_ACME_CA_UNSPECIFIED
	}
}

func protoDNSProviderFromString(provider string) p2pstreamv1.PublicDnsProvider {
	switch normalizePublicDNSProvider(provider) {
	case publicDNSProviderCloudflare:
		return p2pstreamv1.PublicDnsProvider_PUBLIC_DNS_PROVIDER_CLOUDFLARE
	default:
		return p2pstreamv1.PublicDnsProvider_PUBLIC_DNS_PROVIDER_UNSPECIFIED
	}
}

func protoTLSCertificateStatusFromString(status string) p2pstreamv1.PublicTlsCertificateStatus {
	switch normalizePublicTLSCertificateStatus(status) {
	case publicTLSCertificateStatusPending:
		return p2pstreamv1.PublicTlsCertificateStatus_PUBLIC_TLS_CERTIFICATE_STATUS_PENDING
	case publicTLSCertificateStatusRenewing:
		return p2pstreamv1.PublicTlsCertificateStatus_PUBLIC_TLS_CERTIFICATE_STATUS_RENEWING
	case publicTLSCertificateStatusError:
		return p2pstreamv1.PublicTlsCertificateStatus_PUBLIC_TLS_CERTIFICATE_STATUS_ERROR
	case publicTLSCertificateStatusReady:
		return p2pstreamv1.PublicTlsCertificateStatus_PUBLIC_TLS_CERTIFICATE_STATUS_READY
	default:
		return p2pstreamv1.PublicTlsCertificateStatus_PUBLIC_TLS_CERTIFICATE_STATUS_UNSPECIFIED
	}
}

func protocolStringFromProto(protocol p2pstreamv1.PublicListenerProtocol) (string, error) {
	switch protocol {
	case p2pstreamv1.PublicListenerProtocol_PUBLIC_LISTENER_PROTOCOL_HTTP:
		return publicListenerProtocolHTTP, nil
	case p2pstreamv1.PublicListenerProtocol_PUBLIC_LISTENER_PROTOCOL_HTTPS:
		return publicListenerProtocolHTTPS, nil
	default:
		return "", connect.NewError(connect.CodeInvalidArgument, errors.New("listener protocol must be http or https"))
	}
}

func protoProtocolFromString(protocol string) p2pstreamv1.PublicListenerProtocol {
	switch protocol {
	case publicListenerProtocolHTTP:
		return p2pstreamv1.PublicListenerProtocol_PUBLIC_LISTENER_PROTOCOL_HTTP
	case publicListenerProtocolHTTPS:
		return p2pstreamv1.PublicListenerProtocol_PUBLIC_LISTENER_PROTOCOL_HTTPS
	default:
		return p2pstreamv1.PublicListenerProtocol_PUBLIC_LISTENER_PROTOCOL_UNSPECIFIED
	}
}

func boolInt(value bool) int64 {
	if value {
		return 1
	}
	return 0
}

func publicDBError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, sql.ErrNoRows) {
		return connect.NewError(connect.CodeNotFound, err)
	}
	msg := strings.ToLower(err.Error())
	if strings.Contains(msg, "unique constraint failed") {
		return connect.NewError(connect.CodeAlreadyExists, err)
	}
	if strings.Contains(msg, "foreign key constraint failed") {
		return connect.NewError(connect.CodeFailedPrecondition, err)
	}
	return connect.NewError(connect.CodeInternal, err)
}

func publicRouteTargetUpstreamHeadersToConfig(headers []db.PublicRouteTargetUpstreamHeader) []publicRequestHeader {
	resp := make([]publicRequestHeader, 0, len(headers))
	for _, header := range headers {
		resp = append(resp, publicRequestHeader{Name: header.Name, Value: header.Value, Sensitive: header.Sensitive != 0})
	}
	return resp
}

func publicRouteTargetResponseHeadersToConfig(headers []db.PublicRouteTargetResponseHeader) []publicResponseHeader {
	resp := make([]publicResponseHeader, 0, len(headers))
	for _, header := range headers {
		resp = append(resp, publicResponseHeader{Name: header.Name, Value: header.Value})
	}
	return resp
}

func publicRouteTargetHealthCheckRowToConfig(target db.PublicRouteTarget) publicRouteTargetHealthCheckConfig {
	return publicRouteTargetHealthCheckConfig{
		Enabled:            target.HealthCheckEnabled != 0,
		Method:             target.HealthCheckMethod,
		Path:               target.HealthCheckPath,
		Interval:           time.Duration(target.HealthCheckIntervalMillis) * time.Millisecond,
		Timeout:            time.Duration(target.HealthCheckTimeoutMillis) * time.Millisecond,
		HealthyThreshold:   target.HealthCheckHealthyThreshold,
		UnhealthyThreshold: target.HealthCheckUnhealthyThreshold,
		ExpectedStatusMin:  target.HealthCheckExpectedStatusMin,
		ExpectedStatusMax:  target.HealthCheckExpectedStatusMax,
	}
}

func publicAgentLabelsByAgent(labels []db.PublicAgentLabel) map[int64]map[string]string {
	resp := make(map[int64]map[string]string)
	for _, label := range labels {
		if resp[label.AgentID] == nil {
			resp[label.AgentID] = make(map[string]string)
		}
		resp[label.AgentID][label.Key] = label.Value
	}
	return resp
}

func publicAgentEnabledByID(agents []db.Agent) map[int64]bool {
	resp := make(map[int64]bool, len(agents))
	for _, agent := range agents {
		resp[agent.ID] = agent.Enabled != 0
	}
	return resp
}

func publicRouteTargetsByRoute(targets []db.PublicRouteTarget) map[int64][]db.PublicRouteTarget {
	resp := make(map[int64][]db.PublicRouteTarget)
	for _, target := range targets {
		resp[target.RouteID] = append(resp[target.RouteID], target)
	}
	return resp
}

func publicRouteTargetUpstreamHeadersByTarget(headers []db.PublicRouteTargetUpstreamHeader) map[int64][]db.PublicRouteTargetUpstreamHeader {
	resp := make(map[int64][]db.PublicRouteTargetUpstreamHeader)
	for _, header := range headers {
		resp[header.TargetID] = append(resp[header.TargetID], header)
	}
	return resp
}

func publicRouteTargetResponseHeadersByTarget(headers []db.PublicRouteTargetResponseHeader) map[int64][]db.PublicRouteTargetResponseHeader {
	resp := make(map[int64][]db.PublicRouteTargetResponseHeader)
	for _, header := range headers {
		resp[header.TargetID] = append(resp[header.TargetID], header)
	}
	return resp
}

func (a *App) publicRouteTargetHeaderMaps(ctx context.Context) (map[int64][]db.PublicRouteTargetUpstreamHeader, map[int64][]db.PublicRouteTargetResponseHeader) {
	upstreamHeaders, err := a.DB.ListPublicRouteTargetUpstreamHeaders(ctx)
	if err != nil {
		upstreamHeaders = nil
	}
	responseHeaders, err := a.DB.ListPublicRouteTargetResponseHeaders(ctx)
	if err != nil {
		responseHeaders = nil
	}
	return publicRouteTargetUpstreamHeadersByTarget(upstreamHeaders), publicRouteTargetResponseHeadersByTarget(responseHeaders)
}

func publicRouteTargetsToProto(targets []db.PublicRouteTarget, upstreamHeaders map[int64][]db.PublicRouteTargetUpstreamHeader, responseHeaders map[int64][]db.PublicRouteTargetResponseHeader, monitor *publicRouteTargetHealthMonitor) []*p2pstreamv1.PublicRouteTarget {
	resp := make([]*p2pstreamv1.PublicRouteTarget, 0, len(targets))
	for _, target := range targets {
		resp = append(resp, publicRouteTargetToProto(target, upstreamHeaders[target.ID], responseHeaders[target.ID], monitor))
	}
	return resp
}

func publicRouteTargetToProto(target db.PublicRouteTarget, upstreamHeaders []db.PublicRouteTargetUpstreamHeader, responseHeaders []db.PublicRouteTargetResponseHeader, monitor *publicRouteTargetHealthMonitor) *p2pstreamv1.PublicRouteTarget {
	return &p2pstreamv1.PublicRouteTarget{
		Id:                                  target.ID,
		RouteId:                             target.RouteID,
		Name:                                target.Name,
		Position:                            target.Position,
		PriorityGroup:                       target.PriorityGroup,
		Weight:                              target.Weight,
		Enabled:                             target.Enabled != 0,
		TargetType:                          protoPublicRouteTargetTypeFromString(target.TargetType),
		Url:                                 target.Url,
		Transport:                           protoPublicRouteTargetTransportFromString(target.Transport),
		AgentSelector:                       publicAgentSelectorFromJSON(target.AgentSelectorJson),
		AgentLoadBalancing:                  protoLoadBalancingFromString(target.AgentLoadBalancing),
		TlsSkipVerify:                       target.TlsSkipVerify != 0,
		UpstreamResponseHeaderTimeoutMillis: target.UpstreamResponseHeaderTimeoutMillis,
		UpstreamRequestHeaders:              publicRouteTargetUpstreamHeadersToProto(upstreamHeaders),
		UpstreamBasicAuth: &p2pstreamv1.PublicRouteTargetBasicAuth{
			Enabled:     target.UpstreamBasicAuthEnabled != 0,
			Username:    target.UpstreamBasicAuthUsername,
			PasswordSet: target.UpstreamBasicAuthPassword != "",
		},
		HealthCheck: &p2pstreamv1.PublicRouteTargetHealthCheck{
			Enabled:            target.HealthCheckEnabled != 0,
			Method:             target.HealthCheckMethod,
			Path:               target.HealthCheckPath,
			IntervalMillis:     target.HealthCheckIntervalMillis,
			TimeoutMillis:      target.HealthCheckTimeoutMillis,
			HealthyThreshold:   target.HealthCheckHealthyThreshold,
			UnhealthyThreshold: target.HealthCheckUnhealthyThreshold,
			ExpectedStatusMin:  target.HealthCheckExpectedStatusMin,
			ExpectedStatusMax:  target.HealthCheckExpectedStatusMax,
		},
		StaticStatusCode:         target.StaticStatusCode,
		StaticResponseHeaders:    publicRouteTargetResponseHeadersToProto(responseHeaders),
		StaticResponseBody:       target.StaticResponseBody,
		StaticResponseBodyMode:   protoPublicResponseBodyMode(target.StaticResponseBodyMode),
		StaticResponseTemplateId: nullInt64Value(target.StaticResponseTemplateID),
		Health:                   publicRouteTargetHealthToProto(target, monitor),
	}
}

func publicRouteTargetHealthToProto(target db.PublicRouteTarget, monitor *publicRouteTargetHealthMonitor) *p2pstreamv1.PublicRouteTargetHealth {
	if target.Enabled == 0 {
		return &p2pstreamv1.PublicRouteTargetHealth{
			Status:    p2pstreamv1.PublicRouteTargetHealthStatus_PUBLIC_ROUTE_TARGET_HEALTH_STATUS_DISABLED,
			Available: false,
		}
	}
	if target.TargetType == publicRouteTargetTypeStatic {
		return &p2pstreamv1.PublicRouteTargetHealth{
			Status:    p2pstreamv1.PublicRouteTargetHealthStatus_PUBLIC_ROUTE_TARGET_HEALTH_STATUS_HEALTHY,
			Available: true,
			Connected: true,
		}
	}
	if monitor == nil {
		return &p2pstreamv1.PublicRouteTargetHealth{
			Status:    p2pstreamv1.PublicRouteTargetHealthStatus_PUBLIC_ROUTE_TARGET_HEALTH_STATUS_UNKNOWN,
			Available: true,
		}
	}
	snapshot := monitor.snapshot(publicRouteTargetHealthDBAdapter{id: target.ID, enabled: target.Enabled != 0})
	if snapshot == nil {
		return &p2pstreamv1.PublicRouteTargetHealth{
			Status:    p2pstreamv1.PublicRouteTargetHealthStatus_PUBLIC_ROUTE_TARGET_HEALTH_STATUS_UNKNOWN,
			Available: true,
		}
	}
	return &p2pstreamv1.PublicRouteTargetHealth{
		Status:                          snapshot.Status,
		Available:                       snapshot.Status != p2pstreamv1.PublicRouteTargetHealthStatus_PUBLIC_ROUTE_TARGET_HEALTH_STATUS_UNHEALTHY && snapshot.Status != p2pstreamv1.PublicRouteTargetHealthStatus_PUBLIC_ROUTE_TARGET_HEALTH_STATUS_DISABLED,
		Connected:                       true,
		LastCheckedAtUnixMillis:         snapshot.LastCheckedAtUnixMillis,
		LastError:                       snapshot.LastError,
		PassiveUnhealthyUntilUnixMillis: snapshot.PassiveUnhealthyUntilUnixMillis,
		ActiveRequests:                  monitor.activeRequests(target.ID),
	}
}

func publicRouteTargetTransportFromConfig(transport string) p2pstreamv1.PublicRouteTargetTransport {
	if transport == publicRouteTargetTransportAgent {
		return p2pstreamv1.PublicRouteTargetTransport_PUBLIC_ROUTE_TARGET_TRANSPORT_AGENT
	}
	return p2pstreamv1.PublicRouteTargetTransport_PUBLIC_ROUTE_TARGET_TRANSPORT_DIRECT
}

func publicRouteTargetUpstreamHeadersToProto(headers []db.PublicRouteTargetUpstreamHeader) []*p2pstreamv1.PublicRouteTargetUpstreamHeader {
	resp := make([]*p2pstreamv1.PublicRouteTargetUpstreamHeader, 0, len(headers))
	for _, header := range headers {
		value := header.Value
		if header.Sensitive != 0 {
			value = ""
		}
		resp = append(resp, &p2pstreamv1.PublicRouteTargetUpstreamHeader{
			Id:        header.ID,
			TargetId:  header.TargetID,
			Name:      header.Name,
			Value:     value,
			Sensitive: header.Sensitive != 0,
			ValueSet:  true,
			Position:  header.Position,
		})
	}
	return resp
}

func publicRouteTargetResponseHeadersToProto(headers []db.PublicRouteTargetResponseHeader) []*p2pstreamv1.PublicHeader {
	resp := make([]*p2pstreamv1.PublicHeader, 0, len(headers))
	for _, header := range headers {
		resp = append(resp, &p2pstreamv1.PublicHeader{Name: header.Name, Value: header.Value})
	}
	return resp
}

func publicAgentSelectorFromJSON(payload string) *p2pstreamv1.PublicAgentSelector {
	var decoded struct {
		MatchLabels map[string]string `json:"match_labels"`
	}
	_ = json.Unmarshal([]byte(payload), &decoded)
	if decoded.MatchLabels == nil {
		decoded.MatchLabels = map[string]string{}
	}
	return &p2pstreamv1.PublicAgentSelector{MatchLabels: decoded.MatchLabels}
}

func publicAgentSelectorConfigFromJSON(payload string) (publicAgentSelectorConfig, error) {
	payload = strings.TrimSpace(payload)
	if payload == "" {
		payload = "{}"
	}
	var decoded struct {
		MatchLabels map[string]string `json:"match_labels"`
	}
	if err := json.Unmarshal([]byte(payload), &decoded); err != nil {
		return publicAgentSelectorConfig{}, err
	}
	return publicAgentSelectorConfig{MatchLabels: cloneStringMap(decoded.MatchLabels)}, nil
}

func (a *App) publicAgentsToProto(ctx context.Context, agents []db.Agent, useDBFallback bool) []*p2pstreamv1.Agent {
	resp := make([]*p2pstreamv1.Agent, 0, len(agents))
	for _, agent := range agents {
		resp = append(resp, a.agentToProtoWithLatestStats(ctx, agent, useDBFallback))
	}
	return resp
}

func publicListenerToProto(listener db.PublicListener) *p2pstreamv1.PublicListener {
	return &p2pstreamv1.PublicListener{
		Id:                  listener.ID,
		Name:                listener.Name,
		BindAddress:         listener.BindAddress,
		Port:                listener.Port,
		Protocol:            protoProtocolFromString(listener.Protocol),
		Enabled:             listener.Enabled != 0,
		CreatedAtUnixMillis: listener.CreatedAt.UnixMilli(),
		UpdatedAtUnixMillis: listener.UpdatedAt.UnixMilli(),
	}
}

func publicListenersToProto(listeners []db.PublicListener) []*p2pstreamv1.PublicListener {
	resp := make([]*p2pstreamv1.PublicListener, 0, len(listeners))
	for _, listener := range listeners {
		resp = append(resp, publicListenerToProto(listener))
	}
	return resp
}

func publicRouteToProto(route db.PublicRoute, targets []db.PublicRouteTarget, upstreamHeaders map[int64][]db.PublicRouteTargetUpstreamHeader, responseHeaders map[int64][]db.PublicRouteTargetResponseHeader, monitor *publicRouteTargetHealthMonitor) *p2pstreamv1.PublicRoute {
	return &p2pstreamv1.PublicRoute{
		Id:                         route.ID,
		ListenerId:                 route.ListenerID,
		Priority:                   route.Priority,
		HostPattern:                route.HostPattern,
		PathPrefix:                 route.PathPrefix,
		TargetLoadBalancing:        protoLoadBalancingFromString(route.TargetLoadBalancing),
		IsDefault:                  route.IsDefault != 0,
		Targets:                    publicRouteTargetsToProto(targets, upstreamHeaders, responseHeaders, monitor),
		Enabled:                    route.Enabled != 0,
		CreatedAtUnixMillis:        route.CreatedAt.UnixMilli(),
		UpdatedAtUnixMillis:        route.UpdatedAt.UnixMilli(),
		Action:                     protoRouteActionFromString(route.Action),
		RedirectTargetMode:         protoRouteRedirectTargetModeFromString(route.RedirectTargetMode),
		RedirectTarget:             route.RedirectTarget,
		RedirectStatusCode:         route.RedirectStatusCode,
		RedirectPreservePathSuffix: route.RedirectPreservePathSuffix != 0,
		RedirectPreserveQuery:      route.RedirectPreserveQuery != 0,
	}
}

func publicRoutesToProto(routes []db.PublicRoute, targets []db.PublicRouteTarget, upstreamHeaders map[int64][]db.PublicRouteTargetUpstreamHeader, responseHeaders map[int64][]db.PublicRouteTargetResponseHeader, monitor *publicRouteTargetHealthMonitor) []*p2pstreamv1.PublicRoute {
	targetsByRoute := publicRouteTargetsByRoute(targets)
	resp := make([]*p2pstreamv1.PublicRoute, 0, len(routes))
	for _, route := range routes {
		resp = append(resp, publicRouteToProto(route, targetsByRoute[route.ID], upstreamHeaders, responseHeaders, monitor))
	}
	return resp
}

func publicTLSCertificateToProto(cert db.PublicTlsCertificate) *p2pstreamv1.PublicTlsCertificate {
	issuedAt := cert.IssuedAt
	expiresAt := cert.ExpiresAt
	if (!issuedAt.Valid || !expiresAt.Valid) && strings.TrimSpace(cert.CertPath) != "" {
		fileIssuedAt, fileExpiresAt := publicTLSCertificateValidityFromFile(cert.CertPath)
		if !issuedAt.Valid {
			issuedAt = fileIssuedAt
		}
		if !expiresAt.Valid {
			expiresAt = fileExpiresAt
		}
	}
	return &p2pstreamv1.PublicTlsCertificate{
		Id:                             cert.ID,
		ListenerId:                     cert.ListenerID,
		HostnamePattern:                cert.HostnamePattern,
		CertPath:                       cert.CertPath,
		KeyPath:                        cert.KeyPath,
		Enabled:                        cert.Enabled != 0,
		CreatedAtUnixMillis:            cert.CreatedAt.UnixMilli(),
		UpdatedAtUnixMillis:            cert.UpdatedAt.UnixMilli(),
		Source:                         protoTLSCertificateSourceFromString(cert.Source),
		AcmeChallengeType:              protoACMEChallengeTypeFromString(cert.AcmeChallengeType),
		AcmeCa:                         protoACMECAFromString(cert.AcmeCa),
		AcmeEmail:                      cert.AcmeEmail,
		DnsCredentialId:                nullInt64Value(cert.DnsCredentialID),
		Status:                         protoTLSCertificateStatusFromString(cert.Status),
		LastError:                      cert.LastError,
		IssuedAtUnixMillis:             nullTimeUnixMillis(issuedAt),
		ExpiresAtUnixMillis:            nullTimeUnixMillis(expiresAt),
		NextRenewalAtUnixMillis:        nullTimeUnixMillis(cert.NextRenewalAt),
		LastRenewalAttemptAtUnixMillis: nullTimeUnixMillis(cert.LastRenewalAttemptAt),
	}
}

func publicTLSCertificatesToProto(certs []db.PublicTlsCertificate) []*p2pstreamv1.PublicTlsCertificate {
	resp := make([]*p2pstreamv1.PublicTlsCertificate, 0, len(certs))
	for _, cert := range certs {
		resp = append(resp, publicTLSCertificateToProto(cert))
	}
	return resp
}

func publicTLSDNSCredentialToProto(credential db.PublicTlsDnsCredential) *p2pstreamv1.PublicTlsDnsCredential {
	return &p2pstreamv1.PublicTlsDnsCredential{
		Id:                  credential.ID,
		Name:                credential.Name,
		Provider:            protoDNSProviderFromString(credential.Provider),
		CloudflareZoneId:    credential.CloudflareZoneID,
		ApiTokenSet:         credential.ApiToken != "",
		Enabled:             credential.Enabled != 0,
		CreatedAtUnixMillis: credential.CreatedAt.UnixMilli(),
		UpdatedAtUnixMillis: credential.UpdatedAt.UnixMilli(),
	}
}

func publicTLSDNSCredentialsToProto(credentials []db.PublicTlsDnsCredential) []*p2pstreamv1.PublicTlsDnsCredential {
	resp := make([]*p2pstreamv1.PublicTlsDnsCredential, 0, len(credentials))
	for _, credential := range credentials {
		resp = append(resp, publicTLSDNSCredentialToProto(credential))
	}
	return resp
}

func nullInt64Value(value sql.NullInt64) int64 {
	if !value.Valid {
		return 0
	}
	return value.Int64
}
