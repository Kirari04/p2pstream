package server

import (
	"bytes"
	"context"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"

	"p2pstream/httpmsg"
	"p2pstream/internal/db"
	"p2pstream/msg"
)

const environmentProxyPrefix = "/environments/"

var disallowedEnvironmentProxyMethods = map[string]struct{}{
	"ReportStats":                    {},
	"GetSetupState":                  {},
	"SetupAdmin":                     {},
	"Login":                          {},
	"Logout":                         {},
	"GetCurrentUser":                 {},
	"ListEnvironments":               {},
	"CreateEnvironment":              {},
	"UpdateEnvironment":              {},
	"DeleteEnvironment":              {},
	"DiscoverEnvironmentCertificate": {},
	"TrustEnvironmentCertificate":    {},
	"TestEnvironment":                {},
}

type environmentAuthRoundTripper struct {
	token string
	next  http.RoundTripper
}

type environmentAgentRoundTripper struct {
	app *App
	env db.Environment
}

type pendingAgentResponseBody struct {
	io.ReadCloser
	closeOnce sync.Once
	finish    func()
}

func (a *App) environmentProxyHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, err := a.requireAdmin(r.Context(), r.Header); err != nil {
			writeConnectError(w, connect.CodeOf(err), err.Error())
			return
		}
		envID, procedurePath, ok := parseEnvironmentProxyPath(r.URL.Path)
		if !ok {
			http.NotFound(w, r)
			return
		}
		method, ok := environmentProxyMethod(procedurePath)
		if !ok {
			http.NotFound(w, r)
			return
		}
		if _, blocked := disallowedEnvironmentProxyMethods[method]; blocked {
			writeConnectError(w, connect.CodePermissionDenied, "management method cannot be proxied to an environment")
			return
		}
		env, err := a.DB.GetEnvironment(r.Context(), envID)
		if err != nil {
			writeConnectError(w, connect.CodeNotFound, "environment not found")
			return
		}
		client, err := a.environmentHTTPClient(env)
		if err != nil {
			writeConnectError(w, connect.CodeOf(err), err.Error())
			return
		}
		targetURL, err := environmentProcedureURL(env.ManagementUrl, procedurePath, r.URL.RawQuery)
		if err != nil {
			writeConnectError(w, connect.CodeInternal, err.Error())
			return
		}
		outReq, err := http.NewRequestWithContext(r.Context(), r.Method, targetURL, r.Body)
		if err != nil {
			writeConnectError(w, connect.CodeInternal, err.Error())
			return
		}
		outReq.Header = cloneEnvironmentProxyHeader(r.Header)
		outReq.ContentLength = r.ContentLength
		resp, err := client.Do(outReq)
		if err != nil {
			writeEnvironmentProxyTransportError(w, err)
			return
		}
		defer resp.Body.Close()
		copyEnvironmentProxyHeader(w.Header(), resp.Header)
		w.WriteHeader(resp.StatusCode)
		_, _ = io.Copy(w, resp.Body)
	})
}

func parseEnvironmentProxyPath(path string) (int64, string, bool) {
	rest := strings.TrimPrefix(path, environmentProxyPrefix)
	if rest == path || rest == "" {
		return 0, "", false
	}
	idPart, procedurePart, ok := strings.Cut(rest, "/")
	if !ok || idPart == "" || procedurePart == "" {
		return 0, "", false
	}
	id, err := strconv.ParseInt(idPart, 10, 64)
	if err != nil || id <= 0 {
		return 0, "", false
	}
	return id, "/" + procedurePart, true
}

func environmentProxyMethod(procedurePath string) (string, bool) {
	const prefix = "/p2pstream.v1.AgentManagementService/"
	if !strings.HasPrefix(procedurePath, prefix) {
		return "", false
	}
	method := strings.TrimPrefix(procedurePath, prefix)
	if method == "" || strings.Contains(method, "/") {
		return "", false
	}
	return method, true
}

func (a *App) environmentHTTPClient(row db.Environment) (*http.Client, error) {
	if row.Enabled == 0 {
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("environment is disabled"))
	}
	if err := ensureEnvironmentTrusted(row); err != nil {
		return nil, err
	}
	var rt http.RoundTripper
	if row.Transport == environmentTransportAgent {
		rt = environmentAgentRoundTripper{app: a, env: row}
	} else {
		tlsConfig, err := trustedEnvironmentTLSConfig(row.ManagementUrl, row.TrustedCertificatePem, row.TrustedCertificateSha256)
		if err != nil {
			return nil, connect.NewError(connect.CodeFailedPrecondition, err)
		}
		base, ok := http.DefaultTransport.(*http.Transport)
		if !ok {
			return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("default transport is %T, want *http.Transport", http.DefaultTransport))
		}
		transport := base.Clone()
		transport.TLSClientConfig = tlsConfig
		transport.ResponseHeaderTimeout = environmentResponseHeaderTimeout(row)
		rt = transport
	}
	return &http.Client{Transport: environmentAuthRoundTripper{token: row.AccessToken, next: rt}}, nil
}

