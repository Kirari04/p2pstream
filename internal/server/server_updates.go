package server

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"connectrpc.com/connect"
	p2pstreamv1 "p2pstream/gen/proto/p2pstream/v1"
	"p2pstream/internal/buildinfo"
	"p2pstream/internal/serverupdate"
)

func (a *App) serverUpdateCall(ctx context.Context, path string, request serverupdate.Request) (serverupdate.Response, error) {
	if a.Config == nil {
		return serverupdate.Response{}, errors.New("server updater is not configured")
	}
	return serverupdate.Call(ctx, a.Config.ServerUpdateSocket, a.Config.ServerUpdateToken, path, request)
}

func (a *App) requireServerUpdateAdmin(ctx context.Context, header http.Header, instance string) (*authenticatedUser, error) {
	user, err := a.requireAdmin(ctx, header)
	if err != nil {
		return nil, err
	}
	if a.Config == nil || instance == "" || instance != a.Config.ServerUpdateInstanceID {
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("selected server changed or is not enrolled"))
	}
	return user, nil
}

func (a *App) GetServerUpdateOverview(ctx context.Context, req *connect.Request[p2pstreamv1.GetServerUpdateOverviewRequest]) (*connect.Response[p2pstreamv1.GetServerUpdateOverviewResponse], error) {
	if _, err := a.requireAdmin(ctx, req.Header()); err != nil {
		return nil, err
	}
	out := &p2pstreamv1.GetServerUpdateOverviewResponse{Version: buildinfo.Version, Commit: buildinfo.Commit, Channel: buildinfo.Channel}
	if a.Config == nil || a.Config.ServerUpdateSocket == "" {
		out.Warning = "Install the server updater on this environment's host to enable one-click updates."
		return connect.NewResponse(out), nil
	}
	out.ExecutorConfigured = true
	out.InstanceId = a.Config.ServerUpdateInstanceID
	r, err := a.serverUpdateCall(ctx, "/overview", serverupdate.Request{})
	if err != nil {
		out.Warning = err.Error()
		return connect.NewResponse(out), nil
	}
	if r.Overview == nil || r.Overview.InstanceID != out.InstanceId {
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("executor belongs to another instance"))
	}
	out.ExecutorAvailable = true
	out.Channel = r.Overview.Channel
	out.Warning = r.Overview.Warning
	out.Target = serverReleaseProto(r.Overview.Target)
	out.Operation = serverOperationProto(r.Overview.Operation)
	out.Blockers = r.Overview.Current.Blocked
	return connect.NewResponse(out), nil
}

func (a *App) PreviewServerUpdate(ctx context.Context, req *connect.Request[p2pstreamv1.PreviewServerUpdateRequest]) (*connect.Response[p2pstreamv1.PreviewServerUpdateResponse], error) {
	if _, err := a.requireServerUpdateAdmin(ctx, req.Header(), req.Msg.InstanceId); err != nil {
		return nil, err
	}
	r, err := a.serverUpdateCall(ctx, "/preview", serverupdate.Request{Version: req.Msg.TargetVersion})
	if err != nil {
		return nil, connect.NewError(connect.CodeFailedPrecondition, err)
	}
	if r.Plan == nil || r.Plan.InstanceID != req.Msg.InstanceId {
		return nil, connect.NewError(connect.CodeInternal, errors.New("invalid executor preview"))
	}
	data, err := json.Marshal(r.Plan)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&p2pstreamv1.PreviewServerUpdateResponse{InstanceId: r.Plan.InstanceID, CurrentVersion: r.Plan.CurrentVersion, Target: serverReleaseProto(&r.Plan.Release), PlanToken: base64.RawURLEncoding.EncodeToString(data), ExpiresAtUnixMillis: r.Plan.ExpiresAt.UnixMilli()}), nil
}

func (a *App) StartServerUpdate(ctx context.Context, req *connect.Request[p2pstreamv1.StartServerUpdateRequest]) (*connect.Response[p2pstreamv1.StartServerUpdateResponse], error) {
	user, err := a.requireServerUpdateAdmin(ctx, req.Header(), req.Msg.InstanceId)
	if err != nil {
		return nil, err
	}
	if len(req.Msg.PlanToken) > 64<<10 {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("preview too large"))
	}
	data, err := base64.RawURLEncoding.DecodeString(req.Msg.PlanToken)
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("invalid preview"))
	}
	var plan serverupdate.Plan
	if err := json.Unmarshal(data, &plan); err != nil || plan.InstanceID != req.Msg.InstanceId {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("preview belongs to another instance"))
	}
	actor := fmt.Sprintf("user:%d:%s", user.ID, user.Username)
	if user.IsAccessToken {
		actor = "token:" + user.Username
	}
	r, err := a.serverUpdateCall(ctx, "/start", serverupdate.Request{Start: &serverupdate.StartRequest{OperationID: req.Msg.OperationId, Plan: plan, Actor: actor}})
	if err != nil {
		return nil, connect.NewError(connect.CodeFailedPrecondition, err)
	}
	return connect.NewResponse(&p2pstreamv1.StartServerUpdateResponse{Operation: serverOperationProto(r.Operation)}), nil
}

