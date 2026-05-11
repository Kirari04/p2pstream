package server

import (
	"context"
	"crypto/tls"
	"database/sql"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"unicode/utf8"

	"connectrpc.com/connect"

	p2pstreamv1 "p2pstream/gen/proto/p2pstream/v1"
	"p2pstream/internal/config"
	"p2pstream/internal/db"
)

const (
	defaultPublicTargetOrigin                             = "https://httpbin.org"
	defaultPublicHTTPPort                                 = int64(80)
	defaultSelfSignedTLSHost                              = "p2pstream.local"
	publicBackendTypeProxyForward                         = "proxy_forward"
	publicBackendTypeStatic                               = "static"
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
	defaultStaticStatusCode                               = int64(http.StatusOK)
	defaultRedirectStatusCode                             = int64(http.StatusFound)
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
	TLSCertificates        []db.PublicTlsCertificate
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
	Name                      string
	TargetOrigin              string
	BackendType               string
	ForwardMode               string
	LoadBalancing             string
	TLSSkipVerify             int64
	StaticStatusCode          int64
	StaticResponseBody        string
	UpstreamBasicAuthEnabled  int64
	UpstreamBasicAuthUsername string
	UpstreamBasicAuthPassword string
	Enabled                   int64
	Headers                   []publicBackendHeaderInput
	Agents                    []publicBackendAgentInput
}

type publicBackendAgentInput struct {
	AgentID  int64
	Position int64
	Weight   int64
	Enabled  int64
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
		req.Msg.UpstreamRequestHeaders,
		req.Msg.UpstreamBasicAuth,
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
	return connect.NewResponse(&p2pstreamv1.CreatePublicBackendResponse{Backend: publicBackendToProto(backend, storedHeaders, storedUpstreamHeaders, storedAgents)}), nil
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
		req.Msg.UpstreamRequestHeaders,
		req.Msg.UpstreamBasicAuth,
	)
	if err != nil {
		return nil, err
	}
	if !req.Msg.Enabled {
		refs, err := a.DB.CountPublicBackendEnabledReferences(ctx, db.CountPublicBackendEnabledReferencesParams{
			DefaultBackendID: req.Msg.Id,
			BackendID:        sql.NullInt64{Int64: req.Msg.Id, Valid: true},
		})
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
	return connect.NewResponse(&p2pstreamv1.UpdatePublicBackendResponse{Backend: publicBackendToProto(backend, storedHeaders, storedUpstreamHeaders, storedAgents)}), nil
}

