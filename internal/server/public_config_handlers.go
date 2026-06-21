package server

import (
	"context"
	"errors"

	"connectrpc.com/connect"

	p2pstreamv1 "p2pstream/gen/proto/p2pstream/v1"
	"p2pstream/internal/db"
)

func (a *App) GetPublicProxyConfig(
	ctx context.Context,
	req *connect.Request[p2pstreamv1.GetPublicProxyConfigRequest],
) (*connect.Response[p2pstreamv1.GetPublicProxyConfigResponse], error) {
	if _, err := a.requireUser(ctx, req.Header()); err != nil {
		return nil, err
	}
	return a.publicConfigService().GetPublicProxyConfig(ctx, req)
}

func (s *publicConfigService) GetPublicProxyConfig(
	ctx context.Context,
	req *connect.Request[p2pstreamv1.GetPublicProxyConfigRequest],
) (*connect.Response[p2pstreamv1.GetPublicProxyConfigResponse], error) {
	a := s.app
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
	return a.publicConfigService().ListPublicRouteTargetHealthTraces(ctx, req)
}

func (s *publicConfigService) ListPublicRouteTargetHealthTraces(
	ctx context.Context,
	req *connect.Request[p2pstreamv1.ListPublicRouteTargetHealthTracesRequest],
) (*connect.Response[p2pstreamv1.ListPublicRouteTargetHealthTracesResponse], error) {
	if req.Msg.RouteTargetId <= 0 {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("route target id is required"))
	}
	limit := req.Msg.Limit
	if limit <= 0 || limit > publicRouteTargetHealthTraceLimitPerTarget {
		limit = publicRouteTargetHealthTraceLimitPerTarget
	}
	var traces []*p2pstreamv1.PublicRouteTargetHealthTrace
	var retained int64
	if s.targetHealth != nil {
		traces, retained = s.targetHealth.listHealthTraces(req.Msg.RouteTargetId, req.Msg.AgentId, limit, req.Msg.FailuresOnly)
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
	return a.publicConfigService().CreatePublicListener(ctx, req)
}

func (s *publicConfigService) CreatePublicListener(
	ctx context.Context,
	req *connect.Request[p2pstreamv1.CreatePublicListenerRequest],
) (*connect.Response[p2pstreamv1.CreatePublicListenerResponse], error) {
	a := s.app
	params, err := a.validatePublicListenerInput(req.Msg.Name, req.Msg.BindAddress, req.Msg.Port, req.Msg.Protocol, req.Msg.Enabled, false)
	if err != nil {
		return nil, err
	}
	listener, err := s.db.CreatePublicListener(ctx, db.CreatePublicListenerParams{
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
		Proxy:    publicConfigProxyStatus(s.runtime),
	}), nil
}

func (a *App) UpdatePublicListener(
	ctx context.Context,
	req *connect.Request[p2pstreamv1.UpdatePublicListenerRequest],
) (*connect.Response[p2pstreamv1.UpdatePublicListenerResponse], error) {
	if _, err := a.requireAdmin(ctx, req.Header()); err != nil {
		return nil, err
	}
	return a.publicConfigService().UpdatePublicListener(ctx, req)
}

func (s *publicConfigService) UpdatePublicListener(
	ctx context.Context,
	req *connect.Request[p2pstreamv1.UpdatePublicListenerRequest],
) (*connect.Response[p2pstreamv1.UpdatePublicListenerResponse], error) {
	a := s.app
	params, err := a.validatePublicListenerInput(req.Msg.Name, req.Msg.BindAddress, req.Msg.Port, req.Msg.Protocol, req.Msg.Enabled, false)
	if err != nil {
		return nil, err
	}
	params.ID = req.Msg.Id
	listener, err := s.db.UpdatePublicListener(ctx, params)
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
		Proxy:    publicConfigProxyStatus(s.runtime),
	}), nil
}

func (a *App) DeletePublicListener(
	ctx context.Context,
	req *connect.Request[p2pstreamv1.DeletePublicListenerRequest],
) (*connect.Response[p2pstreamv1.DeletePublicListenerResponse], error) {
	if _, err := a.requireAdmin(ctx, req.Header()); err != nil {
		return nil, err
	}
	return a.publicConfigService().DeletePublicListener(ctx, req)
}

func (s *publicConfigService) DeletePublicListener(
	ctx context.Context,
	req *connect.Request[p2pstreamv1.DeletePublicListenerRequest],
) (*connect.Response[p2pstreamv1.DeletePublicListenerResponse], error) {
	a := s.app
	a.proxyMu.Lock()
	running := false
	if runtime := a.publicListenerState[req.Msg.Id]; runtime != nil {
		running = runtime.Server != nil
	}
	a.proxyMu.Unlock()
	if running {
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("stop or disable listener before deleting it"))
	}
	if err := s.db.DeletePublicListener(ctx, req.Msg.Id); err != nil {
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
	return a.publicConfigService().EnablePublicListener(ctx, req)
}

func (s *publicConfigService) EnablePublicListener(
	ctx context.Context,
	req *connect.Request[p2pstreamv1.EnablePublicListenerRequest],
) (*connect.Response[p2pstreamv1.EnablePublicListenerResponse], error) {
	a := s.app
	listener, err := s.db.GetPublicListener(ctx, req.Msg.Id)
	if err != nil {
		return nil, publicDBError(err)
	}
	if _, err := a.validatePublicListenerInput(listener.Name, listener.BindAddress, listener.Port, protoProtocolFromString(listener.Protocol), true, true); err != nil {
		return nil, err
	}
	listener, err = s.db.SetPublicListenerEnabled(ctx, db.SetPublicListenerEnabledParams{ID: req.Msg.Id, Enabled: 1})
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
		Proxy:    publicConfigProxyStatus(s.runtime),
	}), nil
}

