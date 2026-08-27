package server

import (
	"bytes"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	p2pstreamv1 "p2pstream/gen/proto/p2pstream/v1"
	"p2pstream/internal/authutil"
	"p2pstream/internal/db"
)

const publicAccessBasicAuthCacheTTL = 5 * time.Minute

type publicAccessBasicAuthCache struct {
	mu         sync.Mutex
	entries    map[[sha256.Size]byte]publicAccessBasicAuthCacheEntry
	maxEntries int
}

type publicAccessBasicAuthCacheEntry struct {
	username string
	expires  time.Time
}

func newPublicAccessBasicAuthCache(maxEntries int) *publicAccessBasicAuthCache {
	if maxEntries <= 0 {
		maxEntries = 1024
	}
	return &publicAccessBasicAuthCache{entries: make(map[[sha256.Size]byte]publicAccessBasicAuthCacheEntry), maxEntries: maxEntries}
}

func (cache *publicAccessBasicAuthCache) get(authorization string, now time.Time) (string, bool) {
	if cache == nil {
		return "", false
	}
	key := sha256.Sum256([]byte(authorization))
	cache.mu.Lock()
	defer cache.mu.Unlock()
	entry, ok := cache.entries[key]
	if !ok {
		return "", false
	}
	if !now.Before(entry.expires) {
		delete(cache.entries, key)
		return "", false
	}
	return entry.username, true
}

func (cache *publicAccessBasicAuthCache) put(authorization string, username string, now time.Time) {
	if cache == nil {
		return
	}
	key := sha256.Sum256([]byte(authorization))
	cache.mu.Lock()
	defer cache.mu.Unlock()
	if len(cache.entries) >= cache.maxEntries {
		for existingKey, entry := range cache.entries {
			if !now.Before(entry.expires) {
				delete(cache.entries, existingKey)
			}
		}
	}
	if len(cache.entries) >= cache.maxEntries {
		for existingKey := range cache.entries {
			delete(cache.entries, existingKey)
			break
		}
	}
	cache.entries[key] = publicAccessBasicAuthCacheEntry{username: username, expires: now.Add(publicAccessBasicAuthCacheTTL)}
}

const (
	publicAccessLoginQueryKey     = "__p2pstream_access_login"
	publicAccessLogoutQueryKey    = "__p2pstream_access_logout"
	publicAccessUsernameField     = "username"
	publicAccessPasswordField     = "password"
	publicAccessCSRFField         = "p2pstream_csrf"
	maxPublicAccessLoginBytes     = 8 << 10
	publicAccessDummyPasswordHash = "$2b$12$MwX9zrPPoFSFodgWgJrvW.0eWjm7iErz/KNc76leHdqgN38m/8TUe"
)