func (rt environmentAuthRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	clone := req.Clone(req.Context())
	clone.Header.Del("Cookie")
	clone.Header.Set("Authorization", "Bearer "+rt.token)
	return rt.next.RoundTrip(clone)
}

func (rt environmentAgentRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	if rt.app == nil {
		return nil, errors.New("environment agent transport is unavailable")
	}
	return rt.app.roundTripEnvironmentViaAgent(req.Context(), rt.env, req, map[string]string{
		httpmsg.MetadataTrustedCertificatePEM:       rt.env.TrustedCertificatePem,
		httpmsg.MetadataTrustedCertificateSHA256:    rt.env.TrustedCertificateSha256,
		httpmsg.MetadataResponseHeaderTimeoutMillis: strconv.FormatInt(rt.env.ResponseHeaderTimeoutMillis, 10),
	})
}

func (a *App) discoverEnvironmentCertificateViaAgent(ctx context.Context, row db.Environment, timeout time.Duration) (*x509.Certificate, string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, row.ManagementUrl, nil)
	if err != nil {
		return nil, "", err
	}
	resp, err := a.roundTripEnvironmentViaAgent(ctx, row, req, map[string]string{
		httpmsg.MetadataDiscoverCertificate:         "true",
		httpmsg.MetadataResponseHeaderTimeoutMillis: strconv.FormatInt(int64(timeout/time.Millisecond), 10),
	})
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 256*1024))
	if err != nil {
		return nil, "", err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, "", errors.New(strings.TrimSpace(string(body)))
	}
	cert, err := parseEnvironmentCertificatePEM(string(body))
	if err != nil {
		return nil, "", err
	}
	return cert, certificateSHA256Fingerprint(cert), nil
}

func (a *App) roundTripEnvironmentViaAgent(ctx context.Context, row db.Environment, req *http.Request, metadata map[string]string) (*http.Response, error) {
	if !row.AgentID.Valid {
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("environment agent transport requires a selected agent"))
	}
	agent := a.AgentHub.connectedByID(row.AgentID.Int64)
	if agent == nil {
		return nil, connect.NewError(connect.CodeUnavailable, errors.New("selected environment agent is not connected"))
	}
	id, err := uuid.NewV7()
	if err != nil {
		return nil, err
	}
	pendingCtx, pendingCancel := context.WithCancel(ctx)
	pending := &pendingAgentRequest{
		AgentID:       agent.AgentID,
		AgentPublicID: agent.PublicID,
		ResponseCh:    make(chan *msg.Request, 100),
		ErrorCh:       make(chan error, 1),
		ctx:           pendingCtx,
		cancel:        pendingCancel,
	}
	a.PendingRequests.Store(id, pending)
	pendingFinishReason := "environment_completed"
	finishOnce := sync.Once{}
	finish := func() {
		finishOnce.Do(func() {
			pendingCancel()
			a.finishPendingAgentRequest(id, pendingFinishReason)
		})
	}
	finishOnError := func(reason string) {
		pendingFinishReason = reason
		finish()
	}
	enc := httpmsg.NewRequestEncoderWithMetadata(id, req, metadata)
	for {
		chunk, err := enc.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			finishOnError("environment_request_encode_failed")
			return nil, err
		}
		select {
		case agent.WriteCh <- chunk:
		case <-agent.Done:
			finishOnError("environment_agent_disconnected")
			return nil, connect.NewError(connect.CodeUnavailable, errAgentDisconnected)
		case <-ctx.Done():
			finishOnError("environment_client_cancelled")
			return nil, ctx.Err()
		}
	}
	timeoutCtx, cancel := context.WithTimeout(ctx, agentResponseWaitTimeout(environmentResponseHeaderTimeout(row)))
	defer cancel()
	var firstMsg *msg.Request
	select {
	case <-timeoutCtx.Done():
		finishOnError("environment_agent_timeout")
		return nil, connect.NewError(connect.CodeDeadlineExceeded, timeoutCtx.Err())
	case err := <-pending.ErrorCh:
		finishOnError("environment_agent_failed")
		return nil, err
	case firstMsg = <-pending.ResponseCh:
		if firstMsg == nil {
			finishOnError("environment_agent_disconnected")
			return nil, connect.NewError(connect.CodeUnavailable, errAgentDisconnected)
		}
	}
	stream := &httpmsg.ChannelStream{Ctx: pendingCtx, Ch: pending.ResponseCh}
	resp, err := httpmsg.DecodeResponse(firstMsg, stream)
	if err != nil {
		finishOnError("environment_response_decode_failed")
		return nil, err
	}
	resp.Request = req
	if resp.Body == nil {
		resp.Body = http.NoBody
	}
	resp.Body = &pendingAgentResponseBody{
		ReadCloser: resp.Body,
		finish:     finish,
	}
	return resp, nil
}