func (a *App) DisablePublicListener(
	ctx context.Context,
	req *connect.Request[p2pstreamv1.DisablePublicListenerRequest],
) (*connect.Response[p2pstreamv1.DisablePublicListenerResponse], error) {
	if _, err := a.requireAdmin(ctx, req.Header()); err != nil {
		return nil, err
	}
	return a.publicConfigService().DisablePublicListener(ctx, req)
}

func (s *publicConfigService) DisablePublicListener(
	ctx context.Context,
	req *connect.Request[p2pstreamv1.DisablePublicListenerRequest],
) (*connect.Response[p2pstreamv1.DisablePublicListenerResponse], error) {
	a := s.app
	listener, err := s.db.SetPublicListenerEnabled(ctx, db.SetPublicListenerEnabledParams{ID: req.Msg.Id, Enabled: 0})
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
		Proxy:    publicConfigProxyStatus(s.runtime),
	}), nil
}

func (a *App) StartPublicListener(
	ctx context.Context,
	req *connect.Request[p2pstreamv1.StartPublicListenerRequest],
) (*connect.Response[p2pstreamv1.StartPublicListenerResponse], error) {
	if _, err := a.requireAdmin(ctx, req.Header()); err != nil {
		return nil, err
	}
	return a.publicConfigService().StartPublicListener(ctx, req)
}

func (s *publicConfigService) StartPublicListener(
	ctx context.Context,
	req *connect.Request[p2pstreamv1.StartPublicListenerRequest],
) (*connect.Response[p2pstreamv1.StartPublicListenerResponse], error) {
	a := s.app
	if err := a.refreshPublicProxySnapshot(ctx); err != nil {
		return nil, err
	}
	status, err := a.startPublicListenerRuntime(ctx, req.Msg.Id, true)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&p2pstreamv1.StartPublicListenerResponse{Status: status, Proxy: publicConfigProxyStatus(s.runtime)}), nil
}

func (a *App) StopPublicListener(
	ctx context.Context,
	req *connect.Request[p2pstreamv1.StopPublicListenerRequest],
) (*connect.Response[p2pstreamv1.StopPublicListenerResponse], error) {
	if _, err := a.requireAdmin(ctx, req.Header()); err != nil {
		return nil, err
	}
	return a.publicConfigService().StopPublicListener(ctx, req)
}

func (s *publicConfigService) StopPublicListener(
	ctx context.Context,
	req *connect.Request[p2pstreamv1.StopPublicListenerRequest],
) (*connect.Response[p2pstreamv1.StopPublicListenerResponse], error) {
	a := s.app
	if err := a.refreshPublicProxySnapshot(ctx); err != nil {
		return nil, err
	}
	status, err := a.stopPublicListenerRuntime(ctx, req.Msg.Id)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&p2pstreamv1.StopPublicListenerResponse{Status: status, Proxy: publicConfigProxyStatus(s.runtime)}), nil
}

func (a *App) CreatePublicRoute(
	ctx context.Context,
	req *connect.Request[p2pstreamv1.CreatePublicRouteRequest],
) (*connect.Response[p2pstreamv1.CreatePublicRouteResponse], error) {
	if _, err := a.requireAdmin(ctx, req.Header()); err != nil {
		return nil, err
	}
	return a.publicConfigService().CreatePublicRoute(ctx, req)
}

func (s *publicConfigService) CreatePublicRoute(
	ctx context.Context,
	req *connect.Request[p2pstreamv1.CreatePublicRouteRequest],
) (*connect.Response[p2pstreamv1.CreatePublicRouteResponse], error) {
	a := s.app
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
	return connect.NewResponse(&p2pstreamv1.CreatePublicRouteResponse{Route: publicRouteToProto(route, storedTargets, upstreamHeaders, responseHeaders, s.targetHealth)}), nil
}

func (a *App) UpdatePublicRoute(
	ctx context.Context,
	req *connect.Request[p2pstreamv1.UpdatePublicRouteRequest],
) (*connect.Response[p2pstreamv1.UpdatePublicRouteResponse], error) {
	if _, err := a.requireAdmin(ctx, req.Header()); err != nil {
		return nil, err
	}
	return a.publicConfigService().UpdatePublicRoute(ctx, req)
}

func (s *publicConfigService) UpdatePublicRoute(
	ctx context.Context,
	req *connect.Request[p2pstreamv1.UpdatePublicRouteRequest],
) (*connect.Response[p2pstreamv1.UpdatePublicRouteResponse], error) {
	a := s.app
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
	return connect.NewResponse(&p2pstreamv1.UpdatePublicRouteResponse{Route: publicRouteToProto(route, storedTargets, upstreamHeaders, responseHeaders, s.targetHealth)}), nil
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
	return a.publicConfigService().DeletePublicRoute(ctx, req)
}

func (s *publicConfigService) DeletePublicRoute(
	ctx context.Context,
	req *connect.Request[p2pstreamv1.DeletePublicRouteRequest],
) (*connect.Response[p2pstreamv1.DeletePublicRouteResponse], error) {
	a := s.app
	if err := s.db.DeletePublicRoute(ctx, req.Msg.Id); err != nil {
		return nil, publicDBError(err)
	}
	if err := a.refreshPublicProxySnapshot(ctx); err != nil {
		return nil, err
	}
	return connect.NewResponse(&p2pstreamv1.DeletePublicRouteResponse{}), nil
}
