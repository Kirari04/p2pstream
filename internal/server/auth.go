package server

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/x509"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"net/http"
	"strings"
	"time"

	"connectrpc.com/connect"
	"github.com/rs/zerolog/log"

	p2pstreamv1 "p2pstream/gen/proto/p2pstream/v1"
	"p2pstream/internal/authutil"
	"p2pstream/internal/db"
)

const (
	sessionCookieName       = "p2pstream_session"
	setupWindow             = 5 * time.Minute
	setupTokenBytes         = 32
	sessionDuration         = 7 * 24 * time.Hour
	sessionTouchInterval    = 30 * time.Second
	setupWindowExpiredError = "setup window expired; restart the server to retry setup"
)

type authenticatedUser struct {
	ID            int64
	Username      string
	Role          p2pstreamv1.UserRole
	SessionID     int64
	TokenHash     string
	IsAccessToken bool
}

type managementClientCertificateContextKey struct{}

const agentCertificateURIPrefix = "spiffe://p2pstream/agent/"

// ManagementClientCertificateMiddleware stores the verified client certificate
// from the TLS handshake in the request context for agent-authenticated routes.
func ManagementClientCertificateMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if cert := verifiedManagementClientCertificate(r); cert != nil {
			ctx := context.WithValue(r.Context(), managementClientCertificateContextKey{}, cert)
			r = r.WithContext(ctx)
		}
		next.ServeHTTP(w, r)
	})
}

func verifiedManagementClientCertificate(r *http.Request) *x509.Certificate {
	if r == nil || r.TLS == nil || len(r.TLS.VerifiedChains) == 0 || len(r.TLS.VerifiedChains[0]) == 0 {
		return nil
	}
	return r.TLS.VerifiedChains[0][0]
}

func managementClientCertificateFromContext(ctx context.Context) (*x509.Certificate, bool) {
	cert, ok := ctx.Value(managementClientCertificateContextKey{}).(*x509.Certificate)
	return cert, ok && cert != nil
}

func (a *App) requireAgentClientCertificate(ctx context.Context, publicID string) error {
	if a == nil || a.Config == nil || strings.TrimSpace(a.Config.ManagementTLSClientCAFile) == "" {
		return nil
	}
	cert, ok := managementClientCertificateFromContext(ctx)
	if !ok {
		return errors.New("agent client certificate required")
	}
	if !agentCertificateMatchesPublicID(cert, publicID) {
		return errors.New("agent client certificate does not match agent id")
	}
	return nil
}

func agentCertificateMatchesPublicID(cert *x509.Certificate, publicID string) bool {
	if cert == nil {
		return false
	}
	want := agentCertificateURI(publicID)
	for _, uri := range cert.URIs {
		if uri.String() == want {
			return true
		}
	}
	return false
}

func agentCertificateURI(publicID string) string {
	return agentCertificateURIPrefix + publicID
}

func (a *App) GetSetupState(
	ctx context.Context,
	req *connect.Request[p2pstreamv1.GetSetupStateRequest],
) (*connect.Response[p2pstreamv1.GetSetupStateResponse], error) {
	_ = req

	state, err := a.getSetupState(ctx)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(state), nil
}

func (a *App) SetupAdmin(
	ctx context.Context,
	req *connect.Request[p2pstreamv1.SetupAdminRequest],
) (*connect.Response[p2pstreamv1.SetupAdminResponse], error) {
	if a.DB == nil {
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("database is required for setup"))
	}

	a.setupMu.Lock()
	defer a.setupMu.Unlock()

	tx, err := a.DB.BeginTx(ctx, nil)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	defer tx.Rollback()

	qtx := a.DB.WithTx(tx)
	count, err := qtx.CountUsers(ctx)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	if count > 0 {
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("setup has already been completed"))
	}
	if !a.setupAvailable(time.Now()) {
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New(setupWindowExpiredError))
	}
	if !a.validSetupToken(req.Msg.SetupToken) {
		return nil, connect.NewError(connect.CodeUnauthenticated, errors.New("valid setup token required"))
	}

	username := authutil.NormalizeUsername(req.Msg.Username)
	if err := authutil.ValidateUsername(username); err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	if err := authutil.ValidatePassword(req.Msg.Password); err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}

	passwordHash, err := authutil.HashPassword(req.Msg.Password)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}

	user, err := qtx.CreateUser(ctx, db.CreateUserParams{
		Username:     username,
		PasswordHash: passwordHash,
		Role:         roleString(p2pstreamv1.UserRole_USER_ROLE_ADMIN),
	})
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	if err := tx.Commit(); err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}

	return connect.NewResponse(&p2pstreamv1.SetupAdminResponse{
		User: &p2pstreamv1.User{
			Id:       user.ID,
			Username: user.Username,
			Role:     protoRole(user.Role),
		},
	}), nil
}