func (a *App) DeletePublicBackend(
	ctx context.Context,
	req *connect.Request[p2pstreamv1.DeletePublicBackendRequest],
) (*connect.Response[p2pstreamv1.DeletePublicBackendResponse], error) {
	if _, err := a.requireAdmin(ctx, req.Header()); err != nil {
		return nil, err
	}
	refs, err := a.DB.CountPublicBackendEnabledReferences(ctx, db.CountPublicBackendEnabledReferencesParams{
		DefaultBackendID: req.Msg.Id,
		BackendID:        sql.NullInt64{Int64: req.Msg.Id, Valid: true},
	})
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
		Name:                      params.Name,
		TargetOrigin:              params.TargetOrigin,
		BackendType:               params.BackendType,
		ForwardMode:               params.ForwardMode,
		LoadBalancing:             params.LoadBalancing,
		TlsSkipVerify:             params.TLSSkipVerify,
		StaticStatusCode:          params.StaticStatusCode,
		StaticResponseBody:        params.StaticResponseBody,
		UpstreamBasicAuthEnabled:  params.UpstreamBasicAuthEnabled,
		UpstreamBasicAuthUsername: params.UpstreamBasicAuthUsername,
		UpstreamBasicAuthPassword: params.UpstreamBasicAuthPassword,
		Enabled:                   params.Enabled,
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
		ID:                        id,
		Name:                      params.Name,
		TargetOrigin:              params.TargetOrigin,
		BackendType:               params.BackendType,
		ForwardMode:               params.ForwardMode,
		LoadBalancing:             params.LoadBalancing,
		TlsSkipVerify:             params.TLSSkipVerify,
		StaticStatusCode:          params.StaticStatusCode,
		StaticResponseBody:        params.StaticResponseBody,
		UpstreamBasicAuthEnabled:  params.UpstreamBasicAuthEnabled,
		UpstreamBasicAuthUsername: params.UpstreamBasicAuthUsername,
		UpstreamBasicAuthPassword: params.UpstreamBasicAuthPassword,
		Enabled:                   params.Enabled,
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
	params, err := a.validatePublicRouteInput(
		ctx,
		req.Msg.ListenerId,
		req.Msg.Priority,
		req.Msg.HostPattern,
		req.Msg.PathPrefix,
		req.Msg.BackendId,
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
	route, err := a.DB.CreatePublicRoute(ctx, db.CreatePublicRouteParams{
		ListenerID:                 params.ListenerID,
		Priority:                   params.Priority,
		HostPattern:                params.HostPattern,
		PathPrefix:                 params.PathPrefix,
		BackendID:                  params.BackendID,
		Action:                     params.Action,
		RedirectTargetMode:         params.RedirectTargetMode,
		RedirectTarget:             params.RedirectTarget,
		RedirectStatusCode:         params.RedirectStatusCode,
		RedirectPreservePathSuffix: params.RedirectPreservePathSuffix,
		RedirectPreserveQuery:      params.RedirectPreserveQuery,
		Enabled:                    params.Enabled,
	})
	if err != nil {
		return nil, publicDBError(err)
	}
	if err := a.refreshPublicProxySnapshot(ctx); err != nil {
		return nil, err
	}
	return connect.NewResponse(&p2pstreamv1.CreatePublicRouteResponse{Route: publicRouteToProto(route)}), nil
}

func (a *App) UpdatePublicRoute(
	ctx context.Context,
	req *connect.Request[p2pstreamv1.UpdatePublicRouteRequest],
) (*connect.Response[p2pstreamv1.UpdatePublicRouteResponse], error) {
	if _, err := a.requireAdmin(ctx, req.Header()); err != nil {
		return nil, err
	}
	params, err := a.validatePublicRouteInput(
		ctx,
		req.Msg.ListenerId,
		req.Msg.Priority,
		req.Msg.HostPattern,
		req.Msg.PathPrefix,
		req.Msg.BackendId,
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
	route, err := a.DB.UpdatePublicRoute(ctx, params)
	if err != nil {
		return nil, publicDBError(err)
	}
	if err := a.refreshPublicProxySnapshot(ctx); err != nil {
		return nil, err
	}
	return connect.NewResponse(&p2pstreamv1.UpdatePublicRouteResponse{Route: publicRouteToProto(route)}), nil
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

func (a *App) CreatePublicTlsCertificate(
	ctx context.Context,
	req *connect.Request[p2pstreamv1.CreatePublicTlsCertificateRequest],
) (*connect.Response[p2pstreamv1.CreatePublicTlsCertificateResponse], error) {
	if _, err := a.requireAdmin(ctx, req.Header()); err != nil {
		return nil, err
	}
	params, hasUpload, err := a.validatePublicTLSCertificateInput(
		ctx,
		req.Msg.ListenerId,
		req.Msg.HostnamePattern,
		req.Msg.CertPath,
		req.Msg.KeyPath,
		req.Msg.CertPem,
		req.Msg.KeyPem,
		req.Msg.Enabled,
		false,
	)
	if err != nil {
		return nil, err
	}

	var cert db.PublicTlsCertificate
	if hasUpload {
		cert, err = a.createUploadedPublicTLSCertificate(ctx, params, req.Msg.CertPem, req.Msg.KeyPem)
	} else {
		cert, err = a.DB.CreatePublicTlsCertificate(ctx, db.CreatePublicTlsCertificateParams{
			ListenerID:      params.ListenerID,
			HostnamePattern: params.HostnamePattern,
			CertPath:        params.CertPath,
			KeyPath:         params.KeyPath,
			Enabled:         params.Enabled,
		})
	}
	if err != nil {
		return nil, publicDBError(err)
	}
	if err := a.refreshPublicProxySnapshot(ctx); err != nil {
		return nil, err
	}
	_, _ = a.restartTLSListenerIfActive(ctx, cert.ListenerID)
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
	params, hasUpload, err := a.validatePublicTLSCertificateInput(
		ctx,
		req.Msg.ListenerId,
		req.Msg.HostnamePattern,
		req.Msg.CertPath,
		req.Msg.KeyPath,
		req.Msg.CertPem,
		req.Msg.KeyPem,
		req.Msg.Enabled,
		true,
	)
	if err != nil {
		return nil, err
	}
	params.ID = req.Msg.Id
	if params.CertPath == "" && params.KeyPath == "" {
		params.CertPath = existing.CertPath
		params.KeyPath = existing.KeyPath
	}

	var cert db.PublicTlsCertificate
	if hasUpload {
		cert, err = a.updateUploadedPublicTLSCertificate(ctx, params, req.Msg.CertPem, req.Msg.KeyPem)
	} else {
		cert, err = a.DB.UpdatePublicTlsCertificate(ctx, params)
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

func (a *App) createUploadedPublicTLSCertificate(
	ctx context.Context,
	params db.UpdatePublicTlsCertificateParams,
	certPEM []byte,
	keyPEM []byte,
) (db.PublicTlsCertificate, error) {
	tx, err := a.DB.BeginTx(ctx, nil)
	if err != nil {
		return db.PublicTlsCertificate{}, err
	}
	defer tx.Rollback()

	qtx := a.DB.WithTx(tx)
	cert, err := qtx.CreatePublicTlsCertificate(ctx, db.CreatePublicTlsCertificateParams{
		ListenerID:      params.ListenerID,
		HostnamePattern: params.HostnamePattern,
		CertPath:        "",
		KeyPath:         "",
		Enabled:         params.Enabled,
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

	cert, err = qtx.UpdatePublicTlsCertificate(ctx, db.UpdatePublicTlsCertificateParams{
		ID:              cert.ID,
		ListenerID:      params.ListenerID,
		HostnamePattern: params.HostnamePattern,
		CertPath:        certPath,
		KeyPath:         keyPath,
		Enabled:         params.Enabled,
	})
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
	params db.UpdatePublicTlsCertificateParams,
	certPEM []byte,
	keyPEM []byte,
) (db.PublicTlsCertificate, error) {
	certPath, keyPath, err := a.writePublicTLSCertificateFiles(params.ListenerID, params.ID, certPEM, keyPEM)
	if err != nil {
		return db.PublicTlsCertificate{}, err
	}

	cert, err := a.DB.UpdatePublicTlsCertificate(ctx, db.UpdatePublicTlsCertificateParams{
		ID:              params.ID,
		ListenerID:      params.ListenerID,
		HostnamePattern: params.HostnamePattern,
		CertPath:        certPath,
		KeyPath:         keyPath,
		Enabled:         params.Enabled,
	})
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
	a.proxyMu.Unlock()
	a.LoadBalancers.reconcile(snap)

	return &p2pstreamv1.GetPublicProxyConfigResponse{
		Backends:        publicBackendsToProto(rows.Backends, rows.BackendHeaders, rows.BackendUpstreamHeaders, rows.BackendAgents),
		Listeners:       publicListenersToProto(rows.Listeners),
		Routes:          publicRoutesToProto(rows.Routes),
		TlsCertificates: publicTLSCertificatesToProto(rows.TLSCertificates),
		Proxy:           proxy,
		Agents:          a.publicAgentsToProto(ctx, rows.Agents),
		BackendAgents:   publicBackendAgentsToProto(rows.BackendAgents),
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
	a.proxyMu.Unlock()
	a.LoadBalancers.reconcile(snap)
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
	certs, err := a.DB.ListPublicTlsCertificates(ctx)
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
		TLSCertificates:        certs,
	}, nil
}

func (a *App) ensurePublicProxySeeded(ctx context.Context) error {
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

	backend, err := a.DB.CreatePublicBackend(ctx, db.CreatePublicBackendParams{
		Name:                      "default",
		TargetOrigin:              defaultPublicTargetOrigin,
		BackendType:               publicBackendTypeProxyForward,
		ForwardMode:               publicBackendForwardModeDirect,
		LoadBalancing:             publicBackendLoadBalancingRoundRobin,
		TlsSkipVerify:             0,
		StaticStatusCode:          defaultStaticStatusCode,
		StaticResponseBody:        "",
		UpstreamBasicAuthEnabled:  0,
		UpstreamBasicAuthUsername: "",
		UpstreamBasicAuthPassword: "",
		Enabled:                   1,
	})
	if err != nil {
		return publicDBError(err)
	}

	if _, err := a.DB.CreatePublicListener(ctx, db.CreatePublicListenerParams{
		Name:             "public-http",
		BindAddress:      "",
		Port:             defaultPublicHTTPPort,
		Protocol:         publicListenerProtocolHTTP,
		Enabled:          1,
		DefaultBackendID: backend.ID,
	}); err != nil {
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

	certPEM, keyPEM, err := generateManagedSelfSignedCertificatePEM()
	if err != nil {
		return connect.NewError(connect.CodeInternal, err)
	}
	if _, err := a.createUploadedPublicTLSCertificate(ctx, db.UpdatePublicTlsCertificateParams{
		ListenerID:      httpsListener.ID,
		HostnamePattern: defaultSelfSignedTLSHost,
		Enabled:         1,
	}, certPEM, keyPEM); err != nil {
		return publicDBError(err)
	}
	return nil
}

func snapshotFromPublicRows(rows publicConfigRows) (*publicProxySnapshot, error) {
	snap := &publicProxySnapshot{
		Backends:         make(map[int64]publicBackendConfig),
		Agents:           make(map[int64]publicAgentConfig),
		Listeners:        make(map[int64]publicListenerConfig),
		RoutesByListener: make(map[int64][]publicRouteConfig),
		CertsByListener:  make(map[int64][]publicTLSCertificateConfig),
	}

	headersByBackend := publicBackendHeadersByBackend(rows.BackendHeaders)
	upstreamHeadersByBackend := publicBackendUpstreamHeadersByBackend(rows.BackendUpstreamHeaders)
	agentsByBackend := publicBackendAgentsByBackend(rows.BackendAgents)
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
		snap.Backends[backend.ID] = publicBackendConfig{
			ID:                     backend.ID,
			Name:                   backend.Name,
			TargetOrigin:           backend.TargetOrigin,
			BackendType:            backendType,
			ForwardMode:            forwardMode,
			LoadBalancing:          loadBalancing,
			TLSSkipVerify:          backend.TlsSkipVerify != 0,
			StaticStatusCode:       int(backend.StaticStatusCode),
			StaticResponseHeaders:  publicBackendHeaderRowsToConfig(headersByBackend[backend.ID]),
			StaticResponseBody:     backend.StaticResponseBody,
			UpstreamRequestHeaders: publicBackendUpstreamHeaderRowsToConfig(upstreamHeadersByBackend[backend.ID]),
			UpstreamBasicAuth: publicBackendBasicAuthConfig{
				Enabled:  backend.UpstreamBasicAuthEnabled != 0,
				Username: backend.UpstreamBasicAuthUsername,
				Password: backend.UpstreamBasicAuthPassword,
			},
			Enabled:          backend.Enabled != 0,
			ParsedOrigin:     parsed,
			AgentAssignments: publicBackendAgentRowsToConfig(agentsByBackend[backend.ID]),
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
		snap.RoutesByListener[route.ListenerID] = append(snap.RoutesByListener[route.ListenerID], publicRouteConfig{
			ID:                         route.ID,
			ListenerID:                 route.ListenerID,
			Priority:                   route.Priority,
			HostPattern:                normalizeHostPattern(route.HostPattern),
			PathPrefix:                 route.PathPrefix,
			BackendID:                  backendID,
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
			ID:              cert.ID,
			ListenerID:      cert.ListenerID,
			HostnamePattern: normalizeHostPattern(cert.HostnamePattern),
			CertPath:        cert.CertPath,
			KeyPath:         cert.KeyPath,
			Enabled:         cert.Enabled != 0,
		})
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
	upstreamRequestHeaders []*p2pstreamv1.PublicBackendUpstreamHeader,
	upstreamBasicAuth *p2pstreamv1.PublicBackendBasicAuth,
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
		Name:               name,
		BackendType:        backendTypeString,
		ForwardMode:        publicBackendForwardModeDirect,
		LoadBalancing:      publicBackendLoadBalancingRoundRobin,
		StaticStatusCode:   defaultStaticStatusCode,
		StaticResponseBody: "",
		Enabled:            boolInt(enabled),
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
		params.TargetOrigin = targetOrigin
		params.ForwardMode = forwardModeString
		params.LoadBalancing = loadBalancingString
		params.TLSSkipVerify = boolInt(tlsSkipVerify)
		params.UpstreamBasicAuthEnabled = authEnabled
		params.UpstreamBasicAuthUsername = authUsername
		params.UpstreamBasicAuthPassword = authPassword
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
	params.UpstreamBasicAuthEnabled = 0
	params.UpstreamBasicAuthUsername = ""
	params.UpstreamBasicAuthPassword = ""
	return params, headers, nil, nil, nil
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
	enabled bool,
	action p2pstreamv1.PublicRouteAction,
	redirectTargetMode p2pstreamv1.PublicRouteRedirectTargetMode,
	redirectTarget string,
	redirectStatusCode int64,
	redirectPreservePathSuffix bool,
	redirectPreserveQuery bool,
) (db.UpdatePublicRouteParams, error) {
	if _, err := a.DB.GetPublicListener(ctx, listenerID); err != nil {
		return db.UpdatePublicRouteParams{}, publicDBError(err)
	}
	hostPattern = normalizeHostPattern(hostPattern)
	pathPrefix = strings.TrimSpace(pathPrefix)
	if hostPattern == "" && pathPrefix == "" {
		return db.UpdatePublicRouteParams{}, connect.NewError(connect.CodeInvalidArgument, errors.New("route requires a host pattern or path prefix"))
	}
	if hostPattern != "" {
		if err := validateHostPattern(hostPattern); err != nil {
			return db.UpdatePublicRouteParams{}, err
		}
	}
	if pathPrefix != "" && !strings.HasPrefix(pathPrefix, "/") {
		return db.UpdatePublicRouteParams{}, connect.NewError(connect.CodeInvalidArgument, errors.New("path prefix must start with /"))
	}
	actionString, err := routeActionStringFromProto(action)
	if err != nil {
		return db.UpdatePublicRouteParams{}, err
	}
	backendIDParam := sql.NullInt64{}
	redirectMode := ""
	redirectTarget = strings.TrimSpace(redirectTarget)
	if redirectStatusCode == 0 {
		redirectStatusCode = defaultRedirectStatusCode
	}
	if actionString == publicRouteActionForward {
		backend, err := a.DB.GetPublicBackend(ctx, backendID)
		if err != nil {
			return db.UpdatePublicRouteParams{}, publicDBError(err)
		}
		if enabled && backend.Enabled == 0 {
			return db.UpdatePublicRouteParams{}, connect.NewError(connect.CodeFailedPrecondition, errors.New("enabled route requires an enabled backend"))
		}
		backendIDParam = sql.NullInt64{Int64: backendID, Valid: true}
		redirectStatusCode = defaultRedirectStatusCode
		redirectPreservePathSuffix = true
		redirectPreserveQuery = true
	} else {
		redirectMode, err = routeRedirectTargetModeStringFromProto(redirectTargetMode)
		if err != nil {
			return db.UpdatePublicRouteParams{}, err
		}
		if err := validateRouteRedirectTarget(redirectMode, redirectTarget); err != nil {
			return db.UpdatePublicRouteParams{}, err
		}
		if !validRedirectStatusCode(redirectStatusCode) {
			return db.UpdatePublicRouteParams{}, connect.NewError(connect.CodeInvalidArgument, errors.New("redirect status must be 301, 302, 307, or 308"))
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
		Action:                     actionString,
		RedirectTargetMode:         redirectMode,
		RedirectTarget:             redirectTarget,
		RedirectStatusCode:         redirectStatusCode,
		RedirectPreservePathSuffix: boolInt(redirectPreservePathSuffix),
		RedirectPreserveQuery:      boolInt(redirectPreserveQuery),
		Enabled:                    boolInt(enabled),
	}, nil
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
	allowMissingMaterial bool,
) (db.UpdatePublicTlsCertificateParams, bool, error) {
	listener, err := a.DB.GetPublicListener(ctx, listenerID)
	if err != nil {
		return db.UpdatePublicTlsCertificateParams{}, false, publicDBError(err)
	}
	if listener.Protocol != publicListenerProtocolHTTPS {
		return db.UpdatePublicTlsCertificateParams{}, false, connect.NewError(connect.CodeFailedPrecondition, errors.New("TLS certificates can only be configured on HTTPS listeners"))
	}
	hostnamePattern = normalizeHostPattern(hostnamePattern)
	if err := validateHostPattern(hostnamePattern); err != nil {
		return db.UpdatePublicTlsCertificateParams{}, false, err
	}
	certPath = strings.TrimSpace(certPath)
	keyPath = strings.TrimSpace(keyPath)
	hasCertUpload := len(certPEM) > 0
	hasKeyUpload := len(keyPEM) > 0
	if hasCertUpload || hasKeyUpload {
		if !hasCertUpload || !hasKeyUpload {
			return db.UpdatePublicTlsCertificateParams{}, false, connect.NewError(connect.CodeInvalidArgument, errors.New("certificate and private key uploads are both required"))
		}
		if _, err := tls.X509KeyPair(certPEM, keyPEM); err != nil {
			return db.UpdatePublicTlsCertificateParams{}, false, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("certificate and private key must be a valid PEM pair: %w", err))
		}
		return db.UpdatePublicTlsCertificateParams{
			ListenerID:      listenerID,
			HostnamePattern: hostnamePattern,
			Enabled:         boolInt(enabled),
		}, true, nil
	}

	if certPath == "" && keyPath == "" && allowMissingMaterial {
		return db.UpdatePublicTlsCertificateParams{
			ListenerID:      listenerID,
			HostnamePattern: hostnamePattern,
			Enabled:         boolInt(enabled),
		}, false, nil
	}
	if certPath == "" || keyPath == "" {
		return db.UpdatePublicTlsCertificateParams{}, false, connect.NewError(connect.CodeInvalidArgument, errors.New("certificate and key paths are required"))
	}
	return db.UpdatePublicTlsCertificateParams{
		ListenerID:      listenerID,
		HostnamePattern: hostnamePattern,
		CertPath:        certPath,
		KeyPath:         keyPath,
		Enabled:         boolInt(enabled),
	}, false, nil
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

func publicBackendToProto(backend db.PublicBackend, headers []db.PublicBackendHeader, upstreamHeaders []db.PublicBackendUpstreamHeader, agents []db.PublicBackendAgent) *p2pstreamv1.PublicBackend {
	return &p2pstreamv1.PublicBackend{
		Id:                     backend.ID,
		Name:                   backend.Name,
		TargetOrigin:           backend.TargetOrigin,
		Enabled:                backend.Enabled != 0,
		CreatedAtUnixMillis:    backend.CreatedAt.UnixMilli(),
		UpdatedAtUnixMillis:    backend.UpdatedAt.UnixMilli(),
		BackendType:            protoBackendTypeFromString(backend.BackendType),
		ForwardMode:            protoForwardModeFromString(backend.ForwardMode),
		LoadBalancing:          protoLoadBalancingFromString(backend.LoadBalancing),
		TlsSkipVerify:          backend.TlsSkipVerify != 0,
		StaticStatusCode:       backend.StaticStatusCode,
		StaticResponseHeaders:  publicBackendHeaderRowsToProto(headers),
		StaticResponseBody:     backend.StaticResponseBody,
		AgentAssignments:       publicBackendAgentsToProto(agents),
		UpstreamRequestHeaders: publicBackendUpstreamHeaderRowsToProto(upstreamHeaders),
		UpstreamBasicAuth:      publicBackendBasicAuthToProto(backend),
	}
}

func publicBackendsToProto(backends []db.PublicBackend, headers []db.PublicBackendHeader, upstreamHeaders []db.PublicBackendUpstreamHeader, agents []db.PublicBackendAgent) []*p2pstreamv1.PublicBackend {
	headersByBackend := publicBackendHeadersByBackend(headers)
	upstreamHeadersByBackend := publicBackendUpstreamHeadersByBackend(upstreamHeaders)
	agentsByBackend := publicBackendAgentsByBackend(agents)
	resp := make([]*p2pstreamv1.PublicBackend, 0, len(backends))
	for _, backend := range backends {
		resp = append(resp, publicBackendToProto(backend, headersByBackend[backend.ID], upstreamHeadersByBackend[backend.ID], agentsByBackend[backend.ID]))
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

func publicBackendAgentsToProto(agents []db.PublicBackendAgent) []*p2pstreamv1.PublicBackendAgent {
	resp := make([]*p2pstreamv1.PublicBackendAgent, 0, len(agents))
	for _, agent := range agents {
		resp = append(resp, &p2pstreamv1.PublicBackendAgent{
			BackendId: agent.BackendID,
			AgentId:   agent.AgentID,
			Position:  agent.Position,
			Weight:    agent.Weight,
			Enabled:   agent.Enabled != 0,
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

func publicRouteToProto(route db.PublicRoute) *p2pstreamv1.PublicRoute {
	backendID := int64(0)
	if route.BackendID.Valid {
		backendID = route.BackendID.Int64
	}
	return &p2pstreamv1.PublicRoute{
		Id:                         route.ID,
		ListenerId:                 route.ListenerID,
		Priority:                   route.Priority,
		HostPattern:                route.HostPattern,
		PathPrefix:                 route.PathPrefix,
		BackendId:                  backendID,
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

func publicRoutesToProto(routes []db.PublicRoute) []*p2pstreamv1.PublicRoute {
	resp := make([]*p2pstreamv1.PublicRoute, 0, len(routes))
	for _, route := range routes {
		resp = append(resp, publicRouteToProto(route))
	}
	return resp
}

func publicTLSCertificateToProto(cert db.PublicTlsCertificate) *p2pstreamv1.PublicTlsCertificate {
	return &p2pstreamv1.PublicTlsCertificate{
		Id:                  cert.ID,
		ListenerId:          cert.ListenerID,
		HostnamePattern:     cert.HostnamePattern,
		CertPath:            cert.CertPath,
		KeyPath:             cert.KeyPath,
		Enabled:             cert.Enabled != 0,
		CreatedAtUnixMillis: cert.CreatedAt.UnixMilli(),
		UpdatedAtUnixMillis: cert.UpdatedAt.UnixMilli(),
	}
}

func publicTLSCertificatesToProto(certs []db.PublicTlsCertificate) []*p2pstreamv1.PublicTlsCertificate {
	resp := make([]*p2pstreamv1.PublicTlsCertificate, 0, len(certs))
	for _, cert := range certs {
		resp = append(resp, publicTLSCertificateToProto(cert))
	}
	return resp
}
