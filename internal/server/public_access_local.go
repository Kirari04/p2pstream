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

const publicAccessLoginNonceTTL = 10 * time.Minute

type publicAccessLoginNonceStore struct {
	mu         sync.Mutex
	entries    map[[sha256.Size]byte]publicAccessLoginNonce
	maxEntries int
}

type publicAccessLoginNonce struct {
	providerID int64
	host       [sha256.Size]byte
	clientIP   [sha256.Size]byte
	userAgent  [sha256.Size]byte
	expiresAt  time.Time
}

func newPublicAccessLoginNonceStore(maxEntries int) *publicAccessLoginNonceStore {
	if maxEntries < 1 {
		maxEntries = 1
	}
	return &publicAccessLoginNonceStore{
		entries:    make(map[[sha256.Size]byte]publicAccessLoginNonce),
		maxEntries: maxEntries,
	}
}

func (s *publicAccessLoginNonceStore) issue(token string, nonce publicAccessLoginNonce, now time.Time) {
	if s == nil || token == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.entries) >= s.maxEntries {
		// Evict one entry in constant expected time. Public login-page requests
		// are attacker-controlled, so capacity handling must not scan the store.
		for key := range s.entries {
			delete(s.entries, key)
			break
		}
	}
	nonce.expiresAt = now.Add(publicAccessLoginNonceTTL)
	s.entries[sha256.Sum256([]byte(token))] = nonce
}

func (s *publicAccessLoginNonceStore) consume(token string, expected publicAccessLoginNonce, now time.Time) bool {
	if s == nil || token == "" {
		return false
	}
	key := sha256.Sum256([]byte(token))
	s.mu.Lock()
	defer s.mu.Unlock()
	entry, found := s.entries[key]
	if found {
		delete(s.entries, key)
	}
	return found && entry.expiresAt.After(now) && entry.providerID == expected.providerID &&
		entry.host == expected.host && entry.clientIP == expected.clientIP && entry.userAgent == expected.userAgent
}