func handlePublicLocalAccess(
	ctx *publicProxyContext,
	policy publicAccessPolicyConfig,
	provider publicAccessProviderConfig,
) (publicAccessPrincipal, publicProxyStageResult, error) {
	if ctx == nil || ctx.App == nil || ctx.App.DB == nil || ctx.Request == nil {
		return publicAccessPrincipal{}, publicProxyStageDone, errors.New("local authentication database is unavailable")
	}
	query := ctx.Request.URL.Query()
	if values, exists := query[publicAccessLogoutQueryKey]; exists && (len(values) != 1 || values[0] != "1") {
		return publicAccessPrincipal{}, rejectPublicAccessDenied(ctx, policy, provider, http.StatusBadRequest, "access_logout_invalid"), nil
	}
	if values, exists := query[publicAccessLoginQueryKey]; exists && (len(values) != 1 || values[0] != "1") {
		return publicAccessPrincipal{}, rejectPublicAccessDenied(ctx, policy, provider, http.StatusBadRequest, "access_login_invalid"), nil
	}
	if len(query[publicAccessLogoutQueryKey]) == 1 && len(query[publicAccessLoginQueryKey]) == 1 {
		return publicAccessPrincipal{}, rejectPublicAccessDenied(ctx, policy, provider, http.StatusBadRequest, "access_login_invalid"), nil
	}
	if len(query[publicAccessLogoutQueryKey]) == 1 {
		stage, err := handlePublicLocalLogout(ctx, policy, provider)
		return publicAccessPrincipal{}, stage, err
	}
	if len(query[publicAccessLoginQueryKey]) == 1 {
		if !publicAccessLocalFormEnabled(provider.LocalAuthMode) {
			return publicAccessPrincipal{}, rejectPublicAccessDenied(ctx, policy, provider, http.StatusNotFound, "access_login_unavailable"), nil
		}
		return handlePublicLocalFormLogin(ctx, policy, provider)
	}

	if publicAccessLocalBasicEnabled(provider.LocalAuthMode) && publicAccessRequestHasBasicAuthorization(ctx.Request) {
		principal, ok, throttled := authenticatePublicLocalBasic(ctx, provider)
		if ok {
			return principal, publicProxyStageContinue, nil
		}
		return publicAccessPrincipal{}, writePublicLocalBasicChallenge(ctx, policy, provider, throttled), nil
	}

	if publicAccessLocalFormEnabled(provider.LocalAuthMode) {
		principal, found, err := publicAccessPrincipalFromLocalSession(ctx, provider)
		if err != nil {
			return publicAccessPrincipal{}, publicProxyStageDone, err
		}
		if found {
			return principal, publicProxyStageContinue, nil
		}
	}

	if provider.LocalAuthMode == publicAccessLocalAuthModeBasic && len(ctx.Request.Header.Values("Authorization")) > 0 {
		principal, ok, throttled := authenticatePublicLocalBasic(ctx, provider)
		if ok {
			return principal, publicProxyStageContinue, nil
		}
		return publicAccessPrincipal{}, writePublicLocalBasicChallenge(ctx, policy, provider, throttled), nil
	}
	if provider.LocalAuthMode == publicAccessLocalAuthModeBasic {
		return publicAccessPrincipal{}, writePublicLocalBasicChallenge(ctx, policy, provider, false), nil
	}
	if err := writePublicLocalLoginForm(ctx, provider, "", "", http.StatusUnauthorized); err != nil {
		return publicAccessPrincipal{}, publicProxyStageDone, err
	}
	emitPublicAccessTrace(ctx, p2pstreamv1.TrafficTraceStage_TRAFFIC_TRACE_STAGE_ACCESS_DENIED, policy, provider, http.StatusUnauthorized, "access_unauthenticated")
	recordPublicAccessTerminal(ctx, http.StatusUnauthorized, "access_unauthenticated")
	return publicAccessPrincipal{}, publicProxyStageDone, nil
}

func publicAccessLocalFormEnabled(mode string) bool {
	return mode == publicAccessLocalAuthModeForm || mode == publicAccessLocalAuthModeBoth
}

func publicAccessLocalBasicEnabled(mode string) bool {
	return mode == publicAccessLocalAuthModeBasic || mode == publicAccessLocalAuthModeBoth
}

func publicAccessRequestHasBasicAuthorization(request *http.Request) bool {
	if request == nil {
		return false
	}
	for _, value := range request.Header.Values("Authorization") {
		if len(value) >= 6 && strings.EqualFold(value[:6], "Basic ") {
			return true
		}
	}
	return false
}

func publicAccessPrincipalFromLocalUser(provider publicAccessProviderConfig, user publicAccessUserConfig) publicAccessPrincipal {
	forwarded := make(http.Header)
	forwarded.Set("X-Auth-Request-User", user.Username)
	forwarded.Set("X-Auth-Request-Preferred-Username", user.Username)
	if len(user.Groups) > 0 {
		forwarded.Set("X-Auth-Request-Groups", strings.Join(user.Groups, ", "))
	}
	return publicAccessPrincipal{
		ProviderID: provider.ID, Subject: user.Username, Username: user.Username,
		Groups: append([]string(nil), user.Groups...), ForwardedHeader: forwarded,
	}
}

