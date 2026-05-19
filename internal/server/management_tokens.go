package server

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"net/http"
	"strings"
	"time"

	"connectrpc.com/connect"

	p2pstreamv1 "p2pstream/gen/proto/p2pstream/v1"
	"p2pstream/internal/db"
)

const (
	managementAccessTokenBytes  = 32
	managementAccessTokenPrefix = "p2pat_"
)

func (a *App) CreateManagementAccessToken(
	ctx context.Context,
	req *connect.Request[p2pstreamv1.CreateManagementAccessTokenRequest],
) (*connect.Response[p2pstreamv1.CreateManagementAccessTokenResponse], error) {
	if _, err := a.requireAdmin(ctx, req.Header()); err != nil {
		return nil, err
	}
	name, err := validateAgentName(req.Msg.Name)
	if err != nil {
		return nil, err
	}
	expiresAt, err := validateManagementAccessTokenExpiry(req.Msg.ExpiresAtUnixMillis)
	if err != nil {
		return nil, err
	}
	token, tokenHash, err := newManagementAccessToken()
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	row, err := a.DB.CreateManagementAccessToken(ctx, db.CreateManagementAccessTokenParams{
		Name:      name,
		TokenHash: tokenHash,
		Enabled:   boolInt(req.Msg.Enabled),
		ExpiresAt: expiresAt,
	})
	if err != nil {
		return nil, publicDBError(err)
	}
	return connect.NewResponse(&p2pstreamv1.CreateManagementAccessTokenResponse{
		AccessToken: managementAccessTokenToProto(row),
		Token:       token,
	}), nil
}

func (a *App) ListManagementAccessTokens(
	ctx context.Context,
	req *connect.Request[p2pstreamv1.ListManagementAccessTokensRequest],
) (*connect.Response[p2pstreamv1.ListManagementAccessTokensResponse], error) {
	if _, err := a.requireAdmin(ctx, req.Header()); err != nil {
		return nil, err
	}
	rows, err := a.DB.ListManagementAccessTokens(ctx)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	resp := &p2pstreamv1.ListManagementAccessTokensResponse{
		AccessTokens: make([]*p2pstreamv1.ManagementAccessToken, 0, len(rows)),
	}
	for _, row := range rows {
		resp.AccessTokens = append(resp.AccessTokens, managementAccessTokenToProto(row))
	}
	return connect.NewResponse(resp), nil
}

func (a *App) DeleteManagementAccessToken(
	ctx context.Context,
	req *connect.Request[p2pstreamv1.DeleteManagementAccessTokenRequest],
) (*connect.Response[p2pstreamv1.DeleteManagementAccessTokenResponse], error) {
	if _, err := a.requireAdmin(ctx, req.Header()); err != nil {
		return nil, err
	}
	if err := a.DB.DeleteManagementAccessToken(ctx, req.Msg.Id); err != nil {
		return nil, publicDBError(err)
	}
	return connect.NewResponse(&p2pstreamv1.DeleteManagementAccessTokenResponse{}), nil
}

func validateManagementAccessTokenExpiry(expiresAtMillis int64) (sql.NullTime, error) {
	if expiresAtMillis == 0 {
		return sql.NullTime{}, nil
	}
	expiresAt := time.UnixMilli(expiresAtMillis)
	if !expiresAt.After(time.Now()) {
		return sql.NullTime{}, connect.NewError(connect.CodeInvalidArgument, errors.New("access token expiry must be in the future"))
	}
	return sql.NullTime{Time: expiresAt, Valid: true}, nil
}

func managementAccessTokenToProto(row db.ManagementAccessToken) *p2pstreamv1.ManagementAccessToken {
	return &p2pstreamv1.ManagementAccessToken{
		Id:                   row.ID,
		Name:                 row.Name,
		Enabled:              row.Enabled != 0,
		ExpiresAtUnixMillis:  nullTimeUnixMillis(row.ExpiresAt),
		LastUsedAtUnixMillis: nullTimeUnixMillis(row.LastUsedAt),
		CreatedAtUnixMillis:  row.CreatedAt.UnixMilli(),
		UpdatedAtUnixMillis:  row.UpdatedAt.UnixMilli(),
	}
}

func newManagementAccessToken() (token string, tokenHash string, err error) {
	buf := make([]byte, managementAccessTokenBytes)
	if _, err := rand.Read(buf); err != nil {
		return "", "", err
	}
	token = managementAccessTokenPrefix + base64.RawURLEncoding.EncodeToString(buf)
	return token, hashManagementAccessToken(token), nil
}

func hashManagementAccessToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

func managementAccessTokenFromHeader(header http.Header) string {
	token := bearerToken(header.Get("Authorization"))
	if !strings.HasPrefix(token, managementAccessTokenPrefix) {
		return ""
	}
	return token
}