func publicAccessLoginNonceForRequest(ctx *publicProxyContext, providerID int64) publicAccessLoginNonce {
	nonce := publicAccessLoginNonce{providerID: providerID}
	if ctx == nil || ctx.Request == nil {
		return nonce
	}
	nonce.host = sha256.Sum256([]byte(normalizeRequestHost(ctx.Request.Host)))
	clientIP := publicAccessRequestClientIP(ctx.Request)
	if clientIP == "" {
		clientIP = remoteAddrIP(ctx.Request.RemoteAddr)
	}
	nonce.clientIP = sha256.Sum256([]byte(clientIP))
	nonce.userAgent = sha256.Sum256([]byte(ctx.Request.UserAgent()))
	return nonce
}

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
	if !publicAccessHostAllowed(normalizeRequestHost(ctx.Request.Host), provider.LocalAuthAllowedHosts) {
		return publicAccessPrincipal{}, rejectPublicAccessDenied(ctx, policy, provider, http.StatusMisdirectedRequest, "access_host_denied"), nil
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
		principal, ok, retryAfter := authenticatePublicLocalBasic(ctx, provider)
		if ok {
			return principal, publicProxyStageContinue, nil
		}
		return publicAccessPrincipal{}, writePublicLocalBasicChallenge(ctx, policy, provider, retryAfter), nil
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
		principal, ok, retryAfter := authenticatePublicLocalBasic(ctx, provider)
		if ok {
			return principal, publicProxyStageContinue, nil
		}
		return publicAccessPrincipal{}, writePublicLocalBasicChallenge(ctx, policy, provider, retryAfter), nil
	}
	if provider.LocalAuthMode == publicAccessLocalAuthModeBasic {
		return publicAccessPrincipal{}, writePublicLocalBasicChallenge(ctx, policy, provider, 0), nil
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
	cookies := ctx.Request.CookiesNamed(publicAccessSessionCookieNameForProvider(provider))
	if len(cookies) == 0 {
		return publicAccessPrincipal{}, false, nil
	}
	if len(cookies) != 1 || len(cookies[0].Value) < 32 || len(cookies[0].Value) > 128 {
		ctx.ResponseWriter.Header().Add("Set-Cookie", clearPublicAccessCookie(provider, false, ctx.RouteMatch.Listener).String())
		return publicAccessPrincipal{}, false, nil
	}
	tokenHash := hashSessionToken(cookies[0].Value)
	row, err := ctx.App.DB.GetActivePublicAccessSession(ctx.Request.Context(), db.GetActivePublicAccessSessionParams{
		ProviderID: provider.ID, TokenHash: tokenHash,
	})
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			ctx.ResponseWriter.Header().Add("Set-Cookie", clearPublicAccessCookie(provider, false, ctx.RouteMatch.Listener).String())
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

func authenticatePublicLocalBasic(ctx *publicProxyContext, provider publicAccessProviderConfig) (publicAccessPrincipal, bool, time.Duration) {
	values := ctx.Request.Header.Values("Authorization")
	if len(values) != 1 || len(values[0]) > 1024 {
		return publicAccessPrincipal{}, false, 0
	}
	if username, cached := provider.basicAuthCache.get(values[0], time.Now()); cached {
		if user, found := provider.LocalUsers[username]; found && user.Enabled {
			principal := publicAccessPrincipalFromLocalUser(provider, user)
			principal.StripAuthorization = true
			return principal, true, 0
		}
	}
	username, password, ok := ctx.Request.BasicAuth()
	if !ok {
		return publicAccessPrincipal{}, false, 0
	}
	username = authutil.NormalizeUsername(username)
	reservation, admitted, retryAfter := reservePublicAccessLoginAttempt(ctx, provider, username)
	if !admitted {
		return publicAccessPrincipal{}, false, retryAfter
	}
	defer reservation.release()
	user, found := provider.LocalUsers[username]
	hash := publicAccessDummyPasswordHash
	if found && user.Enabled {
		hash = user.PasswordHash
	}
	if authutil.ComparePasswordHash(hash, password) != nil || !found || !user.Enabled {
		reservation.recordFailure(time.Now())
		return publicAccessPrincipal{}, false, 0
	}
	reservation.recordSuccess()
	provider.basicAuthCache.put(values[0], username, time.Now())
	principal := publicAccessPrincipalFromLocalUser(provider, user)
	principal.StripAuthorization = true
	return principal, true, 0
}

func reservePublicAccessLoginAttempt(ctx *publicProxyContext, provider publicAccessProviderConfig, username string) (*loginThrottleReservation, bool, time.Duration) {
	clientIP := publicAccessRequestClientIP(ctx.Request)
	if clientIP == "" {
		clientIP = remoteAddrIP(ctx.Request.RemoteAddr)
	}
	peerKey := strconv.FormatInt(provider.ID, 10) + "@" + clientIP
	now := time.Now()
	usernameKey := loginThrottleKey(peerKey, username)
	clientKey := loginThrottleClientKey(peerKey)
	reservation, admitted := reserveLoginThrottleAttemptWithPolicy(
		ctx.App.publicAccessLoginThrottle,
		ctx.App.publicAccessClientLoginThrottle,
		usernameKey,
		clientKey,
		now,
		provider.LocalAuthLoginThrottle,
	)
	if admitted {
		return reservation, true, 0
	}
	retryAfter := max(
		ctx.App.publicAccessLoginThrottle.retryAfter(usernameKey, now),
		ctx.App.publicAccessClientLoginThrottle.retryAfter(clientKey, now),
	)
	if retryAfter <= 0 {
		retryAfter = time.Second
	}
	return nil, false, retryAfter
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
	csrfCookies := ctx.Request.CookiesNamed(publicAccessCSRFCookieNameForProvider(provider))
	submittedUsername := ""
	if len(usernameValues) == 1 {
		submittedUsername = authutil.NormalizeUsername(usernameValues[0])
	}
	validFormShape := len(csrfValues) == 1 && len(csrfValues[0]) == 43 && len(usernameValues) == 1 && len(passwordValues) == 1
	nonceValid := false
	if validFormShape {
		nonceValid = ctx.App.publicAccessLoginNonces.consume(
			csrfValues[0], publicAccessLoginNonceForRequest(ctx, provider.ID), time.Now(),
		)
	}
	browserSource := publicAccessFormBrowserSource(ctx.Request, ctx.RouteMatch.Listener)
	validationErrorKind := ""
	switch {
	case !validFormShape:
		validationErrorKind = "access_login_invalid_fields"
	case browserSource == publicAccessFormSourceInvalid:
		validationErrorKind = "access_login_origin"
	case nonceValid:
		// The bounded, one-time nonce covers clients that suppress both the CSRF
		// cookie and usable source headers. It is tied to this provider, host,
		// client address, and user agent before credentials are evaluated.
	case browserSource == publicAccessFormSourceTrusted:
		// Browsers prevent scripts from forging Origin or Referer. Treat an exact
		// same-origin source as the primary CSRF boundary so a rejected or stale
		// Set-Cookie cannot lock a user out.
	case len(csrfCookies) == 0:
		validationErrorKind = "access_login_csrf_cookie_missing"
	case len(csrfCookies) != 1 || !publicAccessCSRFTokenMatchesCookie(csrfValues[0], csrfCookies):
		validationErrorKind = "access_login_csrf_cookie_mismatch"
	}
	if validationErrorKind != "" {
		if err := writePublicLocalLoginForm(ctx, provider, submittedUsername, "Your sign-in page expired. Please try again.", http.StatusBadRequest); err != nil {
			return publicAccessPrincipal{}, publicProxyStageDone, err
		}
		emitPublicAccessTrace(ctx, p2pstreamv1.TrafficTraceStage_TRAFFIC_TRACE_STAGE_ACCESS_DENIED, policy, provider, http.StatusBadRequest, validationErrorKind)
		recordPublicAccessTerminal(ctx, http.StatusBadRequest, validationErrorKind)
		return publicAccessPrincipal{}, publicProxyStageDone, nil
	}
	username := authutil.NormalizeUsername(usernameValues[0])
	reservation, admitted, retryAfter := reservePublicAccessLoginAttempt(ctx, provider, username)
	if !admitted {
		ctx.ResponseWriter.Header().Set("Retry-After", publicAccessRetryAfterSeconds(retryAfter))
		if err := writePublicLocalLoginForm(ctx, provider, username, "Too many sign-in attempts. Please try again later.", http.StatusTooManyRequests); err != nil {
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
	ctx.ResponseWriter.Header().Add("Set-Cookie", publicAccessSessionCookie(provider, token, expiresAt, ctx.RouteMatch.Listener).String())
	ctx.ResponseWriter.Header().Add("Set-Cookie", clearPublicAccessCookie(provider, true, ctx.RouteMatch.Listener).String())
	ctx.ResponseWriter.Header().Set("Cache-Control", "no-store")
	ctx.ResponseWriter.Header().Set("Location", publicAccessReturnURI(ctx.Request))
	ctx.ResponseWriter.WriteHeader(http.StatusSeeOther)
	emitPublicAccessTrace(ctx, p2pstreamv1.TrafficTraceStage_TRAFFIC_TRACE_STAGE_ACCESS_GRANTED, policy, provider, http.StatusSeeOther, "")
	recordPublicAccessTerminal(ctx, http.StatusSeeOther, "")
	return publicAccessPrincipal{}, publicProxyStageDone, nil
}

func publicAccessCSRFTokenMatchesCookie(token string, cookies []*http.Cookie) bool {
	if len(token) != 43 || len(cookies) == 0 {
		return false
	}
	matched := 0
	for _, cookie := range cookies {
		if cookie != nil {
			matched |= subtle.ConstantTimeCompare([]byte(token), []byte(cookie.Value))
		}
	}
	return matched == 1
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

type publicAccessFormSourceStatus uint8

const (
	publicAccessFormSourceAbsent publicAccessFormSourceStatus = iota
	publicAccessFormSourceOpaque
	publicAccessFormSourceTrusted
	publicAccessFormSourceInvalid
)

func publicAccessFormBrowserSource(request *http.Request, listener publicListenerConfig) publicAccessFormSourceStatus {
	if request == nil {
		return publicAccessFormSourceAbsent
	}
	origins := request.Header.Values("Origin")
	if len(origins) > 1 {
		return publicAccessFormSourceInvalid
	}
	if len(origins) == 1 && origins[0] != "null" {
		if publicAccessFormOriginAllowed(request, listener) {
			return publicAccessFormSourceTrusted
		}
		return publicAccessFormSourceInvalid
	}
	referers := request.Header.Values("Referer")
	if len(referers) == 0 {
		if len(origins) == 1 {
			return publicAccessFormSourceOpaque
		}
		return publicAccessFormSourceAbsent
	}
	if len(referers) != 1 {
		return publicAccessFormSourceInvalid
	}
	referer, err := url.Parse(referers[0])
	if err != nil || referer.Scheme == "" || referer.Host == "" || referer.User != nil || referer.Opaque != "" {
		return publicAccessFormSourceInvalid
	}
	expected, err := url.Parse(publicAccessOriginalURL(request, listener))
	if err != nil || expected.Scheme == "" || expected.Host == "" {
		return publicAccessFormSourceInvalid
	}
	if strings.EqualFold(referer.Scheme, expected.Scheme) && strings.EqualFold(referer.Host, expected.Host) {
		return publicAccessFormSourceTrusted
	}
	return publicAccessFormSourceInvalid
}

func handlePublicLocalLogout(ctx *publicProxyContext, policy publicAccessPolicyConfig, provider publicAccessProviderConfig) (publicProxyStageResult, error) {
	cookies := ctx.Request.CookiesNamed(publicAccessSessionCookieNameForProvider(provider))
	if len(cookies) == 1 && len(cookies[0].Value) >= 32 && len(cookies[0].Value) <= 128 {
		if err := ctx.App.DB.RevokePublicAccessSessionByTokenHash(ctx.Request.Context(), db.RevokePublicAccessSessionByTokenHashParams{
			ProviderID: provider.ID, TokenHash: hashSessionToken(cookies[0].Value),
		}); err != nil {
			return publicProxyStageDone, err
		}
	}
	ctx.ResponseWriter.Header().Add("Set-Cookie", clearPublicAccessCookie(provider, false, ctx.RouteMatch.Listener).String())
	ctx.ResponseWriter.Header().Add("Set-Cookie", clearPublicAccessCookie(provider, true, ctx.RouteMatch.Listener).String())
	ctx.ResponseWriter.Header().Set("Cache-Control", "no-store")
	ctx.ResponseWriter.Header().Set("Location", publicAccessReturnURI(ctx.Request))
	ctx.ResponseWriter.WriteHeader(http.StatusSeeOther)
	emitPublicAccessTrace(ctx, p2pstreamv1.TrafficTraceStage_TRAFFIC_TRACE_STAGE_ACCESS_DENIED, policy, provider, http.StatusSeeOther, "access_logout")
	recordPublicAccessTerminal(ctx, http.StatusSeeOther, "")
	return publicProxyStageDone, nil
}

func writePublicLocalBasicChallenge(ctx *publicProxyContext, policy publicAccessPolicyConfig, provider publicAccessProviderConfig, retryAfter time.Duration) publicProxyStageResult {
	status := http.StatusUnauthorized
	errorKind := "access_unauthenticated"
	body := "Authentication required\n"
	if retryAfter > 0 {
		status = http.StatusTooManyRequests
		errorKind = "access_login_throttled"
		body = "Too many authentication attempts\n"
		ctx.ResponseWriter.Header().Set("Retry-After", publicAccessRetryAfterSeconds(retryAfter))
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

func publicAccessRetryAfterSeconds(retryAfter time.Duration) string {
	seconds := int64((retryAfter + time.Second - 1) / time.Second)
	return strconv.FormatInt(max(int64(1), seconds), 10)
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
	if ctx.App != nil {
		ctx.App.publicAccessLoginNonces.issue(
			csrfToken, publicAccessLoginNonceForRequest(ctx, provider.ID), time.Now(),
		)
	}
	contentType := strings.TrimSpace(provider.LocalAuthLoginTemplateContentType)
	if contentType == "" {
		contentType = defaultResponseTemplateContentType
	}
	ctx.ResponseWriter.Header().Add("Set-Cookie", publicAccessCSRFCookie(provider, csrfToken, ctx.RouteMatch.Listener).String())
	ctx.ResponseWriter.Header().Set("Cache-Control", "no-store")
	ctx.ResponseWriter.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; img-src 'self' data:; font-src 'self' data:; form-action 'self'; base-uri 'none'; frame-ancestors 'none'")
	ctx.ResponseWriter.Header().Set("Content-Type", contentType)
	ctx.ResponseWriter.Header().Set("Referrer-Policy", "same-origin")
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
	return defaultPublicAccessCookieName + "_" + strconv.FormatInt(providerID, 10)
}

func publicAccessCSRFCookieName(providerID int64) string {
	return defaultPublicAccessCookieName + "_csrf_" + strconv.FormatInt(providerID, 10)
}

func publicAccessSessionCookieNameForProvider(provider publicAccessProviderConfig) string {
	return publicAccessCookieNamePrefix(provider) + "_" + strconv.FormatInt(provider.ID, 10)
}

func publicAccessCSRFCookieNameForProvider(provider publicAccessProviderConfig) string {
	return publicAccessCookieNamePrefix(provider) + "_csrf_" + strconv.FormatInt(provider.ID, 10)
}

func publicAccessCookieNamePrefix(provider publicAccessProviderConfig) string {
	if provider.LocalAuthCookieName == "" {
		return defaultPublicAccessCookieName
	}
	return provider.LocalAuthCookieName
}

func publicAccessSessionCookie(provider publicAccessProviderConfig, token string, expiresAt time.Time, listener publicListenerConfig) *http.Cookie {
	return &http.Cookie{
		Name: publicAccessSessionCookieNameForProvider(provider), Value: token, Path: "/", Expires: expiresAt,
		Domain: provider.LocalAuthCookieDomain, HttpOnly: true, Secure: publicAccessCookieSecure(provider, listener),
		SameSite: publicAccessCookieSameSiteMode(provider.LocalAuthCookieSameSite),
	}
}

func publicAccessCSRFCookie(provider publicAccessProviderConfig, token string, listener publicListenerConfig) *http.Cookie {
	return &http.Cookie{
		Name: publicAccessCSRFCookieNameForProvider(provider), Value: token, Path: "/", MaxAge: 600,
		Domain: provider.LocalAuthCookieDomain, HttpOnly: true, Secure: publicAccessCookieSecure(provider, listener),
		SameSite: publicAccessCookieSameSiteMode(provider.LocalAuthCookieSameSite),
	}
}

func clearPublicAccessCookie(provider publicAccessProviderConfig, csrf bool, listener publicListenerConfig) *http.Cookie {
	name := publicAccessSessionCookieNameForProvider(provider)
	if csrf {
		name = publicAccessCSRFCookieNameForProvider(provider)
	}
	return &http.Cookie{
		Name: name, Value: "", Path: "/", Expires: time.Unix(0, 0), MaxAge: -1,
		Domain: provider.LocalAuthCookieDomain, HttpOnly: true, Secure: publicAccessCookieSecure(provider, listener),
		SameSite: publicAccessCookieSameSiteMode(provider.LocalAuthCookieSameSite),
	}
}

func publicAccessCookieSecure(provider publicAccessProviderConfig, listener publicListenerConfig) bool {
	return provider.LocalAuthCookieSecure || provider.LocalAuthCookieSameSite == publicAccessCookieSameSiteNone ||
		listener.Protocol == publicListenerProtocolHTTPS
}

func publicAccessCookieSameSiteMode(value string) http.SameSite {
	switch normalizePublicAccessCookieSameSite(value) {
	case publicAccessCookieSameSiteStrict:
		return http.SameSiteStrictMode
	case publicAccessCookieSameSiteNone:
		return http.SameSiteNoneMode
	default:
		return http.SameSiteLaxMode
	}
}