func publicAccessPrincipalFromLocalSession(ctx *publicProxyContext, provider publicAccessProviderConfig) (publicAccessPrincipal, bool, error) {
	cookies := ctx.Request.CookiesNamed(publicAccessSessionCookieName(provider.ID))
	if len(cookies) == 0 {
		return publicAccessPrincipal{}, false, nil
	}
	if len(cookies) != 1 || len(cookies[0].Value) < 32 || len(cookies[0].Value) > 128 {
		ctx.ResponseWriter.Header().Add("Set-Cookie", clearPublicAccessCookie(provider.ID, false, ctx.RouteMatch.Listener).String())
		return publicAccessPrincipal{}, false, nil
	}
	tokenHash := hashSessionToken(cookies[0].Value)
	row, err := ctx.App.DB.GetActivePublicAccessSession(ctx.Request.Context(), db.GetActivePublicAccessSessionParams{
		ProviderID: provider.ID, TokenHash: tokenHash,
	})
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			ctx.ResponseWriter.Header().Add("Set-Cookie", clearPublicAccessCookie(provider.ID, false, ctx.RouteMatch.Listener).String())
			return publicAccessPrincipal{}, false, nil
		}
		return publicAccessPrincipal{}, false, err
	}
	groups, err := publicAccessStringListFromJSON(row.GroupsJson)
	if err != nil {
		return publicAccessPrincipal{}, false, fmt.Errorf("local session groups: %w", err)
	}
	groups, err = normalizePublicAccessGroups(groups)
	if err != nil {
		return publicAccessPrincipal{}, false, fmt.Errorf("local session groups: %w", err)
	}
	if row.LastSeenAt.IsZero() || time.Since(row.LastSeenAt) >= sessionTouchInterval {
		if err := ctx.App.DB.TouchPublicAccessSession(ctx.Request.Context(), row.SessionID); err != nil {
			return publicAccessPrincipal{}, false, err
		}
	}
	return publicAccessPrincipalFromLocalUser(provider, publicAccessUserConfig{
		ID: row.UserID, ProviderID: row.ProviderID, Username: row.Username, Enabled: true, Groups: groups,
	}), true, nil
}

func authenticatePublicLocalBasic(ctx *publicProxyContext, provider publicAccessProviderConfig) (publicAccessPrincipal, bool, bool) {
	values := ctx.Request.Header.Values("Authorization")
	if len(values) != 1 || len(values[0]) > 1024 {
		return publicAccessPrincipal{}, false, false
	}
	if username, cached := provider.basicAuthCache.get(values[0], time.Now()); cached {
		if user, found := provider.LocalUsers[username]; found && user.Enabled {
			principal := publicAccessPrincipalFromLocalUser(provider, user)
			principal.StripAuthorization = true
			return principal, true, false
		}
	}
	username, password, ok := ctx.Request.BasicAuth()
	if !ok {
		return publicAccessPrincipal{}, false, false
	}
	username = authutil.NormalizeUsername(username)
	reservation, admitted := reservePublicAccessLoginAttempt(ctx, provider.ID, username)
	if !admitted {
		return publicAccessPrincipal{}, false, true
	}
	defer reservation.release()
	user, found := provider.LocalUsers[username]
	hash := publicAccessDummyPasswordHash
	if found && user.Enabled {
		hash = user.PasswordHash
	}
	if authutil.ComparePasswordHash(hash, password) != nil || !found || !user.Enabled {
		reservation.recordFailure(time.Now())
		return publicAccessPrincipal{}, false, false
	}
	reservation.recordSuccess()
	provider.basicAuthCache.put(values[0], username, time.Now())
	principal := publicAccessPrincipalFromLocalUser(provider, user)
	principal.StripAuthorization = true
	return principal, true, false
}

