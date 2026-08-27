package server

import (
	"context"
	"errors"

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
		req.Msg.LocalAuthMode, req.Msg.LocalAuthSessionDurationMillis, req.Msg.LocalAuthRealm,
		req.Msg.LocalAuthLoginTemplateId,
	)
	if err != nil {
		return nil, err
	}
	if params.ProviderType == publicAccessProviderTypeLocal && req.Msg.LocalAuthLoginTemplateId <= 0 {
		if _, err := a.ensureDefaultPublicResponseTemplates(ctx); err != nil {
			return nil, err
		}
	}
	params.LocalAuthLoginTemplateID, err = resolveLocalAccessLoginTemplateReference(
		ctx, a.DB, params.ProviderType, req.Msg.LocalAuthLoginTemplateId,
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
		LocalAuthMode: params.LocalAuthMode, LocalAuthSessionDurationMillis: params.LocalAuthSessionDurationMillis,
		LocalAuthRealm: params.LocalAuthRealm, LocalAuthLoginTemplateID: params.LocalAuthLoginTemplateID,
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
		req.Msg.LocalAuthMode, req.Msg.LocalAuthSessionDurationMillis, req.Msg.LocalAuthRealm,
		req.Msg.LocalAuthLoginTemplateId,
	)
	if err != nil {
		return nil, err
	}
	tx, err := a.DB.BeginTx(ctx, nil)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	defer tx.Rollback()
	qtx := a.DB.WithTx(tx)
	existing, err := qtx.GetPublicAccessProvider(ctx, req.Msg.Id)
	if err != nil {
		return nil, publicDBError(err)
	}
	if normalizePublicAccessProviderType(existing.ProviderType) != params.ProviderType {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("access provider type cannot be changed; create a new provider instead"))
	}
	templateID := req.Msg.LocalAuthLoginTemplateId
	if templateID <= 0 && existing.LocalAuthLoginTemplateID.Valid {
		templateID = existing.LocalAuthLoginTemplateID.Int64
	}
	params.LocalAuthLoginTemplateID, err = resolveLocalAccessLoginTemplateReference(ctx, qtx, params.ProviderType, templateID)
	if err != nil {
		return nil, err
	}
	params.ID = req.Msg.Id
	provider, err := qtx.UpdatePublicAccessProvider(ctx, params)
	if err != nil {
		return nil, publicDBError(err)
	}
	if normalizePublicAccessProviderType(existing.ProviderType) == publicAccessProviderTypeLocal &&
		(normalizePublicAccessProviderType(provider.ProviderType) != publicAccessProviderTypeLocal ||
			provider.Enabled == 0 || existing.LocalAuthMode != provider.LocalAuthMode ||
			existing.LocalAuthSessionDurationMillis != provider.LocalAuthSessionDurationMillis) {
		if _, err := qtx.RevokePublicAccessProviderSessions(ctx, provider.ID); err != nil {
			return nil, connect.NewError(connect.CodeInternal, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	if err := a.refreshPublicProxySnapshot(ctx); err != nil {
		return nil, err
	}
	return connect.NewResponse(&p2pstreamv1.UpdatePublicAccessProviderResponse{Provider: publicAccessProviderToProto(provider)}), nil
}

func (a *App) CreatePublicAccessUser(
	ctx context.Context,
	req *connect.Request[p2pstreamv1.CreatePublicAccessUserRequest],
) (*connect.Response[p2pstreamv1.CreatePublicAccessUserResponse], error) {
	if _, err := a.requireAdmin(ctx, req.Header()); err != nil {
		return nil, err
	}
	params, err := validatePublicAccessUserInput(req.Msg.Username, req.Msg.Password, req.Msg.Enabled, req.Msg.Groups, "")
	if err != nil {
		return nil, err
	}
	tx, err := a.DB.BeginTx(ctx, nil)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	defer tx.Rollback()
	qtx := a.DB.WithTx(tx)
	provider, err := qtx.GetPublicAccessProvider(ctx, req.Msg.ProviderId)
	if err != nil {
		return nil, publicDBError(err)
	}
	if normalizePublicAccessProviderType(provider.ProviderType) != publicAccessProviderTypeLocal {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("users can only be added to a local access provider"))
	}
	user, err := qtx.CreatePublicAccessUser(ctx, db.CreatePublicAccessUserParams{
		ProviderID: req.Msg.ProviderId, Username: params.Username, PasswordHash: params.PasswordHash,
		Enabled: params.Enabled, GroupsJson: params.GroupsJson,
	})
	if err != nil {
		return nil, publicDBError(err)
	}
	if err := tx.Commit(); err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	if err := a.refreshPublicProxySnapshot(ctx); err != nil {
		return nil, err
	}
	return connect.NewResponse(&p2pstreamv1.CreatePublicAccessUserResponse{User: publicAccessUserToProto(user)}), nil
}

func (a *App) UpdatePublicAccessUser(
	ctx context.Context,
	req *connect.Request[p2pstreamv1.UpdatePublicAccessUserRequest],
) (*connect.Response[p2pstreamv1.UpdatePublicAccessUserResponse], error) {
	if _, err := a.requireAdmin(ctx, req.Header()); err != nil {
		return nil, err
	}
	existing, err := a.DB.GetPublicAccessUser(ctx, req.Msg.Id)
	if err != nil {
		return nil, publicDBError(err)
	}
	params, err := validatePublicAccessUserInput(req.Msg.Username, req.Msg.Password, req.Msg.Enabled, req.Msg.Groups, existing.PasswordHash)
	if err != nil {
		return nil, err
	}
	tx, err := a.DB.BeginTx(ctx, nil)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	defer tx.Rollback()
	qtx := a.DB.WithTx(tx)
	current, err := qtx.GetPublicAccessUser(ctx, req.Msg.Id)
	if err != nil {
		return nil, publicDBError(err)
	}
	if req.Msg.Password == "" {
		params.PasswordHash = current.PasswordHash
	}
	params.ID = req.Msg.Id
	user, err := qtx.UpdatePublicAccessUser(ctx, params)
	if err != nil {
		return nil, publicDBError(err)
	}
	if _, err := qtx.RevokePublicAccessUserSessions(ctx, user.ID); err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	if err := tx.Commit(); err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	if err := a.refreshPublicProxySnapshot(ctx); err != nil {
		return nil, err
	}
	return connect.NewResponse(&p2pstreamv1.UpdatePublicAccessUserResponse{User: publicAccessUserToProto(user)}), nil
}

func (a *App) DeletePublicAccessUser(
	ctx context.Context,
	req *connect.Request[p2pstreamv1.DeletePublicAccessUserRequest],
) (*connect.Response[p2pstreamv1.DeletePublicAccessUserResponse], error) {
	if _, err := a.requireAdmin(ctx, req.Header()); err != nil {
		return nil, err
	}
	if _, err := a.DB.GetPublicAccessUser(ctx, req.Msg.Id); err != nil {
		return nil, publicDBError(err)
	}
	if err := a.DB.DeletePublicAccessUser(ctx, req.Msg.Id); err != nil {
		return nil, publicDBError(err)
	}
	if err := a.refreshPublicProxySnapshot(ctx); err != nil {
		return nil, err
	}
	return connect.NewResponse(&p2pstreamv1.DeletePublicAccessUserResponse{}), nil
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
