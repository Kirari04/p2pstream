package server

import (
	"context"
	"errors"

	"connectrpc.com/connect"

	p2pstreamv1 "p2pstream/gen/proto/p2pstream/v1"
)

func (a *App) managementTLSSnapshot(ctx context.Context) (*p2pstreamv1.ManagementTlsRotation, error) {
	if a.ManagementTLS == nil {
		return &p2pstreamv1.ManagementTlsRotation{StatusMessage: "Management TLS rotation is unavailable until the server is restarted with TLS enabled."}, nil
	}
	return a.ManagementTLS.snapshot(ctx, a)
}

func (a *App) GetManagementTlsRotation(ctx context.Context, req *connect.Request[p2pstreamv1.GetManagementTlsRotationRequest]) (*connect.Response[p2pstreamv1.GetManagementTlsRotationResponse], error) {
	if _, err := a.requireAdmin(ctx, req.Header()); err != nil {
		return nil, err
	}
	rotation, err := a.managementTLSSnapshot(ctx)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	return connect.NewResponse(&p2pstreamv1.GetManagementTlsRotationResponse{Rotation: rotation}), nil
}

func (a *App) StageManagementTlsRotation(ctx context.Context, req *connect.Request[p2pstreamv1.StageManagementTlsRotationRequest]) (*connect.Response[p2pstreamv1.StageManagementTlsRotationResponse], error) {
	if _, err := a.requireAdmin(ctx, req.Header()); err != nil {
		return nil, err
	}
	if a.ManagementTLS == nil {
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("management TLS rotation is unavailable"))
	}
	if err := a.ManagementTLS.stage(req.Msg.CertificatePem, req.Msg.PrivateKeyPem, req.Msg.CaBundlePem, req.Msg.CurrentCaBundlePem); err != nil {
		return nil, managementTLSConnectError(err)
	}
	rotation, err := a.managementTLSSnapshot(ctx)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	return connect.NewResponse(&p2pstreamv1.StageManagementTlsRotationResponse{Rotation: rotation}), nil
}

func (a *App) GenerateManagementTlsRotation(ctx context.Context, req *connect.Request[p2pstreamv1.GenerateManagementTlsRotationRequest]) (*connect.Response[p2pstreamv1.GenerateManagementTlsRotationResponse], error) {
	if _, err := a.requireAdmin(ctx, req.Header()); err != nil {
		return nil, err
	}
	if a.ManagementTLS == nil {
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("management TLS rotation is unavailable"))
	}
	if err := a.ManagementTLS.generateAndStage(); err != nil {
		return nil, managementTLSConnectError(err)
	}
	rotation, err := a.managementTLSSnapshot(ctx)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	return connect.NewResponse(&p2pstreamv1.GenerateManagementTlsRotationResponse{Rotation: rotation}), nil
}

func (a *App) ActivateManagementTlsRotation(ctx context.Context, req *connect.Request[p2pstreamv1.ActivateManagementTlsRotationRequest]) (*connect.Response[p2pstreamv1.ActivateManagementTlsRotationResponse], error) {
	if _, err := a.requireAdmin(ctx, req.Header()); err != nil {
		return nil, err
	}
	if a.ManagementTLS == nil {
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("management TLS rotation is unavailable"))
	}
	if err := a.ManagementTLS.activate(ctx, req.Msg.Force); err != nil {
		return nil, managementTLSConnectError(err)
	}
	rotation, err := a.managementTLSSnapshot(ctx)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	return connect.NewResponse(&p2pstreamv1.ActivateManagementTlsRotationResponse{Rotation: rotation}), nil
}

func (a *App) RollbackManagementTlsRotation(ctx context.Context, req *connect.Request[p2pstreamv1.RollbackManagementTlsRotationRequest]) (*connect.Response[p2pstreamv1.RollbackManagementTlsRotationResponse], error) {
	if _, err := a.requireAdmin(ctx, req.Header()); err != nil {
		return nil, err
	}
	if a.ManagementTLS == nil {
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("management TLS rotation is unavailable"))
	}
	if err := a.ManagementTLS.rollback(); err != nil {
		return nil, managementTLSConnectError(err)
	}
	rotation, err := a.managementTLSSnapshot(ctx)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	return connect.NewResponse(&p2pstreamv1.RollbackManagementTlsRotationResponse{Rotation: rotation}), nil
}

func (a *App) BeginManagementTlsTrustRetirement(ctx context.Context, req *connect.Request[p2pstreamv1.BeginManagementTlsTrustRetirementRequest]) (*connect.Response[p2pstreamv1.BeginManagementTlsTrustRetirementResponse], error) {
	if _, err := a.requireAdmin(ctx, req.Header()); err != nil {
		return nil, err
	}
	if a.ManagementTLS == nil {
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("management TLS rotation is unavailable"))
	}
	if err := a.ManagementTLS.beginRetirement(ctx); err != nil {
		return nil, managementTLSConnectError(err)
	}
	rotation, err := a.managementTLSSnapshot(ctx)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	return connect.NewResponse(&p2pstreamv1.BeginManagementTlsTrustRetirementResponse{Rotation: rotation}), nil
}

func (a *App) FinalizeManagementTlsTrustRetirement(ctx context.Context, req *connect.Request[p2pstreamv1.FinalizeManagementTlsTrustRetirementRequest]) (*connect.Response[p2pstreamv1.FinalizeManagementTlsTrustRetirementResponse], error) {
	if _, err := a.requireAdmin(ctx, req.Header()); err != nil {
		return nil, err
	}
	if a.ManagementTLS == nil {
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("management TLS rotation is unavailable"))
	}
	if err := a.ManagementTLS.finalizeRetirement(ctx, req.Msg.Force); err != nil {
		return nil, managementTLSConnectError(err)
	}
	rotation, err := a.managementTLSSnapshot(ctx)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	return connect.NewResponse(&p2pstreamv1.FinalizeManagementTlsTrustRetirementResponse{Rotation: rotation}), nil
}

func (a *App) CancelManagementTlsRotation(ctx context.Context, req *connect.Request[p2pstreamv1.CancelManagementTlsRotationRequest]) (*connect.Response[p2pstreamv1.CancelManagementTlsRotationResponse], error) {
	if _, err := a.requireAdmin(ctx, req.Header()); err != nil {
		return nil, err
	}
	if a.ManagementTLS == nil {
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("management TLS rotation is unavailable"))
	}
	if err := a.ManagementTLS.cancel(); err != nil {
		return nil, managementTLSConnectError(err)
	}
	rotation, err := a.managementTLSSnapshot(ctx)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	return connect.NewResponse(&p2pstreamv1.CancelManagementTlsRotationResponse{Rotation: rotation}), nil
}

func (a *App) FinalizeManagementTlsTrustCleanup(ctx context.Context, req *connect.Request[p2pstreamv1.FinalizeManagementTlsTrustCleanupRequest]) (*connect.Response[p2pstreamv1.FinalizeManagementTlsTrustCleanupResponse], error) {
	if _, err := a.requireAdmin(ctx, req.Header()); err != nil {
		return nil, err
	}
	if a.ManagementTLS == nil {
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("management TLS rotation is unavailable"))
	}
	if err := a.ManagementTLS.finalizeCleanup(ctx, req.Msg.Force); err != nil {
		return nil, managementTLSConnectError(err)
	}
	rotation, err := a.managementTLSSnapshot(ctx)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	return connect.NewResponse(&p2pstreamv1.FinalizeManagementTlsTrustCleanupResponse{Rotation: rotation}), nil
}