func reservePublicAccessLoginAttempt(ctx *publicProxyContext, providerID int64, username string) (*loginThrottleReservation, bool) {
	clientIP := publicAccessRequestClientIP(ctx.Request)
	if clientIP == "" {
		clientIP = remoteAddrIP(ctx.Request.RemoteAddr)
	}
	peerKey := strconv.FormatInt(providerID, 10) + "@" + clientIP
	return reserveLoginThrottleAttempt(
		ctx.App.publicAccessLoginThrottle,
		ctx.App.publicAccessClientLoginThrottle,
		loginThrottleKey(peerKey, username),
		loginThrottleClientKey(peerKey),
		time.Now(),
	)
}

func handlePublicLocalFormLogin(
	ctx *publicProxyContext,
	policy publicAccessPolicyConfig,
	provider publicAccessProviderConfig,
) (publicAccessPrincipal, publicProxyStageResult, error) {
	if ctx.Request.Method != http.MethodPost {
		return publicAccessPrincipal{}, rejectPublicAccessDenied(ctx, policy, provider, http.StatusMethodNotAllowed, "access_login_method"), nil
	}
	mediaType, _, err := mime.ParseMediaType(ctx.Request.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/x-www-form-urlencoded" {
		return publicAccessPrincipal{}, rejectPublicAccessDenied(ctx, policy, provider, http.StatusUnsupportedMediaType, "access_login_media_type"), nil
	}
	ctx.Request.Body = http.MaxBytesReader(ctx.ResponseWriter, ctx.Request.Body, maxPublicAccessLoginBytes)
	if err := ctx.Request.ParseForm(); err != nil {
		return publicAccessPrincipal{}, rejectPublicAccessDenied(ctx, policy, provider, http.StatusBadRequest, "access_login_invalid"), nil
	}
	csrfValues := ctx.Request.PostForm[publicAccessCSRFField]
	usernameValues := ctx.Request.PostForm[publicAccessUsernameField]
	passwordValues := ctx.Request.PostForm[publicAccessPasswordField]
	csrfCookies := ctx.Request.CookiesNamed(publicAccessCSRFCookieName(provider.ID))
	submittedUsername := ""
	if len(usernameValues) == 1 {
		submittedUsername = authutil.NormalizeUsername(usernameValues[0])
	}
	if len(csrfValues) != 1 || len(usernameValues) != 1 || len(passwordValues) != 1 || len(csrfCookies) != 1 ||
		len(csrfValues[0]) != 43 || subtle.ConstantTimeCompare([]byte(csrfValues[0]), []byte(csrfCookies[0].Value)) != 1 ||
		!publicAccessFormOriginAllowed(ctx.Request, ctx.RouteMatch.Listener) {
		if err := writePublicLocalLoginForm(ctx, provider, submittedUsername, "Your sign-in page expired. Please try again.", http.StatusBadRequest); err != nil {
			return publicAccessPrincipal{}, publicProxyStageDone, err
		}
		emitPublicAccessTrace(ctx, p2pstreamv1.TrafficTraceStage_TRAFFIC_TRACE_STAGE_ACCESS_DENIED, policy, provider, http.StatusBadRequest, "access_login_csrf")
		recordPublicAccessTerminal(ctx, http.StatusBadRequest, "access_login_csrf")
		return publicAccessPrincipal{}, publicProxyStageDone, nil
	}
	username := authutil.NormalizeUsername(usernameValues[0])
	reservation, admitted := reservePublicAccessLoginAttempt(ctx, provider.ID, username)
	if !admitted {
		ctx.ResponseWriter.Header().Set("Retry-After", "300")
		if err := writePublicLocalLoginForm(ctx, provider, username, "Too many sign-in attempts. Try again in a few minutes.", http.StatusTooManyRequests); err != nil {
			return publicAccessPrincipal{}, publicProxyStageDone, err
		}
		emitPublicAccessTrace(ctx, p2pstreamv1.TrafficTraceStage_TRAFFIC_TRACE_STAGE_ACCESS_DENIED, policy, provider, http.StatusTooManyRequests, "access_login_throttled")
		recordPublicAccessTerminal(ctx, http.StatusTooManyRequests, "access_login_throttled")
		return publicAccessPrincipal{}, publicProxyStageDone, nil
	}
	defer reservation.release()
	user, queryErr := ctx.App.DB.GetEnabledPublicAccessUserByProviderAndUsername(ctx.Request.Context(), db.GetEnabledPublicAccessUserByProviderAndUsernameParams{
		ProviderID: provider.ID, Username: username,
	})
	hash := publicAccessDummyPasswordHash
	if queryErr == nil {
		hash = user.PasswordHash
	} else if !errors.Is(queryErr, sql.ErrNoRows) {
		return publicAccessPrincipal{}, publicProxyStageDone, queryErr
	}
	if authutil.ComparePasswordHash(hash, passwordValues[0]) != nil || queryErr != nil {
		reservation.recordFailure(time.Now())
		if err := writePublicLocalLoginForm(ctx, provider, username, "The username or password is incorrect.", http.StatusUnauthorized); err != nil {
			return publicAccessPrincipal{}, publicProxyStageDone, err
		}
		emitPublicAccessTrace(ctx, p2pstreamv1.TrafficTraceStage_TRAFFIC_TRACE_STAGE_ACCESS_DENIED, policy, provider, http.StatusUnauthorized, "access_unauthenticated")
		recordPublicAccessTerminal(ctx, http.StatusUnauthorized, "access_unauthenticated")
		return publicAccessPrincipal{}, publicProxyStageDone, nil
	}
	reservation.recordSuccess()
	token, tokenHash, err := newSessionToken()
	if err != nil {
		return publicAccessPrincipal{}, publicProxyStageDone, err
	}
	expiresAt := time.Now().Add(provider.SessionDuration)
	if _, err := ctx.App.DB.DeleteStalePublicAccessSessions(ctx.Request.Context()); err != nil {
		return publicAccessPrincipal{}, publicProxyStageDone, err
	}
	if _, err := ctx.App.DB.CreatePublicAccessSession(ctx.Request.Context(), db.CreatePublicAccessSessionParams{
		ProviderID: provider.ID, UserID: user.ID, TokenHash: tokenHash, ExpiresAt: expiresAt,
	}); err != nil {
		return publicAccessPrincipal{}, publicProxyStageDone, err
	}
	ctx.ResponseWriter.Header().Add("Set-Cookie", publicAccessSessionCookie(provider.ID, token, expiresAt, ctx.RouteMatch.Listener).String())
	ctx.ResponseWriter.Header().Add("Set-Cookie", clearPublicAccessCookie(provider.ID, true, ctx.RouteMatch.Listener).String())
	ctx.ResponseWriter.Header().Set("Cache-Control", "no-store")
	ctx.ResponseWriter.Header().Set("Location", publicAccessReturnURI(ctx.Request))
	ctx.ResponseWriter.WriteHeader(http.StatusSeeOther)
	emitPublicAccessTrace(ctx, p2pstreamv1.TrafficTraceStage_TRAFFIC_TRACE_STAGE_ACCESS_GRANTED, policy, provider, http.StatusSeeOther, "")
	recordPublicAccessTerminal(ctx, http.StatusSeeOther, "")
	return publicAccessPrincipal{}, publicProxyStageDone, nil
}