func (a *App) initializeSetupToken(ctx context.Context) {
	if a == nil || a.DB == nil {
		return
	}
	if token := strings.TrimSpace(a.Config.ManagementSetupToken); token != "" {
		a.setupTokenHash = hashSetupToken(token)
		return
	}
	count, err := a.DB.CountUsers(ctx)
	if err != nil {
		log.Warn().Err(err).Msg("Failed to inspect setup state for setup token initialization")
		return
	}
	if count > 0 {
		return
	}
	token, tokenHash, err := newSetupToken()
	if err != nil {
		log.Error().Err(err).Msg("Failed to generate setup token")
		return
	}
	a.generatedSetupToken = token
	a.setupTokenHash = tokenHash
}

func (a *App) LogGeneratedSetupToken(baseURL string) {
	if a == nil || a.generatedSetupToken == "" {
		return
	}
	a.setupTokenLogOnce.Do(func() {
		log.Warn().
			Str("setup_token", a.generatedSetupToken).
			Str("url", setupURLWithToken(baseURL, a.generatedSetupToken)).
			Msg("Initial setup requires this one-time setup token")
	})
}

func setupURLWithToken(baseURL string, token string) string {
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if baseURL == "" {
		return ""
	}
	return baseURL + "/?setup_token=" + token
}

func (a *App) validSetupToken(token string) bool {
	token = strings.TrimSpace(token)
	if a == nil || a.setupTokenHash == "" || token == "" {
		return false
	}
	got := hashSetupToken(token)
	return subtle.ConstantTimeCompare([]byte(got), []byte(a.setupTokenHash)) == 1
}

func newSetupToken() (token string, tokenHash string, err error) {
	buf := make([]byte, setupTokenBytes)
	if _, err := rand.Read(buf); err != nil {
		return "", "", err
	}
	token = base64.RawURLEncoding.EncodeToString(buf)
	return token, hashSetupToken(token), nil
}

func hashSetupToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

func (a *App) Login(
	ctx context.Context,
	req *connect.Request[p2pstreamv1.LoginRequest],
) (*connect.Response[p2pstreamv1.LoginResponse], error) {
	if a.DB == nil {
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("database is required for login"))
	}

	username := authutil.NormalizeUsername(req.Msg.Username)
	throttleKey := loginThrottleKey(req.Peer().Addr, username)
	now := time.Now()
	if retryAfter := a.LoginThrottle.retryAfter(throttleKey, now); retryAfter > 0 {
		return nil, connect.NewError(connect.CodeResourceExhausted, errors.New("too many failed login attempts; try again later"))
	}
	user, err := a.DB.GetUserByUsername(ctx, username)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			a.LoginThrottle.recordFailure(throttleKey, now)
			return nil, connect.NewError(connect.CodeUnauthenticated, errors.New("invalid username or password"))
		}
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	if err := authutil.ComparePasswordHash(user.PasswordHash, req.Msg.Password); err != nil {
		a.LoginThrottle.recordFailure(throttleKey, now)
		return nil, connect.NewError(connect.CodeUnauthenticated, errors.New("invalid username or password"))
	}
	a.LoginThrottle.recordSuccess(throttleKey)

	token, tokenHash, err := newSessionToken()
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	expiresAt := time.Now().Add(sessionDuration)
	if _, err := a.DB.CreateSession(ctx, db.CreateSessionParams{
		UserID:    user.ID,
		TokenHash: tokenHash,
		ExpiresAt: expiresAt,
	}); err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}

	resp := connect.NewResponse(&p2pstreamv1.LoginResponse{User: dbUserToProto(user)})
	resp.Header().Add("Set-Cookie", a.sessionCookie(token, expiresAt).String())
	return resp, nil
}

func (a *App) Logout(
	ctx context.Context,
	req *connect.Request[p2pstreamv1.LogoutRequest],
) (*connect.Response[p2pstreamv1.LogoutResponse], error) {
	user, err := a.requireUser(ctx, req.Header())
	if err != nil {
		return nil, err
	}
	if err := a.DB.RevokeSessionByTokenHash(ctx, user.TokenHash); err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}

	resp := connect.NewResponse(&p2pstreamv1.LogoutResponse{})
	resp.Header().Add("Set-Cookie", a.clearSessionCookie().String())
	return resp, nil
}

func (a *App) GetCurrentUser(
	ctx context.Context,
	req *connect.Request[p2pstreamv1.GetCurrentUserRequest],
) (*connect.Response[p2pstreamv1.GetCurrentUserResponse], error) {
	user, err := a.requireUser(ctx, req.Header())
	if err != nil {
		return nil, err
	}

	return connect.NewResponse(&p2pstreamv1.GetCurrentUserResponse{
		User: &p2pstreamv1.User{
			Id:       user.ID,
			Username: user.Username,
			Role:     user.Role,
		},
	}), nil
}