func (b *pendingAgentResponseBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	if err == io.EOF {
		b.closeOnce.Do(func() {
			if b.finish != nil {
				b.finish()
			}
		})
	}
	return n, err
}

func (b *pendingAgentResponseBody) Close() error {
	err := b.ReadCloser.Close()
	b.closeOnce.Do(func() {
		if b.finish != nil {
			b.finish()
		}
	})
	return err
}

func environmentResponseHeaderTimeout(row db.Environment) time.Duration {
	timeoutMillis := row.ResponseHeaderTimeoutMillis
	if timeoutMillis <= 0 {
		timeoutMillis = defaultEnvironmentResponseHeaderTimeoutMillis
	}
	return time.Duration(timeoutMillis) * time.Millisecond
}

func environmentProcedureURL(baseURL string, procedurePath string, rawQuery string) (string, error) {
	parsed, err := url.Parse(baseURL)
	if err != nil {
		return "", err
	}
	basePath := strings.TrimRight(parsed.Path, "/")
	parsed.Path = basePath + procedurePath
	parsed.RawPath = ""
	parsed.RawQuery = rawQuery
	parsed.Fragment = ""
	return parsed.String(), nil
}

func cloneEnvironmentProxyHeader(src http.Header) http.Header {
	dst := make(http.Header, len(src))
	for k, values := range src {
		if isHopByHopHeader(k) || strings.EqualFold(k, "Cookie") || strings.EqualFold(k, "Authorization") {
			continue
		}
		dst[k] = append([]string(nil), values...)
	}
	return dst
}

func copyEnvironmentProxyHeader(dst http.Header, src http.Header) {
	for k, values := range src {
		if isHopByHopHeader(k) {
			continue
		}
		for _, value := range values {
			dst.Add(k, value)
		}
	}
}

func isHopByHopHeader(name string) bool {
	switch strings.ToLower(name) {
	case "connection", "keep-alive", "proxy-authenticate", "proxy-authorization", "te", "trailer", "transfer-encoding", "upgrade":
		return true
	default:
		return false
	}
}

func writeEnvironmentProxyTransportError(w http.ResponseWriter, err error) {
	if isTimeoutError(err) {
		writeConnectError(w, connect.CodeDeadlineExceeded, err.Error())
		return
	}
	switch connect.CodeOf(err) {
	case connect.CodeFailedPrecondition:
		writeConnectError(w, connect.CodeFailedPrecondition, err.Error())
	case connect.CodeUnavailable:
		writeConnectError(w, connect.CodeUnavailable, err.Error())
	case connect.CodeDeadlineExceeded:
		writeConnectError(w, connect.CodeDeadlineExceeded, err.Error())
	default:
		writeConnectError(w, connect.CodeUnavailable, err.Error())
	}
}

func writeConnectError(w http.ResponseWriter, code connect.Code, message string) {
	status := http.StatusInternalServerError
	switch code {
	case connect.CodeUnauthenticated:
		status = http.StatusUnauthorized
	case connect.CodePermissionDenied:
		status = http.StatusForbidden
	case connect.CodeNotFound:
		status = http.StatusNotFound
	case connect.CodeInvalidArgument:
		status = http.StatusBadRequest
	case connect.CodeFailedPrecondition:
		status = http.StatusPreconditionFailed
	case connect.CodeDeadlineExceeded:
		status = http.StatusGatewayTimeout
	case connect.CodeUnavailable:
		status = http.StatusServiceUnavailable
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = io.Copy(w, bytes.NewBufferString(fmt.Sprintf(`{"code":%q,"message":%q}`+"\n", connectCodeJSONName(code), message)))
}

func connectCodeJSONName(code connect.Code) string {
	switch code {
	case connect.CodeCanceled:
		return "canceled"
	case connect.CodeUnknown:
		return "unknown"
	case connect.CodeInvalidArgument:
		return "invalid_argument"
	case connect.CodeDeadlineExceeded:
		return "deadline_exceeded"
	case connect.CodeNotFound:
		return "not_found"
	case connect.CodeAlreadyExists:
		return "already_exists"
	case connect.CodePermissionDenied:
		return "permission_denied"
	case connect.CodeResourceExhausted:
		return "resource_exhausted"
	case connect.CodeFailedPrecondition:
		return "failed_precondition"
	case connect.CodeAborted:
		return "aborted"
	case connect.CodeOutOfRange:
		return "out_of_range"
	case connect.CodeUnimplemented:
		return "unimplemented"
	case connect.CodeInternal:
		return "internal"
	case connect.CodeUnavailable:
		return "unavailable"
	case connect.CodeDataLoss:
		return "data_loss"
	case connect.CodeUnauthenticated:
		return "unauthenticated"
	default:
		return "unknown"
	}
}