func publicAccessFormOriginAllowed(request *http.Request, listener publicListenerConfig) bool {
	if request == nil {
		return false
	}
	origins := request.Header.Values("Origin")
	if len(origins) == 0 {
		// The double-submit token remains the compatibility path for non-browser clients
		// that do not send Origin. Browsers send Origin for form POSTs.
		return true
	}
	if len(origins) != 1 {
		return false
	}
	origin, err := url.Parse(origins[0])
	if err != nil || origin.Scheme == "" || origin.Host == "" || origin.User != nil ||
		origin.Opaque != "" || origin.Path != "" || origin.RawQuery != "" || origin.Fragment != "" {
		return false
	}
	expected, err := url.Parse(publicAccessOriginalURL(request, listener))
	if err != nil || expected.Scheme == "" || expected.Host == "" {
		return false
	}
	return strings.EqualFold(origin.Scheme, expected.Scheme) && strings.EqualFold(origin.Host, expected.Host)
}

func handlePublicLocalLogout(ctx *publicProxyContext, policy publicAccessPolicyConfig, provider publicAccessProviderConfig) (publicProxyStageResult, error) {
	cookies := ctx.Request.CookiesNamed(publicAccessSessionCookieName(provider.ID))
	if len(cookies) == 1 && len(cookies[0].Value) >= 32 && len(cookies[0].Value) <= 128 {
		if err := ctx.App.DB.RevokePublicAccessSessionByTokenHash(ctx.Request.Context(), db.RevokePublicAccessSessionByTokenHashParams{
			ProviderID: provider.ID, TokenHash: hashSessionToken(cookies[0].Value),
		}); err != nil {
			return publicProxyStageDone, err
		}
	}
	ctx.ResponseWriter.Header().Add("Set-Cookie", clearPublicAccessCookie(provider.ID, false, ctx.RouteMatch.Listener).String())
	ctx.ResponseWriter.Header().Add("Set-Cookie", clearPublicAccessCookie(provider.ID, true, ctx.RouteMatch.Listener).String())
	ctx.ResponseWriter.Header().Set("Cache-Control", "no-store")
	ctx.ResponseWriter.Header().Set("Location", publicAccessReturnURI(ctx.Request))
	ctx.ResponseWriter.WriteHeader(http.StatusSeeOther)
	emitPublicAccessTrace(ctx, p2pstreamv1.TrafficTraceStage_TRAFFIC_TRACE_STAGE_ACCESS_DENIED, policy, provider, http.StatusSeeOther, "access_logout")
	recordPublicAccessTerminal(ctx, http.StatusSeeOther, "")
	return publicProxyStageDone, nil
}