func (a *App) GetServerUpdateOperation(ctx context.Context, req *connect.Request[p2pstreamv1.GetServerUpdateOperationRequest]) (*connect.Response[p2pstreamv1.GetServerUpdateOperationResponse], error) {
	if _, err := a.requireServerUpdateAdmin(ctx, req.Header(), req.Msg.InstanceId); err != nil {
		return nil, err
	}
	r, err := a.serverUpdateCall(ctx, "/operation", serverupdate.Request{OperationID: req.Msg.OperationId})
	if err != nil {
		return nil, connect.NewError(connect.CodeUnavailable, err)
	}
	return connect.NewResponse(&p2pstreamv1.GetServerUpdateOperationResponse{Operation: serverOperationProto(r.Operation)}), nil
}

func serverReleaseProto(r *serverupdate.Release) *p2pstreamv1.ServerUpdateRelease {
	if r == nil {
		return nil
	}
	return &p2pstreamv1.ServerUpdateRelease{Version: r.Version, Commit: r.Commit, Channel: r.Channel, ManifestSha256: r.ManifestSHA256, Image: r.Image}
}
func serverOperationProto(o *serverupdate.Operation) *p2pstreamv1.ServerUpdateOperation {
	if o == nil {
		return nil
	}
	return &p2pstreamv1.ServerUpdateOperation{Id: o.ID, Phase: o.Phase, Detail: o.Detail, PreviousVersion: o.Plan.CurrentVersion, TargetVersion: o.Plan.Release.Version, StartedAtUnixMillis: o.StartedAt.UnixMilli(), UpdatedAtUnixMillis: o.UpdatedAt.UnixMilli()}
}

func (a *App) serverUpdateGated() bool {
	if a.Config == nil || a.Config.ServerUpdateGateFile == "" {
		return false
	}
	_, err := os.Lstat(a.Config.ServerUpdateGateFile)
	// Failure to read an enrolled control volume is fail-closed.
	return !errors.Is(err, os.ErrNotExist)
}

