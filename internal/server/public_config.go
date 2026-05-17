package server

import (
	"context"
	"crypto/tls"
	"database/sql"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/mail"
	"net/url"
	"os"
	"regexp"
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
	publicBackendTypeProxyForward                         = "proxy_forward"
	publicBackendTypeStatic                               = "static"
	publicResponseBodyModeInline                          = "inline"
	publicResponseBodyModeTemplate                        = "template"
	publicResponseTemplateKindGenericBody                 = "generic_body"
	publicResponseTemplateKindWafCaptchaPage              = "waf_captcha_page"
	publicResponseTemplateKindWafWaitingRoomPage          = "waf_waiting_room_page"
	defaultResponseTemplateContentType                    = "text/html; charset=utf-8"
	publicBackendForwardModeDirect                        = "direct"
	publicBackendForwardModeAgentPool                     = "agent_pool"
	publicBackendLoadBalancingRoundRobin                  = "round_robin"
	publicBackendLoadBalancingWeightedRoundRobin          = "weighted_round_robin"
	publicBackendLoadBalancingRandom                      = "random"
	publicBackendLoadBalancingWeightedRandom              = "weighted_random"
	publicBackendLoadBalancingLeastActiveRequests         = "least_active_requests"
	publicBackendLoadBalancingWeightedLeastActiveRequests = "weighted_least_active_requests"
	publicRouteActionForward                              = "forward"
	publicRouteActionRedirect                             = "redirect"
	publicRouteRedirectTargetModeSameHostPath             = "same_host_path"
	publicRouteRedirectTargetModeExternalOriginKeepPath   = "external_origin_keep_path"
	publicRouteRedirectTargetModeAbsoluteURL              = "absolute_url"
	publicRateLimitAlgorithmFixedWindow                   = "fixed_window"
	publicRateLimitAlgorithmSlidingWindow                 = "sliding_window"
	publicRateLimitAlgorithmTokenBucket                   = "token_bucket"
	publicRateLimitAlgorithmLeakyBucket                   = "leaky_bucket"
	publicTLSCertificateSourceManual                      = "manual"
	publicTLSCertificateSourceACME                        = "acme"
	publicACMEChallengeHTTP01                             = "http_01"
	publicACMEChallengeTLSALPN01                          = "tls_alpn_01"
	publicACMEChallengeDNS01                              = "dns_01"
	publicACMECAProduction                                = "letsencrypt_production"
	publicACMECAStaging                                   = "letsencrypt_staging"
	publicDNSProviderCloudflare                           = "cloudflare"
	publicTLSCertificateStatusPending                     = "pending"
	publicTLSCertificateStatusReady                       = "ready"
	publicTLSCertificateStatusRenewing                    = "renewing"
	publicTLSCertificateStatusError                       = "error"
	defaultStaticStatusCode                               = int64(http.StatusOK)
	defaultRedirectStatusCode                             = int64(http.StatusFound)
	defaultBackendHealthCheckMethod                       = http.MethodGet
	defaultBackendHealthCheckPath                         = "/"
	defaultBackendHealthCheckIntervalMillis               = int64(10000)
	defaultBackendHealthCheckTimeoutMillis                = int64(2000)
	defaultBackendHealthCheckHealthyThreshold             = int64(2)
	defaultBackendHealthCheckUnhealthyThreshold           = int64(2)
	defaultBackendHealthCheckExpectedStatusMin            = int64(200)
	defaultBackendHealthCheckExpectedStatusMax            = int64(399)
	defaultBackendUpstreamResponseHeaderTimeoutMillis     = int64(60000)
	minBackendUpstreamResponseHeaderTimeoutMillis         = int64(1000)
	maxBackendUpstreamResponseHeaderTimeoutMillis         = int64(3600000)
	maxUpstreamHeaderValueBytes                           = 8192
)

var publicNamePattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]{0,63}$`)

type publicConfigRows struct {
	Backends               []db.PublicBackend
	BackendHeaders         []db.PublicBackendHeader
	BackendUpstreamHeaders []db.PublicBackendUpstreamHeader
	BackendAgents          []db.PublicBackendAgent
	Agents                 []db.Agent
	Listeners              []db.PublicListener
	Routes                 []db.PublicRoute
	RouteBackends          []db.PublicRouteBackend
	TLSCertificates        []db.PublicTlsCertificate
	TLSDNSCredentials      []db.PublicTlsDnsCredential
	RateLimitRules         []db.PublicRateLimitRule
	TrafficShaperRules     []db.PublicTrafficShaperRule
	WafCaptchaProviders    []db.PublicWafCaptchaProvider
	WafRules               []db.PublicWafRule
	WafSettings            db.PublicWafSetting
	CacheSettings          db.PublicCacheSetting
	CacheRules             []db.PublicCacheRule
	ResponseTemplates      []db.PublicResponseTemplate
}

type publicBackendHeaderInput struct {
	Name  string
	Value string
}

type publicBackendUpstreamHeaderInput struct {
	ID        int64
	Name      string
	Value     string
	Sensitive int64
}

type publicBackendMutationInput struct {
	Name                          string
	TargetOrigin                  string
	BackendType                   string
	ForwardMode                   string
	LoadBalancing                 string
	TLSSkipVerify                 int64
	StaticStatusCode              int64
	StaticResponseBody            string
	StaticResponseBodyMode        string
	StaticResponseTemplateID      sql.NullInt64
	UpstreamBasicAuthEnabled      int64
	UpstreamBasicAuthUsername     string
	UpstreamBasicAuthPassword     string
	UpstreamResponseHeaderTimeout int64
	HealthCheckEnabled            int64
	HealthCheckMethod             string
	HealthCheckPath               string
	HealthCheckIntervalMillis     int64
	HealthCheckTimeoutMillis      int64
	HealthCheckHealthyThreshold   int64
	HealthCheckUnhealthyThreshold int64
	HealthCheckExpectedStatusMin  int64
	HealthCheckExpectedStatusMax  int64
	Enabled                       int64
	Headers                       []publicBackendHeaderInput
	Agents                        []publicBackendAgentInput
}

type publicBackendAgentInput struct {
	AgentID  int64
	Position int64
	Weight   int64
	Enabled  int64
}

type publicRouteBackendInput struct {
	BackendID int64
	Position  int64
	Weight    int64
	Enabled   int64
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

func (a *App) ListPublicBackendHealthTraces(
	ctx context.Context,
	req *connect.Request[p2pstreamv1.ListPublicBackendHealthTracesRequest],
) (*connect.Response[p2pstreamv1.ListPublicBackendHealthTracesResponse], error) {
	if _, err := a.requireUser(ctx, req.Header()); err != nil {
		return nil, err
	}
	if req.Msg.BackendId <= 0 {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("backend id is required"))
	}
	limit := req.Msg.Limit
	if limit <= 0 || limit > publicBackendHealthTraceLimitPerTarget {
		limit = publicBackendHealthTraceLimitPerTarget
	}
	var traces []*p2pstreamv1.PublicBackendHealthTrace
	var retained int64
	if a.BackendHealth != nil {
		traces, retained = a.BackendHealth.listHealthTraces(req.Msg.BackendId, req.Msg.AgentId, limit, req.Msg.FailuresOnly)
	}
	return connect.NewResponse(&p2pstreamv1.ListPublicBackendHealthTracesResponse{
		Traces:               traces,
		RetainedCount:        retained,
		MaxRetainedPerTarget: publicBackendHealthTraceLimitPerTarget,
	}), nil
}

func (a *App) CreatePublicBackend(
	ctx context.Context,
	req *connect.Request[p2pstreamv1.CreatePublicBackendRequest],
) (*connect.Response[p2pstreamv1.CreatePublicBackendResponse], error) {
	if _, err := a.requireAdmin(ctx, req.Header()); err != nil {
		return nil, err
	}
	params, headers, upstreamHeaders, agents, err := a.validatePublicBackendInput(
		ctx,
		0,
		req.Msg.Name,
		req.Msg.TargetOrigin,
		req.Msg.Enabled,
		req.Msg.BackendType,
		req.Msg.ForwardMode,
		req.Msg.LoadBalancing,
		req.Msg.AgentAssignments,
		req.Msg.TlsSkipVerify,
		req.Msg.StaticStatusCode,
		req.Msg.StaticResponseHeaders,
		req.Msg.StaticResponseBody,
		req.Msg.StaticResponseBodyMode,
		req.Msg.StaticResponseTemplateId,
		req.Msg.UpstreamRequestHeaders,
		req.Msg.UpstreamBasicAuth,
		req.Msg.HealthCheck,
		req.Msg.UpstreamResponseHeaderTimeoutMillis,
	)
	if err != nil {
		return nil, err
	}
	backend, storedHeaders, storedUpstreamHeaders, storedAgents, err := a.createPublicBackendWithDetails(ctx, params, headers, upstreamHeaders, agents)
	if err != nil {
		return nil, publicDBError(err)
	}
	if err := a.refreshPublicProxySnapshot(ctx); err != nil {
		return nil, err
	}
	return connect.NewResponse(&p2pstreamv1.CreatePublicBackendResponse{Backend: publicBackendToProto(backend, storedHeaders, storedUpstreamHeaders, storedAgents, nil, a.BackendHealth)}), nil
}

func (a *App) UpdatePublicBackend(
	ctx context.Context,
	req *connect.Request[p2pstreamv1.UpdatePublicBackendRequest],
) (*connect.Response[p2pstreamv1.UpdatePublicBackendResponse], error) {
	if _, err := a.requireAdmin(ctx, req.Header()); err != nil {
		return nil, err
	}
	params, headers, upstreamHeaders, agents, err := a.validatePublicBackendInput(
		ctx,
		req.Msg.Id,
		req.Msg.Name,
		req.Msg.TargetOrigin,
		req.Msg.Enabled,
		req.Msg.BackendType,
		req.Msg.ForwardMode,
		req.Msg.LoadBalancing,
		req.Msg.AgentAssignments,
		req.Msg.TlsSkipVerify,
		req.Msg.StaticStatusCode,
		req.Msg.StaticResponseHeaders,
		req.Msg.StaticResponseBody,
		req.Msg.StaticResponseBodyMode,
		req.Msg.StaticResponseTemplateId,
		req.Msg.UpstreamRequestHeaders,
		req.Msg.UpstreamBasicAuth,
		req.Msg.HealthCheck,
		req.Msg.UpstreamResponseHeaderTimeoutMillis,
	)
	if err != nil {
		return nil, err
	}
	if !req.Msg.Enabled {
		refs, err := a.DB.CountPublicBackendEnabledReferences(ctx, req.Msg.Id)
		if err != nil {
			return nil, connect.NewError(connect.CodeInternal, err)
		}
		if refs > 0 {
			return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("backend is referenced by enabled public config"))
		}
	}
	backend, storedHeaders, storedUpstreamHeaders, storedAgents, err := a.updatePublicBackendWithDetails(ctx, req.Msg.Id, params, headers, upstreamHeaders, agents)
	if err != nil {
		return nil, publicDBError(err)
	}
	if err := a.refreshPublicProxySnapshot(ctx); err != nil {
		return nil, err
	}
	return connect.NewResponse(&p2pstreamv1.UpdatePublicBackendResponse{Backend: publicBackendToProto(backend, storedHeaders, storedUpstreamHeaders, storedAgents, nil, a.BackendHealth)}), nil
}

func (a *App) DeletePublicBackend(
	ctx context.Context,
	req *connect.Request[p2pstreamv1.DeletePublicBackendRequest],
) (*connect.Response[p2pstreamv1.DeletePublicBackendResponse], error) {
	if _, err := a.requireAdmin(ctx, req.Header()); err != nil {
		return nil, err
	}
	refs, err := a.DB.CountPublicBackendEnabledReferences(ctx, req.Msg.Id)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	if refs > 0 {
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("backend is referenced by enabled public config"))
	}
	if err := a.DB.DeletePublicBackend(ctx, req.Msg.Id); err != nil {
		return nil, publicDBError(err)
	}
	if err := a.refreshPublicProxySnapshot(ctx); err != nil {
		return nil, err
	}
	return connect.NewResponse(&p2pstreamv1.DeletePublicBackendResponse{}), nil
}

func (a *App) createPublicBackendWithDetails(
	ctx context.Context,
	params publicBackendMutationInput,
	headers []publicBackendHeaderInput,
	upstreamHeaders []publicBackendUpstreamHeaderInput,
	agents []publicBackendAgentInput,
) (db.PublicBackend, []db.PublicBackendHeader, []db.PublicBackendUpstreamHeader, []db.PublicBackendAgent, error) {
	tx, err := a.DB.BeginTx(ctx, nil)
	if err != nil {
		return db.PublicBackend{}, nil, nil, nil, err
	}
	defer tx.Rollback()

	qtx := a.DB.WithTx(tx)
	backend, err := qtx.CreatePublicBackend(ctx, db.CreatePublicBackendParams{
		Name:                                params.Name,
		TargetOrigin:                        params.TargetOrigin,
		BackendType:                         params.BackendType,
		ForwardMode:                         params.ForwardMode,
		LoadBalancing:                       params.LoadBalancing,
		TlsSkipVerify:                       params.TLSSkipVerify,
		StaticStatusCode:                    params.StaticStatusCode,
		StaticResponseBody:                  params.StaticResponseBody,
		StaticResponseBodyMode:              params.StaticResponseBodyMode,
		StaticResponseTemplateID:            params.StaticResponseTemplateID,
		UpstreamBasicAuthEnabled:            params.UpstreamBasicAuthEnabled,
		UpstreamBasicAuthUsername:           params.UpstreamBasicAuthUsername,
		UpstreamBasicAuthPassword:           params.UpstreamBasicAuthPassword,
		UpstreamResponseHeaderTimeoutMillis: params.UpstreamResponseHeaderTimeout,
		HealthCheckEnabled:                  params.HealthCheckEnabled,
		HealthCheckMethod:                   params.HealthCheckMethod,
		HealthCheckPath:                     params.HealthCheckPath,
		HealthCheckIntervalMillis:           params.HealthCheckIntervalMillis,
		HealthCheckTimeoutMillis:            params.HealthCheckTimeoutMillis,
		HealthCheckHealthyThreshold:         params.HealthCheckHealthyThreshold,
		HealthCheckUnhealthyThreshold:       params.HealthCheckUnhealthyThreshold,
		HealthCheckExpectedStatusMin:        params.HealthCheckExpectedStatusMin,
		HealthCheckExpectedStatusMax:        params.HealthCheckExpectedStatusMax,
		Enabled:                             params.Enabled,
	})
	if err != nil {
		return db.PublicBackend{}, nil, nil, nil, err
	}
	storedHeaders, err := insertPublicBackendHeaders(ctx, qtx, backend.ID, headers)
	if err != nil {
		return db.PublicBackend{}, nil, nil, nil, err
	}
	storedUpstreamHeaders, err := insertPublicBackendUpstreamHeaders(ctx, qtx, backend.ID, upstreamHeaders)
	if err != nil {
		return db.PublicBackend{}, nil, nil, nil, err
	}
	storedAgents, err := insertPublicBackendAgents(ctx, qtx, backend.ID, agents)
	if err != nil {
		return db.PublicBackend{}, nil, nil, nil, err
	}
	if err := tx.Commit(); err != nil {
		return db.PublicBackend{}, nil, nil, nil, err
	}
	return backend, storedHeaders, storedUpstreamHeaders, storedAgents, nil
}

func (a *App) updatePublicBackendWithDetails(
	ctx context.Context,
	id int64,
	params publicBackendMutationInput,
	headers []publicBackendHeaderInput,
	upstreamHeaders []publicBackendUpstreamHeaderInput,
	agents []publicBackendAgentInput,
) (db.PublicBackend, []db.PublicBackendHeader, []db.PublicBackendUpstreamHeader, []db.PublicBackendAgent, error) {
	tx, err := a.DB.BeginTx(ctx, nil)
	if err != nil {
		return db.PublicBackend{}, nil, nil, nil, err
	}
	defer tx.Rollback()

	qtx := a.DB.WithTx(tx)
	backend, err := qtx.UpdatePublicBackend(ctx, db.UpdatePublicBackendParams{
		ID:                                  id,
		Name:                                params.Name,
		TargetOrigin:                        params.TargetOrigin,
		BackendType:                         params.BackendType,
		ForwardMode:                         params.ForwardMode,
		LoadBalancing:                       params.LoadBalancing,
		TlsSkipVerify:                       params.TLSSkipVerify,
		StaticStatusCode:                    params.StaticStatusCode,
		StaticResponseBody:                  params.StaticResponseBody,
		StaticResponseBodyMode:              params.StaticResponseBodyMode,
		StaticResponseTemplateID:            params.StaticResponseTemplateID,
		UpstreamBasicAuthEnabled:            params.UpstreamBasicAuthEnabled,
		UpstreamBasicAuthUsername:           params.UpstreamBasicAuthUsername,
		UpstreamBasicAuthPassword:           params.UpstreamBasicAuthPassword,
		UpstreamResponseHeaderTimeoutMillis: params.UpstreamResponseHeaderTimeout,
		HealthCheckEnabled:                  params.HealthCheckEnabled,
		HealthCheckMethod:                   params.HealthCheckMethod,
		HealthCheckPath:                     params.HealthCheckPath,
		HealthCheckIntervalMillis:           params.HealthCheckIntervalMillis,
		HealthCheckTimeoutMillis:            params.HealthCheckTimeoutMillis,
		HealthCheckHealthyThreshold:         params.HealthCheckHealthyThreshold,
		HealthCheckUnhealthyThreshold:       params.HealthCheckUnhealthyThreshold,
		HealthCheckExpectedStatusMin:        params.HealthCheckExpectedStatusMin,
		HealthCheckExpectedStatusMax:        params.HealthCheckExpectedStatusMax,
		Enabled:                             params.Enabled,
	})
	if err != nil {
		return db.PublicBackend{}, nil, nil, nil, err
	}
	if err := qtx.DeletePublicBackendHeaders(ctx, id); err != nil {
		return db.PublicBackend{}, nil, nil, nil, err
	}
	if err := qtx.DeletePublicBackendUpstreamHeaders(ctx, id); err != nil {
		return db.PublicBackend{}, nil, nil, nil, err
	}
	if err := qtx.DeletePublicBackendAgents(ctx, id); err != nil {
		return db.PublicBackend{}, nil, nil, nil, err
	}
	storedHeaders, err := insertPublicBackendHeaders(ctx, qtx, id, headers)
	if err != nil {
		return db.PublicBackend{}, nil, nil, nil, err
	}
	storedUpstreamHeaders, err := insertPublicBackendUpstreamHeaders(ctx, qtx, id, upstreamHeaders)
	if err != nil {
		return db.PublicBackend{}, nil, nil, nil, err
	}
	storedAgents, err := insertPublicBackendAgents(ctx, qtx, id, agents)
	if err != nil {
		return db.PublicBackend{}, nil, nil, nil, err
	}
	if err := tx.Commit(); err != nil {
		return db.PublicBackend{}, nil, nil, nil, err
	}
	return backend, storedHeaders, storedUpstreamHeaders, storedAgents, nil
}

func insertPublicBackendHeaders(
	ctx context.Context,
	queries *db.Queries,
	backendID int64,
	headers []publicBackendHeaderInput,
) ([]db.PublicBackendHeader, error) {
	storedHeaders := make([]db.PublicBackendHeader, 0, len(headers))
	for idx, header := range headers {
		stored, err := queries.CreatePublicBackendHeader(ctx, db.CreatePublicBackendHeaderParams{
			BackendID: backendID,
			Position:  int64(idx),
			Name:      header.Name,
			Value:     header.Value,
		})
		if err != nil {
			return nil, err
		}
		storedHeaders = append(storedHeaders, stored)
	}
	return storedHeaders, nil
}

func insertPublicBackendUpstreamHeaders(
	ctx context.Context,
	queries *db.Queries,
	backendID int64,
	headers []publicBackendUpstreamHeaderInput,
) ([]db.PublicBackendUpstreamHeader, error) {
	storedHeaders := make([]db.PublicBackendUpstreamHeader, 0, len(headers))
	for idx, header := range headers {
		stored, err := queries.CreatePublicBackendUpstreamHeader(ctx, db.CreatePublicBackendUpstreamHeaderParams{
			BackendID: backendID,
			Position:  int64(idx),
			Name:      header.Name,
			Value:     header.Value,
			Sensitive: header.Sensitive,
		})
		if err != nil {
			return nil, err
		}
		storedHeaders = append(storedHeaders, stored)
	}
	return storedHeaders, nil
}

func insertPublicBackendAgents(
	ctx context.Context,
	queries *db.Queries,
	backendID int64,
	agents []publicBackendAgentInput,
) ([]db.PublicBackendAgent, error) {
	storedAgents := make([]db.PublicBackendAgent, 0, len(agents))
	for idx, agent := range agents {
		stored, err := queries.CreatePublicBackendAgent(ctx, db.CreatePublicBackendAgentParams{
			BackendID: backendID,
			AgentID:   agent.AgentID,
			Position:  int64(idx),
			Weight:    agent.Weight,
			Enabled:   agent.Enabled,
		})
		if err != nil {
			return nil, err
		}
		storedAgents = append(storedAgents, stored)
	}
	return storedAgents, nil
}

func (a *App) CreatePublicListener(
	ctx context.Context,
	req *connect.Request[p2pstreamv1.CreatePublicListenerRequest],
) (*connect.Response[p2pstreamv1.CreatePublicListenerResponse], error) {
	if _, err := a.requireAdmin(ctx, req.Header()); err != nil {
		return nil, err
	}
	params, err := a.validatePublicListenerInput(ctx, req.Msg.Name, req.Msg.BindAddress, req.Msg.Port, req.Msg.Protocol, req.Msg.Enabled, req.Msg.DefaultBackendId, false)
	if err != nil {
		return nil, err
	}
	listener, err := a.DB.CreatePublicListener(ctx, db.CreatePublicListenerParams{
		Name:             params.Name,
		BindAddress:      params.BindAddress,
		Port:             params.Port,
		Protocol:         params.Protocol,
		Enabled:          params.Enabled,
		DefaultBackendID: params.DefaultBackendID,
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
	params, err := a.validatePublicListenerInput(ctx, req.Msg.Name, req.Msg.BindAddress, req.Msg.Port, req.Msg.Protocol, req.Msg.Enabled, req.Msg.DefaultBackendId, false)
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
	if _, err := a.validatePublicListenerInput(ctx, listener.Name, listener.BindAddress, listener.Port, protoProtocolFromString(listener.Protocol), true, listener.DefaultBackendID, true); err != nil {
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
	params, routeBackends, err := a.validatePublicRouteInput(
		ctx,
		req.Msg.ListenerId,
		req.Msg.Priority,
		req.Msg.HostPattern,
		req.Msg.PathPrefix,
		req.Msg.BackendId,
		req.Msg.BackendAssignments,
		req.Msg.LoadBalancing,
		req.Msg.FallbackBackendId,
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
	route, storedBackends, err := a.createPublicRouteWithBackends(ctx, params, routeBackends)
	if err != nil {
		return nil, publicDBError(err)
	}
	if err := a.refreshPublicProxySnapshot(ctx); err != nil {
		return nil, err
	}
	return connect.NewResponse(&p2pstreamv1.CreatePublicRouteResponse{Route: publicRouteToProto(route, storedBackends)}), nil
}

func (a *App) UpdatePublicRoute(
	ctx context.Context,
	req *connect.Request[p2pstreamv1.UpdatePublicRouteRequest],
) (*connect.Response[p2pstreamv1.UpdatePublicRouteResponse], error) {
	if _, err := a.requireAdmin(ctx, req.Header()); err != nil {
		return nil, err
	}
	params, routeBackends, err := a.validatePublicRouteInput(
		ctx,
		req.Msg.ListenerId,
		req.Msg.Priority,
		req.Msg.HostPattern,
		req.Msg.PathPrefix,
		req.Msg.BackendId,
		req.Msg.BackendAssignments,
		req.Msg.LoadBalancing,
		req.Msg.FallbackBackendId,
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
	route, storedBackends, err := a.updatePublicRouteWithBackends(ctx, params, routeBackends)
	if err != nil {
		return nil, publicDBError(err)
	}
	if err := a.refreshPublicProxySnapshot(ctx); err != nil {
		return nil, err
	}
	return connect.NewResponse(&p2pstreamv1.UpdatePublicRouteResponse{Route: publicRouteToProto(route, storedBackends)}), nil
}

func (a *App) createPublicRouteWithBackends(
	ctx context.Context,
	params db.UpdatePublicRouteParams,
	backends []publicRouteBackendInput,
) (db.PublicRoute, []db.PublicRouteBackend, error) {
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
		BackendID:                  params.BackendID,
		LoadBalancing:              params.LoadBalancing,
		FallbackBackendID:          params.FallbackBackendID,
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
	storedBackends, err := insertPublicRouteBackends(ctx, qtx, route.ID, backends)
	if err != nil {
		return db.PublicRoute{}, nil, err
	}
	if err := tx.Commit(); err != nil {
		return db.PublicRoute{}, nil, err
	}
	return route, storedBackends, nil
}

func (a *App) updatePublicRouteWithBackends(
	ctx context.Context,
	params db.UpdatePublicRouteParams,
	backends []publicRouteBackendInput,
) (db.PublicRoute, []db.PublicRouteBackend, error) {
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
	if err := qtx.DeletePublicRouteBackends(ctx, params.ID); err != nil {
		return db.PublicRoute{}, nil, err
	}
	storedBackends, err := insertPublicRouteBackends(ctx, qtx, params.ID, backends)
	if err != nil {
		return db.PublicRoute{}, nil, err
	}
	if err := tx.Commit(); err != nil {
		return db.PublicRoute{}, nil, err
	}
	return route, storedBackends, nil
}

func insertPublicRouteBackends(
	ctx context.Context,
	queries *db.Queries,
	routeID int64,
	backends []publicRouteBackendInput,
) ([]db.PublicRouteBackend, error) {
	storedBackends := make([]db.PublicRouteBackend, 0, len(backends))
	for idx, backend := range backends {
		stored, err := queries.CreatePublicRouteBackend(ctx, db.CreatePublicRouteBackendParams{
			RouteID:   routeID,
			BackendID: backend.BackendID,
			Position:  int64(idx),
			Weight:    backend.Weight,
			Enabled:   backend.Enabled,
		})
		if err != nil {
			return nil, err
		}
		storedBackends = append(storedBackends, stored)
	}
	return storedBackends, nil
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
	rows, err := a.loadPublicConfigRows(ctx)
	if err != nil {
		return nil, err
	}
	snap, err := snapshotFromPublicRows(rows)
	if err != nil {
		return nil, err
	}
	a.proxyMu.Lock()
	a.publicSnapshot = snap
	a.ensureListenerStatesLocked(snap)
	proxy := a.proxyStatusLocked()
	active := a.proxyServiceActive
	a.proxyMu.Unlock()
	a.LoadBalancers.reconcile(snap)
	if a.BackendHealth != nil {
		a.BackendHealth.reconcile(a, snap, active)
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

	return &p2pstreamv1.GetPublicProxyConfigResponse{
		Backends:            publicBackendsToProto(rows.Backends, rows.BackendHeaders, rows.BackendUpstreamHeaders, rows.BackendAgents, rows.Agents, a.BackendHealth),
		Listeners:           publicListenersToProto(rows.Listeners),
		Routes:              publicRoutesToProto(rows.Routes, rows.RouteBackends),
		RouteBackends:       publicRouteBackendsToProto(rows.RouteBackends),
		TlsCertificates:     publicTLSCertificatesToProto(rows.TLSCertificates),
		Proxy:               proxy,
		Agents:              a.publicAgentsToProto(ctx, rows.Agents),
		BackendAgents:       publicBackendAgentsToProto(rows.BackendAgents, publicAgentEnabledByID(rows.Agents), a.BackendHealth),
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
	a.proxyMu.Lock()
	a.publicSnapshot = snap
	a.ensureListenerStatesLocked(snap)
	a.proxyStatusLocked()
	active := a.proxyServiceActive
	a.proxyMu.Unlock()
	a.LoadBalancers.reconcile(snap)
	if a.BackendHealth != nil {
		a.BackendHealth.reconcile(a, snap, active)
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
	return nil
}

func (a *App) loadPublicProxySnapshot(ctx context.Context) (*publicProxySnapshot, error) {
	rows, err := a.loadPublicConfigRows(ctx)
	if err != nil {
		return nil, err
	}
	return snapshotFromPublicRows(rows)
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
	backends, err := a.DB.ListPublicBackends(ctx)
	if err != nil {
		return publicConfigRows{}, connect.NewError(connect.CodeInternal, err)
	}
	backendHeaders, err := a.DB.ListPublicBackendHeaders(ctx)
	if err != nil {
		return publicConfigRows{}, connect.NewError(connect.CodeInternal, err)
	}
	backendUpstreamHeaders, err := a.DB.ListPublicBackendUpstreamHeaders(ctx)
	if err != nil {
		return publicConfigRows{}, connect.NewError(connect.CodeInternal, err)
	}
	backendAgents, err := a.DB.ListPublicBackendAgents(ctx)
	if err != nil {
		return publicConfigRows{}, connect.NewError(connect.CodeInternal, err)
	}
	agents, err := a.DB.ListAgents(ctx)
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
	routeBackends, err := a.DB.ListPublicRouteBackends(ctx)
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
		Backends:               backends,
		BackendHeaders:         backendHeaders,
		BackendUpstreamHeaders: backendUpstreamHeaders,
		BackendAgents:          backendAgents,
		Agents:                 agents,
		Listeners:              listeners,
		Routes:                 routes,
		RouteBackends:          routeBackends,
		TLSCertificates:        certs,
		TLSDNSCredentials:      tlsDNSCredentials,
		RateLimitRules:         rateLimitRules,
		TrafficShaperRules:     trafficShaperRules,
		WafCaptchaProviders:    wafCaptchaProviders,
		WafRules:               wafRules,
		WafSettings:            wafSettings,
		CacheSettings:          cacheSettings,
		CacheRules:             cacheRules,
		ResponseTemplates:      responseTemplates,
	}, nil
}

func (a *App) ensurePublicProxySeeded(ctx context.Context) error {
	defaultTemplates, err := a.ensureDefaultPublicResponseTemplates(ctx)
	if err != nil {
		return err
	}
	backends, err := a.DB.CountPublicBackends(ctx)
	if err != nil {
		return connect.NewError(connect.CodeInternal, err)
	}
	listeners, err := a.DB.CountPublicListeners(ctx)
	if err != nil {
		return connect.NewError(connect.CodeInternal, err)
	}
	if backends > 0 || listeners > 0 {
		return nil
	}

	defaultWelcomeTemplate, ok := defaultTemplates["default-welcome"]
	if !ok || defaultWelcomeTemplate.ID <= 0 {
		return connect.NewError(connect.CodeInternal, errors.New("default welcome response template was not seeded"))
	}
	backend, err := a.DB.CreatePublicBackend(ctx, db.CreatePublicBackendParams{
		Name:                                "default",
		TargetOrigin:                        "",
		BackendType:                         publicBackendTypeStatic,
		ForwardMode:                         publicBackendForwardModeDirect,
		LoadBalancing:                       publicBackendLoadBalancingRoundRobin,
		TlsSkipVerify:                       0,
		StaticStatusCode:                    defaultStaticStatusCode,
		StaticResponseBody:                  defaultWelcomeBody,
		StaticResponseBodyMode:              publicResponseBodyModeTemplate,
		StaticResponseTemplateID:            sql.NullInt64{Int64: defaultWelcomeTemplate.ID, Valid: true},
		UpstreamBasicAuthEnabled:            0,
		UpstreamBasicAuthUsername:           "",
		UpstreamBasicAuthPassword:           "",
		UpstreamResponseHeaderTimeoutMillis: defaultBackendUpstreamResponseHeaderTimeoutMillis,
		HealthCheckEnabled:                  0,
		HealthCheckMethod:                   defaultBackendHealthCheckMethod,
		HealthCheckPath:                     defaultBackendHealthCheckPath,
		HealthCheckIntervalMillis:           defaultBackendHealthCheckIntervalMillis,
		HealthCheckTimeoutMillis:            defaultBackendHealthCheckTimeoutMillis,
		HealthCheckHealthyThreshold:         defaultBackendHealthCheckHealthyThreshold,
		HealthCheckUnhealthyThreshold:       defaultBackendHealthCheckUnhealthyThreshold,
		HealthCheckExpectedStatusMin:        defaultBackendHealthCheckExpectedStatusMin,
		HealthCheckExpectedStatusMax:        defaultBackendHealthCheckExpectedStatusMax,
		Enabled:                             1,
	})
	if err != nil {
		return publicDBError(err)
	}

	for idx, header := range []publicBackendHeaderInput{
		{Name: "Content-Type", Value: defaultWelcomeContentType},
		{Name: "X-Content-Type-Options", Value: "nosniff"},
		{Name: "Cache-Control", Value: defaultWelcomeCacheControl},
	} {
		if _, err := a.DB.CreatePublicBackendHeader(ctx, db.CreatePublicBackendHeaderParams{
			BackendID: backend.ID,
			Position:  int64(idx),
			Name:      header.Name,
			Value:     header.Value,
		}); err != nil {
			return publicDBError(err)
		}
	}

	httpListener, err := a.DB.CreatePublicListener(ctx, db.CreatePublicListenerParams{
		Name:             "public-http",
		BindAddress:      "",
		Port:             defaultPublicHTTPPort,
		Protocol:         publicListenerProtocolHTTP,
		Enabled:          1,
		DefaultBackendID: backend.ID,
	})
	if err != nil {
		return publicDBError(err)
	}

	httpsListener, err := a.DB.CreatePublicListener(ctx, db.CreatePublicListenerParams{
		Name:             "public-https",
		BindAddress:      "",
		Port:             443,
		Protocol:         publicListenerProtocolHTTPS,
		Enabled:          1,
		DefaultBackendID: backend.ID,
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
			BackendID:                  sql.NullInt64{Int64: backend.ID, Valid: true},
			LoadBalancing:              publicBackendLoadBalancingRoundRobin,
			FallbackBackendID:          sql.NullInt64{},
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
		if _, err := a.DB.CreatePublicRouteBackend(ctx, db.CreatePublicRouteBackendParams{
			RouteID:   route.ID,
			BackendID: backend.ID,
			Position:  0,
			Weight:    100,
			Enabled:   1,
		}); err != nil {
			return publicDBError(err)
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
		Backends:            make(map[int64]publicBackendConfig),
		Agents:              make(map[int64]publicAgentConfig),
		Listeners:           make(map[int64]publicListenerConfig),
		RoutesByListener:    make(map[int64][]publicRouteConfig),
		CertsByListener:     make(map[int64][]publicTLSCertificateConfig),
		WafCaptchaProviders: make(map[int64]publicWafCaptchaProviderConfig),
		WafCookieSecret:     []byte(rows.WafSettings.CookieSigningSecret),
		CacheSettings:       publicCacheSettingsRowToConfig(rows.CacheSettings),
		ResponseTemplates:   publicResponseTemplatesToConfig(rows.ResponseTemplates),
	}

	headersByBackend := publicBackendHeadersByBackend(rows.BackendHeaders)
	upstreamHeadersByBackend := publicBackendUpstreamHeadersByBackend(rows.BackendUpstreamHeaders)
	agentsByBackend := publicBackendAgentsByBackend(rows.BackendAgents)
	routeBackendsByRoute := publicRouteBackendsByRoute(rows.RouteBackends)
	for _, backend := range rows.Backends {
		backendType := normalizePublicBackendType(backend.BackendType)
		forwardMode := normalizePublicBackendForwardMode(backend.ForwardMode)
		loadBalancing := normalizePublicBackendLoadBalancing(backend.LoadBalancing)
		var parsed *url.URL
		if backendType == publicBackendTypeProxyForward {
			var err error
			parsed, err = parsePublicTargetOrigin(backend.TargetOrigin)
			if err != nil {
				return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("backend %q has invalid target origin: %w", backend.Name, err))
			}
		}
		staticResponseBody, err := effectiveGenericResponseBody(backend.StaticResponseBodyMode, backend.StaticResponseTemplateID, backend.StaticResponseBody, snap.ResponseTemplates)
		if err != nil {
			return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("backend %q has invalid static response template: %w", backend.Name, err))
		}
		snap.Backends[backend.ID] = publicBackendConfig{
			ID:                       backend.ID,
			Name:                     backend.Name,
			TargetOrigin:             backend.TargetOrigin,
			BackendType:              backendType,
			ForwardMode:              forwardMode,
			LoadBalancing:            loadBalancing,
			TLSSkipVerify:            backend.TlsSkipVerify != 0,
			StaticStatusCode:         int(backend.StaticStatusCode),
			StaticResponseHeaders:    publicBackendHeaderRowsToConfig(headersByBackend[backend.ID]),
			StaticResponseBody:       staticResponseBody,
			StaticResponseBodyMode:   normalizePublicResponseBodyMode(backend.StaticResponseBodyMode),
			StaticResponseTemplateID: nullInt64Value(backend.StaticResponseTemplateID),
			UpstreamRequestHeaders:   publicBackendUpstreamHeaderRowsToConfig(upstreamHeadersByBackend[backend.ID]),
			UpstreamBasicAuth: publicBackendBasicAuthConfig{
				Enabled:  backend.UpstreamBasicAuthEnabled != 0,
				Username: backend.UpstreamBasicAuthUsername,
				Password: backend.UpstreamBasicAuthPassword,
			},
			UpstreamResponseHeaderTimeout: time.Duration(normalizePublicBackendUpstreamResponseHeaderTimeoutMillis(backend.UpstreamResponseHeaderTimeoutMillis)) * time.Millisecond,
			Enabled:                       backend.Enabled != 0,
			ParsedOrigin:                  parsed,
			AgentAssignments:              publicBackendAgentRowsToConfig(agentsByBackend[backend.ID]),
			HealthCheck:                   publicBackendHealthCheckRowToConfig(backend),
		}
	}
	for _, agent := range rows.Agents {
		snap.Agents[agent.ID] = publicAgentConfig{
			ID:       agent.ID,
			PublicID: agent.PublicID,
			Name:     agent.Name,
			Enabled:  agent.Enabled != 0,
		}
	}
	for _, listener := range rows.Listeners {
		snap.Listeners[listener.ID] = publicListenerConfig{
			ID:               listener.ID,
			Name:             listener.Name,
			BindAddress:      listener.BindAddress,
			Port:             listener.Port,
			Protocol:         listener.Protocol,
			Enabled:          listener.Enabled != 0,
			DefaultBackendID: listener.DefaultBackendID,
		}
	}
	for _, route := range rows.Routes {
		backendID := int64(0)
		if route.BackendID.Valid {
			backendID = route.BackendID.Int64
		}
		fallbackBackendID := int64(0)
		if route.FallbackBackendID.Valid {
			fallbackBackendID = route.FallbackBackendID.Int64
		}
		snap.RoutesByListener[route.ListenerID] = append(snap.RoutesByListener[route.ListenerID], publicRouteConfig{
			ID:                         route.ID,
			ListenerID:                 route.ListenerID,
			Priority:                   route.Priority,
			HostPattern:                normalizeHostPattern(route.HostPattern),
			PathPrefix:                 route.PathPrefix,
			BackendID:                  backendID,
			LoadBalancing:              normalizePublicBackendLoadBalancing(route.LoadBalancing),
			FallbackBackendID:          fallbackBackendID,
			BackendAssignments:         publicRouteBackendRowsToConfig(routeBackendsByRoute[route.ID]),
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

func (a *App) validatePublicBackendInput(
	ctx context.Context,
	backendID int64,
	name string,
	targetOrigin string,
	enabled bool,
	backendType p2pstreamv1.PublicBackendType,
	forwardMode p2pstreamv1.PublicBackendForwardMode,
	loadBalancing p2pstreamv1.PublicBackendLoadBalancing,
	agentAssignments []*p2pstreamv1.PublicBackendAgent,
	tlsSkipVerify bool,
	staticStatusCode int64,
	staticResponseHeaders []*p2pstreamv1.PublicHeader,
	staticResponseBody string,
	staticResponseBodyMode p2pstreamv1.PublicResponseBodyMode,
	staticResponseTemplateID int64,
	upstreamRequestHeaders []*p2pstreamv1.PublicBackendUpstreamHeader,
	upstreamBasicAuth *p2pstreamv1.PublicBackendBasicAuth,
	healthCheck *p2pstreamv1.PublicBackendHealthCheck,
	upstreamResponseHeaderTimeoutMillis int64,
) (publicBackendMutationInput, []publicBackendHeaderInput, []publicBackendUpstreamHeaderInput, []publicBackendAgentInput, error) {
	name, err := normalizePublicName(name)
	if err != nil {
		return publicBackendMutationInput{}, nil, nil, nil, err
	}

	backendTypeString, err := backendTypeStringFromProto(backendType)
	if err != nil {
		return publicBackendMutationInput{}, nil, nil, nil, err
	}

	params := publicBackendMutationInput{
		Name:                          name,
		BackendType:                   backendTypeString,
		ForwardMode:                   publicBackendForwardModeDirect,
		LoadBalancing:                 publicBackendLoadBalancingRoundRobin,
		StaticStatusCode:              defaultStaticStatusCode,
		StaticResponseBody:            "",
		StaticResponseBodyMode:        publicResponseBodyModeInline,
		HealthCheckMethod:             defaultBackendHealthCheckMethod,
		HealthCheckPath:               defaultBackendHealthCheckPath,
		HealthCheckIntervalMillis:     defaultBackendHealthCheckIntervalMillis,
		HealthCheckTimeoutMillis:      defaultBackendHealthCheckTimeoutMillis,
		HealthCheckHealthyThreshold:   defaultBackendHealthCheckHealthyThreshold,
		HealthCheckUnhealthyThreshold: defaultBackendHealthCheckUnhealthyThreshold,
		HealthCheckExpectedStatusMin:  defaultBackendHealthCheckExpectedStatusMin,
		HealthCheckExpectedStatusMax:  defaultBackendHealthCheckExpectedStatusMax,
		UpstreamResponseHeaderTimeout: defaultBackendUpstreamResponseHeaderTimeoutMillis,
		Enabled:                       boolInt(enabled),
	}

	if backendTypeString == publicBackendTypeProxyForward {
		forwardModeString, err := forwardModeStringFromProto(forwardMode)
		if err != nil {
			return publicBackendMutationInput{}, nil, nil, nil, err
		}
		loadBalancingString, err := loadBalancingStringFromProto(loadBalancing)
		if err != nil {
			return publicBackendMutationInput{}, nil, nil, nil, err
		}
		targetOrigin = strings.TrimSpace(targetOrigin)
		if _, err := parsePublicTargetOrigin(targetOrigin); err != nil {
			return publicBackendMutationInput{}, nil, nil, nil, connect.NewError(connect.CodeInvalidArgument, err)
		}
		var agents []publicBackendAgentInput
		if forwardModeString == publicBackendForwardModeAgentPool {
			agents, err = a.validatePublicBackendAgentAssignments(ctx, agentAssignments)
			if err != nil {
				return publicBackendMutationInput{}, nil, nil, nil, err
			}
		}
		upstreamHeaders, err := a.validatePublicBackendUpstreamHeaders(ctx, backendID, upstreamRequestHeaders, upstreamBasicAuth != nil && upstreamBasicAuth.Enabled)
		if err != nil {
			return publicBackendMutationInput{}, nil, nil, nil, err
		}
		authEnabled, authUsername, authPassword, err := a.validatePublicBackendBasicAuth(ctx, backendID, upstreamBasicAuth)
		if err != nil {
			return publicBackendMutationInput{}, nil, nil, nil, err
		}
		health, err := validatePublicBackendHealthCheck(healthCheck)
		if err != nil {
			return publicBackendMutationInput{}, nil, nil, nil, err
		}
		params.TargetOrigin = targetOrigin
		params.ForwardMode = forwardModeString
		params.LoadBalancing = loadBalancingString
		params.TLSSkipVerify = boolInt(tlsSkipVerify)
		params.UpstreamBasicAuthEnabled = authEnabled
		params.UpstreamBasicAuthUsername = authUsername
		params.UpstreamBasicAuthPassword = authPassword
		timeoutMillis, err := validatePublicBackendUpstreamResponseHeaderTimeout(upstreamResponseHeaderTimeoutMillis)
		if err != nil {
			return publicBackendMutationInput{}, nil, nil, nil, err
		}
		params.UpstreamResponseHeaderTimeout = timeoutMillis
		applyPublicBackendHealthCheckInput(&params, health)
		return params, nil, upstreamHeaders, agents, nil
	}

	if staticStatusCode == 0 {
		staticStatusCode = defaultStaticStatusCode
	}
	if staticStatusCode < 100 || staticStatusCode > 599 {
		return publicBackendMutationInput{}, nil, nil, nil, connect.NewError(connect.CodeInvalidArgument, errors.New("static status code must be between 100 and 599"))
	}
	if !utf8.ValidString(staticResponseBody) {
		return publicBackendMutationInput{}, nil, nil, nil, connect.NewError(connect.CodeInvalidArgument, errors.New("static response body must be valid UTF-8"))
	}
	bodyMode, templateRef, err := a.validateGenericResponseTemplateReference(ctx, staticResponseBodyMode, staticResponseTemplateID)
	if err != nil {
		return publicBackendMutationInput{}, nil, nil, nil, err
	}
	headers, err := validatePublicStaticHeaders(staticResponseHeaders)
	if err != nil {
		return publicBackendMutationInput{}, nil, nil, nil, err
	}

	params.TargetOrigin = ""
	params.TLSSkipVerify = 0
	params.ForwardMode = publicBackendForwardModeDirect
	params.LoadBalancing = publicBackendLoadBalancingRoundRobin
	params.StaticStatusCode = staticStatusCode
	params.StaticResponseBody = staticResponseBody
	params.StaticResponseBodyMode = bodyMode
	params.StaticResponseTemplateID = templateRef
	params.UpstreamBasicAuthEnabled = 0
	params.UpstreamBasicAuthUsername = ""
	params.UpstreamBasicAuthPassword = ""
	params.HealthCheckEnabled = 0
	return params, headers, nil, nil, nil
}

func validatePublicBackendHealthCheck(input *p2pstreamv1.PublicBackendHealthCheck) (publicBackendHealthCheckConfig, error) {
	cfg := publicBackendHealthCheckConfig{
		Enabled:            false,
		Method:             defaultBackendHealthCheckMethod,
		Path:               defaultBackendHealthCheckPath,
		Interval:           time.Duration(defaultBackendHealthCheckIntervalMillis) * time.Millisecond,
		Timeout:            time.Duration(defaultBackendHealthCheckTimeoutMillis) * time.Millisecond,
		HealthyThreshold:   defaultBackendHealthCheckHealthyThreshold,
		UnhealthyThreshold: defaultBackendHealthCheckUnhealthyThreshold,
		ExpectedStatusMin:  defaultBackendHealthCheckExpectedStatusMin,
		ExpectedStatusMax:  defaultBackendHealthCheckExpectedStatusMax,
	}
	if input == nil {
		return cfg, nil
	}
	cfg.Enabled = input.Enabled
	method := strings.ToUpper(strings.TrimSpace(input.Method))
	if method == "" {
		method = defaultBackendHealthCheckMethod
	}
	if method != http.MethodGet && method != http.MethodHead {
		return publicBackendHealthCheckConfig{}, connect.NewError(connect.CodeInvalidArgument, errors.New("health check method must be GET or HEAD"))
	}
	path := strings.TrimSpace(input.Path)
	if path == "" {
		path = defaultBackendHealthCheckPath
	}
	if !strings.HasPrefix(path, "/") {
		return publicBackendHealthCheckConfig{}, connect.NewError(connect.CodeInvalidArgument, errors.New("health check path must start with /"))
	}
	intervalMillis := input.IntervalMillis
	if intervalMillis == 0 {
		intervalMillis = defaultBackendHealthCheckIntervalMillis
	}
	if intervalMillis < 1000 || intervalMillis > 3600000 {
		return publicBackendHealthCheckConfig{}, connect.NewError(connect.CodeInvalidArgument, errors.New("health check interval must be between 1000 and 3600000 milliseconds"))
	}
	timeoutMillis := input.TimeoutMillis
	if timeoutMillis == 0 {
		timeoutMillis = defaultBackendHealthCheckTimeoutMillis
	}
	if timeoutMillis < 100 || timeoutMillis > 30000 {
		return publicBackendHealthCheckConfig{}, connect.NewError(connect.CodeInvalidArgument, errors.New("health check timeout must be between 100 and 30000 milliseconds"))
	}
	if timeoutMillis > intervalMillis {
		return publicBackendHealthCheckConfig{}, connect.NewError(connect.CodeInvalidArgument, errors.New("health check timeout must not exceed interval"))
	}
	healthyThreshold := input.HealthyThreshold
	if healthyThreshold == 0 {
		healthyThreshold = defaultBackendHealthCheckHealthyThreshold
	}
	unhealthyThreshold := input.UnhealthyThreshold
	if unhealthyThreshold == 0 {
		unhealthyThreshold = defaultBackendHealthCheckUnhealthyThreshold
	}
	if healthyThreshold < 1 || healthyThreshold > 10 || unhealthyThreshold < 1 || unhealthyThreshold > 10 {
		return publicBackendHealthCheckConfig{}, connect.NewError(connect.CodeInvalidArgument, errors.New("health check thresholds must be between 1 and 10"))
	}
	statusMin := input.ExpectedStatusMin
	if statusMin == 0 {
		statusMin = defaultBackendHealthCheckExpectedStatusMin
	}
	statusMax := input.ExpectedStatusMax
	if statusMax == 0 {
		statusMax = defaultBackendHealthCheckExpectedStatusMax
	}
	if statusMin < 100 || statusMax > 599 || statusMin > statusMax {
		return publicBackendHealthCheckConfig{}, connect.NewError(connect.CodeInvalidArgument, errors.New("health check expected status range must be between 100 and 599"))
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

func validatePublicBackendUpstreamResponseHeaderTimeout(timeoutMillis int64) (int64, error) {
	if timeoutMillis == 0 {
		return defaultBackendUpstreamResponseHeaderTimeoutMillis, nil
	}
	if timeoutMillis < minBackendUpstreamResponseHeaderTimeoutMillis || timeoutMillis > maxBackendUpstreamResponseHeaderTimeoutMillis {
		return 0, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf(
			"upstream response header timeout must be between %d and %d milliseconds",
			minBackendUpstreamResponseHeaderTimeoutMillis,
			maxBackendUpstreamResponseHeaderTimeoutMillis,
		))
	}
	return timeoutMillis, nil
}

func normalizePublicBackendUpstreamResponseHeaderTimeoutMillis(timeoutMillis int64) int64 {
	if timeoutMillis < minBackendUpstreamResponseHeaderTimeoutMillis || timeoutMillis > maxBackendUpstreamResponseHeaderTimeoutMillis {
		return defaultBackendUpstreamResponseHeaderTimeoutMillis
	}
	return timeoutMillis
}

func applyPublicBackendHealthCheckInput(params *publicBackendMutationInput, cfg publicBackendHealthCheckConfig) {
	params.HealthCheckEnabled = boolInt(cfg.Enabled)
	params.HealthCheckMethod = cfg.Method
	params.HealthCheckPath = cfg.Path
	params.HealthCheckIntervalMillis = cfg.Interval.Milliseconds()
	params.HealthCheckTimeoutMillis = cfg.Timeout.Milliseconds()
	params.HealthCheckHealthyThreshold = cfg.HealthyThreshold
	params.HealthCheckUnhealthyThreshold = cfg.UnhealthyThreshold
	params.HealthCheckExpectedStatusMin = cfg.ExpectedStatusMin
	params.HealthCheckExpectedStatusMax = cfg.ExpectedStatusMax
}

func (a *App) validatePublicBackendAgentAssignments(ctx context.Context, assignments []*p2pstreamv1.PublicBackendAgent) ([]publicBackendAgentInput, error) {
	if len(assignments) == 0 {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("agent backend requires at least one agent assignment"))
	}
	seenAgents := make(map[int64]struct{}, len(assignments))
	resp := make([]publicBackendAgentInput, 0, len(assignments))
	enabledAgents := 0
	for idx, assignment := range assignments {
		if assignment == nil {
			continue
		}
		agentID := assignment.AgentId
		if agentID <= 0 {
			return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("agent assignment requires an agent id"))
		}
		if _, ok := seenAgents[agentID]; ok {
			return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("agent assignments must not include the same agent twice"))
		}
		seenAgents[agentID] = struct{}{}
		weight := assignment.Weight
		if weight == 0 {
			weight = 100
		}
		if weight < 1 || weight > 1000 {
			return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("agent assignment weight must be between 1 and 1000"))
		}
		agent, err := a.DB.GetAgent(ctx, agentID)
		if err != nil {
			return nil, publicDBError(err)
		}
		if assignment.Enabled {
			if agent.Enabled == 0 {
				return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("enabled backend assignment requires an enabled agent"))
			}
			enabledAgents++
		}
		resp = append(resp, publicBackendAgentInput{
			AgentID:  agentID,
			Position: int64(idx),
			Weight:   weight,
			Enabled:  boolInt(assignment.Enabled),
		})
	}
	if enabledAgents == 0 {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("agent backend requires at least one enabled agent assignment"))
	}
	return resp, nil
}

func (a *App) validatePublicRouteBackendAssignments(
	ctx context.Context,
	legacyBackendID int64,
	assignments []*p2pstreamv1.PublicRouteBackend,
) ([]publicRouteBackendInput, error) {
	if len(assignments) == 0 && legacyBackendID > 0 {
		assignments = []*p2pstreamv1.PublicRouteBackend{{
			BackendId: legacyBackendID,
			Position:  0,
			Weight:    100,
			Enabled:   true,
		}}
	}
	if len(assignments) == 0 {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("forwarding route requires at least one backend assignment"))
	}
	seenBackends := make(map[int64]struct{}, len(assignments))
	resp := make([]publicRouteBackendInput, 0, len(assignments))
	for idx, assignment := range assignments {
		if assignment == nil {
			continue
		}
		backendID := assignment.BackendId
		if backendID <= 0 {
			return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("route backend assignment requires a backend id"))
		}
		if _, ok := seenBackends[backendID]; ok {
			return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("route backend assignments must not include the same backend twice"))
		}
		seenBackends[backendID] = struct{}{}
		if _, err := a.DB.GetPublicBackend(ctx, backendID); err != nil {
			return nil, publicDBError(err)
		}
		weight := assignment.Weight
		if weight == 0 {
			weight = 100
		}
		if weight < 1 || weight > 1000 {
			return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("route backend assignment weight must be between 1 and 1000"))
		}
		resp = append(resp, publicRouteBackendInput{
			BackendID: backendID,
			Position:  int64(idx),
			Weight:    weight,
			Enabled:   boolInt(assignment.Enabled),
		})
	}
	if len(resp) == 0 {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("forwarding route requires at least one backend assignment"))
	}
	return resp, nil
}

func (a *App) validatePublicListenerInput(
	ctx context.Context,
	name string,
	bindAddress string,
	port int64,
	protocol p2pstreamv1.PublicListenerProtocol,
	enabled bool,
	defaultBackendID int64,
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
	backend, err := a.DB.GetPublicBackend(ctx, defaultBackendID)
	if err != nil {
		return db.UpdatePublicListenerParams{}, publicDBError(err)
	}
	if enabled && backend.Enabled == 0 {
		return db.UpdatePublicListenerParams{}, connect.NewError(connect.CodeFailedPrecondition, errors.New("enabled listener requires an enabled default backend"))
	}
	return db.UpdatePublicListenerParams{
		Name:             name,
		BindAddress:      bindAddress,
		Port:             port,
		Protocol:         protocolString,
		Enabled:          boolInt(enabled),
		DefaultBackendID: defaultBackendID,
	}, nil
}

func (a *App) validatePublicRouteInput(
	ctx context.Context,
	listenerID int64,
	priority int64,
	hostPattern string,
	pathPrefix string,
	backendID int64,
	backendAssignments []*p2pstreamv1.PublicRouteBackend,
	loadBalancing p2pstreamv1.PublicBackendLoadBalancing,
	fallbackBackendID int64,
	enabled bool,
	action p2pstreamv1.PublicRouteAction,
	redirectTargetMode p2pstreamv1.PublicRouteRedirectTargetMode,
	redirectTarget string,
	redirectStatusCode int64,
	redirectPreservePathSuffix bool,
	redirectPreserveQuery bool,
) (db.UpdatePublicRouteParams, []publicRouteBackendInput, error) {
	if _, err := a.DB.GetPublicListener(ctx, listenerID); err != nil {
		return db.UpdatePublicRouteParams{}, nil, publicDBError(err)
	}
	hostPattern = normalizeHostPattern(hostPattern)
	pathPrefix = strings.TrimSpace(pathPrefix)
	if hostPattern == "" && pathPrefix == "" {
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
	backendIDParam := sql.NullInt64{}
	fallbackBackendIDParam := sql.NullInt64{}
	loadBalancingString := publicBackendLoadBalancingRoundRobin
	var routeBackends []publicRouteBackendInput
	redirectMode := ""
	redirectTarget = strings.TrimSpace(redirectTarget)
	if redirectStatusCode == 0 {
		redirectStatusCode = defaultRedirectStatusCode
	}
	if actionString == publicRouteActionForward {
		loadBalancingString, err = loadBalancingStringFromProto(loadBalancing)
		if err != nil {
			return db.UpdatePublicRouteParams{}, nil, err
		}
		routeBackends, err = a.validatePublicRouteBackendAssignments(ctx, backendID, backendAssignments)
		if err != nil {
			return db.UpdatePublicRouteParams{}, nil, err
		}
		backendIDParam = sql.NullInt64{Int64: routeBackends[0].BackendID, Valid: true}
		if fallbackBackendID > 0 {
			if _, err := a.DB.GetPublicBackend(ctx, fallbackBackendID); err != nil {
				return db.UpdatePublicRouteParams{}, nil, publicDBError(err)
			}
			fallbackBackendIDParam = sql.NullInt64{Int64: fallbackBackendID, Valid: true}
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
		BackendID:                  backendIDParam,
		LoadBalancing:              loadBalancingString,
		FallbackBackendID:          fallbackBackendIDParam,
		Action:                     actionString,
		RedirectTargetMode:         redirectMode,
		RedirectTarget:             redirectTarget,
		RedirectStatusCode:         redirectStatusCode,
		RedirectPreservePathSuffix: boolInt(redirectPreservePathSuffix),
		RedirectPreserveQuery:      boolInt(redirectPreserveQuery),
		Enabled:                    boolInt(enabled),
	}, routeBackends, nil
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

func validatePublicStaticHeaders(headers []*p2pstreamv1.PublicHeader) ([]publicBackendHeaderInput, error) {
	resp := make([]publicBackendHeaderInput, 0, len(headers))
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
		resp = append(resp, publicBackendHeaderInput{Name: name, Value: header.Value})
	}
	return resp, nil
}

func (a *App) validatePublicBackendUpstreamHeaders(
	ctx context.Context,
	backendID int64,
	headers []*p2pstreamv1.PublicBackendUpstreamHeader,
	basicAuthEnabled bool,
) ([]publicBackendUpstreamHeaderInput, error) {
	existingHeaders := map[int64]db.PublicBackendUpstreamHeader{}
	if backendID > 0 {
		rows, err := a.DB.ListPublicBackendUpstreamHeadersByBackend(ctx, backendID)
		if err != nil {
			return nil, publicDBError(err)
		}
		for _, row := range rows {
			existingHeaders[row.ID] = row
		}
	}

	resp := make([]publicBackendUpstreamHeaderInput, 0, len(headers))
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
			existing, ok := existingHeaders[header.Id]
			if !ok || existing.BackendID != backendID || existing.Sensitive == 0 {
				return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("sensitive upstream request header %q requires a value", name))
			}
			value = existing.Value
		}
		if err := validateUpstreamHeaderValue(name, value); err != nil {
			return nil, err
		}
		resp = append(resp, publicBackendUpstreamHeaderInput{
			ID:        header.Id,
			Name:      name,
			Value:     value,
			Sensitive: boolInt(sensitive),
		})
	}
	return resp, nil
}

func (a *App) validatePublicBackendBasicAuth(
	ctx context.Context,
	backendID int64,
	auth *p2pstreamv1.PublicBackendBasicAuth,
) (int64, string, string, error) {
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
	if auth.PasswordSet {
		if password == "" {
			return 0, "", "", connect.NewError(connect.CodeInvalidArgument, errors.New("upstream basic auth password is required"))
		}
	} else if backendID > 0 {
		existing, err := a.DB.GetPublicBackend(ctx, backendID)
		if err != nil {
			return 0, "", "", publicDBError(err)
		}
		if existing.UpstreamBasicAuthEnabled == 0 || existing.UpstreamBasicAuthPassword == "" {
			return 0, "", "", connect.NewError(connect.CodeInvalidArgument, errors.New("upstream basic auth password is required"))
		}
		password = existing.UpstreamBasicAuthPassword
	} else {
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

func backendTypeStringFromProto(backendType p2pstreamv1.PublicBackendType) (string, error) {
	switch backendType {
	case p2pstreamv1.PublicBackendType_PUBLIC_BACKEND_TYPE_UNSPECIFIED,
		p2pstreamv1.PublicBackendType_PUBLIC_BACKEND_TYPE_PROXY_FORWARD:
		return publicBackendTypeProxyForward, nil
	case p2pstreamv1.PublicBackendType_PUBLIC_BACKEND_TYPE_STATIC:
		return publicBackendTypeStatic, nil
	default:
		return "", connect.NewError(connect.CodeInvalidArgument, errors.New("backend type must be proxy_forward or static"))
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

func forwardModeStringFromProto(forwardMode p2pstreamv1.PublicBackendForwardMode) (string, error) {
	switch forwardMode {
	case p2pstreamv1.PublicBackendForwardMode_PUBLIC_BACKEND_FORWARD_MODE_UNSPECIFIED,
		p2pstreamv1.PublicBackendForwardMode_PUBLIC_BACKEND_FORWARD_MODE_DIRECT:
		return publicBackendForwardModeDirect, nil
	case p2pstreamv1.PublicBackendForwardMode_PUBLIC_BACKEND_FORWARD_MODE_AGENT_POOL:
		return publicBackendForwardModeAgentPool, nil
	default:
		return "", connect.NewError(connect.CodeInvalidArgument, errors.New("backend forward mode must be direct or agent_pool"))
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

func loadBalancingStringFromProto(loadBalancing p2pstreamv1.PublicBackendLoadBalancing) (string, error) {
	switch loadBalancing {
	case p2pstreamv1.PublicBackendLoadBalancing_PUBLIC_BACKEND_LOAD_BALANCING_UNSPECIFIED,
		p2pstreamv1.PublicBackendLoadBalancing_PUBLIC_BACKEND_LOAD_BALANCING_ROUND_ROBIN:
		return publicBackendLoadBalancingRoundRobin, nil
	case p2pstreamv1.PublicBackendLoadBalancing_PUBLIC_BACKEND_LOAD_BALANCING_WEIGHTED_ROUND_ROBIN:
		return publicBackendLoadBalancingWeightedRoundRobin, nil
	case p2pstreamv1.PublicBackendLoadBalancing_PUBLIC_BACKEND_LOAD_BALANCING_RANDOM:
		return publicBackendLoadBalancingRandom, nil
	case p2pstreamv1.PublicBackendLoadBalancing_PUBLIC_BACKEND_LOAD_BALANCING_WEIGHTED_RANDOM:
		return publicBackendLoadBalancingWeightedRandom, nil
	case p2pstreamv1.PublicBackendLoadBalancing_PUBLIC_BACKEND_LOAD_BALANCING_LEAST_ACTIVE_REQUESTS:
		return publicBackendLoadBalancingLeastActiveRequests, nil
	case p2pstreamv1.PublicBackendLoadBalancing_PUBLIC_BACKEND_LOAD_BALANCING_WEIGHTED_LEAST_ACTIVE_REQUESTS:
		return publicBackendLoadBalancingWeightedLeastActiveRequests, nil
	default:
		return "", connect.NewError(connect.CodeInvalidArgument, errors.New("unsupported backend load balancing algorithm"))
	}
}

func normalizePublicBackendType(backendType string) string {
	switch strings.TrimSpace(strings.ToLower(backendType)) {
	case "", publicBackendTypeProxyForward:
		return publicBackendTypeProxyForward
	case publicBackendTypeStatic:
		return publicBackendTypeStatic
	default:
		return strings.TrimSpace(strings.ToLower(backendType))
	}
}

func normalizePublicBackendForwardMode(forwardMode string) string {
	switch strings.TrimSpace(strings.ToLower(forwardMode)) {
	case "", publicBackendForwardModeDirect:
		return publicBackendForwardModeDirect
	case publicBackendForwardModeAgentPool:
		return publicBackendForwardModeAgentPool
	default:
		return strings.TrimSpace(strings.ToLower(forwardMode))
	}
}

func normalizePublicBackendLoadBalancing(loadBalancing string) string {
	switch strings.TrimSpace(strings.ToLower(loadBalancing)) {
	case "", publicBackendLoadBalancingRoundRobin:
		return publicBackendLoadBalancingRoundRobin
	case publicBackendLoadBalancingWeightedRoundRobin:
		return publicBackendLoadBalancingWeightedRoundRobin
	case publicBackendLoadBalancingRandom:
		return publicBackendLoadBalancingRandom
	case publicBackendLoadBalancingWeightedRandom:
		return publicBackendLoadBalancingWeightedRandom
	case publicBackendLoadBalancingLeastActiveRequests:
		return publicBackendLoadBalancingLeastActiveRequests
	case publicBackendLoadBalancingWeightedLeastActiveRequests:
		return publicBackendLoadBalancingWeightedLeastActiveRequests
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

func protoBackendTypeFromString(backendType string) p2pstreamv1.PublicBackendType {
	switch normalizePublicBackendType(backendType) {
	case publicBackendTypeProxyForward:
		return p2pstreamv1.PublicBackendType_PUBLIC_BACKEND_TYPE_PROXY_FORWARD
	case publicBackendTypeStatic:
		return p2pstreamv1.PublicBackendType_PUBLIC_BACKEND_TYPE_STATIC
	default:
		return p2pstreamv1.PublicBackendType_PUBLIC_BACKEND_TYPE_UNSPECIFIED
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

func protoForwardModeFromString(forwardMode string) p2pstreamv1.PublicBackendForwardMode {
	switch normalizePublicBackendForwardMode(forwardMode) {
	case publicBackendForwardModeAgentPool:
		return p2pstreamv1.PublicBackendForwardMode_PUBLIC_BACKEND_FORWARD_MODE_AGENT_POOL
	case publicBackendForwardModeDirect:
		return p2pstreamv1.PublicBackendForwardMode_PUBLIC_BACKEND_FORWARD_MODE_DIRECT
	default:
		return p2pstreamv1.PublicBackendForwardMode_PUBLIC_BACKEND_FORWARD_MODE_UNSPECIFIED
	}
}

func protoLoadBalancingFromString(loadBalancing string) p2pstreamv1.PublicBackendLoadBalancing {
	switch normalizePublicBackendLoadBalancing(loadBalancing) {
	case publicBackendLoadBalancingWeightedRoundRobin:
		return p2pstreamv1.PublicBackendLoadBalancing_PUBLIC_BACKEND_LOAD_BALANCING_WEIGHTED_ROUND_ROBIN
	case publicBackendLoadBalancingRandom:
		return p2pstreamv1.PublicBackendLoadBalancing_PUBLIC_BACKEND_LOAD_BALANCING_RANDOM
	case publicBackendLoadBalancingWeightedRandom:
		return p2pstreamv1.PublicBackendLoadBalancing_PUBLIC_BACKEND_LOAD_BALANCING_WEIGHTED_RANDOM
	case publicBackendLoadBalancingLeastActiveRequests:
		return p2pstreamv1.PublicBackendLoadBalancing_PUBLIC_BACKEND_LOAD_BALANCING_LEAST_ACTIVE_REQUESTS
	case publicBackendLoadBalancingWeightedLeastActiveRequests:
		return p2pstreamv1.PublicBackendLoadBalancing_PUBLIC_BACKEND_LOAD_BALANCING_WEIGHTED_LEAST_ACTIVE_REQUESTS
	case publicBackendLoadBalancingRoundRobin:
		return p2pstreamv1.PublicBackendLoadBalancing_PUBLIC_BACKEND_LOAD_BALANCING_ROUND_ROBIN
	default:
		return p2pstreamv1.PublicBackendLoadBalancing_PUBLIC_BACKEND_LOAD_BALANCING_UNSPECIFIED
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

func publicBackendToProto(backend db.PublicBackend, headers []db.PublicBackendHeader, upstreamHeaders []db.PublicBackendUpstreamHeader, agents []db.PublicBackendAgent, agentEnabled map[int64]bool, monitor *publicBackendHealthMonitor) *p2pstreamv1.PublicBackend {
	var healthSnapshot *publicBackendHealthSnapshot
	if monitor != nil {
		healthSnapshot = monitor.snapshot(publicBackendHealthDBAdapter{id: backend.ID, enabled: backend.Enabled != 0})
	}
	return &p2pstreamv1.PublicBackend{
		Id:                                  backend.ID,
		Name:                                backend.Name,
		TargetOrigin:                        backend.TargetOrigin,
		Enabled:                             backend.Enabled != 0,
		CreatedAtUnixMillis:                 backend.CreatedAt.UnixMilli(),
		UpdatedAtUnixMillis:                 backend.UpdatedAt.UnixMilli(),
		BackendType:                         protoBackendTypeFromString(backend.BackendType),
		ForwardMode:                         protoForwardModeFromString(backend.ForwardMode),
		LoadBalancing:                       protoLoadBalancingFromString(backend.LoadBalancing),
		TlsSkipVerify:                       backend.TlsSkipVerify != 0,
		StaticStatusCode:                    backend.StaticStatusCode,
		StaticResponseHeaders:               publicBackendHeaderRowsToProto(headers),
		StaticResponseBody:                  backend.StaticResponseBody,
		StaticResponseBodyMode:              protoPublicResponseBodyMode(backend.StaticResponseBodyMode),
		StaticResponseTemplateId:            nullInt64Value(backend.StaticResponseTemplateID),
		AgentAssignments:                    publicBackendAgentsToProto(agents, agentEnabled, monitor),
		UpstreamRequestHeaders:              publicBackendUpstreamHeaderRowsToProto(upstreamHeaders),
		UpstreamBasicAuth:                   publicBackendBasicAuthToProto(backend),
		HealthCheck:                         publicBackendHealthCheckToProto(backend, healthSnapshot),
		UpstreamResponseHeaderTimeoutMillis: normalizePublicBackendUpstreamResponseHeaderTimeoutMillis(backend.UpstreamResponseHeaderTimeoutMillis),
	}
}

func publicBackendHealthCheckToProto(backend db.PublicBackend, status *publicBackendHealthSnapshot) *p2pstreamv1.PublicBackendHealthCheck {
	resp := &p2pstreamv1.PublicBackendHealthCheck{
		Enabled:            backend.HealthCheckEnabled != 0,
		Method:             backend.HealthCheckMethod,
		Path:               backend.HealthCheckPath,
		IntervalMillis:     backend.HealthCheckIntervalMillis,
		TimeoutMillis:      backend.HealthCheckTimeoutMillis,
		HealthyThreshold:   backend.HealthCheckHealthyThreshold,
		UnhealthyThreshold: backend.HealthCheckUnhealthyThreshold,
		ExpectedStatusMin:  backend.HealthCheckExpectedStatusMin,
		ExpectedStatusMax:  backend.HealthCheckExpectedStatusMax,
		Status:             p2pstreamv1.PublicBackendHealthStatus_PUBLIC_BACKEND_HEALTH_STATUS_UNKNOWN,
	}
	if backend.Enabled == 0 {
		resp.Status = p2pstreamv1.PublicBackendHealthStatus_PUBLIC_BACKEND_HEALTH_STATUS_DISABLED
	}
	if status != nil {
		resp.Status = status.Status
		resp.LastCheckedAtUnixMillis = status.LastCheckedAtUnixMillis
		resp.LastError = status.LastError
		resp.PassiveUnhealthyUntilUnixMillis = status.PassiveUnhealthyUntilUnixMillis
	}
	if !resp.Enabled && backend.Enabled != 0 && status == nil {
		resp.Status = p2pstreamv1.PublicBackendHealthStatus_PUBLIC_BACKEND_HEALTH_STATUS_UNKNOWN
	}
	return resp
}

func publicBackendsToProto(backends []db.PublicBackend, headers []db.PublicBackendHeader, upstreamHeaders []db.PublicBackendUpstreamHeader, agents []db.PublicBackendAgent, allAgents []db.Agent, monitor *publicBackendHealthMonitor) []*p2pstreamv1.PublicBackend {
	headersByBackend := publicBackendHeadersByBackend(headers)
	upstreamHeadersByBackend := publicBackendUpstreamHeadersByBackend(upstreamHeaders)
	agentsByBackend := publicBackendAgentsByBackend(agents)
	agentEnabled := publicAgentEnabledByID(allAgents)
	resp := make([]*p2pstreamv1.PublicBackend, 0, len(backends))
	for _, backend := range backends {
		resp = append(resp, publicBackendToProto(backend, headersByBackend[backend.ID], upstreamHeadersByBackend[backend.ID], agentsByBackend[backend.ID], agentEnabled, monitor))
	}
	return resp
}

func publicBackendHeadersByBackend(headers []db.PublicBackendHeader) map[int64][]db.PublicBackendHeader {
	resp := make(map[int64][]db.PublicBackendHeader)
	for _, header := range headers {
		resp[header.BackendID] = append(resp[header.BackendID], header)
	}
	return resp
}

func publicBackendHeaderRowsToProto(headers []db.PublicBackendHeader) []*p2pstreamv1.PublicHeader {
	resp := make([]*p2pstreamv1.PublicHeader, 0, len(headers))
	for _, header := range headers {
		resp = append(resp, &p2pstreamv1.PublicHeader{Name: header.Name, Value: header.Value})
	}
	return resp
}

func publicBackendHeaderRowsToConfig(headers []db.PublicBackendHeader) []publicResponseHeader {
	resp := make([]publicResponseHeader, 0, len(headers))
	for _, header := range headers {
		resp = append(resp, publicResponseHeader{Name: header.Name, Value: header.Value})
	}
	return resp
}

func publicBackendUpstreamHeadersByBackend(headers []db.PublicBackendUpstreamHeader) map[int64][]db.PublicBackendUpstreamHeader {
	resp := make(map[int64][]db.PublicBackendUpstreamHeader)
	for _, header := range headers {
		resp[header.BackendID] = append(resp[header.BackendID], header)
	}
	return resp
}

func publicBackendUpstreamHeaderRowsToProto(headers []db.PublicBackendUpstreamHeader) []*p2pstreamv1.PublicBackendUpstreamHeader {
	resp := make([]*p2pstreamv1.PublicBackendUpstreamHeader, 0, len(headers))
	for _, header := range headers {
		value := header.Value
		if header.Sensitive != 0 {
			value = ""
		}
		resp = append(resp, &p2pstreamv1.PublicBackendUpstreamHeader{
			Id:        header.ID,
			BackendId: header.BackendID,
			Name:      header.Name,
			Value:     value,
			Sensitive: header.Sensitive != 0,
			ValueSet:  true,
			Position:  header.Position,
		})
	}
	return resp
}

func publicBackendUpstreamHeaderRowsToConfig(headers []db.PublicBackendUpstreamHeader) []publicRequestHeader {
	resp := make([]publicRequestHeader, 0, len(headers))
	for _, header := range headers {
		resp = append(resp, publicRequestHeader{Name: header.Name, Value: header.Value, Sensitive: header.Sensitive != 0})
	}
	return resp
}

func publicBackendBasicAuthToProto(backend db.PublicBackend) *p2pstreamv1.PublicBackendBasicAuth {
	if backend.UpstreamBasicAuthEnabled == 0 {
		return &p2pstreamv1.PublicBackendBasicAuth{}
	}
	return &p2pstreamv1.PublicBackendBasicAuth{
		Enabled:     true,
		Username:    backend.UpstreamBasicAuthUsername,
		Password:    "",
		PasswordSet: backend.UpstreamBasicAuthPassword != "",
	}
}

func publicBackendAgentsByBackend(agents []db.PublicBackendAgent) map[int64][]db.PublicBackendAgent {
	resp := make(map[int64][]db.PublicBackendAgent)
	for _, agent := range agents {
		resp[agent.BackendID] = append(resp[agent.BackendID], agent)
	}
	return resp
}

func publicBackendHealthCheckRowToConfig(backend db.PublicBackend) publicBackendHealthCheckConfig {
	return publicBackendHealthCheckConfig{
		Enabled:            backend.HealthCheckEnabled != 0,
		Method:             backend.HealthCheckMethod,
		Path:               backend.HealthCheckPath,
		Interval:           time.Duration(backend.HealthCheckIntervalMillis) * time.Millisecond,
		Timeout:            time.Duration(backend.HealthCheckTimeoutMillis) * time.Millisecond,
		HealthyThreshold:   backend.HealthCheckHealthyThreshold,
		UnhealthyThreshold: backend.HealthCheckUnhealthyThreshold,
		ExpectedStatusMin:  backend.HealthCheckExpectedStatusMin,
		ExpectedStatusMax:  backend.HealthCheckExpectedStatusMax,
	}
}

func publicAgentEnabledByID(agents []db.Agent) map[int64]bool {
	resp := make(map[int64]bool, len(agents))
	for _, agent := range agents {
		resp[agent.ID] = agent.Enabled != 0
	}
	return resp
}

func publicBackendAgentsToProto(agents []db.PublicBackendAgent, agentEnabled map[int64]bool, monitor *publicBackendHealthMonitor) []*p2pstreamv1.PublicBackendAgent {
	resp := make([]*p2pstreamv1.PublicBackendAgent, 0, len(agents))
	for _, agent := range agents {
		enabled := agent.Enabled != 0
		configEnabled, ok := agentEnabled[agent.AgentID]
		if agentEnabled == nil {
			configEnabled = true
		} else if !ok {
			configEnabled = false
		}
		resp = append(resp, &p2pstreamv1.PublicBackendAgent{
			BackendId: agent.BackendID,
			AgentId:   agent.AgentID,
			Position:  agent.Position,
			Weight:    agent.Weight,
			Enabled:   enabled,
			Health:    publicBackendAgentHealthToProto(agent.BackendID, agent.AgentID, enabled, configEnabled, monitor),
		})
	}
	return resp
}

func publicBackendAgentHealthToProto(backendID int64, agentID int64, assignmentEnabled bool, agentEnabled bool, monitor *publicBackendHealthMonitor) *p2pstreamv1.PublicBackendAgentHealth {
	var snapshot *publicBackendAgentHealthSnapshot
	if monitor != nil {
		snapshot = monitor.agentSnapshot(backendID, agentID, assignmentEnabled, agentEnabled)
	}
	if snapshot == nil {
		status := p2pstreamv1.PublicBackendHealthStatus_PUBLIC_BACKEND_HEALTH_STATUS_UNKNOWN
		if !assignmentEnabled || !agentEnabled {
			status = p2pstreamv1.PublicBackendHealthStatus_PUBLIC_BACKEND_HEALTH_STATUS_DISABLED
		}
		snapshot = &publicBackendAgentHealthSnapshot{Status: status}
	}
	return &p2pstreamv1.PublicBackendAgentHealth{
		Status:                          snapshot.Status,
		Connected:                       snapshot.Connected,
		Available:                       snapshot.Available,
		LastCheckedAtUnixMillis:         snapshot.LastCheckedAtUnixMillis,
		LastError:                       snapshot.LastError,
		PassiveUnhealthyUntilUnixMillis: snapshot.PassiveUnhealthyUntilUnixMillis,
		ActiveRequests:                  snapshot.ActiveRequests,
	}
}

func publicRouteBackendsByRoute(backends []db.PublicRouteBackend) map[int64][]db.PublicRouteBackend {
	resp := make(map[int64][]db.PublicRouteBackend)
	for _, backend := range backends {
		resp[backend.RouteID] = append(resp[backend.RouteID], backend)
	}
	return resp
}

func publicRouteBackendsToProto(backends []db.PublicRouteBackend) []*p2pstreamv1.PublicRouteBackend {
	resp := make([]*p2pstreamv1.PublicRouteBackend, 0, len(backends))
	for _, backend := range backends {
		resp = append(resp, &p2pstreamv1.PublicRouteBackend{
			RouteId:   backend.RouteID,
			BackendId: backend.BackendID,
			Position:  backend.Position,
			Weight:    backend.Weight,
			Enabled:   backend.Enabled != 0,
		})
	}
	return resp
}

func publicRouteBackendRowsToConfig(backends []db.PublicRouteBackend) []publicRouteBackendConfig {
	resp := make([]publicRouteBackendConfig, 0, len(backends))
	for _, backend := range backends {
		resp = append(resp, publicRouteBackendConfig{
			RouteID:   backend.RouteID,
			BackendID: backend.BackendID,
			Position:  backend.Position,
			Weight:    backend.Weight,
			Enabled:   backend.Enabled != 0,
		})
	}
	return resp
}

func publicBackendAgentRowsToConfig(agents []db.PublicBackendAgent) []publicBackendAgentConfig {
	resp := make([]publicBackendAgentConfig, 0, len(agents))
	for _, agent := range agents {
		resp = append(resp, publicBackendAgentConfig{
			BackendID: agent.BackendID,
			AgentID:   agent.AgentID,
			Position:  agent.Position,
			Weight:    agent.Weight,
			Enabled:   agent.Enabled != 0,
		})
	}
	return resp
}

func (a *App) publicAgentsToProto(ctx context.Context, agents []db.Agent) []*p2pstreamv1.Agent {
	resp := make([]*p2pstreamv1.Agent, 0, len(agents))
	for _, agent := range agents {
		resp = append(resp, a.agentToProto(ctx, agent))
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
		DefaultBackendId:    listener.DefaultBackendID,
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

func publicRouteToProto(route db.PublicRoute, assignments []db.PublicRouteBackend) *p2pstreamv1.PublicRoute {
	backendID := int64(0)
	if route.BackendID.Valid {
		backendID = route.BackendID.Int64
	}
	fallbackBackendID := int64(0)
	if route.FallbackBackendID.Valid {
		fallbackBackendID = route.FallbackBackendID.Int64
	}
	return &p2pstreamv1.PublicRoute{
		Id:                         route.ID,
		ListenerId:                 route.ListenerID,
		Priority:                   route.Priority,
		HostPattern:                route.HostPattern,
		PathPrefix:                 route.PathPrefix,
		BackendId:                  backendID,
		LoadBalancing:              protoLoadBalancingFromString(route.LoadBalancing),
		BackendAssignments:         publicRouteBackendsToProto(assignments),
		FallbackBackendId:          fallbackBackendID,
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

func publicRoutesToProto(routes []db.PublicRoute, assignments []db.PublicRouteBackend) []*p2pstreamv1.PublicRoute {
	assignmentsByRoute := publicRouteBackendsByRoute(assignments)
	resp := make([]*p2pstreamv1.PublicRoute, 0, len(routes))
	for _, route := range routes {
		resp = append(resp, publicRouteToProto(route, assignmentsByRoute[route.ID]))
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