func writePublicLocalBasicChallenge(ctx *publicProxyContext, policy publicAccessPolicyConfig, provider publicAccessProviderConfig, throttled bool) publicProxyStageResult {
	status := http.StatusUnauthorized
	errorKind := "access_unauthenticated"
	body := "Authentication required\n"
	if throttled {
		status = http.StatusTooManyRequests
		errorKind = "access_login_throttled"
		body = "Too many authentication attempts\n"
		ctx.ResponseWriter.Header().Set("Retry-After", "300")
	} else {
		realm := strings.NewReplacer("\\", "\\\\", "\"", "\\\"").Replace(provider.LocalAuthRealm)
		ctx.ResponseWriter.Header().Set("WWW-Authenticate", `Basic realm="`+realm+`", charset="UTF-8"`)
	}
	ctx.ResponseWriter.Header().Set("Cache-Control", "no-store")
	ctx.ResponseWriter.Header().Set("Content-Type", "text/plain; charset=utf-8")
	ctx.ResponseWriter.WriteHeader(status)
	if ctx.Request.Method != http.MethodHead {
		_, _ = io.WriteString(ctx.ResponseWriter, body)
	}
	emitPublicAccessTrace(ctx, p2pstreamv1.TrafficTraceStage_TRAFFIC_TRACE_STAGE_ACCESS_DENIED, policy, provider, status, errorKind)
	recordPublicAccessTerminal(ctx, status, errorKind)
	return publicProxyStageDone
}

