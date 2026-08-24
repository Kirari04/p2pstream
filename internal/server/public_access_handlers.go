package server

import (
	"context"

	"connectrpc.com/connect"

	p2pstreamv1 "p2pstream/gen/proto/p2pstream/v1"
	"p2pstream/internal/db"
)

func (a *App) CreatePublicAccessProvider(
	ctx context.Context,
	req *connect.Request[p2pstreamv1.CreatePublicAccessProviderRequest],
) (*connect.Response[p2pstreamv1.CreatePublicAccessProviderResponse], error) {
	if _, err := a.requireAdmin(ctx, req.Header()); err != nil {
		return nil, err
	}
	params, err := validatePublicAccessProviderInput(
		req.Msg.Name, req.Msg.ProviderType, req.Msg.Enabled, req.Msg.ForwardAuthUrl,
		req.Msg.TimeoutMillis, req.Msg.TlsSkipVerify, req.Msg.SubjectHeader,
		req.Msg.UserHeader, req.Msg.EmailHeader, req.Msg.GroupsHeader, req.Msg.ForwardedHeaders,
	)
	if err != nil {
		return nil, err
	}
	provider, err := a.DB.CreatePublicAccessProvider(ctx, db.CreatePublicAccessProviderParams{
		Name: params.Name, ProviderType: params.ProviderType, Enabled: params.Enabled,
		ForwardAuthUrl: params.ForwardAuthUrl, TimeoutMillis: params.TimeoutMillis,
		TlsSkipVerify: params.TlsSkipVerify, SubjectHeader: params.SubjectHeader,
		UserHeader: params.UserHeader, EmailHeader: params.EmailHeader,
		GroupsHeader: params.GroupsHeader, ForwardedHeadersJson: params.ForwardedHeadersJson,
	})
	if err != nil {
		return nil, publicDBError(err)
	}
	if err := a.refreshPublicProxySnapshot(ctx); err != nil {
		return nil, err
	}
	return connect.NewResponse(&p2pstreamv1.CreatePublicAccessProviderResponse{Provider: publicAccessProviderToProto(provider)}), nil
}

func (a *App) UpdatePublicAccessProvider(
	ctx context.Context,
	req *connect.Request[p2pstreamv1.UpdatePublicAccessProviderRequest],
) (*connect.Response[p2pstreamv1.UpdatePublicAccessProviderResponse], error) {
	if _, err := a.requireAdmin(ctx, req.Header()); err != nil {
		return nil, err
	}
	params, err := validatePublicAccessProviderInput(
		req.Msg.Name, req.Msg.ProviderType, req.Msg.Enabled, req.Msg.ForwardAuthUrl,
		req.Msg.TimeoutMillis, req.Msg.TlsSkipVerify, req.Msg.SubjectHeader,
		req.Msg.UserHeader, req.Msg.EmailHeader, req.Msg.GroupsHeader, req.Msg.ForwardedHeaders,
	)
	if err != nil {
		return nil, err
	}
	params.ID = req.Msg.Id
	provider, err := a.DB.UpdatePublicAccessProvider(ctx, params)
	if err != nil {
		return nil, publicDBError(err)
	}
	if err := a.refreshPublicProxySnapshot(ctx); err != nil {
		return nil, err
	}
	return connect.NewResponse(&p2pstreamv1.UpdatePublicAccessProviderResponse{Provider: publicAccessProviderToProto(provider)}), nil
}

func (a *App) DeletePublicAccessProvider(
	ctx context.Context,
	req *connect.Request[p2pstreamv1.DeletePublicAccessProviderRequest],
) (*connect.Response[p2pstreamv1.DeletePublicAccessProviderResponse], error) {
	if _, err := a.requireAdmin(ctx, req.Header()); err != nil {
		return nil, err
	}
	if err := a.DB.DeletePublicAccessProvider(ctx, req.Msg.Id); err != nil {
		return nil, publicDBError(err)
	}
	if err := a.refreshPublicProxySnapshot(ctx); err != nil {
		return nil, err
	}
	return connect.NewResponse(&p2pstreamv1.DeletePublicAccessProviderResponse{}), nil
}

func (a *App) CreatePublicAccessPolicy(
	ctx context.Context,
	req *connect.Request[p2pstreamv1.CreatePublicAccessPolicyRequest],
) (*connect.Response[p2pstreamv1.CreatePublicAccessPolicyResponse], error) {
	if _, err := a.requireAdmin(ctx, req.Header()); err != nil {
		return nil, err
	}
	params, err := validatePublicAccessPolicyInput(
		ctx, a.DB, req.Msg.Name, req.Msg.ProviderId, req.Msg.Enabled,
		req.Msg.RequiredGroups, req.Msg.GroupMatch,
	)
	if err != nil {
		return nil, err
	}
	policy, err := a.DB.CreatePublicAccessPolicy(ctx, db.CreatePublicAccessPolicyParams{
		Name: params.Name, ProviderID: params.ProviderID, Enabled: params.Enabled,
		RequiredGroupsJson: params.RequiredGroupsJson, GroupMatch: params.GroupMatch,
	})
	if err != nil {
		return nil, publicDBError(err)
	}
	if err := a.refreshPublicProxySnapshot(ctx); err != nil {
		return nil, err
	}
	return connect.NewResponse(&p2pstreamv1.CreatePublicAccessPolicyResponse{Policy: publicAccessPolicyToProto(policy)}), nil
}

func (a *App) UpdatePublicAccessPolicy(
	ctx context.Context,
	req *connect.Request[p2pstreamv1.UpdatePublicAccessPolicyRequest],
) (*connect.Response[p2pstreamv1.UpdatePublicAccessPolicyResponse], error) {
	if _, err := a.requireAdmin(ctx, req.Header()); err != nil {
		return nil, err
	}
	params, err := validatePublicAccessPolicyInput(
		ctx, a.DB, req.Msg.Name, req.Msg.ProviderId, req.Msg.Enabled,
		req.Msg.RequiredGroups, req.Msg.GroupMatch,
	)
	if err != nil {
		return nil, err
	}
	params.ID = req.Msg.Id
	policy, err := a.DB.UpdatePublicAccessPolicy(ctx, params)
	if err != nil {
		return nil, publicDBError(err)
	}
	if err := a.refreshPublicProxySnapshot(ctx); err != nil {
		return nil, err
	}
	return connect.NewResponse(&p2pstreamv1.UpdatePublicAccessPolicyResponse{Policy: publicAccessPolicyToProto(policy)}), nil
}

func (a *App) DeletePublicAccessPolicy(
	ctx context.Context,
	req *connect.Request[p2pstreamv1.DeletePublicAccessPolicyRequest],
) (*connect.Response[p2pstreamv1.DeletePublicAccessPolicyResponse], error) {
	if _, err := a.requireAdmin(ctx, req.Header()); err != nil {
		return nil, err
	}
	if err := a.DB.DeletePublicAccessPolicy(ctx, req.Msg.Id); err != nil {
		return nil, publicDBError(err)
	}
	if err := a.refreshPublicProxySnapshot(ctx); err != nil {
		return nil, err
	}
	return connect.NewResponse(&p2pstreamv1.DeletePublicAccessPolicyResponse{}), nil
}