func (a *App) getSetupState(ctx context.Context) (*p2pstreamv1.GetSetupStateResponse, error) {
	if a.DB == nil {
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("database is required for setup"))
	}

	count, err := a.DB.CountUsers(ctx)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	if count > 0 {
		return &p2pstreamv1.GetSetupStateResponse{}, nil
	}

	expiresAt := a.StartedAt.Add(setupWindow)
	resp := &p2pstreamv1.GetSetupStateResponse{
		SetupRequired:            true,
		SetupAvailable:           time.Now().Before(expiresAt) || time.Now().Equal(expiresAt),
		SetupExpiresAtUnixMillis: expiresAt.UnixMilli(),
	}
	if !resp.SetupAvailable {
		resp.SetupUnavailableReason = setupWindowExpiredError
	}
	return resp, nil
}

func (a *App) setupAvailable(now time.Time) bool {
	expiresAt := a.StartedAt.Add(setupWindow)
	return now.Before(expiresAt) || now.Equal(expiresAt)
}

func (a *App) requireUser(ctx context.Context, header http.Header) (*authenticatedUser, error) {
	if a.DB == nil {
		return nil, connect.NewError(connect.CodeUnauthenticated, errors.New("authentication is unavailable"))
	}

	if accessToken := managementAccessTokenFromHeader(header); accessToken != "" {
		tokenHash := hashManagementAccessToken(accessToken)
		row, err := a.DB.GetActiveManagementAccessTokenByHash(ctx, tokenHash)
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return nil, connect.NewError(connect.CodeUnauthenticated, errors.New("login required"))
			}
			return nil, connect.NewError(connect.CodeInternal, err)
		}
		if row.ExpiresAt.Valid && !row.ExpiresAt.Time.After(time.Now()) {
			return nil, connect.NewError(connect.CodeUnauthenticated, errors.New("login required"))
		}
		if err := a.DB.TouchManagementAccessToken(ctx, row.ID); err != nil {
			return nil, connect.NewError(connect.CodeInternal, err)
		}
		return &authenticatedUser{
			Username:      row.Name,
			Role:          protoRole(row.Role),
			TokenHash:     tokenHash,
			IsAccessToken: true,
		}, nil
	}

	token := sessionTokenFromHeader(header)
	if token == "" {
		return nil, connect.NewError(connect.CodeUnauthenticated, errors.New("login required"))
	}

	tokenHash := hashSessionToken(token)
	session, err := a.DB.GetActiveSessionByTokenHash(ctx, tokenHash)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, connect.NewError(connect.CodeUnauthenticated, errors.New("login required"))
		}
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	if session.LastSeenAt.IsZero() || time.Since(session.LastSeenAt) >= sessionTouchInterval {
		if err := a.DB.TouchSession(ctx, session.SessionID); err != nil {
			return nil, connect.NewError(connect.CodeInternal, err)
		}
	}

	return &authenticatedUser{
		ID:        session.ID,
		Username:  session.Username,
		Role:      protoRole(session.Role),
		SessionID: session.SessionID,
		TokenHash: tokenHash,
	}, nil
}

func (a *App) requireAdmin(ctx context.Context, header http.Header) (*authenticatedUser, error) {
	user, err := a.requireUser(ctx, header)
	if err != nil {
		return nil, err
	}
	if user.Role != p2pstreamv1.UserRole_USER_ROLE_ADMIN {
		return nil, connect.NewError(connect.CodePermissionDenied, errors.New("admin permission required"))
	}
	return user, nil
}

func sessionTokenFromHeader(header http.Header) string {
	req := &http.Request{Header: header}
	cookie, err := req.Cookie(sessionCookieName)
	if err != nil {
		return ""
	}
	return cookie.Value
}

func newSessionToken() (token string, tokenHash string, err error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", "", err
	}
	token = base64.RawURLEncoding.EncodeToString(buf)
	return token, hashSessionToken(token), nil
}

func hashSessionToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

func (a *App) sessionCookie(token string, expiresAt time.Time) *http.Cookie {
	return &http.Cookie{
		Name:     sessionCookieName,
		Value:    token,
		Path:     "/",
		Expires:  expiresAt,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   a.secureCookies(),
	}
}

func (a *App) clearSessionCookie() *http.Cookie {
	return &http.Cookie{
		Name:     sessionCookieName,
		Value:    "",
		Path:     "/",
		Expires:  time.Unix(0, 0),
		MaxAge:   -1,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   a.secureCookies(),
	}
}

func (a *App) secureCookies() bool {
	return a.Config != nil && (a.Config.ManagementTLSEnabled || a.Config.Env == "production" || a.Config.ManagementCookieSecure)
}

func dbUserToProto(user db.User) *p2pstreamv1.User {
	return &p2pstreamv1.User{
		Id:       user.ID,
		Username: user.Username,
		Role:     protoRole(user.Role),
	}
}

func protoRole(role string) p2pstreamv1.UserRole {
	switch role {
	case roleString(p2pstreamv1.UserRole_USER_ROLE_ADMIN):
		return p2pstreamv1.UserRole_USER_ROLE_ADMIN
	default:
		return p2pstreamv1.UserRole_USER_ROLE_UNSPECIFIED
	}
}

func roleString(role p2pstreamv1.UserRole) string {
	switch role {
	case p2pstreamv1.UserRole_USER_ROLE_ADMIN:
		return "admin"
	default:
		return "unspecified"
	}
}