func writePublicLocalLoginForm(ctx *publicProxyContext, provider publicAccessProviderConfig, username string, message string, status int) error {
	csrfToken, _, err := newSessionToken()
	if err != nil {
		return err
	}
	title := provider.Name
	if title == "" {
		title = "Protected service"
	}
	if provider.LocalAuthLoginTemplate == nil {
		return errors.New("local access login template is unavailable")
	}
	var body bytes.Buffer
	if err := provider.LocalAuthLoginTemplate.Execute(&body, map[string]any{
		"host":                ctx.Request.Host,
		"page_title":          title,
		"provider_name":       title,
		"login_action":        publicAccessLoginAction(ctx.Request),
		"csrf_field_name":     publicAccessCSRFField,
		"csrf_token":          csrfToken,
		"username_field_name": publicAccessUsernameField,
		"password_field_name": publicAccessPasswordField,
		"username":            username,
		"error_message":       message,
	}); err != nil {
		return fmt.Errorf("render local access login template: %w", err)
	}
	contentType := strings.TrimSpace(provider.LocalAuthLoginTemplateContentType)
	if contentType == "" {
		contentType = defaultResponseTemplateContentType
	}
	ctx.ResponseWriter.Header().Add("Set-Cookie", publicAccessCSRFCookie(provider.ID, csrfToken, ctx.RouteMatch.Listener).String())
	ctx.ResponseWriter.Header().Set("Cache-Control", "no-store")
	ctx.ResponseWriter.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; img-src 'self' data:; font-src 'self' data:; form-action 'self'; base-uri 'none'; frame-ancestors 'none'")
	ctx.ResponseWriter.Header().Set("Content-Type", contentType)
	ctx.ResponseWriter.Header().Set("Referrer-Policy", "no-referrer")
	ctx.ResponseWriter.Header().Set("X-Content-Type-Options", "nosniff")
	ctx.ResponseWriter.Header().Set("X-Frame-Options", "DENY")
	ctx.ResponseWriter.WriteHeader(status)
	if ctx.Request.Method != http.MethodHead {
		_, _ = ctx.ResponseWriter.Write(body.Bytes())
	}
	return nil
}

func publicAccessLoginAction(r *http.Request) string {
	query := r.URL.Query()
	query.Del(publicAccessLogoutQueryKey)
	query.Set(publicAccessLoginQueryKey, "1")
	return "?" + query.Encode()
}

func publicAccessReturnURI(r *http.Request) string {
	query := r.URL.Query()
	query.Del(publicAccessLoginQueryKey)
	query.Del(publicAccessLogoutQueryKey)
	return "?" + query.Encode()
}

func publicAccessSessionCookieName(providerID int64) string {
	return "p2pstream_access_" + strconv.FormatInt(providerID, 10)
}

func publicAccessCSRFCookieName(providerID int64) string {
	return "p2pstream_access_csrf_" + strconv.FormatInt(providerID, 10)
}

func publicAccessSessionCookie(providerID int64, token string, expiresAt time.Time, listener publicListenerConfig) *http.Cookie {
	return &http.Cookie{
		Name: publicAccessSessionCookieName(providerID), Value: token, Path: "/", Expires: expiresAt,
		HttpOnly: true, Secure: listener.Protocol == publicListenerProtocolHTTPS, SameSite: http.SameSiteLaxMode,
	}
}

func publicAccessCSRFCookie(providerID int64, token string, listener publicListenerConfig) *http.Cookie {
	return &http.Cookie{
		Name: publicAccessCSRFCookieName(providerID), Value: token, Path: "/", MaxAge: 600,
		HttpOnly: true, Secure: listener.Protocol == publicListenerProtocolHTTPS, SameSite: http.SameSiteStrictMode,
	}
}

func clearPublicAccessCookie(providerID int64, csrf bool, listener publicListenerConfig) *http.Cookie {
	name := publicAccessSessionCookieName(providerID)
	if csrf {
		name = publicAccessCSRFCookieName(providerID)
	}
	return &http.Cookie{
		Name: name, Value: "", Path: "/", Expires: time.Unix(0, 0), MaxAge: -1,
		HttpOnly: true, Secure: listener.Protocol == publicListenerProtocolHTTPS, SameSite: http.SameSiteLaxMode,
	}
}