func (a *App) serverUpdateAdmission(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		method := filepath.Base(r.URL.Path)
		isUpdate := method == "StartServerUpdate" || method == "PreviewServerUpdate" || method == "GetServerUpdateOverview" || method == "GetServerUpdateOperation"
		if isUpdate && !serverUpdateOriginAllowed(r) {
			writeConnectError(w, connect.CodePermissionDenied, "cross-origin server update request rejected")
			return
		}

		// Update requests must not hold the gate while calling the executor:
		// its prepare handshake waits for all other management work to drain.
		if isUpdate {
			next.ServeHTTP(w, r)
			return
		}
		// The trace subscription is read-only and can remain open indefinitely.
		// Do not let it hold preparation's management-mutation barrier.
		if method == "StreamTrafficTraceEvents" {
			if a.serverUpdateGated() {
				writeConnectError(w, connect.CodeUnavailable, "server update in progress")
				return
			}
			next.ServeHTTP(w, r)
			return
		}
		a.serverUpdateMu.RLock()
		defer a.serverUpdateMu.RUnlock()
		// An operator must be able to reload the UI and reauthenticate while a
		// candidate is validating or host recovery is paused. These finite
		// session/bootstrap requests still participate in preparation's drain;
		// application configuration and rollout mutations remain gated.
		isSession := method == "GetSetupState" || method == "GetCurrentUser" || method == "Login" || method == "Logout" || method == "ListEnvironments"
		if a.serverUpdateGated() && !isSession {
			writeConnectError(w, connect.CodeUnavailable, "server update in progress; management changes are paused")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// StartServerUpdateRuntime provides readiness only over a container-local Unix
// socket. The executor invokes the fixed status command via Docker exec.
func (a *App) StartServerUpdateRuntime(ctx context.Context) error {
	if a.Config == nil || a.Config.ServerUpdateSocket == "" {
		return nil
	}
	if info, err := os.Lstat(serverupdate.RuntimeSocket); err == nil {
		if info.Mode()&os.ModeSocket == 0 {
			return errors.New("unsafe server status socket")
		}
		if err := os.Remove(serverupdate.RuntimeSocket); err != nil {
			return err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	l, err := net.Listen("unix", serverupdate.RuntimeSocket)
	if err != nil {
		return err
	}
	if err := os.Chmod(serverupdate.RuntimeSocket, 0600); err != nil {
		l.Close()
		return err
	}
	srv := &http.Server{ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 5 * time.Second, WriteTimeout: 30 * time.Second, Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || (r.URL.Path != "/status" && r.URL.Path != "/prepare") {
			http.NotFound(w, r)
			return
		}
		if r.URL.Path == "/prepare" {
			prepareCtx, cancel := context.WithTimeout(r.Context(), 25*time.Second)
			defer cancel()
			if err := a.lockServerUpdatePreparation(prepareCtx); err != nil {
				http.Error(w, "management or certificate work did not drain before the preparation deadline", http.StatusServiceUnavailable)
				return
			}
			defer a.serverUpdateMu.Unlock()
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(a.serverUpdateRuntimeStatus(r.Context()))
	})}
	go func() { <-ctx.Done(); _ = srv.Close(); _ = os.Remove(serverupdate.RuntimeSocket) }()
	go func() { _ = srv.Serve(l) }()
	return nil
}

// A canceled preparation must not leave a queued RWMutex writer behind: that
// would block fresh management reads after the executor clears maintenance,
// potentially forever if an external certificate authority never responds.
// The maintenance file already stops new application mutations at admission.
func (a *App) lockServerUpdatePreparation(ctx context.Context) error {
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		if a.serverUpdateMu.TryLock() {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

func (a *App) serverUpdateRuntimeStatus(ctx context.Context) serverupdate.RuntimeStatus {
	s := serverupdate.RuntimeStatus{API: serverupdate.API, InstanceID: a.Config.ServerUpdateInstanceID, Version: buildinfo.Version, Commit: buildinfo.Commit, Ready: true, Maintenance: a.serverUpdateGated(), Agents: []string{}, Listeners: []int64{}, Blocked: []string{}}
	if err := a.DB.QueryRowContext(ctx, "SELECT COALESCE(MAX(version_id),0) FROM goose_db_version WHERE is_applied=1").Scan(&s.Schema); err != nil {
		s.Ready = false
		s.Blocked = append(s.Blocked, "Database schema is unavailable")
	}
	var active int
	if err := a.DB.QueryRowContext(ctx, "SELECT COUNT(*) FROM agent_update_assignments WHERE state NOT IN ('succeeded','failed','cancelled') OR (state='failed' AND desired_action='rollback')").Scan(&active); err != nil || active > 0 {
		s.Blocked = append(s.Blocked, "Finish or cancel active agent updates before updating the server")
	}
	if a.ManagementTLS != nil {
		a.ManagementTLS.mu.RLock()
		phase := a.ManagementTLS.state.Phase
		a.ManagementTLS.mu.RUnlock()
		if phase != "" && phase != "idle" {
			s.Blocked = append(s.Blocked, "Finish management certificate rotation before updating the server")
		}
	}
	for _, listener := range a.proxyStatus().Listeners {
		if !listener.Disabled {
			if !listener.Running {
				s.Ready = false
				s.Blocked = append(s.Blocked, "An enabled public listener is not running")
			}
			s.Listeners = append(s.Listeners, listener.ListenerId)
		}
	}
	for _, agent := range a.AgentHub.connectedIDs() {
		s.Agents = append(s.Agents, agent.PublicID)
	}
	sort.Strings(s.Agents)
	if a.Config.ConfigDir != "/data" || os.Getenv("DATABASE_URL") != "" {
		s.Blocked = append(s.Blocked, "Automatic backups require the standard /data database; use a manual deployment for custom database paths")
	}
	for _, path := range []string{a.Config.ManagementTLSCertFile, a.Config.ManagementTLSKeyFile, a.Config.AgentUpdateAuthorityKeyFile, a.Config.PublicCacheDir} {
		if path != "" && !strings.HasPrefix(filepath.Clean(path), "/data/") {
			s.Blocked = append(s.Blocked, "Custom state paths outside /data require a manual deployment")
			break
		}
	}
	return s
}

func serverUpdateOriginAllowed(r *http.Request) bool {
	if managementAccessTokenFromHeader(r.Header) != "" {
		return true
	}
	if r.Header.Get("Sec-Fetch-Site") == "cross-site" {
		return false
	}
	if origin := r.Header.Get("Origin"); origin != "" {
		u, err := url.Parse(origin)
		return err == nil && u.Host == r.Host && (u.Scheme == "https" || u.Scheme == "http") && u.User == nil && u.Path == "" && u.RawQuery == "" && u.Fragment == ""
	}
	return true
}
